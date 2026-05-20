package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"gorm.io/datatypes"

	"numind-server/internal/numind/store"
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
}

// RunResult 是 AgentRunner.Run 的输出。
type RunResult struct {
	AgentRunID     uint64
	TerminalReason TerminalReason
	FinalOutput    string
	StepCount      int
	Duration       time.Duration
}

// AgentRunner 是 Agent 运行时主接口（蓝本 §4.1.9）。
type AgentRunner interface {
	Run(ctx context.Context, req RunRequest) (*RunResult, error)
	Cancel(runID uint64) bool
}

type agentRunner struct {
	runStore     store.IAgentRunStore
	registry     AgentToolRegistry
	cancels      map[uint64]context.CancelFunc
	mu           sync.Mutex
	defaultHooks *RunHooks // #4 sandbox-integration: wired by biz.go via WithDefaultHooks
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

// NewAgentRunner 工厂。
func NewAgentRunner(runStore store.IAgentRunStore, registry AgentToolRegistry, opts ...RunnerOption) AgentRunner {
	r := &agentRunner{
		runStore: runStore,
		registry: registry,
		cancels:  make(map[uint64]context.CancelFunc),
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

	// 4. 从 registry 装配 Eino 工具列表
	// #4 sandbox-integration: 选 effectiveHooks — RunRequest.Hooks 优先，
	// 否则 r.defaultHooks（biz.go wire SandboxHookManager.AsRunHooks()）。
	effectiveHooks := req.Hooks
	if effectiveHooks == nil {
		effectiveHooks = r.defaultHooks
	}

	// M10: auto-inject Registry if hooks exist but Registry was not provided by caller.
	// This ensures adapter Record() calls are always wired without requiring callers to know about Registry.
	if effectiveHooks != nil && effectiveHooks.Registry == nil {
		effectiveHooks.Registry = NewHookActionRegistry()
	}

	var einoTools []einotool.BaseTool
	if r.registry != nil {
		for _, name := range req.ToolNames {
			if ft, ok := r.registry.GetTool(name); ok {
				einoTools = append(einoTools, adaptFullToEinoTool(ft, effectiveHooks))
			}
		}
	}

	// 5. 构造 adapter + Eino ReAct Agent
	einoAdapter := &aiserviceAdapter{
		modelName: "qwen-turbo",
		taskID:    fmt.Sprintf("agent-runner-%d", run.ID),
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

	// M10: if a hook recorded a non-Continue action, propagate it through state.Transition
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

	return &RunResult{
		AgentRunID:     run.ID,
		TerminalReason: st.TerminalReason,
		FinalOutput:    fmt.Sprintf("[#2 skeleton] received input '%s'", req.Input),
		StepCount:      st.StepCount,
		Duration:       time.Since(startTime),
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
