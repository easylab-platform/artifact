package conda

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

// TestPullThroughRepodataAndPkg drives the adapter end to end.
func TestPullThroughRepodataAndPkg(t *testing.T) {
	repodata := []byte(`{"info":{"subdir":"linux-64"},"packages":{},"conda_pkgs":{}}`)
	pkg := []byte("fake-conda-bytes")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "repodata.json"):
			_, _ = w.Write(repodata)
		case strings.HasSuffix(r.URL.Path, ".conda"):
			_, _ = w.Write(pkg)
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
		ChannelUpstreams: map[string]string{"main": upstream.URL},
	}

	req := httptest.NewRequest(http.MethodGet, "/artifacts/conda/main/linux-64/repodata.json", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "subdir") {
		t.Fatalf("repodata: %d %s", rec.Code, rec.Body.String())
	}
	req2 := httptest.NewRequest(http.MethodGet, "/artifacts/conda/main/linux-64/numpy-1.0.0.conda", nil)
	rec2 := httptest.NewRecorder()
	s.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), "fake-conda") {
		t.Fatalf("pkg: %d", rec2.Code)
	}
	vs, err := meta.ListVersions(context.Background(), "conda", "main/linux-64")
	if err != nil || len(vs) != 2 {
		t.Fatalf("cached: %v %v", vs, err)
	}

	// Unknown channel fails closed.
	req3 := httptest.NewRequest(http.MethodGet, "/artifacts/conda/other/linux-64/repodata.json", nil)
	rec3 := httptest.NewRecorder()
	s.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusNotFound {
		t.Fatalf("unknown channel = %d", rec3.Code)
	}

	// subdir grouping + media types.
	if subdirOf("channeldata.json") != "" || subdirOf("linux-64/repodata.json") != "linux-64" {
		t.Fatal("subdirOf")
	}
	if mediaTypeOf("x.conda") != "application/zip" || mediaTypeOf("y.tar.bz2") != "application/x-tar" {
		t.Fatal("media types")
	}
}
