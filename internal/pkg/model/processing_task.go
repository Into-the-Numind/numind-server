package model

import (
	"time"

	"gorm.io/gorm"
)

// ProcessingTaskM AI处理任务表
type ProcessingTaskM struct {
	gorm.Model
	ID          uint       `gorm:"primaryKey" json:"id"`
	UserID      uint       `gorm:"index;not null" json:"user_id"`           // 用户ID
	ImageID     uint       `gorm:"index;not null" json:"image_id"`          // 图片ID
	BookID      uint       `gorm:"index" json:"book_id"`                    // 卡册ID
	TaskType    string     `gorm:"size:50;not null" json:"task_type"`       // 任务类型: ocr, ai_process, card_generate
	Status      string     `gorm:"size:20;default:'pending'" json:"status"` // 状态: pending, processing, completed, failed
	Progress    int        `gorm:"default:0" json:"progress"`               // 进度百分比 0-100
	Result      string     `gorm:"type:text" json:"result"`                 // 处理结果(JSON格式)
	ErrorMsg    string     `gorm:"size:500" json:"error_msg"`               // 错误信息
	StartedAt   *time.Time `json:"started_at"`                              // 开始时间
	CompletedAt *time.Time `json:"completed_at"`                            // 完成时间
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`

	// 关联关系
	User  User   `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Image ImageM `gorm:"foreignKey:ImageID" json:"image,omitempty"`
	Book  BookM  `gorm:"foreignKey:BookID" json:"book,omitempty"`
}

func (ProcessingTaskM) TableName() string {
	return "processing_task"
}
