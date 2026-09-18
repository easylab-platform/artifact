package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/easylab-platform/artifact/targets"
)

func TestTargetStoreRoundTrip(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	in := targets.Target{
		ID: "maven.corp", Protocol: "maven", Base: "https://nexus.corp/m2",
		Hosts: []string{"nexus.corp"}, Auth: targets.Auth{Mode: targets.AuthBasic, Username: "u", Secret: "env:NEXUS_PW"},
	}
	if err := st.PutTarget(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := st.ListTargets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "maven.corp" || got[0].Base != in.Base {
		t.Fatalf("round trip = %+v", got)
	}
	if got[0].Auth.Mode != targets.AuthBasic || got[0].Auth.Secret != "env:NEXUS_PW" {
		t.Errorf("auth not persisted: %+v", got[0].Auth)
	}

	// Upsert same ID, then delete.
	in.Base = "https://nexus2.corp/m2"
	if err := st.PutTarget(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, _ = st.ListTargets(ctx)
	if len(got) != 1 || got[0].Base != "https://nexus2.corp/m2" {
		t.Fatalf("upsert failed: %+v", got)
	}
	if err := st.DeleteTarget(ctx, "maven.corp"); err != nil {
		t.Fatal(err)
	}
	if got, _ = st.ListTargets(ctx); len(got) != 0 {
		t.Fatalf("delete failed: %+v", got)
	}
}

func TestRegistryLoadFromStore(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	if err := st.PutTarget(ctx, targets.Target{ID: "maven.corp", Protocol: "maven", Base: "https://nexus.corp/m2"}); err != nil {
		t.Fatal(err)
	}

	reg := targets.NewRegistry()
	if err := reg.Load(ctx, st); err != nil {
		t.Fatal(err)
	}
	tgt, ok := reg.Get("maven.corp")
	if !ok {
		t.Fatal("user target not loaded")
	}
	if tgt.Shared() {
		t.Error("user target must be isolated by default")
	}
	// Built-ins survive the load.
	if _, ok := reg.Get("maven"); !ok {
		t.Error("built-in maven target lost during Load")
	}
}
