package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"numind-server/internal/numind/biz/sandbox"
)

// ===========================================================================
// Static / metadata
// ===========================================================================

func TestBashExecTool_Name(t *testing.T) {
	tool := &bashExecTool{}
	if tool.Name() != "bash_exec" {
		t.Errorf("unexpected name: %s", tool.Name())
	}
}

func TestBashExecTool_IsDestructive(t *testing.T) {
	if !(&bashExecTool{}).IsDestructive() {
		t.Error("bash_exec should be destructive")
	}
}

func TestBashExecTool_IsReadOnly(t *testing.T) {
	if (&bashExecTool{}).IsReadOnly() {
		t.Error("bash_exec should not be read-only")
	}
}

func TestBashExecTool_InterruptBehavior(t *testing.T) {
	if got := (&bashExecTool{}).InterruptBehavior(); got != "cancel" {
		t.Errorf("InterruptBehavior = %q; want cancel", got)
	}
}

func TestBashExecTool_IsEnabled(t *testing.T) {
	tool := &bashExecTool{}
	if tool.IsEnabled(ToolConfig{EnableSandbox: false}) {
		t.Error("disabled when EnableSandbox=false")
	}
	if !tool.IsEnabled(ToolConfig{EnableSandbox: true}) {
		t.Error("enabled when EnableSandbox=true")
	}
}

// ===========================================================================
// Execute paths
// ===========================================================================

func TestBashExecTool_Execute_ParseError(t *testing.T) {
	tool := &bashExecTool{}
	res, err := tool.Execute(context.Background(), []byte("not json"))
	if err != nil {
		t.Fatalf("Execute err = %v; want nil for parse error", err)
	}
	if !containsErrorString(t, res, "JSON") {
		t.Errorf("parse error should mention JSON; got %s", string(res))
	}
}

func TestBashExecTool_Execute_EmptyCommand(t *testing.T) {
	tool := &bashExecTool{}
	res, err := tool.Execute(context.Background(), []byte(`{"command":""}`))
	if err != nil {
		t.Fatalf("Execute err = %v; want nil", err)
	}
	if !containsErrorString(t, res, "empty") {
		t.Errorf("empty cmd should mention 'empty'; got %s", string(res))
	}
}

func TestBashExecTool_Execute_ValidatorRejects(t *testing.T) {
	tool := &bashExecTool{}
	res, err := tool.Execute(context.Background(), []byte(`{"command":"echo $(whoami)"}`))
	if err != nil {
		t.Fatalf("Execute err = %v; want nil for validator deny", err)
	}
	if !containsErrorString(t, res, "安全策略") {
		t.Errorf("validator deny should mention 安全策略; got %s", string(res))
	}
	if !containsErrorString(t, res, "CommandSubstitution") {
		t.Errorf("validator deny reason should mention CommandSubstitution; got %s", string(res))
	}
}

func TestBashExecTool_Execute_NoSandboxSessionFriendlyError(t *testing.T) {
	// No SetDefaultHookManager → sandboxSessionForCurrentCall returns nil
	SetDefaultHookManager(nil)
	t.Cleanup(func() { SetDefaultHookManager(nil) })
	tool := &bashExecTool{}
	ctx := WithRunID(context.Background(), 100)
	res, err := tool.Execute(ctx, []byte(`{"command":"echo hello"}`))
	if err != nil {
		t.Fatalf("Execute err = %v; want nil for missing session", err)
	}
	if !containsErrorString(t, res, "沙箱当前不可用") {
		t.Errorf("missing session should yield friendly degrade; got %s", string(res))
	}
}

func TestBashExecTool_Execute_HappyPath(t *testing.T) {
	// Wire a mock pool with happy Borrow + a MockDockerClient that returns
	// canned stdout.
	mockDC := sandbox.NewMockDockerClient()
	pool := &mockSandboxPool{dc: mockDC}
	storeM := &mockSandboxStore{}
	m := NewSandboxHookManager(pool, storeM)
	SetDefaultHookManager(m)
	t.Cleanup(func() { SetDefaultHookManager(nil) })

	// Pre-stash a session into the manager (simulating SandboxHook.PreToolCall)
	containerID, _ := mockDC.Spawn(context.Background(), sandbox.SpawnConfig{ImageTag: "python:3.11-slim"})
	sess := &sandbox.Session{
		ContainerID: containerID,
		ImageTag:    "python:3.11-slim",
		Config:      sandbox.DefaultSandboxConfig,
		BorrowedAt:  time.Now(),
	}
	m.borrows.Store(borrowKey(100, "bash_exec"), &sandboxBorrow{sess: sess, sessionID: 1})

	// Pre-can the exec result so we have a deterministic stdout
	mockDC.RegisterExecResult(containerID, []string{"/bin/sh", "-c", "echo hello"}, sandbox.ExecResult{
		Stdout: "hello\n", Stderr: "", ExitCode: 0, Duration: 7 * time.Millisecond,
	})

	tool := &bashExecTool{}
	ctx := WithRunID(context.Background(), 100)
	res, err := tool.Execute(ctx, []byte(`{"command":"echo hello"}`))
	if err != nil {
		t.Fatalf("Execute err = %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(res, &payload); err != nil {
		t.Fatalf("result not JSON: %v (raw=%s)", err, string(res))
	}
	if payload["stdout"] != "hello\n" {
		t.Errorf("stdout = %v; want hello\\n", payload["stdout"])
	}
	if payload["exit_code"].(float64) != 0 {
		t.Errorf("exit_code = %v; want 0", payload["exit_code"])
	}
}

func TestBashExecTool_Execute_DockerClientNil(t *testing.T) {
	// Manager exists but underlying pool has nil dc
	pool := &mockSandboxPool{dc: nil}
	storeM := &mockSandboxStore{}
	m := NewSandboxHookManager(pool, storeM)
	SetDefaultHookManager(m)
	t.Cleanup(func() { SetDefaultHookManager(nil) })

	// Stash a session (won't actually be usable since dc is nil)
	sess := &sandbox.Session{ContainerID: "stub", Config: sandbox.DefaultSandboxConfig}
	m.borrows.Store(borrowKey(100, "bash_exec"), &sandboxBorrow{sess: sess, sessionID: 1})

	tool := &bashExecTool{}
	ctx := WithRunID(context.Background(), 100)
	res, err := tool.Execute(ctx, []byte(`{"command":"echo hi"}`))
	if err != nil {
		t.Fatalf("Execute err = %v", err)
	}
	if !containsErrorString(t, res, "沙箱客户端未初始化") {
		t.Errorf("nil dc should yield specific friendly error; got %s", string(res))
	}
}

func TestBashExecTool_Execute_DockerExecError(t *testing.T) {
	mockDC := sandbox.NewMockDockerClient()
	mockDC.ExecErr = errors.New("synthetic exec failure")
	pool := &mockSandboxPool{dc: mockDC}
	storeM := &mockSandboxStore{}
	m := NewSandboxHookManager(pool, storeM)
	SetDefaultHookManager(m)
	t.Cleanup(func() { SetDefaultHookManager(nil) })

	containerID, _ := mockDC.Spawn(context.Background(), sandbox.SpawnConfig{ImageTag: "python:3.11-slim"})
	sess := &sandbox.Session{
		ContainerID: containerID,
		Config:      sandbox.DefaultSandboxConfig,
		BorrowedAt:  time.Now(),
	}
	m.borrows.Store(borrowKey(100, "bash_exec"), &sandboxBorrow{sess: sess, sessionID: 1})

	tool := &bashExecTool{}
	ctx := WithRunID(context.Background(), 100)
	res, err := tool.Execute(ctx, []byte(`{"command":"echo hi"}`))
	if err != nil {
		t.Fatalf("Execute err = %v; want nil for sandboxed exec error (soft reject)", err)
	}
	if !containsErrorString(t, res, "沙箱执行失败") {
		t.Errorf("result should contain '沙箱执行失败'; got %s", string(res))
	}
}

// ===========================================================================
// Helpers
// ===========================================================================

func containsErrorString(t *testing.T, raw []byte, substr string) bool {
	t.Helper()
	return strings.Contains(string(raw), substr)
}

// Reuse mockSandboxPool / mockSandboxStore from factory_sandbox_hooks_test.go
// (same package = shared test fixtures).

var _ atomic.Int64 // unused alias to silence go vet about empty import warnings if any
