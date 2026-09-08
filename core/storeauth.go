package artifactkit

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Auth, TokenLevel, and the reference TokenAuth live in auth.go. This file
// adds the database-backed variant: TokenStore (the credential DB seam) and
// StoreAuth (an Auth over it with the same minted-token semantics).

// Principal is the resolved identity behind a credential.
type Principal struct {
	Username string
	Level    TokenLevel
}

// TokenStore is the credential database an Auth consults. It is deliberately
// tiny so a deployment can bridge its own user tables (e.g. easylab's
// easyvcs store) without adopting artifact's storage. Tokens are compared by
// the STORE's semantics — implementations are expected to store hashes and
// hash the presented value on lookup (never plaintext).
type TokenStore interface {
	// LookupToken resolves a presented token to its principal. ok=false when
	// unknown.
	LookupToken(ctx context.Context, token string) (Principal, bool)
	// LookupUsername resolves a principal by username, reporting the
	// strongest credential level registered for that user. ok=false when the
	// user has no credentials (IssueToken then mints pull-only).
	LookupUsername(ctx context.Context, username string) (Principal, bool)
	// OpenInstance reports whether the deployment has no users at all, in
	// which case anonymous write is permitted (dev/single-user mode).
	OpenInstance(ctx context.Context) bool
}

// PrincipalFunc adapts a function to TokenStore (OpenInstance = false).
type PrincipalFunc func(ctx context.Context, token string) (Principal, bool)

func (f PrincipalFunc) LookupToken(ctx context.Context, token string) (Principal, bool) {
	return f(ctx, token)
}
func (f PrincipalFunc) LookupUsername(context.Context, string) (Principal, bool) {
	return Principal{}, false
}
func (f PrincipalFunc) OpenInstance(context.Context) bool { return false }

// StoreAuth is an Auth backed by a TokenStore (hashed credentials in a
// database) plus an in-memory minted-token table for OCI bearer tokens. The
// long-lived database credential is never handed back to a client.
type StoreAuth struct {
	store TokenStore

	mintedMu sync.Mutex
	minted   map[string]*mintedToken
}

// NewStoreAuth builds an Auth over a TokenStore.
func NewStoreAuth(store TokenStore) *StoreAuth {
	return &StoreAuth{store: store, minted: map[string]*mintedToken{}}
}

// open reports whether the deployment permits anonymous write.
func (a *StoreAuth) open(ctx context.Context) bool {
	return a.store.OpenInstance(ctx)
}

// resolve maps a credential (database token or minted token) to its
// principal. The minted table is checked first so mints keep their grant's
// privilege bounds.
func (a *StoreAuth) resolve(ctx context.Context, token string) (Principal, bool) {
	if a.open(ctx) {
		return Principal{Username: "open", Level: LevelWrite}, true
	}
	if m, ok := a.lookupMinted(token); ok {
		return Principal{Username: m.username, Level: m.level}, true
	}
	if p, ok := a.store.LookupToken(ctx, token); ok {
		return p, true
	}
	return Principal{}, false
}

// lookupMinted resolves a minted bearer token, pruning it when expired.
func (a *StoreAuth) lookupMinted(token string) (*mintedToken, bool) {
	a.mintedMu.Lock()
	defer a.mintedMu.Unlock()
	m, ok := a.minted[token]
	if !ok {
		return nil, false
	}
	if time.Now().After(m.expires) {
		delete(a.minted, token)
		return nil, false
	}
	return m, true
}

// Authenticate implements Auth. It returns a username for any valid
// credential (read or write); callers needing a privilege decision must use
// CheckBearer/CheckToken.
func (a *StoreAuth) Authenticate(ctx context.Context, r *http.Request) string {
	for _, tok := range credentialCandidates(r) {
		if p, ok := a.resolve(ctx, tok); ok {
			return p.Username
		}
	}
	return ""
}

// CheckBearer implements Auth. The requested action must be permitted by the
// credential's level: a read-level credential (or a pull-only mint) cannot
// push/delete.
func (a *StoreAuth) CheckBearer(ctx context.Context, token, wantedScope string) (string, bool) {
	action := scopeActionOf(wantedScope)
	p, ok := a.resolve(ctx, token)
	if !ok {
		return "", false
	}
	if action == "push" || action == "delete" {
		if p.Level != LevelWrite {
			return "", false
		}
		// A minted token's grant also bounds the action set.
		if m, isMint := a.lookupMinted(token); isMint && !actionAllowedByScopes(action, m.scopes, m.level) {
			return "", false
		}
	}
	return p.Username, true
}

// scopeActionOf extracts the first action of an OCI scope
// ("repository:<name>:<action[,action]>"), if any.
func scopeActionOf(scope string) string {
	_, action, ok := splitAction(scope)
	if !ok {
		return ""
	}
	if j := strings.Index(action, ","); j >= 0 {
		action = action[:j]
	}
	return action
}

// CheckToken implements Auth.
func (a *StoreAuth) CheckToken(ctx context.Context, token string) (string, bool) {
	p, ok := a.resolve(ctx, token)
	if !ok {
		return "", false
	}
	return p.Username, true
}

// CheckBasic implements Auth: the password carries the token.
func (a *StoreAuth) CheckBasic(ctx context.Context, _, pass string) bool {
	_, ok := a.resolve(ctx, pass)
	return ok
}

// IssueToken implements Auth. It mints a RANDOM bearer token bound to the
// grantee's privilege: a write-level principal gets a push-capable token, a
// read-level (or anonymous) principal gets a pull-only token. The database
// credential is NEVER handed back to a client.
func (a *StoreAuth) IssueToken(ctx context.Context, username string, scopes []string, ttl time.Duration) string {
	if ttl <= 0 {
		ttl = time.Hour
	}
	if username == "" {
		return ""
	}
	level := LevelRead
	if a.open(ctx) {
		level = LevelWrite
	} else if p, ok := a.store.LookupUsername(ctx, username); ok {
		// The grantee's mint inherits the strongest credential level
		// registered for their account.
		level = p.Level
	}
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is unrecoverable; fail closed (no token).
		return ""
	}
	tok := "ak_m_" + base64.RawURLEncoding.EncodeToString(b[:])
	a.mintedMu.Lock()
	// Opportunistically prune expired entries so the table stays bounded.
	now := time.Now()
	for k, m := range a.minted {
		if now.After(m.expires) {
			delete(a.minted, k)
		}
	}
	a.minted[tok] = &mintedToken{username: username, scopes: scopes, level: level, expires: now.Add(ttl)}
	a.mintedMu.Unlock()
	return tok
}
