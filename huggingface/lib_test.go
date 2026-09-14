package huggingface

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

func newFixture(t *testing.T, upstreamURL, hfToken string) *State {
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
	return &State{
		Registry: &artifactkit.Registry{
			Blobs: blobs, Meta: meta,
			Upstreams: &artifactkit.Upstreams{
				Defaults:  map[string]string{"huggingface": upstreamURL},
				Overrides: map[string]string{}, Proxy: map[string]string{},
			},
		},
		HFToken: hfToken,
	}
}

// TestPullThroughModelFile drives the adapter: config.json fetched from the
// fake Hub, cached, and served with the upstream content type.
func TestPullThroughModelFile(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch {
		case strings.Contains(r.URL.Path, "resolve/main/config.json"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"model_type":"llama"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upstream.Close)

	s := newFixture(t, upstream.URL, "hf-test-token")
	req := httptest.NewRequest(http.MethodGet, "/pkgs/huggingface/models/org/repo/resolve/main/config.json", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("fetch: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "llama") {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("content-type = %q", rec.Header().Get("Content-Type"))
	}
	if gotAuth != "Bearer hf-test-token" {
		t.Fatalf("upstream auth = %q", gotAuth)
	}

	// Cached: metadata recorded.
	vs, err := s.Registry.Meta.ListVersions(context.Background(), "huggingface", "models/org/repo")
	if err != nil || len(vs) != 1 {
		t.Fatalf("versions: %v %v", vs, err)
	}
}

// TestRepoTypeValidation verifies unknown repo types and methods.
func TestRepoTypeValidation(t *testing.T) {
	s := newFixture(t, "http://127.0.0.1:1", "")
	req := httptest.NewRequest(http.MethodGet, "/pkgs/huggingface/bogus/ns/repo/resolve/main/x", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bad repo type = %d", rec.Code)
	}
	req2 := httptest.NewRequest(http.MethodPost, "/pkgs/huggingface/models/ns/repo/resolve/main/x", nil)
	rec2 := httptest.NewRecorder()
	s.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d", rec2.Code)
	}
}
