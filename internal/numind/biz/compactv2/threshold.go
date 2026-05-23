// Package compactv2 — V2 context-management 公共常量 + 哨兵 error。
//
// 接入位置（生产路径）：
//
//   - `internal/numind/biz/agent/adapter_compactv2.go`：读 Autocompact* / Hard* 阈值
//   - NumCharsPerToken 估算 token + ErrContextExhausted 触发主循环 break
//   - `internal/numind/biz/agent/runner_v2_artifact.go`：读 ToolArtifactSizeLimit /
//     ArtifactPreviewBytes 决定 L0 写盘
//   - `internal/numind/biz/compactv2/artifact_cleanup.go`：读 ArtifactCleanupBatchSize
//   - `internal/numind/biz/compactv2/tool_read_artifact.go`：读 ToolArtifactReadMaxLimit
//
// 命名风格：PascalCase（Go 惯例）。
package compactv2

import "errors"

// ── L0 tool result write-to-disk（task 2.2） ──────────────────────────────────

// ToolArtifactSizeLimit 是 task 2.2 触发 L0 tool result 写盘的阈值。
// 超过此值的 tool result 内容写到 agent_tool_artifact 表对应的 file_path，
// messages 里只保留 ArtifactRef + Preview。
const ToolArtifactSizeLimit = 16 * 1024

// ArtifactPreviewBytes 是 agent_tool_artifact.preview 字段保留的前缀字节数。
// 用于 LLM 在不读全文的情况下做基本判断。
const ArtifactPreviewBytes = 1024

// ArtifactDefaultTTLDays 是 artifact 默认 TTL（天）。
// 超时的 artifact 由 cleanup cron 按 is_expired 标记 + 物理删除。
// 已 cleanup 的 artifact 在 LLM read_tool_artifact 时返回 "[Artifact expired]"。
const ArtifactDefaultTTLDays = 30

// ToolArtifactReadMaxLimit 是 read_tool_artifact 单次返回的字节上限。
// 与写盘阈值 ToolArtifactSizeLimit 对齐：避免 read_tool_artifact 自己返回 >16KB
// 反过来撑爆 LLM context（导致整个 L0 写盘机制失效）。
// 客户端传入的 limit 若超过此值会被 clamp。
const ToolArtifactReadMaxLimit = 16 * 1024

// ArtifactCleanupBatchSize 是 cleanup cron 单次扫描的最大行数。
// 防止单次 run 时间过长锁表；剩余的 expired artifact 留待下一轮 cron。
const ArtifactCleanupBatchSize = 10000

// ── Token estimation（adapter_compactv2.go） ──────────────────────────────────

// NumCharsPerToken 是 token 粗估系数（content 字符数 / 4 ≈ token 数）。
// 对中文偏低估 ~20%，由 LLM 真返回 prompt_too_long 时 adapter 的 ForcePTLRecover
// 兜底（强制 autocompact + retry）。
const NumCharsPerToken = 4

// ── Sentinel errors ─────────────────────────────────────────────────────────

// ErrContextExhausted 是 adapter compactor 连续 3 次 autocompact 失败 + ratio >= 95%
// 时返回的 sentinel error。
//
// 用法（runner.go 集成）：
//   - adapter.Generate 上抛该 error
//   - runner 主循环 errors.Is(err, compactv2.ErrContextExhausted) 检测后
//     调 terminateRunContextExhausted 写 DB state_reason="context_exhausted" +
//     break loop + 跳过最终 UpdateState（避免被 st.TerminalReason 覆盖）
var ErrContextExhausted = errors.New("compactv2: context exhausted (autocompact 3-fail breaker or hard limit)")
