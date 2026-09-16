package apk

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/easylab-platform/artifact/core"
)

// GenerateAPKINDEX renders a signed-less APKINDEX.tar.gz from hosted .apk
// files. apk's index is a gzipped tar whose APKINDEX member holds one
// stanza per package (C: checksum, P: name, V: version, A: arch, S: size,
// I: installed size, T: description, plus origin/url/license/timestamp).
//
// The index is UNSIGNED: apk has no per-repository insecure switch, so
// clients install hosted packages with `apk add --allow-untrusted` (pull-
// through packages keep the upstream APKINDEX and its original signature).
//
// Files are named "<arch>/<pkg>.apk" (the client appends "<arch>/
// APKINDEX.tar.gz" to every repository URL), so one index is emitted per
// arch subdirectory. Metadata (name/version) is derived from the filename.
//
// C: is apk's "pull checksum": a base64 sha1 of the package's CONTROL gzip
// member (the first member of the concatenated control+data stream), NOT of
// the whole file — apk rejects the package with "v2 package integrity error"
// when the two disagree.
func GenerateAPKINDEX(store artifactkit.HostedStore) artifactkit.Generator {
	return func(files []artifactkit.HostedFile) (map[string]artifactkit.GeneratedFile, error) {
		indexes := map[string]*bytes.Buffer{}
		for _, f := range files {
			if !strings.HasSuffix(f.Name, ".apk") {
				continue
			}
			arch, rel := splitArch(f.Name)
			buf := indexes[arch]
			if buf == nil {
				buf = &bytes.Buffer{}
				indexes[arch] = buf
			}
			name, version, _ := nameVersionArch(rel)
			data, err := readBlob(store, f.Digest)
			if err != nil {
				continue
			}
			cs, ok := apkPullChecksum(data)
			if !ok {
				continue
			}
			fmt.Fprintf(buf, "C:%s\n", cs)
			fmt.Fprintf(buf, "P:%s\n", name)
			fmt.Fprintf(buf, "V:%s\n", version)
			fmt.Fprintf(buf, "A:%s\n", arch)
			fmt.Fprintf(buf, "S:%d\n", f.Size)
			fmt.Fprintf(buf, "I:%d\n", f.Size)
			fmt.Fprintf(buf, "T:hosted package %s\n", name)
			fmt.Fprintf(buf, "o:%s\n", name)
			fmt.Fprintf(buf, "t:%d\n", time.Now().UTC().Unix())
			buf.WriteString("\n")
		}
		out := map[string]artifactkit.GeneratedFile{}
		for arch, buf := range indexes {
			body, ct, err := tarGz("APKINDEX", buf.Bytes())
			if err != nil {
				return nil, err
			}
			out[arch+"/APKINDEX.tar.gz"] = artifactkit.GeneratedFile{Body: body, ContentType: ct}
			if len(indexes) == 1 {
				// A flat repo URL (no arch) still resolves.
				out["APKINDEX.tar.gz"] = artifactkit.GeneratedFile{Body: body, ContentType: ct}
			}
		}
		return out, nil
	}
}

// apkPullChecksum returns apk's C: value: "Q1" + base64(sha1(control member)).
// A v2 .apk is a sequence of gzip streams: an optional .SIGN.RSA signature
// member, then the control tar (.PKGINFO), then the data tar. apk checksums
// the control member, so walk the members and hash the one carrying .PKGINFO.
func apkPullChecksum(data []byte) (string, bool) {
	offset := 0
	for offset < len(data) {
		r := bytes.NewReader(data[offset:])
		zr, err := gzip.NewReader(r)
		if err != nil {
			return "", false
		}
		zr.Multistream(false)
		raw, err := io.ReadAll(zr)
		if err != nil {
			return "", false
		}
		_ = zr.Close()
		// bytes.Reader implements io.ByteReader, so gzip.Reader consumes from
		// it directly (no read-ahead): Len() is exactly the bytes left.
		consumed := len(data) - offset - r.Len()
		if consumed <= 0 {
			return "", false
		}
		if bytes.Contains(raw, []byte(".PKGINFO")) {
			sum := sha1.Sum(data[offset : offset+consumed])
			return "Q1" + base64.StdEncoding.EncodeToString(sum[:]), true
		}
		offset += consumed
	}
	return "", false
}

// splitArch splits "<arch>/<file>" into its parts; a bare filename has arch
// "noarch".
func splitArch(name string) (arch, file string) {
	if i := strings.IndexByte(name, '/'); i >= 0 {
		return name[:i], name[i+1:]
	}
	return "noarch", name
}

// tarGz wraps content in a gzipped tar with a single member.
func tarGz(member string, content []byte) ([]byte, string, error) {
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	if err := tw.WriteHeader(&tar.Header{
		Name: member, Size: int64(len(content)), Mode: 0o644, ModTime: time.Unix(0, 0),
	}); err != nil {
		return nil, "", err
	}
	if _, err := tw.Write(content); err != nil {
		return nil, "", err
	}
	if err := tw.Close(); err != nil {
		return nil, "", err
	}
	var gz bytes.Buffer
	gw := gzip.NewWriter(&gz)
	if _, err := gw.Write(tarBuf.Bytes()); err != nil {
		return nil, "", err
	}
	if err := gw.Close(); err != nil {
		return nil, "", err
	}
	return gz.Bytes(), "application/gzip", nil
}

// nameVersionArch derives (name, version, arch) from an apk filename
// "<name>-<version>-r<rel>.apk"; arch is unknown from the flat repo name and
// defaults to "noarch" unless the filename embeds it.
func nameVersionArch(filename string) (name, version, arch string) {
	stem := strings.TrimSuffix(filename, ".apk")
	// <name>-<pkgver>-r<rel>
	i := strings.LastIndex(stem, "-r")
	if i < 0 {
		return stem, "0", "noarch"
	}
	rel := stem[i+1:] // "r0"
	rest := stem[:i]
	j := strings.LastIndex(rest, "-")
	if j < 0 {
		return rest, rel, "noarch"
	}
	return rest[:j], rest[j+1:] + "-" + rel, "noarch"
}

// readBlob loads a whole CAS blob (index generation reads package bytes to
// compute the checksum).
func readBlob(store artifactkit.HostedStore, digest string) ([]byte, error) {
	rd, err := store.Registry.Blobs.Open(context.Background(), digest)
	if err != nil || rd == nil {
		return nil, fmt.Errorf("blob %s unavailable", digest)
	}
	defer func() { _ = rd.Close() }()
	return io.ReadAll(rd)
}
