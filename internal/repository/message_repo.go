package repository

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

type MessageRepo struct {
	db *gorm.DB
}

func NewMessageRepo(db *gorm.DB) *MessageRepo {
	return &MessageRepo{db: db}
}

// Append inserts a message with the next seq for the conversation.
// Uses a transaction to compute seq + insert atomically (avoids races within the same process).
func (r *MessageRepo) Append(ctx context.Context, m *model.Message) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var maxSeq int
		if err := tx.Model(&model.Message{}).
			Where("conversation_id = ?", m.ConversationID).
			Select("COALESCE(MAX(seq), 0)").
			Scan(&maxSeq).Error; err != nil {
			return err
		}
		m.Seq = maxSeq + 1
		if m.CreatedAt.IsZero() {
			m.CreatedAt = time.Now()
		}
		return tx.Create(m).Error
	})
}

// List returns all messages of a conversation in seq order.
func (r *MessageRepo) List(ctx context.Context, conversationID string) ([]model.Message, error) {
	var out []model.Message
	err := r.db.WithContext(ctx).
		Where("conversation_id = ?", conversationID).
		Order("seq ASC").
		Find(&out).Error
	return out, err
}
