package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/feishu"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

type fakeSkillReadExecutor struct {
	mu       sync.Mutex
	requests []feishu.SkillReadRequest
	result   *feishu.SkillReadPage
	err      error
}

type scriptedSkillReadStep struct {
	result *feishu.SkillReadPage
	err    error
}

type scriptedSkillReadExecutor struct {
	mu       sync.Mutex
	requests []feishu.SkillReadRequest
	steps    []scriptedSkillReadStep
}

func (f *scriptedSkillReadExecutor) Read(_ context.Context, request feishu.SkillReadRequest) (*feishu.SkillReadPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	callIndex := len(f.requests)
	f.requests = append(f.requests, request)
	if callIndex >= len(f.steps) {
		return nil, feishu.ErrSkillReadFailed
	}
	step := f.steps[callIndex]
	if step.result == nil {
		return nil, step.err
	}
	clone := *step.result
	clone.References = append([]string(nil), step.result.References...)
	return &clone, step.err
}

func (f *scriptedSkillReadExecutor) snapshot() []feishu.SkillReadRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]feishu.SkillReadRequest(nil), f.requests...)
}

func (f *fakeSkillReadExecutor) Read(_ context.Context, request feishu.SkillReadRequest) (*feishu.SkillReadPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request)
	if f.result == nil {
		return nil, f.err
	}
	clone := *f.result
	clone.References = append([]string(nil), f.result.References...)
	return &clone, f.err
}

func (f *fakeSkillReadExecutor) snapshot() []feishu.SkillReadRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]feishu.SkillReadRequest(nil), f.requests...)
}

type fakeLarkExecutor struct {
	mu       sync.Mutex
	requests []feishu.ExecuteRequest
	result   *feishu.OperationResult
	err      error
}

type fakeLarkInspector struct {
	mu       sync.Mutex
	requests []feishu.InspectionRequest
	result   *feishu.InspectionResult
	err      error
}

func (f *fakeLarkInspector) Inspect(_ context.Context, request feishu.InspectionRequest) (*feishu.InspectionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	request.Argv = append([]string(nil), request.Argv...)
	request.SkillReceipts = append([]string(nil), request.SkillReceipts...)
	f.requests = append(f.requests, request)
	if f.result == nil {
		return nil, f.err
	}
	clone := *f.result
	clone.GrantedScopes = append([]string(nil), f.result.GrantedScopes...)
	clone.MissingScopes = append([]string(nil), f.result.MissingScopes...)
	clone.Capabilities = make(map[string]string, len(f.result.Capabilities))
	for key, value := range f.result.Capabilities {
		clone.Capabilities[key] = value
	}
	return &clone, f.err
}

func (f *fakeLarkInspector) snapshot() []feishu.InspectionRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]feishu.InspectionRequest(nil), f.requests...)
}

type gatedRejectedLarkExecutor struct {
	mu        sync.Mutex
	calls     int
	gateAfter int
	entered   chan struct{}
	release   chan struct{}
}

type gatedSuccessLarkExecutor struct {
	mu      sync.Mutex
	calls   int
	entered chan struct{}
	release chan struct{}
}

func (f *gatedSuccessLarkExecutor) Execute(_ context.Context, _ feishu.ExecuteRequest) (*feishu.OperationResult, error) {
	f.mu.Lock()
	f.calls++
	callNumber := f.calls
	f.mu.Unlock()
	if callNumber == 1 {
		close(f.entered)
		<-f.release
	}
	return &feishu.OperationResult{
		OperationID: fmt.Sprintf("op-%d", callNumber),
		State:       model.FeishuOperationSucceeded,
		Data:        json.RawMessage(`{"ok":true}`),
	}, nil
}

func (f *gatedSuccessLarkExecutor) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *gatedRejectedLarkExecutor) Execute(_ context.Context, _ feishu.ExecuteRequest) (*feishu.OperationResult, error) {
	f.mu.Lock()
	f.calls++
	callNumber := f.calls
	f.mu.Unlock()
	if callNumber > f.gateAfter {
		f.entered <- struct{}{}
		<-f.release
	}
	return nil, feishu.ErrOperationRequestRejected
}

func (f *gatedRejectedLarkExecutor) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeLarkExecutor) Execute(_ context.Context, request feishu.ExecuteRequest) (*feishu.OperationResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	request.Argv = append([]string(nil), request.Argv...)
	request.StdinJSON = append(json.RawMessage(nil), request.StdinJSON...)
	request.SkillReceipts = append([]string(nil), request.SkillReceipts...)
	f.requests = append(f.requests, request)
	if f.result == nil {
		return nil, f.err
	}
	clone := *f.result
	clone.Data = append(json.RawMessage(nil), f.result.Data...)
	if f.result.Action != nil {
		action := *f.result.Action
		action.Scopes = append([]string(nil), f.result.Action.Scopes...)
		clone.Action = &action
	}
	return &clone, f.err
}

func (f *fakeLarkExecutor) snapshot() []feishu.ExecuteRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]feishu.ExecuteRequest, len(f.requests))
	copy(out, f.requests)
	return out
}

func larkPersonalWorkspaceContext(userID uint, runID uint64, toolCallID string) context.Context {
	ctx := middleware.NewContextWithUserID(context.Background(), userID)
	ctx = WithRunID(ctx, runID)
	return WithToolCallID(ctx, toolCallID)
}

func requireSafeLarkSoftError(t *testing.T, result ToolResult, err error, forbidden ...string) {
	t.Helper()
	require.NoError(t, err)
	require.NotEmpty(t, result)
	assert.Contains(t, string(result), "ERROR")
	for _, value := range forbidden {
		if value != "" {
			assert.NotContains(t, string(result), value)
		}
	}
}

func TestLarkPersonalWorkspace_SkillReadSuccessUsesOnlyRunContext(t *testing.T) {
	executor := &fakeSkillReadExecutor{result: &feishu.SkillReadPage{
		Skill:      "lark-doc",
		Path:       "references/docs.md",
		Content:    "line 1\nquoted: \"safe\"",
		References: []string{"references/docs.md"},
		Receipt:    "opaque-receipt",
	}}
	tool := &larkSkillReadTool{executor: executor}

	result, err := tool.Execute(WithRunID(context.Background(), 41), ToolInput(`{"skill":"lark-doc","reference":"references/docs.md","cursor":"cursor-1"}`))
	require.NoError(t, err)
	requests := executor.snapshot()
	require.Len(t, requests, 1)
	assert.Equal(t, feishu.SkillReadRequest{
		AgentRunID: 41,
		Skill:      "lark-doc",
		Reference:  "references/docs.md",
		Cursor:     "cursor-1",
	}, requests[0], "skill reads must not require or derive a user connection")

	var output struct {
		OK         bool     `json:"ok"`
		Skill      string   `json:"skill"`
		Path       string   `json:"path"`
		Content    string   `json:"content"`
		References []string `json:"references"`
		CLIVersion string   `json:"cli_version"`
	}
	require.NoError(t, json.Unmarshal(result, &output))
	assert.True(t, output.OK)
	assert.Equal(t, executor.result.Skill, output.Skill)
	assert.Equal(t, executor.result.Path, output.Path)
	assert.Equal(t, executor.result.Content, output.Content, "raw content must JSON round-trip without corruption")
	assert.Equal(t, executor.result.References, output.References)
	assert.Equal(t, "1.0.68", output.CLIVersion)
	assert.NotContains(t, string(result), `"cursor"`, "platform-owned pagination must not be model-visible")
	assert.NotContains(t, string(result), `"receipt"`, "internal receipts must not be model-visible")
	assert.True(t, tool.IsReadOnly())
	assert.True(t, tool.IsConcurrencySafe(nil))
	assert.Contains(t, tool.Description(), "lark-shared")
	assert.Contains(t, tool.Description(), "controlled reference")
	assert.Contains(t, tool.Description(), "platform handles pagination")
}

// Customer regression (Dev run 227): cursor is a rolling-compatibility wire
// field, not a model decision. New schemas must hide it, and a rejected skill
// input must be a recoverable correction rather than a terminal red failure.
func TestLarkPersonalWorkspace_SkillReadCursorIsCompatibilityOnlyAndInvalidIsRecoverable(t *testing.T) {
	executor := &fakeSkillReadExecutor{err: fmt.Errorf("wrapped invalid skill input: %w", feishu.ErrSkillReadInvalid)}
	tool := &larkSkillReadTool{executor: executor}

	schema := string(tool.InputSchema())
	assert.NotContains(t, schema, `"cursor"`, "new model calls must not manage internal pagination")
	assert.Contains(t, schema, `"reference"`)

	result, err := tool.Execute(
		WithRunID(context.Background(), 227),
		ToolInput(`{"skill":"lark-doc","cursor":"legacy-opaque-cursor"}`),
	)
	require.NoError(t, err)
	requests := executor.snapshot()
	require.Len(t, requests, 1, "the hidden legacy field must remain wire-compatible during rolling deployment")
	assert.Equal(t, "legacy-opaque-cursor", requests[0].Cursor)

	var output struct {
		Code        string `json:"code"`
		Recoverable bool   `json:"recoverable"`
		Retryable   bool   `json:"retryable"`
	}
	require.NoError(t, json.Unmarshal(result, &output))
	assert.Equal(t, "invalid_skill_input", output.Code)
	assert.True(t, output.Recoverable)
	assert.False(t, output.Retryable)
	assert.NotContains(t, string(result), "legacy-opaque-cursor")
}

func TestLarkPersonalWorkspace_SkillReadDependencyFailureRemainsTerminal(t *testing.T) {
	result, err := (&larkSkillReadTool{executor: &fakeSkillReadExecutor{err: feishu.ErrSkillReadFailed}}).Execute(
		WithRunID(context.Background(), 228),
		ToolInput(`{"skill":"lark-doc"}`),
	)
	require.NoError(t, err)

	var output struct {
		Code        string `json:"code"`
		Recoverable bool   `json:"recoverable"`
	}
	require.NoError(t, json.Unmarshal(result, &output))
	assert.Equal(t, "skill_read_unavailable", output.Code)
	assert.False(t, output.Recoverable)
}

func TestLarkPersonalWorkspace_SkillReadAggregatesPagesInternally(t *testing.T) {
	references := []string{"references/lark-doc-fetch.md"}
	executor := &scriptedSkillReadExecutor{steps: []scriptedSkillReadStep{
		{result: &feishu.SkillReadPage{
			Skill: "lark-doc", Path: "references/lark-doc-fetch.md", Content: "first-",
			References: references, Cursor: "opaque-next", Receipt: "must-not-leak-first",
		}},
		{result: &feishu.SkillReadPage{
			Skill: "lark-doc", Path: "references/lark-doc-fetch.md", Content: "second",
			References: references, Receipt: "must-not-leak-second",
		}},
	}}

	result, err := (&larkSkillReadTool{executor: executor}).Execute(
		WithRunID(context.Background(), 227),
		ToolInput(`{"skill":"lark-doc","cursor":"references/lark-doc-fetch.md"}`),
	)
	require.NoError(t, err)
	assert.NotContains(t, string(result), "ERROR")
	assert.NotContains(t, string(result), `"cursor"`)
	assert.NotContains(t, string(result), `"receipt"`)
	assert.NotContains(t, string(result), "must-not-leak")

	var output struct {
		Content    string   `json:"content"`
		Path       string   `json:"path"`
		References []string `json:"references"`
	}
	require.NoError(t, json.Unmarshal(result, &output))
	assert.Equal(t, "first-second", output.Content)
	assert.Equal(t, "references/lark-doc-fetch.md", output.Path)
	assert.Equal(t, references, output.References)

	requests := executor.snapshot()
	require.Len(t, requests, 2)
	assert.Equal(t, feishu.SkillReadRequest{
		AgentRunID: 227,
		Skill:      "lark-doc",
		Cursor:     "references/lark-doc-fetch.md",
	}, requests[0])
	assert.Equal(t, feishu.SkillReadRequest{
		AgentRunID: 227,
		Skill:      "lark-doc",
		Reference:  "references/lark-doc-fetch.md",
		Cursor:     "opaque-next",
	}, requests[1], "the trusted canonical path must bind the internal continuation")
}

func TestLarkPersonalWorkspace_SkillReadPaginationFailsClosedWithoutPartialContent(t *testing.T) {
	base := &feishu.SkillReadPage{
		Skill:      "lark-doc",
		Path:       "references/lark-doc-fetch.md",
		Content:    "partial-secret",
		References: []string{"references/lark-doc-fetch.md"},
		Cursor:     "cursor-one",
	}
	clonePage := func() *feishu.SkillReadPage {
		clone := *base
		clone.References = append([]string(nil), base.References...)
		return &clone
	}

	for name, steps := range map[string][]scriptedSkillReadStep{
		"repeated cursor": {
			{result: clonePage()},
			{result: clonePage()},
		},
		"third page required": {
			{result: clonePage()},
			{result: func() *feishu.SkillReadPage { page := clonePage(); page.Cursor = "cursor-two"; return page }()},
			{result: func() *feishu.SkillReadPage { page := clonePage(); page.Cursor = ""; return page }()},
		},
		"second page invalid": {
			{result: clonePage()},
			{err: feishu.ErrSkillReadInvalid},
		},
		"skill drift": {
			{result: clonePage()},
			{result: func() *feishu.SkillReadPage {
				page := clonePage()
				page.Skill = "lark-base"
				page.Cursor = ""
				return page
			}()},
		},
		"path drift": {
			{result: clonePage()},
			{result: func() *feishu.SkillReadPage {
				page := clonePage()
				page.Path = "references/other.md"
				page.Cursor = ""
				return page
			}()},
		},
		"references drift": {
			{result: clonePage()},
			{result: func() *feishu.SkillReadPage {
				page := clonePage()
				page.References = []string{"references/other.md"}
				page.Cursor = ""
				return page
			}()},
		},
	} {
		name, steps := name, steps
		t.Run(name, func(t *testing.T) {
			executor := &scriptedSkillReadExecutor{steps: steps}
			result, err := (&larkSkillReadTool{executor: executor}).Execute(
				WithRunID(context.Background(), 235),
				ToolInput(`{"skill":"lark-doc","reference":"references/lark-doc-fetch.md"}`),
			)
			requireSafeLarkSoftError(t, result, err, "partial-secret", "cursor-one", "cursor-two")

			var output struct {
				Code        string `json:"code"`
				Recoverable bool   `json:"recoverable"`
			}
			require.NoError(t, json.Unmarshal(result, &output))
			assert.Equal(t, "skill_read_unavailable", output.Code)
			assert.False(t, output.Recoverable)
			assert.LessOrEqual(t, len(executor.snapshot()), larkSkillReadMaxPages)
		})
	}
}

func TestLarkPersonalWorkspace_SkillReadRejectsOversizedEscapedEnvelope(t *testing.T) {
	content := strings.Repeat("\\\"", 24*1024)
	executor := &fakeSkillReadExecutor{result: &feishu.SkillReadPage{
		Skill: "lark-doc", Path: "SKILL.md", Content: content,
	}}

	result, err := (&larkSkillReadTool{executor: executor}).Execute(
		WithRunID(context.Background(), 236),
		ToolInput(`{"skill":"lark-doc"}`),
	)
	requireSafeLarkSoftError(t, result, err, content[:1024])
	assert.Contains(t, string(result), `"code":"skill_read_unavailable"`)
	assert.NotContains(t, string(result), `"cursor"`)
	assert.Len(t, executor.snapshot(), 1)
}

func TestLarkPersonalWorkspace_SkillReadRejectsOversizedReferencesEnvelope(t *testing.T) {
	content := "tiny-content-must-not-leak"
	reference := "reference-must-not-leak-" + strings.Repeat("R", larkSkillReadAtomicOutputLimit)
	executor := &fakeSkillReadExecutor{result: &feishu.SkillReadPage{
		Skill: "lark-doc", Path: "SKILL.md", Content: content,
		References: []string{reference},
	}}

	result, err := (&larkSkillReadTool{executor: executor}).Execute(
		WithRunID(context.Background(), 237),
		ToolInput(`{"skill":"lark-doc"}`),
	)
	requireSafeLarkSoftError(t, result, err, content, "reference-must-not-leak", reference[:1024])
	assert.Contains(t, string(result), `"code":"skill_read_unavailable"`)
	assert.NotContains(t, string(result), `"cursor"`)
	assert.Len(t, executor.snapshot(), 1)
}

// Customer regression (Dev run 204): upstream skills describe a local CLI and
// told the hosted Agent to run auth/config preflights and request App secrets.
// The hosted boundary must explicitly override those instructions before the
// model chooses a command, while leaving the signed skill content untouched.
func TestLarkPersonalWorkspace_SkillReadPublishesHostedPolicy(t *testing.T) {
	executor := &fakeSkillReadExecutor{result: &feishu.SkillReadPage{
		Skill:   "lark-doc",
		Path:    "skills/lark-doc/SKILL.md",
		Content: "首次使用前执行 lark-cli auth login，并默认使用 --as user。",
		Receipt: "opaque-doc-receipt",
	}}

	result, err := (&larkSkillReadTool{executor: executor}).Execute(
		WithRunID(context.Background(), 204),
		ToolInput(`{"skill":"lark-doc"}`),
	)
	require.NoError(t, err)

	var output struct {
		Content      string `json:"content"`
		HostedPolicy string `json:"hosted_policy"`
	}
	require.NoError(t, json.Unmarshal(result, &output))
	assert.Equal(t, executor.result.Content, output.Content, "signed upstream content must stay byte-for-byte intact")
	assert.NotContains(t, string(result), `"receipt"`)
	assert.Contains(t, output.HostedPolicy, "不要执行 auth/config/whoami")
	assert.Contains(t, output.HostedPolicy, "不要要求用户提供 App ID/App Secret")
	assert.Contains(t, output.HostedPolicy, "不要每次先检查权限")
	assert.Contains(t, output.HostedPolicy, "只读 scope check")
	assert.Contains(t, output.HostedPolicy, "lark_inspect")
	assert.Contains(t, output.HostedPolicy, "生成授权卡片")
	assert.Contains(t, output.HostedPolicy, "技能读取只提供命令说明")
	assert.Contains(t, output.HostedPolicy, "身份、权限与恢复由平台负责")
	assert.NotContains(t, output.HostedPolicy, "skill_receipts")
	assert.Contains(t, output.HostedPolicy, "最多允许 5 次总尝试")
	assert.Contains(t, output.HostedPolicy, "不能原样重复")
	assert.Contains(t, output.HostedPolicy, "unknown_result 只禁止原样重复那一条结果不确定的写命令")
	assert.Contains(t, output.HostedPolicy, "一次核验失败可改用其他合规只读方式")
	assert.Contains(t, output.HostedPolicy, "不得阻止用户要求中的其他 Docs/Base/Wiki/Drive 操作")
}

// Customer regression (Dev run 211): a fresh conversation only had a document
// title. The Agent correctly tried to load lark-drive and search by title, but
// the hosted schema rejected that skill before any Feishu operation existed.
func TestLarkPersonalWorkspace_FreshConversationCanDiscoverByTitle(t *testing.T) {
	tool := &larkSkillReadTool{executor: &fakeSkillReadExecutor{result: &feishu.SkillReadPage{
		Skill: "lark-drive", Path: "skills/lark-drive/SKILL.md", Receipt: "opaque-drive-receipt",
	}}}

	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(tool.InputSchema(), &schema))
	assert.Contains(t, schema.Properties["skill"].Enum, "lark-drive")
	assert.Contains(t, tool.Description(), "lark-drive")
	assert.Contains(t, (&larkExecuteTool{}).Description(), "Drive")

	result, err := tool.Execute(WithRunID(context.Background(), 211), ToolInput(`{"skill":"lark-drive"}`))
	require.NoError(t, err)
	var output struct {
		HostedPolicy string `json:"hosted_policy"`
	}
	require.NoError(t, json.Unmarshal(result, &output))
	assert.Contains(t, output.HostedPolicy, "drive +search")
	assert.Contains(t, output.HostedPolicy, "唯一精确匹配")
	assert.Contains(t, output.HostedPolicy, "多个精确匹配")
	assert.Contains(t, output.HostedPolicy, "没有精确匹配")
	assert.Contains(t, output.HostedPolicy, "title_highlighted")
	assert.Contains(t, output.HostedPolicy, "剥离 <h>/<hb>")
	assert.Contains(t, output.HostedPolicy, "最多 5 页或 100 条")
	assert.Contains(t, output.HostedPolicy, "达到上限仍有更多结果")
	assert.Contains(t, output.HostedPolicy, "wiki +node-get")
	assert.Contains(t, output.HostedPolicy, "obj_type/obj_token")
	assert.Contains(t, output.HostedPolicy, "doc、sheet、mindnote、slides、file")
	assert.Contains(t, output.HostedPolicy, "docx 用 lark-doc")
	assert.Contains(t, output.HostedPolicy, "bitable 用 lark-base")
	assert.NotContains(t, output.HostedPolicy, "receipt")
}

// Customer regression (Dev runs 245-246): an upstream skill documented a
// command outside the hosted catalog, while ambiguous search choices exposed
// only token suffixes. The model then guessed an invalid URL and retried the
// rejected command concurrently.
func TestLarkPersonalWorkspace_BaseSkillPublishesStableHostedContract(t *testing.T) {
	tool := &larkSkillReadTool{executor: &fakeSkillReadExecutor{result: &feishu.SkillReadPage{
		Skill: "lark-base", Path: "skills/lark-base/SKILL.md",
	}}}

	result, err := tool.Execute(WithRunID(context.Background(), 246), ToolInput(`{"skill":"lark-base"}`))
	require.NoError(t, err)
	var output struct {
		HostedPolicy string `json:"hosted_policy"`
	}
	require.NoError(t, json.Unmarshal(result, &output))

	assert.Contains(t, output.HostedPolicy, "精确清单")
	assert.Contains(t, output.HostedPolicy, "base +url-resolve")
	assert.Contains(t, output.HostedPolicy, "完整原始 URL")
	assert.Contains(t, output.HostedPolicy, "不得只展示 token 后缀")
	assert.Contains(t, output.HostedPolicy, "重新执行相同的受限搜索")
	assert.Contains(t, output.HostedPolicy, "lark_execute 必须串行")
	assert.NotContains(t, output.HostedPolicy, "base +record-delete")
}

func TestLarkPersonalWorkspace_SkillReadStrictInputAndSafeFailures(t *testing.T) {
	for name, input := range map[string]string{
		"user identity":     `{"skill":"lark-doc","user_id":9}`,
		"run identity":      `{"skill":"lark-doc","run_id":9}`,
		"tool identity":     `{"skill":"lark-doc","tool_call_id":"tc"}`,
		"raw path":          `{"skill":"lark-doc","path":"/tmp/secret"}`,
		"exact duplicate":   `{"skill":"lark-doc","skill":"lark-wiki"}`,
		"case variant":      `{"Skill":"lark-doc"}`,
		"trailing document": `{"skill":"lark-doc"} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			executor := &fakeSkillReadExecutor{}
			tool := &larkSkillReadTool{executor: executor}
			result, err := tool.Execute(WithRunID(context.Background(), 11), ToolInput(input))
			requireSafeLarkSoftError(t, result, err, "/tmp/secret")
			assert.Empty(t, executor.snapshot())
		})
	}

	internalErr := errors.New("reader failed at /private/home with embedded secret-content")
	for name, testCase := range map[string]struct {
		ctx      context.Context
		executor SkillReadExecutor
	}{
		"nil executor": {ctx: WithRunID(context.Background(), 11)},
		"nil context":  {ctx: nil, executor: &fakeSkillReadExecutor{}},
		"missing run":  {ctx: context.Background(), executor: &fakeSkillReadExecutor{}},
		"reader error": {ctx: WithRunID(context.Background(), 11), executor: &fakeSkillReadExecutor{err: internalErr}},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := (&larkSkillReadTool{executor: testCase.executor}).Execute(testCase.ctx, ToolInput(`{"skill":"lark-doc"}`))
			requireSafeLarkSoftError(t, result, err, internalErr.Error(), "/private/home", "secret-content")
		})
	}
}

func TestLarkPersonalWorkspace_SkillReadOptionalStringsRejectNonStrings(t *testing.T) {
	invalidValues := map[string]string{
		"null":   "null",
		"object": `{}`,
		"array":  `[]`,
		"number": `42`,
		"bool":   `true`,
	}
	for _, field := range []string{"reference", "cursor"} {
		for valueName, value := range invalidValues {
			t.Run(field+"_"+valueName, func(t *testing.T) {
				executor := &fakeSkillReadExecutor{result: &feishu.SkillReadPage{Skill: "lark-doc"}}
				input := ToolInput(`{"skill":"lark-doc","` + field + `":` + value + `}`)
				result, err := (&larkSkillReadTool{executor: executor}).Execute(WithRunID(context.Background(), 12), input)
				requireSafeLarkSoftError(t, result, err)
				assert.Empty(t, executor.snapshot(), "non-string optional input must not reach the skill reader")
			})
		}
	}

	executor := &fakeSkillReadExecutor{result: &feishu.SkillReadPage{Skill: "lark-doc"}}
	result, err := (&larkSkillReadTool{executor: executor}).Execute(
		WithRunID(context.Background(), 13),
		ToolInput(`{"skill":"lark-doc","reference":"","cursor":""}`),
	)
	require.NoError(t, err)
	assert.NotContains(t, string(result), "ERROR")
	requests := executor.snapshot()
	require.Len(t, requests, 1)
	assert.Empty(t, requests[0].Reference)
	assert.Empty(t, requests[0].Cursor)
}

func TestLarkPersonalWorkspace_ExecuteDerivesTenantAndIdempotencyFromContext(t *testing.T) {
	executor := &fakeLarkExecutor{result: &feishu.OperationResult{
		OperationID: "op-success",
		State:       model.FeishuOperationSucceeded,
		Data:        json.RawMessage(`{"document":{"id":"doc-1"},"count":2}`),
	}}
	tool := &larkExecuteTool{executor: executor}
	input := ToolInput(`{"argv":["docs","+fetch","--doc","doc-1"],"stdin_json":{"value":"line\nquoted: \"yes\""},"skill_receipts":["receipt-doc","receipt-shared"]}`)

	first, err := tool.Execute(larkPersonalWorkspaceContext(21, 501, "synthetic-a"), input)
	require.NoError(t, err)
	second, err := tool.Execute(larkPersonalWorkspaceContext(22, 502, "synthetic-b"), ToolInput(`{"argv":["wiki","+get","--node","n1"],"stdin_json":null,"skill_receipts":["receipt-wiki"]}`))
	require.NoError(t, err)

	requests := executor.snapshot()
	require.Len(t, requests, 2)
	assert.Equal(t, uint(21), requests[0].UserID)
	assert.Equal(t, uint64(501), requests[0].AgentRunID)
	assert.Equal(t, "synthetic-a", requests[0].ToolCallID)
	assert.Equal(t, "501:synthetic-a", requests[0].IdempotencyKey)
	assert.Equal(t, []string{"docs", "+fetch", "--doc", "doc-1"}, requests[0].Argv)
	assert.JSONEq(t, `{"value":"line\nquoted: \"yes\""}`, string(requests[0].StdinJSON))
	assert.Empty(t, requests[0].SkillReceipts, "receipts must not cross the Agent execution boundary")
	assert.Equal(t, uint(22), requests[1].UserID, "a second tenant must derive its own identity from context")
	assert.Equal(t, "502:synthetic-b", requests[1].IdempotencyKey)
	assert.Nil(t, requests[1].StdinJSON, "stdin_json:null must normalize to nil")

	var output struct {
		OK          bool            `json:"ok"`
		State       string          `json:"state"`
		OperationID string          `json:"operation_id"`
		Data        json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(first, &output))
	assert.True(t, output.OK)
	assert.Equal(t, model.FeishuOperationSucceeded, output.State)
	assert.Equal(t, "op-success", output.OperationID)
	assert.JSONEq(t, `{"document":{"id":"doc-1"},"count":2}`, string(output.Data), "data must remain JSON, not base64")
	assert.NotContains(t, string(first), "eyJ", "data must not be base64 encoded")
	assert.False(t, tool.IsReadOnly())
	assert.False(t, tool.IsConcurrencySafe(nil))
	assert.Contains(t, tool.Description(), "Docs/Base/Wiki")
	assert.Contains(t, tool.Description(), "leading `lark-cli`")
	assert.Contains(t, tool.Description(), "no shell")
	assert.Contains(t, tool.Description(), "no IM")
	_ = second
}

// Customer regression (Dev run 226): lark_skill_read succeeded, then the Agent
// copied the long model-visible receipt incorrectly twice. Both lark_execute
// calls were rejected before an operation existed, even though the business argv
// was valid. Receipts are not user auth or command policy and must not be carried
// by the model; the trusted platform derives those controls from context+catalog.
func TestLarkExecuteDoesNotRequireModelCarriedReceipts(t *testing.T) {
	executor := &fakeLarkExecutor{result: &feishu.OperationResult{
		OperationID: "op-search",
		State:       model.FeishuOperationSucceeded,
		Data:        json.RawMessage(`{"items":[]}`),
	}}
	tool := &larkExecuteTool{executor: executor}
	argv := `["drive","+search","--query","有数飞书二次连接测试","--only-title","--doc-types","docx,wiki,bitable"]`

	withoutReceipt, err := tool.Execute(
		larkPersonalWorkspaceContext(21, 226, "search-without-receipt"),
		ToolInput(`{"argv":`+argv+`}`),
	)
	require.NoError(t, err)
	assert.NotContains(t, string(withoutReceipt), "ERROR")

	legacyMalformedReceipt, err := tool.Execute(
		larkPersonalWorkspaceContext(21, 227, "search-legacy-receipt"),
		ToolInput(`{"argv":`+argv+`,"skill_receipts":{"copied":"incorrectly"}}`),
	)
	require.NoError(t, err)
	assert.NotContains(t, string(legacyMalformedReceipt), "ERROR")

	requests := executor.snapshot()
	require.Len(t, requests, 2, "both valid business commands must reach the trusted executor")
	assert.Equal(t, requests[0].Argv, requests[1].Argv)
	assert.Empty(t, requests[0].SkillReceipts)
	assert.Empty(t, requests[1].SkillReceipts, "legacy receipt values are compatibility-only")

	schema := string(tool.InputSchema())
	assert.NotContains(t, schema, "skill_receipts", "new model calls must not see an internal receipt field")
	assert.Contains(t, schema, `"required":["argv"]`)

	injected, err := tool.Execute(
		larkPersonalWorkspaceContext(21, 228, "identity-injection"),
		ToolInput(`{"argv":["docs","+fetch","--doc","doc-1"],"user_id":99}`),
	)
	requireSafeLarkSoftError(t, injected, err)
	assert.Len(t, executor.snapshot(), 2, "identity injection must still fail before the executor")
}

// Customer regression (Dev run 199): the official embedded lark skills show
// complete commands beginning with `lark-cli`, so the model naturally preserves
// that executable token in argv. The hosted tool boundary must accept exactly
// that pinned prefix and pass only the command argv to the controlled catalog.
func TestLarkPersonalWorkspace_ExecuteAcceptsOfficialLarkCLIPrefix(t *testing.T) {
	executor := &fakeLarkExecutor{result: &feishu.OperationResult{
		OperationID: "op-create",
		State:       model.FeishuOperationSucceeded,
		Data:        json.RawMessage(`{"document_id":"doc-created"}`),
	}}
	tool := &larkExecuteTool{executor: executor}
	input := ToolInput(`{"argv":["lark-cli","docs","+create","--content","<title>验收</title>"],"skill_receipts":["receipt-doc","receipt-shared"]}`)

	result, err := tool.Execute(larkPersonalWorkspaceContext(21, 199, "dev-run-199"), input)
	require.NoError(t, err)
	assert.NotContains(t, string(result), "ERROR")

	requests := executor.snapshot()
	require.Len(t, requests, 1)
	assert.Equal(t,
		[]string{"docs", "+create", "--content", "<title>验收</title>"},
		requests[0].Argv,
		"the executable belongs to the tool boundary, not the controlled command catalog",
	)
}

func TestLarkPersonalWorkspace_ExecuteRejectsUntrustedIdentityAndStrictJSON(t *testing.T) {
	nextRunID := uint64(7000)
	for name, input := range map[string]string{
		"user id":           `{"argv":["docs"],"skill_receipts":["r"],"user_id":99}`,
		"run id":            `{"argv":["docs"],"skill_receipts":["r"],"run_id":99}`,
		"tool call id":      `{"argv":["docs"],"skill_receipts":["r"],"tool_call_id":"evil"}`,
		"idempotency":       `{"argv":["docs"],"skill_receipts":["r"],"idempotency_key":"evil"}`,
		"scopes":            `{"argv":["docs"],"skill_receipts":["r"],"scopes":["admin"]}`,
		"auth":              `{"argv":["docs"],"skill_receipts":["r"],"auth":{"token":"secret"}}`,
		"risk":              `{"argv":["docs"],"skill_receipts":["r"],"risk":"safe"}`,
		"domain":            `{"argv":["docs"],"skill_receipts":["r"],"domain":"im"}`,
		"home":              `{"argv":["docs"],"skill_receipts":["r"],"home":"/tmp/x"}`,
		"profile":           `{"argv":["docs"],"skill_receipts":["r"],"profile":"root"}`,
		"exact duplicate":   `{"argv":["docs"],"argv":["wiki"],"skill_receipts":["r"]}`,
		"case variant":      `{"Argv":["docs"],"skill_receipts":["r"]}`,
		"mixed case field":  `{"argv":["docs"],"Skill_Receipts":["r"]}`,
		"trailing document": `{"argv":["docs"],"skill_receipts":["r"]} {}`,
		"empty argv":        `{"argv":[],"skill_receipts":["r"]}`,
		"prefix only":       `{"argv":["lark-cli"],"skill_receipts":["r"]}`,
	} {
		runID := nextRunID
		nextRunID++
		t.Run(name, func(t *testing.T) {
			larkExecuteRetryClearRun(runID)
			t.Cleanup(func() { larkExecuteRetryClearRun(runID) })
			executor := &fakeLarkExecutor{}
			result, err := (&larkExecuteTool{executor: executor}).Execute(larkPersonalWorkspaceContext(7, runID, "tc-8"), ToolInput(input))
			requireSafeLarkSoftError(t, result, err, "evil", "secret", "/tmp/x")
			require.Contains(t, string(result), `"code":"invalid_execute_input"`)
			require.Contains(t, string(result), `"recoverable":true`)
			assert.Empty(t, executor.snapshot(), "rejected model input must never reach the operation executor")
		})
	}
}

func TestLarkPersonalWorkspace_ExecuteMissingContextFailsClosed(t *testing.T) {
	contexts := map[string]context.Context{
		"missing user": WithToolCallID(WithRunID(context.Background(), 8), "tc-8"),
		"missing run":  WithToolCallID(middleware.NewContextWithUserID(context.Background(), 7), "tc-8"),
		"missing call": WithRunID(middleware.NewContextWithUserID(context.Background(), 7), 8),
	}
	for name, ctx := range contexts {
		t.Run(name, func(t *testing.T) {
			executor := &fakeLarkExecutor{}
			result, err := (&larkExecuteTool{executor: executor}).Execute(ctx, ToolInput(`{"argv":["docs"],"skill_receipts":["receipt"]}`))
			requireSafeLarkSoftError(t, result, err, "receipt")
			assert.Empty(t, executor.snapshot())
		})
	}

	result, err := (&larkExecuteTool{}).Execute(larkPersonalWorkspaceContext(7, 8, "tc-8"), ToolInput(`{"argv":["docs"],"skill_receipts":["receipt"]}`))
	requireSafeLarkSoftError(t, result, err, "receipt")
}

func TestLarkPersonalWorkspace_ExecuteWaitingYieldsDurableExternalAction(t *testing.T) {
	expiresAt := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	states := []string{
		model.FeishuOperationWaitingConnection,
		model.FeishuOperationWaitingAppScope,
		model.FeishuOperationWaitingUserAuth,
		model.FeishuOperationWaitingConfirmation,
	}
	for _, state := range states {
		t.Run(state, func(t *testing.T) {
			executor := &fakeLarkExecutor{result: &feishu.OperationResult{
				OperationID: "op-" + state,
				State:       state,
				ToolCallID:  "untrusted-result-tool-call",
				Action: &feishu.OperationAction{
					Provider:    "lark",
					OperationID: "op-" + state,
					SessionID:   "session-" + state,
					Phase:       "phase-" + state,
					URL:         "https://open.feishu.cn/action?state=live-only",
					ExpiresAt:   expiresAt,
				},
			}}
			result, err := (&larkExecuteTool{executor: executor}).Execute(
				larkPersonalWorkspaceContext(7, 600, "synthetic-context-call"),
				ToolInput(`{"argv":["docs"],"skill_receipts":["receipt"]}`),
			)
			assert.Nil(t, result)
			var yielded *yieldError
			require.ErrorAs(t, err, &yielded)
			require.NotNil(t, yielded.Payload.ExternalAction)
			action := yielded.Payload.ExternalAction
			assert.Equal(t, executor.result.Action.Provider, action.Provider)
			assert.Equal(t, executor.result.Action.OperationID, action.OperationID)
			assert.Equal(t, executor.result.Action.SessionID, action.SessionID)
			assert.Equal(t, "synthetic-context-call", action.ToolCallID, "external wait must use adapter context identity")
			assert.Equal(t, executor.result.Action.Phase, action.Phase)
			assert.Equal(t, executor.result.Action.URL, action.URL, "live action keeps its transient URL")
			assert.Equal(t, expiresAt, action.ExpiresAt)

			persistent, marshalErr := json.Marshal(action.Persistent())
			require.NoError(t, marshalErr)
			assert.NotContains(t, string(persistent), "url", "restart-safe persistence must strip the live URL")
			assert.Contains(t, string(persistent), "synthetic-context-call")
		})
	}
}

func TestLarkPersonalWorkspace_ExecuteReloadedActionWithoutURLStillYields(t *testing.T) {
	expiresAt := time.Date(2026, 7, 13, 12, 30, 0, 0, time.UTC)
	live := ExternalActionPayload{
		Provider:    "lark",
		OperationID: "op-reloaded-confirmation",
		SessionID:   "session-reloaded-confirmation",
		ToolCallID:  "original-tool-call",
		Phase:       "confirmation",
		URL:         "https://confirmation.example/live-only",
		ExpiresAt:   expiresAt,
	}
	persisted, err := json.Marshal(live.Persistent())
	require.NoError(t, err)
	reloaded, err := ParsePendingExternalAction(persisted)
	require.NoError(t, err)
	require.Empty(t, reloaded.URL, "cross-process reload must not recover the transient URL")

	executor := &fakeLarkExecutor{result: &feishu.OperationResult{
		OperationID: reloaded.OperationID,
		State:       model.FeishuOperationWaitingConfirmation,
		Action: &feishu.OperationAction{
			Provider:    reloaded.Provider,
			OperationID: reloaded.OperationID,
			SessionID:   reloaded.SessionID,
			Phase:       reloaded.Phase,
			ExpiresAt:   reloaded.ExpiresAt,
		},
	}}
	result, err := (&larkExecuteTool{executor: executor}).Execute(
		larkPersonalWorkspaceContext(7, 601, "reloaded-context-call"),
		ToolInput(`{"argv":["docs"],"skill_receipts":["receipt"]}`),
	)
	assert.Nil(t, result)
	var yielded *yieldError
	require.ErrorAs(t, err, &yielded)
	require.NotNil(t, yielded.Payload.ExternalAction)
	assert.Empty(t, yielded.Payload.ExternalAction.URL)
	assert.Equal(t, reloaded.Provider, yielded.Payload.ExternalAction.Provider)
	assert.Equal(t, reloaded.OperationID, yielded.Payload.ExternalAction.OperationID)
	assert.Equal(t, reloaded.SessionID, yielded.Payload.ExternalAction.SessionID)
	assert.Equal(t, reloaded.Phase, yielded.Payload.ExternalAction.Phase)
	assert.Equal(t, reloaded.ExpiresAt, yielded.Payload.ExternalAction.ExpiresAt)

	repersisted, err := json.Marshal(yielded.Payload.ExternalAction.Persistent())
	require.NoError(t, err)
	assert.NotContains(t, string(repersisted), "url")
}

func TestLarkPersonalWorkspace_ExecuteInvalidWaitingAndExecutorErrorsAreSafe(t *testing.T) {
	const runID = uint64(8)
	larkExecuteRetryClearRun(runID)
	t.Cleanup(func() { larkExecuteRetryClearRun(runID) })
	invalidWaiting := &fakeLarkExecutor{result: &feishu.OperationResult{
		OperationID: "op-sensitive",
		State:       model.FeishuOperationWaitingUserAuth,
		Action: &feishu.OperationAction{
			Provider: "lark",
			URL:      "https://secret.example/action",
		},
	}}
	result, err := (&larkExecuteTool{executor: invalidWaiting}).Execute(
		larkPersonalWorkspaceContext(7, runID, "tc-8"),
		ToolInput(`{"argv":["docs","+fetch"],"skill_receipts":["secret-receipt"]}`),
	)
	requireSafeLarkSoftError(t, result, err, "op-sensitive", "secret.example", "secret-receipt", "+fetch")
	var yielded *yieldError
	assert.False(t, errors.As(err, &yielded), "incomplete action must not create an unpersistable card")

	internalErr := errors.New("runner leaked argv docs +fetch receipt-raw at /private/home")
	failing := &fakeLarkExecutor{err: internalErr}
	result, err = (&larkExecuteTool{executor: failing}).Execute(
		larkPersonalWorkspaceContext(7, runID, "tc-8"),
		ToolInput(`{"argv":["docs","+fetch"],"skill_receipts":["receipt-raw"]}`),
	)
	requireSafeLarkSoftError(t, result, err, internalErr.Error(), "receipt-raw", "+fetch", "/private/home")
	assert.Contains(t, string(result), "停止重复调用")
}

// Customer regression (Dev run 204): a rejected hosted command was projected
// as a generic transient error, so the model blindly retried auth/config and
// finally asked the user for App credentials. Rejections need fixed,
// non-secret, non-local-CLI recovery guidance.
func TestLarkPersonalWorkspace_ExecuteRejectedCommandStopsLocalCLIRetries(t *testing.T) {
	const runID = uint64(204)
	larkExecuteRetryClearRun(runID)
	t.Cleanup(func() { larkExecuteRetryClearRun(runID) })

	executor := &fakeLarkExecutor{err: feishu.ErrOperationRequestRejected}
	tool := &larkExecuteTool{executor: executor}
	result, err := tool.Execute(
		larkPersonalWorkspaceContext(1, runID, "dev-run-204-rejected-1"),
		ToolInput(`{"argv":["auth","status","--json"],"skill_receipts":["opaque-shared-receipt"]}`),
	)
	require.NoError(t, err)
	assert.Contains(t, string(result), "不要执行 auth/config/whoami")
	assert.Contains(t, string(result), "不要要求用户提供 App ID/App Secret")
	assert.Contains(t, string(result), "Docs/Base/Wiki")
	assert.Contains(t, string(result), `"max_attempts":5`)
	assert.Contains(t, string(result), `"attempt":1`)
	assert.Contains(t, string(result), `"remaining_attempts":4`)
	assert.Contains(t, string(result), `"code":"command_rejected"`)
	assert.Contains(t, string(result), `"recoverable":true`)
	assert.NotContains(t, string(result), "请稍后重试")
	assert.NotContains(t, string(result), "opaque-shared-receipt")

	for attempt := 2; attempt < 5; attempt++ {
		result, err = tool.Execute(
			larkPersonalWorkspaceContext(1, runID, fmt.Sprintf("dev-run-204-rejected-%d", attempt)),
			ToolInput(`{"argv":["docs","+create","--content","<title>联调</title>"],"skill_receipts":["opaque-shared-receipt","opaque-doc-receipt"]}`),
		)
		require.NoError(t, err)
		assert.Contains(t, string(result), `"code":"command_rejected"`)
		assert.Contains(t, string(result), fmt.Sprintf(`"attempt":%d`, attempt))
		assert.Contains(t, string(result), `"recoverable":true`)
	}

	result, err = tool.Execute(
		larkPersonalWorkspaceContext(1, runID, "dev-run-204-rejected-5"),
		ToolInput(`{"argv":["docs","+create","--content","<title>第五次</title>"],"skill_receipts":["opaque-shared-receipt","opaque-doc-receipt"]}`),
	)
	require.NoError(t, err)
	assert.Contains(t, string(result), `"code":"correction_exhausted"`)
	assert.Contains(t, string(result), `"attempt":5`)
	assert.Contains(t, string(result), `"recoverable":false`)

	result, err = tool.Execute(
		larkPersonalWorkspaceContext(1, runID, "dev-run-204-rejected-6"),
		ToolInput(`{"argv":["docs","+create","--content","<title>不应执行</title>"],"skill_receipts":["opaque-shared-receipt","opaque-doc-receipt"]}`),
	)
	require.NoError(t, err)
	assert.Contains(t, string(result), "已停止后续飞书命令")
	assert.Len(t, executor.snapshot(), 5, "the sixth call must be blocked before reaching the executor")

	const otherRunID = uint64(205)
	t.Cleanup(func() { larkExecuteRetryClearRun(otherRunID) })
	result, err = tool.Execute(
		larkPersonalWorkspaceContext(1, otherRunID, "other-run-first-rejection"),
		ToolInput(`{"argv":["docs","+create","--content","<title>另一个任务</title>"],"skill_receipts":["opaque-shared-receipt","opaque-doc-receipt"]}`),
	)
	require.NoError(t, err)
	assert.Contains(t, string(result), `"max_attempts":5`)
	assert.NotContains(t, string(result), "已停止后续飞书命令")
	assert.Len(t, executor.snapshot(), 6, "an exhausted run must not consume another run's correction budget")

	larkExecuteRetryClearRun(runID)
	result, err = tool.Execute(
		larkPersonalWorkspaceContext(1, runID, "cleared-run-first-rejection"),
		ToolInput(`{"argv":["docs","+create","--content","<title>清理后新执行段</title>"],"skill_receipts":["opaque-shared-receipt","opaque-doc-receipt"]}`),
	)
	require.NoError(t, err)
	assert.Contains(t, string(result), `"max_attempts":5`)
	assert.NotContains(t, string(result), "已停止后续飞书命令")
	assert.Len(t, executor.snapshot(), 7, "run cleanup must restore a fresh correction budget")
}

// Customer regression (Dev run 248): two pre-execution command rejections
// exhausted the run before the model's next, valid correction could reach the
// executor. Correctable commands get five total attempts; success on the fifth
// attempt must still execute and reset the run budget.
func TestLarkPersonalWorkspace_ExecuteAllowsFiveCorrectableAttempts(t *testing.T) {
	const runID = uint64(248)
	larkExecuteRetryClearRun(runID)
	t.Cleanup(func() { larkExecuteRetryClearRun(runID) })

	executor := &fakeLarkExecutor{err: feishu.ErrOperationRequestRejected}
	tool := &larkExecuteTool{executor: executor}
	for attempt := 1; attempt < 5; attempt++ {
		result, err := tool.Execute(
			larkPersonalWorkspaceContext(1, runID, fmt.Sprintf("correctable-%d", attempt)),
			ToolInput(`{"argv":["base","+base-create","--name","联调","--table-name","任务列表"]}`),
		)
		require.NoError(t, err)
		require.Contains(t, string(result), `"code":"command_rejected"`, "attempt %d must remain correctable", attempt)
		require.Contains(t, string(result), `"recoverable":true`)
	}

	executor.mu.Lock()
	executor.err = nil
	executor.result = &feishu.OperationResult{
		OperationID: "op-corrected-fifth", State: model.FeishuOperationSucceeded,
		Data: json.RawMessage(`{"base_token":"base-created"}`),
	}
	executor.mu.Unlock()

	result, err := tool.Execute(
		larkPersonalWorkspaceContext(1, runID, "corrected-fifth"),
		ToolInput(`{"argv":["base","+base-create","--name","联调"]}`),
	)
	require.NoError(t, err)
	require.Contains(t, string(result), `"ok":true`)
	require.Len(t, executor.snapshot(), 5, "all five attempts must reach the executor")
}

func TestLarkPersonalWorkspace_ExecuteCorrectionBudgetIsConcurrentAndResetsAfterSuccess(t *testing.T) {
	t.Run("only one correction may be in flight", func(t *testing.T) {
		const runID = uint64(206)
		larkExecuteRetryClearRun(runID)
		t.Cleanup(func() { larkExecuteRetryClearRun(runID) })
		executor := &gatedRejectedLarkExecutor{
			gateAfter: 1,
			entered:   make(chan struct{}, 3),
			release:   make(chan struct{}),
		}
		tool := &larkExecuteTool{executor: executor}
		_, err := tool.Execute(
			larkPersonalWorkspaceContext(1, runID, "initial-rejection"),
			ToolInput(`{"argv":["auth","status"],"skill_receipts":["shared"]}`),
		)
		require.NoError(t, err)

		firstDone := make(chan struct{})
		go func() {
			defer close(firstDone)
			_, executeErr := tool.Execute(
				larkPersonalWorkspaceContext(1, runID, "in-flight-correction"),
				ToolInput(`{"argv":["docs","+create","--content","<title>联调</title>"],"skill_receipts":["shared","doc"]}`),
			)
			assert.NoError(t, executeErr)
		}()
		select {
		case <-executor.entered:
		case <-time.After(time.Second):
			t.Fatal("the correction did not reach the executor")
		}

		var blocked sync.WaitGroup
		blocked.Add(2)
		for index := 0; index < 2; index++ {
			go func(index int) {
				defer blocked.Done()
				_, executeErr := tool.Execute(
					larkPersonalWorkspaceContext(1, runID, fmt.Sprintf("blocked-concurrent-correction-%d", index)),
					ToolInput(`{"argv":["docs","+create","--content","<title>联调</title>"],"skill_receipts":["shared","doc"]}`),
				)
				assert.NoError(t, executeErr)
			}(index)
		}
		blocked.Wait()
		assert.Equal(t, 2, executor.callCount(), "parallel corrections must not bypass the in-flight guard")

		close(executor.release)
		select {
		case <-firstDone:
		case <-time.After(time.Second):
			t.Fatal("the correction did not finish after release")
		}
		assert.Equal(t, 2, executor.callCount(), "one initial call plus the in-flight correction may reach the executor")
	})

	t.Run("successful correction restores a fresh budget", func(t *testing.T) {
		const runID = uint64(207)
		larkExecuteRetryClearRun(runID)
		t.Cleanup(func() { larkExecuteRetryClearRun(runID) })
		executor := &fakeLarkExecutor{err: feishu.ErrOperationRequestRejected}
		tool := &larkExecuteTool{executor: executor}
		_, err := tool.Execute(
			larkPersonalWorkspaceContext(1, runID, "initial-rejection"),
			ToolInput(`{"argv":["auth","status"],"skill_receipts":["shared"]}`),
		)
		require.NoError(t, err)

		executor.mu.Lock()
		executor.err = nil
		executor.result = &feishu.OperationResult{
			OperationID: "op-success", State: model.FeishuOperationSucceeded, Data: json.RawMessage(`{"ok":true}`),
		}
		executor.mu.Unlock()
		result, err := tool.Execute(
			larkPersonalWorkspaceContext(1, runID, "successful-correction"),
			ToolInput(`{"argv":["docs","+create","--content","<title>成功</title>"],"skill_receipts":["shared","doc"]}`),
		)
		require.NoError(t, err)
		assert.Contains(t, string(result), `"ok":true`)

		executor.mu.Lock()
		executor.err = feishu.ErrOperationRequestRejected
		executor.result = nil
		executor.mu.Unlock()
		result, err = tool.Execute(
			larkPersonalWorkspaceContext(1, runID, "new-cycle-rejection"),
			ToolInput(`{"argv":["docs","+create","--content","<title>新周期</title>"],"skill_receipts":["shared","doc"]}`),
		)
		require.NoError(t, err)
		assert.Contains(t, string(result), `"max_attempts":5`)
		assert.Contains(t, string(result), `"attempt":1`)
		assert.NotContains(t, string(result), "已停止后续飞书命令")
		assert.Len(t, executor.snapshot(), 3)
	})
}

func TestLarkPersonalWorkspace_ParallelCommandIsRecoverableAndDoesNotConsumeRetryBudget(t *testing.T) {
	const runID = uint64(811)
	larkExecuteRetryClearRun(runID)
	t.Cleanup(func() { larkExecuteRetryClearRun(runID) })
	executor := &gatedSuccessLarkExecutor{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	tool := &larkExecuteTool{executor: executor}

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		result, err := tool.Execute(
			larkPersonalWorkspaceContext(1, runID, "base-get"),
			ToolInput(`{"argv":["base","+base-get","base-token"]}`),
		)
		assert.NoError(t, err)
		assert.Contains(t, string(result), `"ok":true`)
	}()

	select {
	case <-executor.entered:
	case <-time.After(time.Second):
		t.Fatal("the first lark_execute call did not enter the executor")
	}

	blocked, err := tool.Execute(
		larkPersonalWorkspaceContext(1, runID, "table-list"),
		ToolInput(`{"argv":["base","+table-list","base-token"]}`),
	)
	require.NoError(t, err)
	require.Contains(t, string(blocked), `"code":"command_in_flight"`)
	require.Contains(t, string(blocked), `"recoverable":true`)
	assert.Equal(t, 1, executor.callCount(), "parallel call must wait outside the executor")

	close(executor.release)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("the first lark_execute call did not finish after release")
	}

	next, err := tool.Execute(
		larkPersonalWorkspaceContext(1, runID, "table-list-after-base-get"),
		ToolInput(`{"argv":["base","+table-list","base-token"]}`),
	)
	require.NoError(t, err)
	require.Contains(t, string(next), `"ok":true`)
	assert.Equal(t, 2, executor.callCount(), "the blocked parallel call must not consume the correction budget or lock the run")
}

func TestLarkPersonalWorkspace_ExecuteTerminalStatesNeverFakeSuccess(t *testing.T) {
	for index, state := range []string{model.FeishuOperationFailed, model.FeishuOperationUnknown, model.FeishuOperationCancelled} {
		t.Run(state, func(t *testing.T) {
			runID := uint64(800 + index)
			larkExecuteRetryClearRun(runID)
			t.Cleanup(func() { larkExecuteRetryClearRun(runID) })
			code, category := feishu.PublicCodeFailed, "failed"
			if state == model.FeishuOperationUnknown {
				code, category = feishu.PublicCodeUnknownResult, "unknown_result"
			}
			if state == model.FeishuOperationCancelled {
				code, category = feishu.PublicCodeCancelled, "cancelled"
			}
			executor := &fakeLarkExecutor{result: &feishu.OperationResult{
				OperationID: "op-1", State: state,
				Failure: &feishu.OperationFailure{Code: code, Category: category, BusinessStarted: true},
			}}
			result, err := (&larkExecuteTool{executor: executor}).Execute(
				larkPersonalWorkspaceContext(7, runID, "tc-8"),
				ToolInput(`{"argv":["docs"],"skill_receipts":["receipt"]}`),
			)
			require.NoError(t, err)
			var output struct {
				OK    bool   `json:"ok"`
				State string `json:"state"`
			}
			require.NoError(t, json.Unmarshal(result, &output))
			assert.False(t, output.OK)
			assert.Equal(t, state, output.State)
		})
	}
}

func TestLarkPersonalWorkspace_ExecuteStructuredOutcomesControlRunRetries(t *testing.T) {
	t.Run("run 255 unknown docs write cannot freeze verification or unrelated base work", func(t *testing.T) {
		const runID = uint64(8255)
		larkExecuteRetryClearRun(runID)
		t.Cleanup(func() { larkExecuteRetryClearRun(runID) })
		executor := &fakeLarkExecutor{result: &feishu.OperationResult{
			OperationID: "op-doc-update-unknown", State: model.FeishuOperationUnknown,
			Failure: &feishu.OperationFailure{
				Code: feishu.PublicCodeUnknownResult, Category: "unknown_result", BusinessStarted: true,
			},
		}}
		tool := &larkExecuteTool{executor: executor}

		unknown, err := tool.Execute(
			larkPersonalWorkspaceContext(7, runID, "doc-update-unknown"),
			ToolInput(`{"argv":["docs","+update","--doc","doxcnABCDEFG123","--command","append","--content","<p>项目介绍</p>"]}`),
		)
		require.NoError(t, err)
		require.Contains(t, string(unknown), `"category":"unknown_result"`)

		executor.mu.Lock()
		executor.result = &feishu.OperationResult{
			OperationID: "op-doc-fetch-failed", State: model.FeishuOperationFailed,
			Failure: &feishu.OperationFailure{
				Code: feishu.PublicCodeFailed, Category: "failed", BusinessStarted: true,
			},
		}
		executor.mu.Unlock()
		failedVerification, err := tool.Execute(
			larkPersonalWorkspaceContext(7, runID, "doc-fetch-failed"),
			ToolInput(`{"argv":["docs","+fetch","--doc","doxcnABCDEFG123"]}`),
		)
		require.NoError(t, err)
		require.Contains(t, string(failedVerification), `"category":"failed"`)

		executor.mu.Lock()
		executor.result = &feishu.OperationResult{
			OperationID: "op-wiki-read", State: model.FeishuOperationSucceeded,
			Data: json.RawMessage(`{"node_token":"wikcnABCDEFG123"}`),
		}
		executor.mu.Unlock()
		alternateVerification, err := tool.Execute(
			larkPersonalWorkspaceContext(7, runID, "wiki-node-read"),
			ToolInput(`{"argv":["wiki","+node-get","--node-token","wikcnABCDEFG123"]}`),
		)
		require.NoError(t, err)
		require.Contains(t, string(alternateVerification), `"ok":true`)

		executor.mu.Lock()
		executor.result = &feishu.OperationResult{
			OperationID: "op-base-create", State: model.FeishuOperationSucceeded,
			Data: json.RawMessage(`{"app_token":"bascnABCDEFG123"}`),
		}
		executor.mu.Unlock()
		unrelatedWrite, err := tool.Execute(
			larkPersonalWorkspaceContext(7, runID, "base-create"),
			ToolInput(`{"argv":["base","+base-create","--name","项目任务","--table-name","任务列表"]}`),
		)
		require.NoError(t, err)
		require.Contains(t, string(unrelatedWrite), `"ok":true`)
		require.Len(t, executor.snapshot(), 4, "only the ambiguous write itself may be fenced")
	})

	t.Run("fifth malformed input exhausts correction budget without executor", func(t *testing.T) {
		const runID = uint64(809)
		larkExecuteRetryClearRun(runID)
		t.Cleanup(func() { larkExecuteRetryClearRun(runID) })
		executor := &fakeLarkExecutor{result: &feishu.OperationResult{
			OperationID: "must-not-run", State: model.FeishuOperationSucceeded,
			Data: json.RawMessage(`{"ok":true}`),
		}}
		tool := &larkExecuteTool{executor: executor}

		for attempt := 1; attempt < 5; attempt++ {
			result, err := tool.Execute(
				larkPersonalWorkspaceContext(7, runID, fmt.Sprintf("malformed-%d", attempt)),
				ToolInput(`{"argv":[]}`),
			)
			requireSafeLarkSoftError(t, result, err)
			require.Contains(t, string(result), `"code":"invalid_execute_input"`)
			require.Contains(t, string(result), fmt.Sprintf(`"attempt":%d`, attempt))
			require.Contains(t, string(result), `"recoverable":true`)
		}

		fifth, err := tool.Execute(
			larkPersonalWorkspaceContext(7, runID, "malformed-5"),
			ToolInput(`{"argv":[]}`),
		)
		requireSafeLarkSoftError(t, fifth, err)
		require.Contains(t, string(fifth), `"code":"correction_exhausted"`)
		require.Contains(t, string(fifth), `"recoverable":false`)
		require.Empty(t, executor.snapshot())
	})

	t.Run("durable external unknown fences only its exact command", func(t *testing.T) {
		const runID = uint64(810)
		larkExecuteRetryClearRun(runID)
		t.Cleanup(func() { larkExecuteRetryClearRun(runID) })
		argv := []string{"docs", "+create", "--title", "duplicate"}
		command, err := feishu.NewCommandCatalog().Normalize(argv, nil)
		require.NoError(t, err)
		fenceKey := larkExecuteWriteFenceKey(command)
		require.NotEmpty(t, fenceKey)
		externalResult, err := feishu.MarshalLarkToolResult(&feishu.OperationResult{
			OperationID: "op-resumed-unknown", State: model.FeishuOperationUnknown,
			Failure: &feishu.OperationFailure{
				Code: feishu.PublicCodeUnknownResult, Category: "unknown_result", BusinessStarted: true,
				WriteFenceKey: fenceKey,
			},
		})
		require.NoError(t, err)
		require.True(t, larkExecuteRetrySeedExternalResult(runID, externalResult))

		executor := &fakeLarkExecutor{result: &feishu.OperationResult{
			OperationID: "must-not-run", State: model.FeishuOperationSucceeded, Data: json.RawMessage(`{"ok":true}`),
		}}
		result, err := (&larkExecuteTool{executor: executor}).Execute(
			larkPersonalWorkspaceContext(7, runID, "write-after-resume"),
			ToolInput(`{"argv":["docs","+create","--title","duplicate"]}`),
		)
		requireSafeLarkSoftError(t, result, err)
		require.Contains(t, string(result), "完全相同")
		require.Empty(t, executor.snapshot())
	})

	t.Run("legacy unknown without an exact fingerprint cannot freeze the run", func(t *testing.T) {
		const runID = uint64(817)
		larkExecuteRetryClearRun(runID)
		t.Cleanup(func() { larkExecuteRetryClearRun(runID) })
		legacyUnknown, err := feishu.MarshalLarkToolResult(&feishu.OperationResult{
			OperationID: "op-legacy-unknown", State: model.FeishuOperationUnknown,
			Failure: &feishu.OperationFailure{
				Code: feishu.PublicCodeUnknownResult, Category: "unknown_result", BusinessStarted: true,
			},
		})
		require.NoError(t, err)
		require.True(t, larkExecuteRetrySeedExternalResult(runID, legacyUnknown))

		executor := &fakeLarkExecutor{result: &feishu.OperationResult{
			OperationID: "op-new-work", State: model.FeishuOperationSucceeded, Data: json.RawMessage(`{"ok":true}`),
		}}
		result, err := (&larkExecuteTool{executor: executor}).Execute(
			larkPersonalWorkspaceContext(7, runID, "new-work-after-legacy-unknown"),
			ToolInput(`{"argv":["base","+base-create","--name","继续任务","--table-name","任务列表"]}`),
		)
		require.NoError(t, err)
		require.Contains(t, string(result), `"ok":true`)
		require.Len(t, executor.snapshot(), 1)
	})

	t.Run("durable transcript keeps unknown write fenced after successful verification", func(t *testing.T) {
		const runID = uint64(816)
		larkExecuteRetryClearRun(runID)
		t.Cleanup(func() { larkExecuteRetryClearRun(runID) })
		argv := []string{"base", "+field-create", "--base-token", "bascnABCDEFG123", "--table-id", "tblABCDEFG123", "--json", `{"name":"测试备注","type":"text"}`}
		command, err := feishu.NewCommandCatalog().Normalize(argv, nil)
		require.NoError(t, err)
		fenceKey := larkExecuteWriteFenceKey(command)
		require.NotEmpty(t, fenceKey)
		unknown, err := feishu.MarshalLarkToolResult(&feishu.OperationResult{
			OperationID: "op-durable-unknown", State: model.FeishuOperationUnknown,
			Failure: &feishu.OperationFailure{
				Code: feishu.PublicCodeUnknownResult, Category: "unknown_result", BusinessStarted: true,
				WriteFenceKey: fenceKey,
			},
		})
		require.NoError(t, err)
		transcript, err := json.Marshal([]map[string]any{
			{"role": "tool", "tool_call_id": "write-unknown", "content": string(unknown)},
			{"role": "tool", "tool_call_id": "verification-read", "content": `{"ok":true,"state":"succeeded","operation_id":"op-read","data":{"items":[]}}`},
		})
		require.NoError(t, err)
		require.True(t, larkExecuteRetrySeedTranscript(runID, transcript))
		validation, err := feishu.MarshalLarkToolResult(&feishu.OperationResult{
			OperationID: "op-later-validation", State: model.FeishuOperationFailed,
			Failure: &feishu.OperationFailure{
				Code: feishu.PublicCodeValidationError, Category: "validation", BusinessStarted: false,
			},
		})
		require.NoError(t, err)
		require.True(t, larkExecuteRetrySeedExternalResult(runID, validation))

		executor := &fakeLarkExecutor{result: &feishu.OperationResult{
			OperationID: "must-not-run", State: model.FeishuOperationSucceeded, Data: json.RawMessage(`{"ok":true}`),
		}}
		result, err := (&larkExecuteTool{executor: executor}).Execute(
			larkPersonalWorkspaceContext(7, runID, "write-after-worker-change"),
			ToolInput(`{"argv":["base","+field-create","--base-token","bascnABCDEFG123","--table-id","tblABCDEFG123","--json","{\"name\":\"测试备注\",\"type\":\"text\"}"]}`),
		)
		requireSafeLarkSoftError(t, result, err)
		require.Contains(t, string(result), "完全相同")
		require.Empty(t, executor.snapshot())
	})

	t.Run("unknown started write permits a different write but fences exact replay", func(t *testing.T) {
		const runID = uint64(811)
		larkExecuteRetryClearRun(runID)
		t.Cleanup(func() { larkExecuteRetryClearRun(runID) })
		executor := &fakeLarkExecutor{result: &feishu.OperationResult{
			OperationID: "op-unknown", State: model.FeishuOperationUnknown,
			Failure: &feishu.OperationFailure{
				Code: feishu.PublicCodeUnknownResult, Category: "unknown_result", BusinessStarted: true,
			},
		}}
		tool := &larkExecuteTool{executor: executor}
		first, err := tool.Execute(
			larkPersonalWorkspaceContext(7, runID, "write-1"),
			ToolInput(`{"argv":["docs","+create","--title","A"],"skill_receipts":["shared","doc"]}`),
		)
		require.NoError(t, err)
		require.Contains(t, string(first), `"category":"unknown_result"`)
		second, err := tool.Execute(
			larkPersonalWorkspaceContext(7, runID, "write-2"),
			ToolInput(`{"argv":["docs","+create","--title","B"],"skill_receipts":["shared","doc"]}`),
		)
		require.NoError(t, err)
		require.Contains(t, string(second), `"category":"unknown_result"`)
		replayed, err := tool.Execute(
			larkPersonalWorkspaceContext(7, runID, "write-1-replayed"),
			ToolInput(`{"argv":["docs","+create","--title","A"],"skill_receipts":["shared","doc"]}`),
		)
		requireSafeLarkSoftError(t, replayed, err)
		require.Contains(t, string(replayed), "完全相同")
		require.Len(t, executor.snapshot(), 2)
	})

	t.Run("unknown write permits read verification but never exact replay", func(t *testing.T) {
		const runID = uint64(815)
		larkExecuteRetryClearRun(runID)
		t.Cleanup(func() { larkExecuteRetryClearRun(runID) })
		executor := &fakeLarkExecutor{result: &feishu.OperationResult{
			OperationID: "op-unknown-field", State: model.FeishuOperationUnknown,
			Failure: &feishu.OperationFailure{
				Code: feishu.PublicCodeUnknownResult, Category: "unknown_result", BusinessStarted: true,
			},
		}}
		tool := &larkExecuteTool{executor: executor}
		unknown, err := tool.Execute(
			larkPersonalWorkspaceContext(7, runID, "field-create-unknown"),
			ToolInput(`{"argv":["base","+field-create","--base-token","bascnABCDEFG123","--table-id","tblABCDEFG123","--json","{\"name\":\"测试备注\",\"type\":\"text\"}"]}`),
		)
		require.NoError(t, err)
		require.Contains(t, string(unknown), `"category":"unknown_result"`)

		executor.mu.Lock()
		executor.result = &feishu.OperationResult{
			OperationID: "op-field-list", State: model.FeishuOperationSucceeded,
			Data: json.RawMessage(`{"items":[{"name":"测试备注","type":"text"}]}`),
		}
		executor.mu.Unlock()
		verified, err := tool.Execute(
			larkPersonalWorkspaceContext(7, runID, "field-list-verification"),
			ToolInput(`{"argv":["base","+field-list","--base-token","bascnABCDEFG123","--table-id","tblABCDEFG123"]}`),
		)
		require.NoError(t, err)
		require.Contains(t, string(verified), `"ok":true`)

		blockedWrite, err := tool.Execute(
			larkPersonalWorkspaceContext(7, runID, "field-create-duplicate"),
			ToolInput(`{"argv":["base","+field-create","--base-token","bascnABCDEFG123","--table-id","tblABCDEFG123","--json","{\"name\":\"测试备注\",\"type\":\"text\"}"]}`),
		)
		requireSafeLarkSoftError(t, blockedWrite, err)
		require.Contains(t, string(blockedWrite), "完全相同")
		require.Len(t, executor.snapshot(), 2, "verification may read but must never replay the ambiguous write")
	})

	t.Run("unknown started write allows reconciliation and different writes but blocks exact replay", func(t *testing.T) {
		const runID = uint64(815)
		larkExecuteRetryClearRun(runID)
		t.Cleanup(func() { larkExecuteRetryClearRun(runID) })
		executor := &fakeLarkExecutor{result: &feishu.OperationResult{
			OperationID: "op-unknown-reconcile", State: model.FeishuOperationUnknown,
			Failure: &feishu.OperationFailure{
				Code: feishu.PublicCodeUnknownResult, Category: "unknown_result", BusinessStarted: true,
			},
		}}
		tool := &larkExecuteTool{executor: executor}

		unknown, err := tool.Execute(
			larkPersonalWorkspaceContext(7, runID, "write-unknown"),
			ToolInput(`{"argv":["docs","+create","--title","A"]}`),
		)
		require.NoError(t, err)
		require.Contains(t, string(unknown), `"category":"unknown_result"`)

		executor.mu.Lock()
		executor.result = &feishu.OperationResult{
			OperationID: "op-read-reconcile", State: model.FeishuOperationSucceeded,
			Data: json.RawMessage(`{"document_id":"doxcnABCDEFG123","markers":["managed/v1"]}`),
		}
		executor.mu.Unlock()
		reconciled, err := tool.Execute(
			larkPersonalWorkspaceContext(7, runID, "read-reconcile"),
			ToolInput(`{"argv":["docs","+fetch","--doc","doxcnABCDEFG123"]}`),
		)
		require.NoError(t, err)
		require.Contains(t, string(reconciled), `"document_id":"doxcnABCDEFG123"`)

		differentWrite, err := tool.Execute(
			larkPersonalWorkspaceContext(7, runID, "write-replay"),
			ToolInput(`{"argv":["docs","+create","--title","B"]}`),
		)
		require.NoError(t, err)
		require.Contains(t, string(differentWrite), `"ok":true`)

		blocked, err := tool.Execute(
			larkPersonalWorkspaceContext(7, runID, "write-exact-replay"),
			ToolInput(`{"argv":["docs","+create","--title","A"]}`),
		)
		requireSafeLarkSoftError(t, blocked, err)
		require.Contains(t, string(blocked), "完全相同")
		require.Len(t, executor.snapshot(), 3, "reads and different writes may continue, but the exact ambiguous write must remain fenced")
	})

	t.Run("validation permits one correction then success resets", func(t *testing.T) {
		const runID = uint64(812)
		larkExecuteRetryClearRun(runID)
		t.Cleanup(func() { larkExecuteRetryClearRun(runID) })
		executor := &fakeLarkExecutor{result: &feishu.OperationResult{
			OperationID: "op-validation", State: model.FeishuOperationFailed,
			Failure: &feishu.OperationFailure{
				Code: feishu.PublicCodeValidationError, Category: "validation", BusinessStarted: true,
			},
		}}
		tool := &larkExecuteTool{executor: executor}
		first, err := tool.Execute(
			larkPersonalWorkspaceContext(7, runID, "read-invalid"),
			ToolInput(`{"argv":["docs","+fetch","--doc","bad"],"skill_receipts":["shared","doc"]}`),
		)
		require.NoError(t, err)
		require.Contains(t, string(first), `"category":"validation"`)
		executor.mu.Lock()
		executor.result = &feishu.OperationResult{
			OperationID: "op-corrected", State: model.FeishuOperationSucceeded,
			Data: json.RawMessage(`{"document_id":"doc-1"}`),
		}
		executor.mu.Unlock()
		corrected, err := tool.Execute(
			larkPersonalWorkspaceContext(7, runID, "read-corrected"),
			ToolInput(`{"argv":["docs","+fetch","--doc","doc-1"],"skill_receipts":["shared","doc"]}`),
		)
		require.NoError(t, err)
		require.Contains(t, string(corrected), `"ok":true`)

		executor.mu.Lock()
		executor.result = &feishu.OperationResult{
			OperationID: "op-not-found", State: model.FeishuOperationFailed,
			Failure: &feishu.OperationFailure{
				Code: feishu.PublicCodeNotFound, Category: "not_found", BusinessStarted: true,
			},
		}
		executor.mu.Unlock()
		notFound, err := tool.Execute(
			larkPersonalWorkspaceContext(7, runID, "read-new-cycle"),
			ToolInput(`{"argv":["docs","+fetch","--doc","missing"],"skill_receipts":["shared","doc"]}`),
		)
		require.NoError(t, err)
		require.Contains(t, string(notFound), `"category":"not_found"`)
		otherRead, err := tool.Execute(
			larkPersonalWorkspaceContext(7, runID, "read-blind-retry"),
			ToolInput(`{"argv":["docs","+fetch","--doc","other"],"skill_receipts":["shared","doc"]}`),
		)
		require.NoError(t, err)
		require.Contains(t, string(otherRead), `"category":"not_found"`)
		require.Len(t, executor.snapshot(), 4, "one terminal read result must not freeze later reads")
	})

	for _, testCase := range []struct {
		name     string
		runID    uint64
		failure  *feishu.OperationFailure
		category string
	}{
		{
			name: "fifth validation exhausts correction budget", runID: 813, category: "validation",
			failure: &feishu.OperationFailure{
				Code: feishu.PublicCodeValidationError, Category: "validation", BusinessStarted: false,
			},
		},
		{
			name: "fifth temporary failure exhausts retry budget", runID: 814, category: "temporary",
			failure: &feishu.OperationFailure{
				Code: feishu.PublicCodeTemporaryError, Category: "temporary", Retryable: true, BusinessStarted: false,
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			larkExecuteRetryClearRun(testCase.runID)
			t.Cleanup(func() { larkExecuteRetryClearRun(testCase.runID) })
			executor := &fakeLarkExecutor{result: &feishu.OperationResult{
				OperationID: "op-first-" + testCase.category,
				State:       model.FeishuOperationFailed,
				Failure:     testCase.failure,
			}}
			tool := &larkExecuteTool{executor: executor}
			for attempt := 1; attempt < 5; attempt++ {
				executor.mu.Lock()
				executor.result.OperationID = fmt.Sprintf("op-%d-%s", attempt, testCase.category)
				executor.mu.Unlock()
				result, err := tool.Execute(
					larkPersonalWorkspaceContext(7, testCase.runID, fmt.Sprintf("attempt-%d-%s", attempt, testCase.category)),
					ToolInput(`{"argv":["docs","+fetch","--doc","candidate"]}`),
				)
				require.NoError(t, err)
				require.Contains(t, string(result), `"category":"`+testCase.category+`"`)
			}

			executor.mu.Lock()
			executor.result.OperationID = "op-fifth-" + testCase.category
			executor.mu.Unlock()
			fifth, err := tool.Execute(
				larkPersonalWorkspaceContext(7, testCase.runID, "fifth-"+testCase.category),
				ToolInput(`{"argv":["docs","+fetch","--doc","candidate"]}`),
			)
			requireSafeLarkSoftError(t, fifth, err)
			require.Contains(t, string(fifth), `"code":"correction_exhausted"`)
			require.Len(t, executor.snapshot(), 5)
		})
	}
}

func TestLarkPersonalWorkspace_ExecuteRejectsNonTerminalNonWaitingStates(t *testing.T) {
	for name, state := range map[string]string{
		"executing":      model.FeishuOperationExecuting,
		"not started":    model.FeishuOperationNotStarted,
		"unknown string": "provider_future_state_sensitive",
		"empty":          "",
	} {
		t.Run(name, func(t *testing.T) {
			executor := &fakeLarkExecutor{result: &feishu.OperationResult{
				OperationID: "op-sensitive-active-state",
				State:       state,
				Data:        json.RawMessage(`{"secret":"active-lease-data"}`),
			}}
			result, err := (&larkExecuteTool{executor: executor}).Execute(
				larkPersonalWorkspaceContext(7, 9, "tc-active-state"),
				ToolInput(`{"argv":["docs"],"skill_receipts":["receipt"]}`),
			)
			requireSafeLarkSoftError(t, result, err,
				state, "op-sensitive-active-state", "active-lease-data")
			var yielded *yieldError
			assert.False(t, errors.As(err, &yielded), "active or unrecognized states must fail closed, not yield")
		})
	}
}

func TestLarkPersonalWorkspace_InspectUsesCurrentIdentityAndNeverExecutesBusiness(t *testing.T) {
	ready := false
	inspector := &fakeLarkInspector{result: &feishu.InspectionResult{
		Mode: feishu.InspectionModeCommand, CommandPath: "docs +update", Domain: "docs", Risk: feishu.RiskWrite,
		Ready:         &ready,
		GrantedScopes: []string{"docx:document:readonly"}, MissingScopes: []string{"docx:document:write_only"},
	}}
	tool := &larkInspectTool{inspector: inspector}
	result, err := tool.Execute(
		larkPersonalWorkspaceContext(29, 601, "inspect-1"),
		ToolInput(`{
			"mode":"command",
			"argv":["lark-cli","docs","+update","--doc","doxcnSECRET123","--command","append","--content","secret body"],
			"skill_receipts":["receipt-shared","receipt-doc"]
		}`),
	)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"mode":"command","command_path":"docs +update","domain":"docs","risk":"write",
		"ready":false,
		"granted_scopes":["docx:document:readonly"],"missing_scopes":["docx:document:write_only"]
	}`, string(result))
	requests := inspector.snapshot()
	require.Len(t, requests, 1)
	require.Equal(t, uint(29), requests[0].UserID)
	require.EqualValues(t, 601, requests[0].AgentRunID)
	require.Equal(t, "docs", requests[0].Argv[0])
	require.NotContains(t, string(result), "doxcnSECRET123")
	require.NotContains(t, string(result), "secret body")
	require.True(t, tool.IsReadOnly())
	require.False(t, tool.IsConcurrencySafe(nil))
}

func TestLarkPersonalWorkspace_InspectConnectionAndStrictBoundaries(t *testing.T) {
	inspector := &fakeLarkInspector{result: &feishu.InspectionResult{
		Mode: feishu.InspectionModeConnection, ConnectionState: model.FeishuConnectionConnected,
		Capabilities: map[string]string{"docs": model.FeishuCapabilityAvailable},
	}}
	tool := &larkInspectTool{inspector: inspector}
	result, err := tool.Execute(
		larkPersonalWorkspaceContext(30, 602, "inspect-connection"), ToolInput(`{"mode":"connection"}`),
	)
	require.NoError(t, err)
	require.Contains(t, string(result), `"connection_state":"connected"`)
	require.Len(t, inspector.snapshot(), 1)

	for name, input := range map[string]string{
		"unknown mode":         `{"mode":"auth"}`,
		"connection argv":      `{"mode":"connection","argv":["docs"]}`,
		"command missing argv": `{"mode":"command","skill_receipts":["r"]}`,
		"prefix only":          `{"mode":"command","argv":["lark-cli"],"skill_receipts":["r"]}`,
		"injected identity":    `{"mode":"connection","user_id":7}`,
		"duplicate mode":       `{"mode":"connection","mode":"command"}`,
	} {
		t.Run(name, func(t *testing.T) {
			before := len(inspector.snapshot())
			got, executeErr := tool.Execute(
				larkPersonalWorkspaceContext(30, 602, "inspect-invalid"), ToolInput(input),
			)
			requireSafeLarkSoftError(t, got, executeErr, "user_id")
			require.Len(t, inspector.snapshot(), before)
		})
	}

	got, err := tool.Execute(context.Background(), ToolInput(`{"mode":"connection"}`))
	requireSafeLarkSoftError(t, got, err)
}

func TestPlatformToolFactory_LarkPersonalWorkspaceBothOrNoneAndNoLegacyTools(t *testing.T) {
	reader := &fakeSkillReadExecutor{}
	inspector := &fakeLarkInspector{}
	executor := &fakeLarkExecutor{}
	legacyNames := []string{"lark_create_doc", "lark_read_bitable", "lark_send_message", "feishu_connect"}

	for name, configure := range map[string]func(ToolFactory){
		"none":           func(ToolFactory) {},
		"reader only":    func(f ToolFactory) { SetFactoryLarkWorkspaceExecutors(f, reader, nil, nil) },
		"inspector only": func(f ToolFactory) { SetFactoryLarkWorkspaceExecutors(f, nil, inspector, nil) },
		"execute only":   func(f ToolFactory) { SetFactoryLarkWorkspaceExecutors(f, nil, nil, executor) },
	} {
		t.Run(name, func(t *testing.T) {
			factory := NewPlatformToolFactory(nil, nil)
			configure(factory)
			tools, metadata, err := factory.LoadTools(context.Background())
			require.NoError(t, err)
			assert.Len(t, tools, 19)
			assert.Len(t, metadata, 19)
			for _, tool := range tools {
				assert.NotContains(t, append(legacyNames, "lark_skill_read", "lark_inspect", "lark_execute"), tool.Name())
			}
		})
	}
	assert.NotPanics(t, func() { SetFactoryLarkWorkspaceExecutors(nil, reader, inspector, executor) })

	factory := NewPlatformToolFactory(nil, nil)
	SetFactoryLarkWorkspaceExecutors(factory, reader, inspector, executor)
	pf := factory.(*platformToolFactory)
	pf.larkProviderOverride = &fakeLarkProvider{api: &fakeLarkAPI{}}
	pf.feishuConnectorOverride = &fakeConnector{step: &feishu.ConnectStep{Phase: feishu.ConnectPhaseDone}}
	tools, metadata, err := factory.LoadTools(context.Background())
	require.NoError(t, err)
	require.Len(t, tools, 22, "19 base tools plus exactly three controlled Lark tools")
	require.Len(t, metadata, 22)
	assert.Equal(t, "lark_skill_read", tools[19].Name())
	assert.Equal(t, "lark_inspect", tools[20].Name())
	assert.Equal(t, "lark_execute", tools[21].Name())
	assert.Equal(t, "lark_skill_read", metadata[19].ToolName)
	assert.Equal(t, "safe", metadata[19].RiskLevel)
	assert.Equal(t, "飞书", metadata[19].Category)
	assert.Equal(t, "lark_inspect", metadata[20].ToolName)
	assert.Equal(t, "safe", metadata[20].RiskLevel)
	assert.Equal(t, "飞书", metadata[20].Category)
	assert.Equal(t, "lark_execute", metadata[21].ToolName)
	assert.Equal(t, "moderate", metadata[21].RiskLevel)
	assert.Equal(t, "飞书", metadata[21].Category)
	assert.Contains(t, metadata[21].Description, "Drive")
	for _, legacy := range legacyNames {
		for _, tool := range tools {
			assert.NotEqual(t, legacy, tool.Name(), "legacy factory registration must stay removed")
		}
	}

	registry := NewAgentToolRegistry(newMockDefStore(), newMockFacStore())
	require.NoError(t, registry.RegisterFactory(factory))
	require.NoError(t, registry.LoadAll(context.Background()))
	_, ok := registry.GetTool("lark_skill_read")
	assert.True(t, ok)
	_, ok = registry.GetTool("lark_inspect")
	assert.True(t, ok)
	_, ok = registry.GetTool("lark_execute")
	assert.True(t, ok)
	for _, legacy := range legacyNames {
		_, exists := registry.GetTool(legacy)
		assert.False(t, exists, "registry must not expose %s", legacy)
	}
}

func TestLarkPersonalWorkspace_BashExecRoutesFeishuToControlledTools(t *testing.T) {
	tool := &bashExecTool{}
	description := tool.Description()
	assert.Contains(t, description, "飞书 Docs/Base/Wiki/Drive 必须通过 `lark_skill_read` + `lark_execute`")

	result, err := tool.Execute(context.Background(), ToolInput(`{"command":"lark-cli docs +fetch"}`))
	require.NoError(t, err)
	assert.Contains(t, string(result), "LarkCLIRoute")
	assert.Contains(t, string(result), "lark_execute")
	assert.NotContains(t, string(result), "沙箱当前不可用", "route denial must happen before sandbox lookup")
}
