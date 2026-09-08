package oci

import (
	"os"
)

// tmpFile is a temp file that is always removed when closed.
type tmpFile struct {
	path string
	f    *os.File
}

func newTempFile() (*tmpFile, error) {
	f, err := os.CreateTemp("", "artifact-oci-cache-*")
	if err != nil {
		return nil, err
	}
	return &tmpFile{path: f.Name(), f: f}, nil
}

// Close closes and removes the file (best-effort removal).
func (t *tmpFile) Close() error {
	if t.f != nil {
		_ = t.f.Close()
	}
	if t.path != "" {
		_ = os.Remove(t.path)
	}
	return nil
}
