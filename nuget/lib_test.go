package nuget

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	artifactkit "github.com/easylab-platform/artifact/core"
	"github.com/easylab-platform/artifact/core/store"
)

func newState(t *testing.T) *State {
	t.Helper()
	dir := t.TempDir()
	meta, err := store.OpenSQLite(dir + "/m.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	blobs, err := store.NewFileBlobStore(dir + "/blobs")
	if err != nil {
		t.Fatal(err)
	}
	return &State{Registry: &artifactkit.Registry{Blobs: blobs, Meta: meta,
		Upstreams: &artifactkit.Upstreams{Defaults: map[string]string{}, Overrides: map[string]string{}, Proxy: map[string]string{}}}}
}

// nupkg builds a minimal .nupkg (a zip carrying a .nuspec) so parseNuspec can
// derive (id, version).
func nupkg(t *testing.T, id, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(id + ".nuspec")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte(`<?xml version="1.0"?><package><metadata><id>` + id + `</id><version>` + version + `</version></metadata></package>`))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestPushThenFlatContainer verifies a pushed package is served through the v3
// flat-container endpoint byte-identically.
func TestPushThenFlatContainer(t *testing.T) {
	s := newState(t)
	pkg := nupkg(t, "Newtonsoft.Json", "13.0.3")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/artifacts/nuget/api/v2/package", bytes.NewReader(pkg))
	req.Header.Set("Content-Type", "application/octet-stream")
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("push = %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/artifacts/nuget/v3/flatcontainer/newtonsoft.json/13.0.3/newtonsoft.json.13.0.3.nupkg", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("flatcontainer = %d", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), pkg) {
		t.Fatal("served nupkg differs from the pushed bytes")
	}
}
