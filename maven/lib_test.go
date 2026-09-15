package maven

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	artifactkit "github.com/easylab-platform/artifact/core"
	"github.com/easylab-platform/artifact/core/store"
)

func TestMetadataXMLOverlay(t *testing.T) {
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
	s := &State{Registry: &artifactkit.Registry{Blobs: blobs, Meta: meta,
		Upstreams: &artifactkit.Upstreams{Defaults: map[string]string{"maven": "https://repo.maven.apache.org/maven2"},
			Overrides: map[string]string{}, Proxy: map[string]string{}}}}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/pkgs/maven/org/slf4j/slf4j-api/maven-metadata.xml", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "slf4j-api") {
		t.Fatalf("body %q", rec.Body.String())
	}
}
