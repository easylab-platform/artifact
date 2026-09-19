package artifactkit

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/easylab-platform/artifact/targets"
)

// resolveAuthHeader turns a target's auth policy into an Authorization header
// for an upstream request. Precedence, when a client credential is available:
//
//	passthrough : the CLIENT's own Authorization is reused, so a private
//	              repository the caller is already authorized for needs no
//	              configuration.
//	basic       : "Basic base64(user:secret)", secret read from a reference.
//	bearer      : "Bearer <secret>".
//
// clientAuth is the request's Authorization header ("" when none). A target
// with no policy returns ("", nil) meaning "anonymous". A policy whose secret
// reference cannot be resolved is an error the caller surfaces as a 502, never
// a silent anonymous request.
func resolveAuthHeader(a targets.Auth, clientAuth string) (string, error) {
	switch a.Mode {
	case "", targets.AuthPassthrough:
		// Passthrough: prefer the client's credential, else anonymous.
		return clientAuth, nil
	case targets.AuthBasic:
		secret, err := readSecret(a.Secret)
		if err != nil {
			return "", err
		}
		raw := a.Username + ":" + secret
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(raw)), nil
	case targets.AuthBearer:
		secret, err := readSecret(a.Secret)
		if err != nil {
			return "", err
		}
		if secret == "" {
			return "", fmt.Errorf("bearer auth: empty secret reference")
		}
		return "Bearer " + secret, nil
	default:
		return "", fmt.Errorf("unknown auth mode %q", a.Mode)
	}
}

// readSecret resolves a secret reference. Supported forms:
//
//	env:NAME       an environment variable (the default when no scheme)
//	file:/path     a file whose contents (trimmed) are the secret
//	literal:VALUE  a literal value (escape hatch; avoid for real secrets)
//
// An empty reference resolves to "" (anonymous), not an error.
func readSecret(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	scheme, rest, ok := strings.Cut(ref, ":")
	if !ok {
		return strings.TrimSpace(os.Getenv(ref)), nil // bare env var name
	}
	switch scheme {
	case "env":
		v := strings.TrimSpace(os.Getenv(rest))
		if v == "" {
			return "", fmt.Errorf("auth secret: env %q is empty or unset", rest)
		}
		return v, nil
	case "file":
		b, err := os.ReadFile(rest)
		if err != nil {
			return "", fmt.Errorf("auth secret: read %s: %w", rest, err)
		}
		return strings.TrimSpace(string(b)), nil
	case "literal":
		return rest, nil
	default:
		// Treat "scheme:rest" as a bare env var name containing a colon.
		return strings.TrimSpace(os.Getenv(ref)), nil
	}
}

// TargetAuthHeader resolves a target's auth policy into an Authorization
// header, reusing clientAuth for passthrough. It is exported so the netcache
// protocol (which fetches a bare URL rather than a Remote) can apply the same
// policy. "" means anonymous.
func TargetAuthHeader(a targets.Auth, clientAuth string) (string, error) {
	return resolveAuthHeader(a, clientAuth)
}

// withTargetAuth returns remote with the target's auth applied. A nil/empty
// policy leaves it unchanged. The client's own credential (from the request)
// is honored for passthrough.
func withTargetAuth(remote *Remote, t targets.Target, clientAuth string) (*Remote, error) {
	h, err := resolveAuthHeader(t.Auth, clientAuth)
	if err != nil {
		return nil, err
	}
	if h == "" {
		return remote, nil
	}
	return remote.WithHeader("Authorization", h), nil
}
