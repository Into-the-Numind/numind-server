package store

import (
	"context"
	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// ILLMProviderStore LLM 供应商数据访问接口
type ILLMProviderStore interface {
	List(ctx context.Context, offset, limit int) ([]model.LLMProvider, int64, error)
	Get(ctx context.Context, id uint64) (*model.LLMProvider, error)
	Create(ctx context.Context, p *model.LLMProvider) error
	Update(ctx context.Context, p *model.LLMProvider) error
	Delete(ctx context.Context, id uint64) error
	ListActive(ctx context.Context) ([]model.LLMProvider, error)
}

type llmProviderStore struct {
	db *gorm.DB
}

var _ ILLMProviderStore = (*llmProviderStore)(nil)

// NewLLMProviderStore 创建 LLM 供应商 Store 实例
func NewLLMProviderStore(db *gorm.DB) ILLMProviderStore {
	return &llmProviderStore{db: db}
}

// List 分页查询所有 LLM 类型供应商（provider_type = 'llm'），排除 OCR/ASR 等其他类型
func (s *llmProviderStore) List(ctx context.Context, offset, limit int) ([]model.LLMProvider, int64, error) {
	var providers []model.LLMProvider
	var total int64

	query := s.db.WithContext(ctx).Model(&model.LLMProvider{}).Where("provider_type = ?", "llm")

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := query.Offset(offset).Limit(limit).Order("id ASC").Find(&providers).Error; err != nil {
		return nil, 0, err
	}

	return providers, total, nil
}

// Get 根据 ID 获取供应商
func (s *llmProviderStore) Get(ctx context.Context, id uint64) (*model.LLMProvider, error) {
	var provider model.LLMProvider
	if err := s.db.WithContext(ctx).First(&provider, id).Error; err != nil {
		return nil, err
	}
	return &provider, nil
}

// Create 创建供应商
func (s *llmProviderStore) Create(ctx context.Context, p *model.LLMProvider) error {
	return s.db.WithContext(ctx).Create(p).Error
}

// Update 更新供应商
func (s *llmProviderStore) Update(ctx context.Context, p *model.LLMProvider) error {
	return s.db.WithContext(ctx).Save(p).Error
}

// Delete 删除供应商
func (s *llmProviderStore) Delete(ctx context.Context, id uint64) error {
	return s.db.WithContext(ctx).Delete(&model.LLMProvider{}, id).Error
}

// ListActive 查询所有激活的 LLM 类型供应商（provider_type = 'llm'），按 id 排序
func (s *llmProviderStore) ListActive(ctx context.Context) ([]model.LLMProvider, error) {
	var providers []model.LLMProvider
	if err := s.db.WithContext(ctx).Where("provider_type = ? AND is_active = ?", "llm", true).Order("id ASC").Find(&providers).Error; err != nil {
		return nil, err
	}
	return providers, nil
}
