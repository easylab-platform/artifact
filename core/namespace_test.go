package artifactkit

import (
	"context"
	"testing"
)

func TestRepoKeyRoundTrip(t *testing.T) {
	cases := []struct{ ns, name, key string }{
		{"", "left-pad", "left-pad"},
		{"default", "left-pad", "left-pad"},
		{"@acme", "ui", "@acme/ui"},
		{"ghcr.io", "library/nginx", "ghcr.io/library/nginx"},
		{"org.slf4j", "slf4j-api", "org.slf4j/slf4j-api"},
	}
	for _, c := range cases {
		if got := RepoKey(c.ns, c.name); got != c.key {
			t.Errorf("RepoKey(%q,%q)=%q want %q", c.ns, c.name, got, c.key)
		}
	}
}

func TestScopeNamespace(t *testing.T) {
	cases := []struct{ path, ns, name, rest string }{
		{"@acme/ui", "@acme", "ui", ""},
		{"@acme/ui/-/ui-1.0.0.tgz", "@acme", "ui", "-/ui-1.0.0.tgz"},
		{"left-pad", "", "left-pad", ""},
		{"left-pad/-/left-pad-1.0.0.tgz", "", "left-pad", "-/left-pad-1.0.0.tgz"},
	}
	for _, c := range cases {
		ns, name, rest := ScopeNamespace("npm", c.path)
		if ns != c.ns || name != c.name || rest != c.rest {
			t.Errorf("ScopeNamespace(%q)=(%q,%q,%q) want (%q,%q,%q)",
				c.path, ns, name, rest, c.ns, c.name, c.rest)
		}
	}
}

func TestSplitExplicitRepo(t *testing.T) {
	repo, rest, ok := SplitExplicitRepo("-/@acme/ui")
	if !ok || repo != "@acme" || rest != "ui" {
		t.Fatalf("explicit: %q %q %v", repo, rest, ok)
	}
	repo, rest, ok = SplitExplicitRepo("@acme/ui")
	if ok || repo != "" || rest != "@acme/ui" {
		t.Fatalf("native: %q %q %v", repo, rest, ok)
	}
}

func TestRepoUpstreamLongestPrefix(t *testing.T) {
	u := &Upstreams{
		Defaults:  map[string]string{"maven": "https://repo.maven.apache.org/maven2"},
		Overrides: map[string]string{},
		Proxy:     map[string]string{},
	}
	u.SetRepo("maven", "org.apache", "https://apache.example/m2", "")
	u.SetRepo("maven", "org.apache.commons", "https://commons.example/m2", "http://p:1")

	if got := u.Repo("maven", "org.apache.commons.io"); got != "https://commons.example/m2" {
		t.Errorf("longest prefix: %q", got)
	}
	if got := u.Repo("maven", "org.apache.tomcat"); got != "https://apache.example/m2" {
		t.Errorf("shorter prefix: %q", got)
	}
	if got := u.Repo("maven", "com.google.guava"); got != "https://repo.maven.apache.org/maven2" {
		t.Errorf("default: %q", got)
	}
	if p, ok := u.RepoProxy("maven", "org.apache.commons.io"); !ok || p != "http://p:1" {
		t.Errorf("repo proxy: %q %v", p, ok)
	}
	// Boundary check: a different groupId sharing the textual prefix must not match.
	if got := u.Repo("maven", "org.apachex"); got != "https://repo.maven.apache.org/maven2" {
		t.Errorf("boundary: %q", got)
	}
}

func TestGoModuleNamespace(t *testing.T) {
	cases := []struct{ path, ns, name string }{
		{"github.com/acme/tool/@v/list", "github.com/acme", "github.com/acme/tool/@v/list"},
		{"golang.org/x/text/@latest", "golang.org/x", "golang.org/x/text/@latest"},
		{"rsc.io/quote", "rsc.io/quote", "rsc.io/quote"},
	}
	for _, c := range cases {
		ns, name, _ := GoModuleNamespace("go", c.path)
		if ns != c.ns || name != c.name {
			t.Errorf("%s -> (%q,%q) want (%q,%q)", c.path, ns, name, c.ns, c.name)
		}
	}
}

func TestOCIHostNamespace(t *testing.T) {
	cases := []struct{ path, ns, name string }{
		{"ghcr.io/acme/app/manifests/latest", "ghcr.io", "acme/app"},
		{"localhost:5000/acme/app/blobs/sha256:abc", "localhost:5000", "acme/app"},
		{"library/nginx/tags/list", "", "library/nginx"},
		{"acme/app/manifests/v1", "", "acme/app"},
	}
	for _, c := range cases {
		ns, name, _ := OCIHostNamespace("oci", c.path)
		if ns != c.ns || name != c.name {
			t.Errorf("%s -> (%q,%q) want (%q,%q)", c.path, ns, name, c.ns, c.name)
		}
	}
}

func TestScopedName(t *testing.T) {
	sc := RepoScope{Namespace: "org.slf4j"}
	if got := sc.ScopedName("slf4j-api"); got != "org.slf4j/slf4j-api" {
		t.Errorf("scoped: %q", got)
	}
	sc2 := RepoScope{Namespace: "@acme"}
	if got := sc2.ScopedName("@acme/ui"); got != "@acme/ui" {
		t.Errorf("idempotent: %q", got)
	}
	if got := sc2.UnscopedName("@acme/ui"); got != "ui" {
		t.Errorf("unscoped: %q", got)
	}
	def := RepoScope{}
	if got := def.ScopedName("left-pad"); got != "left-pad" {
		t.Errorf("default: %q", got)
	}
}

// TestMirrorOrigin locks the alias-origin rule: a request routed through a
// named target emits self-URLs on that target's origin, so JSR's
// npm-compatibility registry (which rides the npm adapter) advertises
// npm.jsr.io, not registry.npmjs.org. The default target returns "" (its
// origin is the protocol's public home and must not bypass the gateway).
func TestMirrorOrigin(t *testing.T) {
	ctx := WithRepoScope(context.Background(), RepoScope{
		Format: "npm", Target: "npm.jsr", Host: "npm.jsr.io", Proto: "https",
	})
	if got := MirrorOrigin(ctx); got != "https://npm.jsr.io" {
		t.Errorf("mirror origin = %q", got)
	}
	// Default target: no override.
	def := WithRepoScope(context.Background(), RepoScope{
		Format: "npm", Target: "npm", Host: "registry.npmjs.org", Proto: "https",
	})
	if got := MirrorOrigin(def); got != "" {
		t.Errorf("default origin = %q, want empty", got)
	}
	// No target recorded (legacy): no override.
	none := WithRepoScope(context.Background(), RepoScope{Format: "npm", Host: "registry.npmjs.org"})
	if got := MirrorOrigin(none); got != "" {
		t.Errorf("untargeted origin = %q, want empty", got)
	}
}

// TestScopedKeyIsolation locks the storage-key shape for a target: a shared
// (mirror) target uses the protocol's namespace, an isolated (user-declared)
// target wraps it in a "t:<id>/" prefix so it cannot shadow public content.
func TestScopedKeyIsolation(t *testing.T) {
	// Shared mirror: no target prefix.
	m := RepoScope{Namespace: "org.slf4j", Target: "maven.google", TargetShared: true}
	if got := m.ScopedKey("slf4j-api"); got != "org.slf4j/slf4j-api" {
		t.Errorf("shared key = %q", got)
	}
	// Isolated user target: target prefix wraps the namespace-qualified key.
	u := RepoScope{Namespace: "org.slf4j", Target: "maven.corp", TargetShared: false}
	if got := u.ScopedKey("slf4j-api"); got != "t:maven.corp/org.slf4j/slf4j-api" {
		t.Errorf("isolated key = %q", got)
	}
	if got := u.UnscopedKey("t:maven.corp/org.slf4j/slf4j-api"); got != "slf4j-api" {
		t.Errorf("isolated unscoped = %q", got)
	}
	// The default target (ID == protocol) is shared and unprefixed.
	d := RepoScope{Namespace: "@acme", Target: "npm", TargetShared: true}
	if got := d.ScopedKey("@acme/ui"); got != "@acme/ui" {
		t.Errorf("default target key = %q", got)
	}
}
