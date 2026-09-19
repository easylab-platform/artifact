package artifactkit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/easylab-platform/artifact/core"
	"github.com/easylab-platform/artifact/core/store"
	"github.com/easylab-platform/artifact/targets"
)

func newCacheRegistry(t *testing.T) *artifactkit.Registry {
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
	return &artifactkit.Registry{
		Blobs: blobs, Meta: artifactkit.NewScopedStore(meta),
		Upstreams: &artifactkit.Upstreams{
			Targets:  targets.NewRegistry(),
			Defaults: map[string]string{}, Overrides: map[string]string{}, Proxy: map[string]string{},
		},
	}
}

// TestPathCacheFetchesOnceAndServesLocally is the plan-A promise applied to the
// path-tree protocols: first call fetches, second is a local hit.
func TestPathCacheFetchesOnceAndServesLocally(t *testing.T) {
	reg := newCacheRegistry(t)
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte("index-bytes"))
	}))
	t.Cleanup(srv.Close)

	ctx := artifactkit.WithRepoScope(context.Background(), artifactkit.RepoScope{Format: "cran", Name: "tree"})
	pol := artifactkit.PathCachePolicy{MediaType: "application/gzip"}

	d, n, hit, ok := reg.FetchCachedPath(ctx, "cran", "tree", "src/contrib/PACKAGES.gz", "/src/contrib/PACKAGES.gz", srv.URL, pol)
	if !ok || hit || d == "" || n == 0 {
		t.Fatalf("first: ok=%v hit=%v d=%q n=%d", ok, hit, d, n)
	}
	d2, _, hit2, ok2 := reg.FetchCachedPath(ctx, "cran", "tree", "src/contrib/PACKAGES.gz", "/src/contrib/PACKAGES.gz", srv.URL, pol)
	if !ok2 || !hit2 || d2 != d {
		t.Fatalf("second: ok=%v hit=%v d=%q want local hit with same digest", ok2, hit2, d2)
	}
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Fatalf("upstream hits = %d, want 1", got)
	}
}

// TestPathCacheSingleFlight collapses a burst into one upstream request.
func TestPathCacheSingleFlight(t *testing.T) {
	reg := newCacheRegistry(t)
	var hits int64
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		<-release // hold the fetch so peers pile up behind the flight
		_, _ = w.Write([]byte("slow-bytes"))
	}))
	t.Cleanup(srv.Close)

	ctx := artifactkit.WithRepoScope(context.Background(), artifactkit.RepoScope{Format: "cran", Name: "tree"})
	pol := artifactkit.PathCachePolicy{}
	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reg.FetchCachedPath(ctx, "cran", "tree", "idx", "/idx", srv.URL, pol)
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Fatalf("upstream hits = %d, want 1 (single-flight)", got)
	}
}

// TestPathCacheNoValidatorServedForever confirms a stale entry with no
// validator is served locally rather than re-downloaded.
func TestPathCacheNoValidatorServedForever(t *testing.T) {
	reg := newCacheRegistry(t)
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		_, _ = w.Write([]byte("no-etag"))
	}))
	t.Cleanup(srv.Close)

	ctx := artifactkit.WithRepoScope(context.Background(), artifactkit.RepoScope{Format: "cran", Name: "tree"})
	cfg := artifactkit.PathCachePolicy{TTL: time.Nanosecond} // expires immediately
	d, _, _, _ := reg.FetchCachedPath(ctx, "cran", "tree", "v", "/v", srv.URL, cfg)
	time.Sleep(2 * time.Millisecond)
	d2, _, hit, ok := reg.FetchCachedPath(ctx, "cran", "tree", "v", "/v", srv.URL, cfg)
	if !ok || !hit || d2 != d {
		t.Fatalf("stale no-validator: ok=%v hit=%v d=%q", ok, hit, d2)
	}
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Fatalf("hits = %d, want 1 (no validator -> never re-download)", got)
	}
}

// TestPathCacheRevalidates304 verifies a stale entry with a validator issues a
// conditional request and a 304 refreshes it without re-downloading.
func TestPathCacheRevalidates304(t *testing.T) {
	reg := newCacheRegistry(t)
	var bodyHits, notModified int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			atomic.AddInt64(&notModified, 1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		atomic.AddInt64(&bodyHits, 1)
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte("index"))
	}))
	t.Cleanup(srv.Close)

	ctx := artifactkit.WithRepoScope(context.Background(), artifactkit.RepoScope{Format: "cran", Name: "tree"})
	cfg := artifactkit.PathCachePolicy{TTL: time.Nanosecond}
	d, _, _, _ := reg.FetchCachedPath(ctx, "cran", "tree", "i", "/i", srv.URL, cfg)
	time.Sleep(2 * time.Millisecond)
	d2, _, _, ok := reg.FetchCachedPath(ctx, "cran", "tree", "i", "/i", srv.URL, cfg)
	if !ok || d2 != d {
		t.Fatalf("after 304: ok=%v d=%q want %q", ok, d2, d)
	}
	if atomic.LoadInt64(&bodyHits) != 1 || atomic.LoadInt64(&notModified) != 1 {
		t.Fatalf("bodyHits=%d notModified=%d, want 1/1", bodyHits, notModified)
	}
}

// TestFlightKindsDoNotCollide guards the single-flight namespace: two calls
// with different result types (a []byte doc vs a Fetched blob) on the same URL
// must not share one in-flight entry, or a type assertion would panic.
func TestFlightKindsDoNotCollide(t *testing.T) {
	reg := newCacheRegistry(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-test")
		_, _ = w.Write([]byte("body"))
	}))
	t.Cleanup(srv.Close)
	ctx := artifactkit.WithRepoScope(context.Background(), artifactkit.RepoScope{Format: "cran", Name: "tree"})

	// A doc fetch ([]byte) and a registry fetch (Fetched) for the same base+path,
	// concurrently.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		reg.Fetch(ctx, "cran", srv.URL, "/same")
	}()
	go func() {
		defer wg.Done()
		r := reg.RemoteAt(srv.URL)
		r.GetBytes(ctx, "/same")
	}()
	wg.Wait() // a collision would panic the test
}

// TestFetchToBlobRetries502 verifies the streaming path retries a transient
// 502 (the egress proxy's classic upstream-blip code) instead of failing a
// client build, matching the buffered Fetch path's resilience.
func TestFetchToBlobRetries502(t *testing.T) {
	reg := newCacheRegistry(t)
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&hits, 1)
		if n == 1 {
			w.WriteHeader(http.StatusBadGateway) // transient
			return
		}
		_, _ = w.Write([]byte("recovered"))
	}))
	t.Cleanup(srv.Close)

	// cran's default upstream is replaced by a per-repo override pointing at
	// the test server.
	reg.Upstreams.SetRepo("cran", "tree", srv.URL, "")
	ctx := artifactkit.WithRepoScope(context.Background(), artifactkit.RepoScope{Format: "cran", Name: "tree"})
	d, n, ok := reg.FetchToBlob(ctx, "cran", "/idx")
	if !ok || d == "" || n == 0 {
		t.Fatalf("expected retry to succeed: ok=%v d=%q n=%d", ok, d, n)
	}
	if atomic.LoadInt64(&hits) < 2 {
		t.Fatalf("hits = %d, want >= 2 (a retry)", hits)
	}
}
