package rubygems

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

// gemFile builds a minimal .gem (a tar carrying a gzipped metadata.gz) so
// extractNameVersionGem can derive (name, version).
func gemFile(t *testing.T, name, version string) []byte {
	t.Helper()
	var meta bytes.Buffer
	zw := gzip.NewWriter(&meta)
	_, _ = zw.Write([]byte("name: " + name + "\nversion: " + version + "\n"))
	_ = zw.Close()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	payload := meta.Bytes()
	if err := tw.WriteHeader(&tar.Header{Name: "metadata.gz", Mode: 0o644, Size: int64(len(payload))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestUploadThenDownload verifies a pushed gem is served byte-identically by its
// canonical filename.
func TestUploadThenDownload(t *testing.T) {
	s := newState(t)
	gem := gemFile(t, "thor", "1.3.0")

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/artifacts/rubygems/api/v1/gems", bytes.NewReader(gem)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/artifacts/rubygems/gems/thor-1.3.0.gem", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("download = %d", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), gem) {
		t.Fatal("downloaded gem differs from the uploaded bytes")
	}

	// The compact index names list includes it.
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/artifacts/rubygems/names", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "thor") {
		t.Fatalf("names = %d %q", rec.Code, rec.Body.String())
	}
}
