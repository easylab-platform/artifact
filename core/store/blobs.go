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

// OpenBlobStore returns a BlobStore for the given backend. "filesystem"
// (default; a filesystem CAS rooted at dir) is built in. Other backends (e.g.
// "s3") register a constructor via RegisterBlobBackend from their own module,
// so this package never imports a heavy cloud SDK and the adapter module graph
// stays lean. Requesting an unregistered backend fails startup loudly — a
// silent fallback would put multi-GB blobs on the wrong storage.
func OpenBlobStore(backend, dir string) (artifactkit.BlobStore, error) {
	switch backend {
	case BlobFilesystem, "":
		return NewFileBlobStore(dir)
	}
	if build, ok := blobBackends[backend]; ok {
		return build(dir)
	}
	return nil, fmt.Errorf("unsupported blob backend %q (filesystem, or a registered one)", backend)
}

// blobBackends holds registered non-filesystem backends (process-global).
var blobBackends = map[string]func(dir string) (artifactkit.BlobStore, error){}

// RegisterBlobBackend installs a constructor for a backend name. A separate
// module (e.g. s3blob) calls this from its init() so consumers enable it with a
// blank import; OpenBlobStore then resolves it by name.
func RegisterBlobBackend(name string, build func(dir string) (artifactkit.BlobStore, error)) {
	if name == "" || build == nil {
		return
	}
	blobBackends[name] = build
}

// BlobBackends lists the registered backend names (filesystem plus any).
func BlobBackends() []string {
	out := []string{BlobFilesystem}
	for n := range blobBackends {
		out = append(out, n)
	}
	return out
}

// EnsureDir creates a directory (and parents) if it does not exist.
func EnsureDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}
