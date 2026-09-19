// Package targets is the single source of truth for package-mirror targets.
//
// A *protocol* is the wire shape (maven's <group>/<artifact>/<version>/<file>,
// npm's packument, OCI's /v2 manifest flow). A *target* is where a protocol's
// content comes from: a base URL, the hostnames a client may address it by,
// and how an inbound client path maps onto the adapter's mount.
//
// The package is deliberately stdlib-only. It is imported by the artifact
// server, by easylab (which injects the sidecar rules) and by easysidecar
// itself; the latter must never pull gorm/sqlite in, so this module carries no
// dependencies.
//
// Layering:
//
//	protocol : wire semantics, selected by target.Protocol
//	target   : upstream identity + routing + path mapping (this package)
//	storage  : Keyed by (protocol, repository, version). A mirror target
//	           shares the protocol's namespace (same coordinates dedupe by
//	           digest); an identity target's host is part of the repository.
package targets

import (
	"net"
	"strings"
)

// MountBase is the HTTP prefix every non-OCI protocol is mounted under.
// OCI is spec-fixed at /v2 and is not mounted here.
const MountBase = "/artifacts"

// Kind distinguishes how a target's host participates in content identity.
type Kind string

const (
	// Mirror serves content for an existing protocol whose coordinates are
	// already global (maven coordinates, npm package names). Several mirrors
	// of the same protocol share storage: the same (repo, version) pulled
	// through Central and Google Maven is one row and one CAS blob.
	Mirror Kind = "mirror"
	// Identity means the upstream host IS part of the content's identity:
	// ghcr.io/acme/app and docker.io/acme/app are different images, and so are
	// the same repo path on two different git hosts. The host becomes the
	// repository namespace.
	Identity Kind = "identity"
)

// AuthMode selects how a target authenticates to its upstream.
type AuthMode string

const (
	// AuthPassthrough forwards the client's own credentials to the upstream
	// (a private repository the client is already authorized for).
	AuthPassthrough AuthMode = "passthrough"
	// AuthBasic uses a stored username/password for the upstream.
	AuthBasic AuthMode = "basic"
	// AuthBearer uses a stored token, acquired or refreshed out of band.
	AuthBearer AuthMode = "bearer"
)

// Auth is a target's upstream credential policy. Secret is a reference
// (e.g. an env var name or secret key), never the credential itself.
type Auth struct {
	Mode     AuthMode `json:"mode,omitempty"`
	Username string   `json:"username,omitempty"`
	Secret   string   `json:"secret,omitempty"`
}

// Target is one upstream a protocol can pull from.
type Target struct {
	// ID is the stable target key, and the path segment under MountBase. The
	// protocol's default target reuses the protocol name ("maven"); a named
	// mirror is "<protocol>.<name>" ("maven.google").
	ID string `json:"id"`
	// Protocol selects the adapter. Always the leading component of ID for
	// named targets, but stored explicitly so a target can be re-pointed.
	Protocol string `json:"protocol"`
	// Kind decides storage sharing and whether Hosts are identity.
	Kind Kind `json:"kind"`
	// Base is the upstream root (scheme://host[/path]). Empty for an Identity
	// target whose base is derived from the request host (git, OCI).
	Base string `json:"base"`
	// Hosts are the hostname patterns a client may address this target by:
	// the upstream's own host plus any alternate mirrors of the same content.
	// They drive transparent (Host-header) routing, the SSRF allow-list and
	// the generated egress policy. Exact names, "*.suffix" wildcards, or bare
	// suffixes.
	Hosts []string `json:"hosts,omitempty"`
	// PathPrefix, when set, scopes this target to a path subtree on its Hosts.
	// It lets one host serve several targets: dl.google.com hosts both the
	// Android Maven repo (/dl/android/maven2) and the SDK repo
	// (/android/repository). The egress rule then matches only paths under the
	// prefix; any other path on the host falls through (to the catch-all).
	PathPrefix string `json:"path_prefix,omitempty"`
	// DirectHosts are hostname patterns the egress policy must leave DIRECT
	// even though they belong to this target's ecosystem: CDN endpoints the
	// adapter reaches server-side (a container registry's blob CDN), which the
	// intercepted client must never be handed.
	DirectHosts []string `json:"direct_hosts,omitempty"`
	// Aux maps a sub-endpoint name to its own base URL, for endpoints that
	// live on a different host than the primary upstream (crates.io's index
	// and static downloads). Aux entries are never mounted, but their hosts
	// are part of the SSRF allow-list.
	Aux map[string]string `json:"aux,omitempty"`
	// ExtraStrips are additional client-path prefixes that map onto this
	// target's mount, beyond the one derived from Base. A mirror that serves
	// the same content under several prefixes lists them here (Spring's
	// /release in addition to its base path /milestone).
	ExtraStrips []string `json:"extra_strips,omitempty"`
	// Outbound, when set, is the origin emitted in raw self-URL mode (the
	// upstream shape an intercepting sidecar maps back). Defaults to Base.
	Outbound string `json:"outbound,omitempty"`
	// Proxy is a per-target proxy policy; "" means derive from the protocol.
	Proxy string `json:"proxy,omitempty"`
	// Share overrides Kind's default storage sharing. Nil for built-ins
	// (Kind decides); user-declared targets default to isolated.
	Share *bool `json:"share,omitempty"`
	// Auth is the upstream credential policy (empty = anonymous).
	Auth Auth `json:"auth,omitempty"`
	// Builtin marks a target shipped in the binary (not user-declared).
	Builtin bool `json:"builtin,omitempty"`
}

// Shared reports whether this target's content is stored in the protocol's
// shared namespace (true) or isolated under the target (false).
func (t Target) Shared() bool {
	if t.Share != nil {
		return *t.Share
	}
	return t.Kind != Identity
}

// Mount is the path prefix this target is served under: /artifacts/<ID>.
func (t Target) Mount() string { return MountBase + "/" + t.ID }

// EgressAdd is the mount the sidecar prepends after stripping. It is the
// PROTOCOL's default mount, not the target's own: a named mirror is reached
// host-driven on the default mount and resolved to the target by its Host.
// OCI returns "" because it is spec-fixed at /v2 and routes by Host alone.
func (t Target) EgressAdd() string {
	if t.Protocol == "oci" {
		return ""
	}
	return MountBase + "/" + t.Protocol
}

// BasePath returns the path component of Base ("/maven2" for
// https://repo.maven.apache.org/maven2), or "" when Base is host-only.
func (t Target) BasePath() string {
	return pathOf(t.Base)
}

// InboundStrips returns the client-path prefixes that belong to the upstream
// ORIGIN and must be removed before the adapter mount sees the path. The
// upstream's own base path comes first (a mirror at host/prefix is dialed as
// host/prefix by its clients), then any ExtraStrips.
func (t Target) InboundStrips() []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.Trim(strings.TrimSpace(s), "/")
		if s == "" {
			return
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, "/"+s)
		}
	}
	add(t.BasePath())
	for _, s := range t.ExtraStrips {
		add(s)
	}
	return out
}

// Origin returns the raw-mode origin (the upstream shape): Outbound when set,
// else Base.
func (t Target) Origin() string {
	if t.Outbound != "" {
		return t.Outbound
	}
	return t.Base
}

// HostOnly reports whether the target has no fixed base (its upstream is the
// request host itself), which is what makes git/OCI targets dynamic.
func (t Target) HostOnly() bool { return t.Base == "" }

// SplitID returns the protocol prefix and the remaining name of a dotted
// target id: "maven.google" -> ("maven", "google"), "maven" -> ("maven", "").
func SplitID(id string) (proto, name string) {
	if i := strings.IndexByte(id, '.'); i >= 0 {
		return id[:i], id[i+1:]
	}
	return id, ""
}

// pathOf extracts the path component of a URL ("/maven2"), or "".
func pathOf(raw string) string {
	rest := raw
	if i := strings.Index(raw, "://"); i >= 0 {
		rest = raw[i+3:]
	}
	i := strings.IndexAny(rest, "/?#")
	if i < 0 {
		return ""
	}
	p := rest[i:]
	if j := strings.IndexAny(p, "?#"); j >= 0 {
		p = p[:j]
	}
	return strings.Trim(p, "/")
}

// IsPublicHost reports whether host is a public DNS name: dotted, not an IP
// literal, not localhost, not a cluster-local name. The egress catch-all uses
// it so "cache everything" never captures cluster-internal traffic (the API
// server, DNS, sibling services) — the SSRF guard for the catch-all rule.
func IsPublicHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return false
	}
	if hostOnly, _, err := net.SplitHostPort(h); err == nil {
		h = hostOnly
	}
	h = strings.Trim(h, "[]")
	if h == "" || h == "localhost" {
		return false
	}
	if ip := net.ParseIP(h); ip != nil {
		return false
	}
	switch {
	case strings.HasSuffix(h, ".local"),
		strings.HasSuffix(h, ".svc"),
		strings.HasSuffix(h, ".cluster.local"),
		strings.HasSuffix(h, ".internal"):
		return false
	}
	return strings.Contains(h, ".")
}
