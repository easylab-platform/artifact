package golang

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	artifactkit "github.com/easylab-platform/artifact/core"
	"github.com/easylab-platform/artifact/core/store"
)

func newTestState(t *testing.T, upstream string) *State {
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
		Upstreams: &artifactkit.Upstreams{Defaults: map[string]string{"go": upstream},
			Overrides: map[string]string{}, Proxy: map[string]string{}}}}
}

func TestServeHTTPPrefixRouting(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/golang.org/x/text/@v/v0.14.0.info" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("bad path " + r.URL.Path))
			return
		}
		_, _ = w.Write([]byte(`{"Version":"v0.14.0"}`))
	}))
	t.Cleanup(up.Close)
	s := newTestState(t, up.URL)

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/pkgs/go/golang.org/x/text/@v/v0.14.0.info", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "v0.14.0") {
		t.Fatalf("info: %d %q", rec.Code, rec.Body.String())
	}

	// @v/list and @latest route without a version split.
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/pkgs/go/golang.org/x/text/@v/list", nil))
	if rec.Code != http.StatusNotFound {
		// upstream fake only serves .info; 404 is the expected passthrough.
		t.Fatalf("list status %d", rec.Code)
	}
}

func TestRegistryFetchUsesConfiguredUpstream(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(up.Close)
	s := newTestState(t, up.URL)
	b, err := s.registryFetch(t.Context(), "/x")
	if err != nil || string(b) != "ok" {
		t.Fatalf("fetch: %q %v", b, err)
	}
}

var _ = net.Dial
