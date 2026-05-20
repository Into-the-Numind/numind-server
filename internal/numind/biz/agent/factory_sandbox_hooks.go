package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	einotool "github.com/cloudwego/eino/components/tool"

	"numind-server/internal/numind/biz/sandbox"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

// sandboxBorrow tracks an in-flight sandbox session borrowed by PreToolCall.
// Stored under key=fmt.Sprintf("%d|%s", runID, toolName) so concurrent runs
// + multiple tool calls per run don't collide.
type sandboxBorrow struct {
	sess      *sandbox.Session
	sessionID uint64 // agent_sandbox_session.id
}

// SandboxHookManager owns the cross-(Pre|Execute|Post) state for the
// sandbox-integration RunHooks. One instance per server process; biz.go
// wires SetDefaultHookManager(m) at startup so bash_exec.Execute can
// access the same instance.
type SandboxHookManager struct {
	pool      sandbox.Pool
	sessStore store.IAgentSandboxSessionStore
	borrows   sync.Map // key string → *sandboxBorrow
}

// NewSandboxHookManager constructs the manager.
func NewSandboxHookManager(pool sandbox.Pool, sessStore store.IAgentSandboxSessionStore) *SandboxHookManager {
	return &SandboxHookManager{pool: pool, sessStore: sessStore}
}

// AsRunHooks returns a *RunHooks whose Pre/PostToolCall closures are bound
// to this manager. Multiple hooks instances from the same manager share
// the borrows map (the manager is the source of truth).
func (m *SandboxHookManager) AsRunHooks() *RunHooks {
	return &RunHooks{
		PreToolCall:  m.preToolCall,
		PostToolCall: m.postToolCall,
	}
}

// DockerClient exposes the dc behind the pool — for bash_exec.Execute via
// dockerClientForCurrentCall.
func (m *SandboxHookManager) DockerClient() sandbox.DockerClient {
	if m == nil || m.pool == nil {
		return nil
	}
	return m.pool.DockerClient()
}

// SandboxSessionFor returns the in-flight session for (runID, toolName), or
// nil. Used by bash_exec.Execute via sandboxSessionForCurrentCall.
func (m *SandboxHookManager) SandboxSessionFor(runID uint64, toolName string) *sandbox.Session {
	v, ok := m.borrows.Load(borrowKey(runID, toolName))
	if !ok {
		return nil
	}
	b, ok := v.(*sandboxBorrow)
	if !ok {
		return nil
	}
	return b.sess
}

func borrowKey(runID uint64, toolName string) string {
	return fmt.Sprintf("%d|%s", runID, toolName)
}

// ===========================================================================
// Hook implementations
// ===========================================================================

// preToolCall handles bash_exec lifecycle setup: borrow a sandbox session
// + write a status='running' audit row + stash the borrow under runID|toolName.
// For non-bash_exec tools or runID=0 / pool errors, it short-circuits to
// HookActionContinue without aborting the run.
func (m *SandboxHookManager) preToolCall(ctx context.Context, t einotool.BaseTool, _ string) (HookAction, error) {
	info, err := t.Info(ctx)
	if err != nil {
		log.Warnw("SandboxHook.PreToolCall: tool.Info failed", "error", err)
		return HookActionContinue, nil
	}
	if info.Name != "bash_exec" {
		return HookActionContinue, nil
	}

	runID := RunIDFromContext(ctx)
	if runID == 0 {
		log.Warnw("SandboxHook.PreToolCall: no runID in ctx; bash_exec without runID — skip sandbox audit")
		return HookActionContinue, nil
	}

	sess, err := m.pool.Borrow(ctx)
	if err != nil {
		// Pool exhausted / disabled — let bash_exec.Execute decide friendly error
		log.Warnw("SandboxHook.PreToolCall: Pool.Borrow failed",
			"run_id", runID,
			"error", err)
		return HookActionContinue, nil
	}

	userID, _ := middleware.UserIDFromCtx(ctx)
	arid := runID // copy to a local so we can take an address that lives past the func
	record := &model.AgentSandboxSession{
		UserID:      userID,
		AgentRunID:  &arid,
		ContainerID: sess.ContainerID,
		ImageTag:    sess.ImageTag,
		Status:      "running",
		MemLimitMB:  sess.Config.MemoryLimitMB,
		CPUQuota:    sess.Config.CPUQuota,
		StartedAt:   sess.BorrowedAt,
	}
	if err := m.sessStore.Create(ctx, record); err != nil {
		log.Warnw("SandboxHook.PreToolCall: sessStore.Create failed",
			"run_id", runID,
			"container_id", sess.ContainerID,
			"error", err)
		// Return session to pool to avoid leak
		_ = m.pool.Return(sess, -1, "audit Create failed")
		return HookActionContinue, nil
	}

	m.borrows.Store(borrowKey(runID, info.Name), &sandboxBorrow{
		sess:      sess,
		sessionID: record.ID,
	})
	return HookActionContinue, nil
}

// postToolCall finalises the bash_exec audit row + returns the session to
// the pool. Idempotent: if Pre never stored a borrow (Pool.Borrow failed),
// this just returns HookActionContinue.
func (m *SandboxHookManager) postToolCall(ctx context.Context, t einotool.BaseTool, _ string, execErr error) (HookAction, error) {
	info, err := t.Info(ctx)
	if err != nil {
		log.Warnw("SandboxHook.PostToolCall: tool.Info failed", "error", err)
		return HookActionContinue, nil
	}
	if info.Name != "bash_exec" {
		return HookActionContinue, nil
	}
	runID := RunIDFromContext(ctx)
	if runID == 0 {
		return HookActionContinue, nil
	}
	val, ok := m.borrows.LoadAndDelete(borrowKey(runID, info.Name))
	if !ok {
		// Pre wasn't called (e.g. pool was exhausted) — nothing to do
		return HookActionContinue, nil
	}
	borrow := val.(*sandboxBorrow)

	status := "terminated"
	var exitCode *int
	var errMsg string
	if execErr != nil {
		status = "failed"
		errMsg = execErr.Error()
		ec := -1
		exitCode = &ec
	} else {
		ok0 := 0
		exitCode = &ok0
	}

	if rerr := m.pool.Return(borrow.sess, intDeref(exitCode), errMsg); rerr != nil {
		log.Warnw("SandboxHook.PostToolCall: Pool.Return failed",
			"run_id", runID,
			"container_id", borrow.sess.ContainerID,
			"error", rerr)
		// Continue to update audit row anyway
	}

	now := time.Now()
	if err := m.sessStore.UpdateState(ctx, borrow.sessionID, status, exitCode, errMsg, &now); err != nil {
		log.Warnw("SandboxHook.PostToolCall: sessStore.UpdateState failed",
			"run_id", runID,
			"session_id", borrow.sessionID,
			"error", err)
	}
	return HookActionContinue, nil
}

func intDeref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
