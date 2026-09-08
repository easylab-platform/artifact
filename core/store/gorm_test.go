package store

import (
	"context"
	"os"
	"testing"

	pkrkit "github.com/easylab-platform/artifact/core"
)

func TestOpenStoreSQLiteRoundTrip(t *testing.T) {
	s, err := OpenSQLite(t.TempDir() + "/m.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	art := pkrkit.Artifact{Format: "oci", Repository: "alpine", Version: "3", Digest: "d1", Source: "push"}
	if err := s.Put(ctx, art); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "oci", "alpine", "3")
	if err != nil {
		t.Fatal(err)
	}
	if got.Digest != "d1" || got.Version != "3" {
		t.Fatalf("roundtrip: %+v", got)
	}
	vs, _ := s.ListVersions(ctx, "oci", "alpine")
	if len(vs) != 1 || vs[0] != "3" {
		t.Fatalf("versions=%v", vs)
	}
}

func TestOpenStoreBackendSwitch(t *testing.T) {
	// postgres/mysql are integration-gated; validate the dialector selection and
	// a sqlite open via the generic path (mirrors easylab wiring).
	s, err := OpenStore(DriverConfig{Kind: KindSQLite, DSN: t.TempDir() + "/m.db"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Put(context.Background(), pkrkit.Artifact{Format: "g", Repository: "r", Version: "1"}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenBlobStoreFilesystem(t *testing.T) {
	dir := t.TempDir()
	b, err := OpenBlobStore(BlobFilesystem, dir)
	if err != nil {
		t.Fatal(err)
	}
	// Stat of an absent but valid-shaped digest returns (nil,nil), not error.
	digest := "sha256:"+hex64
	if n, err := b.Stat(context.Background(), digest); err != nil || n != nil {
		t.Fatalf("stat absent = (%v,%v)", n, err)
	}
}

func TestOpenBlobStoreS3Placeholder(t *testing.T) {
	// s3 is a placeholder: falls back to filesystem (does not crash).
	b, err := OpenBlobStore(BlobS3, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if b == nil {
		t.Fatal("nil blob store")
	}
}

func TestOpenBlobStoreUnknownErr(t *testing.T) {
	if _, err := OpenBlobStore("nope", t.TempDir()); err == nil {
		t.Fatal("expected error for unknown backend")
	}
}



const hex64 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestOpenStorePostgresGated(t *testing.T) {
	dsn := os.Getenv("EASYVCS_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("EASYVCS_TEST_PG_DSN not set")
	}
	s, err := OpenStore(DriverConfig{Kind: KindPostgres, DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Put(context.Background(), pkrkit.Artifact{Format: "g", Repository: "r", Version: "1"}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenStoreMySQLGated(t *testing.T) {
	dsn := os.Getenv("EASYVCS_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("EASYVCS_TEST_MYSQL_DSN not set")
	}
	s, err := OpenStore(DriverConfig{Kind: KindMySQL, DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Put(context.Background(), pkrkit.Artifact{Format: "g", Repository: "r", Version: "1"}); err != nil {
		t.Fatal(err)
	}
}
