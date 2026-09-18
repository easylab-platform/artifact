package store

import (
	"context"
	"encoding/json"

	"github.com/easylab-platform/artifact/targets"
	"gorm.io/gorm/clause"
)

// ListTargets implements targets.Store over the metadata DB.
func (s *Store) ListTargets(ctx context.Context) ([]targets.Target, error) {
	var rows []targetRow
	if err := s.db.Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]targets.Target, 0, len(rows))
	for i := range rows {
		var t targets.Target
		if err := json.Unmarshal(rows[i].JSON, &t); err != nil {
			continue // a corrupt row must not take the whole registry down
		}
		out = append(out, t)
	}
	return out, nil
}

// PutTarget implements targets.Store (upsert by ID).
func (s *Store) PutTarget(ctx context.Context, t targets.Target) error {
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		UpdateAll: true,
	}).Create(&targetRow{ID: t.ID, JSON: data}).Error
}

// DeleteTarget implements targets.Store.
func (s *Store) DeleteTarget(ctx context.Context, id string) error {
	return s.db.Where("id=?", id).Delete(&targetRow{}).Error
}

// ensure targets.Store is satisfied.
var _ targets.Store = (*Store)(nil)
