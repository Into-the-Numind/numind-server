package salesrag

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"log"
	"math"
	"mime/multipart"
	"net"
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

	"numind-server/internal/numind/biz/ali"

	"github.com/disintegration/imaging"
	"gorm.io/gorm"
)

// streamTransport 流式 HTTP 请求共享的 Transport（复用连接池）
var streamTransport = &http.Transport{
	DialContext: (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	TLSHandshakeTimeout:   15 * time.Second,
	ResponseHeaderTimeout: 60 * time.Second,
}

// SalesRAGBiz 定义了销售 RAG 业务层的对外接口
type SalesRAGBiz interface {
	// Ingest 处理文档导入
	Ingest(ctx context.Context, userID uint, filename string, displayName string, reader io.Reader, opts IngestOptions) (uint, error)
	// Retrieve 检索知识（非流式）
	Retrieve(ctx context.Context, query string, docIDs []uint) (*service.RetrievalVerdict, error)
	// RetrieveStream 流式检索知识并生成回答
	// chatMode: "sales" (销售话术) 或 "free" (自由讨论)
	// onEvent: 事件回调，eventType 可为 "verdict"/"token"/"error"/"done"
	RetrieveStream(ctx context.Context, query string, history []string, docIDs []uint, docCategoryMap map[uint]string, deepThinking bool, chatMode string, customerProfile string, salesStage string, onEvent func(eventType string, data interface{}) error) error
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
	AnalyzeDocumentStream(ctx context.Context, userID uint, file io.Reader, filename string, onToken func(token string) error) (string, error)
	// AnalyzeProfileMultiFiles 多文件综合分析生成客户档案
	AnalyzeProfileMultiFiles(ctx context.Context, userID uint, files []*multipart.FileHeader, onToken func(token string) error) (string, error)

	// AnalyzeChatStyle 分析聊天风格（语言指纹分析）
	AnalyzeChatStyle(ctx context.Context, userID uint, chatData io.Reader, filename string) (string, error)
	AnalyzeChatStyleStream(ctx context.Context, userID uint, chatData io.Reader, filename string, onToken func(token string) error) (string, error)

	// GetLanguageStyle 获取用户的语言风格
	GetLanguageStyle(ctx context.Context, userID uint) (string, error)
	// SaveLanguageStyle 保存用户的语言风格
	SaveLanguageStyle(ctx context.Context, userID uint, style string) error

	// OCRAnalyze 识别图片中的文本（压缩 + 上传 COS + 调用视觉大模型）
	OCRAnalyze(ctx context.Context, userID uint, imageData []byte, contentType string, sessionID string, filename string) (ocrText string, cosURL string, err error)

	// ListOpinionTracks 获取系统内置观点赛道列表
	ListOpinionTracks(ctx context.Context) ([]model.OpinionTrack, error)
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
	ProductDocIDs   []uint `json:"product_doc_ids"`   // 产品文档
	CaseDocIDs      []uint `json:"case_doc_ids"`      // 成功案例
	FAQDocIDs       []uint `json:"faq_doc_ids"`       // 百问百答
	OpinionDocIDs   []uint `json:"opinion_doc_ids"`   // 观点库（用户上传）
	OpinionTrackIDs []uint `json:"opinion_track_ids"` // 观点库（系统赛道ID）
	DeepThinking    bool   `json:"deep_thinking"`
	CustomerProfile string `json:"customer_profile"` // Markdown 格式
	SalesStage      string `json:"sales_stage"`      // 销售阶段: ""(未选择), 初次接触, 了解业务, 方案介绍, 成交推进, 售后服务
}

type UpdateSessionRequest struct {
	Title           *string `json:"title"`
	DocumentIDs     []uint  `json:"document_ids"`
	ProductDocIDs   []uint  `json:"product_doc_ids"`   // 产品文档
	CaseDocIDs      []uint  `json:"case_doc_ids"`      // 成功案例
	FAQDocIDs       []uint  `json:"faq_doc_ids"`       // 百问百答
	OpinionDocIDs   []uint  `json:"opinion_doc_ids"`   // 观点库（用户上传）
	OpinionTrackIDs []uint  `json:"opinion_track_ids"` // 观点库（系统赛道ID）
	SalesStage      *string `json:"sales_stage"`       // 销售阶段: ""(未选择), 初次接触, 了解业务, 方案介绍, 成交推进, 售后服务
	DeepThinking    *bool   `json:"deep_thinking"`
	CustomerProfile *string `json:"customer_profile"`
}

type salesRAGBiz struct {
	ds                store.IStore
	ingestionPipeline *service.IngestionPipeline
	ragSvc            *service.SalesRAGService
	volcBiz           VolcBiz    // 添加大模型服务依赖（保留用于 fallback）
	aliBiz            ali.AliBiz // 阿里云 API 客户端
	sessionStore      store.SalesSessionStore
	parser            service.PipelineParser
	dmxClient         *adapter.DMXAPIClient // DMXAPI 客户端（用于 DeepSeek-V3.2）
}

// VolcBiz 火山引擎服务接口（避免循环依赖）
type VolcBiz interface {
	VolcTextStream(ctx context.Context, messages []map[string]string, maxTokens int, temperature float64) (string, error)
	// StreamChat 真正的流式聊天，通过回调函数逐 token 或思维链内容推送
	StreamChat(ctx context.Context, messages []map[string]interface{}, maxTokens int, temperature float64, deepThinking bool, onEvent func(event string, token string) error) (string, error)
	// VisionAnalyze 调用火山方舟视觉模型分析图片
	VisionAnalyze(ctx context.Context, imageURL string, prompt string, model string, maxTokens int, reasoningEffort string) (string, error)
	// VisionAnalyzeStream 流式分析图片
	VisionAnalyzeStream(ctx context.Context, imageURL string, prompt string, model string, maxTokens int, reasoningEffort string, onToken func(token string) error) (string, error)
	// ChatWithModel 非流式聊天
	ChatWithModel(ctx context.Context, messages []map[string]interface{}, model string, maxTokens int, temperature float64) (string, error)
	// StreamChatWithModel 流式聊天，支持指定模型
	StreamChatWithModel(ctx context.Context, messages []map[string]interface{}, model string, maxTokens int, temperature float64, deepThinking bool, onEvent func(event string, token string) error) (string, error)
}

func NewSalesRAGBiz(ds store.IStore, pipeline *service.IngestionPipeline, rag *service.SalesRAGService, volc VolcBiz, ali ali.AliBiz, sessionStore store.SalesSessionStore, parser service.PipelineParser) SalesRAGBiz {
	return &salesRAGBiz{
		ds:                ds,
		ingestionPipeline: pipeline,
		ragSvc:            rag,
		volcBiz:           volc,
		aliBiz:            ali,
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
	if doc.IsSystem {
		return fmt.Errorf("cannot delete system document")
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
// chatMode: "sales" (销售话术) 或// RetrieveStream 流式检索知识并生成回答
// 修改：增加 docCategoryMap 参数，用于传递文档分类信息
func (b *salesRAGBiz) RetrieveStream(ctx context.Context, query string, history []string, docIDs []uint, docCategoryMap map[uint]string, deepThinking bool, chatMode string, customerProfile string, salesStage string, onEvent func(eventType string, data interface{}) error) error {
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

	// 2. 查询用户所有文档 + 系统文档
	docs, err := b.ds.KnowledgeDocuments().ListByUser(ctx, userID)
	if err != nil {
		return onEvent("error", fmt.Sprintf("failed to query user documents: %v", err))
	}
	sysDocs, sysErr := b.ds.KnowledgeDocuments().ListSystemDocs(ctx)
	if sysErr != nil {
		log.Printf("[RetrieveStream] Warning: failed to query system docs: %v", sysErr)
	} else {
		docs = append(docs, sysDocs...)
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
	verdict, err := b.ragSvc.RetrieveForResponseV2(ctx, query, filteredDocIDs, history, chatMode, userID, func(status string) {
		_ = onEvent("status", status)
	})
	if err != nil {
		return onEvent("error", fmt.Sprintf("retrieval failed: %v", err))
	}

	// 注入分类映射
	verdict.DocCategoryMap = docCategoryMap

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
	messages := b.buildPromptMessagesV2(query, verdict, customerProfile, languageStyle, salesStage)

	// 11. 调用 DMXAPI DeepSeek-V3.2 流式聊天（支持思考模式）
	// 注意：deepThinking 参数决定是否启用思维链，不再对输出 Token 设限
	_, err = b.dmxClient.StreamChatCompletion(ctx, "DeepSeek-V3.2", messages, 0.7, 0, deepThinking, func(eventType, content string) error {
		if eventType == "thinking" {
			return onEvent("thinking", content)
		}
		// eventType == "content"
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
	messagesV2 := b.buildPromptMessagesV2(query, verdict, "", "", "")
	result := make([]map[string]string, len(messagesV2))
	for i, msg := range messagesV2 {
		result[i] = map[string]string{
			"role":    msg.Role,
			"content": msg.Content,
		}
	}
	return result
}

// buildPromptMessagesV2 根据检索结果构建 prompt 消息（优化版）
func (b *salesRAGBiz) buildPromptMessagesV2(query string, verdict *service.RetrievalVerdict, customerProfile string, languageStyle string, salesStage string) []adapter.ChatMessage {
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

	// 构建知识上下文（附带 Rerank 置信度信号）
	// 构建知识上下文（按分类分组）
	var knowledgeContext string
	allChunks := verdict.Evidence
	if len(allChunks) > 0 {
		// 分组存储内容
		categorizedContent := make(map[string][]string)
		// 预定义顺序
		categories := []string{"产品文档", "成功案例", "百问百答", "观点库", "其他相关文档"}

		for i, chunk := range allChunks {
			category := "其他相关文档"
			if cat, ok := verdict.DocCategoryMap[chunk.DocumentID]; ok && cat != "" {
				category = cat
			}

			var contentLine string
			if chunk.Score > 0 {
				contentLine = fmt.Sprintf("[知识%d] (相关度:%.0f%%) %s", i+1, chunk.Score*100, chunk.Content)
			} else {
				contentLine = fmt.Sprintf("[知识%d] %s", i+1, chunk.Content)
			}

			categorizedContent[category] = append(categorizedContent[category], contentLine)
		}

		var contextParts []string
		for _, cat := range categories {
			if contents, ok := categorizedContent[cat]; ok && len(contents) > 0 {
				section := fmt.Sprintf("### %s\n%s", cat, strings.Join(contents, "\n\n"))
				contextParts = append(contextParts, section)
			}
		}

		knowledgeContext = strings.Join(contextParts, "\n\n")
	}

	// 构建策略内容（只包含纯内容）
	var strategyContent string
	if verdict.Strategy != nil {
		strategyContent = verdict.Strategy.Content
	}

	var systemPrompt string
	var userMessage string

	if verdict.ChatMode == "free" {
		// ========== Free 模式 (Sales Copilot 顾问模式) ==========
		systemPrompt = b.buildFreeModePrompt(customerProfile, knowledgeContext, strategyContent, languageStyle, verdict.History, salesStage)
		userMessage = query
	} else {
		// ========== Sales 模式 (销售人员本人视角) ==========
		systemPrompt = b.buildSalesModePrompt(customerProfile, knowledgeContext, strategyContent, languageStyle, verdict.History, salesStage)
		userMessage = query
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

// getSalesStageDescription 获取销售阶段简介
func getSalesStageDescription(stage string) string {
	switch stage {
	case "破冰诊断":
		return "建立信任，挖出客户需求"
	case "价值塑造":
		return "塑造产品价值，让客户想要"
	case "异议处理":
		return "打消顾虑，扫除成交障碍"
	case "关单追销":
		return "临门一脚，促成交易下单"
	default:
		return ""
	}
}

// buildSalesModePrompt 构建 Sales 模式提示词
func (b *salesRAGBiz) buildSalesModePrompt(customerProfile, knowledgeContext, strategyContent, languageStyle string, history []string, salesStage string) string {
	var prompt strings.Builder

	// 角色和目标
	prompt.WriteString("你是一位顶尖的销售人员，正在通过微信与客户进行一对一沟通。你的目标是根据当前对话情境，草拟三条不同策略风格的待发送消息。\n\n")

	// 客户背景信息（条件渲染）
	hasBackground := customerProfile != "" || len(history) > 0
	if hasBackground {
		prompt.WriteString("## 客户背景信息\n\n")

		if customerProfile != "" {
			prompt.WriteString("### 客户画像\n")
			prompt.WriteString(customerProfile)
			prompt.WriteString("\n\n")
		}

		if len(history) > 0 {
			prompt.WriteString("### 对话历史\n")
			prompt.WriteString(strings.Join(history, "\n"))
			prompt.WriteString("\n\n")
		}

		prompt.WriteString("---\n")
	}

	// 当前销售阶段（条件渲染 - 仅非空时显示）
	if salesStage != "" {
		stageDesc := getSalesStageDescription(salesStage)
		prompt.WriteString("### 当前销售阶段\n")
		prompt.WriteString(salesStage)
		if stageDesc != "" {
			prompt.WriteString("\n")
			prompt.WriteString(stageDesc)
		}
		prompt.WriteString("\n\n")
	}

	// 核心参考资料
	prompt.WriteString("## 核心参考资料\n\n")

	// 知识库内容（条件渲染，附带权重指引）
	if knowledgeContext != "" {
		prompt.WriteString("### 知识库内容\n")
		prompt.WriteString("> 每条知识附带相关度百分比。相关度 ≥ 70% 的知识应重点融入回答；30%-70% 的仅作补充参考；如果所有知识相关度都偏低，以你的专业判断为主。\n\n")
		prompt.WriteString("> **注意**：这些知识片段可能来自不同文档，彼此之间可能没有直接关联。请先通读理解所有片段的核心信息，形成统一认知后，围绕客户的实际问题用你自己的话自然组织回答。禁止逐条罗列，禁止生硬拼接不相关的信息。如果某条知识与当前问题关联不大，果断忽略即可。\n\n")
		prompt.WriteString(knowledgeContext)
		prompt.WriteString("\n\n")
	}

	// 核心策略参考（条件渲染）
	if strategyContent != "" {
		prompt.WriteString("### 核心策略参考\n")
		prompt.WriteString("请结合实际对话上下文，**灵活参考**该策略中的\"话术模板\"或\"核心逻辑\"构建回复。如果策略与当前对话相符，需要严格按照策略来进行，如果明显不符，请以你的判断为准。\n\n")
		prompt.WriteString("```markdown\n")
		prompt.WriteString(strategyContent)
		prompt.WriteString("\n```\n\n")
	}

	prompt.WriteString("---\n")

	// 你的任务（风格定义 + 输出格式合并）
	prompt.WriteString("## 你的任务\n")
	prompt.WriteString("请综合参考上述信息，为当前客户消息生成三个不同风格的回复选项。直接使用 Markdown 三级标题分隔：\n\n")
	prompt.WriteString("### 选项A：主动型（推进风格）\n")
	prompt.WriteString("侧重价值呈现和时机把握，在尊重客户边界的前提下积极推进。话术坚定有方向感但避免压迫感，用利益引导代替强制推销，用提问和确认代替单向推进，确保客户感受到被尊重的同时明确下一步行动。\n（直接写给客户的话术，可分多段）\n\n")
	prompt.WriteString("### 选项B：保守型（共情风格）\n")
	prompt.WriteString("侧重理解客户压力、提供情绪价值、建立信任关系。话术温暖包容。\n（直接写给客户的话术，可分多段）\n\n")
	prompt.WriteString("### 选项C：高势能回复（专业风格）\n")
	prompt.WriteString("侧重展现专业判断力、行业高度与决策感，通过观点输出建立“选你没错”的心理认知。话术逻辑犀利、引用数据/案例佐证、指出提问者背后的认知盲区。\n（直接写给客户的话术，可分多段）\n\n")
	prompt.WriteString("---\n")

	// 核心规则
	prompt.WriteString("## 核心规则\n\n")
	prompt.WriteString("### 必须严格遵守\n")
	prompt.WriteString("1. **第一人称视角**  \n")
	prompt.WriteString("   直接用\"我\"回复\"您/你\"，你就是销售本人\n\n")
	prompt.WriteString("2. **极度口语化**  \n")
	if languageStyle != "" {
		prompt.WriteString("   必须严格遵循下方的【语言风格参考】  \n")
	}
	prompt.WriteString("   符合微信聊天场景的自然表达\n\n")
	prompt.WriteString("3. **知识为锚，判断为翼**  \n")
	if knowledgeContext != "" {
		prompt.WriteString("   涉及具体产品信息（价格、功能、参数、案例）时，必须基于【知识库内容】  \n")
		prompt.WriteString("   涉及策略分析、客户心理、沟通技巧时，请充分发挥你的专业判断  \n")
		prompt.WriteString("   优先融合相关度高的知识，用你自己的理解重新组织，而非原文照搬  \n")
	}
	if strategyContent != "" {
		prompt.WriteString("   灵活参考【核心策略参考】中的话术模板或逻辑\n\n")
	} else {
		prompt.WriteString("\n")
	}
	prompt.WriteString("4. **灵活判断**  \n")
	prompt.WriteString("   如果推荐策略与当前对话明显不符，以实际情况为准  \n")
	prompt.WriteString("   三种风格只是参考方向，可以适度调整\n\n")
	prompt.WriteString("5. **严守微信销售场景与角色边界**  \n")
	prompt.WriteString("   - 你正在微信上进行销售对话，严禁引导至线下见面、电话沟通或其他非微信渠道  \n")
	prompt.WriteString("   - 你是销售人员，不是顾问/专家，严禁主动提供免费诊断、免费分析、免费咨询等\"增值服务\"  \n")
	prompt.WriteString("   - 销售推进依靠产品价值本身，而非额外赠送的服务或转移场景\n\n")

	prompt.WriteString("### 严格禁止\n")
	prompt.WriteString("1. **禁止元对话**\n")
	prompt.WriteString("   - 不要写\"建议您...\"、\"可以这样回复...\"等建议性语言\n")
	prompt.WriteString("   - 不要分析原因或解释为什么这样回复\n\n")
	prompt.WriteString("2. **禁止编造信息**\n")
	prompt.WriteString("   - 不得虚构产品功能、价格等数据\n")
	prompt.WriteString("   - 不得编造客户案例信息或对话历史\n")
	prompt.WriteString("   - 宁可说\"不确定\"，也不要编造\n\n")
	prompt.WriteString("3. **禁止僵化套用**\n")
	prompt.WriteString("   - 不要机械套用模板而忽视问题本质\n")
	prompt.WriteString("   - 根据实际需求灵活调整\n\n")
	prompt.WriteString("4. **禁止误导性建议**\n")
	prompt.WriteString("   - 不提供违背商业道德的建议\n")
	prompt.WriteString("   - 不得欺骗或误导客户\n")
	prompt.WriteString("   - 不得过度承诺或虚假宣传\n\n")

	prompt.WriteString("---\n")
	prompt.WriteString("### 语言风格参考\n")
	if languageStyle != "" {
		prompt.WriteString(languageStyle)
	} else {
		prompt.WriteString("使用通用的微信聊天风格：简洁、自然、适度使用口语化表达")
	}
	prompt.WriteString("\n\n")

	prompt.WriteString("---\n")
	prompt.WriteString("现在请基于以上所有信息，为客户的这条消息生成三个回复选项。")

	return prompt.String()
}

// buildFreeModePrompt 构建 Free 模式提示词（资深销售教练）
func (b *salesRAGBiz) buildFreeModePrompt(customerProfile, knowledgeContext, strategyContent, languageStyle string, history []string, salesStage string) string {
	var prompt strings.Builder

	// 角色和目标
	prompt.WriteString("你是一位资深的销售教练，拥有丰富的一线实战经验和客户心理洞察力。你以搭档的身份协助销售人员分析局面、制定策略、打磨话术。\n\n")

	// 客户背景信息（条件渲染）
	hasBackground := customerProfile != "" || len(history) > 0
	if hasBackground {
		prompt.WriteString("## 客户背景信息\n\n")

		if customerProfile != "" {
			prompt.WriteString("### 客户画像\n")
			prompt.WriteString(customerProfile)
			prompt.WriteString("\n\n")
		}

		if len(history) > 0 {
			prompt.WriteString("### 对话历史\n")
			prompt.WriteString(strings.Join(history, "\n"))
			prompt.WriteString("\n\n")
		}

		prompt.WriteString("---\n")
	}

	// 当前销售阶段（条件渲染 - 仅非空时显示）
	if salesStage != "" {
		stageDesc := getSalesStageDescription(salesStage)
		prompt.WriteString("### 当前销售阶段\n")
		prompt.WriteString(salesStage)
		if stageDesc != "" {
			prompt.WriteString("\n")
			prompt.WriteString(stageDesc)
		}
		prompt.WriteString("\n\n")
	}

	// 核心参考资料
	prompt.WriteString("## 核心参考资料\n\n")

	// 知识库内容（条件渲染，附带权重指引）
	if knowledgeContext != "" {
		prompt.WriteString("### 知识库内容\n")
		prompt.WriteString("每条知识附带相关度百分比。相关度 ≥ 70% 的知识应重点融入回答；30%-70% 的仅作补充参考；如果所有知识相关度都偏低，以你的专业判断为主。\n")
		prompt.WriteString("注意：这些知识片段可能来自不同文档，彼此之间可能没有直接关联。请先通读理解所有片段的核心信息，形成统一认知后，围绕客户的实际问题用你自己的话自然组织回答。禁止逐条罗列，禁止生硬拼接不相关的信息。如果某条知识与当前问题关联不大，果断忽略即可。\n")
		prompt.WriteString(knowledgeContext)
		prompt.WriteString("\n\n")
	}

	// 核心策略参考（条件渲染）
	if strategyContent != "" {
		prompt.WriteString("### 核心策略参考\n")
		prompt.WriteString("请结合实际对话上下文，灵活参考该策略中的\"话术模板\"或\"核心逻辑\"提供建议。如果策略与当前对话相符，需要严格按照策略来进行，如果明显不符，请以你的判断为准。\n")
		prompt.WriteString(strategyContent)
		prompt.WriteString("\n\n")
	}

	prompt.WriteString("---\n")

	// 你的任务
	prompt.WriteString("## 你的任务\n")
	prompt.WriteString("你需要判断用户问题的意图，并判断是否需要给出示例回复。\n\n")
	prompt.WriteString("### 需示例回复型\n")
	prompt.WriteString("当判断用户的问题需要提供示例回复时，需要根据用户的问题或需求进行解答，同时提供三种回复选项，格式如下：\n")
	prompt.WriteString("选项A：主动型（推进风格）\n")
	prompt.WriteString("侧重价值呈现和时机把握，在尊重客户边界的前提下积极推进。话术坚定有方向感但避免压迫感，用利益引导代替强制推销。适用于客户有一定意向但需要推动决策的场景。\n")
	prompt.WriteString("分析：（为什么选这个策略）\n")
	prompt.WriteString("建议话术：（具体回复内容）\n\n")
	prompt.WriteString("选项B：保守型\n")
	prompt.WriteString("理解客户压力、提供情绪价值、建立信任。适用于客户有顾虑的场景。\n")
	prompt.WriteString("分析：（为什么选这个策略）\n")
	prompt.WriteString("建议话术：（具体回复内容）\n\n")
	prompt.WriteString("选项C：高势能回复\n")
	prompt.WriteString("展现专业判断力、行业高度与决策感，通过观点输出建立信任。话术逻辑犀利、引用数据/案例佐证、指出客户背后的认知盲区。\n")
	prompt.WriteString("分析：（为什么选这个策略）\n")
	prompt.WriteString("建议话术：（具体回复内容）\n\n")
	prompt.WriteString("**如果销售人员明确要求其他方式（如只要一个答案、或特定风格），按需求调整**\n\n")
	prompt.WriteString("### 无需示例回复型\n")
	prompt.WriteString("当判断用户的问题**不**需要提供示例回复时，直接提供清晰、专业的解答。\n\n")
	prompt.WriteString("---\n")

	// 核心规则
	prompt.WriteString("## 核心规则\n\n")
	prompt.WriteString("### 必须严格遵守\n")
	prompt.WriteString("1. 区分事实与判断\n")
	if hasBackground {
		prompt.WriteString("  - 结合【客户背景信息】进行分析\n")
	}
	if knowledgeContext != "" {
		prompt.WriteString("  - 产品事实（价格、功能、参数、案例）必须有知识库依据，不得编造\n")
	} else {
		prompt.WriteString("  - 产品事实（价格、功能、参数、案例）必须核实，不得编造\n")
	}
	if strategyContent != "" {
		prompt.WriteString("  - 策略分析和通用知识（客户心理、沟通技巧、行业经验），参考【核心策略参考】中的方法论和话术模板，并运用你的专业判断自由发挥，如果推荐策略与实际情况明显不符，以实际为准\n")
	} else {
		prompt.WriteString("  - 策略分析和通用知识（客户心理、沟通技巧、行业经验），运用你的专业判断自由发挥\n")
	}
	prompt.WriteString("  - 如果销售人员问的是具体产品信息但知识库中没有，说明\"知识库暂未收录该信息，建议确认\"\n\n")
	prompt.WriteString("2. 顾问视角，专业友好\n")
	prompt.WriteString("  - 你是在帮助销售人员，可以分析、建议、指导\n")
	prompt.WriteString("  - 语气专业但亲切，避免说教\n")
	prompt.WriteString("  - 提供可执行的具体建议，而非空泛理论\n\n")
	prompt.WriteString("3. 灵活判断，因地制宜\n")
	prompt.WriteString("  - 根据问题类型选择最合适的回答方式、格式和深度\n\n")
	prompt.WriteString("4. 尊重销售人员的意图\n")
	prompt.WriteString("  - 准确识别销售人员的真实需求和意图\n")
	prompt.WriteString("  - 如果问题有歧义，优先选择最合理的解释\n")
	prompt.WriteString("  - 不要过度发挥或答非所问\n\n")
	prompt.WriteString("5. 结构清晰：使用 Markdown 格式合理组织内容\n\n")
	prompt.WriteString("6. 严守微信销售场景与角色边界\n")
	prompt.WriteString("  - 你正在微信上进行销售对话，严禁引导至线下见面、电话沟通或其他非微信渠道\n")
	prompt.WriteString("  - 你是销售人员，不是顾问/专家，严禁主动提供免费诊断、免费分析、免费咨询等\"增值服务\"\n")
	prompt.WriteString("  - 销售推进依靠产品价值本身，而非额外赠送的服务或转移场景\n\n")

	prompt.WriteString("### 严格禁止\n")
	prompt.WriteString("1. 禁止编造信息\n")
	prompt.WriteString("  - 不得虚构产品功能、价格等数据\n")
	prompt.WriteString("  - 不得编造客户案例信息或对话历史\n")
	prompt.WriteString("  - 宁可说\"不确定\"，也不要编造\n\n")
	prompt.WriteString("2. 禁止误导性建议\n")
	prompt.WriteString("  - 不提供违背商业道德的建议\n")
	prompt.WriteString("  - 不得欺骗或误导客户\n")
	prompt.WriteString("  - 不得过度承诺或虚假宣传\n\n")
	prompt.WriteString("3. 禁止僵化套用\n")
	prompt.WriteString("  - 不要不管什么问题都输出\"三种风格\"\n")
	prompt.WriteString("  - 不要机械套用模板而忽视问题本质\n")
	prompt.WriteString("  - 根据实际需求灵活调整\n\n")

	prompt.WriteString("---\n")

	// 语言风格参考
	prompt.WriteString("## 语言风格参考\n")
	if languageStyle != "" {
		prompt.WriteString("销售人员的语言风格如下，在提供话术建议时应参考这个风格：")
		prompt.WriteString(languageStyle)
	} else {
		prompt.WriteString("销售人员的语言风格如下，在提供话术建议时应参考这个风格：使用通用的微信聊天风格：简洁、自然、适度使用口语化表达")
	}
	prompt.WriteString("\n")
	prompt.WriteString("注意：语言风格主要用于话术建议，其他类型的回答（如分析、讲解）保持专业清晰即可。\n\n")

	// 结尾引导
	prompt.WriteString("---\n")
	prompt.WriteString("现在请基于以上指引，理解销售人员的问题并提供最合适的帮助。")

	return prompt.String()
}

// ============ 会话管理方法 ============

// CreateSession 创建新的销售会话
func (b *salesRAGBiz) CreateSession(ctx context.Context, userID uint, req CreateSessionRequest) (*model.SalesSession, error) {
	// 序列化 DocumentIDs 为 JSON（向后兼容）
	docIDsJSON, err := json.Marshal(req.DocumentIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal document_ids: %w", err)
	}

	// 序列化分类文档 ID
	productJSON, _ := json.Marshal(req.ProductDocIDs)
	caseJSON, _ := json.Marshal(req.CaseDocIDs)
	faqJSON, _ := json.Marshal(req.FAQDocIDs)
	opinionJSON, _ := json.Marshal(req.OpinionDocIDs)
	opinionTrackJSON, _ := json.Marshal(req.OpinionTrackIDs)

	// 创建会话
	session := &model.SalesSession{
		UserID:          userID,
		Title:           req.Title,
		Status:          "active",
		DocumentIDs:     string(docIDsJSON),
		ProductDocIDs:   string(productJSON),
		CaseDocIDs:      string(caseJSON),
		FAQDocIDs:       string(faqJSON),
		OpinionDocIDs:   string(opinionJSON),
		OpinionTrackIDs: string(opinionTrackJSON),
		DeepThinking:    req.DeepThinking,
		CustomerProfile: req.CustomerProfile,
		SalesStage:      req.SalesStage, // 销售阶段
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
	log.Printf("[UpdateSession] Received request for SessionID: %d, UserID: %d", sessionID, userID)
	log.Printf("[UpdateSession] ProductDocIDs: %v (len: %d)", req.ProductDocIDs, len(req.ProductDocIDs))
	log.Printf("[UpdateSession] CaseDocIDs: %v (len: %d)", req.CaseDocIDs, len(req.CaseDocIDs))
	log.Printf("[UpdateSession] FAQDocIDs: %v (len: %d)", req.FAQDocIDs, len(req.FAQDocIDs))
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
	// 更新三个分类字段
	if req.ProductDocIDs != nil {
		productJSON, _ := json.Marshal(req.ProductDocIDs)
		session.ProductDocIDs = string(productJSON)
	}
	if req.CaseDocIDs != nil {
		caseJSON, _ := json.Marshal(req.CaseDocIDs)
		session.CaseDocIDs = string(caseJSON)
	}
	if req.FAQDocIDs != nil {
		faqJSON, _ := json.Marshal(req.FAQDocIDs)
		session.FAQDocIDs = string(faqJSON)
	}
	if req.OpinionDocIDs != nil {
		opinionJSON, _ := json.Marshal(req.OpinionDocIDs)
		session.OpinionDocIDs = string(opinionJSON)
	}
	if req.OpinionTrackIDs != nil {
		opinionTrackJSON, _ := json.Marshal(req.OpinionTrackIDs)
		session.OpinionTrackIDs = string(opinionTrackJSON)
	}
	if req.DeepThinking != nil {
		session.DeepThinking = *req.DeepThinking
	}
	if req.CustomerProfile != nil {
		session.CustomerProfile = *req.CustomerProfile
	}
	if req.SalesStage != nil {
		session.SalesStage = *req.SalesStage
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

// ListOpinionTracks 获取系统内置观点赛道列表
func (b *salesRAGBiz) ListOpinionTracks(ctx context.Context) ([]model.OpinionTrack, error) {
	var tracks []model.OpinionTrack
	if err := b.ds.DB().WithContext(ctx).Where("is_enabled = ?", true).Order("sort_order ASC").Find(&tracks).Error; err != nil {
		return nil, fmt.Errorf("failed to list opinion tracks: %w", err)
	}
	return tracks, nil
}

// resolveTrackDocIDs 将赛道 ID 解析为对应的 KnowledgeDocument ID
func (b *salesRAGBiz) resolveTrackDocIDs(ctx context.Context, trackIDs []uint) []uint {
	if len(trackIDs) == 0 {
		return nil
	}
	var tracks []model.OpinionTrack
	if err := b.ds.DB().WithContext(ctx).Where("id IN ? AND is_enabled = ?", trackIDs, true).Find(&tracks).Error; err != nil {
		log.Printf("[resolveTrackDocIDs] Warning: failed to query tracks: %v", err)
		return nil
	}
	var docIDs []uint
	for _, t := range tracks {
		if t.DocID > 0 {
			docIDs = append(docIDs, t.DocID)
		}
	}
	return docIDs
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
	// 优先使用分类字段，合并所有分类的文档 ID 传给检索
	var sessionDocIDs []uint
	var productDocIDs, caseDocIDs, faqDocIDs, opinionDocIDs []uint
	var opinionTrackIDs []uint

	// 解析分类字段
	if session.ProductDocIDs != "" && session.ProductDocIDs != "null" {
		if err := json.Unmarshal([]byte(session.ProductDocIDs), &productDocIDs); err != nil {
			log.Printf("[ChatWithSession] Warning: failed to parse product_doc_ids: %v", err)
		}
	}
	if session.CaseDocIDs != "" && session.CaseDocIDs != "null" {
		if err := json.Unmarshal([]byte(session.CaseDocIDs), &caseDocIDs); err != nil {
			log.Printf("[ChatWithSession] Warning: failed to parse case_doc_ids: %v", err)
		}
	}
	if session.FAQDocIDs != "" && session.FAQDocIDs != "null" {
		if err := json.Unmarshal([]byte(session.FAQDocIDs), &faqDocIDs); err != nil {
			log.Printf("[ChatWithSession] Warning: failed to parse faq_doc_ids: %v", err)
		}
	}
	if session.OpinionDocIDs != "" && session.OpinionDocIDs != "null" {
		if err := json.Unmarshal([]byte(session.OpinionDocIDs), &opinionDocIDs); err != nil {
			log.Printf("[ChatWithSession] Warning: failed to parse opinion_doc_ids: %v", err)
		}
	}
	if session.OpinionTrackIDs != "" && session.OpinionTrackIDs != "null" {
		if err := json.Unmarshal([]byte(session.OpinionTrackIDs), &opinionTrackIDs); err != nil {
			log.Printf("[ChatWithSession] Warning: failed to parse opinion_track_ids: %v", err)
		}
	}

	// 将系统赛道 ID 解析为对应的文档 ID
	trackDocIDs := b.resolveTrackDocIDs(ctx, opinionTrackIDs)

	// 合并所有分类文档 ID
	sessionDocIDs = append(sessionDocIDs, productDocIDs...)
	sessionDocIDs = append(sessionDocIDs, caseDocIDs...)
	sessionDocIDs = append(sessionDocIDs, faqDocIDs...)
	sessionDocIDs = append(sessionDocIDs, opinionDocIDs...)
	sessionDocIDs = append(sessionDocIDs, trackDocIDs...)

	// 向后兼容：如果三个分类字段都为空，则 fallback 到旧 document_ids 字段
	if len(sessionDocIDs) == 0 && session.DocumentIDs != "" && session.DocumentIDs != "null" {
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

	// 构建文档分类映射
	docCategoryMap := make(map[uint]string)
	for _, id := range productDocIDs {
		docCategoryMap[id] = "产品文档"
	}
	for _, id := range caseDocIDs {
		docCategoryMap[id] = "成功案例"
	}
	for _, id := range faqDocIDs {
		docCategoryMap[id] = "百问百答"
	}
	for _, id := range opinionDocIDs {
		docCategoryMap[id] = "观点库"
	}
	for _, id := range trackDocIDs {
		docCategoryMap[id] = "观点库"
	}

	// 4. 调用流式检索，累积内容
	// 使用从数据库加载的 sessionDocIDs，而不是函数参数中的 docIDs
	var fullContent strings.Builder
	var verdictJSON string
	var thinkingText string

	err = b.RetrieveStream(ctx, query, history, sessionDocIDs, docCategoryMap, deepThinking, chatMode, session.CustomerProfile, session.SalesStage, func(eventType string, data interface{}) error {
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

// AnalyzeProfileMultiFiles 多文件综合分析生成客户档案
// 支持图片（微信截图）和文档（PDF/DOC/TXT）混合输入
// 采用 "Mixed Context" 模式，将所有内容整合到一个 Context 中由 Doubao-Seed-1.8 模型进行端到端分析
func (b *salesRAGBiz) AnalyzeProfileMultiFiles(ctx context.Context, userID uint, files []*multipart.FileHeader, onToken func(token string) error) (string, error) {
	log.Printf("[AnalyzeProfileMultiFiles] Starting analysis for user %d, file count: %d", userID, len(files))

	// 1. 构建多模态消息内容
	var contentParts []map[string]interface{}

	// 添加引导文本
	contentParts = append(contentParts, map[string]interface{}{
		"type": "text",
		"text": "以下是该客户的相关资料，包含微信聊天记录截图和文档资料：",
	})

	for i, fileHeader := range files {
		src, err := fileHeader.Open()
		if err != nil {
			log.Printf("Failed to open file %s: %v", fileHeader.Filename, err)
			continue
		}
		defer src.Close()

		ext := strings.ToLower(filepath.Ext(fileHeader.Filename))
		isImage := ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".gif" || ext == ".webp"

		if isImage {
			// 图片处理：读取 -> 压缩（如需）-> 上传 COS -> 获取 URL
			imageData, err := io.ReadAll(src)
			if err != nil {
				log.Printf("Failed to read image %s: %v", fileHeader.Filename, err)
				continue
			}

			// 针对微信聊天记录长截图的压缩处理
			// 火山方舟 Doubao-Seed 模型限制：大小 < 10MB, 总像素 < 36,000,000, 长宽比 < 150:1
			const maxVisionSize = 10 * 1024 * 1024 // 10MB
			const maxTotalPixels = 36000000        // 36MP

			// 解码图片检查属性
			img, err := imaging.Decode(bytes.NewReader(imageData))
			if err != nil {
				log.Printf("Failed to decode image %s: %v", fileHeader.Filename, err)
				continue
			}

			bounds := img.Bounds()
			width, height := bounds.Dx(), bounds.Dy()
			totalPixels := int64(width) * int64(height)
			aspectRatio := float64(height) / float64(width)
			if width > height {
				aspectRatio = float64(width) / float64(height)
			}

			// 检查是否需要压缩（大小、像素、长宽比任一超标）
			if len(imageData) > maxVisionSize || totalPixels > maxTotalPixels || aspectRatio > 150 {
				log.Printf("[AnalyzeProfileMultiFiles] Image %s needs compression (Size: %d, Pixels: %d, Ratio: %.2f)",
					fileHeader.Filename, len(imageData), totalPixels, aspectRatio)

				// 微信长截图通常宽高比很大，需要更激进的压缩策略
				scale := 1.0
				if totalPixels > maxTotalPixels {
					scale = math.Sqrt(float64(maxTotalPixels-1000000) / float64(totalPixels))
				}
				// 长宽比过大时进一步缩小，避免模型处理超长图
				if aspectRatio > 140 {
					scale = math.Min(scale, 0.8)
				}

				quality := 85
				// 迭代压缩直到满足限制，确保最终一定能压缩到 10MB 以下
				for len(imageData) > maxVisionSize && (width > 100 || height > 100) {
					if scale < 1.0 {
						width = int(float64(width) * scale)
						height = int(float64(height) * scale)
					}
					// 每次循环都进行缩放，确保尺寸在减小
					if scale >= 1.0 {
						width = int(float64(width) * 0.9)
						height = int(float64(height) * 0.9)
					}

					img = imaging.Resize(img, width, height, imaging.Lanczos)
					totalPixels = int64(width) * int64(height)

					var buf bytes.Buffer
					err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality})
					if err != nil {
						log.Printf("Failed to encode image %s: %v", fileHeader.Filename, err)
						break
					}
					imageData = buf.Bytes()

					log.Printf("[AnalyzeProfileMultiFiles] Compression iteration: %dx%d, size: %d bytes, quality: %d",
						width, height, len(imageData), quality)

					// 如果还超过 10MB，继续降低质量或缩小尺寸
					if len(imageData) > maxVisionSize {
						if quality > 30 {
							// 优先降低质量，直到 30%
							quality -= 10
						} else {
							// 质量到 30% 后，大幅缩小尺寸
							scale = 0.7
						}
					}
				}
			}

			// 最终校验，如果仍然超过 10MB，使用最后的手段：大幅降低质量和尺寸
			if len(imageData) > maxVisionSize {
				log.Printf("[AnalyzeProfileMultiFiles] Image %s still too large after normal compression (%d bytes), applying aggressive compression", fileHeader.Filename, len(imageData))
				// 最后手段：质量降至 20%，尺寸减半
				for len(imageData) > maxVisionSize && (width > 50 || height > 50) {
					width = int(float64(width) * 0.7)
					height = int(float64(height) * 0.7)
					img = imaging.Resize(img, width, height, imaging.Lanczos)

					var buf bytes.Buffer
					err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 20})
					if err != nil {
						log.Printf("Failed to encode image %s in aggressive compression: %v", fileHeader.Filename, err)
						break
					}
					imageData = buf.Bytes()

					log.Printf("[AnalyzeProfileMultiFiles] Aggressive compression: %dx%d, size: %d bytes", width, height, len(imageData))
				}
			}

			// 最终校验，如果仍然超过 10MB，则跳过该图片
			if len(imageData) > maxVisionSize {
				log.Printf("[AnalyzeProfileMultiFiles] Image %s too large even after aggressive compression (%d bytes), skipping", fileHeader.Filename, len(imageData))
				continue
			}

			// 上传到临时目录
			objectKey := fmt.Sprintf("salesrag/analyze_tmp/%d/%d_%d_%s", userID, time.Now().Unix(), i, fileHeader.Filename)
			cosURL, err := util.UploadBytesToCOS(ctx, objectKey, "image/jpeg", imageData)
			if err != nil {
				log.Printf("Failed to upload image %s to COS: %v", fileHeader.Filename, err)
				continue
			}

			// 生成签名 URL (1小时有效)
			signedURL, _ := util.GenerateSignedURL(ctx, objectKey, 3600)
			if signedURL == "" {
				signedURL = cosURL // Fallback
			}

			contentParts = append(contentParts, map[string]interface{}{
				"type": "image_url",
				"image_url": map[string]string{
					"url": signedURL,
				},
			})
			log.Printf("Added image part: %s", fileHeader.Filename)

		} else {
			// 文档处理：解析文本
			// 这里需要重置 reader 因为 Parse 可能会读它
			// 实际上 fileHeader.Open() 每次返回新的 reader

			// 使用 parser 解析
			text, err := b.parser.Parse(ctx, src, fileHeader.Filename)
			if err != nil {
				log.Printf("Failed to parse document %s: %v", fileHeader.Filename, err)
				continue
			}

			// 截断过长文本 (例如每个文档 20k chars)
			if len(text) > 20000 {
				text = text[:20000] + "\n...(truncated)"
			}

			contentParts = append(contentParts, map[string]interface{}{
				"type": "text",
				"text": fmt.Sprintf("\n--- 文档 [%s] 内容 ---\n%s\n", fileHeader.Filename, text),
			})
			log.Printf("Added document part: %s (len: %d)", fileHeader.Filename, len(text))
		}
	}

	// 2. 添加最终提示词 (Unified Prompt)
	// 2. 添加最终提示词 (Unified Prompt)
	// 2. 添加最终提示词 (Unified Prompt)
	// 2. 添加最终提示词 (Unified Prompt)
	unifiedPrompt := `
### 角色定义
你是一位拥有20年B2B销售经验的**商业洞察专家**。你擅长通过零散的信息（无论是正式的招标文档、需求清单，还是非正式的聊天记录截图）拼凑出完整的客户全貌。

### 任务说明
用户将上传一组资料（可能是单一的类型，如图片，也可能是混合资料）。请分析这些材料，生成一份**客户画像分析报告**。

### 核心原则
1. **输入灵活性**：用户上传的资料可能不完整。对于无法从资料中获取的信息，请直接留空或标注“依据不足”，严禁臆造。
2. **术语准确性**：使用专业的销售术语（如：决策链、卡点、显性/隐性需求）。
3. **去伪存真**：能够识别客户的“烟雾弹”。例如：客户一直在谈价格（显性），可能真实原因是怕决策失误（隐性卡点）。

### 输出格式（Markdown）
请直接用 markdown 格式输出以下结构，不要有任何开场白或结束语或用别的格式包裹：

#### 客户背景
- **关键角色判断**：根据现有信息，判断客户的行业、赛道、公司规模、职位、决策链角色等。
- **业务场景聚焦**：客户所在的行业及当前关注的具体业务问题。

#### 需求与博弈分析
- **显性需求清单**：文档或沟通中明确提出的具体需求
- **隐形卡点挖掘**：阻碍项目推进的潜在因素，如：对方案稳定性的担忧、内部利益冲突、预算等

#### 关键信息
- **关键信息**：如果提供的信息中，除了以上内容以外，仍有非常重要且确定的关键信息，则可以补充在此处。如果没有则可以忽略。
`
	contentParts = append(contentParts, map[string]interface{}{
		"type": "text",
		"text": unifiedPrompt,
	})

	// 3. 调用多模态模型 (Doubao-Seed-1.8)
	messages := []map[string]interface{}{
		{
			"role":    "user",
			"content": contentParts,
		},
	}

	log.Printf("[AnalyzeProfileMultiFiles] Calling Volc StreamChatWithModel with %d parts", len(contentParts))
	// 使用 doubao-seed-1-8-251228
	// 开启 deepThinking (如果需要的话，或者设为 false) - 用户之前提到了 deep thinking，这里主要生成画像，暂时不开 deep thinking 以保证速度，或者根据 config。
	// 这里默认 false，因为画像生成通常不需要复杂的推理思考链，而是需要敏锐的提取。
	// 但 doubao-seed 本身支持 thinking，我们可以保留 deepThinking 参数入口，或者默认为 true/false。
	// 既然是"资深专家"，开启 thinking 可能更好，但考虑到速度，先 false。
	// 用户提到"调查doubao-seed...最大处理量"，并没有特别强调要 deep thinking，但为了质量，我们可以开启。
	// 还是先保持 false，因为 StreamChatWithModel 会根据 thinking 参数决定是否启用。
	// 让我们看 AnalyzeDocumentStream 原逻辑，它是 false。
	return b.volcBiz.StreamChatWithModel(ctx, messages, "doubao-seed-2-0-lite-260215", 0, 0.5, false, func(event string, token string) error {
		if event == "message" {
			return onToken(token)
		}
		return nil
	})
}

// analyzeImage 分析图片（微信聊天记录截图）
func (b *salesRAGBiz) analyzeImage(ctx context.Context, userID uint, file io.Reader, filename string) (string, error) {
	// 1. 读取图片数据
	imageData, err := io.ReadAll(file)
	if err != nil {
		return "", fmt.Errorf("读取图片失败: %w", err)
	}

	// 2. 检查图片属性以适配火山方舟模型规格 (Doubao-Seed-1-8)
	// 限制：大小 < 10MB, 总像素 < 36,000,000, 长宽比 < 150:1
	const maxVisionSize = 10 * 1024 * 1024 // 10MB
	const maxTotalPixels = 36000000        // 36MP

	// 解码图片以检查属性
	src, err := imaging.Decode(bytes.NewReader(imageData))
	if err != nil {
		return "", fmt.Errorf("解码图片失败: %w", err)
	}

	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	totalPixels := int64(width) * int64(height)
	aspectRatio := float64(height) / float64(width)
	if width > height {
		aspectRatio = float64(width) / float64(height)
	}

	if len(imageData) > maxVisionSize || totalPixels > maxTotalPixels || aspectRatio > 150 {
		log.Printf("[analyzeImage] Image needs processing (Size: %d, Pixels: %d, Ratio: %.2f)", len(imageData), totalPixels, aspectRatio)

		scale := 1.0
		if totalPixels > maxTotalPixels {
			scale = math.Sqrt(float64(maxTotalPixels-1000000) / float64(totalPixels))
		}
		if aspectRatio > 140 {
			scale = math.Min(scale, 0.8)
		}

		quality := 85
		for (len(imageData) > maxVisionSize || totalPixels > maxTotalPixels) && (width > 20 || height > 20) {
			if scale < 1.0 {
				width = int(float64(width) * scale)
				height = int(float64(height) * scale)
				src = imaging.Resize(src, width, height, imaging.Lanczos)
				totalPixels = int64(width) * int64(height)
			}

			var buf bytes.Buffer
			err = jpeg.Encode(&buf, src, &jpeg.Options{Quality: quality})
			if err != nil {
				return "", fmt.Errorf("压缩图片失败: %w", err)
			}
			imageData = buf.Bytes()

			log.Printf("[analyzeImage] Iterative processing: %dx%d, size: %d bytes, quality: %d", width, height, len(imageData), quality)

			if quality > 60 {
				quality -= 10
			} else {
				scale = 0.8
			}
		}
	}

	// 3. 最终校验
	if len(imageData) > maxVisionSize {
		return "", fmt.Errorf("图片过大且压缩失败 (当前大小: %d bytes)", len(imageData))
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

	// 5. 调用火山方舟视觉模型（doubao-seed-1-8-251228）
	const visionModel = "doubao-seed-1-8-251228"
	profileResult, err := b.volcBiz.VisionAnalyze(ctx, dataURL, combinedPrompt, visionModel, 0, "medium")
	if err != nil {
		return "", fmt.Errorf("视觉端到端分析失败 (火山方舟): %w", err)
	}

	log.Printf("画像端到端生成完成, 长度: %d", len(profileResult))

	return profileResult, nil
}

// analyzeImageStream 是 analyzeImage 的流式版本
func (b *salesRAGBiz) analyzeImageStream(ctx context.Context, userID uint, imageData []byte, onToken func(token string) error) (string, error) {
	log.Printf("[analyzeImageStream] Starting analysis for user %d, image size: %d bytes", userID, len(imageData))

	// 1. 检查图片规格以适配火山方舟模型 (Doubao-Seed-1-8)
	// 限制：文件 < 10MB, 总像素 < 36,000,000, 比例 < 150:1
	const maxVisionSize = 10 * 1024 * 1024 // 10MB
	const maxTotalPixels = 36000000        // 36MP

	// 解码图片以检查尺寸
	src, err := imaging.Decode(bytes.NewReader(imageData))
	if err != nil {
		log.Printf("[analyzeImageStream] Failed to decode image: %v", err)
		return "", fmt.Errorf("解码图片失败: %w", err)
	}

	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	totalPixels := int64(width) * int64(height)
	aspectRatio := float64(height) / float64(width)
	if width > height {
		aspectRatio = float64(width) / float64(height)
	}

	log.Printf("[analyzeImageStream] Image stats: %dx%d, size: %d bytes, pixels: %d, ratio: %.2f", width, height, len(imageData), totalPixels, aspectRatio)

	// 只要符合规格，不做任何处理，保留原始清晰度
	if len(imageData) > maxVisionSize || totalPixels > maxTotalPixels || aspectRatio > 150 {
		log.Printf("[analyzeImageStream] Image exceeds limits, starting smart compression...")

		scale := 1.0
		if totalPixels > maxTotalPixels {
			scale = math.Sqrt(float64(maxTotalPixels-1000000) / float64(totalPixels))
		}

		quality := 85
		for (len(imageData) > maxVisionSize || totalPixels > maxTotalPixels) && (width > 20 || height > 20) {
			if scale < 1.0 {
				width = int(float64(width) * scale)
				height = int(float64(height) * scale)
				src = imaging.Resize(src, width, height, imaging.Lanczos)
				totalPixels = int64(width) * int64(height)
			}

			var buf bytes.Buffer
			err = jpeg.Encode(&buf, src, &jpeg.Options{Quality: quality})
			if err != nil {
				return "", fmt.Errorf("流式压缩图片失败: %w", err)
			}
			imageData = buf.Bytes()

			log.Printf("[analyzeImageStream] Compressed to %dx%d, size: %d, quality: %d", width, height, len(imageData), quality)

			if quality > 60 {
				quality -= 10
			} else {
				scale = 0.8
			}
		}

		if len(imageData) > maxVisionSize {
			return "", fmt.Errorf("图片过大且流式压缩失败 (当前大小: %d bytes)", len(imageData))
		}
	}
	log.Printf("[analyzeImageStream] Resized image to %dx%d, new size: %d bytes", width, height, len(imageData))

	// 2. 准备数据传输 URL (优先使用 COS URL)
	var dataURL string
	ext := ".jpg" // 默认后缀
	objectKey := fmt.Sprintf("vision_tmp/%d/%d%s", userID, time.Now().UnixNano(), ext)

	// 尝试上传到 COS
	if b.ds != nil {
		cosURL, err := util.UploadBytesToCOS(ctx, objectKey, "image/jpeg", imageData)
		if err == nil && cosURL != "" {
			dataURL = cosURL
			log.Printf("[analyzeImageStream] Successfully uploaded to COS, using URL: %s", objectKey)
		} else {
			log.Printf("[analyzeImageStream] COS upload failed: %v", err)
		}
	}

	if dataURL == "" {
		// 回退到 base64 方式
		base64Image := base64.StdEncoding.EncodeToString(imageData)
		dataURL = fmt.Sprintf("data:image/jpeg;base64,%s", base64Image)
		log.Printf("[analyzeImageStream] Using base64 data URL (size: %d)", len(dataURL))
	}

	// 3. 构建提示词
	combinedPrompt := `你是一位顶尖的商业洞察专家，擅长从细微的社交互动中拆解客户画像。
这是一张【经典微信气泡对话式】聊天窗口的截图（可能是一张垂直拼接的长截图）。

请【完整扫描整张图片从上到下】的所有对话内容，并直接为我生成一份专业的客户画像。

### 核心视觉解析逻辑：
1. **场景确认**：请识别微信对话特有的气泡布局。
2. **重心偏移（客户为中心）**：
   - **左边气泡 = 客户方（核心分析对象）**：请深度挖掘此侧的所有表达。
   - **右边气泡 = 销售方（辅助理解）**：仅用于通过销售的提问或回复，来推断和修正对左侧客户真实意图的理解。
3. **内容过滤与提取规则：
   - **保留表情符号**：请务必捕捉文字气泡中的表情（如：[呲牙]、🌹、握手等），这些是判断客户性格、情绪温度的关键指纹。
   - **排除非文字消息类型**：即使图片消息中含有文字，也请视为“多媒体干扰”而直接忽略。

### 画像输出维度：
1. **客户基础标签**
2. **核心诉求与动机**
3. **性格指纹分析**
4. **决策倾向与销售阶段**
5. **高价值跟进建议**

### 约束要求：
- 使用具备商业厚度的 Markdown 格式。
- **直接输出画像内容**，不要有任何寒暄、前排分析或结论语。`

	// 5. 调用火山方舟视觉模型（doubao-seed-1-8-251228）
	const visionModel = "doubao-seed-1-8-251228"
	log.Printf("[analyzeImageStream] Calling Volc VisionAnalyzeStream with model: %s", visionModel)
	result, err := b.volcBiz.VisionAnalyzeStream(ctx, dataURL, combinedPrompt, visionModel, 0, "medium", onToken)

	if err != nil {
		log.Printf("[analyzeImageStream] Volc VisionAnalyzeStream error: %v", err)
		return "", err
	}

	log.Printf("[analyzeImageStream] Volc VisionAnalyzeStream completed, result length: %d", len(result))
	return result, nil
}

// AnalyzeDocumentStream 流式分析文档
func (b *salesRAGBiz) AnalyzeDocumentStream(ctx context.Context, userID uint, file io.Reader, filename string, onToken func(token string) error) (string, error) {
	// 读取内容
	data, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}

	ext := strings.ToLower(filepath.Ext(filename))
	if ext == ".jpg" || ext == ".jpeg" || ext == ".png" {
		return b.analyzeImageStream(ctx, userID, data, onToken)
	}

	// 文本文件使用 dmxapi 流式输出
	return b.analyzeDocumentStreamInternal(ctx, bytes.NewReader(data), filename, onToken)
}

// analyzeDocumentStreamInternal 分析文档（流式输出版本）
func (b *salesRAGBiz) analyzeDocumentStreamInternal(ctx context.Context, file io.Reader, filename string, onToken func(token string) error) (string, error) {
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
- 直接以 Markdown 列表输出干货内容，严禁任何开场白和提示语
- **重要**：不要用代码块包裹输出（如 ` + "```markdown" + ` 或 ` + "```" + `），直接输出纯 Markdown 文本`

	// 4. 调用火山方舟的 doubao-seed-1-8-251228 模型（流式输出）
	messages := []map[string]interface{}{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": "客户文档内容如下：\n\n" + content},
	}
	return b.volcBiz.StreamChatWithModel(ctx, messages, "doubao-seed-1-8-251228", 0, 0.5, true, func(event string, token string) error {
		if event == "message" {
			return onToken(token)
		}
		return nil
	})
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
- 直接以 Markdown 列表输出干货内容，严禁任何开场白和提示语
- **重要**：不要用代码块包裹输出（如 ` + "```markdown" + ` 或 ` + "```" + `），直接输出纯 Markdown 文本`

	// 4. 调用火山方舟的 doubao-seed-1-8-251228 模型
	messages := []map[string]interface{}{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": "客户文档内容如下：\n\n" + content},
	}
	return b.volcBiz.ChatWithModel(ctx, messages, "doubao-seed-1-8-251228", 0, 0.5)
}

// generateProfileFromChat 基于聊天记录生成客户画像
// 已弃用：目前的图片处理流程已改为在 analyzeImage 中一次性完成端到端生

// callDMXAPIStream 调用 dmxapi 的 qwen-turbo 模型（流式输出）
func (b *salesRAGBiz) callDMXAPIStream(ctx context.Context, systemPrompt, userMessage string, onToken func(token string) error) (string, error) {
	url := "https://www.dmxapi.cn/v1/chat/completions"
	apiKey := "sk-XgINDoE22MHQfcSZnToYICS4rNnoknIrXhZHZYs3VQM9DP25"
	model := "qwen-turbo"

	log.Printf("[callDMXAPIStream] Starting API call, model: %s, text length: %d", model, len(userMessage))

	payload := map[string]interface{}{
		"model": model,
		"messages": []map[string]interface{}{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userMessage},
		},
		"temperature": 0.5,
		"max_tokens":  2000,
		"stream":      true, // 启用流式输出
	}

	bodyBytes, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(bodyBytes))
	if err != nil {
		log.Printf("[callDMXAPIStream] Failed to create request: %v", err)
		return "", fmt.Errorf("create request failed: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", apiKey)

	// 流式响应不能使用 Client.Timeout（它覆盖整个请求生命周期包括 body 读取）
	// 使用包级别共享 Transport 复用连接池
	client := &http.Client{Transport: streamTransport}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[callDMXAPIStream] HTTP request failed: %v", err)
		return "", fmt.Errorf("DMXAPI request failed: %w", err)
	}
	defer resp.Body.Close()

	log.Printf("[callDMXAPIStream] Response status code: %d", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[callDMXAPIStream] Error response body: %s", string(body))
		return "", fmt.Errorf("DMXAPI error %d: %s", resp.StatusCode, string(body))
	}

	// 流式读取响应
	var fullContent strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	tokenCount := 0

	for scanner.Scan() {
		line := scanner.Text()

		// 跳过空行
		if line == "" {
			continue
		}

		// SSE 格式：data: {JSON}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		// 检查结束标记
		if data == "[DONE]" {
			log.Printf("[callDMXAPIStream] Received [DONE]")
			break
		}

		// 解析 JSON
		var streamData struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}

		if err := json.Unmarshal([]byte(data), &streamData); err != nil {
			log.Printf("[callDMXAPIStream] Failed to parse stream data: %v, data: %s", err, data)
			continue
		}

		// 提取 content
		if len(streamData.Choices) > 0 {
			content := streamData.Choices[0].Delta.Content
			if content != "" {
				tokenCount++
				fullContent.WriteString(content)

				// 调用回调函数
				if onToken != nil {
					if err := onToken(content); err != nil {
						log.Printf("[callDMXAPIStream] onToken error: %v", err)
						return fullContent.String(), err
					}
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[callDMXAPIStream] Scanner error: %v", err)
		return fullContent.String(), fmt.Errorf("read stream failed: %w", err)
	}

	log.Printf("[callDMXAPIStream] Success, tokens: %d, content length: %d", tokenCount, fullContent.Len())
	return fullContent.String(), nil
}

// callDMXAPI 调用 dmxapi 的 qwen-turbo 模型（用于文本分析）
func (b *salesRAGBiz) callDMXAPI(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	url := "https://www.dmxapi.cn/v1/chat/completions"
	apiKey := "sk-XgINDoE22MHQfcSZnToYICS4rNnoknIrXhZHZYs3VQM9DP25"
	model := "qwen-turbo"

	log.Printf("[callDMXAPI] Starting API call, model: %s, text length: %d", model, len(userMessage))

	payload := map[string]interface{}{
		"model": model,
		"messages": []map[string]interface{}{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userMessage},
		},
		"temperature": 0.5,
		"max_tokens":  2000,
	}

	bodyBytes, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(bodyBytes))
	if err != nil {
		log.Printf("[callDMXAPI] Failed to create request: %v", err)
		return "", fmt.Errorf("create request failed: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", apiKey)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[callDMXAPI] HTTP request failed: %v", err)
		return "", fmt.Errorf("DMXAPI request failed: %w", err)
	}
	defer resp.Body.Close()

	log.Printf("[callDMXAPI] Response status code: %d", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[callDMXAPI] Error response body: %s", string(body))
		return "", fmt.Errorf("DMXAPI error %d: %s", resp.StatusCode, string(body))
	}

	// 读取响应体
	bodyBytes, err = io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[callDMXAPI] Failed to read response body: %v", err)
		return "", fmt.Errorf("read response failed: %w", err)
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

	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		log.Printf("[callDMXAPI] Failed to decode JSON response: %v, body: %s", err, string(bodyBytes))
		return "", fmt.Errorf("decode response failed: %w", err)
	}

	if result.Error != nil {
		log.Printf("[callDMXAPI] API returned error: code=%s, message=%s", result.Error.Code, result.Error.Message)
		return "", fmt.Errorf("DMXAPI error: %s - %s", result.Error.Code, result.Error.Message)
	}

	if len(result.Choices) == 0 {
		log.Printf("[callDMXAPI] Empty choices in response: %s", string(bodyBytes))
		return "", fmt.Errorf("empty choices from DMXAPI")
	}

	log.Printf("[callDMXAPI] Success, content length: %d", len(result.Choices[0].Message.Content))
	return result.Choices[0].Message.Content, nil
}

// AnalyzeChatStyle 分析聊天风格（语言指纹分析）
// 图片使用阿里云 qwen3-vl-flash-2026-01-22，文本使用 dmxapi qwen-turbo
func (b *salesRAGBiz) AnalyzeChatStyle(ctx context.Context, userID uint, file io.Reader, filename string) (string, error) {
	log.Printf("[AnalyzeChatStyle] Starting analysis for user %d, filename: %s", userID, filename)

	// 检查文件类型，如果是图片则使用视觉模型
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == ".jpg" || ext == ".jpeg" || ext == ".png" {
		log.Printf("[AnalyzeChatStyle] Image file detected, using vision model for user %d", userID)

		// 读取图片数据
		data, err := io.ReadAll(file)
		if err != nil {
			log.Printf("[AnalyzeChatStyle] Failed to read image data for user %d: %v", userID, err)
			return "", fmt.Errorf("failed to read image: %w", err)
		}

		// 调用图片分析方法
		return b.analyzeChatStyleImage(ctx, userID, data)
	}

	// 1. 解析文本内容
	text, err := b.parser.Parse(ctx, file, filename)
	if err != nil {
		log.Printf("[AnalyzeChatStyle] Parse failed for user %d: %v", userID, err)
		return "", fmt.Errorf("failed to parse chat file: %w", err)
	}
	log.Printf("[AnalyzeChatStyle] Parsed text length: %d", len(text))

	// 2. 截断 (避免 token 溢出)
	maxLen := 10000
	if len(text) > maxLen {
		text = text[:maxLen] + "\n...(truncated)"
		log.Printf("[AnalyzeChatStyle] Text truncated to %d chars", maxLen)
	}

	// 检查文本是否为空
	if len(strings.TrimSpace(text)) == 0 {
		log.Printf("[AnalyzeChatStyle] Empty text after parsing for user %d", userID)
		return "", fmt.Errorf("文本内容为空，无法分析")
	}

	// 2. 构建系统提示词
	systemPrompt := `你是一个资深的文字风格分析专家。由于现在的场景是微信文字聊天，请根据提供的语料，提炼出该销售人员的【文字沟通指纹】，以便让 AI 能够精准模仿

## 核心要求：
1. **严禁使用或提及任何表情（Emoji/颜文字）**，分析 and 生成的风格必须完全基于纯文字
2. **纯文字复刻**：重点分析文字如何分词、如何分段、如何使用助词，确保回复感真实不生硬

## 提炼维度：
1. **社交人设与称谓习惯**：
   - 沟通角色：是"利落的办事员"、"温润的顾问"、"平等的伙伴"还是别的角色？
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

	// 4. 调用 dmxapi 的 qwen-turbo 模型
	log.Printf("[AnalyzeChatStyle] Calling DMX API for user %d", userID)
	analysis, err := b.callDMXAPI(ctx, systemPrompt, text)
	if err != nil {
		log.Printf("[AnalyzeChatStyle] DMX API call failed for user %d: %v", userID, err)
		return "", fmt.Errorf("AI 分析服务调用失败: %w", err)
	}
	log.Printf("[AnalyzeChatStyle] DMX API success, result length: %d", len(analysis))

	// 5. 保存到数据库
	style := &model.LanguageStyle{
		UserID: userID,
		Style:  analysis,
	}
	if err := b.ds.LanguageStyles().Save(ctx, style); err != nil {
		log.Printf("[AnalyzeChatStyle] Failed to save language style for user %d: %v", userID, err)
	} else {
		log.Printf("[AnalyzeChatStyle] Language style saved successfully for user %d", userID)
	}

	return analysis, nil
}

// AnalyzeChatStyleStream 流式分析聊天风格（语言指纹分析）
// 图片使用阿里云 qwen3-vl-flash-2026-01-22，文本使用 dmxapi qwen-turbo（均为流式）
func (b *salesRAGBiz) AnalyzeChatStyleStream(ctx context.Context, userID uint, file io.Reader, filename string, onToken func(token string) error) (string, error) {
	log.Printf("[AnalyzeChatStyleStream] Starting analysis for user %d, filename: %s", userID, filename)

	// 读取内容
	data, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}

	// 检查文件类型
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == ".jpg" || ext == ".jpeg" || ext == ".png" {
		log.Printf("[AnalyzeChatStyleStream] Image file detected, using vision stream for user %d", userID)
		return b.analyzeChatStyleImageStream(ctx, userID, data, onToken)
	}

	// 文本文件使用 dmxapi 流式输出
	log.Printf("[AnalyzeChatStyleStream] Text file detected, using text stream for user %d", userID)
	return b.analyzeChatStyleTextStream(ctx, userID, bytes.NewReader(data), filename, onToken)
}

// analyzeChatStyleTextStream 流式分析文本聊天记录的语言风格
func (b *salesRAGBiz) analyzeChatStyleTextStream(ctx context.Context, userID uint, file io.Reader, filename string, onToken func(token string) error) (string, error) {
	log.Printf("[analyzeChatStyleTextStream] Starting for user %d", userID)

	// 1. 解析文本内容
	text, err := b.parser.Parse(ctx, file, filename)
	if err != nil {
		log.Printf("[analyzeChatStyleTextStream] Parse failed for user %d: %v", userID, err)
		return "", fmt.Errorf("failed to parse chat file: %w", err)
	}
	log.Printf("[analyzeChatStyleTextStream] Parsed text length: %d", len(text))

	// 2. 截断 (避免 token 溢出)
	maxLen := 10000
	if len(text) > maxLen {
		text = text[:maxLen] + "\n...(truncated)"
		log.Printf("[analyzeChatStyleTextStream] Text truncated to %d chars", maxLen)
	}

	// 检查文本是否为空
	if len(strings.TrimSpace(text)) == 0 {
		log.Printf("[analyzeChatStyleTextStream] Empty text after parsing for user %d", userID)
		return "", fmt.Errorf("文本内容为空，无法分析")
	}

	// 3. 构建系统提示词
	systemPrompt := `你是一个资深的文字风格分析专家。由于现在的场景是微信文字聊天，请根据提供的语料，提炼出该销售人员的【文字沟通指纹】，以便让 AI 能够精准模仿

## 核心要求：
1. **严禁使用或提及任何表情（Emoji/颜文字）**，分析 and 生成的风格必须完全基于纯文字
2. **纯文字复刻**：重点分析文字如何分词、如何分段、如何使用助词，确保回复感真实不生硬

## 提炼维度：
1. **社交人设与称谓习惯**：
   - 沟通角色：是"利落的办事员"、"温润的顾问"、"平等的伙伴"还是别的角色？
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

	// 4. 调用火山方舟模型（流式输出）
	log.Printf("[analyzeChatStyleTextStream] Calling Volc StreamChatWithModel for user %d", userID)
	messages := []map[string]interface{}{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": text},
	}
	analysis, err := b.volcBiz.StreamChatWithModel(ctx, messages, "doubao-seed-1-8-251228", 0, 0.5, true, func(event string, token string) error {
		if event == "message" {
			return onToken(token)
		}
		return nil
	})
	if err != nil {
		log.Printf("[analyzeChatStyleTextStream] Volc StreamChat failed for user %d: %v", userID, err)
		return "", fmt.Errorf("AI 分析服务调用失败: %w", err)
	}
	log.Printf("[analyzeChatStyleTextStream] DMX API stream success, result length: %d", len(analysis))

	// 5. 保存到数据库
	style := &model.LanguageStyle{
		UserID: userID,
		Style:  analysis,
	}
	if err := b.ds.LanguageStyles().Save(ctx, style); err != nil {
		log.Printf("[analyzeChatStyleTextStream] Failed to save language style for user %d: %v", userID, err)
	} else {
		log.Printf("[analyzeChatStyleTextStream] Language style saved successfully for user %d", userID)
	}

	return analysis, nil
}

// analyzeChatStyleImageStream 流式分析聊天截图的语言风格
func (b *salesRAGBiz) analyzeChatStyleImageStream(ctx context.Context, userID uint, imageData []byte, onToken func(token string) error) (string, error) {
	log.Printf("[analyzeChatStyleImageStream] Starting analysis for user %d, image size: %d bytes", userID, len(imageData))

	// 1. 检查图片大小和尺寸，如果超过限制则压缩
	// 火山方舟 Doubao-Seed 模型限制：大小 < 10MB, 总像素 < 36,000,000, 长宽比 < 150:1
	const maxVisionSize = 10 * 1024 * 1024 // 10MB
	const maxTotalPixels int64 = 36000000  // 36MP

	// 解码图片以检查尺寸
	img, err := imaging.Decode(bytes.NewReader(imageData))
	if err != nil {
		log.Printf("[analyzeChatStyleImageStream] Failed to decode image: %v", err)
		return "", fmt.Errorf("解码图片失败: %w", err)
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	totalPixels := int64(width) * int64(height)
	aspectRatio := float64(height) / float64(width)
	if width > height {
		aspectRatio = float64(width) / float64(height)
	}

	log.Printf("[analyzeChatStyleImageStream] Original image: %dx%d, size: %d bytes, pixels: %d, ratio: %.2f",
		width, height, len(imageData), totalPixels, aspectRatio)

	// 检查是否需要压缩（大小、像素、长宽比任一超标）
	if len(imageData) > maxVisionSize || totalPixels > maxTotalPixels || aspectRatio > 150 {
		log.Printf("[analyzeChatStyleImageStream] Image needs compression")

		scale := 1.0
		if totalPixels > maxTotalPixels {
			scale = math.Sqrt(float64(maxTotalPixels-1000000) / float64(totalPixels))
		}
		if aspectRatio > 140 {
			scale = math.Min(scale, 0.8)
		}

		quality := 85
		for len(imageData) > maxVisionSize && (width > 100 || height > 100) {
			if scale < 1.0 {
				width = int(float64(width) * scale)
				height = int(float64(height) * scale)
			} else {
				width = int(float64(width) * 0.9)
				height = int(float64(height) * 0.9)
			}

			img = imaging.Resize(img, width, height, imaging.Lanczos)

			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
				return "", fmt.Errorf("压缩图片失败: %w", err)
			}
			imageData = buf.Bytes()

			log.Printf("[analyzeChatStyleImageStream] Compression iteration: %dx%d, size: %d bytes, quality: %d",
				width, height, len(imageData), quality)

			if len(imageData) > maxVisionSize {
				if quality > 30 {
					quality -= 10
				} else {
					scale = 0.7
				}
			}
		}

		// 激进压缩：质量降至 20%，尺寸持续缩小
		if len(imageData) > maxVisionSize {
			for len(imageData) > maxVisionSize && (width > 50 || height > 50) {
				width = int(float64(width) * 0.7)
				height = int(float64(height) * 0.7)
				img = imaging.Resize(img, width, height, imaging.Lanczos)

				var buf bytes.Buffer
				if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 20}); err != nil {
					return "", fmt.Errorf("激进压缩图片失败: %w", err)
				}
				imageData = buf.Bytes()

				log.Printf("[analyzeChatStyleImageStream] Aggressive compression: %dx%d, size: %d bytes", width, height, len(imageData))
			}
		}

		if len(imageData) > maxVisionSize {
			return "", fmt.Errorf("图片过大且压缩失败 (当前大小: %d bytes)", len(imageData))
		}

		log.Printf("[analyzeChatStyleImageStream] Compression done, final: %dx%d, %d bytes", width, height, len(imageData))
	}

	// 2. 上传图片到 COS 获取 URL
	var dataURL string
	ext := ".jpg"
	objectKey := fmt.Sprintf("chat_style/%d/%d%s", userID, time.Now().UnixNano(), ext)

	// 尝试上传到 COS
	if b.ds != nil {
		signedURL, err := util.UploadBytesToCOS(ctx, objectKey, "image/jpeg", imageData)
		if err == nil && signedURL != "" {
			dataURL = signedURL
			log.Printf("[analyzeChatStyleImageStream] Successfully uploaded to COS, using URL: %s", objectKey)
		} else {
			log.Printf("[analyzeChatStyleImageStream] COS upload failed: %v", err)
		}
	}

	if dataURL == "" {
		// 回退到 base64 方式
		base64Image := base64.StdEncoding.EncodeToString(imageData)
		dataURL = fmt.Sprintf("data:image/jpeg;base64,%s", base64Image)
		log.Printf("[analyzeChatStyleImageStream] Using base64 data URL (size: %d)", len(dataURL))
	}

	// 2. 构建提示词
	systemPrompt := `你是一个资深的文字风格分析专家。这是一张微信聊天截图，请从中提取销售人员的【文字沟通指纹】，以便让 AI 能够精准模仿。

## 核心要求：
1. **识别气泡布局**：
   - 右边绿色气泡 = 销售人员的消息（重点分析对象）
   - 左边白色/灰色气泡 = 客户消息（辅助理解）

2. **严禁使用或提及任何表情（Emoji/颜文字）**，分析和生成的风格必须完全基于纯文字

3. **只提取文字气泡**：忽略图片、语音、视频等多媒体消息

## 提炼维度：
1. **社交人设与称谓习惯**：
   - 沟通角色：是"利落的办事员"、"温润的顾问"、"平等的伙伴"还是别的角色？
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
- 字数控制在 500 字以内
- 只分析销售人员（右边绿色气泡）的语言风格`

	// 5. 调用火山方舟视觉模型（doubao-seed-2-0-lite-260215，与客户档案分析保持一致）
	const visionModel = "doubao-seed-2-0-lite-260215"
	result, err := b.volcBiz.VisionAnalyzeStream(ctx, dataURL, systemPrompt, visionModel, 0, "low", onToken)
	if err != nil {
		log.Printf("[analyzeChatStyleImageStream] Volc VisionAnalyzeStream error: %v", err)
		return "", fmt.Errorf("视觉模型分析失败: %w", err)
	}

	log.Printf("[analyzeChatStyleImageStream] QianwenVisionStream completed, result length: %d", len(result))

	// 3. 保存到数据库
	style := &model.LanguageStyle{
		UserID: userID,
		Style:  result,
	}
	if err := b.ds.LanguageStyles().Save(ctx, style); err != nil {
		log.Printf("[analyzeChatStyleImageStream] Failed to save language style for user %d: %v", userID, err)
	} else {
		log.Printf("[analyzeChatStyleImageStream] Language style saved successfully for user %d", userID)
	}

	return result, nil
}

// analyzeChatStyleImage 使用阿里云视觉模型分析聊天截图的语言风格
func (b *salesRAGBiz) analyzeChatStyleImage(ctx context.Context, userID uint, imageData []byte) (string, error) {
	log.Printf("[analyzeChatStyleImage] Starting analysis for user %d, image size: %d bytes", userID, len(imageData))

	// 1. 检查图片大小和尺寸，如果超过限制则压缩
	// 火山方舟 Doubao-Seed 模型限制：大小 < 10MB, 总像素 < 36,000,000, 长宽比 < 150:1
	const maxVisionSize = 10 * 1024 * 1024 // 10MB
	const maxTotalPixels int64 = 36000000  // 36MP

	// 解码图片以检查尺寸
	img, err := imaging.Decode(bytes.NewReader(imageData))
	if err != nil {
		log.Printf("[analyzeChatStyleImage] Failed to decode image: %v", err)
		return "", fmt.Errorf("解码图片失败: %w", err)
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	totalPixels := int64(width) * int64(height)
	aspectRatio := float64(height) / float64(width)
	if width > height {
		aspectRatio = float64(width) / float64(height)
	}

	log.Printf("[analyzeChatStyleImage] Original image: %dx%d, size: %d bytes, pixels: %d, ratio: %.2f",
		width, height, len(imageData), totalPixels, aspectRatio)

	// 检查是否需要压缩（大小、像素、长宽比任一超标）
	if len(imageData) > maxVisionSize || totalPixels > maxTotalPixels || aspectRatio > 150 {
		log.Printf("[analyzeChatStyleImage] Image needs compression")

		scale := 1.0
		if totalPixels > maxTotalPixels {
			scale = math.Sqrt(float64(maxTotalPixels-1000000) / float64(totalPixels))
		}
		if aspectRatio > 140 {
			scale = math.Min(scale, 0.8)
		}

		quality := 85
		for len(imageData) > maxVisionSize && (width > 100 || height > 100) {
			if scale < 1.0 {
				width = int(float64(width) * scale)
				height = int(float64(height) * scale)
			} else {
				width = int(float64(width) * 0.9)
				height = int(float64(height) * 0.9)
			}

			img = imaging.Resize(img, width, height, imaging.Lanczos)

			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
				return "", fmt.Errorf("压缩图片失败: %w", err)
			}
			imageData = buf.Bytes()

			log.Printf("[analyzeChatStyleImage] Compression iteration: %dx%d, size: %d bytes, quality: %d",
				width, height, len(imageData), quality)

			if len(imageData) > maxVisionSize {
				if quality > 30 {
					quality -= 10
				} else {
					scale = 0.7
				}
			}
		}

		// 激进压缩：质量降至 20%，尺寸持续缩小
		if len(imageData) > maxVisionSize {
			for len(imageData) > maxVisionSize && (width > 50 || height > 50) {
				width = int(float64(width) * 0.7)
				height = int(float64(height) * 0.7)
				img = imaging.Resize(img, width, height, imaging.Lanczos)

				var buf bytes.Buffer
				if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 20}); err != nil {
					return "", fmt.Errorf("激进压缩图片失败: %w", err)
				}
				imageData = buf.Bytes()

				log.Printf("[analyzeChatStyleImage] Aggressive compression: %dx%d, size: %d bytes", width, height, len(imageData))
			}
		}

		if len(imageData) > maxVisionSize {
			return "", fmt.Errorf("图片过大且压缩失败 (当前大小: %d bytes)", len(imageData))
		}

		log.Printf("[analyzeChatStyleImage] Compression done, final: %dx%d, %d bytes", width, height, len(imageData))
	}

	// 2. 上传图片到 COS 获取 URL
	var dataURL string
	ext := ".jpg"
	objectKey := fmt.Sprintf("chat_style/%d/%d%s", userID, time.Now().UnixNano(), ext)

	// 尝试上传到 COS
	if b.ds != nil {
		signedURL, err := util.UploadBytesToCOS(ctx, objectKey, "image/jpeg", imageData)
		if err == nil && signedURL != "" {
			dataURL = signedURL
			log.Printf("[analyzeChatStyleImage] Successfully uploaded to COS, using URL: %s", objectKey)
		} else {
			log.Printf("[analyzeChatStyleImage] COS upload failed: %v", err)
		}
	}

	if dataURL == "" {
		// 回退到 base64 方式
		base64Image := base64.StdEncoding.EncodeToString(imageData)
		dataURL = fmt.Sprintf("data:image/jpeg;base64,%s", base64Image)
		log.Printf("[analyzeChatStyleImage] Using base64 data URL (size: %d)", len(dataURL))
	}

	// 2. 构建提示词
	systemPrompt := `你是一个资深的文字风格分析专家。这是一张微信聊天截图，请从中提取销售人员的【文字沟通指纹】，以便让 AI 能够精准模仿。

## 核心要求：
1. **识别气泡布局**：
   - 右边绿色气泡 = 销售人员的消息（重点分析对象）
   - 左边白色/灰色气泡 = 客户消息（辅助理解）

2. **严禁使用或提及任何表情（Emoji/颜文字）**，分析和生成的风格必须完全基于纯文字

3. **只提取文字气泡**：忽略图片、语音、视频等多媒体消息

## 提炼维度：
1. **社交人设与称谓习惯**：
   - 沟通角色：是"利落的办事员"、"温润的顾问"、"平等的伙伴"还是别的角色？
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
- 字数控制在 500 字以内
- 只分析销售人员（右边绿色气泡）的语言风格`

	const visionModel = "doubao-seed-2-0-lite-260215"

	log.Printf("[analyzeChatStyleImage] Calling Volc VisionAnalyze with model: %s", visionModel)
	result, err := b.volcBiz.VisionAnalyze(ctx, dataURL, systemPrompt, visionModel, 0, "low")

	if err != nil {
		log.Printf("[analyzeChatStyleImage] Volc VisionAnalyze error: %v", err)
		return "", fmt.Errorf("视觉模型分析失败: %w", err)
	}

	log.Printf("[analyzeChatStyleImage] QianwenVision completed, result length: %d", len(result))

	// 3. 保存到数据库
	style := &model.LanguageStyle{
		UserID: userID,
		Style:  result,
	}
	if err := b.ds.LanguageStyles().Save(ctx, style); err != nil {
		log.Printf("[analyzeChatStyleImage] Failed to save language style for user %d: %v", userID, err)
	} else {
		log.Printf("[analyzeChatStyleImage] Language style saved successfully for user %d", userID)
	}

	return result, nil
}

// GetLanguageStyle 获取用户的语言风格
func (b *salesRAGBiz) GetLanguageStyle(ctx context.Context, userID uint) (string, error) {
	style, err := b.ds.LanguageStyles().Get(ctx, userID)
	if err != nil {
		return "", nil // Not found
	}
	return style.Style, nil
}

// SaveLanguageStyle 保存用户的语言风格
func (b *salesRAGBiz) SaveLanguageStyle(ctx context.Context, userID uint, styleContent string) error {
	log.Printf("[SaveLanguageStyle] Saving for user %d, content length: %d", userID, len(styleContent))

	style := &model.LanguageStyle{
		UserID: userID,
		Style:  styleContent,
	}

	if err := b.ds.LanguageStyles().Save(ctx, style); err != nil {
		log.Printf("[SaveLanguageStyle] Failed to save for user %d: %v", userID, err)
		return fmt.Errorf("保存语言风格失败: %w", err)
	}

	log.Printf("[SaveLanguageStyle] Successfully saved for user %d", userID)
	return nil
}

// OCRAnalyze 识别图片中的文本（压缩 + 上传 COS + 调用视觉大模型）
func (b *salesRAGBiz) OCRAnalyze(ctx context.Context, userID uint, imageData []byte, contentType string, sessionID string, filename string) (string, string, error) {
	// 1. 图片压缩（与客户档案分析保持一致）
	// 火山方舟 Doubao-Seed 模型限制：大小 < 10MB, 总像素 < 36,000,000, 长宽比 < 150:1
	const maxVisionSize = 10 * 1024 * 1024 // 10MB
	const maxTotalPixels int64 = 36000000  // 36MP

	img, err := imaging.Decode(bytes.NewReader(imageData))
	if err != nil {
		log.Printf("[OCRAnalyze] Failed to decode image for compression check, user_id: %d, error: %v", userID, err)
		// 解码失败时仍然尝试上传原图
	} else {
		bounds := img.Bounds()
		width, height := bounds.Dx(), bounds.Dy()
		totalPixels := int64(width) * int64(height)
		aspectRatio := float64(height) / float64(width)
		if width > height {
			aspectRatio = float64(width) / float64(height)
		}

		if len(imageData) > maxVisionSize || totalPixels > maxTotalPixels || aspectRatio > 150 {
			log.Printf("[OCRAnalyze] Image needs compression, user_id: %d, size: %d, pixels: %d, ratio: %.2f",
				userID, len(imageData), totalPixels, aspectRatio)

			scale := 1.0
			if totalPixels > maxTotalPixels {
				scale = math.Sqrt(float64(maxTotalPixels-1000000) / float64(totalPixels))
			}
			if aspectRatio > 140 {
				scale = math.Min(scale, 0.8)
			}

			quality := 85
			for len(imageData) > maxVisionSize && (width > 100 || height > 100) {
				if scale < 1.0 {
					width = int(float64(width) * scale)
					height = int(float64(height) * scale)
				} else {
					width = int(float64(width) * 0.9)
					height = int(float64(height) * 0.9)
				}

				img = imaging.Resize(img, width, height, imaging.Lanczos)

				var buf bytes.Buffer
				if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
					log.Printf("[OCRAnalyze] Failed to compress image: %v", err)
					break
				}
				imageData = buf.Bytes()

				if len(imageData) > maxVisionSize {
					if quality > 30 {
						quality -= 10
					} else {
						scale = 0.7
					}
				}
			}

			// 激进压缩：质量降至 20%，尺寸持续缩小
			if len(imageData) > maxVisionSize {
				for len(imageData) > maxVisionSize && (width > 50 || height > 50) {
					width = int(float64(width) * 0.7)
					height = int(float64(height) * 0.7)
					img = imaging.Resize(img, width, height, imaging.Lanczos)

					var buf bytes.Buffer
					if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 20}); err != nil {
						break
					}
					imageData = buf.Bytes()
				}
			}

			if len(imageData) > maxVisionSize {
				return "", "", fmt.Errorf("图片过大且压缩失败，请上传更小的图片")
			}

			contentType = "image/jpeg"
			log.Printf("[OCRAnalyze] Compression done, user_id: %d, final_size: %d, dimensions: %dx%d",
				userID, len(imageData), width, height)
		}
	}

	// 2. 上传到 COS
	objectKey := fmt.Sprintf("sales_chat/%d/%s/%d_%s", userID, sessionID, time.Now().Unix(), filename)

	cosURL, err := util.UploadBytesToCOS(ctx, objectKey, contentType, imageData)
	if err != nil {
		log.Printf("[OCRAnalyze] Upload image to COS failed, user_id: %d, key: %s, error: %v", userID, objectKey, err)
		return "", "", fmt.Errorf("图片存储失败: %w", err)
	}

	// 3. 调用火山引擎 Doubao 视觉模型进行 OCR 识别
	prompt := `你是一个专业的微信聊天记录识别专家。请识别这张微信聊天截图中的对话内容。

  ## 识别要求

  ### 1. 气泡布局识别
  - **左边气泡（白色/灰色）= 客户消息**
  - **右边气泡（绿色）= 销售消息**
  - 从上到下完整扫描所有对话气泡

  ### 2. 内容提取规则
  - **保留表情符号**：如 [微笑]、[呲牙]、🌹 等
  - **只提取文字气泡**：忽略图片、语音、视频等多媒体消息
  - **保持原文**：不要修改、总结或解释对话内容

  ### 3. 输出格式（严格遵守）

  **如果截图包含多轮对话**（2条及以上消息），按以下格式输出：

  【对话历史】
  客户：[第1条客户消息]
  销售：[第1条销售消息]
  客户：[第2条客户消息]
  ...（所有历史对话）

  【客户最新消息】
  客户：[最后一条客户消息]

  **如果截图只有单条消息**，直接输出：

  客户：[消息内容]

  ### 4. 特殊处理规则

  - **最新消息必须是客户发的**：如果截图最后一条是销售发的，往前找到最近的客户消息作为"最新消息"
  - **空消息处理**：如果气泡只有表情没有文字，保留表情符号
  - **时间戳忽略**：不要提取对话中的时间信息

  ## 输出示例

  ### 示例1：多轮对话
  【对话历史】
  客户：你们这个产品怎么样？
  销售：我们的产品在行业内评价很高，已经服务了1000+客户
  客户：价格呢？
  销售：我们有三种套餐，基础版998元...

  【客户最新消息】
  客户：太贵了，能便宜点吗？

  ### 示例2：单条消息
  客户：在吗？

  ### 示例3：包含表情
  【对话历史】
  客户：你好[微笑]
  销售：您好，很高兴为您服务

  【客户最新消息】
  客户：我想了解一下产品

  ## 关键约束

  1. **严格使用【对话历史】和【客户最新消息】标记**，不要使用其他标记
  2. **每条消息前必须有"客户："或"销售："前缀**
  3. **不要添加任何分析、解释或总结**，只输出识别的对话内容
  4. **保持对话的原始顺序和完整性**

  现在请识别这张截图。`
	visionModel := "doubao-seed-2-0-lite-260215"

	// 生成 10 分钟有效的签名 URL 供 API 访问
	signedURL, err := util.GenerateSignedURL(ctx, objectKey, 600)
	if err != nil {
		log.Printf("[OCRAnalyze] Generate signed URL failed, use raw cosURL, error: %v, key: %s", err, objectKey)
		signedURL = cosURL
	}

	// 调用火山引擎视觉模型
	ocrText, err := b.volcBiz.VisionAnalyze(ctx, signedURL, prompt, visionModel, 0, "minimal")
	if err != nil {
		log.Printf("[OCRAnalyze] Volc Engine Vision OCR failed, user_id: %d, url: %s, error: %v", userID, signedURL, err)
		return "", "", fmt.Errorf("图片识别失败，请检查模型配置: %w", err)
	}

	return ocrText, cosURL, nil
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
