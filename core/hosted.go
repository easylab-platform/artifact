package artifactkit

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"
)

// Hosted repository support. A repository key (the first path segment after
// the format) names a repository in one namespace shared by proxied and
// hosted repos:
//
//	/pkgs/<format>/<repo>/<name>    upload / download / generated index
//
// A GET for a repo that has hosted content is served from the CAS (or a
// generated index); otherwise the request is a proxied fetch. Uploads always
// target the hosted store. Internally hosted rows live under the repository
// name "hosted/<repo>" so they never collide with pull-through cache rows.

// HostedStore is the minimal backend the hosted handler needs (satisfied by
// Registry).
type HostedStore struct {
	Registry *Registry
}

// SplitRepo splits a format-relative path into a repository key (its first
// segment) and the remaining path inside it. rest may be empty (repo root).
func SplitRepo(p string) (repo, rest string, ok bool) {
	p = strings.Trim(p, "/")
	if p == "" {
		return "", "", false
	}
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i], p[i+1:], true
	}
	return p, "", true
}

// repoKey is the internal metadata repository name for a hosted repo.
func repoKey(repo string) string { return "hosted/" + repo }

// HostedUpload stores uploaded bytes under (format, repo, name) and indexes
// them. It returns the digest and size.
func (h HostedStore) HostedUpload(ctx context.Context, format, repo, name string, r io.Reader) (string, int64, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", 0, err
	}
	stored, err := h.Registry.StoreAndHash(ctx, data)
	if err != nil {
		return "", 0, err
	}
	if err := h.Registry.Meta.Put(ctx, Artifact{
		Format: format, Repository: repoKey(repo), Version: name,
		MediaType: "application/octet-stream", Digest: stored.Digest,
		Blobs:  []Descriptor{{Digest: stored.Digest, Size: stored.Size, Name: baseName(name)}},
		Source: "push",
	}); err != nil {
		return "", 0, err
	}
	return stored.Digest, stored.Size, nil
}

// HostedFile returns the stored digest for one hosted file ("" when absent).
func (h HostedStore) HostedFile(ctx context.Context, format, repo, name string) (string, bool) {
	art, err := h.Registry.Meta.Get(ctx, format, repoKey(repo), name)
	if err != nil || len(art.Blobs) == 0 {
		return "", false
	}
	return art.Blobs[0].Digest, true
}

// HostedList returns every hosted file name under a repo (sorted).
func (h HostedStore) HostedList(ctx context.Context, format, repo string) ([]string, error) {
	vs, err := h.Registry.Meta.ListVersions(ctx, format, repoKey(repo))
	if err != nil {
		return nil, err
	}
	sort.Strings(vs)
	return vs, nil
}

// HostedDelete removes one hosted file and its CAS blob when unreferenced.
func (h HostedStore) HostedDelete(ctx context.Context, format, repo, name string) error {
	digest, ok := h.HostedFile(ctx, format, repo, name)
	if !ok {
		return nil
	}
	if err := h.Registry.Meta.Delete(ctx, format, repoKey(repo), name); err != nil {
		return err
	}
	// Best-effort blob GC: only delete when no other artifact references it.
	if !h.digestReferenced(ctx, digest) {
		LogMetaErr("hosted gc", h.Registry.Blobs.Delete(ctx, digest))
	}
	return nil
}

// digestReferenced reports whether any artifact still points at the digest.
func (h HostedStore) digestReferenced(ctx context.Context, digest string) bool {
	pkgs, err := h.Registry.Meta.ListPackages(ctx)
	if err != nil {
		return true // be conservative
	}
	for _, p := range pkgs {
		art, err := h.Registry.Meta.Get(ctx, p.Format, p.Repository, p.Version)
		if err != nil {
			continue
		}
		if art.Digest == digest {
			return true
		}
		for _, b := range art.Blobs {
			if b.Digest == digest {
				return true
			}
		}
	}
	return false
}

// GeneratedFile is one index document produced by a Generator.
type GeneratedFile struct {
	Body        []byte
	ContentType string
}

// Generator renders the repo's index document(s) from its file list: a map
// from hosted path name to file. Multi-file indexes (rpm's repomd.xml plus
// the primary metadata it references) return several entries. Regeneration
// is deterministic from `files`, so no caching layer is needed.
type Generator func(files []HostedFile) (map[string]GeneratedFile, error)

// HostedFile is one stored file, as seen by a Generator.
type HostedFile struct {
	Name   string
	Digest string
	Size   int64
}

// HostedHandler serves an upload/generated-index repo for one format.
type HostedHandler struct {
	Format    string
	Store     HostedStore
	Auth      Auth
	Generator Generator
}

// Get serves a hosted file or generated index from repo. It reports whether
// the request was handled (true) or the caller should fall back to proxying
// (false) because the repo holds no hosted content.
//
//	repo  the repository key (first path segment)
//	name  the path inside the repo
func (h *HostedHandler) Get(w http.ResponseWriter, r *http.Request, repo, name string) bool {
	if h == nil {
		return false
	}
	name = path.Clean(name)
	if name == "." || name == "" {
		return false
	}
	// A generated index (e.g. repodata/repomd.xml, Packages) takes priority
	// because it is derived, not stored.
	if h.Generator != nil {
		if names, err := h.Store.HostedList(r.Context(), h.Format, repo); err == nil && len(names) > 0 {
			if gf, ok := h.generate(r.Context(), repo, names, name); ok {
				w.Header().Set("Content-Type", gf.ContentType)
				w.Header().Set("Content-Length", fmt.Sprint(len(gf.Body)))
				w.WriteHeader(http.StatusOK)
				if r.Method != http.MethodHead {
					_, _ = w.Write(gf.Body)
				}
				return true
			}
		}
	}
	if digest, ok := h.Store.HostedFile(r.Context(), h.Format, repo, name); ok {
		if ServeBlobAt(w, r, h.Store.Registry.Blobs, r.Context(), digest, "application/octet-stream") {
			return true
		}
	}
	return false
}

// Put stores an uploaded file into repo.
func (h *HostedHandler) Put(w http.ResponseWriter, r *http.Request, repo, name string) {
	// The repo is the namespace for the hosted formats (apk/debian/rpm/conda):
	// scope-aware authorization keeps a repo's contents under one owner.
	if !AuthorizeWriteFor(w, r, h.Auth, h.Store.Registry, h.Format, name) {
		return
	}
	if _, _, err := h.Store.HostedUpload(r.Context(), h.Format, repo, name, r.Body); err != nil {
		Error(w, http.StatusBadRequest, "upload: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// Delete removes a hosted file from repo.
func (h *HostedHandler) Delete(w http.ResponseWriter, r *http.Request, repo, name string) {
	if !AuthorizeWriteFor(w, r, h.Auth, h.Store.Registry, h.Format, name) {
		return
	}
	if err := h.Store.HostedDelete(r.Context(), h.Format, repo, name); err != nil {
		Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// generate regenerates the repo index from a known file list and returns the
// requested member.
func (h *HostedHandler) generate(ctx context.Context, repo string, names []string, name string) (GeneratedFile, bool) {
	files := make([]HostedFile, 0, len(names))
	for _, n := range names {
		digest, ok := h.Store.HostedFile(ctx, h.Format, repo, n)
		if !ok {
			continue
		}
		f := HostedFile{Name: n, Digest: digest}
		if sz, err := h.Store.Registry.Blobs.Stat(ctx, digest); err == nil && sz != nil {
			f.Size = *sz
		}
		files = append(files, f)
	}
	out, err := h.Generator(files)
	if err != nil {
		return GeneratedFile{}, false
	}
	gf, ok := out[name]
	return gf, ok
}

// baseName is path.Base without the os import.
func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}
