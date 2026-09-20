// Package pub implements the Dart pub.dev protocol: package metadata, archive
// download with pull-through, two-phase upload, pubspec parsing. Mirror of the
// pkglab reference.
package pub

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
	SelfBase string
}

func NewHandler(reg *artifactkit.Registry, cfg map[string]any) (http.Handler, error) {
	s := &State{Registry: reg}
	if a, ok := cfg["auth"].(artifactkit.Auth); ok {
		s.Auth = a
	}
	if v, ok := cfg["self_base"].(string); ok {
		s.SelfBase = v
	}
	return s, nil
}

func init() { artifactkit.Register("pub", NewHandler) }

func (s *State) base() string { return artifactkit.SelfURL(s.SelfBase, "pub") }

func (s *State) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/artifacts/pub")
	path = strings.TrimPrefix(path, "/pub")
	path = strings.Trim(path, "/")

	switch {
	case path == "" || path == "/":
		artifactkit.JSON(w, http.StatusOK, map[string]any{"name": "artifact-pub"})
	case path == "api/packages":
		s.packagesList(w, r)
	case path == "api/packages/versions/new":
		s.versionsNew(w, r)
	case path == "api/packages/versions/newUpload":
		s.newUpload(w, r)
	case path == "api/packages/versions/newUploadFinish":
		s.finish(w, r)
	case strings.HasPrefix(path, "api/packages/") && strings.HasSuffix(path, "/advisories"):
		artifactkit.JSON(w, http.StatusOK, map[string]any{"advisories": []any{}, "advisoriesUpdated": "1970-01-01T00:00:00Z"})
	case strings.HasPrefix(path, "api/packages/") && strings.Contains(path, "/versions/"):
		s.versionArchive(w, r, path)
	case strings.HasPrefix(path, "api/archives/"):
		// Dart's `pub` downloads <archive_url> = {host}/api/archives/{name}-{version}.tar.gz.
		s.archive(w, r, strings.TrimPrefix(path, "api/archives/"))
	case strings.HasPrefix(path, "api/packages/"):
		name := strings.TrimPrefix(path, "api/packages/")
		if r.Method == http.MethodDelete {
			s.retract(w, r, name)
			return
		}
		s.pkgMetadata(w, r, name)
	case strings.HasPrefix(path, "packages/"):
		s.archive(w, r, strings.TrimPrefix(path, "packages/"))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *State) packagesList(w http.ResponseWriter, r *http.Request) {
	repos, _ := s.Registry.Meta.ListRepositoriesByFormat(r.Context(), "pub")
	artifactkit.JSON(w, http.StatusOK, map[string]any{"packages": repos, "next_url": "", "total": len(repos)})
}

func (s *State) versionsNew(w http.ResponseWriter, r *http.Request) {
	base := s.base()
	artifactkit.JSON(w, http.StatusOK, map[string]any{"url": base + "/api/packages/versions/newUpload", "fields": map[string]any{}})
}

func (s *State) newUpload(w http.ResponseWriter, r *http.Request) {
	if !artifactkit.AuthorizeWriteScoped(w, r, s.Auth, s.Registry, "pub") {
		return
	}
	artifactkit.LimitBody(w, r)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		artifactkit.WriteReadErr(w, err)
		return
	}
	ct := r.Header.Get("Content-Type")
	data := raw
	if strings.HasPrefix(ct, "multipart/form-data") {
		if _, part, ok := artifactkit.ExtractFirstFile(raw, ct); ok {
			data = part
		}
	}
	name, version := r.URL.Query().Get("name"), r.URL.Query().Get("version")
	if name == "" || version == "" {
		pn, pv := pubspecNameVersion(data)
		if name == "" {
			name = pn
		}
		if version == "" {
			version = pv
		}
	}
	if name == "" {
		name = "unknown"
	}
	if version == "" {
		version = "0.1.0"
	}
	filename := name + "-" + version + ".tar.gz"
	storeVersionSource(s.Registry, name, version, filename, data, "push", r.Context())
	w.Header().Set("Location", s.base()+"/api/packages/versions/newUploadFinish")
	artifactkit.JSON(w, http.StatusCreated, map[string]any{"success": map[string]any{"message": "Package uploaded"}})
}

func (s *State) finish(w http.ResponseWriter, r *http.Request) {
	if !artifactkit.AuthorizeWriteScoped(w, r, s.Auth, s.Registry, "pub") {
		return
	}
	artifactkit.JSON(w, http.StatusOK, map[string]any{"success": map[string]any{"message": "Successfully uploaded package."}})
}

func (s *State) pkgMetadata(w http.ResponseWriter, r *http.Request, name string) {
	name = strings.Trim(name, "/")
	versions, _ := s.Registry.Meta.ListVersions(r.Context(), "pub", name)
	artifactkit.SortSemver(versions)
	base := s.base()
	// Upstream metadata is authoritative for the publish metadata: it carries
	// each version's dependencies (the synthesized entry omits them, which
	// makes the client mis-resolve transitive packages) and the full version
	// list (a local cache fill must not hide versions we have not seen yet).
	upstream := map[string]map[string]any{}
	var upstreamOrder []string
	if remote, err := s.Registry.Remote(r.Context(), artifactkit.UpstreamSpec{Format: "pub"}); err == nil {
		if body, err := remote.GetCached(r.Context(), artifactkit.SharedIndexCache(), "/api/packages/"+artifactkit.URLencode(name)); err == nil {
			var doc struct {
				Versions []map[string]any `json:"versions"`
			}
			if json.Unmarshal([]byte(body), &doc) == nil {
				for _, v := range doc.Versions {
					if ver, _ := v["version"].(string); ver != "" {
						upstream[ver] = v
						upstreamOrder = append(upstreamOrder, ver)
					}
				}
			}
		}
	}
	if len(versions) == 0 && len(upstreamOrder) == 0 {
		artifactkit.Error(w, http.StatusNotFound, "not found")
		return
	}
	// Local versions first, then any upstream-only version.
	seen := map[string]bool{}
	all := append([]string{}, versions...)
	for _, v := range versions {
		seen[v] = true
	}
	for _, v := range upstreamOrder {
		if !seen[v] {
			all = append(all, v)
			seen[v] = true
		}
	}
	synth := func(v string) map[string]any {
		sha := ""
		if art, err := s.Registry.Meta.Get(r.Context(), "pub", name, v); err == nil && len(art.Blobs) > 0 {
			sha = art.Blobs[0].Hex()
		}
		return map[string]any{
			"version": v, "pubspec": map[string]any{"name": name, "version": v, "environment": map[string]any{"sdk": ">=3.0.0 <4.0.0"}},
			"archive_url": base + "/packages/" + artifactkit.URLencode(name) + "/" + v + ".tar.gz", "archive_sha256": sha,
		}
	}
	entry := func(v string) map[string]any {
		synthetic := synth(v)
		up, ok := upstream[v]
		if !ok {
			return synthetic
		}
		cp := map[string]any{}
		for k, val := range up {
			cp[k] = val
		}
		// Always point the archive at this registry, and use the locally-known
		// hash when we have the bytes.
		cp["archive_url"] = synthetic["archive_url"]
		if art, err := s.Registry.Meta.Get(r.Context(), "pub", name, v); err == nil && len(art.Blobs) > 0 {
			cp["archive_sha256"] = art.Blobs[0].Hex()
		}
		return cp
	}
	var vs []any
	var latestMap map[string]any
	for _, v := range all {
		e := entry(v)
		vs = append(vs, e)
		latestMap = e
	}
	if lv := artifactkit.HighestVersion(all); lv != "" {
		latestMap = entry(lv)
	}
	artifactkit.JSON(w, http.StatusOK, map[string]any{
		"name":     name,
		"latest":   latestMap,
		"versions": vs,
	})
}

func (s *State) versionArchive(w http.ResponseWriter, r *http.Request, path string) {
	rest := strings.TrimPrefix(path, "api/packages/")
	parts := strings.Split(rest, "/")
	if len(parts) < 3 {
		artifactkit.Error(w, http.StatusNotFound, "not found")
		return
	}
	name, version := parts[0], parts[2]
	if strings.HasSuffix(version, ".tar.gz") {
		v := strings.TrimSuffix(version, ".tar.gz")
		filename := name + "-" + v + ".tar.gz"
		if art, err := s.Registry.Meta.Get(r.Context(), "pub", name, v); err == nil {
			for _, b := range art.Blobs {
				if artifactkit.ServeBlobAtNamed(w, r, s.Registry.Blobs, r.Context(), b.Digest, "application/octet-stream", filename) {
					return
				}
			}
		}
		artifactkit.Error(w, http.StatusNotFound, "not found")
		return
	}
	artifactkit.JSON(w, http.StatusOK, map[string]any{"name": name, "version": version, "archive_url": s.base() + "/packages/" + artifactkit.URLencode(name) + "/" + version + ".tar.gz", "pubspec": map[string]any{"name": name, "version": version}})
}

// archive serves a package tarball. Two shapes arrive:
//   - /packages/{name}/{version}.tar.gz     (the archive_url we advertise)
//   - /api/archives/{name}-{version}.tar.gz (the upstream Hub's shape)
func (s *State) archive(w http.ResponseWriter, r *http.Request, rest string) {
	rel := strings.Trim(rest, "/")
	parts := strings.Split(rel, "/")
	filename := parts[len(parts)-1]
	var name, version string
	if len(parts) >= 2 {
		name = parts[0]
		version = strings.TrimSuffix(filename, ".tar.gz")
	} else {
		name, version = nameVersionFromStem(strings.TrimSuffix(filename, ".tar.gz"))
	}
	if name == "" || version == "" {
		artifactkit.Error(w, http.StatusNotFound, "not found")
		return
	}
	if art, err := s.Registry.Meta.Get(r.Context(), "pub", name, version); err == nil {
		for _, b := range art.Blobs {
			if b.Name == filename || strings.HasSuffix(b.Name, filename) {
				if artifactkit.ServeBlobAtNamed(w, r, s.Registry.Blobs, r.Context(), b.Digest, "application/octet-stream", filename) {
					return
				}
			}
		}
	}
	// Stream the package tarball into the CAS (no full buffering).
	digest, size, ok := s.Registry.FetchToBlob(r.Context(), "pub", "/api/archives/"+artifactkit.URLencode(name+"-"+version+".tar.gz"))
	if !ok {
		artifactkit.Error(w, http.StatusNotFound, "not found")
		return
	}
	storeVersionBlob(s.Registry, name, version, filename, digest, size, "pull", r.Context())
	if artifactkit.ServeBlobAtNamed(w, r, s.Registry.Blobs, r.Context(), digest, "application/octet-stream", filename) {
		return
	}
	artifactkit.Error(w, http.StatusBadGateway, "cache error")
}

func (s *State) retract(w http.ResponseWriter, r *http.Request, name string) {
	if !artifactkit.AuthorizeWriteScoped(w, r, s.Auth, s.Registry, "pub") {
		return
	}
	name = strings.Trim(name, "/")
	vs, _ := s.Registry.Meta.ListVersions(r.Context(), "pub", name)
	for _, v := range vs {
		removeVersion(s.Registry, name, v, r.Context())
	}
	artifactkit.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

func nameVersionFromStem(stem string) (string, string) {
	if i := strings.LastIndex(stem, "-"); i > 0 {
		return stem[:i], stem[i+1:]
	}
	return stem, "0.0.0"
}

func removeVersion(reg *artifactkit.Registry, name, version string, ctx context.Context) {
	if art, err := reg.Meta.Get(ctx, "pub", name, version); err == nil {
		for _, b := range art.Blobs {
			artifactkit.LogMetaErr("blob delete", reg.Blobs.Delete(ctx, b.Digest))
		}
	}
	artifactkit.LogMetaErr("meta delete", reg.Meta.Delete(ctx, "pub", name, version))
}

// storeVersionBlob records a tarball already streamed into the CAS.
func storeVersionBlob(reg *artifactkit.Registry, name, version, filename, digest string, size int64, source string, ctx context.Context) {
	art, _ := reg.Meta.Get(ctx, "pub", name, version)
	if art.Format == "" {
		art = artifactkit.Artifact{Format: "pub", Repository: name, Version: version, Source: source}
	}
	var kept []artifactkit.Descriptor
	for _, b := range art.Blobs {
		if b.Name != filename {
			kept = append(kept, b)
		}
	}
	art.Blobs = append(kept, artifactkit.Descriptor{Digest: digest, Size: size, Name: filename})
	artifactkit.LogMetaErr("meta put", reg.Meta.Put(ctx, art))
}

func storeVersionSource(reg *artifactkit.Registry, name, version, filename string, data []byte, source string, ctx context.Context) {
	reg.StoreVersion(ctx, artifactkit.VersionInput{
		Format: "pub", Repository: name, Version: version,
		Filename: filename, Source: source, Data: data,
	})
}

func pubspecNameVersion(data []byte) (string, string) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return "", ""
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		if !strings.HasSuffix(hdr.Name, "pubspec.yaml") {
			continue
		}
		buf, _ := io.ReadAll(io.LimitReader(tr, 1<<20))
		var name, version string
		for _, line := range strings.Split(string(buf), "\n") {
			t := strings.TrimSpace(line)
			if rest, ok := strings.CutPrefix(t, "name:"); ok {
				name = strings.TrimSpace(rest)
			}
			if rest, ok := strings.CutPrefix(t, "version:"); ok {
				version = strings.Trim(strings.TrimSpace(rest), `"'`)
			}
		}
		return name, version
	}
	return "", ""
}
