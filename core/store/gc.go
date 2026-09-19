package store

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/easylab-platform/artifact/core"
)

// GCStats summarizes one garbage-collection pass.
type GCStats struct {
	NegativeExpired int   // negative (404/410) rows removed
	OrphanBlobs     int   // CAS blobs with no referencing row removed
	BlobsKept       int   // blobs still referenced
	BytesFreed      int64 // bytes reclaimed from orphan blobs
}

// ExpireNegative removes netcache negative (404/410) rows whose TTL has passed.
// These hold no bytes (the blob is empty), so deleting them never violates
// "fetch once" — a later request re-probes the origin, as intended.
func (s *Store) ExpireNegative(ctx context.Context, now time.Time) (int, error) {
	res := s.db.Where("media_type = ? AND expires_at > 0 AND expires_at < ?",
		negativeMediaType, now.Unix()).Delete(&artifactRow{})
	if res.Error != nil {
		return 0, res.Error
	}
	return int(res.RowsAffected), nil
}

// referencedDigests returns every CAS digest referenced by any artifact row
// (both the primary Digest and each blob descriptor). One pass over the table
// serves the whole GC cycle, so orphan detection is O(rows) total rather than
// O(rows) per candidate blob.
func (s *Store) referencedDigests(ctx context.Context) (map[string]bool, error) {
	var rows []artifactRow
	if err := s.db.Select("digest", "blobs").Find(&rows).Error; err != nil {
		return nil, err
	}
	refs := make(map[string]bool, len(rows))
	for i := range rows {
		if rows[i].Digest != "" {
			refs[rows[i].Digest] = true
		}
		var des []artifactkit.Descriptor
		if err := json.Unmarshal([]byte(rows[i].Blobs), &des); err != nil {
			continue
		}
		for _, d := range des {
			if d.Digest != "" {
				refs[d.Digest] = true
			}
		}
	}
	return refs, nil
}

// ReapOrphanBlobs deletes CAS blobs that no artifact row references and whose
// file is older than grace (so a blob being written by an in-flight fetch is
// never removed). A non-positive grace uses a 1h default. minAge protects
// recently-added blobs from a race with a concurrent Put that has not yet been
// indexed.
func (s *Store) ReapOrphanBlobs(ctx context.Context, blobs artifactkit.BlobStore, grace time.Duration) (GCStats, error) {
	var st GCStats
	if grace <= 0 {
		grace = time.Hour
	}
	refs, err := s.referencedDigests(ctx)
	if err != nil {
		return st, err
	}
	all, err := blobs.List(ctx)
	if err != nil {
		return st, err
	}
	cutoff := time.Now().Add(-grace)
	aged, ok := blobs.(BlobAger)
	for _, digest := range all {
		if refs[digest] {
			st.BlobsKept++
			continue
		}
		// Only delete blobs old enough that no in-flight write could still be
		// about to index them. Without an age source, be conservative and skip.
		if !ok {
			continue
		}
		mod, err := aged.ModTime(ctx, digest)
		if err != nil || mod.IsZero() || mod.After(cutoff) {
			continue
		}
		if sz, _ := blobs.Stat(ctx, digest); sz != nil {
			st.BytesFreed += *sz
		}
		if err := blobs.Delete(ctx, digest); err != nil {
			continue
		}
		st.OrphanBlobs++
	}
	return st, nil
}

// Stats summarizes the registry's on-disk footprint: row counts and bytes per
// format, and the CAS totals. It backs the admin /stats endpoint so an operator
// can see growth without scanning the filesystem by hand.
type Stats struct {
	Formats []FormatStat `json:"formats"`
	Blobs   BlobStat     `json:"blobs"`
}

// FormatStat is one format's row count and referenced bytes.
type FormatStat struct {
	Format  string `json:"format"`
	Rows    int64  `json:"rows"`
	Blobs   int64  `json:"blobs"` // distinct digests referenced
	Bytes   int64  `json:"bytes"`
	Expired int64  `json:"expired"` // mutable entries past ExpiresAt
}

// BlobStat is the CAS-wide total.
type BlobStat struct {
	Objects int64 `json:"objects"`
	Bytes   int64 `json:"bytes"`
}

// Stats computes the registry footprint. The per-format "bytes" sums each
// distinct digest once per format (shared CAS bytes counted per referencing
// format, which is what an operator wants for attribution).
func (s *Store) Stats(ctx context.Context, blobs artifactkit.BlobStore) (Stats, error) {
	var rows []artifactRow
	if err := s.db.Select("format", "digest", "blobs", "expires_at").Find(&rows).Error; err != nil {
		return Stats{}, err
	}
	now := time.Now().Unix()
	byFormat := map[string]*FormatStat{}
	seenDigest := map[string]map[string]bool{} // format -> digest set
	sizeOf := func(d string) int64 {
		if sz, _ := blobs.Stat(ctx, d); sz != nil {
			return *sz
		}
		return 0
	}
	add := func(format, digest string) {
		if digest == "" {
			return
		}
		set := seenDigest[format]
		if set == nil {
			set = map[string]bool{}
			seenDigest[format] = set
		}
		if set[digest] {
			return
		}
		set[digest] = true
		byFormat[format].Blobs++
		byFormat[format].Bytes += sizeOf(digest)
	}
	for i := range rows {
		f := rows[i].Format
		fs := byFormat[f]
		if fs == nil {
			fs = &FormatStat{Format: f}
			byFormat[f] = fs
		}
		fs.Rows++
		if rows[i].ExpiresAt > 0 && rows[i].ExpiresAt < now {
			fs.Expired++
		}
		add(f, rows[i].Digest)
		var des []artifactkit.Descriptor
		if json.Unmarshal([]byte(rows[i].Blobs), &des) == nil {
			for _, d := range des {
				add(f, d.Digest)
			}
		}
	}
	var out Stats
	for _, fs := range byFormat {
		out.Formats = append(out.Formats, *fs)
	}
	sort.Slice(out.Formats, func(i, j int) bool { return out.Formats[i].Format < out.Formats[j].Format })
	all, _ := blobs.List(ctx)
	out.Blobs.Objects = int64(len(all))
	for _, d := range all {
		out.Blobs.Bytes += sizeOf(d)
	}
	return out, nil
}

// BlobAger is an optional BlobStore capability: the modification time of a
// blob, used to avoid deleting blobs that may be mid-write. A backend without
// it disables orphan reaping (safe default).
type BlobAger interface {
	ModTime(ctx context.Context, digest string) (time.Time, error)
}
