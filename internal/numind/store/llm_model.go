package store

import (
	"context"
	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// ILLMModelStore LLM 模型数据访问接口
type ILLMModelStore interface {
	List(ctx context.Context, offset, limit int) ([]model.LLMModel, int64, error)
	Get(ctx context.Context, id uint64) (*model.LLMModel, error)
	GetByKey(ctx context.Context, key string) (*model.LLMModel, error)
	Create(ctx context.Context, m *model.LLMModel) error
	Update(ctx context.Context, m *model.LLMModel) error
	Delete(ctx context.Context, id uint64) error
	ListActiveBase(ctx context.Context) ([]model.LLMModel, error)
	GetThinkingVariant(ctx context.Context, baseModelID uint64) (*model.LLMModel, error)
	GetDefaultModel(ctx context.Context) (*model.LLMModel, error)
}

type llmModelStore struct {
	db *gorm.DB
}

var _ ILLMModelStore = (*llmModelStore)(nil)

// NewLLMModelStore 创建 LLM 模型 Store 实例
func NewLLMModelStore(db *gorm.DB) ILLMModelStore {
	return &llmModelStore{db: db}
}

// List 分页查询所有模型
func (s *llmModelStore) List(ctx context.Context, offset, limit int) ([]model.LLMModel, int64, error) {
	var models []model.LLMModel
	var total int64

	query := s.db.WithContext(ctx).Model(&model.LLMModel{})

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := query.Offset(offset).Limit(limit).Order("sort_order ASC, id ASC").Find(&models).Error; err != nil {
		return nil, 0, err
	}

	return models, total, nil
}

// Get 根据 ID 获取模型
func (s *llmModelStore) Get(ctx context.Context, id uint64) (*model.LLMModel, error) {
	var m model.LLMModel
	if err := s.db.WithContext(ctx).First(&m, id).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// GetByKey 根据 model_key 获取模型
func (s *llmModelStore) GetByKey(ctx context.Context, key string) (*model.LLMModel, error) {
	var m model.LLMModel
	if err := s.db.WithContext(ctx).Where("model_key = ?", key).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// Create 创建模型
func (s *llmModelStore) Create(ctx context.Context, m *model.LLMModel) error {
	return s.db.WithContext(ctx).Create(m).Error
}

// Update 更新模型
func (s *llmModelStore) Update(ctx context.Context, m *model.LLMModel) error {
	return s.db.WithContext(ctx).Save(m).Error
}

// Delete 删除模型（事务内先清理关联路由和 base_model_id 引用）
func (s *llmModelStore) Delete(ctx context.Context, id uint64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("model_id = ?", id).Delete(&model.LLMModelProvider{}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.LLMModel{}).Where("base_model_id = ?", id).Update("base_model_id", nil).Error; err != nil {
			return err
		}
		return tx.Delete(&model.LLMModel{}, id).Error
	})
}

// ListActiveBase 查询所有激活的基础模型（非思考模式变体），按 sort_order 排序
func (s *llmModelStore) ListActiveBase(ctx context.Context) ([]model.LLMModel, error) {
	var models []model.LLMModel
	if err := s.db.WithContext(ctx).
		Where("is_active = ? AND is_thinking = ?", true, false).
		Order("sort_order ASC, id ASC").
		Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// GetThinkingVariant 获取指定基础模型的思考模式变体
func (s *llmModelStore) GetThinkingVariant(ctx context.Context, baseModelID uint64) (*model.LLMModel, error) {
	var m model.LLMModel
	if err := s.db.WithContext(ctx).
		Where("base_model_id = ? AND is_thinking = ? AND is_active = ?", baseModelID, true, true).
		First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// GetDefaultModel 获取默认模型（sort_order 最小的激活基础模型）
func (s *llmModelStore) GetDefaultModel(ctx context.Context) (*model.LLMModel, error) {
	var m model.LLMModel
	if err := s.db.WithContext(ctx).
		Where("is_active = ? AND is_thinking = ?", true, false).
		Order("sort_order ASC, id ASC").
		First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}
