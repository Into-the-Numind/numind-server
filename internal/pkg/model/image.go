package model

import (
	"gorm.io/gorm"
)

// ImageM 用户上传的原始图片表
type ImageM struct {
	gorm.Model
	UserID      uint   `gorm:"index;not null" json:"user_id"`            // 上传用户ID
	BookID      *uint  `gorm:"index" json:"book_id"`                     // 关联的笔记ID
	OriginalURL string `gorm:"size:512;not null" json:"original_url"`    // 原始图片URL（COS链接）
	ThumbURL    string `gorm:"size:512" json:"thumb_url"`                // 缩略图URL（COS链接）
	FileName    string `gorm:"size:255;not null" json:"file_name"`       // 文件名
	FileSize    int64  `gorm:"not null" json:"file_size"`                // 文件大小(字节)
	ImageType   string `gorm:"size:100" json:"image_type"`               // 图片类型
	Width       int    `json:"width"`                                    // 图片宽度
	Height      int    `json:"height"`                                   // 图片高度
	Status      string `gorm:"size:20;default:'uploaded'" json:"status"` // 状态: uploaded, processing, processed, failed

	// 关联关系
	User User   `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Book *BookM `gorm:"foreignKey:BookID" json:"book,omitempty"` // 关联的笔记
}

func (ImageM) TableName() string {
	return "images"
}
