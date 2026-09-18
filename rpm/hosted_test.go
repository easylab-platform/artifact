package rpm

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

// TestHostedRepodata drives a self-published .rpm: upload, serve repomd.xml,
// follow its <location> to the primary metadata, and download the package.
func TestHostedRepodata(t *testing.T) {
	s := newHostedState(t)
	rpmBytes := []byte("fake-rpm-content")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/artifacts/rpm/fedora41/mypkg-1.0-1.x86_64.rpm", bytes.NewReader(rpmBytes))
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}

	// repomd.xml.
	rec2 := httptest.NewRecorder()
	s.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/artifacts/rpm/fedora41/repodata/repomd.xml", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("repomd: %d", rec2.Code)
	}
	repomd := rec2.Body.String()
	if !strings.Contains(repomd, `<data type="primary">`) {
		t.Fatalf("repomd:\n%s", repomd)
	}
	// Extract the referenced primary location.
	i := strings.Index(repomd, `<location href="`)
	if i < 0 {
		t.Fatal("no location in repomd")
	}
	rest := repomd[i+len(`<location href="`):]
	loc := rest[:strings.Index(rest, `"`)]
	if !strings.HasPrefix(loc, "repodata/") || !strings.HasSuffix(loc, "-primary.xml.gz") {
		t.Fatalf("location = %q", loc)
	}

	// Fetch the primary referenced by repomd (must be regenerable).
	rec3 := httptest.NewRecorder()
	s.ServeHTTP(rec3, httptest.NewRequest(http.MethodGet, "/artifacts/rpm/fedora41/"+loc, nil))
	if rec3.Code != http.StatusOK {
		t.Fatalf("primary: %d %s", rec3.Code, rec3.Body.String())
	}
	if rec3.Header().Get("Content-Type") != "application/gzip" {
		t.Fatalf("primary content-type = %q", rec3.Header().Get("Content-Type"))
	}

	// Package download.
	rec4 := httptest.NewRecorder()
	s.ServeHTTP(rec4, httptest.NewRequest(http.MethodGet, "/artifacts/rpm/fedora41/mypkg-1.0-1.x86_64.rpm", nil))
	if rec4.Code != http.StatusOK || !bytes.Equal(rec4.Body.Bytes(), rpmBytes) {
		t.Fatalf("pkg download: %d", rec4.Code)
	}
}

// TestNvrArch verifies filename NVR parsing.
func TestNvrArch(t *testing.T) {
	n, v, r, a := nvrArch("mypkg-1.2.3-4.el9.x86_64.rpm")
	if n != "mypkg" || v != "1.2.3" || r != "4.el9" || a != "x86_64" {
		t.Fatalf("got %q %q %q %q", n, v, r, a)
	}
}
