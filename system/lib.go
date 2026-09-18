// Package system implements the cross-protocol admin API (/artifacts/system):
// upstream overrides, per-key proxy policy, and package enumeration/deletion.
// Mirror of the pkglab-core system router so an embedder can wire any
// substrate. Lives in its own module because it is inherently cross-protocol.
package system

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/easylab-platform/artifact/core"
	"github.com/easylab-platform/artifact/targets"
)

// State carries the registry + auth for the admin endpoints.
type State struct {
	Registry *artifactkit.Registry
	Auth     artifactkit.Auth
	// TargetsStore persists user-declared targets; nil keeps the instance at
	// its built-ins.
	TargetsStore targets.Store
}

// NewHandler is the artifactkit.Register constructor.
func NewHandler(reg *artifactkit.Registry, cfg map[string]any) (http.Handler, error) {
	s := &State{Registry: reg}
	if a, ok := cfg["auth"].(artifactkit.Auth); ok {
		s.Auth = a
	}
	if st, ok := cfg["targets_store"].(targets.Store); ok {
		s.TargetsStore = st
	}
	if s.TargetsStore == nil && reg != nil {
		s.TargetsStore = reg.TargetStore
	}
	return s, nil
}

func init() { artifactkit.Register("system", NewHandler) }

func (s *State) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := stringsTrimPrefix(r.URL.Path, "/artifacts/system")
	path = stringsTrimPrefix(path, "/system")

	switch {
	case path == "/targets" || path == "/targets/":
		s.targets(w, r)
	case len(path) > len("/targets/") && strings.HasPrefix(path, "/targets/"):
		s.targetKey(w, r, stringsTrimPrefix(path, "/targets/"))
	case path == "/upstreams" || path == "/upstreams/":
		s.listUpstreams(w, r)
	case len(path) > len("/upstreams/") && strings.HasPrefix(path, "/upstreams/"):
		key := stringsTrimPrefix(path, "/upstreams/")
		s.upstreamKey(w, r, key)
	case path == "/proxy" || path == "/proxy/":
		s.listProxy(w, r)
	case len(path) > len("/proxy/") && strings.HasPrefix(path, "/proxy/"):
		key := stringsTrimPrefix(path, "/proxy/")
		s.proxyKey(w, r, key)
	case path == "/packages" || path == "/packages/":
		s.packages(w, r)
	case path == "/repos" || path == "/repos/":
		s.repos(w, r)
	case len(path) > len("/repos/") && strings.HasPrefix(path, "/repos/"):
		s.repoKey(w, r, stringsTrimPrefix(path, "/repos/"))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *State) lookup(key string) (string, bool) {
	// Dotted sub-endpoint first, then bare format.
	if i := indexByte(key, '.'); i > 0 {
		sub := s.Registry.Upstreams.Sub(key[:i], key[i+1:])
		if sub != "" {
			return sub, true
		}
	}
	v := s.Registry.Upstreams.Get(key)
	return v, v != ""
}

// targets lists every configured target (built-ins + user-declared) when a
// registry is present, or the user targets the store holds otherwise.
func (s *State) targets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.Registry != nil && s.Registry.Upstreams != nil && s.Registry.Upstreams.Targets != nil {
		artifactkit.JSON(w, http.StatusOK, map[string]any{"targets": s.Registry.Upstreams.Targets.List()})
		return
	}
	if s.TargetsStore == nil {
		artifactkit.JSON(w, http.StatusOK, map[string]any{"targets": []targets.Target{}})
		return
	}
	ts, err := s.TargetsStore.ListTargets(r.Context())
	if err != nil {
		artifactkit.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	artifactkit.JSON(w, http.StatusOK, map[string]any{"targets": ts})
}

// targetKey gets, sets, or deletes one user-declared target by ID. Deleting a
// built-in ID is refused: built-ins are compile-time policy.
func (s *State) targetKey(w http.ResponseWriter, r *http.Request, id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		artifactkit.JSON(w, http.StatusBadRequest, map[string]any{"error": "missing target id"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		if s.Registry != nil && s.Registry.Upstreams != nil && s.Registry.Upstreams.Targets != nil {
			if t, ok := s.Registry.Upstreams.Targets.Get(id); ok {
				artifactkit.JSON(w, http.StatusOK, t)
				return
			}
		}
		artifactkit.JSON(w, http.StatusNotFound, map[string]any{"error": "unknown target: " + id})
	case http.MethodPut, http.MethodPost:
		if !artifactkit.AuthorizeWrite(w, r, s.Auth) {
			return
		}
		if s.TargetsStore == nil {
			artifactkit.JSON(w, http.StatusNotImplemented, map[string]any{"error": "target store not configured"})
			return
		}
		var t targets.Target
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			artifactkit.JSON(w, http.StatusBadRequest, map[string]any{"error": "bad target body"})
			return
		}
		if t.ID == "" {
			t.ID = id
		}
		if t.ID != id {
			artifactkit.JSON(w, http.StatusBadRequest, map[string]any{"error": "target id mismatch"})
			return
		}
		if t.Protocol == "" {
			t.Protocol, _ = targets.SplitID(t.ID)
		}
		if err := validateTarget(t); err != nil {
			artifactkit.JSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if err := s.TargetsStore.PutTarget(r.Context(), t); err != nil {
			artifactkit.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if s.Registry != nil && s.Registry.Upstreams != nil && s.Registry.Upstreams.Targets != nil {
			s.Registry.Upstreams.Targets.Put(t)
		}
		artifactkit.JSON(w, http.StatusOK, t)
	case http.MethodDelete:
		if !artifactkit.AuthorizeWrite(w, r, s.Auth) {
			return
		}
		if s.TargetsStore == nil {
			artifactkit.JSON(w, http.StatusNotImplemented, map[string]any{"error": "target store not configured"})
			return
		}
		if s.Registry != nil && s.Registry.Upstreams != nil && s.Registry.Upstreams.Targets != nil {
			if t, ok := s.Registry.Upstreams.Targets.Get(id); ok && t.Builtin {
				artifactkit.JSON(w, http.StatusForbidden, map[string]any{"error": "cannot delete a built-in target"})
				return
			}
		}
		if err := s.TargetsStore.DeleteTarget(r.Context(), id); err != nil {
			artifactkit.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if s.Registry != nil && s.Registry.Upstreams != nil && s.Registry.Upstreams.Targets != nil {
			s.Registry.Upstreams.Targets.Delete(id)
		}
		artifactkit.JSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// validateTarget rejects targets that could not be routed or fetched.
func validateTarget(t targets.Target) error {
	if t.ID == "" {
		return errText("missing id")
	}
	if t.Protocol == "" {
		return errText("missing protocol")
	}
	if t.Base == "" && len(t.Hosts) == 0 {
		return errText("a target needs a base URL or at least one host")
	}
	if !strings.Contains(t.ID, "/") && t.ID != "" && strings.ContainsAny(t.ID, " \t") {
		return errText("invalid id")
	}
	switch t.Auth.Mode {
	case "", targets.AuthPassthrough, targets.AuthBasic, targets.AuthBearer:
	default:
		return errText("unknown auth mode: " + string(t.Auth.Mode))
	}
	return nil
}

type errText string

func (e errText) Error() string { return string(e) }

func (s *State) listUpstreams(w http.ResponseWriter, r *http.Request) {
	all := map[string]string{}
	for _, k := range s.Registry.Upstreams.All() {
		all[k.Name] = k.URL
	}
	artifactkit.JSON(w, http.StatusOK, map[string]any{"upstreams": all})
}

func (s *State) upstreamKey(w http.ResponseWriter, r *http.Request, key string) {
	switch r.Method {
	case http.MethodGet:
		u, ok := s.lookup(key)
		if !ok {
			artifactkit.JSON(w, http.StatusNotFound, map[string]any{"error": "unknown upstream key: " + key})
			return
		}
		artifactkit.JSON(w, http.StatusOK, map[string]any{"key": key, "url": u, "override": s.Registry.Upstreams.IsOverride(key)})
	case http.MethodPut:
		if !artifactkit.AuthorizeWrite(w, r, s.Auth) {
			return
		}
		if _, ok := s.lookup(key); !ok {
			artifactkit.JSON(w, http.StatusNotFound, map[string]any{"error": "unknown upstream key: " + key})
			return
		}
		var body struct {
			URL string `json:"url"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.URL == "" {
			artifactkit.JSON(w, http.StatusBadRequest, map[string]any{"error": "missing url"})
			return
		}
		s.Registry.Upstreams.Set(key, body.URL)
		artifactkit.JSON(w, http.StatusOK, map[string]any{"key": key, "url": body.URL})
	case http.MethodDelete:
		if !artifactkit.AuthorizeWrite(w, r, s.Auth) {
			return
		}
		s.Registry.Upstreams.Reset(key)
		artifactkit.JSON(w, http.StatusOK, map[string]any{"key": key, "reset": true})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *State) listProxy(w http.ResponseWriter, r *http.Request) {
	artifactkit.JSON(w, http.StatusOK, map[string]any{"proxy": s.Registry.Upstreams.ProxyStates()})
}

func (s *State) proxyKey(w http.ResponseWriter, r *http.Request, key string) {
	switch r.Method {
	case http.MethodGet:
		if _, ok := s.lookup(key); !ok {
			artifactkit.JSON(w, http.StatusNotFound, map[string]any{"error": "unknown upstream key: " + key})
			return
		}
		p, _ := s.Registry.Upstreams.ProxyURL(key)
		artifactkit.JSON(w, http.StatusOK, map[string]any{"key": key, "proxy": p})
	case http.MethodPut:
		if !artifactkit.AuthorizeWrite(w, r, s.Auth) {
			return
		}
		if _, ok := s.lookup(key); !ok {
			artifactkit.JSON(w, http.StatusNotFound, map[string]any{"error": "unknown upstream key: " + key})
			return
		}
		var body struct {
			Proxy string `json:"proxy"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.Registry.Upstreams.SetProxy(key, body.Proxy)
		p, _ := s.Registry.Upstreams.ProxyURL(key)
		artifactkit.JSON(w, http.StatusOK, map[string]any{"key": key, "proxy": p})
	case http.MethodDelete:
		if !artifactkit.AuthorizeWrite(w, r, s.Auth) {
			return
		}
		s.Registry.Upstreams.SetProxy(key, "")
		artifactkit.JSON(w, http.StatusOK, map[string]any{"key": key, "reset": true})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *State) packages(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		if !artifactkit.AuthorizeWrite(w, r, s.Auth) {
			return
		}
		repo := r.URL.Query().Get("repo")
		if repo == "" {
			artifactkit.JSON(w, http.StatusBadRequest, map[string]any{"error": "missing repo"})
			return
		}
		n, err := s.Registry.Meta.DeleteRepo(r.Context(), "", repo)
		if err != nil {
			artifactkit.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		artifactkit.JSON(w, http.StatusOK, map[string]any{"repo": repo, "deleted": n})
		return
	}
	repo := r.URL.Query().Get("repo")
	pkgs, err := s.Registry.Meta.ListPackages(r.Context())
	if err != nil {
		artifactkit.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if repo != "" {
		var filtered []artifactkit.PackageSummary
		for _, p := range pkgs {
			if p.Repository == repo {
				filtered = append(filtered, p)
			}
		}
		pkgs = filtered
	}
	artifactkit.JSON(w, http.StatusOK, map[string]any{"packages": pkgs})
}

func stringsTrimPrefix(s, p string) string {
	if len(s) >= len(p) && s[:len(p)] == p {
		return s[len(p):]
	}
	return s
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// repos lists every repository override (format/repo -> upstream + proxy) and
// every format's effective default, so an operator can see how a request for
// a given namespace will be routed.
func (s *State) repos(w http.ResponseWriter, r *http.Request) {
	artifactkit.JSON(w, http.StatusOK, map[string]any{
		"repos":   s.Registry.Upstreams.RepoStates(),
		"formats": s.Registry.Upstreams.All(),
	})
}

// repoKey gets or sets one repository override. The key is "format/repo"
// (repo may be a namespace prefix: maven/org.apache, npm/@acme).
func (s *State) repoKey(w http.ResponseWriter, r *http.Request, key string) {
	format, repo, ok := stringsCut(key, "/")
	if !ok || format == "" || repo == "" {
		artifactkit.JSON(w, http.StatusBadRequest, map[string]any{"error": "key must be format/repo"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		if e, ok := s.Registry.Upstreams.RepoOverride(format, repo); ok {
			artifactkit.JSON(w, http.StatusOK, map[string]any{"format": format, "repo": repo, "base": e.Base, "proxy": e.Proxy})
			return
		}
		artifactkit.JSON(w, http.StatusNotFound, map[string]any{"error": "no override for " + key})
	case http.MethodPut:
		if !artifactkit.AuthorizeWrite(w, r, s.Auth) {
			return
		}
		var body struct {
			Base  string `json:"base"`
			Proxy string `json:"proxy"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Base == "" {
			artifactkit.JSON(w, http.StatusBadRequest, map[string]any{"error": "missing base"})
			return
		}
		s.Registry.Upstreams.SetRepo(format, repo, body.Base, body.Proxy)
		artifactkit.JSON(w, http.StatusOK, map[string]any{"format": format, "repo": repo, "base": body.Base})
	case http.MethodDelete:
		if !artifactkit.AuthorizeWrite(w, r, s.Auth) {
			return
		}
		s.Registry.Upstreams.ResetRepo(format, repo)
		artifactkit.JSON(w, http.StatusOK, map[string]any{"format": format, "repo": repo, "reset": true})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func stringsCut(s, sep string) (string, string, bool) {
	if i := indexByte(s, sep[0]); i >= 0 {
		return s[:i], s[i+len(sep):], true
	}
	return s, "", false
}
