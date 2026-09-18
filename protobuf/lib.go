// Package protobuf implements the Buf Schema Registry (BSR) protocol with
// pull-through: module resolution, file downloads, and label reads are
// fetched from the configured upstream (buf.build), cached in the CAS, and
// served verbatim.
//
// Wire format (the BSR curl API used by `buf`):
//
//	/v1/modules/{module}/          module metadata
//	/v1/modules/{module}/ref/{ref} resolve a commit/label
//	/v1/downloads/{module}/{ref}   module archive (content bundle)
//
// Repository layout served here:
//
//	/artifacts/protobuf/{module}/{path}   module = "<org>/<name>" or "<org>/<name>/<plugin>"
package protobuf

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/easylab-platform/artifact/core"
)

type State struct {
	Registry *artifactkit.Registry
	Auth     artifactkit.Auth
	// BSRToken is forwarded upstream as an Authorization header (private
	// modules).
	BSRToken string
}

func NewHandler(reg *artifactkit.Registry, cfg map[string]any) (http.Handler, error) {
	s := &State{Registry: reg}
	if a, ok := cfg["auth"].(artifactkit.Auth); ok {
		s.Auth = a
	}
	if v, ok := cfg["bsr_token"].(string); ok {
		s.BSRToken = v
	}
	return s, nil
}

func init() { artifactkit.Register("protobuf", NewHandler) }

// ServeHTTP dispatches /artifacts/protobuf/{module}/{path} where module is
// "<org>/<name>[/<plugin>]" and path is the BSR API face.
func (s *State) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/artifacts/protobuf/")
	path = strings.TrimPrefix(path, "protobuf/")
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	// module = org/name (+ optional plugin dir); the BSR API face starts at
	// /v1/.
	if len(parts) < 3 {
		artifactkit.Error(w, http.StatusNotFound, "not found")
		return
	}
	// Find the /v1/ marker: everything before it is the module key.
	vIdx := -1
	for i, p := range parts {
		if p == "v1" {
			vIdx = i
			break
		}
	}
	if vIdx < 1 || vIdx >= len(parts) {
		artifactkit.Error(w, http.StatusNotFound, "not found")
		return
	}
	module := strings.Join(parts[:vIdx], "/")
	apiPath := "/" + strings.Join(parts[vIdx:], "/")

	// Local cache first.
	if art, err := s.Registry.Meta.Get(r.Context(), "protobuf", module, path); err == nil && len(art.Blobs) > 0 {
		if artifactkit.ServeBlobAt(w, r, s.Registry.Blobs, r.Context(), art.Blobs[0].Digest, "application/octet-stream") {
			return
		}
	}
	base := s.Registry.Upstreams.Get("protobuf")
	if sc := artifactkit.RepoScopeFrom(r.Context()); sc.Host != "" {
		if b, ok := s.Registry.Upstreams.HostBase(sc.Proto, sc.Host, sc.Prefix); ok {
			base = b
		}
	}
	if base == "" {
		artifactkit.Error(w, http.StatusNotFound, "protobuf upstream disabled (air-gap)")
		return
	}
	up := s.Registry.RemoteAt(strings.TrimSuffix(base, "/") + apiPath)
	if s.BSRToken != "" {
		up = up.WithHeader("Authorization", "Bearer "+s.BSRToken)
	}
	resp, err := up.Get(r.Context(), "")
	if err != nil {
		artifactkit.Error(w, http.StatusBadGateway, "upstream fetch: "+err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		artifactkit.Error(w, resp.StatusCode, "BSR upstream: "+http.StatusText(resp.StatusCode))
		return
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		artifactkit.Error(w, http.StatusBadGateway, "upstream read: "+err.Error())
		return
	}
	storeCache(s, r.Context(), module, path, data, contentType(resp.Header.Get("Content-Type")))
	w.Header().Set("Content-Type", contentType(resp.Header.Get("Content-Type")))
	_, _ = w.Write(data)
}

// storeCache persists a fetched path (best-effort).
func storeCache(s *State, ctx context.Context, module, name string, data []byte, mediaType string) string {
	stored, err := s.Registry.StoreAndHash(ctx, data)
	if err != nil {
		return ""
	}
	artifactkit.LogMetaErr("protobuf cache", s.Registry.Meta.Put(ctx, artifactkit.Artifact{
		Format: "protobuf", Repository: module, Version: name,
		MediaType: mediaType, Digest: stored.Digest,
		Blobs:  []artifactkit.Descriptor{{Digest: stored.Digest, Size: stored.Size, Name: name}},
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
