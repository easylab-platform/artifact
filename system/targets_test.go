package system

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/easylab-platform/artifact/core"
	"github.com/easylab-platform/artifact/targets"
)

// memTargetStore is an in-memory targets.Store for the handler test.
type memTargetStore struct{ m map[string]targets.Target }

func (s *memTargetStore) ListTargets(_ context.Context) ([]targets.Target, error) {
	var out []targets.Target
	for _, t := range s.m {
		out = append(out, t)
	}
	return out, nil
}
func (s *memTargetStore) PutTarget(_ context.Context, t targets.Target) error {
	if s.m == nil {
		s.m = map[string]targets.Target{}
	}
	s.m[t.ID] = t
	return nil
}
func (s *memTargetStore) DeleteTarget(_ context.Context, id string) error {
	delete(s.m, id)
	return nil
}

func newState() (*State, *memTargetStore) {
	reg := targets.NewRegistry()
	pst := &memTargetStore{m: map[string]targets.Target{}}
	r := &artifactkit.Registry{Upstreams: &artifactkit.Upstreams{Targets: reg}}
	s := &State{Registry: r, TargetsStore: pst}
	return s, pst
}

// newGatedState builds a State with a non-nil TokenAuth, so the admin gate is
// active (auth absent = open dev instance; auth present = operator required).
func newGatedState() *State {
	reg := targets.NewRegistry()
	r := &artifactkit.Registry{Upstreams: &artifactkit.Upstreams{Targets: reg}}
	return &State{
		Registry:     r,
		TargetsStore: &memTargetStore{m: map[string]targets.Target{}},
		Auth:         artifactkit.NewTokenAuth("writer=write,reader=read"),
	}
}

func do(t *testing.T, s *State, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, r)
	return rec
}

func TestTargetsListIncludesBuiltins(t *testing.T) {
	s, _ := newState()
	rec := do(t, s, http.MethodGet, "/artifacts/system/targets", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var out struct {
		Targets []targets.Target `json:"targets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	// 40+ built-ins must be listed.
	if len(out.Targets) < 30 {
		t.Fatalf("only %d targets listed", len(out.Targets))
	}
}

func TestTargetCRUD(t *testing.T) {
	s, _ := newState()
	body := map[string]any{
		"id": "maven.corp", "base": "https://nexus.corp/m2",
		"hosts": []string{"nexus.corp"},
	}
	if rec := do(t, s, http.MethodPut, "/artifacts/system/targets/maven.corp", body); rec.Code != http.StatusOK {
		t.Fatalf("put code = %d body=%s", rec.Code, rec.Body.String())
	}
	// It is now resolvable through the registry and isolated.
	tgt, ok := s.Registry.Upstreams.Targets.Get("maven.corp")
	if !ok || tgt.Shared() {
		t.Fatalf("target not registered isolated: %+v ok=%v", tgt, ok)
	}
	// GET one.
	if rec := do(t, s, http.MethodGet, "/artifacts/system/targets/maven.corp", nil); rec.Code != http.StatusOK {
		t.Fatalf("get code = %d", rec.Code)
	}
	// DELETE.
	if rec := do(t, s, http.MethodDelete, "/artifacts/system/targets/maven.corp", nil); rec.Code != http.StatusOK {
		t.Fatalf("delete code = %d", rec.Code)
	}
	if _, ok := s.Registry.Upstreams.Targets.Get("maven.corp"); ok {
		t.Fatal("target still registered after delete")
	}
}

func TestTargetDeleteBuiltinRefused(t *testing.T) {
	s, _ := newState()
	if rec := do(t, s, http.MethodDelete, "/artifacts/system/targets/maven", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("deleting built-in: code = %d, want 403", rec.Code)
	}
}

// TestAdminSurfaceRequiresOperator verifies the whole /artifacts/system
// subtree is operator-only, reads included: anonymous gets a 401 challenge, a
// read-level token gets 403, and a write-level token gets through. Without auth
// configured (dev instance) reads stay open.
func TestAdminSurfaceRequiresOperator(t *testing.T) {
	s := newGatedState()

	// Anonymous read: 401 + a challenge header.
	rec := do(t, s, http.MethodGet, "/artifacts/system/stats", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous GET = %d, want 401", rec.Code)
	}
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Error("401 should carry a WWW-Authenticate challenge")
	}

	// Read-level token: authenticated but not write-capable -> 403.
	req := httptest.NewRequest(http.MethodGet, "/artifacts/system/targets", nil)
	req.Header.Set("Authorization", "Bearer reader")
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("read-level GET = %d, want 403", rec.Code)
	}

	// Write-level token: operator -> 200.
	req = httptest.NewRequest(http.MethodGet, "/artifacts/system/targets", nil)
	req.Header.Set("Authorization", "Bearer writer")
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("write-level GET = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	// A write method is gated the same way (anonymous -> 401).
	if rec := do(t, s, http.MethodPut, "/artifacts/system/targets/maven.corp", map[string]any{"id": "maven.corp", "base": "https://x"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous PUT = %d, want 401", rec.Code)
	}
}

// TestAdminOpenInstanceWhenNoAuth keeps the dev/open behavior: with no Auth the
// admin surface is reachable anonymously (the existing tests rely on it).
func TestAdminOpenInstanceWhenNoAuth(t *testing.T) {
	s, _ := newState()
	if rec := do(t, s, http.MethodGet, "/artifacts/system/stats", nil); rec.Code != http.StatusNotImplemented {
		// no StatsFunc -> 501, but crucially NOT 401: the gate let it through.
		t.Fatalf("open-instance GET = %d, want 501 (not 401)", rec.Code)
	}
}

func TestTargetValidate(t *testing.T) {
	s, _ := newState()
	// No base and no hosts: unroutable.
	body := map[string]any{"id": "maven.bad", "protocol": "maven"}
	if rec := do(t, s, http.MethodPut, "/artifacts/system/targets/maven.bad", body); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for base-less target, got %d", rec.Code)
	}
	// Unknown auth mode.
	body = map[string]any{"id": "maven.bad2", "base": "https://x", "auth": map[string]any{"mode": "kerberos"}}
	if rec := do(t, s, http.MethodPut, "/artifacts/system/targets/maven.bad2", body); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad auth, got %d", rec.Code)
	}
}
