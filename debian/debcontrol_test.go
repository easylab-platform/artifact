package debian

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"testing"
)

// buildDeb assembles a minimal .deb (ar with debian-binary + gzip control.tar
// + dummy data.tar) for parser tests.
func buildDeb(t *testing.T, control string) []byte {
	t.Helper()
	var ctrlTar bytes.Buffer
	tw := tar.NewWriter(&ctrlTar)
	body := []byte(control)
	_ = tw.WriteHeader(&tar.Header{Name: "./control", Size: int64(len(body)), Mode: 0o644})
	_, _ = tw.Write(body)
	_ = tw.Close()
	var ctrlGz bytes.Buffer
	gw := gzip.NewWriter(&ctrlGz)
	_, _ = gw.Write(ctrlTar.Bytes())
	_ = gw.Close()

	ar := func(name string, data []byte) []byte {
		// Exact 60-byte ar header: name(16) mtime(12) uid(6) gid(6) mode(8)
		// size(10) magic(2).
		hdr := fmt.Sprintf("%-16s%-12d%-6d%-6d%-8s%-10d", name, 0, 0, 0, "100644", len(data)) + "`\n"
		out := []byte(hdr)
		out = append(out, data...)
		if len(data)%2 == 1 {
			out = append(out, '\n')
		}
		return out
	}
	var buf bytes.Buffer
	buf.WriteString("!<arch>\n")
	buf.Write(ar("debian-binary", []byte("2.0\n")))
	buf.Write(ar("control.tar.gz", ctrlGz.Bytes()))
	buf.Write(ar("data.tar.gz", []byte("x")))
	return buf.Bytes()
}

// TestExtractDebControlAndParse verifies the ar/tar/control extraction and
// stanza parsing used by the hosted Packages generator.
func TestExtractDebControlAndParse(t *testing.T) {
	control := "Package: easylab-worker\nVersion: 1.2.3\nArchitecture: amd64\nDepends: libc6 (>= 2.31)\nDescription: EasyLab worker agent\n long line two\n"
	deb := buildDeb(t, control)
	raw, err := extractDebControl(deb)
	if err != nil {
		t.Fatal(err)
	}
	fields := parseControlStanza(raw)
	if fields["Package"] != "easylab-worker" {
		t.Fatalf("Package = %q", fields["Package"])
	}
	if fields["Version"] != "1.2.3" {
		t.Fatalf("Version = %q", fields["Version"])
	}
	if fields["Depends"] != "libc6 (>= 2.31)" {
		t.Fatalf("Depends = %q", fields["Depends"])
	}
	if fields["Architecture"] != "amd64" {
		t.Fatalf("Architecture = %q", fields["Architecture"])
	}
}

// TestParseAr verifies a malformed archive is rejected.
func TestParseAr(t *testing.T) {
	if _, err := parseAr([]byte("not an ar")); err == nil {
		t.Fatal("expected error")
	}
}

// TestNameVersionFromDeb verifies filename fallback parsing.
func TestNameVersionFromDeb(t *testing.T) {
	n, v := nameVersionFromDeb("easylab-worker_2.0.1_amd64.deb")
	if n != "easylab-worker" || v != "2.0.1" {
		t.Fatalf("got %q %q", n, v)
	}
}
