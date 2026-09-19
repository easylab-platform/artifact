// Package conda implements the Conda channel protocol with pull-through:
// repodata.json per subdir, channeldata.json, and package archives
// (.conda/.tar.bz2) are fetched from the configured upstream
// (repo.anaconda.com / conda.anaconda.org), cached in the CAS, and served
// byte-for-byte (conda verifies package hashes recorded in repodata).
//
// Wire format (a conda channel layout):
//
//	/channeldata.json
//	/<subdir>/repodata.json        (e.g. linux-64, noarch)
//	/<subdir>/<pkg>.conda|.tar.bz2
//
// Repository layout served here:
//
//	/artifacts/conda/{channel}/{path}   channel = repo key ("main", "conda-forge", ...)
package conda

import (
	"context"
	"net/http"
	"strings"

	"github.com/easylab-platform/artifact/core"
)

type State struct {
	Registry *artifactkit.Registry
	Auth     artifactkit.Auth
	// ChannelUpstreams maps channel keys to upstream bases; nil uses the
	// single "conda" upstream default.
	ChannelUpstreams map[string]string
	// Hosted serves /artifacts/conda/hosted/<channel>/... (self-published).
	Hosted *artifactkit.HostedHandler
}

func NewHandler(reg *artifactkit.Registry, cfg map[string]any) (http.Handler, error) {
	s := &State{Registry: reg}
	if a, ok := cfg["auth"].(artifactkit.Auth); ok {
		s.Auth = a
	}
	if m, ok := cfg["channel_upstreams"].(map[string]string); ok {
		s.ChannelUpstreams = m
	}
	s.Hosted = &artifactkit.HostedHandler{
		Format:    "conda",
		Store:     artifactkit.HostedStore{Registry: reg},
		Auth:      s.Auth,
		Generator: GenerateRepodata(artifactkit.HostedStore{Registry: reg}),
	}
	return s, nil
}

func init() {
	artifactkit.Register("conda", NewHandler)
	// The first path segment is the repository (its upstream may be
	// overridden per repo via -repo-upstreams / the system API).
	artifactkit.RegisterNamespace("conda", artifactkit.FirstSegNamespace)
}

// ServeHTTP dispatches one repo key: a proxied channel (pkgs/main,
// conda-forge, ...) or a self-published hosted channel. Hosted content wins
// for the key; otherwise the request is proxied upstream.
func (s *State) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sub := strings.Trim(strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/artifacts/conda/"), "conda/"), "/")
	repo, rest, ok := artifactkit.SplitRepo(sub)
	if !ok {
		artifactkit.Error(w, http.StatusNotFound, "path required")
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if s.Hosted.Get(w, r, repo, rest) {
			return
		}
		s.proxy(w, r, sub, repo, rest)
	case http.MethodPut, http.MethodPost:
		s.Hosted.Put(w, r, repo, rest)
	case http.MethodDelete:
		s.Hosted.Delete(w, r, repo, rest)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// proxy pulls a channel path through from its upstream. The client's original
// path is preserved by the egress rewrite, so it already mirrors the upstream
// layout exactly:
//
//	repo.anaconda.com/pkgs/<channel>/<subdir>/repodata.json
//	conda.anaconda.org/<channel>/<subdir>/repodata.json
//
// path is the full format-relative path and channel its first segment.
func (s *State) proxy(w http.ResponseWriter, r *http.Request, path, channel, rest string) {
	base := s.upstreamFor(channel)
	if base == "" {
		artifactkit.Error(w, http.StatusNotFound, "unknown conda channel: "+channel)
		return
	}

	repo := channel + "/" + subdirOf(rest)
	if art, err := s.Registry.Meta.Get(r.Context(), "conda", repo, path); err == nil && len(art.Blobs) > 0 {
		if artifactkit.ServeBlobAt(w, r, s.Registry.Blobs, r.Context(), art.Blobs[0].Digest, mediaTypeOf(path)) {
			return
		}
	}
	fetched, err := s.Registry.FetchFor(r.Context(), "conda", channel, base, "/"+path)
	if err != nil {
		artifactkit.Error(w, http.StatusNotFound, "not found upstream")
		return
	}
	if digest := storeCache(s, r.Context(), repo, path, fetched.Data); digest != "" &&
		artifactkit.ServeBlobAt(w, r, s.Registry.Blobs, r.Context(), digest, mediaTypeOf(path)) {
		return
	}
	artifactkit.OctetResponse(w, r, fetched.Data)
}

// upstreamFor resolves the upstream base for a channel key: a per-channel
// explicit override (the legacy config map, then -repo-upstreams) wins, else
// the format default.
func (s *State) upstreamFor(channel string) string {
	if s.ChannelUpstreams != nil {
		if b, ok := s.ChannelUpstreams[channel]; ok {
			return b
		}
	}
	return s.Registry.Upstreams.Repo("conda", channel)
}

// subdirOf extracts the conda subdir for cache grouping ("linux-64",
// "noarch", or the top file for channeldata).
func subdirOf(rest string) string {
	if rest == "channeldata.json" {
		return ""
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[:i]
	}
	return rest
}

// storeCache persists a fetched path (best-effort).
func storeCache(s *State, ctx context.Context, repo, name string, data []byte) string {
	return s.Registry.StorePathBlob(ctx, "conda", repo, name, name, mediaTypeOf(name), data)
}

func mediaTypeOf(name string) string {
	switch {
	case strings.HasSuffix(name, ".json") || name == "channeldata.json":
		return "application/json"
	case strings.HasSuffix(name, ".conda"):
		return "application/zip"
	case strings.HasSuffix(name, ".tar.bz2"):
		return "application/x-tar"
	case strings.HasSuffix(name, ".zst"):
		return "application/zstd"
	default:
		return "application/octet-stream"
	}
}
