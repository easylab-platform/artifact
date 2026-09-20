// Package store provides reference implementations of the artifactkit storage
// interfaces: a filesystem blob CAS, a SQLite/postgres/mysql metadata index,
// and the credential tables. It is the only module that pulls a database
// driver, so the protocol adapters stay dependency-light.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/easylab-platform/artifact/core"
)

// FileBlobStore is a content-addressed store: one file per digest, addressed
// as `sha256/<first-two>/<rest>`, with a `.hashes.json` sidecar recording the
// hash set computed at write time. Bytes never enter RAM (atomic rename from a
// temp file), so multi-GB layers are safe.
type FileBlobStore struct {
	root string
	mu   sync.Mutex
}

// NewFileBlobStore opens a filesystem CAS rooted at dir.
func NewFileBlobStore(dir string) (*FileBlobStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &FileBlobStore{root: dir}, nil
}

// hashSidecarSuffix names the persisted-hashes sidecar for a blob file. List
// validates the exact 64-hex shape, so the sidecar is never mistaken for a blob.
const hashSidecarSuffix = ".hashes.json"

func (s *FileBlobStore) path(digest string) (string, error) {
	hexpart, err := artifactkit.ParseDigest(digest)
	if err != nil {
		return "", err
	}
	if len(hexpart) < 3 {
		return "", errors.New("digest too short")
	}
	return filepath.Join(s.root, "sha256", hexpart[:2], hexpart[2:]), nil
}

// Stat implements BlobStore.
func (s *FileBlobStore) Stat(ctx context.Context, digest string) (*artifactkit.BlobInfo, error) {
	p, err := s.path(digest)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &artifactkit.BlobInfo{Digest: digest, Size: fi.Size(), ModTime: fi.ModTime()}, nil
}

// Open implements BlobStore.
func (s *FileBlobStore) Open(ctx context.Context, digest string) (io.ReadSeekCloser, error) {
	p, err := s.path(digest)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return f, nil
}

// Put implements BlobStore: stream to a temp file while computing every hash in
// one pass, verify against wantDigest (when set), write the hash sidecar, then
// publish with an atomic rename. A partially written file is never visible.
// Returns (stored=false) when the blob already existed.
func (s *FileBlobStore) Put(ctx context.Context, r io.Reader, wantDigest string) (artifactkit.Stored, bool, error) {
	if wantDigest != "" {
		if _, err := artifactkit.ParseDigest(wantDigest); err != nil {
			return artifactkit.Stored{}, false, err
		}
	}
	hexpart := ""
	if wantDigest != "" {
		hexpart, _ = artifactkit.ParseDigest(wantDigest)
	}
	var dir, final string
	if hexpart != "" {
		dir = filepath.Join(s.root, "sha256", hexpart[:2])
		final = filepath.Join(dir, hexpart[2:])
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return artifactkit.Stored{}, false, err
		}
		// Fast dedup path: the blob is already present.
		s.mu.Lock()
		_, statErr := os.Stat(final)
		s.mu.Unlock()
		if statErr == nil {
			h, _, _ := s.Hashes(ctx, wantDigest)
			sz, _ := s.Stat(ctx, wantDigest)
			var n int64
			if sz != nil {
				n = sz.Size
			}
			return artifactkit.Stored{Hashes: h, Size: n, Digest: wantDigest}, false, nil
		}
	} else {
		// No expected digest: write into a temp dir first, then fan out.
		dir = s.root
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return artifactkit.Stored{}, false, err
	}

	tmp, err := os.CreateTemp(dir, ".part-*")
	if err != nil {
		return artifactkit.Stored{}, false, err
	}
	h, err := artifactkit.ComputeHashes(io.TeeReader(r, tmp))
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return artifactkit.Stored{}, false, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return artifactkit.Stored{}, false, err
	}
	got := "sha256:" + h.SHA256
	if wantDigest != "" && got != wantDigest {
		_ = os.Remove(tmp.Name())
		return artifactkit.Stored{}, false, fmt.Errorf("digest mismatch: expected %s got %s", wantDigest, got)
	}
	if final == "" {
		// Derive the final path from the computed digest.
		dir = filepath.Join(s.root, "sha256", h.SHA256[:2])
		if err := os.MkdirAll(dir, 0o755); err != nil {
			_ = os.Remove(tmp.Name())
			return artifactkit.Stored{}, false, err
		}
		final = filepath.Join(dir, h.SHA256[2:])
	}
	sz := fileSize(tmp.Name())

	// Publish under lock to avoid double-rename races.
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(final); err == nil {
		_ = os.Remove(tmp.Name())
		return artifactkit.Stored{Hashes: h, Size: sz, Digest: got}, false, nil
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		_ = os.Remove(tmp.Name())
		return artifactkit.Stored{}, false, err
	}
	if err := writeHashes(final, h); err != nil {
		// The blob is published; a missing sidecar only means Hashes falls
		// back to a recompute. Log-and-continue, never fail the write.
		artifactkit.LogMetaErr("hash sidecar", err)
	}
	return artifactkit.Stored{Hashes: h, Size: sz, Digest: got}, true, nil
}

// Hashes implements BlobStore: the hash set recorded at write time. ok=false
// when no sidecar exists (a blob written before this feature, or by another
// tool); the caller may then fall back to computing it.
func (s *FileBlobStore) Hashes(ctx context.Context, digest string) (artifactkit.Hashes, bool, error) {
	p, err := s.path(digest)
	if err != nil {
		return artifactkit.Hashes{}, false, err
	}
	data, err := os.ReadFile(p + hashSidecarSuffix)
	if err != nil {
		if os.IsNotExist(err) {
			return artifactkit.Hashes{}, false, nil
		}
		return artifactkit.Hashes{}, false, err
	}
	var h artifactkit.Hashes
	if json.Unmarshal(data, &h) != nil || h.SHA256 == "" {
		return artifactkit.Hashes{}, false, nil
	}
	return h, true, nil
}

// Delete implements BlobStore.
func (s *FileBlobStore) Delete(ctx context.Context, digest string) error {
	p, err := s.path(digest)
	if err != nil {
		return err
	}
	_ = os.Remove(p + hashSidecarSuffix)
	err = os.Remove(p)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// List implements BlobStore.
func (s *FileBlobStore) List(ctx context.Context) ([]artifactkit.BlobInfo, error) {
	var out []artifactkit.BlobInfo
	err := filepath.Walk(s.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		digest, ok := digestFromPath(s.root, path)
		if ok {
			out = append(out, artifactkit.BlobInfo{Digest: digest, Size: info.Size(), ModTime: info.ModTime()})
		}
		return nil
	})
	return out, err
}

// writeHashes persists the hash sidecar for a published blob file.
func writeHashes(blobPath string, h artifactkit.Hashes) error {
	data, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return os.WriteFile(blobPath+hashSidecarSuffix, data, 0o644)
}

// fileSize returns a file's size (0 on error).
func fileSize(path string) int64 {
	if fi, err := os.Stat(path); err == nil {
		return fi.Size()
	}
	return 0
}

func digestFromPath(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", false
	}
	// The layout is sha256/<first-two>/<rest> (3 segments); reconstruct the
	// full 64-hex digest from the fan-out dir + filename.
	parts := splitPath(rel)
	if len(parts) != 3 {
		return "", false
	}
	if parts[0] != "sha256" || len(parts[1]) != 2 || len(parts[2]) != 62 {
		return "", false
	}
	return "sha256:" + parts[1] + parts[2], true
}

func splitPath(p string) []string {
	var out []string
	cur := ""
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(p[i])
	}
	out = append(out, cur)
	return out
}
