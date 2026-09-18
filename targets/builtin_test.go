package targets

import "testing"

// legacyDefaults is the artifact server's historical upstream table
// (cmd/main.go defaultUpstreams). The target refactor must reproduce it
// exactly, or a deployment would silently change where it fetches from.
var legacyDefaults = map[string]string{
	"oci":      "https://registry-1.docker.io",
	"cargo":    "https://crates.io",
	"composer": "https://repo.packagist.org",
	"conan":    "https://center.conan.io",
	"go":       "https://proxy.golang.org",
	"helm":     "https://charts.helm.sh/stable",
	"hex":      "https://repo.hex.pm",
	"maven":    "https://repo.maven.apache.org/maven2",
	"npm":      "https://registry.npmjs.org",
	"nuget":    "https://api.nuget.org",
	"pub":      "https://pub.dev",
	"pypi":     "https://pypi.org",
	"rubygems": "https://rubygems.org",
	"swift":    "https://api.spm.swift.org",

	"apk":    "https://dl-cdn.alpinelinux.org",
	"debian": "https://deb.debian.org",
	"rpm":    "https://dl.fedoraproject.org",

	"conda":       "https://repo.anaconda.com",
	"huggingface": "https://huggingface.co",

	"hackage":  "https://hackage.haskell.org",
	"cran":     "https://cran.r-project.org",
	"cpan":     "https://cpan.metacpan.org",
	"luarocks": "https://luarocks.org",
	"juliapkg": "https://pkg.julialang.org",
	"nix":      "https://cache.nixos.org",
	"protobuf": "https://buf.build",
	"gitlfs":   "",

	"jsr":      "https://jsr.io",
	"opam":     "https://opam.ocaml.org",
	"stackage": "https://stackage.org",
	"pecl":     "https://pecl.php.net",
	"bazel":    "https://bcr.bazel.build",
	"jenkins":  "https://updates.jenkins.io",

	"maven.google":  "https://dl.google.com/dl/android/maven2",
	"maven.gradle":  "https://plugins.gradle.org/m2",
	"maven.clojars": "https://repo.clojars.org",
	"maven.spring":  "https://repo.spring.io/milestone",
	"maven.jitpack": "https://jitpack.io",
	"npm.jsr":       "https://npm.jsr.io",

	"cargo.index":        "https://index.crates.io",
	"cargo.static":       "https://static.crates.io/crates",
	"composer.search":    "https://packagist.org",
	"conan.center":       "https://center2.conan.io",
	"go.sumdb":           "https://sum.golang.org",
	"nuget.search":       "https://azuresearch-usnc.nuget.org",
	"nuget.registration": "https://api.nuget.org",
	"hex.repo":           "https://repo.hex.pm",
	"hex.api":            "https://hex.pm",
	"rubygems.index":     "https://index.rubygems.org",
	"rubygems.gems":      "https://rubygems.org/gems",
}

func TestBuiltinsMatchLegacyDefaults(t *testing.T) {
	r := NewRegistry()
	got := map[string]string{}
	for _, tt := range r.List() {
		if tt.ID == "git" {
			// git has no historical Defaults entry (it is host-driven); it
			// exists only to make the upstream allow-list explicit.
			continue
		}
		got[tt.ID] = tt.Base
		for name, base := range tt.Aux {
			got[tt.ID+"."+name] = base
		}
	}
	for k, want := range legacyDefaults {
		if k == "gitlfs" {
			continue // gitlfs is host-identity: Base is "" by design
		}
		if got[k] != want {
			t.Errorf("target %q base = %q, want %q", k, got[k], want)
		}
	}
	for k := range got {
		if _, ok := legacyDefaults[k]; !ok {
			t.Errorf("target %q has no legacy counterpart", k)
		}
	}
}

func TestDefaultTargetIDEqualsProtocol(t *testing.T) {
	r := NewRegistry()
	for _, tt := range r.List() {
		proto, name := SplitID(tt.ID)
		if proto != tt.Protocol {
			t.Errorf("target %q: protocol prefix %q != Protocol %q", tt.ID, proto, tt.Protocol)
		}
		if name == "" && tt.Protocol == "" {
			t.Errorf("target %q has empty protocol", tt.ID)
		}
	}
}

func TestSharedSemantics(t *testing.T) {
	r := NewRegistry()
	cases := map[string]bool{
		"maven":        true,
		"maven.google": true,
		"npm.jsr":      true,
		"oci":          false,
		"git":          false,
		"gitlfs":       false,
	}
	for id, want := range cases {
		tt, ok := r.Get(id)
		if !ok {
			t.Fatalf("missing target %q", id)
		}
		if tt.Shared() != want {
			t.Errorf("%s.Shared() = %v, want %v (kind=%s)", id, tt.Shared(), want, tt.Kind)
		}
	}
}

func TestAllowedSourceHosts(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Get("git"); !ok {
		t.Fatal("git target missing")
	}
	hosts := r.KnownHosts()
	for _, h := range []string{"dl.google.com", "plugins.gradle.org", "repo.maven.apache.org", "index.crates.io", "cdn-lfs.huggingface.co"} {
		if _, ok := hosts[h]; !ok {
			t.Errorf("host %q not allow-listed", h)
		}
	}
}

func TestUserTargetDefaultsIsolated(t *testing.T) {
	r := NewRegistry()
	r.Put(Target{ID: "maven.corp", Protocol: "maven", Base: "https://nexus.corp/repository/maven-public"})
	tt, ok := r.Get("maven.corp")
	if !ok {
		t.Fatal("user target missing")
	}
	if tt.Shared() {
		t.Error("user-declared target must default to isolated storage")
	}
	if tt.Builtin {
		t.Error("user target marked builtin")
	}
	share := true
	r.Put(Target{ID: "maven.corp2", Protocol: "maven", Base: "https://x", Share: &share})
	if t2, _ := r.Get("maven.corp2"); !t2.Shared() {
		t.Error("explicit Share=true ignored")
	}
}
