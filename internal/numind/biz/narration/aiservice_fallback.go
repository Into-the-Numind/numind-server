package narration

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/log"
)

const NarrationFallbackSystemPrompt = `你是一个工具调用动作翻译员。把工具调用的状态翻译为面向中文学员的友好提示。
输入：工具名 + 状态 + 细节。
输出格式：动词|细节（≤8 字 + ≤16 字，用 | 分隔；不要加引号；不要解释）
示例：
- 工具：bash_exec，状态：use，细节：cat /etc/hosts → 查询|系统文件
- 工具：web_search，状态：result，细节："tutorial" → 完成|搜索教程`

// AiserviceLLMFallback uses aiservice.Chat for dynamic narration translation
// when the static yaml renderer misses a tool/state combination.
// Cache is sync.Map (thread-safe for concurrent Run() calls — S1-D7 / S1 reviewer P2-6).
type AiserviceLLMFallback struct {
	cache sync.Map // key="tool:state" → [2]string{verb, detail}
}

// NewAIServiceLLMFallback returns a production LLMFallback that calls aiservice
// with a 200ms timeout; on timeout falls back to stubFallbackFor verbiage
// (fail-allow direction — UX-prioritized, deliberately asymmetric with A6 fail-deny).
func NewAIServiceLLMFallback() LLMFallback {
	return &AiserviceLLMFallback{}
}

// Render satisfies LLMFallback. Never returns error (S1-D12).
func (f *AiserviceLLMFallback) Render(ctx context.Context, toolName string, state State, payload EmitPayload) (verb, detail string) {
	cacheKey := toolName + ":" + string(state)
	if v, ok := f.cache.Load(cacheKey); ok {
		cached := v.([2]string)
		return cached[0], cached[1]
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()

	// EmitPayload has no plain-text detail field; derive hint from OverrideMessage
	// if populated (reserved for #14 LLM-supplied narration), otherwise leave empty.
	detailText := payload.OverrideMessage

	resp, err := chatFn(timeoutCtx, profile.AgentNarrationFallback, aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleSystem, Content: aiservice.MessageContent{Text: NarrationFallbackSystemPrompt}},
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: fmt.Sprintf("工具：%s，状态：%s，细节：%s", toolName, state, detailText)}},
		},
		ModelOverride: "qwen-turbo",
		MaxTokens:     50,
		Temperature:   0.3,
	})
	if err != nil || timeoutCtx.Err() != nil {
		log.Debugw("narration LLM fallback timeout — using stub", "tool", toolName, "state", state)
		return stubFallbackFor(toolName, state)
	}

	verb, detail = parseNarrationContent(resp.Content, toolName)
	f.cache.Store(cacheKey, [2]string{verb, detail})
	return verb, detail
}

// chatFn is a package-private seam to enable unit test mocking of aiservice.Chat.
var chatFn = aiservice.Chat

// parseNarrationContent splits "动词|细节" output; falls back to (trimmed raw, toolName) on bad format.
func parseNarrationContent(raw, toolName string) (verb, detail string) {
	parts := strings.SplitN(strings.TrimSpace(raw), "|", 2)
	if len(parts) == 2 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	}
	return strings.TrimSpace(raw), toolName
}

// stubFallbackFor returns the same verbiage as stubLLMFallback for failure cases.
// Defined here to avoid an import cycle with translator.go's stubLLMFallback type.
func stubFallbackFor(toolName string, state State) (string, string) {
	switch state {
	case StateUse, StateQueued:
		return "正在执行", toolName
	case StateResult:
		return "完成", toolName
	case StateError:
		return "执行出错", toolName
	case StateRejected:
		return "操作被拦截", toolName
	default:
		return "处理中", toolName
	}
}
