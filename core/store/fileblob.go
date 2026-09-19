// Package store provides reference implementations of the artifactkit storage
// interfaces. It ships two blob stores — a filesystem CAS and a SQLite-BLOB
// CAS — plus a SQLite IndexStore, so a deployment can choose where each layer
// physically lives.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/easylab-platform/artifact/core"
)

// FileBlobStore is a content-addressed store: one file per digest, addressed
// as `sha256/<first-two>/<rest>`. This is the default for large immutable
// layers/blobs because it keeps bytes out of the OS page cache worth of RAM
// and survives crashes with atomic rename.
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
func (s *FileBlobStore) Stat(ctx context.Context, digest string) (*int64, error) {
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
	n := fi.Size()
	return &n, nil
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

// PutIfAbsent implements BlobStore with atomic rename so a partially written
// file is never visible. Returns false when the digest already exists.
func (s *FileBlobStore) PutIfAbsent(ctx context.Context, digest string, r io.Reader) (bool, error) {
	hexpart, err := artifactkit.ParseDigest(digest)
	if err != nil {
		return false, err
	}
	dir := filepath.Join(s.root, "sha256", hexpart[:2])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	final := filepath.Join(dir, hexpart[2:])

	s.mu.Lock()
	if _, err := os.Stat(final); err == nil {
		s.mu.Unlock()
		return false, nil // dedup
	}
	tmp, err := os.CreateTemp(dir, ".part-*")
	if err != nil {
		s.mu.Unlock()
		return false, err
	}
	s.mu.Unlock()

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), r); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	got := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if got != digest {
		_ = os.Remove(tmp.Name())
		return false, fmt.Errorf("digest mismatch: expected %s got %s", digest, got)
	}
	// Verify + publish under lock to avoid double-rename races.
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(final); err == nil {
		_ = os.Remove(tmp.Name())
		return false, nil
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		_ = os.Remove(tmp.Name())
		return false, err
	}
	return true, nil
}

// PutHashes implements artifactkit.HashPersister: store the hashes computed
// while streaming the blob as a sidecar file next to it. A sidecar write
// failure is non-fatal (HashesFor recomputes), so the error is advisory.
func (s *FileBlobStore) PutHashes(ctx context.Context, digest string, h artifactkit.Hashes) error {
	p, err := s.path(digest)
	if err != nil {
		return err
	}
	data, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return os.WriteFile(p+hashSidecarSuffix, data, 0o644)
}

// HashesFor implements BlobStore: read the persisted sidecar when present,
// else recompute from the stored bytes. The sidecar makes the Maven/Hex
// checksum paths O(1) instead of re-reading a possibly multi-GB jar.
func (s *FileBlobStore) HashesFor(ctx context.Context, digest string) (artifactkit.Hashes, error) {
	p, err := s.path(digest)
	if err != nil {
		return artifactkit.Hashes{}, err
	}
	if data, err := os.ReadFile(p + hashSidecarSuffix); err == nil {
		var h artifactkit.Hashes
		if json.Unmarshal(data, &h) == nil && h.SHA256 != "" {
			return h, nil
		}
	}
	f, err := s.Open(ctx, digest)
	if err != nil {
		return artifactkit.Hashes{}, err
	}
	if f == nil {
		return artifactkit.Hashes{}, artifactkit.ErrBlobUnknown
	}
	defer func() { _ = f.Close() }()
	h, err := artifactkit.ComputeHashes(f)
	if err != nil {
		return artifactkit.Hashes{}, err
	}
	return h, nil
}

// hashSidecarSuffix names the persisted-hashes sidecar for a blob file. The
// blob path is `sha256/<2>/<62>`; the sidecar is that path plus this suffix, so
// List (which validates the exact 64-hex shape) ignores it.
const hashSidecarSuffix = ".hashes.json"

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
func (s *FileBlobStore) List(ctx context.Context) ([]string, error) {
	var out []string
	err := filepath.Walk(s.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if info.Name() == "sha256" {
			return nil
		}
		digest, ok := digestFromPath(s.root, path)
		if ok {
			out = append(out, digest)
		}
		return nil
	})
	return out, err
}

// ModTime implements store.BlobAger: the blob file's mtime, used by the
// reaper to avoid deleting a blob that may be mid-write. A missing file
// returns the zero time with no error.
func (s *FileBlobStore) ModTime(ctx context.Context, digest string) (time.Time, error) {
	p, err := s.path(digest)
	if err != nil {
		return time.Time{}, err
	}
	fi, err := os.Stat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	return fi.ModTime(), nil
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
