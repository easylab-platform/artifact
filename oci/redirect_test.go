package oci

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/easylab-platform/artifact/core"
	"github.com/easylab-platform/artifact/core/store"
)

// TestBlobFollowsRedirect: the blob endpoint 307-redirects to a CDN (as Docker
// Hub / MCR / GHCR do). The adapter must follow it, verify the digest against
// the requested one, cache it, and serve the bytes.
func TestBlobFollowsRedirect(t *testing.T) {
	payload := []byte("layer-bytes-for-redirect-test")
	digest := "sha256:" + hexDigest(payload)

	var cdnAuth string
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cdnAuth = r.Header.Get("Authorization")
		_, _ = w.Write(payload)
	}))
	t.Cleanup(cdn.Close)

	registrySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The OCI registry redirects blob GETs to the CDN.
		if strings.Contains(r.URL.Path, "/blobs/") {
			http.Redirect(w, r, cdn.URL+"/blob", http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(registrySrv.Close)

	blobs, err := store.NewFileBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	meta, err := store.OpenSQLite(t.TempDir() + "/meta.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	reg := &artifactkit.Registry{Blobs: blobs, Meta: meta,
		Upstreams: &artifactkit.Upstreams{
			Defaults: map[string]string{}, Overrides: map[string]string{},
			Proxy: map[string]string{"*": ""}, // direct (no env proxy in tests)
		}}
	a := New(&OciState{Registry: reg})

	regHost := strings.TrimPrefix(registrySrv.URL, "http://") // 127.0.0.1:port
	req := httptest.NewRequest(http.MethodGet, "/v2/team/app/blobs/"+digest, nil)
	req.Host = regHost // the registry travels in Host, not the path
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != string(payload) {
		t.Fatalf("body = %q want %q", got, payload)
	}
	// The blob is now cached.
	if info, _ := blobs.Stat(t.Context(), digest); info == nil || info.Size != int64(len(payload)) {
		t.Fatalf("blob not cached")
	}
	// The CDN received the presigned request WITHOUT the registry bearer
	// credential (a cross-host redirect must drop Authorization).
	if cdnAuth != "" {
		t.Fatalf("CDN saw Authorization %q; must be dropped on cross-host redirect", cdnAuth)
	}
}

// TestBlobRedirectDigestMismatch: a redirect target serving the wrong bytes
// fails the digest check and caches nothing (502).
func TestBlobRedirectDigestMismatch(t *testing.T) {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("wrong bytes"))
	}))
	t.Cleanup(cdn.Close)
	registrySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, cdn.URL+"/blob", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(registrySrv.Close)

	blobs, _ := store.NewFileBlobStore(t.TempDir())
	meta, _ := store.OpenSQLite(t.TempDir() + "/meta.db")
	t.Cleanup(func() { _ = meta.Close() })
	reg := &artifactkit.Registry{Blobs: blobs, Meta: meta,
		Upstreams: &artifactkit.Upstreams{Proxy: map[string]string{"*": ""}}}
	a := New(&OciState{Registry: reg})

	digest := "sha256:" + hexDigest([]byte("the-real-bytes"))
	regHost := strings.TrimPrefix(registrySrv.URL, "http://")
	req := httptest.NewRequest(http.MethodGet, "/v2/team/app/blobs/"+digest, nil)
	req.Host = regHost
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d want 502", rec.Code)
	}
	if size, _ := blobs.Stat(t.Context(), digest); size != nil {
		t.Fatal("mismatched blob must not be cached")
	}
}
