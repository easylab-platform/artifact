package artifactkit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/easylab-platform/artifact/targets"
	"golang.org/x/sync/singleflight"
)

// fetchGroup collapses concurrent identical upstream fetches process-wide: N
// simultaneous misses for one URL trigger a single upstream request, so a burst
// of clients (or a slow fetch) never downloads the same object N times. The key
// includes a digest of the request headers, so two callers with different
// credentials (a passthrough target) never share a result.
var fetchGroup singleflight.Group

// flightKey identifies one upstream fetch: the absolute URL plus a digest of the
// resolved request headers (Authorization in practice).
func flightKey(remote *Remote, path string) string {
	url := remote.URL(path)
	if len(remote.headers) == 0 {
		return url
	}
	keys := make([]string, 0, len(remote.headers))
	for k := range remote.headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		_, _ = io.WriteString(h, k)
		_, _ = io.WriteString(h, "\x00")
		_, _ = io.WriteString(h, remote.headers[k])
		_, _ = io.WriteString(h, "\x00")
	}
	return url + "\x00" + hex.EncodeToString(h.Sum(nil))
}

// UserAgent is sent on every upstream request. Registries rate-limit generic
// user agents (Maven Central 429s the default Go UA), so a stable bespoke one
// is used.
const UserAgent = "artifactkit/1.0 (pull-through mirror)"

// ClientFactory builds (and caches) an http.Client for a proxy policy:
//   - nil           -> follow the environment proxy configuration
//   - Some("")      -> direct connection (env proxy bypassed)
//   - Some(url)     -> always route through the given proxy
type ClientFactory struct {
	clients map[string]*http.Client
}

// NewClientFactory returns an empty factory.
func NewClientFactory() *ClientFactory {
	return &ClientFactory{clients: map[string]*http.Client{}}
}

// Client returns a cached client for the proxy policy that does NOT follow
// redirects. Adapters use it when a 3xx must be surfaced to the caller (e.g.
// HuggingFace resolve → CDN, or a Debian mirror redirect) so the client
// decides: transparent pass-through, or an explicit re-fetch. Silently
// following such a redirect would download the whole object server-side and
// defeat the range/caching policy.
func (f *ClientFactory) Client(proxy *string) *http.Client {
	key := proxyKey(proxy)
	if c, ok := f.clients[key]; ok {
		return c
	}
	c := &http.Client{
		Transport: newTransport(proxy),
		// No overall Timeout: large blobs stream for minutes.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	f.clients[key] = c
	return c
}

// Redirecting returns a cached client for the proxy policy that FOLLOWS
// redirects (Go's default policy: up to 10 hops). It is used by registries
// whose blob endpoint 307-redirects to a CDN (Docker Hub → CloudFront, MCR →
// Azure Blob, GHCR/Quay): the adapter must read the object through the
// redirect to verify and cache it server-side.
//
// Cross-host redirects drop the Authorization header automatically (Go's
// shouldCopyHeaderOnRedirect), which is exactly what a presigned CDN URL
// needs; same-host redirects keep it.
func (f *ClientFactory) Redirecting(proxy *string) *http.Client {
	key := "__follow__" + proxyKey(proxy)
	if c, ok := f.clients[key]; ok {
		return c
	}
	c := &http.Client{Transport: newTransport(proxy)}
	f.clients[key] = c
	return c
}

func proxyKey(proxy *string) string {
	if proxy == nil {
		return "__env__"
	}
	return *proxy
}

// newTransport builds the shared HTTP transport for a proxy policy.
func newTransport(proxy *string) *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = 20
	tr.IdleConnTimeout = 90 * time.Second
	tr.ResponseHeaderTimeout = 120 * time.Second
	if proxy != nil {
		if *proxy == "" {
			tr.Proxy = nil // direct
		} else {
			tr.Proxy = http.ProxyURL(mustParseURL(*proxy))
		}
	}
	return tr
}

// Remote is a proxy-aware upstream root URL. Adapters map their URL layout
// onto Get/GetBytes/GetCached.
type Remote struct {
	Base    string
	client  *http.Client
	headers map[string]string
}

// NewRemote creates a Remote for a base URL (trailing slash trimmed).
func NewRemote(factory *ClientFactory, base string, proxy *string) *Remote {
	return &Remote{Base: trimSlash(base), client: factory.Client(proxy)}
}

// WithHeader returns a copy with an extra header (e.g. Accept, auth).
func (r *Remote) WithHeader(k, v string) *Remote {
	cp := *r
	cp.headers = map[string]string{}
	for a, b := range r.headers {
		cp.headers[a] = b
	}
	cp.headers[k] = v
	return &cp
}

// URL joins a path onto the base.
func (r *Remote) URL(path string) string { return r.Base + path }

// Do issues an arbitrary request against the remote root. body may be nil; a
// non-empty contentType is set as the request Content-Type. It is the general
// escape hatch for protocols (git smart-HTTP) whose verbs are not plain GET.
func (r *Remote) Do(ctx context.Context, method, path string, body io.Reader, contentType, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, r.URL(path), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	for k, v := range r.headers {
		req.Header.Set(k, v)
	}
	return r.client.Do(req)
}

// Get issues GET and returns the raw response (non-2xx returned as-is).
func (r *Remote) Get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL(path), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	for k, v := range r.headers {
		req.Header.Set(k, v)
	}
	return r.client.Do(req)
}

// GetBytes GETs and errors on non-2xx, returning the body bytes. Transient
// failures — transport errors or 502/503/504 responses (an egress proxy often
// surfaces upstream blips that way) — are retried: these are idempotent
// index/file GETs, and short backoff rides out multi-second proxy flaps.
func (r *Remote) GetBytes(ctx context.Context, path string) ([]byte, error) {
	resp, err := r.getStable(ctx, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &UpstreamStatusError{Path: path, Status: resp.StatusCode}
	}
	return io.ReadAll(resp.Body)
}

// retryBackoff spaces the retries in getStable (a flap usually clears within
// a couple of seconds).
var retryBackoff = []time.Duration{500 * time.Millisecond, 1500 * time.Millisecond}

// getStable performs a GET, retrying transport errors and 502/503/504
// (classic egress-proxy hiccup codes) with backoff. Redirects and other
// statuses are returned as-is on the first response.
func (r *Remote) getStable(ctx context.Context, path string) (*http.Response, error) {
	resp, err := r.Get(ctx, path)
	for _, wait := range retryBackoff {
		if err == nil && resp.StatusCode != 502 && resp.StatusCode != 503 && resp.StatusCode != 504 {
			return resp, nil
		}
		code := 0
		if resp != nil {
			code = resp.StatusCode
			_ = resp.Body.Close()
		}
		// Retries ride on the transport's keep-alive pool; if the pinned
		// connection's egress route is the thing that's broken, every retry
		// through it fails the same way. Drop the pool so the retry dials a
		// fresh connection (and, with a proxy, possibly a fresh exit).
		r.client.CloseIdleConnections()
		if ctx.Err() != nil {
			if code == 0 {
				return nil, err
			}
			return nil, &UpstreamStatusError{Path: path, Status: code}
		}
		select {
		case <-ctx.Done():
			if code == 0 {
				return nil, err
			}
			return nil, &UpstreamStatusError{Path: path, Status: code}
		case <-time.After(wait):
		}
		resp, err = r.Get(ctx, path)
	}
	return resp, err
}

// GetCached GETs through a TTL cache keyed by absolute URL (for small index
// documents re-fetched on every client resolution).
func (r *Remote) GetCached(ctx context.Context, cache *MemCache, path string) (string, error) {
	key := r.URL(path)
	if body, ok := cache.Get(key); ok {
		return body, nil
	}
	body, err := r.GetBytes(ctx, path)
	if err != nil {
		return "", err
	}
	s := string(body)
	cache.Set(key, s)
	return s, nil
}

// UpstreamStatusError is the shared non-2xx upstream error.
type UpstreamStatusError struct {
	Path   string
	Status int
}

func (e *UpstreamStatusError) Error() string {
	return fmt.Sprintf("upstream %s: status %d", e.Path, e.Status)
}

// Registry implementation ----------------------------------------------------

// Fetch pulls `path` from an upstream and caches it in the blob store. A
// redirect from the upstream (3xx with Location) is surfaced as an error so
// callers can decide how to handle it instead of silently downloading the
// whole target server-side.
func (r *Registry) Fetch(ctx context.Context, format, upstreamBase, path string) (Fetched, error) {
	if upstreamBase == "" {
		// No explicit base: resolve by the full priority and restore the
		// client's stripped prefix (maven's /maven2, helm's /stable) so the
		// upstream sees the path it published.
		return r.FetchPath(ctx, format, path)
	}
	remote, err := r.remote(ctx, format, upstreamBase)
	if err != nil {
		return Fetched{}, err
	}
	return r.fetchWith(ctx, remote, path)
}

// FetchFor is Fetch for one repository of a format: the repository's upstream
// override (longest prefix match) and proxy policy decide where the request
// goes. An empty upstreamBase means "resolve from the repository policy".
func (r *Registry) FetchFor(ctx context.Context, format, repo, upstreamBase, path string) (Fetched, error) {
	remote, err := r.remoteFor(ctx, format, repo, upstreamBase)
	if err != nil {
		return Fetched{}, err
	}
	return r.fetchWith(ctx, remote, path)
}

// FetchPath pulls a format-relative path using the request's full upstream
// priority (per-repo override → client origin → table default), prepending the
// client's stripped prefix so the upstream sees the original path. Adapters
// call this instead of assembling a base themselves.
func (r *Registry) FetchPath(ctx context.Context, format, path string) (Fetched, error) {
	sc := RepoScopeFrom(ctx)
	repo := sc.Namespace
	if repo == "" {
		repo = sc.Name
	}
	remote, err := r.remoteFor(ctx, format, repo, "")
	if err != nil {
		return Fetched{}, err
	}
	return r.fetchWith(ctx, remote, path)
}

// fetchWith issues one GET against a resolved upstream and caches the bytes.
// Concurrent identical fetches (same URL and auth headers) are collapsed into
// one upstream request by a process-wide single-flight group.
func (r *Registry) fetchWith(ctx context.Context, remote *Remote, path string) (Fetched, error) {
	v, err, _ := fetchGroup.Do(flightKey(remote, path), func() (any, error) {
		return r.fetchWithDirect(ctx, remote, path)
	})
	if err != nil {
		return Fetched{}, err
	}
	return v.(Fetched), nil
}

// fetchWithDirect is fetchWith without the single-flight wrapper.
func (r *Registry) fetchWithDirect(ctx context.Context, remote *Remote, path string) (Fetched, error) {
	resp, err := remote.getStable(ctx, path)
	if err != nil {
		return Fetched{}, fmt.Errorf("http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return Fetched{}, &UpstreamRedirectError{Path: path, Status: resp.StatusCode, Location: resp.Header.Get("Location")}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Fetched{}, &UpstreamStatusError{Path: path, Status: resp.StatusCode}
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return Fetched{}, err
	}
	return r.finishFetch(ctx, data)
}

// UpstreamRedirectError reports a 3xx from an upstream.
type UpstreamRedirectError struct {
	Path     string
	Status   int
	Location string
}

func (e *UpstreamRedirectError) Error() string {
	return fmt.Sprintf("upstream %s: redirected (%d) to %s", e.Path, e.Status, e.Location)
}

// FetchViaRedirect follows an upstream redirect explicitly and fetches the
// target. It is used by adapters whose upstream legitimately redirects to a
// content host that easylab still wants to cache (e.g. HF LFS blobs).
func (r *Registry) FetchViaRedirect(ctx context.Context, url string) (Fetched, error) {
	return r.FetchAbsolute(ctx, url)
}

// FetchAbsolute pulls a full URL verbatim.
func (r *Registry) FetchAbsolute(ctx context.Context, url string) (Fetched, error) {
	factory := NewClientFactory()
	remote := NewRemote(factory, "", proxyPtr(r.Upstreams, "generic"))
	resp, err := remote.getStable(ctx, url)
	if err != nil {
		return Fetched{}, fmt.Errorf("http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Fetched{}, &UpstreamStatusError{Path: url, Status: resp.StatusCode}
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return Fetched{}, err
	}
	return r.finishFetch(ctx, data)
}

// FetchAbsoluteBytes pulls a full URL verbatim and returns its bytes.
func (r *Registry) FetchAbsoluteBytes(ctx context.Context, url string) ([]byte, error) {
	fetched, err := r.FetchAbsolute(ctx, url)
	if err != nil {
		return nil, err
	}
	return fetched.Data, nil
}

// FetchAbsoluteToBlob streams an absolute URL into the CAS, verifying it
// matches wantDigest (when non-empty) and following redirects. It is used for
// presigned CDN hrefs (git LFS objects) that the caller learned out of band.
func (r *Registry) FetchAbsoluteToBlob(ctx context.Context, url, wantDigest string) (int64, error) {
	factory := NewClientFactory()
	remote := NewRemote(factory, "", proxyPtr(r.Upstreams, "generic"))
	resp, err := remote.Get(ctx, url)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return 0, &UpstreamStatusError{Path: url, Status: resp.StatusCode}
	}
	digest := wantDigest
	if digest == "" {
		digest = "sha256:unknown"
	}
	if _, err := r.Blobs.PutIfAbsent(ctx, digest, resp.Body); err != nil {
		return 0, err
	}
	if sz, err := r.Blobs.Stat(ctx, digest); err == nil && sz != nil {
		return *sz, nil
	}
	return 0, nil
}

// StoreAndHash writes into the blob store (dedup by sha256) and returns a summary.
func (r *Registry) StoreAndHash(ctx context.Context, data []byte) (Stored, error) {
	h, _ := ComputeHashesBytes(data)
	digest := "sha256:" + h.SHA256
	if _, err := r.Blobs.PutIfAbsent(ctx, digest, bytes.NewReader(data)); err != nil {
		return Stored{}, err
	}
	return Stored{Hashes: h, Size: int64(len(data)), Digest: digest}, nil
}

// StorePathBlob is the shared "cache one fetched path" flow for the path-tree
// protocols (apk/debian/rpm/conda/nix/...): store the bytes in the CAS by
// digest, then index them under (format, repository, version). It is
// best-effort: a cache write failure never fails the response, so the caller
// keeps serving the bytes it already has.
//
// blobName is recorded on the descriptor (adapters differ: some use the
// basename, some the full relative path). Empty falls back to version.
func (r *Registry) StorePathBlob(ctx context.Context, format, repository, version, blobName, mediaType string, data []byte) string {
	stored, err := r.StoreAndHash(ctx, data)
	if err != nil {
		return ""
	}
	if blobName == "" {
		blobName = baseNameOf(version)
	}
	LogMetaErr(format+" cache", r.Meta.Put(ctx, Artifact{
		Format: format, Repository: repository, Version: version,
		MediaType: mediaType, Digest: stored.Digest,
		Blobs:  []Descriptor{{Digest: stored.Digest, Size: stored.Size, Name: blobName}},
		Source: "pull",
	}))
	return stored.Digest
}

// baseNameOf returns the final path segment ("b/bash.rpm" -> "bash.rpm").
func baseNameOf(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// Remote returns a proxy-aware upstream handle for a format. When
// upstreamBase is "" the format's configured/default upstream is used.
func (r *Registry) Remote(format, upstreamBase string) (*Remote, error) {
	base := upstreamBase
	if base == "" {
		base = r.Upstreams.Get(format)
		if base == "" {
			return nil, fmt.Errorf("no upstream for %s", format)
		}
	}
	factory := NewClientFactory()
	return NewRemote(factory, base, proxyPtr(r.Upstreams, format)), nil
}

// RemoteFor returns a proxy-aware upstream handle for one repository of a
// format: the repository override wins over the format default, and the
// repository's own proxy policy wins over the format's.
func (r *Registry) RemoteFor(format, repo, upstreamBase string) (*Remote, error) {
	base := upstreamBase
	if base == "" {
		base = r.Upstreams.Repo(format, repo)
		if base == "" {
			return nil, fmt.Errorf("no upstream for %s/%s", format, repo)
		}
	}
	factory := NewClientFactory()
	proxy, ok := r.Upstreams.RepoProxy(format, repo)
	var pp *string
	if ok {
		pp = &proxy
	}
	return NewRemote(factory, base, pp), nil
}

// RemoteAt returns a remote against an arbitrary absolute base using the
// format's proxy policy.
func (r *Registry) RemoteAt(base string) *Remote {
	factory := NewClientFactory()
	return NewRemote(factory, base, proxyPtr(r.Upstreams, "generic"))
}

// proxyPtr returns a *string to the configured proxy for key, or nil when
// unset (meaning "follow environment proxy"). An explicitly configured empty
// string means "direct" and produces a pointer to "".
func proxyPtr(u *Upstreams, key string) *string {
	v, ok := u.ProxyURL(key)
	if !ok {
		return nil
	}
	return &v
}

// remote resolves the upstream for a format. The priority is:
//
//  1. an explicit per-repository override (operator intent), else
//  2. the origin the client reached us by (X-Forwarded-Host/Proto/Prefix,
//     applied only for hosts the table already knows), else
//  3. the table default for the format's repository.
//
// An explicitly supplied base is honored verbatim (adapters that must fetch
// from a URL they already resolved).
func (r *Registry) remote(ctx context.Context, format, upstreamBase string) (*Remote, error) {
	if upstreamBase != "" {
		return r.Remote(format, upstreamBase)
	}
	sc := RepoScopeFrom(ctx)
	repo := sc.Namespace
	if repo == "" {
		repo = sc.Name
	}
	return r.remoteFor(ctx, format, repo, "")
}

// resolveBase applies the upstream priority above and returns the base plus
// its proxy policy. ok is false when no upstream is configured (air-gap).
func (r *Registry) resolveBase(ctx context.Context, format, repo string) (string, *string, bool) {
	u := r.Upstreams
	if u == nil || u.AirGap {
		return "", nil, false
	}
	// 1. explicit per-repository override.
	if e, ok := u.RepoOverride(format, repo); ok {
		var pp *string
		if e.Proxy != "" {
			pp = &e.Proxy
		} else if p, has := u.ProxyURL(format); has {
			pp = &p
		}
		return e.Base, pp, true
	}
	// 1b. an explicitly mounted target (/artifacts/<target>/...) is operator
	// intent and beats the host the client happened to dial: a request to a
	// mirror's own path always means that mirror.
	if t, ok := u.targetFromScope(ctx); ok && !t.HostOnly() {
		var pp *string
		if t.Proxy != "" {
			pp = &t.Proxy
		} else if p, has := u.ProxyURL(format); has {
			pp = &p
		}
		return trimSlash(t.Base), pp, true
	}
	// 2. origin the client used (host-driven; allow-listed by the table).
	if sc := RepoScopeFrom(ctx); sc.Host != "" {
		if base, ok := u.HostBase(sc.Proto, sc.Host, sc.Prefix); ok {
			var pp *string
			// A target may pin its own proxy; the protocol policy wins only
			// when the target leaves it unset.
			if t, ok := u.TargetFor(format, sc.Host); ok && t.Proxy != "" {
				pp = &t.Proxy
			} else if p, has := u.ProxyURL(format); has {
				pp = &p
			}
			return base, pp, true
		}
	}
	// 3. table default.
	if base := u.Repo(format, repo); base != "" {
		proxy, has := u.RepoProxy(format, repo)
		var pp *string
		if has {
			pp = &proxy
		}
		return base, pp, true
	}
	return "", nil, false
}

// remoteFor resolves a Remote for one repository using resolveBase, applying
// the resolved target's auth policy (passthrough/basic/bearer).
func (r *Registry) remoteFor(ctx context.Context, format, repo, upstreamBase string) (*Remote, error) {
	if upstreamBase != "" {
		return r.RemoteFor(format, repo, upstreamBase)
	}
	base, proxy, ok := r.resolveBase(ctx, format, repo)
	if !ok {
		return nil, fmt.Errorf("no upstream for %s/%s", format, repo)
	}
	factory := NewClientFactory()
	remote := NewRemote(factory, base, proxy)
	return r.applyAuth(ctx, format, repo, remote)
}

// applyAuth layers the resolved target's auth onto a remote. When no target is
// configured the remote is returned unchanged (anonymous).
func (r *Registry) applyAuth(ctx context.Context, format, repo string, remote *Remote) (*Remote, error) {
	t, ok := r.targetFor(ctx, format, repo)
	if !ok {
		return remote, nil
	}
	clientAuth := RepoScopeFrom(ctx).ClientAuth
	authed, err := withTargetAuth(remote, t, clientAuth)
	if err != nil {
		return nil, fmt.Errorf("auth for target %s: %w", t.ID, err)
	}
	return authed, nil
}

// targetFor resolves the target a (format, repo) request maps to: the explicit
// scope target, else the host-driven target, else the protocol default.
func (r *Registry) targetFor(ctx context.Context, format, repo string) (targets.Target, bool) {
	u := r.Upstreams
	if u == nil || u.Targets == nil {
		return targets.Target{}, false
	}
	if t, ok := u.targetFromScope(ctx); ok {
		return t, true
	}
	if sc := RepoScopeFrom(ctx); sc.Host != "" {
		if t, ok := u.TargetFor(format, sc.Host); ok {
			return t, true
		}
	}
	return u.Targets.Default(format)
}

func (r *Registry) finishFetch(ctx context.Context, data []byte) (Fetched, error) {
	stored, err := r.StoreAndHash(ctx, data)
	if err != nil {
		return Fetched{}, err
	}
	return Fetched{Data: data, Hashes: stored.Hashes, Size: stored.Size, Digest: stored.Digest}, nil
}

// ResolveUpstream exposes the upstream priority (per-repo override → client
// origin → table default) plus the client's stripped path prefix, for adapters
// that need to build their own request (metadata overlays, HEAD probes) rather
// than use Fetch.
func (r *Registry) ResolveUpstream(ctx context.Context, format, repo string) (base, prefix string, ok bool) {
	base, _, ok = r.resolveBase(ctx, format, repo)
	if !ok {
		return "", "", false
	}
	if sc := RepoScopeFrom(ctx); strings.HasPrefix(sc.Prefix, "/") {
		prefix = strings.TrimSuffix(sc.Prefix, "/")
	}
	return base, prefix, true
}

// RemoteCtx resolves the upstream for a format using the request context's
// host/repository priority (per-repo override → client origin → table
// default). Adapters should prefer it over Remote so a client's original
// hostname (recorded by the egress proxy) selects the upstream without a
// per-ecosystem table.
func (r *Registry) RemoteCtx(ctx context.Context, format string) (*Remote, error) {
	return r.remote(ctx, format, "")
}

// RemoteUpstream resolves the upstream for a format using the request context
// (host-driven) and falls back to explicitBase when the table has nothing (a
// sub-endpoint the caller knows about). It returns nil when neither exists.
func (r *Registry) RemoteUpstream(ctx context.Context, format, explicitBase string) *Remote {
	if m, err := r.remote(ctx, format, ""); err == nil {
		return m
	}
	if explicitBase == "" {
		return nil
	}
	return r.remoteAtBase(explicitBase)
}

// remoteAtBase is the ctx-free absolute-base constructor.
func (r *Registry) remoteAtBase(base string) *Remote {
	factory := NewClientFactory()
	return NewRemote(factory, base, proxyPtr(r.Upstreams, "generic"))
}

// RemoteForSub resolves a sub-endpoint of a format (cargo's index/static,
// nuget's search/registration, hex's api, ...). The configured sub-endpoint
// base is authoritative: a sub-endpoint lives on a host of its own, and the
// host the client happened to dial for the parent request (index.crates.io)
// must not redirect a static download. A per-repository override still wins,
// and the format default is the last resort.
func (r *Registry) RemoteForSub(ctx context.Context, format, sub string) (*Remote, error) {
	u := r.Upstreams
	if u == nil {
		return nil, fmt.Errorf("no upstream for %s.%s", format, sub)
	}
	if base := u.Sub(format, sub); base != "" {
		return r.remoteAtBase(base), nil
	}
	if sc := RepoScopeFrom(ctx); sc.Host != "" {
		if base, ok := u.HostBase(sc.Proto, sc.Host, sc.Prefix); ok {
			return r.remoteAtBase(base), nil
		}
	}
	if base := u.Get(format); base != "" {
		return r.remoteAtBase(base), nil
	}
	return nil, fmt.Errorf("no upstream for %s.%s", format, sub)
}

// RemoteHost resolves a Remote for an upstream identified by its hostname
// (the git/ivy mirrors address servers directly). Priority: a per-host
// repository override, then the upstream table, then any syntactically valid
// public host (source mirrors treat the requested host as the source).
func (r *Registry) RemoteHost(format, host string) (*Remote, bool) {
	if r.Upstreams == nil {
		return nil, false
	}
	if e, ok := r.Upstreams.RepoOverride(format, host); ok {
		return r.remoteAtBase(e.Base), true
	}
	if base, ok := r.Upstreams.HostBase("", host, ""); ok {
		return r.remoteAtBase(base), true
	}
	if scheme, ok := r.Upstreams.AllowedSource(host); ok {
		return r.remoteAtBase(scheme + "://" + host), true
	}
	return nil, false
}

// GetBytesFollow is GetBytes but follows redirects (up to the default policy).
// Ivy servers redirect to their artifact store (repo.scala-sbt.org →
// scala.jfrog.io) and the redirected body is what must be cached.
func (r *Remote) GetBytesFollow(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL(path), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	for k, v := range r.headers {
		req.Header.Set(k, v)
	}
	// The proxy policy is baked into r.client; reuse its transport.
	client := &http.Client{Transport: r.client.Transport}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &UpstreamStatusError{Path: path, Status: resp.StatusCode}
	}
	return io.ReadAll(resp.Body)
}

// FetchPathFollow is FetchPath but follows redirects, caching the final body.
// It is for the plain-HTTP trees whose roots redirect to a regional mirror
// (pkg.julialang.org → in.pkg.julialang.org) and for registries that redirect
// to an artifact store the mirror should read through (Ivy).
func (r *Registry) FetchPathFollow(ctx context.Context, format, path string) (Fetched, error) {
	sc := RepoScopeFrom(ctx)
	repo := sc.Namespace
	if repo == "" {
		repo = sc.Name
	}
	remote, err := r.remoteFor(ctx, format, repo, "")
	if err != nil {
		return Fetched{}, err
	}
	v, err, _ := fetchGroup.Do(flightKey(remote, path), func() (any, error) {
		data, err := remote.GetBytesFollow(ctx, path)
		if err != nil {
			return Fetched{}, err
		}
		return r.finishFetch(ctx, data)
	})
	if err != nil {
		return Fetched{}, err
	}
	return v.(Fetched), nil
}

// FetchToBlob streams a remote object into the CAS and returns its digest and
// size. Unlike Fetch it never buffers the whole body in memory, so it is the
// right call for the large, range-addressable files of the plain-HTTP trees
// (Hackage's 100MB+ index) and for anything a client may request a Range of.
// Redirects are followed (a tree root may point at a regional mirror).
func (r *Registry) FetchToBlob(ctx context.Context, format, path string) (string, int64, bool) {
	sc := RepoScopeFrom(ctx)
	repo := sc.Namespace
	if repo == "" {
		repo = sc.Name
	}
	remote, err := r.remoteFor(ctx, format, repo, "")
	if err != nil {
		return "", 0, false
	}
	type blobResult struct {
		digest string
		size   int64
		ok     bool
	}
	v, _, _ := fetchGroup.Do(flightKey(remote, path), func() (any, error) {
		digest, size, ok := r.fetchToBlobDirect(ctx, remote, path)
		return blobResult{digest, size, ok}, nil
	})
	res := v.(blobResult)
	return res.digest, res.size, res.ok
}

// fetchToBlobDirect is FetchToBlob without the single-flight wrapper.
func (r *Registry) fetchToBlobDirect(ctx context.Context, remote *Remote, path string) (string, int64, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, remote.URL(path), nil)
	if err != nil {
		return "", 0, false
	}
	req.Header.Set("User-Agent", UserAgent)
	for k, v := range remote.headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Transport: remote.client.Transport}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", 0, false
	}
	tmp, err := os.CreateTemp("", "artifact-fetch-*")
	if err != nil {
		return "", 0, false
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	defer func() { _ = tmp.Close() }()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), resp.Body)
	if err != nil {
		return "", 0, false
	}
	digest := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return "", 0, false
	}
	if _, err := r.Blobs.PutIfAbsent(ctx, digest, tmp); err != nil {
		return "", 0, false
	}
	return digest, n, true
}
