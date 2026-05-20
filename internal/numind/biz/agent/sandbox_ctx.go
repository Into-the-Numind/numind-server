package agent

import (
	"context"
	"sync"

	"numind-server/internal/numind/biz/sandbox"
)

// runIDCtxKey is an unexported type used as the ctx.Value key for the
// agent run ID — keeps the value private to this package (per Go ctx
// best practice).
type runIDCtxKey struct{}

// WithRunID returns a derived ctx with the agent run ID stored.
// runner.go calls this immediately after r.runStore.Create yields run.ID,
// so all downstream tool calls / hooks can read it via RunIDFromContext.
func WithRunID(ctx context.Context, runID uint64) context.Context {
	return context.WithValue(ctx, runIDCtxKey{}, runID)
}

// RunIDFromContext returns the agent run ID stored in ctx, or 0 if absent.
// (Hooks treat 0 as "skip sandbox audit, fall through Continue".)
func RunIDFromContext(ctx context.Context) uint64 {
	if v, ok := ctx.Value(runIDCtxKey{}).(uint64); ok {
		return v
	}
	return 0
}

// ===========================================================================
// Default hook manager (process-wide singleton)
// ===========================================================================
//
// bash_exec.Execute and the hooks (PreToolCall / PostToolCall) need to share
// state across tool-call lifecycle:
//   - Pre: pool.Borrow + audit row Create + sandbox session stash
//   - Execute (bash_exec): look up the borrowed sandbox session + dc
//   - Post: pool.Return + audit row UpdateState
//
// We cannot thread the manager through every call site (FullTool.Execute is
// a fixed interface). Instead, biz.go calls SetDefaultHookManager once at
// startup; bash_exec.Execute reads via sandboxSessionForCurrentCall and
// dockerClientForCurrentCall. Both lookups are nil-safe (return nil if the
// manager hasn't been set yet — Tasks 8+10 ordering).

var (
	defaultHookManager   *SandboxHookManager
	defaultHookManagerMu sync.RWMutex
)

// SetDefaultHookManager installs the process-wide SandboxHookManager.
// Called once during biz.NewBiz. Safe to call concurrently with reads;
// callers that want to swap the manager (e.g., tests) should treat
// concurrent Execute calls as in-flight and wait before swap.
func SetDefaultHookManager(m *SandboxHookManager) {
	defaultHookManagerMu.Lock()
	defer defaultHookManagerMu.Unlock()
	defaultHookManager = m
}

// DefaultHookManager returns the currently-installed manager, or nil.
// Exposed for tests + biz.go wire diagnostics.
func DefaultHookManager() *SandboxHookManager {
	defaultHookManagerMu.RLock()
	defer defaultHookManagerMu.RUnlock()
	return defaultHookManager
}

// sandboxSessionForCurrentCall returns the sandbox.Session borrowed by
// the current bash_exec PreToolCall, or nil if absent.
//
// bash_exec.Execute uses this to find its container without taking dc /
// sess through factory wires.
func sandboxSessionForCurrentCall(ctx context.Context, toolName string) *sandbox.Session {
	m := DefaultHookManager()
	if m == nil {
		return nil
	}
	runID := RunIDFromContext(ctx)
	if runID == 0 {
		return nil
	}
	return m.SandboxSessionFor(runID, toolName)
}

// dockerClientForCurrentCall returns the sandbox.DockerClient for the
// current call, or nil. bash_exec.Execute uses this to pass dc into
// sandbox.ExecCommand.
func dockerClientForCurrentCall(_ context.Context) sandbox.DockerClient {
	m := DefaultHookManager()
	if m == nil {
		return nil
	}
	return m.DockerClient()
}
