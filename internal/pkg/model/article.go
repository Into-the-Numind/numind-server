package model

import (
	"time"
)

// ArticleM 文章表
type ArticleM struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	UserID           uint      `gorm:"index;not null" json:"user_id"`
	URL              string    `gorm:"size:255;index" json:"url"`
	Title            string    `gorm:"size:255" json:"title"`
	AccountName      string    `gorm:"size:100" json:"account_name"`
	PublishTime      string    `gorm:"size:50" json:"publish_time"`
	CategoryID       *uint     `gorm:"index" json:"category_id"`
	Content          JSON      `gorm:"type:json" json:"content"`
	ContentTxt       string    `gorm:"type:longtext" json:"content_txt"`
	Summary          string    `gorm:"type:longtext" json:"summary"`
	AnnotatedContent string    `gorm:"type:longtext" json:"annotated_content"`
	CreatedAt        time.Time `json:"created_at"`
	CategoryAt       time.Time `json:"category_at"`

	// 关联关系
	User      User       `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Category  *CategoryM `gorm:"foreignKey:CategoryID" json:"category,omitempty"`
	Favorites []Favorite `gorm:"foreignKey:ArticleID" json:"favorites,omitempty"`
}

func (ArticleM) TableName() string {
	return "articles"
}
