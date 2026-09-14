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

// Hosted repository support. A "hosted" path segment in an adapter's URL
// namespace routes to this shared handler instead of upstream pull-through:
//
//	/pkgs/<format>/hosted/<repo>/<pkg path>    upload / download
//	/pkgs/<format>/hosted/<repo>/<generated>   generated index (per adapter)
//
// Uploads store bytes in the CAS and record an artifact row; downloads
// stream from the CAS. Index generation is adapter-specific (each adapter
// passes a Generator), because the file format differs per ecosystem.

// HostedStore is the minimal backend the hosted handler needs (satisfied by
// Registry).
type HostedStore struct {
	Registry *Registry
}

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
		Format: format, Repository: "hosted/" + repo, Version: name,
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
	art, err := h.Registry.Meta.Get(ctx, format, "hosted/"+repo, name)
	if err != nil || len(art.Blobs) == 0 {
		return "", false
	}
	return art.Blobs[0].Digest, true
}

// HostedList returns every hosted file name under a repo (sorted).
func (h HostedStore) HostedList(ctx context.Context, format, repo string) ([]string, error) {
	vs, err := h.Registry.Meta.ListVersions(ctx, format, "hosted/"+repo)
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
	if err := h.Registry.Meta.Delete(ctx, format, "hosted/"+repo, name); err != nil {
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

// HostedHandler serves the hosted paths for one format.
type HostedHandler struct {
	Format    string
	Store     HostedStore
	Auth      Auth
	Generator Generator
}

// ServeHTTP dispatches /pkgs/<format>/hosted/<repo>/<name>.
func (h *HostedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request, sub string, prefix string) bool {
	rest := strings.TrimPrefix(sub, "hosted/")
	repo, name, ok := cutRepoName(rest)
	if !ok {
		Error(w, http.StatusNotFound, "not found")
		return true
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if h.Generator != nil {
			if gf, ok := h.generated(r.Context(), repo, name); ok {
				w.Header().Set("Content-Type", gf.ContentType)
				w.Header().Set("Content-Length", fmt.Sprint(len(gf.Body)))
				w.WriteHeader(http.StatusOK)
				if r.Method != http.MethodHead {
					_, _ = w.Write(gf.Body)
				}
				return true
			}
		}
		if digest, ok := h.Store.HostedFile(r.Context(), h.Format, repo, name); ok {
			if ServeBlobAt(w, r, h.Store.Registry.Blobs, r.Context(), digest, "application/octet-stream") {
				return true
			}
		}
		Error(w, http.StatusNotFound, "not found")
		return true
	case http.MethodPut, http.MethodPost:
		if !AuthorizeWrite(w, r, h.Auth) {
			return true
		}
		if _, _, err := h.Store.HostedUpload(r.Context(), h.Format, repo, name, r.Body); err != nil {
			Error(w, http.StatusBadRequest, "upload: "+err.Error())
			return true
		}
		_ = prefix // reserved
		w.WriteHeader(http.StatusCreated)
		return true
	case http.MethodDelete:
		if !AuthorizeWrite(w, r, h.Auth) {
			return true
		}
		if err := h.Store.HostedDelete(r.Context(), h.Format, repo, name); err != nil {
			Error(w, http.StatusInternalServerError, err.Error())
			return true
		}
		w.WriteHeader(http.StatusNoContent)
		return true
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return true
	}
}

// generated regenerates the repo index and returns the requested member.
// The requested name must be a generated index document (or, for adapters
// whose index location is fixed, match the generator's canonical name).
func (h *HostedHandler) generated(ctx context.Context, repo, name string) (GeneratedFile, bool) {
	names, err := h.Store.HostedList(ctx, h.Format, repo)
	if err != nil {
		return GeneratedFile{}, false
	}
	files := make([]HostedFile, 0, len(names))
	for _, n := range names {
		digest, ok := h.Store.HostedFile(ctx, h.Format, repo, n)
		if !ok {
			continue
		}
		files = append(files, HostedFile{Name: n, Digest: digest})
	}
	out, err := h.Generator(files)
	if err != nil {
		return GeneratedFile{}, false
	}
	gf, ok := out[name]
	return gf, ok
}

// cutRepoName splits "<repo>/<name>" where repo is one path segment.
func cutRepoName(rest string) (repo, name string, ok bool) {
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return "", "", false
	}
	i := strings.IndexByte(rest, '/')
	if i < 0 {
		return "", "", false
	}
	return rest[:i], path.Clean(rest[i+1:]), true
}

// baseName is path.Base without the os import.
func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}
