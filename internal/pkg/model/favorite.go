package model

import (
	"time"
)

// Favorite 收藏表
type Favorite struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"index" json:"user_id"`
	ArticleID uint      `gorm:"index" json:"article_id"`
	CreatedAt time.Time `json:"created_at"`

	// 关联关系
	User    User    `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Article Article `gorm:"foreignKey:ArticleID" json:"article,omitempty"`
}

func (Favorite) TableName() string {
	return "favorites"
}
