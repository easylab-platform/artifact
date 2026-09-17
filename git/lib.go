// Package git implements Git's smart-HTTP protocol as a caching pull-through
// mirror with a **local bare mirror** per repository. The first clone populates
// the mirror from the upstream; every later clone/fetch is served from local
// objects, so a repeated clone is fast and works even when the upstream is
// unreachable.
//
// Repository layout served here:
//
//	/pkgs/git/<host>/<path>            e.g. /pkgs/git/github.com/julialang/general
//
// The host is the first path segment (github.com, gitlab.com, ...), so one
// instance mirrors any number of git servers and the host is the repository
// namespace (isolation + per-host upstream overrides).
//
// Implementation note: the mirror uses the host's git CLI (`git clone --mirror`
// on first use, `git fetch --prune` afterwards) and serves the smart-HTTP
// protocol with `git upload-pack --stateless-rpc` / `git receive-pack
// --stateless-rpc` (the same pair CGI servers use, i.e. what `git http-backend`
// drives). That keeps pack negotiation correct without reimplementing the
// protocol, and pushes land in the local mirror.
package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/easylab-platform/artifact/core"
)

type State struct {
	Registry *artifactkit.Registry
	Auth     artifactkit.Auth
	// Dir roots the bare mirrors (usually <data>/git). Empty uses
	// <TempDir>/artifact-git.
	Dir string
	// SelfBase is the external base URL the client used, for emitted URLs.
	SelfBase string
	// Timeout bounds one upstream git command (clone/fetch). Empty = 10m.
	Timeout time.Duration
	// Refresh is how long a mirror may serve stale content before a background
	// fetch is kicked off. Empty = 1m. A request never waits for the refresh;
	// only the very first clone of a repository is synchronous.
	Refresh time.Duration
	// DisableLocal writes through to the upstream without keeping a mirror
	// (a pure proxy). Off by default.
	DisableLocal bool

	mu         sync.Mutex
	locks      map[string]*sync.Mutex
	refreshing map[string]bool
}

func NewHandler(reg *artifactkit.Registry, cfg map[string]any) (http.Handler, error) {
	s := &State{Registry: reg, locks: map[string]*sync.Mutex{}}
	if a, ok := cfg["auth"].(artifactkit.Auth); ok {
		s.Auth = a
	}
	if v, ok := cfg["dir"].(string); ok && v != "" {
		s.Dir = v
	}
	if v, ok := cfg["self_base"].(string); ok {
		s.SelfBase = strings.TrimSuffix(v, "/")
	}
	if v, ok := cfg["timeout"].(time.Duration); ok {
		s.Timeout = v
	}
	if v, ok := cfg["disable_local"].(bool); ok {
		s.DisableLocal = v
	}
	return s, nil
}

func init() { artifactkit.Register("git", NewHandler) }

// maxBody bounds a pack upload (a push of a large history can be big).
const maxBody = 2 << 30

func (s *State) dir() string {
	if s.Dir != "" {
		return s.Dir
	}
	return filepath.Join(os.TempDir(), "artifact-git")
}

func (s *State) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return 10 * time.Minute
}

// ServeHTTP dispatches the smart-HTTP surface of one upstream repository.
//
// The upstream host is taken from the request's origin (the egress proxy
// records it as X-Forwarded-Host, surfaced in the repo scope) when it names a
// real git server, and otherwise from an explicit leading path segment. That
// makes both shapes work:
//
//	git clone https://github.com/me/repo            (rewritten: host is origin)
//	git clone http://gateway/pkgs/git/github.com/me/repo   (host in the path)
func (s *State) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/pkgs/git/"), "/")
	path = strings.TrimPrefix(path, "git/")

	host := artifactkit.CanonicalHost(artifactkit.RepoScopeFrom(r.Context()).Host)
	if !artifactkit.IsPublicHostname(host) {
		host = ""
	}
	if host != "" {
		// The path is the repository (the origin carried the host).
		if path == "" {
			artifactkit.Error(w, http.StatusNotFound, "repository required")
			return
		}
		s.dispatch(w, r, host, path)
		return
	}
	host, repoPath, ok := artifactkit.SplitRepo(path)
	if !ok || host == "" || repoPath == "" {
		artifactkit.Error(w, http.StatusNotFound, "host and repository required")
		return
	}
	s.dispatch(w, r, artifactkit.CanonicalHost(host), repoPath)
}

// dispatch routes one repository's endpoints.
func (s *State) dispatch(w http.ResponseWriter, r *http.Request, host, repoPath string) {

	// Service endpoints (git-upload-pack / git-receive-pack and their
	// discovery) operate on the mirror; everything else (dumb objects) is
	// proxied so odd clients still work.
	switch {
	case strings.HasSuffix(repoPath, "/info/refs"):
		s.infoRefs(w, r, host, strings.TrimSuffix(repoPath, "/info/refs"))
	case strings.HasSuffix(repoPath, "/git-upload-pack"):
		s.service(w, r, host, strings.TrimSuffix(repoPath, "/git-upload-pack"), "upload-pack", false)
	case strings.HasSuffix(repoPath, "/git-receive-pack"):
		s.service(w, r, host, strings.TrimSuffix(repoPath, "/git-receive-pack"), "receive-pack", true)
	default:
		s.proxy(w, r, host, repoPath)
	}
}

// mirrorPath is the on-disk bare mirror for host/repo.
func (s *State) mirrorPath(host, repo string) string {
	sum := sha256.Sum256([]byte(host + "/" + repo))
	return filepath.Join(s.dir(), host, hex.EncodeToString(sum[:8])+".git")
}

// repoLock returns the per-repository mutex that serialises clone/fetch.
func (s *State) repoLock(key string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locks == nil {
		s.locks = map[string]*sync.Mutex{}
	}
	m, ok := s.locks[key]
	if !ok {
		m = &sync.Mutex{}
		s.locks[key] = m
	}
	return m
}

// ensureMirror makes the local mirror exist and reasonably fresh. It is a
// no-op when local storage is disabled.
//
// Freshness policy: a request NEVER blocks on the upstream when a mirror
// already exists — it is served from local objects immediately and a
// background refresh is kicked off (at most one at a time per repository, and
// only when the mirror is older than Refresh). The first request for a
// repository must clone synchronously because there is nothing to serve yet.
func (s *State) ensureMirror(ctx context.Context, host, repo string) (string, error) {
	if s.DisableLocal {
		return "", nil
	}
	dir := s.mirrorPath(host, repo)
	key := host + "/" + repo
	mu := s.repoLock(key)
	mu.Lock()
	defer mu.Unlock()

	if isGitDir(dir) {
		s.maybeRefresh(dir, host, repo)
		return dir, nil
	}

	tctx, cancel := context.WithTimeout(ctx, s.timeout())
	defer cancel()
	upstream, err := s.upstreamURL(ctx, host, repo)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(tctx, "git", "clone", "--mirror", upstream, dir)
	cmd.Env = gitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(dir)
		return "", &gitError{Op: "clone", Err: err, Out: string(out)}
	}
	_ = os.WriteFile(dir+"/.artifact-fetched", []byte(time.Now().UTC().Format(time.RFC3339)), 0o644)
	return dir, nil
}

// maybeRefresh refreshes a mirrored repository in the background when it is
// older than Refresh. The caller holds the repository lock; the refresh runs
// on its own goroutine with its own lock so it does not serialize requests.
func (s *State) maybeRefresh(dir, host, repo string) {
	interval := s.Refresh
	if interval <= 0 {
		interval = time.Minute
	}
	if b, err := os.ReadFile(dir + "/.artifact-fetched"); err == nil {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b))); err == nil {
			if time.Since(t) < interval {
				return
			}
		}
	}
	if !s.beginRefresh(host + "/" + repo) {
		return
	}
	go func() {
		defer s.endRefresh(host + "/" + repo)
		mu := s.repoLock(host + "/" + repo)
		mu.Lock()
		defer mu.Unlock()
		upstream, err := s.upstreamURL(context.Background(), host, repo)
		if err != nil {
			return
		}
		tctx, cancel := context.WithTimeout(context.Background(), s.timeout())
		defer cancel()
		cmd := exec.CommandContext(tctx, "git", "--git-dir", dir, "remote", "set-url", "origin", upstream)
		cmd.Env = gitEnv()
		if err := cmd.Run(); err != nil {
			return
		}
		cmd = exec.CommandContext(tctx, "git", "--git-dir", dir, "fetch", "--prune", "origin",
			"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")
		cmd.Env = gitEnv()
		if err := cmd.Run(); err == nil {
			_ = os.WriteFile(dir+"/.artifact-fetched", []byte(time.Now().UTC().Format(time.RFC3339)), 0o644)
		}
	}()
}

// beginRefresh marks a repository as refreshing; false means one is running.
func (s *State) beginRefresh(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refreshing == nil {
		s.refreshing = map[string]bool{}
	}
	if s.refreshing[key] {
		return false
	}
	s.refreshing[key] = true
	return true
}

func (s *State) endRefresh(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.refreshing, key)
}

// upstreamURL is where the bare mirror is cloned/fetched from.
func (s *State) upstreamURL(ctx context.Context, host, repo string) (string, error) {
	override, _ := s.Registry.Upstreams.RepoOverride("git", host+"/"+repo)
	if override.Base != "" {
		return override.Base, nil
	}
	// Any host explicitly allowed (or whose scheme is recorded in the table)
	// may be mirrored; the host alone identifies the git server, so a dotted
	// hostname is accepted as https. Container registries in the table are not
	// git servers, but a git override for them would take the branch above.
	if _, ok := s.Registry.Upstreams.AllowedSource(host); ok {
		return "https://" + host + "/" + repo, nil
	}
	return "", &gitError{Op: "upstream", Err: errUnknownHost{host}}
}

// infoRefs serves the ref advertisement for upload-pack (fetch) or
// receive-pack (push). Fetch reads it from the local mirror when one exists.
func (s *State) infoRefs(w http.ResponseWriter, r *http.Request, host, repo string) {
	service := r.URL.Query().Get("service")
	if service != "git-upload-pack" && service != "git-receive-pack" {
		s.proxy(w, r, host, repo+"/info/refs?"+r.URL.RawQuery)
		return
	}
	if service == "git-receive-pack" && !artifactkit.AuthorizeWriteFor(w, r, s.Auth, s.Registry, "git", repo) {
		return
	}
	dir, err := s.ensureMirror(r.Context(), host, repo)
	if err != nil {
		artifactkit.Error(w, http.StatusBadGateway, "upstream: "+err.Error())
		return
	}
	if dir == "" { // local storage disabled: proxy the discovery
		s.proxy(w, r, host, repo+"/info/refs?"+r.URL.RawQuery)
		return
	}
	// Allow an annotated serving of the advertisement via the git CLI.
	w.Header().Set("Content-Type", "application/x-"+service+"-advertisement")
	w.Header().Set("Cache-Control", "no-cache")
	writePktLine(w, "# service="+service+"\n")
	_, _ = io.WriteString(w, "0000") // flush, then the advertisement
	s.runService(w, r, dir, service, true)
}

// service handles the POST body of upload-pack / receive-pack against the
// local mirror (stateless RPC).
func (s *State) service(w http.ResponseWriter, r *http.Request, host, repo, service string, write bool) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if write && !artifactkit.AuthorizeWriteFor(w, r, s.Auth, s.Registry, "git", repo) {
		return
	}
	dir, err := s.ensureMirror(r.Context(), host, repo)
	if err != nil {
		artifactkit.Error(w, http.StatusBadGateway, "upstream: "+err.Error())
		return
	}
	if dir == "" {
		s.proxy(w, r, host, repo+"/"+service)
		return
	}
	artifactkit.LimitBody(w, r)
	w.Header().Set("Content-Type", "application/x-git-"+service+"-result")
	w.Header().Set("Cache-Control", "no-cache")
	s.runService(w, r, dir, service, false)
}

// runService executes `git <service> --stateless-rpc` against the mirror,
// streaming the request body in and the result out. advertise adds the
// --advertise-refs flag (discovery).
func (s *State) runService(w http.ResponseWriter, r *http.Request, dir, service string, advertise bool) {
	// service is "git-upload-pack"/"git-receive-pack"; the CLI subcommand is
	// the bare "upload-pack"/"receive-pack".
	sub := strings.TrimPrefix(service, "git-")
	args := []string{sub, "--stateless-rpc"}
	if advertise {
		args = append(args, "--advertise-refs")
	}
	args = append(args, dir)
	cmd := exec.CommandContext(r.Context(), "git", args...)
	cmd.Env = gitEnv()
	if !advertise {
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(io.LimitReader(r.Body, maxBody))
		}
		cmd.Stdin = bytes.NewReader(body)
	}
	cmd.Stdout = w
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// The head is already written; append the error for diagnostics only.
		_, _ = io.WriteString(w, "\n# git "+service+" error: "+err.Error()+" "+stderr.String())
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

// writePktLine writes one git pkt-line ("<4-hex-len><payload>").
func writePktLine(w io.Writer, payload string) {
	_, _ = io.WriteString(w, formatPktLen(len(payload)+4)+payload)
}

// proxy streams a non-smart request (dumb objects, HEAD, LFS batch) upstream.
func (s *State) proxy(w http.ResponseWriter, r *http.Request, host, path string) {
	var body []byte
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		artifactkit.LimitBody(w, r)
		body, _ = io.ReadAll(io.LimitReader(r.Body, maxBody))
	}
	base, ok := s.Registry.Upstreams.HostBase("", host, "")
	if !ok {
		base = "https://" + host
	}
	remote := s.Registry.RemoteAt(base)
	resp, err := remote.Do(r.Context(), r.Method, "/"+strings.TrimPrefix(path, "/"), bytes.NewReader(body), r.Header.Get("Content-Type"), r.Header.Get("Accept"))
	if err != nil {
		artifactkit.Error(w, http.StatusBadGateway, "upstream: "+err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// gitError carries the command output so the gateway surfaces why a mirror
// could not be built.
type gitError struct {
	Op  string
	Err error
	Out string
}

func (e *gitError) Error() string {
	out := strings.TrimSpace(e.Out)
	if len(out) > 400 {
		out = out[:400] + "..."
	}
	if out == "" {
		return "git " + e.Op + ": " + e.Err.Error()
	}
	return "git " + e.Op + ": " + e.Err.Error() + ": " + out
}

type errUnknownHost struct{ host string }

func (e errUnknownHost) Error() string { return "unknown git host: " + e.host }

// isGitDir reports whether p looks like a bare git repository.
func isGitDir(p string) bool {
	for _, f := range []string{"HEAD", "objects", "refs"} {
		if _, err := os.Stat(filepath.Join(p, f)); err != nil {
			return false
		}
	}
	return true
}

// gitEnv is a non-interactive, credentials-free environment for the git CLI.
// The upstream proxy (mihomo) comes from the inherited HTTP(S)_PROXY.
func gitEnv() []string {
	env := os.Environ()
	env = append(env,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/true",
		"GCM_INTERACTIVE=never",
	)
	return env
}

// formatPktLen renders a git pkt-line length prefix (4 lowercase hex digits).
func formatPktLen(n int) string {
	const hexd = "0123456789abcdef"
	return string([]byte{hexd[(n>>12)&0xf], hexd[(n>>8)&0xf], hexd[(n>>4)&0xf], hexd[n&0xf]})
}
