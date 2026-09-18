// Package golang implements the Go module proxy (GOPROXY) protocol:
// @v/list, @latest, .info/.mod/.zip with pull-through and a local PUT
// /upload, mirroring the pkglab reference.
package golang

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
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

func init() {
	artifactkit.Register("go", NewHandler)
	artifactkit.RegisterNamespace("go", artifactkit.GoModuleNamespace)
}

// EncodeModulePath escapes uppercase letters as !lower per the Go proxy spec.
func EncodeModulePath(m string) string {
	var b strings.Builder
	for _, c := range m {
		if c >= 'A' && c <= 'Z' {
			b.WriteByte('!')
			b.WriteRune(c - 'A' + 'a')
		} else {
			b.WriteRune(c)
		}
	}
	return b.String()
}

func (s *State) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Module paths are case-sensitive and may legitimately begin with "go"
	// (golang.org/...), so only the mount prefix is stripped — never a second
	// "/go" segment.
	path := strings.TrimPrefix(r.URL.Path, "/artifacts/go")
	path = strings.Trim(path, "/")

	if path == "upload" {
		switch r.Method {
		case http.MethodPut:
			s.upload(w, r)
			return
		case http.MethodDelete:
			s.deleteModule(w, r)
			return
		}
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Checksum database (sum.golang.org). Three shapes hit this mirror:
	//   /sumdb/sum.golang.org/<path>   (GOPROXY sumdb passthrough)
	//   /lookup/... , /tile/...        (direct sumdb access, host spoofed)
	//   /latest, /supported            (direct sumdb metadata)
	// All are read-only, so cache+forward verbatim.
	if strings.HasPrefix(path, "sumdb/") {
		rest := strings.TrimPrefix(path, "sumdb/")
		// Drop the "<sumdb-host>/" prefix; the upstream base selects it.
		if _, tail, ok := strings.Cut(rest, "/"); ok {
			rest = tail
		} else {
			rest = ""
		}
		s.sumdb(w, r, rest)
		return
	}
	if strings.HasPrefix(path, "lookup/") || strings.HasPrefix(path, "tile/") ||
		path == "latest" || path == "supported" {
		s.sumdb(w, r, path)
		return
	}

	switch {
	case strings.HasSuffix(path, "/@latest"):
		s.latest(w, r, strings.TrimSuffix(path, "/@latest"))
	case strings.HasSuffix(path, "/@v/list"):
		s.versions(w, r, strings.TrimSuffix(path, "/@v/list"))
	default:
		for _, ext := range []string{".info", ".mod", ".zip"} {
			if strings.HasSuffix(path, ext) {
				rest := strings.TrimSuffix(path, ext)
				mod, ver := splitModuleVersion(rest)
				if ver == "" {
					break
				}
				switch ext {
				case ".info":
					s.info(w, r, mod, ver)
				case ".mod":
					s.goMod(w, r, mod, ver)
				case ".zip":
					s.moduleZip(w, r, mod, ver)
				}
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}
}

func splitModuleVersion(rest string) (string, string) {
	if i := strings.Index(rest, "/@v/"); i >= 0 {
		return rest[:i], rest[i+len("/@v/"):]
	}
	return rest, ""
}

func (s *State) latest(w http.ResponseWriter, r *http.Request, module string) {
	versions, _ := s.Registry.Meta.ListVersions(r.Context(), "go", module)
	if v := artifactkit.HighestVersion(versions); v != "" {
		s.jsonOk(w, map[string]any{"Version": v, "Time": "2024-01-01T00:00:00Z"})
		return
	}
	if len(versions) > 0 {
		s.jsonOk(w, map[string]any{"Version": versions[len(versions)-1], "Time": "2024-01-01T00:00:00Z"})
		return
	}
	s.proxyOne(w, r, module, "@latest", "application/json")
}

func (s *State) versions(w http.ResponseWriter, r *http.Request, module string) {
	versions, _ := s.Registry.Meta.ListVersions(r.Context(), "go", module)
	if len(versions) > 0 {
		artifactkit.SortSemver(versions)
		artifactkit.Text(w, http.StatusOK, strings.Join(versions, "\n"), "text/plain")
		return
	}
	s.proxyOne(w, r, module, "@v/list", "text/plain")
}

func (s *State) info(w http.ResponseWriter, r *http.Request, module, version string) {
	if _, err := s.Registry.Meta.Get(r.Context(), "go", module, version); err == nil {
		s.jsonOk(w, map[string]any{"Version": version, "Time": "2024-01-01T00:00:00Z"})
		return
	}
	s.proxyOne(w, r, module, "@v/"+version+".info", "application/json")
}

func (s *State) goMod(w http.ResponseWriter, r *http.Request, module, version string) {
	if art, err := s.Registry.Meta.Get(r.Context(), "go", module, version); err == nil {
		for _, b := range art.Blobs {
			rd, err := s.Registry.Blobs.Open(r.Context(), b.Digest)
			if err != nil || rd == nil {
				continue
			}
			data, _ := io.ReadAll(rd)
			_ = rd.Close()
			if gm := extractGoMod(data); gm != "" {
				artifactkit.Text(w, http.StatusOK, gm, "text/plain; charset=utf-8")
				return
			}
		}
	}
	s.proxyOne(w, r, module, "@v/"+version+".mod", "text/plain; charset=utf-8")
}

func (s *State) moduleZip(w http.ResponseWriter, r *http.Request, module, version string) {
	filename := module + "-" + version + ".zip"
	if art, err := s.Registry.Meta.Get(r.Context(), "go", module, version); err == nil {
		for _, b := range art.Blobs {
			if artifactkit.ServeBlobAtNamed(w, r, s.Registry.Blobs, r.Context(), b.Digest, "application/octet-stream", filename) {
				return
			}
		}
	}
	ep := EncodeModulePath(module)
	body, err := s.registryFetch(r.Context(), "/"+ep+"/@v/"+version+".zip")
	if err != nil {
		artifactkit.Error(w, http.StatusNotFound, "not found")
		return
	}
	storeVersionSource(s.Registry, module, version, body, "pull", r.Context())
	artifactkit.ServeData(w, r, s.Registry, r.Context(), body, "application/octet-stream", filename)
}

func (s *State) proxyOne(w http.ResponseWriter, r *http.Request, module, suffix, ct string) {
	ep := EncodeModulePath(module)
	body, err := s.registryFetch(r.Context(), "/"+ep+"/"+suffix)
	if err != nil {
		artifactkit.Error(w, http.StatusNotFound, "not found")
		return
	}
	artifactkit.Text(w, http.StatusOK, string(body), ct)
}

// sumdb proxies the Go checksum database, caching lookups/tiles in the CAS so
// repeated installs do not re-fetch them. The upstream base is the "go.sumdb"
// upstream when set, else sum.golang.org. sumdb responses are signed by the
// database; caching them verbatim preserves verification.
func (s *State) sumdb(w http.ResponseWriter, r *http.Request, rest string) {
	rest = strings.Trim(rest, "/")
	if rest == "" {
		artifactkit.Error(w, http.StatusNotFound, "not found")
		return
	}
	base := ""
	if base == "" {
		base = "https://sum.golang.org"
	}
	// Split the first path segment (lookup | tile | latest | supported).
	seg, tail, _ := strings.Cut(rest, "/")
	key := "sumdb/" + seg + "/" + tail
	if art, err := s.Registry.Meta.Get(r.Context(), "go", key, "sumdb"); err == nil && len(art.Blobs) > 0 {
		if artifactkit.ServeBlobAt(w, r, s.Registry.Blobs, r.Context(), art.Blobs[0].Digest, "text/plain; charset=UTF-8") {
			return
		}
	}
	upURL := "/" + rest
	if q := r.URL.RawQuery; q != "" {
		upURL += "?" + q
	}
	remote, _ := s.Registry.RemoteForSub(r.Context(), "go", "sumdb")
	if remote == nil {
		remote, _ = s.Registry.RemoteCtx(r.Context(), "go")
	}
	body, err := remote.GetBytes(r.Context(), upURL)
	if err != nil {
		artifactkit.Error(w, http.StatusNotFound, "not found")
		return
	}
	stored, serr := s.Registry.StoreAndHash(r.Context(), body)
	if serr == nil {
		artifactkit.LogMetaErr("go sumdb cache", s.Registry.Meta.Put(r.Context(), artifactkit.Artifact{
			Format: "go", Repository: key, Version: "sumdb",
			MediaType: "text/plain", Digest: stored.Digest,
			Blobs:  []artifactkit.Descriptor{{Digest: stored.Digest, Size: stored.Size, Name: seg}},
			Source: "pull",
		}))
	}
	artifactkit.OctetResponse(w, r, body)
}

func (s *State) registryFetch(ctx context.Context, path string) ([]byte, error) {
	remote, err := s.Registry.RemoteCtx(ctx, "go")
	if err != nil {
		return nil, err
	}
	return remote.GetBytes(ctx, path)
}

func (s *State) jsonOk(w http.ResponseWriter, v any) {
	artifactkit.JSON(w, http.StatusOK, v)
}

func (s *State) upload(w http.ResponseWriter, r *http.Request) {
	if !artifactkit.AuthorizeWriteScoped(w, r, s.Auth, s.Registry, "go") {
		return
	}
	name, version := r.URL.Query().Get("name"), r.URL.Query().Get("version")
	if name == "" {
		name = r.URL.Query().Get("module")
	}
	artifactkit.LimitBody(w, r)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		artifactkit.WriteReadErr(w, err)
		return
	}
	if name == "" {
		name = jsonStr(data, "name")
		if name == "" {
			name = jsonStr(data, "module")
		}
		if version == "" {
			version = jsonStr(data, "version")
		}
	}
	if name == "" {
		name = "go/local"
	}
	if version == "" {
		version = "v0.0.0"
	}
	storeVersionSource(s.Registry, name, version, data, "push", r.Context())
	artifactkit.JSON(w, http.StatusCreated, map[string]any{"ok": true})
}

func (s *State) deleteModule(w http.ResponseWriter, r *http.Request) {
	if !artifactkit.AuthorizeWriteScoped(w, r, s.Auth, s.Registry, "go") {
		return
	}
	module := r.URL.Query().Get("name")
	if module == "" {
		module = r.URL.Query().Get("module")
	}
	if module == "" {
		artifactkit.Error(w, http.StatusBadRequest, "missing name")
		return
	}
	version := r.URL.Query().Get("version")
	if version != "" {
		if art, err := s.Registry.Meta.Get(r.Context(), "go", module, version); err == nil {
			for _, b := range art.Blobs {
				artifactkit.LogMetaErr("blob delete", s.Registry.Blobs.Delete(r.Context(), b.Digest))
			}
		}
		artifactkit.LogMetaErr("meta delete", s.Registry.Meta.Delete(r.Context(), "go", module, version))
	} else {
		vs, _ := s.Registry.Meta.ListVersions(r.Context(), "go", module)
		for _, v := range vs {
			if art, err := s.Registry.Meta.Get(r.Context(), "go", module, v); err == nil {
				for _, b := range art.Blobs {
					artifactkit.LogMetaErr("blob delete", s.Registry.Blobs.Delete(r.Context(), b.Digest))
				}
			}
			artifactkit.LogMetaErr("meta delete", s.Registry.Meta.Delete(r.Context(), "go", module, v))
		}
	}
	artifactkit.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

func storeVersionSource(reg *artifactkit.Registry, module, version string, data []byte, source string, ctx context.Context) {
	if version == "" {
		version = "v0.0.0"
	}
	art := artifactkit.Artifact{Format: "go", Repository: module, Version: version, Source: source}
	if len(data) > 0 {
		h, _ := artifactkit.ComputeHashesBytes(data)
		digest := "sha256:" + h.SHA256
		if _, err := reg.Blobs.PutIfAbsent(ctx, digest, bytes.NewReader(data)); err == nil {
			art.Blobs = append(art.Blobs, artifactkit.Descriptor{Digest: digest, Size: int64(len(data)), Name: module + "-" + version + ".zip"})
		}
	}
	artifactkit.LogMetaErr("meta put", reg.Meta.Put(ctx, art))
}

func jsonStr(data []byte, key string) string {
	var m map[string]any
	if json.Unmarshal(data, &m) == nil {
		if v, ok := m[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

func extractGoMod(zipData []byte) string {
	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return ""
	}
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/go.mod") || f.Name == "go.mod" {
			rc, err := f.Open()
			if err != nil {
				return ""
			}
			buf, _ := io.ReadAll(io.LimitReader(rc, 1<<20))
			_ = rc.Close()
			return string(buf)
		}
	}
	return ""
}
