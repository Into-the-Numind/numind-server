package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"numind-server/internal/numind/biz/compact"
	"numind-server/internal/numind/biz/skill"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

// RunRequest 是 AgentRunner.Run 的输入。
type RunRequest struct {
	UserID    uint
	SessionID string
	Input     string
	ToolNames []string
	Hooks     *RunHooks
	// AgentDefinitionID 为 0 时 fall through（使用 #2 mock 行为，不注入 Skill）。
	// 非 0 时 runner.Run 通过 skillStore.GetByIDIncludeInactive 装载 agent 定义，
	// 并按 advanced_mode 选 GeneratedSkillBody 或 CustomSkillBody 组装 SystemPrompt。
	AgentDefinitionID uint64
	// SystemPrompt 由 runner.Run 内部填充（skill lookup 后注入），调用方无需手动赋值。
	// 若 AgentDefinitionID=0，则保持为空字符串（fall through）。
	SystemPrompt string
}

// RunResult 是 AgentRunner.Run 的输出。
type RunResult struct {
	AgentRunID     uint64
	TerminalReason TerminalReason
	FinalOutput    string
	StepCount      int
	Duration       time.Duration
	// SkillVersion 是本次 Run 装载的 agent_definition.version；0 表示未注入 Skill（fall through）。
	SkillVersion int
	// PermissionDenial 在 TerminalReason == TerminalPermissionDenied 时填充（#6 permission-pipeline）；
	// JSON omitempty 兼容旧消费者（nil 时不序列化）。
	PermissionDenial *PermissionDenialDetail `json:"permission_denial,omitempty"`
}

// AgentRunner 是 Agent 运行时主接口（蓝本 §4.1.9）。
type AgentRunner interface {
	Run(ctx context.Context, req RunRequest) (*RunResult, error)
	Cancel(runID uint64) bool
}

type agentRunner struct {
	runStore        store.IAgentRunStore
	registry        AgentToolRegistry
	cancels         map[uint64]context.CancelFunc
	mu              sync.Mutex
	defaultHooks    *RunHooks                   // #4 sandbox-integration: wired by biz.go via WithDefaultHooks
	skillStore      store.IAgentDefinitionStore // #11 skill-system: wired by biz.go via WithSkillStore; may be nil
	compactProvider compact.CompactProvider     // #9 compact: wired by biz.go via WithCompactProvider; may be nil
	compactConfig   compact.Config              // #9 compact: defaults to compact.DefaultConfig() in NewAgentRunner
}

var _ AgentRunner = (*agentRunner)(nil)

// RunnerOption configures an agentRunner at construction (functional options).
type RunnerOption func(*agentRunner)

// WithDefaultHooks installs a *RunHooks to use when RunRequest.Hooks is nil.
// #4 sandbox-integration wires sandboxHookManager.AsRunHooks() this way so
// every Run gets sandbox lifecycle hooks by default — callers that want to
// override pass their own RunRequest.Hooks.
func WithDefaultHooks(h *RunHooks) RunnerOption {
	return func(r *agentRunner) {
		r.defaultHooks = h
	}
}

// WithSkillStore installs an IAgentDefinitionStore to enable Skill lookup in Run().
// #11 skill-system wires this so runner.Run can call GetByIDIncludeInactive when
// RunRequest.AgentDefinitionID > 0. When nil (default), skill lookup is skipped
// and SkillVersion=0 is returned (fall through / #2 mock behaviour preserved).
func WithSkillStore(s store.IAgentDefinitionStore) RunnerOption {
	return func(r *agentRunner) {
		r.skillStore = s
	}
}

// WithCompactProvider sets the CompactProvider used by PTL / pre-LLM compact
// helpers. #14 wires a real aiservice.Chat-backed provider; v1 uses
// compact.MockCompactProvider so helpers stay testable in #9 isolation.
// When nil, helpers no-op (tryPreLLMCompact returns input unchanged;
// handlePTLError step 2 returns a terminal error).
func WithCompactProvider(p compact.CompactProvider) RunnerOption {
	return func(r *agentRunner) {
		r.compactProvider = p
	}
}

// WithCompactConfig overrides the qwen-plus defaults set by NewAgentRunner.
// Use this when tuning AutoCompactThreshold / MaxConsecutiveAutoCompactFailures
// for a different model.
func WithCompactConfig(cfg compact.Config) RunnerOption {
	return func(r *agentRunner) {
		r.compactConfig = cfg
	}
}

// NewAgentRunner 工厂。
func NewAgentRunner(runStore store.IAgentRunStore, registry AgentToolRegistry, opts ...RunnerOption) AgentRunner {
	r := &agentRunner{
		runStore:      runStore,
		registry:      registry,
		cancels:       make(map[uint64]context.CancelFunc),
		compactConfig: compact.DefaultConfig(), // #9 compact: qwen-plus default; overridable via WithCompactConfig
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Run 主流程（spec §4.4）。
// 简化：#2 仅跑通 mock + 真实流程基础架构；
// LLM 调用 / Eino agent.Generate 真实集成靠 Task 8 集成测试覆盖。
func (r *agentRunner) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	startTime := time.Now()

	// 0. 注入 userID 到 context，供工具（如 kbSearchTool）读取。
	ctx = middleware.NewContextWithUserID(ctx, req.UserID)

	// 1. 创建 DB 行
	run := &model.AgentRun{
		UserID:    req.UserID,
		SessionID: req.SessionID,
		Status:    "running",
		Messages:  datatypes.JSON([]byte("[]")),
		StartedAt: startTime,
	}
	if err := r.runStore.Create(ctx, run); err != nil {
		return nil, fmt.Errorf("AgentRunner.Run: %w", err)
	}

	// 1.5. #4 sandbox-integration: 注入 runID 到 ctx，供 SandboxHook /
	// bash_exec.Execute 通过 RunIDFromContext / sandboxSessionForCurrentCall
	// 找到对应的 sandbox session。
	ctx = WithRunID(ctx, run.ID)

	// 1.6. #6 permission-pipeline: 创建 per-Run permission denial sink + 注入 ctx。
	// wrapper.PreToolCall deny 时通过此 sink 把 detail 回传给 runner 填 RunResult。
	// buffered size=1 + non-blocking send，避免 wrapper 阻塞；每 Run 独立实例避免 cross-run race。
	permDenialSink := make(chan *PermissionDenialDetail, 1)
	ctx = WithPermissionSink(ctx, permDenialSink)

	// 2. Langfuse trace
	traceID := langfuse.TraceID()
	langfuse.CreateTrace(traceID, "agent-runtime-run",
		langfuse.WithUserID(req.UserID),
		langfuse.WithTraceInput(map[string]any{
			"agent_run_id": run.ID,
			"session_id":   req.SessionID,
			"user_input":   req.Input,
		}),
		langfuse.WithTraceTags("agent-runtime-skeleton"),
	)
	ctx = langfuse.WithTrace(ctx, traceID)

	// 3. AbortController 三层 + 注册 cancel
	queryCtx, queryCancel := DeriveQueryCtx(ctx)
	r.registerCancel(run.ID, queryCancel)
	defer r.unregisterCancel(run.ID)
	defer queryCancel()

	// 4. #5 skill-system: 装载 agent_definition 并组装 SystemPrompt（若指定了 AgentDefinitionID）。
	var skillVer int
	if req.AgentDefinitionID > 0 && r.skillStore != nil {
		ad, err := r.skillStore.GetByIDIncludeInactive(ctx, req.AgentDefinitionID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errno.ErrSkillNotFound
			}
			return nil, fmt.Errorf("AgentRunner.Run skill lookup: %w", err)
		}
		if ad.ParentUserID != req.UserID {
			// 不暴露存在性：跨用户访问当作 NotFound 返回
			return nil, errno.ErrSkillNotFound
		}
		body := ad.GeneratedSkillBody
		if ad.AdvancedMode {
			body = ad.CustomSkillBody
		}
		req.SystemPrompt = skill.PlatformBasePrompt + body + skill.PlatformSafetyFooter
		skillVer = int(ad.Version)
		// #6 permission-pipeline: 注入 agent_definition_id + parent_user_id 到 ctx，
		// 供 ToolFlag / TenantAdminRule validator 读取。
		ctx = WithAgentDefCtx(ctx, req.AgentDefinitionID, ad.ParentUserID)
	}

	// 5. 从 registry 装配 Eino 工具列表
	// #4 sandbox-integration: 选 effectiveHooks — RunRequest.Hooks 优先，
	// 否则 r.defaultHooks（biz.go wire SandboxHookManager.AsRunHooks()）。
	effectiveHooks := req.Hooks
	if effectiveHooks == nil {
		effectiveHooks = r.defaultHooks
	}

	// Auto-inject Registry if hooks exist but caller didn't provide one.
	// Caller-provided Registry is kept as-is (caller controls its lifecycle).
	// Cross-Run stale risk for pool-shared *RunHooks (e.g. SandboxHookManager.AsRunHooks
	// stored in r.defaultHooks): mitigated by future caller using Registry.Reset()
	// between runs, or by passing fresh hooks per call. Single-Run sessions are unaffected.
	if effectiveHooks != nil && effectiveHooks.Registry == nil {
		effectiveHooks.Registry = NewHookActionRegistry()
	}

	var einoTools []einotool.BaseTool
	toolMap := make(map[string]FullTool)
	if r.registry != nil {
		for _, name := range req.ToolNames {
			if ft, ok := r.registry.GetTool(name); ok {
				einoTools = append(einoTools, adaptFullToEinoTool(ft, effectiveHooks))
				toolMap[name] = ft
			}
		}
	}
	// #6 permission-pipeline: stash FullTool map into ctx，
	// 供 WrapHooks.buildRequest 反查每个工具的 FullTool 实例（取 IsDestructive 等元数据）。
	ctx = WithFullToolMap(ctx, toolMap)

	// 6. 构造 adapter + Eino ReAct Agent
	einoAdapter := &aiserviceAdapter{
		modelName:    "qwen-turbo",
		taskID:       fmt.Sprintf("agent-runner-%d", run.ID),
		systemPrompt: req.SystemPrompt, // #5 skill-system: injected by skill lookup above
	}
	// P2-1 fix: Eino react.NewAgent requires at least one tool; if registry is nil or
	// ToolNames is empty/all-unresolved, einoTools is nil → react.NewAgent returns an
	// error and Run() exits cleanly (status=terminated, reason=model_error).
	// Real ReAct loop integration (#3+ follow-up) should validate ToolNames upfront.
	if len(einoTools) == 0 {
		log.Warnw("AgentRunner.Run: no tools resolved from registry; skipping Eino agent construction",
			"agent_run_id", run.ID, "requested_tools", req.ToolNames)
	}
	einoAgent, err := react.NewAgent(queryCtx, &react.AgentConfig{
		ToolCallingModel: einoAdapter,
		ToolsConfig: compose.ToolsNodeConfig{
			Tools: einoTools,
		},
		MaxStep: 30,
	})
	if err != nil {
		endedAt := time.Now()
		if uerr := r.runStore.UpdateState(ctx, run.ID, "terminated", string(TerminalModelError), &endedAt); uerr != nil {
			log.Warnw("AgentRunner.Run UpdateState failed on NewAgent error", "agent_run_id", run.ID, "error", uerr)
		}
		return nil, fmt.Errorf("AgentRunner.Run NewAgent: %w", err)
	}
	_ = einoAgent // #2 不在 runner 内执行完整 loop，留给 Task 8 集成测试

	// 6. 简化状态机：写终止 messages + UpdateState
	// #2 仅返回"成功创建 run"状态，真实 LLM 调用 + state.Transition 由 Task 8 集成测试覆盖
	st := &LoopState{}
	st.TerminalReason = TerminalCompleted // default

	// If a hook recorded a non-Continue action, propagate it through state.Transition
	// to produce the correct TerminalReason (replacing the hardcoded TerminalCompleted default).
	if effectiveHooks != nil && effectiveHooks.Registry != nil {
		if last := effectiveHooks.Registry.LastAction(); last != HookActionContinue {
			if ev := HookActionToLoopEvent(last); ev != LoopEventInvalid {
				if term, _, isTerminal := st.Transition(ev); isTerminal {
					st.TerminalReason = term
				}
			}
		}
	}

	finalMessages, _ := json.Marshal([]map[string]any{
		{"role": "user", "content": req.Input},
		{"role": "assistant", "content": fmt.Sprintf("[#2 skeleton] received input '%s'", req.Input)},
	})
	if err := r.runStore.WriteTurn(ctx, run.ID, json.RawMessage(finalMessages)); err != nil {
		log.Warnw("AgentRunner.Run WriteTurn failed", "agent_run_id", run.ID, "error", err)
	}

	endedAt := time.Now()
	if err := r.runStore.UpdateState(ctx, run.ID, "terminated", string(st.TerminalReason), &endedAt); err != nil {
		log.Warnw("AgentRunner.Run UpdateState failed", "agent_run_id", run.ID, "error", err)
	}

	// #6 permission-pipeline: 末尾非阻塞收 sink，填 RunResult.PermissionDenial。
	var permDetail *PermissionDenialDetail
	select {
	case permDetail = <-permDenialSink:
	default:
	}

	return &RunResult{
		AgentRunID:       run.ID,
		TerminalReason:   st.TerminalReason,
		FinalOutput:      fmt.Sprintf("[#2 skeleton] received input '%s'", req.Input),
		StepCount:        st.StepCount,
		Duration:         time.Since(startTime),
		SkillVersion:     skillVer,
		PermissionDenial: permDetail,
	}, nil
}

// Cancel 实现（spec §4.1）。
func (r *agentRunner) Cancel(runID uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	cancel, ok := r.cancels[runID]
	if !ok {
		return false
	}
	cancel()
	delete(r.cancels, runID)
	return true
}

func (r *agentRunner) registerCancel(runID uint64, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancels[runID] = cancel
}

func (r *agentRunner) unregisterCancel(runID uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cancels, runID)
}

// #9 compact helpers — wired in by biz.go via WithCompactProvider/WithCompactConfig.
//
// These helpers are intentionally NOT called from Run() in #9. #2 mock Run()
// remains unchanged. #14 ReAct loop will wire the helpers into the real
// agent.Generate fail/retry chain.
//
// Concurrency: caller is responsible for ensuring *LoopState is not shared
// across goroutines. Within a single Run() the loop is single-threaded; across
// independent Run() invocations each has its own LoopState.

// tryPreLLMCompact estimates total tokens across `messages`. When the estimate
// exceeds the configured AutoCompactThreshold, it triggers ReactiveCompact and
// returns (summary || collapsed) as the new message slice. Returns the input
// unchanged with didCompact=false when the threshold is not hit, when the
// CompactProvider is nil, or when ReactiveCompact fails (err is propagated).
//
// Uses ReactiveCompact's finalMessages (the actually-fed slice) for the
// CollapseDrain so the summary and the trailing tail describe the same scope
// (S2 reviewer P1 fix).
func (r *agentRunner) tryPreLLMCompact(ctx context.Context, messages []compact.Message) ([]compact.Message, bool, error) {
	if r.compactProvider == nil {
		return messages, false, nil
	}
	// Use strings.Builder to avoid O(n²) allocation — compact triggers exactly
	// when the message list is large, so naive += would be the worst case.
	var sb strings.Builder
	for _, m := range messages {
		sb.WriteString(m.Content)
		sb.WriteByte('\n')
	}
	tokens := compact.EstimateTokens(sb.String())
	if tokens < r.compactConfig.AutoCompactThreshold {
		return messages, false, nil
	}
	result, finalMessages, err := compact.ReactiveCompact(ctx, r.compactProvider, messages, r.compactConfig)
	if err != nil {
		return messages, false, err
	}
	collapsed := compact.CollapseDrain(finalMessages, r.compactConfig.PTLCollapseKeepTurns)
	summary := compact.Message{
		Role:          "system",
		Content:       result.Summary,
		IsCompactMark: true,
	}
	return append([]compact.Message{summary}, collapsed...), true, nil
}

// handlePTLError consumes one st.Transition(LoopEventLLMErrPTL). Returns the
// ContinueReason that the state machine produced plus the new messages slice
// the caller should send to the next LLM attempt; or, when the state machine
// terminated the run, isTerminal=true and the TerminalReason.
//
// The caller MUST NOT call st.Transition again on the returned ContinueReason —
// the helper has already advanced PTLRetries. Calling Transition twice would
// double-count and trip MaxPTLRetries early (S1 reviewer P1 fix).
//
// Step 1 (PTLRetries=1) → CollapseDrain only (no LLM call).
// Step 2 (PTLRetries=2) → ReactiveCompact (real LLM call via provider) and
// then CollapseDrain on the actually-fed message set (S2 reviewer P1 fix).
// Step 3+ (PTLRetries>2) → TerminalPromptTooLong.
func (r *agentRunner) handlePTLError(
	ctx context.Context, st *LoopState, messages []compact.Message,
) (cont ContinueReason, newMessages []compact.Message, isTerminal bool, terminal TerminalReason, err error) {
	term, c, isTerm := st.Transition(LoopEventLLMErrPTL)
	if isTerm {
		return "", nil, true, term, nil
	}
	switch c {
	case ContinueCollapseDrainRetry:
		collapsed := compact.CollapseDrain(messages, r.compactConfig.PTLCollapseKeepTurns)
		return c, collapsed, false, "", nil
	case ContinueReactiveCompactRetry:
		if r.compactProvider == nil {
			return "", nil, true, TerminalPromptTooLong, fmt.Errorf("handlePTLError: nil compactProvider, cannot reactive_compact")
		}
		result, finalMessages, rerr := compact.ReactiveCompact(ctx, r.compactProvider, messages, r.compactConfig)
		if rerr != nil {
			return "", nil, true, TerminalPromptTooLong, rerr
		}
		summary := compact.Message{
			Role:          "system",
			Content:       result.Summary,
			IsCompactMark: true,
		}
		collapsed := compact.CollapseDrain(finalMessages, r.compactConfig.PTLCollapseKeepTurns)
		return c, append([]compact.Message{summary}, collapsed...), false, "", nil
	default:
		return "", nil, true, TerminalModelError, fmt.Errorf("handlePTLError: unexpected continue reason %s", c)
	}
}

// handleMaxOutputError consumes one st.Transition(LoopEventLLMErrMaxOutput).
// Returns the ContinueReason plus the max_tokens the caller should use on the
// next attempt, or isTerminal=true with TerminalErrorMaxBudget once the
// recovery budget is exhausted.
//
// Same no-double-counting rule as handlePTLError (S1 reviewer P1 fix).
//
// Escalate stage (MaxOutputRetries=1) → EscalatedMaxTokens (65536).
// Recovery stage (MaxOutputRetries=2) → currentMaxTokens preserved; the LLM
// should be allowed to exhaust its budget (S1 reviewer P2 fix — recovery does
// not call EscalateMaxTokens).
// Beyond limit (MaxOutputRetries>2) → TerminalErrorMaxBudget.
func (r *agentRunner) handleMaxOutputError(
	ctx context.Context, st *LoopState, currentMaxTokens int,
) (cont ContinueReason, newMaxTokens int, isTerminal bool, terminal TerminalReason) {
	term, c, isTerm := st.Transition(LoopEventLLMErrMaxOutput)
	if isTerm {
		return "", 0, true, term
	}
	switch c {
	case ContinueMaxOutputEscalate:
		return c, compact.EscalateMaxTokens(currentMaxTokens), false, ""
	case ContinueMaxOutputRecovery:
		return c, currentMaxTokens, false, ""
	default:
		return "", 0, true, TerminalModelError
	}
}
