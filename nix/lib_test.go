package nix

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

const testHash = "0ha4fad6awpq6vmc0y3n6i7pjg7na5v6" // 32 chars, nix-base32 shape

// TestCachePathValidation verifies the allowlist of cache path shapes.
func TestCachePathValidation(t *testing.T) {
	cases := map[string]bool{
		"nix-cache-info":              true,
		testHash + ".narinfo":         true,
		"nar/" + testHash + ".nar":    true,
		"nar/" + testHash + ".nar.xz": true,
		"../etc/passwd":               false,
		"some/other/path":             false,
		testHash + ".txt":             false,
		"nar/short.nar":               false,
	}
	for path, want := range cases {
		if got := isCachePath(path); got != want {
			t.Errorf("isCachePath(%q) = %v want %v", path, got, want)
		}
	}
}

// TestPullThroughNarinfoAndNar drives the adapter end to end.
func TestPullThroughNarinfoAndNar(t *testing.T) {
	narinfo := []byte("StorePath: /nix/store/" + testHash + "-hello-2.12.1\nURL: nar/" + testHash + ".nar.xz\nNarHash: sha256:0000\nNarSize: 42\n")
	nar := []byte("fake-nar-bytes")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".narinfo"):
			_, _ = w.Write(narinfo)
		case strings.HasSuffix(r.URL.Path, ".nar.xz"):
			_, _ = w.Write(nar)
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
	s := &State{Registry: &artifactkit.Registry{
		Blobs: blobs, Meta: meta,
		Upstreams: &artifactkit.Upstreams{
			Defaults:  map[string]string{"nix": upstream.URL},
			Overrides: map[string]string{}, Proxy: map[string]string{},
		},
	}}

	// Cache root info is static.
	req := httptest.NewRequest(http.MethodGet, "/artifacts/nix/nix-cache-info", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "StoreDir: /nix/store") {
		t.Fatalf("nix-cache-info: %d %s", rec.Code, rec.Body.String())
	}

	// narinfo pull-through.
	req2 := httptest.NewRequest(http.MethodGet, "/artifacts/nix/"+testHash+".narinfo", nil)
	rec2 := httptest.NewRecorder()
	s.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), "NarHash") {
		t.Fatalf("narinfo: %d", rec2.Code)
	}

	// nar pull-through + cache.
	req3 := httptest.NewRequest(http.MethodGet, "/artifacts/nix/nar/"+testHash+".nar.xz", nil)
	rec3 := httptest.NewRecorder()
	s.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK || !strings.Contains(rec3.Body.String(), "fake-nar") {
		t.Fatalf("nar: %d", rec3.Code)
	}
	vs, err := meta.ListVersions(context.Background(), "nix", "cache")
	if err != nil || len(vs) != 2 {
		t.Fatalf("cached versions: %v %v", vs, err)
	}
}
