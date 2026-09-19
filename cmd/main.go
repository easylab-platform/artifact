// Command artifact runs a multi-protocol package registry. Each protocol is a
// separate module that registers itself with artifactkit at compile time (see the
// blank imports in adapters). The server can mount a single protocol or many,
// so a pull-through mirror may be started per-protocol or all-at-once.
//
// Example:
//
//	artifact server --protocols=oci,pypi --listen :8080 --data ./data
//	artifact server --protocols=oci --oci.upstream https://registry-1.docker.io
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/easylab-platform/artifact/cmd/adapters" // registers all enabled protocols
	"github.com/easylab-platform/artifact/core"
	"github.com/easylab-platform/artifact/core/store"
	"github.com/easylab-platform/artifact/targets"
)

func main() {
	var (
		listen        = flag.String("listen", ":8080", "HTTP listen address")
		dataDir       = flag.String("data", "./data", "substrate root (sqlite + blobs + upstreams)")
		protocols     = flag.String("protocols", "", "comma-separated protocols to mount (default: all registered)")
		selfBase      = flag.String("self-base", "", "external base URL for auth realms / self URIs")
		selfBaseRaw   = flag.Bool("self-base-raw", false, "treat -self-base as the protocol's own upstream origin (do NOT append /artifacts/<name>): emitted URLs take the upstream shape so an intercepting client proxy can map them back")
		selfBaseMap   = flag.String("self-base-map", "", "per-protocol origin overrides, `proto=url` pairs comma separated; each entry implies origin (raw) mode for that protocol (e.g. npm=https://registry.npmjs.org,pypi=https://pypi.org)")
		tokens        = flag.String("tokens", "", "`token=level` pairs (read|write) to seed into the credential DB (hashed), comma separated; repeat on every start to keep them registered")
		airGap        = flag.Bool("air-gap", false, "disable all upstream pull-through")
		blobBackend   = flag.String("blob-backend", "filesystem", "blob backend: filesystem | s3")
		upstreamSet   = flag.String("upstreams", "", "override upstream base for a format, `format=url` pairs comma separated (e.g. go=http://proxy.golang.org)")
		repoUpstreams = flag.String("repo-upstreams", "", "per-repository upstream overrides, `format/repo=url` pairs comma separated; the longest matching repo prefix wins (e.g. maven/org.apache=https://mirror.example/m2,npm/@acme=https://npm.example)")
		upstreamProxy = flag.String("upstream-proxy", "", "HTTP proxy URL for upstream fetches (empty = follow env, \"none\" = direct)")
		gcInterval    = flag.Duration("gc-interval", time.Hour, "background reaper period (expired negative entries + orphan blobs); 0 disables")
		gcGrace       = flag.Duration("gc-grace", time.Hour, "only orphan blobs older than this are removed (protects in-flight writes)")
	)
	flag.Parse()

	upstreams := defaultUpstreams(*airGap)
	applyUpstreamOverrides(upstreams, *upstreamSet, *upstreamProxy)
	applyRepoUpstreams(upstreams, *repoUpstreams)
	// Open the metadata store (SQLite) always; blob backend is chosen below.
	idxPath := filepath.Join(*dataDir, "pkglab.db")
	meta, err := store.OpenSQLite(idxPath)
	if err != nil {
		log.Fatalf("open metadata: %v", err)
	}
	defer func() { _ = meta.Close() }()

	// Auth: credentials live in the metadata DB (SHA-256 hashed at rest).
	// --tokens seeds them (idempotent); once any user exists the instance is
	// closed and only registered credentials work. With no users at all the
	// instance is open (anonymous read/write, dev mode).
	var auth artifactkit.Auth
	if *tokens != "" {
		if err := seedTokens(meta, *tokens); err != nil {
			log.Fatalf("seed tokens: %v", err)
		}
	}
	if meta.OpenInstance(context.Background()) {
		log.Printf("auth: no users registered — open instance (anonymous read/write)")
	} else {
		auth = artifactkit.NewStoreAuth(meta)
	}

	var blobs artifactkit.BlobStore
	// Blob backend: filesystem (default) via the factory, or S3 placeholder. The
	// inline-SQLite blob backend is removed; artifact bytes live on the filesystem.
	blobs, err = store.OpenBlobStore(*blobBackend, filepath.Join(*dataDir, "blobs"))
	if err != nil {
		log.Fatalf("open blob store: %v", err)
	}

	reg := &artifactkit.Registry{Blobs: blobs, Meta: artifactkit.NewScopedStore(meta), Upstreams: upstreams, TargetStore: meta}
	// Load user-declared targets from the metadata DB on top of the built-ins.
	// A load failure must not prevent startup: the built-in table is a working
	// registry, and a broken row is dropped by the store.
	if err := upstreams.Targets.Load(context.Background(), meta); err != nil {
		log.Printf("targets: load user targets: %v", err)
	}

	// Mount every TARGET of the enabled protocols. A protocol's default target
	// has the protocol name as its ID, so /artifacts/<protocol> is unchanged;
	// a named mirror (maven.google) gets its own mount. The scope middleware
	// rewrites the target mount to the protocol mount and records the target,
	// so adapters never see which mirror served a request.
	names := *protocols
	enabled := map[string]bool{}
	if names == "" {
		for _, n := range artifactkit.Registered() {
			enabled[n] = true
		}
	} else {
		for _, n := range strings.Split(names, ",") {
			if n = strings.TrimSpace(n); n != "" {
				enabled[n] = true
			}
		}
	}
	mux := http.NewServeMux()
	mounted := 0
	selfBases := parseSelfBaseMap(*selfBaseMap)
	handlers := map[string]http.Handler{}
	for proto := range enabled {
		// Build one handler per protocol (its targets share it).
		if !artifactkit.ProtocolRegistered(proto) {
			log.Fatalf("build protocol %q: unknown protocol", proto)
		}
		handler, err := artifactkit.Build(proto, reg, configFor(proto, *selfBase, *selfBaseRaw, auth, *dataDir, selfBases, meta, blobs))
		if err != nil {
			log.Fatalf("build protocol %q: %v", proto, err)
		}
		if proto == "oci" {
			// OCI is spec-fixed at /v2 and its registry is the request Host.
			if auth != nil {
				tokenH := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					serveToken(w, r, auth)
				})
				mux.Handle("/v2/token", tokenH)
				mux.Handle("/token", tokenH)
			}
			scoped := artifactkit.ScopeMiddlewareForFormat("oci", "/v2", handler)
			mux.Handle("/v2", scoped)
			mux.Handle("/v2/", scoped)
			mounted++
			continue
		}
		handlers[proto] = handler
	}
	if len(handlers) > 0 {
		// One dispatcher serves every target under /artifacts/<target-id>/...,
		// resolving the target per request so a target created through the
		// admin API is immediately reachable. A protocol's default target has
		// the protocol name as its ID, so /artifacts/<protocol> is unchanged.
		d := artifactkit.NewTargetDispatcher(artifactkit.MountBase, reg, handlers)
		mux.Handle(artifactkit.MountBase+"/", d)
		mux.Handle(artifactkit.MountBase, d)
		mounted += len(handlers)
	}
	if mounted == 0 {
		log.Fatal("no protocols registered/enabled")
	}

	// Health endpoints (unauthenticated, outside the protocol mounts):
	//   /healthz  liveness  — the process is up (always 200).
	//   /readyz   readiness — the metadata store answers a cheap query; a
	//                         failing DB (locked, gone) pulls the pod from the
	//                         Service instead of serving errors.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		if _, err := meta.ListRepositories(r.Context()); err != nil {
			http.Error(w, "metadata store unavailable: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ready\n")
	})

	addr := *listen
	log.Printf("artifact listening on %s (%d protocols: %s), data=%s, airgap=%v",
		addr, mounted, names, *dataDir, *airGap)
	// Background reaper: expire negative cache entries and reclaim orphan
	// blobs. -gc-interval=0 disables it.
	go store.RunReaper(context.Background(), meta, blobs, *gcInterval, *gcGrace, log.Printf)
	// ARTIFACT_DEBUG=1 logs every request (method, path, framing, status) and
	// is invaluable when a client's upload/download framing is in question.
	debug := os.Getenv("ARTIFACT_DEBUG") != ""
	var root http.Handler = cleanPaths(mux)
	if debug {
		root = debugHandler(root)
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           root,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       10 * time.Minute, // large layer uploads stream slowly
		WriteTimeout:      10 * time.Minute, // large layer downloads stream slowly
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

// cleanPaths canonicalizes the request path before routing. Go's ServeMux
// already issues a 301 for paths that need cleaning, but clients such as apt
// do not always follow that redirect for their per-line base URLs (they send
// "/repo/./Packages" with a literal "." segment). Cleaning first keeps apt's
// flat-repo requests on the same route as a normal fetch.
func cleanPaths(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "" && strings.Contains(r.URL.Path, "/.") {
			if u := *r.URL; true {
				u.Path = path.Clean(u.Path)
				if u.RawPath != "" {
					u.RawPath = path.Clean(u.RawPath)
				}
				r2 := new(http.Request)
				*r2 = *r
				r2.URL = &u
				r = r2
			}
		}
		next.ServeHTTP(w, r)
	})
}

// debugHandler logs each request's method, path, body framing and the
// resulting status when ARTIFACT_DEBUG is set.
func debugHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lw := &logResponseWriter{ResponseWriter: w, status: 200}
		var body counter
		if r.Body != nil {
			r.Body = &countingBody{ReadCloser: r.Body, n: &body}
		}
		next.ServeHTTP(lw, r)
		cl := r.Header.Get("Content-Length")
		if cl == "" {
			cl = "-"
		}
		log.Printf("req %s %s cl=%s te=%q ct=%q body=%d dedic=%q sha1=%q -> %d (%s)",
			r.Method, r.URL.Path, cl, r.TransferEncoding, r.Header.Get("Content-Type"),
			body.n, r.Header.Get("X-Checksum-Deploy"), r.Header.Get("X-Checksum-Sha1"),
			lw.status, time.Since(start).Round(time.Millisecond))
	})
}

type counter struct{ n int64 }

type countingBody struct {
	io.ReadCloser
	n *counter
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.n.n += int64(n)
	return n, err
}

type logResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *logResponseWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// defaultUpstreams builds the upstream table from the target registry, which is
// the single source of truth for public upstreams and mirrors. The flat
// Defaults map is a projection of the registry so existing resolvers, the admin
// API and per-deployment overrides keep working unchanged.
func defaultUpstreams(airGap bool) *artifactkit.Upstreams {
	reg := targets.NewRegistry()
	return &artifactkit.Upstreams{
		Targets:   reg,
		Defaults:  reg.LegacyDefaults(),
		Overrides: map[string]string{},
		Proxy:     map[string]string{},
		AirGap:    airGap,
	}
}

// applyUpstreamOverrides mutates the upstream table from flag input:
//   - overrides: comma-separated "format=url" pairs replacing a format's base
//     (also usable to add a non-default format key).
//   - proxy: an HTTP proxy URL applied to every format's upstream fetches
//     (mihomo in the dev cluster); "none" forces direct connections.
func applyUpstreamOverrides(u *artifactkit.Upstreams, overrides, proxy string) {
	for _, pair := range strings.Split(overrides, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		u.Overrides[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if proxy == "" {
		return
	}
	if u.Proxy == nil {
		u.Proxy = map[string]string{}
	}
	if strings.EqualFold(proxy, "none") {
		u.Proxy["*"] = ""
		return
	}
	for k := range u.Defaults {
		u.Proxy[k] = proxy
	}
	u.Proxy["*"] = proxy
}

// parseSelfBaseMap parses "-self-base-map" (`proto=url` pairs). Each entry is
// an origin (raw) override for one protocol, letting a single instance serve
// every protocol with its own upstream-shaped self URLs.
func parseSelfBaseMap(s string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k != "" && v != "" {
			out[k] = strings.TrimSuffix(v, "/")
		}
	}
	return out
}

func configFor(name, selfBase string, selfBaseRaw bool, auth artifactkit.Auth, dataDir string, selfBases map[string]string, meta *store.Store, blobs artifactkit.BlobStore) map[string]any {
	cfg := map[string]any{}
	// Each protocol mounts under /artifacts/<name> (OCI is special-cased to /v2),
	// so its emitted self-URLs must carry that prefix. selfBase is the global
	// origin (scheme://host[:port]). For OCI, SelfBase is used ONLY to derive
	// the /token realm, which lives at the origin root — so pass the bare base.
	//
	// With -self-base-raw the base is the protocol's OWN upstream origin: the
	// adapter emits upstream-shaped URLs (registry.npmjs.org/react, pypi.org/
	// simple/..., index.crates.io/config.json) unchanged, and an intercepting
	// client proxy (easyproxy) maps them back onto /artifacts/<name>. This is what
	// makes publish/pull fully transparent to an unmodified client.
	//
	// A -self-base-map entry is a per-protocol origin and implies raw mode for
	// that protocol, so one instance serves every protocol with its own shape.
	if origin, ok := selfBases[name]; ok && origin != "" {
		cfg["self_base"] = origin
	} else if selfBase != "" {
		if name == "oci" || selfBaseRaw {
			cfg["self_base"] = strings.TrimSuffix(selfBase, "/")
		} else {
			cfg["self_base"] = strings.TrimSuffix(selfBase, "/") + "/artifacts/" + name
		}
	}
	if auth != nil {
		cfg["auth"] = auth
	}
	if name == "oci" {
		// Upload sessions live under the data dir (survive restarts, one
		// place to back up).
		cfg["upload_dir"] = filepath.Join(dataDir, "oci-uploads")
	}
	if name == "git" {
		// Bare mirrors live under the data dir: the second clone of a
		// repository is served from local objects.
		cfg["dir"] = filepath.Join(dataDir, "git")
	}
	if name == "system" && meta != nil {
		// The admin API can CRUD user-declared targets and report footprint.
		cfg["targets_store"] = meta
		cfg["stats_func"] = func(ctx context.Context) (any, error) {
			return meta.Stats(ctx, blobs)
		}
	}
	return cfg
}

// serveToken issues an OCI bearer token from the configured auth. Pull-only
// scopes are granted to anyone (an anonymous pull token carries no write
// privilege); push/delete scopes require an authenticated write-level
// principal, and the minted token never exposes the static credential.
func serveToken(w http.ResponseWriter, r *http.Request, auth artifactkit.Auth) {
	// Support both the GET token flow (scope in the query, credentials in
	// headers) and the OAuth2 password-grant POST (form body) used by
	// buildkit/containerd push.
	_ = r.ParseForm()
	scopeVals := append([]string{}, r.URL.Query()["scope"]...)
	scopeVals = append(scopeVals, r.PostForm["scope"]...)
	scopes := collectScopes(scopeVals)
	username := auth.Authenticate(r.Context(), r)
	if username == "" {
		// Password carries the credential (same as Basic); refresh_token may
		// carry a raw token.
		if pass := r.PostForm.Get("password"); pass != "" {
			if u, ok := auth.CheckToken(r.Context(), pass); ok {
				username = u
			}
		} else if ref := r.PostForm.Get("refresh_token"); ref != "" {
			if u, ok := auth.CheckToken(r.Context(), ref); ok {
				username = u
			}
		}
	}
	if username == "" && canPush(scopes) {
		// Anonymous push scope: challenge rather than mint anything.
		w.Header().Set("WWW-Authenticate", `Basic realm="/token"`)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"errors": []any{
			map[string]any{"code": "UNAUTHORIZED", "message": "authentication required"},
		}})
		return
	}
	tok := auth.IssueToken(r.Context(), username, scopes, time.Hour)
	if tok == "" && canPush(scopes) {
		// The auth layer refused to mint (e.g. a read-level principal asking
		// for push): the credential is valid but not privileged enough.
		w.Header().Set("WWW-Authenticate", `Basic realm="/token"`)
		writeJSON(w, http.StatusForbidden, map[string]any{"errors": []any{
			map[string]any{"code": "DENIED", "message": "insufficient privilege for requested scope"},
		}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token": tok, "access_token": tok, "expires_in": 3600,
	})
}

func collectScopes(vals []string) []string {
	var out []string
	for _, v := range vals {
		for _, s := range strings.Fields(v) {
			if s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func canPush(scopes []string) bool {
	for _, s := range scopes {
		if i := strings.LastIndex(s, ":"); i >= 0 {
			act := s[i+1:]
			// An OCI scope may carry comma-separated actions ("pull,push").
			for _, a := range strings.Split(act, ",") {
				if a == "push" || a == "delete" || a == "*" {
					return true
				}
			}
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = fmt.Fprintf(w, "%s", mustJSON(v))
}

func mustJSON(v any) string {
	b, err := jsonMarshal(v)
	if err != nil {
		return `{}`
	}
	return string(b)
}

// seedTokens registers `token=level` pairs into the credential DB. Idempotent:
// a token already present (same hash) is not duplicated.
func seedTokens(meta *store.Store, spec string) error {
	ctx := context.Background()
	for _, pair := range strings.FieldsFunc(spec, func(r rune) bool { return r == ',' || r == ' ' }) {
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 || parts[0] == "" {
			return fmt.Errorf("malformed token pair %q (want token=level)", pair)
		}
		tok, level := parts[0], parts[1]
		switch level {
		case "read", "r", "rl":
			level = "read"
		case "write", "w", "rw":
			level = "write"
		default:
			return fmt.Errorf("invalid level %q (read|write)", level)
		}
		if _, ok := meta.LookupToken(ctx, tok); ok {
			continue // already seeded
		}
		uid, err := meta.CreateAuthUser(ctx, "token-user")
		if err != nil {
			return err
		}
		if err := meta.CreateAuthToken(ctx, tok, uid, level); err != nil {
			return err
		}
		log.Printf("auth: seeded credential (level %s)", level)
	}
	return nil
}

// applyRepoUpstreams installs per-repository upstream overrides from
// "format/repo=url" pairs. The repository part may be a namespace prefix
// (maven/org.apache, npm/@acme, go/github.com/acme); the longest matching
// prefix wins at request time, so one entry covers a whole subtree.
func applyRepoUpstreams(u *artifactkit.Upstreams, pairs string) {
	for _, pair := range strings.Split(pairs, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		format, repo, ok := strings.Cut(k, "/")
		if !ok || format == "" || repo == "" || v == "" {
			continue
		}
		u.SetRepo(format, repo, v, "")
	}
}
