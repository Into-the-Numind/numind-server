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

// chatModeFromCtx 提取 chatMode；未注入时返回 "sales"（与旧默认一致）。
func chatModeFromCtx(ctx context.Context) string {
	if v, ok := ctx.Value(chatModeCtxKey{}).(string); ok && v != "" {
		return v
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
}

// Rewrite 实现 retrieval/port.QueryRewriter，委托 IntentRouter.AnalyzeIntentV2。
// chatMode 经 context 透传（RetrieveForResponseV2 注入），默认 "sales"。
func (a routerRewriter) Rewrite(ctx context.Context, query string, history []string) (retrievalport.RewriteResult, error) {
	res, err := a.router.AnalyzeIntentV2(ctx, query, history, chatModeFromCtx(ctx))
	if err != nil {
		return retrievalport.RewriteResult{}, err
	}
	return retrievalport.RewriteResult{
		Queries: res.SearchQueries,
		HyDE:    res.HyDEQuery,
	}, nil
}
