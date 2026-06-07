package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/compactv2"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
)

// adapter_compactv2_test.go — V2 compact 接入 adapter.Generate 的集成单测。
//
// 测试维度（spec §设计要点 + 板块 2 验证场景）：
//  1. 阈值未到时不压缩（pass-through）
//  2. 阈值到达时 autocompact LLM 摘要并替换 in
//  3. aiservice prompt_too_long 错误时 ForcePTLRecover 自愈
//  4. PTL recovery 第二次仍 PTL → 不再 retry，原错误冒泡
//  5. 连续 3 次摘要失败 + ratio >= HardLimitRatio(0.85) → ErrContextExhausted（hard limit）
//  6. compactor == nil 时彻底跳过 V2 逻辑（行为完全等价集成前）
//  7. isPromptTooLongErr 关键词匹配覆盖主流 provider 措辞

// ── helper: build messages with specific token estimate ─────────────────────

// buildLongConversation 构造一组 messages，总 token 估算约 ~targetTokens（按 char/4）。
// 用于触发 compact 阈值。每条 message 0.5KB 内容，按 system + user + alternating
// assistant/tool 排列，让 alignToolCallPairs 有真实场景可对齐。
func buildLongConversation(targetTokens int) []*schema.Message {
	// 每条消息 ~2KB content = ~500 tokens
	chunk := strings.Repeat("A中文B", 400) // 400 * 5 chars = 2000 chars ≈ 500 tokens
	// 至少 9 条 messages（system + user + 7 assistant），保 alignToolCallPairs / autocompact
	// 都有足够材料工作（compactv2.AutocompactPreserveRecentMessages+2 = 7 阈值之上）。
	wantMsgs := (targetTokens / 500) + 2
	if wantMsgs < 7 {
		wantMsgs = 7
	}

	msgs := []*schema.Message{
		{Role: schema.System, Content: "你是 agent"},
		{Role: schema.User, Content: chunk},
	}
	for i := 0; i < wantMsgs; i++ {
		msgs = append(msgs,
			&schema.Message{Role: schema.Assistant, Content: chunk},
		)
	}
	return msgs
}

// withMockChat 替换 chatFn 包级 seam，t.Cleanup 自动恢复。
func withMockChat(t *testing.T, mock func(ctx context.Context, taskID string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error)) {
	t.Helper()
	orig := chatFn
	chatFn = mock
	t.Cleanup(func() { chatFn = orig })
}

// validSummary 是符合 D5 XML 包裹规范的最小合规 summary string。
const validSummary = `<reference-only data-internal="true">
[CONTEXT COMPACTION — REFERENCE ONLY]
## 1. Active Task
test summary
## 2. Goal

## 3. Constraints

## 4. Completed Actions

## 5. Active State

## 6. In Progress

## 7. Blocked

## 8. Key Decisions

## 9. Resolved Questions

## 10. Pending User Asks

## 11. Relevant Files / Artifacts

## 12. Critical Context
</reference-only>`

// ── tests ──────────────────────────────────────────────────────────────────

func TestAdapter_NoCompactBelowThreshold(t *testing.T) {
	withMockChat(t, func(_ context.Context, taskID string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		// 阈值未到 → 应直接走 AgentRun，不应有 AgentCompact 调用
		assert.NotEqual(t, profile.AgentCompact, taskID, "未到阈值不应该调摘要 LLM")
		return &aiservice.ChatResponse{Content: "answer"}, nil
	})

	adapter := &aiserviceAdapter{taskID: profile.AgentRun, compactor: newAdapterCompactor(1_000_000)}
	in := buildLongConversation(1_000) // ~1K tokens vs 1M window → ratio 0.001%

	out, err := adapter.Generate(context.Background(), in)
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "answer", out.Content)
}

func TestAdapter_AutocompactTriggersAboveThreshold(t *testing.T) {
	callIdx := 0
	withMockChat(t, func(_ context.Context, taskID string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		callIdx++
		switch callIdx {
		case 1:
			// First call: summarize. Should hit AgentCompact profile.
			assert.Equal(t, profile.AgentCompact, taskID, "第一次调用应当是摘要")
			return &aiservice.ChatResponse{Content: validSummary}, nil
		case 2:
			// Second call: actual ReAct LLM after compaction. Should be AgentRun.
			assert.Equal(t, profile.AgentRun, taskID, "压缩后应当是 AgentRun")
			// 验证 in 被替换成了 [system, summary, recent...]
			require.GreaterOrEqual(t, len(req.Messages), 3, "至少 system + summary + recent 5 messages")
			// summary 应当是 system role 含 <reference-only> 包裹
			foundSummary := false
			for _, m := range req.Messages {
				if m.Role == aiservice.MessageRoleSystem && strings.Contains(m.Content.Text, compactv2.AutocompactOpenTag) {
					foundSummary = true
					break
				}
			}
			assert.True(t, foundSummary, "压缩后 messages 应当含 reference-only summary")
			return &aiservice.ChatResponse{Content: "compacted answer"}, nil
		}
		t.Fatalf("unexpected 3rd chatFn call")
		return nil, nil
	})

	// 1K context window，conversation 估算 ~10K tokens → ratio ~1000% → 远超触发线 0.70
	adapter := &aiserviceAdapter{taskID: profile.AgentRun, compactor: newAdapterCompactor(1_000)}
	in := buildLongConversation(5_000)

	out, err := adapter.Generate(context.Background(), in)
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "compacted answer", out.Content)
	assert.Equal(t, 2, callIdx, "应当调了 2 次 LLM（摘要 + 实际 ReAct）")
	// compactor 失败计数应当复位
	assert.Equal(t, 0, adapter.compactor.consecutiveFailures)
}

// TestAdapter_AutocompactTriggersInLoweredBand pins the T7 (#8) threshold drop
// 0.85 → 0.70: a conversation whose estimated/window ratio lands in [0.70, 0.85)
// now triggers L3 autocompact, whereas under the old 0.85 threshold it would have
// passed through un-compacted — risking prompt_too_long once real Chinese tokens
// exceed the byte/4 estimate (prod p90 calibration_ratio=1.51×). This test FAILS
// against the old 0.85 threshold (case-1 would be AgentRun, not AgentCompact).
func TestAdapter_AutocompactTriggersInLoweredBand(t *testing.T) {
	callIdx := 0
	withMockChat(t, func(_ context.Context, taskID string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		callIdx++
		switch callIdx {
		case 1:
			assert.Equal(t, profile.AgentCompact, taskID, "ratio∈[0.70,0.85) 应触发摘要（旧 0.85 阈值下不会）")
			return &aiservice.ChatResponse{Content: validSummary}, nil
		case 2:
			assert.Equal(t, profile.AgentRun, taskID, "压缩后应当是 AgentRun")
			return &aiservice.ChatResponse{Content: "compacted answer"}, nil
		}
		t.Fatalf("unexpected 3rd chatFn call")
		return nil, nil
	})

	// buildLongConversation(1000) → clamps to 7 assistant + 1 user chunk (8×3200B) +
	// system ≈ 6403 estimated tokens. window 8500 → ratio ≈ 0.753, inside [0.70,0.85).
	adapter := &aiserviceAdapter{taskID: profile.AgentRun, compactor: newAdapterCompactor(8_500)}
	in := buildLongConversation(1_000)

	out, err := adapter.Generate(context.Background(), in)
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "compacted answer", out.Content)
	assert.Equal(t, 2, callIdx, "应当调了 2 次 LLM（摘要 + 实际 ReAct）")
}

func TestAdapter_PTLRecoverySucceeds(t *testing.T) {
	callIdx := 0
	withMockChat(t, func(_ context.Context, taskID string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		callIdx++
		switch callIdx {
		case 1:
			// First call: AgentRun returns PTL
			assert.Equal(t, profile.AgentRun, taskID)
			return nil, errors.New("prompt_too_long: context window exceeded")
		case 2:
			// Second call: ForcePTLRecover 触发的摘要
			assert.Equal(t, profile.AgentCompact, taskID, "PTL recovery 应当触发摘要")
			return &aiservice.ChatResponse{Content: validSummary}, nil
		case 3:
			// Third call: retry with compacted in
			assert.Equal(t, profile.AgentRun, taskID, "压缩后 retry AgentRun")
			return &aiservice.ChatResponse{Content: "recovered answer"}, nil
		}
		t.Fatalf("unexpected 4th chatFn call")
		return nil, nil
	})

	// context window 极大，prevention 不会触发；PTL 错误是 LLM 返回的（模拟 token 估算错误）
	adapter := &aiserviceAdapter{taskID: profile.AgentRun, compactor: newAdapterCompactor(1_000_000)}
	in := buildLongConversation(1_000) // ratio 极低，prevention 不触发

	out, err := adapter.Generate(context.Background(), in)
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "recovered answer", out.Content)
	assert.Equal(t, 3, callIdx)
	assert.Equal(t, 0, adapter.compactor.consecutiveFailures, "recovery 成功应当复位 failure 计数")
}

func TestAdapter_PTLRecoveryFailsBubblesOriginalError(t *testing.T) {
	callIdx := 0
	withMockChat(t, func(_ context.Context, taskID string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		callIdx++
		// 所有调用都返回 PTL（包括摘要本身也 fail）
		return nil, errors.New("context_length_exceeded: too long")
	})

	adapter := &aiserviceAdapter{taskID: profile.AgentRun, compactor: newAdapterCompactor(1_000_000)}
	in := buildLongConversation(1_000)

	_, err := adapter.Generate(context.Background(), in)
	require.Error(t, err)
	// 摘要调用本身 PTL → ForcePTLRecover 返回错误（包装的 chat err），caller 上抛
	// 验证原 PTL 字串保留在 error chain 里
	assert.Contains(t, err.Error(), "context_length_exceeded", "原 LLM error 应当在 chain 里")
	// 至少调了 2 次：第 1 次 AgentRun PTL；第 2 次摘要也 PTL
	assert.GreaterOrEqual(t, callIdx, 2)
	// failure counter +1
	assert.Equal(t, 1, adapter.compactor.consecutiveFailures)
}

func TestAdapter_HardLimit_ErrContextExhausted(t *testing.T) {
	// Pre-set compactor to 3 consecutive failures
	c := newAdapterCompactor(1_000)
	c.consecutiveFailures = 3

	withMockChat(t, func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		t.Fatalf("hard limit + 3 fails 时不应当调 LLM")
		return nil, nil
	})

	adapter := &aiserviceAdapter{taskID: profile.AgentRun, compactor: c}
	// ratio 远超 95%
	in := buildLongConversation(10_000) // 10K tokens vs 1K window → ratio 1000%

	_, err := adapter.Generate(context.Background(), in)
	require.Error(t, err)
	assert.ErrorIs(t, err, compactv2.ErrContextExhausted, "应当返回 ErrContextExhausted")
}

func TestAdapter_NilCompactor_FullPassthrough(t *testing.T) {
	callIdx := 0
	withMockChat(t, func(_ context.Context, taskID string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		callIdx++
		assert.Equal(t, profile.AgentRun, taskID, "nil compactor 不应当跑摘要")
		return &aiservice.ChatResponse{Content: "direct answer"}, nil
	})

	adapter := &aiserviceAdapter{taskID: profile.AgentRun /* compactor: nil */}
	in := buildLongConversation(100_000) // 极大也无所谓，nil compactor 全跳过

	out, err := adapter.Generate(context.Background(), in)
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "direct answer", out.Content)
	assert.Equal(t, 1, callIdx, "nil compactor → 只调一次 LLM")
}

func TestAdapter_ShortConversation_NoCompact(t *testing.T) {
	// 阈值到达但 messages 太短（< AutocompactPreserveRecentMessages+2 = 7）→ 不压缩
	callIdx := 0
	withMockChat(t, func(_ context.Context, taskID string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		callIdx++
		assert.Equal(t, profile.AgentRun, taskID, "短消息应直接走 AgentRun")
		return &aiservice.ChatResponse{Content: "answer"}, nil
	})

	// 6 messages，长度上 ratio 应到 100%，但 < 7 messages 不压缩
	huge := strings.Repeat("X", 10_000)
	adapter := &aiserviceAdapter{taskID: profile.AgentRun, compactor: newAdapterCompactor(100)}
	in := []*schema.Message{
		{Role: schema.System, Content: huge},
		{Role: schema.User, Content: huge},
		{Role: schema.Assistant, Content: huge},
		{Role: schema.User, Content: huge},
		{Role: schema.Assistant, Content: huge},
		{Role: schema.User, Content: huge},
	}

	out, err := adapter.Generate(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, "answer", out.Content)
	assert.Equal(t, 1, callIdx, "短消息不压缩，只调一次 LLM")
}

// ── isPromptTooLongErr keyword coverage ──────────────────────────────────────

func TestIsPromptTooLongErr_KnownProviderMessages(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"openai_canonical", errors.New("context_length_exceeded: This model's maximum context length is 8192 tokens"), true},
		{"openai_natural", errors.New("Your prompt is too long, please reduce input"), true},
		{"dashscope_input_range", errors.New("Range of input length should be [1, 32768]: input range exceeded"), true},
		{"ark_prompt_too_long", errors.New("prompt_too_long: 50000 tokens"), true},
		{"ark_max_token", errors.New("max_token limit reached"), true},
		{"deepseek_capital", errors.New("Maximum context length of 32768 was exceeded by your input"), true},
		{"generic_exceeds_max", errors.New("input exceeds the maximum context window"), true},

		{"network_timeout", errors.New("dial tcp: i/o timeout"), false},
		{"auth_401", errors.New("HTTP 401 Unauthorized"), false},
		{"empty", nil, false},
		{"random", errors.New("something went wrong"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isPromptTooLongErr(tc.err))
		})
	}
}

// ── alignToolCallPairsEino ───────────────────────────────────────────────────

func TestAlignToolCallPairsEino_SimpleNoToolCalls(t *testing.T) {
	in := []*schema.Message{
		{Role: schema.System, Content: "sys"},
		{Role: schema.User, Content: "u1"},
		{Role: schema.Assistant, Content: "a1"},
		{Role: schema.User, Content: "u2"},
		{Role: schema.Assistant, Content: "a2"},
	}
	// 纯文本对话，cut=3 应当不变
	assert.Equal(t, 3, alignToolCallPairsEino(in, 3))
}

func TestAlignToolCallPairsEino_CutOnDanglingTool(t *testing.T) {
	in := []*schema.Message{
		{Role: schema.System},
		{Role: schema.User, Content: "u"},
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "tc1"}}},
		{Role: schema.Tool, ToolCallID: "tc1", Content: "result"},
		{Role: schema.Assistant, Content: "final"},
	}
	// cut=3 指向 tool reply → 向前推到 cut=2（assistant.ToolCalls 也归入压缩）
	got := alignToolCallPairsEino(in, 3)
	assert.LessOrEqual(t, got, 2, "cut 应当回退到 assistant.ToolCalls 之前")
}

func TestAlignToolCallPairsEino_EdgeBoundaries(t *testing.T) {
	in := []*schema.Message{{Role: schema.System}, {Role: schema.User}}
	assert.Equal(t, 0, alignToolCallPairsEino(in, 0)) // cut=0 不变
	assert.Equal(t, 1, alignToolCallPairsEino(in, 1)) // cut=1 不变
	assert.Equal(t, 2, alignToolCallPairsEino(in, 2)) // cut==len 不变
	assert.Equal(t, 5, alignToolCallPairsEino(in, 5)) // cut>len 不变
}

// ── estimateTokensEino sanity ────────────────────────────────────────────────

func TestEstimateTokensEino_Basic(t *testing.T) {
	in := []*schema.Message{
		{Role: schema.User, Content: strings.Repeat("a", 400)},      // 400 chars
		{Role: schema.Assistant, Content: strings.Repeat("b", 400)}, // 400 chars
	}
	// 800 chars / 4 = 200 tokens
	got := estimateTokensEino(in)
	assert.GreaterOrEqual(t, got, int64(190))
	assert.LessOrEqual(t, got, int64(210))
}

func TestEstimateTokensEino_IncludesToolCallsAndReasoning(t *testing.T) {
	in := []*schema.Message{
		{
			Role:             schema.Assistant,
			Content:          "answer",
			ReasoningContent: "thinking long",
			ToolCalls: []schema.ToolCall{
				{ID: "x", Function: schema.FunctionCall{Name: "tool", Arguments: `{"k":"v"}`}},
			},
		},
	}
	got := estimateTokensEino(in)
	assert.Greater(t, got, int64(0), "应当统计 ReasoningContent + ToolCalls JSON")
}

// 防御性 panic 测：nil message slice / nil entry 不应崩
func TestEstimateTokensEino_NilSafe(t *testing.T) {
	assert.Equal(t, int64(0), estimateTokensEino(nil))
	assert.Equal(t, int64(0), estimateTokensEino([]*schema.Message{nil, nil}))
}

// ── 验证 newAdapterCompactor 兜底 ────────────────────────────────────────────

func TestNewAdapterCompactor_FallbackContextWindow(t *testing.T) {
	c := newAdapterCompactor(0)
	assert.Greater(t, c.contextWindow, 0, "0 → 兜底默认值")
	c2 := newAdapterCompactor(-1)
	assert.Greater(t, c2.contextWindow, 0, "负数 → 兜底默认值")
	c3 := newAdapterCompactor(50_000)
	assert.Equal(t, 50_000, c3.contextWindow)
}

// 小语法兜底：buildLongConversation 至少长到能跨阈值
func TestBuildLongConversation_ProducesExpectedTokenRange(t *testing.T) {
	in := buildLongConversation(10_000)
	got := estimateTokensEino(in)
	// 留宽容（±50%）：char/4 估算和我们的人工 chunk 设计可能有偏差
	if got < int64(5_000) {
		t.Errorf("buildLongConversation 估算偏低: %d (want >= 5000)", got)
	}
}

// 这一行用于 silence "fmt imported but not used" — 部分 case 调用 fmt
var _ = fmt.Sprintf
