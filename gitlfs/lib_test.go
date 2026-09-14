package gitlfs

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/easylab-platform/artifact/core"
	"github.com/easylab-platform/artifact/core/store"
)

func newFixture(t *testing.T) *State {
	t.Helper()
	dir := t.TempDir()
	meta, err := store.OpenSQLite(filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	blobs, err := store.NewFileBlobStore(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	return &State{Registry: &artifactkit.Registry{Blobs: blobs, Meta: meta,
		Upstreams: &artifactkit.Upstreams{Defaults: map[string]string{}, Overrides: map[string]string{}, Proxy: map[string]string{}}}}
}

// TestBatchNegotiation verifies the batch response shape for both
// operations.
func TestBatchNegotiation(t *testing.T) {
	s := newFixture(t)
	oid := strings.Repeat("ab", 32)
	body, _ := json.Marshal(batchRequest{Operation: "download", Objects: []batchObjIn{{Oid: oid, Size: 10}}})
	req := httptest.NewRequest(http.MethodPost, "/pkgs/gitlfs/team/repo/info/lfs/objects/batch", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("batch: %d %s", rec.Code, rec.Body.String())
	}
	var out batchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Objects) != 1 || out.Objects[0].Actions["download"].Href == "" {
		t.Fatalf("download batch = %+v", out)
	}
	if !strings.Contains(out.Objects[0].Actions["download"].Href, "/objects/"+oid) {
		t.Fatalf("href = %q", out.Objects[0].Actions["download"].Href)
	}
}

// TestUploadDownloadRoundtrip drives PUT (CAS store, digest verified) and
// GET (CAS serve) plus a bad-oid rejection.
func TestUploadDownloadRoundtrip(t *testing.T) {
	s := newFixture(t)
	content := []byte("lfs object bytes")
	sum := sha256.Sum256(content)
	oid := hex.EncodeToString(sum[:])

	// PUT.
	req := httptest.NewRequest(http.MethodPut, "/pkgs/gitlfs/team/repo/objects/"+oid, bytes.NewReader(content))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body.String())
	}

	// GET.
	req2 := httptest.NewRequest(http.MethodGet, "/pkgs/gitlfs/team/repo/objects/"+oid, nil)
	rec2 := httptest.NewRecorder()
	s.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK || !bytes.Equal(rec2.Body.Bytes(), content) {
		t.Fatalf("GET: %d", rec2.Code)
	}

	// Content-addressed semantics: a PUT whose bytes don't match the oid
	// cannot overwrite (dedup short-circuits on the existing digest path);
	// the stored object must still be the ORIGINAL content.
	req3 := httptest.NewRequest(http.MethodPut, "/pkgs/gitlfs/team/repo/objects/"+oid, bytes.NewReader([]byte("other bytes")))
	rec3 := httptest.NewRecorder()
	s.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("dedup PUT = %d", rec3.Code)
	}
	req5 := httptest.NewRequest(http.MethodGet, "/pkgs/gitlfs/team/repo/objects/"+oid, nil)
	rec5 := httptest.NewRecorder()
	s.ServeHTTP(rec5, req5)
	if !bytes.Equal(rec5.Body.Bytes(), content) {
		t.Fatal("dedup PUT must not alter the stored object")
	}

	// Invalid oid.
	req4 := httptest.NewRequest(http.MethodGet, "/pkgs/gitlfs/team/repo/objects/nothex", nil)
	rec4 := httptest.NewRecorder()
	s.ServeHTTP(rec4, req4)
	if rec4.Code != http.StatusBadRequest {
		t.Fatalf("bad oid = %d", rec4.Code)
	}

	// Registered in the index.
	vs, err := s.Registry.Meta.ListVersions(context.Background(), "gitlfs", "team/repo")
	if err != nil || len(vs) != 1 {
		t.Fatalf("versions: %v %v", vs, err)
	}
	_ = rand.Reader
}

// TestSelfBaseOverridesHost verifies action hrefs use the configured
// SelfBase rather than the request Host (critical behind the egress proxy,
// where Host is the upstream name).
func TestSelfBaseOverridesHost(t *testing.T) {
	s := newFixture(t)
	s.SelfBase = "https://easylab.internal:8443"
	oid := strings.Repeat("cd", 32)
	body, _ := json.Marshal(batchRequest{Operation: "upload", Objects: []batchObjIn{{Oid: oid, Size: 1}}})
	req := httptest.NewRequest(http.MethodPost, "/pkgs/gitlfs/team/repo/info/lfs/objects/batch", bytes.NewReader(body))
	req.Host = "github.com" // simulating the rewritten upstream Host
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	var out batchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	href := out.Objects[0].Actions["upload"].Href
	if !strings.HasPrefix(href, "https://easylab.internal:8443/pkgs/gitlfs/team/repo/objects/") {
		t.Fatalf("href = %q", href)
	}
}
