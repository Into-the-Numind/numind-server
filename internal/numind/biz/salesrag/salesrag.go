package salesrag

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"numind-server/internal/numind/biz/salesrag/domain"
	"numind-server/internal/numind/biz/salesrag/service"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"

	"github.com/spf13/viper"
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
	volcBiz           VolcBiz // 添加大模型服务依赖
}

// VolcBiz 火山引擎服务接口（避免循环依赖）
type VolcBiz interface {
	VolcTextStream(ctx context.Context, messages []map[string]string, maxTokens int, temperature float64) (string, error)
}

func NewSalesRAGBiz(ds store.IStore, pipeline *service.IngestionPipeline, rag *service.SalesRAGService, volc VolcBiz) SalesRAGBiz {
	return &salesRAGBiz{
		ds:                ds,
		ingestionPipeline: pipeline,
		ragSvc:            rag,
		volcBiz:           volc,
	}
}

func (b *salesRAGBiz) Ingest(ctx context.Context, userID uint, filename string, reader io.Reader, opts IngestOptions) (uint, error) {
	// 1. Upload to Cloud Object Storage (COS)
	// Read file content
	data, err := io.ReadAll(reader)
	if err != nil {
		return 0, fmt.Errorf("failed to read file content: %w", err)
	}

	// Generate object key: <env>/sales_rag/<user_id>/<timestamp>_<filename>
	// 使用 runmode 区分环境 (debug/release/test)，防止本地开发污染 dev 环境数据
	env := viper.GetString("runmode")
	if env == "" {
		env = "unknown"
	}
	objectKey := fmt.Sprintf("%s/sales_rag/%d/%d_%s", env, userID, time.Now().Unix(), filename)

	// Determine content type (simple guess or default)
	contentType := "application/octet-stream"
	if filepath.Ext(filename) == ".pdf" {
		contentType = "application/pdf"
	} else if filepath.Ext(filename) == ".md" {
		contentType = "text/markdown"
	} else if filepath.Ext(filename) == ".txt" {
		contentType = "text/plain"
	}

	// Upload to COS using util package
	// Note: We need to import "numind-server/internal/pkg/util"
	cosURL, err := util.UploadBytesToCOS(ctx, objectKey, contentType, data)
	if err != nil {
		return 0, fmt.Errorf("failed to upload to COS: %w", err)
	}
	if cosURL == "" {
		return 0, fmt.Errorf("COS upload returned empty URL")
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
		FilePath:    cosURL, // Store COS URL instead of local path
		Status:      string(domain.DocStatusPending),
		Description: opts.Description,
		Tags:        tagsJson,
		FileSize:    int64(len(data)),
		Type:        opts.Type,
		IsEnabled:   true,
	}
	if err := b.ds.KnowledgeDocuments().Create(ctx, doc); err != nil {
		return 0, err
	}

	// 3. Submit to pipeline
	dDoc := &domain.KnowledgeDocument{
		ID:          doc.ID,
		UserID:      doc.UserID,
		Name:        doc.Name,
		FilePath:    doc.FilePath, // This is now a URL
		Status:      domain.DocStatusPending,
		Description: doc.Description,
		Tags:        opts.Tags,
		Type:        domain.DocType(doc.Type),
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
	verdict, err := b.ragSvc.RetrieveForResponse(ctx, query, stage, filteredDocIDs)
	if err != nil {
		return nil, err
	}

	// 7. 调用大模型生成最终回复
	answer, err := b.generateAnswer(ctx, query, stage, verdict)
	if err != nil {
		// 生成失败时，返回友好提示
		if verdict.IsChitChat {
			verdict.Answer = "您好，我是销售智能助手。请问有什么可以帮您的吗？"
		} else {
			verdict.Answer = "抱歉，我遇到了一些问题，请稍后再试。"
		}
	} else {
		verdict.Answer = answer
	}

	return verdict, nil
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

// generateAnswer 使用大模型生成最终回复
func (b *salesRAGBiz) generateAnswer(ctx context.Context, query string, stage domain.SalesStage, verdict *service.RetrievalVerdict) (string, error) {
	// 1. 如果是闲聊，生成友好的闲聊回复
	if verdict.IsChitChat {
		messages := []map[string]string{
			{
				"role":    "system",
				"content": "你是一个专业、友好的销售智能助手。请用简洁、自然的方式回复用户的问候或闲聊。",
			},
			{
				"role":    "user",
				"content": query,
			},
		}
		return b.volcBiz.VolcTextStream(ctx, messages, 200, 0.7)
	}

	// 2. 构建知识上下文
	var contextParts []string

	// 合并所有检索到的知识
	allChunks := append(verdict.Facts, verdict.Strategies...)
	allChunks = append(allChunks, verdict.Cases...)

	if len(allChunks) == 0 {
		// 没有检索到相关知识，让大模型基于自身知识回答
		messages := []map[string]string{
			{
				"role":    "system",
				"content": "你是一个专业的销售智能助手。由于知识库中没有找到相关信息，请基于你的通用知识给出专业、有帮助的回答。",
			},
			{
				"role":    "user",
				"content": query,
			},
		}
		return b.volcBiz.VolcTextStream(ctx, messages, 1000, 0.7)
	}

	// 构建知识上下文
	for i, chunk := range allChunks {
		contextParts = append(contextParts, fmt.Sprintf("[知识%d] %s", i+1, chunk.Content))
		if i >= 4 { // 最多使用5条知识
			break
		}
	}
	knowledgeContext := strings.Join(contextParts, "\n\n")

	// 3. 根据销售阶段定制系统提示词
	var systemPrompt string
	switch stage {
	case domain.StageDiscovery:
		systemPrompt = `你是一个专业的销售智能助手，当前处于【需求发现】阶段。

你的任务是基于提供的知识库信息，回答用户的问题。请注意：
1. 准确引用知识库中的内容，不要虚构信息
2. 用友好、专业的语气回答
3. 帮助用户了解产品的核心功能和价值
4. 如果知识库中没有直接答案，可以引导用户提供更多信息

知识库内容：
` + knowledgeContext

	case domain.StageNegotiation:
		systemPrompt = `你是一个专业的销售智能助手，当前处于【方案协商】阶段。

你的任务是基于提供的知识库信息，回答用户的问题。请注意：
1. 准确引用知识库中的定价、方案等信息
2. 强调产品的价值和ROI
3. 用清晰、有说服力的语气回答
4. 帮助用户理解不同方案的优势

知识库内容：
` + knowledgeContext

	case domain.StageClosing:
		systemPrompt = `你是一个专业的销售智能助手，当前处于【成交跟进】阶段。

你的任务是基于提供的知识库信息，回答用户的问题。请注意：
1. 准确引用知识库中的交付、支持等信息
2. 强调服务保障和售后支持
3. 用专业、可靠的语气回答
4. 帮助用户消除最后的顾虑

知识库内容：
` + knowledgeContext

	default:
		systemPrompt = `你是一个专业的销售智能助手。

你的任务是基于提供的知识库信息，准确、友好地回答用户的问题。

知识库内容：
` + knowledgeContext
	}

	// 4. 构建消息并调用大模型
	messages := []map[string]string{
		{
			"role":    "system",
			"content": systemPrompt,
		},
		{
			"role":    "user",
			"content": query,
		},
	}

	return b.volcBiz.VolcTextStream(ctx, messages, 1000, 0.7)
}
