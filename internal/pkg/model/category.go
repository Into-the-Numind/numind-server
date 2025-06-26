package model

import "time"

// Category 文章分类表
type Category struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"size:50;uniqueIndex;not null" json:"name"`
	Description string    `gorm:"size:255" json:"description"`
	CreatedAt   time.Time `json:"created_at"`

	// 关联关系
	Articles []Article `gorm:"foreignKey:CategoryID" json:"articles,omitempty"`
}

func (Category) TableName() string {
	return "categories"
}
