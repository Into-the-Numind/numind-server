package model

import (
	"time"

	"gorm.io/gorm"
)

const (
	ChatbotStatusDraft     = "draft"
	ChatbotStatusPublished = "published"
	ChatbotStatusOffline   = "offline"

	ChatbotSessionStatusActive = "active"
	ChatbotSessionStatusClosed = "closed"
)

// ChatbotConfig 智能体配置
type ChatbotConfig struct {
	gorm.Model
	UserID       uint   `gorm:"not null;index:idx_cc_user_status" json:"user_id"`
	Name         string `gorm:"size:100;not null" json:"name"`
	Description  string `gorm:"size:1024" json:"description"`
	Avatar       string `gorm:"size:500" json:"avatar"`
	SystemPrompt string `gorm:"type:longtext;not null" json:"system_prompt"`
	Status       string `gorm:"size:20;not null;default:'draft';index:idx_cc_user_status" json:"status"`
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
	gorm.Model
	UserID       uint   `gorm:"not null;index:idx_cs_user_chatbot" json:"user_id"`
	ChatbotID    uint   `gorm:"not null;index:idx_cs_user_chatbot" json:"chatbot_id"`
	Title        string `gorm:"size:200" json:"title"`
	Status       string `gorm:"size:20;not null;default:'active'" json:"status"`
	MessageCount int    `gorm:"default:0" json:"message_count"`
}

// TableName returns the table name for ChatbotSession.
func (ChatbotSession) TableName() string { return "chatbot_session" }

// ChatbotMessage 对话消息（追加型，无软删除）
type ChatbotMessage struct {
	ID               uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	SessionID        uint      `gorm:"not null;index:idx_cm_session_seq" json:"session_id"`
	UserID           uint      `gorm:"not null;index:idx_cm_user_id" json:"user_id"`
	Role             string    `gorm:"size:20;not null" json:"role"`
	Content          string    `gorm:"type:longtext" json:"content"`
	Thinking         string    `gorm:"type:longtext" json:"thinking"`
	TraceID          string    `gorm:"size:100" json:"trace_id"`
	Seq              int       `gorm:"not null;default:0;index:idx_cm_session_seq" json:"seq"`
	PromptTokens     int       `gorm:"not null;default:0" json:"prompt_tokens"`
	CompletionTokens int       `gorm:"not null;default:0" json:"completion_tokens"`
	CreatedAt        time.Time `json:"created_at"`
}

// TableName returns the table name for ChatbotMessage.
func (ChatbotMessage) TableName() string { return "chatbot_message" }
