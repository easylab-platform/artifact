package artifactkit

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/easylab-platform/artifact/targets"
)

// recordingHandler captures the path and scope the adapter would see.
type recordingHandler struct {
	path  string
	scope RepoScope
}

func (h *recordingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.path = r.URL.Path
	h.scope = RepoScopeFrom(r.Context())
	w.WriteHeader(http.StatusOK)
}

func newDispatch(t *testing.T) (*TargetDispatcher, *recordingHandler, *targets.Registry) {
	t.Helper()
	reg := targets.NewRegistry()
	r := &Registry{Upstreams: &Upstreams{Targets: reg, Defaults: reg.LegacyDefaults()}}
	h := &recordingHandler{}
	d := NewTargetDispatcher(MountBase, r, map[string]http.Handler{"maven": h, "npm": h})
	return d, h, reg
}

// TestDispatchBuiltinMirror checks a built-in named mirror is reachable and
// rewritten to the protocol mount.
func TestDispatchBuiltinMirror(t *testing.T) {
	d, h, _ := newDispatch(t)
	req := httptest.NewRequest(http.MethodGet, "/artifacts/maven.google/org/slf4j/x.pom", nil)
	d.ServeHTTP(httptest.NewRecorder(), req)
	if h.path != "/artifacts/maven/org/slf4j/x.pom" {
		t.Errorf("path = %q", h.path)
	}
	if h.scope.Target != "maven.google" || !h.scope.TargetShared {
		t.Errorf("scope = %+v", h.scope)
	}
}

// TestDispatchDefaultProtocolIsTransparent checks /artifacts/<protocol>/ stays
// unchanged.
func TestDispatchDefaultProtocolIsTransparent(t *testing.T) {
	d, h, _ := newDispatch(t)
	req := httptest.NewRequest(http.MethodGet, "/artifacts/npm/left-pad", nil)
	d.ServeHTTP(httptest.NewRecorder(), req)
	if h.path != "/artifacts/npm/left-pad" {
		t.Errorf("path = %q", h.path)
	}
	if h.scope.Format != "npm" || h.scope.Name != "left-pad" {
		t.Errorf("scope = %+v", h.scope)
	}
}

// TestDispatchDynamicTarget is the point of the dispatcher: a target created
// AFTER startup must be reachable with no restart and with no mux change.
func TestDispatchDynamicTarget(t *testing.T) {
	d, h, reg := newDispatch(t)
	reg.Put(targets.Target{ID: "maven.corp", Protocol: "maven", Base: "https://nexus.corp/m2"})

	req := httptest.NewRequest(http.MethodGet, "/artifacts/maven.corp/org/acme/lib/1.0/lib-1.0.pom", nil)
	d.ServeHTTP(httptest.NewRecorder(), req)

	if h.path != "/artifacts/maven/org/acme/lib/1.0/lib-1.0.pom" {
		t.Fatalf("path = %q", h.path)
	}
	if h.scope.Target != "maven.corp" {
		t.Errorf("target = %q", h.scope.Target)
	}
	if h.scope.TargetShared {
		t.Error("user target must isolate storage")
	}
}

// TestDispatchHostDrivenProvenance checks a host-driven request on the default
// mount records the mirror its Host resolved to.
func TestDispatchHostDrivenProvenance(t *testing.T) {
	d, h, _ := newDispatch(t)
	req := httptest.NewRequest(http.MethodGet, "/artifacts/maven/org/slf4j/x.pom", nil)
	req.Header.Set("X-Forwarded-Host", "dl.google.com")
	req.Header.Set("X-Forwarded-Proto", "https")
	d.ServeHTTP(httptest.NewRecorder(), req)
	if h.scope.Target != "maven.google" {
		t.Errorf("provenance target = %q, want maven.google", h.scope.Target)
	}
	if !h.scope.TargetShared {
		t.Error("host-driven mirror must be shared")
	}
}

// TestDispatchUnknownTargetNotFound checks an unknown segment 404s.
func TestDispatchUnknownTargetNotFound(t *testing.T) {
	d, _, _ := newDispatch(t)
	req := httptest.NewRequest(http.MethodGet, "/artifacts/nope/x", nil)
	rec := httptest.NewRecorder()
	d.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rec.Code)
	}
}
