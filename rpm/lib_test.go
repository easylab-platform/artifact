package rpm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/easylab-platform/artifact/core"
	"github.com/easylab-platform/artifact/core/store"
)

// TestPullThroughRepodataAndRpm drives the adapter end to end: a repo key
// mapped at the fake upstream serves repomd.xml and a package; both are
// cached and served byte-for-byte.
func TestPullThroughRepodataAndRpm(t *testing.T) {
	repomd := []byte(`<?xml version="1.0"?><repomd><data type="primary"/></repomd>`)
	rpmBytes := []byte("fake-rpm-bytes")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "repomd.xml"):
			_, _ = w.Write(repomd)
		case strings.HasSuffix(r.URL.Path, ".rpm"):
			_, _ = w.Write(rpmBytes)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upstream.Close)

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
	s := &State{
		Registry: &artifactkit.Registry{
			Blobs: blobs, Meta: meta,
			Upstreams: &artifactkit.Upstreams{Defaults: map[string]string{}, Overrides: map[string]string{}, Proxy: map[string]string{}},
		},
		RepoUpstreams: map[string]string{"fedora41": upstream.URL},
	}

	req := httptest.NewRequest(http.MethodGet, "/pkgs/rpm/fedora41/repodata/repomd.xml", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "repomd") {
		t.Fatalf("repomd: %d %s", rec.Code, rec.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodGet, "/pkgs/rpm/fedora41/packages/b/bash.rpm", nil)
	rec2 := httptest.NewRecorder()
	s.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), "fake-rpm") {
		t.Fatalf("rpm: %d", rec2.Code)
	}

	// Both cached.
	vs, err := meta.ListVersions(context.Background(), "rpm", "fedora41")
	if err != nil || len(vs) != 2 {
		t.Fatalf("cached versions: %v %v", vs, err)
	}

	// Unknown repo fails closed.
	req3 := httptest.NewRequest(http.MethodGet, "/pkgs/rpm/unknown/repodata/repomd.xml", nil)
	rec3 := httptest.NewRecorder()
	s.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusNotFound {
		t.Fatalf("unknown repo = %d", rec3.Code)
	}

	// Media types.
	if mediaTypeOf("repodata/repomd.xml") != "application/xml" || mediaTypeOf("b/bash.rpm") != "application/x-rpm" {
		t.Fatal("media types")
	}
}
