package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/easylab-platform/artifact/core"
)

// digestOf returns the CAS digest of data (matching FileBlobStore's check).
func digestOf(data string) string {
	h := sha256.Sum256([]byte(data))
	return "sha256:" + hex.EncodeToString(h[:])
}

// TestBlobStoreListReconstructsDigests guards the sha256/<2>/<62> path layout:
// List must return the full 64-hex digest (a length/precision bug here silently
// disabled orphan reaping).
func TestBlobStoreListReconstructsDigests(t *testing.T) {
	blobs, err := NewFileBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	want := digestOf("list-me")
	if _, err := blobs.PutIfAbsent(ctx, want, strings.NewReader("list-me")); err != nil {
		t.Fatal(err)
	}
	got, err := blobs.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("List() = %v, want [%s]", got, want)
	}
}

// TestStats verifies the footprint summary attributes rows and bytes per
// format and totals the CAS.
func TestStats(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenSQLite(filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	blobs, err := NewFileBlobStore(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	body := "stats-body"
	d := digestOf(body)
	if _, err := blobs.PutIfAbsent(ctx, d, strings.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	if err := st.Put(ctx, artifactkit.Artifact{
		Format: "npm", Repository: "left-pad", Version: "1.0.0", Digest: d,
		Blobs: []artifactkit.Descriptor{{Digest: d, Size: int64(len(body))}},
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := st.Stats(ctx, blobs)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Blobs.Objects != 1 || stats.Blobs.Bytes != int64(len(body)) {
		t.Fatalf("blob totals = %+v", stats.Blobs)
	}
	var found bool
	for _, f := range stats.Formats {
		if f.Format == "npm" {
			found = true
			if f.Rows != 1 || f.Bytes != int64(len(body)) {
				t.Fatalf("npm stat = %+v", f)
			}
		}
	}
	if !found {
		t.Fatal("npm format missing from stats")
	}
}

func TestReapOrphanBlobsAndExpireNegative(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenSQLite(filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	blobs, err := NewFileBlobStore(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// A referenced blob (indexed) and an orphan blob (not indexed).
	referencedData := "referenced-bytes"
	orphanData := "orphan-bytes"
	referenced := digestOf(referencedData)
	orphan := digestOf(orphanData)
	if _, err := blobs.PutIfAbsent(ctx, referenced, strings.NewReader(referencedData)); err != nil {
		t.Fatal(err)
	}
	if _, err := blobs.PutIfAbsent(ctx, orphan, strings.NewReader(orphanData)); err != nil {
		t.Fatal(err)
	}
	if err := st.Put(ctx, artifactkit.Artifact{
		Format: "test", Repository: "r", Version: "v", Digest: referenced,
		Blobs: []artifactkit.Descriptor{{Digest: referenced, Size: int64(len(referencedData))}},
	}); err != nil {
		t.Fatal(err)
	}

	// A negative cache entry that is already expired.
	if err := st.Put(ctx, artifactkit.Artifact{
		Format: "netcache", Repository: "uri", Version: "https://x/missing",
		MediaType: negativeMediaType, ExpiresAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	// Grace must exceed the blob's age for the reaper to act. Sleep a moment
	// then use a tiny grace so the just-written blobs count as "old enough".
	time.Sleep(20 * time.Millisecond)
	stat, err := st.ReapOrphanBlobs(ctx, blobs, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if stat.OrphanBlobs != 1 {
		t.Fatalf("orphan_blobs = %d, want 1", stat.OrphanBlobs)
	}
	if stat.BlobsKept != 1 {
		t.Fatalf("blobs_kept = %d, want 1 (the referenced blob)", stat.BlobsKept)
	}
	// The referenced blob survives; the orphan is gone.
	if sz, _ := blobs.Stat(ctx, referenced); sz == nil {
		t.Error("referenced blob was removed")
	}
	if sz, _ := blobs.Stat(ctx, orphan); sz != nil {
		t.Error("orphan blob was not removed")
	}

	n, err := st.ExpireNegative(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expired_negative = %d, want 1", n)
	}
	if _, err := st.Get(ctx, "netcache", "uri", "https://x/missing"); err == nil {
		t.Error("expired negative entry still present")
	}
}
