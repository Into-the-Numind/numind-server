package model

import (
	"gorm.io/gorm"
)

// ImageM 用户上传的原始图片表
type ImageM struct {
	gorm.Model
	UserID      uint   `gorm:"index;not null" json:"user_id"`            // 上传用户ID
	OriginalURL string `gorm:"size:512;not null" json:"original_url"`    // 原始图片URL
	ThumbURL    string `gorm:"size:512" json:"thumb_url"`                // 缩略图URL
	FileName    string `gorm:"size:255;not null" json:"file_name"`       // 文件名
	FileSize    int64  `gorm:"not null" json:"file_size"`                // 文件大小(字节)
	MimeType    string `gorm:"size:100" json:"mime_type"`                // 文件类型
	Width       int    `json:"width"`                                    // 图片宽度
	Height      int    `json:"height"`                                   // 图片高度
	Status      string `gorm:"size:20;default:'uploaded'" json:"status"` // 状态: uploaded, processing, processed, failed
	ProcessMsg  string `gorm:"type:text" json:"process_msg"`             // 处理信息

	// 关联关系
	User  User    `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Cards []CardM `gorm:"many2many:card_rel_image;joinForeignKey:ImageID;JoinReferences:CardID" json:"cards,omitempty"`
}

func (ImageM) TableName() string {
	return "images"
}
