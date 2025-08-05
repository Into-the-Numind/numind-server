package chat

import (
	"context"
	"fmt"
	"time"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// ChatBiz 定义了对话相关的业务逻辑接口
type ChatBiz interface {
	CreateSession(ctx context.Context, userID uint, title string) (*model.ChatSession, error)
	GetSession(ctx context.Context, sessionID uint, userID uint) (*model.ChatSession, error)
	ListSessions(ctx context.Context, userID uint, offset, limit int) ([]*model.ChatSession, int64, error)
	UpdateSession(ctx context.Context, sessionID uint, userID uint, title string) error
	DeleteSession(ctx context.Context, sessionID uint, userID uint) error

	CreateMessage(ctx context.Context, sessionID uint, userID uint, content string, role string) (*model.ChatMessage, error)
	GetMessage(ctx context.Context, messageID uint, userID uint) (*model.ChatMessage, error)
	ListMessages(ctx context.Context, sessionID uint, userID uint, offset, limit int) ([]*model.ChatMessage, int64, error)
	UpdateMessage(ctx context.Context, messageID uint, userID uint, content string) error
	DeleteMessage(ctx context.Context, messageID uint, userID uint) error

	GetSessionWithMessages(ctx context.Context, sessionID uint, userID uint) (*model.ChatSession, error)

	// WebSocket相关方法
	ProcessWebSocketMessage(ctx context.Context, userID uint, msg *model.WebSocketMessage) (*model.WebSocketMessage, error)
	GenerateAssistantResponse(ctx context.Context, userMessage string) (string, error)
}

// chatBiz 是 ChatBiz 的具体实现
type chatBiz struct {
	ds store.IStore
}

// New 创建一个新的 ChatBiz 实例
func New(ds store.IStore) ChatBiz {
	return &chatBiz{ds: ds}
}

// CreateSession 创建新的对话会话
func (b *chatBiz) CreateSession(ctx context.Context, userID uint, title string) (*model.ChatSession, error) {
	session := &model.ChatSession{
		UserID: userID,
		Title:  title,
		Status: "active",
	}

	if err := b.ds.Chats().CreateSession(ctx, session); err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	return session, nil
}

// GetSession 获取对话会话
func (b *chatBiz) GetSession(ctx context.Context, sessionID uint, userID uint) (*model.ChatSession, error) {
	session, err := b.ds.Chats().GetSession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	// 验证用户权限
	if session.UserID != userID {
		return nil, fmt.Errorf("unauthorized access to session")
	}

	return session, nil
}

// ListSessions 获取用户的对话会话列表
func (b *chatBiz) ListSessions(ctx context.Context, userID uint, offset, limit int) ([]*model.ChatSession, int64, error) {
	return b.ds.Chats().ListSessions(ctx, userID, offset, limit)
}

// UpdateSession 更新对话会话
func (b *chatBiz) UpdateSession(ctx context.Context, sessionID uint, userID uint, title string) error {
	session, err := b.GetSession(ctx, sessionID, userID)
	if err != nil {
		return err
	}

	session.Title = title
	return b.ds.Chats().UpdateSession(ctx, session)
}

// DeleteSession 删除对话会话
func (b *chatBiz) DeleteSession(ctx context.Context, sessionID uint, userID uint) error {
	// 验证用户权限
	_, err := b.GetSession(ctx, sessionID, userID)
	if err != nil {
		return err
	}

	return b.ds.Chats().DeleteSession(ctx, sessionID)
}

// CreateMessage 创建新的对话消息
func (b *chatBiz) CreateMessage(ctx context.Context, sessionID uint, userID uint, content string, role string) (*model.ChatMessage, error) {
	// 验证会话存在且用户有权限
	_, err := b.GetSession(ctx, sessionID, userID)
	if err != nil {
		return nil, err
	}

	message := &model.ChatMessage{
		SessionID: sessionID,
		UserID:    userID,
		Role:      role,
		Content:   content,
		Status:    "sent",
	}

	if err := b.ds.Chats().CreateMessage(ctx, message); err != nil {
		return nil, fmt.Errorf("failed to create message: %w", err)
	}

	// 更新会话的消息数量
	if err := b.ds.Chats().UpdateSessionMessageCount(ctx, sessionID); err != nil {
		log.Errorw("Failed to update session message count", "error", err)
	}

	return message, nil
}

// GetMessage 获取对话消息
func (b *chatBiz) GetMessage(ctx context.Context, messageID uint, userID uint) (*model.ChatMessage, error) {
	message, err := b.ds.Chats().GetMessage(ctx, messageID)
	if err != nil {
		return nil, fmt.Errorf("failed to get message: %w", err)
	}

	// 验证用户权限
	if message.UserID != userID {
		return nil, fmt.Errorf("unauthorized access to message")
	}

	return message, nil
}

// ListMessages 获取会话的消息列表
func (b *chatBiz) ListMessages(ctx context.Context, sessionID uint, userID uint, offset, limit int) ([]*model.ChatMessage, int64, error) {
	// 验证会话权限
	_, err := b.GetSession(ctx, sessionID, userID)
	if err != nil {
		return nil, 0, err
	}

	return b.ds.Chats().ListMessages(ctx, sessionID, offset, limit)
}

// UpdateMessage 更新对话消息
func (b *chatBiz) UpdateMessage(ctx context.Context, messageID uint, userID uint, content string) error {
	message, err := b.GetMessage(ctx, messageID, userID)
	if err != nil {
		return err
	}

	message.Content = content
	return b.ds.Chats().UpdateMessage(ctx, message)
}

// DeleteMessage 删除对话消息
func (b *chatBiz) DeleteMessage(ctx context.Context, messageID uint, userID uint) error {
	// 验证用户权限
	_, err := b.GetMessage(ctx, messageID, userID)
	if err != nil {
		return err
	}

	return b.ds.Chats().DeleteMessage(ctx, messageID)
}

// GetSessionWithMessages 获取会话及其消息
func (b *chatBiz) GetSessionWithMessages(ctx context.Context, sessionID uint, userID uint) (*model.ChatSession, error) {
	// 验证会话权限
	_, err := b.GetSession(ctx, sessionID, userID)
	if err != nil {
		return nil, err
	}

	return b.ds.Chats().GetSessionWithMessages(ctx, sessionID)
}

// ProcessWebSocketMessage 处理WebSocket消息
func (b *chatBiz) ProcessWebSocketMessage(ctx context.Context, userID uint, msg *model.WebSocketMessage) (*model.WebSocketMessage, error) {
	switch msg.Type {
	case "message":
		return b.handleChatMessage(ctx, userID, msg)
	case "session":
		return b.handleSessionMessage(ctx, userID, msg)
	case "ping":
		return &model.WebSocketMessage{
			Type:      "pong",
			Timestamp: time.Now(),
		}, nil
	default:
		return &model.WebSocketMessage{
			Type:      "error",
			Error:     "unknown message type",
			Timestamp: time.Now(),
		}, nil
	}
}

// handleChatMessage 处理聊天消息
func (b *chatBiz) handleChatMessage(ctx context.Context, userID uint, msg *model.WebSocketMessage) (*model.WebSocketMessage, error) {
	// 创建或获取会话
	var sessionID uint
	if msg.SessionID == 0 {
		// 创建新会话
		session, err := b.CreateSession(ctx, userID, "新对话")
		if err != nil {
			return nil, err
		}
		sessionID = session.ID
	} else {
		// 验证现有会话
		session, err := b.GetSession(ctx, msg.SessionID, userID)
		if err != nil {
			return nil, err
		}
		sessionID = session.ID
	}

	// 保存用户消息
	_, err := b.CreateMessage(ctx, sessionID, userID, msg.Content, "user")
	if err != nil {
		return nil, err
	}

	// 生成助手回复
	assistantContent, err := b.GenerateAssistantResponse(ctx, msg.Content)
	if err != nil {
		return nil, err
	}

	// 保存助手消息
	assistantMessage, err := b.CreateMessage(ctx, sessionID, userID, assistantContent, "assistant")
	if err != nil {
		return nil, err
	}

	return &model.WebSocketMessage{
		Type:      "message",
		SessionID: sessionID,
		MessageID: assistantMessage.ID,
		Content:   assistantContent,
		Role:      "assistant",
		Timestamp: time.Now(),
	}, nil
}

// handleSessionMessage 处理会话相关消息
func (b *chatBiz) handleSessionMessage(ctx context.Context, userID uint, msg *model.WebSocketMessage) (*model.WebSocketMessage, error) {
	// 这里可以处理会话相关的操作，比如获取会话列表等
	return &model.WebSocketMessage{
		Type:      "session",
		Data:      "session operation completed",
		Timestamp: time.Now(),
	}, nil
}

// GenerateAssistantResponse 生成助手回复
func (b *chatBiz) GenerateAssistantResponse(ctx context.Context, userMessage string) (string, error) {
	// 这里可以集成AI服务，比如调用OpenAI API
	// 目前返回一个简单的回复
	response := fmt.Sprintf("我收到了你的消息：%s。这是一个简单的回复，你可以在这里集成AI服务。", userMessage)
	return response, nil
}
