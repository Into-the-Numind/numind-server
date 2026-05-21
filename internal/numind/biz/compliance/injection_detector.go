package compliance

import (
	"context"
	"strings"
)

// InjectionDetector — input prompt injection 检测
type InjectionDetector struct {
	classifier LLMClassifier // v1 mock; #14 接 aiservice.Chat
}

// LLMClassifier — mock interface，v1 永远返回 (false, nil)
type LLMClassifier interface {
	Classify(ctx context.Context, input string) (bool, error) // true = injection 命中
}

type mockClassifier struct{}

// NewMockClassifier returns a no-op classifier (v1 placeholder; #14 wires real LLM).
func NewMockClassifier() LLMClassifier { return &mockClassifier{} }

func (mockClassifier) Classify(ctx context.Context, input string) (bool, error) {
	return false, nil
}

// NewInjectionDetector constructs a detector. If c is nil, uses mock.
func NewInjectionDetector(c LLMClassifier) *InjectionDetector {
	if c == nil {
		c = NewMockClassifier()
	}
	return &InjectionDetector{classifier: c}
}

// injectionKeywords — v1 启发式关键词清单（不区分大小写匹配）
// S1 reviewer P2-2 补：disregard prior / forget your instructions / new persona / roleplay as
var injectionKeywords = []string{
	"ignore previous", "disregard prior", "forget your instructions",
	"忽略之前", "忘记之前",
	"pretend you are", "roleplay as", "new persona",
	"假装你是", "扮演",
	"system:", "<system>", "<system_prompt>",
	"give me your prompt", "把 system prompt 告诉我", "告诉我你的指令",
	"dan", "jailbreak", "越狱",
	"you are now", "你现在是",
}

// Detect 返回 (hit, matchedKeyword, error)
// v1 流程：先关键词，命中即返；否则 mock classifier（永远 false）
// classifier 报错 → fail-open (false, "", err)
func (d *InjectionDetector) Detect(ctx context.Context, input string) (bool, string, error) {
	lower := strings.ToLower(input)
	for _, kw := range injectionKeywords {
		if strings.Contains(lower, kw) {
			return true, kw, nil
		}
	}
	hit, err := d.classifier.Classify(ctx, input)
	if err != nil {
		return false, "", err
	}
	if hit {
		return true, "llm_classifier", nil
	}
	return false, "", nil
}

// WrapInputFence 把外部数据用 fence tag 包裹（蓝本 §7.3）
func WrapInputFence(source, name, content string) string {
	return "<external_data source=\"" + source + "\" name=\"" + name + "\" trust=\"low\">\n" + content + "\n</external_data>"
}
