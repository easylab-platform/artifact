package conda

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/easylab-platform/artifact/core"
	"github.com/easylab-platform/artifact/core/store"
)

func newHostedState(t *testing.T) *State {
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
	h, err := NewHandler(&artifactkit.Registry{Blobs: blobs, Meta: meta,
		Upstreams: &artifactkit.Upstreams{Defaults: map[string]string{}, Overrides: map[string]string{}, Proxy: map[string]string{}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return h.(*State)
}

// TestHostedRepodata drives a self-published conda package: upload under a
// subdir, then verify repodata.json carries the package with its real
// md5/sha256.
func TestHostedRepodata(t *testing.T) {
	s := newHostedState(t)
	pkg := []byte("fake-conda-content")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/pkgs/conda/mychannel/linux-64/mytool-1.2.3-py310_0.conda", bytes.NewReader(pkg))
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}

	rec2 := httptest.NewRecorder()
	s.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/pkgs/conda/mychannel/linux-64/repodata.json", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("repodata: %d", rec2.Code)
	}
	var doc struct {
		Info     map[string]any            `json:"info"`
		Packages map[string]map[string]any `json:"packages.conda"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Info["subdir"] != "linux-64" {
		t.Fatalf("subdir = %v", doc.Info["subdir"])
	}
	entry, ok := doc.Packages["mytool-1.2.3-py310_0.conda"]
	if !ok {
		t.Fatalf("package entry missing: %v", doc.Packages)
	}
	if entry["name"] != "mytool" || entry["version"] != "1.2.3" {
		t.Fatalf("entry = %v", entry)
	}
	if entry["sha256"] == "" || entry["md5"] == "" {
		t.Fatalf("checksums missing: %v", entry)
	}
	if entry["build_number"] != float64(0) {
		t.Fatalf("build_number = %v", entry["build_number"])
	}

	// Download.
	rec3 := httptest.NewRecorder()
	s.ServeHTTP(rec3, httptest.NewRequest(http.MethodGet, "/pkgs/conda/mychannel/linux-64/mytool-1.2.3-py310_0.conda", nil))
	if rec3.Code != http.StatusOK || !bytes.Equal(rec3.Body.Bytes(), pkg) {
		t.Fatalf("download: %d", rec3.Code)
	}
}

// TestCondaNVRB verifies conda filename parsing.
func TestCondaNVRB(t *testing.T) {
	n, v, b, bn := condaNVRB("numpy-1.26.0-py311h64a7726_0.conda")
	if n != "numpy" || v != "1.26.0" || b != "py311h64a7726_0" || bn != 0 {
		t.Fatalf("got %q %q %q %d", n, v, b, bn)
	}
	n2, v2, b2, bn2 := condaNVRB("scikit-learn-1.3.2-py310_5.tar.bz2")
	if n2 != "scikit-learn" || v2 != "1.3.2" || b2 != "py310_5" || bn2 != 5 {
		t.Fatalf("got %q %q %q %d", n2, v2, b2, bn2)
	}
}
