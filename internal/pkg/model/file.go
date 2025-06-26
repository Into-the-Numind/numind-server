package model

import "time"

type File struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"index" json:"user_id"`
	FileName  string    `gorm:"size:255;not null" json:"file_name"`
	FileURL   string    `gorm:"size:512;not null" json:"file_url"`
	FileType  string    `gorm:"size:50" json:"file_type"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}

func (File) TableName() string {
	return "files"
}
