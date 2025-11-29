package model

import (
	"time"

	"gorm.io/gorm"
)

// ChatSession 对话会话表
type ChatSession struct {
	gorm.Model
	UserID       uint   `gorm:"index;not null" json:"user_id"`
	BookID       *uint  `gorm:"index" json:"book_id,omitempty"` // 关联的笔记ID（可为空，支持通用聊天）
	Title        string `gorm:"size:255" json:"title"`
	Status       string `gorm:"size:20;default:'active'" json:"status"` // active, closed
	MessageCount int    `gorm:"default:0" json:"message_count"`

	// 关联关系
	User     User          `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Book     *BookM        `gorm:"foreignKey:BookID" json:"book,omitempty"` // 关联的笔记
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
// 请求消息（客户端 -> 服务端）：
//   - type: "chat" (聊天消息，可省略，默认为聊天)
//   - question: 用户问题（必填）
//   - book_ids: 笔记ID数组（必填，至少1个）
//   - session_id: 会话ID（可选，用于继续已有会话；如果未提供，系统会自动创建新会话）
//   - deep_thinking: 是否开启深度思考（可选，默认 false）
//
// 响应消息（服务端 -> 客户端）：
//   - type: "session_created" (新会话已创建，仅在首次对话时发送) | "message_chunk" (流式消息块) | "message_done" (消息完成) | "error" (错误)
//   - session_id: 会话ID（所有响应都包含）
//   - message_id: 消息ID（message_done 时包含）
//   - content: 消息内容
//   - role: "user" | "assistant"
//   - data: 其他数据（session_created 时包含会话详情）
type WebSocketMessage struct {
	Type         string      `json:"type,omitempty"`          // 请求时：可省略（默认"chat"）或 "chat"；响应时：message_chunk, message_done, error
	SessionID    uint        `json:"session_id,omitempty"`    // 会话ID
	Question     string      `json:"question,omitempty"`      // 用户问题（请求时必填，与HTTP保持一致）
	BookIDs      []uint      `json:"book_ids,omitempty"`      // 笔记ID数组（请求时必填，与HTTP保持一致）
	BookID       *uint       `json:"book_id,omitempty"`       // 保留向后兼容，如果提供则转换为book_ids
	DeepThinking bool        `json:"deep_thinking,omitempty"` // 是否开启深度思考（请求时可选，默认 false）
	MessageID    uint        `json:"message_id,omitempty"`    // 消息ID（响应时）
	Content      string      `json:"content,omitempty"`       // 消息内容（响应时）
	Role         string      `json:"role,omitempty"`          // 角色（响应时）
	Data         interface{} `json:"data,omitempty"`          // 其他数据
	Error        string      `json:"error,omitempty"`         // 错误信息
	Timestamp    time.Time   `json:"timestamp"`               // 时间戳
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
