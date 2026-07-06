package model

import "time"

// PendingApproval mirrors an in-flight tool-call approval so it survives a
// process restart. Written when the middleware fires tool.Interrupt, deleted
// when the user's decision is applied (or the conversation is cleared).
// Composite unique on (conversation_id, interrupt_id) — we do not assume
// eino's interrupt id is globally unique, only unique within a checkpoint.
type PendingApproval struct {
	ID             uint      `gorm:"primaryKey;autoIncrement"`
	ConversationID string    `gorm:"size:64;not null;uniqueIndex:idx_pending_conv_int,priority:1;index"`
	InterruptID    string    `gorm:"size:128;not null;uniqueIndex:idx_pending_conv_int,priority:2"`
	CallID         string    `gorm:"size:128"`
	Tool           string    `gorm:"size:128"`
	Args           string    `gorm:"type:text"`
	CreatedAt      time.Time `gorm:"index"`
}

func (PendingApproval) TableName() string {
	return "pending_approvals"
}
