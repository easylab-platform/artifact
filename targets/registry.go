package targets

import (
	"context"
	"sort"
	"strings"
)

// Store persists user-declared targets. The artifact server implements it over
// the metadata DB; embedders may leave it nil for built-ins only.
type Store interface {
	// ListTargets returns every persisted user target.
	ListTargets(ctx context.Context) ([]Target, error)
	// PutTarget upserts one user target.
	PutTarget(ctx context.Context, t Target) error
	// DeleteTarget removes one user target.
	DeleteTarget(ctx context.Context, id string) error
}

// Load populates user targets from a store. Built-ins are never overwritten by
// an empty store; a store error leaves the registry at its built-ins (the
// caller decides whether that is fatal).
func (r *Registry) Load(ctx context.Context, st Store) error {
	if st == nil {
		return nil
	}
	ts, err := st.ListTargets(ctx)
	if err != nil {
		return err
	}
	for _, t := range ts {
		r.Put(t)
	}
	return nil
}

// Registry is a target table: built-ins overlaid with operator/user targets.
// It is the runtime view the resolver consults. A zero Registry with only
// built-ins is what the artifact server and easysidecar start from.
type Registry struct {
	builtins map[string]Target
	user     map[string]Target
	// order preserves declaration order for stable listing.
	builtinOrder []string
	userOrder    []string
}

// NewRegistry returns a Registry preloaded with the built-in targets.
func NewRegistry() *Registry {
	r := &Registry{builtins: map[string]Target{}, user: map[string]Target{}}
	for _, t := range Builtins() {
		r.builtins[t.ID] = t
		r.builtinOrder = append(r.builtinOrder, t.ID)
	}
	return r
}

// Get returns a target by ID (user overrides built-in).
func (r *Registry) Get(id string) (Target, bool) {
	if r == nil {
		return Target{}, false
	}
	if t, ok := r.user[id]; ok {
		return t, true
	}
	t, ok := r.builtins[id]
	return t, ok
}

// Put installs a user target (overriding a built-in of the same ID).
// A user-declared target defaults to ISOLATED storage: a private repository
// re-publishing coordinates that also exist publicly must not shadow the
// public cache. Set Share=true to opt into the protocol's shared namespace.
func (r *Registry) Put(t Target) {
	if r.user == nil {
		r.user = map[string]Target{}
	}
	if _, seen := r.user[t.ID]; !seen {
		r.userOrder = append(r.userOrder, t.ID)
	}
	if t.Share == nil {
		share := false
		t.Share = &share
	}
	if t.Kind == "" {
		t.Kind = Mirror
	}
	t.Builtin = false
	r.user[t.ID] = t
}

// Delete removes a user target. Built-ins cannot be removed; deleting one
// leaves the built-in in place.
func (r *Registry) Delete(id string) {
	if r.user == nil {
		return
	}
	if _, ok := r.user[id]; !ok {
		return
	}
	delete(r.user, id)
	for i, v := range r.userOrder {
		if v == id {
			r.userOrder = append(r.userOrder[:i], r.userOrder[i+1:]...)
			break
		}
	}
}

// List returns every target: user targets first (newest last), then built-ins
// that are not shadowed, each group in declaration order.
func (r *Registry) List() []Target {
	if r == nil {
		return nil
	}
	out := make([]Target, 0, len(r.builtins)+len(r.user))
	for _, id := range r.userOrder {
		out = append(out, r.user[id])
	}
	for _, id := range r.builtinOrder {
		if _, shadowed := r.user[id]; shadowed {
			continue
		}
		out = append(out, r.builtins[id])
	}
	return out
}

// ForProtocol returns every target serving a protocol, default first.
func (r *Registry) ForProtocol(protocol string) []Target {
	var out []Target
	for _, t := range r.List() {
		if t.Protocol == protocol {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		// The default target (ID == protocol) sorts first.
		return out[i].ID == protocol && out[j].ID != protocol
	})
	return out
}

// Default returns the protocol's default target (the one whose ID is the
// protocol name).
func (r *Registry) Default(protocol string) (Target, bool) { return r.Get(protocol) }

// LegacyDefaults projects the registry onto the historical flat upstream map:
// each protocol's default target under its protocol key, each named mirror
// under its dotted ID, and each Aux entry as "protocol.sub". It exists so
// deployments that still read the flat table keep working while the registry
// is the single source of truth.
func (r *Registry) LegacyDefaults() map[string]string {
	out := map[string]string{}
	for _, t := range r.List() {
		// A protocol's default target is keyed by the protocol; a named
		// mirror by its full dotted ID.
		out[t.ID] = t.Base
		for name, base := range t.Aux {
			out[t.ID+"."+name] = base
		}
	}
	return out
}

// ByHost returns every target whose Hosts match host, in list order. A target
// with no explicit Hosts is reachable at its Base's host. Several targets may
// match (github.com is both a git and a git-lfs upstream), which is why a host
// is never a complete routing key on its own.
func (r *Registry) ByHost(host string) []Target {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	var out []Target
	for _, t := range r.List() {
		for _, pat := range targetHosts(t) {
			if matchHost(pat, h) {
				out = append(out, t)
				break
			}
		}
	}
	return out
}

// targetHosts returns the host patterns a target is addressed by: its explicit
// Hosts, or the host of its Base when none are declared.
func targetHosts(t Target) []string {
	if len(t.Hosts) > 0 {
		return t.Hosts
	}
	if h := baseHostOf(t.Base); h != "" {
		return []string{h}
	}
	return nil
}

// baseHostOf extracts the host from a base URL ("" when none).
func baseHostOf(raw string) string {
	if raw == "" {
		return ""
	}
	rest := raw
	if i := strings.Index(raw, "://"); i >= 0 {
		rest = raw[i+3:]
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.LastIndex(rest, "@"); i >= 0 {
		rest = rest[i+1:]
	}
	return strings.ToLower(rest)
}

// ForHostProtocol returns the target for (protocol, host): the default when
// no host matches, so a request to a protocol's own upstream is always
// resolvable.
func (r *Registry) ForHostProtocol(protocol, host string) (Target, bool) {
	for _, t := range r.ByHost(host) {
		if t.Protocol == protocol {
			return t, true
		}
	}
	return r.Default(protocol)
}

// KnownHosts returns every host pattern the table knows, mapped to its
// scheme. It feeds the SSRF allow-list: a client may only be steered to a
// host this table declares.
func (r *Registry) KnownHosts() map[string]string {
	out := map[string]string{}
	add := func(raw string) {
		if raw == "" {
			return
		}
		scheme, host := splitHost(raw)
		if host == "" {
			return
		}
		if _, ok := out[host]; !ok {
			out[host] = scheme
		}
	}
	for _, t := range r.List() {
		add(t.Base)
		add(t.Outbound)
		for _, h := range t.Hosts {
			if isPattern(h) {
				add(h)
			} else {
				// A bare host in Hosts has no scheme; default https.
				if _, ok := out[h]; !ok {
					out[h] = "https"
				}
			}
		}
		for _, aux := range t.Aux {
			add(aux)
		}
	}
	return out
}

// SchemeFor returns the recorded scheme for a host, defaulting to https.
func (r *Registry) SchemeFor(host string) string {
	h := strings.ToLower(host)
	for pat, scheme := range r.KnownHosts() {
		if matchHost(pat, h) {
			return scheme
		}
	}
	return "https"
}

// splitHost extracts (scheme, host) from a base URL.
func splitHost(raw string) (scheme, host string) {
	scheme = "https"
	rest := raw
	if i := strings.Index(raw, "://"); i >= 0 {
		scheme, rest = raw[:i], raw[i+3:]
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.LastIndex(rest, "@"); i >= 0 {
		rest = rest[i+1:]
	}
	return scheme, strings.ToLower(rest)
}

func isPattern(h string) bool {
	return strings.Contains(h, "*") || strings.Contains(h, "/") || strings.Contains(h, ":")
}

// matchHost lives in egress.go, shared by the registry and the egress table.
