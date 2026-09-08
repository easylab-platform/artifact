package oci

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/easylab-platform/artifact/core"
	"github.com/easylab-platform/artifact/core/store"
)

// newTestAdapter builds an OCI adapter over a temp filesystem substrate.
func newTestAdapter(t *testing.T) (*Adapter, *artifactkit.Registry) {
	t.Helper()
	dir := t.TempDir()
	meta, err := store.OpenSQLite(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	blobs, err := store.NewFileBlobStore(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	reg := &artifactkit.Registry{Blobs: blobs, Meta: meta, Upstreams: &artifactkit.Upstreams{}}
	a := New(&OciState{Registry: reg, UploadDir: filepath.Join(dir, "uploads")})
	t.Cleanup(a.Stop)
	return a, reg
}

// TestUploadSessionLifecycle drives the full chunked-upload protocol:
// POST starts a session, PATCH appends, PUT commits with the digest, and the
// blob is then readable through the blob store.
func TestUploadSessionLifecycle(t *testing.T) {
	a, reg := newTestAdapter(t)
	payload := []byte("hello layer")

	// POST: start session.
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v2/team/app/blobs/uploads/", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start session: %d %s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if loc == "" {
		t.Fatal("no Location header")
	}

	// PATCH: append the chunk.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPatch, loc, bytes.NewReader(payload))
	a.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusAccepted {
		t.Fatalf("patch: %d %s", rec2.Code, rec2.Body.String())
	}
	if rng := rec2.Header().Get("Range"); rng != "0-10" {
		t.Fatalf("range after patch = %q", rng)
	}

	// GET: resume reports the offset from disk.
	rec3 := httptest.NewRecorder()
	a.ServeHTTP(rec3, httptest.NewRequest(http.MethodGet, loc, nil))
	if rec3.Code != http.StatusNoContent {
		t.Fatalf("resume: %d", rec3.Code)
	}
	if rng := rec3.Header().Get("Range"); rng != "0-10" {
		t.Fatalf("range on resume = %q", rng)
	}

	// PUT with a matching digest commits.
	sum := sha256.Sum256(payload)
	dgst := "sha256:" + hex.EncodeToString(sum[:])
	rec4 := httptest.NewRecorder()
	a.ServeHTTP(rec4, httptest.NewRequest(http.MethodPut, loc+"?digest="+dgst, nil))
	if rec4.Code != http.StatusCreated {
		t.Fatalf("commit: %d %s", rec4.Code, rec4.Body.String())
	}
	// Blob readable.
	rd, err := reg.Blobs.Open(context.Background(), dgst)
	if err != nil || rd == nil {
		t.Fatalf("blob after commit: %v", err)
	}
	defer func() { _ = rd.Close() }()
	got, _ := io.ReadAll(rd)
	if !bytes.Equal(got, payload) {
		t.Fatalf("blob roundtrip: %q", got)
	}
	// Session is gone: a further PATCH 404s.
	rec5 := httptest.NewRecorder()
	a.ServeHTTP(rec5, httptest.NewRequest(http.MethodPatch, loc, bytes.NewReader(payload)))
	if rec5.Code != http.StatusNotFound {
		t.Fatalf("post-commit patch should 404, got %d", rec5.Code)
	}
}

// TestUploadSessionWrongDigest verifies a commit with a mismatched digest is
// rejected and the session stays resumable.
func TestUploadSessionWrongDigest(t *testing.T) {
	a, _ := newTestAdapter(t)
	payload := []byte("data")

	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v2/team/app/blobs/uploads/", nil))
	loc := rec.Header().Get("Location")

	rec2 := httptest.NewRecorder()
	a.ServeHTTP(rec2, httptest.NewRequest(http.MethodPatch, loc, bytes.NewReader(payload)))
	if rec2.Code != http.StatusAccepted {
		t.Fatalf("patch: %d", rec2.Code)
	}

	bad := "sha256:" + hex.EncodeToString(make([]byte, 32))
	rec3 := httptest.NewRecorder()
	a.ServeHTTP(rec3, httptest.NewRequest(http.MethodPut, loc+"?digest="+bad, nil))
	if rec3.Code != http.StatusBadRequest {
		t.Fatalf("bad digest commit: %d", rec3.Code)
	}

	// Session survives; correct digest still commits.
	sum := sha256.Sum256(payload)
	good := "sha256:" + hex.EncodeToString(sum[:])
	rec4 := httptest.NewRecorder()
	a.ServeHTTP(rec4, httptest.NewRequest(http.MethodPut, loc+"?digest="+good, nil))
	if rec4.Code != http.StatusCreated {
		t.Fatalf("good digest after bad: %d %s", rec4.Code, rec4.Body.String())
	}
}

// TestUploadSessionRepoBinding verifies a session cannot be mounted onto a
// different repository than the one that started it.
func TestUploadSessionRepoBinding(t *testing.T) {
	a, _ := newTestAdapter(t)
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v2/team/app/blobs/uploads/", nil))
	loc := rec.Header().Get("Location")

	// Cross-repo PATCH is rejected.
	other := "/v2" + bytesToString(bytes.ReplaceAll([]byte(loc), []byte("/team/app/"), []byte("/other/repo/")))
	rec2 := httptest.NewRecorder()
	a.ServeHTTP(rec2, httptest.NewRequest(http.MethodPatch, other, bytes.NewReader([]byte("x"))))
	if rec2.Code == http.StatusAccepted {
		t.Fatal("cross-repository session append must be rejected")
	}
}

// TestUploadSingleShot verifies the monolithic POST?digest= upload path.
func TestUploadSingleShot(t *testing.T) {
	a, reg := newTestAdapter(t)
	payload := []byte("one shot")
	sum := sha256.Sum256(payload)
	dgst := "sha256:" + hex.EncodeToString(sum[:])

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v2/team/app/blobs/uploads/?digest="+dgst, bytes.NewReader(payload))
	a.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("single shot: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := reg.Blobs.Stat(context.Background(), dgst); err != nil {
		t.Fatal(err)
	}
}

// TestBlobMount verifies the cross-repo blob mount shortcut.
func TestBlobMount(t *testing.T) {
	a, reg := newTestAdapter(t)
	payload := []byte("shared layer")
	sum := sha256.Sum256(payload)
	dgst := "sha256:" + hex.EncodeToString(sum[:])
	if _, err := reg.Blobs.PutIfAbsent(context.Background(), dgst, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v2/other/repo/blobs/uploads/?mount="+dgst, nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("mount: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Location") != "/v2/other/repo/blobs/"+dgst {
		t.Fatalf("mount location = %q", rec.Header().Get("Location"))
	}
}

// TestValidSessionID hardens the path-traversal guard.
func TestValidSessionID(t *testing.T) {
	if !validSessionID("sess-" + hex.EncodeToString(make([]byte, 16))) {
		t.Fatal("well-formed id must be accepted")
	}
	if validSessionID("../../etc/passwd") {
		t.Fatal("traversal id must be rejected")
	}
	if validSessionID("sess-abc") {
		t.Fatal("short id must be rejected")
	}
	if validSessionID("") {
		t.Fatal("empty id must be rejected")
	}
}

// TestManifestPutAndGet verifies manifest round-trip incl. digest aliasing.
func TestManifestPutAndGet(t *testing.T) {
	a, reg := newTestAdapter(t)
	body := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"c","digest":"sha256:x","size":1},"layers":[]}`)
	sum := sha256.Sum256(body)
	dgst := "sha256:" + hex.EncodeToString(sum[:])

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/v2/team/app/manifests/v1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	a.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("manifest put: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Docker-Content-Digest") != dgst {
		t.Fatalf("digest header = %q", rec.Header().Get("Docker-Content-Digest"))
	}

	// Tag resolution serves the body.
	rec2 := httptest.NewRecorder()
	a.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/v2/team/app/manifests/v1", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("manifest get: %d", rec2.Code)
	}
	if !bytes.Equal(rec2.Body.Bytes(), body) {
		t.Fatal("manifest body mismatch")
	}
	// Digest alias resolves too.
	rec3 := httptest.NewRecorder()
	a.ServeHTTP(rec3, httptest.NewRequest(http.MethodGet, "/v2/team/app/manifests/"+dgst, nil))
	if rec3.Code != http.StatusOK {
		t.Fatalf("manifest get by digest: %d", rec3.Code)
	}
	_ = reg
}

// TestManifestDigestMismatch verifies a digest-pinned PUT with wrong content
// is rejected.
func TestManifestDigestMismatch(t *testing.T) {
	a, _ := newTestAdapter(t)
	body := []byte(`{"schemaVersion":2}`)
	sum := sha256.Sum256([]byte("different"))
	wrong := "sha256:" + hex.EncodeToString(sum[:])
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/v2/team/app/manifests/"+wrong, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	a.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("mismatched digest put: %d", rec.Code)
	}
}

// TestCatalogListsRepositories verifies the local catalog (no upstream).
func TestCatalogListsRepositories(t *testing.T) {
	a, reg := newTestAdapter(t)
	if err := reg.Meta.Put(context.Background(), artifactkit.Artifact{Format: "oci", Repository: "a/b", Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v2/_catalog", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog: %d", rec.Code)
	}
	var out struct {
		Repositories []string `json:"repositories"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Repositories) != 1 || out.Repositories[0] != "a/b" {
		t.Fatalf("catalog = %v", out.Repositories)
	}
}

func bytesToString(b []byte) string { return string(b) }
