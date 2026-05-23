package compactv2

// Task 2.1 仅声明本文件所需常量；其余阈值（PruneThresholdRatio / MicrocompactThreshold /
// AutocompactThreshold / HardLimitRatio / AutocompactBufferTokens /
// MaxConsecutiveAutocompactFailures / AutocompactPreserveRecent）由 task 2.2-2.4 追加。
//
// 参考板块 README §D2「阈值常数」。
//
// 命名风格：PascalCase（Go 惯例 + 与 V1 compact 包 DefaultMaxTokens / EscalatedMaxTokens 一致）。
const (
	// ToolArtifactSizeLimit 是 task 2.2 触发 L0 tool result 写盘的阈值。
	// 超过此值的 tool result 内容写到 agent_tool_artifact 表对应的 file_path，
	// messages 里只保留 ArtifactRef + Preview。
	ToolArtifactSizeLimit = 16 * 1024

	// ArtifactPreviewBytes 是 agent_tool_artifact.preview 字段保留的前缀字节数。
	// 用于 LLM 在不读全文的情况下做基本判断。
	ArtifactPreviewBytes = 1024

	// ArtifactDefaultTTLDays 是 artifact 默认 TTL（天）。
	// 超时的 artifact 由 cleanup cron（task 2.2 实现）按 is_expired 标记 + 物理删除。
	// 已 cleanup 的 artifact 在 LLM read_tool_artifact 时返回 "[Artifact expired]"。
	ArtifactDefaultTTLDays = 30

	// ToolArtifactReadMaxLimit 是 read_tool_artifact 单次返回的字节上限。
	// 与写盘阈值 ToolArtifactSizeLimit 对齐：避免 read_tool_artifact 自己返回 >16KB
	// 反过来撑爆 LLM context（导致整个 L0 写盘机制失效）。
	// 客户端传入的 limit 若超过此值会被 clamp。
	ToolArtifactReadMaxLimit = 16 * 1024

	// ArtifactCleanupBatchSize 是 cleanup cron 单次扫描的最大行数。
	// 防止单次 run 时间过长锁表；剩余的 expired artifact 留待下一轮 cron。
	ArtifactCleanupBatchSize = 10000

	// ── Task 2.3: L1 prune + L2 microcompact 阈值 ─────────────────────────────
	// （`AutocompactThreshold = 0.85` 由 task 2.4 追加，不在本 task 范围）

	// PruneThresholdRatio 是 L1 prune 触发的 token usage 比例阈值（estimated / context_window）。
	// 到达 50% 时仅跑 L1 prune（清旧 tool result placeholder，不调 LLM）。
	PruneThresholdRatio = 0.50

	// MicrocompactThreshold 是 L1 + L2 同时触发的 token usage 比例阈值。
	// 到达 70% 时先跑 L2 microcompact（同名 tool 合并），再跑 L1 prune（旧 tool result）。
	MicrocompactThreshold = 0.70

	// PruneMinAgeTurns 是 L1 prune 的最低 age（currentTurn - msg.TurnIndex）。
	// 仅 ≥5 轮前的 tool result 才可能被 prune；保护近期推理链。
	PruneMinAgeTurns = 5

	// PruneProtectRecentTurns 是 L1 prune 的"绝对保护窗口"。
	// 最近 3 轮无论 age 如何都不被 prune（即使 age >= 5 也跳过；常发生在重启后 currentTurn 跳跃场景）。
	PruneProtectRecentTurns = 3

	// MicrocompactKeepPerTool 是 L2 microcompact 每个 tool name 至少保留的最新 envelope 个数。
	// 同名 tool 超过 3 个 envelope 时，把"较旧的多余 envelope"替换为 placeholder。
	MicrocompactKeepPerTool = 3

	// NumCharsPerToken 是 token 粗估系数（content 字符数 / 4 ≈ token 数）。
	// 对中文偏低估 ~20%，由 `max(estimated, actual)` + provider usage 校准兜底。
	NumCharsPerToken = 4
)
