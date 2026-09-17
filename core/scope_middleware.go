package artifactkit

import (
	"net/http"
	"strings"
)

// ScopeMiddleware resolves a request's RepoScope and stashes it in the request
// context. It runs for EVERY format, so no adapter needs to know about
// namespaces: the scoping IndexStore decorator applies the namespace when the
// adapter reads or writes metadata, and Registry.Fetch consults it to choose
// the upstream.
//
// Two addressing forms are understood:
//
//	/pkgs/<fmt>/<native-path>            native (namespace from the resolver)
//	/pkgs/<fmt>/-/<repo>/<native-path>   explicit repository
//
// The explicit form is rewritten to the native path with the repository in the
// context, so adapters only ever see their normal URL shape.
//
// base is the mount prefix "/pkgs"; OCI's /v2 uses ScopeMiddlewareForFormat.
func ScopeMiddleware(base string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scope, path, ok := resolveScope(base, r)
		if ok {
			r.URL.Path = path
			r = r.WithContext(WithRepoScope(r.Context(), scope))
		}
		next.ServeHTTP(w, r)
	})
}

// ScopeMiddlewareForFormat is ScopeMiddleware for a mount whose format is
// fixed by the route rather than present in the path (OCI's /v2): the whole
// path inside base is the native path, and only the explicit "/-/<repo>/"
// marker (if any) names a repository.
func ScopeMiddlewareForFormat(format, base string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scope, path, ok := resolveScopeForFormat(format, base, r)
		if ok {
			r.URL.Path = path
			r = r.WithContext(WithRepoScope(r.Context(), scope))
		}
		next.ServeHTTP(w, r)
	})
}

// dashReserved prevents a format's own "-/" endpoints from being read as an
// explicit-repository marker. npm uses "/-/ping", "/-/whoami", "/-/v1/...",
// "/-/package/...", "/-/npm/...", "/-/user/..."; anything else after "-/"
// (e.g. "/-/private/@team/pub") is an explicit repository.
var npmReservedDash = map[string]bool{
	"ping": true, "whoami": true, "v1": true, "package": true,
	"npm": true, "user": true,
}

// explicitRepoMarker returns the repository named by an explicit marker, or ok
// false when the path is not an explicit-repo form for this format.
func explicitRepoMarker(format, nativePath string) (repo, rest string, ok bool) {
	first, after, split := SplitRepo(nativePath)
	if !split || first != ExplicitRepoMarker {
		return "", "", false
	}
	if format == "npm" {
		second, _, _ := SplitRepo(after)
		if npmReservedDash[second] {
			return "", "", false
		}
	}
	repo, rest, ok = SplitRepo(after)
	if !ok || repo == "" {
		return "", "", false
	}
	return repo, rest, true
}

// resolveScope computes the RepoScope for a /pkgs/<fmt>/... request. ok is
// false when the path does not belong to the mount (then it is passed through).
func resolveScope(base string, r *http.Request) (RepoScope, string, bool) {
	p := r.URL.Path
	prefix := base + "/"
	if !strings.HasPrefix(p, prefix) {
		return RepoScope{}, p, false
	}
	rest := p[len(prefix):]
	format, nativePath, ok := SplitRepo(rest)
	if !ok {
		return RepoScope{}, p, false
	}

	scope := RepoScope{Format: format, Host: CanonicalHost(requestHost(r)),
		Proto: forwardedProto(r), Prefix: forwardedPrefix(r)}

	// Explicit "/-/<repo>/" marker: the repository is given verbatim and the
	// path is rewritten to the native form for the adapter.
	if repo, inner, explicit := explicitRepoMarker(format, nativePath); explicit {
		scope.Namespace, scope.Explicit = repo, true
		scope.Name, _, _ = ResolveNamespace(format, inner)
		return scope, prefix + format + "/" + strings.Trim(inner, "/"), true
	}

	// Explicit `?repository=<repo>` selects a repository too. Protocols whose
	// publish path has no room for a repository segment (the flat formats:
	// pypi, rubygems, composer, pub, hex, ...) take it from the query, which is
	// how the publish templates tag a private repo. It must be a single clean
	// segment so it cannot smuggle a path into the storage key.
	if repo := strings.TrimSpace(r.URL.Query().Get("repository")); repo != "" && !strings.Contains(repo, "/") {
		scope.Namespace, scope.Explicit = repo, true
	}

	ns, name, _ := ResolveNamespace(format, nativePath)
	if !scope.Explicit {
		scope.Namespace = ns
	}
	scope.Name = name
	return scope, p, true
}

// requestHost returns the origin hostname the client used: the egress proxy's
// X-Forwarded-Host when present (the spoofed upstream name), else the request
// Host (a direct client, or a test).
func requestHost(r *http.Request) string {
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		return h
	}
	return r.Host
}

func forwardedProto(r *http.Request) string {
	p := strings.ToLower(r.Header.Get("X-Forwarded-Proto"))
	if p == "http" || p == "https" {
		return p
	}
	if r.TLS != nil {
		return "https"
	}
	return ""
}

func forwardedPrefix(r *http.Request) string {
	return r.Header.Get("X-Forwarded-Prefix")
}

// resolveScopeForFormat is resolveScope for a mount whose format is fixed by
// the route (OCI's /v2). The path inside base is the native path, and the
// request Host is the registry — a container client never puts the registry in
// the path, so the Host is where it is learned (and what namespaces the
// repository, keeping ghcr.io/foo and docker.io/foo distinct).
func resolveScopeForFormat(format, base string, r *http.Request) (RepoScope, string, bool) {
	p := r.URL.Path
	prefix := base + "/"
	if p != base && !strings.HasPrefix(p, prefix) {
		return RepoScope{}, p, false
	}
	nativePath := strings.TrimPrefix(strings.TrimPrefix(p, base), "/")
	scope := RepoScope{Format: format, Host: CanonicalHost(requestHost(r)),
		Proto: forwardedProto(r), Prefix: forwardedPrefix(r)}
	if repo, inner, explicit := SplitExplicitRepo(nativePath); explicit {
		scope.Namespace, scope.Explicit = repo, true
		scope.Name, _, _ = ResolveNamespace(format, inner)
		return scope, base + "/" + strings.Trim(inner, "/"), true
	}
	ns, name, _ := ResolveNamespace(format, nativePath)
	if ns == "" && IsRegistryHost(scope.Host) {
		ns = scope.Host
	}
	scope.Namespace, scope.Name = ns, name
	return scope, p, true
}
