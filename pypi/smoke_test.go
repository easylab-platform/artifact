package pypi

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/easylab-platform/artifact/core"
	"github.com/easylab-platform/artifact/core/store"
)

// TestUploadPublishSmoke verifies the multipart publish path: name/version
// extraction, blob persistence, and index write.
func TestUploadPublishSmoke(t *testing.T) {
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
	s := &State{Registry: &artifactkit.Registry{Blobs: blobs, Meta: meta, Upstreams: &artifactkit.Upstreams{}}}

	// Build a multipart publish body like twine's.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField(":action", "file_upload")
	_ = mw.WriteField("name", "Demo-Package")
	_ = mw.WriteField("version", "1.0.0")
	fw, _ := mw.CreateFormFile("content", "demo_package-1.0.0-py3-none-any.whl")
	_, _ = fw.Write([]byte("fake wheel bytes"))
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+mw.Boundary())
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}

	// The normalized project must be indexed with the version.
	vs, err := meta.ListVersions(context.Background(), "pypi", "demo-package")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range vs {
		if v == "1.0.0" {
			found = true
		}
	}
	if !found {
		t.Fatalf("version not indexed: %v", vs)
	}
}

// TestSimpleRootJSON verifies the PEP 691 JSON project index.
func TestSimpleRootJSON(t *testing.T) {
	dir := t.TempDir()
	meta, _ := store.OpenSQLite(filepath.Join(dir, "m.db"))
	t.Cleanup(func() { _ = meta.Close() })
	blobs, _ := store.NewFileBlobStore(filepath.Join(dir, "blobs"))
	s := &State{Registry: &artifactkit.Registry{Blobs: blobs, Meta: meta, Upstreams: &artifactkit.Upstreams{}}}
	if err := meta.Put(context.Background(), artifactkit.Artifact{Format: "pypi", Repository: "demo-package", Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/pkgs/pypi/simple/", nil)
	req.Header.Set("Accept", "application/vnd.pypi.simple.v1+json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("simple root: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "demo-package") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	var v map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("not json: %v", err)
	}
}
