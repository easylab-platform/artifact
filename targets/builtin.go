package targets

// Builtins returns the compile-time target table: the public upstream for every
// protocol plus the named mirrors (Maven-layout and JSR's npm-compatibility
// registry) and the host-identity targets (OCI, git, git-lfs).
//
// This is the single source formerly duplicated between artifact's
// defaultUpstreams (cmd/main.go), easylab's tables and easysidecar's default
// rules. Base values must stay byte-identical to artifact's historical table;
// TestBuiltinsMatchLegacyDefaults enforces it.
//
// A protocol's default target reuses the protocol name as its ID, so its mount
// (/artifacts/<protocol>) is unchanged by the target refactor. Named mirrors
// carry a dotted ID (/artifacts/<protocol>.<name>). Sub-endpoints that live on
// a different host than the primary upstream (crates.io -> index.crates.io) are
// Aux entries of the same target, not targets of their own: they are never
// mounted. Hosts carries the upstream's own host plus any alternate hosts of
// the same content (the distro security archive, an org mirror); the egress
// policy is derived from it.
//
// Order matters: the egress policy is emitted in declaration order and the
// sidecar is first-match-wins, so a target whose host could be shadowed by a
// broader pattern must be declared first (npm.jsr.io before jsr.io).
func Builtins() []Target {
	return []Target{
		{
			ID: "oci", Protocol: "oci", Kind: Identity,
			Base: "https://registry-1.docker.io",
			Hosts: []string{
				"registry-1.docker.io", "docker.io", "index.docker.io",
				"ghcr.io", "quay.io", "gcr.io", "registry.k8s.io",
				"mcr.microsoft.com", "public.ecr.aws", "nvcr.io",
			},
			// Container registries 307-redirect layer downloads to a CDN; the
			// adapter follows the redirect server-side, so the intercepted
			// client must never be handed the CDN host.
			DirectHosts: []string{"production.cloudflare.docker.com", "*.cloudflarestorage.com"},
			Builtin:     true,
		},
		{
			ID: "cargo", Protocol: "cargo", Kind: Mirror,
			Base: "https://crates.io",
			Hosts: []string{
				"crates.io", "index.crates.io", "static.crates.io", "crates.io",
			},
			Aux: map[string]string{
				"index":  "https://index.crates.io",
				"static": "https://static.crates.io/crates",
			},
			Builtin: true,
		},
		{
			ID: "composer", Protocol: "composer", Kind: Mirror,
			Base:    "https://repo.packagist.org",
			Hosts:   []string{"repo.packagist.org", "packagist.org"},
			Aux:     map[string]string{"search": "https://packagist.org"},
			Builtin: true,
		},
		{
			ID: "conan", Protocol: "conan", Kind: Mirror,
			Base:    "https://center.conan.io",
			Hosts:   []string{"center.conan.io", "center2.conan.io"},
			Aux:     map[string]string{"center": "https://center2.conan.io"},
			Builtin: true,
		},
		{
			ID: "go", Protocol: "go", Kind: Mirror,
			Base:    "https://proxy.golang.org",
			Hosts:   []string{"proxy.golang.org", "sum.golang.org"},
			Aux:     map[string]string{"sumdb": "https://sum.golang.org"},
			Builtin: true,
		},
		{
			ID: "helm", Protocol: "helm", Kind: Mirror,
			Base: "https://charts.helm.sh/stable", Hosts: []string{"charts.helm.sh"}, Builtin: true,
		},
		{
			ID: "hex", Protocol: "hex", Kind: Mirror,
			Base:  "https://repo.hex.pm",
			Hosts: []string{"repo.hex.pm", "hex.pm", "api.hex.pm"},
			Aux: map[string]string{
				"repo": "https://repo.hex.pm",
				"api":  "https://hex.pm",
			},
			Builtin: true,
		},
		{
			ID: "maven", Protocol: "maven", Kind: Mirror,
			Base: "https://repo.maven.apache.org/maven2", Hosts: []string{"repo.maven.apache.org"}, Builtin: true,
		},
		{
			ID: "npm", Protocol: "npm", Kind: Mirror,
			Base:    "https://registry.npmjs.org",
			Hosts:   []string{"registry.npmjs.org", "*.npmjs.org"},
			Aux:     map[string]string{"jsr": "https://npm.jsr.io"},
			Builtin: true,
		},
		{
			ID: "nuget", Protocol: "nuget", Kind: Mirror,
			Base:  "https://api.nuget.org",
			Hosts: []string{"api.nuget.org", "azuresearch-usnc.nuget.org"},
			Aux: map[string]string{
				"search":       "https://azuresearch-usnc.nuget.org",
				"registration": "https://api.nuget.org",
			},
			Builtin: true,
		},
		{ID: "pub", Protocol: "pub", Kind: Mirror, Base: "https://pub.dev", Hosts: []string{"pub.dev"}, Builtin: true},
		{ID: "pypi", Protocol: "pypi", Kind: Mirror, Base: "https://pypi.org",
			Hosts: []string{"pypi.org", "files.pythonhosted.org"}, Builtin: true},
		{
			ID: "rubygems", Protocol: "rubygems", Kind: Mirror,
			Base:  "https://rubygems.org",
			Hosts: []string{"rubygems.org", "index.rubygems.org"},
			Aux: map[string]string{
				"index": "https://index.rubygems.org",
				"gems":  "https://rubygems.org/gems",
			},
			Builtin: true,
		},
		{ID: "swift", Protocol: "swift", Kind: Mirror, Base: "https://api.spm.swift.org",
			Hosts: []string{"api.spm.swift.org"}, Builtin: true},

		// System package repositories.
		{ID: "apk", Protocol: "apk", Kind: Mirror, Base: "https://dl-cdn.alpinelinux.org",
			Hosts: []string{"dl-cdn.alpinelinux.org"}, Builtin: true},
		{
			ID: "debian", Protocol: "debian", Kind: Mirror,
			Base:    "https://deb.debian.org",
			Hosts:   []string{"deb.debian.org", "security.debian.org", "archive.ubuntu.com", "security.ubuntu.com"},
			Builtin: true,
		},
		{
			ID: "rpm", Protocol: "rpm", Kind: Mirror,
			Base:    "https://dl.fedoraproject.org",
			Hosts:   []string{"dl.fedoraproject.org", "*.elrepo.org", "mirror.stream.centos.org"},
			Builtin: true,
		},

		// AI/ML, functional, schema registries.
		{ID: "conda", Protocol: "conda", Kind: Mirror, Base: "https://repo.anaconda.com",
			Hosts: []string{"repo.anaconda.com", "conda.anaconda.org"}, Builtin: true},
		{ID: "huggingface", Protocol: "huggingface", Kind: Mirror, Base: "https://huggingface.co",
			Hosts: []string{"huggingface.co", "*.huggingface.co", "cdn-lfs.huggingface.co"}, Builtin: true},
		{ID: "nix", Protocol: "nix", Kind: Mirror, Base: "https://cache.nixos.org",
			Hosts: []string{"cache.nixos.org"}, Builtin: true},
		{ID: "protobuf", Protocol: "protobuf", Kind: Mirror, Base: "https://buf.build",
			Hosts: []string{"buf.build"}, Builtin: true},

		// Plain-HTTP package trees (no protocol of their own).
		{ID: "hackage", Protocol: "hackage", Kind: Mirror, Base: "https://hackage.haskell.org",
			Hosts: []string{"hackage.haskell.org"}, Builtin: true},
		{ID: "cran", Protocol: "cran", Kind: Mirror, Base: "https://cran.r-project.org",
			Hosts: []string{"cran.r-project.org"}, Builtin: true},
		{ID: "cpan", Protocol: "cpan", Kind: Mirror, Base: "https://cpan.metacpan.org",
			Hosts: []string{"cpan.metacpan.org"}, Builtin: true},
		{ID: "luarocks", Protocol: "luarocks", Kind: Mirror, Base: "https://luarocks.org",
			Hosts: []string{"luarocks.org"}, Builtin: true},
		{ID: "juliapkg", Protocol: "juliapkg", Kind: Mirror, Base: "https://pkg.julialang.org",
			Hosts: []string{"pkg.julialang.org", "*.pkg.julialang.org"}, Builtin: true},
		{ID: "opam", Protocol: "opam", Kind: Mirror, Base: "https://opam.ocaml.org",
			Hosts: []string{"opam.ocaml.org"}, Builtin: true},
		{ID: "stackage", Protocol: "stackage", Kind: Mirror, Base: "https://stackage.org",
			Hosts: []string{"stackage.org"}, Builtin: true},
		{ID: "pecl", Protocol: "pecl", Kind: Mirror, Base: "https://pecl.php.net",
			Hosts: []string{"pecl.php.net"}, Builtin: true},
		{ID: "bazel", Protocol: "bazel", Kind: Mirror, Base: "https://bcr.bazel.build",
			Hosts: []string{"bcr.bazel.build"}, Builtin: true},
		{ID: "jenkins", Protocol: "jenkins", Kind: Mirror, Base: "https://updates.jenkins.io",
			Hosts: []string{"updates.jenkins.io"}, Builtin: true},

		// Source mirrors: git smart-HTTP and Ivy repositories.
		{ID: "git", Protocol: "git", Kind: Identity,
			Hosts: []string{"github.com", "codeload.github.com"}, Builtin: true},
		{
			ID: "ivy", Protocol: "ivy", Kind: Identity,
			Hosts: []string{"repo.scala-sbt.org", "scala.jfrog.io"},
			// Ivy addresses servers by host in the path, so its Base is the
			// request host; these hosts only scope the allow-list and egress.
			Builtin: true,
		},
		{ID: "gitlfs", Protocol: "gitlfs", Kind: Identity, Hosts: []string{"github.com"}, Builtin: true},

		// JSR's npm-compatibility registry: same coordinates as npm, so it
		// shares the npm namespace. Declared before jsr so npm.jsr.io is not
		// shadowed by jsr's bare-suffix match (the sidecar is first-match-wins).
		{ID: "npm.jsr", Protocol: "npm", Kind: Mirror, Base: "https://npm.jsr.io",
			Hosts: []string{"npm.jsr.io"}, Builtin: true},
		{ID: "jsr", Protocol: "jsr", Kind: Mirror, Base: "https://jsr.io",
			Hosts: []string{"jsr.io"}, Builtin: true},

		// Maven-layout mirrors. Each publishes the same coordinates under a
		// host path prefix; the prefix is derived from Base (and any
		// ExtraStrips) so the inbound strip and the outbound base cannot drift.
		// They share the maven namespace, so a coordinate fetched through any
		// of them dedupes by digest.
		{ID: "maven.google", Protocol: "maven", Kind: Mirror,
			Base: "https://dl.google.com/dl/android/maven2", Hosts: []string{"dl.google.com"}, Builtin: true},
		{ID: "maven.gradle", Protocol: "maven", Kind: Mirror,
			Base: "https://plugins.gradle.org/m2", Hosts: []string{"plugins.gradle.org"}, Builtin: true},
		{ID: "maven.clojars", Protocol: "maven", Kind: Mirror,
			Base: "https://repo.clojars.org", Hosts: []string{"repo.clojars.org"}, Builtin: true},
		{ID: "maven.spring", Protocol: "maven", Kind: Mirror,
			Base: "https://repo.spring.io/milestone", Hosts: []string{"repo.spring.io"},
			// Spring serves the same content under /release and /milestone;
			// both map onto the target's mount. /release requires auth
			// upstream, so the base (where we FETCH) is the public /milestone.
			ExtraStrips: []string{"/release"}, Builtin: true},
		{ID: "maven.jitpack", Protocol: "maven", Kind: Mirror,
			Base: "https://jitpack.io", Hosts: []string{"jitpack.io"}, Builtin: true},
	}
}
