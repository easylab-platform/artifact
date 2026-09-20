// Package s3blob implements artifactkit's BlobStore on S3-compatible object
// storage (AWS S3, MinIO, Ceph RGW). It lives in its own module so the heavy
// aws-sdk-go-v2 dependency never reaches the protocol adapters (which depend
// only on core); a consumer enables it with a blank import in cmd:
//
//	import _ "github.com/easylab-platform/artifact/s3blob"
//
// The CAS layout mirrors the filesystem store: one object per digest at
// <prefix>/sha256/<first-two>/<rest>, plus a `.hashes.json` sidecar recording
// the hash set computed at write time. Blobs are immutable and content-addressed.
package s3blob

import (
	"bytes"
	"context"
	"encoding/json"
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

// hashSidecarSuffix names the persisted-hashes sidecar object for a blob.
const hashSidecarSuffix = ".hashes.json"

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
func (s *Store) Stat(ctx context.Context, digest string) (*artifactkit.BlobInfo, error) {
	k, err := s.key(digest)
	if err != nil {
		return nil, err
	}
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: aws.String(k)})
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	var size int64
	if out.ContentLength != nil {
		size = *out.ContentLength
	}
	var mod time.Time
	if out.LastModified != nil {
		mod = *out.LastModified
	}
	return &artifactkit.BlobInfo{Digest: digest, Size: size, ModTime: mod}, nil
}

// Open implements BlobStore with a LAZY ranged reader: it HEADs for the size
// and issues Range GETs on demand, so a multi-GB layer never sits in RAM. The
// reader satisfies http.ServeContent's Seek/Range contract.
func (s *Store) Open(ctx context.Context, digest string) (io.ReadSeekCloser, error) {
	k, err := s.key(digest)
	if err != nil {
		return nil, err
	}
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: aws.String(k)})
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	var size int64
	if out.ContentLength != nil {
		size = *out.ContentLength
	}
	return &rangeReader{ctx: ctx, store: s, key: k, size: size}, nil
}

// Put implements BlobStore: stream to a temp file while computing every hash in
// one pass, verify against wantDigest (when set), upload, then write the hash
// sidecar. S3 PUT cannot be replayed after a mismatch, so the body is buffered
// to disk (never RAM).
func (s *Store) Put(ctx context.Context, r io.Reader, wantDigest string) (artifactkit.Stored, bool, error) {
	if wantDigest != "" {
		if _, err := artifactkit.ParseDigest(wantDigest); err != nil {
			return artifactkit.Stored{}, false, err
		}
		if info, err := s.Stat(ctx, wantDigest); err != nil {
			return artifactkit.Stored{}, false, err
		} else if info != nil {
			h, _, _ := s.Hashes(ctx, wantDigest)
			return artifactkit.Stored{Hashes: h, Size: info.Size, Digest: wantDigest}, false, nil
		}
	}
	tmp, err := os.CreateTemp("", "s3blob-*")
	if err != nil {
		return artifactkit.Stored{}, false, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	defer func() { _ = tmp.Close() }()
	h, err := artifactkit.ComputeHashes(io.TeeReader(r, tmp))
	if err != nil {
		return artifactkit.Stored{}, false, err
	}
	got := "sha256:" + h.SHA256
	if wantDigest != "" && got != wantDigest {
		return artifactkit.Stored{}, false, fmt.Errorf("s3blob: digest mismatch: expected %s got %s", wantDigest, got)
	}
	k, err := s.key(got)
	if err != nil {
		return artifactkit.Stored{}, false, err
	}
	sz, _ := tmp.Seek(0, io.SeekEnd)
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return artifactkit.Stored{}, false, err
	}
	if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &s.bucket, Key: aws.String(k), Body: tmp,
	}); err != nil {
		return artifactkit.Stored{}, false, err
	}
	if err := s.putHashes(ctx, k, h); err != nil {
		artifactkit.LogMetaErr("hash sidecar", err)
	}
	return artifactkit.Stored{Hashes: h, Size: sz, Digest: got}, true, nil
}

// Hashes implements BlobStore: the hash set recorded at write time. ok=false
// when no sidecar exists (a blob written before this feature).
func (s *Store) Hashes(ctx context.Context, digest string) (artifactkit.Hashes, bool, error) {
	k, err := s.key(digest)
	if err != nil {
		return artifactkit.Hashes{}, false, err
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: aws.String(k + hashSidecarSuffix)})
	if err != nil {
		if isNotFound(err) {
			return artifactkit.Hashes{}, false, nil
		}
		return artifactkit.Hashes{}, false, err
	}
	defer func() { _ = out.Body.Close() }()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return artifactkit.Hashes{}, false, err
	}
	var h artifactkit.Hashes
	if json.Unmarshal(data, &h) != nil || h.SHA256 == "" {
		return artifactkit.Hashes{}, false, nil
	}
	return h, true, nil
}

// putHashes writes the hash sidecar object.
func (s *Store) putHashes(ctx context.Context, key string, h artifactkit.Hashes) error {
	data, err := json.Marshal(h)
	if err != nil {
		return err
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: &s.bucket, Key: aws.String(key + hashSidecarSuffix), Body: bytes.NewReader(data)})
	return err
}

// Delete implements BlobStore (unconditional; a missing key is not an error).
func (s *Store) Delete(ctx context.Context, digest string) error {
	k, err := s.key(digest)
	if err != nil {
		return err
	}
	_, _ = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: aws.String(k + hashSidecarSuffix)})
	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: aws.String(k)})
	return err
}

// List implements BlobStore: every blob under the prefix with size + mtime.
func (s *Store) List(ctx context.Context) ([]artifactkit.BlobInfo, error) {
	prefix := s.prefix
	if prefix != "" {
		prefix += "/"
	}
	prefix += "sha256/"
	var out []artifactkit.BlobInfo
	var token *string
	for {
		page, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: &s.bucket, Prefix: &prefix, ContinuationToken: token,
		})
		if err != nil {
			return nil, err
		}
		for _, obj := range page.Contents {
			d, ok := digestFromKey(aws.ToString(obj.Key))
			if !ok {
				continue
			}
			var size int64
			if obj.Size != nil {
				size = *obj.Size
			}
			var mod time.Time
			if obj.LastModified != nil {
				mod = *obj.LastModified
			}
			out = append(out, artifactkit.BlobInfo{Digest: d, Size: size, ModTime: mod})
		}
		if page.IsTruncated == nil || !*page.IsTruncated {
			break
		}
		token = page.NextContinuationToken
	}
	return out, nil
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

// rangeReader is a lazy io.ReadSeekCloser over an S3 object: it issues a Range
// GET per contiguous read span instead of buffering the whole object, so
// http.ServeContent's Seek/Range semantics are satisfied without RAM.
type rangeReader struct {
	ctx   context.Context
	store *Store
	key   string
	size  int64
	off   int64
	body  io.ReadCloser
}

func (r *rangeReader) Read(p []byte) (int, error) {
	if r.off >= r.size {
		return 0, io.EOF
	}
	if r.body == nil {
		out, err := r.store.client.GetObject(r.ctx, &s3.GetObjectInput{
			Bucket: &r.store.bucket, Key: aws.String(r.key),
			Range: aws.String(fmt.Sprintf("bytes=%d-", r.off)),
		})
		if err != nil {
			return 0, err
		}
		r.body = out.Body
	}
	n, err := r.body.Read(p)
	r.off += int64(n)
	if err == io.EOF && r.off < r.size {
		// The ranged stream ended early; reopen on the next Read.
		_ = r.body.Close()
		r.body = nil
		return n, nil
	}
	return n, err
}

func (r *rangeReader) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		r.off = off
	case io.SeekCurrent:
		r.off += off
	case io.SeekEnd:
		r.off = r.size + off
	}
	if r.off < 0 {
		r.off = 0
	}
	// A seek invalidates the open stream; the next Read reopens at r.off.
	if r.body != nil {
		_ = r.body.Close()
		r.body = nil
	}
	return r.off, nil
}

func (r *rangeReader) Close() error {
	if r.body != nil {
		return r.body.Close()
	}
	return nil
}

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
