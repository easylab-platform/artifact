package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"gorm.io/gorm/clause"

	"github.com/easylab-platform/artifact/core"
)

// GORM models for the credential tables. Tokens are stored as SHA-256 hashes;
// the plaintext exists only at seed/creation time.
type authUserRow struct {
	ID       int64  `gorm:"primaryKey;autoIncrement"`
	Username string `gorm:"not null;uniqueIndex"`
}

type authTokenRow struct {
	ID      int64  `gorm:"primaryKey;autoIncrement"`
	Token   string `gorm:"not null;uniqueIndex"` // sha256 hex of the credential
	UserID  int64  `gorm:"not null"`
	Level   string `gorm:"not null;default:'write'"`
	Created int64  `gorm:"not null"`
}

func (authUserRow) TableName() string  { return "auth_users" }
func (authTokenRow) TableName() string { return "auth_tokens" }

// hashCredential derives the storage form of a token: hex(SHA-256(token)).
func hashCredential(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateAuthUser inserts a user (idempotent on the username).
func (s *Store) CreateAuthUser(ctx context.Context, username string) (int64, error) {
	row := &authUserRow{Username: username}
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "username"}},
		DoNothing: true,
	}).Create(row).Error
	if err != nil {
		return 0, err
	}
	// Fetch the id whether it existed or not.
	var got authUserRow
	if err := s.db.WithContext(ctx).Where("username=?", username).First(&got).Error; err != nil {
		return 0, err
	}
	return got.ID, nil
}

// CreateAuthToken registers a credential for a user. The plaintext is hashed
// on the way in and never persisted. level is "read" or "write".
func (s *Store) CreateAuthToken(ctx context.Context, token string, userID int64, level string) error {
	if level != string(artifactkit.LevelRead) && level != string(artifactkit.LevelWrite) {
		return fmt.Errorf("invalid level %q (read|write)", level)
	}
	row := &authTokenRow{Token: hashCredential(token), UserID: userID, Level: level, Created: time.Now().UTC().UnixMilli()}
	return s.db.WithContext(ctx).Create(row).Error
}

// LookupToken implements artifactkit.TokenStore.
func (s *Store) LookupToken(ctx context.Context, token string) (artifactkit.Principal, bool) {
	var row authTokenRow
	err := s.db.WithContext(ctx).Where("token=?", hashCredential(token)).First(&row).Error
	if err != nil {
		return artifactkit.Principal{}, false
	}
	var user authUserRow
	name := fmt.Sprintf("user:%d", row.UserID)
	if err := s.db.WithContext(ctx).Where("id=?", row.UserID).First(&user).Error; err == nil {
		name = user.Username
	}
	return artifactkit.Principal{Username: name, Level: artifactkit.TokenLevel(row.Level)}, true
}

// OpenInstance implements artifactkit.TokenStore: no users registered means
// a fresh single-user deployment where anonymous write is permitted.
func (s *Store) OpenInstance(ctx context.Context) bool {
	var n int64
	if err := s.db.WithContext(ctx).Model(&authUserRow{}).Count(&n).Error; err != nil {
		return false
	}
	return n == 0
}

// AuthTokenCount returns the number of registered credentials (diagnostics).
func (s *Store) AuthTokenCount(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&authTokenRow{}).Count(&n).Error
	return n, err
}

// LookupUsername implements artifactkit.TokenStore: the strongest credential
// level registered for the user (write wins).
func (s *Store) LookupUsername(ctx context.Context, username string) (artifactkit.Principal, bool) {
	var user authUserRow
	if err := s.db.WithContext(ctx).Where("username=?", username).First(&user).Error; err != nil {
		return artifactkit.Principal{}, false
	}
	var rows []authTokenRow
	if err := s.db.WithContext(ctx).Where("user_id=?", user.ID).Find(&rows).Error; err != nil || len(rows) == 0 {
		return artifactkit.Principal{}, false
	}
	level := artifactkit.LevelRead
	for _, r := range rows {
		if r.Level == string(artifactkit.LevelWrite) {
			level = artifactkit.LevelWrite
			break
		}
	}
	return artifactkit.Principal{Username: username, Level: level}, true
}
