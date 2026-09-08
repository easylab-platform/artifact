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

// Auth is the protocol-agnostic credential interface. The reference
// implementation maps a static `token=level` table (read/write) onto the
// per-protocol credential models (OCI bearer, cargo raw token, nuget api-key,
// basic auth, ...).
type Auth interface {
	// Authenticate extracts a username from the request, or "" when
	// anonymous. Implementations must understand the common Authorization
	// prefixes plus protocol-specific header forms.
	Authenticate(ctx context.Context, r *http.Request) string

	// CheckBearer validates an OCI bearer token against a scope.
	// wantedScope is like "repository:<name>:<action>".
	CheckBearer(ctx context.Context, token, wantedScope string) (string, bool)

	// CheckToken validates a bare/raw token (cargo, npm publish, ...).
	CheckToken(ctx context.Context, token string) (string, bool)

	// CheckBasic validates Basic credentials.
	CheckBasic(ctx context.Context, user, pass string) bool

	// IssueToken mints an OCI bearer token (returned to clients verbatim).
	IssueToken(ctx context.Context, username string, scopes []string, ttl time.Duration) string
}

// TokenLevel is the privilege carried by a static token.
type TokenLevel string

const (
	LevelRead  TokenLevel = "read"
	LevelWrite TokenLevel = "write"
)

// mintedToken is an OCI bearer token minted by IssueToken. It carries the
// level its holder was entitled to at mint time, so a pull-only grant can
// never authorize a push even though both flow through the same Bearer
// mechanism.
type mintedToken struct {
	username string
	scopes   []string
	level    TokenLevel
	expires  time.Time
}

// TokenAuth is the reference Auth implementation for a `token=level` table.
type TokenAuth struct {
	// Tokens maps a token string to its level.
	Tokens map[string]TokenLevel

	// minted holds bearer tokens issued by IssueToken (random values bound
	// to the grantee's level + scopes + expiry). They never collide with the
	// static table because they are prefixed.
	mintedMu sync.Mutex
	minted   map[string]*mintedToken
}

// NewTokenAuth builds a TokenAuth from a `level=token` or `token=level`
// comma/space separated string (e.g. "devtoken=write,readtoken=read").
func NewTokenAuth(spec string) *TokenAuth {
	t := &TokenAuth{Tokens: map[string]TokenLevel{}, minted: map[string]*mintedToken{}}
	for _, pair := range splitTokens(spec) {
		parts, ok := splitOnce(pair, '=')
		if !ok {
			continue
		}
		name, level := parts[0], parts[1]
		if name == "" {
			// A malformed pair like "=write" would otherwise register the
			// empty string as a valid credential.
			continue
		}
		switch TokenLevel(level) {
		case LevelRead, LevelWrite:
			t.Tokens[name] = TokenLevel(level)
		case "rl", "r":
			t.Tokens[name] = LevelRead
		case "rw", "w":
			t.Tokens[name] = LevelWrite
		}
	}
	return t
}

func (t *TokenAuth) level(token string) (TokenLevel, bool) {
	l, ok := t.Tokens[token]
	return l, ok
}

func (t *TokenAuth) username(token string) (string, bool) {
	if _, ok := t.level(token); ok {
		return "token:" + token, true
	}
	return "", false
}

func (t *TokenAuth) permits(token, action string) bool {
	if l, ok := t.level(token); ok {
		switch l {
		case LevelWrite:
			return true
		case LevelRead:
			return action == "pull"
		}
	}
	return false
}

// lookupMinted resolves a bearer token issued by IssueToken, pruning it when
// expired.
func (t *TokenAuth) lookupMinted(token string) (*mintedToken, bool) {
	t.mintedMu.Lock()
	defer t.mintedMu.Unlock()
	m, ok := t.minted[token]
	if !ok {
		return nil, false
	}
	if time.Now().After(m.expires) {
		delete(t.minted, token)
		return nil, false
	}
	return m, true
}

// Authenticate implements Auth. It accepts Bearer/token prefixes, a bare
// token, Basic (user:token), and the X-NuGet-ApiKey header. The returned
// username identifies ANY valid credential (read or write); callers that need
// a privilege decision must use CheckBearer/CheckToken/permits.
func (t *TokenAuth) Authenticate(ctx context.Context, r *http.Request) string {
	raw := r.Header.Get("Authorization")
	if _, rest, ok := cutPrefix(raw, "Bearer "); ok {
		if m, ok := t.lookupMinted(rest); ok {
			return m.username
		}
		if u, ok := t.username(rest); ok {
			return u
		}
	}
	if prefix, ok := stripTokenPrefix(raw); ok {
		if u, ok := t.username(prefix); ok {
			return u
		}
	}
	// Bare token (cargo registry token sends Authorization: <token>).
	if raw != "" && !hasAnyPrefix(raw, "Bearer ", "token ", "Token ", "Basic ") {
		if u, ok := t.username(raw); ok {
			return u
		}
	}
	if basic, ok := strings.CutPrefix(raw, "Basic "); ok {
		if decoded, err := base64.StdEncoding.DecodeString(basic); err == nil {
			if _, secret, ok := strings.Cut(string(decoded), ":"); ok {
				if u, ok := t.username(strings.TrimSpace(secret)); ok {
					return u
				}
			}
		}
	}
	if key := r.Header.Get("X-NuGet-ApiKey"); key != "" {
		if u, ok := t.username(key); ok {
			return u
		}
	}
	return ""
}

// CheckBearer implements Auth. The token may be a minted bearer token (whose
// grant bounds the allowed action) or a static token checked by level.
func (t *TokenAuth) CheckBearer(ctx context.Context, token, wantedScope string) (string, bool) {
	_, action, ok := splitAction(wantedScope)
	if !ok {
		return "", false
	}
	if m, ok := t.lookupMinted(token); ok {
		if !actionAllowedByScopes(action, m.scopes, m.level) {
			return "", false
		}
		return m.username, true
	}
	if t.permits(token, action) {
		if u, ok := t.username(token); ok {
			return u, true
		}
	}
	return "", false
}

// actionAllowedByScopes reports whether `action` (push/delete) is covered by
// the minted grant. Pull is always allowed for a minted token.
func actionAllowedByScopes(action string, scopes []string, level TokenLevel) bool {
	switch action {
	case "pull", "":
		return true
	}
	if level != LevelWrite {
		return false
	}
	for _, s := range scopes {
		if i := strings.LastIndex(s, ":"); i >= 0 {
			for _, a := range strings.Split(s[i+1:], ",") {
				if a == action || a == "*" {
					return true
				}
			}
		}
	}
	return false
}

func (t *TokenAuth) CheckToken(ctx context.Context, token string) (string, bool) {
	return t.username(token)
}

func (t *TokenAuth) CheckBasic(ctx context.Context, _, pass string) bool {
	_, ok := t.level(pass)
	return ok
}

// IssueToken implements Auth. It mints a RANDOM bearer token bound to the
// grantee's privilege: a write-level principal gets a push-capable token, a
// read-level (or anonymous) principal gets a pull-only token. The static
// write token is NEVER handed back to a client.
func (t *TokenAuth) IssueToken(ctx context.Context, username string, scopes []string, ttl time.Duration) string {
	if ttl <= 0 {
		ttl = time.Hour
	}
	level := LevelRead
	if u, ok := strings.CutPrefix(username, "token:"); ok {
		if l, ok := t.level(u); ok && l == LevelWrite {
			level = LevelWrite
		}
	}
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is unrecoverable; fail closed (no token).
		return ""
	}
	tok := "ak_m_" + base64.RawURLEncoding.EncodeToString(b[:])
	t.mintedMu.Lock()
	// Opportunistically prune expired entries so the table stays bounded.
	now := time.Now()
	for k, m := range t.minted {
		if now.After(m.expires) {
			delete(t.minted, k)
		}
	}
	t.minted[tok] = &mintedToken{username: username, scopes: scopes, level: level, expires: now.Add(ttl)}
	t.mintedMu.Unlock()
	return tok
}

// --- small helpers (kept internal to avoid depending on stdlib extras) -------

func splitTokens(s string) []string {
	var out []string
	cur := ""
	for _, c := range s {
		if c == ',' || c == ' ' || c == '\n' || c == '\t' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(c)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// splitOnce splits s at the first sep. It reports false when sep is absent or
// when either side is empty.
func splitOnce(s string, sep byte) ([2]string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			if i == 0 || i == len(s)-1 {
				return [2]string{}, false
			}
			return [2]string{s[:i], s[i+1:]}, true
		}
	}
	return [2]string{}, false
}

func cutPrefix(s, prefix string) (string, string, bool) {
	if strings.HasPrefix(s, prefix) {
		return prefix, s[len(prefix):], true
	}
	return "", s, false
}

func stripTokenPrefix(raw string) (string, bool) {
	for _, p := range []string{"Bearer ", "token ", "Token "} {
		if strings.HasPrefix(raw, p) {
			return raw[len(p):], true
		}
	}
	return "", false
}

func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func splitAction(scope string) (repo string, action string, ok bool) {
	// wantedScope: "repository:<name>:<action>".
	for i := len(scope) - 1; i >= 0; i-- {
		if scope[i] == ':' {
			return scope[:i], scope[i+1:], true
		}
	}
	return "", "", false
}
