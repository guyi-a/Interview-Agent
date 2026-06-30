package model

import "time"

type Project struct {
	ID        string `gorm:"primaryKey;type:varchar(64)"`
	Name      string `gorm:"type:varchar(255)"`
	Workspace string `gorm:"type:varchar(512);not null"`
	CreatedAt time.Time
	UpdatedAt time.Time `gorm:"index"`
}

func (Project) TableName() string {
	return "projects"
}
