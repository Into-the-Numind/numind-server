package model

import (
	"time"

	"gorm.io/gorm"
)

// User 用户表
type User struct {
	// 用户相关字段
	gorm.Model
	OpenID     string `gorm:"uniqueIndex;size:50" json:"openid"`
	Phone      string `gorm:"size:20;index" json:"phone"`
	Nickname   string `gorm:"size:100" json:"nickname"`
	AvatarURL  string `gorm:"size:255" json:"avatar_url"`
	IsPro      bool   `gorm:"default:false" json:"is_pro"`
	BookNum    int    `gorm:"default:0" json:"book_num"`
	BookAllNum int64  `gorm:"default:0" json:"book_all_num"` // 状态为非failed的书本数量
	CardNum    int    `gorm:"default:0" json:"card_num"`
	ChatNum    int    `gorm:"default:0" json:"chat_num"`

	// 管理员相关字段
	Username  string     `gorm:"size:50;uniqueIndex" json:"username,omitempty"`
	Password  string     `gorm:"size:255" json:"-"`
	IsAdmin   bool       `gorm:"default:false" json:"is_admin,omitempty"`
	Status    int        `gorm:"default:0" json:"status,omitempty"`
	LastLogin *time.Time `json:"last_login,omitempty"`

	// 关联关系
	//Articles        []ArticleM        `gorm:"foreignKey:UserID" json:"articles,omitempty"`
	Favorites []Favorite `gorm:"foreignKey:UserID" json:"favorites,omitempty"`
	Feedbacks []Feedback `gorm:"foreignKey:UserID" json:"feedbacks,omitempty"`
	Images    []ImageM   `gorm:"foreignKey:UserID" json:"images,omitempty"`
	Books     []BookM    `gorm:"foreignKey:UserID" json:"books,omitempty"`
}

func (User) TableName() string {
	return "user"
}
