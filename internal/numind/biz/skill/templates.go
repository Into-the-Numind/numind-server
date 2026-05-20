package skill

import (
	"context"
	"fmt"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// TemplateService thin wrapper over ISkillTemplateStore.
// service.ListTemplates 直接调本包，不重复 biz 逻辑（v1 仅列表查询）。
type TemplateService struct {
	store store.ISkillTemplateStore
}

// NewTemplateService 构造 TemplateService。
func NewTemplateService(s store.ISkillTemplateStore) *TemplateService {
	return &TemplateService{store: s}
}

// List 列举所有激活的技能模板（is_active=1），按 display_order ASC 排序。
func (s *TemplateService) List(ctx context.Context) ([]model.SkillTemplate, error) {
	list, err := s.store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("TemplateService.List: %w", err)
	}
	return list, nil
}

// GetByID 按 ID 查询技能模板（不过滤 is_active，供 agent 关联校验使用）。
func (s *TemplateService) GetByID(ctx context.Context, id uint64) (*model.SkillTemplate, error) {
	t, err := s.store.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("TemplateService.GetByID(id=%d): %w", id, err)
	}
	return t, nil
}
