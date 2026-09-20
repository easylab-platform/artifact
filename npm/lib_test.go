package npm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// tarball builds a gzipped tar carrying package/package.json so the adapter can
// derive the name/version and metadata.
func tarball(t *testing.T, name, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	pj := `{"name":"` + name + `","version":"` + version + `"}`
	if err := tw.WriteHeader(&tar.Header{Name: "package/package.json", Mode: 0o644, Size: int64(len(pj))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(pj)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestPublishThenPackumentAndTarball verifies a CouchDB-style publish indexes
// the package, serves its packument, and serves the tarball byte-identically.
func TestPublishThenPackumentAndTarball(t *testing.T) {
	s := newState(t)
	tb := tarball(t, "@acme/left-pad", "1.3.0")
	doc := map[string]any{
		"name":      "@acme/left-pad",
		"versions":  map[string]any{"1.3.0": map[string]any{"name": "@acme/left-pad", "version": "1.3.0"}},
		"dist-tags": map[string]any{"latest": "1.3.0"},
		"_attachments": map[string]any{
			"left-pad-1.3.0.tgz": map[string]any{"data": base64.StdEncoding.EncodeToString(tb)},
		},
	}
	body, _ := json.Marshal(doc)

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/artifacts/npm/@acme/left-pad", bytes.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("publish = %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/artifacts/npm/@acme/left-pad", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "1.3.0") {
		t.Fatalf("packument = %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/artifacts/npm/@acme/left-pad/-/left-pad-1.3.0.tgz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("tarball = %d", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), tb) {
		t.Fatal("served tarball differs from the published bytes")
	}
}
