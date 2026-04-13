package store

import (
	"context"
	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// IChatbotConfigStore 智能体配置数据访问接口
type IChatbotConfigStore interface {
	Create(ctx context.Context, config *model.ChatbotConfig) error
	Get(ctx context.Context, id uint) (*model.ChatbotConfig, error)
	List(ctx context.Context, userID uint, offset, limit int) ([]model.ChatbotConfig, int64, error)
	Update(ctx context.Context, config *model.ChatbotConfig) error
	Delete(ctx context.Context, id uint) error
	UpdateStatus(ctx context.Context, id uint, status string) error

	// KB 挂载
	MountKnowledgeBases(ctx context.Context, chatbotID uint, kbIDs []uint) error
	ReplaceKnowledgeBases(ctx context.Context, chatbotID uint, kbIDs []uint) error
	UnmountKnowledgeBase(ctx context.Context, chatbotID uint, kbID uint) error
	ListMountedKBs(ctx context.Context, chatbotID uint) ([]model.KnowledgeBase, error)
	UnmountAllByKB(ctx context.Context, kbID uint) error

	// C端查询
	ListPublishedByOwner(ctx context.Context, ownerUserID uint) ([]model.ChatbotConfig, error)
}

type chatbotConfigStore struct {
	db *gorm.DB
}

var _ IChatbotConfigStore = (*chatbotConfigStore)(nil)

// NewChatbotConfigStore 创建智能体配置 Store 实例
func NewChatbotConfigStore(db *gorm.DB) IChatbotConfigStore {
	return &chatbotConfigStore{db: db}
}

// Create 创建智能体配置
func (s *chatbotConfigStore) Create(ctx context.Context, config *model.ChatbotConfig) error {
	return s.db.WithContext(ctx).Create(config).Error
}

// Get 根据 ID 获取智能体配置
func (s *chatbotConfigStore) Get(ctx context.Context, id uint) (*model.ChatbotConfig, error) {
	var config model.ChatbotConfig
	if err := s.db.WithContext(ctx).First(&config, id).Error; err != nil {
		return nil, err
	}
	return &config, nil
}

// List 获取用户的智能体配置列表（分页）
func (s *chatbotConfigStore) List(ctx context.Context, userID uint, offset, limit int) ([]model.ChatbotConfig, int64, error) {
	var configs []model.ChatbotConfig
	var total int64

	query := s.db.WithContext(ctx).Model(&model.ChatbotConfig{}).Where("user_id = ?", userID)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := query.Offset(offset).Limit(limit).Order("created_at DESC").Find(&configs).Error; err != nil {
		return nil, 0, err
	}

	return configs, total, nil
}

// Update 更新智能体配置
func (s *chatbotConfigStore) Update(ctx context.Context, config *model.ChatbotConfig) error {
	return s.db.WithContext(ctx).Save(config).Error
}

// Delete 删除智能体配置（软删除）
func (s *chatbotConfigStore) Delete(ctx context.Context, id uint) error {
	return s.db.WithContext(ctx).Delete(&model.ChatbotConfig{}, id).Error
}

// UpdateStatus 更新智能体状态
func (s *chatbotConfigStore) UpdateStatus(ctx context.Context, id uint, status string) error {
	return s.db.WithContext(ctx).Model(&model.ChatbotConfig{}).Where("id = ?", id).Update("status", status).Error
}

// MountKnowledgeBases 批量挂载知识库到智能体
func (s *chatbotConfigStore) MountKnowledgeBases(ctx context.Context, chatbotID uint, kbIDs []uint) error {
	if len(kbIDs) == 0 {
		return nil
	}

	var mounts []model.ChatbotKnowledgeBase
	for _, kbID := range kbIDs {
		mounts = append(mounts, model.ChatbotKnowledgeBase{
			ChatbotID:       chatbotID,
			KnowledgeBaseID: kbID,
		})
	}

	return s.db.WithContext(ctx).Create(&mounts).Error
}

// ReplaceKnowledgeBases 替换智能体的知识库挂载（事务：先清除再重建）
func (s *chatbotConfigStore) ReplaceKnowledgeBases(ctx context.Context, chatbotID uint, kbIDs []uint) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 清除所有现有挂载
		if err := tx.Where("chatbot_id = ?", chatbotID).Delete(&model.ChatbotKnowledgeBase{}).Error; err != nil {
			return err
		}

		// 重建挂载
		if len(kbIDs) == 0 {
			return nil
		}
		var mounts []model.ChatbotKnowledgeBase
		for _, kbID := range kbIDs {
			mounts = append(mounts, model.ChatbotKnowledgeBase{
				ChatbotID:       chatbotID,
				KnowledgeBaseID: kbID,
			})
		}
		return tx.Create(&mounts).Error
	})
}

// UnmountKnowledgeBase 卸载智能体的单个知识库
func (s *chatbotConfigStore) UnmountKnowledgeBase(ctx context.Context, chatbotID uint, kbID uint) error {
	return s.db.WithContext(ctx).
		Where("chatbot_id = ? AND knowledge_base_id = ?", chatbotID, kbID).
		Delete(&model.ChatbotKnowledgeBase{}).Error
}

// ListMountedKBs 获取智能体挂载的所有知识库
func (s *chatbotConfigStore) ListMountedKBs(ctx context.Context, chatbotID uint) ([]model.KnowledgeBase, error) {
	var kbs []model.KnowledgeBase

	err := s.db.WithContext(ctx).
		Joins("INNER JOIN chatbot_knowledge_base ON chatbot_knowledge_base.knowledge_base_id = knowledge_base.id").
		Where("chatbot_knowledge_base.chatbot_id = ?", chatbotID).
		Find(&kbs).Error

	return kbs, err
}

// UnmountAllByKB 删除某知识库的所有挂载关系（KB删除级联）
func (s *chatbotConfigStore) UnmountAllByKB(ctx context.Context, kbID uint) error {
	return s.db.WithContext(ctx).
		Where("knowledge_base_id = ?", kbID).
		Delete(&model.ChatbotKnowledgeBase{}).Error
}

// ListPublishedByOwner 获取指定用户已发布的智能体列表
func (s *chatbotConfigStore) ListPublishedByOwner(ctx context.Context, ownerUserID uint) ([]model.ChatbotConfig, error) {
	var configs []model.ChatbotConfig

	err := s.db.WithContext(ctx).
		Where("user_id = ? AND status = ?", ownerUserID, model.ChatbotStatusPublished).
		Order("created_at DESC").
		Find(&configs).Error

	return configs, err
}
