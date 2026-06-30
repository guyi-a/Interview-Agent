package model

import "time"

type Conversation struct {
	ID        string    `gorm:"primaryKey;type:varchar(64)"`
	Title     string    `gorm:"type:varchar(255)"`
	Status    string    `gorm:"type:varchar(20);default:'active';index"`
	Pinned    bool      `gorm:"default:false"`
	CreatedAt time.Time
	UpdatedAt time.Time `gorm:"index"`
}

func (Conversation) TableName() string {
	return "conversations"
}
