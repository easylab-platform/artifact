package debian

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/easylab-platform/artifact/core"
)

// GeneratePackages renders a flat-repo "Packages" index from hosted .deb
// files. apt's flat format needs, per package: Package, Version,
// Architecture, Filename, Size, and a checksum line. We extract the control
// fields by reading the .deb's control member (ar → control.tar.* →
// ./control). When extraction fails, a minimal stanza keyed by filename is
// emitted so the repo is still usable with [trusted=yes].
//
// This index is UNSIGNED: the source line must carry [trusted=yes] (dev
// deployments). Pull-through packages keep upstream signatures untouched.
func GeneratePackages(store artifactkit.HostedStore) artifactkit.Generator {
	return func(ctx context.Context, files []artifactkit.HostedFile) (map[string]artifactkit.GeneratedFile, error) {
		var b strings.Builder
		sorted := make([]artifactkit.HostedFile, len(files))
		copy(sorted, files)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
		for _, f := range sorted {
			if !strings.HasSuffix(f.Name, ".deb") {
				continue
			}
			ctrl, err := debControlFields(ctx, store, f)
			if err != nil || ctrl["Package"] == "" {
				// Minimal stanza: filename-derived name/version.
				name, ver := nameVersionFromDeb(f.Name)
				ctrl = map[string]string{"Package": name, "Version": ver, "Architecture": "amd64"}
			}
			b.WriteString("Package: " + ctrl["Package"] + "\n")
			b.WriteString("Version: " + ctrl["Version"] + "\n")
			if a := ctrl["Architecture"]; a != "" {
				b.WriteString("Architecture: " + a + "\n")
			}
			if d := ctrl["Depends"]; d != "" {
				b.WriteString("Depends: " + d + "\n")
			}
			if d := ctrl["Description"]; d != "" {
				b.WriteString("Description: " + strings.ReplaceAll(d, "\n", "\n ") + "\n")
			}
			b.WriteString("Filename: ./" + f.Name + "\n")
			fmt.Fprintf(&b, "Size: %d\n", f.Size)
			b.WriteString("SHA256: " + digestHex(f.Digest) + "\n")
			b.WriteString("\n")
		}
		body := []byte(b.String())
		gz, err := gzipBytes(body)
		if err != nil {
			return nil, err
		}
		return map[string]artifactkit.GeneratedFile{
			"Packages":    {Body: body, ContentType: "text/plain"},
			"Packages.gz": {Body: gz, ContentType: "application/gzip"},
		}, nil
	}
}

// gzipBytes gzips a document (apt prefers Packages.gz over Packages).
func gzipBytes(body []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(body); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// debControlFields extracts the control stanza of a hosted .deb.
func debControlFields(ctx context.Context, store artifactkit.HostedStore, f artifactkit.HostedFile) (map[string]string, error) {
	data, err := readBlob(ctx, store, f.Digest)
	if err != nil {
		return nil, err
	}
	ctrl, err := extractDebControl(data)
	if err != nil {
		return nil, err
	}
	return parseControlStanza(ctrl), nil
}
