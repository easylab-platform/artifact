package artifactkit

import (
	"context"
	"strings"

	"github.com/easylab-platform/artifact/targets"
)

// Registry host handling for the container protocols. A docker/podman client
// puts the registry in TLS SNI and the Host header, never in the path:
//
//	docker pull ghcr.io/acme/app  ->  Host: ghcr.io, GET /v2/acme/app/...
//	docker pull acme/app          ->  Host: registry-1.docker.io, GET /v2/acme/app/...
//
// So the Host is the only place the target registry can be learned, and it is
// also the right namespace: ghcr.io/acme/app and docker.io/acme/app are
// different images.

// canonicalHosts folds the aliases of one registry onto a single host name, so
// an image pulled by any alias lands in the same repository.
var canonicalHosts = map[string]string{
	"index.docker.io":      "docker.io",
	"registry-1.docker.io": "docker.io",
	"registry.docker.io":   "docker.io",
}

// CanonicalHost lower-cases a host and folds known registry aliases.
func CanonicalHost(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if c, ok := canonicalHosts[h]; ok {
		return c
	}
	return h
}

// IsRegistryHost reports whether a Host header names an upstream container
// registry rather than the gateway itself. The gateway is reached by an
// in-cluster name ("easylab", "easylab.ns.svc.cluster.local") or a bare
// address; a public registry is a dotted hostname. The docker
// insecure-registry convention additionally makes "localhost:port" and
// "IP:port" registries, which is what the test/local-registry paths use.
func IsRegistryHost(host string) bool {
	h := CanonicalHost(host)
	if h == "" {
		return false
	}
	hostOnly, _, hasPort := splitHostPort(h)
	switch {
	case hostOnly == "localhost" || isIPLiteral(hostOnly):
		// Bare addresses are the gateway; with an explicit port they follow
		// the insecure-registry convention and are treated as registries.
		return hasPort
	case strings.HasSuffix(hostOnly, ".local"),
		strings.HasSuffix(hostOnly, ".svc"),
		strings.HasSuffix(hostOnly, ".cluster.local"),
		strings.HasSuffix(hostOnly, ".internal"):
		return false
	}
	// A single-label name is an in-cluster service (easylab, artifact, ...).
	// Anything dotted is a public registry name.
	return strings.Contains(hostOnly, ".")
}

// baseHost extracts the lowercased host of a base URL.
func baseHost(raw string) string {
	rest := raw
	if i := strings.Index(raw, "://"); i >= 0 {
		rest = raw[i+3:]
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.LastIndex(rest, "@"); i >= 0 {
		rest = rest[i+1:]
	}
	return CanonicalHost(rest)
}

// splitHostPort splits "host:port"; a bare host returns hasPort=false.
func splitHostPort(h string) (host, port string, hasPort bool) {
	i := strings.LastIndexByte(h, ':')
	if i < 0 {
		return h, "", false
	}
	return h[:i], h[i+1:], true
}

func isIPLiteral(h string) bool {
	if h == "" {
		return false
	}
	dots, colons := 0, 0
	for _, c := range h {
		switch {
		case c >= '0' && c <= '9':
		case c == '.':
			dots++
		case c == ':':
			colons++
		case (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') || c == '%':
		default:
			return false
		}
	}
	return dots == 3 || colons > 0
}

type registryHostKey struct{}

// WithRegistryHost stores the registry host implied by a request's Host
// header. OCI uses it as the repository namespace and the upstream selector.
func WithRegistryHost(ctx context.Context, host string) context.Context {
	return context.WithValue(ctx, registryHostKey{}, host)
}

// RegistryHostFrom returns the registry host for a request: the explicit value
// from WithRegistryHost when set, else the Host the scope middleware recorded
// (OCI's /v2 mount). "" means "not a registry host", i.e. the default registry.
func RegistryHostFrom(ctx context.Context) string {
	h := RepoScopeFrom(ctx).Host
	if v, ok := ctx.Value(registryHostKey{}).(string); ok && v != "" {
		h = v
	}
	if !IsRegistryHost(h) {
		return ""
	}
	return h
}

// knownHosts returns the set of upstream hosts this registry knows, derived
// from the target registry when present (the single source of truth) and from
// the upstream table otherwise, plus any explicitly allowed hosts. The host is
// the key; the value is the scheme.
//
// Deriving it from the table (rather than keeping a second list) means the
// table stays the single source of truth: adding an ecosystem to the defaults
// also makes its host reachable through the X-Forwarded-* path.
func (u *Upstreams) knownHosts() map[string]string {
	if u.Targets != nil {
		out := u.Targets.KnownHosts()
		// Explicit per-deployment entries may add hosts the built-in table
		// does not carry (a private mirror, an internal proxy).
		for _, raw := range u.Overrides {
			addHost(out, raw)
		}
		for _, e := range u.Repos {
			addHost(out, e.Base)
		}
		for _, h := range u.AllowedHosts {
			addHost(out, h)
		}
		return out
	}
	out := map[string]string{}
	for _, base := range u.Defaults {
		addHost(out, base)
	}
	for _, base := range u.Overrides {
		addHost(out, base)
	}
	for _, e := range u.Repos {
		addHost(out, e.Base)
	}
	for _, h := range u.AllowedHosts {
		addHost(out, h)
	}
	return out
}

// addHost registers one raw base/URL/host into a scheme->host map (first wins).
func addHost(out map[string]string, raw string) {
	if raw == "" {
		return
	}
	scheme := "https"
	rest := raw
	if i := strings.Index(raw, "://"); i >= 0 {
		scheme, rest = raw[:i], raw[i+3:]
	}
	// Strip path, query, userinfo.
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.LastIndex(rest, "@"); i >= 0 {
		rest = rest[i+1:]
	}
	if rest == "" {
		return
	}
	host := CanonicalHost(rest)
	if _, ok := out[host]; !ok {
		out[host] = scheme
	}
}

// HostBase reconstructs an upstream base (scheme://host) from the origin a
// client reached us by (the egress proxy records the original host/scheme as
// X-Forwarded-Host/Proto). It succeeds only when host is one the table already
// knows, so a hostile client cannot aim the mirror at an arbitrary origin. The
// stored scheme is the fallback; an http/https X-Forwarded-Proto wins
// (deb.debian.org is reached over http).
//
// The client's stripped path prefix (/maven2, /stable) is restored here
// because it belongs to the origin the client dialed; every other upstream
// base comes from the table and needs none.
//
// When a target registry is configured, the target that owns the host is
// authoritative: its Base already carries the mirror's path prefix (Google
// Maven's /dl/android/maven2) and its scheme is used, so a client that did not
// strip anything still reaches the right place.
func (u *Upstreams) HostBase(proto, host, prefix string) (string, bool) {
	if u == nil || u.AirGap {
		return "", false
	}
	host = CanonicalHost(host)
	if host == "" {
		return "", false
	}
	if u.Targets != nil {
		// A target with a fixed base is authoritative for its OWN host: the
		// base already carries the mirror's path prefix (Google Maven's
		// /dl/android/maven2), so a client that stripped nothing still lands
		// on the right path. The base's host must equal the requested host,
		// so a wildcard host entry (cdn-lfs.huggingface.co) does not inherit
		// the parent's base.
		for _, cand := range u.Targets.ByHost(host) {
			if cand.Base == "" {
				continue
			}
			if baseHost(cand.Base) == host {
				return trimSlash(cand.Base), true
			}
		}
	}
	scheme, ok := u.knownHosts()[host]
	if !ok {
		return "", false
	}
	if proto == "http" || proto == "https" {
		scheme = proto
	}
	base := scheme + "://" + host
	if strings.HasPrefix(prefix, "/") {
		base += strings.TrimSuffix(prefix, "/")
	}
	return base, true
}

// AllowedSource reports the recorded scheme for a host that may be used as an
// upstream source. Unlike HostBase it does not require the host to be in the
// upstream table: any syntactically valid public hostname is accepted, because
// the caller (the git/ivy mirrors) treats the requested host as the source
// identity. Cluster-local names and bare addresses are rejected so the mirror
// cannot be pointed at the gateway itself.
func (u *Upstreams) AllowedSource(host string) (string, bool) {
	if u == nil || u.AirGap {
		return "", false
	}
	h := CanonicalHost(host)
	if h == "" || !IsRegistryHost(h) {
		return "", false
	}
	if scheme, ok := u.knownHosts()[h]; ok {
		return scheme, true
	}
	return "https", true
}

// IsPublicHostname reports whether host looks like a public DNS name (dotted,
// not an IP literal, not a cluster-local name, not localhost). The git and ivy
// mirrors use it to tell a real upstream server (github.com) from the gateway
// they are themselves running behind, since their upstream identity may come
// from the request origin rather than the path. It delegates to the shared
// targets guard so the egress catch-all and the adapters agree.
func IsPublicHostname(host string) bool {
	return targets.IsPublicHost(CanonicalHost(host))
}
