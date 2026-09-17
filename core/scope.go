package artifactkit

import (
	"context"
	"strings"
)

// RepoScope is the repository a request targets, resolved from its path. It
// is carried in the request context so the storage and upstream layers can
// isolate content and choose a remote WITHOUT every adapter threading the
// namespace through its calls.
//
// Resolution order for Namespace:
//  1. an explicit "/-/<repo>/" marker in the path (any protocol), else
//  2. the format's registered NamespaceResolver (npm scope, OCI host, maven
//     groupId, go module prefix, ...), else
//  3. DefaultNamespace.
type RepoScope struct {
	Format    string
	Namespace string
	Name      string
	// Explicit is true when the request used the "/-/<repo>/" marker.
	Explicit bool
}

type repoScopeKey struct{}

// WithRepoScope returns a context carrying the request's repository scope.
func WithRepoScope(ctx context.Context, s RepoScope) context.Context {
	return context.WithValue(ctx, repoScopeKey{}, s)
}

// RepoScopeFrom returns the request's repository scope, or the zero value.
func RepoScopeFrom(ctx context.Context) RepoScope {
	s, _ := ctx.Value(repoScopeKey{}).(RepoScope)
	return s
}

// ScopedName maps an adapter-computed repository key onto its fully-qualified
// form for the scope. The namespace is prefixed only when the key does not
// already carry it, so npm's "@scope/name" (where the adapter already includes
// the scope) is left alone while maven's bare artifactId and a flat protocol's
// package name gain the namespace.
func (s RepoScope) ScopedName(key string) string {
	ns := strings.Trim(s.Namespace, "/")
	if ns == "" || ns == DefaultNamespace {
		return key
	}
	if key == ns || strings.HasPrefix(key, ns+"/") {
		return key
	}
	return ns + "/" + key
}

// UnscopedName is ScopedName's inverse for catalog/list filtering.
func (s RepoScope) UnscopedName(key string) string {
	ns := strings.Trim(s.Namespace, "/")
	if ns == "" || ns == DefaultNamespace {
		return key
	}
	if key == ns {
		return ""
	}
	if strings.HasPrefix(key, ns+"/") {
		return key[len(ns)+1:]
	}
	return ""
}
