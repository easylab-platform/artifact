package helm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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

// chartTgz builds a minimal Helm chart tarball carrying Chart.yaml so
// chartNameVersion can derive (name, version).
func chartTgz(t *testing.T, name, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	chart := "apiVersion: v2\nname: " + name + "\nversion: " + version + "\n"
	if err := tw.WriteHeader(&tar.Header{Name: name + "/Chart.yaml", Mode: 0o644, Size: int64(len(chart))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(chart)); err != nil {
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

// TestUploadThenDownloadAndIndex verifies a pushed chart is served by its
// filename and appears in the generated index.yaml.
func TestUploadThenDownloadAndIndex(t *testing.T) {
	s := newState(t)
	tgz := chartTgz(t, "mychart", "1.2.3")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/artifacts/helm/api/charts", bytes.NewReader(tgz))
	req.Header.Set("Content-Type", "application/octet-stream")
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body.String())
	}

	// Download by the canonical filename.
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/artifacts/helm/charts/mychart-1.2.3.tgz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("download = %d", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), tgz) {
		t.Fatal("downloaded chart differs from the uploaded bytes")
	}

	// The index lists the chart.
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/artifacts/helm/index.yaml", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "mychart") {
		t.Fatalf("index = %d %s", rec.Code, rec.Body.String())
	}
}
