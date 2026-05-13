package model

import (
	"time"

	"gorm.io/gorm"
)

const (
	// 智能体发布状态（2 态：draft = 未发布，published = 已发布）
	ChatbotStatusDraft     = "draft"
	ChatbotStatusPublished = "published"

	ChatbotSessionStatusActive = "active"
	ChatbotSessionStatusClosed = "closed"
)

// ChatbotConfig 智能体配置
type ChatbotConfig struct {
	ID                   uint           `gorm:"primaryKey" json:"id"`
	CreatedAt            time.Time      `json:"created_at"`
	UpdatedAt            time.Time      `json:"updated_at"`
	DeletedAt            gorm.DeletedAt `gorm:"index" json:"deleted_at"`
	UserID               uint           `gorm:"not null;index:idx_cc_user_status" json:"user_id"`
	Name                 string         `gorm:"size:100;not null" json:"name"`
	Description          string         `gorm:"size:1024" json:"description"`
	SystemPrompt         string         `gorm:"type:longtext;not null" json:"system_prompt"`
	Status               string         `gorm:"size:20;not null;default:'draft';index:idx_cc_user_status" json:"status"`
	GreetingEnabled      bool           `gorm:"not null;default:0" json:"greeting_enabled"`
	GreetingMessage      string         `gorm:"type:text" json:"greeting_message"`
	VisibilityRestricted bool           `gorm:"not null;default:0" json:"visibility_restricted"` // 可见范围限制: false=全部子用户可见; true=仅 chatbot_visibility_grant 白名单子用户可见
}

// TableName returns the table name for ChatbotConfig.
func (ChatbotConfig) TableName() string { return "chatbot_config" }

// ChatbotKnowledgeBase 智能体-知识库挂载（硬删除）
type ChatbotKnowledgeBase struct {
	ID              uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	ChatbotID       uint      `gorm:"not null;uniqueIndex:idx_ckb_chatbot_kb" json:"chatbot_id"`
	KnowledgeBaseID uint      `gorm:"not null;uniqueIndex:idx_ckb_chatbot_kb" json:"knowledge_base_id"`
	CreatedAt       time.Time `json:"created_at"`
}

// TableName returns the table name for ChatbotKnowledgeBase.
func (ChatbotKnowledgeBase) TableName() string { return "chatbot_knowledge_base" }

// ChatbotSession 对话会话
type ChatbotSession struct {
	ID           uint           `gorm:"primaryKey" json:"id"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"deleted_at"`
	UserID       uint           `gorm:"not null;index:idx_cs_user_chatbot" json:"user_id"`
	ChatbotID    uint           `gorm:"not null;index:idx_cs_user_chatbot" json:"chatbot_id"`
	Title        string         `gorm:"size:200" json:"title"`
	Status       string         `gorm:"size:20;not null;default:'active'" json:"status"`
	MessageCount int            `gorm:"default:0" json:"message_count"`
	// PinnedAt 置顶时间。NULL=未置顶；非 NULL 同时承担"是否置顶"标记和置顶组内排序键。
	// 重复置顶（已置顶 session 再点击置顶）会刷新此字段为最新 NOW()，
	// 实现"最近一次置顶操作"语义。Feature: chatbot-session-rename-pin S4 Task 1.
	PinnedAt *time.Time `json:"pinned_at,omitempty"`
}

// TableName returns the table name for ChatbotSession.
func (ChatbotSession) TableName() string { return "chatbot_session" }

// ChatbotMessage 对话消息（追加型，无软删除）
type ChatbotMessage struct {
	ID        uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	SessionID uint   `gorm:"not null;index:idx_cm_session_seq" json:"session_id"`
	UserID    uint   `gorm:"not null;index:idx_cm_user_id" json:"user_id"`
	Role      string `gorm:"size:20;not null" json:"role"`
	Content   string `gorm:"type:longtext" json:"content"`
	Thinking  string `gorm:"type:longtext" json:"thinking"`
	TraceID   string `gorm:"size:100" json:"trace_id"`
	Seq       int    `gorm:"not null;default:0;index:idx_cm_session_seq" json:"seq"`
	// ModelName 记录生成 assistant 消息时实际使用的模型（Gateway 路径填充；
	// user 消息留空）。用于审计、按模型切片分析和历史记录展示。
	ModelName        string    `gorm:"size:100;not null;default:''" json:"model_name"`
	PromptTokens     int       `gorm:"not null;default:0" json:"prompt_tokens"`
	CompletionTokens int       `gorm:"not null;default:0" json:"completion_tokens"`
	CreatedAt        time.Time `json:"created_at"`
}

// TableName returns the table name for ChatbotMessage.
func (ChatbotMessage) TableName() string { return "chatbot_message" }
