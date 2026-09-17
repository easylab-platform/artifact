// Package httpcache mirrors the package repositories that are plain HTTP
// directory trees with no protocol of their own: Hackage (Haskell), CRAN (R),
// CPAN (Perl) and LuaRocks. A client fetches an index file and then the
// tarball the index names:
//
//	hackage.haskell.org/01-index.tar.gz
//	hackage.haskell.org/package/<pkg>-<ver>/<pkg>-<ver>.tar.gz
//	cran.r-project.org/src/contrib/PACKAGES.gz
//	cran.r-project.org/src/contrib/<pkg>_<ver>.tar.gz
//	cpan.metacpan.org/modules/02packages.details.txt.gz
//	cpan.metacpan.org/authors/id/<A>/<AB>/<AUTHOR>/<Dist>.tar.gz
//	luarocks.org/manifest-5.4
//	luarocks.org/<rock>.src.rock
//	pkg.julialang.org/registries
//	pkg.julialang.org/meta
//	pkg.julialang.org/<uuid>/<treehash>
//
// One implementation covers all of them: a path-keyed pull-through cache.
// Each tree is its own format (so the upstream table, repository namespace and
// per-repo overrides all work per tree).
//
// Repository layout served here:
//
//	/pkgs/<tree>/<path>    e.g. /pkgs/cran/src/contrib/PACKAGES.gz
//
// Host-driven mode works too: the egress proxy rewrites
// https://cran.r-project.org/... onto /pkgs/cran/... with the original Host
// preserved, so the mirror is transparent to an unmodified client.
package httpcache

import (
	"context"
	"net/http"
	"strings"

	"github.com/easylab-platform/artifact/core"
)

// trees lists the formats this package serves. The value is only a hint for
// documentation: the upstream comes from the registry's table (or the client's
// Host), never from here.
var trees = []string{"hackage", "cran", "cpan", "luarocks", "juliapkg"}

type State struct {
	Registry *artifactkit.Registry
	Auth     artifactkit.Auth
	// Tree is the format this handler serves (hackage, cran, cpan, luarocks).
	Tree string
}

func newHandler(tree string) func(reg *artifactkit.Registry, cfg map[string]any) (http.Handler, error) {
	return func(reg *artifactkit.Registry, cfg map[string]any) (http.Handler, error) {
		s := &State{Registry: reg, Tree: tree}
		if a, ok := cfg["auth"].(artifactkit.Auth); ok {
			s.Auth = a
		}
		return s, nil
	}
}

func init() {
	for _, tree := range trees {
		artifactkit.Register(tree, newHandler(tree))
	}
}

// ServeHTTP serves one tree path, cache-first.
func (s *State) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/pkgs/"+s.Tree)
	path = strings.TrimPrefix(path, "/"+s.Tree)
	path = strings.Trim(path, "/")
	if path == "" {
		artifactkit.JSON(w, http.StatusOK, map[string]any{"tree": s.Tree})
		return
	}
	// Local cache first (streamed, with HEAD/Range semantics).
	if art, err := s.Registry.Meta.Get(r.Context(), s.Tree, "tree", path); err == nil && len(art.Blobs) > 0 {
		ct := art.MediaType
		if ct == "" {
			ct = mediaTypeOf(path)
		}
		if artifactkit.ServeBlobAtNamed(w, r, s.Registry.Blobs, r.Context(), art.Blobs[0].Digest, ct, pathBase(path)) {
			return
		}
	}
	// Pull-through: the host-driven origin (when the request arrived through
	// the egress proxy) or the table default. Redirects are followed because
	// a tree root may point at a regional mirror (pkg.julialang.org).
	fetched, err := s.Registry.FetchPathFollow(r.Context(), s.Tree, "/"+path)
	if err != nil {
		artifactkit.Error(w, http.StatusNotFound, "not found upstream")
		return
	}
	ct := mediaTypeOf(path)
	if digest := storeCache(s, r.Context(), path, fetched.Data, ct); digest != "" &&
		artifactkit.ServeBlobAtNamed(w, r, s.Registry.Blobs, r.Context(), digest, ct, pathBase(path)) {
		return
	}
	artifactkit.OctetResponse(w, r, fetched.Data)
}

func storeCache(s *State, ctx context.Context, path string, data []byte, mediaType string) string {
	stored, err := s.Registry.StoreAndHash(ctx, data)
	if err != nil {
		return ""
	}
	artifactkit.LogMetaErr(s.Tree+" cache", s.Registry.Meta.Put(ctx, artifactkit.Artifact{
		Format: s.Tree, Repository: "tree", Version: path,
		MediaType: mediaType, Digest: stored.Digest,
		Blobs:  []artifactkit.Descriptor{{Digest: stored.Digest, Size: stored.Size, Name: pathBase(path)}},
		Source: "pull",
	}))
	return stored.Digest
}

func pathBase(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// mediaTypeOf keeps the content type the client expects for cached bytes.
func mediaTypeOf(p string) string {
	switch {
	case strings.HasSuffix(p, ".gz"):
		return "application/gzip"
	case strings.HasSuffix(p, ".xz"):
		return "application/x-xz"
	case strings.HasSuffix(p, ".json"):
		return "application/json"
	default:
		return "application/octet-stream"
	}
}
