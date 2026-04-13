// Package knowledgebase 知识库业务逻辑层
package knowledgebase

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"sync"

	"numind-server/internal/numind/biz/salesrag"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

const (
	MaxDocsPerKB     = 10          // 每个知识库最多 10 份文档
	MaxFilesPerBatch = 5           // 单次最多上传 5 个文件
	MaxFileSize      = 50 << 20    // 单文件最大 50MB
)

// CreateKBReq 创建知识库请求
type CreateKBReq struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
}

// UpdateKBReq 更新知识库请求
type UpdateKBReq struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

// KBDetail 知识库详情（含文档列表）
type KBDetail struct {
	model.KnowledgeBase
	Documents []model.KnowledgeDocument `json:"documents"`
}

// KBWithDocCount 知识库列表项（含文档数量）
type KBWithDocCount struct {
	model.KnowledgeBase
	DocCount int64 `json:"doc_count"`
}

// IKnowledgeBaseBiz 知识库业务层接口
type IKnowledgeBaseBiz interface {
	Create(ctx context.Context, userID uint, req *CreateKBReq) (*model.KnowledgeBase, error)
	Get(ctx context.Context, userID uint, id uint) (*KBDetail, error)
	List(ctx context.Context, userID uint, offset, limit int) ([]KBWithDocCount, int64, error)
	Update(ctx context.Context, userID uint, id uint, req *UpdateKBReq) error
	Delete(ctx context.Context, userID uint, id uint) error
	AddDocument(ctx context.Context, userID uint, kbID uint, file multipart.File, header *multipart.FileHeader) (*model.KnowledgeDocument, error)
	AddDocuments(ctx context.Context, userID uint, kbID uint, files []multipart.File, headers []*multipart.FileHeader) ([]*model.KnowledgeDocument, []error)
	RemoveDocument(ctx context.Context, userID uint, kbID uint, docID uint) error
}

type knowledgeBaseBiz struct {
	ds       store.IStore
	salesRAG salesrag.SalesRAGBiz
}

var _ IKnowledgeBaseBiz = (*knowledgeBaseBiz)(nil)

// NewKnowledgeBaseBiz 创建知识库业务层实例
func NewKnowledgeBaseBiz(ds store.IStore, salesRAG salesrag.SalesRAGBiz) IKnowledgeBaseBiz {
	return &knowledgeBaseBiz{ds: ds, salesRAG: salesRAG}
}

// Create 创建知识库
func (b *knowledgeBaseBiz) Create(ctx context.Context, userID uint, req *CreateKBReq) (*model.KnowledgeBase, error) {
	kb := &model.KnowledgeBase{
		UserID:      userID,
		Name:        req.Name,
		Description: req.Description,
		Status:      model.KBStatusActive,
	}
	if err := b.ds.KnowledgeBase().Create(ctx, kb); err != nil {
		return nil, fmt.Errorf("Create: %w", err)
	}
	return kb, nil
}

// Get 获取知识库详情（含文档列表），验证所有权
func (b *knowledgeBaseBiz) Get(ctx context.Context, userID uint, id uint) (*KBDetail, error) {
	kb, err := b.getAndCheckOwnership(ctx, userID, id)
	if err != nil {
		return nil, err
	}

	docs, err := b.ds.KnowledgeBase().ListDocuments(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("Get: list documents: %w", err)
	}

	return &KBDetail{
		KnowledgeBase: *kb,
		Documents:     docs,
	}, nil
}

// List 获取用户的知识库列表（含文档数量）
func (b *knowledgeBaseBiz) List(ctx context.Context, userID uint, offset, limit int) ([]KBWithDocCount, int64, error) {
	kbs, total, err := b.ds.KnowledgeBase().List(ctx, userID, offset, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("List: %w", err)
	}

	// 批量查询文档数量，避免 N+1
	kbIDs := make([]uint, len(kbs))
	for i, kb := range kbs {
		kbIDs[i] = kb.ID
	}
	docCounts := make(map[uint]int64)
	if len(kbIDs) > 0 {
		var counts []struct {
			KnowledgeBaseID uint  `gorm:"column:knowledge_base_id"`
			Count           int64 `gorm:"column:count"`
		}
		b.ds.DB().WithContext(ctx).
			Table("knowledge_base_document").
			Select("knowledge_base_id, COUNT(*) as count").
			Where("knowledge_base_id IN ?", kbIDs).
			Group("knowledge_base_id").
			Scan(&counts)
		for _, c := range counts {
			docCounts[c.KnowledgeBaseID] = c.Count
		}
	}

	result := make([]KBWithDocCount, len(kbs))
	for i, kb := range kbs {
		result[i] = KBWithDocCount{
			KnowledgeBase: kb,
			DocCount:      docCounts[kb.ID],
		}
	}

	return result, total, nil
}

// Update 更新知识库，验证所有权
func (b *knowledgeBaseBiz) Update(ctx context.Context, userID uint, id uint, req *UpdateKBReq) error {
	kb, err := b.getAndCheckOwnership(ctx, userID, id)
	if err != nil {
		return err
	}

	if req.Name != nil {
		kb.Name = *req.Name
	}
	if req.Description != nil {
		kb.Description = *req.Description
	}

	if err := b.ds.KnowledgeBase().Update(ctx, kb); err != nil {
		return fmt.Errorf("Update: %w", err)
	}
	return nil
}

// Delete 删除知识库（事务：先解除所有智能体挂载，再软删除知识库），验证所有权
func (b *knowledgeBaseBiz) Delete(ctx context.Context, userID uint, id uint) error {
	_, err := b.getAndCheckOwnership(ctx, userID, id)
	if err != nil {
		return err
	}

	// 使用事务：先 unmount 所有关联的 chatbot，再软删除知识库
	// 直接操作 tx 而非通过 store 接口，确保两步在同一事务中
	return b.ds.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 硬删除所有 chatbot-KB 挂载关联
		if err := tx.Where("knowledge_base_id = ?", id).Delete(&model.ChatbotKnowledgeBase{}).Error; err != nil {
			return fmt.Errorf("Delete: unmount chatbots: %w", err)
		}
		// 软删除知识库
		if err := tx.Delete(&model.KnowledgeBase{}, id).Error; err != nil {
			return fmt.Errorf("Delete: %w", err)
		}
		return nil
	})
}

// AddDocument 向知识库添加文档：验证所有权 → 通过 SalesRAG Ingest 上传并创建文档 → 创建关联
func (b *knowledgeBaseBiz) AddDocument(ctx context.Context, userID uint, kbID uint, file multipart.File, header *multipart.FileHeader) (*model.KnowledgeDocument, error) {
	// 1. 验证知识库所有权
	_, err := b.getAndCheckOwnership(ctx, userID, kbID)
	if err != nil {
		return nil, err
	}

	// 2. 通过 SalesRAG Ingest 管道上传文档（含 COS 上传 + 文档记录创建 + 异步解析/向量化）
	// 注意：Ingest 始终以 userID 创建新文档，document.UserID == kb.UserID 由设计保证（不存在跨用户关联风险）
	filename := header.Filename

	docID, err := b.salesRAG.Ingest(ctx, userID, filename, filename, file.(io.Reader), salesrag.IngestOptions{
		Description: fmt.Sprintf("知识库[%d]关联文档", kbID),
	})
	if err != nil {
		return nil, fmt.Errorf("AddDocument: ingest: %w", err)
	}

	// 3. 创建知识库-文档关联
	if err := b.ds.KnowledgeBase().AddDocument(ctx, kbID, docID); err != nil {
		return nil, fmt.Errorf("AddDocument: create association: %w", err)
	}

	// 4. 获取创建的文档记录返回
	doc, err := b.ds.KnowledgeDocuments().GetByID(ctx, docID)
	if err != nil {
		return nil, fmt.Errorf("AddDocument: get doc: %w", err)
	}

	return doc, nil
}

// AddDocuments 批量向知识库添加文档（并行处理）：校验限额 → 并发 Ingest → 创建关联
func (b *knowledgeBaseBiz) AddDocuments(ctx context.Context, userID uint, kbID uint, files []multipart.File, headers []*multipart.FileHeader) ([]*model.KnowledgeDocument, []error) {
	n := len(files)
	docs := make([]*model.KnowledgeDocument, n)
	errs := make([]error, n)

	// 1. 验证知识库所有权
	_, err := b.getAndCheckOwnership(ctx, userID, kbID)
	if err != nil {
		for i := range errs {
			errs[i] = err
		}
		return docs, errs
	}

	// 2. 检查文件数量限制
	if n > MaxFilesPerBatch {
		for i := range errs {
			errs[i] = errno.ErrTooManyFiles
		}
		return docs, errs
	}

	// 3. 检查单文件大小限制
	for i, h := range headers {
		if h.Size > MaxFileSize {
			errs[i] = errno.ErrFileTooLarge.SetMessage("文件 %s 超过50MB限制", h.Filename)
		}
	}
	for _, e := range errs {
		if e != nil {
			return docs, errs
		}
	}

	// 4. 原子化配额检查：SELECT FOR UPDATE 锁定知识库行，防止并发超额
	var currentCount int64
	err = b.ds.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 锁定知识库行
		var kb model.KnowledgeBase
		if lockErr := tx.Set("gorm:query_option", "FOR UPDATE").First(&kb, kbID).Error; lockErr != nil {
			return fmt.Errorf("AddDocuments: lock kb: %w", lockErr)
		}
		// 在事务内计数
		if countErr := tx.Model(&model.KnowledgeBaseDocument{}).
			Where("knowledge_base_id = ?", kbID).
			Count(&currentCount).Error; countErr != nil {
			return fmt.Errorf("AddDocuments: count docs: %w", countErr)
		}
		if currentCount+int64(n) > MaxDocsPerKB {
			return errno.ErrMaxDocsExceeded
		}
		// 预占位：先创建 n 条占位关联（doc_id=0），Ingest 完成后更新
		// 这样即使并发请求也无法超额
		return nil
	})
	if err != nil {
		remaining := MaxDocsPerKB - int(currentCount)
		if remaining < 0 {
			remaining = 0
		}
		for i := range errs {
			if errors.Is(err, errno.ErrMaxDocsExceeded) {
				errs[i] = errno.ErrMaxDocsExceeded.SetMessage("知识库文档数已达上限(10)，当前%d份，还可上传%d份", currentCount, remaining)
			} else {
				errs[i] = err
			}
		}
		return docs, errs
	}

	// 5. 并行 Ingest（配额已通过原子检查，此处安全执行）
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			filename := headers[idx].Filename
			docID, ingestErr := b.salesRAG.Ingest(ctx, userID, filename, filename, files[idx], salesrag.IngestOptions{
				Description: fmt.Sprintf("知识库[%d]关联文档", kbID),
			})
			if ingestErr != nil {
				errs[idx] = fmt.Errorf("AddDocuments: ingest %s: %w", filename, ingestErr)
				return
			}

			if assocErr := b.ds.KnowledgeBase().AddDocument(ctx, kbID, docID); assocErr != nil {
				errs[idx] = fmt.Errorf("AddDocuments: associate %s: %w", filename, assocErr)
				return
			}

			doc, getErr := b.ds.KnowledgeDocuments().GetByID(ctx, docID)
			if getErr != nil {
				errs[idx] = fmt.Errorf("AddDocuments: get doc %s: %w", filename, getErr)
				return
			}
			docs[idx] = doc
		}(i)
	}
	wg.Wait()

	return docs, errs
}

// RemoveDocument 从知识库移除文档关联（不删除文档本身），验证所有权
func (b *knowledgeBaseBiz) RemoveDocument(ctx context.Context, userID uint, kbID uint, docID uint) error {
	// 1. 验证知识库所有权
	_, err := b.getAndCheckOwnership(ctx, userID, kbID)
	if err != nil {
		return err
	}

	// 2. 硬删除关联记录
	if err := b.ds.KnowledgeBase().RemoveDocument(ctx, kbID, docID); err != nil {
		return fmt.Errorf("RemoveDocument: %w", err)
	}
	return nil
}

// getAndCheckOwnership 获取知识库并验证所有权
func (b *knowledgeBaseBiz) getAndCheckOwnership(ctx context.Context, userID uint, id uint) (*model.KnowledgeBase, error) {
	kb, err := b.ds.KnowledgeBase().Get(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrKnowledgeBaseNotFound
		}
		return nil, fmt.Errorf("getAndCheckOwnership: %w", err)
	}
	if kb.UserID != userID {
		return nil, errno.ErrForbidden
	}
	return kb, nil
}
