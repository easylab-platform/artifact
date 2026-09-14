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
//	/pkgs/conda/{channel}/{path}   channel = repo key ("main", "conda-forge", ...)
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
}

func NewHandler(reg *artifactkit.Registry, cfg map[string]any) (http.Handler, error) {
	s := &State{Registry: reg}
	if a, ok := cfg["auth"].(artifactkit.Auth); ok {
		s.Auth = a
	}
	if m, ok := cfg["channel_upstreams"].(map[string]string); ok {
		s.ChannelUpstreams = m
	}
	return s, nil
}

func init() { artifactkit.Register("conda", NewHandler) }

// ServeHTTP dispatches /pkgs/conda/{channel}/{path}.
func (s *State) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/pkgs/conda/")
	path = strings.TrimPrefix(path, "conda/")
	path = strings.Trim(path, "/")
	channel, rest, ok := strings.Cut(path, "/")
	if !ok || rest == "" {
		artifactkit.Error(w, http.StatusNotFound, "channel required")
		return
	}
	base := s.upstreamFor(channel)
	if base == "" {
		artifactkit.Error(w, http.StatusNotFound, "unknown conda channel: "+channel)
		return
	}

	repo := channel + "/" + subdirOf(rest)
	if art, err := s.Registry.Meta.Get(r.Context(), "conda", repo, rest); err == nil && len(art.Blobs) > 0 {
		if artifactkit.ServeBlobAt(w, r, s.Registry.Blobs, r.Context(), art.Blobs[0].Digest, mediaTypeOf(rest)) {
			return
		}
	}
	fetched, err := s.Registry.Fetch(r.Context(), "conda", base, "/"+rest)
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

// upstreamFor resolves the upstream base for a channel key.
func (s *State) upstreamFor(channel string) string {
	if s.ChannelUpstreams != nil {
		if b, ok := s.ChannelUpstreams[channel]; ok {
			return b
		}
		return ""
	}
	return s.Registry.Upstreams.Get("conda")
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
	stored, err := s.Registry.StoreAndHash(ctx, data)
	if err != nil {
		return ""
	}
	artifactkit.LogMetaErr("conda cache", s.Registry.Meta.Put(ctx, artifactkit.Artifact{
		Format: "conda", Repository: repo, Version: name,
		MediaType: mediaTypeOf(name), Digest: stored.Digest,
		Blobs:  []artifactkit.Descriptor{{Digest: stored.Digest, Size: stored.Size, Name: name}},
		Source: "pull",
	}))
	return stored.Digest
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
