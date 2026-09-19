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
		// An arbitrary public host is now covered by the catch-all: it is
		// cached through netcache rather than fetched twice.
		"example.com": true,
		// Cluster-local names and IPs are NOT covered: the catch-all only
		// matches public hostnames, so it never captures in-cluster traffic.
		"kubernetes.default.svc": false,
		"easylab":                false,
		"127.0.0.1":              false,
		"localhost":              false,
	}
	for host, want := range cases {
		if got := EgressCovers(host); got != want {
			t.Errorf("EgressCovers(%q) = %v want %v", host, got, want)
		}
	}
}

// TestEgressAddMountsEveryProtocol pins the invariant the derivation exists to
// guarantee: every non-OCI target steers its hosts onto its protocol mount.
// The pre-derivation table omitted add_prefix for most protocols, which broke
// transparent pull-through on the real gateway.
func TestEgressAddMountsEveryProtocol(t *testing.T) {
	for _, e := range EgressPolicy() {
		if e.Direct {
			continue
		}
		if _, isOCI := index(e.Match, "registry-1.docker.io"); isOCI {
			continue // OCI has no add (see TestEgressOCIHasNoAdd)
		}
		if e.Add != MountBase+"/"+protoOf(e) {
			t.Errorf("rule %v: add = %q, want %q", e.Match, e.Add, MountBase+"/"+protoOf(e))
		}
		if protoOf(e) == "" {
			t.Errorf("rule %v has no protocol mount", e.Match)
		}
	}
}

// protoOf recovers the protocol from a rule's add prefix (for OCI it is "").
func protoOf(e Egress) string {
	return strings.TrimPrefix(e.Add, MountBase+"/")
}

// TestEgressOCIHasNoAdd keeps OCI spec-fixed: it routes by Host at /v2, so the
// sidecar must not prefix its path.
func TestEgressOCIHasNoAdd(t *testing.T) {
	for _, e := range EgressPolicy() {
		if e.Direct {
			continue
		}
		if _, ok := index(e.Match, "registry-1.docker.io"); ok {
			if e.Add != "" {
				t.Errorf("OCI rule has add_prefix %q, want none", e.Add)
			}
		}
	}
}

// TestEgressMultiStrip pins Spring: it serves the same content under /release
// and /milestone, and both prefixes belong to the origin, so both must be
// stripped before the mount.
func TestEgressMultiStrip(t *testing.T) {
	for _, e := range EgressPolicy() {
		if _, ok := index(e.Match, "repo.spring.io"); !ok {
			continue
		}
		if len(e.Strips) != 2 {
			t.Fatalf("spring strips = %v, want 2", e.Strips)
		}
		got := map[string]bool{}
		for _, s := range e.Strips {
			got[s] = true
		}
		if !got["/milestone"] || !got["/release"] {
			t.Errorf("spring strips = %v, want /milestone and /release", e.Strips)
		}
		return
	}
	t.Fatal("no spring rule")
}

// TestEgressPathScoping pins dl.google.com: its maven rule is scoped to
// /dl/android/maven2 so the Android SDK path (/android/repository) falls
// through to the catch-all instead of being routed to the maven adapter.
func TestEgressPathScoping(t *testing.T) {
	var sawScoped bool
	for _, e := range EgressPolicy() {
		if _, ok := index(e.Match, "dl.google.com"); !ok {
			continue
		}
		if e.PathPrefix != "/dl/android/maven2" {
			t.Fatalf("dl.google.com path_prefix = %q, want /dl/android/maven2", e.PathPrefix)
		}
		sawScoped = true
		if e.Add != MountBase+"/maven" {
			t.Errorf("dl.google.com add = %q, want maven", e.Add)
		}
	}
	if !sawScoped {
		t.Fatal("no dl.google.com rule")
	}
	// The catch-all must still be present (the SDK path falls through to it),
	// and dl.google.com must not be claimed (a path-scoped host is re-matchable).
	for _, e := range EgressPolicy() {
		if len(e.Match) == 1 && e.Match[0] == CatchAllPattern {
			return
		}
	}
	t.Fatal("catch-all rule missing")
}

// host must not be reachable by a later, broader pattern, or first-match-wins
// would route it to the wrong target.
func TestEgressNoShadowing(t *testing.T) {
	// npm.jsr.io must be claimed before jsr.io (bare suffix "jsr.io" would
	// otherwise match npm.jsr.io).
	var order []string
	for _, e := range EgressPolicy() {
		if !e.Direct {
			order = append(order, e.Match...)
		}
	}
	pos := func(h string) int {
		for i, v := range order {
			if v == h {
				return i
			}
		}
		return -1
	}
	if pos("npm.jsr.io") == -1 || pos("jsr.io") == -1 {
		t.Fatalf("missing rules: %v", order)
	}
	if pos("npm.jsr.io") > pos("jsr.io") {
		t.Errorf("npm.jsr.io must precede jsr.io (got %d > %d)", pos("npm.jsr.io"), pos("jsr.io"))
	}
}

// TestEgressDirectCDNs checks the container blob CDNs stay direct (the adapter
// follows the 307 server-side).
func TestEgressDirectCDNs(t *testing.T) {
	for _, e := range EgressPolicy() {
		if _, ok := index(e.Match, "*.cloudflarestorage.com"); ok && !e.Direct {
			t.Error("cloudflare storage must be direct")
		}
	}
}

func index(hay []string, needle string) (int, bool) {
	for i, h := range hay {
		if h == needle {
			return i, true
		}
	}
	return -1, false
}
