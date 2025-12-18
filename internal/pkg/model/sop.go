package model

import (
	"time"

	"gorm.io/gorm"
)

// SopTemplate SOP模板表
type SopTemplate struct {
	gorm.Model
	Name        string `gorm:"size:100;not null;index" json:"name"`
	Description string `gorm:"type:text" json:"description"`
	Status      string `gorm:"size:20;default:'active';index" json:"status"` // active, inactive
	Prompt      string `gorm:"type:text" json:"prompt"`                      // 预处理提示词，在执行第一个节点前发送
}

func (SopTemplate) TableName() string {
	return "sop_template"
}

// SopNode SOP节点表
type SopNode struct {
	gorm.Model
	TemplateID     uint   `gorm:"not null;index:idx_template_sort" json:"template_id"`
	ParentID       *uint  `gorm:"index" json:"parent_id"`                        // NULL表示根节点
	Name           string `gorm:"size:100;not null" json:"name"`                 // 节点名称
	Status         string `gorm:"size:20;default:'active'" json:"status"`        // active, inactive
	BaseURL        string `gorm:"size:255;not null" json:"base_url"`             // AI服务地址
	ModelName      string `gorm:"size:100;not null" json:"model_name"`           // 模型名称
	APIKey         string `gorm:"size:255" json:"api_key"`                       // API密钥，调用大模型时使用
	TimeoutSeconds int    `gorm:"default:60" json:"timeout_seconds"`             // 超时时间（秒）
	Sort           int    `gorm:"default:0;index:idx_template_sort" json:"sort"` // 排序，用于线性执行顺序
	IsRoot         bool   `gorm:"default:false;index" json:"is_root"`            // 是否为根节点
	Prompt         string `gorm:"type:text" json:"prompt"`                       // 节点提示词模板

	// 关联
	Template *SopTemplate `gorm:"foreignKey:TemplateID" json:"template,omitempty"`
	Parent   *SopNode     `gorm:"foreignKey:ParentID" json:"parent,omitempty"`
}

func (SopNode) TableName() string {
	return "sop_node"
}

// SopRun SOP执行记录表
type SopRun struct {
	gorm.Model
	TemplateID     uint       `gorm:"not null;index" json:"template_id"`
	UserID         uint       `gorm:"not null;index" json:"user_id"`
	Status         string     `gorm:"size:20;default:'pending';index" json:"status"` // pending, running, succeeded, failed
	ConversationID string     `gorm:"size:100;index" json:"conversation_id"`         // 隔离的对话ID
	FinalNoteID    *uint      `gorm:"index" json:"final_note_id"`                    // 最终生成的Note ID（不设置外键约束，避免循环依赖）
	StartedAt      *time.Time `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at"`
	ErrorMessage   string     `gorm:"type:text" json:"error_message"` // 错误信息

	// 关联（FinalNote不设置外键约束，避免与SopNote的循环依赖）
	Template *SopTemplate `gorm:"foreignKey:TemplateID" json:"template,omitempty"`
	User     *User        `gorm:"foreignKey:UserID" json:"user,omitempty"`
	// FinalNote字段不定义关联，避免循环依赖，需要时通过FinalNoteID手动查询
}

func (SopRun) TableName() string {
	return "sop_run"
}

// SopNodeRun SOP节点执行记录表
type SopNodeRun struct {
	gorm.Model
	RunID          uint       `gorm:"not null;index:idx_run_sort" json:"run_id"`
	NodeID         uint       `gorm:"not null;index" json:"node_id"`
	TemplateID     uint       `gorm:"not null;index" json:"template_id"`
	UserID         uint       `gorm:"not null;index" json:"user_id"`
	ParentNodeID   *uint      `gorm:"index" json:"parent_node_id"`
	Status         string     `gorm:"size:20;default:'pending';index" json:"status"` // pending, running, succeeded, failed
	Input          string     `gorm:"type:longtext" json:"input"`                    // 节点输入（使用LONGTEXT支持超长文本）
	Output         string     `gorm:"type:longtext" json:"output"`                   // 节点输出（使用LONGTEXT支持超长文本）
	LatencyMs      int64      `gorm:"default:0" json:"latency_ms"`                   // 执行耗时（毫秒）
	ConversationID string     `gorm:"size:100;index" json:"conversation_id"`         // 对话ID（与Run保持一致）
	Sort           int        `gorm:"default:0;index:idx_run_sort" json:"sort"`      // 执行顺序
	StartedAt      *time.Time `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at"`
	ErrorMessage   string     `gorm:"type:text" json:"error_message"` // 错误信息

	// 关联
	Run      *SopRun      `gorm:"foreignKey:RunID" json:"run,omitempty"`
	Node     *SopNode     `gorm:"foreignKey:NodeID" json:"node,omitempty"`
	Template *SopTemplate `gorm:"foreignKey:TemplateID" json:"template,omitempty"`
	User     *User        `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

func (SopNodeRun) TableName() string {
	return "sop_node_run"
}

// SopNote SOP执行生成的笔记表
type SopNote struct {
	gorm.Model
	Content    string `gorm:"type:text;not null" json:"content"` // 最终输出内容
	Title      string `gorm:"size:255" json:"title"`             // 标题（可选）
	UserID     uint   `gorm:"not null;index" json:"user_id"`
	TemplateID uint   `gorm:"not null;index" json:"template_id"`
	RunID      uint   `gorm:"not null;index" json:"run_id"`

	// 关联
	User     *User        `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Template *SopTemplate `gorm:"foreignKey:TemplateID" json:"template,omitempty"`
	Run      *SopRun      `gorm:"foreignKey:RunID" json:"run,omitempty"`
}

func (SopNote) TableName() string {
	return "sop_note"
}

// SOP状态常量
const (
	SopStatusPending   = "pending"
	SopStatusRunning   = "running"
	SopStatusSucceeded = "succeeded"
	SopStatusFailed    = "failed"

	SopNodeStatusActive   = "active"
	SopNodeStatusInactive = "inactive"
)
