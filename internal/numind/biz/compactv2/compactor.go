// Package compactv2 — task 2.3 L1 prune + L2 microcompact（不调 LLM）。
//
// 当 context usage 跨过 50% / 70% 阈值时用廉价方式收纳历史 tool result：
//   - L1 (PruneOldToolResults)：把 ≥5 轮前、超出 3 轮保护窗口的旧 tool result 替换为 placeholder
//   - L2 (MicrocompactByToolName)：同名 tool 只保最近 3 个 envelope，更旧的合并为 placeholder
//
// 叠加策略：70% 触发时先跑 L2 再跑 L1（顺序无副作用，都靠 Meta.IsCompacted 短路）。
//
// 关键设计原则：
//   - **纯字符串 / map / slice 操作**：无任何外部 IO（DB / HTTP / LLM）。
//     UpdateMessagesV2 的 DB 写在 runner.go 里执行，不算 compactor 自身成本。
//   - **不删 entry**：L1 / L2 修改 messages 数组的 Content 字段而非 splice，保持索引稳定，
//     ReAct 工具调用链 (assistant.tool_calls -> tool.tool_call_id) 不被破坏。
//   - **L0 已处理短路**：Meta.IsCompacted=true 的 entry（含 L0 写盘 + L1/L2 已处理）直接跳过，
//     避免重复 compact 或破坏 L0 <persisted-output> 引用 XML。
//   - **空 ToolName 跳过 L2**：防 byTool[""] 误聚合。L1 仍可对空 ToolName 的 tool result 操作
//     （只是 placeholder 里 tool name 为空串）。
//   - **assistant 不动**：只处理 role="tool"。assistant.reasoning + tool_calls 不动，
//     避免破坏 ReAct 思维链。
//
// 参考：
//   - spec：/Users/zhiyuchen/Downloads/有数-Agent-Mode-V1.5-NDF-spec/02-context/task-03-prune-microcompact.md
//   - 调用方：internal/numind/biz/agent/runner.go maybeCompactV2
//   - 阈值：threshold.go PruneThresholdRatio / MicrocompactThreshold / PruneMinAgeTurns ...

package compactv2

import (
	"encoding/json"
	"fmt"
	"time"
)

// PruneOldToolResults 是 L1 prune 算法实现（不调 LLM，纯字符串操作）。
//
// 规则：
//  1. 仅处理 role="tool" 的 message；assistant 不动
//  2. Meta.IsCompacted=true 短路跳过（L0 / L1 / L2 已处理）
//  3. age = currentTurn - msg.Meta.TurnIndex；Meta nil 视为 active 且无 turn 信息，跳过
//     （旧 V1 messages 经 NewMessageFromJSON 兜底成 meta=nil，本函数不能误伤）
//  4. age < PruneMinAgeTurns (5) 跳过：近 5 轮 tool result 保持完整推理链
//  5. age <= PruneProtectRecentTurns (3) 跳过：最近 3 轮绝对保护
//     （冗余约束：当 PruneMinAgeTurns=5 > PruneProtectRecentTurns=3 时，age<5 已包含 age<=3；
//     但 spec §设计要点边界 ② 明确要求保护窗口独立判定，避免后续阈值调整时漏判。）
//  6. 命中 prune：Content 改为 placeholder（含 tool name + size + duration）；
//     Meta 字段补 IsCompacted=true / CompactionPhase="L1" / OriginalSizeBytes
//
// 返回：
//   - messages：**原 slice 的修改版本**（in-place 改 Content + Meta，保持 len 不变）。
//     调用方收到的 slice header 与传入相同，无需 reassign 但允许（语法一致）。
//   - pruned：本次实际命中 prune 的 entry 个数。
//
// 性能：O(n)，单次遍历，无堆分配（除 placeholder 字符串）。
func PruneOldToolResults(messages []MessageV2, currentTurn int, now time.Time) ([]MessageV2, int) {
	pruned := 0
	for i := range messages {
		msg := &messages[i]
		if msg.Role != "tool" {
			continue
		}
		// L0 / L1 / L2 已处理 → 短路（spec §设计要点边界 ①）
		if msg.Meta != nil && msg.Meta.IsCompacted {
			continue
		}
		// 旧 V1 messages（Meta nil）无 turn 信息 → 跳过，不误 prune
		if msg.Meta == nil {
			continue
		}
		// TurnIndex==0 视为 "未初始化"：runner 当前还没在 append message 时
		// 填入真实 turn（spec §风险 R1 / R2.3 review P1）。如果不跳过，所有
		// TurnIndex=0 的 tool message age = currentTurn - 0 = currentTurn，
		// 一旦 currentTurn >= 5 + 3 = 8 就会把所有 tool result 全部 prune，
		// 破坏 ReAct 推理链。这里保守跳过，等后续 task 把 TurnIndex 正确填入
		// 后该条件自动失效（合法的 turn 索引从 1 起算，0 是 sentinel）。
		if msg.Meta.TurnIndex == 0 {
			continue
		}
		age := currentTurn - msg.Meta.TurnIndex
		if age < PruneMinAgeTurns {
			continue
		}
		if age <= PruneProtectRecentTurns {
			continue
		}
		origSize := len(msg.Content)
		msg.Content = fmt.Sprintf(
			"[Old tool result cleared - %s, %d bytes - %s ago]",
			msg.Meta.ToolName, origSize, durationSince(msg.Meta.Timestamp, now),
		)
		msg.Meta.IsCompacted = true
		msg.Meta.CompactionPhase = "L1"
		msg.Meta.OriginalSizeBytes = int64(origSize)
		msg.Meta.CompactedAt = now
		pruned++
	}
	return messages, pruned
}

// MicrocompactByToolName 是 L2 microcompact 算法实现（不调 LLM）。
//
// 规则：
//  1. 仅处理 role="tool" 的 message；assistant 不动
//  2. Meta.IsCompacted=true 短路跳过（L0 / L1 / L2 已处理）
//  3. Meta nil 或 ToolName 空串跳过（防 byTool[""] 误聚合，spec §设计要点边界 ④）
//  4. 按 ToolName 分桶 → 桶内 entry 个数 ≤ MicrocompactKeepPerTool (3) 跳过该桶
//  5. 否则：保最后 N=3 个，旧的（前面的）替换为 placeholder
//     （桶内顺序由 messages 原序决定，越早的 i 越旧）
//
// 返回：
//   - messages：**原 slice 的修改版本**（in-place）
//   - compacted：本次实际命中 L2 的 entry 个数
//
// 性能：O(n) 单次扫描 + 桶遍历总和也 O(n)，无 O(n²) 风险。
//
// now 参数仅用于 CompactedAt 记录（与 L1 接口对齐）；nil-safe by `time.Time` zero value
// — 调用方传 time.Now() 即可。
func MicrocompactByToolName(messages []MessageV2, now time.Time) ([]MessageV2, int) {
	// 第一遍：分桶，记 tool name → entry index 列表
	byTool := make(map[string][]int)
	for i := range messages {
		msg := &messages[i]
		if msg.Role != "tool" {
			continue
		}
		if msg.Meta != nil && msg.Meta.IsCompacted {
			continue
		}
		// Meta nil 跳过：spec 边界 ④ + V1 旧 messages 兼容
		if msg.Meta == nil {
			continue
		}
		// 空 ToolName 跳过：避免 byTool[""] 把不同工具误并桶
		if msg.Meta.ToolName == "" {
			continue
		}
		byTool[msg.Meta.ToolName] = append(byTool[msg.Meta.ToolName], i)
	}

	// 第二遍：超过 keep 个的桶，把前面（最旧）的 mark L2
	compacted := 0
	for toolName, idxs := range byTool {
		if len(idxs) <= MicrocompactKeepPerTool {
			continue
		}
		toCompact := idxs[:len(idxs)-MicrocompactKeepPerTool]
		for _, i := range toCompact {
			msg := &messages[i]
			origSize := len(msg.Content)
			msg.Content = fmt.Sprintf(
				"[%s tool result superseded by newer call - %d bytes]",
				toolName, origSize,
			)
			// Meta 必非 nil（第一遍已过滤）
			msg.Meta.IsCompacted = true
			msg.Meta.CompactionPhase = "L2"
			msg.Meta.OriginalSizeBytes = int64(origSize)
			msg.Meta.CompactedAt = now
			compacted++
		}
	}
	return messages, compacted
}

// EstimateMessagesTokens 是 V2 messages 的 token 粗估（不调 tokenizer，纯字符算）。
//
// 公式：`总字符数 / NumCharsPerToken (4)`。
//
// 计入字符的来源：
//   - Content
//   - ReasoningContent（assistant 思考内容也算 prompt 输入）
//   - ToolCalls JSON serialize 后的字符串（function name + arguments）
//
// 不计入：
//   - Meta（V2 元数据，不送 LLM）
//   - UUID / ToolCallID / Role 等 envelope 字段（LLM 看到的是 OpenAI 格式包装）
//   - HasFileRef / IsCompactMark（同 Meta，本地标记）
//
// 偏差：对中文偏低估 ~20%（中文一个字 ≈ 1.5 token，非 0.25）。
// Mitigated by：调用方用 `max(estimated, actual)` + provider.Usage.PromptTokens 校准。
//
// 性能：O(n * avgLen)，n=messages 数；ToolCalls JSON marshal 是主要开销。
func EstimateMessagesTokens(messages []MessageV2) int64 {
	var totalChars int64
	for i := range messages {
		msg := &messages[i]
		totalChars += int64(len(msg.Content))
		totalChars += int64(len(msg.ReasoningContent))
		if len(msg.ToolCalls) > 0 {
			// ToolCalls 是 []map[string]any，序列化后才有可比字符长度
			if b, err := json.Marshal(msg.ToolCalls); err == nil {
				totalChars += int64(len(b))
			}
		}
	}
	return totalChars / NumCharsPerToken
}

// durationSince 返回 ts 到 now 的人类可读时间差。
//
// 输出格式：
//   - "0s ago" 若 ts 为零值（spec 推荐：placeholder 仍要可读，不报错）
//   - "Ns ago"  / "Nm ago" / "Nh ago" / "Nd ago" 按数量级降级
//
// 设计：从 day → second 级联判断，避免显示 "0.5h ago" 这种小数。
// 单元用 ago suffix（spec §设计要点 L1 placeholder 格式：`"%s ago"`）。
func durationSince(ts time.Time, now time.Time) string {
	if ts.IsZero() {
		return "0s"
	}
	d := now.Sub(ts)
	if d < 0 {
		// 时钟 skew / 未来时间：保底显示 0s 而非负数
		return "0s"
	}
	if d >= 24*time.Hour {
		days := int(d / (24 * time.Hour))
		return fmt.Sprintf("%dd", days)
	}
	if d >= time.Hour {
		hours := int(d / time.Hour)
		return fmt.Sprintf("%dh", hours)
	}
	if d >= time.Minute {
		mins := int(d / time.Minute)
		return fmt.Sprintf("%dm", mins)
	}
	secs := int(d / time.Second)
	return fmt.Sprintf("%ds", secs)
}
