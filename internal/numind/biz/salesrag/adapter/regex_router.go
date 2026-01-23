package adapter

import (
	"context"
	"regexp"
	"strings"

	"numind-server/internal/numind/biz/salesrag/port"
)

// RegexRouter 基于简单规则和正则的意图路由器 (v0)
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
		chitChat:    regexp.MustCompile(`^(你好|嗨|hello|hi|谢谢|不再见|bye)$`),
	}
}

// AnalyzeIntent 分析意图
func (r *RegexRouter) AnalyzeIntent(ctx context.Context, query string, history []string) (port.IntentType, string, error) {
	q := strings.TrimSpace(strings.ToLower(query))

	// 1. ChitChat
	if r.chitChat.MatchString(q) {
		return port.IntentChitChat, "", nil
	}

	// 2. Ambiguous (指代)
	if r.pronouns.MatchString(q) {
		// 在真实场景中，这里应该调用 LLM 进行 Rewrite
		// 这里 v0 版本直接返回 Ambiguous 状态，交由上层处理
		return port.IntentAmbiguous, query, nil
	}

	// 3. Complex (对比)
	if r.comparisons.MatchString(q) {
		return port.IntentComplex, query, nil
	}

	// 4. Default: Direct
	return port.IntentDirect, query, nil
}
