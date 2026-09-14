package debian

import (
	"bytes"
	"context"
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

// TestHostedUploadAndIndex drives a self-published .deb through the hosted
// path: PUT the package, GET the generated Packages index, then download the
// package back byte-for-byte.
func TestHostedUploadAndIndex(t *testing.T) {
	s := newHostedState(t)
	deb := buildDeb(t, "Package: easylab-worker\nVersion: 1.0.0\nArchitecture: amd64\nDescription: worker\n")

	// Upload.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/pkgs/debian/hosted/easylab/easylab-worker_1.0.0_amd64.deb", bytes.NewReader(deb))
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}

	// Index.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/pkgs/debian/hosted/easylab/Packages", nil)
	s.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("index: %d", rec2.Code)
	}
	idx := rec2.Body.String()
	if !strings.Contains(idx, "Package: easylab-worker") || !strings.Contains(idx, "Version: 1.0.0") {
		t.Fatalf("index:\n%s", idx)
	}
	if !strings.Contains(idx, "Filename: ./easylab-worker_1.0.0_amd64.deb") {
		t.Fatalf("index filename missing:\n%s", idx)
	}
	if !strings.Contains(idx, "SHA256: ") || !strings.Contains(idx, "Size: ") {
		t.Fatalf("index checksum/size missing:\n%s", idx)
	}

	// Download.
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, "/pkgs/debian/hosted/easylab/easylab-worker_1.0.0_amd64.deb", nil)
	s.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK || !bytes.Equal(rec3.Body.Bytes(), deb) {
		t.Fatalf("download: %d", rec3.Code)
	}

	// Delete then index empties.
	rec4 := httptest.NewRecorder()
	s.ServeHTTP(rec4, httptest.NewRequest(http.MethodDelete, "/pkgs/debian/hosted/easylab/easylab-worker_1.0.0_amd64.deb", nil))
	if rec4.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec4.Code)
	}
	rec5 := httptest.NewRecorder()
	s.ServeHTTP(rec5, httptest.NewRequest(http.MethodGet, "/pkgs/debian/hosted/easylab/Packages", nil))
	if strings.Contains(rec5.Body.String(), "easylab-worker") {
		t.Fatalf("index after delete:\n%s", rec5.Body.String())
	}

	// Hosted files are separate from pull-through cache rows.
	vs, err := s.Registry.Meta.ListVersions(context.Background(), "debian", "hosted/easylab")
	if err != nil || len(vs) != 0 {
		t.Fatalf("hosted rows after delete: %v %v", vs, err)
	}
}
