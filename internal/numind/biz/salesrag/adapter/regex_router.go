package adapter

import (
	"context"
	"regexp"
	"strings"

	"numind-server/internal/numind/biz/salesrag/port"
)

// RegexRouter 基于简单规则和正则的意图路由器 (v0)
// Deprecated: 请使用 LLMRouter 以获得更好的意图识别效果
type RegexRouter struct {
	// 预编译的正则
	pronouns    *regexp.Regexp
	comparisons *regexp.Regexp
	chitChat    *regexp.Regexp
}

func NewRegexRouter() *RegexRouter {
	return &RegexRouter{
		pronouns:    regexp.MustCompile(`(它|这个|那个|这款|前者|后者|it|this|that)`),
		comparisons: regexp.MustCompile(`(对比|区别|优势|vs|compare|diff)`),
		chitChat:    regexp.MustCompile(`^(你好|嗨|hello|hi|谢谢|不用了|再见|bye)$`),
	}
}

// AnalyzeIntentV2 分析用户意图并生成搜索策略（V2 接口实现）
func (r *RegexRouter) AnalyzeIntentV2(ctx context.Context, query string, history []string) (*port.IntentAnalysisResult, error) {
	intent, rewrite, err := r.AnalyzeIntent(ctx, query, history)
	if err != nil {
		return nil, err
	}

	// 映射旧意图类型到新类型
	newIntent := mapLegacyIntent(intent)

	// 构建搜索词列表
	searchQueries := []string{query}
	if rewrite != "" && rewrite != query {
		searchQueries = append(searchQueries, rewrite)
	}

	return &port.IntentAnalysisResult{
		Intent:        newIntent,
		SearchQueries: searchQueries,
		Reason:        "RegexRouter v0 分析（建议使用 LLMRouter）",
	}, nil
}

// AnalyzeIntent 分析意图（旧版接口，保持向后兼容）
func (r *RegexRouter) AnalyzeIntent(ctx context.Context, query string, history []string) (port.IntentType, string, error) {
	q := strings.TrimSpace(strings.ToLower(query))

	// 1. ChitChat
	if r.chitChat.MatchString(q) {
		return port.IntentChitChat, "", nil
	}

	// 2. Ambiguous (指代) -> 映射为 Inquiry
	if r.pronouns.MatchString(q) {
		return port.IntentAmbiguous, query, nil
	}

	// 3. Complex (对比) -> 映射为 Comparison
	if r.comparisons.MatchString(q) {
		return port.IntentComplex, query, nil
	}

	// 4. Default: Direct -> 映射为 Inquiry
	return port.IntentDirect, query, nil
}

// mapLegacyIntent 将旧意图类型映射到新的销售导向类型
func mapLegacyIntent(legacy port.IntentType) port.IntentType {
	switch legacy {
	case port.IntentChitChat:
		return port.IntentChitChat
	case port.IntentAmbiguous, port.IntentDirect:
		return port.IntentInquiry
	case port.IntentComplex:
		return port.IntentComparison
	default:
		return port.IntentInquiry
	}
}
