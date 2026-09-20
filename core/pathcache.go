package artifactkit

import (
	"context"
	"net/http"
	"time"
)

// This file generalizes the netcache freshness model to the path-tree protocol
// adapters (apk/debian/rpm/conda/nix/httpcache/...): cache-first reads that
// respect a TTL, conditional revalidation with If-None-Match/If-Modified-Since,
// and negative caching. The storage KEY is the adapter's own
// (format, repository, version); only the freshness/refresh behavior is shared,
// so coordinate protocols keep their (package, version) identity unchanged.

// DefaultPathTTL is how long a mutable path-tree entry is trusted before a
// conditional revalidation. It only matters when the origin returns a validator
// (ETag/Last-Modified): without one, cached bytes are served indefinitely
// rather than re-downloaded (a large index cannot be cheaply revalidated).
const DefaultPathTTL = 5 * time.Minute

// PathCachePolicy tunes how one path-tree entry is kept fresh.
type PathCachePolicy struct {
	// Immutable marks content that never changes (a versioned/digest file):
	// it is cached forever and never revalidated.
	Immutable bool
	// TTL is how long a mutable entry is trusted before revalidation. Zero
	// uses DefaultPathTTL.
	TTL time.Duration
	// MediaType is recorded and replayed for the entry.
	MediaType string
	// BlobName is the descriptor name; defaults to the path's base.
	BlobName string
	// NoStore proxies without reading or writing the cache (a per-client
	// credential must not be shared), matching netcache's NoStore.
	NoStore bool
}

// PathCacheResult is the outcome of FetchCachedPath.
type PathCacheResult struct {
	// Artifact carries the digest, size, media type and the replayable HTTP
	// metadata (Content-Encoding/ETag/Last-Modified). Serve it with
	// ServeCachedBlob so the stored headers reach the client.
	Artifact Artifact
	// Hit is true when the bytes were served from the cache with no upstream
	// request.
	Hit bool
	// OK is false when the object is absent upstream (or air-gapped/unreachable).
	OK bool
}

// Digest is the CAS digest of the served bytes ("" when !OK).
func (p PathCacheResult) Digest() string { return p.Artifact.Digest }

// Size is the byte length (0 when !OK).
func (p PathCacheResult) Size() int64 {
	if len(p.Artifact.Blobs) > 0 {
		return p.Artifact.Blobs[0].Size
	}
	return 0
}

// pathCacheResult is the single-flight payload.
type pathCacheResult struct {
	art Artifact
	hit bool
	ok  bool
}

// FetchCachedPath returns the cached artifact for one path-tree object, serving
// local bytes when fresh and revalidating or fetching otherwise. OK is false
// when the object is absent upstream (or air-gapped / unreachable via a 4xx).
// Hit is true when the bytes were served from the cache without any upstream
// request. Serve the result with ServeCachedBlob to replay stored headers.
//
// Revalidation rules:
//   - fresh (immutable, or within TTL): served locally, no network.
//   - stale WITH a validator: one conditional request; 304 extends the TTL and
//     serves the cached bytes; 200 replaces them.
//   - stale WITHOUT a validator: served locally. Re-downloading a large index
//     that cannot be conditionally revalidated would violate "fetch once".
func (r *Registry) FetchCachedPath(ctx context.Context, format, repo, version, path, upstreamBase string, pol PathCachePolicy) PathCacheResult {
	now := time.Now()

	// Cache read (unless proxying an unshareable, credentialed request).
	var art Artifact
	var have bool
	if !pol.NoStore {
		if a, err := r.Meta.Get(ctx, format, repo, version); err == nil && a.Digest != "" && len(a.Blobs) > 0 {
			art, have = a, true
			if isFresh(a, now) {
				return PathCacheResult{Artifact: a, Hit: true, OK: true}
			}
			// Stale. Without a validator we cannot cheaply revalidate, so keep
			// serving the cached bytes instead of re-downloading.
			if !hasValidator(a) {
				return PathCacheResult{Artifact: a, Hit: true, OK: true}
			}
		}
	}

	// Resolve the upstream for this path.
	remote, err := r.remoteFor(ctx, format, repo, upstreamBase)
	if err != nil {
		return PathCacheResult{}
	}

	// Single-flight: one upstream request per (url, auth) even under a burst.
	// The key includes the storage identity too: the same path can name
	// different objects in different repos/versions (conda channels, debian
	// suites), and those must not collapse onto one result.
	flight := flightKey(flightPathBlob, remote, path) + "\x00" + format + "\x00" + repo + "\x00" + version
	v, _, _ := fetchGroup.Do(flight, func() (any, error) {
		// Re-check inside the flight: a peer may have just refreshed the entry
		// we found stale. Only a genuinely fresh entry short-circuits.
		if !pol.NoStore {
			if a, err := r.Meta.Get(ctx, format, repo, version); err == nil && a.Digest != "" && len(a.Blobs) > 0 {
				if isFresh(a, time.Now()) {
					return pathCacheResult{art: a, hit: true, ok: true}, nil
				}
			}
		}
		return r.fetchCachedPathDirect(ctx, remote, format, repo, version, path, pol, have, art)
	})
	res := v.(pathCacheResult)
	return PathCacheResult{Artifact: res.art, Hit: res.hit, OK: res.ok}
}

// fetchCachedPathDirect performs the (conditional) upstream GET and stores it.
func (r *Registry) fetchCachedPathDirect(ctx context.Context, remote *Remote, format, repo, version, path string, pol PathCachePolicy, have bool, prev Artifact) (pathCacheResult, error) {
	// Conditional revalidation when we hold a stale copy with a validator.
	extra := map[string]string{}
	if have && !pol.NoStore {
		extra = conditionalHeaders(prev)
	}
	// Retrying GET (survives egress-proxy 502 flaps); follows redirects so a
	// tree root may point at a regional mirror.
	resp, err := remote.getStreamRetryCond(ctx, path, extra)
	if err != nil {
		return pathCacheResult{}, nil
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotModified && have && !pol.NoStore {
		// Refresh the TTL/validators and serve the cached bytes.
		refreshFrom304(&prev, resp.Header, ttlOf(pol))
		LogMetaErr(format+" revalidate", r.Meta.Put(ctx, prev))
		return pathCacheResult{art: prev, hit: true, ok: true}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return pathCacheResult{}, nil
	}

	digest, size, err := r.streamToBlob(ctx, resp.Body)
	if err != nil {
		return pathCacheResult{}, nil
	}
	art := artifactFromResponse(format, repo, version, digest, size, resp.Header,
		pol.MediaType, blobNameOr(pol.BlobName, path), pol.Immutable, ttlOf(pol))
	art.Target = RepoScopeFrom(ctx).Target
	if pol.NoStore {
		return pathCacheResult{art: art, hit: false, ok: true}, nil
	}
	LogMetaErr(format+" cache", r.Meta.Put(ctx, art))
	return pathCacheResult{art: art, hit: false, ok: true}, nil
}

// expiryFor computes a mutable entry's expiry from the policy.
func expiryFor(pol PathCachePolicy) time.Time {
	return time.Now().Add(ttlOf(pol))
}

// ttlOf is the effective TTL for a policy.
func ttlOf(pol PathCachePolicy) time.Duration {
	if pol.TTL > 0 {
		return pol.TTL
	}
	return DefaultPathTTL
}

func blobNameOr(name, path string) string {
	if name != "" {
		return name
	}
	return baseNameOf(path)
}
