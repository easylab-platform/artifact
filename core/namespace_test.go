package artifactkit

import "testing"

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
