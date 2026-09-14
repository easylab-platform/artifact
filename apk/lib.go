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
//	/pkgs/apk/<repo>/...  where <repo> encodes suite/component, e.g. "main"
package apk

import (
	"bytes"
	"compress/gzip"
	"context"
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
}

func NewHandler(reg *artifactkit.Registry, cfg map[string]any) (http.Handler, error) {
	s := &State{Registry: reg}
	if a, ok := cfg["auth"].(artifactkit.Auth); ok {
		s.Auth = a
	}
	if v, ok := cfg["self_base"].(string); ok {
		s.SelfBase = v
	}
	return s, nil
}

func init() { artifactkit.Register("apk", NewHandler) }

// ServeHTTP dispatches /pkgs/apk/{repo}/{suite}/{arch}/{file}.
func (s *State) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/pkgs/apk/")
	path = strings.TrimPrefix(path, "apk/")
	path = strings.Trim(path, "/")
	if path == "" {
		artifactkit.JSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	s.fetch(w, r, path)
}

// fetch serves a repository path (index or package) through the local CAS
// with upstream pull-through. Bytes are stored by digest so repeated fetches
// hit the cache and the APKINDEX signature survives verbatim.
func (s *State) fetch(w http.ResponseWriter, r *http.Request, repoPath string) {
	// Local cache first: the (repo, path) pair is stored as an artifact
	// version whose blobs[0] holds the bytes.
	repo, name := splitRepoPath(repoPath)
	if art, err := s.Registry.Meta.Get(r.Context(), "apk", repo, name); err == nil && len(art.Blobs) > 0 {
		rd, err := s.Registry.Blobs.Open(r.Context(), art.Blobs[0].Digest)
		if err == nil && rd != nil {
			data, _ := io.ReadAll(rd)
			_ = rd.Close()
			artifactkit.OctetResponse(w, data)
			return
		}
	}
	base := s.Registry.Upstreams.Get("apk")
	if base == "" {
		artifactkit.Error(w, http.StatusNotFound, "apk upstream disabled (air-gap)")
		return
	}
	fetched, err := s.Registry.Fetch(r.Context(), "apk", base, "/"+repoPath)
	if err != nil {
		artifactkit.Error(w, http.StatusNotFound, "not found upstream")
		return
	}
	storeCache(s, r.Context(), repo, name, fetched.Data)
	artifactkit.OctetResponse(w, fetched.Data)
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

// storeCache persists a fetched path into the CAS + index. Index files get
// a per-path version key; packages likewise. Best-effort: cache failures do
// not break the response.
func storeCache(s *State, ctx context.Context, repo, name string, data []byte) {
	stored, err := s.Registry.StoreAndHash(ctx, data)
	if err != nil {
		return
	}
	artifactkit.LogMetaErr("apk cache", s.Registry.Meta.Put(ctx, artifactkit.Artifact{
		Format: "apk", Repository: repo, Version: name,
		MediaType: mediaTypeOf(name), Digest: stored.Digest,
		Blobs:  []artifactkit.Descriptor{{Digest: stored.Digest, Size: stored.Size, Name: name}},
		Source: "pull",
	}))
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
