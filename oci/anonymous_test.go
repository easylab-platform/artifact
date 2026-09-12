package oci

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	artifactkit "github.com/easylab-platform/artifact/core"
)

// TestAnonymousPullPublicPushProtected verifies the public-read policy: with
// auth enabled, an unauthenticated pull is allowed while an unauthenticated
// push is still challenged.
func TestAnonymousPullPublicPushProtected(t *testing.T) {
	auth := artifactkit.NewTokenAuth("writer=write")
	a := New(&OciState{Registry: testRegistry(t), Auth: auth})
	ctx := context.Background()

	req := func() *http.Request {
		return httptest.NewRequest(http.MethodGet, "/v2/x/manifests/latest", nil).WithContext(ctx)
	}

	// Anonymous pull: public.
	if !a.authorize(req(), "x", artifactkit.ActionPull) {
		t.Fatal("anonymous pull must be allowed")
	}
	// Anonymous push/delete: still requires a write credential.
	if a.authorize(req(), "x", artifactkit.ActionPush) {
		t.Fatal("anonymous push must be denied")
	}
	if a.authorize(req(), "x", artifactkit.ActionDelete) {
		t.Fatal("anonymous delete must be denied")
	}
	// A write token may push.
	r := req()
	r.Header.Set("Authorization", "Bearer writer")
	if !a.authorize(r, "x", artifactkit.ActionPush) {
		t.Fatal("write token must push")
	}
}
