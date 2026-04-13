package store

import (
	"context"
	"strings"
	"unicode/utf8"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// sanitizeUTF8 清理字符串中无效的 UTF-8 字节序列
// 避免 MySQL Error 1366: Incorrect string value
func sanitizeUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	// 移除无效的 UTF-8 字节
	var builder strings.Builder
	builder.Grow(len(s))
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && size == 1 {
			// 跳过无效字节
			s = s[1:]
			continue
		}
		builder.WriteRune(r)
		s = s[size:]
	}
	return builder.String()
}

// KnowledgeChunkStore 定义知识切片的数据库操作接口
type KnowledgeChunkStore interface {
	// Create 创建单个切片
	Create(ctx context.Context, chunk *model.KnowledgeChunk) error

	// BatchCreate 批量创建切片（使用事务）
	BatchCreate(ctx context.Context, chunks []*model.KnowledgeChunk) error

	// GetByID 根据ID获取切片
	GetByID(ctx context.Context, id uint) (*model.KnowledgeChunk, error)

	// ListByDocument 列出文档的所有切片（按sequence排序）
	ListByDocument(ctx context.Context, documentID uint, limit int) ([]*model.KnowledgeChunk, error)

	// ListByDocumentAndUser 列出文档的所有切片（带用户权限检查）
	ListByDocumentAndUser(ctx context.Context, documentID uint, userID uint, limit int) ([]*model.KnowledgeChunk, error)

	// Update 更新切片
	Update(ctx context.Context, chunk *model.KnowledgeChunk) error

	// UpdateColumns 更新特定字段
	UpdateColumns(ctx context.Context, id uint, updates map[string]interface{}) error

	// DeleteByDocument 删除文档的所有切片
	DeleteByDocument(ctx context.Context, documentID uint) error

	// GetByVectorID 根据向量数据库ID获取切片
	GetByVectorID(ctx context.Context, vectorID string) (*model.KnowledgeChunk, error)

	// CountByDocument 统计文档的切片数量
	CountByDocument(ctx context.Context, documentID uint) (int64, error)
}

type knowledgeChunks struct {
	db *gorm.DB
}

func newKnowledgeChunks(db *gorm.DB) *knowledgeChunks {
	return &knowledgeChunks{db}
}

// Create 创建单个切片
func (s *knowledgeChunks) Create(ctx context.Context, chunk *model.KnowledgeChunk) error {
	return s.db.WithContext(ctx).Create(chunk).Error
}

// BatchCreate 批量创建切片（使用事务）
func (s *knowledgeChunks) BatchCreate(ctx context.Context, chunks []*model.KnowledgeChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	// 清理每个 chunk 的 Content 和 Summary 字段，移除无效 UTF-8 字节
	for _, chunk := range chunks {
		chunk.Content = sanitizeUTF8(chunk.Content)
		chunk.Summary = sanitizeUTF8(chunk.Summary)
	}
	// 使用批量插入，每批100条
	return s.db.WithContext(ctx).CreateInBatches(chunks, 100).Error
}

// GetByID 根据ID获取切片
func (s *knowledgeChunks) GetByID(ctx context.Context, id uint) (*model.KnowledgeChunk, error) {
	var chunk model.KnowledgeChunk
	if err := s.db.WithContext(ctx).First(&chunk, id).Error; err != nil {
		return nil, err
	}
	return &chunk, nil
}

// ListByDocument 列出文档的所有切片（按sequence排序）
func (s *knowledgeChunks) ListByDocument(ctx context.Context, documentID uint, limit int) ([]*model.KnowledgeChunk, error) {
	var chunks []*model.KnowledgeChunk
	query := s.db.WithContext(ctx).Where("document_id = ?", documentID).Order("sequence ASC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if err := query.Find(&chunks).Error; err != nil {
		return nil, err
	}
	return chunks, nil
}

// ListByDocumentAndUser 列出文档的所有切片（带用户权限检查）
func (s *knowledgeChunks) ListByDocumentAndUser(ctx context.Context, documentID uint, userID uint, limit int) ([]*model.KnowledgeChunk, error) {
	var chunks []*model.KnowledgeChunk
	query := s.db.WithContext(ctx).
		Where("document_id = ? AND user_id = ?", documentID, userID).
		Order("sequence ASC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if err := query.Find(&chunks).Error; err != nil {
		return nil, err
	}
	return chunks, nil
}

// Update 更新切片
func (s *knowledgeChunks) Update(ctx context.Context, chunk *model.KnowledgeChunk) error {
	return s.db.WithContext(ctx).Save(chunk).Error
}

// UpdateColumns 更新特定字段
func (s *knowledgeChunks) UpdateColumns(ctx context.Context, id uint, updates map[string]interface{}) error {
	return s.db.WithContext(ctx).Model(&model.KnowledgeChunk{}).Where("id = ?", id).Updates(updates).Error
}

// DeleteByDocument 删除文档的所有切片（软删除）
func (s *knowledgeChunks) DeleteByDocument(ctx context.Context, documentID uint) error {
	return s.db.WithContext(ctx).Where("document_id = ?", documentID).Delete(&model.KnowledgeChunk{}).Error
}

// GetByVectorID 根据向量数据库ID获取切片
func (s *knowledgeChunks) GetByVectorID(ctx context.Context, vectorID string) (*model.KnowledgeChunk, error) {
	var chunk model.KnowledgeChunk
	if err := s.db.WithContext(ctx).Where("vector_id = ?", vectorID).First(&chunk).Error; err != nil {
		return nil, err
	}
	return &chunk, nil
}

// CountByDocument 统计文档的切片数量
func (s *knowledgeChunks) CountByDocument(ctx context.Context, documentID uint) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&model.KnowledgeChunk{}).Where("document_id = ?", documentID).Count(&count).Error
	return count, err
}
