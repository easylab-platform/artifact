package artifactkit

import "strings"

// Namespace support. A repository is the content boundary of one format: it
// owns its content, its upstream policy, its publish rights and its
// visibility. Internally a repository is encoded as a plain string key
// ("namespace/name", or just "name" when the namespace is the implicit
// default), so the metadata store needs no new column and existing rows stay
// valid.
//
// Protocol adapters know how their native path maps onto (namespace, name):
// npm takes the scope, OCI the registry host, maven the groupId, go the module
// prefix. That knowledge lives in a NamespaceResolver registered per format,
// so the routing layer stays protocol-agnostic.

// DefaultNamespace is the namespace of everything that carries no explicit
// namespace of its own.
const DefaultNamespace = "default"

// ExplicitRepoMarker introduces an explicit repository in a request path:
//
//	/pkgs/npm/-/@acme/ui        -> repo @acme, path ui
//	/pkgs/pypi/-/internal/simple -> repo internal, path simple
//
// "-" is safe as a marker: npm reserves a bare "-/" itself, OCI repository
// names cannot be a lone "-", and no protocol has a real path segment that is
// exactly "-". Requests without the marker keep their native shape.
const ExplicitRepoMarker = "-"

// RepoKey encodes (namespace, name) into the canonical repository string.
// The default namespace is elided so unscoped names keep their historical key
// (and existing metadata rows keep resolving).
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

// SplitRepoKey reverses RepoKey: it returns the leading namespace and the
// remainder. A key with no "/" has the default namespace.
func SplitRepoKey(key string) (namespace, name string) {
	key = strings.Trim(key, "/")
	if i := strings.IndexByte(key, '/'); i >= 0 {
		return key[:i], key[i+1:]
	}
	return "", key
}

// NamespaceResolver maps a format-relative request path onto a namespace, a
// package name and the remainder of the path (the part inside the package).
// Returning ns=="" means the default namespace.
type NamespaceResolver func(format, path string) (namespace, name, rest string)

var namespaceResolvers = map[string]NamespaceResolver{}

// RegisterNamespace installs a format's resolver. Called from adapter init()
// next to Register. Adapters that do not register get FlatNamespace.
func RegisterNamespace(format string, fn NamespaceResolver) {
	if format == "" || fn == nil {
		return
	}
	namespaceResolvers[format] = fn
}

// Built-in resolvers. Registering them here (rather than only in the adapter
// packages) means any embedder — easylab mounts the same core — gets
// namespace-aware routing without importing every adapter package. Adapters may
// re-register the same function; the assignment is idempotent.
func init() {
	RegisterNamespace("npm", ScopeNamespace)
	RegisterNamespace("maven", MavenNamespace)
	RegisterNamespace("go", GoModuleNamespace)
	RegisterNamespace("oci", OCIHostNamespace)
	RegisterNamespace("apk", FirstSegNamespace)
	RegisterNamespace("debian", FirstSegNamespace)
	RegisterNamespace("rpm", FirstSegNamespace)
	RegisterNamespace("conda", FirstSegNamespace)
}

// ResolveNamespace applies the format's resolver, defaulting to FlatNamespace.
func ResolveNamespace(format, path string) (namespace, name, rest string) {
	if fn, ok := namespaceResolvers[format]; ok {
		return fn(format, path)
	}
	return FlatNamespace(format, path)
}

// FlatNamespace is the default resolver: the first path segment is the name
// and the rest is the in-package remainder. It matches the historical
// behavior of the flat formats (pypi, rubygems, composer, ...).
func FlatNamespace(_ /*format*/, path string) (namespace, name, rest string) {
	path = strings.Trim(path, "/")
	if path == "" {
		return "", "", ""
	}
	if i := strings.IndexByte(path, '/'); i >= 0 {
		return "", path[:i], path[i+1:]
	}
	return "", path, ""
}

// FirstSegNamespace resolves a protocol whose first path segment is already
// the repository (apk/debian/rpm/conda and the hosted repo model): the first
// segment is the namespace, the whole remainder is the name (these protocols
// treat the artifact path as one opaque object inside the repo).
func FirstSegNamespace(_ /*format*/, path string) (namespace, name, rest string) {
	path = strings.Trim(path, "/")
	if path == "" {
		return "", "", ""
	}
	ns, rem, _ := SplitRepo(path)
	return ns, rem, ""
}

// ScopeNamespace resolves npm-style scoped names: a leading "@scope" is the
// namespace and the rest is the package name (which may itself contain a
// slash, e.g. "@scope/pkg/sub" for a tarball path).
func ScopeNamespace(_ /*format*/, path string) (namespace, name, rest string) {
	path = strings.Trim(path, "/")
	if path == "" {
		return "", "", ""
	}
	if !strings.HasPrefix(path, "@") {
		// Unscoped: the package name is the first segment.
		if i := strings.IndexByte(path, '/'); i >= 0 {
			return "", path[:i], path[i+1:]
		}
		return "", path, ""
	}
	i := strings.IndexByte(path, '/')
	if i < 0 {
		return "", path, ""
	}
	namespace = path[:i] // "@scope"
	rest = path[i+1:]
	// rest is the package name followed (optionally) by a deeper path.
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		return namespace, rest[:j], rest[j+1:]
	}
	return namespace, rest, ""
}

// SplitExplicitRepo strips an explicit "/-/<repo>/" prefix from a
// format-relative path. It reports whether the marker was present; when it is
// absent the path is returned unchanged (native addressing).
func SplitExplicitRepo(path string) (repo, rest string, explicit bool) {
	path = strings.Trim(path, "/")
	if path == "" {
		return "", "", false
	}
	first, rest, ok := SplitRepo(path)
	if !ok || first != ExplicitRepoMarker {
		return "", path, false
	}
	return SplitRepo(rest)
}

// MavenNamespace resolves a maven path into (groupId, artifactId). Maven's
// layout is <group>/<artifact>/<version>/<file> with a variable-length group,
// plus <group>/<artifact>/maven-metadata.xml for enumeration, so the name and
// group are found from the right: strip the metadata/file tail, then the
// version, leaving group + artifact.
//
//	org/slf4j/slf4j-api/2.0.9/slf4j-api-2.0.9.pom -> ("org.slf4j", "slf4j-api")
//	org/slf4j/slf4j-api/maven-metadata.xml        -> ("org.slf4j", "slf4j-api")
//	com/google/guava/guava/33.0.0/guava-33.0.0.jar -> ("com.google.guava", "guava")
func MavenNamespace(_ /*format*/, path string) (namespace, name, rest string) {
	p := strings.Trim(path, "/")
	p = strings.TrimSuffix(p, ".sha1")
	p = strings.TrimSuffix(p, ".md5")
	p = strings.TrimSuffix(p, "/maven-metadata.xml")
	p = strings.TrimSuffix(p, "/")
	parts := splitNonEmpty(p)
	// Drop the trailing filename when present (it contains a dot; directory
	// segments like "org", "slf4j" or a version "2.0.9" are handled next).
	if n := len(parts); n > 0 && strings.Contains(parts[n-1], ".") && !looksLikeVersion(parts[n-1]) {
		parts = parts[:n-1]
	}
	// Drop the version.
	if n := len(parts); n > 0 && looksLikeVersion(parts[n-1]) {
		parts = parts[:n-1]
	}
	if len(parts) < 2 {
		if len(parts) == 1 {
			return "", parts[0], ""
		}
		return "", "", ""
	}
	artifactID := parts[len(parts)-1]
	groupID := strings.Join(parts[:len(parts)-1], ".")
	return groupID, artifactID, ""
}

func splitNonEmpty(p string) []string {
	out := []string{}
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// looksLikeVersion is a light semver-ish check for maven version segments.
func looksLikeVersion(s string) bool {
	if s == "" || s[0] < '0' || s[0] > '9' {
		return false
	}
	for _, c := range s {
		if c >= '0' && c <= '9' {
			continue
		}
		switch c {
		case '.', '-', '_', '+':
			continue
		}
		// Common qualifier letters (1.0.0-alpha, 1.0-RC1, 2.0.Final).
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			continue
		}
		return false
	}
	return true
}

// GoModuleNamespace resolves a Go module path into (namespace, module): the
// namespace is the hosting prefix (host + owner for the common two-segment
// case) and the name is the module path itself, so an upstream override keyed
// "go/github.com/acme" covers every module under that owner.
//
//	github.com/acme/tool/sub -> ("github.com/acme", "github.com/acme/tool/sub")
//	golang.org/x/text        -> ("golang.org/x", "golang.org/x/text")
func GoModuleNamespace(_ /*format*/, path string) (namespace, name, rest string) {
	p := strings.Trim(path, "/")
	if p == "" {
		return "", "", ""
	}
	parts := strings.Split(p, "/")
	if len(parts) >= 2 {
		return parts[0] + "/" + parts[1], p, ""
	}
	return "", p, ""
}

// OCIHostNamespace resolves an OCI repository reference into (registry-host,
// repository). A leading component is treated as a host when it contains a dot
// or colon or is "localhost" (the registry convention); otherwise there is no
// host and the repository is the whole name.
//
//	ghcr.io/acme/app          -> ("ghcr.io", "acme/app")
//	localhost:5000/acme/app   -> ("localhost:5000", "acme/app")
//	acme/app                  -> ("", "acme/app")
func OCIHostNamespace(_ /*format*/, path string) (namespace, name, rest string) {
	p := strings.Trim(path, "/")
	// OCI request paths carry action suffixes; trim them so the scope is the
	// repository, not the manifest/blob being addressed.
	for _, marker := range []string{"/manifests/", "/blobs/uploads/", "/blobs/", "/referrers/"} {
		if i := strings.LastIndex(p, marker); i >= 0 {
			p = p[:i]
			break
		}
	}
	p = strings.TrimSuffix(p, "/tags/list")
	first, remainder, ok := SplitRepo(p)
	if !ok {
		return "", "", ""
	}
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		return first, remainder, ""
	}
	return "", p, ""
}
