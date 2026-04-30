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
	"math"
	"mime/multipart"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/biz/salesrag/domain"
	"numind-server/internal/numind/biz/salesrag/service"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	aismw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/billing"
	cb "numind-server/internal/pkg/contextbudget"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/pricing"
	"numind-server/internal/pkg/util"

	"numind-server/internal/numind/biz/ali"
	"numind-server/internal/numind/biz/baidu"

	"github.com/disintegration/imaging"
	"gorm.io/gorm"
)

// 观点库业务限制常量：系统赛道 + 自定义赛道合计上限
const maxOpinionTotal = 2

// 文档分类常量
const (
	catProduct = "产品文档"
	catCase    = "成功案例"
	catFAQ     = "百问百答"
	catOther   = "其他相关文档"
)

// analyzeProfileSystemPrompt 客户档案分析的 system prompt（AnalyzeProfileMultiFiles 和 AnalyzeProfileText 共用）
const analyzeProfileSystemPrompt = `你是一位拥有20年B2B销售经验的商业洞察专家。你擅长通过零散的信息（无论是正式的招标文档、需求清单，还是非正式的聊天记录截图）拼凑出完整的客户全貌。

## 核心原则

1. **事实优先，严禁臆造**：所有结论必须有素材中的原文或截图内容作为依据。对于无法从资料中获取的信息，直接留空或标注"依据不足"。宁可少写，也不编造。
2. **区分事实与推断**：直接引用素材的信息标记为事实；需要推理的信息必须标注推理依据（如"根据对话中提到XX，推断..."）。
3. **术语准确**：使用专业销售术语（决策链、卡点、显性/隐性需求等）。
4. **识别烟雾弹**：客户表面诉求未必是真实诉求。例如客户反复谈价格（显性），真实原因可能是怕决策失误（隐性卡点）。但这类推断必须注明依据。

## 分析步骤

在输出报告之前，请先完成以下内部分析（不需要输出分析过程）：
1. 识别素材中出现的所有人物角色及其关系（谁是客户、谁是销售、谁是决策者等）
2. 梳理事件或需求的时间线（如有）
3. 提取所有明确的事实信息（客户直接说的话、文档中的数据）
4. 基于事实进行有限度的推断（必须有依据支撑）
5. 按下方模板组织输出

## 输出格式

请直接用 Markdown 格式输出以下结构，不要有任何开场白或结束语：

#### 客户背景
- **行业/赛道**：（依据不足则留空）
- **公司规模**：（依据不足则留空）
- **关键角色**：判断对方在决策链中的角色（决策者/影响者/使用者），附判断依据
- **业务场景**：客户当前关注的具体业务问题

#### 需求分析
- **显性需求**：文档或沟通中明确提出的具体需求（逐条列出）
- **隐性卡点**：可推断的阻碍因素（每条必须附推理依据，格式："现象 → 推断 → 依据"）

#### 竞争与预算线索
- **竞品线索**：素材中提到或暗示的竞品、替代方案（依据不足则标注"未提及"）
- **预算信号**：价格敏感度、预算区间等线索（依据不足则标注"未提及"）

#### 关键信息摘要
- 不属于以上类别、但确定且重要的信息（如果没有则省略此板块）`

// fetchProfilePrompt 从 Langfuse 获取客户画像分析 prompt，fallback 到硬编码
func fetchProfilePrompt() string {
	p, _ := langfuse.FetchPrompt("salesrag-profile-analysis", analyzeProfileSystemPrompt)
	return p
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
	// RetrieveStream 流式检索并生成回复
	// retrievalQuery: 用于知识库检索的查询（含OCR文字）
	// promptQuery: 用户原始文字（不含OCR，OCR文字通过 ocrTexts 参数在构建 prompt 时追加）
	// ocrTexts: 图片OCR识别的文字，会追加到发给模型的 user message 中
	RetrieveStream(ctx context.Context, retrievalQuery string, promptQuery string, ocrTexts []string, history []string, docIDs []uint, opinionDocIDs []uint, docCategoryMap map[uint]string, deepThinking bool, chatMode string, customerProfile string, salesStage string, onEvent func(eventType string, data interface{}) error) error
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
	// ocrTexts: 图片OCR识别文字，仅用于知识库检索，不进AI prompt
	ChatWithSession(ctx context.Context, userID uint, sessionID uint, query string, ocrTexts []string, images []string, docIDs []uint, deepThinking bool, chatMode string, onEvent func(eventType string, data interface{}) error) error

	// AnalyzeProfileMultiFiles 多文件综合分析生成客户档案
	AnalyzeProfileMultiFiles(ctx context.Context, userID uint, files []*multipart.FileHeader, onToken func(token string) error) (string, error)

	// AnalyzeProfileText 纯文本分析生成客户档案
	AnalyzeProfileText(ctx context.Context, userID uint, text string, onToken func(token string) error) (string, error)

	// AnalyzeChatStyleStream 流式分析聊天风格（语言指纹分析）
	AnalyzeChatStyleStream(ctx context.Context, userID uint, chatData io.Reader, filename string, onToken func(token string) error) (string, error)

	// GetLanguageStyle 获取用户的语言风格
	GetLanguageStyle(ctx context.Context, userID uint) (string, error)
	// SaveLanguageStyle 保存用户的语言风格
	SaveLanguageStyle(ctx context.Context, userID uint, style string) error

	// OCRAnalyze 识别图片中的文本
	// engine: "baidu"（百度光学OCR，默认）或 "vision"（火山视觉大模型）
	OCRAnalyze(ctx context.Context, userID uint, imageData []byte, contentType string, sessionID string, filename string, engine string) (ocrText string, cosURL string, err error)

	// ListOpinionTracks 获取系统内置观点赛道列表
	ListOpinionTracks(ctx context.Context) ([]model.OpinionTrack, error)

	// SubmitFeedback 提交消息反馈（点赞/点踩），同步推送到 Langfuse
	SubmitFeedback(ctx context.Context, userID, sessionID, messageID uint, rating int, comment string) error
	// GetFeedback 获取消息反馈
	GetFeedback(ctx context.Context, userID, sessionID, messageID uint) (*model.SalesMessageFeedback, error)
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

	// Credits-system wiring (Phase 2 Task 2.2). When creditSvc is nil the biz
	// silently skips credit deduction (backward-compat for call sites that
	// haven't been rewired yet, e.g. opinion track seeder + legacy tests).
	// Once biz.go is updated to pass the new deps, every user-triggered Chat
	// flows through Reserve → LLM → Reconcile/Refund.
	creditSvc       credit.ICreditService
	pricing         pricing.ICalculator
	registry        registry.Registry // resolves task_profile.salesrag.chat → real provider+model for CheckAndEstimate; nil-safe (test fixtures construct &salesRAGBiz{} without it)
	defaultModel    string            // fallback model tag for R2 estimation (empty → global coef)
	defaultProvider string            // fallback provider tag (empty → global coef)
}

// VolcBiz 火山引擎服务接口（避免循环依赖）
type VolcBiz interface {
	VolcTextStream(ctx context.Context, messages []map[string]string, maxTokens int, temperature float64) (string, *billing.TokenUsage, error)
	// StreamChat 真正的流式聊天，通过回调函数逐 token 或思维链内容推送
	StreamChat(ctx context.Context, messages []map[string]interface{}, maxTokens int, temperature float64, deepThinking bool, onEvent func(event string, token string) error) (string, *billing.TokenUsage, error)
	// VisionAnalyze 调用火山方舟视觉模型分析图片
	VisionAnalyze(ctx context.Context, imageURL string, prompt string, model string, maxTokens int, reasoningEffort string) (string, *billing.TokenUsage, error)
	// VisionAnalyzeStream 流式分析图片
	VisionAnalyzeStream(ctx context.Context, imageURL string, prompt string, model string, maxTokens int, reasoningEffort string, onToken func(token string) error) (string, *billing.TokenUsage, error)
	// ChatWithModel 非流式聊天
	ChatWithModel(ctx context.Context, messages []map[string]interface{}, model string, maxTokens int, temperature float64) (string, *billing.TokenUsage, error)
	// StreamChatWithModel 流式聊天，支持指定模型和思考程度
	StreamChatWithModel(ctx context.Context, messages []map[string]interface{}, model string, maxTokens int, temperature float64, reasoningEffort string, onEvent func(event string, token string) error) (string, *billing.TokenUsage, error)
}

func NewSalesRAGBiz(ds store.IStore, pipeline *service.IngestionPipeline, rag *service.SalesRAGService, volc VolcBiz, ali ali.AliBiz, sessionStore store.SalesSessionStore, parser service.PipelineParser) SalesRAGBiz {
	return NewSalesRAGBizWithCredits(ds, pipeline, rag, volc, ali, sessionStore, parser, nil, nil, nil)
}

// NewSalesRAGBizWithCredits constructs a SalesRAGBiz with the credits-system
// deps injected (Phase 2 Task 2.2). When either creditSvc or pc is nil, the
// biz falls back to the legacy no-op behaviour so callers that haven't been
// updated still compile and run. Production wiring in biz.go passes both.
func NewSalesRAGBizWithCredits(
	ds store.IStore,
	pipeline *service.IngestionPipeline,
	rag *service.SalesRAGService,
	volc VolcBiz,
	ali ali.AliBiz,
	sessionStore store.SalesSessionStore,
	parser service.PipelineParser,
	creditSvc credit.ICreditService,
	pc pricing.ICalculator,
	reg registry.Registry,
) SalesRAGBiz {
	return &salesRAGBiz{
		ds:                ds,
		ingestionPipeline: pipeline,
		ragSvc:            rag,
		volcBiz:           volc,
		aliBiz:            ali,
		sessionStore:      sessionStore,
		parser:            parser,
		creditSvc:         creditSvc,
		pricing:           pc,
		registry:          reg,
		// defaultModel/defaultProvider stay empty: when registry is nil
		// (test fixtures) acquireSalesragCredits falls back to passing them,
		// which hits the ('llm_chat','','') global pricing row when seeded.
	}
}

// wrapCreditError maps ICreditService denial errors onto the errno domain
// error HTTP 402 (Credits.Insufficient). legacy_tier users carry the Chinese
// `Reason` string from CanRunSOP; credits users get a default zh message.
// Non-credit errors bubble through unchanged so the caller sees the original
// failure (DB error, coefficient missing, etc.).
func (b *salesRAGBiz) wrapCreditError(err error, pre *credit.PreCheckResult) error {
	if err == nil {
		return nil
	}
	if !errors.Is(err, credit.ErrInsufficientCredits) {
		return err
	}
	if pre != nil && pre.Reason != "" {
		return errno.ErrInsufficientCredits.SetMessage("%s", pre.Reason)
	}
	return errno.ErrInsufficientCredits
}

// salesragCreditContext bundles the per-chat-call credit state so the
// ChatWithSession wrapper can emit the full Reserve → LLM → Reconcile/Refund
// pipeline while keeping the LLM-call body uncluttered.
//
// Lifecycle:
//  1. acquireSalesragCredits() — runs CheckAndEstimate + Reserve; returns
//     either (ctx, cc, nil) on success or (ctx, nil, err) on denial/failure.
//  2. caller invokes LLM stream, captures usage + streamErr.
//  3. caller invokes cc.recordLLMResult(usage, streamErr) which computes
//     actualCost.
//  4. deferred cc.finalize(ctx) runs Reconcile/Refund with the outcome.
//
// When creditSvc is nil, acquireSalesragCredits returns (ctx, nil, nil) and
// the caller treats that as a no-op (legacy behaviour).
type salesragCreditContext struct {
	biz        *salesRAGBiz
	rsv        *credit.Reservation
	pre        *credit.PreCheckResult
	actualCost int64
	opErr      error
}

// acquireSalesragCredits performs the CheckAndEstimate pre-flight check for a
// salesrag_chat call. promptChars is caller-provided. The sessionID parameter
// is reserved for future scope-aware billing and is currently unused.
//
// P1 fix (Task 10 spec compliance): RetrieveStream now always populates
// ContextFragments, which means the Gateway middleware (ContextBudgetCredits)
// runs doReserveBudget for credits-mode users. To prevent double-reservation,
// this function performs CheckAndEstimate (early balance denial) but skips the
// inline Reserve for credits users — the Gateway middleware owns the Reserve,
// Reconcile, and Refund lifecycle for that path.
//
// Legacy-tier users are unaffected: SkipDeduction=true means no Reserve
// happens either way, but CheckAndEstimate still runs CanRunSOP() gating.
//
// The caller still calls `defer cc.finalize(ctx)` — for legacy and test paths
// where cc.rsv is nil, finalize is a safe no-op.
func (b *salesRAGBiz) acquireSalesragCredits(
	ctx context.Context, userID uint, _ uint, promptChars int,
) (*salesragCreditContext, error) {
	if b.creditSvc == nil {
		return nil, nil
	}
	user, err := b.ds.Users().GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("acquireSalesragCredits: load user: %w", err)
	}
	provider, modelName := b.resolveSalesragChatRoute(ctx)
	pre, err := b.creditSvc.CheckAndEstimate(ctx, user, credit.OpSalesragChat, credit.EstimationInput{
		PromptChars: promptChars,
		Model:       modelName,
		Provider:    provider,
	})
	if err != nil {
		return &salesragCreditContext{biz: b, pre: pre}, b.wrapCreditError(err, pre)
	}
	cc := &salesragCreditContext{biz: b, pre: pre}
	if pre.SkipDeduction {
		// legacy_tier path: no Reserve, defer-finalize is a no-op.
		return cc, nil
	}
	// credits-mode path: Reserve is delegated to the Gateway middleware
	// (ContextBudgetCredits → doReserveBudget) which fires when
	// ContextFragments is non-empty (always the case since Task 10).
	// Returning cc with rsv=nil means cc.finalize is a no-op here; the
	// middleware's Finalize handles Reconcile/Refund after the stream drains.
	return cc, nil
}

// resolveSalesragChatRoute looks up the current task_profile.salesrag.chat
// binding so CheckAndEstimate hits the precise pricing rule (e.g.
// aihubmix/deepseek-v3.2-thinking @ ¥2.16/¥3.24) instead of the
// ('llm_chat',”,”) global fallback row — which is missing on dev DB and
// caused "pricing lookup: record not found" 500s.
//
// Failures (registry nil, task unbound, network) fall back to ("","") so
// the lookup degrades to whatever per-provider/global rows exist; the
// callee surfaces ErrRecordNotFound only when no rule matches at all.
func (b *salesRAGBiz) resolveSalesragChatRoute(ctx context.Context) (provider, modelName string) {
	if b.registry == nil {
		return "", ""
	}
	route, _, err := b.registry.ResolveTask(ctx, profile.SalesragChat)
	if err != nil || route == nil {
		log.Printf("[salesRAGBiz] resolveSalesragChatRoute(%s) fallback to empty: %v", profile.SalesragChat, err)
		return "", ""
	}
	return route.Provider.Name, route.ServiceKey
}

// recordLLMResult mutates the credit context with the observed actual cost
// (pricing.CalculateCost over the streamed token counts) and any stream
// error. Called once per chat after the LLM stream drains. Safe to call
// even when cc == nil (no-op for legacy wiring).
func (cc *salesragCreditContext) recordLLMResult(
	ctx context.Context, streamErr error,
	provider, modelName string,
	promptTokens, completionTokens int,
) {
	if cc == nil || cc.rsv == nil {
		return
	}
	if streamErr != nil {
		cc.opErr = streamErr
		return
	}
	if promptTokens <= 0 || cc.biz.pricing == nil {
		// Leave actualCost=0 → defer path Refund(no_actual_cost).
		return
	}
	if provider == "" {
		provider = cc.biz.defaultProvider
	}
	if modelName == "" {
		modelName = cc.biz.defaultModel
	}
	cost, err := cc.biz.pricing.CalculateCost(ctx, "llm_chat",
		provider, modelName, promptTokens, completionTokens)
	if err != nil {
		log.Printf("[salesragCreditContext] pricing.CalculateCost failed provider=%s model=%s: %v",
			provider, modelName, err)
		// actualCost stays 0 → Refund(no_actual_cost).
		return
	}
	cc.actualCost = cost
	// 把真实 token 数写到 rsv，FinalizeReservation 会透传到
	// credit-reconcile span metadata（spec §5.1.3）。
	cc.rsv.ActualPromptTokens = promptTokens
	cc.rsv.ActualCompletionTokens = completionTokens
}

// finalize runs ICreditService.FinalizeReservation with a detached context
// (context.WithoutCancel) so the refund/reconcile DB writes complete even
// when the caller's ctx has already been cancelled (client disconnect).
// Safe to call with cc == nil.
func (cc *salesragCreditContext) finalize(ctx context.Context) {
	if cc == nil || cc.rsv == nil {
		return
	}
	detached := context.WithoutCancel(ctx)
	_ = cc.biz.creditSvc.FinalizeReservation(detached, cc.rsv, &cc.actualCost, &cc.opErr)
}

func (b *salesRAGBiz) Ingest(ctx context.Context, userID uint, filename string, displayName string, reader io.Reader, opts IngestOptions) (uint, error) {
	// 注入计费上下文（覆盖 Embedding、VectorDB 等下游调用）
	ctx = billing.WithBilling(ctx, userID, "salesrag_ingest")

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

	// 记录 COS 上传用量
	billing.RecordCOS(userID, "salesrag_ingest_upload", int64(len(data)),
		billing.Metadata("object_key", objectKey, "filename", filename))

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
	if uid, ok := middleware.UserIDFromCtx(ctx); ok {
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
		if uid, ok := middleware.UserIDFromCtx(ctx); ok {
			ctx = billing.WithBilling(ctx, uid, "salesrag_generate_answer")
		}
		result, _, err := b.volcBiz.VolcTextStream(ctx, messages, 1000, 0.7)
		return result, err
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

	if uid, ok := middleware.UserIDFromCtx(ctx); ok {
		ctx = billing.WithBilling(ctx, uid, "salesrag_generate_answer")
	}
	result, _, err := b.volcBiz.VolcTextStream(ctx, messages, 1000, 0.7)
	return result, err
}

// scoreToImportance maps a rerank score in [0.0, 1.0] to an integer importance
// in [0, 10] for use in ContextFragment.Importance. A score of 1.0 maps to 10;
// 0.0 maps to 0. Values outside [0.0, 1.0] are clamped.
func scoreToImportance(score float32) int {
	if score <= 0 {
		return 0
	}
	if score >= 1.0 {
		return 10
	}
	return int(score * 10)
}

// buildSalesRAGEvidenceFragments converts a slice of retrieved knowledge chunks
// into ordered ContextFragment values for the context-budget middleware.
//
// Each chunk becomes a RoleEvidence + SourceKB + CompressReference fragment.
// The Importance is derived from the chunk's rerank Score via scoreToImportance.
// SourceReference is set to chunk.ID (the content-addressable vector DB key) so
// the planner can replace the fragment with a lightweight "[ref: <id>]" pointer.
//
// NOTE: this function intentionally does NOT set any SOP-specific or chatbot-
// specific metadata keys. The contextbudget package must never branch on
// business-domain metadata keys (spec §2.2 enforcement rule).
func buildSalesRAGEvidenceFragments(chunks []domain.KnowledgeChunk) []cb.ContextFragment {
	frags := make([]cb.ContextFragment, 0, len(chunks))
	for i, chunk := range chunks {
		sourceRef := chunk.ID
		if sourceRef == "" {
			sourceRef = fmt.Sprintf("salesrag-chunk-%d", i)
		}
		frags = append(frags, cb.ContextFragment{
			ID:              fmt.Sprintf("ev-%d", i),
			Role:            cb.RoleEvidence,
			Source:          cb.SourceKB,
			ContentType:     cb.ContentText,
			Content:         chunk.Content,
			Importance:      scoreToImportance(chunk.Score),
			Order:           100 + i, // evidence slots: 100, 101, 102... (between system@0 and user@1000)
			Compressibility: cb.CompressReference,
			SourceReference: sourceRef,
		})
	}
	return frags
}

// buildSalesRAGSystemFragment returns a RoleImmutable system fragment for a
// salesrag prompt. The content is treated as the top-level persona/instruction
// that must always be present regardless of budget pressure.
//
// Rendering order: Order=0 (system < evidence@100+ < user@1000) per spec §9.2.
func buildSalesRAGSystemFragment(id, systemPrompt string) cb.ContextFragment {
	return cb.ContextFragment{
		ID:              id,
		Role:            cb.RoleImmutable,
		Source:          cb.SourceSystem,
		ContentType:     cb.ContentText,
		Content:         systemPrompt,
		Importance:      10,
		Order:           0, // system always first; evidence @100+, user @1000
		Compressibility: cb.CompressNone,
		Critical:        true,
	}
}

// buildSalesRAGUserFragment returns a RoleRecent + Critical user message fragment
// for salesrag operations. The current user query must not be dropped.
//
// Rendering order: Order=1000 (always last, after system@0 and evidence@100+) per spec §9.2.
func buildSalesRAGUserFragment(id, userMessage string) cb.ContextFragment {
	return cb.ContextFragment{
		ID:              id,
		Role:            cb.RoleRecent,
		Source:          cb.SourceUser,
		ContentType:     cb.ContentText,
		Content:         userMessage,
		Importance:      9,
		Order:           1000, // user query always last; system @0, evidence @100+
		Compressibility: cb.CompressNone,
		Critical:        true,
	}
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
func (b *salesRAGBiz) RetrieveStream(ctx context.Context, retrievalQuery string, promptQuery string, ocrTexts []string, history []string, docIDs []uint, opinionDocIDs []uint, docCategoryMap map[uint]string, deepThinking bool, chatMode string, customerProfile string, salesStage string, onEvent func(eventType string, data interface{}) error) error {
	// 1. 从上下文获取用户ID
	var userID uint
	if uid, ok := middleware.UserIDFromCtx(ctx); ok {
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

	// 4b. 过滤观点库 docIDs
	var filteredOpinionDocIDs []uint
	if len(opinionDocIDs) > 0 {
		for _, id := range opinionDocIDs {
			if enabledDocIDs[id] {
				filteredOpinionDocIDs = append(filteredOpinionDocIDs, id)
			}
		}
	}

	// 发送状态：正在检索知识库与匹配策略
	if err := onEvent("status", "正在检索知识库与匹配策略..."); err != nil {
		return err
	}

	// 6. 执行检索（使用 V2 版本，传递 chatMode 和 history）
	// 注意：RetrieveForResponseV2 内部并行执行 RAG 检索、策略选择和观点库独立检索
	var retrievalSpanID, retrievalTraceID string
	if tc := langfuse.FromContext(ctx); tc != nil {
		retrievalSpanID = langfuse.SpanID()
		retrievalTraceID = tc.TraceID
		langfuse.CreateSpan(tc.TraceID, retrievalSpanID, "parallel_search",
			langfuse.WithSpanParent(tc.ParentObservationID),
			langfuse.WithSpanInput(map[string]interface{}{"query": retrievalQuery, "doc_count": len(filteredDocIDs)}),
		)
		ctx = langfuse.WithTraceAndParent(ctx, tc.TraceID, retrievalSpanID)
	}
	verdict, err := b.ragSvc.RetrieveForResponseV2(ctx, retrievalQuery, filteredDocIDs, filteredOpinionDocIDs, history, chatMode, userID, func(status string) {
		_ = onEvent("status", status)
	})
	if retrievalSpanID != "" {
		if err != nil {
			langfuse.EndSpan(retrievalTraceID, retrievalSpanID, langfuse.WithSpanError(err.Error()))
		} else {
			langfuse.EndSpan(retrievalTraceID, retrievalSpanID, langfuse.WithSpanOutput(map[string]interface{}{
				"evidence_count": len(verdict.Evidence),
				"opinion_count":  len(verdict.OpinionEvidence),
			}))
		}
		// 重置 ParentObservationID — 让后续 chat generation 作为 trace 根的兄弟 span，而非 retrieval span 子节点
		if tc := langfuse.FromContext(ctx); tc != nil {
			ctx = langfuse.WithTrace(ctx, tc.TraceID)
		}
	}
	if err != nil {
		return onEvent("error", fmt.Sprintf("retrieval failed: %v", err))
	}

	// 注入分类映射
	verdict.DocCategoryMap = docCategoryMap

	// 7. 填充 evidence 中的 document_name（从数据库查询）
	// 观点库和常规知识库的文档 ID 集合不重叠，分别查询即可
	b.enrichChunksWithDocNames(ctx, verdict.Evidence)
	b.enrichChunksWithDocNames(ctx, verdict.OpinionEvidence)

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

	// 10. 构建 prompt 并流式生成回答（用 promptQuery + OCR文字）
	messages := b.buildPromptMessagesV2(promptQuery, ocrTexts, verdict, customerProfile, languageStyle, salesStage)

	// 11. 通过 AI Gateway 流式生成回答（profile.SalesragChat）
	ctx = billing.WithBilling(ctx, userID, "salesrag_chat_generate")
	ctx = aismw.WithUserID(ctx, userID)
	ctx = aiservice.WithSkipLegacyBilling(ctx)

	// 将 []map[string]interface{} 转换为 []aiservice.ChatMessage
	aiMessages := make([]aiservice.ChatMessage, 0, len(messages))
	for _, m := range messages {
		role, _ := m["role"].(string)
		content, _ := m["content"].(string)
		aiMessages = append(aiMessages, aiservice.ChatMessage{
			Role:    aiservice.MessageRole(role),
			Content: aiservice.MessageContent{Text: content},
		})
	}

	// Build ContextFragments for the context-budget middleware (spec §9.2 Task 10).
	// Evidence chunks from verdict.Evidence carry score-based importance so the planner
	// can prioritise high-relevance chunks under budget pressure.
	// The system prompt and user query are also wrapped as typed fragments.
	//
	// P2-1 fix: the user fragment carries only the raw user query (promptQuery),
	// NOT the full assembled user message that may include OCR text appended by
	// buildPromptMessagesV2. OCR text is a retrieval aid, not the user's expressed
	// intent, so it should not be tagged as SourceUser + Critical. The assembled
	// messages slice (aiMessages) is still passed to the provider unchanged — this
	// only affects how the context-budget middleware classifies fragments.
	var salesragFragments []cb.ContextFragment
	if len(messages) >= 2 {
		sysMsgContent, _ := messages[0]["content"].(string)
		salesragFragments = append(salesragFragments, buildSalesRAGSystemFragment("sys-0", sysMsgContent))
		salesragFragments = append(salesragFragments, buildSalesRAGEvidenceFragments(verdict.Evidence)...)
		// Use promptQuery (raw user text only, no OCR suffix) as the SourceUser fragment.
		salesragFragments = append(salesragFragments, buildSalesRAGUserFragment("cur-msg", promptQuery))
	}

	// Pre-stream errors (auth, routing) return synchronously via chatErr.
	// Mid-stream errors surface via chunk.IsFinal && chunk.Err on the terminal
	// chunk (A3 contract) — forward to the client as an "error" event instead
	// of silently returning a truncated reply.
	ch, chatErr := aiservice.ChatStream(ctx, profile.SalesragChat, aiservice.ChatRequest{
		Messages:         aiMessages,
		ContextFragments: salesragFragments,
		Temperature:      0.7,
	})
	if chatErr != nil {
		return onEvent("error", fmt.Sprintf("stream chat failed: %v", chatErr))
	}
	var receivedTokens bool
	var streamErr error
	// Token/model metadata captured from the final chunk (spec §3.5). Emitted
	// as a "usage" event after stream drain so the caller (ChatWithSession)
	// can compute actualCost → ICreditService.Reconcile.
	var finalUsage *aiservice.TokenUsage
	var finalModel, finalProvider string
	for chunk := range ch {
		if chunk.ReasoningDelta != "" {
			if evErr := onEvent("thinking", chunk.ReasoningDelta); evErr != nil {
				return evErr
			}
		}
		if chunk.Delta != "" {
			receivedTokens = true
			if evErr := onEvent("token", chunk.Delta); evErr != nil {
				return evErr
			}
		}
		if chunk.Model != "" {
			finalModel = chunk.Model
		}
		if chunk.Provider != "" {
			finalProvider = chunk.Provider
		}
		if chunk.IsFinal {
			if chunk.Usage != nil {
				// Copy so downstream callers don't hold a pointer into the
				// stream channel's memory.
				u := *chunk.Usage
				finalUsage = &u
			}
			if chunk.Err != nil {
				streamErr = chunk.Err
			}
		}
	}
	// Emit a post-drain "usage" event so the ChatWithSession wrapper can
	// synchronously compute actualCost via pricing.CalculateCost before the
	// deferred Finalize fires. onEvent for unknown types is a no-op upstream,
	// so legacy RetrieveStream callers that don't care about usage simply
	// ignore this.
	if finalUsage != nil {
		usagePayload := map[string]interface{}{
			"prompt_tokens":     finalUsage.PromptTokens,
			"completion_tokens": finalUsage.CompletionTokens,
			"total_tokens":      finalUsage.TotalTokens,
			"model":             finalModel,
			"provider":          finalProvider,
		}
		if evErr := onEvent("usage", usagePayload); evErr != nil {
			return evErr
		}
	}
	if streamErr != nil {
		// Emit an internal-only "stream_error" event carrying the raw error so
		// the ChatWithSession wrapper can capture it for credit Refund
		// classification. The controller-facing error is still emitted so SSE
		// clients see the same payload as before (backward compat).
		_ = onEvent("stream_error", streamErr)
		return onEvent("error", fmt.Sprintf("stream chat failed mid-flight: %v", streamErr))
	}
	if !receivedTokens {
		log.Printf("[RetrieveStream] Warning: chat stream ended without any tokens — possible mid-stream error or empty model response")
	}

	// 12. 发送完成事件
	return onEvent("done", nil)
}

// buildPromptMessagesV2 根据检索结果构建 prompt 消息（优化版）
// ocrTexts 非空时，将 OCR 识别的文字追加到 user message 中
func (b *salesRAGBiz) buildPromptMessagesV2(query string, ocrTexts []string, verdict *service.RetrievalVerdict, customerProfile string, languageStyle string, salesStage string) []map[string]interface{} {
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

	// 构建知识上下文（按分类分组，观点库走独立通道）
	var knowledgeContext string
	allChunks := verdict.Evidence
	if len(allChunks) > 0 || len(verdict.OpinionEvidence) > 0 {
		// 分组存储内容（常规知识库，不含观点库）
		categorizedContent := make(map[string][]string)
		categories := []string{catProduct, catCase, catFAQ, catOther}

		for i, chunk := range allChunks {
			category := catOther
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

	// 构建观点库上下文（独立通道，不混入 knowledgeContext）
	var opinionContext string
	if len(verdict.OpinionEvidence) > 0 {
		var opinionLines []string
		for i, chunk := range verdict.OpinionEvidence {
			idx := i + 1
			if chunk.Score > 0 {
				opinionLines = append(opinionLines, fmt.Sprintf("[观点%d] (相关度:%.0f%%) %s", idx, chunk.Score*100, chunk.Content))
			} else {
				opinionLines = append(opinionLines, fmt.Sprintf("[观点%d] %s", idx, chunk.Content))
			}
		}
		opinionContext = strings.Join(opinionLines, "\n\n")
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
		systemPrompt = b.buildFreeModePrompt(customerProfile, knowledgeContext, opinionContext, strategyContent, languageStyle, verdict.History, salesStage)
		userMessage = query
	} else {
		// ========== Sales 模式 (销售人员本人视角) ==========
		systemPrompt = b.buildSalesModePrompt(customerProfile, knowledgeContext, opinionContext, strategyContent, languageStyle, verdict.History, salesStage)
		userMessage = query
	}

	// 构建 user message 的 content（当有 OCR 文字时追加到用户消息中）
	if len(ocrTexts) > 0 {
		ocrBlock := truncate(strings.Join(ocrTexts, "\n"), maxUserInputChars)
		if userMessage != "" {
			userMessage = userMessage + "\n\n【用户上传图片的OCR识别内容】\n" + ocrBlock
		} else {
			userMessage = "【用户上传图片的OCR识别内容】\n" + ocrBlock
		}
	}
	var userContent interface{} = userMessage

	return []map[string]interface{}{
		{
			"role":    "system",
			"content": systemPrompt,
		},
		{
			"role":    "user",
			"content": userContent,
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
// 优先从 Langfuse 获取静态骨架模板，动态段落（条件渲染）仍由 Go 负责组装
func (b *salesRAGBiz) buildSalesModePrompt(customerProfile, knowledgeContext, opinionContext, strategyContent, languageStyle string, history []string, salesStage string) string {
	// 1. 组装动态段落（Go 负责条件渲染逻辑）
	sections := b.buildPromptSections(customerProfile, knowledgeContext, opinionContext, strategyContent, languageStyle, history, salesStage)

	// 2. 尝试从 Langfuse 获取静态骨架模板
	tmpl, _ := langfuse.FetchPrompt("salesrag-answer-sales", "")
	if tmpl != "" {
		return langfuse.Compile(tmpl, sections)
	}

	// 3. Fallback 到硬编码逻辑
	return b.buildSalesModePromptFallback(customerProfile, knowledgeContext, opinionContext, strategyContent, languageStyle, history, salesStage)
}

// buildPromptSections 组装动态段落，供 Langfuse 模板使用
func (b *salesRAGBiz) buildPromptSections(customerProfile, knowledgeContext, opinionContext, strategyContent, languageStyle string, history []string, salesStage string) map[string]string {
	var bgSection, refSection, rulesAddendum strings.Builder

	// 背景段落
	if customerProfile != "" {
		bgSection.WriteString("### 客户画像\n")
		bgSection.WriteString(customerProfile)
		bgSection.WriteString("\n\n")
	}
	if len(history) > 0 {
		bgSection.WriteString("### 对话历史\n")
		bgSection.WriteString(strings.Join(history, "\n"))
		bgSection.WriteString("\n\n")
	}
	if salesStage != "" {
		stageDesc := getSalesStageDescription(salesStage)
		bgSection.WriteString("### 当前销售阶段\n")
		bgSection.WriteString(salesStage)
		if stageDesc != "" {
			bgSection.WriteString("\n")
			bgSection.WriteString(stageDesc)
		}
		bgSection.WriteString("\n\n")
	}

	// 参考资料段落
	if knowledgeContext != "" {
		refSection.WriteString("### 知识库内容\n")
		refSection.WriteString("> 每条知识附带相关度百分比。相关度 ≥ 70% 的知识应重点融入回答；30%-70% 的仅作补充参考；如果所有知识相关度都偏低，以你的专业判断为主。\n\n")
		refSection.WriteString("> **注意**：这些知识片段可能来自不同文档，彼此之间可能没有直接关联。请先通读理解所有片段的核心信息，形成统一认知后，围绕客户的实际问题用你自己的话自然组织回答。禁止逐条罗列，禁止生硬拼接不相关的信息。如果某条知识与当前问题关联不大，果断忽略即可。\n\n")
		refSection.WriteString(knowledgeContext)
		refSection.WriteString("\n\n")
	}
	if opinionContext != "" {
		refSection.WriteString("### 观点库\n")
		refSection.WriteString("> 以下观点库供你参考，每条观点包含场景与关键词触达、客户潜台词、金句观点、活人感话术建议。请根据用户的消息，判断其情绪和所处场景，从观点库中挑选最匹配的观点，将其中的金句和话术建议自然地融入回复中。\n\n")
		refSection.WriteString("> **注意**：不要生硬地罗列观点，而是转化为你自己的语言，让回复听起来真诚、有洞察力。特别关注「客户潜台词」来精准把握客户真实心理，用「活人感话术」的风格让回复有温度。**重要！**：如果你觉得观点库不适合当轮的情况，则可以战术性放弃采用。\n\n")
		refSection.WriteString(opinionContext)
		refSection.WriteString("\n\n")
	}
	if strategyContent != "" {
		refSection.WriteString("### 核心策略参考\n")
		refSection.WriteString("> 请结合实际对话上下文，**灵活参考**该策略中的\"话术模板\"或\"核心逻辑\"构建回复。如果策略与当前对话相符，需要严格按照策略来进行，如果明显不符，请以你的判断为准。\n\n")
		refSection.WriteString("```markdown\n")
		refSection.WriteString(strategyContent)
		refSection.WriteString("\n```\n\n")
	}

	// 规则补充段（根据有无知识库/观点库/策略的条件规则）
	if knowledgeContext != "" {
		rulesAddendum.WriteString("   涉及具体产品信息（价格、功能、参数、案例）时，必须基于【知识库内容】  \n")
		rulesAddendum.WriteString("   涉及策略分析、客户心理、沟通技巧时，请充分发挥你的专业判断  \n")
		rulesAddendum.WriteString("   优先融合相关度高的知识，用你自己的理解重新组织，而非原文照搬  \n")
	}
	if opinionContext != "" {
		rulesAddendum.WriteString("   涉及观点输出和洞察展现时，优先从【观点库】中挑选场景匹配的观点，参考其金句和活人感话术，转化为自己的语言自然融入  \n")
	}
	if strategyContent != "" {
		rulesAddendum.WriteString("   灵活参考【核心策略参考】中的话术模板或逻辑\n")
	}

	// 语言风格
	styleSection := "使用通用的微信聊天风格：简洁、自然、适度使用口语化表达"
	if languageStyle != "" {
		styleSection = languageStyle
	}

	return map[string]string{
		"background_section": bgSection.String(),
		"reference_section":  refSection.String(),
		"rules_addendum":     rulesAddendum.String(),
		"language_style":     styleSection,
	}
}

// buildSalesModePromptFallback 硬编码的 Sales 模式提示词（Langfuse 不可用时的 fallback）
func (b *salesRAGBiz) buildSalesModePromptFallback(customerProfile, knowledgeContext, opinionContext, strategyContent, languageStyle string, history []string, salesStage string) string {
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

	// 观点库（条件渲染 - 独立通道）
	if opinionContext != "" {
		prompt.WriteString("### 观点库\n")
		prompt.WriteString("> 以下观点库供你参考，每条观点包含场景与关键词触达、客户潜台词、金句观点、活人感话术建议。请根据用户的消息，判断其情绪和所处场景，从观点库中挑选最匹配的观点，将其中的金句和话术建议自然地融入回复中。\n\n")
		prompt.WriteString("> **注意**：不要生硬地罗列观点，而是转化为你自己的语言，让回复听起来真诚、有洞察力。特别关注「客户潜台词」来精准把握客户真实心理，用「活人感话术」的风格让回复有温度。**重要！**：如果你觉得观点库不适合当轮的情况，则可以战术性放弃采用。\n\n")
		prompt.WriteString(opinionContext)
		prompt.WriteString("\n\n")
	}

	// 核心策略参考（条件渲染）
	if strategyContent != "" {
		prompt.WriteString("### 核心策略参考\n")
		prompt.WriteString("> 请结合实际对话上下文，**灵活参考**该策略中的\"话术模板\"或\"核心逻辑\"构建回复。如果策略与当前对话相符，需要严格按照策略来进行，如果明显不符，请以你的判断为准。\n\n")
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
	prompt.WriteString("### 选项C：高势能回复（造梦风格）\n")
	prompt.WriteString("以\"造梦\"为核心策略，通过具体画面描绘客户成功后的未来愿景，辅以真实学员案例证明路径可行性，再戳破当前痛点给出确定性价值，让客户感受到希望、掌控感和可复制的成功路径。\n（直接写给客户的话术，可分多段）\n\n")
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
	if opinionContext != "" {
		prompt.WriteString("   涉及观点输出和洞察展现时，优先从【观点库】中挑选场景匹配的观点，参考其金句和活人感话术，转化为自己的语言自然融入  \n")
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
// 优先从 Langfuse 获取静态骨架模板，动态段落（条件渲染）仍由 Go 负责组装
func (b *salesRAGBiz) buildFreeModePrompt(customerProfile, knowledgeContext, opinionContext, strategyContent, languageStyle string, history []string, salesStage string) string {
	// 1. 组装动态段落（复用 buildPromptSections）
	sections := b.buildPromptSections(customerProfile, knowledgeContext, opinionContext, strategyContent, languageStyle, history, salesStage)

	// 2. 尝试从 Langfuse 获取静态骨架模板
	tmpl, _ := langfuse.FetchPrompt("salesrag-answer-free", "")
	if tmpl != "" {
		return langfuse.Compile(tmpl, sections)
	}

	// 3. Fallback 到硬编码逻辑
	return b.buildFreeModePromptFallback(customerProfile, knowledgeContext, opinionContext, strategyContent, languageStyle, history, salesStage)
}

// buildFreeModePromptFallback 硬编码的 Free 模式提示词（Langfuse 不可用时的 fallback）
func (b *salesRAGBiz) buildFreeModePromptFallback(customerProfile, knowledgeContext, opinionContext, strategyContent, languageStyle string, history []string, salesStage string) string {
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
		prompt.WriteString("> 每条知识附带相关度百分比。相关度 ≥ 70% 的知识应重点融入回答；30%-70% 的仅作补充参考；如果所有知识相关度都偏低，以你的专业判断为主。\n\n")
		prompt.WriteString("> **注意**：这些知识片段可能来自不同文档，彼此之间可能没有直接关联。请先通读理解所有片段的核心信息，形成统一认知后，围绕客户的实际问题用你自己的话自然组织回答。禁止逐条罗列，禁止生硬拼接不相关的信息。如果某条知识与当前问题关联不大，果断忽略即可。\n\n")
		prompt.WriteString(knowledgeContext)
		prompt.WriteString("\n\n")
	}

	// 观点库（条件渲染 - 独立通道）
	if opinionContext != "" {
		prompt.WriteString("### 观点库\n")
		prompt.WriteString("> 以下观点库供你参考，每条观点包含场景与关键词触达、客户潜台词、金句观点、活人感话术建议。请根据用户的消息，判断其情绪和所处场景，从观点库中挑选最匹配的观点，将其中的金句和话术建议自然地融入回复中。\n\n")
		prompt.WriteString("> **注意**：不要生硬地罗列观点，而是转化为你自己的语言，让回复听起来真诚、有洞察力。特别关注「客户潜台词」来精准把握客户真实心理，用「活人感话术」的风格让回复有温度。**重要！**：如果你觉得观点库不适合当轮的情况，则可以战术性放弃采用。\n\n")
		prompt.WriteString(opinionContext)
		prompt.WriteString("\n\n")
	}

	// 核心策略参考（条件渲染）
	if strategyContent != "" {
		prompt.WriteString("### 核心策略参考\n")
		prompt.WriteString("> 请结合实际对话上下文，**灵活参考**该策略中的\"话术模板\"或\"核心逻辑\"提供建议。如果策略与当前对话相符，需要严格按照策略来进行，如果明显不符，请以你的判断为准。\n\n")
		prompt.WriteString("```markdown\n")
		prompt.WriteString(strategyContent)
		prompt.WriteString("\n```\n\n")
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
	prompt.WriteString("选项C：高势能回复（造梦风格）\n")
	prompt.WriteString("以\"造梦\"为核心，用具体画面描绘未来愿景，以真实案例证明可行性，戳破痛点给出确定性路径。适用于需要激发客户行动力的场景。\n")
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
	if opinionContext != "" {
		prompt.WriteString("  - 涉及观点输出和立场建议时，可从【观点库】中挑选场景匹配的观点，参考其金句和活人感话术，融入话术建议中\n")
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
	// 校验观点库上限：系统赛道 + 自定义赛道合计最多 2 个
	if len(req.OpinionTrackIDs)+len(req.OpinionDocIDs) > maxOpinionTotal {
		return nil, fmt.Errorf("系统赛道与自定义赛道合计最多选择 %d 个", maxOpinionTotal)
	}

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

	// 校验观点库上限：系统赛道 + 自定义赛道合计最多 2 个
	if len(req.OpinionTrackIDs)+len(req.OpinionDocIDs) > maxOpinionTotal {
		return fmt.Errorf("系统赛道与自定义赛道合计最多选择 %d 个", maxOpinionTotal)
	}

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
// 安全校验：仅返回 is_enabled 且关联文档为系统文档（is_system=true）的 doc_id
func (b *salesRAGBiz) resolveTrackDocIDs(ctx context.Context, trackIDs []uint) []uint {
	if len(trackIDs) == 0 {
		return nil
	}
	// 上限校验：最多 maxOpinionTotal 个赛道（合计上限的保护）
	if len(trackIDs) > maxOpinionTotal {
		log.Printf("[resolveTrackDocIDs] Warning: too many track IDs (%d), truncating to %d", len(trackIDs), maxOpinionTotal)
		trackIDs = trackIDs[:maxOpinionTotal]
	}
	var tracks []model.OpinionTrack
	if err := b.ds.DB().WithContext(ctx).Where("id IN ? AND is_enabled = ?", trackIDs, true).Find(&tracks).Error; err != nil {
		log.Printf("[resolveTrackDocIDs] Warning: failed to query tracks: %v", err)
		return nil
	}
	// 收集候选 doc_id
	candidateDocIDs := make([]uint, 0, len(tracks))
	for _, t := range tracks {
		if t.DocID > 0 {
			candidateDocIDs = append(candidateDocIDs, t.DocID)
		}
	}
	if len(candidateDocIDs) == 0 {
		return nil
	}
	// 二次校验：确认关联的文档确实是系统文档（is_system=true）
	var validDocs []model.KnowledgeDocument
	if err := b.ds.DB().WithContext(ctx).Where("id IN ? AND is_system = ?", candidateDocIDs, true).Select("id").Find(&validDocs).Error; err != nil {
		log.Printf("[resolveTrackDocIDs] Warning: failed to verify system docs: %v", err)
		return nil
	}
	docIDs := make([]uint, 0, len(validDocs))
	for _, doc := range validDocs {
		docIDs = append(docIDs, doc.ID)
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
// ocrTexts: 图片OCR识别文字，仅用于知识库检索，不进AI prompt
func (b *salesRAGBiz) ChatWithSession(ctx context.Context, userID uint, sessionID uint, query string, ocrTexts []string, images []string, docIDs []uint, deepThinking bool, chatMode string, onEvent func(eventType string, data interface{}) error) error {
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

	// 解析分类字段（JSON 解析失败时记录错误并继续，不中断对话）
	var parseErrors int
	parseDocIDs := func(raw string, fieldName string, target *[]uint) {
		if raw == "" || raw == "null" {
			return
		}
		if err := json.Unmarshal([]byte(raw), target); err != nil {
			log.Printf("[ChatWithSession] Error: failed to parse %s (raw=%q): %v", fieldName, raw, err)
			parseErrors++
		}
	}
	parseDocIDs(session.ProductDocIDs, "product_doc_ids", &productDocIDs)
	parseDocIDs(session.CaseDocIDs, "case_doc_ids", &caseDocIDs)
	parseDocIDs(session.FAQDocIDs, "faq_doc_ids", &faqDocIDs)
	parseDocIDs(session.OpinionDocIDs, "opinion_doc_ids", &opinionDocIDs)
	parseDocIDs(session.OpinionTrackIDs, "opinion_track_ids", &opinionTrackIDs)
	if parseErrors > 0 {
		log.Printf("[ChatWithSession] Warning: %d field(s) had JSON parse errors for session %d, some documents may be missing from retrieval", parseErrors, sessionID)
	}

	// 将系统赛道 ID 解析为对应的文档 ID
	trackDocIDs := b.resolveTrackDocIDs(ctx, opinionTrackIDs)

	// 合并非观点分类文档 ID（观点库走独立通道）
	sessionDocIDs = append(sessionDocIDs, productDocIDs...)
	sessionDocIDs = append(sessionDocIDs, caseDocIDs...)
	sessionDocIDs = append(sessionDocIDs, faqDocIDs...)

	// 观点文档单独收集，不混入 sessionDocIDs
	allOpinionDocIDs := make([]uint, 0, len(opinionDocIDs)+len(trackDocIDs))
	allOpinionDocIDs = append(allOpinionDocIDs, opinionDocIDs...)
	allOpinionDocIDs = append(allOpinionDocIDs, trackDocIDs...)

	// 向后兼容：如果所有分类字段都为空，则 fallback 到旧 document_ids 字段
	if len(sessionDocIDs) == 0 && len(allOpinionDocIDs) == 0 && session.DocumentIDs != "" && session.DocumentIDs != "null" {
		if err := json.Unmarshal([]byte(session.DocumentIDs), &sessionDocIDs); err != nil {
			log.Printf("[ChatWithSession] Warning: failed to parse session document_ids: %v", err)
		}
	}

	// 3. 处理用户消息（只存用户文字，不存OCR内容）
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

	// 3b. 构建检索用查询（用户文字 + OCR识别文字拼接，仅用于知识库检索）
	retrievalQuery := query
	if len(ocrTexts) > 0 {
		ocrBlock := strings.Join(ocrTexts, "\n")
		if query != "" {
			retrievalQuery = query + "\n" + ocrBlock
		} else {
			retrievalQuery = ocrBlock
		}
	}

	// 构建文档分类映射（仅常规知识库，观点库走独立通道不需要分类映射）
	docCategoryMap := make(map[uint]string)
	for _, id := range productDocIDs {
		docCategoryMap[id] = catProduct
	}
	for _, id := range caseDocIDs {
		docCategoryMap[id] = catCase
	}
	for _, id := range faqDocIDs {
		docCategoryMap[id] = catFAQ
	}

	// 4. 创建 Langfuse trace（贯穿整条 SalesRAG 调用链路）
	traceID := langfuse.TraceID()
	langfuse.CreateTrace(traceID, "salesrag_chat",
		langfuse.WithUserID(userID),
		langfuse.WithSessionID(fmt.Sprintf("%d", sessionID)),
		langfuse.WithTraceInput(map[string]interface{}{"query": query, "chatMode": chatMode, "deepThinking": deepThinking}),
		langfuse.WithTraceTags("salesrag"),
	)
	ctx = langfuse.WithTrace(ctx, traceID)

	// 将 trace_id 写入 billing metadata，关联 billing 和 Langfuse
	if bc := billing.FromContext(ctx); bc != nil {
		if bc.Meta == nil {
			bc.Meta = make(map[string]string)
		}
		bc.Meta["trace_id"] = traceID
	}

	// 4b. Credits-system pre-check + reservation (Phase 2 Task 2.2 / spec §3.5).
	//
	// Order of ops relative to SSE headers: the controller has already written
	// SSE headers before we got here, so any credit-denial error returned from
	// this function surfaces to the user as an SSE "error" event (via the
	// controller's err-write path at sales_rag.go:216). That matches sop_run /
	// sop_chat's behaviour post-integration.
	//
	// When creditSvc is nil (legacy wiring / tests without the new deps), this
	// block is a no-op and the biz behaves exactly as before — the controller
	// still invokes the old CanPerformAIOperation + DeductCredits fire-and-
	// forget path, so prod traffic is never left un-billed even in a partial
	// rollout.
	//
	// Prompt-char estimate based on user input + retrieval query + customer
	// profile + history. The RAG-retrieved context is NOT yet known (retrieval
	// happens inside RetrieveStream), so we add a small allowance for it;
	// the R2 safety_buffer_pct (≥ 20%) covers residual under-estimation and
	// the Reconcile delta trues up the final cost.
	promptChars := utf8.RuneCountInString(retrievalQuery) +
		utf8.RuneCountInString(session.CustomerProfile)
	for _, h := range history {
		promptChars += utf8.RuneCountInString(h)
	}
	const ragContextAllowance = 2000
	promptChars += ragContextAllowance

	cc, creditErr := b.acquireSalesragCredits(ctx, userID, sessionID, promptChars)
	if creditErr != nil {
		return creditErr
	}
	// defer FinalizeReservation using a detached context (done inside
	// cc.finalize) so Refund/Reconcile still completes after the request
	// context is cancelled (client disconnect = context.Canceled).
	defer cc.finalize(ctx)

	// 调用流式检索，累积内容
	// 使用从数据库加载的 sessionDocIDs，而不是函数参数中的 docIDs
	var fullContent strings.Builder
	var verdictJSON string
	var thinkingText string
	// Stream metadata captured from RetrieveStream internal "usage" event —
	// used post-drain to compute actualCost via pricing.CalculateCost.
	var streamPromptTokens, streamCompletionTokens int
	var streamModel, streamProvider string
	var streamErr error

	err = b.RetrieveStream(ctx, retrievalQuery, query, ocrTexts, history, sessionDocIDs, allOpinionDocIDs, docCategoryMap, deepThinking, chatMode, session.CustomerProfile, session.SalesStage, func(eventType string, data interface{}) error {
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

		case "usage":
			// Internal event emitted by RetrieveStream after stream drain.
			// Swallow it here — not forwarded to the SSE client (no existing
			// consumer expects this frame).
			if usage, ok := data.(map[string]interface{}); ok {
				if pt, ok := usage["prompt_tokens"].(int); ok {
					streamPromptTokens = pt
				}
				if ct, ok := usage["completion_tokens"].(int); ok {
					streamCompletionTokens = ct
				}
				if m, ok := usage["model"].(string); ok {
					streamModel = m
				}
				if p, ok := usage["provider"].(string); ok {
					streamProvider = p
				}
			}
			return nil

		case "stream_error":
			// Internal event — capture the raw error for credit Refund
			// classification but don't forward (the companion "error" event
			// already reaches the SSE client via RetrieveStream).
			if e, ok := data.(error); ok {
				streamErr = e
			}
			return nil

		case "error", "done":
			// 直接传递
			return onEvent(eventType, data)

		default:
			return onEvent(eventType, data)
		}
	})

	// Propagate stream outcomes to the credit context so FinalizeReservation
	// (deferred above) fires the correct branch: Reconcile on success with an
	// observed actualCost, Refund with classified reason on error / abort.
	// Pre-stream errors (err != nil from RetrieveStream) take precedence
	// because they represent hard failures that never reached the LLM.
	switch {
	case err != nil:
		cc.recordLLMResult(ctx, err, streamProvider, streamModel, streamPromptTokens, streamCompletionTokens)
		return err
	case streamErr != nil:
		cc.recordLLMResult(ctx, streamErr, streamProvider, streamModel, streamPromptTokens, streamCompletionTokens)
		return streamErr
	default:
		cc.recordLLMResult(ctx, nil, streamProvider, streamModel, streamPromptTokens, streamCompletionTokens)
	}

	// 5. 保存助手消息（关联 Langfuse traceID，用于后续反馈评分）
	assistantMessage := &model.SalesMessage{
		SessionID: sessionID,
		UserID:    userID,
		Role:      "assistant",
		Content:   fullContent.String(),
		Status:    "sent",
		Verdict:   verdictJSON,
		Thinking:  thinkingText,
		TraceID:   traceID,
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

// SubmitFeedback 提交消息反馈（点赞/点踩）
func (b *salesRAGBiz) SubmitFeedback(ctx context.Context, userID, sessionID, messageID uint, rating int, comment string) error {
	// 1. 验证会话属于该用户
	_, err := b.sessionStore.GetSession(ctx, sessionID, userID)
	if err != nil {
		return errno.ErrForbidden
	}
	// 2. 验证消息存在且属于该会话
	msg, err := b.sessionStore.GetMessage(ctx, messageID)
	if err != nil {
		return errno.ErrInvalidParameter.SetMessage("消息不存在")
	}
	if msg.SessionID != sessionID {
		return errno.ErrForbidden
	}

	// 3. 保存到本地 DB（upsert）
	feedback := &model.SalesMessageFeedback{
		MessageID: messageID,
		UserID:    userID,
		Rating:    rating,
		Comment:   comment,
		TraceID:   msg.TraceID,
	}
	if err := b.sessionStore.CreateOrUpdateFeedback(ctx, feedback); err != nil {
		return fmt.Errorf("保存反馈失败: %w", err)
	}

	// 3. 发送到 Langfuse（异步，不阻塞）
	if msg.TraceID != "" {
		langfuse.Score(msg.TraceID, "user_feedback", float64(rating), comment)
	}
	return nil
}

// GetFeedback 获取消息反馈
func (b *salesRAGBiz) GetFeedback(ctx context.Context, userID, sessionID, messageID uint) (*model.SalesMessageFeedback, error) {
	// 验证会话属于该用户
	_, err := b.sessionStore.GetSession(ctx, sessionID, userID)
	if err != nil {
		return nil, errno.ErrForbidden
	}

	feedback, err := b.sessionStore.GetFeedback(ctx, messageID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil // 无反馈，返回 nil
		}
		return nil, err
	}
	return feedback, nil
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

			// 截断过长文本（按 unicode 字符数，30000 字符）
			if runes := []rune(text); len(runes) > 30000 {
				text = string(runes[:30000]) + "\n...(truncated)"
			}

			contentParts = append(contentParts, map[string]interface{}{
				"type": "text",
				"text": fmt.Sprintf("\n--- 文档 [%s] 内容 ---\n%s\n", fileHeader.Filename, text),
			})
			log.Printf("Added document part: %s (len: %d)", fileHeader.Filename, len(text))
		}
	}

	// 2. 构建 system + user 消息（DEC-016: system/user 分离）
	// 3. 调用多模态模型 (Doubao-Seed-2.0-Lite, thinking: medium)
	messages := []map[string]interface{}{
		{
			"role":    "system",
			"content": fetchProfilePrompt(),
		},
		{
			"role":    "user",
			"content": contentParts,
		},
	}

	log.Printf("[AnalyzeProfileMultiFiles] Calling AI Gateway (profile.SalesragProfile) with %d content parts", len(contentParts))
	ctx = billing.WithBilling(ctx, userID, "salesrag_analyze_profile")
	ctx = aismw.WithUserID(ctx, userID)
	ctx = aiservice.WithSkipLegacyBilling(ctx)

	// 转换 contentParts ([]map[string]interface{}) 为 aiservice.MessagePart 切片
	aiParts := make([]aiservice.MessagePart, 0, len(contentParts))
	for _, p := range contentParts {
		partType, _ := p["type"].(string)
		switch partType {
		case "text":
			text, _ := p["text"].(string)
			aiParts = append(aiParts, aiservice.MessagePart{
				Type: aiservice.MessagePartTypeText,
				Text: text,
			})
		case "image_url":
			imgMap, _ := p["image_url"].(map[string]string)
			url := imgMap["url"]
			aiParts = append(aiParts, aiservice.MessagePart{
				Type:     aiservice.MessagePartTypeImageURL,
				ImageURL: &aiservice.ImageURL{URL: url},
			})
		}
	}

	systemPromptText, _ := messages[0]["content"].(string)
	aiMessages := []aiservice.ChatMessage{
		{
			Role:    aiservice.MessageRoleSystem,
			Content: aiservice.MessageContent{Text: systemPromptText},
		},
		{
			Role:    aiservice.MessageRoleUser,
			Content: aiservice.MessageContent{Parts: aiParts},
		},
	}

	// Build ContextFragments for context-budget middleware (salesrag_analyze_profile).
	// The user message contains multipart content (text + images); we represent it
	// as a single RoleRecent + Critical fragment with the text portion for estimation.
	// Vision/image parts cannot be token-estimated by the budget system, so we include
	// only the text portion here; the legacy aiMessages path carries the full Parts payload.
	var userTextForFragment string
	for _, p := range contentParts {
		if t, _ := p["type"].(string); t == "text" {
			if txt, _ := p["text"].(string); txt != "" {
				userTextForFragment += txt + "\n"
			}
		}
	}
	profileFragments := []cb.ContextFragment{
		buildSalesRAGSystemFragment("sys-0", systemPromptText),
		buildSalesRAGUserFragment("cur-msg", strings.TrimSpace(userTextForFragment)),
	}

	ch, err := aiservice.ChatStream(ctx, profile.SalesragProfile, aiservice.ChatRequest{
		Messages:         aiMessages,
		ContextFragments: profileFragments,
		Temperature:      0.5,
	})
	if err != nil {
		return "", fmt.Errorf("AI Gateway profile stream failed: %w", err)
	}
	var result string
	var streamErr error
	for chunk := range ch {
		if chunk.Delta != "" {
			result += chunk.Delta
			if err := onToken(chunk.Delta); err != nil {
				return result, err
			}
		}
		if chunk.IsFinal && chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if streamErr != nil {
		// Refuse to return a partial profile that the caller would treat as
		// a complete analysis — surface the failure so upstream retries or
		// marks the job as failed.
		return result, fmt.Errorf("AnalyzeProfileMultiFiles: stream error: %w", streamErr)
	}
	return result, nil
}

// AnalyzeProfileText 纯文本分析生成客户档案
// 与 AnalyzeProfileMultiFiles 使用相同的 system prompt，但直接接收文本输入
func (b *salesRAGBiz) AnalyzeProfileText(ctx context.Context, userID uint, text string, onToken func(token string) error) (string, error) {
	log.Printf("[AnalyzeProfileText] Starting analysis for user %d, text length: %d", userID, len(text))

	// 截断过长文本（按 unicode 字符数，30000 字符）
	if runes := []rune(text); len(runes) > 30000 {
		text = string(runes[:30000]) + "\n...(truncated)"
	}

	// 构建 content parts
	contentParts := []map[string]interface{}{
		{
			"type": "text",
			"text": "以下是该客户的相关资料：\n\n" + text,
		},
	}

	log.Printf("[AnalyzeProfileText] Calling AI Gateway (profile.SalesragProfile)")
	ctx = billing.WithBilling(ctx, userID, "salesrag_analyze_profile_text")
	ctx = aismw.WithUserID(ctx, userID)
	ctx = aiservice.WithSkipLegacyBilling(ctx)

	aiParts := make([]aiservice.MessagePart, 0, len(contentParts))
	for _, p := range contentParts {
		t, _ := p["text"].(string)
		aiParts = append(aiParts, aiservice.MessagePart{
			Type: aiservice.MessagePartTypeText,
			Text: t,
		})
	}
	profilePrompt := fetchProfilePrompt()
	aiMessages := []aiservice.ChatMessage{
		{
			Role:    aiservice.MessageRoleSystem,
			Content: aiservice.MessageContent{Text: profilePrompt},
		},
		{
			Role:    aiservice.MessageRoleUser,
			Content: aiservice.MessageContent{Parts: aiParts},
		},
	}

	// Build ContextFragments (salesrag_analyze_profile_text operation).
	userText := "以下是该客户的相关资料：\n\n" + text
	profileTextFragments := []cb.ContextFragment{
		buildSalesRAGSystemFragment("sys-0", profilePrompt),
		buildSalesRAGUserFragment("cur-msg", userText),
	}

	ch, err := aiservice.ChatStream(ctx, profile.SalesragProfile, aiservice.ChatRequest{
		Messages:         aiMessages,
		ContextFragments: profileTextFragments,
		Temperature:      0.5,
	})
	if err != nil {
		return "", fmt.Errorf("AI Gateway profile text stream failed: %w", err)
	}
	var result string
	var streamErr error
	for chunk := range ch {
		if chunk.Delta != "" {
			result += chunk.Delta
			if err := onToken(chunk.Delta); err != nil {
				return result, err
			}
		}
		if chunk.IsFinal && chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if streamErr != nil {
		return result, fmt.Errorf("AnalyzeProfileText: stream error: %w", streamErr)
	}
	return result, nil
}

// AnalyzeChatStyleStream 流式分析聊天风格（语言指纹分析）
// 图片通过 Gateway profile.SalesragChatstyle 路由（vision），文本通过 Gateway profile.SalesragChatstyle 路由（均为流式）
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

	// 文本文件通过 Gateway profile.SalesragChatstyle 路由
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

	// 4. 通过 AI Gateway 调用（profile.SalesragChatstyle，流式输出）
	log.Printf("[analyzeChatStyleTextStream] Calling AI Gateway (profile.SalesragChatstyle) for user %d", userID)
	ctx = billing.WithBilling(ctx, userID, "salesrag_chat_style_text")
	ctx = aismw.WithUserID(ctx, userID)
	ctx = aiservice.WithSkipLegacyBilling(ctx)
	aiMessages := []aiservice.ChatMessage{
		{
			Role:    aiservice.MessageRoleSystem,
			Content: aiservice.MessageContent{Text: systemPrompt},
		},
		{
			Role:    aiservice.MessageRoleUser,
			Content: aiservice.MessageContent{Text: text},
		},
	}
	// Build ContextFragments (salesrag_chat_style_text operation).
	chatStyleFragments := []cb.ContextFragment{
		buildSalesRAGSystemFragment("sys-0", systemPrompt),
		buildSalesRAGUserFragment("cur-msg", text),
	}
	ch, err := aiservice.ChatStream(ctx, profile.SalesragChatstyle, aiservice.ChatRequest{
		Messages:         aiMessages,
		ContextFragments: chatStyleFragments,
		Temperature:      0.5,
	})
	if err != nil {
		log.Printf("[analyzeChatStyleTextStream] AI Gateway stream failed for user %d: %v", userID, err)
		return "", fmt.Errorf("AI 分析服务调用失败: %w", err)
	}
	var analysis string
	var streamErr error
	for chunk := range ch {
		if chunk.Delta != "" {
			analysis += chunk.Delta
			if err := onToken(chunk.Delta); err != nil {
				return analysis, err
			}
		}
		if chunk.IsFinal && chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if streamErr != nil {
		// Don't persist a half-built language style; propagate up so the
		// caller can retry or surface the error to the user.
		log.Printf("[analyzeChatStyleTextStream] stream error for user %d: %v", userID, streamErr)
		return analysis, fmt.Errorf("analyzeChatStyleTextStream: stream error: %w", streamErr)
	}
	log.Printf("[analyzeChatStyleTextStream] AI Gateway success, result length: %d", len(analysis))

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

	// 5. 通过 AI Gateway 调用视觉模型（profile.SalesragChatstyle，流式输出）
	ctx = billing.WithBilling(ctx, userID, "salesrag_chat_style_image")
	ctx = aismw.WithUserID(ctx, userID)
	ctx = aiservice.WithSkipLegacyBilling(ctx)
	visionMessages := []aiservice.ChatMessage{
		{
			Role:    aiservice.MessageRoleSystem,
			Content: aiservice.MessageContent{Text: systemPrompt},
		},
		{
			Role: aiservice.MessageRoleUser,
			Content: aiservice.MessageContent{
				Parts: []aiservice.MessagePart{
					{
						Type:     aiservice.MessagePartTypeImageURL,
						ImageURL: &aiservice.ImageURL{URL: dataURL},
					},
				},
			},
		},
	}
	visionCh, err := aiservice.ChatStream(ctx, profile.SalesragChatstyle, aiservice.ChatRequest{
		Messages:    visionMessages,
		Temperature: 0.5,
	})
	if err != nil {
		log.Printf("[analyzeChatStyleImageStream] AI Gateway vision stream error: %v", err)
		return "", fmt.Errorf("视觉模型分析失败: %w", err)
	}
	var result string
	var visionStreamErr error
	for chunk := range visionCh {
		if chunk.Delta != "" {
			result += chunk.Delta
			if err := onToken(chunk.Delta); err != nil {
				return result, err
			}
		}
		if chunk.IsFinal && chunk.Err != nil {
			visionStreamErr = chunk.Err
		}
	}
	if visionStreamErr != nil {
		// Same fail-fast contract as the text path: don't persist a half-built
		// language style on mid-stream failure.
		log.Printf("[analyzeChatStyleImageStream] stream error for user %d: %v", userID, visionStreamErr)
		return result, fmt.Errorf("analyzeChatStyleImageStream: stream error: %w", visionStreamErr)
	}

	log.Printf("[analyzeChatStyleImageStream] AI Gateway vision stream completed, result length: %d", len(result))

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

// OCRAnalyze 识别图片中的文本
// engine: "baidu"（百度光学OCR，默认）或 "vision"（火山视觉大模型）
func (b *salesRAGBiz) OCRAnalyze(ctx context.Context, userID uint, imageData []byte, contentType string, sessionID string, filename string, engine string) (string, string, error) {
	if engine == "" {
		engine = "baidu"
	}

	// 1. 上传原图到 COS（两种引擎都需要）
	objectKey := fmt.Sprintf("sales_chat/%d/%s/%d_%s", userID, sessionID, time.Now().Unix(), filename)
	cosURL, err := util.UploadBytesToCOS(ctx, objectKey, contentType, imageData)
	if err != nil {
		log.Printf("[OCRAnalyze] Upload image to COS failed, user_id: %d, key: %s, error: %v", userID, objectKey, err)
		return "", "", fmt.Errorf("图片存储失败: %w", err)
	}

	// 1b. 如果图片超过火山方舟限制，压缩后重新上传
	// 返回的 URL 后续会作为多模态 image_url 传给 AI
	// 火山方舟限制：大小 < 10MB, 总像素 < 36MP, 宽高比 < 150:1
	const maxAIImageSize = 10 * 1024 * 1024
	const maxAIPixels int64 = 36_000_000
	const maxAIAspectRatio = 150.0

	displayObjectKey := objectKey
	needsCompress := len(imageData) > maxAIImageSize
	if img, decErr := imaging.Decode(bytes.NewReader(imageData)); decErr == nil {
		width, height := img.Bounds().Dx(), img.Bounds().Dy()
		totalPixels := int64(width) * int64(height)
		aspectRatio := float64(width) / float64(height)
		if height > width {
			aspectRatio = float64(height) / float64(width)
		}

		if totalPixels > maxAIPixels || aspectRatio > maxAIAspectRatio {
			needsCompress = true
		}

		if needsCompress {
			log.Printf("[OCRAnalyze] Image needs compression for AI: size=%d, pixels=%d, ratio=%.1f, user_id=%d",
				len(imageData), totalPixels, aspectRatio, userID)

			// 先按像素/宽高比计算初始缩放
			scale := 1.0
			if totalPixels > maxAIPixels {
				scale = math.Sqrt(float64(maxAIPixels-1_000_000) / float64(totalPixels))
			}
			if aspectRatio > maxAIAspectRatio {
				scale = math.Min(scale, 0.8)
			}
			if scale < 1.0 {
				width = int(float64(width) * scale)
				height = int(float64(height) * scale)
				img = imaging.Resize(img, width, height, imaging.Lanczos)
			}

			// 循环压缩直到满足大小限制
			quality := 85
			var compressed []byte
			var buf bytes.Buffer
			if encErr := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); encErr == nil {
				compressed = buf.Bytes()
			}
			for len(compressed) > maxAIImageSize && (width > 100 || height > 100) {
				width = int(float64(width) * 0.85)
				height = int(float64(height) * 0.85)
				resized := imaging.Resize(img, width, height, imaging.Lanczos)
				buf.Reset()
				if encErr := jpeg.Encode(&buf, resized, &jpeg.Options{Quality: quality}); encErr != nil {
					break
				}
				compressed = buf.Bytes()
				if len(compressed) > maxAIImageSize && quality > 30 {
					quality -= 10
				}
			}

			if len(compressed) > 0 && len(compressed) <= maxAIImageSize {
				compressedKey := objectKey + "_ai.jpg"
				if _, upErr := util.UploadBytesToCOS(ctx, compressedKey, "image/jpeg", compressed); upErr == nil {
					displayObjectKey = compressedKey
					log.Printf("[OCRAnalyze] Compressed for AI: %d -> %d bytes, %dx%d, user_id: %d",
						len(imageData), len(compressed), width, height, userID)
				}
			}
		}
	}

	// 生成前端展示用的签名 URL（使用压缩版，若有）
	frontendURL, err := util.GenerateSignedURL(ctx, displayObjectKey, 86400) // 24h
	if err != nil {
		log.Printf("[OCRAnalyze] Generate frontend signed URL failed, fallback to raw, error: %v", err)
		frontendURL = cosURL
	}

	// 2. 根据引擎选择识别方式
	switch engine {
	case "baidu":
		return b.ocrWithBaidu(ctx, userID, imageData, frontendURL)
	case "vision":
		return b.ocrWithVisionModel(ctx, userID, imageData, contentType, objectKey, cosURL, frontendURL)
	default:
		return "", "", fmt.Errorf("不支持的 OCR 引擎: %s", engine)
	}
}

// ocrWithBaidu 使用百度光学 OCR 识别微信聊天截图（经由 AI Gateway）
func (b *salesRAGBiz) ocrWithBaidu(ctx context.Context, userID uint, imageData []byte, frontendURL string) (string, string, error) {
	log.Printf("[OCRAnalyze] Using Baidu OCR engine, user_id: %d, image_size: %d", userID, len(imageData))

	// 获取图片宽度，用于判断文字左右位置
	imageWidth := 0
	if img, err := imaging.Decode(bytes.NewReader(imageData)); err == nil {
		imageWidth = img.Bounds().Dx()
	}

	// 注入 Gateway 中间件上下文
	ctx = billing.WithBilling(ctx, userID, "salesrag_ocr")
	ctx = aismw.WithUserID(ctx, userID)
	ctx = aiservice.WithSkipLegacyBilling(ctx)

	ocrText, err := baidu.RecognizeChatText(ctx, imageData, imageWidth)
	if err != nil {
		log.Printf("[OCRAnalyze] Baidu OCR failed, user_id: %d, error: %v", userID, err)
		return "", "", fmt.Errorf("百度 OCR 识别失败: %w", err)
	}

	log.Printf("[OCRAnalyze] Baidu OCR done, user_id: %d, text_length: %d", userID, len(ocrText))
	return ocrText, frontendURL, nil
}

// ocrWithVisionModel 使用火山引擎视觉大模型识别图片文字
func (b *salesRAGBiz) ocrWithVisionModel(ctx context.Context, userID uint, imageData []byte, contentType string, objectKey string, cosURL string, frontendURL string) (string, string, error) {
	log.Printf("[OCRAnalyze] Using vision model engine, user_id: %d, image_size: %d", userID, len(imageData))

	// 图片压缩（火山方舟模型限制：大小 < 10MB, 总像素 < 36,000,000, 长宽比 < 150:1）
	const maxVisionSize = 10 * 1024 * 1024 // 10MB
	const maxTotalPixels int64 = 36000000  // 36MP

	img, err := imaging.Decode(bytes.NewReader(imageData))
	if err != nil {
		log.Printf("[OCRAnalyze] Failed to decode image for compression check, user_id: %d, error: %v", userID, err)
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

			// 压缩后需要重新上传到 COS
			compressedKey := objectKey + "_compressed.jpg"
			_, uploadErr := util.UploadBytesToCOS(ctx, compressedKey, "image/jpeg", imageData)
			if uploadErr != nil {
				log.Printf("[OCRAnalyze] Upload compressed image failed: %v", uploadErr)
			} else {
				objectKey = compressedKey
			}

			log.Printf("[OCRAnalyze] Compression done, user_id: %d, final_size: %d, dimensions: %dx%d",
				userID, len(imageData), width, height)
		}
	}

	// 生成签名 URL 供视觉模型访问
	signedURL, err := util.GenerateSignedURL(ctx, objectKey, 600)
	if err != nil {
		log.Printf("[OCRAnalyze] Generate signed URL failed, use raw cosURL, error: %v, key: %s", err, objectKey)
		signedURL = cosURL
	}

	prompt := `你是一个专业的微信聊天记录识别专家。请严格按照以下流程识别这张微信聊天截图。

  ## 识别流程（必须按步骤执行）

  ### 第1步：识别头像位置
  - 找到截图中左侧和右侧的头像
  - **左侧头像 = 客户**，**右侧头像 = 销售**
  - 头像是判断说话人的唯一依据：每条气泡紧贴哪一侧的头像，就属于哪个人

  ### 第2步：从上到下逐条扫描气泡
  - 严格按照从上到下的顺序，逐条处理每个气泡
  - 对每条气泡，先确认它紧贴的是左侧头像还是右侧头像，再读取内容
  - 不要跳过任何气泡，不要调整顺序

  ### 第3步：提取气泡中的文字内容
  - 只输出气泡中实际可见的文字，逐字忠实转录
  - 如果气泡中有表情符号，只输出你确实在图中看到的表情，禁止添加图中不存在的表情
  - 如果某处文字模糊看不清，用 [不清晰] 标记，禁止猜测
  - 忽略图片消息、语音消息、视频消息、红包等非文字内容
  - 忽略对话中的时间戳和日期分隔线

  ## 输出格式

  每条消息一行，格式为"说话人：内容"，按截图中从上到下的原始顺序排列。

  ### 示例1：多轮对话
  客户：你们这个产品怎么样？
  销售：我们的产品在行业内评价很高，已经服务了1000+客户
  客户：价格呢？
  销售：我们有三种套餐，基础版998元起
  客户：太贵了，能便宜点吗？

  ### 示例2：单条消息
  客户：在吗？

  ## 绝对禁止

  1. **禁止凭空捏造**：不要输出图中没有的文字或表情
  2. **禁止调整顺序**：必须严格按截图从上到下的顺序输出
  3. **禁止混淆说话人**：每条气泡的归属只看它紧贴哪侧头像
  4. **禁止添加任何额外内容**：不要分析、总结、解释，只做纯转录

  现在请识别这张截图。`
	visionModel := "doubao-seed-2-0-lite-260215"
	ctx = billing.WithBilling(ctx, userID, "salesrag_ocr")

	ocrText, _, err := b.volcBiz.VisionAnalyze(ctx, signedURL, prompt, visionModel, 0, "medium")
	if err != nil {
		log.Printf("[OCRAnalyze] Volc Engine Vision OCR failed, user_id: %d, url: %s, error: %v", userID, signedURL, err)
		return "", "", fmt.Errorf("图片识别失败，请检查模型配置: %w", err)
	}

	return ocrText, frontendURL, nil
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

// enrichChunksWithDocNames 为 chunks 填充 document_name 字段（单次批量查询）
func (b *salesRAGBiz) enrichChunksWithDocNames(ctx context.Context, chunks []domain.KnowledgeChunk) {
	if len(chunks) == 0 {
		return
	}

	// 1. 收集所有唯一的 document_id
	docIDSet := make(map[uint]bool)
	for _, chunk := range chunks {
		if chunk.DocumentID > 0 {
			docIDSet[chunk.DocumentID] = true
		}
	}
	if len(docIDSet) == 0 {
		return
	}

	ids := make([]uint, 0, len(docIDSet))
	for id := range docIDSet {
		ids = append(ids, id)
	}

	// 2. 单次批量查询（替代 N+1 逐条查询）
	docs, err := b.ds.KnowledgeDocuments().GetByIDs(ctx, ids)
	if err != nil {
		log.Printf("[enrichChunksWithDocNames] batch query failed: %v", err)
		return
	}

	docIDToName := make(map[uint]string, len(docs))
	for _, doc := range docs {
		docIDToName[doc.ID] = doc.Name
	}

	// 3. 填充文档名称
	enriched := 0
	for i := range chunks {
		if name, ok := docIDToName[chunks[i].DocumentID]; ok {
			chunks[i].DocumentName = name
			enriched++
		}
	}

	log.Printf("[enrichChunksWithDocNames] %d chunks, %d docs, %d enriched", len(chunks), len(docIDSet), enriched)
}
