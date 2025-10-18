package store

import (
	"context"
	"time"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

type ConfigStore interface {
	Create(ctx context.Context, config *model.SystemConfigM) error
	GetByKey(ctx context.Context, key string) (*model.SystemConfigM, error)
	GetAll(ctx context.Context) ([]*model.SystemConfigM, error)
	Update(ctx context.Context, config *model.SystemConfigM) error
	Delete(ctx context.Context, key string) error
	InitDefaultConfigs(ctx context.Context) error
}

type configs struct {
	db *gorm.DB
}

var _ ConfigStore = (*configs)(nil)

func NewConfigStore(db *gorm.DB) ConfigStore {
	return &configs{db}
}

func (s *configs) Create(ctx context.Context, config *model.SystemConfigM) error {
	return s.db.WithContext(ctx).Create(config).Error
}

func (s *configs) GetByKey(ctx context.Context, key string) (*model.SystemConfigM, error) {
	var config model.SystemConfigM
	err := s.db.WithContext(ctx).Where("key = ?", key).First(&config).Error
	if err != nil {
		return nil, err
	}
	return &config, nil
}

func (s *configs) GetAll(ctx context.Context) ([]*model.SystemConfigM, error) {
	var configs []*model.SystemConfigM
	err := s.db.WithContext(ctx).Find(&configs).Error
	return configs, err
}

func (s *configs) Update(ctx context.Context, config *model.SystemConfigM) error {
	config.UpdatedAt = time.Now()
	return s.db.WithContext(ctx).Save(config).Error
}

func (s *configs) Delete(ctx context.Context, key string) error {
	return s.db.WithContext(ctx).Where("key = ?", key).Delete(&model.SystemConfigM{}).Error
}

func (s *configs) InitDefaultConfigs(ctx context.Context) error {
	defaultConfigs := []model.SystemConfigM{
		{
			Key:         "ai_prompt",
			Value:       "请对以下文章进行总结和分析：",
			Description: "AI分析文章的提示词",
		},
		{
			Key:         "max_articles_per_user",
			Value:       "1000",
			Description: "每个用户最大文章数量",
		},
		{
			Key:         "article_retention_days",
			Value:       "365",
			Description: "文章保留天数",
		},
	}

	for _, config := range defaultConfigs {
		var existing model.SystemConfigM
		err := s.db.WithContext(ctx).Where("key = ?", config.Key).First(&existing).Error
		if err == gorm.ErrRecordNotFound {
			s.db.WithContext(ctx).Create(&config)
		}
	}

	return nil
}
