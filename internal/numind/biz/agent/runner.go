package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"gorm.io/datatypes"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/model"
)

// RunRequest 是 AgentRunner.Run 的输入。
type RunRequest struct {
	UserID    uint
	SessionID string
	Input     string
	Tools     []tool.BaseTool
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
	runStore store.IAgentRunStore
	cancels  map[uint64]context.CancelFunc
	mu       sync.Mutex
}

var _ AgentRunner = (*agentRunner)(nil)

// NewAgentRunner 工厂。
func NewAgentRunner(runStore store.IAgentRunStore) AgentRunner {
	return &agentRunner{
		runStore: runStore,
		cancels:  make(map[uint64]context.CancelFunc),
	}
}

// Run 主流程（spec §4.4）。
// 简化：#2 仅跑通 mock + 真实流程基础架构；
// LLM 调用 / Eino agent.Generate 真实集成靠 Task 8 集成测试覆盖。
func (r *agentRunner) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	startTime := time.Now()

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

	// 4. 构造 adapter + Eino ReAct Agent
	einoAdapter := &aiserviceAdapter{
		modelName: "qwen-turbo",
		taskID:    fmt.Sprintf("agent-runner-%d", run.ID),
	}
	einoAgent, err := react.NewAgent(queryCtx, &react.AgentConfig{
		ToolCallingModel: einoAdapter,
		ToolsConfig: compose.ToolsNodeConfig{
			Tools: req.Tools,
		},
		MaxStep: 30,
	})
	if err != nil {
		endedAt := time.Now()
		_ = r.runStore.UpdateState(ctx, run.ID, "terminated", string(TerminalModelError), &endedAt)
		return nil, fmt.Errorf("AgentRunner.Run NewAgent: %w", err)
	}
	_ = einoAgent // #2 不在 runner 内执行完整 loop，留给 Task 8 集成测试

	// 5. 简化状态机：写终止 messages + UpdateState
	// #2 仅返回"成功创建 run"状态，真实 LLM 调用 + state.Transition 由 Task 8 集成测试覆盖
	st := &LoopState{}
	st.TerminalReason = TerminalCompleted

	finalMessages, _ := json.Marshal([]map[string]any{
		{"role": "user", "content": req.Input},
		{"role": "assistant", "content": fmt.Sprintf("[#2 skeleton] received input '%s'", req.Input)},
	})
	_ = r.runStore.WriteTurn(ctx, run.ID, json.RawMessage(finalMessages))

	endedAt := time.Now()
	_ = r.runStore.UpdateState(ctx, run.ID, "terminated", string(st.TerminalReason), &endedAt)

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
