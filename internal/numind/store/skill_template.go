package store

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

// ISkillTemplateStore 定义 skill_template 表的只读存取接口。
// 注：模板只暴露 GET（list / by-id），不暴露 POST/PATCH/DELETE（见 model 注释）。
type ISkillTemplateStore interface {
	// List 列举所有激活的技能模板（is_active=1），按 display_order ASC 排序。
	List(ctx context.Context) ([]model.SkillTemplate, error)
	// GetByID 按 ID 查询技能模板（不过滤 is_active）。
	GetByID(ctx context.Context, id uint64) (*model.SkillTemplate, error)
}

type skillTemplateStore struct {
	db *gorm.DB
}

var _ ISkillTemplateStore = (*skillTemplateStore)(nil)

func newSkillTemplateStore(db *gorm.DB) ISkillTemplateStore {
	return &skillTemplateStore{db: db}
}

// List 列举所有激活的技能模板，按 display_order ASC 排序。
func (s *skillTemplateStore) List(ctx context.Context) ([]model.SkillTemplate, error) {
	var templates []model.SkillTemplate
	if err := s.db.WithContext(ctx).
		Where("is_active = 1").
		Order("display_order ASC").
		Find(&templates).Error; err != nil {
		return nil, fmt.Errorf("skillTemplateStore.List: %w", err)
	}
	return templates, nil
}

// GetByID 按 ID 查询技能模板（不过滤 is_active，供详情及 agent 关联校验使用）。
func (s *skillTemplateStore) GetByID(ctx context.Context, id uint64) (*model.SkillTemplate, error) {
	var t model.SkillTemplate
	if err := s.db.WithContext(ctx).First(&t, id).Error; err != nil {
		return nil, fmt.Errorf("skillTemplateStore.GetByID(id=%d): %w", id, err)
	}
	return &t, nil
}
