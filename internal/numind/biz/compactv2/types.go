// Package compactv2 实现 Agent Mode V1.5 板块 2「上下文管理 V2」的平行重做版本。
//
// 关键架构决策（D3 — 平行重做）：
//   - V1 包 `internal/numind/biz/compact/` **完全保留不动**（零 diff）
//   - 所有 V2 新代码进 `internal/numind/biz/compactv2/`（本包）独立实现
//   - DB 新增独立 `*_v2` 字段（agent_run.compact_state_v2 等），不动现有 compact_state / compact_summary
//   - agent mode 通过 RunRequest.UseCompactV2 feature flag 走 V2，其他场景（SOP / SalesRAG / 监控）继续 V1
//   - 6 个月稳定期后再评估替换 V1（不在 V1.5 范围）
//
// Task 2.1 范围（本文件）：
//   - 类型定义：CompactStateV2 / MessageV2 / MessageMetaV2
//   - 统一入口：NewMessageFromJSON（兜底 meta nil / uuid 缺失）
//
// 后续 task 范围：
//   - Task 2.2: L0 tool artifact 写盘（tool_artifact.go / read_tool_artifact 工具）
//   - Task 2.3: L1 prune + L2 microcompact（compactor.go，不调 LLM）
//   - Task 2.4: L3 autocompact（autocompact.go，12 段固定模板 + <reference-only> XML 包裹）
//   - Task 2.5: Streaming Context Scrubber（scrubber/）
package compactv2

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// CompactStateV2 持久化到 agent_run.compact_state_v2（JSON）。
//
// 与 V1 `compact.CompactStateV1` 完全独立，不继承也不 embed。V1 状态字段（agent_run.compact_state）
// 由 V1 包独立读写，不影响 V2。
//
// 字段说明：
//   - CurrentPhase: 当前降级阶段（active|L1_pruned|L2_microcompacted|L3_summarized）
//   - EstimatedTokens: 当前 messages 估算 token 总量（NUM_CHARS_PER_TOKEN=4 估算 + provider usage 校准）
//   - ConsecutiveAutocompactFailures: 连续 autocompact 失败次数，达 MAX_CONSECUTIVE_AUTOCOMPACT_FAILURES 触发 terminal
//   - SummaryMessageUUID: L3 autocompact 产出的 summary 消息 UUID，用于后续 reconcile 与去重
//   - LastCompactionAt: 上次任意一层 compact 触发时间
//   - TotalAutocompactRuns: L3 autocompact 累计触发次数（用于运营指标 + 风险评估）
type CompactStateV2 struct {
	CurrentPhase                   string    `json:"current_phase,omitempty"`
	EstimatedTokens                int       `json:"estimated_tokens,omitempty"`
	ConsecutiveAutocompactFailures int       `json:"consecutive_autocompact_failures,omitempty"`
	SummaryMessageUUID             string    `json:"summary_message_uuid,omitempty"`
	LastCompactionAt               time.Time `json:"last_compaction_at,omitempty"`
	TotalAutocompactRuns           int       `json:"total_autocompact_runs,omitempty"`
}

// MessageV2 是 compactv2 包对 agent_run.messages JSON entry 的解读。
//
// V2 在 messages JSON entry 中追加 `uuid` + `meta` 字段；旧 entry（无 uuid / meta）reader
// 视为 active 且生成 transient uuid（不写回 DB）。V1 包 read 时遇到 V2 新字段直接忽略（向后兼容）。
//
// 注意：V2 不强行 migrate 旧 messages。旧 run 的 messages JSON 进入 V2 路径时由
// NewMessageFromJSON 兜底处理。
type MessageV2 struct {
	UUID             string           `json:"uuid,omitempty"` // V2 新增；缺失 reader 兜底生成 transient（不写回 DB）
	Role             string           `json:"role"`
	Content          string           `json:"content"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
	ToolCalls        []map[string]any `json:"tool_calls,omitempty"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	HasFileRef       bool             `json:"has_file_ref,omitempty"`
	IsCompactMark    bool             `json:"is_compact_mark,omitempty"`
	Meta             *MessageMetaV2   `json:"meta,omitempty"` // V2 新增；nil 视为 active
}

// MessageMetaV2 是 MessageV2 的 V2 专属元数据。
//
// nil 时 reader 应当兜底为 active（task 2.3/2.4 实现细节）。
// 字段说明：
//   - IsCompacted: 是否已被 compact（L1/L2/L3 任一层）
//   - CompactionPhase: 该消息所属的降级阶段（active|L0|L1|L2|L3）
//   - OriginalSizeBytes: 原始内容字节数（compact 前），用于估算/统计
//   - ArtifactRef: 若内容已写盘（L0），存 agent_tool_artifact.uuid
//   - Preview: 前 200 字预览（compact 后保留少量上下文）
//   - CompactedAt: 该消息进入当前 CompactionPhase 的时间
//   - ToolName: role="tool" 的消息必填，task 2.3 L2 microcompact 用
//   - TurnIndex: task 2.3 L1 prune 用（旧 turn 优先清理）
//   - Timestamp: 消息原始时间戳（可选）
type MessageMetaV2 struct {
	IsCompacted       bool      `json:"is_compacted,omitempty"`
	CompactionPhase   string    `json:"compaction_phase,omitempty"`
	OriginalSizeBytes int64     `json:"original_size_bytes,omitempty"`
	ArtifactRef       string    `json:"artifact_ref,omitempty"`
	Preview           string    `json:"preview,omitempty"`
	CompactedAt       time.Time `json:"compacted_at,omitempty"`
	ToolName          string    `json:"tool_name,omitempty"`
	TurnIndex         int       `json:"turn_index,omitempty"`
	Timestamp         time.Time `json:"timestamp,omitempty"`
}

// NewMessageFromJSON 是 V2 解析 messages JSON entry 的**统一入口**。
//
// 兜底语义（R6 — 硬约束，task 2.2/2.3/2.4 禁止直接 json.Unmarshal 到 MessageV2）：
//  1. meta nil → 不补 meta（保留 nil；调用方应当将 nil meta 视为 active）
//  2. uuid 缺失 → 生成 transient uuid（uuid.NewString()），**不写回 DB**
//  3. JSON unmarshal 失败 → 返回 error
//
// 旧 run 的 messages（无 uuid / meta）经此 helper 即可在 V2 路径下安全使用。
func NewMessageFromJSON(raw []byte) (MessageV2, error) {
	var msg MessageV2
	if err := json.Unmarshal(raw, &msg); err != nil {
		return MessageV2{}, fmt.Errorf("compactv2.NewMessageFromJSON unmarshal: %w", err)
	}
	if msg.UUID == "" {
		// transient uuid — 仅本次解析使用，不写回 DB（调用方若要持久化必须显式 set）
		msg.UUID = uuid.NewString()
	}
	// meta nil 不兜底（保留 nil 由调用方判断 active），参见 R6 + spec §设计要点边界 ①
	return msg, nil
}
