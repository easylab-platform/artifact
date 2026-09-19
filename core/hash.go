package artifactkit

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"hash"
	"io"
)

// multiHasher threads bytes through md5, sha1, sha256 and sha512 in one pass.
// It is an io.Writer so it can sit in an io.MultiWriter next to the destination
// file, letting StoreStream persist and hash a body in a single read.
type multiHasher struct {
	h256, h1, m5, h512 hash.Hash
}

func newMultiHasher() *multiHasher {
	return &multiHasher{h256: sha256.New(), h1: sha1.New(), m5: md5.New(), h512: sha512.New()}
}

func (m *multiHasher) Write(p []byte) (int, error) {
	if _, err := m.h256.Write(p); err != nil {
		return 0, err
	}
	_, _ = m.h1.Write(p)
	_, _ = m.m5.Write(p)
	_, _ = m.h512.Write(p)
	return len(p), nil
}

func (m *multiHasher) sum() Hashes {
	return Hashes{
		SHA256: hex.EncodeToString(m.h256.Sum(nil)),
		SHA1:   hex.EncodeToString(m.h1.Sum(nil)),
		MD5:    hex.EncodeToString(m.m5.Sum(nil)),
		SHA512: hex.EncodeToString(m.h512.Sum(nil)),
	}
}

// ComputeHashes streams data once, threading it through md5, sha1, sha256 and
// sha512 simultaneously. It returns the raw hex digests.
func ComputeHashes(r io.Reader) (Hashes, error) {
	h := newMultiHasher()
	if _, err := io.Copy(h, r); err != nil {
		return Hashes{}, err
	}
	return h.sum(), nil
}

// ComputeHashesBytes is the byte-slice convenience.
func ComputeHashesBytes(b []byte) (Hashes, error) {
	return ComputeHashes(&bytesReader{b})
}

type bytesReader struct{ b []byte }

func (r *bytesReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.b)
	r.b = r.b[n:]
	return n, nil
}

// DigestOf returns "sha256:<hex>" for bytes.
func DigestOf(b []byte) string {
	h, _ := ComputeHashesBytes(b)
	return "sha256:" + h.SHA256
}

// ParseDigest validates an "algo:hex" digest and returns the hex portion.
// sha256/sha512 only (the algorithms registries actually use on the wire).
func ParseDigest(d string) (string, error) {
	i := indexByte(d, ':')
	if i <= 0 || i == len(d)-1 {
		return "", errors.New("invalid digest: missing ':' or empty part")
	}
	algo, hexpart := d[:i], d[i+1:]
	var want int
	switch algo {
	case "sha256":
		want = 64
	case "sha512":
		want = 128
	default:
		return "", errors.New("invalid digest: unsupported algorithm " + algo)
	}
	if len(hexpart) != want || !isHex(hexpart) {
		return "", errors.New("invalid digest: wrong length or non-hex")
	}
	return hexpart, nil
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return len(s) > 0
}
