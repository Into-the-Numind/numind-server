package store

import (
	"context"
	"time"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// SalesSessionStore 定义了销售智能体会话相关的数据访问接口
type SalesSessionStore interface {
	CreateSession(ctx context.Context, session *model.SalesSession) error
	GetSession(ctx context.Context, sessionID uint, userID uint) (*model.SalesSession, error)
	ListSessions(ctx context.Context, userID uint, offset, limit int, salesStage string) ([]*model.SalesSession, int64, error)
	UpdateSession(ctx context.Context, session *model.SalesSession) error
	DeleteSession(ctx context.Context, sessionID uint, userID uint) error

	CreateMessage(ctx context.Context, message *model.SalesMessage) error
	GetMessage(ctx context.Context, messageID uint) (*model.SalesMessage, error)
	ListMessages(ctx context.Context, sessionID uint, userID uint, offset, limit int) ([]*model.SalesMessage, int64, error)
	UpdateMessage(ctx context.Context, message *model.SalesMessage) error
	DeleteMessage(ctx context.Context, messageID uint) error

	GetSessionWithMessages(ctx context.Context, sessionID uint, userID uint) (*model.SalesSession, error)
	UpdateSessionMessageCount(ctx context.Context, sessionID uint) error

	// 置顶相关
	PinSession(ctx context.Context, sessionID uint, userID uint) error
	UnpinSession(ctx context.Context, sessionID uint, userID uint) error

	// 重命名
	RenameSession(ctx context.Context, sessionID uint, userID uint, newTitle string) error
}

// salesSessionStore 是 SalesSessionStore 的具体实现
type salesSessionStore struct {
	db *gorm.DB
}

// NewSalesSessionStore 创建一个新的 SalesSessionStore 实例
func NewSalesSessionStore(db *gorm.DB) SalesSessionStore {
	return &salesSessionStore{db: db}
}

// CreateSession 创建新的销售会话
func (s *salesSessionStore) CreateSession(ctx context.Context, session *model.SalesSession) error {
	return s.db.WithContext(ctx).Create(session).Error
}

// GetSession 获取销售会话（带用户权限验证）
func (s *salesSessionStore) GetSession(ctx context.Context, sessionID uint, userID uint) (*model.SalesSession, error) {
	var session model.SalesSession
	err := s.db.WithContext(ctx).
		Where("id = ? AND user_id = ?", sessionID, userID).
		First(&session).Error
	if err != nil {
		return nil, err
	}
	return &session, nil
}

// ListSessions 获取用户的销售会话列表（支持按销售阶段过滤）
func (s *salesSessionStore) ListSessions(ctx context.Context, userID uint, offset, limit int, salesStage string) ([]*model.SalesSession, int64, error) {
	var sessions []*model.SalesSession
	var total int64

	// 构建查询
	query := s.db.WithContext(ctx).Model(&model.SalesSession{}).Where("user_id = ?", userID)
	if salesStage != "" {
		query = query.Where("sales_stage = ?", salesStage)
	}

	// 获取总数
	err := query.Count(&total).Error
	if err != nil {
		return nil, 0, err
	}

	// 获取会话列表，置顶的会话优先显示，按照置顶时间降序，未置顶的按更新时间降序
	query = s.db.WithContext(ctx).Where("user_id = ?", userID)
	if salesStage != "" {
		query = query.Where("sales_stage = ?", salesStage)
	}
	err = query.Order("is_pinned DESC, CASE WHEN is_pinned = true THEN pinned_at ELSE updated_at END DESC").
		Offset(offset).Limit(limit).
		Find(&sessions).Error

	return sessions, total, err
}

// UpdateSession 更新销售会话
func (s *salesSessionStore) UpdateSession(ctx context.Context, session *model.SalesSession) error {
	return s.db.WithContext(ctx).Save(session).Error
}

// DeleteSession 删除销售会话（带用户权限验证）
func (s *salesSessionStore) DeleteSession(ctx context.Context, sessionID uint, userID uint) error {
	return s.db.WithContext(ctx).
		Where("id = ? AND user_id = ?", sessionID, userID).
		Delete(&model.SalesSession{}).Error
}

// CreateMessage 创建新的销售消息
func (s *salesSessionStore) CreateMessage(ctx context.Context, message *model.SalesMessage) error {
	return s.db.WithContext(ctx).Create(message).Error
}

// GetMessage 获取销售消息
func (s *salesSessionStore) GetMessage(ctx context.Context, messageID uint) (*model.SalesMessage, error) {
	var message model.SalesMessage
	err := s.db.WithContext(ctx).Where("id = ?", messageID).First(&message).Error
	if err != nil {
		return nil, err
	}
	return &message, nil
}

// ListMessages 获取会话的销售消息列表（带用户权限验证）
func (s *salesSessionStore) ListMessages(ctx context.Context, sessionID uint, userID uint, offset, limit int) ([]*model.SalesMessage, int64, error) {
	var messages []*model.SalesMessage
	var total int64

	// 首先验证会话属于该用户
	var session model.SalesSession
	err := s.db.WithContext(ctx).
		Where("id = ? AND user_id = ?", sessionID, userID).
		First(&session).Error
	if err != nil {
		return nil, 0, err
	}

	// 获取总数
	err = s.db.WithContext(ctx).Model(&model.SalesMessage{}).
		Where("session_id = ?", sessionID).
		Count(&total).Error
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

// UpdateMessage 更新销售消息
func (s *salesSessionStore) UpdateMessage(ctx context.Context, message *model.SalesMessage) error {
	return s.db.WithContext(ctx).Save(message).Error
}

// DeleteMessage 删除销售消息
func (s *salesSessionStore) DeleteMessage(ctx context.Context, messageID uint) error {
	return s.db.WithContext(ctx).Delete(&model.SalesMessage{}, messageID).Error
}

// GetSessionWithMessages 获取销售会话及其消息（带用户权限验证）
func (s *salesSessionStore) GetSessionWithMessages(ctx context.Context, sessionID uint, userID uint) (*model.SalesSession, error) {
	var session model.SalesSession
	err := s.db.WithContext(ctx).
		Preload("Messages", func(db *gorm.DB) *gorm.DB {
			return db.Order("created_at ASC")
		}).
		Where("id = ? AND user_id = ?", sessionID, userID).
		First(&session).Error
	if err != nil {
		return nil, err
	}
	return &session, nil
}

// UpdateSessionMessageCount 更新会话的消息数量
func (s *salesSessionStore) UpdateSessionMessageCount(ctx context.Context, sessionID uint) error {
	var count int64
	err := s.db.WithContext(ctx).Model(&model.SalesMessage{}).
		Where("session_id = ?", sessionID).
		Count(&count).Error
	if err != nil {
		return err
	}

	return s.db.WithContext(ctx).Model(&model.SalesSession{}).
		Where("id = ?", sessionID).
		Update("message_count", count).Error
}

// PinSession 置顶会话
func (s *salesSessionStore) PinSession(ctx context.Context, sessionID uint, userID uint) error {
	now := time.Now()
	return s.db.WithContext(ctx).Model(&model.SalesSession{}).
		Where("id = ? AND user_id = ?", sessionID, userID).
		Updates(map[string]interface{}{
			"is_pinned": true,
			"pinned_at": now,
		}).Error
}

// UnpinSession 取消置顶会话
func (s *salesSessionStore) UnpinSession(ctx context.Context, sessionID uint, userID uint) error {
	return s.db.WithContext(ctx).Model(&model.SalesSession{}).
		Where("id = ? AND user_id = ?", sessionID, userID).
		Updates(map[string]interface{}{
			"is_pinned": false,
			"pinned_at": nil,
		}).Error
}

// RenameSession 重命名会话
func (s *salesSessionStore) RenameSession(ctx context.Context, sessionID uint, userID uint, newTitle string) error {
	return s.db.WithContext(ctx).Model(&model.SalesSession{}).
		Where("id = ? AND user_id = ?", sessionID, userID).
		Update("title", newTitle).Error
}
