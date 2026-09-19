// Package s3blob implements artifactkit's BlobStore on S3-compatible object
// storage (AWS S3, MinIO, Ceph RGW). It lives in its own module so the heavy
// aws-sdk-go-v2 dependency never reaches the protocol adapters (which depend
// only on core); a consumer enables it with a blank import in cmd:
//
//	import _ "github.com/easylab-platform/artifact/s3blob"
//
// The CAS layout mirrors the filesystem store: one object per digest at
// <prefix>/sha256/<first-two>/<rest>. Blobs are immutable and content-addressed,
// so PutIfAbsent is a HEAD-then-PUT and deletes are unconditional.
package s3blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	artifactkit "github.com/easylab-platform/artifact/core"
	"github.com/easylab-platform/artifact/core/store"
)

// Config selects the bucket and connection. Environment fallbacks let a
// deployment set the chart's objectStore values without new plumbing:
//
//	ARTIFACT_S3_ENDPOINT   (e.g. http://minio.develop.svc.cluster.local:9000)
//	ARTIFACT_S3_BUCKET     (required)
//	ARTIFACT_S3_REGION     (default us-east-1)
//	ARTIFACT_S3_ACCESS_KEY / ARTIFACT_S3_SECRET_KEY (static creds)
//	ARTIFACT_S3_PREFIX     (key prefix, default "artifact")
//	ARTIFACT_S3_PATH_STYLE ("1" forces path-style; default true for a custom endpoint)
type Config struct {
	Endpoint  string
	Bucket    string
	Region    string
	AccessKey string
	SecretKey string
	Prefix    string
	PathStyle bool
}

// Store is an S3-backed BlobStore.
type Store struct {
	client *s3.Client
	bucket string
	prefix string
}

// New builds a Store from a Config.
func New(cfg Config) (*Store, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("s3blob: bucket is required")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	cfg.Prefix = strings.Trim(cfg.Prefix, "/")
	optFns := []func(*s3.Options){
		func(o *s3.Options) {
			o.Region = cfg.Region
			o.UsePathStyle = cfg.PathStyle
			if cfg.Endpoint != "" {
				o.BaseEndpoint = aws.String(cfg.Endpoint)
			}
		},
	}
	awsCfg := aws.Config{Region: cfg.Region}
	if cfg.AccessKey != "" || cfg.SecretKey != "" {
		awsCfg.Credentials = credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")
	}
	return &Store{client: s3.NewFromConfig(awsCfg, optFns...), bucket: cfg.Bucket, prefix: cfg.Prefix}, nil
}

// key maps a digest to its object key.
func (s *Store) key(digest string) (string, error) {
	hexpart, err := artifactkit.ParseDigest(digest)
	if err != nil {
		return "", err
	}
	if len(hexpart) < 3 {
		return "", errors.New("digest too short")
	}
	base := "sha256/" + hexpart[:2] + "/" + hexpart[2:]
	if s.prefix != "" {
		return s.prefix + "/" + base, nil
	}
	return base, nil
}

// Stat implements BlobStore.
func (s *Store) Stat(ctx context.Context, digest string) (*int64, error) {
	k, err := s.key(digest)
	if err != nil {
		return nil, err
	}
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: &k})
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return out.ContentLength, nil
}

// Open implements BlobStore. The returned reader buffers the whole object in
// memory: the S3 GetObject body is stream-only, but ServeBlob needs Seek for
// Range. Blobs here are the same ones the filesystem store mmaps; a bounded
// read is acceptable and keeps Range correct.
func (s *Store) Open(ctx context.Context, digest string) (io.ReadSeekCloser, error) {
	k, err := s.key(digest)
	if err != nil {
		return nil, err
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &k})
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = out.Body.Close() }()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, err
	}
	return &memReadSeekCloser{data: data}, nil
}

// PutIfAbsent implements BlobStore with a HEAD-then-PUT. It verifies the sha256
// while streaming, so a corrupted transfer is never stored. Returns false when
// the digest already exists (dedup).
func (s *Store) PutIfAbsent(ctx context.Context, digest string, r io.Reader) (bool, error) {
	k, err := s.key(digest)
	if err != nil {
		return false, err
	}
	if sz, err := s.Stat(ctx, digest); err != nil {
		return false, err
	} else if sz != nil {
		return false, nil // dedup
	}
	// Buffer to a temp file so the sha256 can be verified before the PUT (S3
	// PUT cannot be replayed after a mismatch). Large layers stream to disk,
	// not RAM.
	tmp, err := os.CreateTemp("", "s3blob-*")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	defer func() { _ = tmp.Close() }()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), r); err != nil {
		return false, err
	}
	if got := "sha256:" + hex.EncodeToString(h.Sum(nil)); got != digest {
		return false, fmt.Errorf("s3blob: digest mismatch: expected %s got %s", digest, got)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &s.bucket, Key: &k, Body: tmp,
	}); err != nil {
		return false, err
	}
	return true, nil
}

// HashesFor implements BlobStore: recompute from the stored bytes.
func (s *Store) HashesFor(ctx context.Context, digest string) (artifactkit.Hashes, error) {
	rd, err := s.Open(ctx, digest)
	if err != nil {
		return artifactkit.Hashes{}, err
	}
	if rd == nil {
		return artifactkit.Hashes{}, artifactkit.ErrBlobUnknown
	}
	defer func() { _ = rd.Close() }()
	return artifactkit.ComputeHashes(rd)
}

// Delete implements BlobStore (unconditional; a missing key is not an error).
func (s *Store) Delete(ctx context.Context, digest string) error {
	k, err := s.key(digest)
	if err != nil {
		return err
	}
	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &k})
	return err
}

// List implements BlobStore: every digest under the prefix, paginated.
func (s *Store) List(ctx context.Context) ([]string, error) {
	prefix := s.prefix
	if prefix != "" {
		prefix += "/"
	}
	prefix += "sha256/"
	var out []string
	var token *string
	for {
		page, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: &s.bucket, Prefix: &prefix, ContinuationToken: token,
		})
		if err != nil {
			return nil, err
		}
		for _, obj := range page.Contents {
			if d, ok := digestFromKey(aws.ToString(obj.Key)); ok {
				out = append(out, d)
			}
		}
		if page.IsTruncated == nil || !*page.IsTruncated {
			break
		}
		token = page.NextContinuationToken
	}
	return out, nil
}

// ModTime implements store.BlobAger so the reaper can skip in-flight writes.
func (s *Store) ModTime(ctx context.Context, digest string) (time.Time, error) {
	k, err := s.key(digest)
	if err != nil {
		return time.Time{}, err
	}
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: &k})
	if err != nil {
		if isNotFound(err) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	if out.LastModified != nil {
		return *out.LastModified, nil
	}
	return time.Time{}, nil
}

// digestFromKey recovers the digest from a key ending sha256/<2>/<62>.
func digestFromKey(k string) (string, bool) {
	i := strings.LastIndex(k, "sha256/")
	if i < 0 {
		return "", false
	}
	rest := k[i+len("sha256/"):]
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 62 {
		return "", false
	}
	return "sha256:" + parts[0] + parts[1], true
}

// isNotFound reports whether an S3 error is a missing key/bucket.
func isNotFound(err error) bool {
	var nsk *types.NoSuchKey
	if errors.As(err, &nsk) {
		return true
	}
	var nf *types.NotFound
	if errors.As(err, &nf) {
		return true
	}
	return false
}

// memReadSeekCloser is an in-memory io.ReadSeekCloser for ServeBlob.
type memReadSeekCloser struct {
	data []byte
	off  int64
}

func (m *memReadSeekCloser) Read(p []byte) (int, error) {
	if m.off >= int64(len(m.data)) {
		return 0, io.EOF
	}
	n := copy(p, m.data[m.off:])
	m.off += int64(n)
	return n, nil
}

func (m *memReadSeekCloser) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		m.off = off
	case io.SeekCurrent:
		m.off += off
	case io.SeekEnd:
		m.off = int64(len(m.data)) + off
	}
	if m.off < 0 {
		m.off = 0
	}
	return m.off, nil
}

func (m *memReadSeekCloser) Close() error { return nil }

// init registers the "s3" backend.
func init() {
	store.RegisterBlobBackend("s3", newFromEnv)
}

// newFromEnv builds a Store from the environment. dir is unused (object storage
// has no root path).
func newFromEnv(_ string) (artifactkit.BlobStore, error) {
	cfg := Config{
		Endpoint:  os.Getenv("ARTIFACT_S3_ENDPOINT"),
		Bucket:    os.Getenv("ARTIFACT_S3_BUCKET"),
		Region:    os.Getenv("ARTIFACT_S3_REGION"),
		AccessKey: os.Getenv("ARTIFACT_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("ARTIFACT_S3_SECRET_KEY"),
		Prefix:    os.Getenv("ARTIFACT_S3_PREFIX"),
		PathStyle: os.Getenv("ARTIFACT_S3_PATH_STYLE") != "0",
	}
	return New(cfg)
}
