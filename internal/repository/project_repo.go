package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

type ProjectRepo struct {
	db *gorm.DB
}

func NewProjectRepo(db *gorm.DB) *ProjectRepo {
	return &ProjectRepo{db: db}
}

func (r *ProjectRepo) Get(ctx context.Context, id string) (*model.Project, error) {
	var p model.Project
	if err := r.db.WithContext(ctx).First(&p, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

func (r *ProjectRepo) Create(ctx context.Context, p *model.Project) error {
	now := time.Now()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	return r.db.WithContext(ctx).Create(p).Error
}

func (r *ProjectRepo) List(ctx context.Context) ([]model.Project, error) {
	var out []model.Project
	err := r.db.WithContext(ctx).Order("updated_at DESC").Find(&out).Error
	return out, err
}
