package store

import (
	"context"
	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// KnowledgeDocumentStore 定义了知识库文档在数据库中的操作接口
type KnowledgeDocumentStore interface {
	Create(ctx context.Context, doc *model.KnowledgeDocument) error
	GetByID(ctx context.Context, id uint) (*model.KnowledgeDocument, error)
	ListByUser(ctx context.Context, userID uint) ([]*model.KnowledgeDocument, error)
	Update(ctx context.Context, doc *model.KnowledgeDocument) error
	UpdateStatus(ctx context.Context, id uint, status string, errorMsg string) error // 便捷状态更新方法
	Delete(ctx context.Context, id uint) error
	UpdateColumns(ctx context.Context, id uint, updates map[string]interface{}) error
}

type knowledgeDocuments struct {
	db *gorm.DB
}

func newKnowledgeDocuments(db *gorm.DB) *knowledgeDocuments {
	return &knowledgeDocuments{db}
}

func (s *knowledgeDocuments) Create(ctx context.Context, doc *model.KnowledgeDocument) error {
	return s.db.WithContext(ctx).Create(doc).Error
}

func (s *knowledgeDocuments) GetByID(ctx context.Context, id uint) (*model.KnowledgeDocument, error) {
	var doc model.KnowledgeDocument
	if err := s.db.WithContext(ctx).First(&doc, id).Error; err != nil {
		return nil, err
	}
	return &doc, nil
}

func (s *knowledgeDocuments) ListByUser(ctx context.Context, userID uint) ([]*model.KnowledgeDocument, error) {
	var docs []*model.KnowledgeDocument
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).Find(&docs).Error; err != nil {
		return nil, err
	}
	return docs, nil
}

func (s *knowledgeDocuments) Update(ctx context.Context, doc *model.KnowledgeDocument) error {
	return s.db.WithContext(ctx).Save(doc).Error
}

func (s *knowledgeDocuments) UpdateColumns(ctx context.Context, id uint, updates map[string]interface{}) error {
	return s.db.WithContext(ctx).Model(&model.KnowledgeDocument{}).Where("id = ?", id).Updates(updates).Error
}

// UpdateStatus 便捷方法：仅更新状态和错误信息字段
func (s *knowledgeDocuments) UpdateStatus(ctx context.Context, id uint, status string, errorMsg string) error {
	updates := map[string]interface{}{
		"status": status,
	}
	if errorMsg != "" {
		updates["error_msg"] = errorMsg
	}
	return s.UpdateColumns(ctx, id, updates)
}

func (s *knowledgeDocuments) Delete(ctx context.Context, id uint) error {
	return s.db.WithContext(ctx).Delete(&model.KnowledgeDocument{}, id).Error
}
