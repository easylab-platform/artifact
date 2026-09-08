package artifactkit

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestTokenAuthLevels verifies the privilege matrix of the static token
// table: write permits pull+push, read permits pull only, unknown/empty
// permits nothing.
func TestTokenAuthLevels(t *testing.T) {
	a := NewTokenAuth("reader=read writer=write")
	ctx := context.Background()

	cases := []struct {
		token  string
		action string
		want   bool
	}{
		{"writer", "pull", true},
		{"writer", "push", true},
		{"writer", "delete", true},
		{"reader", "pull", true},
		{"reader", "push", false},
		{"reader", "delete", false},
		{"unknown", "pull", false},
		{"", "pull", false},
	}
	for _, c := range cases {
		if _, ok := a.CheckBearer(ctx, c.token, "repository:repo/x:"+c.action); ok != c.want {
			t.Errorf("CheckBearer(%q, %s) = %v want %v", c.token, c.action, ok, c.want)
		}
	}
}

// TestTokenAuthMalformedSpec verifies malformed pairs (empty name/level,
// missing separator) never register a credential — especially the empty
// string.
func TestTokenAuthMalformedSpec(t *testing.T) {
	a := NewTokenAuth("=write, writer=, nosep, =, ok=write")
	if _, ok := a.CheckToken(context.Background(), ""); ok {
		t.Fatal("empty string must never authenticate")
	}
	if _, ok := a.CheckToken(context.Background(), "writer"); ok {
		t.Fatal("empty-level pair must be rejected")
	}
	if _, ok := a.CheckToken(context.Background(), "ok"); !ok {
		t.Fatal("valid pair should register")
	}
}

// TestIssueTokenNeverLeaksStaticToken verifies the OCI bearer minting path:
// the static write token is never returned, and a minted token carries only
// the grantee's privilege.
func TestIssueTokenNeverLeaksStaticToken(t *testing.T) {
	a := NewTokenAuth("writertok=write readertok=read")
	ctx := context.Background()

	// A write principal asking for push gets a random minted token.
	minted := a.IssueToken(ctx, "token:writertok", []string{"repository:x:push,pull"}, time.Hour)
	if minted == "" {
		t.Fatal("write principal should get a token")
	}
	if minted == "writertok" {
		t.Fatal("IssueToken must not return the static write token")
	}
	if !hasPrefix(minted, "ak_m_") {
		t.Fatalf("minted token should be prefixed, got %q", minted)
	}
	// The minted token authorizes push.
	if _, ok := a.CheckBearer(ctx, minted, "repository:x:push"); !ok {
		t.Fatal("minted write token should authorize push")
	}
	// But ONLY for the granted scope's repository actions.
	if _, ok := a.CheckBearer(ctx, minted, "repository:x:delete"); ok {
		t.Fatal("minted token grants only the requested actions (push,pull)")
	}

	// A read principal gets a pull-only minted token.
	rt := a.IssueToken(ctx, "token:readertok", []string{"repository:x:pull"}, time.Hour)
	if rt == "" || rt == "writertok" {
		t.Fatalf("read mint = %q", rt)
	}
	if _, ok := a.CheckBearer(ctx, rt, "repository:x:push"); ok {
		t.Fatal("read principal's minted token must not authorize push")
	}
	if _, ok := a.CheckBearer(ctx, rt, "repository:x:pull"); !ok {
		t.Fatal("read principal's minted token should authorize pull")
	}

	// Anonymous mint is pull-only too.
	anon := a.IssueToken(ctx, "", []string{"repository:x:pull"}, time.Hour)
	if _, ok := a.CheckBearer(ctx, anon, "repository:x:push"); ok {
		t.Fatal("anonymous minted token must not authorize push")
	}
}

// TestIssueTokenExpiry verifies minted tokens stop working after their TTL.
func TestIssueTokenExpiry(t *testing.T) {
	a := NewTokenAuth("w=write")
	ctx := context.Background()
	tok := a.IssueToken(ctx, "token:w", []string{"repository:x:pull"}, 20*time.Millisecond)
	if _, ok := a.CheckBearer(ctx, tok, "repository:x:pull"); !ok {
		t.Fatal("fresh mint should work")
	}
	time.Sleep(40 * time.Millisecond)
	if _, ok := a.CheckBearer(ctx, tok, "repository:x:pull"); ok {
		t.Fatal("expired mint must fail")
	}
}

// TestAuthorizeWriteRequiresWriteLevel verifies the generic adapter gate:
// anonymous -> 401, read-level -> 403, write-level -> allowed — for EVERY
// credential scheme a protocol may present (Bearer, bare Authorization,
// Basic password, X-NuGet-ApiKey).
func TestAuthorizeWriteRequiresWriteLevel(t *testing.T) {
	a := NewTokenAuth("reader=read writer=write")

	mkReq := func(authz string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/upload", nil)
		if authz != "" {
			r.Header.Set("Authorization", authz)
		}
		return r
	}

	rec := httptest.NewRecorder()
	if AuthorizeWrite(rec, mkReq(""), a) {
		t.Fatal("anonymous should be denied")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous code = %d", rec.Code)
	}

	rec2 := httptest.NewRecorder()
	r2 := mkReq("Bearer reader")
	if AuthorizeWrite(rec2, r2, a) {
		t.Fatal("read-level should be denied")
	}
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("read-level code = %d", rec2.Code)
	}

	rec3 := httptest.NewRecorder()
	r3 := mkReq("Bearer writer")
	if !AuthorizeWrite(rec3, r3, a) {
		t.Fatalf("write-level should be allowed, got %d %s", rec3.Code, rec3.Body.String())
	}

	// Auth disabled: anonymous writes allowed (dev mode).
	rec4 := httptest.NewRecorder()
	if !AuthorizeWrite(rec4, mkReq(""), nil) {
		t.Fatal("nil auth must allow (dev mode)")
	}
}

// TestAuthorizeWriteAllSchemes drives the write-token through every scheme a
// protocol adapter may use. Each must authenticate AND pass the write-level
// check (the v0.2.0 regression only recognized the Bearer scheme).
func TestAuthorizeWriteAllSchemes(t *testing.T) {
	a := NewTokenAuth("writer=write reader=read")

	mk := func(headers map[string]string) *http.Request {
		r := httptest.NewRequest(http.MethodPut, "/publish", nil)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		return r
	}

	// Bearer.
	rec := httptest.NewRecorder()
	if !AuthorizeWrite(rec, mk(map[string]string{"Authorization": "Bearer writer"}), a) {
		t.Fatalf("Bearer write: %d %s", rec.Code, rec.Body.String())
	}
	// Bare Authorization (cargo).
	rec2 := httptest.NewRecorder()
	if !AuthorizeWrite(rec2, mk(map[string]string{"Authorization": "writer"}), a) {
		t.Fatalf("bare write: %d %s", rec2.Code, rec2.Body.String())
	}
	// Basic with the token as password (composer/hex style).
	rec3 := httptest.NewRecorder()
	if !AuthorizeWrite(rec3, mk(map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("user:writer"))}), a) {
		t.Fatalf("Basic write: %d %s", rec3.Code, rec3.Body.String())
	}
	// NuGet API key.
	rec4 := httptest.NewRecorder()
	if !AuthorizeWrite(rec4, mk(map[string]string{"X-NuGet-ApiKey": "writer"}), a) {
		t.Fatalf("NuGet-ApiKey write: %d %s", rec4.Code, rec4.Body.String())
	}

	// The read token must be denied in every scheme too (403, not 401: the
	// credential is recognized, just not privileged).
	for name, headers := range map[string]map[string]string{
		"Bearer":       {"Authorization": "Bearer reader"},
		"bare":         {"Authorization": "reader"},
		"Basic":        {"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("user:reader"))},
		"NuGet-ApiKey": {"X-NuGet-ApiKey": "reader"},
	} {
		rec := httptest.NewRecorder()
		if AuthorizeWrite(rec, mk(headers), a) {
			t.Fatalf("%s read-level should be denied", name)
		}
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s read-level code = %d", name, rec.Code)
		}
	}
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

// TestIssueTokenTTLUnit guards against the classic Duration bug: a caller
// passing a bare 3600 means 3600 NANOSECONDS, and the mint would expire
// instantly. This test fails if IssueToken ever mishandles sub-millisecond
// TTLs by treating them as "already expired" silently — the contract is: ttl
// <= 0 is replaced by the default; tiny positive TTLs are honored (and thus
// expire), which callers must express in real Duration units.
func TestIssueTokenTTLUnit(t *testing.T) {
	a := NewTokenAuth("w=write")
	ctx := context.Background()

	// A bare integer like 3600 is 3600ns: honored, so expired immediately
	// after. This documents the trap; serveToken callers must pass time.Hour.
	tok := a.IssueToken(ctx, "token:w", []string{"repository:x:pull"}, 3600)
	if tok == "" {
		t.Fatal("mint should still be created for a tiny TTL")
	}
	time.Sleep(2 * time.Millisecond)
	if _, ok := a.CheckBearer(ctx, tok, "repository:x:pull"); ok {
		t.Fatal("3600ns TTL mint must already be expired (Duration units!)")
	}

	// A real hour works.
	tok2 := a.IssueToken(ctx, "token:w", []string{"repository:x:pull"}, time.Hour)
	if _, ok := a.CheckBearer(ctx, tok2, "repository:x:pull"); !ok {
		t.Fatal("1h TTL mint should be valid")
	}
}
