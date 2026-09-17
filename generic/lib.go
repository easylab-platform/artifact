// Package generic implements the generic raw-artifact store:
// /{name}/{version}/{filename}. Local-only (no upstream) as in the reference.
package generic

import (
	"context"
	"io"
	"net/http"
	"os"
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

func init() { artifactkit.Register("generic", NewHandler) }

func (s *State) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/pkgs/generic/")
	path = strings.TrimPrefix(path, "generic/")
	path = strings.Trim(path, "/")

	if strings.HasSuffix(path, ".version") {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimSuffix(path, ".version")
		if !artifactkit.AuthorizeWriteScoped(w, r, s.Auth, s.Registry, "generic") {
			return
		}
		vs, _ := s.Registry.Meta.ListVersions(r.Context(), "generic", name)
		for _, v := range vs {
			artifactkit.LogMetaErr("meta delete", s.Registry.Meta.Delete(r.Context(), "generic", name, v))
		}
		artifactkit.JSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	parts := strings.Split(path, "/")
	if len(parts) != 3 {
		artifactkit.Error(w, http.StatusNotFound, "not found")
		return
	}
	name, version, filename := parts[0], parts[1], parts[2]

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		art, err := s.Registry.Meta.Get(r.Context(), "generic", name, version)
		if err != nil {
			artifactkit.Error(w, http.StatusNotFound, "not found")
			return
		}
		for _, b := range art.Blobs {
			if b.Name != filename {
				continue
			}
			if artifactkit.ServeBlobAtNamed(w, r, s.Registry.Blobs, r.Context(), b.Digest, "application/octet-stream", filename) {
				return
			}
		}
		artifactkit.Error(w, http.StatusNotFound, "not found")
	case http.MethodPut:
		if !artifactkit.AuthorizeWriteScoped(w, r, s.Auth, s.Registry, "generic") {
			return
		}
		artifactkit.LimitBody(w, r)
		// Stream through a temp file: large artifacts (multi-hundred-MB
		// toolchains) must not be buffered in memory before hashing.
		tmp, err := os.CreateTemp("", "artifact-generic-*")
		if err != nil {
			artifactkit.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer func() { _ = os.Remove(tmp.Name()) }()
		defer func() { _ = tmp.Close() }()
		h, err := artifactkit.ComputeHashes(io.TeeReader(r.Body, tmp))
		if err != nil {
			artifactkit.WriteReadErr(w, err)
			return
		}
		digest := "sha256:" + h.SHA256
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			artifactkit.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		store(s.Registry, name, version, filename, tmp, digest, r.Context())
		artifactkit.JSON(w, http.StatusCreated, map[string]any{"ok": true})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func store(reg *artifactkit.Registry, name, version, filename string, r io.Reader, digest string, ctx context.Context) {
	art, _ := reg.Meta.Get(ctx, "generic", name, version)
	if art.Repository == "" {
		art.Format, art.Repository, art.Version = "generic", name, version
	}
	var removed []artifactkit.Descriptor
	var kept []artifactkit.Descriptor
	for _, b := range art.Blobs {
		if b.Name == filename {
			removed = append(removed, b)
		} else {
			kept = append(kept, b)
		}
	}
	art.Blobs = kept
	if _, err := reg.Blobs.PutIfAbsent(ctx, digest, r); err == nil {
		var n int64
		if size, _ := reg.Blobs.Stat(ctx, digest); size != nil {
			n = *size
		}
		art.Blobs = append(art.Blobs, artifactkit.Descriptor{Digest: digest, Size: n, Name: filename})
	}
	for _, b := range removed {
		if b.Digest != digest {
			artifactkit.LogMetaErr("blob delete", reg.Blobs.Delete(ctx, b.Digest))
		}
	}
	artifactkit.LogMetaErr("meta put", reg.Meta.Put(ctx, art))
}
