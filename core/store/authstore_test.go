package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/easylab-platform/artifact/core"
)

func newAuthStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestAuthTokenHashedAtRest verifies the tokens table never holds plaintext.
func TestAuthTokenHashedAtRest(t *testing.T) {
	s := newAuthStore(t)
	ctx := context.Background()
	uid, err := s.CreateAuthUser(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAuthToken(ctx, "supersecret", uid, "write"); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := s.DB().Raw("SELECT token FROM auth_tokens LIMIT 1").Scan(&raw).Error; err != nil {
		t.Fatal(err)
	}
	if raw == "supersecret" {
		t.Fatal("credential stored in plaintext")
	}
	if len(raw) != 64 { // sha256 hex
		t.Fatalf("hash length = %d, want 64", len(raw))
	}
	// Lookup resolves the plaintext to the principal.
	p, ok := s.LookupToken(ctx, "supersecret")
	if !ok || p.Username != "alice" || p.Level != artifactkit.LevelWrite {
		t.Fatalf("lookup: %+v %v", p, ok)
	}
	// Wrong token fails.
	if _, ok := s.LookupToken(ctx, "nope"); ok {
		t.Fatal("unknown token must not resolve")
	}
}

// TestOpenInstanceTransition verifies OpenInstance flips once a user exists.
func TestOpenInstanceTransition(t *testing.T) {
	s := newAuthStore(t)
	ctx := context.Background()
	if !s.OpenInstance(ctx) {
		t.Fatal("fresh store must be open")
	}
	if _, err := s.CreateAuthUser(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if s.OpenInstance(ctx) {
		t.Fatal("store with a user must not be open")
	}
}

// TestStoreAuthPrivilegeMatrix drives an artifactkit.StoreAuth over the
// GORM credential tables: read cannot push, write can, mints never leak.
func TestStoreAuthPrivilegeMatrix(t *testing.T) {
	s := newAuthStore(t)
	ctx := context.Background()
	alice, _ := s.CreateAuthUser(ctx, "alice")
	bob, _ := s.CreateAuthUser(ctx, "bob")
	if err := s.CreateAuthToken(ctx, "writetok", alice, "write"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAuthToken(ctx, "readtok", bob, "read"); err != nil {
		t.Fatal(err)
	}
	a := artifactkit.NewStoreAuth(s)

	// Levels.
	if _, ok := a.CheckBearer(ctx, "writetok", "repository:x:push"); !ok {
		t.Fatal("write token should push")
	}
	if _, ok := a.CheckBearer(ctx, "readtok", "repository:x:pull"); !ok {
		t.Fatal("read token should pull")
	}
	if _, ok := a.CheckBearer(ctx, "readtok", "repository:x:push"); ok {
		t.Fatal("read token must not push")
	}

	// Mints never echo the database credential.
	m := a.IssueToken(ctx, "alice", []string{"repository:x:pull,push"}, 3600_000_000_000)
	if m == "" || m == "writetok" {
		t.Fatalf("mint = %q", m)
	}
	if _, ok := a.CheckBearer(ctx, m, "repository:x:push"); !ok {
		t.Fatal("minted write grant should authorize push")
	}
	if _, ok := a.CheckBearer(ctx, m, "repository:x:delete"); ok {
		t.Fatal("mint must not exceed its granted actions")
	}

	// Open instance: anonymous write permitted.
	s2 := newAuthStore(t)
	a2 := artifactkit.NewStoreAuth(s2)
	if _, ok := a2.CheckBearer(ctx, "anything", "repository:x:push"); !ok {
		t.Fatal("open instance should allow any credential")
	}
}
