package targets

import (
	"fmt"
	"strings"
)

// Egress is one hostname set steered into the gateway by the default sidecar
// policy, with the path transform that maps the client's shape onto the
// gateway. It is the renderable form of the egress table.
type Egress struct {
	// Match are hostname patterns (exact / *.suffix / bare suffix). First
	// match wins, so order matters when sets overlap.
	Match []string
	// Strip is a leading path prefix removed before Add is applied (a mirror
	// that publishes the repo under a host path prefix).
	Strip string
	// Add is the prefix prepended after Strip. It is set only for clients
	// that address a mirror/tree by bare hostname and therefore cannot carry
	// the gateway mount themselves; the protocol clients that are configured
	// with an explicit registry URL omit it. The relay always preserves the
	// upstream Host either way.
	Add string
}

// EgressPolicy is the ordered default egress table. This is the SINGLE source
// formerly duplicated between easylab's k8s client (defaultUpstreams) and
// easysidecar's rule/defaultrules.go, which had to be kept in sync by hand.
//
// It is explicit rather than derived from Builtins() on purpose: the Add
// column encodes the per-ecosystem routing decision (see Egress.Add), and the
// table also carries hosts that are not targets at all (alternate distro
// mirrors, CDN carve-outs). Deriving it would hide both.
func EgressPolicy() []Egress {
	return []Egress{
		// OCI / containers. The public registries a client reaches by name;
		// each is preserved as the Host (and therefore the repository
		// namespace) by the rewrite relay, so ghcr.io/acme/app and
		// docker.io/acme/app stay distinct.
		{Match: []string{"registry-1.docker.io", "docker.io", "index.docker.io"}},
		{Match: []string{"ghcr.io", "quay.io", "gcr.io", "registry.k8s.io",
			"mcr.microsoft.com", "public.ecr.aws", "nvcr.io"}},
		// Container blob CDNs: registries 307-redirect layer downloads here and
		// artifact follows the redirect itself, so they must stay direct.
		{Match: []string{"production.cloudflare.docker.com", "*.cloudflarestorage.com"}},

		// Language registries.
		{Match: []string{"registry.npmjs.org", "*.npmjs.org"}},
		// JSR's npm-compatibility registry (deno/bun/npm resolve @jsr/* here).
		{Match: []string{"npm.jsr.io"}, Add: MountBase + "/npm"},
		{Match: []string{"pypi.org", "files.pythonhosted.org"}},
		{Match: []string{"proxy.golang.org", "sum.golang.org"}},
		{Match: []string{"crates.io", "index.crates.io", "static.crates.io"}},
		{Match: []string{"repo.maven.apache.org"}},
		// Maven-layout mirrors: the maven adapter serves them host-driven, so
		// the mirror's path prefix is stripped before the mount.
		//
		// NOTE: spring strips /release (what easylab/easysidecar shipped),
		// while the maven.spring target's Base is repo.spring.io/milestone
		// (public; /release requires auth). The strip is the prefix the CLIENT
		// dialed, the Base is where we FETCH; they are different layers and are
		// allowed to differ. Reconciling them is a separate decision.
		{Match: []string{"dl.google.com"}, Strip: "/dl/android/maven2", Add: MountBase + "/maven"},
		{Match: []string{"plugins.gradle.org"}, Strip: "/m2", Add: MountBase + "/maven"},
		{Match: []string{"repo.clojars.org"}, Add: MountBase + "/maven"},
		{Match: []string{"repo.spring.io"}, Strip: "/release", Add: MountBase + "/maven"},
		{Match: []string{"jitpack.io"}, Add: MountBase + "/maven"},
		{Match: []string{"api.nuget.org", "azuresearch-usnc.nuget.org"}},
		{Match: []string{"rubygems.org", "index.rubygems.org"}},
		{Match: []string{"repo.packagist.org"}},
		{Match: []string{"repo.hex.pm"}},
		// hex.pm is the API host: publish, search and `mix hex.user auth`.
		{Match: []string{"hex.pm", "api.hex.pm"}},
		{Match: []string{"pub.dev"}},
		{Match: []string{"charts.helm.sh"}},
		{Match: []string{"center.conan.io", "center2.conan.io"}},
		{Match: []string{"api.spm.swift.org"}},

		// System packages.
		{Match: []string{"dl-cdn.alpinelinux.org"}},
		{Match: []string{"deb.debian.org", "security.debian.org"}},
		{Match: []string{"archive.ubuntu.com", "security.ubuntu.com"}},
		{Match: []string{"*.elrepo.org", "mirror.stream.centos.org", "dl.fedoraproject.org"}},

		// AI/ML.
		{Match: []string{"huggingface.co", "*.huggingface.co", "cdn-lfs.huggingface.co"}},
		{Match: []string{"repo.anaconda.com", "conda.anaconda.org"}},

		// Nix binary cache.
		{Match: []string{"cache.nixos.org"}},

		// Source mirrors: git smart-HTTP and Ivy repositories.
		{Match: []string{"github.com", "codeload.github.com"}},
		{Match: []string{"repo.scala-sbt.org", "scala.jfrog.io"}},

		// Plain-HTTP package trees (Haskell, R, Perl, Lua) + Julia's pkg server.
		{Match: []string{"hackage.haskell.org"}},
		{Match: []string{"cran.r-project.org"}},
		{Match: []string{"cpan.metacpan.org"}},
		{Match: []string{"luarocks.org"}},
		{Match: []string{"pkg.julialang.org", "*.pkg.julialang.org"}},

		// Additional plain-HTTP trees (JSR native, opam, Stackage, PECL, Bazel
		// BCR, Jenkins update center).
		{Match: []string{"jsr.io"}, Add: MountBase + "/jsr"},
		{Match: []string{"opam.ocaml.org"}, Add: MountBase + "/opam"},
		{Match: []string{"stackage.org"}, Add: MountBase + "/stackage"},
		{Match: []string{"pecl.php.net"}, Add: MountBase + "/pecl"},
		{Match: []string{"bcr.bazel.build"}, Add: MountBase + "/bazel"},
		{Match: []string{"updates.jenkins.io"}, Add: MountBase + "/jenkins"},
	}
}

// RenderEgressYAML renders the default egress policy as a sidecar rules.yaml
// with the given gateway target ("host:port"). header is an optional leading
// comment block (already prefixed with "# " per line).
//
// The shape matches what easylab and easysidecar have always emitted, so a
// caller can switch from its local table to this function with no routing
// change.
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
		b.WriteString("    action: rewrite\n")
		b.WriteString("    target: \"" + gatewayHostPort + "\"\n")
		if u.Strip != "" {
			b.WriteString("    strip_prefix: \"" + u.Strip + "\"\n")
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
// suffix) so both layers agree on pattern semantics.
func matchHost(pattern, host string) bool {
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
