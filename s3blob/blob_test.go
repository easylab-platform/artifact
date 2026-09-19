package s3blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// TestS3RoundTrip exercises PutIfAbsent/Stat/Open/List/Delete/HashesFor against
// a live S3-compatible endpoint. It is skipped unless ARTIFACT_S3_TEST_ENDPOINT
// is set, so CI without object storage stays green; the dev cluster runs it
// against the shared MinIO.
func TestS3RoundTrip(t *testing.T) {
	endpoint := os.Getenv("ARTIFACT_S3_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("ARTIFACT_S3_TEST_ENDPOINT not set")
	}
	bucket := os.Getenv("ARTIFACT_S3_TEST_BUCKET")
	if bucket == "" {
		bucket = "artifact-s3test"
	}
	region := "us-east-1"
	access, secret := "root", "devpassword"

	// Ensure the bucket exists (idempotent).
	awsCfg := aws.Config{Region: region, Credentials: credentials.NewStaticCredentialsProvider(access, secret, "")}
	raw := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = true
		o.BaseEndpoint = aws.String(endpoint)
	})
	ctx := context.Background()
	if _, err := raw.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &bucket}); err != nil && !strings.Contains(err.Error(), "BucketAlreadyOwnedByYou") && !strings.Contains(err.Error(), "BucketAlreadyExists") {
		t.Fatalf("create bucket: %v", err)
	}

	st, err := New(Config{Endpoint: endpoint, Bucket: bucket, Region: region, AccessKey: access, SecretKey: secret, Prefix: "test", PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	body := "s3-roundtrip-body"
	h := sha256.Sum256([]byte(body))
	digest := "sha256:" + hex.EncodeToString(h[:])

	// Put (new), then Put again (dedup -> false).
	ok, err := st.PutIfAbsent(ctx, digest, strings.NewReader(body))
	if err != nil || !ok {
		t.Fatalf("put: ok=%v err=%v", ok, err)
	}
	ok2, err := st.PutIfAbsent(ctx, digest, strings.NewReader(body))
	if err != nil || ok2 {
		t.Fatalf("dedup put: ok=%v err=%v (want false)", ok2, err)
	}

	// Stat.
	sz, err := st.Stat(ctx, digest)
	if err != nil || sz == nil || *sz != int64(len(body)) {
		t.Fatalf("stat: sz=%v err=%v", sz, err)
	}
	// Open.
	rd, err := st.Open(ctx, digest)
	if err != nil || rd == nil {
		t.Fatalf("open: %v", err)
	}
	got := make([]byte, len(body))
	if _, err := rd.Read(got); err != nil {
		t.Fatalf("read: %v", err)
	}
	_ = rd.Close()
	if string(got) != body {
		t.Fatalf("roundtrip body = %q", got)
	}
	// List contains the digest.
	all, err := st.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, d := range all {
		if d == digest {
			found = true
		}
	}
	if !found {
		t.Fatalf("digest %s not in List (%d entries)", digest, len(all))
	}
	// HashesFor.
	hs, err := st.HashesFor(ctx, digest)
	if err != nil || hs.SHA256 != hex.EncodeToString(h[:]) {
		t.Fatalf("hashes: %v %+v", err, hs)
	}
	// Delete.
	if err := st.Delete(ctx, digest); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if sz, _ := st.Stat(ctx, digest); sz != nil {
		t.Fatal("blob still present after delete")
	}
}
