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
// Metadata (name/version/arch) is derived from the filename; the content
// checksum (C:) is the real sha1 of the stored bytes.
func GenerateAPKINDEX(store artifactkit.HostedStore) artifactkit.Generator {
	return func(files []artifactkit.HostedFile) (map[string]artifactkit.GeneratedFile, error) {
		var index bytes.Buffer
		for _, f := range files {
			if !strings.HasSuffix(f.Name, ".apk") {
				continue
			}
			name, version, arch := nameVersionArch(f.Name)
			data, err := readBlob(store, f.Digest)
			if err != nil {
				continue
			}
			sum := sha1.Sum(data)
			cs := "Q1" + base64.StdEncoding.EncodeToString(sum[:])
			fmt.Fprintf(&index, "C:%s\n", cs)
			fmt.Fprintf(&index, "P:%s\n", name)
			fmt.Fprintf(&index, "V:%s\n", version)
			fmt.Fprintf(&index, "A:%s\n", arch)
			fmt.Fprintf(&index, "S:%d\n", f.Size)
			fmt.Fprintf(&index, "I:%d\n", f.Size)
			fmt.Fprintf(&index, "T:hosted package %s\n", name)
			fmt.Fprintf(&index, "o:%s\n", name)
			fmt.Fprintf(&index, "t:%d\n", time.Now().UTC().Unix())
			index.WriteString("\n")
		}
		body, ct, err := tarGz("APKINDEX", index.Bytes())
		if err != nil {
			return nil, err
		}
		return map[string]artifactkit.GeneratedFile{
			"APKINDEX.tar.gz": {Body: body, ContentType: ct},
		}, nil
	}
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
