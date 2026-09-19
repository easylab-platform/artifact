package artifactkit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
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

// OctetResponse writes a small known-in-memory body as
// application/octet-stream. It honors HEAD (no body) and sets
// Content-Length. For large/streamed content use ServeBlob.
func OctetResponse(w http.ResponseWriter, r *http.Request, data []byte) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	w.WriteHeader(http.StatusOK)
	if r != nil && r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}

// ServeBlob streams a seekable blob to the client with full HTTP semantics:
// Range/206, If-Range, HEAD (no body), Content-Length, and the given
// content type. It never buffers the whole blob in memory. When ct is empty
// it resolves by filename via mime by extension, falling back to
// application/octet-stream.
//
// ServeBlob is the single response path for artifact bytes: OCI layers, apt
// pool files, apk archives, conda packages, nix NARs, and LFS objects.
func ServeBlob(w http.ResponseWriter, r *http.Request, rd io.ReadSeeker, ct string, modTime time.Time) {
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Accept-Ranges", "bytes")
	// http.ServeContent handles Range, If-Range, If-Modified-Since, HEAD,
	// and Content-Length. A zero modTime disables conditional requests.
	http.ServeContent(w, r, "", modTime, rd)
}

// ServeBlobAt serves a blob opened from a BlobStore by digest:
// (nil, nil) from Open means the blob is absent -> 404.
func ServeBlobAt(w http.ResponseWriter, r *http.Request, store BlobStore, ctx context.Context, digest, ct string) bool {
	return ServeBlobAtNamed(w, r, store, ctx, digest, ct, "")
}

// ServeBlobAtNamed is ServeBlobAt with an optional download filename: when
// non-empty the response carries Content-Disposition: attachment (package
// managers that save the file rely on it). It streams with full
// Range/206/HEAD semantics and never buffers the blob.
func ServeBlobAtNamed(w http.ResponseWriter, r *http.Request, store BlobStore, ctx context.Context, digest, ct, filename string) bool {
	size, err := store.Stat(ctx, digest)
	if err != nil || size == nil {
		return false
	}
	rd, err := store.Open(ctx, digest)
	if err != nil || rd == nil {
		return false
	}
	defer func() { _ = rd.Close() }()
	if filename != "" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	}
	ServeBlob(w, r, rd, ct, time.Time{})
	return true
}

// ServeCachedBlob serves a cached artifact's blob with full HTTP semantics and
// replays the response metadata captured at fetch time. It is the single serve
// path for the caching adapters (netcache, pathcache trees) so a stored
// Content-Encoding / ETag / Last-Modified reaches the client verbatim — without
// Content-Encoding replay a transparently-gzipped body would be handed to the
// client unlabeled and fail to decode.
//
// ct overrides the artifact's media type when non-empty (an adapter may know a
// better type from the path). filename, when non-empty, adds a Content-
// Disposition. It returns false when the blob is absent (caller decides 404).
func ServeCachedBlob(w http.ResponseWriter, r *http.Request, store BlobStore, ctx context.Context, art Artifact, ct, filename string) bool {
	if ct == "" {
		ct = art.MediaType
	}
	if art.ContentEncoding != "" {
		w.Header().Set("Content-Encoding", art.ContentEncoding)
	}
	if art.ETag != "" {
		w.Header().Set("ETag", art.ETag)
	}
	var modTime time.Time
	if art.LastModified != "" {
		w.Header().Set("Last-Modified", art.LastModified)
		if t, err := http.ParseTime(art.LastModified); err == nil {
			modTime = t
		}
	}
	size, err := store.Stat(ctx, art.Digest)
	if err != nil || size == nil {
		return false
	}
	rd, err := store.Open(ctx, art.Digest)
	if err != nil || rd == nil {
		return false
	}
	defer func() { _ = rd.Close() }()
	if filename != "" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	}
	ServeBlob(w, r, rd, ct, modTime)
	return true
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

// AuthorizeWrite gives the universal publish gate for adapters that do not
// track package names: it is AuthorizeWriteFor driven entirely by the
// request's RepoScope. The /artifacts middleware resolves (namespace, name) for
// every format, so an adapter can call this one function and get full
// namespace-aware ownership without knowing its own naming rules.
func AuthorizeWriteScoped(w http.ResponseWriter, r *http.Request, auth Auth, reg *Registry, format string) bool {
	sc := RepoScopeFrom(r.Context())
	name := sc.Name
	if name == "" {
		name = sc.Namespace
	}
	return AuthorizeWriteFor(w, r, auth, reg, format, name)
}

// AuthorizeReadScoped is the read-side counterpart of AuthorizeWriteScoped.
func AuthorizeReadScoped(w http.ResponseWriter, r *http.Request, auth Auth, reg *Registry, format string) bool {
	sc := RepoScopeFrom(r.Context())
	name := sc.Name
	if name == "" {
		name = sc.Namespace
	}
	return AuthorizeReadFor(w, r, auth, reg, format, name)
}

// AuthorizeWriteFor is the tenancy-aware publish gate used by protocol
// adapters that know their native package name (npm name, OCI repository,
// generic repo). It performs the write-credential check of AuthorizeWrite
// and then, when the registry carries an Ownership layer, resolves the
// caller's tenant and enforces publish rights for (format, repository):
// unclaimed names are claimed by the caller's tenant, claimed names must
// match it. The error text is protocol-neutral JSON, like AuthorizeWrite.
//
// When the request carries a RepoScope (the /artifacts gate installs one for every
// format), the ownership check is made against the (namespace, name) pair, so
// a namespace — npm scope, OCI host, maven groupId — is a boundary in its own
// right. The repository argument is qualified with the scope for the flat
// fallback path.
func AuthorizeWriteFor(w http.ResponseWriter, r *http.Request, auth Auth, reg *Registry, format, repository string) bool {
	if !AuthorizeWrite(w, r, auth) {
		return false
	}
	if reg == nil || reg.Owners == nil {
		return true
	}
	tid := TenantOfRequest(r.Context(), auth, r)
	sc := RepoScopeFrom(r.Context())
	// The adapter knows the package name; if it matches the scope's name the
	// scope already tells us the namespace. Otherwise (a name the adapter
	// derived, e.g. a tarball path) qualify it under the same namespace.
	name := repository
	if sc.Namespace != "" {
		name = sc.UnscopedName(repository)
		if name == "" {
			name = repository
		}
	}
	if err := AuthorizePublish(r.Context(), reg.Owners, format, sc.Namespace, name, tid); err != nil {
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
//
// Like the write path, the check is made against the request's RepoScope when
// one is present, so a private namespace is hidden in one step.
func AuthorizeReadFor(w http.ResponseWriter, r *http.Request, auth Auth, reg *Registry, format, repository string) bool {
	if reg == nil || reg.Owners == nil {
		return true
	}
	uid := TenantOfRequest(r.Context(), auth, r)
	sc := RepoScopeFrom(r.Context())
	name := repository
	if sc.Namespace != "" {
		if n := sc.UnscopedName(repository); n != "" {
			name = n
		}
	}
	if CanRead(r.Context(), reg.Owners, format, sc.Namespace, name, uid) {
		return true
	}
	JSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found"})
	return false
}

// AuthorizeWrite gates a write operation on an optional Auth, requiring a
// WRITE-level credential (a read-level token is not enough to publish). On
// success it returns true; on failure it writes a 401/403 challenge and
// returns false (the caller should stop). auth == nil permits the write (an
// open/dev instance).
func AuthorizeWrite(w http.ResponseWriter, r *http.Request, auth Auth) bool {
	return authorizeWriteLevel(w, r, auth, "registry")
}

// AuthorizeAdmin gates the operator/admin surface (the /artifacts/system
// subtree: upstream/target/proxy config, package inventory, footprint). It
// requires a WRITE-level credential — the operator level in this codebase — on
// every method, including reads, because the response body is configuration,
// not package bytes. A realm names the challenge so a browser/proxy can
// prompt. auth == nil (dev mode) permits access, exactly like writes.
func AuthorizeAdmin(w http.ResponseWriter, r *http.Request, auth Auth, realm string) bool {
	if realm == "" {
		realm = "admin"
	}
	return authorizeWriteLevel(w, r, auth, realm)
}

// authorizeWriteLevel is the shared privilege gate: no auth configured allows
// access (open/dev instance); otherwise a request must carry a credential
// (401 challenge) that is write-capable (403). realm names the WWW-Authenticate
// challenge.
func authorizeWriteLevel(w http.ResponseWriter, r *http.Request, auth Auth, realm string) bool {
	if auth == nil {
		return true // auth disabled: anonymous writes allowed (dev mode)
	}
	username := auth.Authenticate(r.Context(), r)
	if username == "" {
		w.Header().Set("WWW-Authenticate", `Basic realm="`+realm+`"`)
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

// ServeData stores data into the CAS (idempotent) and streams it to the
// client with Range/HEAD semantics and an attachment filename. It is the
// streaming replacement for BlobResponse on pull-through paths where the
// bytes were just fetched into memory: storing first means the response is
// served from the blob store rather than re-buffered.
func ServeData(w http.ResponseWriter, r *http.Request, reg *Registry, ctx context.Context, data []byte, ct, filename string) {
	stored, err := reg.StoreAndHash(ctx, data)
	if err == nil && stored.Digest != "" {
		if ServeBlobAtNamed(w, r, reg.Blobs, ctx, stored.Digest, ct, filename) {
			return
		}
	}
	// Fallback: direct write (HEAD-aware).
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	if filename != "" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	}
	w.WriteHeader(http.StatusOK)
	if r != nil && r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}
