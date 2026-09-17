// Package ivy implements the Apache Ivy repository protocol, which sbt (and
// Ant) use for artifacts that are not published to a Maven layout — most
// legacy sbt plugins. A repository is a tree of directories:
//
//	[organisation]/[module]/[revision]/ivys/ivy.xml
//	[organisation]/[module]/[revision]/jars/[artifact].[ext]
//	[organisation]/[module]/[revision]/[type]s/[artifact].[ext]
//
// and the sbt-plugin shape adds two extra attributes before the revision:
//
//	[organisation]/[module]/[scalaVersion]/[sbtVersion]/[revision]/ivys/ivy.xml
//
// Repository layout served here:
//
//	/pkgs/ivy/<host>/<path>     e.g. /pkgs/ivy/repo.scala-sbt.org/scalasbt/sbt-plugin-releases/...
//
// The host is the first path segment, so one instance mirrors any Ivy server
// and the host is the repository namespace (isolation + per-host upstreams).
//
// Pull-through caches every fetched file in the CAS. Publish (PUT) stores
// hand-uploaded files under the same key, and the hosted copy always wins.
package ivy

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
	// SelfBase is the external base URL the client used (for rewritten URLs).
	SelfBase string
}

func NewHandler(reg *artifactkit.Registry, cfg map[string]any) (http.Handler, error) {
	s := &State{Registry: reg}
	if a, ok := cfg["auth"].(artifactkit.Auth); ok {
		s.Auth = a
	}
	if v, ok := cfg["self_base"].(string); ok {
		s.SelfBase = strings.TrimSuffix(v, "/")
	}
	return s, nil
}

func init() {
	artifactkit.Register("ivy", NewHandler)
	// The first path segment is the upstream host (repo.scala-sbt.org), which
	// is also the namespace the files are stored under.
	artifactkit.RegisterNamespace("ivy", artifactkit.FirstSegNamespace)
}

// ServeHTTP serves one upstream's Ivy tree. The host comes from the origin the
// client used (X-Forwarded-Host via the repo scope) when it names a real
// server, else from an explicit leading path segment, so both the rewritten
// (repo.scala-sbt.org/...) and explicit (/pkgs/ivy/repo.scala-sbt.org/...)
// forms work.
func (s *State) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/pkgs/ivy/"), "/")
	path = strings.TrimPrefix(path, "ivy/")

	host := artifactkit.CanonicalHost(artifactkit.RepoScopeFrom(r.Context()).Host)
	if !artifactkit.IsPublicHostname(host) {
		host = ""
	}
	if host != "" {
		if path == "" {
			artifactkit.Error(w, http.StatusNotFound, "path required")
			return
		}
		s.serve(w, r, host, path)
		return
	}
	host, filePath, ok := artifactkit.SplitRepo(path)
	if !ok || host == "" || filePath == "" {
		artifactkit.Error(w, http.StatusNotFound, "host and path required")
		return
	}
	s.serve(w, r, artifactkit.CanonicalHost(host), filePath)
}

func (s *State) serve(w http.ResponseWriter, r *http.Request, host, filePath string) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		s.get(w, r, host, filePath)
	case http.MethodPut, http.MethodPost:
		s.put(w, r, host, filePath)
	case http.MethodDelete:
		if !artifactkit.AuthorizeWriteFor(w, r, s.Auth, s.Registry, "ivy", filePath) {
			return
		}
		if err := s.Registry.Meta.Delete(r.Context(), "ivy", host, filePath); err != nil {
			artifactkit.Error(w, http.StatusNotFound, "not found")
			return
		}
		artifactkit.JSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// get serves a hosted or cached file, else pulls it through from the upstream.
func (s *State) get(w http.ResponseWriter, r *http.Request, host, filePath string) {
	if art, err := s.Registry.Meta.Get(r.Context(), "ivy", host, filePath); err == nil && len(art.Blobs) > 0 {
		name := filePathBase(filePath)
		ct := art.MediaType
		if ct == "" {
			ct = contentTypeOf(filePath)
		}
		if artifactkit.ServeBlobAtNamed(w, r, s.Registry.Blobs, r.Context(), art.Blobs[0].Digest, ct, name) {
			return
		}
	}
	up, ok := s.Registry.RemoteHost("ivy", host)
	if !ok {
		artifactkit.Error(w, http.StatusNotFound, "unknown upstream host: "+host)
		return
	}
	// Ivy servers redirect to their artifact store (repo.scala-sbt.org →
	// scala.jfrog.io), so the fetch follows redirects.
	data, err := up.GetBytesFollow(r.Context(), "/"+strings.TrimPrefix(filePath, "/"))
	if err != nil {
		if artifactkit.IsUnknown(err) {
			artifactkit.Error(w, http.StatusNotFound, "not found upstream")
			return
		}
		if se, ok := err.(*artifactkit.UpstreamStatusError); ok && se.Status == 404 {
			artifactkit.Error(w, http.StatusNotFound, "not found upstream")
			return
		}
		artifactkit.Error(w, http.StatusBadGateway, "upstream: "+err.Error())
		return
	}
	s.store(r.Context(), host, filePath, data, "pull")
	artifactkit.OctetResponse(w, r, data)
}

// put stores an uploaded file (publish), which then wins over pull-through.
func (s *State) put(w http.ResponseWriter, r *http.Request, host, filePath string) {
	if !artifactkit.AuthorizeWriteFor(w, r, s.Auth, s.Registry, "ivy", filePath) {
		return
	}
	artifactkit.LimitBody(w, r)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		artifactkit.WriteReadErr(w, err)
		return
	}
	s.store(r.Context(), host, filePath, data, "push")
	w.WriteHeader(http.StatusCreated)
}

// store writes bytes into the CAS and indexes them under (host, filePath).
func (s *State) store(ctx context.Context, host, filePath string, data []byte, source string) {
	stored, err := s.Registry.StoreAndHash(ctx, data)
	if err != nil {
		return
	}
	artifactkit.LogMetaErr("ivy store", s.Registry.Meta.Put(ctx, artifactkit.Artifact{
		Format: "ivy", Repository: host, Version: filePath,
		MediaType: contentTypeOf(filePath), Digest: stored.Digest,
		Blobs: []artifactkit.Descriptor{{
			Digest: stored.Digest, Size: stored.Size, Name: filePathBase(filePath),
		}},
		Source: source,
	}))
}

func filePathBase(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// contentTypeOf maps a file extension to the media type ivy clients expect.
func contentTypeOf(p string) string {
	switch {
	case strings.HasSuffix(p, ".xml"):
		return "application/xml"
	case strings.HasSuffix(p, ".jar"):
		return "application/java-archive"
	default:
		return "application/octet-stream"
	}
}
