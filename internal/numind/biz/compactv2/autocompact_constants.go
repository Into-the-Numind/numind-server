// Package compactv2 — task 2.4 L3 autocompact 常量。
//
// 关键设计决策：
//   - D3 平行重做：V1 `compact/` 包零改动；本 task 全部新代码进 compactv2/
//   - D4 profile.AgentCompact：autocompact 复用既有 profile（V2 不新增 task profile），
//     DB Registry 路由到 qwen-plus / deepseek-v3-2 等长 context 非 thinking 模型
//   - D5 XML 包裹：summary 整段用 `<reference-only data-internal="true">...</reference-only>`
//     标签包裹（替代旧 `[REFERENCE ONLY]` 文本前缀），与 task 2.5 Streaming Scrubber
//     的 whitelist 标签约定保持一致；data-internal="true" 是 Scrubber 白名单标识
//
// 参考：
//   - spec /Users/zhiyuchen/Downloads/有数-Agent-Mode-V1.5-NDF-spec/02-context/task-04-autocompact.md
//   - README §D5 — XML 边界 vs `[REFERENCE ONLY]` 前缀对比
//   - task 2.3 已声明 PruneThresholdRatio (0.50) / MicrocompactThreshold (0.70)；
//     本文件追加 AutocompactThreshold (0.70) / HardLimitRatio (0.85) 及配套常量（T7 下调，见各常量注释）
package compactv2

const (
	// AutocompactThreshold 是 L3 autocompact 触发的 token usage 比例阈值（estimated / context_window）。
	// 到达该比例时调用 LLM 把历史压缩成 12 段固定模板（见 prompt.go AutocompactPromptTemplate）。
	//
	// T7 (#8, 2026-06-05): 0.85 → 0.70，依据 prod context_budget_event 2453 条真实调用分析：
	// estimateTokensEino 的 bytes/4 估算在长尾严重低估真实 token——36% 调用实际 > 估算，
	// p90 calibration_ratio=1.51×（思考模型如 claude-thinking 达 1.89×）。85% 触发线下，
	// 估算 85% 时真实用量在 p90 已 ≈128% 撑爆 context，compaction 来不及触发即 prompt_too_long。
	// 0.70 触发线使 0.70×1.51≈1.05 覆盖到 p90 长尾。注：旧注释提到的 L1/L2 (PruneThresholdRatio
	// 0.50 / MicrocompactThreshold 0.70) 在 V2 adapter 路径未接线（vestigial），故下调到 0.70 无冲突。
	// 更彻底的修法=estimateTokensEino 改用 contextbudget 分语言估算器(zh=0.60)——记为 follow-up。
	AutocompactThreshold = 0.70

	// AutocompactPreserveRecentMessages 是 autocompact 后保留的最近 messages 数（不进 LLM summary）。
	// 设计：autocompact 替换的是 [1:cut] 区间，保 systemMsg (index 0) + recent N，
	// 切点 cut 由 alignToolCallPairs 校正到 tool_calls/tool_result 对的完整边界。
	AutocompactPreserveRecentMessages = 5

	// AutocompactBufferTokens 是 autocompact 后期望空出的 token buffer（用于估算 summary 上限）。
	// 期望摘要后 messages 的 context usage 下降到 (limit - buffer) 以下，留余地继续 ReAct。
	// 当前仅作配置参考，runner.go maybeCompactV2 不直接使用（用 ratio 判定）。
	AutocompactBufferTokens = 13_000

	// AutocompactMaxSummaryTokens 是 LLM 生成 summary 的 MaxTokens 上限。
	// 12 段模板 + 平均填充 ~3000 中文字符 → ~750-1000 真实 token；4000 提供 4x 安全余量。
	// 超过上限 → 模型 truncate → summary 缺末段或 </reference-only> 闭标签 → 校验失败计入 ConsecutiveFailures。
	AutocompactMaxSummaryTokens = 4_000

	// AutocompactTemperature 是 summary 生成的 temperature。
	// 低温（0.3）减少 hallucinate，但允许少量自然语言变化（vs 0.0 完全确定性，可能过于死板）。
	// V1 compact 包用 0.0；V2 选 0.3 因为 12 段模板下"无内容写'无'"需要轻微判断弹性。
	AutocompactTemperature = 0.3

	// MaxConsecutiveAutocompactFailures 是触发 terminal "context_exhausted" 的连续失败次数阈值。
	// 失败计入：LLM error + XML 开闭标签校验失败。
	// 达到 3 次（含本次）即 break circuit；caller 设 run.Status="terminated" + state_reason="context_exhausted"。
	MaxConsecutiveAutocompactFailures = 3

	// HardLimitRatio 是触发 hard limit 评估的 token usage 比例。
	// 当 ratio >= HardLimitRatio 且 state.ConsecutiveAutocompactFailures >= 3 → terminate；
	// 否则仍走 autocompact 重试一次（让 break circuit 晚于触发线生效，避免触发线单次失败立刻 terminate）。
	//
	// T7 (#8, 2026-06-05): 0.95 → 0.85，与触发线 0.70 同步下移（保持触发→breaker 的重试带）。
	// 0.95 估算下真实用量 p90 已 ≈143% 远超 context，breaker 太晚；0.85 让连续失败时更早熔断。
	HardLimitRatio = 0.85

	// AutocompactOpenTag / AutocompactCloseTag 是 D5 摘要 XML 包裹的开闭标签字面值。
	//
	// 校验逻辑：callLLMForSummary 在 TrimSpace 后用 strings.HasPrefix / HasSuffix 比对。
	// 校验失败 → 计入 ConsecutiveAutocompactFailures，连续 3 次 → terminate。
	//
	// data-internal="true" 是 task 2.5 Scrubber 的白名单标识：
	// - 用户输入若恰好含 <reference-only> 但缺 data-internal="true" 属性 → Scrubber 不剥离（保护用户输入）
	// - 内部生成的 summary 一律带此属性 → Scrubber 一律剥离 stream output 中的 echo（防 LLM 把 summary 内容回放给用户）
	AutocompactOpenTag  = `<reference-only data-internal="true">`
	AutocompactCloseTag = `</reference-only>`
)
