package store

import (
	"context"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// ChatStore 定义了对话相关的数据访问接口
type ChatStore interface {
	CreateSession(ctx context.Context, session *model.ChatSession) error
	GetSession(ctx context.Context, sessionID uint) (*model.ChatSession, error)
	ListSessions(ctx context.Context, userID uint, offset, limit int) ([]*model.ChatSession, int64, error)
	UpdateSession(ctx context.Context, session *model.ChatSession) error
	DeleteSession(ctx context.Context, sessionID uint) error

	CreateMessage(ctx context.Context, message *model.ChatMessage) error
	GetMessage(ctx context.Context, messageID uint) (*model.ChatMessage, error)
	ListMessages(ctx context.Context, sessionID uint, offset, limit int) ([]*model.ChatMessage, int64, error)
	UpdateMessage(ctx context.Context, message *model.ChatMessage) error
	DeleteMessage(ctx context.Context, messageID uint) error

	GetSessionWithMessages(ctx context.Context, sessionID uint) (*model.ChatSession, error)
	UpdateSessionMessageCount(ctx context.Context, sessionID uint) error
}

// chatStore 是 ChatStore 的具体实现
type chatStore struct {
	db *gorm.DB
}

// NewChatStore 创建一个新的 ChatStore 实例
func NewChatStore(db *gorm.DB) ChatStore {
	return &chatStore{db: db}
}

// CreateSession 创建新的对话会话
func (s *chatStore) CreateSession(ctx context.Context, session *model.ChatSession) error {
	return s.db.WithContext(ctx).Create(session).Error
}

// GetSession 获取对话会话
func (s *chatStore) GetSession(ctx context.Context, sessionID uint) (*model.ChatSession, error) {
	var session model.ChatSession
	err := s.db.WithContext(ctx).Where("id = ?", sessionID).First(&session).Error
	if err != nil {
		return nil, err
	}
	return &session, nil
}

// ListSessions 获取用户的对话会话列表
func (s *chatStore) ListSessions(ctx context.Context, userID uint, offset, limit int) ([]*model.ChatSession, int64, error) {
	var sessions []*model.ChatSession
	var total int64

	// 获取总数
	err := s.db.WithContext(ctx).Model(&model.ChatSession{}).Where("user_id = ?", userID).Count(&total).Error
	if err != nil {
		return nil, 0, err
	}

	// 获取会话列表
	err = s.db.WithContext(ctx).Where("user_id = ?", userID).
		Order("updated_at DESC").
		Offset(offset).Limit(limit).
		Find(&sessions).Error

	return sessions, total, err
}

// UpdateSession 更新对话会话
func (s *chatStore) UpdateSession(ctx context.Context, session *model.ChatSession) error {
	return s.db.WithContext(ctx).Save(session).Error
}

// DeleteSession 删除对话会话
func (s *chatStore) DeleteSession(ctx context.Context, sessionID uint) error {
	return s.db.WithContext(ctx).Delete(&model.ChatSession{}, sessionID).Error
}

// CreateMessage 创建新的对话消息
func (s *chatStore) CreateMessage(ctx context.Context, message *model.ChatMessage) error {
	return s.db.WithContext(ctx).Create(message).Error
}

// GetMessage 获取对话消息
func (s *chatStore) GetMessage(ctx context.Context, messageID uint) (*model.ChatMessage, error) {
	var message model.ChatMessage
	err := s.db.WithContext(ctx).Where("id = ?", messageID).First(&message).Error
	if err != nil {
		return nil, err
	}
	return &message, nil
}

// ListMessages 获取会话的消息列表
func (s *chatStore) ListMessages(ctx context.Context, sessionID uint, offset, limit int) ([]*model.ChatMessage, int64, error) {
	var messages []*model.ChatMessage
	var total int64

	// 获取总数
	err := s.db.WithContext(ctx).Model(&model.ChatMessage{}).Where("session_id = ?", sessionID).Count(&total).Error
	if err != nil {
		return nil, 0, err
	}

	// 获取消息列表
	err = s.db.WithContext(ctx).Where("session_id = ?", sessionID).
		Order("created_at ASC").
		Offset(offset).Limit(limit).
		Find(&messages).Error

	return messages, total, err
}

// UpdateMessage 更新对话消息
func (s *chatStore) UpdateMessage(ctx context.Context, message *model.ChatMessage) error {
	return s.db.WithContext(ctx).Save(message).Error
}

// DeleteMessage 删除对话消息
func (s *chatStore) DeleteMessage(ctx context.Context, messageID uint) error {
	return s.db.WithContext(ctx).Delete(&model.ChatMessage{}, messageID).Error
}

// GetSessionWithMessages 获取会话及其消息
func (s *chatStore) GetSessionWithMessages(ctx context.Context, sessionID uint) (*model.ChatSession, error) {
	var session model.ChatSession
	err := s.db.WithContext(ctx).
		Preload("Messages", func(db *gorm.DB) *gorm.DB {
			return db.Order("created_at ASC")
		}).
		Where("id = ?", sessionID).
		First(&session).Error
	if err != nil {
		return nil, err
	}
	return &session, nil
}

// UpdateSessionMessageCount 更新会话的消息数量
func (s *chatStore) UpdateSessionMessageCount(ctx context.Context, sessionID uint) error {
	var count int64
	err := s.db.WithContext(ctx).Model(&model.ChatMessage{}).Where("session_id = ?", sessionID).Count(&count).Error
	if err != nil {
		return err
	}

	return s.db.WithContext(ctx).Model(&model.ChatSession{}).
		Where("id = ?", sessionID).
		Update("message_count", count).Error
}
