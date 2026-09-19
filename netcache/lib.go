// Package netcache implements the generic URI cache: it fetches any absolute
// HTTP(S) URL through the gateway, stores the bytes in the blob CAS, and
// serves later requests from local storage. It is what makes "no asset is
// fetched from the internet twice" true for arbitrary hosts, not just the
// ecosystems with a dedicated protocol adapter.
//
// Addressing:
//
//	/artifacts/netcache/<host>/<path>?<query>   explicit (host in the path)
//
// Transparent (the egress catch-all preserves the client's Host):
//
//	X-Forwarded-Host: <host>, path = /artifacts/netcache/<path>?<query>
//
// In both forms the cached identity is the normalized absolute URL (path +
// query; credential query parameters are stripped, so a presigned URL and its
// unsigned form share one entry).
package netcache

import (
	"net/http"
	"strings"

	"github.com/easylab-platform/artifact/core"
)

type State struct {
	Registry *artifactkit.Registry
	Auth     artifactkit.Auth
}

func NewHandler(reg *artifactkit.Registry, cfg map[string]any) (http.Handler, error) {
	s := &State{Registry: reg}
	if a, ok := cfg["auth"].(artifactkit.Auth); ok {
		s.Auth = a
	}
	return s, nil
}

func init() { artifactkit.Register("netcache", NewHandler) }

// ServeHTTP resolves the target URL and serves it cache-first.
func (s *State) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	rawURL, ok := s.targetURL(r)
	if !ok {
		artifactkit.Error(w, http.StatusNotFound, "not found")
		return
	}
	opts := artifactkit.URIOptions{Headers: passthroughHeaders(r), KeyURL: clientURL(r)}
	// A request carrying a per-client credential must never be shared: the
	// response may be private to that credential. Proxy it without storing.
	if r.Header.Get("Authorization") != "" || r.Header.Get("Proxy-Authorization") != "" {
		opts.NoStore = true
	}
	res, err := s.Registry.FetchURIToBlob(r.Context(), rawURL, opts)
	if err != nil {
		artifactkit.Error(w, http.StatusBadGateway, "upstream: "+err.Error())
		return
	}
	ct := res.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}
	// Replay the upstream's content-coding so the client decodes bytes the
	// same way it would from the origin. go's http.ServeContent does not set
	// Content-Encoding for us.
	if enc := res.Header.Get("Content-Encoding"); enc != "" {
		w.Header().Set("Content-Encoding", enc)
	}
	if !artifactkit.ServeBlobAt(w, r, s.Registry.Blobs, r.Context(), res.Digest, ct) {
		artifactkit.Error(w, http.StatusBadGateway, "cache error")
	}
}

// targetURL reconstructs the absolute URL the client meant. The host comes from
// the request scope when the egress proxy preserved a public host (transparent
// mode), else from the first path segment (explicit mode). The query string is
// preserved verbatim.
//
// A host resolved to a DECLARED target fetches from that target's Base (an
// operator can point a public name at a private mirror, or declare an internal
// host); any other host must be a public DNS name (SSRF guard).
func (s *State) targetURL(r *http.Request) (string, bool) {
	rest := strings.TrimPrefix(r.URL.Path, artifactkit.MountBase+"/netcache")
	rest = strings.TrimPrefix(rest, "/netcache")
	rest = strings.TrimPrefix(rest, "/")

	sc := artifactkit.RepoScopeFrom(r.Context())
	host := artifactkit.CanonicalHost(sc.Host)
	scheme := sc.Proto

	// Explicit form when the request did not arrive on a rewritten public
	// host: the first path segment names the host.
	if !artifactkit.IsPublicHostname(host) {
		h, tail, ok := splitHost(rest)
		if !ok {
			return "", false
		}
		host = artifactkit.CanonicalHost(h)
		rest = tail
	}
	if host == "" {
		return "", false
	}

	// A declared target wins: it supplies the real origin (and, later, auth).
	if t, ok := s.Registry.Upstreams.TargetFor("netcache", host); ok && t.Base != "" {
		return joinURL(t.Base, rest, r.URL.RawQuery), true
	}
	if !artifactkit.IsPublicHostname(host) {
		return "", false
	}
	if scheme != "http" && scheme != "https" {
		scheme = "https"
	}
	return joinURL(scheme+"://"+host, rest, r.URL.RawQuery), true
}

// joinURL builds base + "/" + rest + "?" + query without doubling slashes.
func joinURL(base, rest, query string) string {
	base = strings.TrimSuffix(base, "/")
	if rest != "" {
		base += "/" + rest
	}
	if query != "" {
		base += "?" + query
	}
	return base
}

// clientURL is the logical asset identity: the URL the client asked for, using
// the host it dialed (transparent) or named (explicit) and the request path +
// query. It is the cache key, so the same logical asset served by two different
// mirrors still shares one entry.
func clientURL(r *http.Request) string {
	rest := strings.TrimPrefix(r.URL.Path, artifactkit.MountBase+"/netcache")
	rest = strings.TrimPrefix(rest, "/netcache")
	rest = strings.TrimPrefix(rest, "/")

	sc := artifactkit.RepoScopeFrom(r.Context())
	host := artifactkit.CanonicalHost(sc.Host)
	scheme := sc.Proto
	if !artifactkit.IsPublicHostname(host) {
		h, tail, ok := splitHost(rest)
		if !ok {
			return ""
		}
		host = artifactkit.CanonicalHost(h)
		rest = tail
	}
	if scheme != "http" && scheme != "https" {
		scheme = "https"
	}
	return joinURL(scheme+"://"+host, rest, r.URL.RawQuery)
}

// splitHost splits "<host>/<rest>" (host is the first segment).
func splitHost(p string) (host, rest string, ok bool) {
	p = strings.Trim(p, "/")
	if p == "" {
		return "", "", false
	}
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i], p[i+1:], true
	}
	return p, "", true
}

// passthroughHeaders forwards the client's own credentials to the upstream so a
// private asset behind the same authorization works with no configuration.
// Authorization is the only credential carried; Range is NOT forwarded because
// the full object is cached and Range is served locally afterwards, and no
// other header is forwarded so a hostile client cannot inject routing headers.
func passthroughHeaders(r *http.Request) http.Header {
	h := http.Header{}
	for _, k := range []string{"Authorization", "Proxy-Authorization", "Accept", "Accept-Language"} {
		if v := r.Header.Get(k); v != "" {
			h.Set(k, v)
		}
	}
	return h
}

var _ = artifactkit.MountBase
