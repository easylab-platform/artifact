package oci

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/easylab-platform/artifact/core"
)

// --- blob handlers ----------------------------------------------------------

func (a *Adapter) blob(w http.ResponseWriter, r *http.Request, name, digest string) {
	if _, err := artifactkit.ParseDigest(digest); err != nil {
		writeJSON(w, http.StatusBadRequest, ociError("DIGEST_INVALID", "invalid digest"))
		return
	}
	switch r.Method {
	case http.MethodHead:
		if !a.authorize(r, name, artifactkit.ActionPull) {
			a.challenge(w, name, artifactkit.ActionPull)
			return
		}
		a.checkBlob(w, r, digest)
	case http.MethodGet:
		if !a.authorize(r, name, artifactkit.ActionPull) {
			a.challenge(w, name, artifactkit.ActionPull)
			return
		}
		a.getBlob(w, r, name, digest)
	case http.MethodDelete:
		if !a.authorize(r, name, artifactkit.ActionDelete) {
			a.challenge(w, name, artifactkit.ActionDelete)
			return
		}
		if err := a.state.Registry.Blobs.Delete(r.Context(), digest); err != nil {
			writeJSON(w, http.StatusInternalServerError, ociError("UNKNOWN", err.Error()))
			return
		}
		w.WriteHeader(http.StatusAccepted)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (a *Adapter) checkBlob(w http.ResponseWriter, r *http.Request, digest string) {
	size, _ := a.state.Registry.Blobs.Stat(r.Context(), digest)
	if size == nil {
		writeJSON(w, http.StatusNotFound, ociError("BLOB_UNKNOWN", "blob unknown to registry"))
		return
	}
	w.Header().Set("Content-Length", strconv.FormatInt(*size, 10))
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(http.StatusOK)
}

func (a *Adapter) getBlob(w http.ResponseWriter, r *http.Request, name, digest string) {
	if !artifactkit.AuthorizeReadFor(w, r, a.state.Auth, a.state.Registry, "oci", name) {
		return
	}
	w.Header().Set("Docker-Content-Digest", digest)
	if artifactkit.ServeBlobAt(w, r, a.state.Registry.Blobs, r.Context(), digest, "application/octet-stream") {
		return
	}
	// Pull-through: stream from upstream, caching locally while verifying.
	registry := artifactkit.RegistryHostFrom(r.Context())
	up := a.upstreamForRegistry(registry)
	if up == nil {
		writeJSON(w, http.StatusNotFound, ociError("BLOB_UNKNOWN", "blob unknown to registry"))
		return
	}
	resp, _, err := up.GetBlob(name, digest)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, ociError("UPSTREAM_ERROR", err.Error()))
		return
	}
	defer func() { _ = resp.Body.Close() }()
	// Stream through a temp file: hash while copying, verify, then commit to
	// the blob store and serve FROM THE BLOB STORE (no full in-memory copy —
	// a multi-GB layer never exceeds one page cache).
	tmp, err := newTempFile()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ociError("UNKNOWN", err.Error()))
		return
	}
	defer func() { _ = tmp.Close() }() // removes the file
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp.f, h), resp.Body); err != nil {
		writeJSON(w, http.StatusBadGateway, ociError("UPSTREAM_ERROR", err.Error()))
		return
	}
	got := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if got != digest {
		// Mismatch: nothing is cached; the client gets a 502 so it can retry.
		writeJSON(w, http.StatusBadGateway, ociError("UPSTREAM_ERROR", "digest mismatch from upstream"))
		return
	}
	if err := tmp.f.Close(); err != nil {
		writeJSON(w, http.StatusInternalServerError, ociError("UNKNOWN", err.Error()))
		return
	}
	if _, err := a.state.Registry.Blobs.PutIfAbsent(r.Context(), digest, mustOpen(tmp.path)); err != nil {
		writeJSON(w, http.StatusInternalServerError, ociError("UNKNOWN", err.Error()))
		return
	}
	w.Header().Set("Docker-Content-Digest", digest)
	if artifactkit.ServeBlobAt(w, r, a.state.Registry.Blobs, r.Context(), digest, "application/octet-stream") {
		return
	}
	writeJSON(w, http.StatusInternalServerError, ociError("UNKNOWN", "verified blob vanished"))
}

// mustOpen opens a file, closing it on the caller's behalf through the
// returned reader only on success (errors return nil).
func mustOpen(path string) io.Reader {
	f, err := os.Open(path)
	if err != nil {
		return errReader{err}
	}
	return f
}

// errReader always errors (used to hand an open failure into PutIfAbsent).
type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }
