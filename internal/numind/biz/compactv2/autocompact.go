// Package compactv2 — task 2.4 L3 autocompact 实现。
//
// 调用入口：runner.go maybeCompactV2 在 ratio >= AutocompactThreshold (0.85) 时调本包 Autocompact。
//
// 核心流程（spec §设计要点 — Autocompact 算法）：
//  1. 加载 messages（已由 caller 完成）
//  2. messages 太短（< AutocompactPreserveRecentMessages + 2）→ Triggered=false 直接返回
//  3. 切：systemMsg + toCompact + recent；alignToolCallPairs 校正 cut 点
//  4. 读 state；nil → 默认 CompactStateV2{}
//  5. callLLMForSummary：profile.AgentCompact + AutocompactTemperature + AutocompactMaxSummaryTokens
//  6. XML 开闭标签校验失败 / LLM error → state.ConsecutiveAutocompactFailures++
//     连续 3 次 → TerminalReason=context_exhausted（caller 设 run.Status=terminated）
//  7. 成功 → 构造 summaryMsg（Role="system" / Meta.IsCompacted=true / CompactionPhase="L3"）
//  8. newMsgs = [systemMsg, summaryMsg, ...recent]；UpdateMessagesV2 + UpdateCompactStateV2
//
// 关键 invariants（违反 = review FAIL）：
//   - 不新增 TerminalReason 枚举：复用现有字符串值 "context_exhausted"（CLAUDE.md §6b agent-mode I2）
//   - aiservice 唯一入口：通过 deps.Chat 注入，不直接 import aiservice provider 包
//   - V1 路径完全不动：本文件不依赖 V1 compact 包；仅 runner 在 useCompactV2 == true 时调本包
//   - 不展开 artifact ref：serializeForSummary 保留 <persisted-output ref="..."/> 字面值
//
// 参考：
//   - spec /Users/zhiyuchen/Downloads/有数-Agent-Mode-V1.5-NDF-spec/02-context/task-04-autocompact.md
//   - README §D5 — XML 边界决策
//   - V1 等价物 internal/numind/biz/compact/aiservice_provider.go（仅作 OAI ChatRequest 调用模式参考）
package compactv2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/model"
)

// ErrContextExhausted 是 Autocompact 连续 3 次失败后产生的 sentinel error。
//
// 用法（runner.go 集成 — task 2.4 review P1 fix）：
//   - autocompactV2 在收到 AutocompactResult.TerminalReason="context_exhausted" 时
//     调用 terminateRunContextExhausted（写 DB run.state_reason）+ 返回 ErrContextExhausted
//   - maybeCompactV2 透传该 error
//   - runner 主循环 errors.Is(err, compactv2.ErrContextExhausted) → break loop +
//     skip 最终的 UpdateState（否则会用 st.TerminalReason 覆盖 "context_exhausted"）
//
// 这是 review P1 fix：之前 terminateRunContextExhausted 写 DB 后未向上传递信号，
// 导致主循环继续跑 Generate() 并在 line 753 用 st.TerminalReason 覆盖 "context_exhausted"。
var ErrContextExhausted = errors.New("compactv2: context exhausted (autocompact 3-fail breaker or hard limit)")

// AutocompactResult 是 Autocompact 的返回 metadata。
//
// 字段说明：
//   - Triggered=true：本次成功跑了一轮 LLM summarize 并更新 messages + state
//   - Triggered=false：消息过短或失败放弃（caller 不重试，本轮继续 ReAct；下轮 ratio 再判定）
//   - TerminalReason="context_exhausted"：连续失败 3 次或 hard limit 触发 → caller 必须 terminate
//   - OriginalMsgCount：被压缩的 messages 数（cut - 1，不含 systemMsg）
//   - CompactedMsgCount：压缩后 summary 消息数（恒为 1）
//   - SummaryUUID：新生成的 summary message UUID（写到 state.SummaryMessageUUID）
//   - CompressionRatio：summary 字符数 / 原 serialized 字符数（越小压缩越彻底）
type AutocompactResult struct {
	Triggered         bool
	OriginalMsgCount  int
	CompactedMsgCount int
	SummaryUUID       string
	CompressionRatio  float64
	TerminalReason    string
}

// CompactV2StateStore 是 compactv2 包内定义的"autocompact 需要的 store 行为"结构性接口。
//
// 与 task 2.2 deps.go 套路一致：避免 compactv2 import store 产生 import cycle
// （store 包已 import compactv2 引用 CompactStateV2 / MessageV2 类型）。
// store.IAgentCompactV2Store 方法签名完全满足此接口（结构性匹配），caller 直接传 store 实例即可。
type CompactV2StateStore interface {
	GetCompactStateV2(ctx context.Context, runID uint64) (*CompactStateV2, error)
	UpdateCompactStateV2(ctx context.Context, runID uint64, state *CompactStateV2) error
	UpdateMessagesV2(ctx context.Context, runID uint64, messages []MessageV2) error
}

// ChatFn 是 aiservice.Chat 的函数签名抽象（caller 注入，便于单测 mock）。
//
// 生产环境 caller（runner）传入 closure：
//
//	chat := func(ctx context.Context, taskID string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
//	    return aiservice.Chat(ctx, taskID, req)
//	}
//
// 单测注入自定义 closure，返回 mock summary 或 error。
//
// 与 V1 compact.chatFn 思路完全一致（aiservice_provider.go），但 V2 把 seam 暴露在 Deps 上而非
// package-level var，避免单测互相污染。
type ChatFn func(ctx context.Context, taskID string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error)

// MetricsRecorder 是 autocompact 的可观测性接口。
//
// 实现由 caller 注入（如 prometheus + zap helper）；单测用 noop / map 实现。
// counters per spec §观测：agent.autocompact.{trigger,success,fail,compression_ratio} +
// agent.context_exhausted_terminate。
//
// Observe 用于 histogram (compression_ratio)；Incr 用于 counter。
type MetricsRecorder interface {
	Incr(name string)
	Observe(name string, value float64)
}

// NoopMetrics 是 MetricsRecorder 的 no-op 实现，单测 / 未配置 metrics collector 时使用。
type NoopMetrics struct{}

// Incr noop。
func (NoopMetrics) Incr(string) {}

// Observe noop。
func (NoopMetrics) Observe(string, float64) {}

// AutocompactDeps 是 Autocompact 的依赖注入。
//
// 字段说明：
//   - Chat：aiservice.Chat closure（必填，nil → 返回 error）
//   - CompactV2Store：V2 state + messages 持久化（必填，nil → 返回 error）
//   - Metrics：可观测性 counter；nil → 自动用 NoopMetrics 兜底
type AutocompactDeps struct {
	Chat           ChatFn
	CompactV2Store CompactV2StateStore
	Metrics        MetricsRecorder
}

// Autocompact 执行 L3 autocompact（调 LLM 把历史压缩成 12 段固定模板）。
//
// 触发判定由 caller (runner.go maybeCompactV2) 完成；本函数只负责具体实现。
//
// 算法：
//  1. 加载 messages（从 run.Messages 解析）
//  2. 太短（< AutocompactPreserveRecentMessages + 2 = 7）→ Triggered=false 直接返回
//  3. 切：systemMsg = msgs[0]；cut = len(msgs) - AutocompactPreserveRecentMessages
//     toCompact = msgs[1:cut]；recent = msgs[cut:]
//     用 alignToolCallPairs 校正 cut，防 OAI 协议链断裂
//  4. 读 state；nil → 默认空 state（首次 autocompact）
//  5. callLLMForSummary（profile.AgentCompact + AutocompactTemperature + AutocompactMaxSummaryTokens）
//  6. 失败（LLM error 或 XML 标签校验失败）：
//     state.ConsecutiveAutocompactFailures++ → UpdateCompactStateV2
//     如果 >= MaxConsecutiveAutocompactFailures (3) → TerminalReason=context_exhausted
//  7. 成功：构造 summaryMsg → newMsgs = [systemMsg, summaryMsg, ...recent]
//     UpdateMessagesV2 + UpdateCompactStateV2（CurrentPhase=L3_summarized）
//
// 错误返回语义：
//   - error != nil：调用方应当 warn log 继续运行（fail-open；下轮 ratio 再判）
//   - TerminalReason != ""：调用方**必须** terminate run（state_reason=TerminalReason）
//
// 注意：error 与 TerminalReason 是**互斥**的两个返回路径——LLM 失败仅返回 (Triggered=false, nil)，
// 累计 3 次 error 后才返回 (TerminalReason="context_exhausted", nil)。caller 区分这两个分支。
func Autocompact(ctx context.Context, run *model.AgentRun, deps AutocompactDeps) (AutocompactResult, error) {
	if deps.Chat == nil {
		return AutocompactResult{}, fmt.Errorf("compactv2.Autocompact: deps.Chat is required")
	}
	if deps.CompactV2Store == nil {
		return AutocompactResult{}, fmt.Errorf("compactv2.Autocompact: deps.CompactV2Store is required")
	}
	if deps.Metrics == nil {
		deps.Metrics = NoopMetrics{}
	}
	if run == nil {
		return AutocompactResult{}, fmt.Errorf("compactv2.Autocompact: run is required")
	}

	// trigger counter（无论后续成功 / 失败，触发一次就计一次）
	deps.Metrics.Incr("agent.autocompact.trigger")

	// 1. 加载 messages（caller 已 unmarshal 过；本函数再 unmarshal 一次保持单一职责）
	msgs, err := loadMessagesV2(run)
	if err != nil {
		return AutocompactResult{}, fmt.Errorf("compactv2.Autocompact load messages: %w", err)
	}

	// 2. 太短跳过（spec §设计要点边界 ⑦）
	if len(msgs) < AutocompactPreserveRecentMessages+2 {
		return AutocompactResult{Triggered: false}, nil
	}

	// 3. 切：systemMsg + toCompact + recent
	systemMsg := msgs[0]
	cut := len(msgs) - AutocompactPreserveRecentMessages
	// 校正 cut 到 tool_calls/tool_result 对的完整边界（OAI 协议链）
	cut = alignToolCallPairs(msgs, cut)
	if cut <= 1 {
		// 校正后所有 messages 都被吃进 recent → 无可压缩 → 跳过
		return AutocompactResult{Triggered: false}, nil
	}
	toCompact := msgs[1:cut]
	recent := msgs[cut:]

	// 4. 读 state
	state, err := deps.CompactV2Store.GetCompactStateV2(ctx, run.ID)
	if err != nil {
		return AutocompactResult{}, fmt.Errorf("compactv2.Autocompact GetCompactStateV2: %w", err)
	}
	if state == nil {
		state = &CompactStateV2{}
	}

	// 5. 调 LLM
	summary, serializedLen, llmErr := callLLMForSummary(ctx, deps.Chat, toCompact)
	if llmErr != nil {
		// LLM 失败或 XML 校验失败 → ConsecutiveFailures++
		state.ConsecutiveAutocompactFailures++
		deps.Metrics.Incr("agent.autocompact.fail")
		// 写回 state（即使后面 terminate 也要先 persist 失败计数，避免重启后状态丢失）
		if uerr := deps.CompactV2Store.UpdateCompactStateV2(ctx, run.ID, state); uerr != nil {
			// state 写失败仍以原 LLM error 为主因；warn 用 error wrap 保留两路信息
			return AutocompactResult{}, fmt.Errorf("compactv2.Autocompact persist failure state: %w (also LLM err: %v)", uerr, llmErr)
		}
		if state.ConsecutiveAutocompactFailures >= MaxConsecutiveAutocompactFailures {
			deps.Metrics.Incr("agent.context_exhausted_terminate")
			return AutocompactResult{TerminalReason: "context_exhausted"}, nil
		}
		// 未达 break circuit：本轮放弃，caller 继续运行（下轮 ratio 再判定）。
		// 把原 LLM err 透传出去（review P2 fix）：caller fail-open warn 时可看到具体失败原因
		// （网络 timeout / 401 / XML 校验等），便于诊断。
		return AutocompactResult{Triggered: false}, llmErr
	}

	// 6. 成功：构造 summary message
	now := time.Now()
	summaryUUID := uuid.NewString()
	summaryMsg := MessageV2{
		UUID:    summaryUUID,
		Role:    "system", // 用 system role 强化 LLM "这是历史，不是新指令"
		Content: summary,  // 已含 <reference-only data-internal="true">...</reference-only> 包裹
		Meta: &MessageMetaV2{
			IsCompacted:     true,
			CompactionPhase: "L3",
			CompactedAt:     now,
			Timestamp:       now,
		},
	}

	// newMsgs = [systemMsg, summaryMsg, ...recent]
	newMsgs := make([]MessageV2, 0, 2+len(recent))
	newMsgs = append(newMsgs, systemMsg, summaryMsg)
	newMsgs = append(newMsgs, recent...)

	// 7. 更新 messages + state
	if err := deps.CompactV2Store.UpdateMessagesV2(ctx, run.ID, newMsgs); err != nil {
		return AutocompactResult{}, fmt.Errorf("compactv2.Autocompact UpdateMessagesV2: %w", err)
	}

	state.CurrentPhase = "L3_summarized"
	state.SummaryMessageUUID = summaryUUID
	state.ConsecutiveAutocompactFailures = 0
	state.LastCompactionAt = now
	state.TotalAutocompactRuns++
	if err := deps.CompactV2Store.UpdateCompactStateV2(ctx, run.ID, state); err != nil {
		// messages 已写但 state 写失败 → log + 返回错误，caller 决定是否重试
		return AutocompactResult{}, fmt.Errorf("compactv2.Autocompact UpdateCompactStateV2: %w", err)
	}

	deps.Metrics.Incr("agent.autocompact.success")

	// 计算 compression ratio（histogram）：summary 字符数 / 原 serialized 长度
	ratio := 0.0
	if serializedLen > 0 {
		ratio = float64(len(summary)) / float64(serializedLen)
	}
	deps.Metrics.Observe("agent.autocompact.compression_ratio", ratio)

	return AutocompactResult{
		Triggered:         true,
		OriginalMsgCount:  cut - 1, // 不含 systemMsg
		CompactedMsgCount: 1,       // 恒 1：一个 summary message
		SummaryUUID:       summaryUUID,
		CompressionRatio:  ratio,
	}, nil
}

// callLLMForSummary 调 aiservice.Chat 生成 summary 字符串，并校验 XML 开闭标签。
//
// 参数：
//   - chat：注入的 ChatFn（aiservice.Chat closure 或 mock）
//   - messages：待压缩的 messages 切片（不含 systemMsg / recent）
//
// 返回：
//   - summary：合规的 summary 字符串（首行 `<reference-only data-internal="true">` 末行 `</reference-only>`）
//   - serializedLen：原 messages serialize 后的字节数（compression_ratio 计算用）
//   - err：LLM 错误或 XML 校验失败
//
// 校验逻辑（D5）：
//  1. TrimSpace 后用 strings.HasPrefix(text, AutocompactOpenTag) 校开标签
//  2. strings.HasSuffix(text, AutocompactCloseTag) 校闭标签
//  3. 失败 → fmt.Errorf 返回（caller 计入 ConsecutiveAutocompactFailures）
//
// profile.AgentCompact 路由由 DB Registry 控制（D4），路由模型应是 qwen-plus 或 deepseek-v3-2
// 等长 context 非 thinking 模型；本函数不关心具体模型选择。
func callLLMForSummary(ctx context.Context, chat ChatFn, messages []MessageV2) (string, int, error) {
	serialized := serializeForSummary(messages)
	serializedLen := len(serialized)

	req := aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleSystem, Content: aiservice.MessageContent{Text: AutocompactPromptTemplate}},
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: serialized}},
		},
		MaxTokens:   AutocompactMaxSummaryTokens,
		Temperature: AutocompactTemperature,
	}

	resp, err := chat(ctx, profile.AgentCompact, req)
	if err != nil {
		return "", serializedLen, fmt.Errorf("callLLMForSummary chat: %w", err)
	}
	if resp == nil {
		return "", serializedLen, fmt.Errorf("callLLMForSummary: nil response")
	}

	text := strings.TrimSpace(resp.Content)

	// D5: 校验 XML 开闭标签（替代旧的 [CONTEXT COMPACTION] 文本前缀）
	if !strings.HasPrefix(text, AutocompactOpenTag) {
		return "", serializedLen, fmt.Errorf("callLLMForSummary: summary missing required open tag %s", AutocompactOpenTag)
	}
	if !strings.HasSuffix(text, AutocompactCloseTag) {
		return "", serializedLen, fmt.Errorf("callLLMForSummary: summary missing required close tag %s", AutocompactCloseTag)
	}

	return text, serializedLen, nil
}

// loadMessagesV2 从 run.Messages JSON 解析成 []MessageV2，通过 NewMessageFromJSON 兜底
// transient uuid / meta nil（task 2.1 R6 硬约束）。
//
// 该 helper 与 runner.maybeCompactV2 的解析逻辑重复——本包独立解析避免 caller 强制传递 msgs。
// 未来若有性能压力可考虑 caller pass-in。
func loadMessagesV2(run *model.AgentRun) ([]MessageV2, error) {
	if len(run.Messages) == 0 {
		return nil, nil
	}
	// JSON array → []json.RawMessage → []MessageV2
	var raws []json.RawMessage
	if err := json.Unmarshal(run.Messages, &raws); err != nil {
		return nil, fmt.Errorf("loadMessagesV2 unmarshal array: %w", err)
	}
	msgs := make([]MessageV2, 0, len(raws))
	for _, raw := range raws {
		m, err := NewMessageFromJSON(raw)
		if err != nil {
			return nil, fmt.Errorf("loadMessagesV2 NewMessageFromJSON: %w", err)
		}
		msgs = append(msgs, m)
	}
	return msgs, nil
}
