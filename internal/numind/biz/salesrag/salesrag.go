package salesrag

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
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

	"github.com/disintegration/imaging"
	"gorm.io/gorm"
)

// SalesRAGBiz 定义了销售 RAG 业务层的对外接口
type SalesRAGBiz interface {
	// Ingest 处理文档导入
	Ingest(ctx context.Context, userID uint, filename string, displayName string, reader io.Reader, opts IngestOptions) (uint, error)
	// Retrieve 检索知识（非流式）
	Retrieve(ctx context.Context, query string, docIDs []uint) (*service.RetrievalVerdict, error)
	// RetrieveStream 流式检索知识并生成回答
	// chatMode: "sales" (销售话术) 或 "free" (自由讨论)
	// onEvent: 事件回调，eventType 可为 "verdict"/"token"/"error"/"done"
	RetrieveStream(ctx context.Context, query string, history []string, docIDs []uint, deepThinking bool, chatMode string, customerProfile string, onEvent func(eventType string, data interface{}) error) error
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
	// chatMode: "sales" (销售话术模式) 或 "free" (自由讨论模式)
	ChatWithSession(ctx context.Context, userID uint, sessionID uint, query string, images []string, docIDs []uint, deepThinking bool, chatMode string, onEvent func(eventType string, data interface{}) error) error

	// AnalyzeDocument 解析文档并生成客户档案
	AnalyzeDocument(ctx context.Context, userID uint, file io.Reader, filename string) (string, error)

	// AnalyzeChatStyle 分析聊天风格（语言指纹分析）
	AnalyzeChatStyle(ctx context.Context, userID uint, file io.Reader, filename string) (string, error)

	// GetLanguageStyle 获取用户的语言风格
	GetLanguageStyle(ctx context.Context, userID uint) (string, error)
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
	CustomerProfile string `json:"customer_profile"` // Markdown 格式
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
	// VisionAnalyze 调用火山方舟视觉模型分析图片
	VisionAnalyze(ctx context.Context, imageURL string, prompt string, model string) (string, error)
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

func (b *salesRAGBiz) Ingest(ctx context.Context, userID uint, filename string, displayName string, reader io.Reader, opts IngestOptions) (uint, error) {
	// 0. 验证文件名
	if filename == "" {
		return 0, fmt.Errorf("filename cannot be empty")
	}

	// 验证是否包含文件扩展名
	ext := filepath.Ext(filename)
	if ext == "" {
		return 0, fmt.Errorf("filename must have an extension: %s", filename)
	}

	// 如果 displayName 为空，则使用 filename
	if displayName == "" {
		displayName = filename
	}

	log.Printf("Starting document ingestion: filename=%s, displayName=%s, user_id=%d", filename, displayName, userID)

	// 1. Upload to Cloud Object Storage (COS)
	// Read file content
	data, err := io.ReadAll(reader)
	if err != nil {
		return 0, fmt.Errorf("failed to read file content: %w", err)
	}

	// Generate object key: sales_rag/<user_id>/<timestamp>_<filename>
	objectKey := fmt.Sprintf("sales_rag/%d/%d_%s", userID, time.Now().Unix(), filename)

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
		Name:        displayName,
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
		Name:        filename,     // Use original filename for pipeline processing (extension detection)
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
		if doc.IsEnabled && doc.Status == string(domain.DocStatusCompleted) {
			enabledDocIDs[doc.ID] = true
		}
	}

	// 4. 过滤前端传来的docIDs，仅保留启用且已完成的
	var filteredDocIDs []uint
	if len(docIDs) > 0 {
		// 前端指定了文档，需要校验是否启用且已完成
		for _, id := range docIDs {
			if enabledDocIDs[id] {
				filteredDocIDs = append(filteredDocIDs, id)
			}
		}
	}

	// 5. 执行检索（即使 filteredDocIDs 为空也会执行，返回空证据）
	verdict, err := b.ragSvc.RetrieveForResponse(ctx, query, filteredDocIDs, userID)
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
		limit = 10000 // 默认返回10000条，确保能获取所有切片
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
	// 1. 构建知识上下文
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
// RetrieveStream 流式检索知识并生成回答
// chatMode: "sales" (销售话术) 或 "free" (自由讨论)
func (b *salesRAGBiz) RetrieveStream(ctx context.Context, query string, history []string, docIDs []uint, deepThinking bool, chatMode string, customerProfile string, onEvent func(eventType string, data interface{}) error) error {
	// 1. 从上下文获取用户ID
	var userID uint
	if uid, ok := ctx.Value("userID").(uint); ok {
		userID = uid
	} else {
		return onEvent("error", "user_id not found in context")
	}

	// 发送初始状态：正在分析...
	if err := onEvent("status", "正在分析您的问题..."); err != nil {
		return err
	}

	// 2. 查询用户所有文档
	docs, err := b.ds.KnowledgeDocuments().ListByUser(ctx, userID)
	if err != nil {
		return onEvent("error", fmt.Sprintf("failed to query user documents: %v", err))
	}

	// 3. 构建启用且已完成的文档ID白名单
	enabledDocIDs := make(map[uint]bool)
	for _, doc := range docs {
		if doc.IsEnabled && doc.Status == string(domain.DocStatusCompleted) {
			enabledDocIDs[doc.ID] = true
		}
	}

	// 4. 过滤前端传来的docIDs
	var filteredDocIDs []uint
	if len(docIDs) > 0 {
		for _, id := range docIDs {
			if enabledDocIDs[id] {
				filteredDocIDs = append(filteredDocIDs, id)
			}
		}
	}

	// 发送状态：正在检索知识库与匹配策略
	if err := onEvent("status", "正在检索知识库与匹配策略..."); err != nil {
		return err
	}

	// 6. 执行检索（使用 V2 版本，传递 chatMode 和 history）
	// 注意：RetrieveForResponseV2 内部并行执行 RAG 检索和策略选择
	verdict, err := b.ragSvc.RetrieveForResponseV2(ctx, query, filteredDocIDs, history, chatMode, userID)
	if err != nil {
		return onEvent("error", fmt.Sprintf("retrieval failed: %v", err))
	}

	// 7. 填充 evidence 中的 document_name（从数据库查询）
	b.enrichChunksWithDocNames(ctx, verdict.Evidence)

	// 8. 立即发送 verdict 事件
	if err := onEvent("verdict", verdict); err != nil {
		return err
	}

	// 发送状态：正在生成回复...
	if err := onEvent("status", "正在生成回复..."); err != nil {
		return err
	}

	// 9. 获取语言风格
	languageStyle, _ := b.GetLanguageStyle(ctx, userID)

	// 10. 构建 prompt 并流式生成回答
	messages := b.buildPromptMessagesV2(query, verdict, customerProfile, languageStyle)

	// 11. 调用 DMXAPI DeepSeek-V3.2 流式聊天（非思考模式）
	_, err = b.dmxClient.StreamChatCompletion(ctx, "DeepSeek-V3.2", messages, 0.7, 2000, func(content string) error {
		return onEvent("token", content)
	})
	if err != nil {
		return onEvent("error", fmt.Sprintf("stream chat failed: %v", err))
	}

	// 12. 发送完成事件
	return onEvent("done", nil)
}

// buildPromptMessages 根据检索结果构建 prompt 消息 (V1 兼容)
// Deprecated: 请使用 buildPromptMessagesV2
func (b *salesRAGBiz) buildPromptMessages(query string, verdict *service.RetrievalVerdict) []map[string]string {
	messagesV2 := b.buildPromptMessagesV2(query, verdict, "", "")
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
func (b *salesRAGBiz) buildPromptMessagesV2(query string, verdict *service.RetrievalVerdict, customerProfile string, languageStyle string) []adapter.ChatMessage {
	// 上下文长度限制常量
	const (
		maxCustomerProfileChars = 5000  // 客户画像最大字符数
		maxLanguageStyleChars   = 5000  // 语言风格最大字符数
		maxUserInputChars       = 40000 // 用户输入最大字符数
	)

	// 截断辅助函数
	truncate := func(s string, maxLen int) string {
		runes := []rune(s)
		if len(runes) <= maxLen {
			return s
		}
		return string(runes[:maxLen]) + "...(已截断)"
	}

	// 应用截断
	customerProfile = truncate(customerProfile, maxCustomerProfileChars)
	languageStyle = truncate(languageStyle, maxLanguageStyleChars)
	query = truncate(query, maxUserInputChars)

	// 合并所有检索到的知识
	allChunks := verdict.Evidence

	// 构建知识上下文（所有 reranked chunks）
	var contextParts []string
	if len(allChunks) > 0 {
		for i, chunk := range allChunks {
			contextParts = append(contextParts, fmt.Sprintf("[知识%d] %s", i+1, chunk.Content))
		}
	}
	knowledgeContext := strings.Join(contextParts, "\n\n")
	if knowledgeContext == "" {
		knowledgeContext = "（知识库中未找到相关内容，请基于通用销售经验回答）"
	}

	// 处理策略内容 (参考性)
	var strategyContent string
	if verdict.Strategy != nil {
		strategyContent = fmt.Sprintf(`
## 🌟 核心策略参考 (推荐方案)
系统分析认为客户当前可能处于【%s】阶段，推荐参考以下战术【%s】。
请结合实际对话上下文，**灵活参考**该策略中的"话术模板"或"核心逻辑"构建回复。如果策略与当前对话明显不符，请以你的判断为准。

### 推荐策略：[%s] %s
`+"```markdown"+`
%s
`+"```"+`
`, verdict.Strategy.MetaID, verdict.Strategy.Name, verdict.Strategy.ID, verdict.Strategy.Name, verdict.Strategy.Content)
	}

	// 统一的回复风格定义
	const responseStyles = `
### 选项A：激进型 (逼单)
- 风格：侧重利益刺激、稀缺性强调、促成即刻行动。
- 话术：直接、有冲击力。
### 选项B：保守型 (共情)
- 风格：侧重理解客户压力，提供情绪价值，建立信任关系。
- 话术：温暖、包容、不给压力。
### 选项C：纯知识回复 (专业)
- 风格：基于知识库内容，给出客观、专业、中立的解答。
- 话术：逻辑严密、数据支撑。`

	var systemPrompt string
	var userMessage string

	if verdict.ChatMode == "free" {
		// ========== Free 模式 (顾问/教练模式) ==========
		systemPrompt = fmt.Sprintf(`你是一位资深的销售顾问助手，正在帮助销售人员回复客户消息。

## 客户画像（如果为空则忽略此部分）
%s
%s
## 你的任务
请综合参考上述【核心策略参考】以及下方的【知识库内容】，为销售人员提供建议。
你需要分析客户意图，并基于以下三种风格分别给出回复建议：
%s

## 回复要求
1. 语气专业但亲切，适合微信聊天场景
2. 先给出分析或建议，再提供具体话术建议
3. **严禁编造**：回复建议必须严格遵循知识库。如果知识库中没有直接答案，你必须明确说明“知识库中未检索到相关信息，无法提供准确建议”，不得虚构信息。

## 回复话术的语言风格（如果为空则忽略此部分）
%s

## 知识库内容
%s`, customerProfile, strategyContent, responseStyles, languageStyle, knowledgeContext)

		userMessage = query

	} else {
		// ========== Sales 模式 (Sales Copilot) ==========
		// 目标：生成三个不同风格的回复选项，供销售参考
		systemPrompt = fmt.Sprintf(`你是一位顶尖的销售 Copilot。请根据当前对话情境，生成三条不同策略的待发送消息。

## 客户画像（如果为空则忽略此部分）
%s
%s
## 你的任务
请综合参考上述【核心策略参考】以及下方的【知识库内容】，生成以下三种风格的候选回复：
%s

## 🚫 严格禁止
1. **禁止解释**：不要写 "建议您..." 或分析原因，直接写能直接发送给客户的话术。
2. **严禁编造**：如果知识库中没有相关答案，必须在话术中说明需进一步核实。

## ✅ 核心要求
1. **极度口语化**：必须严格遵循下方的“语言风格”进行回复。
2. **第一人称**：直接用 "我" 回复 "您/你"。
3. **严格基于知识**：你的所有回复内容必须能在知识库中找到依据。
4. **参考策略**：请优先参考上方的【核心策略参考】进行回复。

## 输出格式要求
不需要使用任何 XML 标签。请直接使用 Markdown 标题分隔三个选项，格式如下：

### 选项A：激进型
(内容...)

### 选项B：保守型
(内容...)

### 选项C：纯知识回复
(内容...)

## 语言风格（如果为空则忽略此部分）
%s

## 知识库内容
%s`, customerProfile, strategyContent, responseStyles, languageStyle, knowledgeContext)

		userMessage = query
	}

	// 注入历史记录上下文
	if len(verdict.History) > 0 {
		historyStr := strings.Join(verdict.History, "\n")
		systemPrompt += fmt.Sprintf("\n\n## 对话历史上下文\n%s", historyStr)
	}

	return []adapter.ChatMessage{
		{
			Role:    "system",
			Content: systemPrompt,
		},
		{
			Role:    "user",
			Content: userMessage,
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
// chatMode: "sales" (销售话术模式) 或 "free" (自由讨论模式)
func (b *salesRAGBiz) ChatWithSession(ctx context.Context, userID uint, sessionID uint, query string, images []string, docIDs []uint, deepThinking bool, chatMode string, onEvent func(eventType string, data interface{}) error) error {
	// 1. 验证会话并加载历史消息
	session, err := b.sessionStore.GetSessionWithMessages(ctx, sessionID, userID)
	if err != nil {
		return fmt.Errorf("failed to get session: %w", err)
	}

	// 提取最近历史消息（例如最近 10 条）
	// 注意：session.Messages 是按时间正序排列的
	// 限制：最多5轮（10条消息），总字符数不超过20000
	const maxHistoryTurns = 5     // 最多5轮对话
	const maxHistoryChars = 20000 // 最大字符数

	var history []string
	if len(session.Messages) > 0 {
		// 从最近的消息开始，向前遍历
		maxMessages := maxHistoryTurns * 2 // 每轮2条消息
		start := 0
		if len(session.Messages) > maxMessages {
			start = len(session.Messages) - maxMessages
		}
		recentMsgs := session.Messages[start:]

		var tempHistory []string
		var totalChars int

		// 从最近的消息开始添加，直到达到字符限制
		for i := len(recentMsgs) - 1; i >= 0; i-- {
			m := recentMsgs[i]
			roleName := "销售"
			if m.Role == "user" {
				roleName = "客户"
			} else if m.Role == "assistant" {
				roleName = "销售助手"
			}
			entry := fmt.Sprintf("%s: %s", roleName, m.Content)

			// 检查是否超过字符限制
			if totalChars+len(entry) > maxHistoryChars {
				break
			}

			tempHistory = append(tempHistory, entry)
			totalChars += len(entry)
		}

		// 反转，恢复时间正序
		for i := len(tempHistory) - 1; i >= 0; i-- {
			history = append(history, tempHistory[i])
		}
	}

	// 2. 准备会话配置
	// 无论前端传什么，以数据库存储的选中知识库为准，确保一致性
	var sessionDocIDs []uint
	if session.DocumentIDs != "" && session.DocumentIDs != "null" {
		if err := json.Unmarshal([]byte(session.DocumentIDs), &sessionDocIDs); err != nil {
			log.Printf("[ChatWithSession] Warning: failed to parse session document_ids: %v", err)
		}
	}

	// 3. 处理用户消息
	var imagesJSON string
	if len(images) > 0 {
		imgBytes, _ := json.Marshal(images)
		imagesJSON = string(imgBytes)
	}

	userMessage := &model.SalesMessage{
		SessionID: sessionID,
		UserID:    userID,
		Role:      "user",
		Content:   query,
		Status:    "sent",
		Images:    imagesJSON,
	}
	if err := b.sessionStore.CreateMessage(ctx, userMessage); err != nil {
		return fmt.Errorf("failed to save user message: %w", err)
	}

	// 4. 调用流式检索，累积内容
	// 使用从数据库加载的 sessionDocIDs，而不是函数参数中的 docIDs
	var fullContent strings.Builder
	var verdictJSON string
	var thinkingText string

	err = b.RetrieveStream(ctx, query, history, sessionDocIDs, deepThinking, chatMode, session.CustomerProfile, func(eventType string, data interface{}) error {
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

// AnalyzeDocument 解析文档或图片并生成客户档案
// 支持文档类型（PDF、DOC等）和图片类型（微信聊天记录截图）
// 文档使用 dmxapi 处理，图片使用火山方舟视觉模型处理
func (b *salesRAGBiz) AnalyzeDocument(ctx context.Context, userID uint, file io.Reader, filename string) (string, error) {
	// 检测文件类型
	ext := strings.ToLower(filepath.Ext(filename))
	isImage := ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".gif" || ext == ".webp"

	if isImage {
		// 图片处理流程：上传到COS获取临时URL，然后调用视觉模型
		return b.analyzeImage(ctx, userID, file, filename)
	}

	// 文档处理流程（原有逻辑）
	return b.analyzeDocument(ctx, file, filename)
}

// analyzeImage 分析图片（微信聊天记录截图）
func (b *salesRAGBiz) analyzeImage(ctx context.Context, userID uint, file io.Reader, filename string) (string, error) {
	// 1. 读取图片数据
	imageData, err := io.ReadAll(file)
	if err != nil {
		return "", fmt.Errorf("读取图片失败: %w", err)
	}

	// 2. 检查图片大小，如果超过 10MB 则压缩
	const maxVisionSize = 10 * 1024 * 1024 // 10MB
	if len(imageData) > maxVisionSize {
		log.Printf("[analyzeImage] Image size (%d bytes) exceeds 10MB, starting compression...", len(imageData))

		// 转换字节为 image.Image
		src, err := imaging.Decode(bytes.NewReader(imageData))
		if err != nil {
			return "", fmt.Errorf("解码图片失败: %w", err)
		}

		// 计算缩放比例：基于像素面积的简单启发式
		// 视觉模型通常对像素总量有限制，或者我们通过缩放来显著减小文件体积
		// 我们先尝试将长宽各缩小到原来的 70% (面积约 50%)
		bounds := src.Bounds()
		width := bounds.Dx()
		height := bounds.Dy()

		// 如果是超长截图（长宽比过大），我们要小心缩放逻辑
		// 这里采用逐步缩放策略，直到大小达标或达到最小阈值
		quality := 85
		for len(imageData) > maxVisionSize && (width > 500 || height > 500) {
			width = int(float64(width) * 0.8)
			height = int(float64(height) * 0.8)

			dst := imaging.Resize(src, width, height, imaging.Lanczos)

			var buf bytes.Buffer
			// 使用标准库 jpeg 包进行编码，以支持质量设置
			err = jpeg.Encode(&buf, dst, &jpeg.Options{Quality: quality})
			if err != nil {
				return "", fmt.Errorf("压缩图片失败: %w", err)
			}
			imageData = buf.Bytes()

			log.Printf("[analyzeImage] Compressed to %dx%d, size: %d bytes (quality: %d)", width, height, len(imageData), quality)

			// 如果缩放后还是大，降低质量
			if quality > 60 {
				quality -= 10
			}

			// 更新当前 src 为已缩放的图，方便下一轮
			src = dst
		}

		if len(imageData) > maxVisionSize {
			return "", fmt.Errorf("图片过大且压缩失败 (当前大小: %d bytes)", len(imageData))
		}
	}

	log.Printf("图片已处理完成, 最终大小: %d bytes, 格式: image/jpeg", len(imageData))

	// 4. 选择传输方式：优先使用 COS URL，失败或未开启则回退到 base64
	var dataURL string
	if util.IsCOSEnabled() {
		// 生成 COS 对象路径: salesrag/analyze/userID/timestamp_filename.jpg
		objectKey := fmt.Sprintf("salesrag/analyze/%d/%d_%s", userID, time.Now().Unix(), "analysis.jpg")

		// 上传并设置 Content-Type
		cosURL, err := util.UploadBytesToCOS(ctx, objectKey, "image/jpeg", imageData)
		if err == nil && cosURL != "" {
			// 生成 10 分钟有效的签名 URL 供外部 API 访问
			signedURL, err := util.GenerateSignedURL(ctx, objectKey, 600)
			if err == nil && signedURL != "" {
				dataURL = signedURL
				log.Printf("[analyzeImage] Successfully uploaded to COS, using signed URL: %s", objectKey)
			}
		}

		if dataURL == "" {
			log.Printf("[analyzeImage] COS upload or sign failed, fallback to base64")
		}
	}

	if dataURL == "" {
		// 回退到 base64 方式
		base64Image := base64.StdEncoding.EncodeToString(imageData)
		dataURL = fmt.Sprintf("data:image/jpeg;base64,%s", base64Image)
		log.Printf("[analyzeImage] Using base64 data URL (size: %d)", len(dataURL))
	}

	// 4. 构建更加精准的端到端视觉分析提示词
	combinedPrompt := `你是一位顶尖的商业洞察专家，擅长从细微的社交互动中拆解客户画像。
这是一张【经典微信气泡对话式】聊天窗口的截图（可能是一张垂直拼接的长截图）。

请【完整扫描整张图片从上到下】的所有对话内容，并直接为我生成一份专业的客户画像。

### 核心视觉解析逻辑：
1. **场景确认**：请识别微信对话特有的气泡布局。
2. **重心偏移（客户为中心）**：
   - **左边气泡 = 客户方（核心分析对象）**：请深度挖掘此侧的所有表达。
   - **右边气泡 = 销售方（辅助理解）**：仅用于通过销售的提问或回复，来推断和修正对左侧客户真实意图的理解。
3. **内容过滤与提取规则**：
   - **保留表情符号**：请务必捕捉文字气泡中的表情（如：[呲牙]、🌹、握手等），这些是判断客户性格、情绪温度的关键指纹。
   - **排除非文字消息类型**：即使图片消息中含有文字，也请视为“多媒体干扰”而直接忽略（因为它们不代表当前的对话文字流）。

### 画像输出维度：
请基于上述扫描到的事实，生成以下画像：
1. **客户基础标签**：身份推测、行业背景、当前的沟通氛围。
2. **核心诉求与动机**：客户在对话中最关切的利益点、未明说的隐忧、购买的心理诱因。
3. **性格指纹分析**：结合文字风格与“表情包使用偏好”，分析客户是属于何种社交类型（如：谨慎型、豪爽型、礼貌疏离型等）。
4. **决策倾向与销售阶段**：当前所处的成交距离，以及客户在决策上的核心卡点。
5. **高价值跟进建议**：你应该如何调整自己的沟通节奏和话术风格来匹配该客户？

### 约束要求：
- 哪怕截图再长，也必须确保覆盖所有气泡内容。
- 严禁编造，必须有图有真相。
- 使用洗练、具备商业厚度的 Markdown 格式。
- **禁止任何开场白**，直接输出画像正文。`

	// 5. 调用火山方舟视觉模型（一步到位）
	profileResult, err := b.volcBiz.VisionAnalyze(ctx, dataURL, combinedPrompt, "")
	if err != nil {
		return "", fmt.Errorf("视觉端到端分析失败: %w", err)
	}

	log.Printf("画像端到端生成完成, 长度: %d", len(profileResult))

	return profileResult, nil
}

// analyzeDocument 分析文档（原有逻辑）
func (b *salesRAGBiz) analyzeDocument(ctx context.Context, file io.Reader, filename string) (string, error) {
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
	systemPrompt := `你是一个敏锐的销售战略专家。任务是基于提供的片段，为销售人员提取一份"高干货"客户画像，用于指导后续的回复话术生成。

##  核心原则：事实第一，不强求全
- 如果文档信息极少，只提炼最有保障的事实或高概率推测，严禁编造信息。
- 允许跳过无法确定的维度。格式虽然是结构化的，但内容根据实际资料灵活裁剪。

## 提炼维度（仅在信息可识别时输出）：
1. **基础背景**：对方是谁？（姓名、所属行业、规模、当前沟通背景/阶段等）
2. **核心需求与敏锐点**：对方在乎什么？（急需解决的问题，强烈的需求，或者对价格、安全、效率等维度的敏感倾向）
3. **性格与风格推断**：对方说话/行文的方式体现了什么特征？（如：老练果断、极度关注细节、犹豫不决等）
4. **其他关键信息**：你认为至关重要的客户特点

## 约束：
- 严格控制在300 - 500 字以内
- 直接以 Markdown 列表输出干货内容，严禁任何开场白和提示语`

	// 4. 调用 dmxapi 的 qwen-turbo-latest 模型
	return b.callDMXAPI(ctx, systemPrompt, "客户文档内容如下：\n\n"+content)
}

// generateProfileFromChat 基于聊天记录生成客户画像
// 已弃用：目前的图片处理流程已改为在 analyzeImage 中一次性完成端到端生

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

// AnalyzeChatStyle 分析聊天风格（语言指纹分析）
// 使用 dmxapi 的 qwen-turbo-latest 模型
func (b *salesRAGBiz) AnalyzeChatStyle(ctx context.Context, userID uint, file io.Reader, filename string) (string, error) {
	// 1. 解析内容
	text, err := b.parser.Parse(ctx, file, filename)
	if err != nil {
		return "", fmt.Errorf("failed to parse chat file: %w", err)
	}

	// 2. 截断 (避免 token 溢出)
	maxLen := 10000
	if len(text) > maxLen {
		text = text[:maxLen] + "\n...(truncated)"
	}

	// 2. 构建系统提示词
	systemPrompt := `你是一个资深的文字风格分析专家。由于现在的场景是微信文字聊天，请根据提供的语料，提炼出该销售人员的【文字沟通指纹】，以便让 AI 能够精准模仿

## 核心要求：
1. **严禁使用或提及任何表情（Emoji/颜文字）**，分析 and 生成的风格必须完全基于纯文字
2. **纯文字复刻**：重点分析文字如何分词、如何分段、如何使用助词，确保回复感真实不生硬

## 提炼维度：
1. **社交人设与称谓习惯**：
   - 沟通角色：是“利落的办事员”、“温润的顾问”、“平等的伙伴”还是别的角色？
   - 称谓偏好：对客户的称呼习惯（您/你，或者其他特定称谓）

2. **文字视觉指纹**：
   - 句式习惯：爱发大长段，还是习惯短句换行？
   - 标点符号：爱用规范标点，还是爱用空格/换行代替标点？

3. **语气词与词汇场**：
   - 标志性结尾：习惯用哪些收尾词（如：哈、呢、吧、哒、！）？
   - 高频用语：提取 10 个该销售最具代表性的口头禅或职业用语

4. **沟通逻辑脉络**：
   - 它是如何回答难题或提出建议的？（如：先说结论、先给方案、还是先客套？）

## 约束：
- 直接输出 Markdown 格式的风格说明书，严禁开场白和任何提示语
- 字数控制在 500 字以内`

	// 4. 调用 dmxapi 的 qwen-turbo-latest 模型
	analysis, err := b.callDMXAPI(ctx, systemPrompt, text)
	if err != nil {
		return "", err
	}

	// 5. 保存到数据库
	style := &model.LanguageStyle{
		UserID: userID,
		Style:  analysis,
	}
	if err := b.ds.LanguageStyles().Save(ctx, style); err != nil {
		log.Printf("Failed to save language style: %v", err)
	}

	return analysis, nil
}

// GetLanguageStyle 获取用户的语言风格
func (b *salesRAGBiz) GetLanguageStyle(ctx context.Context, userID uint) (string, error) {
	style, err := b.ds.LanguageStyles().Get(ctx, userID)
	if err != nil {
		return "", nil // Not found
	}
	return style.Style, nil
}

// CheckSemanticSplitterStatus 检查语义切分器状态
// 返回: (是否可用, 诊断信息, 错误)
func CheckSemanticSplitterStatus() (bool, string, error) {
	splitter := service.NewEmbeddingSplitter(service.EmbeddingSplitterConfig{
		Threshold:    0.6,
		MinChunkSize: 100,
		MaxChunkSize: 1000,
		OverlapSize:  100,
	})

	available := splitter.IsAvailable()
	if available {
		return true, "语义切分器(bge-small-zh)已就绪", nil
	}

	// 返回诊断信息
	info := `语义切分器(bge-small-zh)不可用。可能的原因：
1. Python3 未安装或不在 PATH 中
2. sentence-transformers 未安装: pip3 install sentence-transformers
3. 模型首次下载需要网络连接

安装命令:
  bash scripts/install_semantic_deps.sh

系统将自动回退到规则切分器。`

	return false, info, nil
}

// enrichChunksWithDocNames 为 chunks 填充 document_name 字段
// 通过批量查询数据库获取文档信息，避免 N+1 查询
func (b *salesRAGBiz) enrichChunksWithDocNames(ctx context.Context, chunks []domain.KnowledgeChunk) {
	if len(chunks) == 0 {
		log.Printf("[enrichChunksWithDocNames] No chunks to process")
		return
	}

	log.Printf("[enrichChunksWithDocNames] Processing %d chunks", len(chunks))

	// 1. 收集所有唯一的 document_id
	docIDSet := make(map[uint]bool)
	for i, chunk := range chunks {
		log.Printf("[enrichChunksWithDocNames] Chunk %d: ID=%s, DocID=%d, Score=%f", i, chunk.ID, chunk.DocumentID, chunk.Score)
		if chunk.DocumentID > 0 {
			docIDSet[chunk.DocumentID] = true
		}
	}

	if len(docIDSet) == 0 {
		log.Printf("[enrichChunksWithDocNames] No document IDs found in chunks")
		return
	}

	log.Printf("[enrichChunksWithDocNames] Found %d unique document IDs", len(docIDSet))

	// 2. 批量查询文档信息
	docIDToName := make(map[uint]string)
	for docID := range docIDSet {
		doc, err := b.ds.KnowledgeDocuments().GetByID(ctx, docID)
		if err != nil {
			log.Printf("[enrichChunksWithDocNames] Warning: failed to get document %d: %v", docID, err)
			continue
		}
		docIDToName[docID] = doc.Name
		log.Printf("[enrichChunksWithDocNames] Document %d -> Name: %s", docID, doc.Name)
	}

	// 3. 填充文档名称
	for i := range chunks {
		if name, ok := docIDToName[chunks[i].DocumentID]; ok {
			chunks[i].DocumentName = name
			log.Printf("[enrichChunksWithDocNames] Set chunk %d DocumentName to: %s", i, name)
		}
	}

	log.Printf("[enrichChunksWithDocNames] Finished processing. First chunk: DocName=%s, Score=%f",
		chunks[0].DocumentName, chunks[0].Score)
}
