package pypi

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	artifactkit "github.com/easylab-platform/artifact/core"
	"github.com/easylab-platform/artifact/core/store"
)

func buildWheel(t *testing.T, meta string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("six-1.17.0.dist-info/METADATA")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(w, meta)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func newState(t *testing.T, upstream string) *State {
	t.Helper()
	dir := t.TempDir()
	meta, err := store.OpenSQLite(dir + "/m.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	blobs, err := store.NewFileBlobStore(dir + "/blobs")
	if err != nil {
		t.Fatal(err)
	}
	return &State{Registry: &artifactkit.Registry{Blobs: blobs, Meta: meta,
		Upstreams: &artifactkit.Upstreams{Defaults: map[string]string{"pypi": upstream},
			Overrides: map[string]string{}, Proxy: map[string]string{}}}}
}

// TestMetadataFileFromCache verifies a cached wheel's internal METADATA is
// returned (byte-identical), so pip's data-dist-info-metadata hash matches.
func TestMetadataFileFromCache(t *testing.T) {
	meta := "Metadata-Version: 2.1\nName: six\nVersion: 1.17.0\n\nbody\n"
	wheel := buildWheel(t, meta)
	s := newState(t, "")
	stored, err := s.Registry.StoreAndHash(t.Context(), wheel)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Registry.Meta.Put(t.Context(), artifactkit.Artifact{
		Format: "pypi", Repository: "six", Version: "1.17.0",
		Blobs: []artifactkit.Descriptor{{Digest: stored.Digest, Size: stored.Size, Name: "six-1.17.0-py2.py3-none-any.whl"}},
	}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/pkgs/pypi/simple/six/six-1.17.0-py2.py3-none-any.whl.metadata", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != meta {
		t.Fatalf("metadata: %d %q", rec.Code, rec.Body.String())
	}
}

func TestVersionFromFilename(t *testing.T) {
	if got := versionFromFilename("six-1.17.0-py2.py3-none-any.whl", "six"); got != "1.17.0" {
		t.Fatalf("wheel version = %q", got)
	}
	if got := versionFromFilename("six-1.17.0.tar.gz", "six"); got != "1.17.0" {
		t.Fatalf("sdist version = %q", got)
	}
}

var _ = strings.TrimSpace
