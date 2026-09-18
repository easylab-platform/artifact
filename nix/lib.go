// Package nix implements the Nix binary cache protocol (nix-serve face) with
// pull-through: nix-cache-info, per-store-path narinfo metadata, and NAR
// archives are fetched from the configured upstream (cache.nixos.org),
// cached in the CAS, and served byte-for-byte so NAR signature/ digest
// verification by the nix client stays intact.
//
// Wire format:
//
//	/nix-cache-info                        ("StoreDir: /nix/store\nWantMassQuery: 1\nPriority: 40\n")
//	/<hash>.narinfo                        (text metadata: URL, NarHash, NarSize, Sig, ...)
//	/nar/<hash>.nar[.xz|zst]               (the archive itself)
//
// Repository layout served here: everything under /artifacts/nix/ maps 1:1 onto
// the upstream cache root (the binary cache is one global namespace).
package nix

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

func init() { artifactkit.Register("nix", NewHandler) }

// ServeHTTP maps /artifacts/nix/<path> onto the upstream cache root.
func (s *State) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/artifacts/nix/")
	path = strings.TrimPrefix(path, "nix/")
	path = strings.Trim(path, "/")
	if path == "" || path == "nix-cache-info" {
		artifactkit.OctetResponse(w, r, []byte(defaultNixCacheInfo))
		return
	}
	if !isCachePath(path) {
		artifactkit.Error(w, http.StatusNotFound, "not found")
		return
	}

	// Local cache first (single global repo "cache"), streamed.
	if art, err := s.Registry.Meta.Get(r.Context(), "nix", "cache", path); err == nil && len(art.Blobs) > 0 {
		if artifactkit.ServeBlobAt(w, r, s.Registry.Blobs, r.Context(), art.Blobs[0].Digest, mediaTypeOf(path)) {
			return
		}
	}
	base := s.Registry.Upstreams.Get("nix")
	if base == "" {
		artifactkit.Error(w, http.StatusNotFound, "nix upstream disabled (air-gap)")
		return
	}
	fetched, err := s.Registry.Fetch(r.Context(), "nix", base, "/"+path)
	if err != nil {
		artifactkit.Error(w, http.StatusNotFound, "not found upstream")
		return
	}
	if digest := storeCache(s, r.Context(), path, fetched.Data, mediaTypeOf(path)); digest != "" &&
		artifactkit.ServeBlobAt(w, r, s.Registry.Blobs, r.Context(), digest, mediaTypeOf(path)) {
		return
	}
	artifactkit.OctetResponse(w, r, fetched.Data)
}

// defaultNixCacheInfo is served at the cache root (mirrors cache.nixos.org).
const defaultNixCacheInfo = "StoreDir: /nix/store\nWantMassQuery: 1\nPriority: 40\n"

// isCachePath allows only the three real cache path shapes (defense against
// using the adapter as a generic open proxy): nix-cache-info, <32 hex>
// [optional suffix].narinfo, and nar/<hex>.nar[.ext].
func isCachePath(path string) bool {
	switch {
	case path == "nix-cache-info":
		return true
	case strings.HasSuffix(path, ".narinfo"):
		h := strings.TrimSuffix(path, ".narinfo")
		return isHex32(h)
	case strings.HasPrefix(path, "nar/"):
		base := strings.TrimPrefix(path, "nar/")
		dot := strings.IndexByte(base, '.')
		if dot < 0 {
			return false
		}
		return isHex32(base[:dot]) && strings.HasPrefix(base[dot:], ".nar")
	}
	return false
}

// isHex32 validates a Nix store-path hash (32 chars of the nix base32
// alphabet: digits + lowercase letters, minus e/o/u).
func isHex32(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		ok := ('0' <= c && c <= '9') || ('a' <= c && c <= 'z')
		if !ok {
			return false
		}
	}
	return true
}

// storeCache persists a fetched path (best-effort).
func storeCache(s *State, ctx context.Context, name string, data []byte, mediaType string) string {
	stored, err := s.Registry.StoreAndHash(ctx, data)
	if err != nil {
		return ""
	}
	artifactkit.LogMetaErr("nix cache", s.Registry.Meta.Put(ctx, artifactkit.Artifact{
		Format: "nix", Repository: "cache", Version: name,
		MediaType: mediaType, Digest: stored.Digest,
		Blobs:  []artifactkit.Descriptor{{Digest: stored.Digest, Size: stored.Size, Name: name}},
		Source: "pull",
	}))
	return stored.Digest
}

func mediaTypeOf(path string) string {
	switch {
	case path == "nix-cache-info":
		return "text/plain"
	case strings.HasSuffix(path, ".narinfo"):
		return "text/x-nix-narinfo"
	case strings.HasSuffix(path, ".xz"):
		return "application/x-xz"
	case strings.HasSuffix(path, ".zst"):
		return "application/zstd"
	default:
		return "application/octet-stream"
	}
}
