// Package compactv2 — task 2.4 序列化 helpers。
//
// 本文件提供 autocompact LLM 调用前的两个 helper：
//   - serializeForSummary: messages 序列化成给 LLM 看的字符串（role: content\n... 形式）
//   - alignToolCallPairs:  cut 点回退到 tool_calls/tool_result 完整 pair 边界，防 OAI 协议链断裂
//   - serializedSize:      估算 serialize 后的总字节数（compression_ratio 计算用）
//
// 关键设计原则：
//   - **artifact ref 不展开**（spec §设计要点边界 ②）：messages 中含 `<persisted-output ref="xxx"/>`
//     标签的 entry，serializeForSummary 保留 ref 字面值，**不读盘**。autocompact 不应让 L0 已外置的
//     大对象（PDF / audio 解析结果）重新进 LLM context。
//   - **tool_call pair 边界**（spec §设计要点边界 ①）：assistant.tool_calls 后必然跟若干 role="tool" 消息
//     与每个 tool_call.id 一一对应。cut 点若落在 pair 中间 → OAI 协议链断裂 → provider 直接 401。
//     alignToolCallPairs 把 cut 往**前**推到上一组完整 pair 之后的位置（最坏 cut=1 仅保 systemMsg）。
//
// 参考 spec §设计要点 — Autocompact 算法 / 边界 case ① ②。
package compactv2

import (
	"encoding/json"
	"strings"
)

// serializeForSummary 把 messages 拍平成单 string 喂给 LLM。
//
// 输出格式（每条 message 一段）：
//
//	role: content
//	[tool_calls: <json>]                 (assistant tool_calls 时追加)
//	[tool_call_id: <id>]                 (tool message 时追加，区分多个并行 tool_call)
//
// 关键规则：
//  1. Content 原样输出（不剥离 `<persisted-output ref="xxx"/>`）—— ref 字面值保留即可，LLM 自行
//     理解"这里曾有大对象，但已外置"，不需要把 artifact 内容塞回来。
//  2. ReasoningContent 原样附加在 content 后（thinking-mode 模型的思考链需要留给 summary 用）。
//  3. 不输出 UUID / Meta / IsCompactMark / HasFileRef 等本地 envelope 字段（LLM 不需要）。
//  4. 空 content 也保留一行（"role: \n"），保持 turn 边界清晰。
//
// 性能：O(n * avgLen)，n=messages 数；ToolCalls JSON marshal 是主开销，n 不大（autocompact 通常 < 50）。
func serializeForSummary(msgs []MessageV2) string {
	var b strings.Builder
	for i := range msgs {
		m := &msgs[i]
		b.WriteString(m.Role)
		b.WriteString(": ")
		b.WriteString(m.Content)
		if m.ReasoningContent != "" {
			b.WriteString("\n[reasoning] ")
			b.WriteString(m.ReasoningContent)
		}
		if len(m.ToolCalls) > 0 {
			// ToolCalls 序列化便于 LLM 理解 assistant 调了哪些工具
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

// alignToolCallPairs 把切点 cut 校正到完整 tool_call pair 的边界。
//
// OAI 协议链：assistant 消息含 ToolCalls[] → 后续每个 ToolCall.id 必须对应一个 role="tool"
// 的消息（ToolCallID 字段匹配）。若 cut 落在 assistant.ToolCalls 与对应 tool messages 之间，
// 留下来的"recent"切片会出现 dangling assistant.tool_calls（没对应的 tool reply）→ provider 401。
//
// 算法（spec §设计要点边界 ①）：
//  1. 若 cut <= 1（仅保 systemMsg）或 cut >= len(messages） → 无需对齐
//  2. 如果 messages[cut] 是 role="tool" → cut 点在 pair 中间 → 向前推到上一个 assistant.tool_calls 位置
//  3. 如果 messages[cut-1] 是 role="assistant" 且有 ToolCalls → cut 点正好在 assistant 之后但 tool reply 之前
//     → 同样向前推到上一组 pair 之前
//  4. 收敛保护：单次最多回退 len(messages) 步；若回退到 1（仅 systemMsg）就停（不强制返回 0，否则 systemMsg 也丢）
//
// 返回：新的 cut 点（满足 messages[cut:] 是完整的 N 段 pair，不含 dangling 元素）。
//
// 边界 case：
//   - cut == 0 → return 0（messages 太短，调用方应当跳过 autocompact）
//   - cut == len(messages) → return cut（无需对齐）
//   - 所有 messages 都是 tool message（不合常理）→ 退到 cut=1
//   - 没有 assistant.ToolCalls 的"纯文本对话"路径 → cut 不变（无 pair 可破坏）
//
// 性能：O(cut)，最坏 O(n)。
func alignToolCallPairs(messages []MessageV2, cut int) int {
	if cut <= 1 || cut >= len(messages) {
		return cut
	}
	// 情景 A：cut 点在 role="tool"（dangling tool reply）→ 向前找到对应的 assistant.tool_calls 之前
	// 情景 B：cut 点在 assistant 之后，但该 assistant 有 ToolCalls，紧随其后的 tool reply 已被切掉
	//        → 同样需要向前推
	//
	// 实现：向前回溯直到 messages[cut-1] 不是 "正在等待 tool reply 的 assistant" 且 messages[cut] 不是 tool
	for cut > 1 {
		// 情景 A：当前 cut 指向的 message 是 tool —— 它的"配对 assistant"在 cut 之前的某处
		// 回退一步：把这条 tool message 也归入"被压缩"区，留给下一轮判定
		if messages[cut].Role == "tool" {
			cut--
			continue
		}
		// 情景 B：cut 之前是 assistant.tool_calls 且后面 5 条 recent 没有对应的 tool reply
		// 校验：messages[cut-1] 是 assistant 且 ToolCalls 非空 → 看后续是否有匹配的 tool reply
		prev := &messages[cut-1]
		if prev.Role == "assistant" && len(prev.ToolCalls) > 0 {
			// 收集 prev.ToolCalls 的 id 集合
			needIDs := make(map[string]bool, len(prev.ToolCalls))
			for _, tc := range prev.ToolCalls {
				if id, ok := tc["id"].(string); ok && id != "" {
					needIDs[id] = true
				}
			}
			// 扫 cut..len(messages) 看是否每个 needIDs[id] 都出现了对应的 tool message
			satisfied := 0
			for j := cut; j < len(messages); j++ {
				m := &messages[j]
				if m.Role != "tool" {
					break // 出现非 tool 消息 → 后续 tool reply 已结束
				}
				if needIDs[m.ToolCallID] {
					satisfied++
				}
			}
			if satisfied < len(needIDs) {
				// 至少一个 tool reply 被切走 → cut 回退一步把 assistant 也归入压缩区
				cut--
				continue
			}
		}
		// 既不是 tool 也不是悬挂 assistant.tool_calls → cut 已对齐
		break
	}
	return cut
}

// serializedSize 估算 serializeForSummary 后的字节数（不真的 serialize 全量，避免双倍内存）。
//
// 用于 AutocompactResult.CompressionRatio 计算：
//
//	ratio = len(summary) / serializedSize(toCompact)
//
// 估算策略：直接复用 serializeForSummary 的结果长度（一次 string 构建，调用方若需 ratio 必须先 serialize）。
// 性能权衡：autocompact 路径上 serialize 已经做了一次，serializedSize 在 caller 持有 serialized 字符串后
// 直接用 len(serialized) 即可，本函数主要用于测试 / 独立计算场景。
func serializedSize(msgs []MessageV2) int {
	return len(serializeForSummary(msgs))
}
