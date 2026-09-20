package composer

import (
	"bytes"
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

// TestUploadThenP2 verifies a pushed package is indexed and served through the
// Composer v2 metadata endpoint (/p2/<vendor>/<pkg>.json).
func TestUploadThenP2(t *testing.T) {
	s := newState(t)
	body := []byte(`{"name":"acme/lib","version":"1.4.0","description":"x"}`)

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/artifacts/composer/api/packages", bytes.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/artifacts/composer/p2/acme/lib.json", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "1.4.0") {
		t.Fatalf("p2 = %d %s", rec.Code, rec.Body.String())
	}
}
