package targets

import (
	"os"
	"strings"
	"testing"
)

// body strips the leading comment block from a rendered rules file, leaving
// the rules themselves. The two historical renderers differ ONLY in their
// header comment, so the bodies must match exactly.
func body(s string) string {
	i := strings.Index(s, "rules:\n")
	if i < 0 {
		return s
	}
	return s[i:]
}

// TestRenderMatchesEasylabGolden locks the egress renderer against the exact
// output easylab shipped before the target refactor. A drift here would
// silently change which upstreams traffic is steered to.
func TestRenderMatchesEasylabGolden(t *testing.T) {
	want, err := os.ReadFile("testdata/easylab_rules_golden.yaml")
	if err != nil {
		t.Fatal(err)
	}
	got := RenderEgressYAML("gw:80", false, "easysidecar default egress policy (managed by easylab).")
	if body(got) != body(string(want)) {
		t.Errorf("renderer diverged from easylab golden:\n--- got ---\n%s\n--- want ---\n%s", body(got), body(string(want)))
	}
}

// TestRenderMatchesEasyproxyGolden locks it against easysidecar's output too.
func TestRenderMatchesEasyproxyGolden(t *testing.T) {
	want, err := os.ReadFile("testdata/easyproxy_rules_golden.yaml")
	if err != nil {
		t.Fatal(err)
	}
	hdr := "easysidecar default egress policy: package-manager upstreams are\nsteered into easylab's pull-through registry; everything else is direct."
	got := RenderEgressYAML("gw:80", false, hdr)
	if body(got) != body(string(want)) {
		t.Errorf("renderer diverged from easysidecar golden:\n--- got ---\n%s\n--- want ---\n%s", body(got), body(string(want)))
	}
}

// TestRenderMITMDefault verifies the mitm_default toggle.
func TestRenderMITMDefault(t *testing.T) {
	if !strings.Contains(RenderEgressYAML("gw:80", true, ""), "mitm_default: true") {
		t.Error("mitm default not rendered")
	}
	if !strings.Contains(RenderEgressYAML("gw:80", false, ""), "mitm_default: false") {
		t.Error("mitm false not rendered")
	}
}

// TestEgressCovers locks the coverage helper that easysidecar uses for
// diagnostics.
func TestEgressCovers(t *testing.T) {
	cases := map[string]bool{
		"registry-1.docker.io":   true,
		"registry.npmjs.org":     true,
		"pypi.org":               true,
		"files.pythonhosted.org": true,
		"proxy.golang.org":       true,
		"crates.io":              true,
		"repo.maven.apache.org":  true,
		"api.nuget.org":          true,
		"rubygems.org":           true,
		"dl-cdn.alpinelinux.org": true,
		"deb.debian.org":         true,
		"archive.ubuntu.com":     true,
		"cdn-lfs.huggingface.co": true,
		"conda.anaconda.org":     true,
		"cache.nixos.org":        true,
		"example.com":            false, // not an upstream: direct
	}
	for host, want := range cases {
		if got := EgressCovers(host); got != want {
			t.Errorf("EgressCovers(%q) = %v want %v", host, got, want)
		}
	}
}

// TestLegacyDefaultsStillMatches guards the flat projection used by the
// registry's legacy Defaults field.
func TestEgressAddOnlyWhereExpected(t *testing.T) {
	// Only the recently-added mirrors/trees carry an explicit mount: the
	// protocol clients configured with a registry URL carry the mount
	// themselves. If this set changes, routing changes for every deployment.
	wantAdd := map[string]string{
		"npm.jsr.io":        "/artifacts/npm",
		"dl.google.com":     "/artifacts/maven",
		"plugins.gradle.org": "/artifacts/maven",
		"repo.clojars.org":  "/artifacts/maven",
		"repo.spring.io":    "/artifacts/maven",
		"jitpack.io":        "/artifacts/maven",
		"jsr.io":            "/artifacts/jsr",
		"opam.ocaml.org":    "/artifacts/opam",
		"stackage.org":      "/artifacts/stackage",
		"pecl.php.net":      "/artifacts/pecl",
		"bcr.bazel.build":   "/artifacts/bazel",
		"updates.jenkins.io": "/artifacts/jenkins",
	}
	got := map[string]string{}
	for _, e := range EgressPolicy() {
		for _, m := range e.Match {
			if e.Add != "" {
				got[m] = e.Add
			} else {
				got[m] = ""
			}
		}
	}
	for host, want := range wantAdd {
		if got[host] != want {
			t.Errorf("%s: add = %q, want %q", host, got[host], want)
		}
	}
	// And nothing outside the set gained an Add.
	for host, a := range got {
		if a != "" {
			if _, ok := wantAdd[host]; !ok {
				t.Errorf("%s unexpectedly has add_prefix %q", host, a)
			}
		}
	}
}
