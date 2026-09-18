package apk

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/easylab-platform/artifact/core"
	"github.com/easylab-platform/artifact/core/store"
)

// TestServeBlobRangeAndHead verifies the streaming response path: Range
// yields 206 with the right bytes, HEAD has no body, and Content-Length is
// correct.
func TestServeBlobRangeAndHead(t *testing.T) {
	dir := t.TempDir()
	meta, err := store.OpenSQLite(filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	blobs, err := store.NewFileBlobStore(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("0123456789abcdef")
	reg := &artifactkit.Registry{Blobs: blobs, Meta: meta}
	stored, err := reg.StoreAndHash(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	s := &State{Registry: &artifactkit.Registry{Blobs: blobs, Meta: meta}}

	// Full fetch.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/artifacts/apk/v3.21/main/x86_64/busybox-1.0.0-r0.apk", nil)
	_ = req
	// Pre-cache the artifact record so the adapter serves it.
	if err := meta.Put(context.Background(), artifactkit.Artifact{
		Format: "apk", Repository: "v3.21/main/x86_64", Version: "busybox-1.0.0-r0.apk",
		Digest: stored.Digest,
		Blobs:  []artifactkit.Descriptor{{Digest: stored.Digest, Size: stored.Size, Name: "busybox-1.0.0-r0.apk"}},
		Source: "pull",
	}); err != nil {
		t.Fatal(err)
	}
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != string(payload) {
		t.Fatalf("full fetch: %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Length") != "16" {
		t.Fatalf("content-length = %q", rec.Header().Get("Content-Length"))
	}

	// HEAD: no body.
	recH := httptest.NewRecorder()
	s.ServeHTTP(recH, httptest.NewRequest(http.MethodHead, "/artifacts/apk/v3.21/main/x86_64/busybox-1.0.0-r0.apk", nil))
	if recH.Code != http.StatusOK || recH.Body.Len() != 0 {
		t.Fatalf("HEAD: %d body=%d", recH.Code, recH.Body.Len())
	}
	if recH.Header().Get("Content-Length") != "16" {
		t.Fatalf("HEAD content-length = %q", recH.Header().Get("Content-Length"))
	}

	// Range: 206 partial.
	recR := httptest.NewRecorder()
	reqR := httptest.NewRequest(http.MethodGet, "/artifacts/apk/v3.21/main/x86_64/busybox-1.0.0-r0.apk", nil)
	reqR.Header.Set("Range", "bytes=4-7")
	s.ServeHTTP(recR, reqR)
	if recR.Code != http.StatusPartialContent {
		t.Fatalf("range: %d", recR.Code)
	}
	if recR.Body.String() != "4567" {
		t.Fatalf("range body = %q", recR.Body.String())
	}
	if cr := recR.Header().Get("Content-Range"); cr != "bytes 4-7/16" {
		t.Fatalf("content-range = %q", cr)
	}
}
