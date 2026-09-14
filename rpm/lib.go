// Package rpm implements the YUM/DNF repository protocol with pull-through:
// repodata (repomd.xml, primary/other/filelists XML) and .rpm packages are
// fetched from the configured upstream, cached in the CAS, and served
// byte-for-byte so repository signatures stay valid.
//
// Wire format (a yum repo layout):
//
//	repodata/repomd.xml           (index of the metadata files, signed)
//	repodata/*-primary.xml.gz     (package index)
//	.../packages/.../x.rpm
//
// Repository layout served here:
//
//	/pkgs/rpm/<repo>/<path>   where <repo> is a user-chosen repository key
//	(e.g. "fedorarelease41"); the upstream base for each repo key is resolved
//	from the upstream table by repo name.
package rpm

import (
	"context"
	"net/http"
	"strings"

	"github.com/easylab-platform/artifact/core"
)

type State struct {
	Registry *artifactkit.Registry
	Auth     artifactkit.Auth
	// RepoUpstreams maps repository keys to upstream bases. When nil, the
	// single "rpm" upstream from the registry table is used for every repo.
	RepoUpstreams map[string]string
	// Hosted serves /pkgs/rpm/hosted/<repo>/... (self-published packages).
	Hosted *artifactkit.HostedHandler
}

func NewHandler(reg *artifactkit.Registry, cfg map[string]any) (http.Handler, error) {
	s := &State{Registry: reg}
	if a, ok := cfg["auth"].(artifactkit.Auth); ok {
		s.Auth = a
	}
	if m, ok := cfg["repo_upstreams"].(map[string]string); ok {
		s.RepoUpstreams = m
	}
	s.Hosted = &artifactkit.HostedHandler{
		Format:    "rpm",
		Store:     artifactkit.HostedStore{Registry: reg},
		Auth:      s.Auth,
		Generator: GenerateRepodata(artifactkit.HostedStore{Registry: reg}),
	}
	return s, nil
}

func init() { artifactkit.Register("rpm", NewHandler) }

// ServeHTTP dispatches /pkgs/rpm/{repo}/{upstream path}.
func (s *State) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sub := strings.TrimPrefix(r.URL.Path, "/pkgs/rpm/")
	sub = strings.TrimPrefix(sub, "rpm/")
	if strings.HasPrefix(sub, "hosted/") {
		s.Hosted.ServeHTTP(w, r, sub, "/pkgs/rpm/")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	path := strings.Trim(sub, "/")
	if path == "" {
		artifactkit.JSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	repo, rest, ok := strings.Cut(path, "/")
	if !ok || rest == "" {
		artifactkit.Error(w, http.StatusNotFound, "repo required")
		return
	}
	base := s.upstreamFor(repo)
	if base == "" {
		artifactkit.Error(w, http.StatusNotFound, "unknown rpm repo: "+repo)
		return
	}

	// Local cache first (streamed with Range/HEAD semantics).
	if art, err := s.Registry.Meta.Get(r.Context(), "rpm", repo, rest); err == nil && len(art.Blobs) > 0 {
		if artifactkit.ServeBlobAt(w, r, s.Registry.Blobs, r.Context(), art.Blobs[0].Digest, mediaTypeOf(rest)) {
			return
		}
	}
	fetched, err := s.Registry.Fetch(r.Context(), "rpm", base, "/"+rest)
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

// upstreamFor resolves the upstream base for a repository key.
func (s *State) upstreamFor(repo string) string {
	if s.RepoUpstreams != nil {
		if b, ok := s.RepoUpstreams[repo]; ok {
			return b
		}
		return ""
	}
	return s.Registry.Upstreams.Get("rpm")
}

// storeCache persists a fetched path into the CAS + index (best-effort).
func storeCache(s *State, ctx context.Context, repo, name string, data []byte) string {
	stored, err := s.Registry.StoreAndHash(ctx, data)
	if err != nil {
		return ""
	}
	artifactkit.LogMetaErr("rpm cache", s.Registry.Meta.Put(ctx, artifactkit.Artifact{
		Format: "rpm", Repository: repo, Version: name,
		MediaType: mediaTypeOf(name), Digest: stored.Digest,
		Blobs:  []artifactkit.Descriptor{{Digest: stored.Digest, Size: stored.Size, Name: name}},
		Source: "pull",
	}))
	return stored.Digest
}

// mediaTypeOf maps well-known yum repository files to media types.
func mediaTypeOf(name string) string {
	switch {
	case strings.HasSuffix(name, "repomd.xml"):
		return "application/xml"
	case strings.HasSuffix(name, ".rpm"):
		return "application/x-rpm"
	case strings.HasSuffix(name, ".xml.gz") || strings.HasSuffix(name, ".gz"):
		return "application/gzip"
	case strings.HasSuffix(name, ".xml.zck"):
		return "application/zstd"
	default:
		return "application/octet-stream"
	}
}
