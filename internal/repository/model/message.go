package model

import "time"

type Message struct {
	ID               uint64    `gorm:"primaryKey;autoIncrement"`
	ConversationID   string    `gorm:"type:varchar(64);index:idx_conv_seq,priority:1;not null"`
	Seq              int       `gorm:"index:idx_conv_seq,priority:2;not null"`
	Role             string    `gorm:"type:varchar(20);not null"`
	Content          string    `gorm:"type:text"`
	ReasoningContent string    `gorm:"type:text"`
	ToolCalls        string    `gorm:"type:text"`
	ToolCallID       string    `gorm:"type:varchar(64)"`
	ToolName         string    `gorm:"type:varchar(128)"`
	Extra            string    `gorm:"type:text"`
	CreatedAt        time.Time
}

func (Message) TableName() string {
	return "messages"
}
