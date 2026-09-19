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
//	/artifacts/<tree>/<path>    e.g. /artifacts/cran/src/contrib/PACKAGES.gz
//
// Host-driven mode works too: the egress proxy rewrites
// https://cran.r-project.org/... onto /artifacts/cran/... with the original Host
// preserved, so the mirror is transparent to an unmodified client.
package httpcache

import (
	"net/http"
	"strings"

	"github.com/easylab-platform/artifact/core"
)

// trees lists the formats this package serves. The value is only a hint for
// documentation: the upstream comes from the registry's table (or the client's
// Host), never from here.
//
// Most are plain package trees; a few carry a JSON API on the same paths:
//   - jsr     JSR's native registry API: /@scope/pkg/meta.json,
//     /@scope/pkg/<ver>_meta.json and /@scope/pkg/<ver>/<path> modules.
//   - bazel   Bazel Central Registry: /modules/<name>/<ver>/*.json + source.json.
//   - jenkins Jenkins update center: /download/plugins/... and update-center.json.
//   - opam    opam repository index + cache archives.
var trees = []string{
	"hackage", "cran", "cpan", "luarocks", "juliapkg",
	"jsr", "opam", "stackage", "pecl", "bazel", "jenkins",
}

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
	path := strings.TrimPrefix(r.URL.Path, "/artifacts/"+s.Tree)
	path = strings.TrimPrefix(path, "/"+s.Tree)
	path = strings.Trim(path, "/")
	if path == "" {
		artifactkit.JSON(w, http.StatusOK, map[string]any{"tree": s.Tree})
		return
	}
	// Cache-first with shared freshness handling (single-flight, TTL +
	// conditional revalidation). The body streams into the CAS, so a client's
	// Range request is satisfied locally after the first fetch; these trees
	// carry 100MB+ index files.
	ct := mediaTypeOf(path)
	digest, _, _, ok := s.Registry.FetchCachedPath(r.Context(), s.Tree, "tree", path, "/"+path, "",
		artifactkit.PathCachePolicy{MediaType: ct, BlobName: pathBase(path)})
	if !ok {
		artifactkit.Error(w, http.StatusNotFound, "not found upstream")
		return
	}
	if artifactkit.ServeBlobAtNamed(w, r, s.Registry.Blobs, r.Context(), digest, ct, pathBase(path)) {
		return
	}
	artifactkit.Error(w, http.StatusBadGateway, "cache error")
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
	case strings.HasSuffix(p, ".ts"), strings.HasSuffix(p, ".mts"), strings.HasSuffix(p, ".cts"):
		return "application/typescript"
	case strings.HasSuffix(p, ".js"), strings.HasSuffix(p, ".mjs"), strings.HasSuffix(p, ".cjs"):
		return "text/javascript"
	case strings.HasSuffix(p, ".wasm"):
		return "application/wasm"
	case strings.HasSuffix(p, ".md"):
		return "text/markdown"
	case strings.HasSuffix(p, ".txt"):
		return "text/plain; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}
