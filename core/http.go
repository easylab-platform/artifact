package artifactkit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// DefaultMaxBody is the default request body limit (8 GiB: large OCI layers
// legitimately reach multi-GB).
const DefaultMaxBody = 8 << 30

// maxBody returns the effective request body limit. Override with
// ARTIFACT_MAX_BODY (bytes).
func maxBody() int64 {
	if v := os.Getenv("ARTIFACT_MAX_BODY"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return DefaultMaxBody
}

// LimitBody bounds a request body (http.MaxBytesReader). Adapters must call
// this on every upload entrypoint before reading r.Body; an over-limit read
// fails with *http.MaxBytesError which LimitBodyError maps to 413.
func LimitBody(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody())
}

// IsBodyTooLarge reports whether err is a request-body size violation.
func IsBodyTooLarge(err error) bool {
	_, ok := err.(*http.MaxBytesError)
	return ok
}

// JSON writes an application/json response with the given body value.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Text writes a response with an explicit content type.
func Text(w http.ResponseWriter, status int, body, contentType string) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// Error writes an {"ok":false,"error":msg} JSON error response.
func Error(w http.ResponseWriter, status int, msg string) {
	JSON(w, status, map[string]any{"ok": false, "error": msg})
}

// BlobResponse writes an application/octet-stream body with a content-disposition.
func BlobResponse(w http.ResponseWriter, data []byte, filename string) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// OctetResponse writes an application/octet-stream body without a filename.
func OctetResponse(w http.ResponseWriter, data []byte) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// URLencode percent-encodes a path segment (RFC 3986 unreserved set plus ~
// stay literal, everything else is %XX uppercase).
func URLencode(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case ('A' <= c && c <= 'Z') || ('a' <= c && c <= 'z') || ('0' <= c && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0f])
		}
	}
	return b.String()
}

// AuthorizeWrite gates a write operation on an optional Auth, requiring a
// WRITE-level credential (a read-level token is not enough to publish).
// On success it returns true; on failure it writes a 401/403 challenge and
// returns false (the caller should stop).
// AuthorizeWriteFor is the tenancy-aware publish gate used by protocol
// adapters that know their native package name (npm name, OCI repository,
// generic repo). It performs the write-credential check of AuthorizeWrite
// and then, when the registry carries an Ownership layer, resolves the
// caller's tenant and enforces publish rights for (format, repository):
// unclaimed names are claimed by the caller's tenant, claimed names must
// match it. The error text is protocol-neutral JSON, like AuthorizeWrite.
func AuthorizeWriteFor(w http.ResponseWriter, r *http.Request, auth Auth, reg *Registry, format, repository string) bool {
	if !AuthorizeWrite(w, r, auth) {
		return false
	}
	if reg == nil || reg.Owners == nil {
		return true
	}
	tid := TenantOfRequest(r.Context(), auth, r)
	if err := reg.Owners.AuthorizePublish(r.Context(), format, repository, tid); err != nil {
		JSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": err.Error()})
		return false
	}
	return true
}

// TenantOfRequest resolves the caller's tenant through the Auth (StoreAuth
// bridges TenantTokenStore); anonymous or unresolvable callers map to the
// default tenant 1.
func TenantOfRequest(ctx context.Context, auth Auth, r *http.Request) int64 {
	tr, ok := auth.(TenantResolver)
	if !ok || auth == nil {
		return 1
	}
	if tid := tr.TenantID(ctx, r); tid != 0 {
		return tid
	}
	return 1
}

// TenantResolver is implemented by Auth implementations that can resolve a
// request's tenant (StoreAuth does, bridging TenantTokenStore).
type TenantResolver interface {
	TenantID(ctx context.Context, r *http.Request) int64
}

// AuthorizeReadFor gates a READ of one registry name on the ownership layer:
// public/unclaimed names are readable by everyone; a private name requires a
// role on its scope (mapped repo members, else the owning user). When the
// registry carries no Ownership layer the read is allowed. It writes a 404 on
// denial (not 403: a hidden name must not reveal its existence) and returns
// false so the caller stops.
func AuthorizeReadFor(w http.ResponseWriter, r *http.Request, auth Auth, reg *Registry, format, repository string) bool {
	if reg == nil || reg.Owners == nil {
		return true
	}
	uid := TenantOfRequest(r.Context(), auth, r)
	if reg.Owners.CanRead(r.Context(), format, repository, uid) {
		return true
	}
	JSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found"})
	return false
}

func AuthorizeWrite(w http.ResponseWriter, r *http.Request, auth Auth) bool {
	if auth == nil {
		return true // auth disabled: anonymous writes allowed (dev mode)
	}
	username := auth.Authenticate(r.Context(), r)
	if username == "" {
		w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
		JSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "authentication required"})
		return false
	}
	// A named principal must actually be write-capable. Protocols present the
	// SAME static token through different schemes (Bearer, bare
	// Authorization, Basic password, X-NuGet-ApiKey), so every candidate the
	// request could be carrying is checked — not just the Bearer header.
	if !WriteCapable(r.Context(), auth, r) {
		JSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "write-level credential required"})
		return false
	}
	return true
}

// WriteCapable reports whether the request carries a write-level credential
// in ANY of the protocol credential schemes. It is the scheme-agnostic
// privilege check behind AuthorizeWrite (default implementation).
func WriteCapable(ctx context.Context, a Auth, r *http.Request) bool {
	for _, tok := range credentialCandidates(r) {
		if _, ok := a.CheckBearer(ctx, tok, "repository:*:push"); ok {
			return true
		}
	}
	return false
}

// credentialCandidates extracts every raw token the request may be carrying:
// the Bearer credential, a bare Authorization value, the Basic password, and
// the NuGet API-key header. Duplicates are collapsed.
func credentialCandidates(r *http.Request) []string {
	raw := r.Header.Get("Authorization")
	var out []string
	seen := func(s string) bool {
		for _, o := range out {
			if o == s {
				return true
			}
		}
		return false
	}
	add := func(s string) {
		if s != "" && !seen(s) {
			out = append(out, s)
		}
	}
	if rest, ok := strings.CutPrefix(raw, "Bearer "); ok {
		add(rest)
	} else if rest, ok := strings.CutPrefix(raw, "token "); ok {
		add(rest)
	} else if rest, ok := strings.CutPrefix(raw, "Token "); ok {
		add(rest)
	} else if rest, ok := strings.CutPrefix(raw, "Basic "); ok {
		if decoded, err := base64.StdEncoding.DecodeString(rest); err == nil {
			if _, pass, ok := strings.Cut(string(decoded), ":"); ok {
				add(strings.TrimSpace(pass))
			}
		}
	} else if raw != "" {
		// Bare token (cargo style: Authorization: <token>).
		add(raw)
	}
	add(r.Header.Get("X-NuGet-ApiKey"))
	return out
}

// WriteReadErr maps a request-body read failure to a status. A body over the
// MaxBytesReader cap is 413; anything else is a 400.
func WriteReadErr(w http.ResponseWriter, err error) {
	if IsBodyTooLarge(err) {
		Error(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	Error(w, http.StatusBadRequest, "error reading request body")
}
