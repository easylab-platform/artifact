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
	"encoding/json"
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
	// Repo-type prefix is optional (models have none; datasets/spaces carry
	// it): everything before /resolve|raw/ is the repo id ("{ns}/{name}" or a
	// bare name for canonical models/datasets).
	repoType := "models"
	if repoTypes[parts[0]] {
		repoType = parts[0]
		parts = parts[1:]
	}
	marker := -1
	for i, s := range parts {
		if s == "resolve" || s == "raw" {
			marker = i
			break
		}
	}
	if marker < 1 {
		artifactkit.Error(w, http.StatusNotFound, "not found")
		return
	}
	repoID := strings.Join(parts[:marker], "/")
	repoKey := repoType + "/" + repoID
	// The Hub's model URLs have NO repo-type segment ("/{repo}/resolve/..."),
	// while datasets/spaces keep it ("/datasets/{repo}/...").
	upstreamPath := strings.Join(parts, "/")
	if repoType != "models" {
		upstreamPath = repoType + "/" + upstreamPath
	}

	// Local cache first (by full upstream path as version key), streamed.
	// The stored metadata replays the Hub headers (X-Repo-Commit et al).
	if art, err := s.Registry.Meta.Get(r.Context(), "huggingface", repoKey, path); err == nil && len(art.Blobs) > 0 {
		replayStoredHeaders(w, art.Proprietary)
		if artifactkit.ServeBlobAt(w, r, s.Registry.Blobs, r.Context(), art.Blobs[0].Digest, art.MediaType) {
			return
		}
	}

	resp, err := s.upstreamGet(r, upstreamPath)
	if err != nil {
		artifactkit.Error(w, http.StatusBadGateway, "upstream fetch: "+err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()
	// A 307 to the CDN (LFS-backed files) is passed through so the client
	// fetches directly — easylab's egress rules keep the CDN host inside the
	// controlled path (no server-side full download).
	if resp.StatusCode == http.StatusTemporaryRedirect || resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently {
		if loc := resp.Header.Get("Location"); loc != "" {
			http.Redirect(w, r, loc, resp.StatusCode)
			return
		}
	}
	if resp.StatusCode != http.StatusOK {
		artifactkit.Error(w, resp.StatusCode, "huggingface upstream: "+http.StatusText(resp.StatusCode))
		return
	}
	// The Hub marks its resolve responses with X-Repo-Commit / ETag and the
	// hub client refuses to accept a file without them. Forward the metadata
	// headers a Hub-compatible endpoint must carry.
	forwardHubHeaders(w, resp)
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		artifactkit.Error(w, http.StatusBadGateway, "upstream read: "+err.Error())
		return
	}
	ct := contentType(resp.Header.Get("Content-Type"))
	if digest := storeCache(s, r.Context(), repoKey, path, data, ct, resp); digest != "" {
		forwardHubHeaders(w, resp)
		if artifactkit.ServeBlobAt(w, r, s.Registry.Blobs, r.Context(), digest, ct) {
			return
		}
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", itoa(len(data)))
	_, _ = w.Write(data)
}

// hubHeaderAllow lists the response headers the Hub sets on resolve/raw
// responses that clients require (X-Repo-Commit is mandatory) plus the useful
// caching ones.
var hubHeaderAllow = []string{
	"X-Repo-Commit", "X-Linked-Etag", "X-Linked-Size", "ETag",
	"X-Cache", "Content-Disposition", "Link", "X-Error-Message",
}

func forwardHubHeaders(w http.ResponseWriter, resp *http.Response) {
	for _, h := range hubHeaderAllow {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
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

// storeCache persists a fetched path (best-effort), recording the Hub
// response headers in Proprietary so a cache hit can replay them.
func storeCache(s *State, ctx context.Context, repo, name string, data []byte, mediaType string, resp *http.Response) string {
	stored, err := s.Registry.StoreAndHash(ctx, data)
	if err != nil {
		return ""
	}
	hdrs := map[string]string{}
	for _, h := range hubHeaderAllow {
		if v := resp.Header.Get(h); v != "" {
			hdrs[h] = v
		}
	}
	prop, _ := json.Marshal(map[string]any{"headers": hdrs})
	artifactkit.LogMetaErr("huggingface cache", s.Registry.Meta.Put(ctx, artifactkit.Artifact{
		Format: "huggingface", Repository: repo, Version: name,
		MediaType: mediaType, Digest: stored.Digest, Proprietary: prop,
		Blobs:  []artifactkit.Descriptor{{Digest: stored.Digest, Size: stored.Size, Name: lastSeg(name)}},
		Source: "pull",
	}))
	return stored.Digest
}

// replayStoredHeaders restores the Hub headers recorded for a cached entry.
func replayStoredHeaders(w http.ResponseWriter, prop []byte) {
	if len(prop) == 0 {
		return
	}
	var doc struct {
		Headers map[string]string `json:"headers"`
	}
	if json.Unmarshal(prop, &doc) != nil {
		return
	}
	for k, v := range doc.Headers {
		w.Header().Set(k, v)
	}
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
