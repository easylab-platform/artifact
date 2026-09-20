package artifactkit

import (
	"context"
	"strings"
)

// RepoScope is the repository a request targets, resolved once at the mount
// boundary. It is carried in the request context so the storage and upstream
// layers can isolate content and choose a remote WITHOUT every adapter
// threading the namespace through its calls.
//
// Namespace resolution order:
//  1. an explicit "/-/<repo>/" marker in the path, else
//  2. the format's NamespaceResolver (npm scope, maven groupId, go module
//     prefix, apk/debian/rpm/conda first segment, OCI registry host), else
//  3. DefaultNamespace.
//
// Host is the request's original Host header. It matters for OCI: a docker
// client puts the registry in SNI/Host, never in the path, so the host is the
// only place the registry can be learned. The OCI resolver reads it from here.
//
// Proto and Prefix describe the origin the CLIENT reached us by, when the
// request came through the egress proxy (which sets X-Forwarded-Proto and
// X-Forwarded-Prefix next to preserving the Host). Together with Host they let
// the upstream be reconstructed without a per-ecosystem table:
//
//	<proto>://<host><prefix><format-relative-path>
type RepoScope struct {
	Format    string
	Namespace string
	Name      string
	Host      string
	Proto     string
	Prefix    string
	// Explicit is true when the request used the "/-/<repo>/" marker.
	Explicit bool
	// Target is the target id the request was routed through ("maven",
	// "maven.google"). Empty means the protocol's default target.
	Target string
	// TargetShared is true when the target stores into the protocol's shared
	// namespace (mirrors) rather than an isolated one (user-declared private
	// repositories). Only meaningful when Target is set.
	TargetShared bool
	// ClientAuth is the request's Authorization header, captured so a target
	// with passthrough auth can forward the caller's own credential upstream.
	// It is never logged or stored as key material.
	ClientAuth string
}

// RepoKey encodes (namespace, name) into the canonical repository string. The
// default namespace is elided so unscoped names keep their historical key (and
// existing metadata rows keep resolving). It is the primitive behind
// RepoScope.ScopedKey, which adds the isolated-target prefix on top.
func RepoKey(namespace, name string) string {
	namespace = strings.Trim(namespace, "/")
	name = strings.Trim(name, "/")
	if namespace == "" || namespace == DefaultNamespace {
		return name
	}
	if name == "" {
		return namespace
	}
	return namespace + "/" + name
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

// MirrorOrigin returns the scheme://host[/prefix] a request arrived on when it
// was routed through a NAMED target (a mirror/alias such as npm.jsr.io), else
// "". An adapter uses it to emit self-URLs in the shape the client dialed, so
// an alias that shares an adapter with its base protocol (JSR's
// npm-compatibility registry riding the npm adapter) advertises its own origin
// instead of the base protocol's. The default target returns "": its origin is
// the protocol's public home, and emitting that would bypass the gateway.
func MirrorOrigin(ctx context.Context) string {
	sc := RepoScopeFrom(ctx)
	if sc.Target == "" || sc.Target == sc.Format || sc.Host == "" {
		return ""
	}
	proto := sc.Proto
	if proto != "http" && proto != "https" {
		proto = "https"
	}
	return proto + "://" + sc.Host
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
	// Idempotent: a key that already carries its namespace (npm's
	// "@scope/name", conda's "channel/subdir") is left unchanged, so adapters
	// that encode the namespace themselves are not double-prefixed.
	if key == ns || strings.HasPrefix(key, ns+"/") {
		return key
	}
	return RepoKey(ns, key)
}

// targetPrefix is the storage key prefix that isolates a target's content from
// the protocol's shared namespace: "" for a shared (mirror) target or the
// default target, "t:<id>" for an isolated (user-declared private) one.
func (s RepoScope) targetPrefix() string {
	if s.Target == "" || s.TargetShared {
		return ""
	}
	return "t:" + s.Target
}

// ScopedKey is the canonical storage key for an adapter-supplied repository:
// the target prefix (when isolated) around the namespace-qualified name.
func (s RepoScope) ScopedKey(key string) string {
	key = s.ScopedName(key)
	if p := s.targetPrefix(); p != "" {
		if key == p || strings.HasPrefix(key, p+"/") {
			return key
		}
		return p + "/" + key
	}
	return key
}

// UnscopedKey is ScopedKey's inverse for metadata reads/list filtering: it
// strips the target prefix first, then the namespace.
func (s RepoScope) UnscopedKey(key string) string {
	if p := s.targetPrefix(); p != "" {
		if key == p {
			return ""
		}
		if strings.HasPrefix(key, p+"/") {
			key = key[len(p)+1:]
		}
	}
	return s.UnscopedName(key)
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
	return key
}
