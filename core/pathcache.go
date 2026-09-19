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

// pathCacheResult is the single-flight payload.
type pathCacheResult struct {
	digest string
	size   int64
	hit    bool
	ok     bool
}

// FetchCachedPath returns the CAS digest and size for one path-tree object,
// serving local bytes when fresh and revalidating or fetching otherwise. ok is
// false when the object is absent upstream (or air-gapped / unreachable via a
// 4xx). hit is true when the bytes were served from the cache without any
// upstream request.
//
// Revalidation rules:
//   - fresh (immutable, or within TTL): served locally, no network.
//   - stale WITH a validator: one conditional request; 304 extends the TTL and
//     serves the cached bytes; 200 replaces them.
//   - stale WITHOUT a validator: served locally. Re-downloading a large index
//     that cannot be conditionally revalidated would violate "fetch once".
func (r *Registry) FetchCachedPath(ctx context.Context, format, repo, version, path, upstreamBase string, pol PathCachePolicy) (digest string, size int64, hit bool, ok bool) {
	now := time.Now()

	// Cache read (unless proxying an unshareable, credentialed request).
	var art Artifact
	var have bool
	if !pol.NoStore {
		if a, err := r.Meta.Get(ctx, format, repo, version); err == nil && a.Digest != "" && len(a.Blobs) > 0 {
			art, have = a, true
			immutable := a.CacheControl == "immutable"
			fresh := immutable || a.ExpiresAt.IsZero() || now.Before(a.ExpiresAt)
			if fresh {
				return a.Digest, a.Blobs[0].Size, true, true
			}
			// Stale. Without a validator we cannot cheaply revalidate, so keep
			// serving the cached bytes instead of re-downloading.
			if a.ETag == "" && a.LastModified == "" {
				return a.Digest, a.Blobs[0].Size, true, true
			}
		}
	}

	// Resolve the upstream for this path.
	remote, err := r.remoteFor(ctx, format, repo, upstreamBase)
	if err != nil {
		return "", 0, false, false
	}

	// Single-flight: one upstream request per (url, auth) even under a burst.
	v, _, _ := fetchGroup.Do(flightKey(remote, path), func() (any, error) {
		// Re-check inside the flight: a peer may have just refreshed the entry
		// we found stale. Only a genuinely fresh entry short-circuits.
		if !pol.NoStore {
			if a, err := r.Meta.Get(ctx, format, repo, version); err == nil && a.Digest != "" && len(a.Blobs) > 0 {
				immutable := a.CacheControl == "immutable"
				stillFresh := immutable || a.ExpiresAt.IsZero() || time.Now().Before(a.ExpiresAt)
				if stillFresh {
					return pathCacheResult{a.Digest, a.Blobs[0].Size, true, true}, nil
				}
			}
		}
		return r.fetchCachedPathDirect(ctx, remote, format, repo, version, path, pol, have, art)
	})
	res := v.(pathCacheResult)
	return res.digest, res.size, res.hit, res.ok
}

// fetchCachedPathDirect performs the (conditional) upstream GET and stores it.
func (r *Registry) fetchCachedPathDirect(ctx context.Context, remote *Remote, format, repo, version, path string, pol PathCachePolicy, have bool, prev Artifact) (pathCacheResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, remote.URL(path), nil)
	if err != nil {
		return pathCacheResult{}, nil
	}
	req.Header.Set("User-Agent", UserAgent)
	for k, v := range remote.headers {
		req.Header.Set(k, v)
	}
	// Conditional revalidation when we hold a stale copy with a validator.
	if have && !pol.NoStore {
		if prev.ETag != "" {
			req.Header.Set("If-None-Match", prev.ETag)
		}
		if prev.LastModified != "" {
			req.Header.Set("If-Modified-Since", prev.LastModified)
		}
	}
	// Follow redirects (a tree root may point at a regional mirror).
	client := &http.Client{Transport: remote.client.Transport}
	resp, err := client.Do(req)
	if err != nil {
		return pathCacheResult{}, nil
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotModified && have && !pol.NoStore {
		// Refresh the TTL/validators and serve the cached bytes.
		prev.ETag = firstNonEmpty(resp.Header.Get("ETag"), prev.ETag)
		prev.LastModified = firstNonEmpty(resp.Header.Get("Last-Modified"), prev.LastModified)
		prev.ExpiresAt = expiryFor(pol)
		LogMetaErr(format+" revalidate", r.Meta.Put(ctx, prev))
		return pathCacheResult{prev.Digest, prev.Blobs[0].Size, true, true}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return pathCacheResult{}, nil
	}

	digest, size, err := r.streamToBlob(ctx, resp.Body)
	if err != nil {
		return pathCacheResult{}, nil
	}
	if pol.NoStore {
		return pathCacheResult{digest, size, false, true}, nil
	}
	art := Artifact{
		Format: format, Repository: repo, Version: version,
		MediaType:       pol.MediaType,
		Digest:          digest,
		ETag:            resp.Header.Get("ETag"),
		LastModified:    resp.Header.Get("Last-Modified"),
		ContentEncoding: resp.Header.Get("Content-Encoding"),
		Blobs:           []Descriptor{{Digest: digest, Size: size, Name: blobNameOr(pol.BlobName, path)}},
		Source:          "pull",
		Target:          RepoScopeFrom(ctx).Target,
	}
	if pol.Immutable {
		art.CacheControl = "immutable"
	} else {
		art.ExpiresAt = expiryFor(pol)
	}
	LogMetaErr(format+" cache", r.Meta.Put(ctx, art))
	return pathCacheResult{digest, size, false, true}, nil
}

// expiryFor computes a mutable entry's expiry from the policy.
func expiryFor(pol PathCachePolicy) time.Time {
	ttl := pol.TTL
	if ttl <= 0 {
		ttl = DefaultPathTTL
	}
	return time.Now().Add(ttl)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func blobNameOr(name, path string) string {
	if name != "" {
		return name
	}
	return baseNameOf(path)
}
