package store

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

// IChatbotVisibilityGrantStore Chatbot 可见范围 grant store.
// 结构与 ISopVisibilityGrantStore 对称, 字段替换为 ChatbotID. 详见 spec §4.1 / §5.3 / §9 EC-6.
type IChatbotVisibilityGrantStore interface {
	// ListSubUserIDsByChatbotID 返回某 chatbot 的白名单子用户 ID (未软删).
	ListSubUserIDsByChatbotID(ctx context.Context, chatbotID uint) ([]uint, error)

	// ListVisibleChatbotIDsBySubUser 返回某子用户被授权可见的 chatbot ID set (未软删).
	ListVisibleChatbotIDsBySubUser(ctx context.Context, subUserID uint) (map[uint]struct{}, error)

	// CountBySubUserAndChatbot 返回 (sub_user_id, chatbot_id) 未软删的记录数.
	CountBySubUserAndChatbot(ctx context.Context, subUserID, chatbotID uint) (int64, error)

	// ReplaceGrantsTx 物理删全部该 chatbot 的现有 grant 后插入新 grant.
	// 用于 UpdateChatbotVisibility restricted=true 路径.
	ReplaceGrantsTx(ctx context.Context, tx *gorm.DB, chatbotID, parentUserID uint, subUserIDs []uint) error

	// CleanupBySubUser 软删某子用户的所有 chatbot grant (DeleteSubUser 路径).
	CleanupBySubUser(ctx context.Context, tx *gorm.DB, subUserID uint) error

	// CleanupByEntity 软删某 chatbot 的所有 grant (DeleteChatbot 路径, EC-6).
	CleanupByEntity(ctx context.Context, tx *gorm.DB, chatbotID uint) error
}

// chatbotVisibilityGrantStore IChatbotVisibilityGrantStore 的实现.
type chatbotVisibilityGrantStore struct {
	db *gorm.DB
}

// 确保 chatbotVisibilityGrantStore 实现了 IChatbotVisibilityGrantStore 接口.
var _ IChatbotVisibilityGrantStore = (*chatbotVisibilityGrantStore)(nil)

// NewChatbotVisibilityGrantStore 创建 ChatbotVisibilityGrant store 实例.
func NewChatbotVisibilityGrantStore(db *gorm.DB) *chatbotVisibilityGrantStore {
	return &chatbotVisibilityGrantStore{db: db}
}

// ListSubUserIDsByChatbotID 返回某 chatbot 的白名单子用户 ID.
func (s *chatbotVisibilityGrantStore) ListSubUserIDsByChatbotID(ctx context.Context, chatbotID uint) ([]uint, error) {
	var ids []uint
	if err := s.db.WithContext(ctx).
		Model(&model.ChatbotVisibilityGrant{}).
		Where("chatbot_id = ?", chatbotID).
		Pluck("sub_user_id", &ids).Error; err != nil {
		return nil, fmt.Errorf("ListSubUserIDsByChatbotID: %w", err)
	}
	return ids, nil
}

// ListVisibleChatbotIDsBySubUser 返回某子用户能看到的 chatbot ID set.
func (s *chatbotVisibilityGrantStore) ListVisibleChatbotIDsBySubUser(ctx context.Context, subUserID uint) (map[uint]struct{}, error) {
	var ids []uint
	if err := s.db.WithContext(ctx).
		Model(&model.ChatbotVisibilityGrant{}).
		Where("sub_user_id = ?", subUserID).
		Pluck("chatbot_id", &ids).Error; err != nil {
		return nil, fmt.Errorf("ListVisibleChatbotIDsBySubUser: %w", err)
	}
	set := make(map[uint]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set, nil
}

// CountBySubUserAndChatbot 返回 (sub_user_id, chatbot_id) 未软删记录数.
func (s *chatbotVisibilityGrantStore) CountBySubUserAndChatbot(ctx context.Context, subUserID, chatbotID uint) (int64, error) {
	var count int64
	if err := s.db.WithContext(ctx).
		Model(&model.ChatbotVisibilityGrant{}).
		Where("sub_user_id = ? AND chatbot_id = ?", subUserID, chatbotID).
		Count(&count).Error; err != nil {
		return 0, fmt.Errorf("CountBySubUserAndChatbot: %w", err)
	}
	return count, nil
}

// ReplaceGrantsTx 物理删 + 重插. Unscoped() 跳过软删 scope 避免唯一索引冲突.
func (s *chatbotVisibilityGrantStore) ReplaceGrantsTx(ctx context.Context, tx *gorm.DB, chatbotID, parentUserID uint, subUserIDs []uint) error {
	if err := tx.WithContext(ctx).Unscoped().
		Where("chatbot_id = ?", chatbotID).
		Delete(&model.ChatbotVisibilityGrant{}).Error; err != nil {
		return fmt.Errorf("ReplaceGrantsTx: physical delete: %w", err)
	}
	if len(subUserIDs) == 0 {
		return nil
	}
	records := make([]model.ChatbotVisibilityGrant, 0, len(subUserIDs))
	for _, uid := range subUserIDs {
		records = append(records, model.ChatbotVisibilityGrant{
			ParentUserID: parentUserID,
			SubUserID:    uid,
			ChatbotID:    chatbotID,
		})
	}
	if err := tx.WithContext(ctx).Create(&records).Error; err != nil {
		return fmt.Errorf("ReplaceGrantsTx: insert new grants: %w", err)
	}
	return nil
}

// CleanupBySubUser 软删该子用户的所有 chatbot grant.
// 保留软删记录用于审计 (谁在哪天失去过 visibility); 下次 ReplaceGrantsTx
// 通过 Unscoped() 物理清理, 避免唯一索引堆积.
func (s *chatbotVisibilityGrantStore) CleanupBySubUser(ctx context.Context, tx *gorm.DB, subUserID uint) error {
	if err := tx.WithContext(ctx).
		Where("sub_user_id = ?", subUserID).
		Delete(&model.ChatbotVisibilityGrant{}).Error; err != nil {
		return fmt.Errorf("CleanupBySubUser: %w", err)
	}
	return nil
}

// CleanupByEntity 软删该 chatbot 的所有 grant (EC-6).
func (s *chatbotVisibilityGrantStore) CleanupByEntity(ctx context.Context, tx *gorm.DB, chatbotID uint) error {
	if err := tx.WithContext(ctx).
		Where("chatbot_id = ?", chatbotID).
		Delete(&model.ChatbotVisibilityGrant{}).Error; err != nil {
		return fmt.Errorf("CleanupByEntity: %w", err)
	}
	return nil
}
