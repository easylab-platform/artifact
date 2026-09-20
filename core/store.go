package artifactkit

import (
	"context"
	"errors"
	"io"
	"log"
	"time"
)

// Digest is the canonical content-addressed identifier of a blob. The OCI
// form is "sha256:<hex>". The BlobStore is keyed by digest strings, so stems
// of implementations are free to use any algorithm.
const SHA256 = "sha256"

// BlobInfo is the metadata of one stored blob. ModTime lets the reaper skip
// blobs that may still be mid-write, so no separate ager capability is needed.
type BlobInfo struct {
	Digest  string
	Size    int64
	ModTime time.Time
}

// BlobStore abstracts the CAS for immutable artifact content. The default
// implementation is a filesystem store (one file per digest); an S3-compatible
// store is provided by the s3blob module.
//
// The store owns INTEGRITY: Put streams the body once, computes the full hash
// set in that pass, verifies it against the caller's expected digest, and
// persists the hashes. Callers therefore never hash a body themselves and never
// re-read a blob to learn its hashes (Hashes), which is what makes publish and
// checksum sidecars cheap.
type BlobStore interface {
	// Stat returns the blob's metadata, or (nil, nil) when absent.
	Stat(ctx context.Context, digest string) (*BlobInfo, error)
	// Open returns a reader (supporting Seek when possible), or (nil, nil).
	Open(ctx context.Context, digest string) (io.ReadSeekCloser, error)
	// Put streams r into the CAS, computing every hash in one pass. When
	// wantDigest is non-empty the content MUST hash to it (else an error);
	// when empty the computed digest is used. stored is false when the blob
	// already existed (dedup). The returned Stored carries the hashes, so a
	// caller never hashes the bytes again.
	Put(ctx context.Context, r io.Reader, wantDigest string) (Stored, bool, error)
	// Hashes returns the hash set recorded for a blob at write time. ok=false
	// when the store has no record (a blob written by an older tool).
	Hashes(ctx context.Context, digest string) (Hashes, bool, error)
	// Delete removes the blob.
	Delete(ctx context.Context, digest string) error
	// List returns every blob's metadata (digest, size, mtime).
	List(ctx context.Context) ([]BlobInfo, error)
}

// UploadRecord is a persisted in-progress multi-chunk upload session.
type UploadRecord struct {
	ID         string `json:"id"`
	Format     string `json:"format"`
	Repository string `json:"repository"`
	Digest     string `json:"digest,omitempty"`
	Bytes      int64  `json:"bytes"`
	Complete   bool   `json:"complete"`
}

// IndexStore abstracts the structured metadata layer (SQLite reference
// implementation). It holds the mutable index: repositories, versions, and
// the descriptors pointing at immutable blobs.
type IndexStore interface {
	// Put upserts an artifact's metadata by (format, repository, version).
	Put(ctx context.Context, a Artifact) error
	// Get fetches (format, repository, version) metadata.
	Get(ctx context.Context, format, repository, version string) (Artifact, error)
	// Delete removes one (format, repository, version).
	Delete(ctx context.Context, format, repository, version string) error
	// ListVersions returns all versions for a repository, ordered.
	ListVersions(ctx context.Context, format, repository string) ([]string, error)
	// ListArtifacts returns every artifact row for a repository in one pass
	// (rather than Get-per-version). Index generators use it so a repo with N
	// files costs one query, not N.
	ListArtifacts(ctx context.Context, format, repository string) ([]Artifact, error)
	// ListRepositoriesByFormat returns every repository that has ≥1 version.
	ListRepositoriesByFormat(ctx context.Context, format string) ([]string, error)
	// ListRepositories returns every repository across all formats.
	ListRepositories(ctx context.Context) ([]string, error)
	// ListPackages returns a compact summary for the catalog.
	ListPackages(ctx context.Context) ([]PackageSummary, error)
	// ReferencedDigests returns every CAS digest any artifact references
	// (primary Digest plus blob descriptors), so a caller can decide whether a
	// blob is still in use without an N+1 scan. One pass over the index.
	ReferencedDigests(ctx context.Context) (map[string]bool, error)
	// DeleteRepo removes an entire repository for a format.
	DeleteRepo(ctx context.Context, format, repository string) (int, error)
	// SaveUpload persists an upload session.
	SaveUpload(ctx context.Context, u UploadRecord) error
	// GetUpload returns a session by id.
	GetUpload(ctx context.Context, id string) (UploadRecord, error)
	// DeleteUpload removes a session.
	DeleteUpload(ctx context.Context, id string) error
	// ListUploads returns session ids.
	ListUploads(ctx context.Context) ([]string, error)
	// GetMeta returns a per-repository metadata blob (e.g. a resolved index).
	GetMeta(ctx context.Context, format, repository string) ([]byte, error)
	// SetMeta stores a per-repository metadata blob.
	SetMeta(ctx context.Context, format, repository string, data []byte) error
	// Close releases the underlying resources.
	Close() error
}

// Sentinels for mapping store errors onto protocol responses.
var (
	ErrArtifactUnknown = errors.New("artifact unknown")
	ErrBlobUnknown     = errors.New("blob unknown")
	ErrUploadUnknown   = errors.New("upload unknown")
)

// IsUnknown reports whether a store error is a plain "not found".
func IsUnknown(err error) bool {
	return errors.Is(err, ErrArtifactUnknown) ||
		errors.Is(err, ErrBlobUnknown) ||
		errors.Is(err, ErrUploadUnknown)
}

// LogMetaErr is used by adapter helper flows (storeVersion / removeVersion)
// where an index write failure must not abort a proxied download but must be
// visible in the server log. Returns the error for callers that CAN act.
func LogMetaErr(op string, err error) {
	if err != nil {
		log.Printf("artifactkit: %s: %v", op, err)
	}
}
