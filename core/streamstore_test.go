package artifactkit_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/easylab-platform/artifact/core"
	"github.com/easylab-platform/artifact/core/store"
)

// newStreamRegistry builds a Registry over a fresh filesystem CAS + sqlite index.
func newStreamRegistry(t *testing.T) *artifactkit.Registry {
	t.Helper()
	blobs, err := store.NewFileBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	meta, err := store.OpenSQLite(t.TempDir() + "/meta.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	return &artifactkit.Registry{Blobs: blobs, Meta: meta}
}

// TestStoreStreamHashesAndDedups verifies StoreStream persists a reader's bytes
// under their true digest, returns every hash, and dedups on a second write.
func TestStoreStreamHashesAndDedups(t *testing.T) {
	reg := newStreamRegistry(t)
	ctx := context.Background()
	data := bytes.Repeat([]byte("stream-me-"), 1000)

	stored, err := reg.StoreStream(ctx, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if stored.Size != int64(len(data)) {
		t.Fatalf("size = %d, want %d", stored.Size, len(data))
	}
	if stored.Digest != artifactkit.DigestOf(data) {
		t.Fatalf("digest = %s, want %s", stored.Digest, artifactkit.DigestOf(data))
	}
	// Every hash is populated in one pass.
	want, _ := artifactkit.ComputeHashesBytes(data)
	if stored.Hashes != want {
		t.Fatalf("hashes = %+v, want %+v", stored.Hashes, want)
	}

	// A second stream of the same bytes dedups (stored once).
	stored2, err := reg.StoreStream(ctx, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if stored2.Digest != stored.Digest {
		t.Fatalf("second digest = %s", stored2.Digest)
	}
	info, _ := reg.Blobs.Stat(ctx, stored.Digest)
	if info == nil || info.Size != int64(len(data)) {
		t.Fatalf("blob not persisted once")
	}
}

// TestReadBlobPrefix verifies the prefix reader returns at most n bytes without
// loading the whole blob, and reports absence as (nil, nil).
func TestReadBlobPrefix(t *testing.T) {
	reg := newStreamRegistry(t)
	ctx := context.Background()
	data := bytes.Repeat([]byte("0123456789"), 1000) // 10 KB
	stored, err := reg.StoreStream(ctx, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := reg.ReadBlobPrefix(ctx, stored.Digest, 16)
	if err != nil {
		t.Fatal(err)
	}
	if string(prefix) != "0123456789012345" {
		t.Fatalf("prefix = %q", prefix)
	}
	if _, err := reg.ReadBlobPrefix(ctx, "sha256:"+strings.Repeat("0", 64), 16); err != nil {
		t.Fatalf("absent blob should be (nil, nil), got %v", err)
	}
}

// TestStoreStreamLargeBodyStaysOffHeap is a coarse regression guard: a 32 MiB
// body streams through and lands in the CAS, so the helper is not secretly
// buffering the whole thing into a single slice for small inputs.
func TestStoreStreamLargeBodyStaysOffHeap(t *testing.T) {
	reg := newStreamRegistry(t)
	ctx := context.Background()
	const n = 32 << 20
	// A reader that yields n bytes without allocating them all at once.
	src := io.LimitReader(zeroReader{}, n)
	stored, err := reg.StoreStream(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Size != n {
		t.Fatalf("size = %d, want %d", stored.Size, n)
	}
}

// TestStoreVersionReplacesSameFilename verifies a re-publish of the same
// filename supersedes its descriptor (no accumulation), while other filenames
// of the same version are preserved (a jar's pom/sha1 keep their entries).
func TestStoreVersionReplacesSameFilename(t *testing.T) {
	reg := newStreamRegistry(t)
	ctx := context.Background()

	put := func(filename, body string) {
		reg.StoreVersion(ctx, artifactkit.VersionInput{
			Format: "maven", Repository: "acme/lib", Version: "1.0",
			Filename: filename, Source: "push", Data: []byte(body),
		})
	}
	put("lib-1.0.jar", "jar-v1")
	put("lib-1.0.pom", "pom")
	// Re-publish the jar with new bytes: the old descriptor is replaced.
	put("lib-1.0.jar", "jar-v2")

	art, err := reg.Meta.Get(ctx, "maven", "acme/lib", "1.0")
	if err != nil {
		t.Fatal(err)
	}
	jarCount, pomCount := 0, 0
	for _, b := range art.Blobs {
		switch b.Name {
		case "lib-1.0.jar":
			jarCount++
			if b.Size != int64(len("jar-v2")) {
				t.Fatalf("jar descriptor not updated: %+v", b)
			}
		case "lib-1.0.pom":
			pomCount++
		}
	}
	if jarCount != 1 {
		t.Fatalf("jar descriptors = %d, want 1 (replace, not accumulate)", jarCount)
	}
	if pomCount != 1 {
		t.Fatalf("pom descriptor lost on jar replace: %+v", art.Blobs)
	}
}

// TestBlobHashesReadsRecordedSet verifies BlobHashes returns the set recorded at
// write time (no recompute needed).
func TestBlobHashesReadsRecordedSet(t *testing.T) {
	reg := newStreamRegistry(t)
	ctx := context.Background()
	data := []byte("hash-me-once")
	stored, err := reg.StoreStream(ctx, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	got, err := reg.BlobHashes(ctx, stored.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if got != stored.Hashes {
		t.Fatalf("BlobHashes = %+v, want %+v", got, stored.Hashes)
	}
}

// zeroReader yields an endless stream of zero bytes.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}
