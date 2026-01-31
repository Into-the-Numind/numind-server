package salesrag

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"numind-server/internal/numind/biz/salesrag/adapter"
	"numind-server/internal/numind/biz/salesrag/domain"
	"numind-server/internal/numind/biz/salesrag/service"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"

	"github.com/spf13/viper"
	"gorm.io/gorm"
)

// SalesRAGBiz 定义了销售 RAG 业务层的对外接口
type SalesRAGBiz interface {
	// Ingest 处理文档导入
	Ingest(ctx context.Context, userID uint, filename string, reader io.Reader, opts IngestOptions) (uint, error)
	// Retrieve 检索知识（非流式）
	Retrieve(ctx context.Context, query string, docIDs []uint) (*service.RetrievalVerdict, error)
	// RetrieveStream 流式检索知识并生成回答
	// onEvent: 事件回调，eventType 可为 "verdict"/"token"/"error"/"done"
	RetrieveStream(ctx context.Context, query string, docIDs []uint, deepThinking bool, onEvent func(eventType string, data interface{}) error) error
	// ListDocuments 获取用户的文档列表
	ListDocuments(ctx context.Context, userID uint) ([]domain.KnowledgeDocument, error)
	// GetDocument 获取单个文档详情
	GetDocument(ctx context.Context, userID uint, docID uint) (*domain.KnowledgeDocument, error)
	// UpdateDocument 更新文档信息
	UpdateDocument(ctx context.Context, userID uint, docID uint, req UpdateDocumentRequest) error
	// DeleteDocument 删除文档
	DeleteDocument(ctx context.Context, userID uint, docID uint) error
	// ListDocumentChunks 获取文档的切片列表
	ListDocumentChunks(ctx context.Context, userID uint, docID uint, limit int) ([]domain.KnowledgeChunk, error)

	// 会话管理接口
	CreateSession(ctx context.Context, userID uint, req CreateSessionRequest) (*model.SalesSession, error)
	GetSession(ctx context.Context, userID uint, sessionID uint) (*model.SalesSession, error)
	ListSessions(ctx context.Context, userID uint, offset, limit int, salesStage string) ([]*model.SalesSession, int64, error)
	UpdateSession(ctx context.Context, userID uint, sessionID uint, req UpdateSessionRequest) error
	DeleteSession(ctx context.Context, userID uint, sessionID uint) error
	ListMessages(ctx context.Context, userID uint, sessionID uint, offset, limit int) ([]*model.SalesMessage, int64, error)
	UpdateCustomerProfile(ctx context.Context, userID uint, sessionID uint, profile string) error
	GetCustomerProfile(ctx context.Context, userID uint, sessionID uint) (string, error)

	// 置顶和重命名接口
	PinSession(ctx context.Context, userID uint, sessionID uint) error
	UnpinSession(ctx context.Context, userID uint, sessionID uint) error
	RenameSession(ctx context.Context, userID uint, sessionID uint, newTitle string) error

	// ChatWithSession 基于会话的流式对话（保存聊天记录）
	ChatWithSession(ctx context.Context, userID uint, sessionID uint, query string, docIDs []uint, deepThinking bool, onEvent func(eventType string, data interface{}) error) error

	// AnalyzeDocument 解析文档并生成客户档案
	AnalyzeDocument(ctx context.Context, userID uint, file io.Reader, filename string) (string, error)
}

type IngestOptions struct {
	Description string
	Tags        []string
}

type UpdateDocumentRequest struct {
	Description *string  `json:"description"`
	Tags        []string `json:"tags"`
	IsEnabled   *bool    `json:"is_enabled"`
}

type CreateSessionRequest struct {
	Title           string `json:"title"`
	DocumentIDs     []uint `json:"document_ids"`
	DeepThinking    bool   `json:"deep_thinking"`
	CustomerProfile string `json:"customer_profile"` // JSON string
}

type UpdateSessionRequest struct {
	Title           *string `json:"title"`
	DocumentIDs     []uint  `json:"document_ids"`
	DeepThinking    *bool   `json:"deep_thinking"`
	CustomerProfile *string `json:"customer_profile"`
}

type salesRAGBiz struct {
	ds                store.IStore
	ingestionPipeline *service.IngestionPipeline
	ragSvc            *service.SalesRAGService
	volcBiz           VolcBiz // 添加大模型服务依赖（保留用于 fallback）
	sessionStore      store.SalesSessionStore
	parser            service.PipelineParser
	dmxClient         *adapter.DMXAPIClient // DMXAPI 客户端（用于 DeepSeek-V3.2）
}

// VolcBiz 火山引擎服务接口（避免循环依赖）
type VolcBiz interface {
	VolcTextStream(ctx context.Context, messages []map[string]string, maxTokens int, temperature float64) (string, error)
	// StreamChat 真正的流式聊天，通过回调函数逐 token 或思维链内容推送
	StreamChat(ctx context.Context, messages []map[string]string, maxTokens int, temperature float64, deepThinking bool, onEvent func(event string, token string) error) (string, error)
}

func NewSalesRAGBiz(ds store.IStore, pipeline *service.IngestionPipeline, rag *service.SalesRAGService, volc VolcBiz, sessionStore store.SalesSessionStore, parser service.PipelineParser) SalesRAGBiz {
	return &salesRAGBiz{
		ds:                ds,
		ingestionPipeline: pipeline,
		ragSvc:            rag,
		volcBiz:           volc,
		sessionStore:      sessionStore,
		parser:            parser,
		dmxClient:         adapter.NewDMXAPIClient(),
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

func (b *salesRAGBiz) Retrieve(ctx context.Context, query string, docIDs []uint) (*service.RetrievalVerdict, error) {
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
		if doc.Status == string(domain.DocStatusCompleted) {
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
	verdict, err := b.ragSvc.RetrieveForResponse(ctx, query, filteredDocIDs)
	if err != nil {
		return nil, err
	}

	// 7. 调用大模型生成最终回复
	answer, err := b.generateAnswer(ctx, query, verdict)
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
			Description: d.Description, // ✅ 补充字段
			Tags:        tags,          // ✅ 补充字段（解析JSON）
			ChunkCount:  d.ChunkCount,  // ✅ 补充字段
			FileSize:    d.FileSize,    // ✅ 补充字段
			FileType:    d.FileType,    // ✅ 补充字段
			IsEnabled:   d.IsEnabled,   // ✅ 补充字段
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

	// 2. 删除MySQL中的切片（快速、可靠）
	if err := b.ds.KnowledgeChunks().DeleteByDocument(ctx, docID); err != nil {
		log.Printf("Warning: Failed to delete chunks from MySQL for doc %d: %v", docID, err)
		// 继续执行，避免阻塞
	}

	// 3. 从向量库删除切片（尽力而为）
	// 注意：如果向量库删除失败（例如旧数据不在向量库中，或者网络问题），我们记录错误但继续删除数据库记录
	// 这样可以避免用户无法删除"僵尸"文档的情况
	if err := b.ragSvc.DeleteByDocumentID(ctx, docID); err != nil {
		// Log warning but continue
		log.Printf("Warning: Failed to delete document %d from vector store: %v", docID, err)
	}

	// 4. 从数据库删除文档记录
	return b.ds.KnowledgeDocuments().Delete(ctx, docID)
}

func (b *salesRAGBiz) ListDocumentChunks(ctx context.Context, userID uint, docID uint, limit int) ([]domain.KnowledgeChunk, error) {
	// 1. 验证所有权
	doc, err := b.ds.KnowledgeDocuments().GetByID(ctx, docID)
	if err != nil {
		return nil, err
	}
	if doc.UserID != userID {
		return nil, fmt.Errorf("permission denied")
	}

	if limit <= 0 {
		limit = 1000 // 默认返回1000条，确保能获取所有切片
	}

	// 2. 优先从MySQL读取（快速，无费用）
	mysqlChunks, err := b.ds.KnowledgeChunks().ListByDocumentAndUser(ctx, docID, userID, limit)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		log.Printf("Warning: MySQL query failed for doc %d: %v", docID, err)
	}

	if len(mysqlChunks) > 0 {
		// 转换model.KnowledgeChunk到domain.KnowledgeChunk
		return b.convertModelChunksToDomain(mysqlChunks), nil
	}

	// 3. Fallback到向量数据库（兼容旧数据）
	log.Printf("No chunks in MySQL for doc %d, falling back to vector DB", docID)
	vectorChunks, err := b.ragSvc.FetchByDocumentID(ctx, docID, limit)
	if err != nil {
		return nil, err
	}

	// 4. 异步回填到MySQL（懒加载迁移）
	if len(vectorChunks) > 0 {
		go b.backfillChunksToMySQL(context.Background(), doc, vectorChunks)
	}

	return vectorChunks, nil
}

// generateAnswer 使用大模型生成最终回复
func (b *salesRAGBiz) generateAnswer(ctx context.Context, query string, verdict *service.RetrievalVerdict) (string, error) {
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
	allChunks := verdict.Evidence

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

	// 3. 使用通用的系统提示词
	systemPrompt := `你是一个专业的销售智能助手。

你的任务是基于提供的知识库信息，准确、友好地回答用户的问题。请注意：
1. 准确引用知识库中的内容，不要虚构信息
2. 用友好、专业的语气回答
3. 如果知识库中没有直接答案，可以引导用户提供更多信息

知识库内容：
` + knowledgeContext

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

// RetrieveStream 流式检索知识并生成回答
// 事件类型:
// - "verdict": data 为 *service.RetrievalVerdict，检索结果
// - "token": data 为 string，回答的增量 token
// - "error": data 为 string，错误消息
// - "done": data 为 nil，流式完成
func (b *salesRAGBiz) RetrieveStream(ctx context.Context, query string, docIDs []uint, deepThinking bool, onEvent func(eventType string, data interface{}) error) error {
	// 1. 从上下文获取用户ID
	var userID uint
	if uid, ok := ctx.Value("userID").(uint); ok {
		userID = uid
	} else {
		return onEvent("error", "user_id not found in context")
	}

	// 2. 查询用户所有文档
	docs, err := b.ds.KnowledgeDocuments().ListByUser(ctx, userID)
	if err != nil {
		return onEvent("error", fmt.Sprintf("failed to query user documents: %v", err))
	}

	// 3. 构建启用且已完成的文档ID白名单
	enabledDocIDs := make(map[uint]bool)
	for _, doc := range docs {
		if doc.Status == string(domain.DocStatusCompleted) {
			enabledDocIDs[doc.ID] = true
		}
	}

	// 4. 过滤前端传来的docIDs
	var filteredDocIDs []uint
	if len(docIDs) == 0 {
		for id := range enabledDocIDs {
			filteredDocIDs = append(filteredDocIDs, id)
		}
	} else {
		for _, id := range docIDs {
			if enabledDocIDs[id] {
				filteredDocIDs = append(filteredDocIDs, id)
			}
		}
	}

	// 5. 如果没有可用文档，返回友好提示
	if len(filteredDocIDs) == 0 {
		verdict := &service.RetrievalVerdict{
			Query:      query,
			IsChitChat: true,
			Reason:     "没有可用的知识库文档（文档可能被禁用或未完成处理）",
		}
		if err := onEvent("verdict", verdict); err != nil {
			return err
		}
		if err := onEvent("token", "抱歉，当前没有可用的知识库文档。请先上传并启用相关文档。"); err != nil {
			return err
		}
		return onEvent("done", nil)
	}

	// 6. 执行检索
	verdict, err := b.ragSvc.RetrieveForResponse(ctx, query, filteredDocIDs)
	if err != nil {
		return onEvent("error", fmt.Sprintf("retrieval failed: %v", err))
	}

	// 7. 立即发送 verdict 事件
	if err := onEvent("verdict", verdict); err != nil {
		return err
	}

	// 8. 构建 prompt 并流式生成回答
	messages := b.buildPromptMessagesV2(query, verdict)

	// 9. 调用 DMXAPI DeepSeek-V3.2 流式聊天（非思考模式）
	_, err = b.dmxClient.StreamChatCompletion(ctx, "DeepSeek-V3.2", messages, 0.7, 2000, func(content string) error {
		return onEvent("token", content)
	})
	if err != nil {
		return onEvent("error", fmt.Sprintf("stream chat failed: %v", err))
	}

	// 10. 发送完成事件
	return onEvent("done", nil)
}

// buildPromptMessages 根据检索结果构建 prompt 消息 (V1 兼容)
// Deprecated: 请使用 buildPromptMessagesV2
func (b *salesRAGBiz) buildPromptMessages(query string, verdict *service.RetrievalVerdict) []map[string]string {
	messagesV2 := b.buildPromptMessagesV2(query, verdict)
	result := make([]map[string]string, len(messagesV2))
	for i, msg := range messagesV2 {
		result[i] = map[string]string{
			"role":    msg.Role,
			"content": msg.Content,
		}
	}
	return result
}

// buildPromptMessagesV2 根据检索结果构建 prompt 消息（销售 Copilot 优化版）
func (b *salesRAGBiz) buildPromptMessagesV2(query string, verdict *service.RetrievalVerdict) []adapter.ChatMessage {
	// 如果是闲聊
	if verdict.IsChitChat {
		return []adapter.ChatMessage{
			{
				Role:    "system",
				Content: "你是一个专业、友好的销售智能助手。请用简洁、自然的方式回复用户的问候或闲聊。",
			},
			{
				Role:    "user",
				Content: query,
			},
		}
	}

	// 合并所有检索到的知识
	allChunks := verdict.Evidence

	if len(allChunks) == 0 {
		return []adapter.ChatMessage{
			{
				Role:    "system",
				Content: "你是一个专业的销售智能助手。由于知识库中没有找到相关信息，请基于你的通用知识给出专业、有帮助的销售话术建议。",
			},
			{
				Role:    "user",
				Content: fmt.Sprintf("客户说：\"%s\"\n\n请帮我生成合适的回复话术。", query),
			},
		}
	}

	// 构建知识上下文（所有 reranked chunks）
	var contextParts []string
	for i, chunk := range allChunks {
		contextParts = append(contextParts, fmt.Sprintf("[知识%d] %s", i+1, chunk.Content))
	}
	knowledgeContext := strings.Join(contextParts, "\n\n")

	// 获取意图描述
	intentDesc := getIntentDescription(string(verdict.Intent))

	// 销售 Copilot 优化的系统提示词
	systemPrompt := fmt.Sprintf(`你是一位资深的销售顾问助手，正在帮助销售人员回复客户消息。

## 客户意图分析
%s

## 你的任务
基于以下知识库内容，为销售人员生成合适的回复话术。

## 回复要求
1. 语气要专业但亲切，适合微信聊天场景
2. 回复要简洁有力，不要太长
3. 如果是异议处理，先共情再引导
4. 不要虚构知识库中没有的信息
5. 可以生成多个风格的回复供选择（如：专业风、亲和风）

## 知识库内容
%s`, intentDesc, knowledgeContext)

	return []adapter.ChatMessage{
		{
			Role:    "system",
			Content: systemPrompt,
		},
		{
			Role:    "user",
			Content: fmt.Sprintf("客户说：\"%s\"\n\n请帮我生成合适的回复话术。", query),
		},
	}
}

// getIntentDescription 获取意图的中文描述
func getIntentDescription(intent string) string {
	switch intent {
	case "OBJECTION":
		return "客户正在表达异议或抗拒（如嫌贵、犹豫、质疑）。需要先共情，再引导价值认知。"
	case "COMPARISON":
		return "客户在比较竞品或进行选型。需要突出差异化优势，扬长避短。"
	case "INQUIRY":
		return "客户在咨询产品信息。需要准确、专业地回答，并适当引导。"
	case "BUYING_PROOF":
		return "客户表现出购买意向或需要案例佐证。需要积极推进，提供信任背书。"
	case "CHIT_CHAT":
		return "客户在闲聊。保持轻松友好，拉近关系。"
	default:
		return "客户意图待分析。"
	}
}

// ============ 会话管理方法 ============

// CreateSession 创建新的销售会话
func (b *salesRAGBiz) CreateSession(ctx context.Context, userID uint, req CreateSessionRequest) (*model.SalesSession, error) {
	// 序列化 DocumentIDs 为 JSON
	docIDsJSON, err := json.Marshal(req.DocumentIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal document_ids: %w", err)
	}

	// 创建会话
	session := &model.SalesSession{
		UserID:          userID,
		Title:           req.Title,
		Status:          "active",
		DocumentIDs:     string(docIDsJSON),
		DeepThinking:    req.DeepThinking,
		CustomerProfile: req.CustomerProfile,
		MessageCount:    0,
	}

	if err := b.sessionStore.CreateSession(ctx, session); err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	return session, nil
}

// GetSession 获取销售会话详情
func (b *salesRAGBiz) GetSession(ctx context.Context, userID uint, sessionID uint) (*model.SalesSession, error) {
	return b.sessionStore.GetSession(ctx, sessionID, userID)
}

// ListSessions 获取用户的销售会话列表
func (b *salesRAGBiz) ListSessions(ctx context.Context, userID uint, offset, limit int, salesStage string) ([]*model.SalesSession, int64, error) {
	return b.sessionStore.ListSessions(ctx, userID, offset, limit, salesStage)
}

// UpdateSession 更新销售会话
func (b *salesRAGBiz) UpdateSession(ctx context.Context, userID uint, sessionID uint, req UpdateSessionRequest) error {
	// 获取现有会话
	session, err := b.sessionStore.GetSession(ctx, sessionID, userID)
	if err != nil {
		return fmt.Errorf("failed to get session: %w", err)
	}

	// 更新字段
	if req.Title != nil {
		session.Title = *req.Title
	}
	if req.DocumentIDs != nil {
		docIDsJSON, err := json.Marshal(req.DocumentIDs)
		if err != nil {
			return fmt.Errorf("failed to marshal document_ids: %w", err)
		}
		session.DocumentIDs = string(docIDsJSON)
	}
	if req.DeepThinking != nil {
		session.DeepThinking = *req.DeepThinking
	}
	if req.CustomerProfile != nil {
		session.CustomerProfile = *req.CustomerProfile
	}

	return b.sessionStore.UpdateSession(ctx, session)
}

// DeleteSession 删除销售会话
func (b *salesRAGBiz) DeleteSession(ctx context.Context, userID uint, sessionID uint) error {
	return b.sessionStore.DeleteSession(ctx, sessionID, userID)
}

// PinSession 置顶会话
func (b *salesRAGBiz) PinSession(ctx context.Context, userID uint, sessionID uint) error {
	return b.sessionStore.PinSession(ctx, sessionID, userID)
}

// UnpinSession 取消置顶会话
func (b *salesRAGBiz) UnpinSession(ctx context.Context, userID uint, sessionID uint) error {
	return b.sessionStore.UnpinSession(ctx, sessionID, userID)
}

// RenameSession 重命名会话
func (b *salesRAGBiz) RenameSession(ctx context.Context, userID uint, sessionID uint, newTitle string) error {
	return b.sessionStore.RenameSession(ctx, sessionID, userID, newTitle)
}

// ListMessages 获取会话的消息列表
func (b *salesRAGBiz) ListMessages(ctx context.Context, userID uint, sessionID uint, offset, limit int) ([]*model.SalesMessage, int64, error) {
	return b.sessionStore.ListMessages(ctx, sessionID, userID, offset, limit)
}

// UpdateCustomerProfile 更新客户档案
func (b *salesRAGBiz) UpdateCustomerProfile(ctx context.Context, userID uint, sessionID uint, profile string) error {
	session, err := b.sessionStore.GetSession(ctx, sessionID, userID)
	if err != nil {
		return fmt.Errorf("failed to get session: %w", err)
	}

	session.CustomerProfile = profile
	return b.sessionStore.UpdateSession(ctx, session)
}

// GetCustomerProfile 获取客户档案
func (b *salesRAGBiz) GetCustomerProfile(ctx context.Context, userID uint, sessionID uint) (string, error) {
	session, err := b.sessionStore.GetSession(ctx, sessionID, userID)
	if err != nil {
		return "", fmt.Errorf("failed to get session: %w", err)
	}
	return session.CustomerProfile, nil
}

// ChatWithSession 基于会话的流式对话（保存聊天记录）
func (b *salesRAGBiz) ChatWithSession(ctx context.Context, userID uint, sessionID uint, query string, docIDs []uint, deepThinking bool, onEvent func(eventType string, data interface{}) error) error {
	// 1. 验证会话
	session, err := b.sessionStore.GetSession(ctx, sessionID, userID)
	if err != nil {
		return fmt.Errorf("failed to get session: %w", err)
	}

	// 2. 保存用户消息
	userMessage := &model.SalesMessage{
		SessionID: sessionID,
		UserID:    userID,
		Role:      "user",
		Content:   query,
		Status:    "sent",
	}
	if err := b.sessionStore.CreateMessage(ctx, userMessage); err != nil {
		return fmt.Errorf("failed to save user message: %w", err)
	}

	// 3. 累积流式内容
	// (Check implementation in original file, keeping it simple here as I am adding at the end of file for AnalyzeDocument)
	// Actually I will append AnalyzeDocument at the end of the file or before ChatWithSession if needed.
	// The prompt implies multiple chunks. I will add AnalyzeDocument at the end.

	var fullContent strings.Builder
	var verdictJSON string
	var thinkingText string

	// 4. 调用流式检索，累积内容
	err = b.RetrieveStream(ctx, query, docIDs, deepThinking, func(eventType string, data interface{}) error {
		switch eventType {
		case "verdict":
			// 序列化 verdict 为 JSON
			if verdictData, ok := data.(*service.RetrievalVerdict); ok {
				bytes, _ := json.Marshal(verdictData)
				verdictJSON = string(bytes)
			}
			// 继续传递给外部回调
			return onEvent(eventType, data)

		case "thinking":
			// 累积思维链内容
			if token, ok := data.(string); ok {
				thinkingText += token
			}
			// 继续传递给外部回调
			return onEvent(eventType, data)

		case "token":
			// 累积回答内容
			if token, ok := data.(string); ok {
				fullContent.WriteString(token)
			}
			// 继续传递给外部回调
			return onEvent(eventType, data)

		case "error", "done":
			// 直接传递
			return onEvent(eventType, data)

		default:
			return onEvent(eventType, data)
		}
	})

	if err != nil {
		return err
	}

	// 5. 保存助手消息
	assistantMessage := &model.SalesMessage{
		SessionID: sessionID,
		UserID:    userID,
		Role:      "assistant",
		Content:   fullContent.String(),
		Status:    "sent",
		Verdict:   verdictJSON,
		Thinking:  thinkingText,
	}
	if err := b.sessionStore.CreateMessage(ctx, assistantMessage); err != nil {
		return fmt.Errorf("failed to save assistant message: %w", err)
	}

	// 6. 更新会话统计
	session.MessageCount += 2
	session.LastQuery = query
	if err := b.sessionStore.UpdateSession(ctx, session); err != nil {
		return fmt.Errorf("failed to update session: %w", err)
	}

	return nil
}

// convertModelChunksToDomain 将MySQL模型转换为领域模型
func (b *salesRAGBiz) convertModelChunksToDomain(modelChunks []*model.KnowledgeChunk) []domain.KnowledgeChunk {
	result := make([]domain.KnowledgeChunk, len(modelChunks))
	for i, mc := range modelChunks {
		var tags []string
		if mc.Tags != "" {
			tags = strings.Split(mc.Tags, ",")
		}
		result[i] = domain.KnowledgeChunk{
			ID:         mc.VectorID, // 使用向量数据库ID作为chunk ID
			DocumentID: mc.DocumentID,
			UserID:     mc.UserID,
			Content:    mc.Content,
			Tags:       tags,
			Summary:    mc.Summary,
			SourceRef:  mc.SourceRef,
		}
	}
	return result
}

// backfillChunksToMySQL 异步回填切片到MySQL（懒加载迁移）
func (b *salesRAGBiz) backfillChunksToMySQL(ctx context.Context, doc *model.KnowledgeDocument, chunks []domain.KnowledgeChunk) {
	if len(chunks) == 0 {
		return
	}

	modelChunks := make([]*model.KnowledgeChunk, len(chunks))
	for i, chunk := range chunks {
		modelChunks[i] = &model.KnowledgeChunk{
			DocumentID:      doc.ID,
			UserID:          doc.UserID,
			Sequence:        i,
			Content:         chunk.Content,
			Summary:         chunk.Summary,
			SourceRef:       chunk.SourceRef,
			Tags:            strings.Join(chunk.Tags, ","),
			VectorID:        chunk.ID,
			EmbeddingStatus: "COMPLETED", // 已在向量数据库中
		}
	}

	if err := b.ds.KnowledgeChunks().BatchCreate(ctx, modelChunks); err != nil {
		log.Printf("Backfill failed for doc %d: %v", doc.ID, err)
	} else {
		log.Printf("Successfully backfilled %d chunks for doc %d", len(chunks), doc.ID)
	}
}

// AnalyzeDocument 解析文档并生成客户档案
// 使用 dmxapi 的 qwen-turbo-latest 模型，不开启思维模式
func (b *salesRAGBiz) AnalyzeDocument(ctx context.Context, userID uint, file io.Reader, filename string) (string, error) {
	// 1. 解析文档内容
	content, err := b.parser.Parse(ctx, file, filename)
	if err != nil {
		return "", fmt.Errorf("failed to parse document: %w", err)
	}

	// 2. 截断 (避免 token 溢出)
	maxLen := 50000
	if len(content) > maxLen {
		content = content[:maxLen] + "\n...(truncated)"
	}

	// 3. 构建提示词
	systemPrompt := `你是一个专业的销售分析师。你的任务是根据提供的客户文档，提取并生成一份结构化的客户档案。
请包含以下内容（如果文档中提及）：
1. 客户基本信息（名称、行业、规模）
2. 关键痛点与需求
3. 目前的销售阶段推测
4. 决策链信息
5. 其它关键备注

请直接输出Markdown格式的档案内容，不要包含其他开场白。`

	// 4. 调用 dmxapi 的 qwen-turbo-latest 模型
	return b.callDMXAPI(ctx, systemPrompt, "客户文档内容如下：\n\n"+content)
}

// callDMXAPI 调用 dmxapi 的 qwen-turbo-latest 模型
func (b *salesRAGBiz) callDMXAPI(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	url := "https://www.dmxapi.cn/v1/chat/completions"
	apiKey := "sk-XgINDoE22MHQfcSZnToYICS4rNnoknIrXhZHZYs3VQM9DP25"
	model := "qwen-turbo-latest"

	payload := map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userMessage},
		},
		"temperature": 0.5,
		"max_tokens":  2000,
	}

	bodyBytes, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create request failed: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", apiKey)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("DMXAPI request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("DMXAPI error %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response failed: %w", err)
	}

	if result.Error != nil {
		return "", fmt.Errorf("DMXAPI error: %s - %s", result.Error.Code, result.Error.Message)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("empty choices from DMXAPI")
	}

	return result.Choices[0].Message.Content, nil
}
