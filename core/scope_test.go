package artifactkit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveScopeMaven(t *testing.T) {
	rq := httptest.NewRequest(http.MethodGet, "/pkgs/maven/org/slf4j/slf4j-api/2.0.9/slf4j-api-2.0.9.pom", nil)
	sc, path, ok := resolveScope("/pkgs", rq)
	if !ok || sc.Format != "maven" || sc.Namespace != "org.slf4j" || sc.Name != "slf4j-api" {
		t.Fatalf("scope=%+v path=%q ok=%v", sc, path, ok)
	}
	if path != "/pkgs/maven/org/slf4j/slf4j-api/2.0.9/slf4j-api-2.0.9.pom" {
		t.Fatalf("path changed: %q", path)
	}
}

func TestResolveScopeExplicit(t *testing.T) {
	rq := httptest.NewRequest(http.MethodGet, "/pkgs/maven/-/internal/org/acme/lib/1.0/lib-1.0.pom", nil)
	sc, path, ok := resolveScope("/pkgs", rq)
	if !ok || sc.Namespace != "internal" || !sc.Explicit {
		t.Fatalf("scope=%+v ok=%v", sc, ok)
	}
	if path != "/pkgs/maven/org/acme/lib/1.0/lib-1.0.pom" {
		t.Fatalf("rewrite: %q", path)
	}
}

func TestResolveScopeNpmDashNotExplicit(t *testing.T) {
	// npm reserves /-/ for its own endpoints; it must not be read as a repo.
	rq := httptest.NewRequest(http.MethodGet, "/pkgs/npm/-/v1/search", nil)
	sc, path, ok := resolveScope("/pkgs", rq)
	if !ok || sc.Explicit {
		t.Fatalf("npm dash misread as explicit: %+v", sc)
	}
	if path != "/pkgs/npm/-/v1/search" {
		t.Fatalf("path rewritten: %q", path)
	}
}

func TestResolveScopeOCIHost(t *testing.T) {
	rq := httptest.NewRequest(http.MethodGet, "/v2/acme/app/manifests/latest", nil)
	rq.Host = "ghcr.io"
	sc, _, ok := resolveScopeForFormat("oci", "/v2", rq)
	if !ok || sc.Namespace != "ghcr.io" || sc.Host != "ghcr.io" {
		t.Fatalf("scope=%+v ok=%v", sc, ok)
	}
}

func TestScopedStoreNamespaces(t *testing.T) {
	mem := newFakeIndex()
	st := NewScopedStore(mem)
	ctx := WithRepoScope(context.Background(), RepoScope{Format: "maven", Namespace: "org.slf4j", Name: "slf4j-api"})
	if err := st.Put(ctx, Artifact{Format: "maven", Repository: "slf4j-api", Version: "2.0.9"}); err != nil {
		t.Fatal(err)
	}
	// Stored under the namespaced key.
	if _, err := mem.Get(context.Background(), "maven", "org.slf4j/slf4j-api", "2.0.9"); err != nil {
		t.Fatalf("not namespaced: %v", err)
	}
	// Read back through the scope sees the unscoped name.
	got, err := st.Get(ctx, "maven", "slf4j-api", "2.0.9")
	if err != nil || got.Repository != "slf4j-api" {
		t.Fatalf("scoped get: %+v %v", got, err)
	}
	// A different namespace is isolated.
	ctx2 := WithRepoScope(context.Background(), RepoScope{Format: "maven", Namespace: "com.acme", Name: "lib"})
	if _, err := st.Get(ctx2, "maven", "slf4j-api", "2.0.9"); err == nil {
		t.Fatal("namespace isolation broken")
	}
}

func TestScopeMiddlewareSetsContext(t *testing.T) {
	var seen RepoScope
	h := ScopeMiddleware("/pkgs", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RepoScopeFrom(r.Context())
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/pkgs/npm/@acme/ui", nil))
	if seen.Namespace != "@acme" || seen.Name != "ui" {
		t.Fatalf("seen=%+v", seen)
	}
}

// fakeIndex is a minimal in-memory IndexStore for the scoped-store test.
type fakeIndex struct{ m map[string]Artifact }

func newFakeIndex() *fakeIndex { return &fakeIndex{m: map[string]Artifact{}} }

func (f *fakeIndex) key(format, repo, ver string) string { return format + "|" + repo + "|" + ver }
func (f *fakeIndex) Put(_ context.Context, a Artifact) error {
	f.m[f.key(a.Format, a.Repository, a.Version)] = a
	return nil
}
func (f *fakeIndex) Get(_ context.Context, format, repo, ver string) (Artifact, error) {
	a, ok := f.m[f.key(format, repo, ver)]
	if !ok {
		return Artifact{}, ErrArtifactUnknown
	}
	return a, nil
}
func (f *fakeIndex) Delete(_ context.Context, format, repo, ver string) error {
	delete(f.m, f.key(format, repo, ver))
	return nil
}
func (f *fakeIndex) ListVersions(_ context.Context, format, repo string) ([]string, error) {
	var out []string
	for k, a := range f.m {
		if a.Format == format && a.Repository == repo {
			out = append(out, a.Version)
		}
		_ = k
	}
	return out, nil
}
func (f *fakeIndex) ListRepositoriesByFormat(context.Context, string) ([]string, error) {
	return nil, nil
}
func (f *fakeIndex) ListRepositories(context.Context) ([]string, error) { return nil, nil }
func (f *fakeIndex) ListPackages(context.Context) ([]PackageSummary, error) {
	return nil, nil
}
func (f *fakeIndex) DeleteRepo(context.Context, string, string) (int, error) { return 0, nil }
func (f *fakeIndex) SaveUpload(context.Context, UploadRecord) error          { return nil }
func (f *fakeIndex) GetUpload(context.Context, string) (UploadRecord, error) {
	return UploadRecord{}, ErrUploadUnknown
}
func (f *fakeIndex) DeleteUpload(context.Context, string) error { return nil }
func (f *fakeIndex) ListUploads(context.Context) ([]string, error) {
	return nil, nil
}
func (f *fakeIndex) GetMeta(context.Context, string, string) ([]byte, error) { return nil, nil }
func (f *fakeIndex) SetMeta(context.Context, string, string, []byte) error   { return nil }
func (f *fakeIndex) Close() error                                            { return nil }
