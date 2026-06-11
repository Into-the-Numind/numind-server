package retrieve

import (
	"context"
	"errors"

	"numind-server/internal/pkg/retrieval/domain"
)

// ErrEmptyScope 当 Scope 既未指定 DocumentIDs 也未启用 AllEnabled 时返回。
// 严格模式：不静默降级为全量检索，强制调用方明确范围。
var ErrEmptyScope = errors.New("retrieval: scope must specify DocumentIDs or AllEnabled")

// Scope 限定检索范围。DocumentIDs 与 AllEnabled 互斥语义：
// 优先使用 AllEnabled（解析为该用户全部启用文档），否则使用 DocumentIDs。
// 两者皆空（DocumentIDs 为空且 AllEnabled=false）→ Retrieve 返回 ErrEmptyScope。
type Scope struct {
	UserID      uint
	DocumentIDs []uint // 明确范围
	AllEnabled  bool   // 显式"全部启用文档"（与 DocumentIDs 互斥；agent 用）
}

// Options 控制单次检索行为。
type Options struct {
	TopK              int      // 每路召回 limit（对齐 sales_rag.go parallelSearch 现用值，默认沿用现状）
	RerankTopN        int      // 重排后保留（0=不重排）
	RewriteQuery      bool     // 是否走 QueryRewriter
	PrewrittenQueries []string // 已有改写结果直接复用（非空则跳过 rewriter；opinion 第二 scope 用，保 I1）
	History           []string // 多轮历史，传给 QueryRewriter
	BillingLabel      string   // 计费/trace 归因

	// RerankMinScore 最低 rerank 分阈值。0=用现有默认（rerankScoreThreshold 常量 0.3）；
	// >0=用此值。chatbot 通用问答传 0.6 丢弃低相关度内容；salesrag 传 0（保现状）。
	RerankMinScore float32
	// RerankNoFloor 关闭保底：true=全部 chunk 都低于阈值时返回空（不兜底 top-1）；
	// false(默认)=保留 rerank top-1（salesrag 现有行为，逐位一致）。
	// chatbot 传 true：召回全是垃圾时返回空 → 走纯聊天而非 grounding 在垃圾上。
	RerankNoFloor bool
}

// RetrievalResult 检索结果。Chunks 为去重 + 可选重排后的最终切片，
// RewriteQueries 为实际用于检索的 query 列表（含改写/HyDE），供上层组装与 trace。
type RetrievalResult struct {
	Chunks         []domain.KnowledgeChunk
	RewriteQueries []string
}

// DocStore 解析 AllEnabled scope 所需的最小依赖（biz 层用 ds.KnowledgeDocuments() 实现）。
type DocStore interface {
	ListEnabledDocIDs(ctx context.Context, userID uint) ([]uint, error)
}
