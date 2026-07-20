package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/feishu"
	"numind-server/internal/pkg/aiservice"
)

const (
	pipelineBaseToken = "bascnABCDEFG123"
	pipelineTableID   = "tblABCDEFG123"
	pipelineProfile   = "doxcnProfileABC123"
	pipelineTopics    = "doxcnTopicsABC123"
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
	mu                 sync.Mutex
	expected           []pipelineModelStep
	next               int
	calls              []pipelineWorkflowCall
	err                error
	prompt             string
	systemPrompt       string
	promptObserved     bool
	expectedToolNames  []string
	toolSchemaObserved bool
}

func (r *pipelineWorkflowRecorder) consume(tool, args string) ToolResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, pipelineWorkflowCall{Tool: tool, Args: args})
	if r.err == nil {
		r.err = validatePipelineToolInput(tool, args)
	}
	if r.next >= len(r.expected) {
		if r.err == nil {
			r.err = fmt.Errorf("unexpected extra tool call %s(%s)", tool, args)
		}
		return ToolResult(`{"ok":false,"error":"unexpected_call"}`)
	}
	want := r.expected[r.next]
	if (want.Tool != tool || want.Args != args) && r.err == nil {
		r.err = fmt.Errorf("step %d: got %s(%s), want %s(%s)", r.next, tool, args, want.Tool, want.Args)
	}
	r.next++
	if want.Result == "" {
		return ToolResult(`{"ok":true}`)
	}
	return ToolResult(want.Result)
}

func (r *pipelineWorkflowRecorder) observeRequest(req aiservice.ChatRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, message := range req.Messages {
		if message.Role == aiservice.MessageRoleSystem {
			if r.systemPrompt == "" {
				r.systemPrompt = message.Content.Text
			}
			if pipelinePromptWasInjected(message.Content.Text, r.prompt) {
				r.promptObserved = true
			}
		}
	}
	names := make([]string, 0, len(req.Tools))
	for _, tool := range req.Tools {
		names = append(names, tool.Function.Name)
		if tool.Function.Parameters == nil && r.err == nil {
			r.err = fmt.Errorf("tool %s reached the model without its production JSON schema", tool.Function.Name)
		}
	}
	sort.Strings(names)
	if strings.Join(names, ",") == strings.Join(r.expectedToolNames, ",") {
		r.toolSchemaObserved = true
	} else if r.err == nil {
		r.err = fmt.Errorf("model tool set = %v, want strict allowlist %v", names, r.expectedToolNames)
	}
}

func (r *pipelineWorkflowRecorder) snapshot() ([]pipelineWorkflowCall, int, bool, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]pipelineWorkflowCall(nil), r.calls...), r.next, r.promptObserved, r.toolSchemaObserved, r.err
}

type pipelineWorkflowTool struct {
	BaseTool
	name     string
	recorder *pipelineWorkflowRecorder
}

func (t *pipelineWorkflowTool) Name() string { return t.name }
func (t *pipelineWorkflowTool) Description() string {
	return "scripted implementation of the production pipeline contract"
}
func (t *pipelineWorkflowTool) UserFacingName() string { return t.name }
func (t *pipelineWorkflowTool) NarrationVerb() string  { return "执行" }
func (t *pipelineWorkflowTool) InputSchema() json.RawMessage {
	switch t.name {
	case "xhs_note_list":
		return (&xhsNoteListTool{}).InputSchema()
	case "file_read":
		return (&fileReadTool{}).InputSchema()
	case "lark_skill_read":
		return (&larkSkillReadTool{}).InputSchema()
	case "lark_inspect":
		return (&larkInspectTool{}).InputSchema()
	case "lark_execute":
		return (&larkExecuteTool{}).InputSchema()
	case "ask_user_question":
		return (&askUserQuestionTool{}).InputSchema()
	default:
		return nil
	}
}
func (t *pipelineWorkflowTool) Execute(_ context.Context, input ToolInput) (ToolResult, error) {
	return t.recorder.consume(t.name, string(input)), nil
}

func validatePipelineToolInput(tool, raw string) error {
	input := ToolInput(raw)
	switch tool {
	case "lark_execute":
		decoded, err := decodeLarkExecuteInput(input)
		if err != nil {
			return fmt.Errorf("invalid lark_execute input: %w", err)
		}
		if _, err = feishu.NewCommandCatalog().Normalize(decoded.Argv, decoded.StdinJSON); err != nil {
			return fmt.Errorf("command outside production catalog: %w", err)
		}
	case "lark_inspect":
		request, err := decodeLarkInspectInput(input)
		if err != nil {
			return fmt.Errorf("invalid lark_inspect input: %w", err)
		}
		if request.Mode == feishu.InspectionModeCommand {
			if _, err = feishu.NewCommandCatalog().Normalize(request.Argv, nil); err != nil {
				return fmt.Errorf("inspection command outside production catalog: %w", err)
			}
		}
	case "ask_user_question":
		_, err := (&askUserQuestionTool{}).Execute(context.Background(), input)
		var yielded *yieldError
		if !errors.As(err, &yielded) {
			return fmt.Errorf("ask_user_question did not satisfy the real yield contract")
		}
	case "file_read":
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.DisallowUnknownFields()
		var decoded fileReadInput
		if err := decoder.Decode(&decoded); err != nil || decoded.FileURL == "" {
			return fmt.Errorf("invalid file_read input")
		}
		if _, ok := extractCOSObjectKey(decoded.FileURL); !ok {
			return fmt.Errorf("file_read URL is not a managed COS URL")
		}
		owner, err := extractUserIDFromURL(decoded.FileURL)
		if err != nil || owner != 1 {
			return fmt.Errorf("file_read URL is not owned by current user")
		}
		if decoded.Offset != nil && *decoded.Offset < 0 {
			return fmt.Errorf("file_read offset is negative")
		}
	case "xhs_note_list":
		if _, err := normalizeXhsNoteListInput(input); err != nil {
			return fmt.Errorf("invalid xhs_note_list input: %w", err)
		}
	case "lark_skill_read":
		var decoded struct {
			Skill     string `json:"skill"`
			Reference string `json:"reference,omitempty"`
		}
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&decoded); err != nil || !map[string]bool{"lark-doc": true, "lark-base": true, "lark-drive": true, "lark-shared": true, "lark-wiki": true}[decoded.Skill] {
			return fmt.Errorf("invalid lark_skill_read input")
		}
	default:
		return fmt.Errorf("unexpected pipeline tool %q", tool)
	}
	return nil
}

func scriptedPipelineChat(recorder *pipelineWorkflowRecorder, steps []pipelineModelStep, final string, callCount *int) func(context.Context, string, aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
	return func(_ context.Context, _ string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		recorder.observeRequest(req)
		index := *callCount
		*callCount++
		if index < len(steps) {
			current := steps[index]
			return &aiservice.ChatResponse{
				ToolCalls: []aiservice.ToolCall{{
					ID: fmt.Sprintf("pipeline-call-%d", index), Type: "function",
					Function: aiservice.ToolCallFunction{Name: current.Tool, Arguments: current.Args},
				}},
				FinishReason: "tool_calls", Model: "test-model", Provider: "test",
			}, nil
		}
		return &aiservice.ChatResponse{Content: final, FinishReason: "stop", Model: "test-model", Provider: "test"}, nil
	}
}

type pipelineWorkflowScenario struct {
	name         string
	agent        int
	agentID      uint64
	input        string
	steps        []pipelineModelStep
	final        string
	wantMetadata map[string]string
	check        func(*testing.T, []pipelineWorkflowCall)
}

func runScriptedPipelineWorkflow(t *testing.T, scenario pipelineWorkflowScenario) ([]pipelineWorkflowCall, map[string]string) {
	t.Helper()
	prompt := loadPipelineSystemPrompt(t, scenario.agent)
	toolNames := uniquePipelineToolNames(scenario.steps)
	recorder := &pipelineWorkflowRecorder{expected: scenario.steps, prompt: prompt, expectedToolNames: toolNames}
	allNames := []string{"xhs_note_list", "file_read", "lark_skill_read", "lark_inspect", "lark_execute", "ask_user_question"}
	tools := make([]FullTool, 0, len(allNames))
	for _, name := range allNames {
		tools = append(tools, &pipelineWorkflowTool{name: name, recorder: recorder})
	}
	callCount := 0
	withMockChatFn(t, scriptedPipelineChat(recorder, scenario.steps, scenario.final, &callCount))
	events := capturePipelineLangfuseEvents(t)
	skillStore := newMemorySkillStore(1, scenario.agentID, "")
	skillStore.fixed.Name = pipelineAgentName(scenario.agent)
	skillStore.fixed.SystemPrompt = prompt
	runner := NewAgentRunner(newMockStore(), newStaticRegistry(tools...), WithSkillStore(skillStore))

	result, err := runner.Run(context.Background(), RunRequest{
		UserID: 1, AgentDefinitionID: scenario.agentID,
		SessionID: fmt.Sprintf("pipeline-%d", scenario.agentID), Input: scenario.input,
		ToolNames: toolNames, EnforceToolAllowlist: true,
	})
	require.NoError(t, err)
	require.Equal(t, TerminalCompleted, result.TerminalReason)
	require.Equal(t, scenario.final, result.FinalOutput)
	require.Equal(t, len(scenario.steps)+1, callCount)
	calls, consumed, promptObserved, schemasObserved, recordErr := recorder.snapshot()
	require.NoError(t, recordErr)
	require.Equal(t, len(scenario.steps), consumed)
	require.True(t, promptObserved, "the checked-in Agent %d prompt must reach the model (source bytes=%d, model bytes=%d)", scenario.agent, len(prompt), len(recorder.systemPrompt))
	require.True(t, schemasObserved, "only explicitly allowed production-schema tools must reach the model")
	require.Len(t, calls, len(scenario.steps))
	metadata := findPipelineTraceMetadata(t, *events)
	for key, value := range scenario.wantMetadata {
		assert.Equal(t, value, metadata[key], "safe metric %s", key)
	}
	return calls, metadata
}

func loadPipelineSystemPrompt(t *testing.T, agentNumber int) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	require.True(t, ok)
	path := filepath.Join(filepath.Dir(current), "../../../../docs/agent-definitions/three-agent-feishu-pipeline", fmt.Sprintf("agent-%d-system-prompt.md", agentNumber))
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotEmpty(t, bytes.TrimSpace(content))
	return string(content)
}

func pipelinePromptWasInjected(systemPrompt, checkedInPrompt string) bool {
	return strings.Contains(systemPrompt, strings.TrimSpace(checkedInPrompt))
}

func pipelineAgentName(agentNumber int) string {
	switch agentNumber {
	case 1:
		return pipelineAgent1Name
	case 2:
		return pipelineAgent2Name
	case 3:
		return pipelineAgent3Name
	default:
		panic("unknown pipeline agent")
	}
}

func uniquePipelineToolNames(steps []pipelineModelStep) []string {
	seen := make(map[string]struct{}, len(steps))
	for _, step := range steps {
		seen[step.Tool] = struct{}{}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func TestThreeAgentPipelineWorkflow_Agent1Over100AndCheckpointResume(t *testing.T) {
	allIDs := noteIDs(1, 243)
	firstSteps := []pipelineModelStep{
		skillStep("lark-base"),
		larkStep(baseListArgv(), `{"records":[],"has_more":false}`),
	}
	firstSteps = append(firstSteps, indexSteps(allIDs)...)
	firstSteps = append(firstSteps, fullStep(allIDs[:40]))
	firstSteps = append(firstSteps, batchCreateSteps(allIDs[:40])...)
	first := pipelineWorkflowScenario{
		name: "first pass reaches a durable 40-row checkpoint", agent: 1, agentID: 7100, input: "分析全部未分析笔记",
		steps: firstSteps, final: marker1(40, 0, 203, 0), wantMetadata: metrics1(40, 0, 203, 0),
		check: func(t *testing.T, calls []pipelineWorkflowCall) {
			assert.Equal(t, 3, countCallsContaining(calls, `"projection":"index"`))
			assert.Equal(t, 2, countCallsContaining(calls, "+record-batch-create"))
			assertBatchLimits(t, calls, 20)
		},
	}
	runScriptedPipelineWorkflow(t, first)

	remaining := allIDs[40:]
	resumeSteps := []pipelineModelStep{larkStep(baseListArgv(), jsonString(map[string]any{"completed_keys": allIDs[:40], "has_more": false}))}
	resumeSteps = append(resumeSteps, indexSteps(allIDs)...)
	for _, ids := range chunkStrings(remaining, 100) {
		resumeSteps = append(resumeSteps, fullStep(ids))
	}
	resumeSteps = append(resumeSteps, batchCreateSteps(remaining)...)
	resume := pipelineWorkflowScenario{
		name: "second pass resumes after checkpoint and never rewrites completed rows", agent: 1, agentID: 7101, input: "继续上次打标",
		steps: resumeSteps, final: marker1(203, 40, 0, 0), wantMetadata: metrics1(203, 40, 0, 0),
		check: func(t *testing.T, calls []pipelineWorkflowCall) {
			assert.Equal(t, 3, countCallsContaining(calls, `"projection":"index"`))
			assert.Equal(t, 3, countCallsContaining(calls, `"projection":"full"`))
			assertBatchLimits(t, calls, 20)
			fullAndWrites := workflowCallsString(calls[4:])
			for _, completed := range allIDs[:40] {
				assert.NotContains(t, fullAndWrites, `"`+completed+`"`)
			}
			assert.NotContains(t, fullAndWrites, "+record-batch-update")
		},
	}
	runScriptedPipelineWorkflow(t, resume)
}

func TestThreeAgentPipelineWorkflow_Agent1Matrix(t *testing.T) {
	scenarios := []pipelineWorkflowScenario{
		{
			name: "second run skips every completed key", agent: 1, agentID: 7112, input: "再次执行",
			steps: []pipelineModelStep{
				larkStep(baseListArgv(), `{"completed_keys":["n1","n2"]}`),
				step("xhs_note_list", `{"projection":"index","limit":100}`, `{"items":[{"xhs_note_id":"n1"},{"xhs_note_id":"n2"}],"has_more":false}`),
			},
			final: marker1(0, 2, 0, 0), wantMetadata: metrics1(0, 2, 0, 0),
			check: noCallContaining(`"projection":"full"`, "+record-batch-create", "+record-upsert"),
		},
		{
			name: "new capture after prior run is the only write", agent: 1, agentID: 7113, input: "分析新增内容",
			steps: []pipelineModelStep{
				larkStep(baseListArgv(), `{"completed_keys":["n1"]}`),
				step("xhs_note_list", `{"projection":"index","limit":100}`, `{"items":[{"xhs_note_id":"n1"},{"xhs_note_id":"n2"}],"has_more":false}`),
				fullStep([]string{"n2"}),
				larkStep(baseCreateArgv([]string{"n2"}), `{"record_ids":["recN2"]}`),
			},
			final: marker1(1, 1, 0, 0), wantMetadata: metrics1(1, 1, 0, 0), check: exactlyOneCallContaining("+record-batch-create"),
		},
		{
			name: "incomplete existing row uses one-record upsert", agent: 1, agentID: 7114, input: "继续打标",
			steps: []pipelineModelStep{
				larkStep(baseListArgv(), `{"incomplete":{"n3":"recNoteN3ABC"}}`),
				step("xhs_note_list", `{"projection":"index","limit":100}`, `{"items":[{"xhs_note_id":"n3"}],"has_more":false}`),
				fullStep([]string{"n3"}),
				larkStep(baseUpsertArgv("n3", "recNoteN3ABC"), `{"record_id":"recNoteN3ABC"}`),
			},
			final: marker1(1, 0, 0, 0), wantMetadata: metrics1(1, 0, 0, 0),
			check: func(t *testing.T, calls []pipelineWorkflowCall) {
				exactlyOneCallContaining("+record-upsert")(t, calls)
				noCallContaining("+record-batch-update")(t, calls)
			},
		},
		{
			name: "explicit historical scope reanalyses only named keys", agent: 1, agentID: 7115, input: "只重新分析 n4 和 n5，并覆盖这两条",
			steps: []pipelineModelStep{
				larkStep(baseSearchArgv("n4"), `{"records":[{"id":"recNoteN4ABC"},{"id":"recNoteN5ABC"}]}`),
				fullStep([]string{"n4", "n5"}),
				larkStep(baseUpsertArgv("n4", "recNoteN4ABC"), `{"record_id":"recNoteN4ABC"}`),
				larkStep(baseUpsertArgv("n5", "recNoteN5ABC"), `{"record_id":"recNoteN5ABC"}`),
			},
			final: marker1(2, 0, 0, 0), wantMetadata: metrics1(2, 0, 0, 0),
			check: func(t *testing.T, calls []pipelineWorkflowCall) {
				assert.Equal(t, 2, countCallsContaining(calls, "+record-upsert"))
				noCallContaining("+record-batch-update")(t, calls)
			},
		},
		{
			name: "duplicate business key stops for disambiguation", agent: 1, agentID: 7116, input: "打标",
			steps: []pipelineModelStep{
				larkStep(baseListArgv(), `{"duplicates":{"n6":["recNoteN61ABC","recNoteN62ABC"]}}`),
				askStep("发现重复的小红书笔记ID n6，请先选择保留记录", `{"status":"waiting"}`),
			},
			final: marker1(0, 0, 1, 1), wantMetadata: metrics1(0, 0, 1, 1),
			check: noCallContaining("+record-batch-create", "+record-upsert", `"projection":"full"`),
		},
		{
			name: "partial success counts one failure without batch retry", agent: 1, agentID: 7117, input: "打标两条",
			steps: []pipelineModelStep{
				fullStep([]string{"n7", "n8"}),
				larkStep(baseCreateArgv([]string{"n7"}), `{"record_ids":["recN7"]}`),
				larkStep(baseCreateArgv([]string{"n8"}), `{"ok":false,"error":"write_failed"}`),
			},
			final: marker1(1, 0, 1, 1), wantMetadata: metrics1(1, 0, 1, 1),
			check: func(t *testing.T, calls []pipelineWorkflowCall) {
				assert.Equal(t, 2, countCallsContaining(calls, "+record-batch-create"))
				noCallContaining("+record-batch-update")(t, calls)
			},
		},
		{
			name: "unknown write is read-after-write reconciled and never replayed", agent: 1, agentID: 7118, input: "打标 n9",
			steps: []pipelineModelStep{
				fullStep([]string{"n9"}),
				larkStep(baseCreateArgv([]string{"n9"}), `{"state":"unknown"}`),
				larkStep(baseSearchArgv("n9"), `{"records":[{"小红书笔记ID":"n9","分析状态":"已完成"}]}`),
			},
			final: marker1(1, 0, 0, 0), wantMetadata: metrics1(1, 0, 0, 0),
			check: func(t *testing.T, calls []pipelineWorkflowCall) {
				exactlyOneCallContaining("+record-batch-create")(t, calls)
				assertOrder(t, calls, "+record-batch-create", "+record-search")
			},
		},
	}
	runPipelineScenarioMatrix(t, scenarios)
}

func TestThreeAgentPipelineWorkflow_Agent2Matrix(t *testing.T) {
	scenarios := []pipelineWorkflowScenario{
		{
			name: "upload is read to final byte before create", agent: 2, agentID: 7201, input: "从上传文件生成客户画像",
			steps: []pipelineModelStep{
				fileStep("customer.pdf", 0, "", `{"has_more":true,"next_offset":65536,"read_token":"stable"}`),
				fileStep("customer.pdf", 65536, "stable", `{"has_more":false}`),
				skillStep("lark-doc"), skillStep("lark-drive"),
				larkStep(driveSearchArgv("客户A-核心信息与人群画像"), `{"matches":[]}`),
				larkStep(docsCreateArgv("客户A-核心信息与人群画像", profileContent("客户A")), `{"doc":"new"}`),
			},
			final: marker2(1, "create"), wantMetadata: metrics23(1, "create"),
			check: func(t *testing.T, calls []pipelineWorkflowCall) {
				assertOrder(t, calls, `"offset":65536`, "docs\",\"+create")
			},
		},
		{
			name: "Feishu source and existing managed target are both fully read before overwrite", agent: 2, agentID: 7202, input: "读取飞书资料并更新画像",
			steps: []pipelineModelStep{
				skillStep("lark-doc"),
				larkStep(docsFetchArgv("doxcnSourceABC123"), `{"content":"完整来源","scope":"full"}`),
				skillStep("lark-drive"),
				larkStep(driveSearchArgv("客户B-核心信息与人群画像"), `{"matches":[{"doc":"doxcnProfileABC123"}]}`),
				larkStep(docsFetchArgv(pipelineProfile), `{"content":"<!-- numind-managed agent=agent-2 contract=customer-profile/v1 customer_key=客户B -->\n旧完整画像","scope":"full"}`),
				larkStep(docsUpdateArgv(pipelineProfile, "overwrite", profileContent("客户B"), ""), `{"ok":true}`),
			},
			final: marker2(1, "update"), wantMetadata: metrics23(1, "update"),
			check: func(t *testing.T, calls []pipelineWorkflowCall) {
				assertOrder(t, calls, "doxcnProfileABC123\",\"--scope\",\"full", "overwrite")
				exactlyOneCallContaining("overwrite")(t, calls)
			},
		},
		{
			name: "mixed sources both complete before write", agent: 2, agentID: 7203, input: "结合附件和飞书生成画像",
			steps: []pipelineModelStep{
				fileStep("brief.docx", 0, "", `{"has_more":false}`),
				larkStep(docsFetchArgv("doxcnResearchABC123"), `{"content":"完整飞书资料","scope":"full"}`),
				larkStep(driveSearchArgv("客户C-核心信息与人群画像"), `{"matches":[]}`),
				larkStep(docsCreateArgv("客户C-核心信息与人群画像", profileContent("客户C")), `{"ok":true}`),
			},
			final: marker2(2, "create"), wantMetadata: metrics23(2, "create"),
			check: func(t *testing.T, calls []pipelineWorkflowCall) {
				assertOrder(t, calls, "file_read", "docs\",\"+fetch", "docs\",\"+create")
			},
		},
		{
			name: "more than one exact target asks without write", agent: 2, agentID: 7204, input: "生成画像",
			steps: []pipelineModelStep{
				fileStep("brief.txt", 0, "", `{"has_more":false}`),
				larkStep(driveSearchArgv("客户D-核心信息与人群画像"), `{"matches":["d1","d2"]}`),
				askStep("找到 2 个同名文档，请选择目标", `{"status":"waiting"}`),
			},
			final: marker2(1, "unavailable"), wantMetadata: metrics23(1, "unavailable"), check: noCallContaining("docs\",\"+create", "overwrite"),
		},
		{
			name: "official authorization recovery resumes the exact business read", agent: 2, agentID: 7205, input: "从飞书生成画像",
			steps: []pipelineModelStep{
				larkStep(docsFetchArgv("doxcnSourceAuth123"), `{"auth_required":true}`),
				askStep("请完成飞书官方授权，完成后回复继续", `{"authorized":true}`),
				larkStep(docsFetchArgv("doxcnSourceAuth123"), `{"content":"完整来源","scope":"full"}`),
				larkStep(driveSearchArgv("客户E-核心信息与人群画像"), `{"matches":[]}`),
				larkStep(docsCreateArgv("客户E-核心信息与人群画像", profileContent("客户E")), `{"ok":true}`),
			},
			final: marker2(1, "create"), wantMetadata: metrics23(1, "create"),
			check: func(t *testing.T, calls []pipelineWorkflowCall) {
				assert.Equal(t, calls[0].Args, calls[2].Args)
				assertOrder(t, calls, "ask_user_question", "docs\",\"+create")
			},
		},
		{
			name: "unmanaged exact collision is not overwritten", agent: 2, agentID: 7206, input: "更新画像",
			steps: []pipelineModelStep{
				fileStep("brief.txt", 0, "", `{"has_more":false}`),
				larkStep(driveSearchArgv("客户F-核心信息与人群画像"), `{"matches":[{"doc":"doxcnCollision123"}]}`),
				larkStep(docsFetchArgv("doxcnCollision123"), `{"content":"用户自己的无标记文档","scope":"full"}`),
				askStep("同名文档不含受管标记，请选择接管或改名", `{"status":"waiting"}`),
			},
			final: marker2(1, "unavailable"), wantMetadata: metrics23(1, "unavailable"), check: noCallContaining("overwrite", "docs\",\"+create"),
		},
		{
			name: "damaged managed marker blocks overwrite", agent: 2, agentID: 7207, input: "更新画像",
			steps: []pipelineModelStep{
				fileStep("brief.txt", 0, "", `{"has_more":false}`),
				larkStep(driveSearchArgv("客户G-核心信息与人群画像"), `{"matches":[{"doc":"doxcnDamagedABC"}]}`),
				larkStep(docsFetchArgv("doxcnDamagedABC"), `{"content":"<!-- numind-managed damaged -->","scope":"full"}`),
				askStep("目标文档受管标记损坏，请确认处理方式", `{"status":"waiting"}`),
			},
			final: marker2(1, "unavailable"), wantMetadata: metrics23(1, "unavailable"), check: noCallContaining("overwrite", "docs\",\"+create"),
		},
		{
			name: "incomplete source cannot produce complete profile", agent: 2, agentID: 7208, input: "生成画像",
			steps: []pipelineModelStep{
				fileStep("broken.pdf", 0, "", `{"has_more":true,"next_offset":65536,"read_token":"required"}`),
				fileStep("broken.pdf", 65536, "required", `{"ok":false,"error":"read_token_mismatch"}`),
				askStep("来源未能完整读取，请重新上传", `{"status":"waiting"}`),
			},
			final: marker2(0, "unavailable"), wantMetadata: metrics23(0, "unavailable"), check: noCallContaining("overwrite", "docs\",\"+create"),
		},
	}
	runPipelineScenarioMatrix(t, scenarios)
}

func TestThreeAgentPipelineWorkflow_Agent3Matrix(t *testing.T) {
	fixture := topicRoundContent("R20260720T083000Z-a1b2c3")
	scenarios := []pipelineWorkflowScenario{
		{
			name: "qualified and partially-qualified sources produce a compliant nine-field round", agent: 3, agentID: 7301, input: "规划新一轮选题，账号不是蓝V，处于0-1阶段",
			steps: []pipelineModelStep{
				skillStep("lark-base"),
				larkStep(baseListArgv(), agent1SourceFixture()),
				skillStep("lark-doc"),
				larkStep(docsFetchArgv(pipelineProfile), `{"content":"客户处于0-1阶段；账号非蓝V；事实来自画像卡","scope":"full"}`),
				skillStep("lark-drive"),
				larkStep(driveSearchArgv("客户A-选题规划"), `{"matches":[{"doc":"doxcnTopicsABC123"}]}`),
				larkStep(docsFetchArgv(pipelineTopics), `{"content":"<!-- numind-managed agent=agent-3 contract=topic-plan/v1 customer_key=客户A -->\n旧轮次完整内容","scope":"full"}`),
				larkStep(docsUpdateArgv(pipelineTopics, "append", fixture, ""), `{"ok":true}`),
			},
			final: marker3(2, "append"), wantMetadata: metrics23(2, "append"),
			check: func(t *testing.T, calls []pipelineWorkflowCall) {
				joined := workflowCallsString(calls)
				assert.NotContains(t, joined, "xhs_note_list")
				for _, field := range topicNineFields() {
					assert.GreaterOrEqual(t, strings.Count(joined, field+"："), 2, "every formal topic must contain %s", field)
				}
				assert.Contains(t, joined, "仅借局部手法")
				assert.Contains(t, joined, "可借鉴：反常识开头")
				assert.Contains(t, joined, "不可照搬：客户结果数字")
				assert.NotContains(t, joined, "不达标来源-n3")
				assert.Contains(t, joined, "该账号非蓝 V，本轮跳过硬广营销类选题")
				assert.Contains(t, joined, "硬广营销类数量：0")
				assert.NotContains(t, joined, "主语自检：机构")
				assert.Contains(t, joined, "主语自检：创始人本人")
				assert.Contains(t, joined, "不足70条说明")
				assertOrder(t, calls, "doxcnTopicsABC123\",\"--scope\",\"full", "append")
			},
		},
		{
			name: "zero target creates customer planning doc", agent: 3, agentID: 7302, input: "为客户H做选题",
			steps: []pipelineModelStep{
				fileStep("agent1.csv", 0, "", `{"has_more":false}`),
				fileStep("agent2.docx", 0, "", `{"has_more":false}`),
				larkStep(driveSearchArgv("客户H-选题规划"), `{"matches":[]}`),
				larkStep(docsCreateArgv("客户H-选题规划", topicsDocumentContent("客户H", "R-new")), `{"ok":true}`),
			},
			final: marker3(2, "create"), wantMetadata: metrics23(2, "create"), check: exactlyOneCallContaining("docs\",\"+create"),
		},
		{
			name: "specified existing round is replaced exactly", agent: 3, agentID: 7303, input: "修改 R-fixed 这一轮",
			steps: []pipelineModelStep{
				larkStep(baseListArgv(), agent1SourceFixture()),
				larkStep(docsFetchArgv(pipelineProfile), `{"content":"完整画像","scope":"full"}`),
				larkStep(docsFetchArgv(pipelineTopics), `{"content":"<!-- numind-managed agent=agent-3 contract=topic-plan/v1 customer_key=客户A -->\n<!-- round:start id=R-old -->旧<!-- round:end id=R-old -->\n<!-- round:start id=R-fixed -->待替换<!-- round:end id=R-fixed -->","scope":"full"}`),
				larkStep(docsUpdateArgv(pipelineTopics, "str_replace", topicRoundContent("R-fixed"), "<!-- round:start id=R-fixed -->待替换<!-- round:end id=R-fixed -->"), `{"ok":true}`),
			},
			final: marker3(2, "replace-round"), wantMetadata: metrics23(2, "replace-round"),
			check: func(t *testing.T, calls []pipelineWorkflowCall) {
				exactlyOneCallContaining("str_replace")(t, calls)
				assert.Contains(t, calls[len(calls)-1].Args, "R-fixed")
				noCallContaining(`"append"`, "docs\",\"+create")(t, calls)
			},
		},
		{
			name: "unknown append is reconciled through round marker after a pre-write full read", agent: 3, agentID: 7304, input: "追加一轮",
			steps: []pipelineModelStep{
				larkStep(baseListArgv(), agent1SourceFixture()),
				larkStep(docsFetchArgv(pipelineProfile), `{"content":"完整画像","scope":"full"}`),
				larkStep(docsFetchArgv(pipelineTopics), `{"content":"<!-- numind-managed agent=agent-3 contract=topic-plan/v1 customer_key=客户A -->\n旧轮次","scope":"full"}`),
				larkStep(docsUpdateArgv(pipelineTopics, "append", topicRoundContent("R-unknown"), ""), `{"state":"unknown"}`),
				larkStep(docsFetchArgv(pipelineTopics), `{"content":"<!-- round:start id=R-unknown -->已存在<!-- round:end id=R-unknown -->","scope":"full"}`),
			},
			final: marker3(2, "append"), wantMetadata: metrics23(2, "append"),
			check: func(t *testing.T, calls []pipelineWorkflowCall) {
				exactlyOneCallContaining(`"append"`)(t, calls)
				assertOrder(t, calls, "doxcnTopicsABC123\",\"--scope\",\"full", `"append"`, "doxcnTopicsABC123\",\"--scope\",\"full")
			},
		},
		{
			name: "unmanaged exact collision is never taken over", agent: 3, agentID: 7305, input: "规划选题",
			steps: []pipelineModelStep{
				larkStep(baseListArgv(), agent1SourceFixture()),
				larkStep(docsFetchArgv(pipelineProfile), `{"content":"完整画像","scope":"full"}`),
				larkStep(driveSearchArgv("客户I-选题规划"), `{"matches":[{"doc":"doxcnCollisionI12"}]}`),
				larkStep(docsFetchArgv("doxcnCollisionI12"), `{"content":"无受管标记","scope":"full"}`),
				askStep("同名文档无受管标记，请选择接管或改名", `{"status":"waiting"}`),
			},
			final: marker3(2, "unavailable"), wantMetadata: metrics23(2, "unavailable"), check: noCallContaining(`"append"`, "docs\",\"+create", "str_replace"),
		},
		{
			name: "more than one exact target asks for disambiguation", agent: 3, agentID: 7306, input: "规划选题",
			steps: []pipelineModelStep{
				fileStep("agent1.csv", 0, "", `{"has_more":false}`),
				fileStep("agent2.docx", 0, "", `{"has_more":false}`),
				larkStep(driveSearchArgv("客户J-选题规划"), `{"matches":["j1","j2"]}`),
				askStep("找到 2 个同名选题文档，请选择目标", `{"status":"waiting"}`),
			},
			final: marker3(2, "unavailable"), wantMetadata: metrics23(2, "unavailable"), check: noCallContaining(`"append"`, "docs\",\"+create", "str_replace"),
		},
	}
	runPipelineScenarioMatrix(t, scenarios)
}

func TestThreeAgentPipelineWorkflow_RealAskQuestionYieldsAndPersists(t *testing.T) {
	prompt := loadPipelineSystemPrompt(t, 2)
	original := chatFn
	t.Cleanup(func() { chatFn = original })
	chatFn = func(_ context.Context, _ string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		found := false
		for _, message := range req.Messages {
			found = found || (message.Role == aiservice.MessageRoleSystem && pipelinePromptWasInjected(message.Content.Text, prompt))
		}
		require.True(t, found)
		return &aiservice.ChatResponse{
			ToolCalls: []aiservice.ToolCall{{ID: "pipeline-real-yield", Type: "function", Function: aiservice.ToolCallFunction{
				Name: "ask_user_question", Arguments: askArgs("请提供本轮客户的准确名称"),
			}}}, FinishReason: "tool_calls", Model: "test-model", Provider: "test",
		}, nil
	}
	store := newMockStore()
	skills := newMemorySkillStore(1, 7399, "")
	skills.fixed.SystemPrompt = prompt
	runner := NewAgentRunner(store, newStaticRegistry(NewAskUserQuestionTool()), WithSkillStore(skills))
	result, err := runner.Run(context.Background(), RunRequest{
		UserID: 1, AgentDefinitionID: 7399, SessionID: "pipeline-real-yield", Input: "生成画像",
		ToolNames: []string{"ask_user_question"}, EnforceToolAllowlist: true,
	})
	require.NoError(t, err)
	require.Equal(t, TerminalWaitingForUserChoice, result.TerminalReason)
	stored, err := store.Get(context.Background(), result.AgentRunID)
	require.NoError(t, err)
	assert.Equal(t, string(TerminalWaitingForUserChoice), stored.StateReason)
	assert.Contains(t, string(stored.PendingQuestionJSON), "请提供本轮客户的准确名称")
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

func skillStep(skill string) pipelineModelStep {
	return step("lark_skill_read", jsonString(map[string]string{"skill": skill}), `{"ok":true}`)
}

func larkStep(argv []string, result string) pipelineModelStep {
	return step("lark_execute", jsonString(map[string]any{"argv": argv}), result)
}

func askStep(question, result string) pipelineModelStep {
	return step("ask_user_question", askArgs(question), result)
}

func askArgs(question string) string {
	return jsonString(map[string]any{"questions": []any{map[string]any{"question": question, "options": []any{}}}})
}

func fileStep(name string, offset int, token, result string) pipelineModelStep {
	input := map[string]any{
		"file_url": fmt.Sprintf("https://numind-test.cos.ap-shanghai.myqcloud.com/agent-attachments/1/%s?sign=redacted", name),
		"offset":   offset,
	}
	if token != "" {
		input["read_token"] = token
	}
	return step("file_read", jsonString(input), result)
}

func fullStep(ids []string) pipelineModelStep {
	items := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		items = append(items, map[string]any{"xhs_note_id": id, "title": "标题 " + id, "content": "正文", "note_url": "https://xhs.example/" + id})
	}
	return step("xhs_note_list", jsonString(map[string]any{"projection": "full", "limit": 100, "xhs_note_ids": ids}), jsonString(map[string]any{"items": items, "has_more": false}))
}

func indexSteps(ids []string) []pipelineModelStep {
	chunks := chunkStrings(ids, 100)
	steps := make([]pipelineModelStep, 0, len(chunks))
	normalized, err := normalizeXhsNoteListInput(ToolInput(`{"projection":"index","limit":100}`))
	if err != nil {
		panic(err)
	}
	var previousCursor string
	for index, chunk := range chunks {
		input := map[string]any{"projection": "index", "limit": 100}
		if index > 0 {
			input["cursor"] = previousCursor
		}
		items := make([]map[string]string, 0, len(chunk))
		for _, id := range chunk {
			items = append(items, map[string]string{"xhs_note_id": id})
		}
		output := map[string]any{"items": items, "snapshot_total": len(ids), "has_more": index < len(chunks)-1}
		if index < len(chunks)-1 {
			previousCursor, err = encodeXhsNoteListCursor(xhsNoteListCursor{
				Version: xhsNoteListCursorVersion, AfterID: uint64((index + 1) * 100), SnapshotMaxID: uint64(len(ids)),
				SnapshotTotal: int64(len(ids)), FilterSHA256: normalized.filterSHA256, Projection: "index",
			})
			if err != nil {
				panic(err)
			}
			output["next_cursor"] = previousCursor
		}
		steps = append(steps, step("xhs_note_list", jsonString(input), jsonString(output)))
	}
	return steps
}

func batchCreateSteps(ids []string) []pipelineModelStep {
	chunks := chunkStrings(ids, 20)
	steps := make([]pipelineModelStep, 0, len(chunks))
	for _, chunk := range chunks {
		steps = append(steps, larkStep(baseCreateArgv(chunk), jsonString(map[string]any{"created": len(chunk)})))
	}
	return steps
}

func baseListArgv() []string {
	return []string{"base", "+record-list", "--base-token", pipelineBaseToken, "--table-id", pipelineTableID, "--limit", "100"}
}

func baseSearchArgv(keyword string) []string {
	return []string{"base", "+record-search", "--base-token", pipelineBaseToken, "--table-id", pipelineTableID, "--keyword", keyword, "--search-field", "小红书笔记ID", "--limit", "100"}
}

func baseCreateArgv(ids []string) []string {
	rows := make([][]any, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, []any{id, "已完成"})
	}
	payload := jsonString(map[string]any{"fields": []string{"小红书笔记ID", "分析状态"}, "rows": rows})
	return []string{"base", "+record-batch-create", "--base-token", pipelineBaseToken, "--table-id", pipelineTableID, "--json", payload}
}

func baseUpsertArgv(id, recordID string) []string {
	payload := jsonString(map[string]any{"小红书笔记ID": id, "分析状态": "已完成"})
	return []string{"base", "+record-upsert", "--base-token", pipelineBaseToken, "--table-id", pipelineTableID, "--record-id", recordID, "--json", payload}
}

func docsFetchArgv(doc string) []string {
	return []string{"docs", "+fetch", "--doc", doc, "--scope", "full", "--doc-format", "markdown"}
}

func docsCreateArgv(title, content string) []string {
	return []string{"docs", "+create", "--title", title, "--content", content, "--doc-format", "markdown"}
}

func docsUpdateArgv(doc, command, content, pattern string) []string {
	argv := []string{"docs", "+update", "--doc", doc, "--command", command, "--content", content, "--doc-format", "markdown"}
	if pattern != "" {
		argv = append(argv, "--pattern", pattern)
	}
	return argv
}

func driveSearchArgv(title string) []string {
	return []string{"drive", "+search", "--query", title, "--only-title", "--doc-types", "docx", "--page-size", "20"}
}

func profileContent(customer string) string {
	return fmt.Sprintf("# 客户核心信息与人群画像卡\n客户名称：%s\n<!-- numind-managed agent=agent-2 contract=customer-profile/v1 customer_key=%s -->\n## 一、客户基础信息\n完整\n## 二、客户定位\n完整\n## 三、核心人群画像\n完整\n## 四、案例素材\n完整\n## 五、产品与服务\n完整\n## 六、内容边界\n完整\n## 七、资料缺口与待确认\n完整", customer, customer)
}

func topicsDocumentContent(customer, round string) string {
	return fmt.Sprintf("# 选题规划\n<!-- numind-managed agent=agent-3 contract=topic-plan/v1 customer_key=%s -->\n%s", customer, topicRoundContent(round))
}

func topicNineFields() []string {
	return []string{"选题内容", "选择原因", "归属小类", "生成路径", "推导链", "参考来源链接", "参考类型", "变形说明", "主语自检"}
}

func topicRoundContent(round string) string {
	return fmt.Sprintf(`<!-- round:start id=%s -->
## 本轮选题
### 选题 1
- 选题内容：创始人亲述：为什么多数咨询方案落不了地
- 选择原因：画像卡确认客户处于 0-1 阶段，适合以创始人本人建立信任
- 归属小类：人设型-专业判断
- 生成路径：画像事实 + 达标来源 n1 的冲突结构
- 推导链：客户事实 → 来源结构 → 新选题
- 参考来源链接：https://xhs.example/n1
- 参考类型：达标完整借鉴
- 变形说明：只借结构，不借原行业事实
- 主语自检：创始人本人

### 选题 2
- 选题内容：顾问视角：服务型老板最容易忽略的一个交付节点
- 选择原因：部分达标来源仍有可迁移的反常识开头
- 归属小类：精准痛点-交付误区
- 生成路径：画像事实 + 部分达标来源 n2，仅借局部手法
- 推导链：客户交付事实 → 可借鉴：反常识开头 → 避开不可照搬：客户结果数字
- 参考来源链接：https://xhs.example/n2
- 参考类型：部分达标，仅借局部手法
- 变形说明：可借鉴：反常识开头；不可照搬：客户结果数字
- 主语自检：顾问本人

该账号非蓝 V，本轮跳过硬广营销类选题。
硬广营销类数量：0
不足70条说明：当前仅有 2 条满足来源、画像、主语与合规约束的正式选题，不以不达标或缺字段素材凑数。
<!-- round:end id=%s -->`, round, round)
}

func agent1SourceFixture() string {
	return `{"records":[{"小红书笔记ID":"n1","达标判定":"达标","原文链接":"https://xhs.example/n1","必要标签":"完整","跨赛道结论":"可迁移"},{"小红书笔记ID":"n2","达标判定":"部分达标","可借鉴部分":"反常识开头","不可照搬部分":"客户结果数字","原文链接":"https://xhs.example/n2","必要标签":"完整","跨赛道结论":"局部迁移"},{"小红书笔记ID":"不达标来源-n3","达标判定":"不达标","原文链接":"https://xhs.example/n3"}]}`
}

func noteIDs(from, through int) []string {
	ids := make([]string, 0, through-from+1)
	for value := from; value <= through; value++ {
		ids = append(ids, fmt.Sprintf("note-%03d", value))
	}
	return ids
}

func chunkStrings(values []string, size int) [][]string {
	chunks := make([][]string, 0, (len(values)+size-1)/size)
	for start := 0; start < len(values); start += size {
		end := start + size
		if end > len(values) {
			end = len(values)
		}
		chunks = append(chunks, values[start:end])
	}
	return chunks
}

func jsonString(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func assertBatchLimits(t *testing.T, calls []pipelineWorkflowCall, maximum int) {
	t.Helper()
	for _, call := range calls {
		if call.Tool != "lark_execute" || !strings.Contains(call.Args, "+record-batch-create") {
			continue
		}
		decoded, err := decodeLarkExecuteInput(ToolInput(call.Args))
		require.NoError(t, err)
		jsonIndex := -1
		for index, arg := range decoded.Argv {
			if arg == "--json" {
				jsonIndex = index + 1
				break
			}
		}
		require.Greater(t, jsonIndex, 0)
		var payload struct {
			Rows [][]any `json:"rows"`
		}
		require.NoError(t, json.Unmarshal([]byte(decoded.Argv[jsonIndex]), &payload))
		assert.LessOrEqual(t, len(payload.Rows), maximum)
	}
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
	return map[string]string{"status": "ok", "processed": fmt.Sprint(processed), "skipped": fmt.Sprint(skipped), "remaining": fmt.Sprint(remaining), "failed": fmt.Sprint(failed)}
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
		index := strings.Index(joined[previous+1:], needle)
		require.GreaterOrEqual(t, index, 0, "missing %q after prior checkpoint in %s", needle, joined)
		previous += index + 1
	}
}
