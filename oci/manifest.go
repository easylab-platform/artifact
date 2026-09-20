// Package oci implements the OCI Distribution Spec v1.1 registry protocol as
// a pull-through mirror. It mounts the spec-fixed /v2 routes and, on a local
// miss, fetches manifests/blobs from an upstream registry (default Docker
// Hub), caches them, and streams to the client.
//
// The adapter depends only on *artifactkit.Registry, so an embedder can wire any
// BlobStore / IndexStore / Upstreams implementation.
package oci

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/easylab-platform/artifact/core"
)

// Accept is the media-types advertised for manifest GET (OCI 1.1).
const Accept = "application/vnd.oci.image.manifest.v1+json," +
	"application/vnd.docker.distribution.manifest.v2+json," +
	"application/vnd.docker.distribution.manifest.list.v2+json," +
	"application/vnd.oci.image.index.v1+json," +
	"application/vnd.oci.artifact.manifest.v1+json"

// ParseDigest validates "algo:hex" and returns the lowercase hex portion.
// sha256/sha512 only.
func ParseDigest(d string) (string, error) { return artifactkit.ParseDigest(d) }

// ReferenceIsDigest reports whether a tag-or-digest reference is a digest.
func ReferenceIsDigest(ref string) bool { return strings.Contains(ref, ":") }

// splitRegistry splits an OCI repository name into (explicit-registry,
// repository). A first component that looks like a registry host (contains a
// dot or colon, or equals "localhost") is stripped so "ghcr.io/foo/bar" and
// "foo/bar" share the same local repository.
func splitRegistry(name string) (string, string) {
	i := strings.Index(name, "/")
	if i < 0 {
		return "", name
	}
	first := name[:i]
	if strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost" {
		return first, name[i+1:]
	}
	return "", name
}

// extractBlobs returns the blob digests referenced by a manifest or index
// body (config + layers, or nested manifests for an index).
func extractBlobs(body []byte) []artifactkit.Descriptor {
	var probe struct {
		MediaType string `json:"mediaType"`
	}
	_ = json.Unmarshal(body, &probe)

	if probe.MediaType == "application/vnd.oci.image.index.v1+json" ||
		probe.MediaType == "application/vnd.docker.distribution.manifest.list.v2+json" {
		var idx struct {
			Manifests []struct {
				Digest    string `json:"digest"`
				MediaType string `json:"mediaType"`
				Size      int64  `json:"size"`
			} `json:"manifests"`
		}
		if json.Unmarshal(body, &idx) != nil {
			return nil
		}
		var out []artifactkit.Descriptor
		for _, m := range idx.Manifests {
			out = append(out, artifactkit.Descriptor{Digest: m.Digest, MediaType: m.MediaType, Size: m.Size})
		}
		return out
	}

	var m struct {
		Config struct {
			Digest    string `json:"digest"`
			MediaType string `json:"mediaType"`
			Size      int64  `json:"size"`
		} `json:"config"`
		Layers []struct {
			Digest    string `json:"digest"`
			MediaType string `json:"mediaType"`
			Size      int64  `json:"size"`
		} `json:"layers"`
	}
	if json.Unmarshal(body, &m) != nil {
		return nil
	}
	var out []artifactkit.Descriptor
	if m.Config.Digest != "" {
		out = append(out, artifactkit.Descriptor{Digest: m.Config.Digest, MediaType: m.Config.MediaType, Size: m.Config.Size})
	}
	for _, l := range m.Layers {
		out = append(out, artifactkit.Descriptor{Digest: l.Digest, MediaType: l.MediaType, Size: l.Size})
	}
	return out
}

// manifestSubject returns the subject digest of an OCI 1.1 referrer body.
func manifestSubject(body []byte) string {
	var probe struct {
		Subject struct {
			Digest string `json:"digest"`
		} `json:"subject"`
	}
	if json.Unmarshal(body, &probe) != nil {
		return ""
	}
	return probe.Subject.Digest
}

// manifestArtifactType returns artifactType, falling back to config mediaType.
func manifestArtifactType(body []byte) string {
	var probe struct {
		ArtifactType string `json:"artifactType"`
		Config       struct {
			MediaType string `json:"mediaType"`
		} `json:"config"`
	}
	if json.Unmarshal(body, &probe) != nil {
		return ""
	}
	if probe.ArtifactType != "" {
		return probe.ArtifactType
	}
	return probe.Config.MediaType
}

// --- manifest handlers ------------------------------------------------------

func (a *Adapter) manifest(w http.ResponseWriter, r *http.Request, name, ref string) {
	if strings.Contains(ref, ":") {
		if _, err := artifactkit.ParseDigest(ref); err != nil {
			writeJSON(w, http.StatusBadRequest, ociError("DIGEST_INVALID", "invalid digest reference"))
			return
		}
	}

	switch r.Method {
	case http.MethodHead:
		if !artifactkit.AuthorizeReadFor(w, r, a.state.Auth, a.state.Registry, "oci", name) {
			return
		}
		a.getManifest(w, r, name, ref, false)
	case http.MethodGet:
		if !artifactkit.AuthorizeReadFor(w, r, a.state.Auth, a.state.Registry, "oci", name) {
			return
		}
		a.getManifest(w, r, name, ref, true)
	case http.MethodPut:
		if !a.authorize(r, name, artifactkit.ActionPush) {
			a.challenge(w, name, artifactkit.ActionPush)
			return
		}
		a.putManifest(w, r, name, ref)
	case http.MethodDelete:
		if !a.authorize(r, name, artifactkit.ActionDelete) {
			a.challenge(w, name, artifactkit.ActionDelete)
			return
		}
		// Deleting a manifest removes the addressed revision. A delete by
		// digest (what `skopeo delete`/`regctl` send after resolving a tag)
		// must also drop any tag pointing at that same manifest, or the tag
		// keeps resolving through its alias row.
		delVersions := []string{ref}
		if art, err := a.state.Registry.Meta.Get(r.Context(), "oci", name, ref); err == nil && art.Digest != "" {
			if vs, err := a.state.Registry.Meta.ListVersions(r.Context(), "oci", name); err == nil {
				for _, v := range vs {
					if v == ref {
						continue
					}
					if other, err := a.state.Registry.Meta.Get(r.Context(), "oci", name, v); err == nil && other.Digest == art.Digest {
						delVersions = append(delVersions, v)
					}
				}
			}
		}
		for _, v := range delVersions {
			if err := a.state.Registry.Meta.Delete(r.Context(), "oci", name, v); err != nil && !artifactkit.IsUnknown(err) {
				writeJSON(w, http.StatusInternalServerError, ociError("UNKNOWN", err.Error()))
				return
			}
		}
		w.WriteHeader(http.StatusAccepted)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (a *Adapter) getManifest(w http.ResponseWriter, r *http.Request, name, ref string, body bool) {
	registry := artifactkit.RegistryHostFrom(r.Context())
	// A manifest addressed by digest names immutable content: cache it forever.
	// A tag may move, so a cached tag row expires and is re-resolved (the
	// origin's mutable-tag semantics; a PUSHed manifest is stored immutable and
	// never expires). Without this, `docker pull img:latest` would serve the
	// first-seen manifest indefinitely.
	byDigest := strings.Contains(ref, ":")
	if art, err := a.state.Registry.Meta.Get(r.Context(), "oci", name, ref); err == nil &&
		len(art.Proprietary) > 0 && (byDigest || artifactkit.Fresh(art, time.Now())) {
		dgst := art.Digest
		if dgst == "" {
			dgst = "sha256:" + hexDigest(art.Proprietary)
		}
		writeManifest(w, art.Proprietary, art.MediaType, dgst, body)
		return
	}
	// Pull-through.
	up := a.upstreamForRegistry(registry)
	if up != nil {
		if mbody, ct, err := up.GetManifest(name, ref); err == nil {
			dgst := "sha256:" + hexDigest(mbody)
			blobs := extractBlobs(mbody)
			// Store the digest row as immutable (it can never change) and the
			// tag row with a TTL (the tag may be re-pushed upstream).
			dgRow := artifactkit.Artifact{
				Format: "oci", Repository: name, Version: dgst,
				MediaType: ct, Proprietary: mbody, Digest: dgst, Blobs: blobs,
				Source: "pull", CacheControl: artifactkit.ImmutableTag,
			}
			artifactkit.LogMetaErr("oci pull cache", a.state.Registry.Meta.Put(r.Context(), dgRow))
			if !byDigest {
				tagRow := dgRow
				tagRow.Version = ref
				tagRow.CacheControl = ""
				tagRow.ExpiresAt = time.Now().Add(defaultManifestTTL)
				artifactkit.LogMetaErr("oci pull cache", a.state.Registry.Meta.Put(r.Context(), tagRow))
			}
			writeManifest(w, mbody, ct, dgst, body)
			return
		}
	}
	writeJSON(w, http.StatusNotFound, ociError("MANIFEST_UNKNOWN", "manifest unknown"))
}

func (a *Adapter) putManifest(w http.ResponseWriter, r *http.Request, name, ref string) {
	artifactkit.LimitBody(w, r)
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		if artifactkit.IsBodyTooLarge(err) {
			writeJSON(w, http.StatusRequestEntityTooLarge, ociError("MANIFEST_INVALID", "manifest too large"))
			return
		}
		writeJSON(w, http.StatusBadRequest, ociError("MANIFEST_INVALID", "error reading manifest"))
		return
	}
	mt := r.Header.Get("Content-Type")
	if mt == "" {
		mt = "application/vnd.oci.image.manifest.v1+json"
	}
	sha := "sha256:" + hexDigest(body)
	eff := sha
	if strings.Contains(ref, ":") {
		if _, err := artifactkit.ParseDigest(ref); err != nil {
			writeJSON(w, http.StatusBadRequest, ociError("DIGEST_INVALID", "invalid digest"))
			return
		}
		if !strings.HasPrefix(ref, "sha256:") {
			writeJSON(w, http.StatusBadRequest, ociError("DIGEST_INVALID", "unsupported digest algorithm"))
			return
		}
		if eff != ref {
			writeJSON(w, http.StatusBadRequest, ociError("MANIFEST_INVALID", "manifest digest does not match content"))
			return
		}
	}
	blobs := extractBlobs(body)
	art := artifactkit.Artifact{Format: "oci", Repository: name, Version: ref,
		MediaType: mt, Proprietary: body, Digest: eff, Blobs: blobs, Source: "push"}
	if err := a.state.Registry.Meta.Put(r.Context(), art); err != nil {
		writeJSON(w, http.StatusInternalServerError, ociError("UNKNOWN", err.Error()))
		return
	}
	if eff != ref {
		dg := art
		dg.Version = eff
		artifactkit.LogMetaErr("oci alias put", a.state.Registry.Meta.Put(r.Context(), dg))
	}
	// OCI 1.1 ?tag= values.
	if q := r.URL.Query().Get("tag"); q != "" {
		tagArt := art
		tagArt.Version = q
		artifactkit.LogMetaErr("oci tag put", a.state.Registry.Meta.Put(r.Context(), tagArt))
	}
	w.Header().Set("Docker-Content-Digest", eff)
	w.Header().Set("Location", "/v2/"+name+"/manifests/"+eff)
	w.WriteHeader(http.StatusCreated)
}
