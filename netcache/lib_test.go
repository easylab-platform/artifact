package netcache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/easylab-platform/artifact/core"
	"github.com/easylab-platform/artifact/core/store"
	"github.com/easylab-platform/artifact/targets"
)

func newState(t *testing.T) (*State, *store.Store) {
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
	reg := &artifactkit.Registry{
		Blobs: blobs, Meta: artifactkit.NewScopedStore(meta),
		Upstreams: &artifactkit.Upstreams{
			Targets:  targets.NewRegistry(),
			Defaults: map[string]string{}, Overrides: map[string]string{}, Proxy: map[string]string{},
		},
	}
	return &State{Registry: reg}, meta
}

// declare points a public host pattern at an origin. This is the operator path
// for a private mirror or an internal server; the tests use it to aim
// assets.example.com at a loopback httptest server.
func declare(s *State, host, base string) {
	s.Registry.Upstreams.Targets.Put(targets.Target{
		ID: "netcache." + host, Protocol: "netcache", Base: base, Hosts: []string{host},
	})
}

// TestFetchTwiceHitsCacheOnce is the core promise: an arbitrary URL is fetched
// from the upstream exactly once; the second request is served locally.
func TestFetchTwiceHitsCacheOnce(t *testing.T) {
	var hits int64
	body := []byte("arbitrary-asset-bytes")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Content-Type", "application/x-custom")
		_, _ = w.Write(body)
	}))
	t.Cleanup(upstream.Close)

	s, meta := newState(t)
	declare(s, "assets.example.com", upstream.URL)
	u := "https://assets.example.com/path/to/asset.bin?version=3"

	// Explicit addressing: /artifacts/netcache/<host>/<path>
	reqPath := "/artifacts/netcache/assets.example.com/path/to/asset.bin?version=3"
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, reqPath, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: code = %d body=%s", i, rec.Code, rec.Body.String())
		}
		if rec.Body.String() != string(body) {
			t.Fatalf("request %d: body = %q", i, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/x-custom" {
			t.Errorf("request %d: content-type = %q", i, ct)
		}
	}
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Fatalf("upstream hits = %d, want 1", got)
	}
	// The cache holds exactly one entry, keyed by the normalized URI.
	arts, _ := meta.ListVersions(context.Background(), "netcache", "uri")
	if len(arts) != 1 {
		t.Fatalf("cache entries = %d, want 1", len(arts))
	}
	if arts[0] != u {
		t.Errorf("cache key = %q, want %q", arts[0], u)
	}
}

// TestCredentialedRequestNotShared verifies a request carrying Authorization
// is proxied but never stored: a second client with different credentials must
// not receive the first client's private bytes.
func TestCredentialedRequestNotShared(t *testing.T) {
	var hits int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		_, _ = w.Write([]byte("secret-" + r.Header.Get("Authorization")))
	}))
	t.Cleanup(upstream.Close)

	s, meta := newState(t)
	declare(s, "assets.example.com", upstream.URL)
	reqPath := "/artifacts/netcache/assets.example.com/private.bin"

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, reqPath, nil)
	req.Header.Set("Authorization", "Bearer alice")
	s.ServeHTTP(rec, req)
	if rec.Body.String() != "secret-Bearer alice" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	// Nothing stored.
	if arts, _ := meta.ListVersions(context.Background(), "netcache", "uri"); len(arts) != 0 {
		t.Fatalf("credentialed response was cached: %v", arts)
	}
	// A second credentialed request hits upstream again (no shared cache).
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, reqPath, nil)
	req2.Header.Set("Authorization", "Bearer bob")
	s.ServeHTTP(rec2, req2)
	if rec2.Body.String() != "secret-Bearer bob" {
		t.Fatalf("second body = %q", rec2.Body.String())
	}
	if got := atomic.LoadInt64(&hits); got != 2 {
		t.Fatalf("hits = %d, want 2 (no caching for credentialed requests)", got)
	}
}

// TestClusterLocalRejected keeps the cache from being pointed at cluster-local
// names (SSRF guard).
func TestClusterLocalRejected(t *testing.T) {
	s, _ := newState(t)
	for _, p := range []string{
		"/artifacts/netcache/kubernetes.default.svc/secret",
		"/artifacts/netcache/localhost/x",
		"/artifacts/netcache/127.0.0.1/x",
	} {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: code = %d, want 404", p, rec.Code)
		}
	}
}

// TestRevalidation304 verifies a mutable entry is revalidated with
// If-None-Match after its TTL, and a 304 keeps serving the cached bytes without
// re-downloading the body.
func TestRevalidation304(t *testing.T) {
	var bodyHits, notModified int64
	body := []byte("mutable-index-v1")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			atomic.AddInt64(&notModified, 1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		atomic.AddInt64(&bodyHits, 1)
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(upstream.Close)

	s, meta := newState(t)
	declare(s, "assets.example.com", upstream.URL)
	// Force the entry immediately stale so the next request revalidates.
	artifactkit.SetURICacheTTL(1 * time.Nanosecond)
	defer artifactkit.SetURICacheTTL(-1)

	reqPath := "/artifacts/netcache/assets.example.com/index.json"
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, reqPath, nil))
	if rec.Body.String() != string(body) {
		t.Fatalf("first body = %q", rec.Body.String())
	}
	// Second request: entry is expired, so it revalidates; origin answers 304.
	rec2 := httptest.NewRecorder()
	s.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, reqPath, nil))
	if rec2.Code != http.StatusOK || rec2.Body.String() != string(body) {
		t.Fatalf("second: code=%d body=%q", rec2.Code, rec2.Body.String())
	}
	if atomic.LoadInt64(&bodyHits) != 1 {
		t.Errorf("body fetched %d times, want 1 (304 must not re-download)", bodyHits)
	}
	if atomic.LoadInt64(&notModified) != 1 {
		t.Errorf("conditional requests = %d, want 1", notModified)
	}
	_ = meta
}
