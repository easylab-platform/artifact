// Package generic implements the generic raw-artifact store:
// /{name}/{version}/{filename}. Local-only (no upstream) as in the reference.
package generic

import (
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

func init() { artifactkit.Register("generic", NewHandler) }

func (s *State) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/artifacts/generic/")
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
		// StoreStream streams the body into the CAS (no in-memory copy) and
		// replaces any prior descriptor of the same filename.
		s.Registry.StoreVersion(r.Context(), artifactkit.VersionInput{
			Format: "generic", Repository: name, Version: version,
			Filename: filename, Source: "push", Body: r.Body,
		})
		artifactkit.JSON(w, http.StatusCreated, map[string]any{"ok": true})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
