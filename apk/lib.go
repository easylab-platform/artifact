// Package apk implements the Alpine Linux APK repository protocol with
// pull-through: APKINDEX.tar.gz indexes and .apk packages are fetched from
// the configured upstream (dl-cdn.alpinelinux.org), cached in the CAS, and
// served byte-for-byte so the distribution's signatures stay valid.
//
// Wire format (apk's default repository layout):
//
//	/v3.21/main/x86_64/APKINDEX.tar.gz   (gzipped tar of APKINDEX + signature)
//	/v3.21/main/x86_64/<name>-<ver>.apk
//
// Repository layout served here (the "repository root" is the repository
// name — a fixed "main" repo backed by the upstream suite/component):
//
//	/artifacts/apk/<repo>/...  where <repo> encodes suite/component, e.g. "main"
package apk

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"

	"github.com/easylab-platform/artifact/core"
)

type State struct {
	Registry *artifactkit.Registry
	Auth     artifactkit.Auth
	// SelfBase is the external base URL for rewritten APKINDEX URLs (the apk
	// client fetches package files relative to the index URL, so no rewrite
	// is needed; SelfBase is reserved for future hosted-repo support).
	SelfBase string
	// Hosted serves /artifacts/apk/hosted/<repo>/... (self-published packages).
	Hosted *artifactkit.HostedHandler
}

func NewHandler(reg *artifactkit.Registry, cfg map[string]any) (http.Handler, error) {
	s := &State{Registry: reg}
	if a, ok := cfg["auth"].(artifactkit.Auth); ok {
		s.Auth = a
	}
	if v, ok := cfg["self_base"].(string); ok {
		s.SelfBase = v
	}
	s.Hosted = &artifactkit.HostedHandler{
		Format:    "apk",
		Store:     artifactkit.HostedStore{Registry: reg},
		Auth:      s.Auth,
		Generator: GenerateAPKINDEX(artifactkit.HostedStore{Registry: reg}),
	}
	return s, nil
}

func init() {
	artifactkit.Register("apk", NewHandler)
	// The first path segment is the repository (its upstream may be
	// overridden per repo via -repo-upstreams / the system API).
	artifactkit.RegisterNamespace("apk", artifactkit.FirstSegNamespace)
}

// ServeHTTP dispatches one repo key: a proxied repository (v<ver>/<component>
// /<arch>) or a self-published hosted repo. Hosted content wins for the key;
// otherwise the request is proxied upstream.
func (s *State) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sub := strings.Trim(strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/artifacts/apk/"), "apk/"), "/")
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
		// APK repo keys span multiple path segments (v3.24/main/x86_64), so
		// the whole format-relative path is the proxy path here.
		s.fetch(w, r, sub)
	case http.MethodPut, http.MethodPost:
		s.Hosted.Put(w, r, repo, rest)
	case http.MethodDelete:
		s.Hosted.Delete(w, r, repo, rest)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// fetch serves a repository path (index or package) through the local CAS
// with upstream pull-through. Bytes are stored by digest so repeated fetches
// hit the cache and the APKINDEX signature survives verbatim. Freshness
// (single-flight, TTL, conditional revalidation) is shared with the other
// path-tree protocols via FetchCachedPath.
func (s *State) fetch(w http.ResponseWriter, r *http.Request, repoPath string) {
	repo, name := splitRepoPath(repoPath)
	ct := mediaTypeOf(name)
	res := s.Registry.FetchCachedPath(r.Context(), "apk", repo, name, "/"+repoPath, "",
		artifactkit.PathCachePolicy{MediaType: ct, BlobName: name})
	if !res.OK {
		artifactkit.Error(w, http.StatusNotFound, "not found upstream")
		return
	}
	if artifactkit.ServeCachedBlob(w, r, s.Registry.Blobs, r.Context(), res.Artifact, ct, "") {
		return
	}
	artifactkit.Error(w, http.StatusBadGateway, "cache error")
}

// splitRepoPath splits "v3.21/main/x86_64/APKINDEX.tar.gz" into
// repo="v3.21/main/x86_64" and name="APKINDEX.tar.gz" (the last segment).
// The repository (suite/component/arch) is the version key; the file is the
// version value.
func splitRepoPath(repoPath string) (repo, name string) {
	i := strings.LastIndexByte(repoPath, '/')
	if i < 0 {
		return "main", repoPath
	}
	return repoPath[:i], repoPath[i+1:]
}

// mediaTypeOf maps well-known apk repository files to media types.
func mediaTypeOf(name string) string {
	switch {
	case name == "APKINDEX.tar.gz":
		return "application/gzip"
	case strings.HasSuffix(name, ".apk"):
		return "application/vnd.alpine.package.tar"
	default:
		return "application/octet-stream"
	}
}

// RewrittenIndexURLs is a helper for tests: verifies an APKINDEX tar.gz
// contains the expected package entries (no URL rewriting needed — apk
// clients resolve package files relative to the index URL).
func indexContains(indexGz []byte, needle string) bool {
	gz, err := gzip.NewReader(bytes.NewReader(indexGz))
	if err != nil {
		return false
	}
	defer func() { _ = gz.Close() }()
	raw, err := io.ReadAll(gz)
	if err != nil {
		return false
	}
	return strings.Contains(string(raw), needle)
}
