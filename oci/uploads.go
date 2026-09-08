package oci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/easylab-platform/artifact/core"
)

// uploadSessions stores in-progress OCI blob uploads on disk (one append-only
// .part file per session) so a server restart does not invalidate them, and
// serializes appends per session. The uploadRow in the metadata store remains
// the session's index record (repository binding + committed size).
type uploadSessions struct {
	root string
	mu   sync.Map // session id -> *sync.Mutex
}

func newUploadSessions(root string) (*uploadSessions, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &uploadSessions{root: root}, nil
}

// lock returns the per-session mutex, held by the caller for the whole
// read-modify-write cycle.
func (s *uploadSessions) lock(session string) *sync.Mutex {
	v, _ := s.mu.LoadOrStore(session, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// partPath is the session's backing file.
func (s *uploadSessions) partPath(session string) (string, error) {
	if !validSessionID(session) {
		return "", fmt.Errorf("invalid session id")
	}
	return filepath.Join(s.root, session+".part"), nil
}

// validSessionID rejects path traversal: our session ids are exactly
// "sess-" followed by 32 lowercase hex characters.
func validSessionID(session string) bool {
	const prefix = "sess-"
	if !strings.HasPrefix(session, prefix) {
		return false
	}
	hexPart := session[len(prefix):]
	if len(hexPart) != 32 {
		return false
	}
	for _, c := range hexPart {
		if !isLowerHex(c) {
			return false
		}
	}
	return true
}

// append copies r.Body onto the end of the session file and returns the new
// total size.
func (s *uploadSessions) append(ctx context.Context, session string, r *http.Request) (int64, error) {
	mu := s.lock(session)
	mu.Lock()
	defer mu.Unlock()
	p, err := s.partPath(session)
	if err != nil {
		return 0, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	if _, err := io.Copy(f, r.Body); err != nil {
		_ = f.Close()
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// size reports the session's current byte count (0 when the file is absent).
func (s *uploadSessions) size(session string) int64 {
	p, err := s.partPath(session)
	if err != nil {
		return 0
	}
	fi, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// commit finalizes a session: the verified bytes move into the blob store
// (streamed from the part file — no full in-memory buffer) and the session is
// dropped.
func (s *uploadSessions) commit(ctx context.Context, session, digest string, blobs artifactkit.BlobStore) error {
	mu := s.lock(session)
	mu.Lock()
	defer mu.Unlock()
	p, err := s.partPath(session)
	if err != nil {
		return err
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := blobs.PutIfAbsent(ctx, digest, f); err != nil {
		return err
	}
	s.remove(session)
	return nil
}

// verify hashes the session part file and compares it against the declared
// digest (streamed; never buffers the whole layer).
func (s *uploadSessions) verify(session, digest string) error {
	mu := s.lock(session)
	mu.Lock()
	defer mu.Unlock()
	p, err := s.partPath(session)
	if err != nil {
		return err
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if got != digest {
		return fmt.Errorf("digest mismatch: got %s", got)
	}
	return nil
}

// remove deletes the session file (idempotent).
func (s *uploadSessions) remove(session string) {
	if p, err := s.partPath(session); err == nil {
		_ = os.Remove(p)
	}
	s.mu.Delete(session)
}

// sweep drops sessions (part files + mutexes) untouched for olderThan. It is
// driven by a background ticker started by the adapter (StartSweeper).
func (s *uploadSessions) sweep(olderThan time.Duration) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-olderThan)
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil || fi.IsDir() {
			continue
		}
		if fi.ModTime().Before(cutoff) {
			name := e.Name()
			if len(name) > 5 && filepath.Ext(name) == ".part" {
				_ = os.Remove(filepath.Join(s.root, name))
			}
		}
	}
}

// isLowerHex reports whether c is [0-9a-f].
func isLowerHex(c rune) bool {
	return ('0' <= c && c <= '9') || ('a' <= c && c <= 'f')
}
