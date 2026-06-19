// Package rag 提供领域无关、标准化、可被所有 RAG 消费方（chatbot / salesrag / agent）
// 统一调用的查询改写器与可答性门。
//
//   - UniversalRewriter：一个中性 prompt、一次 LLM 调用，输出 {指代补全 + 多路改写 + HyDE}，
//     替代各业务各自为政的销售意图改写。负责"拉满召回"。
//   - Gate（gate.go）：可答性判定，负责"兜住拒答"。
//
// 两者通过 feature flag 控制（默认关 = 不改变现状），可独立开启灰度。
package rag

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/spf13/viper"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/retrieval/port"
)

// FlagUniversalRewriter 控制是否启用通用改写器。关（默认/缺省）→ 走 fallback（各消费方原改写器）。
const FlagUniversalRewriter = "features.universal_rewriter.enabled"

// rewriterPrompt 中性、与业务无关的查询改写指令。一次输出三件事。
const rewriterPrompt = `你是检索查询改写器，与具体业务无关。根据【用户当前问题】和【最近对话】，输出严格 JSON：
{
  "completed_query": "用对话历史把当前问题里的指代词/省略补全成可独立理解的完整问题；无需补全则原样返回",
  "search_queries": ["2~3 条用于向量检索的改写，覆盖不同问法/同义表达，但只能围绕问题本身的关键实体和意图；严禁引入问题中不存在的新主题、产品名或宽泛类别词"],
  "hyde": "一段 2~3 句、像知识库条目那样直接回答该问题的假设性文字，用于语义匹配"
}
只输出 JSON，不要任何解释。`

// rewriterOut 是 LLM 输出的解析目标。
type rewriterOut struct {
	CompletedQuery string   `json:"completed_query"`
	SearchQueries  []string `json:"search_queries"`
	HyDE           string   `json:"hyde"`
}

// UniversalRewriter 实现 port.QueryRewriter。通过 aiservice 统一入口（salesrag.intent 任务画像
// → deepseek-v4-flash 非思考）调用，绝不裸 HTTP；出错时 fail-soft 返回原 query。
type UniversalRewriter struct{}

// NewUniversalRewriter 构造通用改写器。
func NewUniversalRewriter() *UniversalRewriter { return &UniversalRewriter{} }

// Rewrite 实现 port.QueryRewriter。
func (r *UniversalRewriter) Rewrite(ctx context.Context, query string, history []string) (port.RewriteResult, error) {
	raw := port.RewriteResult{Queries: []string{query}}

	userMsg := "【用户当前问题】\n" + query
	if len(history) > 0 {
		userMsg = "【最近对话】\n" + strings.Join(history, "\n") + "\n\n" + userMsg
	}
	resp, err := aiservice.Chat(ctx, profile.SalesragIntent, aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleSystem, Content: aiservice.MessageContent{Text: rewriterPrompt}},
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: userMsg}},
		},
		Temperature:    0.1,
		MaxTokens:      800,
		ResponseFormat: aiservice.ResponseFormatJSONObject,
	})
	if err != nil {
		log.C(ctx).Warnw("universal rewriter LLM failed, fallback to raw query", "error", err)
		return raw, nil
	}

	var out rewriterOut
	if jerr := json.Unmarshal([]byte(extractJSON(resp.Content)), &out); jerr != nil {
		log.C(ctx).Warnw("universal rewriter JSON parse failed, fallback to raw query", "error", jerr, "resp", resp.Content)
		return raw, nil
	}

	// 组装检索 query：补全后的问题 + 多路改写 + 原问题（去重保序，原问题始终在内做兜底）。
	queries := dedupeNonEmpty(append(append([]string{out.CompletedQuery}, out.SearchQueries...), query))
	if len(queries) == 0 {
		queries = []string{query}
	}
	if len(queries) > 4 { // 补全1 + 改写3 上限，防扇出爆炸
		queries = queries[:4]
	}
	return port.RewriteResult{Queries: queries, HyDE: strings.TrimSpace(out.HyDE)}, nil
}

// FlaggedRewriter 按 feature flag 在 primary（通用改写器）与 fallback（各消费方原改写器）之间切换。
// flag 关时行为与改造前逐位一致（fallback 为 nil 则返回原 query，等价于不改写）。
type FlaggedRewriter struct {
	primary  port.QueryRewriter
	fallback port.QueryRewriter // 可为 nil → flag 关时返回原 query
}

// NewFlaggedRewriter 构造切换器。fallback 传各消费方现有改写器以保 flag 关时零行为变化。
func NewFlaggedRewriter(primary, fallback port.QueryRewriter) *FlaggedRewriter {
	return &FlaggedRewriter{primary: primary, fallback: fallback}
}

// Rewrite 实现 port.QueryRewriter，按 flag 逐调用决定走 primary 还是 fallback。
func (f *FlaggedRewriter) Rewrite(ctx context.Context, query string, history []string) (port.RewriteResult, error) {
	if viper.GetBool(FlagUniversalRewriter) && f.primary != nil {
		return f.primary.Rewrite(ctx, query, history)
	}
	if f.fallback != nil {
		return f.fallback.Rewrite(ctx, query, history)
	}
	return port.RewriteResult{Queries: []string{query}}, nil
}

// dedupeNonEmpty 去重 + 去空白，保持出现顺序。
func dedupeNonEmpty(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// extractJSON 从可能带 ```json 围栏或前后噪声的文本中提取第一个 JSON 对象。
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j > i {
			return s[i : j+1]
		}
	}
	return s
}
