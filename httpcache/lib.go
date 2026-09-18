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
	// the egress proxy) or the table default. The body is streamed into the
	// CAS (these trees carry 100MB+ index files) and served FROM the store, so
	// a client's Range request is satisfied locally after the first fetch.
	digest, size, ok := s.Registry.FetchToBlob(r.Context(), s.Tree, "/"+path)
	if !ok {
		artifactkit.Error(w, http.StatusNotFound, "not found upstream")
		return
	}
	ct := mediaTypeOf(path)
	artifactkit.LogMetaErr(s.Tree+" cache", s.Registry.Meta.Put(r.Context(), artifactkit.Artifact{
		Format: s.Tree, Repository: "tree", Version: path,
		MediaType: ct, Digest: digest,
		Blobs:  []artifactkit.Descriptor{{Digest: digest, Size: size, Name: pathBase(path)}},
		Source: "pull",
	}))
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
