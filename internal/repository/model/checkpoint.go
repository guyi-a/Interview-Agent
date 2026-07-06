package model

import "time"

type Checkpoint struct {
	ID        string    `gorm:"primaryKey;type:varchar(128)"`
	Data      []byte    `gorm:"type:blob;not null"`
	UpdatedAt time.Time `gorm:"index"`
}

func (Checkpoint) TableName() string {
	return "checkpoints"
}
