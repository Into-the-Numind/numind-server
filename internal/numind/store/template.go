package store

import (
	"context"
	"errors"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

type TemplateStore interface {
	Create(ctx context.Context, template *model.Template) error
	GetByID(ctx context.Context, id uint) (*model.Template, error)
	List(ctx context.Context, offset, limit int) (int64, []*model.Template, error)
	Update(ctx context.Context, template *model.Template) error
	Delete(ctx context.Context, id uint) error
	GetByName(ctx context.Context, name string) (*model.Template, error)
}

type templates struct {
	db *gorm.DB
}

var _ TemplateStore = (*templates)(nil)

func NewTemplateStore(db *gorm.DB) TemplateStore {
	return &templates{db}
}

func (s *templates) Create(ctx context.Context, template *model.Template) error {
	return s.db.WithContext(ctx).Create(template).Error
}

func (s *templates) GetByID(ctx context.Context, id uint) (*model.Template, error) {
	var template model.Template
	err := s.db.WithContext(ctx).First(&template, id).Error
	if err != nil {
		return nil, err
	}
	return &template, nil
}

func (s *templates) List(ctx context.Context, offset, limit int) (count int64, ret []*model.Template, err error) {
	err = s.db.WithContext(ctx).
		Offset(offset).Limit(defaultLimit(limit)).Order("id desc").Find(&ret).
		Offset(-1).Limit(-1).Count(&count).Error
	return
}

func (s *templates) Update(ctx context.Context, template *model.Template) error {
	return s.db.WithContext(ctx).Save(template).Error
}

func (s *templates) Delete(ctx context.Context, id uint) error {
	err := s.db.WithContext(ctx).Delete(&model.Template{}, id).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return nil
}

func (s *templates) GetByName(ctx context.Context, name string) (*model.Template, error) {
	var template model.Template
	err := s.db.WithContext(ctx).Where("name = ?", name).First(&template).Error
	if err != nil {
		return nil, err
	}
	return &template, nil
}
