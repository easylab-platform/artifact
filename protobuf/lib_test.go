package protobuf

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

func newFixture(t *testing.T, upstreamURL string) *State {
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
	return &State{Registry: &artifactkit.Registry{
		Blobs: blobs, Meta: meta,
		Upstreams: &artifactkit.Upstreams{
			Defaults:  map[string]string{"protobuf": upstreamURL},
			Overrides: map[string]string{}, Proxy: map[string]string{},
		},
	}}
}

// TestPullThroughModuleResolve verifies the module path parsing (org/name +
// plugin variants), upstream auth forwarding, and cache indexing.
func TestPullThroughModuleResolve(t *testing.T) {
	var gotPath, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"commit":"abc123"}`))
	}))
	t.Cleanup(upstream.Close)

	s2 := newFixture(t, upstream.URL)
	meta := s2.Registry.Meta
	s2.BSRToken = "bsr-token"
	req := httptest.NewRequest(http.MethodGet, "/artifacts/protobuf/acme/weather/v1/modules/acme/weather/ref/main", nil)
	rec := httptest.NewRecorder()
	s2.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "abc123") {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if gotPath != "/v1/modules/acme/weather/ref/main" {
		t.Fatalf("upstream path = %q", gotPath)
	}
	if gotAuth != "Bearer bsr-token" {
		t.Fatalf("auth = %q", gotAuth)
	}
	vs, err := meta.ListVersions(context.Background(), "protobuf", "acme/weather")
	if err != nil || len(vs) != 1 {
		t.Fatalf("versions: %v %v", vs, err)
	}

	// Air-gap / no upstream fails closed.
	s3 := &State{Registry: &artifactkit.Registry{
		Blobs: s2.Registry.Blobs, Meta: meta,
		Upstreams: &artifactkit.Upstreams{Defaults: map[string]string{}, Overrides: map[string]string{}, Proxy: map[string]string{}, AirGap: true},
	}}
	req4 := httptest.NewRequest(http.MethodGet, "/artifacts/protobuf/acme/other/v1/modules/acme/other/ref/main", nil)
	rec4 := httptest.NewRecorder()
	s3.ServeHTTP(rec4, req4)
	if rec4.Code != http.StatusNotFound {
		t.Fatalf("air-gapped = %d", rec4.Code)
	}
}

// TestModulePathValidation verifies malformed module paths.
func TestModulePathValidation(t *testing.T) {
	s := newFixture(t, "http://127.0.0.1:1")
	cases := []string{
		"/artifacts/protobuf/acme",                  // too short
		"/artifacts/protobuf/acme/name/extra/notv1", // no /v1/ marker
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, c, nil)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", c, rec.Code)
		}
	}
}
