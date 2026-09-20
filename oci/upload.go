package oci

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"

	"github.com/easylab-platform/artifact/core"
)

// --- blob upload session handlers -------------------------------------------

func (a *Adapter) upload(w http.ResponseWriter, r *http.Request, name, session string) {
	if !a.authorize(r, name, artifactkit.ActionPush) {
		a.challenge(w, name, artifactkit.ActionPush)
		return
	}
	switch r.Method {
	case http.MethodPost:
		// Mount / single-shot / start session.
		if mnt := r.URL.Query().Get("mount"); mnt != "" {
			if size, _ := a.state.Registry.Blobs.Stat(r.Context(), mnt); size != nil {
				w.Header().Set("Location", "/v2/"+name+"/blobs/"+mnt)
				w.Header().Set("Docker-Content-Digest", mnt)
				w.WriteHeader(http.StatusCreated)
				return
			}
		}
		if dgst := r.URL.Query().Get("digest"); dgst != "" {
			if _, err := artifactkit.ParseDigest(dgst); err != nil {
				writeJSON(w, http.StatusBadRequest, ociError("DIGEST_INVALID", "invalid digest"))
				return
			}
			artifactkit.LimitBody(w, r)
			// Stream the body into the CAS (no full in-memory copy) and verify
			// the client's digest against the one we computed.
			stored, err := a.state.Registry.StoreStream(r.Context(), r.Body)
			if err != nil {
				if artifactkit.IsBodyTooLarge(err) {
					writeJSON(w, http.StatusRequestEntityTooLarge, ociError("BLOB_UPLOAD_INVALID", "blob too large"))
					return
				}
				writeJSON(w, http.StatusBadRequest, ociError("DIGEST_INVALID", "read error"))
				return
			}
			if dgst != stored.Digest {
				// The body was streamed under its true digest; the client
				// named a different one, so drop the bytes we just stored.
				artifactkit.LogMetaErr("oci reject blob", a.state.Registry.Blobs.Delete(r.Context(), stored.Digest))
				writeJSON(w, http.StatusBadRequest, ociError("DIGEST_INVALID", "digest does not match content"))
				return
			}
			w.Header().Set("Location", "/v2/"+name+"/blobs/"+dgst)
			w.Header().Set("Docker-Content-Digest", dgst)
			w.WriteHeader(http.StatusCreated)
			return
		}
		// Start a session.
		sid := a.nextSessionID()
		if sid == "" {
			writeJSON(w, http.StatusInternalServerError, ociError("UNKNOWN", "session id generation failed"))
			return
		}
		sessID := "sess-" + sid
		if err := a.state.Registry.Meta.SaveUpload(r.Context(), artifactkit.UploadRecord{ID: sessID, Format: "oci", Repository: name}); err != nil {
			writeJSON(w, http.StatusInternalServerError, ociError("UNKNOWN", err.Error()))
			return
		}
		w.Header().Set("Location", "/v2/"+name+"/blobs/uploads/"+sessID)
		w.Header().Set("Docker-Upload-UUID", sessID)
		w.WriteHeader(http.StatusAccepted)
	case http.MethodGet:
		u, err := a.state.Registry.Meta.GetUpload(r.Context(), session)
		if err != nil {
			writeJSON(w, http.StatusNotFound, ociError("BLOB_UPLOAD_UNKNOWN", "blob upload unknown"))
			return
		}
		// The part file is authoritative for the resumable offset.
		w.Header().Set("Docker-Upload-UUID", u.ID)
		w.Header().Set("Range", fmt.Sprintf("0-%d", max64(a.uploads.size(session)-1, 0)))
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPatch:
		// Append the chunk to the on-disk session file.
		u, ok := a.beginUpload(w, r, name, session)
		if !ok {
			return
		}
		artifactkit.LimitBody(w, r)
		total, err := a.uploads.append(r.Context(), session, r)
		if err != nil {
			if artifactkit.IsBodyTooLarge(err) {
				writeJSON(w, http.StatusRequestEntityTooLarge, ociError("BLOB_UPLOAD_INVALID", "chunk too large"))
				return
			}
			writeJSON(w, http.StatusInternalServerError, ociError("UNKNOWN", err.Error()))
			return
		}
		a.saveUploadSize(r.Context(), u, total)
		w.Header().Set("Docker-Upload-UUID", session)
		w.Header().Set("Range", fmt.Sprintf("0-%d", max64(total-1, 0)))
		w.Header().Set("Location", "/v2/"+name+"/blobs/uploads/"+session)
		w.WriteHeader(http.StatusAccepted)
	case http.MethodPut:
		// Final chunk (may carry the remainder of the body) + digest commit.
		u, ok := a.beginUpload(w, r, name, session)
		if !ok {
			return
		}
		artifactkit.LimitBody(w, r)
		total, err := a.uploads.append(r.Context(), session, r)
		if err != nil {
			if artifactkit.IsBodyTooLarge(err) {
				writeJSON(w, http.StatusRequestEntityTooLarge, ociError("BLOB_UPLOAD_INVALID", "final chunk too large"))
				return
			}
			writeJSON(w, http.StatusInternalServerError, ociError("UNKNOWN", err.Error()))
			return
		}
		a.saveUploadSize(r.Context(), u, total)
		dgst := r.URL.Query().Get("digest")
		if dgst == "" {
			writeJSON(w, http.StatusBadRequest, ociError("DIGEST_INVALID", "digest parameter missing"))
			return
		}
		if _, err := artifactkit.ParseDigest(dgst); err != nil {
			writeJSON(w, http.StatusBadRequest, ociError("DIGEST_INVALID", "invalid digest"))
			return
		}
		if err := a.verifySessionDigest(session, dgst); err != nil {
			writeJSON(w, http.StatusBadRequest, ociError("DIGEST_INVALID", "digest does not match content"))
			return
		}
		// Stream the part file into the blob store (no full in-memory copy).
		if err := a.uploads.commit(r.Context(), session, dgst, a.state.Registry.Blobs); err != nil {
			writeJSON(w, http.StatusInternalServerError, ociError("UNKNOWN", err.Error()))
			return
		}
		_ = a.state.Registry.Meta.DeleteUpload(r.Context(), session)
		w.Header().Set("Location", "/v2/"+name+"/blobs/"+dgst)
		w.Header().Set("Docker-Content-Digest", dgst)
		w.WriteHeader(http.StatusCreated)
	case http.MethodDelete:
		a.uploads.remove(session)
		_ = a.state.Registry.Meta.DeleteUpload(r.Context(), session)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// beginUpload validates that the session exists AND belongs to the named
// repository (a session id leaked to another client cannot be cross-mounted),
// writing the appropriate error response when invalid.
func (a *Adapter) beginUpload(w http.ResponseWriter, r *http.Request, name, session string) (artifactkit.UploadRecord, bool) {
	u, err := a.state.Registry.Meta.GetUpload(r.Context(), session)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ociError("BLOB_UPLOAD_UNKNOWN", "blob upload unknown"))
		return artifactkit.UploadRecord{}, false
	}
	if u.Repository != name || u.Format != "oci" {
		writeJSON(w, http.StatusBadRequest, ociError("BLOB_UPLOAD_INVALID", "upload session belongs to a different repository"))
		return artifactkit.UploadRecord{}, false
	}
	return u, true
}

// saveUploadSize persists the running byte count for resume reporting.
func (a *Adapter) saveUploadSize(ctx context.Context, u artifactkit.UploadRecord, total int64) {
	u.Bytes = total
	_ = a.state.Registry.Meta.SaveUpload(ctx, u)
}

// verifySessionDigest hashes the session part file and compares it with the
// client-declared digest without buffering the whole layer.
func (a *Adapter) verifySessionDigest(session, digest string) error {
	return a.uploads.verify(session, digest)
}

func (a *Adapter) listReferrers(w http.ResponseWriter, r *http.Request, name, subject string) {
	if !a.authorize(r, name, artifactkit.ActionPull) {
		a.challenge(w, name, artifactkit.ActionPull)
		return
	}
	filter := r.URL.Query().Get("artifactType")
	var manifestList []map[string]any
	versions, _ := a.state.Registry.Meta.ListVersions(r.Context(), "oci", name)
	for _, v := range versions {
		if _, err := artifactkit.ParseDigest(v); err != nil {
			continue
		}
		art, err := a.state.Registry.Meta.Get(r.Context(), "oci", name, v)
		if err != nil {
			continue
		}
		at := manifestArtifactType(art.Proprietary)
		if at == "" || manifestSubject(art.Proprietary) != subject {
			continue
		}
		if filter != "" && at != filter {
			continue
		}
		manifestList = append(manifestList, map[string]any{
			"mediaType": art.MediaType, "digest": art.Digest, "size": len(art.Proprietary),
			"artifactType": at, "annotations": map[string]any{},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json",
		"manifests": manifestList,
	})
}

// nextSessionID returns a unique upload-session id (crypto/rand). A
// generation failure returns "" which the caller treats as a 500.
func (a *Adapter) nextSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}
