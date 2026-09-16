package apk

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/easylab-platform/artifact/core"
	"github.com/easylab-platform/artifact/core/store"
)

func newHostedState(t *testing.T) *State {
	t.Helper()
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
	h, err := NewHandler(&artifactkit.Registry{Blobs: blobs, Meta: meta,
		Upstreams: &artifactkit.Upstreams{Defaults: map[string]string{}, Overrides: map[string]string{}, Proxy: map[string]string{}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return h.(*State)
}

// TestHostedAPKINDEX drives a self-published .apk: upload, fetch the
// generated APKINDEX.tar.gz, and confirm the stanza (name/version/arch/
// checksum) is present and the archive decompresses.
func TestHostedAPKINDEX(t *testing.T) {
	s := newHostedState(t)
	apkBytes := []byte("fake-apk-content")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/pkgs/apk/myrepo/mytool-1.2.3-r0.apk", bytes.NewReader(apkBytes))
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}

	rec2 := httptest.NewRecorder()
	s.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/pkgs/apk/myrepo/APKINDEX.tar.gz", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("index: %d", rec2.Code)
	}
	if rec2.Header().Get("Content-Type") != "application/gzip" {
		t.Fatalf("content-type = %q", rec2.Header().Get("Content-Type"))
	}
	gz, err := gzip.NewReader(bytes.NewReader(rec2.Body.Bytes()))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	raw, _ := io.ReadAll(gz)
	idx := string(raw)
	if !strings.Contains(idx, "P:mytool") {
		t.Fatalf("index missing P: entry:\n%s", idx)
	}
	if !strings.Contains(idx, "V:1.2.3-r0") {
		t.Fatalf("index missing V: entry:\n%s", idx)
	}
	if !strings.Contains(idx, "C:Q1") {
		t.Fatalf("index missing checksum:\n%s", idx)
	}

	// Package download byte-for-byte.
	rec3 := httptest.NewRecorder()
	s.ServeHTTP(rec3, httptest.NewRequest(http.MethodGet, "/pkgs/apk/myrepo/mytool-1.2.3-r0.apk", nil))
	if rec3.Code != http.StatusOK || !bytes.Equal(rec3.Body.Bytes(), apkBytes) {
		t.Fatalf("download: %d", rec3.Code)
	}
}

// TestNameVersionArch verifies apk filename parsing.
func TestNameVersionArch(t *testing.T) {
	n, v, a := nameVersionArch("mytool-1.2.3-r4.apk")
	if n != "mytool" || v != "1.2.3-r4" || a != "noarch" {
		t.Fatalf("got %q %q %q", n, v, a)
	}
}
