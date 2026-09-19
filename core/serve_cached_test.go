package artifactkit_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/easylab-platform/artifact/core"
)

// TestServeCachedBlobReplaysHeaders verifies Content-Encoding/ETag/Last-Modified
// stored on an artifact are replayed to the client (a transparently-gzipped
// body must carry its coding or the client fails to decode).
func TestServeCachedBlobReplaysHeaders(t *testing.T) {
	reg := newCacheRegistry(t)
	body := "compressed-bytes"
	h := sha256.Sum256([]byte(body))
	digest := "sha256:" + hex.EncodeToString(h[:])
	if _, err := reg.Blobs.PutIfAbsent(context.Background(), digest, strings.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	art := artifactkit.Artifact{
		Digest: digest, MediaType: "application/json",
		ContentEncoding: "gzip", ETag: `"e1"`, LastModified: "Mon, 02 Jan 2006 15:04:05 GMT",
	}
	rec := httptest.NewRecorder()
	ok := artifactkit.ServeCachedBlob(rec, httptest.NewRequest(http.MethodGet, "/x", nil), reg.Blobs, context.Background(), art, "", "")
	if !ok {
		t.Fatal("serve returned false")
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Errorf("Content-Encoding = %q, want gzip", got)
	}
	if got := rec.Header().Get("ETag"); got != `"e1"` {
		t.Errorf("ETag = %q", got)
	}
	if got := rec.Header().Get("Last-Modified"); got == "" {
		t.Error("Last-Modified not replayed")
	}
	if rec.Body.String() != body {
		t.Errorf("body = %q", rec.Body.String())
	}
}
