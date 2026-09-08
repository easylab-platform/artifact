package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/easylab-platform/artifact/core"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// Store is a metadata (IndexStore) engine backed by GORM, so it can run on
// sqlite (default, via glebarez/sqlite which wraps modernc — no cgo), postgres
// (pgx), or mysql/mariadb (go-sql-driver) with one code path. Blob bytes live
// separately (see FileBlobStore / the blob factory), never inline here.
type Store struct {
	db *gorm.DB
}

// DriverConfig selects the metadata backend. Kind is "sqlite", "postgres", or
// "mysql"/"mariadb" (aliased to mysql). It shares EASYVCS_DB_DRIVER / DSN with
// the easyvcs engine so one deployment configures both.
type DriverConfig struct {
	Kind string
	DSN  string
}

// OpenStore opens the metadata store on the configured backend. The semantic
// layer (pkrkit protocol handlers) is agnostic — only this boundary changes.
// sqlite uses the same pure-Go dialector (glebarez/sqlite) as easyvcs, so the
// "sqlite" database/sql driver is registered exactly once per process.
func OpenStore(cfg DriverConfig) (*Store, error) {
	kind := normalizeKind(cfg.Kind)
	if kind == "" {
		return nil, fmt.Errorf("unsupported db driver %q (sqlite|postgres|mysql)", cfg.Kind)
	}
	dsn := cfg.DSN
	if kind == KindSQLite {
		if dsn == "" {
			return nil, fmt.Errorf("sqlite metadata requires a DSN (path)")
		}
		if dir := filepath.Dir(dsn); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, err
			}
		}
	}
	db, err := gorm.Open(dialector(kind, dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(&artifactRow{}, &uploadRow{}, &metaRow{}); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// OpenSQLite opens the metadata store at a SQLite path (legacy helper; the
// convenient wrapper over OpenStore for the default backend).
func OpenSQLite(path string) (*Store, error) {
	return OpenStore(DriverConfig{Kind: KindSQLite, DSN: path})
}

// DB exposes the underlying handle so callers may coordinate with a blob store
// when needed. Kept for compatibility; blob stores no longer reuse it by
// default (blob lives on the filesystem / S3).
func (s *Store) DB() *gorm.DB { return s.db }

// Close implements IndexStore.
func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// Put implements IndexStore.
func (s *Store) Put(ctx context.Context, a pkrkit.Artifact) error {
	if a.Repository == "" {
		return errors.New("artifact: empty repository")
	}
	blobs, err := json.Marshal(a.Blobs)
	if err != nil {
		return err
	}
	row := &artifactRow{
		Format: a.Format, Repository: a.Repository, Version: a.Version,
		MediaType: a.MediaType, Digest: a.Digest, Proprietary: a.Proprietary,
		Blobs: string(blobs), Source: a.Source,
	}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "format"}, {Name: "repository"}, {Name: "version"}},
		UpdateAll: true,
	}).Create(row).Error
}

// Get implements IndexStore.
func (s *Store) Get(ctx context.Context, format, repository, version string) (pkrkit.Artifact, error) {
	var row artifactRow
	err := s.db.Where("format=? AND repository=? AND version=?", format, repository, version).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return pkrkit.Artifact{}, pkrkit.ErrArtifactUnknown
	}
	if err != nil {
		return pkrkit.Artifact{}, err
	}
	var a pkrkit.Artifact
	_ = json.Unmarshal([]byte(row.Blobs), &a.Blobs)
	a.Format, a.Repository, a.Version = row.Format, row.Repository, row.Version
	a.MediaType, a.Digest, a.Source = row.MediaType, row.Digest, row.Source
	a.Proprietary = row.Proprietary
	return a, nil
}

// Delete implements IndexStore.
func (s *Store) Delete(ctx context.Context, format, repository, version string) error {
	res := s.db.Where("format=? AND repository=? AND version=?", format, repository, version).Delete(&artifactRow{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return pkrkit.ErrArtifactUnknown
	}
	return nil
}

// ListVersions implements IndexStore.
func (s *Store) ListVersions(ctx context.Context, format, repository string) ([]string, error) {
	var rows []string
	if err := s.db.Model(&artifactRow{}).Where("format=? AND repository=?", format, repository).Order("version").Pluck("version", &rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ListRepositoriesByFormat implements IndexStore.
func (s *Store) ListRepositoriesByFormat(ctx context.Context, format string) ([]string, error) {
	var rows []string
	if err := s.db.Model(&artifactRow{}).Where("format=?", format).Distinct().Order("repository").Pluck("repository", &rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ListRepositories implements IndexStore.
func (s *Store) ListRepositories(ctx context.Context) ([]string, error) {
	var rows []string
	if err := s.db.Model(&artifactRow{}).Distinct().Order("repository").Pluck("repository", &rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ListPackages implements IndexStore.
func (s *Store) ListPackages(ctx context.Context) ([]pkrkit.PackageSummary, error) {
	var rows []artifactRow
	if err := s.db.Order("format, repository, version").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]pkrkit.PackageSummary, 0, len(rows))
	for i := range rows {
		// size approximated by blob count (kept minimal for the sketch).
		var des []json.RawMessage
		_ = json.Unmarshal([]byte(rows[i].Blobs), &des)
		out = append(out, pkrkit.PackageSummary{
			Format: rows[i].Format, Repository: rows[i].Repository, Version: rows[i].Version,
			MediaType: rows[i].MediaType, Digest: rows[i].Digest, Size: int64(len(des)),
		})
	}
	return out, nil
}

// DeleteRepo implements IndexStore.
func (s *Store) DeleteRepo(ctx context.Context, format, repository string) (int, error) {
	res := s.db.Where("format=? AND repository=?", format, repository).Delete(&artifactRow{})
	if res.Error != nil {
		return 0, res.Error
	}
	return int(res.RowsAffected), nil
}

// SaveUpload implements IndexStore.
func (s *Store) SaveUpload(ctx context.Context, u pkrkit.UploadRecord) error {
	row := &uploadRow{ID: u.ID, Format: u.Format, Repository: u.Repository, Digest: u.Digest, Bytes: u.Bytes, Complete: boolToInt(u.Complete)}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		UpdateAll: true,
	}).Create(row).Error
}

// GetUpload implements IndexStore.
func (s *Store) GetUpload(ctx context.Context, id string) (pkrkit.UploadRecord, error) {
	var row uploadRow
	err := s.db.Where("id=?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return pkrkit.UploadRecord{}, pkrkit.ErrUploadUnknown
	}
	if err != nil {
		return pkrkit.UploadRecord{}, err
	}
	return pkrkit.UploadRecord{ID: row.ID, Format: row.Format, Repository: row.Repository, Digest: row.Digest, Bytes: row.Bytes, Complete: row.Complete != 0}, nil
}

// DeleteUpload implements IndexStore.
func (s *Store) DeleteUpload(ctx context.Context, id string) error {
	res := s.db.Where("id=?", id).Delete(&uploadRow{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return pkrkit.ErrUploadUnknown
	}
	return nil
}

// ListUploads implements IndexStore.
func (s *Store) ListUploads(ctx context.Context) ([]string, error) {
	var rows []string
	if err := s.db.Model(&uploadRow{}).Pluck("id", &rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// GetMeta implements IndexStore.
func (s *Store) GetMeta(ctx context.Context, format, repository string) ([]byte, error) {
	var row metaRow
	err := s.db.Where("format=? AND repository=?", format, repository).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("%s/%s: %w", format, repository, pkrkit.ErrArtifactUnknown)
	}
	if err != nil {
		return nil, err
	}
	return row.Data, nil
}

// SetMeta implements IndexStore.
func (s *Store) SetMeta(ctx context.Context, format, repository string, data []byte) error {
	row := &metaRow{Format: format, Repository: repository, Data: data}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "format"}, {Name: "repository"}},
		UpdateAll: true,
	}).Create(row).Error
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// normalizeKind / dialector / Kind constants are shared with the metadata
// backend helpers below so both store and blob seams use the same config.
func normalizeKind(k string) string {
	switch strings.ToLower(strings.TrimSpace(k)) {
	case KindSQLite:
		return KindSQLite
	case KindPostgres, "postgresql":
		return KindPostgres
	case KindMySQL, "mariadb", "maria":
		return KindMySQL
	default:
		return ""
	}
}
