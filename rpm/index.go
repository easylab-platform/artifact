package rpm

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/easylab-platform/artifact/core"
)

// GenerateRepodata renders a minimal but valid yum/dnf repository index from
// hosted .rpm files: repodata/repomd.xml plus a primary metadata document.
// The index is derived from filenames (NVR.arch.rpm) and the real content
// checksums of the stored bytes.
//
// Both documents are UNSIGNED. The repo config must set gpgcheck=0 (there is
// no per-metadata insecure switch for the index signature; rpm's own package
// signature is separately controlled by gpgcheck). Pull-through repos keep
// the upstream repodata with its original signature.
//
// repomd.xml references exactly one <data type="primary"> member; dnf treats
// filelists/other as optional, so a primary-only repo is valid.
func GenerateRepodata(store artifactkit.HostedStore) artifactkit.Generator {
	return func(files []artifactkit.HostedFile) (map[string]artifactkit.GeneratedFile, error) {
		var primary bytes.Buffer
		primary.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
		primary.WriteString(`<metadata xmlns="http://linux.duke.edu/metadata/common" xmlns:rpm="http://linux.duke.edu/metadata/rpm" packages="`)
		count := 0
		var pkgs []string
		for _, f := range files {
			if !strings.HasSuffix(f.Name, ".rpm") {
				continue
			}
			name, ver, rel, arch := nvrArch(f.Name)
			sum := sha256Hex(f.Digest)
			pkgs = append(pkgs, fmt.Sprintf(`<package type="rpm">
<name>%s</name>
<arch>%s</arch>
<version epoch="0" ver="%s" rel="%s"/>
<checksum type="sha256" pkgid="YES">%s</checksum>
<summary>hosted package %s</summary>
<description>hosted package %s</description>
<packager></packager>
<url></url>
<time file="%d" build="%d"/>
<size package="%d" installed="%d" archive="%d"/>
<location href="%s"/>
<format>
<rpm:license></rpm:license>
<rpm:group></rpm:group>
<rpm:buildhost></rpm:buildhost>
<rpm:sourcerpm></rpm:sourcerpm>
<rpm:header-range start="0" end="0"/>
</format>
</package>`, name, arch, ver, rel, sum, name, name, nowUnix(), nowUnix(), f.Size, f.Size, f.Size, f.Name))
			count++
		}
		fmt.Fprintf(&primary, "%d\">", count)
		primary.WriteString("\n")
		for _, p := range pkgs {
			primary.WriteString(p)
			primary.WriteString("\n")
		}
		primary.WriteString("</metadata>\n")

		// Gzip the primary document and hash both forms (repomd records the
		// checksum of the COMPRESSED file and open-checksum of the raw XML).
		rawSum := sha256.Sum256(primary.Bytes())
		var gz bytes.Buffer
		gw := gzip.NewWriter(&gz)
		if _, err := gw.Write(primary.Bytes()); err != nil {
			return nil, err
		}
		if err := gw.Close(); err != nil {
			return nil, err
		}
		gzSum := sha256.Sum256(gz.Bytes())
		primaryName := hex.EncodeToString(gzSum[:])[:16] + "-primary.xml.gz"

		var repomd bytes.Buffer
		repomd.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
		repomd.WriteString(`<repomd xmlns="http://linux.duke.edu/metadata/repo" xmlns:rpm="http://linux.duke.edu/metadata/rpm">` + "\n")
		fmt.Fprintf(&repomd, "<revision>%d</revision>\n", nowUnix())
		repomd.WriteString(`<data type="primary">` + "\n")
		fmt.Fprintf(&repomd, "<checksum type=\"sha256\">%s</checksum>\n", hex.EncodeToString(gzSum[:]))
		fmt.Fprintf(&repomd, "<open-checksum type=\"sha256\">%s</open-checksum>\n", hex.EncodeToString(rawSum[:]))
		fmt.Fprintf(&repomd, "<location href=\"repodata/%s\"/>\n", primaryName)
		fmt.Fprintf(&repomd, "<timestamp>%d</timestamp>\n", nowUnix())
		fmt.Fprintf(&repomd, "<size>%d</size>\n", gz.Len())
		fmt.Fprintf(&repomd, "<open-size>%d</open-size>\n", primary.Len())
		repomd.WriteString("</data>\n")
		repomd.WriteString("</repomd>\n")

		return map[string]artifactkit.GeneratedFile{
			"repodata/repomd.xml":     {Body: repomd.Bytes(), ContentType: "application/xml"},
			"repodata/" + primaryName: {Body: gz.Bytes(), ContentType: "application/gzip"},
		}, nil
	}
}

// nvrArch derives (name, version, release, arch) from
// "<name>-<version>-<release>.<arch>.rpm".
func nvrArch(filename string) (name, version, release, arch string) {
	stem := strings.TrimSuffix(filename, ".rpm")
	if i := strings.LastIndex(stem, "."); i >= 0 {
		arch = stem[i+1:]
		stem = stem[:i]
	}
	if arch == "" {
		arch = "x86_64"
	}
	// <name>-<version>-<release>
	if i := strings.LastIndex(stem, "-"); i >= 0 {
		release = stem[i+1:]
		stem = stem[:i]
	} else {
		release = "1"
	}
	if i := strings.LastIndex(stem, "-"); i >= 0 {
		version = stem[i+1:]
		name = stem[:i]
	} else {
		version = "0"
		name = stem
	}
	return name, version, release, arch
}

func sha256Hex(digest string) string {
	if i := strings.IndexByte(digest, ':'); i >= 0 {
		return digest[i+1:]
	}
	return digest
}

func nowUnix() int64 { return time.Now().UTC().Unix() }
