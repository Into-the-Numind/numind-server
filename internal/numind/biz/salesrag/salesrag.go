package salesrag

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"numind-server/internal/numind/biz/salesrag/domain"
	"numind-server/internal/numind/biz/salesrag/service"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// SalesRAGBiz 定义了销售 RAG 业务层的对外接口
type SalesRAGBiz interface {
	// Ingest 处理文档导入
	Ingest(ctx context.Context, userID uint, filename string, reader io.Reader, opts IngestOptions) (uint, error)
	// Retrieve 检索知识
	Retrieve(ctx context.Context, query string, stage domain.SalesStage, docIDs []uint) (*service.RetrievalVerdict, error)
	// ListDocuments 获取用户的文档列表
	ListDocuments(ctx context.Context, userID uint) ([]domain.KnowledgeDocument, error)
	// GetDocument 获取单个文档详情
	GetDocument(ctx context.Context, userID uint, docID uint) (*domain.KnowledgeDocument, error)
	// UpdateDocument 更新文档信息
	UpdateDocument(ctx context.Context, userID uint, docID uint, req UpdateDocumentRequest) error
	// DeleteDocument 删除文档
	DeleteDocument(ctx context.Context, userID uint, docID uint) error
}

type IngestOptions struct {
	Description string
	Tags        []string
	Type        string
}

type UpdateDocumentRequest struct {
	Description *string
	Tags        []string
	IsEnabled   *bool
}

type salesRAGBiz struct {
	ds                store.IStore
	ingestionPipeline *service.IngestionPipeline
	ragSvc            *service.SalesRAGService
}

func NewSalesRAGBiz(ds store.IStore, pipeline *service.IngestionPipeline, rag *service.SalesRAGService) SalesRAGBiz {
	return &salesRAGBiz{
		ds:                ds,
		ingestionPipeline: pipeline,
		ragSvc:            rag,
	}
}

func (b *salesRAGBiz) Ingest(ctx context.Context, userID uint, filename string, reader io.Reader, opts IngestOptions) (uint, error) {
	// 1. Save file locally
	// Ensure uploads directory exists
	uploadDir := "uploads/sales_rag"
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		return 0, fmt.Errorf("failed to create upload directory: %w", err)
	}

	// Create unique filename
	uniqueName := fmt.Sprintf("%d_%s", userID, filename)
	filePath := filepath.Join(uploadDir, uniqueName)

	dst, err := os.Create(filePath)
	if err != nil {
		return 0, fmt.Errorf("failed to create file: %w", err)
	}
	defer dst.Close()

	written, err := io.Copy(dst, reader)
	if err != nil {
		return 0, fmt.Errorf("failed to save file: %w", err)
	}

	// Tags 序列化
	tagsJson := "[]"
	if len(opts.Tags) > 0 {
		bytes, _ := json.Marshal(opts.Tags)
		tagsJson = string(bytes)
	}

	// 2. 创建文档记录
	doc := &model.KnowledgeDocument{
		UserID:      userID,
		Name:        filename,
		FilePath:    filePath,
		Status:      string(domain.DocStatusPending),
		Description: opts.Description,
		Tags:        tagsJson,
		FileSize:    written,
		Type:        opts.Type, // Save Type
		IsEnabled:   true,      // 默认启用
	}
	if err := b.ds.KnowledgeDocuments().Create(ctx, doc); err != nil {
		return 0, err
	}

	// 3. Submit to pipeline
	// Map model.KnowledgeDocument to domain.KnowledgeDocument
	dDoc := &domain.KnowledgeDocument{
		ID:          doc.ID,
		UserID:      doc.UserID,
		Name:        doc.Name,
		FilePath:    doc.FilePath,
		Status:      domain.DocStatusPending,
		Description: doc.Description,
		Tags:        opts.Tags,
		Type:        domain.DocType(doc.Type), // Pass Type to pipeline
		FileSize:    doc.FileSize,
		IsEnabled:   doc.IsEnabled,
	}

	b.ingestionPipeline.Submit(dDoc)

	return doc.ID, nil
}

func (b *salesRAGBiz) UpdateDocument(ctx context.Context, userID uint, docID uint, req UpdateDocumentRequest) error {
	// 1. 获取文档并验证权限
	doc, err := b.ds.KnowledgeDocuments().GetByID(ctx, docID)
	if err != nil {
		return err
	}
	if doc.UserID != userID {
		return fmt.Errorf("permission denied")
	}

	// 2. 准备更新数据
	updates := make(map[string]interface{})
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.Tags != nil {
		bytes, _ := json.Marshal(req.Tags)
		updates["tags"] = string(bytes)
	}

	// 处理 IsEnabled 状态变更
	if req.IsEnabled != nil {
		updates["is_enabled"] = *req.IsEnabled

		// 如果是禁用/启用，是否需要同步到向量库？
		// 方案：向量库中存储 is_enabled 字段，或者检索时过滤。
		// 这里我们暂时只更新数据库。最佳实践是在 Search 时先查 DB 过滤。
		// 但为了保险，我们可以异步触发一次 UpdateVectorMeta (如果 DashVector 支持的话)
		// 目前 DashVector Update 比较麻烦，通常是 Overwrite。
		// 简单起见，检索层做过滤是最稳的。
	}

	if len(updates) == 0 {
		return nil
	}

	// 3. 更新数据库
	return b.ds.KnowledgeDocuments().UpdateColumns(ctx, docID, updates)
}

func (b *salesRAGBiz) Retrieve(ctx context.Context, query string, stage domain.SalesStage, docIDs []uint) (*service.RetrievalVerdict, error) {
	// 🔴 关键风险点：IsEnabled 过滤
	// 从数据库查询用户所有启用且已完成的文档ID，作为白名单进行二次过滤

	// 1. 从上下文获取用户ID
	var userID uint
	if uid, ok := ctx.Value("userID").(uint); ok {
		userID = uid
	} else {
		return nil, fmt.Errorf("user_id not found in context")
	}

	// 2. 查询用户所有文档
	docs, err := b.ds.KnowledgeDocuments().ListByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to query user documents: %w", err)
	}

	// 3. 构建启用且已完成的文档ID白名单
	enabledDocIDs := make(map[uint]bool)
	for _, doc := range docs {
		if doc.IsEnabled && doc.Status == string(domain.DocStatusCompleted) {
			enabledDocIDs[doc.ID] = true
		}
	}

	// 4. 过滤前端传来的docIDs，仅保留启用且已完成的
	var filteredDocIDs []uint
	if len(docIDs) == 0 {
		// 前端未指定文档，默认使用所有启用且已完成的
		for id := range enabledDocIDs {
			filteredDocIDs = append(filteredDocIDs, id)
		}
	} else {
		// 前端指定了文档，需要校验是否启用且已完成
		for _, id := range docIDs {
			if enabledDocIDs[id] {
				filteredDocIDs = append(filteredDocIDs, id)
			}
		}
	}

	// 5. 如果过滤后没有可用文档，返回友好提示
	if len(filteredDocIDs) == 0 {
		return &service.RetrievalVerdict{
			Query:      query,
			IsChitChat: true,
			Reason:     "没有可用的知识库文档（文档可能被禁用或未完成处理）",
		}, nil
	}

	// 6. 使用过滤后的文档ID进行检索
	return b.ragSvc.RetrieveForResponse(ctx, query, stage, filteredDocIDs)
}

func (b *salesRAGBiz) ListDocuments(ctx context.Context, userID uint) ([]domain.KnowledgeDocument, error) {
	docs, err := b.ds.KnowledgeDocuments().ListByUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	results := make([]domain.KnowledgeDocument, 0, len(docs))
	for _, d := range docs {
		// 解析 Tags（从JSON字符串）
		var tags []string
		if d.Tags != "" && d.Tags != "[]" {
			_ = json.Unmarshal([]byte(d.Tags), &tags)
		}

		results = append(results, domain.KnowledgeDocument{
			ID:          d.ID,
			UserID:      d.UserID,
			Name:        d.Name,
			FilePath:    d.FilePath,
			Status:      domain.DocStatus(d.Status),
			ErrorMsg:    d.ErrorMsg,
			Description: d.Description,          // ✅ 补充字段
			Tags:        tags,                   // ✅ 补充字段（解析JSON）
			ChunkCount:  d.ChunkCount,           // ✅ 补充字段
			FileSize:    d.FileSize,             // ✅ 补充字段
			FileType:    d.FileType,             // ✅ 补充字段
			Type:        domain.DocType(d.Type), // ✅ 补充字段
			IsEnabled:   d.IsEnabled,            // ✅ 补充字段
			CreatedAt:   d.CreatedAt,
			UpdatedAt:   d.UpdatedAt,
		})
	}
	return results, nil
}

func (b *salesRAGBiz) GetDocument(ctx context.Context, userID uint, docID uint) (*domain.KnowledgeDocument, error) {
	doc, err := b.ds.KnowledgeDocuments().GetByID(ctx, docID)
	if err != nil {
		return nil, err
	}
	if doc.UserID != userID {
		return nil, fmt.Errorf("permission denied")
	}

	// 解析 Tags（从JSON字符串）
	var tags []string
	if doc.Tags != "" && doc.Tags != "[]" {
		_ = json.Unmarshal([]byte(doc.Tags), &tags)
	}

	return &domain.KnowledgeDocument{
		ID:          doc.ID,
		UserID:      doc.UserID,
		Name:        doc.Name,
		FilePath:    doc.FilePath,
		Status:      domain.DocStatus(doc.Status),
		ErrorMsg:    doc.ErrorMsg,
		Description: doc.Description,
		Tags:        tags,
		ChunkCount:  doc.ChunkCount,
		FileSize:    doc.FileSize,
		FileType:    doc.FileType,
		Type:        domain.DocType(doc.Type),
		IsEnabled:   doc.IsEnabled,
		CreatedAt:   doc.CreatedAt,
		UpdatedAt:   doc.UpdatedAt,
	}, nil
}

func (b *salesRAGBiz) DeleteDocument(ctx context.Context, userID uint, docID uint) error {
	// 1. 验证所有权
	doc, err := b.ds.KnowledgeDocuments().GetByID(ctx, docID)
	if err != nil {
		return err
	}
	if doc.UserID != userID {
		return fmt.Errorf("permission denied")
	}

	// 2. 从向量库删除
	// 注意：如果向量库删除失败（例如旧数据不在向量库中，或者网络问题），我们记录错误但继续删除数据库记录
	// 这样可以避免用户无法删除"僵尸"文档的情况
	if err := b.ragSvc.DeleteByDocumentID(ctx, docID); err != nil {
		// Log warning but continue
		fmt.Printf("Warning: Failed to delete document %d from vector store: %v\n", docID, err)
	}

	// 3. 从数据库删除
	return b.ds.KnowledgeDocuments().Delete(ctx, docID)
}
