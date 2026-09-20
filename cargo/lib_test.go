package cargo

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	artifactkit "github.com/easylab-platform/artifact/core"
	"github.com/easylab-platform/artifact/core/store"
)

func newState(t *testing.T) *State {
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
		Upstreams: &artifactkit.Upstreams{Defaults: map[string]string{}, Overrides: map[string]string{}, Proxy: map[string]string{}}}}
}

// publishBody builds cargo's publish framing: u32 json len, json, u32 crate
// len, crate bytes.
func publishBody(t *testing.T, name, version string, crate []byte) []byte {
	t.Helper()
	j, _ := json.Marshal(map[string]any{"name": name, "vers": version})
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(j)))
	buf.Write(j)
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(crate)))
	buf.Write(crate)
	return buf.Bytes()
}

// TestPublishThenDownload verifies a published crate is served byte-identically.
func TestPublishThenDownload(t *testing.T) {
	s := newState(t)
	crate := []byte("crate-tarball-bytes")

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/artifacts/cargo/api/v1/crates/new", bytes.NewReader(publishBody(t, "anyhow", "1.0.0", crate))))
	if rec.Code != http.StatusCreated {
		t.Fatalf("publish = %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/artifacts/cargo/api/v1/crates/anyhow/1.0.0/download", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("download = %d", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), crate) {
		t.Fatal("downloaded crate differs from the published bytes")
	}
}
