// Package debian implements the APT repository protocol with pull-through:
// distro indexes (Release, Packages, Sources) and .deb packages are fetched
// from the configured upstream (deb.debian.org / archive.ubuntu.com), cached
// in the CAS, and served byte-for-byte so the distribution's GPG signatures
// stay valid (UpstreamPassthrough strategy — clients keep trusting the
// distro key; local re-signing is a future phase).
//
// Wire format (apt's repository layout):
//
//	dists/<suite>/Release, Release.gpg, InRelease
//	dists/<suite>/<component>/binary-<arch>/Packages[.gz|.xz]
//	pool/<component>/<source>/<deb>
//
// Repository layout served here:
//
//	/pkgs/debian/<distro>/dists/...   distro is the host key ("debian",
//	"ubuntu", ...) so multiple upstreams can coexist.
package debian

import (
	"context"
	"net/http"
	"strings"

	"github.com/easylab-platform/artifact/core"
)

type State struct {
	Registry *artifactkit.Registry
	Auth     artifactkit.Auth
}

func NewHandler(reg *artifactkit.Registry, cfg map[string]any) (http.Handler, error) {
	s := &State{Registry: reg}
	if a, ok := cfg["auth"].(artifactkit.Auth); ok {
		s.Auth = a
	}
	return s, nil
}

func init() { artifactkit.Register("debian", NewHandler) }

// ServeHTTP dispatches /pkgs/debian/{distro}/<upstream path>.
// The upstream host is selected by distro: debian → deb.debian.org +
// security.debian.org, ubuntu → archive.ubuntu.com + security.ubuntu.com
// (archive vs security chosen by path prefix, mirroring sources.list).
func (s *State) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/pkgs/debian/")
	path = strings.TrimPrefix(path, "debian/")
	path = strings.Trim(path, "/")
	if path == "" {
		artifactkit.JSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	distro, rest, ok := strings.Cut(path, "/")
	if !ok || rest == "" {
		artifactkit.Error(w, http.StatusNotFound, "distro required")
		return
	}
	base := upstreamFor(distro, rest)
	if base == "" {
		artifactkit.Error(w, http.StatusNotFound, "unknown distro: "+distro)
		return
	}

	repo := distro + "/" + suiteOf(rest)
	// Local cache first (streamed with Range/HEAD semantics).
	if art, err := s.Registry.Meta.Get(r.Context(), "debian", repo, rest); err == nil && len(art.Blobs) > 0 {
		if artifactkit.ServeBlobAt(w, r, s.Registry.Blobs, r.Context(), art.Blobs[0].Digest, mediaTypeOf(rest)) {
			return
		}
	}
	fetched, err := s.Registry.Fetch(r.Context(), "debian", base, "/"+rest)
	if err != nil {
		artifactkit.Error(w, http.StatusNotFound, "not found upstream")
		return
	}
	if digest := storeCache(s, r.Context(), repo, rest, fetched.Data); digest != "" &&
		artifactkit.ServeBlobAt(w, r, s.Registry.Blobs, r.Context(), digest, mediaTypeOf(rest)) {
		return
	}
	artifactkit.OctetResponse(w, r, fetched.Data)
}

// upstreamFor picks the mirror host for a distro + path. The suite (or the
// archive prefix) determines which host serves it: Debian security lives at
// security.debian.org under debian-security/ + pool/updates/; Ubuntu security
// is the -security/-updates suites at security.ubuntu.com.
func upstreamFor(distro, path string) string {
	parts := strings.Split(path, "/")
	// Debian security archive prefix (security.debian.org/debian-security/...).
	if len(parts) > 0 && parts[0] == "debian-security" {
		return "https://security.debian.org"
	}
	// dists/<suite>/...: the suite suffix names the archive line.
	if len(parts) >= 2 && parts[0] == "dists" {
		suite := parts[1]
		if strings.HasSuffix(suite, "-security") || strings.HasSuffix(suite, "-updates") {
			switch distro {
			case "debian":
				return "https://security.debian.org"
			case "ubuntu":
				return "https://security.ubuntu.com"
			}
		}
	}
	// pool/updates/... is the Debian security package pool.
	if len(parts) >= 2 && parts[0] == "pool" && parts[1] == "updates" {
		return "https://security.debian.org"
	}
	switch distro {
	case "debian":
		return "https://deb.debian.org"
	case "ubuntu":
		return "https://archive.ubuntu.com"
	}
	return ""
}

// suiteOf extracts the suite (first path segment under dists/, or "pool" for
// package paths) so cache entries group by distribution line.
func suiteOf(rest string) string {
	parts := strings.Split(rest, "/")
	if len(parts) >= 2 && parts[0] == "dists" {
		return parts[1]
	}
	return "pool"
}

// storeCache persists a fetched path into the CAS + index (best-effort).
func storeCache(s *State, ctx context.Context, repo, name string, data []byte) string {
	stored, err := s.Registry.StoreAndHash(ctx, data)
	if err != nil {
		return ""
	}
	artifactkit.LogMetaErr("debian cache", s.Registry.Meta.Put(ctx, artifactkit.Artifact{
		Format: "debian", Repository: repo, Version: name,
		MediaType: mediaTypeOf(name), Digest: stored.Digest,
		Blobs:  []artifactkit.Descriptor{{Digest: stored.Digest, Size: stored.Size, Name: name}},
		Source: "pull",
	}))
	return stored.Digest
}

// mediaTypeOf maps well-known apt repository files to media types.
func mediaTypeOf(name string) string {
	switch {
	case name == "InRelease" || strings.HasSuffix(name, "Release.gpg") || strings.HasSuffix(name, "Release"):
		return "application/pgp-signature"
	case strings.HasSuffix(name, ".deb"):
		return "application/vnd.debian.binary-package"
	case strings.HasSuffix(name, ".gz"):
		return "application/gzip"
	case strings.HasSuffix(name, ".xz"):
		return "application/x-xz"
	default:
		return "text/plain"
	}
}
