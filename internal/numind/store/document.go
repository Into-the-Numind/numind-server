package store

import (
	"context"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// IDocumentStore 定义文档系统的数据库操作接口（document-system v1）。
type IDocumentStore interface {
	// GetByUserAndSource 按 (user_id, source_object_key) 查文档；
	// 不存在返回 gorm.ErrRecordNotFound，由 biz 层显式处理（首次打开走建档分支）。
	GetByUserAndSource(ctx context.Context, userID uint, objectKey string) (*model.Document, error)
	// GetByID 按主键查文档；不存在返回 gorm.ErrRecordNotFound。
	GetByID(ctx context.Context, id uint64) (*model.Document, error)
	// Create 插入一条文档。
	Create(ctx context.Context, d *model.Document) error
	// UpdateContent 更新文档正文与标题（last-write-wins）。
	// 用 map 形式 Updates 避免 GORM 对零值字段跳过更新（见 .claude/rules/database.md §6b）。
	UpdateContent(ctx context.Context, id uint64, contentMD, title string) error
}

// documentStore 是 IDocumentStore 的 GORM 实现。
type documentStore struct {
	db *gorm.DB
}

// newDocumentStore 创建一个 IDocumentStore 实例。
func newDocumentStore(db *gorm.DB) *documentStore {
	return &documentStore{db: db}
}

// GetByUserAndSource 按 (user_id, source_object_key) 查文档。
func (s *documentStore) GetByUserAndSource(ctx context.Context, userID uint, objectKey string) (*model.Document, error) {
	var d model.Document
	if err := s.db.WithContext(ctx).
		Where("user_id = ? AND source_object_key = ?", userID, objectKey).
		First(&d).Error; err != nil {
		return nil, err
	}
	return &d, nil
}

// GetByID 按主键查文档。
func (s *documentStore) GetByID(ctx context.Context, id uint64) (*model.Document, error) {
	var d model.Document
	if err := s.db.WithContext(ctx).First(&d, id).Error; err != nil {
		return nil, err
	}
	return &d, nil
}

// Create 插入一条文档。
func (s *documentStore) Create(ctx context.Context, d *model.Document) error {
	return s.db.WithContext(ctx).Create(d).Error
}

// UpdateContent 更新文档正文与标题（last-write-wins）。
// 目标 id 不存在时返回 gorm.ErrRecordNotFound（检查 RowsAffected，避免静默 no-op）。
func (s *documentStore) UpdateContent(ctx context.Context, id uint64, contentMD, title string) error {
	res := s.db.WithContext(ctx).
		Model(&model.Document{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"content_md": contentMD,
			"title":      title,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
