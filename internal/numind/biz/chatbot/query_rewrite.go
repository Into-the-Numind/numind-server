package chatbot

import (
	"context"
	"strings"

	"github.com/spf13/viper"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
)

// queryRewriteEnabled reports whether chatbot retrieval query-rewrite (③) is on.
// Default OFF (flag absent → false) so production is unaffected until explicitly
// enabled in config; dev enables it via features.chatbot_query_rewrite.enabled.
func queryRewriteEnabled() bool {
	return viper.GetBool("features.chatbot_query_rewrite.enabled")
}

// queryRewriteSystemPrompt is the chatbot-specific NEUTRAL declarative rewrite
// prompt. It deliberately does NOT use salesrag's sales-intent rewriter (which
// skews educational Q&A — the reason retrieve.Options.RewriteQuery stays false).
// The few-shot examples are the exact rewrites that, on the eval harness,
// recovered all three paraphrase/multi misses while keeping out_of_kb refusal
// at 1.0. Anchor preservation (rule 2) is load-bearing: over-abstracting to a
// clean topic label drops the rerank score below 0.6 and regresses recall.
const queryRewriteSystemPrompt = `你是知识库检索的查询改写器。把用户的口语化问题改写成一句简洁的【陈述式知识检索短语】，用于在企业知识库里做向量检索。

规则：
1. 去掉对话框架（"你们""我觉得""怎么办"等），保留核心信息需求。
2. **务必保留原问题里的具体领域词钩子**（如：话术、异议、陪跑服务、价格太贵、预算有限、培训机构、新人、案例 等专有/具体词）——这些词决定能否命中正确文档，绝不能抽象掉。
3. 不要添加任何原问题没有的内容、不做销售视角发挥、不编造答案。
4. 只输出改写后的检索短语本身，不要解释、不加引号。
5. 与知识库无关的问题（天气、写代码、闲聊等）照常按规则改写，不要硬往业务上靠。

示例：
用户：我觉得你们收费偏高、预算有限,你们怎么说服我?
改写：价格太贵预算有限 异议处理 销冠话术
用户：你们和市面上那些教做流量的培训机构有什么不一样?
改写：我们卖的是陪跑服务不是培训 与教做流量的培训机构的区别
用户：你们的服务都包含什么、客户常见的疑问又怎么解答?
改写：陪跑服务包含哪些内容 以及客户百问百答常见疑问解答
用户：帮我写一段 Python 快速排序代码。
改写：Python快速排序代码实现`

// rewriteQueryForRetrieval rewrites the user message into a neutral declarative
// knowledge-lookup phrase to improve retrieval recall (③). Validated on the eval
// harness (intent-declarative): recovers the paraphrase/multi misses
// (in-KB recall 0.842→1.0, MRR 0.789→0.947) WITHOUT lowering the 0.6 rerank
// threshold and WITHOUT weakening out_of_kb refusal (stays 1.0).
//
// Graceful degrade: on any error/timeout/empty/pathological output it returns the
// ORIGINAL message verbatim, so retrieval never blocks or breaks on the rewrite.
// Reuses the salesrag.intent task profile for model routing (a small/fast model),
// NOT the salesrag prompt. A dedicated chatbot.query_rewrite profile is a future
// cleanup (tracked as tech debt).
func (b *chatbotBiz) rewriteQueryForRetrieval(ctx context.Context, message string) string {
	msg := strings.TrimSpace(message)
	if msg == "" {
		return message // span not started yet → no EndSpan needed
	}

	var tc *langfuse.TraceCtx
	var spanID string
	if tc = langfuse.FromContext(ctx); tc != nil {
		spanID = langfuse.SpanID()
		langfuse.CreateSpan(tc.TraceID, spanID, "chatbot-query-rewrite",
			langfuse.WithSpanParent(tc.ParentObservationID),
			langfuse.WithSpanInput(msg),
		)
	}

	// 计费归因：调用方传的 retrieveCtx 用 pkg/middleware 的 userID key（=owner），而 aiservice
	// billing 中间件读的是另一个 struct key → 这里看到的 userID=0，改写调用不计用户积分（与同
	// retrieveCtx 上的 embed/rerank 计费行为一致，属检索侧系统成本，非用户每问加扣）。
	// 注：salesrag.intent 的 task_profile 在 DB 里带 json_mode feature，但那只是选模型的路由提示，
	// 网关不会注入 response_format，本调用输出是纯文本。
	resp, err := aiservice.Chat(ctx, profile.SalesragIntent, aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleSystem, Content: aiservice.MessageContent{Text: queryRewriteSystemPrompt}},
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: msg}},
		},
		Temperature: 0.1, // low temp → deterministic, anchor-preserving rewrites
		MaxTokens:   128,
	})
	if err != nil {
		if tc != nil {
			langfuse.EndSpan(tc.TraceID, spanID, langfuse.WithSpanError(err.Error()))
		}
		log.C(ctx).Warnw("chatbot query rewrite failed, using original query", "error", err)
		return message
	}

	rewritten := sanitizeRewrite(message, resp.Content)
	if tc != nil {
		if rewritten == message {
			langfuse.EndSpan(tc.TraceID, spanID, langfuse.WithSpanOutput("(fallback: original — empty/oversized rewrite)"))
		} else {
			langfuse.EndSpan(tc.TraceID, spanID, langfuse.WithSpanOutput(rewritten))
		}
	}
	return rewritten
}

// sanitizeRewrite returns the trimmed rewrite if it is non-empty and not runaway
// (<=200 runes), else falls back to the original message. Guards the dominant
// risk the harness surfaced: an over-abstracted or pathological rewrite that
// would tank retrieval recall. Pure (no I/O) so it is unit-testable.
func sanitizeRewrite(original, rewritten string) string {
	r := strings.TrimSpace(rewritten)
	if r == "" || len([]rune(r)) > 200 {
		return original
	}
	return r
}
