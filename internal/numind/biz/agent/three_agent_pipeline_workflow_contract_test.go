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
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"

	"numind-server/internal/numind/biz/feishu"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/model"
)

const (
	pipelineBaseToken    = "bascnABCDEFG123"
	pipelineTableID      = "tblABCDEFG123"
	pipelineProfile      = "doxcnProfileABC123"
	pipelineTopics       = "doxcnTopicsABC123"
	profileManagedBegin  = "[有数AI受管区：客户画像｜契约 profile/v1｜开始]"
	profileManagedEnd    = "[有数AI受管区：客户画像｜契约 profile/v1｜结束]"
	topicsManagedHeader  = "[有数AI受管文档：选题规划｜契约 topics/v1]"
	pipelineRoundOld     = "R20260719T083000Z-111111"
	pipelineRoundNew     = "R20260720T083000Z-a1b2c3"
	pipelineRoundCreate  = "R20260720T090000Z-b2c3d4"
	pipelineRoundFixed   = "R20260720T091500Z-c3d4e5"
	pipelineRoundUnknown = "R20260720T093000Z-d4e5f6"
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
		r.err = fmt.Errorf("model tool set = %v, want global registry set %v", names, r.expectedToolNames)
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
		if len(decoded.Argv) >= 2 && decoded.Argv[0] == "base" && (decoded.Argv[1] == "+record-batch-create" || decoded.Argv[1] == "+record-upsert") {
			if err = validateCompleteAgent1Write(decoded.Argv); err != nil {
				return err
			}
		}
		if len(decoded.Argv) >= 2 && decoded.Argv[0] == "base" && decoded.Argv[1] == "+base-create" {
			if err = validateAgent1BaseCreate(decoded.Argv); err != nil {
				return err
			}
		}
		if len(decoded.Argv) >= 2 && decoded.Argv[0] == "docs" && (decoded.Argv[1] == "+create" || decoded.Argv[1] == "+update") {
			if content, exists := argvValue(decoded.Argv, "--content"); exists {
				if strings.Contains(content, profileManagedBegin) {
					if err = validateProfileContent(content); err != nil {
						return err
					}
				}
				if strings.Contains(content, topicsManagedHeader) || strings.Contains(content, "[有数AI轮次：") {
					if err = validateTopicContent(content); err != nil {
						return err
					}
				}
			}
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
	if scenario.agent == 1 {
		require.NoError(t, validateAgent1RawLineage(scenario.steps))
	}
	prompt := loadPipelineSystemPrompt(t, scenario.agent)
	toolNames := uniquePipelineToolNames(scenario.steps)
	allNames := []string{"ask_user_question", "file_read", "lark_execute", "lark_inspect", "lark_skill_read", "xhs_note_list"}
	recorder := &pipelineWorkflowRecorder{expected: scenario.steps, prompt: prompt, expectedToolNames: allNames}
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
	skillStore.fixed.ToolFlags = directPipelineToolFlags(allNames, toolNames)
	runner := NewAgentRunner(newMockStore(), newStaticRegistry(tools...), WithSkillStore(skillStore))

	result, err := runner.Run(context.Background(), RunRequest{
		UserID: 1, AgentDefinitionID: scenario.agentID,
		SessionID: fmt.Sprintf("pipeline-%d", scenario.agentID), Input: scenario.input,
		// Deliberately stale caller policy: it cannot restrict the global registry set.
		ToolNames: allNames, EnforceToolAllowlist: false,
	})
	require.NoError(t, err)
	require.Equal(t, TerminalCompleted, result.TerminalReason)
	require.Equal(t, scenario.final, result.FinalOutput)
	require.Equal(t, len(scenario.steps)+1, callCount)
	calls, consumed, promptObserved, schemasObserved, recordErr := recorder.snapshot()
	require.NoError(t, recordErr)
	require.Equal(t, len(scenario.steps), consumed)
	require.True(t, promptObserved, "the checked-in Agent %d prompt must reach the model (source bytes=%d, model bytes=%d)", scenario.agent, len(prompt), len(recorder.systemPrompt))
	require.True(t, schemasObserved, "every globally registered production-schema tool must reach the model")
	require.Len(t, calls, len(scenario.steps))
	metadata := findPipelineTraceMetadata(t, *events)
	for key, value := range scenario.wantMetadata {
		assert.Equal(t, value, metadata[key], "safe metric %s", key)
	}
	return calls, metadata
}

func directPipelineToolFlags(allNames, allowedNames []string) datatypes.JSON {
	allowed := make(map[string]bool, len(allowedNames))
	for _, name := range allowedNames {
		allowed[name] = true
	}
	flags := make(map[string]bool, len(allNames))
	for _, name := range allNames {
		flags[name] = allowed[name]
	}
	return datatypes.JSON(jsonString(flags))
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
				larkStep(baseSearchArgv("n9"), jsonString(map[string]any{"records": []any{completeAgent1Record("n9")}})),
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

func TestThreeAgentPipelineWorkflow_Agent1BaseTargetResolution(t *testing.T) {
	scenarios := []pipelineWorkflowScenario{
		{
			name: "missing Base target asks for exact file name before search", agent: 1, agentID: 7120, input: "开始打标",
			steps: []pipelineModelStep{
				askStep("请确认要保存到哪个飞书文件，以及对应的精确文件名", `{"status":"waiting"}`),
			},
			final: marker1(0, 0, 0, 0), wantMetadata: metrics1(0, 0, 0, 0),
			check: noCallContaining("drive\",\"+search", "base\",\"+base-create", "base\",\"+field-list"),
		},
		{
			name: "zero exact Base creates the 34-field workspace", agent: 1, agentID: 7121, input: "保存到精确文件名“爆款素材分析库”，开始打标",
			steps: []pipelineModelStep{
				skillStep("lark-drive"),
				larkStep(driveSearchArgvWithType("爆款素材分析库", "bitable"), `{"matches":[]}`),
				skillStep("lark-base"),
				larkStep(baseCreateWorkspaceArgv(), `{"base_token":"bascnABCDEFG123","table_id":"tblABCDEFG123"}`),
			},
			final: marker1(0, 0, 0, 0), wantMetadata: metrics1(0, 0, 0, 0),
			check: func(t *testing.T, calls []pipelineWorkflowCall) {
				exactlyOneCallContaining("base\",\"+base-create")(t, calls)
				assert.Contains(t, workflowCallsString(calls), `"--table-name","爆款素材"`)
			},
		},
		{
			name: "one exact Base is reused after schema inspection", agent: 1, agentID: 7122, input: "保存到精确文件名“爆款素材分析库”，开始打标",
			steps: []pipelineModelStep{
				larkStep(driveSearchArgvWithType("爆款素材分析库", "bitable"), `{"matches":[{"base_token":"bascnABCDEFG123","table_id":"tblABCDEFG123"}]}`),
				larkStep(baseFieldListArgv(), jsonString(map[string]any{"fields": baseFieldDefinitions(), "has_more": false})),
			},
			final: marker1(0, 0, 0, 0), wantMetadata: metrics1(0, 0, 0, 0),
			check: func(t *testing.T, calls []pipelineWorkflowCall) {
				exactlyOneCallContaining("base\",\"+field-list")(t, calls)
				noCallContaining("base\",\"+base-create", "ask_user_question")(t, calls)
			},
		},
		{
			name: "multiple exact Bases ask for disambiguation without a write", agent: 1, agentID: 7123, input: "保存到精确文件名“爆款素材分析库”，开始打标",
			steps: []pipelineModelStep{
				larkStep(driveSearchArgvWithType("爆款素材分析库", "bitable"), `{"matches":["b1","b2"]}`),
				askStep("找到 2 个同名爆款素材分析库，请选择本次目标", `{"status":"waiting"}`),
			},
			final: marker1(0, 0, 0, 0), wantMetadata: metrics1(0, 0, 0, 0),
			check: noCallContaining("base\",\"+base-create", "base\",\"+field-list", "+record-batch-create"),
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
				larkStep(docsFetchArgv(pipelineProfile), jsonString(map[string]any{"content": profileContent("客户B"), "scope": "full"})),
				larkStep(docsUpdateArgv(pipelineProfile, "overwrite", profileContent("客户B"), ""), `{"ok":true}`),
			},
			final: marker2(1, "update"), wantMetadata: metrics23(1, "update"),
			check: func(t *testing.T, calls []pipelineWorkflowCall) {
				assertOrder(t, calls, "doxcnProfileABC123\",\"--scope\",\"full", "overwrite")
				exactlyOneCallContaining("overwrite")(t, calls)
				assertProfileContract(t, workflowCallsString(calls))
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
				larkStep(docsFetchArgv("doxcnDamagedABC"), jsonString(map[string]any{"content": profileManagedBegin + "\n缺少结束标记", "scope": "full"})),
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
	fixture := topicRoundContent(pipelineRoundNew, 2)
	fixedOld := strings.Replace(topicRoundContent(pipelineRoundFixed, 2), "多数咨询方案", "许多咨询方案", 1)
	scenarios := []pipelineWorkflowScenario{
		{
			name: "qualified and partially-qualified sources produce a compliant nine-field round", agent: 3, agentID: 7301, input: "规划新一轮选题，账号不是蓝V，处于0-1阶段",
			steps: []pipelineModelStep{
				skillStep("lark-base"),
				larkStep(baseListArgv(), agent1SourceFixture()),
				skillStep("lark-doc"),
				larkStep(docsFetchArgv(pipelineProfile), jsonString(map[string]any{"content": profileContent("客户A") + "\n账号阶段：0-1\n蓝V：否", "scope": "full"})),
				skillStep("lark-drive"),
				larkStep(driveSearchArgv("客户A-选题规划"), `{"matches":[{"doc":"doxcnTopicsABC123"}]}`),
				larkStep(docsFetchArgv(pipelineTopics), jsonString(map[string]any{"content": topicsDocumentContent("客户A", pipelineRoundOld), "scope": "full"})),
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
				assert.Contains(t, joined, "生成路径：向外求")
				assert.Contains(t, joined, "参考类型：结构+内容")
				assert.Contains(t, joined, "参考类型：仅参考结构")
				assertOrder(t, calls, "doxcnTopicsABC123\",\"--scope\",\"full", "append")
			},
		},
		{
			name: "zero target creates customer planning doc", agent: 3, agentID: 7302, input: "为客户H做选题",
			steps: []pipelineModelStep{
				fileStep("agent1.csv", 0, "", `{"has_more":false}`),
				fileStep("agent2.docx", 0, "", `{"has_more":false}`),
				larkStep(driveSearchArgv("客户H-选题规划"), `{"matches":[]}`),
				larkStep(docsCreateArgv("客户H-选题规划", topicsDocumentContent("客户H", pipelineRoundCreate)), `{"ok":true}`),
			},
			final: marker3(2, "create"), wantMetadata: metrics23(2, "create"), check: exactlyOneCallContaining("docs\",\"+create"),
		},
		{
			name: "specified existing round is replaced exactly", agent: 3, agentID: 7303, input: "修改 " + pipelineRoundFixed + " 这一轮",
			steps: []pipelineModelStep{
				larkStep(baseListArgv(), agent1SourceFixture()),
				larkStep(docsFetchArgv(pipelineProfile), jsonString(map[string]any{"content": profileContent("客户A"), "scope": "full"})),
				larkStep(docsFetchArgv(pipelineTopics), jsonString(map[string]any{"content": topicsManagedHeader + "\n# 客户A 选题规划\n" + topicRoundContent(pipelineRoundOld, 1) + "\n" + fixedOld, "scope": "full"})),
				larkStep(docsUpdateArgv(pipelineTopics, "str_replace", topicRoundContent(pipelineRoundFixed, 2), fixedOld), `{"ok":true}`),
			},
			final: marker3(2, "replace-round"), wantMetadata: metrics23(2, "replace-round"),
			check: func(t *testing.T, calls []pipelineWorkflowCall) {
				exactlyOneCallContaining("str_replace")(t, calls)
				assert.Contains(t, calls[len(calls)-1].Args, pipelineRoundFixed)
				noCallContaining(`"append"`, "docs\",\"+create")(t, calls)
			},
		},
		{
			name: "unknown append is reconciled through round marker after a pre-write full read", agent: 3, agentID: 7304, input: "追加一轮",
			steps: []pipelineModelStep{
				larkStep(baseListArgv(), agent1SourceFixture()),
				larkStep(docsFetchArgv(pipelineProfile), jsonString(map[string]any{"content": profileContent("客户A"), "scope": "full"})),
				larkStep(docsFetchArgv(pipelineTopics), jsonString(map[string]any{"content": topicsDocumentContent("客户A", pipelineRoundOld), "scope": "full"})),
				larkStep(docsUpdateArgv(pipelineTopics, "append", topicRoundContent(pipelineRoundUnknown, 2), ""), `{"state":"unknown"}`),
				larkStep(docsFetchArgv(pipelineTopics), jsonString(map[string]any{"content": topicsDocumentContent("客户A", pipelineRoundOld) + "\n" + topicRoundContent(pipelineRoundUnknown, 2), "scope": "full"})),
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
				larkStep(docsFetchArgv(pipelineProfile), jsonString(map[string]any{"content": profileContent("客户I"), "scope": "full"})),
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

func TestThreeAgentPipelineWorkflow_OfficialFeishuAuthorizationResumesOriginalToolCall(t *testing.T) {
	prompt := loadPipelineSystemPrompt(t, 2)
	toolCallID := "agent2-auth-call"
	operationID := "op-agent2-auth"
	argv := docsFetchArgv("doxcnSourceAuth123")
	original := chatFn
	t.Cleanup(func() { chatFn = original })
	chatFn = func(_ context.Context, _ string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		promptObserved := false
		for _, message := range req.Messages {
			promptObserved = promptObserved || (message.Role == aiservice.MessageRoleSystem && pipelinePromptWasInjected(message.Content.Text, prompt))
		}
		require.True(t, promptObserved)
		require.Len(t, req.Tools, 1)
		require.Equal(t, "lark_execute", req.Tools[0].Function.Name)
		return &aiservice.ChatResponse{
			ToolCalls: []aiservice.ToolCall{{ID: toolCallID, Type: "function", Function: aiservice.ToolCallFunction{
				Name: "lark_execute", Arguments: jsonString(map[string]any{"argv": argv}),
			}}},
			FinishReason: "tool_calls", Model: "test-model", Provider: "test",
		}, nil
	}
	expiresAt := time.Now().UTC().Add(time.Hour)
	executor := &fakeLarkExecutor{result: &feishu.OperationResult{
		OperationID: operationID, State: model.FeishuOperationWaitingUserAuth,
		Action: &feishu.OperationAction{
			Provider: "lark", OperationID: operationID, SessionID: "session-agent2-auth", Phase: "user_auth",
			URL: "https://open.feishu.cn/open-apis/authen/v1/authorize?state=live-only", ExpiresAt: expiresAt,
		},
	}}
	writer := &externalActionWriterStore{mockAgentRunStore: newMockStore()}
	skills := newMemorySkillStore(1, 7299, "")
	skills.fixed.Name = pipelineAgent2Name
	skills.fixed.SystemPrompt = prompt
	skills.fixed.ToolFlags = datatypes.JSON(`{"lark_execute":true,"ask_user_question":false}`)
	runner := NewAgentRunner(writer, newStaticRegistry(&larkExecuteTool{executor: executor}), WithSkillStore(skills))

	result, err := runner.Run(context.Background(), RunRequest{
		UserID: 1, AgentDefinitionID: 7299, SessionID: "pipeline-real-auth", Input: "从飞书生成画像",
		ToolNames: []string{"ask_user_question"}, EnforceToolAllowlist: false,
	})
	require.NoError(t, err)
	require.Equal(t, TerminalWaitingForUserChoice, result.TerminalReason)
	requests := executor.snapshot()
	require.Len(t, requests, 1)
	require.Equal(t, argv, requests[0].Argv)
	actualToolCallID := requests[0].ToolCallID
	require.NotEmpty(t, actualToolCallID)

	stored, err := writer.Get(context.Background(), result.AgentRunID)
	require.NoError(t, err)
	require.Empty(t, stored.PendingQuestionJSON, "official authorization is an external action, not a model question")
	require.NotEmpty(t, stored.PendingExternalActionJSON)
	require.NotContains(t, string(stored.PendingExternalActionJSON), "live-only")
	pending, err := ParsePendingExternalAction(stored.PendingExternalActionJSON)
	require.NoError(t, err)
	require.Equal(t, operationID, pending.OperationID)
	require.Equal(t, actualToolCallID, pending.ToolCallID)

	completed := json.RawMessage(`{"ok":true,"state":"succeeded","operation_id":"op-agent2-auth","data":{"content":"完整来源"}}`)
	service := NewStudentRunService(nil, writer, skills, nil, nil, nil)
	resume, err := service.buildExternalResumeRequest(context.Background(), stored, ExternalToolResult{
		RunID: stored.ID, OperationID: operationID, ToolCallID: actualToolCallID, Result: completed,
	})
	require.NoError(t, err)
	require.True(t, resume.ContinueWithoutUserInput)
	require.Equal(t, stored.ID, resume.ExistingRunID)
	require.Empty(t, resume.Input)
	require.NotEmpty(t, resume.ExternalContinuationResult)
	require.GreaterOrEqual(t, len(resume.History), 2)
	assistantCall := resume.History[len(resume.History)-2]
	toolResult := resume.History[len(resume.History)-1]
	require.Equal(t, schema.Assistant, assistantCall.Role)
	require.Len(t, assistantCall.ToolCalls, 1)
	require.Equal(t, actualToolCallID, assistantCall.ToolCalls[0].ID)
	require.Equal(t, "lark_execute", assistantCall.ToolCalls[0].Function.Name)
	require.Equal(t, schema.Tool, toolResult.Role)
	require.Equal(t, actualToolCallID, toolResult.ToolCallID)
	require.JSONEq(t, string(completed), toolResult.Content)
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
		items = append(items, map[string]any{
			"id": 1001, "xhs_note_id": id, "collected_at": "2026-07-20T08:00:00+08:00", "note_type": model.XhsNoteTypeNormal,
			"title": "标题 " + id, "content": "正文", "video_transcript": nil,
			"like_count": 1200, "collect_count": 300, "comment_count": 88, "comment_texts": []string{},
			"note_url": "https://xhs.example/" + id,
		})
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
		row := completeAgent1Record(id)
		values := make([]any, 0, len(agent1FieldNames()))
		for _, field := range agent1FieldNames() {
			values = append(values, row[field])
		}
		rows = append(rows, values)
	}
	payload := jsonString(map[string]any{"fields": agent1FieldNames(), "rows": rows})
	return []string{"base", "+record-batch-create", "--base-token", pipelineBaseToken, "--table-id", pipelineTableID, "--json", payload}
}

func baseUpsertArgv(id, recordID string) []string {
	payload := jsonString(completeAgent1Record(id))
	return []string{"base", "+record-upsert", "--base-token", pipelineBaseToken, "--table-id", pipelineTableID, "--record-id", recordID, "--json", payload}
}

func agent1FieldNames() []string {
	return []string{
		"小红书笔记ID", "有数笔记ID", "笔记类型", "笔记标题", "笔记正文", "视频文字稿",
		"点赞数", "收藏数", "评论数", "评论区文本", "原文链接", "采集时间", "达标判定", "判定说明",
		"原生赛道", "适配赛道", "人群画像", "标题钩子", "开头留人", "正文结构", "情绪调动点", "结尾CTA",
		"主语身份", "六大类标签", "子类标签", "能否跨赛道", "跨赛道理由", "可借鉴部分", "不可照搬部分",
		"推导链", "分析状态", "分析完成时间", "分析规则版本", "有数契约版本",
	}
}

func completeAgent1Record(id string) map[string]any {
	return map[string]any{
		"小红书笔记ID": id, "有数笔记ID": 1001, "笔记类型": "图文", "笔记标题": "标题 " + id,
		"笔记正文": "正文", "视频文字稿": "信息不足", "点赞数": 1200, "收藏数": 300, "评论数": 88,
		"评论区文本": "信息不足", "原文链接": "https://xhs.example/" + id, "采集时间": "2026-07-20T08:00:00+08:00",
		"达标判定": "达标", "判定说明": "结构完整且互动动机明确", "原生赛道": "知识服务",
		"适配赛道": []string{"求职"}, "人群画像": "处于转型期的服务型创业者", "标题钩子": "反常识结论",
		"开头留人": "先指出常见误区", "正文结构": "问题—原因—方法", "情绪调动点": "焦虑转为确定感",
		"结尾CTA": "邀请讨论", "主语身份": "自己", "六大类标签": []string{"人设型"},
		"子类标签": []string{"人设型-信念"}, "能否跨赛道": "能", "跨赛道理由": "冲突结构不依赖行业事实",
		"可借鉴部分": "反常识标题与三段论结构", "不可照搬部分": "原作者人物与行业数据",
		"推导链": "内容证据→结构拆解→跨赛道边界", "分析状态": "已完成",
		"分析完成时间": "2026-07-20T08:30:00+08:00", "分析规则版本": "prompt-1/sha256:test-v1",
		"有数契约版本": "xhs-viral-base/v1",
	}
}

func validateCompleteAgent1Write(argv []string) error {
	payload, ok := argvValue(argv, "--json")
	if !ok {
		return fmt.Errorf("Agent 1 write is missing --json")
	}
	wantFields := agent1FieldNames()
	if argv[1] == "+record-batch-create" {
		var batch struct {
			Fields []string `json:"fields"`
			Rows   [][]any  `json:"rows"`
		}
		if err := json.Unmarshal([]byte(payload), &batch); err != nil {
			return fmt.Errorf("invalid Agent 1 batch payload: %w", err)
		}
		if !equalStrings(batch.Fields, wantFields) || len(batch.Rows) == 0 {
			return fmt.Errorf("Agent 1 batch must contain the exact 34-field contract")
		}
		for _, row := range batch.Rows {
			if len(row) != len(wantFields) {
				return fmt.Errorf("Agent 1 row has %d fields, want 34", len(row))
			}
			mapped := make(map[string]any, len(wantFields))
			for index, field := range wantFields {
				mapped[field] = row[index]
			}
			if err := validateCompleteAgent1Record(mapped); err != nil {
				return err
			}
		}
		return nil
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(payload), &record); err != nil {
		return fmt.Errorf("invalid Agent 1 upsert payload: %w", err)
	}
	return validateCompleteAgent1Record(record)
}

func validateCompleteAgent1Record(record map[string]any) error {
	wantFields := agent1FieldNames()
	if len(record) != len(wantFields) {
		return fmt.Errorf("Agent 1 completed record has %d fields, want 34", len(record))
	}
	for _, field := range wantFields {
		value, ok := record[field]
		if !ok || value == nil || (fmt.Sprint(value) == "" && field != "采集时间") {
			return fmt.Errorf("Agent 1 completed record is missing %s", field)
		}
	}
	if record["分析状态"] != "已完成" || record["有数契约版本"] != "xhs-viral-base/v1" {
		return fmt.Errorf("Agent 1 completed record has invalid checkpoint fields")
	}
	return nil
}

func validateAgent1RawLineage(steps []pipelineModelStep) error {
	rawByID := make(map[string]map[string]any)
	for _, workflowStep := range steps {
		if workflowStep.Tool == "xhs_note_list" && strings.Contains(workflowStep.Args, `"projection":"full"`) {
			var output struct {
				Items []map[string]any `json:"items"`
			}
			if err := json.Unmarshal([]byte(workflowStep.Result), &output); err != nil {
				return fmt.Errorf("invalid Agent 1 full fixture: %w", err)
			}
			for _, item := range output.Items {
				id, _ := item["xhs_note_id"].(string)
				if id == "" {
					return fmt.Errorf("Agent 1 full fixture is missing xhs_note_id")
				}
				rawByID[id] = item
			}
			continue
		}
		if workflowStep.Tool != "lark_execute" {
			continue
		}
		decoded, err := decodeLarkExecuteInput(ToolInput(workflowStep.Args))
		if err != nil || len(decoded.Argv) < 2 || decoded.Argv[0] != "base" ||
			(decoded.Argv[1] != "+record-batch-create" && decoded.Argv[1] != "+record-upsert") {
			continue
		}
		records, err := agent1RecordsFromWrite(decoded.Argv)
		if err != nil {
			return err
		}
		for _, record := range records {
			id, _ := record["小红书笔记ID"].(string)
			raw, ok := rawByID[id]
			if !ok {
				return fmt.Errorf("Agent 1 write %s has no preceding full source", id)
			}
			if err := compareAgent1RawFields(record, raw); err != nil {
				return fmt.Errorf("Agent 1 write %s: %w", id, err)
			}
		}
	}
	return nil
}

func agent1RecordsFromWrite(argv []string) ([]map[string]any, error) {
	payload, ok := argvValue(argv, "--json")
	if !ok {
		return nil, fmt.Errorf("Agent 1 write is missing --json")
	}
	if argv[1] == "+record-upsert" {
		var record map[string]any
		if err := json.Unmarshal([]byte(payload), &record); err != nil {
			return nil, err
		}
		return []map[string]any{record}, nil
	}
	var batch struct {
		Fields []string `json:"fields"`
		Rows   [][]any  `json:"rows"`
	}
	if err := json.Unmarshal([]byte(payload), &batch); err != nil {
		return nil, err
	}
	records := make([]map[string]any, 0, len(batch.Rows))
	for _, row := range batch.Rows {
		if len(row) != len(batch.Fields) {
			return nil, fmt.Errorf("Agent 1 batch row width mismatch")
		}
		record := make(map[string]any, len(row))
		for index, field := range batch.Fields {
			record[field] = row[index]
		}
		records = append(records, record)
	}
	return records, nil
}

func compareAgent1RawFields(record, raw map[string]any) error {
	expected := map[string]any{
		"小红书笔记ID": raw["xhs_note_id"],
		"有数笔记ID":  raw["id"],
		"笔记类型":    agent1BaseNoteType(raw["note_type"]),
		"笔记标题":    rawOrDefault(raw["title"], "信息不足"),
		"笔记正文":    rawOrDefault(raw["content"], "信息不足"),
		"视频文字稿":   rawOrDefault(raw["video_transcript"], "信息不足"),
		"点赞数":     raw["like_count"],
		"收藏数":     raw["collect_count"],
		"评论数":     raw["comment_count"],
		"评论区文本":   commentTextsOrDefault(raw["comment_texts"]),
		"原文链接":    rawOrDefault(raw["note_url"], "未提供"),
		"采集时间":    rawOrDefault(raw["collected_at"], ""),
	}
	for field, want := range expected {
		if fmt.Sprint(record[field]) != fmt.Sprint(want) {
			return fmt.Errorf("raw field %s = %v, want source-derived %v", field, record[field], want)
		}
	}
	return nil
}

func agent1BaseNoteType(value any) string {
	switch fmt.Sprint(value) {
	case model.XhsNoteTypeNormal:
		return "图文"
	case model.XhsNoteTypeVideo:
		return "视频"
	default:
		return "信息不足"
	}
}

func rawOrDefault(value any, fallback string) any {
	if value == nil || fmt.Sprint(value) == "" {
		return fallback
	}
	return value
}

func commentTextsOrDefault(value any) string {
	items, _ := value.([]any)
	if len(items) == 0 {
		return "信息不足"
	}
	texts := make([]string, 0, len(items))
	for _, item := range items {
		texts = append(texts, fmt.Sprint(item))
	}
	return strings.Join(texts, "\n")
}

func TestAgent1RawLineageValidatorRejectsFabricatedCapturedFields(t *testing.T) {
	write := completeAgent1Record("n-lineage")
	write["点赞数"] = 9999
	steps := []pipelineModelStep{
		fullStep([]string{"n-lineage"}),
		larkStep(baseUpsertRecordArgv(write, "recLineageABC123"), `{"ok":true}`),
	}
	require.ErrorContains(t, validateAgent1RawLineage(steps), "raw field 点赞数")
}

func TestAgent1RawLineageValidatorMapsProductionNoteTypes(t *testing.T) {
	tests := []struct {
		name     string
		rawType  any
		baseType string
	}{
		{name: "normal", rawType: model.XhsNoteTypeNormal, baseType: "图文"},
		{name: "video", rawType: model.XhsNoteTypeVideo, baseType: "视频"},
		{name: "null", rawType: nil, baseType: "信息不足"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := "n-type-" + tc.name
			source := fullStep([]string{id})
			var output map[string]any
			require.NoError(t, json.Unmarshal([]byte(source.Result), &output))
			items := output["items"].([]any)
			items[0].(map[string]any)["note_type"] = tc.rawType
			source.Result = jsonString(output)
			record := completeAgent1Record(id)
			record["笔记类型"] = tc.baseType
			steps := []pipelineModelStep{
				source,
				larkStep(baseUpsertRecordArgv(record, "recTypeABC123"), `{"ok":true}`),
			}
			require.NoError(t, validateAgent1RawLineage(steps))
		})
	}
}

func baseUpsertRecordArgv(record map[string]any, recordID string) []string {
	return []string{"base", "+record-upsert", "--base-token", pipelineBaseToken, "--table-id", pipelineTableID, "--record-id", recordID, "--json", jsonString(record)}
}

func validateAgent1BaseCreate(argv []string) error {
	name, hasName := argvValue(argv, "--name")
	tableName, hasTable := argvValue(argv, "--table-name")
	rawFields, hasFields := argvValue(argv, "--fields")
	if !hasName || !hasTable || !hasFields || name != "爆款素材分析库" || tableName != "爆款素材" {
		return fmt.Errorf("Agent 1 Base target has invalid name or table")
	}
	var fields []map[string]any
	if err := json.Unmarshal([]byte(rawFields), &fields); err != nil || len(fields) != 34 {
		return fmt.Errorf("Agent 1 Base schema must contain 34 fields")
	}
	for index, fieldName := range agent1FieldNames() {
		if fields[index]["name"] != fieldName || strings.TrimSpace(fmt.Sprint(fields[index]["type"])) == "" {
			return fmt.Errorf("Agent 1 Base field %d does not match the contract", index+1)
		}
	}
	if fields[0]["type"] != "text" {
		return fmt.Errorf("Agent 1 business key must be the first text field")
	}
	return nil
}

func argvValue(argv []string, flag string) (string, bool) {
	for index := 0; index+1 < len(argv); index++ {
		if argv[index] == flag {
			return argv[index+1], true
		}
	}
	return "", false
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
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
	return driveSearchArgvWithType(title, "docx")
}

func driveSearchArgvWithType(title, docType string) []string {
	return []string{"drive", "+search", "--query", title, "--only-title", "--doc-types", docType, "--page-size", "20"}
}

func baseFieldListArgv() []string {
	return []string{"base", "+field-list", "--base-token", pipelineBaseToken, "--table-id", pipelineTableID, "--limit", "100"}
}

func baseCreateWorkspaceArgv() []string {
	return []string{"base", "+base-create", "--name", "爆款素材分析库", "--table-name", "爆款素材", "--fields", jsonString(baseFieldDefinitions())}
}

func baseFieldDefinitions() []map[string]any {
	types := map[string]string{
		"小红书笔记ID": "text", "有数笔记ID": "number", "笔记类型": "select", "笔记标题": "text", "笔记正文": "text",
		"视频文字稿": "text", "点赞数": "number", "收藏数": "number", "评论数": "number", "评论区文本": "text",
		"原文链接": "text", "采集时间": "datetime", "达标判定": "select", "判定说明": "text", "原生赛道": "text",
		"适配赛道": "select", "人群画像": "text", "标题钩子": "text", "开头留人": "text", "正文结构": "text",
		"情绪调动点": "text", "结尾CTA": "text", "主语身份": "select", "六大类标签": "select", "子类标签": "select",
		"能否跨赛道": "select", "跨赛道理由": "text", "可借鉴部分": "text", "不可照搬部分": "text", "推导链": "text",
		"分析状态": "select", "分析完成时间": "datetime", "分析规则版本": "text", "有数契约版本": "text",
	}
	multiple := map[string]bool{"适配赛道": true, "六大类标签": true, "子类标签": true}
	options := map[string][]string{
		"笔记类型":  {"图文", "视频", "信息不足"},
		"达标判定":  {"达标", "部分达标", "不达标"},
		"适配赛道":  {"房产", "留学", "移民", "保险", "求职", "雅思"},
		"主语身份":  {"自己", "客户", "第三方", "泛人称"},
		"六大类标签": {"人设型", "泛流量型", "垂直人群泛话题", "精准选题", "硬广营销类", "其他"},
		"子类标签":  {"人设型-来路", "人设型-信念", "人设型-代价", "人设型-日常", "人设型-价值观", "人设型-其他", "泛流量型-选哪个", "泛流量型-要花多少", "泛流量型-是不是现在", "泛流量型-有什么坑", "泛流量型-其他", "垂直人群泛话题", "精准选题-爆款型", "精准选题-痛点型", "精准选题-深度型", "精准选题-案例型", "精准选题-其他", "硬广营销类-产品型", "硬广营销类-好评型", "硬广营销类-其他", "其他"},
		"能否跨赛道": {"能", "部分能", "不能"},
		"分析状态":  {"已完成", "失败待重试"},
	}
	fields := make([]map[string]any, 0, len(agent1FieldNames()))
	for _, name := range agent1FieldNames() {
		field := map[string]any{"name": name, "type": types[name]}
		if multiple[name] {
			field["multiple"] = true
		}
		if values := options[name]; len(values) > 0 {
			fieldOptions := make([]map[string]string, 0, len(values))
			for _, value := range values {
				fieldOptions = append(fieldOptions, map[string]string{"name": value})
			}
			field["options"] = fieldOptions
		}
		switch name {
		case "有数笔记ID", "点赞数", "收藏数", "评论数":
			field["style"] = map[string]any{"precision": 0}
		case "原文链接":
			field["style"] = map[string]any{"url": true}
		}
		fields = append(fields, field)
	}
	return fields
}

func profileContent(customer string) string {
	return fmt.Sprintf(`%s
# 客户核心信息与人群画像卡
客户：%s
本次更新：2026-07-20T08:30:00+08:00
## 一、资料来源判断
完整
## 二、账号定位素材
完整
## 三、核心人群画像
完整
## 四、向内求素材库
完整
## 五、第三方素材说明
完整
## 六、深度看见候选点
完整
## 七、资料缺口清单
完整
%s`, profileManagedBegin, customer, profileManagedEnd)
}

func assertProfileContract(t *testing.T, content string) {
	t.Helper()
	assert.Contains(t, content, profileManagedBegin)
	assert.Contains(t, content, profileManagedEnd)
	for _, module := range []string{"资料来源判断", "账号定位素材", "核心人群画像", "向内求素材库", "第三方素材说明", "深度看见候选点", "资料缺口清单"} {
		assert.Contains(t, content, module)
	}
}

func validateProfileContent(content string) error {
	if strings.Count(content, profileManagedBegin) != 1 || strings.Count(content, profileManagedEnd) != 1 ||
		strings.Index(content, profileManagedBegin) > strings.Index(content, profileManagedEnd) {
		return fmt.Errorf("Agent 2 profile markers must be unique and paired")
	}
	for _, module := range []string{"资料来源判断", "账号定位素材", "核心人群画像", "向内求素材库", "第三方素材说明", "深度看见候选点", "资料缺口清单"} {
		if !strings.Contains(content, module) {
			return fmt.Errorf("Agent 2 profile is missing module %s", module)
		}
	}
	return nil
}

func validateTopicContent(content string) error {
	if strings.Contains(content, topicsManagedHeader) && strings.Count(content, topicsManagedHeader) != 1 {
		return fmt.Errorf("Agent 3 document header must be unique")
	}
	if strings.Count(content, "｜开始]") != strings.Count(content, "｜结束]") || strings.Count(content, "｜开始]") == 0 {
		return fmt.Errorf("Agent 3 round markers must be paired")
	}
	topics := strings.Split(content, "### 选题 ")[1:]
	if len(topics) == 0 {
		return fmt.Errorf("Agent 3 round contains no formal topics")
	}
	for _, topic := range topics {
		for _, field := range topicNineFields() {
			if topicFieldValue(topic, field) == "" {
				return fmt.Errorf("Agent 3 topic is missing %s", field)
			}
		}
		category := topicFieldValue(topic, "归属小类")
		if !validTopicCategory(category) {
			return fmt.Errorf("Agent 3 topic category is outside the formal taxonomy")
		}
		generationPath := topicFieldValue(topic, "生成路径")
		if generationPath != "向内求" && generationPath != "向外求" {
			return fmt.Errorf("Agent 3 generation path is outside the fixed enum")
		}
		referenceType := topicFieldValue(topic, "参考类型")
		if referenceType != "结构+内容" && referenceType != "仅参考结构" && referenceType != "无，独立生成" {
			return fmt.Errorf("Agent 3 reference type is outside the fixed enum")
		}
		subject := topicFieldValue(topic, "主语自检")
		if strings.HasPrefix(category, "人设型-") {
			if subject == "" || subject == "-" {
				return fmt.Errorf("Agent 3 persona topic must name the checked subject")
			}
		} else if subject != "-" {
			return fmt.Errorf("Agent 3 non-persona topic subject check must be '-'")
		}
	}
	return nil
}

func validTopicCategory(category string) bool {
	if category == "垂直人群泛话题" || category == "其他" {
		return true
	}
	allowed := map[string]map[string]bool{
		"人设型":   {"来路": true, "信念": true, "代价": true, "日常": true, "价值观": true, "其他": true},
		"泛流量型":  {"选哪个": true, "要花多少": true, "是不是现在": true, "有什么坑": true, "其他": true},
		"精准":    {"爆款型": true, "痛点型": true, "深度型": true, "案例型": true, "其他": true},
		"精准选题":  {"爆款型": true, "痛点型": true, "深度型": true, "案例型": true, "其他": true},
		"硬广":    {"产品型": true, "好评型": true, "其他": true},
		"硬广营销类": {"产品型": true, "好评型": true, "其他": true},
	}
	parts := strings.SplitN(category, "-", 2)
	return len(parts) == 2 && allowed[parts[0]][parts[1]]
}

func TestValidateTopicContentRejectsIncompleteOrUnknownTopic(t *testing.T) {
	valid := topicRoundContent(pipelineRoundNew, 1)
	tests := []struct {
		name    string
		content string
		error   string
	}{
		{name: "no topic", content: strings.ReplaceAll(valid, "### 选题 ", "### 方向 "), error: "no formal topics"},
		{name: "missing category", content: strings.Replace(valid, "- 归属小类：人设型-信念\n", "", 1), error: "missing 归属小类"},
		{name: "unknown category", content: strings.Replace(valid, "人设型-信念", "未知型-随意", 1), error: "formal taxonomy"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorContains(t, validateTopicContent(tc.content), tc.error)
		})
	}
}

func topicFieldValue(topic, field string) string {
	prefix := "- " + field + "："
	for _, line := range strings.Split(topic, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), prefix))
		}
	}
	return ""
}

func topicsDocumentContent(customer, round string) string {
	return fmt.Sprintf("%s\n# %s 选题规划\n%s", topicsManagedHeader, customer, topicRoundContent(round, 1))
}

func topicNineFields() []string {
	return []string{"选题内容", "选择原因", "归属小类", "生成路径", "推导链", "参考来源链接", "参考类型", "变形说明", "主语自检"}
}

func topicRoundContent(round string, number int) string {
	return fmt.Sprintf(`[有数AI轮次：%s｜第 %d 轮｜开始]
## 第 %d 轮｜2026-07-20｜0-1｜蓝V：否
### 选题 1
- 选题内容：创始人亲述：为什么多数咨询方案落不了地
- 选择原因：画像卡确认客户处于 0-1 阶段，适合以创始人本人建立信任
- 归属小类：人设型-信念
- 生成路径：向外求
- 推导链：客户事实 → 来源结构 → 新选题
- 参考来源链接：https://xhs.example/n1
- 参考类型：结构+内容
- 变形说明：只借结构，不借原行业事实
- 主语自检：创始人本人

### 选题 2
- 选题内容：顾问视角：服务型老板最容易忽略的一个交付节点
- 选择原因：部分达标来源仍有可迁移的反常识开头
- 归属小类：精准-痛点型
- 生成路径：向外求
- 推导链：客户交付事实 → 可借鉴：反常识开头 → 避开不可照搬：客户结果数字
- 参考来源链接：https://xhs.example/n2
- 参考类型：仅参考结构
- 变形说明：仅借局部手法；可借鉴：反常识开头；不可照搬：客户结果数字
- 主语自检：-

本轮规避重点：不使用不达标来源，不虚构客户事实。
六大类：人设型 1；精准选题 1；其他 0。
该账号非蓝 V，本轮跳过硬广营销类选题。
硬广营销类数量：0
不足70条说明：当前仅有 2 条满足来源、画像、主语与合规约束的正式选题，不以不达标或缺字段素材凑数。
待确认清单：补充更多达标来源后扩充选题。
[有数AI轮次：%s｜第 %d 轮｜结束]`, round, number, number, round, number)
}

func agent1SourceFixture() string {
	n1 := completeAgent1Record("n1")
	n2 := completeAgent1Record("n2")
	n2["达标判定"] = "部分达标"
	n2["能否跨赛道"] = "部分能"
	n2["可借鉴部分"] = "反常识开头"
	n2["不可照搬部分"] = "客户结果数字"
	n3 := completeAgent1Record("不达标来源-n3")
	n3["达标判定"] = "不达标"
	return jsonString(map[string]any{"records": []any{n1, n2, n3}})
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
