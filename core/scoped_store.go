package artifactkit

import "context"

// scopedStore decorates an IndexStore so every repository argument is
// qualified with the request's RepoScope namespace. It is what makes
// namespaces universal: an adapter keeps writing (format, repository, version)
// exactly as before, and the decorator turns the repository into the canonical
// RepoKey(namespace, repository) on the way in and back to its unscoped form on
// the way out.
//
// The context carries the scope (WithRepoScope). Calls without a scope — the
// startup/open-instance paths, background maintenance — pass through verbatim.
//
// Scope application is idempotent: a key that already includes the namespace
// (npm's "@scope/name", conda's "channel/subdir", OCI's "host/repo") is left
// unchanged, so adapters that already encode the namespace are not
// double-prefixed.
type scopedStore struct {
	inner IndexStore
}

// NewScopedStore wraps an IndexStore with request-scoped namespacing.
func NewScopedStore(inner IndexStore) IndexStore { return &scopedStore{inner: inner} }

// scopeIn qualifies an adapter-supplied repository with the context's scope:
// the target prefix first (isolated targets), then the namespace.
func scopeIn(ctx context.Context, repository string) string {
	s := RepoScopeFrom(ctx)
	if s.Namespace == "" && !s.Explicit && s.targetPrefix() == "" {
		return repository
	}
	return s.ScopedKey(repository)
}

func (s *scopedStore) Put(ctx context.Context, a Artifact) error {
	a.Repository = scopeIn(ctx, a.Repository)
	// Record provenance: the target the request was routed through, when the
	// adapter did not set one. It never affects the primary key.
	if a.Target == "" {
		if sc := RepoScopeFrom(ctx); sc.Target != "" {
			a.Target = sc.Target
		}
	}
	return s.inner.Put(ctx, a)
}

func (s *scopedStore) Get(ctx context.Context, format, repository, version string) (Artifact, error) {
	a, err := s.inner.Get(ctx, format, scopeIn(ctx, repository), version)
	if err != nil {
		return a, err
	}
	// Present the repository in its unscoped form so adapters see the shape
	// they wrote.
	if sc := RepoScopeFrom(ctx); sc.Namespace != "" || sc.targetPrefix() != "" {
		a.Repository = sc.UnscopedKey(a.Repository)
	}
	return a, nil
}

func (s *scopedStore) Delete(ctx context.Context, format, repository, version string) error {
	return s.inner.Delete(ctx, format, scopeIn(ctx, repository), version)
}

func (s *scopedStore) ListVersions(ctx context.Context, format, repository string) ([]string, error) {
	return s.inner.ListVersions(ctx, format, scopeIn(ctx, repository))
}

func (s *scopedStore) DeleteRepo(ctx context.Context, format, repository string) (int, error) {
	return s.inner.DeleteRepo(ctx, format, scopeIn(ctx, repository))
}

func (s *scopedStore) SaveUpload(ctx context.Context, u UploadRecord) error {
	u.Repository = scopeIn(ctx, u.Repository)
	return s.inner.SaveUpload(ctx, u)
}

func (s *scopedStore) GetUpload(ctx context.Context, id string) (UploadRecord, error) {
	u, err := s.inner.GetUpload(ctx, id)
	if err != nil {
		return u, err
	}
	if sc := RepoScopeFrom(ctx); sc.Namespace != "" {
		u.Repository = sc.UnscopedName(u.Repository)
	}
	return u, nil
}

func (s *scopedStore) GetMeta(ctx context.Context, format, repository string) ([]byte, error) {
	return s.inner.GetMeta(ctx, format, scopeIn(ctx, repository))
}

func (s *scopedStore) SetMeta(ctx context.Context, format, repository string, data []byte) error {
	return s.inner.SetMeta(ctx, format, scopeIn(ctx, repository), data)
}

// The listing/summary calls are namespace-agnostic (they walk everything), so
// they pass straight through.
func (s *scopedStore) ListRepositoriesByFormat(ctx context.Context, format string) ([]string, error) {
	return s.inner.ListRepositoriesByFormat(ctx, format)
}

func (s *scopedStore) ListRepositories(ctx context.Context) ([]string, error) {
	return s.inner.ListRepositories(ctx)
}

func (s *scopedStore) ListPackages(ctx context.Context) ([]PackageSummary, error) {
	return s.inner.ListPackages(ctx)
}

// ReferencedDigests is namespace-agnostic (it walks everything), so it passes
// straight through.
func (s *scopedStore) ReferencedDigests(ctx context.Context) (map[string]bool, error) {
	return s.inner.ReferencedDigests(ctx)
}

func (s *scopedStore) DeleteUpload(ctx context.Context, id string) error {
	return s.inner.DeleteUpload(ctx, id)
}

func (s *scopedStore) ListUploads(ctx context.Context) ([]string, error) {
	return s.inner.ListUploads(ctx)
}

func (s *scopedStore) Close() error { return s.inner.Close() }
