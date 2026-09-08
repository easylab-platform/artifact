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
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/easylab-platform/artifact/cmd/adapters" // registers all enabled protocols
	"github.com/easylab-platform/artifact/core"
	"github.com/easylab-platform/artifact/core/store"
)

func main() {
	var (
		listen      = flag.String("listen", ":8080", "HTTP listen address")
		dataDir     = flag.String("data", "./data", "substrate root (sqlite + blobs + upstreams)")
		protocols   = flag.String("protocols", "", "comma-separated protocols to mount (default: all registered)")
		selfBase    = flag.String("self-base", "", "external base URL for auth realms / self URIs")
		tokens      = flag.String("tokens", "", "`token=level` pairs (read|write) to seed into the credential DB (hashed), comma separated; repeat on every start to keep them registered")
		airGap      = flag.Bool("air-gap", false, "disable all upstream pull-through")
		blobBackend = flag.String("blob-backend", "filesystem", "blob backend: filesystem | s3")
	)
	flag.Parse()

	upstreams := defaultUpstreams(*airGap)

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

	reg := &artifactkit.Registry{Blobs: blobs, Meta: meta, Upstreams: upstreams}

	// Determine which protocols to mount.
	names := *protocols
	if names == "" {
		all := artifactkit.Registered()
		names = strings.Join(all, ",")
	}
	mux := http.NewServeMux()
	mounted := 0
	for _, name := range strings.Split(names, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		handler, err := artifactkit.Build(name, reg, configFor(name, *selfBase, auth, *dataDir))
		if err != nil {
			log.Fatalf("build protocol %q: %v", name, err)
		}
		// OCI is spec-fixed at /v2; everything else under /pkgs/<name>.
		if name == "oci" {
			// Wire the /token auth endpoint the OCI challenges reference.
			// easylab serves it under /v2/token (the /v2 mount), so expose both.
			if auth != nil {
				tokenH := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					serveToken(w, r, auth)
				})
				mux.Handle("/v2/token", tokenH)
				mux.Handle("/token", tokenH)
			}
			mux.Handle("/v2", handler)
			mux.Handle("/v2/", handler)
		} else {
			mux.Handle("/pkgs/"+name+"/", handler)
			mux.Handle("/pkgs/"+name, handler)
		}
		mounted++
	}
	if mounted == 0 {
		log.Fatal("no protocols registered/enabled")
	}

	addr := *listen
	log.Printf("artifact listening on %s (%d protocols: %s), data=%s, airgap=%v",
		addr, mounted, names, *dataDir, *airGap)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
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

func defaultUpstreams(airGap bool) *artifactkit.Upstreams {
	return &artifactkit.Upstreams{
		Defaults: map[string]string{
			"oci":      "https://registry-1.docker.io",
			"cargo":    "https://crates.io",
			"composer": "https://repo.packagist.org",
			"conan":    "https://center.conan.io",
			"go":       "https://proxy.golang.org",
			"helm":     "https://charts.helm.sh/stable",
			"hex":      "https://repo.hex.pm",
			"maven":    "https://repo.maven.apache.org/maven2",
			"npm":      "https://registry.npmjs.org",
			"nuget":    "https://api.nuget.org",
			"pub":      "https://pub.dev",
			"pypi":     "https://pypi.org",
			"rubygems": "https://rubygems.org",
			"swift":    "https://api.spm.swift.org",
			// Sub-endpoints that live on a different host than the format's
			// primary upstream (crates.io: index + static downloads are
			// served from index.crates.io / static.crates.io).
			"cargo.index":        "https://index.crates.io",
			"cargo.static":       "https://static.crates.io/crates",
			"conan.center":       "https://center2.conan.io",
			"nuget.search":       "https://azuresearch-usnc.nuget.org",
			"nuget.registration": "https://api.nuget.org",
			"hex.repo":           "https://repo.hex.pm",
			"rubygems.index":     "https://index.rubygems.org",
			"rubygems.gems":      "https://rubygems.org/gems",
		},
		Overrides: map[string]string{},
		Proxy:     map[string]string{},
		AirGap:    airGap,
	}
}

func configFor(name, selfBase string, auth artifactkit.Auth, dataDir string) map[string]any {
	cfg := map[string]any{}
	// Each protocol mounts under /pkgs/<name> (OCI is special-cased to /v2),
	// so its emitted self-URLs must carry that prefix. selfBase is the global
	// origin (scheme://host[:port]). For OCI, SelfBase is used ONLY to derive
	// the /token realm, which lives at the origin root — so pass the bare base.
	if selfBase != "" {
		if name == "oci" {
			cfg["self_base"] = strings.TrimSuffix(selfBase, "/")
		} else {
			cfg["self_base"] = strings.TrimSuffix(selfBase, "/") + "/pkgs/" + name
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
	return cfg
}

// serveToken issues an OCI bearer token from the configured auth. Pull-only
// scopes are granted to anyone (an anonymous pull token carries no write
// privilege); push/delete scopes require an authenticated write-level
// principal, and the minted token never exposes the static credential.
func serveToken(w http.ResponseWriter, r *http.Request, auth artifactkit.Auth) {
	scopes := collectScopes(r.URL.Query()["scope"])
	username := auth.Authenticate(r.Context(), r)
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
