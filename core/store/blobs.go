package store

import (
	"fmt"
	"os"

	artifactkit "github.com/easylab-platform/artifact/core"
)

const (
	BlobFilesystem = "filesystem"
	BlobS3         = "s3"
)

// OpenBlobStore returns a BlobStore for the given backend. Only "filesystem"
// (default; a filesystem CAS rooted at dir) is fully implemented; "s3" is a
// placeholder that falls back to the filesystem CAS so a misconfigured
// deployment never fails to start. Metadata is never stored inline as blobs.
func OpenBlobStore(backend, dir string) (artifactkit.BlobStore, error) {
	switch backend {
	case BlobFilesystem, "":
		return NewFileBlobStore(dir)
	case BlobS3:
		// Placeholder: S3/MinIO blob backend is not implemented yet. Fall back
		// to the filesystem CAS so startup and reads still work; the mismatch is
		// surfaced via a clear log in the caller.
		return NewFileBlobStore(dir)
	default:
		return nil, fmt.Errorf("unsupported blob backend %q (filesystem|s3)", backend)
	}
}

// EnsureDir creates a directory (and parents) if it does not exist.
func EnsureDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}
