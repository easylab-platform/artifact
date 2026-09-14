// Package huggingface implements the HuggingFace Hub protocol with
// pull-through: model/dataset metadata, file trees, and (LFS) blobs are
// fetched from huggingface.co, cached in the CAS, and served verbatim.
//
// Wire format (the Hub's REST face used by transformers/diffusers):
//
//	/{repo-type}/{namespace}/{name}/resolve/{revision}/{path}   file content
//	/{repo-type}/{namespace}/{name}/raw/{revision}/{path}       raw text
//	/api/models/{ns}/{name}                                      metadata JSON
//
// repo-type ∈ models | datasets | spaces. The client authenticates with a
// bearer token for gated repos; a per-deployment HF token (from the
// config) is forwarded upstream when set.
//
// Repository layout served here:
//
//	/pkgs/huggingface/{repo-type}/{namespace}/{name}/...
package huggingface

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/easylab-platform/artifact/core"
)

type State struct {
	Registry *artifactkit.Registry
	Auth     artifactkit.Auth
	// HFToken is forwarded upstream as a Bearer credential (gated repos).
	HFToken string
}

func NewHandler(reg *artifactkit.Registry, cfg map[string]any) (http.Handler, error) {
	s := &State{Registry: reg}
	if a, ok := cfg["auth"].(artifactkit.Auth); ok {
		s.Auth = a
	}
	if v, ok := cfg["hf_token"].(string); ok {
		s.HFToken = v
	}
	return s, nil
}

func init() { artifactkit.Register("huggingface", NewHandler) }

// upstreamBase is the HF Hub root (overridable for tests via the upstream
// table entry "huggingface").
func (s *State) upstreamBase() string {
	if b := s.Registry.Upstreams.Get("huggingface"); b != "" {
		return b
	}
	return "https://huggingface.co"
}

// repoTypes recognized on the path.
var repoTypes = map[string]bool{"models": true, "datasets": true, "spaces": true}

// ServeHTTP dispatches /pkgs/huggingface/{repo-type}/{ns}/{name}/{rest} and
// /pkgs/huggingface/api/{...} metadata calls.
func (s *State) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/pkgs/huggingface/")
	path = strings.TrimPrefix(path, "huggingface/")
	path = strings.Trim(path, "/")
	if path == "" {
		artifactkit.JSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	// /api/... is the Hub metadata API (list/search/resolve): a repo key is
	// not derivable from arbitrary API paths, so these are proxied without
	// path-keyed caching (the response is still streamed).
	if strings.HasPrefix(path, "api/") {
		s.proxyUncached(w, r, path)
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) < 3 || !repoTypes[parts[0]] {
		artifactkit.Error(w, http.StatusNotFound, "not found")
		return
	}
	repoType, ns, name := parts[0], parts[1], parts[2]
	repoKey := repoType + "/" + ns + "/" + name

	// Local cache first (by full upstream path as version key), streamed.
	if art, err := s.Registry.Meta.Get(r.Context(), "huggingface", repoKey, path); err == nil && len(art.Blobs) > 0 {
		if artifactkit.ServeBlobAt(w, r, s.Registry.Blobs, r.Context(), art.Blobs[0].Digest, art.MediaType) {
			return
		}
	}

	resp, err := s.upstreamGet(r, path)
	if err != nil {
		artifactkit.Error(w, http.StatusBadGateway, "upstream fetch: "+err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()
	// A 302 to the CDN (LFS-backed files) is passed through so the client
	// fetches directly — easylab's egress rules keep the CDN host inside the
	// controlled path (no server-side full download).
	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently {
		if loc := resp.Header.Get("Location"); loc != "" {
			http.Redirect(w, r, loc, resp.StatusCode)
			return
		}
	}
	if resp.StatusCode != http.StatusOK {
		artifactkit.Error(w, resp.StatusCode, "huggingface upstream: "+http.StatusText(resp.StatusCode))
		return
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		artifactkit.Error(w, http.StatusBadGateway, "upstream read: "+err.Error())
		return
	}
	ct := contentType(resp.Header.Get("Content-Type"))
	if digest := storeCache(s, r.Context(), repoKey, path, data, ct); digest != "" &&
		artifactkit.ServeBlobAt(w, r, s.Registry.Blobs, r.Context(), digest, ct) {
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", itoa(len(data)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

// proxyUncached forwards an /api/... call (metadata/search) and streams the
// response body; 3xx Location is passed through.
func (s *State) proxyUncached(w http.ResponseWriter, r *http.Request, path string) {
	resp, err := s.upstreamGet(r, path)
	if err != nil {
		artifactkit.Error(w, http.StatusBadGateway, "upstream fetch: "+err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		if loc := resp.Header.Get("Location"); loc != "" {
			http.Redirect(w, r, loc, resp.StatusCode)
			return
		}
	}
	ct := contentType(resp.Header.Get("Content-Type"))
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(resp.StatusCode)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, resp.Body)
	}
}

// upstreamGet issues the Hub request with the optional bearer token.
func (s *State) upstreamGet(r *http.Request, path string) (*http.Response, error) {
	up := s.Registry.RemoteAt(strings.TrimSuffix(s.upstreamBase(), "/") + "/" + path)
	if s.HFToken != "" {
		up = up.WithHeader("Authorization", "Bearer "+s.HFToken)
	}
	return up.Get(r.Context(), "")
}

// itoa is strconv.Itoa without the extra import at call sites.
func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

// storeCache persists a fetched path (best-effort).
func storeCache(s *State, ctx context.Context, repo, name string, data []byte, mediaType string) string {
	stored, err := s.Registry.StoreAndHash(ctx, data)
	if err != nil {
		return ""
	}
	artifactkit.LogMetaErr("huggingface cache", s.Registry.Meta.Put(ctx, artifactkit.Artifact{
		Format: "huggingface", Repository: repo, Version: name,
		MediaType: mediaType, Digest: stored.Digest,
		Blobs:  []artifactkit.Descriptor{{Digest: stored.Digest, Size: stored.Size, Name: lastSeg(name)}},
		Source: "pull",
	}))
	return stored.Digest
}

func contentType(ct string) string {
	if ct == "" {
		return "application/octet-stream"
	}
	return ct
}

func lastSeg(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}
