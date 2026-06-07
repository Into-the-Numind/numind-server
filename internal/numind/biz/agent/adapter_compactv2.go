package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/cloudwego/eino/schema"

	"numind-server/internal/numind/biz/compactv2"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
)

// adapter_compactv2.go — V2 compact 接入 Eino's per-ReAct-round LLM 调用层。
//
// 问题背景：板块 2 task 2.1-2.5 写完后发现，maybeCompactV2 在 runner.go outer
// loop 调用时看到的是 `run.Messages`（只在 WriteTurn 时写一次 [user, assistant]），
// 而 Eino 的 ReAct internal loop 累积的 system+user+assistant(tool_calls)+tool+...
// 中间消息**只在 aiserviceAdapter.Generate 的 in 参数里出现**，runner 外层看不见。
//
// 结果：V2 的 L3 autocompact / L4 hard limit 在生产里**从未触发**——因为
// maybeCompactV2 每次看到 len(msgs)==0 直接 return nil。
//
// 修复（本文件）：把 V2 的核心 prevention 逻辑下沉到 adapter.Generate 调用前。
// Eino 每跑一个 ReAct round 都会调一次 Generate(in)，in 是完整累积对话。我们在
// 这里：
//
//   1. 估算 in 的 token 数（char/4）
//   2. ratio >= AutocompactThreshold(0.70) → 跑 L3 autocompact（LLM 摘要 + recent 5）
//   3. ratio >= HardLimitRatio(0.85) 且连续 3 次失败 → 返回 ErrContextExhausted
//   4. aiservice 返回 prompt_too_long error → 强制 autocompact + retry 一次（PTL recovery，
//      代替已删的 V1 ReactiveCompact 链）
//
// 不做的：
//   - L1 prune / L2 microcompact：依赖 Meta.TurnIndex / Meta.ToolName 元数据，
//     Eino schema.Message 没有这些字段，从 ToolCallID 反向解析 tool name 复杂度
//     不值得；省略 L1/L2 让 L3 (LLM summary) 直接处理所有需要压缩的场景。
//   - 把这些状态持久化到 DB CompactStateV2：adapter 是 per-Run 短命对象，
//     consecutive_failures 在 Run 内追踪即可；跨 Run 重启不需要继承（每个 Run
//     都是新的会话上下文）。
//   - max_output_tokens escalation：单独 fix（要靠 DB Registry 调 profile 的
//     max_tokens，不属于 compact 范畴）。

// adapterCompactor 是 V2 compact 在 adapter 层的实现，per-Run 单实例（不并发使用，
// runner.go Run 是单线程的；adapter.WithTools 共享同一个 compactor 指针）。
//
// 字段：
//   - contextWindow: 当前 model 的 context window 上限（token），来自 capability
//     matrix 或 runner 注入的兜底值（32K）；ratio = estimatedTokens / contextWindow
//   - consecutiveFailures: 本 Run 内连续 autocompact 失败次数；3 次 → terminate
//   - mu: 防御性 mutex；理论上 Eino ReAct 是单线程的，但 adapter 通过 WithTools
//     被复制到新实例，旧实例若被并发持有可能有 race。
type adapterCompactor struct {
	contextWindow       int
	consecutiveFailures int
	mu                  sync.Mutex
}

// newAdapterCompactor 构造。contextWindow <= 0 时用兜底默认值。
func newAdapterCompactor(contextWindow int) *adapterCompactor {
	if contextWindow <= 0 {
		// 兜底：32K 是多数主流 chat model 的 default context window；待板块 1
		// capability matrix 落地后改为按 model_key 查真值。
		contextWindow = 32_000
	}
	return &adapterCompactor{contextWindow: contextWindow}
}

// MaybeCompact 在 aiservice.Chat 调用前根据 in 的估算 token 数决定是否压缩。
//
// 返回值：
//   - newIn：压缩后的 messages 切片（未压缩时返回原 in 不复制）
//   - didCompact：是否实际做了压缩（true → caller 应记 log 便于运维观察）
//   - err：
//   - compactv2.ErrContextExhausted：连续 3 次失败 + ratio >= HardLimitRatio(0.85) → caller 必须放弃
//   - 其他 error：LLM 失败但未达 break circuit；caller 应当原样传 in 给 aiservice，
//     让上游决定是否 PTL 或继续
//
// 单调不变：本函数 PRE-CALL（aiservice.Chat 调用前）；ForcePTLRecover 是 POST-CALL
// 的 PTL 错误恢复入口，两者互补不重叠。
func (c *adapterCompactor) MaybeCompact(ctx context.Context, in []*schema.Message) ([]*schema.Message, bool, error) {
	if len(in) < compactv2.AutocompactPreserveRecentMessages+2 {
		// 短消息列表：无意义压缩。常见场景：Run 刚开始，Eino 只送了 [system, user]
		// 就来调 Generate，此时不需要也无法压缩。
		return in, false, nil
	}

	estimated := estimateTokensEino(in)
	ratio := float64(estimated) / float64(c.contextWindow)

	if ratio < compactv2.AutocompactThreshold {
		// Prevention threshold 未到，直通。
		return in, false, nil
	}

	c.mu.Lock()
	fails := c.consecutiveFailures
	c.mu.Unlock()

	// Hard limit + 3 fail breaker：连续 3 次摘要失败说明 LLM 路由本身有问题，
	// 继续重试只会更慢更贵；上抛 ErrContextExhausted 让 runner 终止 run。
	if ratio >= compactv2.HardLimitRatio && fails >= compactv2.MaxConsecutiveAutocompactFailures {
		return nil, false, compactv2.ErrContextExhausted
	}

	return c.runAutocompact(ctx, in)
}

// ForcePTLRecover 在 aiservice.Chat 返回 prompt_too_long 错误时调用，强制跑一次
// autocompact 然后让 caller 用新 in 重试。无论 ratio 多少都跑（错误本身就是
// "context 撑爆"的信号）。
//
// PTL recovery 不计入 consecutiveFailures 的"成功复位"（即使 PTL 恢复成功，
// failures 计数也不重置——因为上一轮 MaybeCompact 已经计过了；这里只做
// 兜底恢复）。
//
// PTL 在 force 路径再失败 → 直接返回 ErrContextExhausted（不再 retry 套娃）。
func (c *adapterCompactor) ForcePTLRecover(ctx context.Context, in []*schema.Message) ([]*schema.Message, bool, error) {
	if len(in) < compactv2.AutocompactPreserveRecentMessages+2 {
		// 太短，没法压缩；让 PTL 错误原样冒泡。
		return in, false, nil
	}

	c.mu.Lock()
	fails := c.consecutiveFailures
	c.mu.Unlock()

	if fails >= compactv2.MaxConsecutiveAutocompactFailures {
		// 已经在 break circuit 上了，不再尝试。
		return nil, false, compactv2.ErrContextExhausted
	}

	return c.runAutocompact(ctx, in)
}

// runAutocompact 实际跑一次 L3 autocompact（LLM 摘要 + recent 5 拼接），不区分
// 触发原因（threshold or PTL）。失败累计 failures；成功复位。
func (c *adapterCompactor) runAutocompact(ctx context.Context, in []*schema.Message) ([]*schema.Message, bool, error) {
	// 1. 切点 + tool_call pair 对齐
	systemMsg := in[0]
	cut := len(in) - compactv2.AutocompactPreserveRecentMessages
	cut = alignToolCallPairsEino(in, cut)
	if cut <= 1 {
		// 切完只剩 systemMsg，无可压缩内容（recent 已吃完全部 messages）；caller
		// 应当让 aiservice 继续处理，看 LLM 是否能在原 in 上工作。
		return in, false, nil
	}
	toCompact := in[1:cut]
	recent := in[cut:]

	// 2. 调 LLM 摘要
	summary, err := summarizeEinoViaLLM(ctx, toCompact)
	if err != nil {
		c.mu.Lock()
		c.consecutiveFailures++
		c.mu.Unlock()
		return in, false, fmt.Errorf("adapterCompactor.runAutocompact: %w", err)
	}

	// 3. 成功 → reset failure counter + 拼新 messages
	c.mu.Lock()
	c.consecutiveFailures = 0
	c.mu.Unlock()

	summaryMsg := &schema.Message{
		Role:    schema.System,
		Content: summary,
	}

	out := make([]*schema.Message, 0, 2+len(recent))
	out = append(out, systemMsg, summaryMsg)
	out = append(out, recent...)
	return out, true, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

// estimateTokensEino 是 char/4 粗估，与 compactv2.EstimateMessagesTokens 等价
// 但操作 Eino schema.Message 直接（避免无谓的格式转换）。
func estimateTokensEino(in []*schema.Message) int64 {
	var chars int64
	for _, m := range in {
		if m == nil {
			continue
		}
		chars += int64(len(m.Content))
		chars += int64(len(m.ReasoningContent))
		chars += int64(len(m.ToolCallID))
		if len(m.ToolCalls) > 0 {
			if raw, err := json.Marshal(m.ToolCalls); err == nil {
				chars += int64(len(raw))
			}
		}
	}
	return chars / int64(compactv2.NumCharsPerToken)
}

// alignToolCallPairsEino 是 compactv2.alignToolCallPairs 的 Eino schema.Message
// 版本。规则一致（spec §设计要点边界 ①）：cut 点落在 assistant.ToolCalls 与对应
// tool reply 之间会导致 OAI 协议链断裂；本函数向前回溯到完整 pair 之外。
//
// 边界：
//   - cut <= 1 或 cut >= len(in) → 直接返回（无需对齐）
//   - in[cut] 是 schema.Tool 角色 → 向前回退到对应 assistant.ToolCalls 之前
//   - in[cut-1] 是 schema.Assistant 且有 ToolCalls 但下一条 tool reply 在 cut 之后 → 同上
//
// 收敛：最多回退到 cut=1（保 systemMsg 不丢）。
func alignToolCallPairsEino(in []*schema.Message, cut int) int {
	if cut <= 1 || cut >= len(in) {
		return cut
	}
	for cut > 1 {
		// 情景 A: cut 指向 tool reply（dangling），向前推
		if in[cut].Role == schema.Tool {
			cut--
			continue
		}
		// 情景 B: cut-1 是 assistant 且有 tool_calls，但 cut 不是 tool（说明 tool reply 已在 toCompact 区）
		// → 把 assistant.tool_calls 也归入 toCompact，向前推
		if cut-1 >= 0 && in[cut-1].Role == schema.Assistant && len(in[cut-1].ToolCalls) > 0 {
			cut--
			continue
		}
		break
	}
	return cut
}

// serializeEinoForSummary 把 Eino messages 拍平成 LLM 摘要 prompt 输入字符串。
// 等价于 compactv2.serializeForSummary 但操作 schema.Message 直接。
//
// 关键规则（spec §设计要点边界 ②）：
//   - <persisted-output ref="..."/> 不展开（保留 ref 字面值，不把 artifact 内容重塞回 prompt）
//   - ReasoningContent 附加在 content 后面（thinking-mode 模型的思考链留给摘要使用）
//   - ToolCalls JSON 序列化，让摘要 LLM 看到 assistant 调了哪些工具
func serializeEinoForSummary(msgs []*schema.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		if m == nil {
			continue
		}
		b.WriteString(string(m.Role))
		b.WriteString(": ")
		b.WriteString(m.Content)
		if m.ReasoningContent != "" {
			b.WriteString("\n[reasoning] ")
			b.WriteString(m.ReasoningContent)
		}
		if len(m.ToolCalls) > 0 {
			if raw, err := json.Marshal(m.ToolCalls); err == nil {
				b.WriteString("\n[tool_calls] ")
				b.Write(raw)
			}
		}
		if m.ToolCallID != "" {
			b.WriteString("\n[tool_call_id] ")
			b.WriteString(m.ToolCallID)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// summarizeEinoViaLLM 调 aiservice.Chat（profile.AgentCompact 路由 qwen-plus /
// deepseek-v3-2，D4 决策）生成 summary 字符串，校验 D5 XML 开闭标签。失败 → error。
//
// 注意：这里直接用包级 chatFn seam（aiservice.Chat），便于测试 mock；不复用
// compactv2.callLLMForSummary 因为后者要求 MessageV2 参数（多一层转换无意义）。
func summarizeEinoViaLLM(ctx context.Context, messages []*schema.Message) (string, error) {
	serialized := serializeEinoForSummary(messages)

	req := aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleSystem, Content: aiservice.MessageContent{Text: compactv2.AutocompactPromptTemplate}},
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: serialized}},
		},
		MaxTokens:   compactv2.AutocompactMaxSummaryTokens,
		Temperature: compactv2.AutocompactTemperature,
	}

	resp, err := chatFn(ctx, profile.AgentCompact, req)
	if err != nil {
		return "", fmt.Errorf("summarizeEinoViaLLM chat: %w", err)
	}
	if resp == nil {
		return "", fmt.Errorf("summarizeEinoViaLLM: nil response")
	}

	text := strings.TrimSpace(resp.Content)
	// D5：开闭标签校验（替代旧 [REFERENCE ONLY] 文本前缀）
	if !strings.HasPrefix(text, compactv2.AutocompactOpenTag) {
		return "", fmt.Errorf("summarizeEinoViaLLM: missing open tag %s", compactv2.AutocompactOpenTag)
	}
	if !strings.HasSuffix(text, compactv2.AutocompactCloseTag) {
		return "", fmt.Errorf("summarizeEinoViaLLM: missing close tag %s", compactv2.AutocompactCloseTag)
	}
	return text, nil
}

// isPromptTooLongErr 判断 aiservice 返回的 error 是不是 prompt_too_long。
//
// aiservice 不暴露强类型 error，只能字符串匹配。覆盖主流 provider 的措辞：
//   - OpenAI / DMXAPI: "context_length_exceeded" / "prompt is too long" / "exceeds the maximum context"
//   - 阿里 DashScope: "input range" / "context limit"
//   - 火山 Ark: "prompt_too_long" / "max_token"
//   - DeepSeek: "Maximum context length"
//
// 任一关键词匹配（case-insensitive）→ 视为 PTL。匹配过宽不构成 false positive
// 风险——只是会触发一次"无用"的 ForcePTLRecover；恢复失败后原 error 仍冒泡。
func isPromptTooLongErr(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	keywords := []string{
		"prompt_too_long",
		"prompt is too long",
		"context_length_exceeded",
		"context length",
		"maximum context",
		"max_token",
		"input range",
		"context limit",
		"exceeds the maximum",
	}
	for _, k := range keywords {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}
