package store

import (
	"context"
	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// ILLMModelProviderStore 模型×供应商路由映射数据访问接口
type ILLMModelProviderStore interface {
	ListByModel(ctx context.Context, modelID uint64) ([]model.LLMModelProvider, error)
	ListActiveByModel(ctx context.Context, modelID uint64) ([]model.LLMModelProvider, error)
	Get(ctx context.Context, id uint64) (*model.LLMModelProvider, error)
	Create(ctx context.Context, mp *model.LLMModelProvider) error
	Update(ctx context.Context, mp *model.LLMModelProvider) error
	Delete(ctx context.Context, id uint64) error
}

type llmModelProviderStore struct {
	db *gorm.DB
}

var _ ILLMModelProviderStore = (*llmModelProviderStore)(nil)

// NewLLMModelProviderStore 创建模型×供应商路由映射 Store 实例
func NewLLMModelProviderStore(db *gorm.DB) ILLMModelProviderStore {
	return &llmModelProviderStore{db: db}
}

// ListByModel 查询指定模型的所有路由映射
func (s *llmModelProviderStore) ListByModel(ctx context.Context, modelID uint64) ([]model.LLMModelProvider, error) {
	var mps []model.LLMModelProvider
	if err := s.db.WithContext(ctx).
		Preload("Provider").
		Where("model_id = ?", modelID).
		Order("priority DESC, id ASC").
		Find(&mps).Error; err != nil {
		return nil, err
	}
	return mps, nil
}

// ListActiveByModel 查询指定模型的所有激活路由映射（仅返回供应商也激活的记录）
func (s *llmModelProviderStore) ListActiveByModel(ctx context.Context, modelID uint64) ([]model.LLMModelProvider, error) {
	var mps []model.LLMModelProvider
	if err := s.db.WithContext(ctx).
		Preload("Provider", "is_active = ?", true).
		Where("model_id = ? AND is_active = ?", modelID, true).
		Order("priority DESC, id ASC").
		Find(&mps).Error; err != nil {
		return nil, err
	}

	// 过滤掉因供应商未激活导致 Provider 为 nil 的记录
	active := mps[:0]
	for _, mp := range mps {
		if mp.Provider != nil {
			active = append(active, mp)
		}
	}
	return active, nil
}

// Get 根据 ID 获取路由映射
func (s *llmModelProviderStore) Get(ctx context.Context, id uint64) (*model.LLMModelProvider, error) {
	var mp model.LLMModelProvider
	if err := s.db.WithContext(ctx).First(&mp, id).Error; err != nil {
		return nil, err
	}
	return &mp, nil
}

// Create 创建路由映射
func (s *llmModelProviderStore) Create(ctx context.Context, mp *model.LLMModelProvider) error {
	return s.db.WithContext(ctx).Create(mp).Error
}

// Update 更新路由映射
func (s *llmModelProviderStore) Update(ctx context.Context, mp *model.LLMModelProvider) error {
	return s.db.WithContext(ctx).Save(mp).Error
}

// Delete 删除路由映射
func (s *llmModelProviderStore) Delete(ctx context.Context, id uint64) error {
	return s.db.WithContext(ctx).Delete(&model.LLMModelProvider{}, id).Error
}
