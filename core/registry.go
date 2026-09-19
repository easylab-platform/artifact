package artifactkit

import (
	"context"
	"sort"
	"strings"

	"github.com/easylab-platform/artifact/targets"
)

// MountBase is the HTTP prefix every non-OCI protocol is mounted under. It is
// re-exported from the targets package so callers of artifactkit need not
// import both.
const MountBase = targets.MountBase

// clientFactoryFor returns the process-wide client factory. Formerly a
// separate instance; it now delegates to sharedFactory so OCI's remote
// upstreams share the same connection pool as every other fetch.
func clientFactoryFor(u *Upstreams) *ClientFactory { return sharedFactory }

// Registry is the composed substrate handed to every protocol adapter. It
// bundles the metadata store (mutable index), the blob store (immutable CAS)
// and the upstream table, plus the generic pull-through flows. Adapters only
// depend on the interfaces, so a caller can wire any implementation.
type Registry struct {
	Blobs     BlobStore
	Meta      IndexStore
	Upstreams *Upstreams
	// Owners is the optional package-ownership layer: a GLOBAL namespace per
	// format where every name belongs to exactly one tenant. Nil disables
	// ownership enforcement (single-tenant / dev mode).
	Owners Ownership
	// TargetStore persists user-declared targets. Nil means built-ins only,
	// and the admin API treats target writes as unsupported.
	TargetStore targets.Store
}

// Ownership is the npm-official-style ownership model over the registry's
// global namespace: names are globally unique, publishing requires the
// caller's tenant to own (or first-claim) the name, and private packages are
// readable only by their owning tenant.
//
// A repository is identified by (format, repository) where repository is the
// canonical RepoKey(namespace, name). Implementations that predate namespaces
// implement the base interface; NamespacedOwnership additionally exposes the
// namespace as its own boundary. Callers use the Authorize* helpers, which
// prefer the namespaced form when available.
type Ownership interface {
	// AuthorizePublish returns nil when tenant may publish (format, name).
	// An unclaimed name is claimed for the tenant on first publish; a claimed
	// name requires the same tenant (admins excepted per implementation).
	AuthorizePublish(ctx context.Context, format, repository string, tenantID int64) error
	// CanRead reports whether tenant may read (format, name): true for
	// public/unclaimed names, owning tenant only for private ones.
	CanRead(ctx context.Context, format, repository string, tenantID int64) bool
}

// NamespacedOwnership is the namespace-aware extension of Ownership. A
// namespace (npm scope, OCI host, maven groupId, ...) is a boundary of its
// own: a tenant owning @acme may publish any @acme package; a tenant may also
// be granted read on a whole namespace.
type NamespacedOwnership interface {
	Ownership
	// AuthorizePublishNS authorizes publishing name inside namespace.
	AuthorizePublishNS(ctx context.Context, format, namespace, name string, tenantID int64) error
	// CanReadNS reports whether tenant may read name in namespace.
	CanReadNS(ctx context.Context, format, namespace, name string, tenantID int64) bool
}

// AuthorizePublish resolves the most specific ownership check available:
// NamespacedOwnership when the layer implements it, else the flat form over
// the canonical repository key.
func AuthorizePublish(ctx context.Context, o Ownership, format, namespace, name string, tenantID int64) error {
	if o == nil {
		return nil
	}
	if ns, ok := o.(NamespacedOwnership); ok {
		return ns.AuthorizePublishNS(ctx, format, namespace, name, tenantID)
	}
	return o.AuthorizePublish(ctx, format, RepoKey(namespace, name), tenantID)
}

// CanRead resolves the most specific read check available.
func CanRead(ctx context.Context, o Ownership, format, namespace, name string, tenantID int64) bool {
	if o == nil {
		return true
	}
	if ns, ok := o.(NamespacedOwnership); ok {
		return ns.CanReadNS(ctx, format, namespace, name, tenantID)
	}
	return o.CanRead(ctx, format, RepoKey(namespace, name), tenantID)
}

// Upstream describes a remote base URL for a protocol (or a sub-endpoint).
type Upstream struct {
	Base    string // "" means the format's configured/default upstream
	Proxy   string // "" = direct, otherwise a proxy URL
	Default *string
}

// Upstreams resolves the effective remote for a format / sub-endpoint against
// per-key overrides and per-key proxy policy, honoring an air-gap flag.
//
// Targets is the target registry (protocol -> upstream identity). When set it
// is the single source of truth for hostnames (the SSRF allow-list) and for
// host-driven resolution; Defaults/Overrides/Repos remain as per-deployment
// policy layered on top. A nil Targets keeps the legacy table-only behavior.
type Upstreams struct {
	// Targets is the target registry. Nil disables target-driven resolution.
	Targets *targets.Registry
	// Defaults maps a format to its built-in public upstream base.
	Defaults map[string]string
	// Overrides overrides Defaults; empty value reverts to the default.
	Overrides map[string]string
	// Proxy is per-key proxy URL. "" means direct; absence means env proxy.
	Proxy map[string]string
	// Repos overrides the upstream per (format, repository/namespace). Keys are
	// "format/repo" (e.g. "npm/@acme", "maven/org.apache", "go/github.com/acme")
	// and match the LONGEST repository prefix, so one entry covers a subtree.
	Repos map[string]RepoUpstream
	// AllowedHosts are extra upstream hosts that may be reached through the
	// X-Forwarded-Host path even though they are not in Defaults (container
	// registries a client addresses by name: ghcr.io, quay.io, ...). Empty
	// values ("host" or "scheme://host") default to https.
	AllowedHosts []string
	// AirGap, when true, returns no upstreams at all (local-only registry).
	AirGap bool
}

// RepoUpstream is the per-repository upstream policy.
type RepoUpstream struct {
	Base  string `json:"base"`
	Proxy string `json:"proxy,omitempty"`
}

// Repo returns the effective upstream for one format's repository, using the
// longest matching repository prefix, then the format default. "" means
// disabled (air-gap or no upstream).
func (u *Upstreams) Repo(format, repo string) string {
	if u.AirGap {
		return ""
	}
	if e, ok := u.repoEntry(format, repo); ok && e.Base != "" {
		return trimSlash(e.Base)
	}
	return u.Get(format)
}

// RepoProxy returns the proxy policy for a repository: the repository entry's
// own proxy when it sets one, else the format's proxy policy.
func (u *Upstreams) RepoProxy(format, repo string) (string, bool) {
	if e, ok := u.repoEntry(format, repo); ok && e.Proxy != "" {
		return e.Proxy, true
	}
	return u.ProxyURL(format)
}

// RepoOverride returns the explicit repository override for (format, repo)
// (longest prefix), without falling back to the format default. Callers that
// already have their own default (OCI derives https://<host>) use this to let
// -repo-upstreams win.
func (u *Upstreams) RepoOverride(format, repo string) (RepoUpstream, bool) {
	if u == nil || u.AirGap {
		return RepoUpstream{}, false
	}
	e, ok := u.repoEntry(format, repo)
	if !ok || e.Base == "" {
		return RepoUpstream{}, false
	}
	return e, true
}

// repoEntry finds the longest repository prefix for (format, repo).
func (u *Upstreams) repoEntry(format, repo string) (RepoUpstream, bool) {
	prefix := format + "/"
	bestLen := -1
	var best RepoUpstream
	for k, e := range u.Repos {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		sub := k[len(prefix):]
		if sub == "" || !repoPrefixMatches(sub, repo) {
			continue
		}
		if len(sub) > bestLen {
			bestLen, best = len(sub), e
		}
	}
	return best, bestLen >= 0
}

// repoPrefixMatches reports whether key is a prefix of repo at a namespace
// boundary (".", "/", "@" or end of string), so "org.apache" matches
// "org.apache.commons" and "@acme" matches "@acme" but not "@acmeevil".
func repoPrefixMatches(key, repo string) bool {
	if !strings.HasPrefix(repo, key) {
		return false
	}
	if len(repo) == len(key) {
		return true
	}
	switch repo[len(key)] {
	case '.', '/', '@':
		return true
	}
	return false
}

// SetRepo overrides the upstream (and optionally the proxy) for a repository.
func (u *Upstreams) SetRepo(format, repo, base, proxy string) {
	if format == "" {
		return
	}
	if u.Repos == nil {
		u.Repos = map[string]RepoUpstream{}
	}
	key := format
	if repo != "" {
		key = format + "/" + strings.Trim(repo, "/")
	}
	u.Repos[key] = RepoUpstream{Base: base, Proxy: proxy}
}

// ResetRepo removes a repository override (exact key).
func (u *Upstreams) ResetRepo(format, repo string) {
	key := format
	if repo != "" {
		key = format + "/" + strings.Trim(repo, "/")
	}
	delete(u.Repos, key)
}

// RepoStates lists every repository override (sorted by key).
func (u *Upstreams) RepoStates() []RepoState {
	out := make([]RepoState, 0, len(u.Repos))
	for k, e := range u.Repos {
		format, repo := k, ""
		if i := strings.IndexByte(k, '/'); i >= 0 {
			format, repo = k[:i], k[i+1:]
		}
		out = append(out, RepoState{Format: format, Repo: repo, Base: e.Base, Proxy: e.Proxy})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Format+"/"+out[i].Repo < out[j].Format+"/"+out[j].Repo
	})
	return out
}

// RepoState is one repository override in RepoStates().
type RepoState struct {
	Format string `json:"format"`
	Repo   string `json:"repo"`
	Base   string `json:"base"`
	Proxy  string `json:"proxy,omitempty"`
}

// Get returns the effective base URL for a format, or "" when disabled.
func (u *Upstreams) Get(format string) string {
	if u.AirGap {
		return ""
	}
	if v, ok := u.Overrides[format]; ok {
		if v != "" {
			return trimSlash(v)
		}
	}
	if v := u.Defaults[format]; v != "" {
		return trimSlash(v)
	}
	// Target registry: the protocol's default target supplies the base.
	if u.Targets != nil {
		if t, ok := u.Targets.Default(format); ok && t.Base != "" {
			return trimSlash(t.Base)
		}
	}
	return ""
}

// Sub returns the effective base URL for a dotted sub-endpoint.
func (u *Upstreams) Sub(format, sub string) string {
	key := format + "." + sub
	if u.AirGap {
		return ""
	}
	if v, ok := u.Overrides[key]; ok {
		if v != "" {
			return trimSlash(v)
		}
	}
	if v := u.Defaults[key]; v != "" {
		return trimSlash(v)
	}
	// A target may carry the sub-endpoint as an Aux entry ("cargo.static").
	if u.Targets != nil {
		if t, ok := u.Targets.Get(key); ok {
			return trimSlash(t.Base)
		}
		if t, ok := u.Targets.Default(format); ok {
			if v := t.Aux[sub]; v != "" {
				return trimSlash(v)
			}
		}
	}
	return ""
}

// TargetFor returns the target a (format, host) request resolves to, when a
// target registry is configured. It is the host-driven half of the upstream
// priority: the caller layers per-repo overrides on top.
func (u *Upstreams) TargetFor(format, host string) (targets.Target, bool) {
	if u == nil || u.Targets == nil || u.AirGap || host == "" {
		return targets.Target{}, false
	}
	return u.Targets.ForHostProtocol(format, host)
}

// targetFromScope returns the NAMED target a request was explicitly mounted at
// (/artifacts/<target-id>/...). The default target (ID == protocol) is
// deliberately excluded: its base is the table default, and the client's Host
// must still be able to select a mirror (a request to index.crates.io goes to
// index.crates.io, not to cargo's default crates.io).
func (u *Upstreams) targetFromScope(ctx context.Context) (targets.Target, bool) {
	if u == nil || u.Targets == nil || u.AirGap {
		return targets.Target{}, false
	}
	sc := RepoScopeFrom(ctx)
	if sc.Target == "" {
		return targets.Target{}, false
	}
	// A dotted ID is a named target; the bare protocol name is the default
	// target, whose base is the table default and must NOT outrank the
	// client's host (a request to index.crates.io goes to index.crates.io).
	if _, name := targets.SplitID(sc.Target); name == "" {
		return targets.Target{}, false
	}
	return u.Targets.Get(sc.Target)
}

// TargetsFor returns every target serving a protocol (empty when no registry).
func (u *Upstreams) TargetsFor(format string) []targets.Target {
	if u == nil || u.Targets == nil {
		return nil
	}
	return u.Targets.ForProtocol(format)
}

// ProxyURL returns the proxy policy for a key, walking dotted parents, then
// falling back to the "*" catch-all entry when present (applied to every
// upstream). "" means direct; absence means the env proxy.
func (u *Upstreams) ProxyURL(key string) (string, bool) {
	for {
		if v, ok := u.Proxy[key]; ok {
			return v, true
		}
		for i := len(key) - 1; i >= 0; i-- {
			if key[i] == '.' {
				key = key[:i]
				goto next
			}
		}
		break
	next:
	}
	if v, ok := u.Proxy["*"]; ok {
		return v, true
	}
	return "", false
}

// ProxyFactory returns a client factory honored by the Upstreams' proxy
// policy. Adapters use it to build Remote handles.
func (u *Upstreams) ProxyFactory() *ClientFactory { return clientFactoryFor(u) }

// All returns the effective upstream for every known format + sub-endpoint.
func (u *Upstreams) All() []UpstreamEntry {
	var out []UpstreamEntry
	seen := map[string]bool{}
	for f := range u.Defaults {
		if v := u.Get(f); v != "" && !seen[f] {
			out = append(out, UpstreamEntry{Name: f, URL: v})
			seen[f] = true
		}
	}
	// Sub-endpoints whose key starts with "format.".
	for k := range u.Defaults {
		seen[k] = true
	}
	for k := range u.Overrides {
		if i := indexDot(k); i > 0 {
			sub := u.Sub(k[:i], k[i+1:])
			if sub != "" {
				out = append(out, UpstreamEntry{Name: k, URL: sub})
			}
		}
	}
	// Repository-level overrides ("format/repo") are surfaced verbatim so the
	// admin API can list them alongside formats.
	for k, e := range u.Repos {
		out = append(out, UpstreamEntry{Name: k, URL: e.Base})
	}
	return out
}

// IsOverride reports whether a key has an explicit (non-default) override.
func (u *Upstreams) IsOverride(key string) bool { return u.Overrides[key] != "" }

// Set overrides a format/sub-endpoint URL and persists it in the in-memory map.
func (u *Upstreams) Set(key, url string) {
	if key == "" {
		return
	}
	u.Overrides[key] = url
}

// Reset reverts a key override to its default.
func (u *Upstreams) Reset(key string) {
	delete(u.Overrides, key)
}

// SetProxy stores a per-key proxy URL ("" = direct).
func (u *Upstreams) SetProxy(key, value string) {
	if key == "" {
		return
	}
	u.Proxy[key] = value
}

// ProxyStates returns the proxy policy for every known key.
func (u *Upstreams) ProxyStates() []ProxyState {
	var out []ProxyState
	for _, e := range u.All() {
		p, ok := u.ProxyURL(e.Name)
		out = append(out, ProxyState{Key: e.Name, Proxy: p, Explicit: ok})
	}
	return out
}

// UpstreamEntry is one effective mapping in All().
type UpstreamEntry struct {
	Name string `json:"key"`
	URL  string `json:"url"`
}

// ProxyState is the per-key proxy policy in ProxyStates().
type ProxyState struct {
	Key      string `json:"key"`
	Proxy    string `json:"proxy"`
	Explicit bool   `json:"explicit"`
}

func indexDot(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			return i
		}
	}
	return -1
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// Fetched is the result of a pull-through fetch: full upstream bytes (cached
// or fresh) plus hashes.
type Fetched struct {
	Data   []byte
	Hashes Hashes
	Size   int64
	// Digest is the CAS digest of the fetched bytes ("" on a legacy path that
	// did not store them).
	Digest string
}

// Stored is the summary of bytes written into the CAS.
type Stored struct {
	Hashes Hashes
	Size   int64
	Digest string
}

// RegistryApi is a convenience trait object so adapters can hold either the
// concrete Registry or a narrowed view, and accept a blob sink other than the
// default.
type RegistryApi interface {
	// Fetch pulls `path` from an upstream, stores it (dedup by sha256) and
	// returns bytes + hashes. An empty upstreamBase means "use the format's
	// default"; non-empty bases are used verbatim.
	Fetch(ctx context.Context, format, upstreamBase, path string) (Fetched, error)
	// FetchFor is Fetch for one repository of a format, honoring the
	// repository's upstream override and proxy policy.
	FetchFor(ctx context.Context, format, repo, upstreamBase, path string) (Fetched, error)
	// FetchAbsolute pulls a full URL verbatim.
	FetchAbsolute(ctx context.Context, url string) (Fetched, error)
	// StoreAndHash writes bytes into the blob CAS and returns their summary.
	StoreAndHash(ctx context.Context, data []byte) (Stored, error)
}
