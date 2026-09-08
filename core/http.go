package artifactkit

import (
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
	// A named principal must actually be write-capable. Check via the bearer
	// path with a push scope; static read tokens resolve to a username but
	// fail this check.
	if _, ok := auth.CheckBearer(r.Context(), bearerOf(r), "repository:*:push"); !ok {
		JSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "write-level credential required"})
		return false
	}
	return true
}

// bearerOf extracts the raw Bearer credential ("" when absent).
func bearerOf(r *http.Request) string {
	raw := r.Header.Get("Authorization")
	if rest, ok := strings.CutPrefix(raw, "Bearer "); ok {
		return rest
	}
	return raw
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
