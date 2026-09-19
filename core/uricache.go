package artifactkit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// This file implements the generic URI cache: fetch any absolute HTTP(S) URL,
// stream it into the blob CAS, and serve later requests from local bytes. It
// is the engine behind the `netcache` protocol and the "no asset is fetched
// from the internet twice" goal.
//
// Design points:
//
//   - The cache KEY is the normalized request URI (path + query, with
//     credential-bearing query parameters removed), not the path alone. A
//     presigned URL and the same object without its signature must not collide.
//   - Bytes are streamed into the CAS (never buffered whole), so multi-GB
//     assets are safe.
//   - Concurrent misses for one key are collapsed (single-flight) so N
//     simultaneous requests cause one upstream fetch.
//   - Immutable URLs (a digest or content hash appears in the URI) are cached
//     permanently; everything else carries a TTL and is revalidated with
//     If-None-Match / If-Modified-Since before being re-fetched.
type uriCache struct {
	sf singleflight.Group
	// ttl is how long a mutable entry is trusted before revalidation.
	ttl time.Duration
	// negTTL is how long a negative (404/410) result is remembered.
	negTTL time.Duration
}

var (
	uriCacheOnce sync.Once
	sharedURI    *uriCache
)

// DefaultURICacheTTL is how long a mutable (non-immutable) URI is served from
// cache before a revalidation request is issued.
const DefaultURICacheTTL = 5 * time.Minute

// DefaultNegativeTTL is how long a 404/410 is remembered, so a scanner cannot
// hammer the upstream for a missing object.
const DefaultNegativeTTL = 60 * time.Second

// NegativeMediaType marks a remembered netcache negative (404/410) entry: it
// holds no blob and exists only to absorb repeated misses until it expires. It
// is stored in the MediaType column and is the one media type that is read
// BEFORE the digest/blob emptiness checks, because a negative entry has
// neither.
const NegativeMediaType = "application/x-netcache-miss"

func sharedURICache() *uriCache {
	uriCacheOnce.Do(func() {
		sharedURI = &uriCache{ttl: DefaultURICacheTTL, negTTL: DefaultNegativeTTL}
	})
	return sharedURI
}

// SetURICacheTTL overrides the shared URI cache's mutable-entry TTL. It exists
// for tests and operator tuning; a non-positive value restores the default.
func SetURICacheTTL(ttl time.Duration) {
	if ttl <= 0 {
		ttl = DefaultURICacheTTL
	}
	sharedURICache().ttl = ttl
}

// CacheKey normalizes a URL into the cache key: scheme and host lowercased,
// default ports dropped, query retained but credential-bearing parameters
// stripped and the rest sorted (so parameter order does not fragment the
// cache). The result is stable across the presigned/unsigned forms of the same
// object.
func CacheKey(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	// Drop the default port so https://h:443/x and https://h/x share.
	if (u.Scheme == "https" && strings.HasSuffix(u.Host, ":443")) ||
		(u.Scheme == "http" && strings.HasSuffix(u.Host, ":80")) {
		u.Host = strings.TrimSuffix(strings.TrimSuffix(u.Host, ":443"), ":80")
	}
	q := u.Query()
	for k := range q {
		if credentialParam(k) {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode() // url.Values.Encode sorts by key
	u.Fragment = ""
	return u.String()
}

// credentialParam reports whether a query parameter carries a per-request
// credential (a presigned URL's signature). Such parameters are excluded from
// the cache key so the object is cached under its stable identity, not under a
// short-lived authorization.
func credentialParam(k string) bool {
	lk := strings.ToLower(k)
	switch lk {
	case "token", "access_token", "signature", "sig", "expires", "expire",
		"x-amz-signature", "x-amz-credential", "x-amz-security-token",
		"x-amz-date", "x-amz-expires", "x-amz-signedheaders",
		"x-goog-signature", "x-goog-credential", "x-goog-date",
		"awsaccesskeyid", "x-ms-signature":
		return true
	}
	return false
}

// IsImmutableURL reports whether a URL denotes content that can never change,
// so it is cached permanently: a digest/hash appears in the path or query, or
// the filename carries a version-like immutable marker.
func IsImmutableURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	hay := strings.ToLower(u.Path + "?" + u.RawQuery)
	for _, marker := range []string{
		"sha256:", "sha512:", "sha1:", "md5:",
		"sha256=", "sha512=", "sha1=", "md5=",
		"integrity=", "checksum=", "digest=", "@sha256:",
	} {
		if strings.Contains(hay, marker) {
			return true
		}
	}
	return false
}

// URICacheResult is the outcome of caching one URI.
type URICacheResult struct {
	Digest string
	Size   int64
	Header http.Header
	// Immutable is true when the entry is cached forever.
	Immutable bool
	// Status is the upstream status the bytes came from (200/304).
	Status int
}

// URIOptions tunes one FetchURIToBlob call.
type URIOptions struct {
	// Headers are sent on the upstream request (passthrough credentials,
	// Accept). They never enter the cache key.
	Headers http.Header
	// KeyURL, when set, is the cache identity instead of the fetch URL. It is
	// the CLIENT-facing absolute URL: a declared target may fetch the same
	// asset from an internal mirror, but the asset's identity is what the
	// client asked for, so two clients of the same logical asset share one
	// entry regardless of which mirror served it.
	KeyURL string
	// NoStore skips both the cache read and the cache write, so a request
	// carrying a per-client credential is proxied but never shared. Immutable
	// public objects do not need this; credentialed private ones do.
	NoStore bool
}

// FetchURIToBlob fetches an absolute URL into the CAS, deduplicating concurrent
// requests for the same key and streaming the body (no full buffering). When a
// cached entry exists and is fresh (immutable, or within TTL), it is returned
// without touching the network. Redirects are followed.
func (r *Registry) FetchURIToBlob(ctx context.Context, rawURL string, opts URIOptions) (URICacheResult, error) {
	keyURL := rawURL
	if opts.KeyURL != "" {
		keyURL = opts.KeyURL
	}
	key := CacheKey(keyURL)
	uc := sharedURICache()

	if !opts.NoStore {
		// Fast path: a fresh cached entry, or a remembered negative so a
		// scanner does not hammer the origin for a missing object.
		if res, ok := r.cachedURI(ctx, key); ok {
			return res, nil
		}
		if r.cachedNegative(ctx, key) {
			return URICacheResult{}, &UpstreamStatusError{Path: key, Status: http.StatusNotFound}
		}
	}

	v, err, _ := uc.sf.Do(key, func() (any, error) {
		if !opts.NoStore {
			// Re-check inside the single-flight: a peer may have just stored it.
			if res, ok := r.cachedURI(ctx, key); ok {
				return res, nil
			}
			if r.cachedNegative(ctx, key) {
				return URICacheResult{}, &UpstreamStatusError{Path: key, Status: http.StatusNotFound}
			}
		}
		return r.fetchURI(ctx, key, rawURL, opts)
	})
	if err != nil {
		return URICacheResult{}, err
	}
	return v.(URICacheResult), nil
}

// cachedURI returns a fresh cached entry for key, or false. Immutable entries
// never expire; mutable ones are trusted for the cache TTL. A negative entry
// is NOT a usable entry and always reports false (the caller consults
// cachedNegative separately).
func (r *Registry) cachedURI(ctx context.Context, key string) (URICacheResult, bool) {
	art, err := r.Meta.Get(ctx, "netcache", "uri", key)
	if err != nil || art.Digest == "" || len(art.Blobs) == 0 {
		return URICacheResult{}, false
	}
	if art.MediaType == NegativeMediaType {
		return URICacheResult{}, false
	}
	if !isFresh(art, time.Now()) {
		return URICacheResult{}, false
	}
	return URICacheResult{
		Digest:    art.Digest,
		Size:      art.Blobs[0].Size,
		Header:    headerFromArtifact(art),
		Immutable: art.CacheControl == "immutable",
		Status:    http.StatusOK,
	}, true
}

// cachedNegative reports whether a fresh negative (404/410) entry is remembered
// for key. It is checked separately from cachedURI because a negative entry has
// no digest or blobs, so cachedURI would reject it before ever inspecting the
// marker. A stale entry reports false, so the caller re-probes the origin.
func (r *Registry) cachedNegative(ctx context.Context, key string) bool {
	art, err := r.Meta.Get(ctx, "netcache", "uri", key)
	if err != nil || art.MediaType != NegativeMediaType {
		return false
	}
	return isFresh(art, time.Now())
}

// fetchURI performs the upstream GET and stores the result. When a stale entry
// exists with a validator, the request is conditional and a 304 refreshes the
// entry's TTL without re-downloading the body.
func (r *Registry) fetchURI(ctx context.Context, key, rawURL string, opts URIOptions) (URICacheResult, error) {
	// The call's own headers (passthrough Authorization, Accept) ride on the
	// Remote; conditional validators are added per attempt by the retrying GET.
	remote := NewRemote(sharedFactory, "", proxyPtr(r.Upstreams, "netcache"))
	for k, vs := range opts.Headers {
		for _, v := range vs {
			remote = remote.WithHeader(k, v)
		}
	}
	// Conditional revalidation: if we hold a stale copy with a validator, ask
	// the origin whether it changed. A credentialed request is never
	// revalidated against a shared entry (NoStore skips this path entirely).
	extra := map[string]string{}
	if !opts.NoStore {
		if prev, err := r.Meta.Get(ctx, "netcache", "uri", key); err == nil && hasValidator(prev) {
			extra = conditionalHeaders(prev)
		}
	}
	// Retrying, redirect-following GET (presigned CDN URLs, regional mirrors;
	// the egress proxy surfaces upstream blips as 502).
	resp, err := remote.getStreamRetryCond(ctx, rawURL, extra)
	if err != nil {
		return URICacheResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	// 304: the cached bytes are still current. Refresh the TTL and serve them.
	if resp.StatusCode == http.StatusNotModified && !opts.NoStore {
		if res, ok := r.refreshURI(ctx, key, resp.Header); ok {
			return res, nil
		}
		// No usable entry to refresh: fall through and treat as a miss.
		return URICacheResult{}, &UpstreamStatusError{Path: key, Status: resp.StatusCode}
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		if !opts.NoStore {
			r.storeNegative(ctx, key)
		}
		return URICacheResult{}, &UpstreamStatusError{Path: key, Status: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return URICacheResult{}, &UpstreamStatusError{Path: key, Status: resp.StatusCode}
	}

	digest, size, err := r.streamToBlob(ctx, resp.Body)
	if err != nil {
		return URICacheResult{}, err
	}
	if opts.NoStore {
		return URICacheResult{Digest: digest, Size: size, Header: resp.Header, Status: http.StatusOK}, nil
	}
	immutable := IsImmutableURL(rawURL)
	if opts.KeyURL != "" {
		immutable = immutable || IsImmutableURL(opts.KeyURL)
	}
	art := artifactFromResponse("netcache", "uri", key, digest, size, resp.Header,
		mediaTypeOrOctet(resp.Header.Get("Content-Type")), "", immutable, sharedURICache().ttl)
	LogMetaErr("netcache put", r.Meta.Put(ctx, art))
	return URICacheResult{Digest: digest, Size: size, Header: resp.Header, Immutable: immutable, Status: http.StatusOK}, nil
}

// refreshURI extends a cached entry's TTL after a 304, adopting any validators
// the origin returned. Returns the served result.
func (r *Registry) refreshURI(ctx context.Context, key string, h http.Header) (URICacheResult, bool) {
	art, err := r.Meta.Get(ctx, "netcache", "uri", key)
	if err != nil || art.Digest == "" || len(art.Blobs) == 0 {
		return URICacheResult{}, false
	}
	refreshFrom304(&art, h, sharedURICache().ttl)
	LogMetaErr("netcache revalidate", r.Meta.Put(ctx, art))
	return URICacheResult{
		Digest: art.Digest, Size: art.Blobs[0].Size,
		Header: headerFromArtifact(art), Immutable: art.CacheControl == "immutable",
		Status: http.StatusOK,
	}, true
}

// streamToBlob copies a stream into the CAS, hashing as it goes, and returns
// the digest and size without buffering the whole body in memory.
func (r *Registry) streamToBlob(ctx context.Context, body io.Reader) (string, int64, error) {
	tmp, err := os.CreateTemp("", "artifact-uricache-*")
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	defer func() { _ = tmp.Close() }()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), body)
	if err != nil {
		return "", 0, err
	}
	digest := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	if _, err := r.Blobs.PutIfAbsent(ctx, digest, tmp); err != nil {
		return "", 0, err
	}
	return digest, n, nil
}

// storeNegative records a 404/410 briefly so a scanner does not hammer the
// upstream. Best-effort.
func (r *Registry) storeNegative(ctx context.Context, key string) {
	LogMetaErr("netcache neg", r.Meta.Put(ctx, Artifact{
		Format: "netcache", Repository: "uri", Version: key,
		MediaType: "application/x-netcache-miss",
		ExpiresAt: time.Now().Add(sharedURICache().negTTL),
		Source:    "pull",
	}))
}

// mediaTypeOrOctet defaults an empty content type.
func mediaTypeOrOctet(ct string) string {
	if ct == "" {
		return "application/octet-stream"
	}
	return ct
}
