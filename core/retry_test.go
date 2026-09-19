package artifactkit

import (
	"net/http"
	"testing"
	"time"
)

// TestRetryableStatus locks the set of statuses the fetch layer retries:
// transport errors (0), the egress-proxy hiccup codes, and 429 rate limits.
func TestRetryableStatus(t *testing.T) {
	for _, c := range []int{0, 502, 503, 504, 429} {
		if !retryableStatus(c) {
			t.Errorf("status %d should be retryable", c)
		}
	}
	for _, c := range []int{200, 204, 304, 400, 401, 403, 404, 410, 500, 501} {
		if retryableStatus(c) {
			t.Errorf("status %d should NOT be retryable", c)
		}
	}
}

// TestRetryWaitHonorsRetryAfter verifies a server's Retry-After (seconds or
// HTTP date) overrides the default backoff, and is capped.
func TestRetryWaitHonorsRetryAfter(t *testing.T) {
	fallback := 500 * time.Millisecond
	mk := func(v string) *http.Response {
		h := http.Header{}
		if v != "" {
			h.Set("Retry-After", v)
		}
		return &http.Response{Header: h}
	}
	if d := retryWait(mk("2"), fallback); d != 2*time.Second {
		t.Errorf("Retry-After 2 = %v, want 2s", d)
	}
	if d := retryWait(mk(""), fallback); d != fallback {
		t.Errorf("no Retry-After = %v, want fallback", d)
	}
	// A huge value is capped.
	if d := retryWait(mk("9999"), fallback); d != maxRetryAfter {
		t.Errorf("Retry-After 9999 = %v, want cap %v", d, maxRetryAfter)
	}
	// An HTTP-date in the past falls back.
	past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
	if d := retryWait(mk(past), fallback); d != fallback {
		t.Errorf("past Retry-After = %v, want fallback", d)
	}
	// nil response is safe.
	if d := retryWait(nil, fallback); d != fallback {
		t.Errorf("nil resp = %v, want fallback", d)
	}
}
