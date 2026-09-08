package store

import (
	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Metadata backend kinds (shared with easyvcs).
const (
	KindSQLite   = "sqlite"
	KindPostgres = "postgres"
	KindMySQL    = "mysql"
)

// dialector returns the GORM dialector for a backend kind. sqlite uses the
// pure-Go glebarez/sqlite dialector (which wraps modernc, registering the
// "sqlite" database/sql driver exactly once) so easyvcs and artifact share one
// driver registration per process — avoiding "Register called twice".
func dialector(kind, dsn string) gorm.Dialector {
	switch kind {
	case KindPostgres:
		return postgres.Open(dsn)
	case KindMySQL:
		return mysql.Open(dsn)
	default:
		return sqlite.Open(dsn)
	}
}


// GORM models for the metadata index. Table names are pinned to the legacy
// schema so any existing raw-SQL callers remain consistent.
type artifactRow struct {
	Format      string `gorm:"primaryKey;not null"`
	Repository  string `gorm:"primaryKey;not null"`
	Version     string `gorm:"primaryKey;not null"`
	MediaType   string `gorm:"not null;default:''"`
	Digest      string `gorm:"not null;default:''"`
	Proprietary []byte
	Blobs       string `gorm:"not null;default:'[]'"`
	Source      string `gorm:"not null;default:''"`
}

type uploadRow struct {
	ID         string `gorm:"primaryKey;not null"`
	Format     string `gorm:"not null"`
	Repository string `gorm:"not null"`
	Digest     string `gorm:"not null;default:''"`
	Bytes      int64  `gorm:"not null;default:0"`
	Complete   int    `gorm:"not null;default:0"`
}

type metaRow struct {
	Format     string `gorm:"primaryKey;not null"`
	Repository string `gorm:"primaryKey;not null"`
	Data       []byte
}

func (artifactRow) TableName() string { return "artifacts" }
func (uploadRow) TableName() string   { return "uploads" }
func (metaRow) TableName() string     { return "meta" }

