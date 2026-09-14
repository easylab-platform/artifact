package debian

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/easylab-platform/artifact/core"
	"github.com/ulikunitz/xz"
)

// .deb is an `ar` archive containing (in order) debian-binary, control.tar.*,
// and data.tar.*. We only need control.tar.* to read the control stanza. This
// file implements just enough `ar` + tar(.gz/.xz/Z) handling for that, with
// pure-Go decompression (no external dpkg/ar binaries).

// readBlob loads a whole CAS blob into memory (index generation reads only
// control members, which are small; the bytes fetched here are bounded by
// the uploaded .deb size).
func readBlob(store artifactkit.HostedStore, digest string) ([]byte, error) {
	rd, err := store.Registry.Blobs.Open(context.Background(), digest)
	if err != nil || rd == nil {
		return nil, fmt.Errorf("blob %s unavailable", digest)
	}
	defer func() { _ = rd.Close() }()
	return io.ReadAll(rd)
}

// extractDebControl returns the raw control file bytes from a .deb.
func extractDebControl(deb []byte) ([]byte, error) {
	entries, err := parseAr(deb)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.name, "control.tar") {
			return extractControlFile(e.data)
		}
	}
	return nil, fmt.Errorf("no control.tar in .deb")
}

// arEntry is one member of an ar archive.
type arEntry struct {
	name string
	data []byte
}

// parseAr parses the common `ar` format (with the GNU string table handled
// by ignoring the special name and using the main header names).
func parseAr(b []byte) ([]arEntry, error) {
	if len(b) < 8 || string(b[:8]) != "!<arch>\n" {
		return nil, fmt.Errorf("not an ar archive")
	}
	var out []arEntry
	p := 8
	for p+60 <= len(b) {
		hdr := b[p : p+60]
		p += 60
		name := strings.TrimRight(string(hdr[0:16]), " ")
		sizeStr := strings.TrimSpace(string(hdr[48:58]))
		var size int
		if _, err := fmt.Sscanf(sizeStr, "%d", &size); err != nil {
			return nil, fmt.Errorf("bad ar size %q", sizeStr)
		}
		// GNU long names: the real name lives in the "//" table; the member
		// name is "/<offset>". We don't emit those and can skip them.
		if name == "//" {
			p += size + size%2
			continue
		}
		if p+size > len(b) {
			return nil, fmt.Errorf("truncated ar member")
		}
		data := b[p : p+size]
		p += size
		if p%2 == 1 {
			p++ // padding
		}
		name = strings.TrimSuffix(name, "/")
		out = append(out, arEntry{name: name, data: data})
	}
	return out, nil
}

// extractControlFile reads ./control from a (possibly compressed) tar.
func extractControlFile(compressed []byte) ([]byte, error) {
	r, err := decompress(compressed)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Name == "./control" || h.Name == "control" {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("control not found in control.tar")
}

// decompress wraps gzip/xz/zlib-plain based on magic; control.tar is
// conventionally gzip or xz.
func decompress(b []byte) (io.Reader, error) {
	switch {
	case len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b:
		return gzip.NewReader(bytes.NewReader(b))
	case len(b) >= 6 && bytes.Equal(b[:6], []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}):
		return xz.NewReader(bytes.NewReader(b))
	default:
		// Old .deb control.tar (uncompressed tar) or unknown: try tar as-is.
		return bytes.NewReader(b), nil
	}
}

// parseControlStanza parses "Field: value\n" lines (continuation lines start
// with a space), returning the first stanza's fields.
func parseControlStanza(b []byte) map[string]string {
	fields := map[string]string{}
	var lastKey string
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			if len(fields) > 0 {
				break // end of first stanza
			}
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if lastKey != "" {
				fields[lastKey] += "\n" + strings.TrimRight(line[1:], "\r")
			}
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		lastKey = k
		fields[k] = strings.TrimSpace(strings.TrimRight(v, "\r"))
	}
	return fields
}

// nameVersionFromDeb derives (name, version) from a Debian filename
// "<name>_<version>_<arch>.deb".
func nameVersionFromDeb(filename string) (string, string) {
	stem := strings.TrimSuffix(filename, ".deb")
	parts := strings.Split(stem, "_")
	if len(parts) >= 2 {
		return parts[0], parts[1]
	}
	return stem, "0"
}

// digestHex strips the "sha256:" prefix from a digest.
func digestHex(d string) string {
	if i := strings.IndexByte(d, ':'); i >= 0 {
		return d[i+1:]
	}
	return d
}
