package artifactkit

import "testing"

// TestCacheKeyNormalization locks the cache identity: scheme/host case and
// default ports fold, credential query parameters are dropped, and parameter
// order does not fragment the cache.
func TestCacheKeyNormalization(t *testing.T) {
	cases := map[string]string{
		"https://Example.COM/a/b":     "https://example.com/a/b",
		"https://example.com:443/a/b": "https://example.com/a/b",
		"http://example.com:80/a":     "http://example.com/a",
		// A presigned URL and its unsigned form share the key.
		"https://h/x?X-Amz-Signature=abc&X-Amz-Expires=60": "https://h/x",
		"https://h/x?b=2&a=1":                              "https://h/x?a=1&b=2",
		"https://h/x?a=1&b=2":                              "https://h/x?a=1&b=2",
		// Non-credential query is retained.
		"https://h/x?version=3": "https://h/x?version=3",
	}
	for in, want := range cases {
		if got := CacheKey(in); got != want {
			t.Errorf("CacheKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestIsImmutableURL distinguishes digest-pinned (never-changing) URLs from
// mutable ones.
func TestIsImmutableURL(t *testing.T) {
	immutable := []string{
		"https://h/obj?sha256=abc",
		"https://h/obj?digest=sha256:abc",
		"https://reg/v2/x/blobs/sha256:abc",
		"https://h/f.tgz?integrity=sha512-xyz",
	}
	for _, u := range immutable {
		if !IsImmutableURL(u) {
			t.Errorf("IsImmutableURL(%q) = false, want true", u)
		}
	}
	mutable := []string{
		"https://h/latest.json",
		"https://h/versions",
		"https://h/pkg-1.2.3.tar.gz",
	}
	for _, u := range mutable {
		if IsImmutableURL(u) {
			t.Errorf("IsImmutableURL(%q) = true, want false", u)
		}
	}
}
