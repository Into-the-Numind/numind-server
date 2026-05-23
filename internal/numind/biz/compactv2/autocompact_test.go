// Package compactv2 — task 2.4 Autocompact 单元测试。
//
// 覆盖 spec §验证策略 7 个 case + 1 个补充 hard limit case：
//  1. Success_12SectionTemplate_XMLWrapped — 合法 12 段 XML 包裹 summary 替换 messages
//  2. MissingOpenTag_CountsAsFailure — 缺开标签 → ConsecutiveAutocompactFailures+=1
//  3. MissingCloseTag_CountsAsFailure — 缺闭标签 → ConsecutiveAutocompactFailures+=1
//  4. 3FailureTerminate — 连续 3 次 LLM error → TerminalReason=context_exhausted
//  5. ToolCallPairAlignment — recent 5 切到 assistant.tool_calls 中间 → cut 回退
//  6. ArtifactRefNotExpanded — <persisted-output ref="xxx"/> 不被展开 / 不读盘
//  7. TooShortNoOp — messages < 7 → Triggered=false 不调 LLM
//     +. HardLimitWithoutPriorFailures_StillCalls — hard limit 但 failures<3 仍调 autocompact
//
// mock 注入方式：
//   - Chat：直接传 closure（ChatFn 是 func 类型），返回固定 summary 字符串或 error
//   - CompactV2Store：用 in-memory `fakeCompactV2Store` 实现，map 存 state + messages
package compactv2

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/model"
)

// ── 测试 helpers ──────────────────────────────────────────────────────────────

// fakeCompactV2Store 是 CompactV2StateStore 的 in-memory 实现，单测专用。
//
// 注：单测里 runID -> state / messages 都用 map，足够覆盖 round-trip 场景。
// 不实现并发安全（单测单线程跑），生产环境用真实 store.IAgentCompactV2Store。
type fakeCompactV2Store struct {
	states   map[uint64]*CompactStateV2
	messages map[uint64][]MessageV2
	getErr   error
	putErr   error
	msgErr   error
}

func newFakeStore() *fakeCompactV2Store {
	return &fakeCompactV2Store{
		states:   make(map[uint64]*CompactStateV2),
		messages: make(map[uint64][]MessageV2),
	}
}

func (f *fakeCompactV2Store) GetCompactStateV2(_ context.Context, runID uint64) (*CompactStateV2, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	s, ok := f.states[runID]
	if !ok {
		return nil, nil // 模拟 store 行为：未写入返回 (nil, nil)
	}
	// 深拷贝避免单测之间互相污染（caller 修改 state 后再 UpdateCompactStateV2）
	cp := *s
	return &cp, nil
}

func (f *fakeCompactV2Store) UpdateCompactStateV2(_ context.Context, runID uint64, state *CompactStateV2) error {
	if f.putErr != nil {
		return f.putErr
	}
	if state == nil {
		return errors.New("fakeCompactV2Store.UpdateCompactStateV2: state nil")
	}
	cp := *state
	f.states[runID] = &cp
	return nil
}

func (f *fakeCompactV2Store) UpdateMessagesV2(_ context.Context, runID uint64, msgs []MessageV2) error {
	if f.msgErr != nil {
		return f.msgErr
	}
	cp := make([]MessageV2, len(msgs))
	copy(cp, msgs)
	f.messages[runID] = cp
	return nil
}

// validSummary 是符合 D5 XML 边界 + 12 段模板的合法 summary。
const validSummary = `<reference-only data-internal="true">
[CONTEXT COMPACTION — REFERENCE ONLY]
Below is a summary of earlier conversation. These are HISTORICAL events, NOT new requests.
Only respond to the most recent user message AFTER this summary block.

## 1. Active Task
用户在调研 X 项目。

## 2. Goal
得出 X 是否值得迁移。

## 3. Constraints
不能改老配置。

## 4. Completed Actions
- 读了 README
- 查了 issue 列表

## 5. Active State
当前 cursor 在 file A。

## 6. In Progress
比较 X / Y。

## 7. Blocked
无

## 8. Key Decisions
保留旧方案。

## 9. Resolved Questions
- Q1 → A1

## 10. Pending User Asks
无

## 11. Relevant Files / Artifacts
- /path/to/a.go
- artifact-uuid-1234

## 12. Critical Context
项目截止 Q3。
</reference-only>`

// makeRunWithNMessages 构造一个 model.AgentRun，含 n 条 messages：
//   - msgs[0]: role=system
//   - msgs[1..n-1]: 交替 user / assistant，role=user 时 content="user N"，role=assistant 时 content="reply N"
func makeRunWithNMessages(t *testing.T, n int) *model.AgentRun {
	t.Helper()
	msgs := make([]MessageV2, 0, n)
	msgs = append(msgs, MessageV2{Role: "system", Content: "you are an agent"})
	for i := 1; i < n; i++ {
		role := "user"
		content := "user msg " + strconv.Itoa(i)
		if i%2 == 0 {
			role = "assistant"
			content = "assistant msg " + strconv.Itoa(i)
		}
		msgs = append(msgs, MessageV2{Role: role, Content: content})
	}
	raw, err := json.Marshal(msgs)
	require.NoError(t, err)
	return &model.AgentRun{
		ID:       1,
		Messages: datatypes.JSON(raw),
	}
}

// successChatFn 返回固定的合法 summary。
func successChatFn(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
	return &aiservice.ChatResponse{Content: validSummary}, nil
}

// ── 单元测试 ─────────────────────────────────────────────────────────────────

// 1. Success — mock LLM 返回合法 12 段 + <reference-only> XML 包裹
// → messages 替换成 [system, summary, recent 5]
// → state.CurrentPhase=L3_summarized / TotalAutocompactRuns=1
func TestAutocompact_Success_12SectionTemplate_XMLWrapped(t *testing.T) {
	run := makeRunWithNMessages(t, 12) // 1 system + 11 talk = 12 total
	store := newFakeStore()
	deps := AutocompactDeps{
		Chat:           successChatFn,
		CompactV2Store: store,
		Metrics:        NoopMetrics{},
	}

	result, err := Autocompact(context.Background(), run, deps)
	require.NoError(t, err)
	assert.True(t, result.Triggered, "should trigger")
	assert.Empty(t, result.TerminalReason, "no terminal")
	assert.NotEmpty(t, result.SummaryUUID, "summary uuid set")

	// state 检查
	st, sErr := store.GetCompactStateV2(context.Background(), run.ID)
	require.NoError(t, sErr)
	require.NotNil(t, st)
	assert.Equal(t, "L3_summarized", st.CurrentPhase)
	assert.Equal(t, 1, st.TotalAutocompactRuns)
	assert.Equal(t, 0, st.ConsecutiveAutocompactFailures)
	assert.Equal(t, result.SummaryUUID, st.SummaryMessageUUID)

	// messages 检查：应替换成 [system, summary, recent 5]
	newMsgs, ok := store.messages[run.ID]
	require.True(t, ok)
	require.Len(t, newMsgs, 7, "system + summary + 5 recent")
	assert.Equal(t, "system", newMsgs[0].Role, "msg[0] systemMsg")
	assert.Equal(t, "system", newMsgs[1].Role, "msg[1] summary role=system (D5)")
	assert.True(t, strings.HasPrefix(strings.TrimSpace(newMsgs[1].Content), AutocompactOpenTag),
		"msg[1] starts with <reference-only data-internal=\"true\">")
	assert.True(t, strings.HasSuffix(strings.TrimSpace(newMsgs[1].Content), AutocompactCloseTag),
		"msg[1] ends with </reference-only>")
	require.NotNil(t, newMsgs[1].Meta)
	assert.True(t, newMsgs[1].Meta.IsCompacted)
	assert.Equal(t, "L3", newMsgs[1].Meta.CompactionPhase)
}

// 2. MissingOpenTag — LLM 缺 <reference-only data-internal="true"> 开标签 → ConsecutiveFailures++
func TestAutocompact_MissingOpenTag_CountsAsFailure(t *testing.T) {
	run := makeRunWithNMessages(t, 10)
	store := newFakeStore()

	// 缺开标签的 summary（直接进 12 段内容，没有 <reference-only> 包裹开头）
	badSummary := `[CONTEXT COMPACTION]
## 1. Active Task
test
</reference-only>`
	deps := AutocompactDeps{
		Chat: func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
			return &aiservice.ChatResponse{Content: badSummary}, nil
		},
		CompactV2Store: store,
		Metrics:        NoopMetrics{},
	}

	result, err := Autocompact(context.Background(), run, deps)
	// review P2 fix：单次失败时 Autocompact 透传原 LLM err 让 caller 诊断
	require.Error(t, err, "single failure should propagate llmErr for diagnostics")
	assert.Contains(t, err.Error(), "open tag", "err 应当含 XML 校验失败原因")
	assert.False(t, result.Triggered)
	assert.Empty(t, result.TerminalReason)

	// state.ConsecutiveAutocompactFailures+=1
	st, _ := store.GetCompactStateV2(context.Background(), run.ID)
	require.NotNil(t, st)
	assert.Equal(t, 1, st.ConsecutiveAutocompactFailures)
	assert.Equal(t, 0, st.TotalAutocompactRuns, "fail does not bump TotalAutocompactRuns")

	// messages 未替换
	_, ok := store.messages[run.ID]
	assert.False(t, ok, "messages unchanged on failure")
}

// 3. MissingCloseTag — LLM 缺 </reference-only> 闭标签 → ConsecutiveFailures++
func TestAutocompact_MissingCloseTag_CountsAsFailure(t *testing.T) {
	run := makeRunWithNMessages(t, 10)
	store := newFakeStore()

	// 缺闭标签
	badSummary := `<reference-only data-internal="true">
[CONTEXT COMPACTION]
## 1. Active Task
test
`
	deps := AutocompactDeps{
		Chat: func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
			return &aiservice.ChatResponse{Content: badSummary}, nil
		},
		CompactV2Store: store,
		Metrics:        NoopMetrics{},
	}

	result, err := Autocompact(context.Background(), run, deps)
	require.Error(t, err, "single failure (missing close tag) should propagate llmErr")
	assert.Contains(t, err.Error(), "close tag")
	assert.False(t, result.Triggered)
	assert.Empty(t, result.TerminalReason)

	st, _ := store.GetCompactStateV2(context.Background(), run.ID)
	require.NotNil(t, st)
	assert.Equal(t, 1, st.ConsecutiveAutocompactFailures)
}

// 4. 3FailureTerminate — 连续 3 次 LLM error → TerminalReason="context_exhausted"
func TestAutocompact_3FailureTerminate(t *testing.T) {
	run := makeRunWithNMessages(t, 10)
	store := newFakeStore()

	deps := AutocompactDeps{
		Chat: func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
			return nil, errors.New("LLM 502 bad gateway")
		},
		CompactV2Store: store,
		Metrics:        NoopMetrics{},
	}

	// 第 1 次：failures=1，无 terminal，但 err 透传（review P2 fix）
	r1, err := Autocompact(context.Background(), run, deps)
	require.Error(t, err, "first failure should propagate llmErr")
	assert.Contains(t, err.Error(), "502 bad gateway")
	assert.Empty(t, r1.TerminalReason)
	st, _ := store.GetCompactStateV2(context.Background(), run.ID)
	assert.Equal(t, 1, st.ConsecutiveAutocompactFailures)

	// 第 2 次：failures=2，无 terminal，err 仍透传
	r2, err := Autocompact(context.Background(), run, deps)
	require.Error(t, err, "second failure should propagate llmErr")
	assert.Empty(t, r2.TerminalReason)
	st, _ = store.GetCompactStateV2(context.Background(), run.ID)
	assert.Equal(t, 2, st.ConsecutiveAutocompactFailures)

	// 第 3 次：failures=3 → TerminalReason="context_exhausted"
	// 此时返回 err=nil（caller 通过 TerminalReason 判定 terminate，不需要 err）
	r3, err := Autocompact(context.Background(), run, deps)
	require.NoError(t, err, "3-fail breaker returns nil err + TerminalReason signal")
	assert.Equal(t, "context_exhausted", r3.TerminalReason)
	st, _ = store.GetCompactStateV2(context.Background(), run.ID)
	assert.Equal(t, 3, st.ConsecutiveAutocompactFailures)
}

// 5. ToolCallPairAlignment — recent 5 切到 assistant.tool_calls 中间 → cut 回退到完整 pair
func TestAutocompact_ToolCallPairAlignment(t *testing.T) {
	// 构造 messages：
	//   0: system
	//   1: user
	//   2: assistant(tool_calls=[id=tc1, id=tc2])
	//   3: tool(tool_call_id=tc1)
	//   4: tool(tool_call_id=tc2)
	//   5: assistant("after tools")
	//   6: user
	//   7: assistant(tool_calls=[id=tc3])
	//   8: tool(tool_call_id=tc3)
	//   9: assistant("done")
	//
	// len=10，AutocompactPreserveRecentMessages=5，初始 cut = 10-5 = 5
	// messages[5] = assistant("after tools") — 不是 tool；
	// messages[4] = tool(tc2)，messages[3] = tool(tc1) — 这些已在 toCompact 区
	// messages[2] = assistant.tool_calls，msgs[3..4] 是它的两个 tool reply 在 toCompact 内
	// → cut=5 已落在完整 pair 之后；recent = msgs[5..9] 内含完整的 assistant.tool_calls (idx 7) +
	//   tool(tc3) (idx 8) pair → 合法
	//
	// 现在故意把 recent 5 切到 pair 中间：构造 cut=8 表示 msgs[8..9]（tool + assistant）作 recent
	// 但 messages[8] 是 tool 没有 assistant.tool_calls 对应 → alignToolCallPairs 应回退
	//
	// 简化：直接测 alignToolCallPairs 函数：
	tcs := []map[string]any{{"id": "tc1"}, {"id": "tc2"}}
	msgs := []MessageV2{
		{Role: "system", Content: "sys"},                                               // 0
		{Role: "user", Content: "u1"},                                                  // 1
		{Role: "assistant", Content: "a1", ToolCalls: tcs},                             // 2
		{Role: "tool", ToolCallID: "tc1", Content: "tr1"},                              // 3
		{Role: "tool", ToolCallID: "tc2", Content: "tr2"},                              // 4
		{Role: "assistant", Content: "after"},                                          // 5
		{Role: "user", Content: "u2"},                                                  // 6
		{Role: "assistant", Content: "a2", ToolCalls: []map[string]any{{"id": "tc3"}}}, // 7
		{Role: "tool", ToolCallID: "tc3", Content: "tr3"},                              // 8
		{Role: "assistant", Content: "done"},                                           // 9
	}

	// cut=4 (msgs[4..9]) → msgs[4]=tool(tc2)，但 msgs[2] assistant.tool_calls 的 tc1 在 toCompact，tc2 也在 toCompact 但 msgs[4] tool reply 已经在 recent ←
	// alignToolCallPairs 检测：msgs[4].Role="tool" → 回退一步 → cut=3
	// cut=3 → msgs[3]=tool(tc1) → 再回退 → cut=2
	// cut=2 → msgs[2]=assistant.tool_calls，msgs[1] 不是 tool 但 msgs[1].Role="user" 不是 assistant.tool_calls 悬挂场景
	//        msgs[cut-1]=msgs[1] user → 不触发情景 B → cut=2 停
	aligned := alignToolCallPairs(msgs, 4)
	assert.LessOrEqual(t, aligned, 4, "cut should not increase")
	// 验证 aligned 处不是 tool（不悬挂 tool reply）
	if aligned < len(msgs) {
		assert.NotEqual(t, "tool", msgs[aligned].Role, "msgs[aligned] should not be a dangling tool reply")
	}
	// 验证 aligned-1 处不是有悬挂 ToolCalls 的 assistant
	if aligned > 0 && aligned <= len(msgs) {
		prev := msgs[aligned-1]
		if prev.Role == "assistant" && len(prev.ToolCalls) > 0 {
			// 检查 prev.ToolCalls 的每个 id 在 msgs[aligned:] 都有对应 tool reply
			needIDs := make(map[string]bool, len(prev.ToolCalls))
			for _, tc := range prev.ToolCalls {
				if id, ok := tc["id"].(string); ok {
					needIDs[id] = true
				}
			}
			found := 0
			for j := aligned; j < len(msgs) && msgs[j].Role == "tool"; j++ {
				if needIDs[msgs[j].ToolCallID] {
					found++
				}
			}
			assert.Equal(t, len(needIDs), found, "every tool_call id must have a matching tool reply in recent")
		}
	}

	// 额外测试 cut=8（tool 中间）→ 应回退
	aligned8 := alignToolCallPairs(msgs, 8)
	assert.NotEqual(t, "tool", msgs[aligned8].Role, "cut=8 should not point to dangling tool reply")
}

// 6. ArtifactRefNotExpanded — messages 含 <persisted-output ref="xxx"/> → serializeForSummary 保留 ref
func TestAutocompact_ArtifactRefNotExpanded(t *testing.T) {
	msgs := []MessageV2{
		{Role: "user", Content: "请把 80KB PDF 解析了"},
		{Role: "assistant", Content: "已解析", ToolCalls: []map[string]any{{"id": "tc1", "function": map[string]any{"name": "file_read"}}}},
		{Role: "tool", ToolCallID: "tc1", Content: `<persisted-output ref="art-uuid-1234" size_bytes="83456" mime_type="application/pdf" preview="abcdef..."/>`},
	}

	serialized := serializeForSummary(msgs)
	assert.Contains(t, serialized, `<persisted-output ref="art-uuid-1234"`, "ref literal preserved")
	// 关键：serializeForSummary 不应尝试读盘 / fetch artifact —— 它只是个字符串拼接函数，
	// 没有任何 IO 调用 path。本测试通过"无 panic + ref 字面值完整保留"间接验证。
	// 注：preview 字符串属于 ref XML 的 attribute 值的一部分，保留下来不算"展开"——
	// "不展开"指的是不去 DB 找 art-uuid-1234 把完整 83KB 字节读回来塞 serialized 里。
	assert.Contains(t, serialized, "size_bytes=\"83456\"", "ref attributes preserved as-is")
	assert.True(t, strings.Contains(serialized, "请把 80KB PDF 解析了"), "user message preserved")
}

// 7. TooShortNoOp — messages 只有 < 7 条 → Triggered=false 不调 LLM
func TestAutocompact_TooShortNoOp(t *testing.T) {
	run := makeRunWithNMessages(t, 4) // < 7 = AutocompactPreserveRecentMessages + 2
	store := newFakeStore()
	var callCount int32
	deps := AutocompactDeps{
		Chat: func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
			atomic.AddInt32(&callCount, 1)
			return &aiservice.ChatResponse{Content: validSummary}, nil
		},
		CompactV2Store: store,
		Metrics:        NoopMetrics{},
	}

	result, err := Autocompact(context.Background(), run, deps)
	require.NoError(t, err)
	assert.False(t, result.Triggered)
	assert.Empty(t, result.TerminalReason)
	assert.Equal(t, int32(0), atomic.LoadInt32(&callCount), "LLM not called when messages too short")

	// state 未写入
	st, _ := store.GetCompactStateV2(context.Background(), run.ID)
	assert.Nil(t, st)
}

// 8. HardLimit + failures<3 still calls — runner 进入 hard-limit 分支但前置失败<3
// 应该 fall through 走 autocompact path（不直接 terminate）。
//
// 这个 case 直接测 Autocompact 函数：传入 state.ConsecutiveAutocompactFailures=2 + 合法 LLM 响应，
// 应当：成功 → state.ConsecutiveAutocompactFailures 重置为 0。
func TestAutocompact_HardLimitWithoutPriorFailures_StillCalls(t *testing.T) {
	run := makeRunWithNMessages(t, 10)
	store := newFakeStore()
	// 预置 state：failures=2 < 3
	require.NoError(t, store.UpdateCompactStateV2(context.Background(), run.ID, &CompactStateV2{
		ConsecutiveAutocompactFailures: 2,
		CurrentPhase:                   "active",
	}))

	deps := AutocompactDeps{
		Chat:           successChatFn,
		CompactV2Store: store,
		Metrics:        NoopMetrics{},
	}

	result, err := Autocompact(context.Background(), run, deps)
	require.NoError(t, err)
	assert.True(t, result.Triggered, "should still try autocompact when failures<3")
	assert.Empty(t, result.TerminalReason)

	st, _ := store.GetCompactStateV2(context.Background(), run.ID)
	require.NotNil(t, st)
	assert.Equal(t, 0, st.ConsecutiveAutocompactFailures, "success resets ConsecutiveFailures to 0")
	assert.Equal(t, "L3_summarized", st.CurrentPhase)
}

// ── 额外保护：basic prompt template integrity ────────────────────────────────

// TestSerializedSize_MatchesSerializedString — serializedSize 应当与 serializeForSummary 长度一致
// （sanity-check helper，确保 caller 用任一函数得到一致结果）
func TestSerializedSize_MatchesSerializedString(t *testing.T) {
	msgs := []MessageV2{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello back"},
	}
	got := serializedSize(msgs)
	expected := len(serializeForSummary(msgs))
	assert.Equal(t, expected, got, "serializedSize must match serializeForSummary length")
	assert.Greater(t, got, 0, "non-empty messages → non-zero size")
}

// TestAutocompactPromptTemplate_HasRequiredSections — 模板必须含 12 段标题（防误改）
func TestAutocompactPromptTemplate_HasRequiredSections(t *testing.T) {
	tpl := AutocompactPromptTemplate
	required := []string{
		"## 1. Active Task",
		"## 2. Goal",
		"## 3. Constraints",
		"## 4. Completed Actions",
		"## 5. Active State",
		"## 6. In Progress",
		"## 7. Blocked",
		"## 8. Key Decisions",
		"## 9. Resolved Questions",
		"## 10. Pending User Asks",
		"## 11. Relevant Files / Artifacts",
		"## 12. Critical Context",
	}
	for _, sec := range required {
		assert.True(t, strings.Contains(tpl, sec), "AutocompactPromptTemplate missing required section: %s", sec)
	}
	// 必须含 XML 开闭标签字面值
	assert.True(t, strings.Contains(tpl, AutocompactOpenTag), "template must include open tag literal")
	assert.True(t, strings.Contains(tpl, AutocompactCloseTag), "template must include close tag literal")
}
