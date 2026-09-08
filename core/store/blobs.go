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
// (default; a filesystem CAS rooted at dir) is implemented. Requesting "s3"
// is a configuration error and fails startup loudly — a silent fallback to
// the local filesystem would put multi-GB blobs on the wrong storage and
// surprise an operator at disk-full time. Metadata is never stored inline as
// blobs.
func OpenBlobStore(backend, dir string) (artifactkit.BlobStore, error) {
	switch backend {
	case BlobFilesystem, "":
		return NewFileBlobStore(dir)
	case BlobS3:
		return nil, fmt.Errorf("blob backend %q is not implemented yet (use filesystem)", backend)
	default:
		return nil, fmt.Errorf("unsupported blob backend %q (filesystem|s3)", backend)
	}
}

// EnsureDir creates a directory (and parents) if it does not exist.
func EnsureDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}
