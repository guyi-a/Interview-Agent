package repository

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

type PendingApprovalRepo struct {
	db *gorm.DB
}

func NewPendingApprovalRepo(db *gorm.DB) *PendingApprovalRepo {
	return &PendingApprovalRepo{db: db}
}

// Insert is idempotent by (conversation_id, interrupt_id): a duplicate write
// (e.g. from a retry of Record) is a no-op instead of a UNIQUE-violation
// error the caller has to special-case.
func (r *PendingApprovalRepo) Insert(ctx context.Context, row *model.PendingApproval) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error
}

// DeleteByInterruptID always scopes by conversation_id — an interrupt id
// alone is not authoritative (see PendingApproval doc comment).
func (r *PendingApprovalRepo) DeleteByInterruptID(ctx context.Context, convID, interruptID string) error {
	return r.db.WithContext(ctx).
		Where("conversation_id = ? AND interrupt_id = ?", convID, interruptID).
		Delete(&model.PendingApproval{}).Error
}

func (r *PendingApprovalRepo) DeleteByConversationID(ctx context.Context, convID string) error {
	return r.db.WithContext(ctx).
		Where("conversation_id = ?", convID).
		Delete(&model.PendingApproval{}).Error
}

func (r *PendingApprovalRepo) ListByConversationID(ctx context.Context, convID string) ([]model.PendingApproval, error) {
	var rows []model.PendingApproval
	err := r.db.WithContext(ctx).
		Where("conversation_id = ?", convID).
		Order("created_at ASC").
		Find(&rows).Error
	return rows, err
}

func (r *PendingApprovalRepo) CountByConversationID(ctx context.Context, convID string) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).
		Model(&model.PendingApproval{}).
		Where("conversation_id = ?", convID).
		Count(&n).Error
	return n, err
}

// ListAll is used at startup to hydrate the in-memory PendingStore. Rows are
// ordered by CreatedAt ASC so multiple pending items in the same conversation
// come back in arrival order.
func (r *PendingApprovalRepo) ListAll(ctx context.Context) ([]model.PendingApproval, error) {
	var rows []model.PendingApproval
	err := r.db.WithContext(ctx).
		Order("created_at ASC").
		Find(&rows).Error
	return rows, err
}
