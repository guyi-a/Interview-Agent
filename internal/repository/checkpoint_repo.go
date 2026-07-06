package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

type CheckpointRepo struct {
	db *gorm.DB
}

func NewCheckpointRepo(db *gorm.DB) *CheckpointRepo {
	return &CheckpointRepo{db: db}
}

func (r *CheckpointRepo) Get(ctx context.Context, id string) ([]byte, bool, error) {
	var cp model.Checkpoint
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&cp).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return cp.Data, true, nil
}

// Set upserts by id — a resumed run overwrites the previous checkpoint at
// the same id as state advances.
func (r *CheckpointRepo) Set(ctx context.Context, id string, data []byte) error {
	cp := model.Checkpoint{ID: id, Data: data}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"data", "updated_at"}),
	}).Create(&cp).Error
}

func (r *CheckpointRepo) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&model.Checkpoint{}).Error
}
