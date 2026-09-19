package oci

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/easylab-platform/artifact/core"
	"github.com/easylab-platform/artifact/core/store"
)

// TestManifestTagRevalidatesDigestImmutable verifies the two cache policies:
// a DIGEST reference names immutable content and is served from cache forever,
// while a TAG is mutable and must be re-resolved once its TTL expires (so a
// re-pushed :latest does not go stale).
func TestManifestTagRevalidatesDigestImmutable(t *testing.T) {
	manifestV1 := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":2},"layers":[]}`)
	manifestV2 := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","size":2},"layers":[]}`)

	var bodyHits int64
	var latest atomic.Value
	latest.Store(manifestV1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/manifests/") {
			atomic.AddInt64(&bodyHits, 1)
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			_, _ = w.Write(latest.Load().([]byte))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(upstream.Close)

	blobs, _ := store.NewFileBlobStore(t.TempDir())
	meta, _ := store.OpenSQLite(t.TempDir() + "/meta.db")
	t.Cleanup(func() { _ = meta.Close() })
	reg := &artifactkit.Registry{Blobs: blobs, Meta: meta,
		Upstreams: &artifactkit.Upstreams{
			Defaults: map[string]string{}, Overrides: map[string]string{},
			Proxy: map[string]string{"*": ""},
		}}
	a := New(&OciState{Registry: reg, DefaultUpstream: upstream.URL})
	regHost := strings.TrimPrefix(upstream.URL, "http://")

	get := func(ref string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v2/team/app/manifests/"+ref, nil)
		req.Host = regHost
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, req)
		return rec
	}

	// First pull by tag caches it. Second pull within the TTL is a cache hit.
	if rec := get("latest"); rec.Code != http.StatusOK {
		t.Fatalf("first latest: %d %s", rec.Code, rec.Body.String())
	}
	if rec := get("latest"); rec.Code != http.StatusOK {
		t.Fatalf("second latest: %d", rec.Code)
	}
	if got := atomic.LoadInt64(&bodyHits); got != 1 {
		t.Fatalf("upstream hits after fresh tag = %d, want 1", got)
	}

	// The tag row carries a finite ExpiresAt; the digest row is immutable.
	tagArt, err := meta.Get(t.Context(), "oci", "team/app", "latest")
	if err != nil {
		t.Fatal(err)
	}
	if tagArt.ExpiresAt.IsZero() {
		t.Fatal("tag row must have an expiry (mutable)")
	}
	dgst := tagArt.Digest
	dgArt, err := meta.Get(t.Context(), "oci", "team/app", dgst)
	if err != nil {
		t.Fatal(err)
	}
	if !artifactkit.Fresh(dgArt, tagArt.ExpiresAt.Add(24*time.Hour)) {
		t.Fatal("digest row must be immutable (fresh forever)")
	}

	// Expire the tag row: the next tag pull must re-resolve upstream and pick
	// up the re-pushed manifest, while a digest pull stays cached.
	tagArt.ExpiresAt = tagArt.ExpiresAt.Add(-time.Hour)
	_ = meta.Put(t.Context(), tagArt)
	latest.Store(manifestV2)

	if rec := get("latest"); rec.Code != http.StatusOK {
		t.Fatalf("revalidated latest: %d", rec.Code)
	}
	if got := atomic.LoadInt64(&bodyHits); got != 2 {
		t.Fatalf("upstream hits after expired tag = %d, want 2", got)
	}
	if rec := get(dgst); rec.Code != http.StatusOK {
		t.Fatalf("digest pull: %d", rec.Code)
	}
	if got := atomic.LoadInt64(&bodyHits); got != 2 {
		t.Fatalf("immutable digest must not re-hit upstream; hits = %d", got)
	}
}
