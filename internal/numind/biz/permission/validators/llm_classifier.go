package validators

import (
	"context"
	"strings"
	"time"

	"numind-server/internal/numind/biz/permission"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/log"
)

// PermissionClassifierSystemPrompt is the system prompt for the L3 auto-mode
// LLM classifier.  Deliberately terse to stay within MaxTokens=5 response budget.
const PermissionClassifierSystemPrompt = `你是一个工具调用权限分类器。判断这个工具调用是否需要学员明确确认才能执行。
- 输出仅一个词：confirm 或 allow
- confirm：销毁性操作（rm/drop/delete/format）、外部网络写入、隐私数据访问
- allow：只读查询、计算、本地读取`

// LLMClassifier is the M-A7 abstraction for auto-mode permission decisions.
// Implementations decide whether a tool call needs explicit learner confirmation.
type LLMClassifier interface {
	Classify(ctx context.Context, toolName, args string) (needsConfirm bool, err error)
}

// AiserviceLLMClassifier is the production LLMClassifier backed by
// aiservice.Chat (Agent Mode #14/A7). Used by the L3 auto-mode validator.
type AiserviceLLMClassifier struct{}

// NewAIServiceLLMClassifier returns a production classifier with 250ms timeout
// and fail-allow direction (timeout returns false — UX-prioritized, deliberately
// asymmetric with A6 fail-deny — S0-D12).
func NewAIServiceLLMClassifier() LLMClassifier {
	return &AiserviceLLMClassifier{}
}

// Classify satisfies LLMClassifier.
// It issues a qwen-turbo call with a 250 ms deadline.
// On timeout or any error it returns (false, nil) — fail-allow — to avoid
// blocking the learner when the classifier is unavailable.
func (c *AiserviceLLMClassifier) Classify(ctx context.Context, toolName, args string) (bool, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()

	resp, err := chatFn(timeoutCtx, profile.AgentPermissionCheck, aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{
				Role:    aiservice.MessageRoleSystem,
				Content: aiservice.MessageContent{Text: PermissionClassifierSystemPrompt},
			},
			{
				Role:    aiservice.MessageRoleUser,
				Content: aiservice.MessageContent{Text: "工具：" + toolName + "，参数：" + args},
			},
		},
		ModelOverride: "qwen-turbo",
		MaxTokens:     5,
		Temperature:   0.0,
	})
	if err != nil || timeoutCtx.Err() != nil {
		log.Warnw("permission LLM classifier timeout or error — fail-allow",
			"tool", toolName,
			"error", err,
			"ctx_err", timeoutCtx.Err())
		return false, nil // fail-allow: don't block user on classifier failure
	}

	answer := strings.TrimSpace(strings.ToLower(resp.Content))
	return strings.HasPrefix(answer, "confirm"), nil
}

// chatFn is a package-level variable used as the call seam so that unit tests
// can replace aiservice.Chat with a stub without importing the real gateway.
var chatFn = aiservice.Chat

// AutoModeLLMValidator is the L3 auto-mode permission validator.
// It uses an LLMClassifier to decide whether to Ask (require confirmation) or
// Passthrough (let the downstream pipeline decide).
//
// Wire this into the permission pipeline after L1/L2 hard-rule validators so
// that it only fires when no earlier validator has already Allow/Deny-ed.
//
// biz.go wire deferred to M-A-wire task.
type AutoModeLLMValidator struct {
	classifier LLMClassifier
}

// NewAutoModeLLMValidator returns an L3 validator backed by the given LLMClassifier.
func NewAutoModeLLMValidator(classifier LLMClassifier) permission.Validator {
	return &AutoModeLLMValidator{classifier: classifier}
}

func (v *AutoModeLLMValidator) ID() string { return "AutoModeLLMValidator" }

// Validate classifies the tool call and returns Ask when the classifier says
// confirmation is required, or Passthrough otherwise.
// On classifier failure the validator passes through (fail-allow).
func (v *AutoModeLLMValidator) Validate(ctx context.Context, req permission.PermissionRequest) permission.PermissionResult {
	if v.classifier == nil || req.Tool == nil {
		return permission.Passthrough(v.ID(), permission.DecisionReasonClassifier, "no classifier or nil tool")
	}

	needsConfirm, err := v.classifier.Classify(ctx, req.Tool.Name(), req.InputJSON)
	if err != nil {
		// fail-allow: classifier returned an error (unlikely given fail-allow in
		// AiserviceLLMClassifier, but defensive)
		return permission.Passthrough(v.ID(), permission.DecisionReasonClassifier, "classifier error fail-allow")
	}

	if needsConfirm {
		return permission.Ask(v.ID(), permission.DecisionReasonClassifier, "LLM classifier: 该操作需要学员确认")
	}
	return permission.Passthrough(v.ID(), permission.DecisionReasonClassifier, "LLM classifier: allow")
}
