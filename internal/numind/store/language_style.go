package store

import (
	"context"
	"errors"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// LanguageStyleStore 定义了语言风格数据访问接口
type LanguageStyleStore interface {
	Save(ctx context.Context, style *model.LanguageStyle) error
	Get(ctx context.Context, userID uint) (*model.LanguageStyle, error)
}

type languageStyleStore struct {
	db *gorm.DB
}

func NewLanguageStyleStore(db *gorm.DB) LanguageStyleStore {
	return &languageStyleStore{db: db}
}

func (s *languageStyleStore) Save(ctx context.Context, style *model.LanguageStyle) error {
	var existing model.LanguageStyle
	err := s.db.WithContext(ctx).Where("user_id = ?", style.UserID).First(&existing).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return s.db.WithContext(ctx).Create(style).Error
		}
		return err
	}

	existing.Style = style.Style
	return s.db.WithContext(ctx).Save(&existing).Error
}

func (s *languageStyleStore) Get(ctx context.Context, userID uint) (*model.LanguageStyle, error) {
	var style model.LanguageStyle
	err := s.db.WithContext(ctx).Where("user_id = ?", userID).First(&style).Error
	if err != nil {
		return nil, err
	}
	return &style, nil
}
