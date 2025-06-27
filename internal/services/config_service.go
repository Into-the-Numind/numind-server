package services

import (
	"numind-server/internal/pkg/model"
	"time"

	"gorm.io/gorm"
)

type ConfigService struct {
	db *gorm.DB
}

type ConfigUpdateRequest struct {
	Value       string `json:"value" binding:"required"`
	Description string `json:"description"`
}

func NewConfigService(db *gorm.DB) *ConfigService {
	return &ConfigService{
		db: db,
	}
}

// GetConfigs 获取所有配置
func (s *ConfigService) GetConfigs() ([]model.SystemConfigM, error) {
	var configs []model.SystemConfigM
	err := s.db.Find(&configs).Error
	return configs, err
}

// GetConfig 获取单个配置
func (s *ConfigService) GetConfig(key string) (*model.SystemConfigM, error) {
	var config model.SystemConfigM
	err := s.db.Where("key = ?", key).First(&config).Error
	return &config, err
}

// UpdateConfig 更新配置
func (s *ConfigService) UpdateConfig(key string, req *ConfigUpdateRequest) error {
	var config model.SystemConfigM
	err := s.db.Where("key = ?", key).First(&config).Error
	if err == gorm.ErrRecordNotFound {
		// 创建新配置
		config = model.SystemConfigM{
			Key:         key,
			Value:       req.Value,
			Description: req.Description,
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}
		return s.db.Create(&config).Error
	}

	// 更新现有配置
	config.Value = req.Value
	if req.Description != "" {
		config.Description = req.Description
	}
	config.UpdatedAt = time.Now()

	return s.db.Save(&config).Error
}

// DeleteConfig 删除配置
func (s *ConfigService) DeleteConfig(key string) error {
	return s.db.Where("key = ?", key).Delete(&model.SystemConfigM{}).Error
}

// InitDefaultConfigs 初始化默认配置
func (s *ConfigService) InitDefaultConfigs() error {
	defaultConfigs := []model.SystemConfigM{
		{
			Key:         "ai_prompt",
			Value:       "请对以下文章进行总结和分析：",
			Description: "AI分析文章的提示词",
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		},
		{
			Key:         "max_articles_per_user",
			Value:       "1000",
			Description: "每个用户最大文章数量",
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		},
		{
			Key:         "article_retention_days",
			Value:       "365",
			Description: "文章保留天数",
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		},
	}

	for _, config := range defaultConfigs {
		var existing model.SystemConfigM
		err := s.db.Where("key = ?", config.Key).First(&existing).Error
		if err == gorm.ErrRecordNotFound {
			s.db.Create(&config)
		}
	}

	return nil
}
