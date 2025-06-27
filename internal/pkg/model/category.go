package model

import "time"

// CategoryM 文章分类表
type CategoryM struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"size:50;uniqueIndex;not null" json:"name"`
	Description string    `gorm:"size:255" json:"description"`
	CreatedAt   time.Time `json:"created_at"`

	// 关联关系
	Articles []ArticleM `gorm:"foreignKey:CategoryID" json:"articles,omitempty"`
}

func (CategoryM) TableName() string {
	return "category"
}
