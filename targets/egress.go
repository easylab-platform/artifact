package targets

import (
	"fmt"
	"strings"
)

// Egress is one sidecar rewrite in the default policy, DERIVED from a target
// (or a target's DirectHosts). It is never hand-written: EgressPolicy builds it
// from Builtins so the inbound path mapping and the outbound base cannot drift.
type Egress struct {
	// Match are hostname patterns (exact / *.suffix / bare suffix). First
	// match wins, so order across the policy matters.
	Match []string
	// Strips are client-path prefixes removed before Add is applied. Several
	// when the mirror serves the same content under multiple prefixes.
	Strips []string
	// Add is the prefix prepended after Strips (the protocol mount). Empty for
	// OCI, which is spec-fixed at /v2 and routes by Host alone.
	Add string
	// PathPrefix scopes the rule to a path subtree on its Match hosts, so one
	// host can serve several targets (see Target.PathPrefix).
	PathPrefix string
	// Direct keeps the hosts out of the gateway (action: direct). Used for CDN
	// hosts the adapter reaches server-side.
	Direct bool
}

// CatchAllPattern is the match pattern of the final rule in the policy. It
// means "any PUBLIC hostname" (the sidecar applies the public-host guard), so
// every other host is cached through netcache unless an earlier rule handled
// it. It is explicitly in the rule list rather than the `default:` action so it
// is visible in the ConfigMap and overridable, and so the guard cannot be
// forgotten by a bare `default: rewrite`.
const CatchAllPattern = "*"

// CatchAllAdd is the mount the catch-all steers unmatched public hosts into.
const CatchAllAdd = MountBase + "/netcache"

// EgressPolicy derives the ordered default egress table from the target
// registry: for each target, its not-yet-claimed Hosts become one rewrite rule
// whose strips come from the target's Base path (+ ExtraStrips) and whose add
// is the protocol mount; its DirectHosts become direct rules. A final catch-all
// rule routes every other public host through netcache, so an arbitrary
// external asset is cached the first time it is fetched.
//
// This is the SINGLE source formerly duplicated between easylab's k8s client
// (defaultUpstreams) and easysidecar's rule/defaultrules.go. Deriving it is the
// point: a host is never listed here by hand, so it cannot drift from the
// upstream it belongs to.
//
// First match wins, so declaration order in Builtins() is the precedence: an
// alias whose host would also match a later, broader pattern (npm.jsr.io vs
// jsr.io) must be declared first. TestEgressNoShadowing enforces it.
func EgressPolicy() []Egress {
	var out []Egress
	seen := map[string]bool{}
	for _, t := range Builtins() {
		// A path-scoped target must not CLAIM its host from later targets:
		// only its path subtree is its own, other paths fall through to a
		// later rule (the catch-all). So its hosts are emitted WITHOUT marking
		// them claimed.
		if t.PathPrefix != "" {
			if hosts := dedupe(t.Hosts); len(hosts) > 0 {
				out = append(out, Egress{
					Match:      hosts,
					Strips:     t.InboundStrips(),
					Add:        t.EgressAdd(),
					PathPrefix: t.PathPrefix,
				})
			}
			continue
		}
		hosts := unclaimed(seen, t.Hosts)
		if len(hosts) > 0 {
			out = append(out, Egress{
				Match:  hosts,
				Strips: t.InboundStrips(),
				Add:    t.EgressAdd(),
			})
		}
		if direct := unclaimed(seen, t.DirectHosts); len(direct) > 0 {
			out = append(out, Egress{Match: direct, Direct: true})
		}
	}
	// Catch-all: any remaining PUBLIC host is cached through netcache. It must
	// be last (first-match-wins) and carries no strip: netcache keys on the
	// full client path + query.
	out = append(out, Egress{Match: []string{CatchAllPattern}, Add: CatchAllAdd})
	return out
}

// unclaimed filters hosts, marking each as claimed so it is emitted once.
func unclaimed(seen map[string]bool, hosts []string) []string {
	var out []string
	for _, h := range hosts {
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	return out
}

// dedupe returns hosts with duplicates and empties removed, WITHOUT claiming
// them (used by path-scoped targets, whose host may still be claimed by a
// whole-host target).
func dedupe(hosts []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, h := range hosts {
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	return out
}

// RenderEgressYAML renders the default egress policy as a sidecar rules.yaml
// with the given gateway target ("host:port"). header is an optional leading
// comment block.
func RenderEgressYAML(gatewayHostPort string, mitmDefault bool, header string) string {
	var b strings.Builder
	if header != "" {
		for _, line := range strings.Split(strings.TrimRight(header, "\n"), "\n") {
			b.WriteString("# " + line + "\n")
		}
	}
	b.WriteString("rules:\n")
	for _, u := range EgressPolicy() {
		b.WriteString("  - match: [" + quoteList(u.Match) + "]\n")
		if u.Direct {
			b.WriteString("    action: direct\n")
			continue
		}
		b.WriteString("    action: rewrite\n")
		b.WriteString("    target: \"" + gatewayHostPort + "\"\n")
		if u.PathPrefix != "" {
			b.WriteString("    path_prefix: \"" + u.PathPrefix + "\"\n")
		}
		switch len(u.Strips) {
		case 0:
		case 1:
			b.WriteString("    strip_prefix: \"" + u.Strips[0] + "\"\n")
		default:
			b.WriteString("    strip_prefixes: [" + quoteList(u.Strips) + "]\n")
		}
		if u.Add != "" {
			b.WriteString("    add_prefix: \"" + u.Add + "\"\n")
		}
	}
	b.WriteString("default: direct\n")
	if mitmDefault {
		b.WriteString("mitm_default: true\n")
	} else {
		b.WriteString("mitm_default: false\n")
	}
	return b.String()
}

func quoteList(items []string) string {
	quoted := make([]string, 0, len(items))
	for _, i := range items {
		quoted = append(quoted, fmt.Sprintf("%q", i))
	}
	return strings.Join(quoted, ", ")
}

// matchHost mirrors easysidecar's rule.MatchHost (exact / *.suffix / bare
// suffix) so both layers agree on pattern semantics. "*" matches any PUBLIC
// hostname (the SSRF guard is applied by the sidecar's MatchPublic).
func matchHost(pattern, host string) bool {
	if pattern == CatchAllPattern {
		return IsPublicHost(host)
	}
	pattern = strings.ToLower(strings.TrimSuffix(pattern, "."))
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if pattern == host {
		return true
	}
	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[1:]
		return strings.HasSuffix(host, suffix) && len(host) > len(suffix)
	}
	return strings.HasSuffix(host, "."+pattern)
}

// EgressCovers reports whether a hostname is covered by the default egress
// table (used by tests and diagnostics).
func EgressCovers(host string) bool {
	for _, u := range EgressPolicy() {
		for _, m := range u.Match {
			if matchHost(m, host) {
				return true
			}
		}
	}
	return false
}
