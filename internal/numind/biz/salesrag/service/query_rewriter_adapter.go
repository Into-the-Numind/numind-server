package service

import (
	"context"

	sport "numind-server/internal/numind/biz/salesrag/port" // IntentRouter（销售意图，未搬迁）
	retrievalport "numind-server/internal/pkg/retrieval/port"
)

// chatModeCtxKey 是把 per-request chatMode 透传给 routerRewriter 的 context key。
//
// 底座 retrieval.port.QueryRewriter.Rewrite 的签名是领域无关的
// （ctx, query, history），不含销售特有的 chatMode；而 salesrag 的
// IntentRouter.AnalyzeIntentV2 需要 chatMode（"sales"/"free" 会改变 LLM 改写
// prompt 的行为）。SalesRAGService 在构造时把单个 retrieve.Service + routerRewriter
// 装好（chatMode 还未知），故 chatMode 在每次 RetrieveForResponseV2 时经
// context 注入，routerRewriter.Rewrite 时取出。缺省（未注入）等价于 "sales"，
// 与 IntentRouter.AnalyzeIntent 旧接口的默认一致。
type chatModeCtxKey struct{}

// withChatMode 把 chatMode 注入 context，供 routerRewriter.Rewrite 提取。
func withChatMode(ctx context.Context, chatMode string) context.Context {
	return context.WithValue(ctx, chatModeCtxKey{}, chatMode)
}

// chatModeFromCtx 提取 chatMode；未注入时返回 fallback（调用方提供的默认模式）。
// salesrag 主干传 "sales"（与旧默认一致）；chatbot 经 NewRouterRewriter 传 "free"。
func chatModeFromCtx(ctx context.Context, fallback string) string {
	if v, ok := ctx.Value(chatModeCtxKey{}).(string); ok && v != "" {
		return v
	}
	if fallback != "" {
		return fallback
	}
	return "sales"
}

// routerRewriter 把 salesrag 的 IntentRouter（含 AnalyzeIntentV2）适配为底座的
// retrieval/port.QueryRewriter。它丢弃销售专属返回字段（Intent / SalesInstruction /
// CustomerMessage / Reason），只取通用主干需要的 SearchQueries + HyDEQuery。
//
// 关键：底座 RewriteResult.Queries 必须 = 原 RetrieveForResponseV2 喂给
// parallelSearch 的那批 query 之「改写部分」（即 intentResult.SearchQueries），
// HyDE 的追加由底座 determineQueries 负责（res.HyDE 非空时 append 到末尾）。
// 这样「适配器 Queries(=SearchQueries) + 底座追加 HyDE」组合后产出的 query 列表
// 与原主干 allSearchQueries（intentResult.SearchQueries + HyDE）完全相同、同序，
// 保 T1.6 逐位一致。
type routerRewriter struct {
	router sport.IntentRouter
	// defaultChatMode 是 context 未注入 chatMode 时的回退模式。salesrag 主干留空
	// （等价 "sales"）；chatbot 经 NewRouterRewriter 传 "free"，避免销售话术 prompt
	// 污染纯知识库问答的 query 改写。
	defaultChatMode string
}

// NewRouterRewriter 把 salesrag 的 IntentRouter 适配为底座 retrieval/port.QueryRewriter，
// 供 salesrag 之外的调用方（如 chatbot）复用改写主干。
//
// defaultChatMode 是 context 未注入 chatMode 时的回退模式：
//   - chatbot 传 "free"（自由讨论模式，不套销售话术）
//   - 传空 等价 "sales"（与 salesrag 内部默认一致）
//
// 注意：若调用链经 withChatMode 注入了 chatMode，仍以 context 值优先（defaultChatMode
// 仅作回退）。chatbot 不注入 chatMode，故始终走 defaultChatMode="free"。
func NewRouterRewriter(router sport.IntentRouter, defaultChatMode string) retrievalport.QueryRewriter {
	return routerRewriter{router: router, defaultChatMode: defaultChatMode}
}

// Rewrite 实现 retrieval/port.QueryRewriter，委托 IntentRouter.AnalyzeIntentV2。
// chatMode 优先取 context 注入值（RetrieveForResponseV2 注入），否则用 defaultChatMode，
// 仍为空则回退 "sales"。
func (a routerRewriter) Rewrite(ctx context.Context, query string, history []string) (retrievalport.RewriteResult, error) {
	res, err := a.router.AnalyzeIntentV2(ctx, query, history, chatModeFromCtx(ctx, a.defaultChatMode))
	if err != nil {
		return retrievalport.RewriteResult{}, err
	}
	return retrievalport.RewriteResult{
		Queries: res.SearchQueries,
		HyDE:    res.HyDEQuery,
	}, nil
}
