package artifactkit

import (
	"context"
	"testing"

	"github.com/easylab-platform/artifact/targets"
)

// targetUpstreams builds the same table the server builds: the target registry
// plus its legacy projection and empty policy maps.
func targetUpstreams() *Upstreams {
	reg := targets.NewRegistry()
	return &Upstreams{
		Targets:   reg,
		Defaults:  reg.LegacyDefaults(),
		Overrides: map[string]string{},
		Proxy:     map[string]string{},
	}
}

// TestHostDrivenMavenMirror checks that a client reaching us as dl.google.com
// (the egress proxy sets X-Forwarded-Host) resolves to Google Maven's base
// INCLUDING its path prefix, so a client that stripped nothing still lands on
// the right upstream path.
func TestHostDrivenMavenMirror(t *testing.T) {
	u := targetUpstreams()
	base, ok := u.HostBase("https", "dl.google.com", "")
	if !ok {
		t.Fatal("dl.google.com not resolvable")
	}
	if base != "https://dl.google.com/dl/android/maven2" {
		t.Errorf("base = %q", base)
	}
}

// TestHostDrivenGradle checks the strip prefix comes from the target, not the
// client, when the client sent no prefix.
func TestHostDrivenGradle(t *testing.T) {
	u := targetUpstreams()
	base, ok := u.HostBase("https", "plugins.gradle.org", "")
	if !ok {
		t.Fatal("plugins.gradle.org not resolvable")
	}
	if base != "https://plugins.gradle.org/m2" {
		t.Errorf("base = %q", base)
	}
}

// TestHostDrivenDefaultUpstream keeps the historical behavior: a host with no
// target of its own (an upstream reached through the table) restores the
// client's stripped prefix.
func TestHostDrivenDefaultUpstream(t *testing.T) {
	u := targetUpstreams()
	base, ok := u.HostBase("https", "repo.maven.apache.org", "/maven2")
	if !ok {
		t.Fatal("repo.maven.apache.org not resolvable")
	}
	if base != "https://repo.maven.apache.org/maven2" {
		t.Errorf("base = %q", base)
	}
}

// TestUnknownHostRejected is the SSRF guard: a client cannot aim the mirror at
// an arbitrary origin.
func TestUnknownHostRejected(t *testing.T) {
	u := targetUpstreams()
	if _, ok := u.HostBase("https", "evil.example.net", ""); ok {
		t.Error("unknown host was accepted")
	}
}

// TestWildcardHostDoesNotInheritBase guards the wildcard case: a request to
// cdn-lfs.huggingface.co must not be rewritten to huggingface.co's base
// (whose host differs), or the CDN object would be fetched from the API host.
func TestWildcardHostDoesNotInheritBase(t *testing.T) {
	u := targetUpstreams()
	base, ok := u.HostBase("https", "cdn-lfs.huggingface.co", "")
	if !ok {
		t.Fatal("cdn-lfs.huggingface.co not resolvable")
	}
	if base == "https://huggingface.co" {
		t.Errorf("CDN host inherited the API base: %q", base)
	}
}

// TestResolveBasePriority locks the invariant order:
// per-repo override > client origin > table default.
func TestResolveBasePriority(t *testing.T) {
	u := targetUpstreams()
	r := &Registry{Upstreams: u}

	// 3. table default.
	ctx := WithRepoScope(context.Background(), RepoScope{Format: "maven", Name: "guava"})
	base, _, ok := r.resolveBase(ctx, "maven", "guava")
	if !ok || base != "https://repo.maven.apache.org/maven2" {
		t.Fatalf("default: %q ok=%v", base, ok)
	}

	// 2. client origin beats the default.
	ctxHost := WithRepoScope(context.Background(), RepoScope{
		Format: "maven", Name: "guava", Host: "dl.google.com", Proto: "https",
	})
	base, _, ok = r.resolveBase(ctxHost, "maven", "guava")
	if !ok || base != "https://dl.google.com/dl/android/maven2" {
		t.Fatalf("host: %q ok=%v", base, ok)
	}

	// 1. a per-repo override beats the client origin.
	u.SetRepo("maven", "com.google.guava", "https://mirror.corp/m2", "")
	base, _, ok = r.resolveBase(ctxHost, "maven", "com.google.guava")
	if !ok || base != "https://mirror.corp/m2" {
		t.Fatalf("override: %q ok=%v", base, ok)
	}
}

// TestKnownHostsFromTargets verifies the allow-list is derived from the target
// registry (previously the fake "maven.google" format had to exist for its
// host to be reachable).
func TestKnownHostsFromTargets(t *testing.T) {
	u := targetUpstreams()
	hosts := u.knownHosts()
	for _, h := range []string{
		"dl.google.com", "plugins.gradle.org", "repo.clojars.org",
		"npm.jsr.io", "index.crates.io", "github.com",
	} {
		if _, ok := hosts[h]; !ok {
			t.Errorf("host %q not allow-listed", h)
		}
	}
}

// TestAirGapStillReturnsNothing confirms the air-gap flag wins over targets.
func TestAirGapStillReturnsNothing(t *testing.T) {
	u := targetUpstreams()
	u.AirGap = true
	if _, ok := u.HostBase("https", "dl.google.com", ""); ok {
		t.Error("air-gap returned an upstream")
	}
	if u.Get("maven") != "" {
		t.Error("air-gap Get returned an upstream")
	}
}

// TestNamedTargetFromScope locks the explicit-mount resolution path: a request
// served under /artifacts/maven.google/... must select Google Maven even when
// the Host header is the gateway (no X-Forwarded-Host). This regressed in
// testing because the guard rejected dotted target IDs as if they were the
// default target.
func TestNamedTargetFromScope(t *testing.T) {
	u := targetUpstreams()
	r := &Registry{Upstreams: u}
	ctx := WithRepoScope(context.Background(), RepoScope{
		Format: "maven", Target: "maven.google", TargetShared: true, Name: "gradle",
	})
	base, _, ok := r.resolveBase(ctx, "maven", "gradle")
	if !ok {
		t.Fatal("named target did not resolve")
	}
	if base != "https://dl.google.com/dl/android/maven2" {
		t.Errorf("base = %q, want Google Maven", base)
	}
}

// TestDefaultTargetFromScopeFallsThrough keeps the default target from
// outranking the client host: /artifacts/cargo/... served to a client that
// reached us as index.crates.io must go to index.crates.io, not crates.io.
func TestDefaultTargetFromScopeFallsThrough(t *testing.T) {
	u := targetUpstreams()
	r := &Registry{Upstreams: u}
	ctx := WithRepoScope(context.Background(), RepoScope{
		Format: "cargo", Target: "cargo", TargetShared: true, Host: "index.crates.io", Proto: "https",
	})
	base, _, ok := r.resolveBase(ctx, "cargo", "serde")
	if !ok || base != "https://index.crates.io" {
		t.Errorf("base = %q ok=%v, want index.crates.io", base, ok)
	}
}
