package store

import (
	"context"
	"time"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// IChatbotSessionStore 智能体会话数据访问接口
type IChatbotSessionStore interface {
	CreateSession(ctx context.Context, session *model.ChatbotSession) error
	GetSession(ctx context.Context, id uint) (*model.ChatbotSession, error)
	ListSessions(ctx context.Context, userID uint, offset, limit int) ([]model.ChatbotSession, int64, error)
	DeleteSession(ctx context.Context, id uint) error
	IncrementMessageCount(ctx context.Context, sessionID uint) error

	// UpdateTitle 更新会话标题。使用 UpdateColumn 绕开 updated_at 自动刷新（D2 决策）。
	// sessionID 不存在时返回 gorm.ErrRecordNotFound。
	UpdateTitle(ctx context.Context, sessionID uint, title string) error

	// SetPinnedAt 设置或清除会话的置顶时间。使用 UpdateColumn 绕开 updated_at 自动刷新（D2 决策）。
	// pinnedAt == nil 时写入 SQL NULL（取消置顶）。sessionID 不存在时返回 gorm.ErrRecordNotFound。
	SetPinnedAt(ctx context.Context, sessionID uint, pinnedAt *time.Time) error

	// ListSessionsByChatbot 获取指定用户在指定智能体下的会话列表（分页）。
	// 排序：置顶优先（pinned_at IS NULL ASC），置顶组内按 pinned_at DESC，未置顶组按 updated_at DESC。
	ListSessionsByChatbot(ctx context.Context, userID, chatbotID uint, offset, limit int) ([]model.ChatbotSession, int64, error)

	CreateMessage(ctx context.Context, msg *model.ChatbotMessage) error
	ListMessages(ctx context.Context, sessionID uint, offset, limit int) ([]model.ChatbotMessage, int64, error)
	DeleteMessagesBySession(ctx context.Context, sessionID uint) error
	GetMaxSeq(ctx context.Context, sessionID uint) (int, error)
}

type chatbotSessionStore struct {
	db *gorm.DB
}

var _ IChatbotSessionStore = (*chatbotSessionStore)(nil)

// NewChatbotSessionStore 创建智能体会话 Store 实例
func NewChatbotSessionStore(db *gorm.DB) IChatbotSessionStore {
	return &chatbotSessionStore{db: db}
}

// CreateSession 创建会话
func (s *chatbotSessionStore) CreateSession(ctx context.Context, session *model.ChatbotSession) error {
	return s.db.WithContext(ctx).Create(session).Error
}

// GetSession 根据 ID 获取会话
func (s *chatbotSessionStore) GetSession(ctx context.Context, id uint) (*model.ChatbotSession, error) {
	var session model.ChatbotSession
	if err := s.db.WithContext(ctx).First(&session, id).Error; err != nil {
		return nil, err
	}
	return &session, nil
}

// ListSessions 获取用户的会话列表（分页）
func (s *chatbotSessionStore) ListSessions(ctx context.Context, userID uint, offset, limit int) ([]model.ChatbotSession, int64, error) {
	var sessions []model.ChatbotSession
	var total int64

	query := s.db.WithContext(ctx).Model(&model.ChatbotSession{}).Where("user_id = ?", userID)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := query.Offset(offset).Limit(limit).Order("updated_at DESC").Find(&sessions).Error; err != nil {
		return nil, 0, err
	}

	return sessions, total, nil
}

// DeleteSession 删除会话（软删除）
func (s *chatbotSessionStore) DeleteSession(ctx context.Context, id uint) error {
	return s.db.WithContext(ctx).Delete(&model.ChatbotSession{}, id).Error
}

// IncrementMessageCount 原子递增会话消息计数
func (s *chatbotSessionStore) IncrementMessageCount(ctx context.Context, sessionID uint) error {
	return s.db.WithContext(ctx).
		Model(&model.ChatbotSession{}).
		Where("id = ?", sessionID).
		UpdateColumn("message_count", gorm.Expr("message_count + ?", 1)).Error
}

// UpdateTitle 更新会话标题，使用 UpdateColumn 绕开 updated_at 自动刷新（D2 决策）。
// sessionID 不存在时返回 gorm.ErrRecordNotFound。
func (s *chatbotSessionStore) UpdateTitle(ctx context.Context, sessionID uint, title string) error {
	result := s.db.WithContext(ctx).
		Model(&model.ChatbotSession{}).
		Where("id = ?", sessionID).
		UpdateColumn("title", title)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// SetPinnedAt 设置或清除会话的置顶时间，使用 UpdateColumn 绕开 updated_at 自动刷新（D2 决策）。
// pinnedAt == nil 时写入 SQL NULL（取消置顶）。sessionID 不存在时返回 gorm.ErrRecordNotFound。
func (s *chatbotSessionStore) SetPinnedAt(ctx context.Context, sessionID uint, pinnedAt *time.Time) error {
	result := s.db.WithContext(ctx).
		Model(&model.ChatbotSession{}).
		Where("id = ?", sessionID).
		UpdateColumn("pinned_at", pinnedAt)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ListSessionsByChatbot 获取指定用户在指定智能体下的会话列表（分页）。
// 排序：置顶优先（pinned_at IS NULL ASC），置顶组内按 pinned_at DESC，未置顶组按 updated_at DESC。
func (s *chatbotSessionStore) ListSessionsByChatbot(ctx context.Context, userID, chatbotID uint, offset, limit int) ([]model.ChatbotSession, int64, error) {
	var sessions []model.ChatbotSession
	var total int64

	query := s.db.WithContext(ctx).Model(&model.ChatbotSession{}).
		Where("user_id = ? AND chatbot_id = ?", userID, chatbotID)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := query.
		Order("pinned_at IS NULL ASC, pinned_at DESC, updated_at DESC").
		Offset(offset).
		Limit(limit).
		Find(&sessions).Error; err != nil {
		return nil, 0, err
	}

	return sessions, total, nil
}

// CreateMessage 创建消息
func (s *chatbotSessionStore) CreateMessage(ctx context.Context, msg *model.ChatbotMessage) error {
	return s.db.WithContext(ctx).Create(msg).Error
}

// ListMessages 获取会话的消息列表（分页，按 seq 升序）
func (s *chatbotSessionStore) ListMessages(ctx context.Context, sessionID uint, offset, limit int) ([]model.ChatbotMessage, int64, error) {
	var messages []model.ChatbotMessage
	var total int64

	query := s.db.WithContext(ctx).Model(&model.ChatbotMessage{}).Where("session_id = ?", sessionID)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := query.Offset(offset).Limit(limit).Order("seq ASC").Find(&messages).Error; err != nil {
		return nil, 0, err
	}

	return messages, total, nil
}

// DeleteMessagesBySession 硬删除会话的所有消息
func (s *chatbotSessionStore) DeleteMessagesBySession(ctx context.Context, sessionID uint) error {
	return s.db.WithContext(ctx).Unscoped().
		Where("session_id = ?", sessionID).
		Delete(&model.ChatbotMessage{}).Error
}

// GetMaxSeq 获取会话中的最大消息序号，无消息时返回 0
func (s *chatbotSessionStore) GetMaxSeq(ctx context.Context, sessionID uint) (int, error) {
	var maxSeq *int
	err := s.db.WithContext(ctx).
		Model(&model.ChatbotMessage{}).
		Where("session_id = ?", sessionID).
		Select("MAX(seq)").
		Scan(&maxSeq).Error
	if err != nil {
		return 0, err
	}
	if maxSeq == nil {
		return 0, nil
	}
	return *maxSeq, nil
}
