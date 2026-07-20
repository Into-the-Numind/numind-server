package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/aiservice"
)

type pipelineWorkflowCall struct {
	Tool string
	Args string
}

type pipelineModelStep struct {
	Tool   string
	Args   string
	Result string
}

type pipelineWorkflowRecorder struct {
	mu       sync.Mutex
	expected []pipelineModelStep
	next     int
	calls    []pipelineWorkflowCall
	err      error
}

func (r *pipelineWorkflowRecorder) consume(tool, args string) ToolResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, pipelineWorkflowCall{Tool: tool, Args: args})
	if r.next >= len(r.expected) {
		r.err = fmt.Errorf("unexpected extra tool call %s(%s)", tool, args)
		return ToolResult(`{"ok":false,"error":"unexpected_call"}`)
	}
	want := r.expected[r.next]
	if want.Tool != tool || want.Args != args {
		r.err = fmt.Errorf("step %d: got %s(%s), want %s(%s)", r.next, tool, args, want.Tool, want.Args)
	}
	r.next++
	if want.Result == "" {
		return ToolResult(`{"ok":true}`)
	}
	return ToolResult(want.Result)
}

func (r *pipelineWorkflowRecorder) snapshot() ([]pipelineWorkflowCall, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]pipelineWorkflowCall(nil), r.calls...), r.next, r.err
}

type pipelineWorkflowTool struct {
	BaseTool
	name     string
	recorder *pipelineWorkflowRecorder
}

func (t *pipelineWorkflowTool) Name() string           { return t.name }
func (t *pipelineWorkflowTool) Description() string    { return "scripted pipeline contract tool" }
func (t *pipelineWorkflowTool) UserFacingName() string { return t.name }
func (t *pipelineWorkflowTool) NarrationVerb() string  { return "执行" }
func (t *pipelineWorkflowTool) Execute(_ context.Context, input ToolInput) (ToolResult, error) {
	return t.recorder.consume(t.name, string(input)), nil
}

func scriptedPipelineChat(steps []pipelineModelStep, final string, callCount *int) func(context.Context, string, aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
	return func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		index := *callCount
		*callCount++
		if index < len(steps) {
			step := steps[index]
			return &aiservice.ChatResponse{
				ToolCalls: []aiservice.ToolCall{{
					ID: fmt.Sprintf("pipeline-call-%d", index), Type: "function",
					Function: aiservice.ToolCallFunction{Name: step.Tool, Arguments: step.Args},
				}},
				FinishReason: "tool_calls", Model: "test-model", Provider: "test",
			}, nil
		}
		return &aiservice.ChatResponse{
			Content: final, FinishReason: "stop", Model: "test-model", Provider: "test",
		}, nil
	}
}

type pipelineWorkflowScenario struct {
	name         string
	agentName    string
	agentID      uint64
	input        string
	steps        []pipelineModelStep
	final        string
	wantMetadata map[string]string
	check        func(*testing.T, []pipelineWorkflowCall)
}

func runScriptedPipelineWorkflow(t *testing.T, scenario pipelineWorkflowScenario) ([]pipelineWorkflowCall, map[string]string) {
	t.Helper()
	recorder := &pipelineWorkflowRecorder{expected: scenario.steps}
	toolNames := []string{"xhs_note_list", "file_read", "lark_skill_read", "lark_inspect", "lark_execute", "ask_user_question"}
	tools := make([]FullTool, 0, len(toolNames))
	for _, name := range toolNames {
		tools = append(tools, &pipelineWorkflowTool{name: name, recorder: recorder})
	}
	callCount := 0
	withMockChatFn(t, scriptedPipelineChat(scenario.steps, scenario.final, &callCount))
	events := capturePipelineLangfuseEvents(t)
	skillStore := newMemorySkillStore(1, scenario.agentID, "")
	skillStore.fixed.Name = scenario.agentName
	runner := NewAgentRunner(newMockStore(), newStaticRegistry(tools...), WithSkillStore(skillStore))

	result, err := runner.Run(context.Background(), RunRequest{
		UserID: 1, AgentDefinitionID: scenario.agentID,
		SessionID: fmt.Sprintf("pipeline-%d", scenario.agentID), Input: scenario.input,
	})
	require.NoError(t, err)
	require.Equal(t, TerminalCompleted, result.TerminalReason)
	require.Equal(t, scenario.final, result.FinalOutput)
	require.Equal(t, len(scenario.steps)+1, callCount)
	calls, consumed, recordErr := recorder.snapshot()
	require.NoError(t, recordErr)
	require.Equal(t, len(scenario.steps), consumed)
	require.Len(t, calls, len(scenario.steps))
	metadata := findPipelineTraceMetadata(t, *events)
	for key, value := range scenario.wantMetadata {
		assert.Equal(t, value, metadata[key], "safe metric %s", key)
	}
	return calls, metadata
}

func TestThreeAgentPipelineWorkflow_Agent1Matrix(t *testing.T) {
	scenarios := []pipelineWorkflowScenario{
		{
			name: "over one hundred notes paginate before one atomic create", agentName: pipelineAgent1Name, agentID: 7101,
			input: "分析所有未分析笔记",
			steps: []pipelineModelStep{
				step("lark_skill_read", `{"skill":"lark-base"}`, `{"ok":true}`),
				step("lark_execute", `{"argv":["base","record-list","--page-size","100"]}`, `{"records":100,"has_more":false}`),
				step("xhs_note_list", `{"projection":"index","limit":100}`, `{"items":100,"has_more":true,"cursor":"page-2"}`),
				step("xhs_note_list", `{"projection":"index","limit":100,"cursor":"page-2"}`, `{"items":1,"has_more":false}`),
				step("xhs_note_list", `{"projection":"full","limit":100,"xhs_note_ids":["note-101"]}`, `{"items":[{"xhs_note_id":"note-101"}],"has_more":false}`),
				step("lark_execute", `{"argv":["base","record-create"],"business_key":"note-101","analysis_status":"completed"}`, `{"record_id":"rec-101"}`),
			},
			final: marker1(1, 100, 0, 0), wantMetadata: metrics1(1, 100, 0, 0),
			check: func(t *testing.T, calls []pipelineWorkflowCall) {
				assertOrder(t, calls, `"projection":"index"`, `"projection":"full"`, "record-create")
				assert.NotContains(t, workflowCallsString(calls), "record-batch-update")
			},
		},
		{
			name: "second run skips every completed key", agentName: pipelineAgent1Name, agentID: 7102, input: "再次执行",
			steps: []pipelineModelStep{
				step("lark_execute", `{"argv":["base","record-list"]}`, `{"completed_keys":["n1","n2"]}`),
				step("xhs_note_list", `{"projection":"index","limit":100}`, `{"items":["n1","n2"],"has_more":false}`),
			},
			final: marker1(0, 2, 0, 0), wantMetadata: metrics1(0, 2, 0, 0),
			check: noCallContaining(`"projection":"full"`, "record-create", "record-upsert"),
		},
		{
			name: "new capture after prior run is the only write", agentName: pipelineAgent1Name, agentID: 7103, input: "分析新增内容",
			steps: []pipelineModelStep{
				step("lark_execute", `{"argv":["base","record-list"]}`, `{"completed_keys":["n1"]}`),
				step("xhs_note_list", `{"projection":"index","limit":100}`, `{"items":["n1","n2"],"has_more":false}`),
				step("xhs_note_list", `{"projection":"full","xhs_note_ids":["n2"],"limit":100}`, `{"items":[{"xhs_note_id":"n2"}]}`),
				step("lark_execute", `{"argv":["base","record-create"],"business_key":"n2"}`, `{"record_id":"r2"}`),
			},
			final: marker1(1, 1, 0, 0), wantMetadata: metrics1(1, 1, 0, 0),
			check: exactlyOneCallContaining("record-create"),
		},
		{
			name: "incomplete existing row uses one-record upsert", agentName: pipelineAgent1Name, agentID: 7104, input: "继续打标",
			steps: []pipelineModelStep{
				step("lark_execute", `{"argv":["base","record-list"]}`, `{"incomplete":{"n3":"rec3"}}`),
				step("xhs_note_list", `{"projection":"index","limit":100}`, `{"items":["n3"],"has_more":false}`),
				step("xhs_note_list", `{"projection":"full","xhs_note_ids":["n3"],"limit":100}`, `{"items":[{"xhs_note_id":"n3"}]}`),
				step("lark_execute", `{"argv":["base","record-upsert","rec3"],"business_key":"n3"}`, `{"record_id":"rec3"}`),
			},
			final: marker1(1, 0, 0, 0), wantMetadata: metrics1(1, 0, 0, 0),
			check: func(t *testing.T, calls []pipelineWorkflowCall) {
				exactlyOneCallContaining("record-upsert")(t, calls)
				noCallContaining("record-batch-update")(t, calls)
			},
		},
		{
			name: "explicit historical scope reanalyses only named keys", agentName: pipelineAgent1Name, agentID: 7105,
			input: "只重新分析 n4 和 n5，并覆盖这两条",
			steps: []pipelineModelStep{
				step("lark_execute", `{"argv":["base","record-list"],"business_keys":["n4","n5"]}`, `{"records":["n4","n5"]}`),
				step("xhs_note_list", `{"projection":"full","xhs_note_ids":["n4","n5"],"limit":100}`, `{"items":["n4","n5"]}`),
				step("lark_execute", `{"argv":["base","record-upsert","rec4"],"business_key":"n4"}`, `{"record_id":"rec4"}`),
				step("lark_execute", `{"argv":["base","record-upsert","rec5"],"business_key":"n5"}`, `{"record_id":"rec5"}`),
			},
			final: marker1(2, 0, 0, 0), wantMetadata: metrics1(2, 0, 0, 0),
			check: func(t *testing.T, calls []pipelineWorkflowCall) {
				assert.Equal(t, 2, countCallsContaining(calls, "record-upsert"))
				noCallContaining("record-batch-update")(t, calls)
			},
		},
		{
			name: "duplicate business key stops for disambiguation", agentName: pipelineAgent1Name, agentID: 7106, input: "打标",
			steps: []pipelineModelStep{
				step("lark_execute", `{"argv":["base","record-list"]}`, `{"duplicates":{"n6":["r61","r62"]}}`),
				step("ask_user_question", `{"question":"发现重复的小红书笔记ID n6，请先选择保留记录"}`, `{"status":"waiting"}`),
			},
			final: marker1(0, 0, 1, 1), wantMetadata: metrics1(0, 0, 1, 1),
			check: noCallContaining("record-create", "record-upsert", `"projection":"full"`),
		},
		{
			name: "partial success counts one failure without batch retry", agentName: pipelineAgent1Name, agentID: 7107, input: "打标两条",
			steps: []pipelineModelStep{
				step("xhs_note_list", `{"projection":"full","xhs_note_ids":["n7","n8"],"limit":100}`, `{"items":["n7","n8"]}`),
				step("lark_execute", `{"argv":["base","record-create"],"business_key":"n7"}`, `{"record_id":"r7"}`),
				step("lark_execute", `{"argv":["base","record-create"],"business_key":"n8"}`, `{"ok":false,"error":"write_failed"}`),
			},
			final: marker1(1, 0, 1, 1), wantMetadata: metrics1(1, 0, 1, 1),
			check: func(t *testing.T, calls []pipelineWorkflowCall) {
				assert.Equal(t, 2, countCallsContaining(calls, "record-create"))
				noCallContaining("record-batch-update")(t, calls)
			},
		},
		{
			name: "unknown write is read-after-write reconciled and never replayed", agentName: pipelineAgent1Name, agentID: 7108, input: "打标 n9",
			steps: []pipelineModelStep{
				step("xhs_note_list", `{"projection":"full","xhs_note_ids":["n9"],"limit":100}`, `{"items":["n9"]}`),
				step("lark_execute", `{"argv":["base","record-create"],"business_key":"n9"}`, `{"status":"unknown"}`),
				step("lark_execute", `{"argv":["base","record-list"],"business_key":"n9","purpose":"reconcile"}`, `{"records":["n9"]}`),
			},
			final: marker1(1, 0, 0, 0), wantMetadata: metrics1(1, 0, 0, 0),
			check: func(t *testing.T, calls []pipelineWorkflowCall) {
				exactlyOneCallContaining("record-create")(t, calls)
				assertOrder(t, calls, "record-create", "reconcile")
			},
		},
	}
	runPipelineScenarioMatrix(t, scenarios)
}

func TestThreeAgentPipelineWorkflow_Agent2Matrix(t *testing.T) {
	scenarios := []pipelineWorkflowScenario{
		{
			name: "upload is read to final byte before create", agentName: pipelineAgent2Name, agentID: 7201, input: "从上传文件生成客户画像",
			steps: []pipelineModelStep{
				step("file_read", `{"file_url":"attachment://customer.pdf","offset":0}`, `{"has_more":true,"next_offset":65536,"read_token":"stable"}`),
				step("file_read", `{"file_url":"attachment://customer.pdf","offset":65536,"read_token":"stable"}`, `{"has_more":false}`),
				step("lark_skill_read", `{"skill":"lark-doc"}`, `{"ok":true}`),
				step("lark_execute", `{"argv":["docs","search"],"exact_name":"客户A-核心信息与人群画像"}`, `{"matches":[]}`),
				step("lark_execute", `{"argv":["docs","create"],"managed_contract":"profile/v1","modules":7}`, `{"doc":"new"}`),
			},
			final: marker2(1, "create"), wantMetadata: metrics23(1, "create"),
			check: func(t *testing.T, calls []pipelineWorkflowCall) {
				assertOrder(t, calls, `"offset":65536`, "docs\",\"create")
			},
		},
		{
			name: "feishu source all pages then one exact managed match updates", agentName: pipelineAgent2Name, agentID: 7202, input: "读取飞书资料并更新画像",
			steps: []pipelineModelStep{
				step("lark_skill_read", `{"skill":"lark-doc"}`, `{"ok":true}`),
				step("lark_inspect", `{"url":"https://feishu.example/source"}`, `{"connected":true,"type":"doc"}`),
				step("lark_execute", `{"argv":["docs","fetch","source"],"page_token":""}`, `{"has_more":true,"page_token":"p2"}`),
				step("lark_execute", `{"argv":["docs","fetch","source"],"page_token":"p2"}`, `{"has_more":false}`),
				step("lark_execute", `{"argv":["docs","search"],"exact_name":"客户B-核心信息与人群画像"}`, `{"matches":[{"managed":true,"doc":"profile-b"}]}`),
				step("lark_execute", `{"argv":["docs","overwrite","profile-b"],"managed_contract":"profile/v1","modules":7}`, `{"ok":true}`),
			},
			final: marker2(1, "update"), wantMetadata: metrics23(1, "update"),
			check: func(t *testing.T, calls []pipelineWorkflowCall) {
				assertOrder(t, calls, `"page_token":"p2"`, "docs\",\"overwrite")
				exactlyOneCallContaining(`"modules":7`)(t, calls)
			},
		},
		{
			name: "mixed sources both complete before write", agentName: pipelineAgent2Name, agentID: 7203, input: "结合附件和飞书生成画像",
			steps: []pipelineModelStep{
				step("file_read", `{"file_url":"attachment://brief.docx","offset":0}`, `{"has_more":false}`),
				step("lark_execute", `{"argv":["docs","fetch","research"]}`, `{"has_more":false}`),
				step("lark_execute", `{"argv":["docs","search"],"exact_name":"客户C-核心信息与人群画像"}`, `{"matches":[]}`),
				step("lark_execute", `{"argv":["docs","create"],"managed_contract":"profile/v1","modules":7}`, `{"ok":true}`),
			},
			final: marker2(2, "create"), wantMetadata: metrics23(2, "create"),
			check: func(t *testing.T, calls []pipelineWorkflowCall) {
				assertOrder(t, calls, "file_read", "docs\",\"fetch", "docs\",\"create")
			},
		},
		{
			name: "more than one exact target asks without write", agentName: pipelineAgent2Name, agentID: 7204, input: "生成画像",
			steps: []pipelineModelStep{
				step("file_read", `{"file_url":"attachment://brief.txt","offset":0}`, `{"has_more":false}`),
				step("lark_execute", `{"argv":["docs","search"],"exact_name":"客户D-核心信息与人群画像"}`, `{"matches":["d1","d2"]}`),
				step("ask_user_question", `{"question":"找到 2 个同名文档，请选择目标"}`, `{"status":"waiting"}`),
			},
			final: marker2(1, "unavailable"), wantMetadata: metrics23(1, "unavailable"),
			check: noCallContaining("docs\",\"create", "docs\",\"overwrite"),
		},
		{
			name: "official authorization recovery resumes exact search", agentName: pipelineAgent2Name, agentID: 7205, input: "从飞书生成画像",
			steps: []pipelineModelStep{
				step("lark_inspect", `{"url":"https://feishu.example/source"}`, `{"connected":false,"auth_required":true}`),
				step("ask_user_question", `{"question":"请完成飞书官方授权，完成后回复继续"}`, `{"authorized":true}`),
				step("lark_inspect", `{"url":"https://feishu.example/source"}`, `{"connected":true}`),
				step("lark_execute", `{"argv":["docs","fetch","source"]}`, `{"has_more":false}`),
				step("lark_execute", `{"argv":["docs","search"],"exact_name":"客户E-核心信息与人群画像"}`, `{"matches":[]}`),
				step("lark_execute", `{"argv":["docs","create"],"managed_contract":"profile/v1","modules":7}`, `{"ok":true}`),
			},
			final: marker2(1, "create"), wantMetadata: metrics23(1, "create"),
			check: func(t *testing.T, calls []pipelineWorkflowCall) {
				require.GreaterOrEqual(t, len(calls), 3)
				assert.Equal(t, "lark_inspect", calls[0].Tool)
				assert.Equal(t, "ask_user_question", calls[1].Tool)
				assert.Equal(t, "lark_inspect", calls[2].Tool)
				assertOrder(t, calls, "ask_user_question", "docs\",\"create")
			},
		},
		{
			name: "unmanaged exact collision is not overwritten", agentName: pipelineAgent2Name, agentID: 7206, input: "更新画像",
			steps: []pipelineModelStep{
				step("file_read", `{"file_url":"attachment://brief.txt","offset":0}`, `{"has_more":false}`),
				step("lark_execute", `{"argv":["docs","search"],"exact_name":"客户F-核心信息与人群画像"}`, `{"matches":[{"managed":false,"doc":"f"}]}`),
				step("ask_user_question", `{"question":"同名文档不含受管标记，请选择接管或改名"}`, `{"status":"waiting"}`),
			},
			final: marker2(1, "unavailable"), wantMetadata: metrics23(1, "unavailable"),
			check: noCallContaining("docs\",\"overwrite", "docs\",\"create"),
		},
		{
			name: "damaged managed marker blocks overwrite", agentName: pipelineAgent2Name, agentID: 7207, input: "更新画像",
			steps: []pipelineModelStep{
				step("file_read", `{"file_url":"attachment://brief.txt","offset":0}`, `{"has_more":false}`),
				step("lark_execute", `{"argv":["docs","search"],"exact_name":"客户G-核心信息与人群画像"}`, `{"matches":[{"doc":"g","marker":"damaged"}]}`),
				step("ask_user_question", `{"question":"目标文档受管标记损坏，请确认处理方式"}`, `{"status":"waiting"}`),
			},
			final: marker2(1, "unavailable"), wantMetadata: metrics23(1, "unavailable"),
			check: noCallContaining("docs\",\"overwrite", "docs\",\"create"),
		},
		{
			name: "incomplete source cannot produce complete profile", agentName: pipelineAgent2Name, agentID: 7208, input: "生成画像",
			steps: []pipelineModelStep{
				step("file_read", `{"file_url":"attachment://broken.pdf","offset":0}`, `{"has_more":true,"next_offset":65536}`),
				step("file_read", `{"file_url":"attachment://broken.pdf","offset":65536,"read_token":"required"}`, `{"ok":false,"error":"read_token_mismatch"}`),
				step("ask_user_question", `{"question":"来源未能完整读取，请重新上传"}`, `{"status":"waiting"}`),
			},
			final: marker2(0, "unavailable"), wantMetadata: metrics23(0, "unavailable"),
			check: noCallContaining("docs\",\"overwrite", "docs\",\"create"),
		},
	}
	runPipelineScenarioMatrix(t, scenarios)
}

func TestThreeAgentPipelineWorkflow_Agent3Matrix(t *testing.T) {
	scenarios := []pipelineWorkflowScenario{
		{
			name: "only agent one and two outputs feed a nine-field round append", agentName: pipelineAgent3Name, agentID: 7301, input: "规划新一轮选题",
			steps: []pipelineModelStep{
				step("lark_skill_read", `{"skill":"lark-base"}`, `{"ok":true}`),
				step("lark_execute", `{"argv":["base","record-list","agent1-output"]}`, `{"records":[{"达标":true},{"达标":false}]}`),
				step("lark_skill_read", `{"skill":"lark-doc"}`, `{"ok":true}`),
				step("lark_execute", `{"argv":["docs","fetch","agent2-profile"]}`, `{"has_more":false}`),
				step("lark_execute", `{"argv":["docs","append","topics-doc"],"round_id":"R20260720T083000Z-a1b2c3","fields":9,"selection_reason":true,"benchmark_count":4,"blue_v_count":1,"rule":"达标=true"}`, `{"ok":true}`),
			},
			final: marker3(2, "append"), wantMetadata: metrics23(2, "append"),
			check: func(t *testing.T, calls []pipelineWorkflowCall) {
				joined := workflowCallsString(calls)
				assert.NotContains(t, joined, "xhs_note_list")
				assert.Contains(t, joined, `"fields":9`)
				assert.Contains(t, joined, `"selection_reason":true`)
				assert.Contains(t, joined, `"blue_v_count":1`)
				assert.Contains(t, joined, `"rule":"达标=true"`)
			},
		},
		{
			name: "zero target creates customer planning doc", agentName: pipelineAgent3Name, agentID: 7302, input: "为客户H做选题",
			steps: []pipelineModelStep{
				step("file_read", `{"file_url":"attachment://agent1.csv","offset":0}`, `{"has_more":false}`),
				step("file_read", `{"file_url":"attachment://agent2.docx","offset":0}`, `{"has_more":false}`),
				step("lark_execute", `{"argv":["docs","search"],"exact_name":"客户H-选题规划"}`, `{"matches":[]}`),
				step("lark_execute", `{"argv":["docs","create"],"managed_contract":"topics/v1","round_id":"R-new","fields":9}`, `{"ok":true}`),
			},
			final: marker3(2, "create"), wantMetadata: metrics23(2, "create"),
			check: exactlyOneCallContaining("docs\",\"create"),
		},
		{
			name: "specified existing round is replaced exactly", agentName: pipelineAgent3Name, agentID: 7303, input: "修改 R-fixed 这一轮",
			steps: []pipelineModelStep{
				step("lark_execute", `{"argv":["base","record-list","agent1-output"]}`, `{"records":4}`),
				step("lark_execute", `{"argv":["docs","fetch","agent2-profile"]}`, `{"has_more":false}`),
				step("lark_execute", `{"argv":["docs","fetch","topics-doc"],"purpose":"locate-round-markers"}`, `{"rounds":["R-old","R-fixed"]}`),
				step("lark_execute", `{"argv":["docs","str_replace","topics-doc"],"round_id":"R-fixed","fields":9}`, `{"ok":true}`),
			},
			final: marker3(2, "replace-round"), wantMetadata: metrics23(2, "replace-round"),
			check: func(t *testing.T, calls []pipelineWorkflowCall) {
				exactlyOneCallContaining("str_replace")(t, calls)
				assert.Contains(t, calls[len(calls)-1].Args, `"round_id":"R-fixed"`)
				noCallContaining("docs\",\"append", "docs\",\"create")(t, calls)
			},
		},
		{
			name: "unknown append is reconciled through round marker", agentName: pipelineAgent3Name, agentID: 7304, input: "追加一轮",
			steps: []pipelineModelStep{
				step("lark_execute", `{"argv":["base","record-list","agent1-output"]}`, `{"records":4}`),
				step("lark_execute", `{"argv":["docs","fetch","agent2-profile"]}`, `{"has_more":false}`),
				step("lark_execute", `{"argv":["docs","append","topics-doc"],"round_id":"R-unknown","fields":9}`, `{"status":"unknown"}`),
				step("lark_execute", `{"argv":["docs","fetch","topics-doc"],"purpose":"reconcile","round_id":"R-unknown"}`, `{"round_found":true}`),
			},
			final: marker3(2, "append"), wantMetadata: metrics23(2, "append"),
			check: func(t *testing.T, calls []pipelineWorkflowCall) {
				exactlyOneCallContaining("docs\",\"append")(t, calls)
				assertOrder(t, calls, "docs\",\"append", "reconcile")
			},
		},
		{
			name: "unmanaged exact collision is never taken over", agentName: pipelineAgent3Name, agentID: 7305, input: "规划选题",
			steps: []pipelineModelStep{
				step("lark_execute", `{"argv":["base","record-list","agent1-output"]}`, `{"records":4}`),
				step("lark_execute", `{"argv":["docs","fetch","agent2-profile"]}`, `{"has_more":false}`),
				step("lark_execute", `{"argv":["docs","search"],"exact_name":"客户I-选题规划"}`, `{"matches":[{"managed":false}]}`),
				step("ask_user_question", `{"question":"同名文档无受管标记，请选择接管或改名"}`, `{"status":"waiting"}`),
			},
			final: marker3(2, "unavailable"), wantMetadata: metrics23(2, "unavailable"),
			check: noCallContaining("docs\",\"append", "docs\",\"create", "str_replace"),
		},
		{
			name: "more than one exact target asks for disambiguation", agentName: pipelineAgent3Name, agentID: 7306, input: "规划选题",
			steps: []pipelineModelStep{
				step("file_read", `{"file_url":"attachment://agent1.csv","offset":0}`, `{"has_more":false}`),
				step("file_read", `{"file_url":"attachment://agent2.docx","offset":0}`, `{"has_more":false}`),
				step("lark_execute", `{"argv":["docs","search"],"exact_name":"客户J-选题规划"}`, `{"matches":["j1","j2"]}`),
				step("ask_user_question", `{"question":"找到 2 个同名选题文档，请选择目标"}`, `{"status":"waiting"}`),
			},
			final: marker3(2, "unavailable"), wantMetadata: metrics23(2, "unavailable"),
			check: noCallContaining("docs\",\"append", "docs\",\"create", "str_replace"),
		},
	}
	runPipelineScenarioMatrix(t, scenarios)
}

func runPipelineScenarioMatrix(t *testing.T, scenarios []pipelineWorkflowScenario) {
	t.Helper()
	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			calls, _ := runScriptedPipelineWorkflow(t, scenario)
			if scenario.check != nil {
				scenario.check(t, calls)
			}
		})
	}
}

func step(tool, args, result string) pipelineModelStep {
	return pipelineModelStep{Tool: tool, Args: args, Result: result}
}

func marker1(processed, skipped, remaining, failed int) string {
	return fmt.Sprintf("完成\n<!-- numind-pipeline-report/v1 agent=agent-1 {\"processed\":%d,\"skipped\":%d,\"remaining\":%d,\"failed\":%d} -->", processed, skipped, remaining, failed)
}

func marker2(sourceCount int, outputMode string) string {
	return fmt.Sprintf("完成\n<!-- numind-pipeline-report/v1 agent=agent-2 {\"source_count\":%d,\"output_mode\":%q} -->", sourceCount, outputMode)
}

func marker3(sourceCount int, outputMode string) string {
	return fmt.Sprintf("完成\n<!-- numind-pipeline-report/v1 agent=agent-3 {\"source_count\":%d,\"output_mode\":%q} -->", sourceCount, outputMode)
}

func metrics1(processed, skipped, remaining, failed int) map[string]string {
	return map[string]string{
		"status": "ok", "processed": fmt.Sprint(processed), "skipped": fmt.Sprint(skipped),
		"remaining": fmt.Sprint(remaining), "failed": fmt.Sprint(failed),
	}
}

func metrics23(sourceCount int, outputMode string) map[string]string {
	return map[string]string{"status": "ok", "source_count": fmt.Sprint(sourceCount), "output_mode": outputMode}
}

func workflowCallsString(calls []pipelineWorkflowCall) string {
	parts := make([]string, 0, len(calls))
	for _, call := range calls {
		parts = append(parts, call.Tool+":"+call.Args)
	}
	return strings.Join(parts, " > ")
}

func countCallsContaining(calls []pipelineWorkflowCall, needle string) int {
	count := 0
	for _, call := range calls {
		if strings.Contains(call.Tool+":"+call.Args, needle) {
			count++
		}
	}
	return count
}

func exactlyOneCallContaining(needle string) func(*testing.T, []pipelineWorkflowCall) {
	return func(t *testing.T, calls []pipelineWorkflowCall) {
		t.Helper()
		assert.Equal(t, 1, countCallsContaining(calls, needle), "calls containing %q", needle)
	}
}

func noCallContaining(needles ...string) func(*testing.T, []pipelineWorkflowCall) {
	return func(t *testing.T, calls []pipelineWorkflowCall) {
		t.Helper()
		joined := workflowCallsString(calls)
		for _, needle := range needles {
			assert.NotContains(t, joined, needle)
		}
	}
}

func assertOrder(t *testing.T, calls []pipelineWorkflowCall, needles ...string) {
	t.Helper()
	joined := workflowCallsString(calls)
	previous := -1
	for _, needle := range needles {
		index := strings.Index(joined, needle)
		require.Greater(t, index, previous, "expected %q after prior checkpoint in %s", needle, joined)
		previous = index
	}
}
