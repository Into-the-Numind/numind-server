package model

import (
	"time"

	"gorm.io/gorm"
)

// ChatSession 对话会话表
type ChatSession struct {
	gorm.Model
	UserID       uint   `gorm:"index;not null" json:"user_id"`
	Title        string `gorm:"size:255" json:"title"`
	Status       string `gorm:"size:20;default:'active'" json:"status"` // active, closed
	MessageCount int    `gorm:"default:0" json:"message_count"`

	// 关联关系
	User     User          `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Messages []ChatMessage `gorm:"foreignKey:SessionID" json:"messages,omitempty"`
}

func (ChatSession) TableName() string {
	return "chat_session"
}

// ChatMessage 对话消息表
type ChatMessage struct {
	gorm.Model
	SessionID uint   `gorm:"index;not null" json:"session_id"`
	UserID    uint   `gorm:"index;not null" json:"user_id"`
	Role      string `gorm:"size:20;not null" json:"role"` // user, assistant, system
	Content   string `gorm:"type:text;not null" json:"content"`
	Status    string `gorm:"size:20;default:'sent'" json:"status"` // sent, delivered, read

	// 关联关系
	Session ChatSession `gorm:"foreignKey:SessionID" json:"session,omitempty"`
	User    User        `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

func (ChatMessage) TableName() string {
	return "chat_message"
}

// WebSocketMessage WebSocket消息结构
type WebSocketMessage struct {
	Type      string      `json:"type"` // message, session, error, ping, pong
	SessionID uint        `json:"session_id,omitempty"`
	MessageID uint        `json:"message_id,omitempty"`
	Content   string      `json:"content,omitempty"`
	Role      string      `json:"role,omitempty"`
	Data      interface{} `json:"data,omitempty"`
	Error     string      `json:"error,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
}

// ChatRequest 创建对话请求
type ChatRequest struct {
	SessionID uint   `json:"session_id,omitempty"`
	Content   string `json:"content" binding:"required"`
	Title     string `json:"title,omitempty"`
}

// ChatResponse 对话响应
type ChatResponse struct {
	SessionID uint      `json:"session_id"`
	MessageID uint      `json:"message_id"`
	Content   string    `json:"content"`
	Role      string    `json:"role"`
	Timestamp time.Time `json:"timestamp"`
}
