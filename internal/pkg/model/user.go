package model

import (
	"time"

	"gorm.io/gorm"
)

// User 用户表
type User struct {
	// 用户相关字段
	gorm.Model
	OpenID    string `gorm:"uniqueIndex;size:50" json:"openid"`
	Phone     string `gorm:"size:20;index" json:"phone"`
	Nickname  string `gorm:"size:100" json:"nickname"`
	AvatarURL string `gorm:"size:255" json:"avatar_url"`
	IsActive  bool   `gorm:"default:true" json:"is_active"`
	IsPro     bool   `gorm:"default:false" json:"is_pro"`
	BookNum   int    `gorm:"default:0" json:"book_num"`

	// 管理员相关字段
	Username  string     `gorm:"size:50;uniqueIndex" json:"username"`
	Password  string     `gorm:"size:255" json:"-"`
	IsAdmin   bool       `gorm:"default:false" json:"is_admin"`
	Status    int        `gorm:"default:0" json:"status"`
	LastLogin *time.Time `json:"last_login"`

	// 关联关系
	//Articles        []ArticleM        `gorm:"foreignKey:UserID" json:"articles,omitempty"`
	Favorites       []Favorite        `gorm:"foreignKey:UserID" json:"favorites,omitempty"`
	Feedbacks       []Feedback        `gorm:"foreignKey:UserID" json:"feedbacks,omitempty"`
	Images          []ImageM          `gorm:"foreignKey:UserID" json:"images,omitempty"`
	Books           []BookM           `gorm:"foreignKey:UserID" json:"books,omitempty"`
	ProcessingTasks []ProcessingTaskM `gorm:"foreignKey:UserID" json:"processing_tasks,omitempty"`
}

func (User) TableName() string {
	return "user"
}
