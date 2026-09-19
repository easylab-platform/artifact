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
//	/artifacts/debian/<distro>/dists/...   distro is the host key ("debian",
//	"ubuntu", ...) so multiple upstreams can coexist.
package debian

import (
	"net/http"
	"strings"

	"github.com/easylab-platform/artifact/core"
)

type State struct {
	Registry *artifactkit.Registry
	Auth     artifactkit.Auth
	// Hosted serves self-published packages under the same repo key as the
	// proxied archive (unsigned index for [trusted=yes] sources).
	Hosted *artifactkit.HostedHandler
}

func NewHandler(reg *artifactkit.Registry, cfg map[string]any) (http.Handler, error) {
	s := &State{Registry: reg}
	if a, ok := cfg["auth"].(artifactkit.Auth); ok {
		s.Auth = a
	}
	s.Hosted = &artifactkit.HostedHandler{
		Format:    "debian",
		Store:     artifactkit.HostedStore{Registry: reg},
		Auth:      s.Auth,
		Generator: GeneratePackages(artifactkit.HostedStore{Registry: reg}),
	}
	return s, nil
}

func init() {
	artifactkit.Register("debian", NewHandler)
	// The first path segment is the repository (its upstream may be
	// overridden per repo via -repo-upstreams / the system API).
	artifactkit.RegisterNamespace("debian", artifactkit.FirstSegNamespace)
}

// ServeHTTP dispatches one repo key: a proxied archive (debian,
// debian-security, ubuntu) or a self-published hosted repo. Hosted content
// for the key wins; otherwise the request is proxied upstream.
//
// The egress proxy preserves the original host path, so an apt source of
// "http://deb.debian.org/debian" arrives as /artifacts/debian/debian/dists/... —
// the first segment is the repository key.
func (s *State) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sub := strings.Trim(strings.TrimPrefix(r.URL.Path, "/artifacts/debian/"), "/")
	repo, rest, ok := artifactkit.SplitRepo(sub)
	if !ok {
		artifactkit.JSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if s.Hosted.Get(w, r, repo, rest) {
			return
		}
		s.proxy(w, r, repo, rest)
	case http.MethodPut, http.MethodPost:
		s.Hosted.Put(w, r, repo, rest)
	case http.MethodDelete:
		s.Hosted.Delete(w, r, repo, rest)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// proxy pulls a path through from the archive's upstream mirror.
func (s *State) proxy(w http.ResponseWriter, r *http.Request, archive, rest string) {
	path := archive + "/" + rest
	base := upstreamFor(archive, rest)
	if b := s.Registry.Upstreams.Repo("debian", archive); b != "" && base == "" {
		base = b
	}
	if base == "" {
		artifactkit.Error(w, http.StatusNotFound, "unknown archive: "+archive)
		return
	}
	// The client's source keeps the archive prefix in the URL
	// (deb.debian.org/debian/dists/...), so fetch and cache the full path.
	repo := archive + "/" + suiteOf(rest)
	ct := mediaTypeOf(rest)
	res := s.Registry.FetchCachedPath(r.Context(), "debian", repo, path, "/"+path, base,
		artifactkit.PathCachePolicy{MediaType: ct, BlobName: path})
	if !res.OK {
		artifactkit.Error(w, http.StatusNotFound, "not found upstream")
		return
	}
	if artifactkit.ServeCachedBlob(w, r, s.Registry.Blobs, r.Context(), res.Artifact, ct, "") {
		return
	}
	artifactkit.Error(w, http.StatusBadGateway, "cache error")
}

// upstreamFor picks the mirror host for an archive key + path. The archive
// key is the first path segment of the client's source URL (debian,
// debian-security, ubuntu); the suite selects the security line.
func upstreamFor(archive, path string) string {
	parts := strings.Split(path, "/")
	// Debian security archive (security.debian.org/debian-security/...).
	if archive == "debian-security" {
		return "https://security.debian.org"
	}
	// dists/<suite>/...: only the -security suite lives on the security
	// mirror, while -updates (and -backports/-proposed-updates) stay on the
	// main archive for both distros.
	if len(parts) >= 2 && parts[0] == "dists" {
		suite := parts[1]
		if strings.HasSuffix(suite, "-security") {
			switch archive {
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
	switch archive {
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
