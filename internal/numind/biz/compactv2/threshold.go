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
)
