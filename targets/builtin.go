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
// mounted.
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
			Builtin: true,
		},
		{
			ID: "cargo", Protocol: "cargo", Kind: Mirror,
			Base: "https://crates.io",
			Aux: map[string]string{
				"index":  "https://index.crates.io",
				"static": "https://static.crates.io/crates",
			},
			Builtin: true,
		},
		{
			ID: "composer", Protocol: "composer", Kind: Mirror,
			Base: "https://repo.packagist.org",
			Aux:  map[string]string{"search": "https://packagist.org"},
			Builtin: true,
		},
		{
			ID: "conan", Protocol: "conan", Kind: Mirror,
			Base: "https://center.conan.io",
			Aux:  map[string]string{"center": "https://center2.conan.io"},
			Builtin: true,
		},
		{
			ID: "go", Protocol: "go", Kind: Mirror,
			Base: "https://proxy.golang.org",
			Aux:  map[string]string{"sumdb": "https://sum.golang.org"},
			Builtin: true,
		},
		{ID: "helm", Protocol: "helm", Kind: Mirror, Base: "https://charts.helm.sh/stable", Builtin: true},
		{
			ID: "hex", Protocol: "hex", Kind: Mirror,
			Base: "https://repo.hex.pm",
			Aux: map[string]string{
				"repo": "https://repo.hex.pm",
				"api":  "https://hex.pm",
			},
			Builtin: true,
		},
		{ID: "maven", Protocol: "maven", Kind: Mirror, Base: "https://repo.maven.apache.org/maven2", Builtin: true},
		{
			ID: "npm", Protocol: "npm", Kind: Mirror,
			Base:  "https://registry.npmjs.org",
			Hosts: []string{"*.npmjs.org"},
			Aux:   map[string]string{"jsr": "https://npm.jsr.io"},
			Builtin: true,
		},
		{
			ID: "nuget", Protocol: "nuget", Kind: Mirror,
			Base: "https://api.nuget.org",
			Aux: map[string]string{
				"search":       "https://azuresearch-usnc.nuget.org",
				"registration": "https://api.nuget.org",
			},
			Builtin: true,
		},
		{ID: "pub", Protocol: "pub", Kind: Mirror, Base: "https://pub.dev", Builtin: true},
		{ID: "pypi", Protocol: "pypi", Kind: Mirror, Base: "https://pypi.org", Builtin: true},
		{
			ID: "rubygems", Protocol: "rubygems", Kind: Mirror,
			Base: "https://rubygems.org",
			Aux: map[string]string{
				"index": "https://index.rubygems.org",
				"gems":  "https://rubygems.org/gems",
			},
			Builtin: true,
		},
		{ID: "swift", Protocol: "swift", Kind: Mirror, Base: "https://api.spm.swift.org", Builtin: true},

		// System package repositories.
		{ID: "apk", Protocol: "apk", Kind: Mirror, Base: "https://dl-cdn.alpinelinux.org", Builtin: true},
		{ID: "debian", Protocol: "debian", Kind: Mirror, Base: "https://deb.debian.org", Builtin: true},
		{ID: "rpm", Protocol: "rpm", Kind: Mirror, Base: "https://dl.fedoraproject.org", Builtin: true},

		// AI/ML, functional, schema registries.
		{ID: "conda", Protocol: "conda", Kind: Mirror, Base: "https://repo.anaconda.com", Builtin: true},
		{ID: "huggingface", Protocol: "huggingface", Kind: Mirror, Base: "https://huggingface.co",
			Hosts: []string{"*.huggingface.co", "cdn-lfs.huggingface.co"}, Builtin: true},
		{ID: "nix", Protocol: "nix", Kind: Mirror, Base: "https://cache.nixos.org", Builtin: true},
		{ID: "protobuf", Protocol: "protobuf", Kind: Mirror, Base: "https://buf.build", Builtin: true},

		// Plain-HTTP package trees (no protocol of their own).
		{ID: "hackage", Protocol: "hackage", Kind: Mirror, Base: "https://hackage.haskell.org", Builtin: true},
		{ID: "cran", Protocol: "cran", Kind: Mirror, Base: "https://cran.r-project.org", Builtin: true},
		{ID: "cpan", Protocol: "cpan", Kind: Mirror, Base: "https://cpan.metacpan.org", Builtin: true},
		{ID: "luarocks", Protocol: "luarocks", Kind: Mirror, Base: "https://luarocks.org", Builtin: true},
		{ID: "juliapkg", Protocol: "juliapkg", Kind: Mirror, Base: "https://pkg.julialang.org", Builtin: true},
		{ID: "jsr", Protocol: "jsr", Kind: Mirror, Base: "https://jsr.io", Builtin: true},
		{ID: "opam", Protocol: "opam", Kind: Mirror, Base: "https://opam.ocaml.org", Builtin: true},
		{ID: "stackage", Protocol: "stackage", Kind: Mirror, Base: "https://stackage.org", Builtin: true},
		{ID: "pecl", Protocol: "pecl", Kind: Mirror, Base: "https://pecl.php.net", Builtin: true},
		{ID: "bazel", Protocol: "bazel", Kind: Mirror, Base: "https://bcr.bazel.build", Builtin: true},
		{ID: "jenkins", Protocol: "jenkins", Kind: Mirror, Base: "https://updates.jenkins.io", Builtin: true},

		// Host-identity source mirrors: the host is part of the content's
		// identity, so two hosts never share storage. Their Base is empty
		// because the upstream is the request host itself.
		{ID: "git", Protocol: "git", Kind: Identity, Hosts: []string{"github.com", "codeload.github.com"}, Builtin: true},
		{ID: "gitlfs", Protocol: "gitlfs", Kind: Identity, Hosts: []string{"github.com"}, Builtin: true},

		// Maven-layout mirrors. Each publishes the same coordinates under a
		// host path prefix; Strip removes it so the maven adapter sees the
		// native layout. They share the maven namespace, so a coordinate
		// fetched through any of them dedupes by digest.
		{
			ID: "maven.google", Protocol: "maven", Kind: Mirror,
			Base: "https://dl.google.com/dl/android/maven2", Strip: "/dl/android/maven2",
			Builtin: true,
		},
		{
			ID: "maven.gradle", Protocol: "maven", Kind: Mirror,
			Base: "https://plugins.gradle.org/m2", Strip: "/m2", Builtin: true,
		},
		{ID: "maven.clojars", Protocol: "maven", Kind: Mirror, Base: "https://repo.clojars.org", Builtin: true},
		{
			ID: "maven.spring", Protocol: "maven", Kind: Mirror,
			Base: "https://repo.spring.io/milestone", Strip: "/milestone", Builtin: true,
		},
		{ID: "maven.jitpack", Protocol: "maven", Kind: Mirror, Base: "https://jitpack.io", Builtin: true},

		// JSR's npm-compatibility registry: same coordinates as npm, so it
		// shares the npm namespace.
		{ID: "npm.jsr", Protocol: "npm", Kind: Mirror, Base: "https://npm.jsr.io", Builtin: true},
	}
}
