package compactv2

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeToolMsg 构造一条 V2 风格的 role="tool" message（测试 helper）。
//
// turn = msg.Meta.TurnIndex；为 0 时 Meta 仍 non-nil，由 caller 决定是否 prune。
func makeToolMsg(toolName string, turn int, content string, ts time.Time) MessageV2 {
	return MessageV2{
		Role:       "tool",
		ToolCallID: "tc-" + toolName + "-" + content,
		Content:    content,
		Meta: &MessageMetaV2{
			TurnIndex: turn,
			ToolName:  toolName,
			Timestamp: ts,
		},
	}
}

// TestPruneOldToolResults_Basic — spec §验证策略 case 1：
// 12 轮 ReAct，currentTurn=12，10 个 tool result 分布 turn=1..10。
// 期望：
//   - turn 1..7（age=11..5）触发 prune（age ≥ MinAge=5 且 age > ProtectRecent=3）
//   - turn 8..10（age=4..2）保留（age <= ProtectRecent=3 或 age < MinAge=5）
//
// 注：age=4 (turn=8) 是 `< MinAge=5` 跳过；age=2/3 (turn=9/10) 是 `<= ProtectRecent=3` 跳过。
// 同时 spec §验证策略 case 5（保护窗口边界）：currentTurn=10, turn=7 age=3 应保留；
// turn=6 age=4 应保留；turn=5 age=5 应 prune — 综合验证。
func TestPruneOldToolResults_Basic(t *testing.T) {
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	msgs := []MessageV2{}
	for turn := 1; turn <= 10; turn++ {
		ts := now.Add(-time.Duration(11-turn) * time.Minute) // turn=1 最旧，turn=10 最新
		msgs = append(msgs, makeToolMsg("file_read", turn, "content-turn-"+itoa(turn), ts))
	}

	result, pruned := PruneOldToolResults(msgs, 12, now)

	assert.Equal(t, 7, pruned, "should prune turn 1..7")
	// turn 1..7 (age=11..5) → pruned
	for i := 0; i < 7; i++ {
		assert.True(t, result[i].Meta.IsCompacted, "turn %d should be pruned", i+1)
		assert.Equal(t, "L1", result[i].Meta.CompactionPhase)
		assert.Contains(t, result[i].Content, "Old tool result cleared")
		assert.Contains(t, result[i].Content, "file_read")
		assert.Greater(t, result[i].Meta.OriginalSizeBytes, int64(0))
	}
	// turn 8..10 (age=4..2) → preserved
	for i := 7; i < 10; i++ {
		assert.False(t, result[i].Meta.IsCompacted, "turn %d should NOT be pruned", i+1)
		assert.Equal(t, "content-turn-"+itoa(i+1), result[i].Content)
	}
}

// TestPruneOldToolResults_ProtectWindow — spec §验证策略 case 5（保护窗口边界细分）。
//
// 显式独立测试三档：
//   - currentTurn=10, turn=7 (age=3) → 保留（age <= PROTECT_RECENT=3）
//   - currentTurn=10, turn=6 (age=4) → 保留（age < MIN_AGE=5）
//   - currentTurn=10, turn=5 (age=5) → prune（age ≥ MIN_AGE 且 > PROTECT_RECENT）
func TestPruneOldToolResults_ProtectWindow(t *testing.T) {
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	msgs := []MessageV2{
		makeToolMsg("file_read", 5, "old-5", now.Add(-5*time.Minute)),
		makeToolMsg("file_read", 6, "edge-6", now.Add(-4*time.Minute)),
		makeToolMsg("file_read", 7, "recent-7", now.Add(-3*time.Minute)),
	}
	result, pruned := PruneOldToolResults(msgs, 10, now)
	assert.Equal(t, 1, pruned, "only turn=5 (age=5) should prune")
	assert.True(t, result[0].Meta.IsCompacted, "turn=5 age=5 → prune")
	assert.False(t, result[1].Meta.IsCompacted, "turn=6 age=4 → preserve (< MinAge)")
	assert.False(t, result[2].Meta.IsCompacted, "turn=7 age=3 → preserve (<= ProtectRecent)")
}

// TestMicrocompactByToolName_Basic — spec §验证策略 case 2：
// file_read 6 次 + bash 2 次。
// 期望：file_read 前 3 个 compacted，后 3 个保留；bash 全保留（2 <= keep=3）。
func TestMicrocompactByToolName_Basic(t *testing.T) {
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	msgs := []MessageV2{}
	for i := 1; i <= 6; i++ {
		msgs = append(msgs, makeToolMsg("file_read", i, "fr-"+itoa(i), now))
	}
	msgs = append(msgs, makeToolMsg("bash", 7, "bash-1", now))
	msgs = append(msgs, makeToolMsg("bash", 8, "bash-2", now))

	result, compacted := MicrocompactByToolName(msgs, time.Now())
	assert.Equal(t, 3, compacted, "first 3 file_read should be L2 compacted")

	// First 3 file_read → compacted
	for i := 0; i < 3; i++ {
		assert.True(t, result[i].Meta.IsCompacted, "file_read[%d] should be L2", i)
		assert.Equal(t, "L2", result[i].Meta.CompactionPhase)
		assert.Contains(t, result[i].Content, "superseded by newer call")
		assert.Contains(t, result[i].Content, "file_read")
	}
	// Last 3 file_read → preserved
	for i := 3; i < 6; i++ {
		assert.False(t, result[i].Meta.IsCompacted, "file_read[%d] should preserve", i)
		assert.Equal(t, "fr-"+itoa(i+1), result[i].Content)
	}
	// bash entries → preserved (2 <= keep=3)
	assert.False(t, result[6].Meta.IsCompacted)
	assert.False(t, result[7].Meta.IsCompacted)
}

// TestL0_AlreadyCompacted_Skipped — spec §验证策略 case 3：
// 5 个 tool result，其中 2 个已 L0 写盘 (IsCompacted=true CompactionPhase="L0")。
// 期望：L1 + L2 都不动这 2 个 L0 entry，原 Content / Meta 完全保留。
func TestL0_AlreadyCompacted_Skipped(t *testing.T) {
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	msgs := []MessageV2{
		makeToolMsg("file_read", 1, "fr-1", now.Add(-10*time.Minute)),
		makeToolMsg("file_read", 2, "fr-2", now.Add(-9*time.Minute)),
		// L0 写盘形态
		{
			Role:       "tool",
			ToolCallID: "tc-l0",
			Content:    `<persisted-output ref="abc-uuid" tool="file_read" size="20000">PREVIEW</persisted-output>`,
			Meta: &MessageMetaV2{
				IsCompacted:       true,
				CompactionPhase:   "L0",
				OriginalSizeBytes: 20000,
				ToolName:          "file_read",
				ArtifactRef:       "abc-uuid",
				TurnIndex:         3,
				Timestamp:         now.Add(-8 * time.Minute),
			},
		},
		makeToolMsg("file_read", 4, "fr-4", now.Add(-7*time.Minute)),
		// 又一个 L0
		{
			Role:       "tool",
			ToolCallID: "tc-l0-2",
			Content:    `<persisted-output ref="def-uuid" tool="file_read" size="30000">PREVIEW2</persisted-output>`,
			Meta: &MessageMetaV2{
				IsCompacted:       true,
				CompactionPhase:   "L0",
				OriginalSizeBytes: 30000,
				ToolName:          "file_read",
				ArtifactRef:       "def-uuid",
				TurnIndex:         5,
				Timestamp:         now.Add(-6 * time.Minute),
			},
		},
	}

	// L1 prune: currentTurn=20 → 所有 active entry 应 prune（age>>5），L0 entry 应跳过
	_, prunedL1 := PruneOldToolResults(msgs, 20, now)
	assert.Equal(t, 3, prunedL1, "3 active entries should L1-prune, 2 L0 entries skipped")
	// Verify L0 untouched
	assert.Equal(t, "L0", msgs[2].Meta.CompactionPhase, "L0 entry[2] phase unchanged")
	assert.Contains(t, msgs[2].Content, "persisted-output", "L0 entry[2] content unchanged")
	assert.Equal(t, "L0", msgs[4].Meta.CompactionPhase, "L0 entry[4] phase unchanged")
	assert.Contains(t, msgs[4].Content, "persisted-output", "L0 entry[4] content unchanged")

	// L2 microcompact: 3 active 已 L1，剩 2 L0 — 都 IsCompacted=true，L2 应不动任何
	_, compactedL2 := MicrocompactByToolName(msgs, time.Now())
	assert.Equal(t, 0, compactedL2, "after L1, all active are L1-compacted; L0 entries also IsCompacted → 0 L2")
}

// TestL1_L2_Composed — spec §验证策略 case 4：
// 70% 触发场景叠加：10 个 file_read 跨多轮 + 几个其它工具。
// 先跑 L2 再跑 L1 → 期望 L2 处理 7 个最旧（保最近 3），L1 再扫剩余 active 中 age≥5 的。
func TestL1_L2_Composed(t *testing.T) {
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	// 10 个 file_read，跨 turn=1..10
	msgs := []MessageV2{}
	for i := 1; i <= 10; i++ {
		msgs = append(msgs, makeToolMsg("file_read", i, "fr-"+itoa(i), now.Add(-time.Duration(11-i)*time.Minute)))
	}
	currentTurn := 12

	// 先 L2：file_read 10 个 → 保最后 3，前 7 个 L2
	_, l2n := MicrocompactByToolName(msgs, time.Now())
	assert.Equal(t, 7, l2n)
	// 然后 L1：file_read 后 3 个 (turn=8/9/10) age=4/3/2，都不 prune
	_, l1n := PruneOldToolResults(msgs, currentTurn, now)
	assert.Equal(t, 0, l1n, "remaining 3 active entries are all within ProtectRecent or MinAge")

	// 验证最终状态：前 7 个 L2，后 3 个 active
	for i := 0; i < 7; i++ {
		assert.Equal(t, "L2", msgs[i].Meta.CompactionPhase)
	}
	for i := 7; i < 10; i++ {
		assert.False(t, msgs[i].Meta.IsCompacted)
	}
}

// TestL1_L2_Composed_AdditiveSweep — 进阶叠加：
// 12 个 file_read 分布 turn=1..12，currentTurn=15。
// 先 L2 (保最后 3：turn 10/11/12) → 前 9 个 L2 mark。
// 再 L1：剩余 active=turn 10/11/12，age=5/4/3。
//   - turn 10 (age=5) → 满足 age≥MinAge=5 且 age>ProtectRecent=3 → L1 prune
//   - turn 11 (age=4) → age<MinAge=5 → skip
//   - turn 12 (age=3) → age<=ProtectRecent=3 → skip
//
// 期望：L2 处理 9 个 + L1 再处理 1 个（turn 10）。
// 这验证"L1/L2 短路语义"：L2 已处理的不再被 L1 重复处理（IsCompacted=true 跳过）。
func TestL1_L2_Composed_AdditiveSweep(t *testing.T) {
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	msgs := []MessageV2{}
	for i := 1; i <= 12; i++ {
		msgs = append(msgs, makeToolMsg("file_read", i, "fr-"+itoa(i), now.Add(-time.Duration(13-i)*time.Minute)))
	}
	_, l2n := MicrocompactByToolName(msgs, time.Now())
	assert.Equal(t, 9, l2n, "first 9 file_read (turn 1..9) → L2")
	_, l1n := PruneOldToolResults(msgs, 15, now)
	assert.Equal(t, 1, l1n, "turn 10 (age=5) is the only remaining old entry; turn 11/12 protected")

	// Verify final phases
	for i := 0; i < 9; i++ {
		assert.Equal(t, "L2", msgs[i].Meta.CompactionPhase, "turn %d → L2", i+1)
	}
	assert.Equal(t, "L1", msgs[9].Meta.CompactionPhase, "turn 10 → L1 (post-L2 sweep)")
	assert.False(t, msgs[10].Meta.IsCompacted, "turn 11 → preserved")
	assert.False(t, msgs[11].Meta.IsCompacted, "turn 12 → preserved")
}

// TestEstimateMessagesTokens_Basic — spec §验证策略 case 6：
// 已知字符数的 messages → totalChars/4。
func TestEstimateMessagesTokens_Basic(t *testing.T) {
	msgs := []MessageV2{
		{Role: "user", Content: strings.Repeat("a", 400)},      // 400 chars
		{Role: "assistant", Content: strings.Repeat("b", 200)}, // 200 chars
	}
	got := EstimateMessagesTokens(msgs)
	assert.Equal(t, int64(150), got, "600 chars / 4 = 150 tokens")
}

// TestEstimateMessagesTokens_WithToolCalls — 含 ToolCalls 的估算。
// ToolCalls JSON marshal 字符串 length 计入 totalChars。
func TestEstimateMessagesTokens_WithToolCalls(t *testing.T) {
	toolCalls := []map[string]any{
		{
			"id":   "call-1",
			"type": "function",
			"function": map[string]any{
				"name":      "file_read",
				"arguments": `{"path":"/tmp/foo.txt"}`,
			},
		},
	}
	msgs := []MessageV2{
		{
			Role:      "assistant",
			Content:   "I'll read the file.",
			ToolCalls: toolCalls,
		},
	}
	got := EstimateMessagesTokens(msgs)
	// Content = 19 chars + ToolCalls JSON marshal ≈ 90+ chars; / 4 ≈ 27+
	assert.Greater(t, got, int64(20))
	// Also include ReasoningContent
	msgs[0].ReasoningContent = strings.Repeat("r", 400)
	got2 := EstimateMessagesTokens(msgs)
	assert.GreaterOrEqual(t, got2, got+100, "+400 chars reasoning → +100 tokens at least")
}

// TestEstimateMessagesTokens_ChineseCharacters — spec §风险 R4：
// 中文 char/4 偏差。验证估算返回 totalChars/4，调用方需在 runner 用 max(estimated, actual) 校准。
//
// 这个 case 是"诚实声明"：本估算对中文偏低估，需要 provider usage 校准。
func TestEstimateMessagesTokens_ChineseCharacters(t *testing.T) {
	// 中文：每字 ≈ 3 bytes (UTF-8)，但 len() 返回 byte 数；100 个中文字 ≈ 300 bytes
	chinese := strings.Repeat("中", 100)
	msgs := []MessageV2{
		{Role: "user", Content: chinese},
	}
	got := EstimateMessagesTokens(msgs)
	// len() = 300 bytes / 4 = 75 tokens
	assert.Equal(t, int64(75), got, "100 Chinese chars (300 bytes UTF-8) / 4 = 75 tokens")
	// 实际 tokenizer 通常算 100~150 token；本估算偏低估，需 runner.maybeCompactV2 取 max
}

// TestPruneOldToolResults_AssistantUntouched — spec §设计要点边界 ⑤：
// assistant 的 reasoning + tool_calls 不动。
func TestPruneOldToolResults_AssistantUntouched(t *testing.T) {
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	msgs := []MessageV2{
		{
			Role:             "assistant",
			Content:          "thinking...",
			ReasoningContent: strings.Repeat("r", 1000),
			ToolCalls:        []map[string]any{{"id": "tc1"}},
			Meta:             &MessageMetaV2{TurnIndex: 1, Timestamp: now.Add(-10 * time.Minute)},
		},
		makeToolMsg("file_read", 1, "fr-1", now.Add(-10*time.Minute)),
	}
	_, pruned := PruneOldToolResults(msgs, 20, now)
	assert.Equal(t, 1, pruned, "only the tool entry, not assistant")
	assert.Equal(t, "thinking...", msgs[0].Content, "assistant content untouched")
	assert.NotNil(t, msgs[0].ToolCalls, "assistant tool_calls untouched")
	assert.Equal(t, strings.Repeat("r", 1000), msgs[0].ReasoningContent, "reasoning untouched")
}

// TestMicrocompactByToolName_EmptyToolName_Skipped — spec §设计要点边界 ④：
// Meta.ToolName 空串的 entry 不入桶。
func TestMicrocompactByToolName_EmptyToolName_Skipped(t *testing.T) {
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	msgs := []MessageV2{
		// 4 个无 ToolName tool 消息 — 不应被 L2 误聚合并 compact
		{Role: "tool", Content: "no-name-1", Meta: &MessageMetaV2{TurnIndex: 1, Timestamp: now}},
		{Role: "tool", Content: "no-name-2", Meta: &MessageMetaV2{TurnIndex: 2, Timestamp: now}},
		{Role: "tool", Content: "no-name-3", Meta: &MessageMetaV2{TurnIndex: 3, Timestamp: now}},
		{Role: "tool", Content: "no-name-4", Meta: &MessageMetaV2{TurnIndex: 4, Timestamp: now}},
	}
	_, compacted := MicrocompactByToolName(msgs, time.Now())
	assert.Equal(t, 0, compacted, "empty ToolName → not compacted")
	for i := range msgs {
		assert.False(t, msgs[i].Meta.IsCompacted, "msg %d untouched", i)
	}
}

// TestPruneOldToolResults_NilMetaSkipped — V1 兼容性：
// 旧 V1 messages（无 meta）经 NewMessageFromJSON 兜底成 meta=nil，本函数不应误 prune。
func TestPruneOldToolResults_NilMetaSkipped(t *testing.T) {
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	// V1 旧消息：role=tool 但 meta=nil
	raw := []byte(`{"role":"tool","content":"old-v1-content","tool_call_id":"tc-v1"}`)
	msg, err := NewMessageFromJSON(raw)
	require.NoError(t, err)
	assert.Nil(t, msg.Meta, "V1 message should parse with nil Meta")

	msgs := []MessageV2{msg}
	_, pruned := PruneOldToolResults(msgs, 100, now)
	assert.Equal(t, 0, pruned, "V1 messages (Meta nil) must not be pruned")
	assert.Equal(t, "old-v1-content", msgs[0].Content)
}

// TestDurationSince — 人类可读时间差边界。
func TestDurationSince(t *testing.T) {
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	assert.Equal(t, "0s", durationSince(time.Time{}, now), "zero time → 0s")
	assert.Equal(t, "0s", durationSince(now.Add(time.Hour), now), "future time → 0s")
	assert.Equal(t, "5s", durationSince(now.Add(-5*time.Second), now))
	assert.Equal(t, "5m", durationSince(now.Add(-5*time.Minute), now))
	assert.Equal(t, "2h", durationSince(now.Add(-2*time.Hour), now))
	assert.Equal(t, "3d", durationSince(now.Add(-3*24*time.Hour), now))
}

// TestEstimateMessagesTokens_ToolCallsJSONMarshalErrorTolerant —
// 防御性：ToolCalls 不可序列化时跳过该 entry 的 ToolCalls 部分（不报错也不 panic）。
// 当前 ToolCalls 类型是 []map[string]any，理论上总能 marshal；这个 test 主要确保实现路径有 err 分支。
func TestEstimateMessagesTokens_ToolCallsJSONMarshalErrorTolerant(t *testing.T) {
	// 用一个不可 marshal 的 channel 类型（json: unsupported type: chan int）— 不会 panic
	bad := make(chan int)
	msgs := []MessageV2{
		{
			Role:      "assistant",
			Content:   "hello",
			ToolCalls: []map[string]any{{"bad": bad}},
		},
	}
	// 不该 panic，只是 ToolCalls 的字符贡献为 0
	got := EstimateMessagesTokens(msgs)
	assert.Equal(t, int64(len("hello")/NumCharsPerToken), got)

	// Sanity: json.Marshal indeed errors on chan
	_, err := json.Marshal(msgs[0].ToolCalls)
	assert.Error(t, err)
}

// itoa is a tiny helper to avoid importing strconv just for one digit.
func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}
