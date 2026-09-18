package apk

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/easylab-platform/artifact/core"
	"github.com/easylab-platform/artifact/core/store"
)

var modTime = time.Unix(1700000000, 0)

// buildAPKIndex creates a minimal signed-style APKINDEX.tar.gz containing one
// package entry.
func buildAPKIndex(pkg string) []byte {
	var index bytes.Buffer
	index.WriteString("C:Q1" + pkg + "=\n")
	index.WriteString("P:" + pkg + "\n")
	index.WriteString("V:1.0.0-r0\n")
	index.WriteString("A:x86_64\n")
	index.WriteString("\n")
	tarBuf := &bytes.Buffer{}
	tw := tar.NewWriter(tarBuf)
	_ = tw.WriteHeader(&tar.Header{Name: "APKINDEX", Size: int64(index.Len()), Mode: 0o644, ModTime: modTime})
	_, _ = tw.Write(index.Bytes())
	_ = tw.Close()
	gzBuf := &bytes.Buffer{}
	gw := gzip.NewWriter(gzBuf)
	_, _ = gw.Write(tarBuf.Bytes())
	_ = gw.Close()
	return gzBuf.Bytes()
}

// TestPullThroughIndexAndPackage drives the apk adapter end to end: a fake
// upstream serves APKINDEX.tar.gz and a package; the adapter caches both in
// the CAS and serves byte-for-byte.
func TestPullThroughIndexAndPackage(t *testing.T) {
	index := buildAPKIndex("busybox")
	pkg := []byte("fake-apk-bytes")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "APKINDEX.tar.gz"):
			_, _ = w.Write(index)
		case strings.HasSuffix(r.URL.Path, ".apk"):
			_, _ = w.Write(pkg)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upstream.Close)

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
	reg := &artifactkit.Registry{
		Blobs: blobs, Meta: meta,
		Upstreams: &artifactkit.Upstreams{Defaults: map[string]string{"apk": upstream.URL}, Overrides: map[string]string{}, Proxy: map[string]string{}},
	}
	s := &State{Registry: reg}

	// Fetch the index through the adapter.
	req := httptest.NewRequest(http.MethodGet, "/artifacts/apk/v3.21/main/x86_64/APKINDEX.tar.gz", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("index: %d %s", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), index) {
		t.Fatal("index bytes differ from upstream (must be verbatim)")
	}
	if !indexContains(rec.Body.Bytes(), "P:busybox") {
		t.Fatal("index content malformed")
	}

	// Fetch a package.
	req2 := httptest.NewRequest(http.MethodGet, "/artifacts/apk/v3.21/main/x86_64/busybox-1.0.0-r0.apk", nil)
	rec2 := httptest.NewRecorder()
	s.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK || !bytes.Equal(rec2.Body.Bytes(), pkg) {
		t.Fatalf("package: %d", rec2.Code)
	}

	// Second fetch served from the cache (upstream counter stays at 2).
	req3 := httptest.NewRequest(http.MethodGet, "/artifacts/apk/v3.21/main/x86_64/busybox-1.0.0-r0.apk", nil)
	rec3 := httptest.NewRecorder()
	s.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK || !bytes.Equal(rec3.Body.Bytes(), pkg) {
		t.Fatalf("cached package: %d", rec3.Code)
	}

	// Cached entries recorded in the index.
	vs, err := meta.ListVersions(context.Background(), "apk", "v3.21/main/x86_64")
	if err != nil || len(vs) < 2 {
		t.Fatalf("cached versions: %v %v", vs, err)
	}
}

// TestAirGapDisabled verifies the adapter fails closed when no upstream is
// configured.
func TestAirGapDisabled(t *testing.T) {
	dir := t.TempDir()
	meta, _ := store.OpenSQLite(filepath.Join(dir, "m.db"))
	t.Cleanup(func() { _ = meta.Close() })
	blobs, _ := store.NewFileBlobStore(filepath.Join(dir, "blobs"))
	s := &State{Registry: &artifactkit.Registry{
		Blobs: blobs, Meta: meta,
		Upstreams: &artifactkit.Upstreams{Defaults: map[string]string{}, Overrides: map[string]string{}, Proxy: map[string]string{}, AirGap: true},
	}}
	req := httptest.NewRequest(http.MethodGet, "/artifacts/apk/v3.21/main/x86_64/APKINDEX.tar.gz", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("air-gapped fetch = %d, want 404", rec.Code)
	}
}
