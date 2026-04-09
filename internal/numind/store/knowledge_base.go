package store

import (
	"context"
	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// IKnowledgeBaseStore 知识库数据访问接口
type IKnowledgeBaseStore interface {
	Create(ctx context.Context, kb *model.KnowledgeBase) error
	Get(ctx context.Context, id uint) (*model.KnowledgeBase, error)
	List(ctx context.Context, userID uint, offset, limit int) ([]model.KnowledgeBase, int64, error)
	Update(ctx context.Context, kb *model.KnowledgeBase) error
	Delete(ctx context.Context, id uint) error

	// 文档关联
	AddDocument(ctx context.Context, kbID uint, docID uint) error
	RemoveDocument(ctx context.Context, kbID uint, docID uint) error
	ListDocuments(ctx context.Context, kbID uint) ([]model.KnowledgeDocument, error)
	ListDocumentIDsByKBs(ctx context.Context, kbIDs []uint) ([]uint, error)
	CountDocuments(ctx context.Context, kbID uint) (int64, error)
}

type knowledgeBaseStore struct {
	db *gorm.DB
}

var _ IKnowledgeBaseStore = (*knowledgeBaseStore)(nil)

// NewKnowledgeBaseStore 创建知识库 Store 实例
func NewKnowledgeBaseStore(db *gorm.DB) IKnowledgeBaseStore {
	return &knowledgeBaseStore{db: db}
}

// Create 创建知识库
func (s *knowledgeBaseStore) Create(ctx context.Context, kb *model.KnowledgeBase) error {
	return s.db.WithContext(ctx).Create(kb).Error
}

// Get 根据 ID 获取知识库
func (s *knowledgeBaseStore) Get(ctx context.Context, id uint) (*model.KnowledgeBase, error) {
	var kb model.KnowledgeBase
	if err := s.db.WithContext(ctx).First(&kb, id).Error; err != nil {
		return nil, err
	}
	return &kb, nil
}

// List 获取用户的知识库列表（分页）
func (s *knowledgeBaseStore) List(ctx context.Context, userID uint, offset, limit int) ([]model.KnowledgeBase, int64, error) {
	var kbs []model.KnowledgeBase
	var total int64

	query := s.db.WithContext(ctx).Model(&model.KnowledgeBase{}).Where("user_id = ?", userID)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := query.Offset(offset).Limit(limit).Order("created_at DESC").Find(&kbs).Error; err != nil {
		return nil, 0, err
	}

	return kbs, total, nil
}

// Update 更新知识库
func (s *knowledgeBaseStore) Update(ctx context.Context, kb *model.KnowledgeBase) error {
	return s.db.WithContext(ctx).Save(kb).Error
}

// Delete 删除知识库（软删除）
func (s *knowledgeBaseStore) Delete(ctx context.Context, id uint) error {
	return s.db.WithContext(ctx).Delete(&model.KnowledgeBase{}, id).Error
}

// AddDocument 将文档添加到知识库
func (s *knowledgeBaseStore) AddDocument(ctx context.Context, kbID uint, docID uint) error {
	doc := &model.KnowledgeBaseDocument{
		KnowledgeBaseID: kbID,
		DocumentID:      docID,
	}
	return s.db.WithContext(ctx).Create(doc).Error
}

// RemoveDocument 从知识库移除文档
func (s *knowledgeBaseStore) RemoveDocument(ctx context.Context, kbID uint, docID uint) error {
	return s.db.WithContext(ctx).
		Where("knowledge_base_id = ? AND document_id = ?", kbID, docID).
		Delete(&model.KnowledgeBaseDocument{}).Error
}

// ListDocuments 获取知识库下的所有文档
func (s *knowledgeBaseStore) ListDocuments(ctx context.Context, kbID uint) ([]model.KnowledgeDocument, error) {
	var docs []model.KnowledgeDocument

	err := s.db.WithContext(ctx).
		Joins("INNER JOIN knowledge_base_document ON knowledge_base_document.document_id = knowledge_document.id").
		Where("knowledge_base_document.knowledge_base_id = ?", kbID).
		Find(&docs).Error

	return docs, err
}

// CountDocuments 统计知识库下的文档数量
func (s *knowledgeBaseStore) CountDocuments(ctx context.Context, kbID uint) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).
		Model(&model.KnowledgeBaseDocument{}).
		Where("knowledge_base_id = ?", kbID).
		Count(&count).Error
	return count, err
}

// ListDocumentIDsByKBs 批量获取多个知识库下状态为 COMPLETED 的文档 ID
func (s *knowledgeBaseStore) ListDocumentIDsByKBs(ctx context.Context, kbIDs []uint) ([]uint, error) {
	if len(kbIDs) == 0 {
		return nil, nil
	}

	var ids []uint
	err := s.db.WithContext(ctx).
		Table("knowledge_base_document").
		Select("DISTINCT knowledge_base_document.document_id").
		Joins("INNER JOIN knowledge_document ON knowledge_document.id = knowledge_base_document.document_id").
		Where("knowledge_base_document.knowledge_base_id IN ? AND knowledge_document.status = ? AND knowledge_document.deleted_at IS NULL", kbIDs, "COMPLETED").
		Pluck("knowledge_base_document.document_id", &ids).Error

	return ids, err
}
