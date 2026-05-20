package sandbox

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// MockDockerClient is an in-memory DockerClient impl for unit tests that
// records spawns / execs and returns canned results. Exported so other
// packages (factory_sandbox_hooks_test, etc.) can reuse it.
type MockDockerClient struct {
	mu          sync.Mutex
	nextID      atomic.Uint64
	containers  map[string]string // id → image_tag
	execResults map[string]ExecResult
	// hooks for individual call paths
	SpawnErr   error
	ExecErr    error
	DestroyErr error
}

// NewMockDockerClient returns a fresh MockDockerClient.
func NewMockDockerClient() *MockDockerClient {
	return &MockDockerClient{
		containers:  make(map[string]string),
		execResults: make(map[string]ExecResult),
	}
}

var _ DockerClient = (*MockDockerClient)(nil)

func (m *MockDockerClient) Spawn(_ context.Context, cfg SpawnConfig) (string, error) {
	if m.SpawnErr != nil {
		return "", m.SpawnErr
	}
	id := fmt.Sprintf("mock-%d", m.nextID.Add(1))
	m.mu.Lock()
	m.containers[id] = cfg.ImageTag
	m.mu.Unlock()
	return id, nil
}

func (m *MockDockerClient) Exec(_ context.Context, containerID string, cmd []string, _ ExecOpts) (ExecResult, error) {
	if m.ExecErr != nil {
		return ExecResult{}, m.ExecErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.containers[containerID]; !ok {
		return ExecResult{}, fmt.Errorf("container not found: %s", containerID)
	}
	// Pre-registered canned result keyed by command joined
	key := containerID + "|" + strings.Join(cmd, " ")
	if r, ok := m.execResults[key]; ok {
		return r, nil
	}
	// Default: echo back the command in stdout, exit 0
	return ExecResult{
		Stdout:   "mock-stdout: " + strings.Join(cmd, " "),
		Stderr:   "",
		ExitCode: 0,
		Duration: 5 * time.Millisecond,
	}, nil
}

func (m *MockDockerClient) Destroy(_ context.Context, containerID string) error {
	if m.DestroyErr != nil {
		return m.DestroyErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.containers, containerID)
	return nil
}

func (m *MockDockerClient) Inspect(_ context.Context, containerID string) (InspectResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.containers[containerID]; !ok {
		return InspectResult{Status: "exited", ExitCode: 0}, nil
	}
	return InspectResult{Status: "running", ExitCode: 0}, nil
}

// RegisterExecResult lets a test pre-can a particular (containerID, cmd) tuple.
func (m *MockDockerClient) RegisterExecResult(containerID string, cmd []string, result ExecResult) {
	key := containerID + "|" + strings.Join(cmd, " ")
	m.mu.Lock()
	defer m.mu.Unlock()
	m.execResults[key] = result
}

// CountAliveContainers returns the # of containers spawned but not destroyed.
func (m *MockDockerClient) CountAliveContainers() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.containers)
}

// ===========================================================================
// Tests
// ===========================================================================

func TestMockDockerClient_LifecycleHappyPath(t *testing.T) {
	m := NewMockDockerClient()
	ctx := context.Background()

	id, err := m.Spawn(ctx, SpawnConfig{ImageTag: "python:3.11-slim"})
	if err != nil {
		t.Fatalf("Spawn err = %v", err)
	}
	if id == "" {
		t.Fatal("Spawn returned empty id")
	}

	res, err := m.Exec(ctx, id, []string{"/bin/sh", "-c", "echo hi"}, ExecOpts{})
	if err != nil {
		t.Fatalf("Exec err = %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("default ExitCode = %d; want 0", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "echo hi") {
		t.Errorf("default Stdout %q should echo cmd", res.Stdout)
	}

	insp, err := m.Inspect(ctx, id)
	if err != nil {
		t.Fatalf("Inspect err = %v", err)
	}
	if insp.Status != "running" {
		t.Errorf("Inspect Status = %q; want running", insp.Status)
	}

	if err := m.Destroy(ctx, id); err != nil {
		t.Fatalf("Destroy err = %v", err)
	}

	if m.CountAliveContainers() != 0 {
		t.Errorf("after Destroy alive=%d; want 0", m.CountAliveContainers())
	}

	// Exec on destroyed container → error
	if _, err := m.Exec(ctx, id, []string{"echo"}, ExecOpts{}); err == nil {
		t.Errorf("Exec on destroyed container should error")
	}
}

func TestMockDockerClient_RegisterExecResultOverridesDefault(t *testing.T) {
	m := NewMockDockerClient()
	id, _ := m.Spawn(context.Background(), SpawnConfig{ImageTag: "python:3.11-slim"})
	cmd := []string{"/bin/sh", "-c", "exit 42"}
	m.RegisterExecResult(id, cmd, ExecResult{Stdout: "", Stderr: "boom", ExitCode: 42})

	res, _ := m.Exec(context.Background(), id, cmd, ExecOpts{})
	if res.ExitCode != 42 {
		t.Errorf("ExitCode = %d; want 42", res.ExitCode)
	}
	if res.Stderr != "boom" {
		t.Errorf("Stderr = %q; want boom", res.Stderr)
	}
}

func TestMockDockerClient_DestroyIdempotent(t *testing.T) {
	m := NewMockDockerClient()
	id, _ := m.Spawn(context.Background(), SpawnConfig{ImageTag: "python:3.11-slim"})
	if err := m.Destroy(context.Background(), id); err != nil {
		t.Fatalf("first Destroy err = %v", err)
	}
	if err := m.Destroy(context.Background(), id); err != nil {
		// MockDockerClient.Destroy is idempotent (no error on missing container)
		t.Fatalf("second Destroy err = %v; should be idempotent", err)
	}
}

func TestMockDockerClient_SpawnErrPropagates(t *testing.T) {
	m := NewMockDockerClient()
	m.SpawnErr = fmt.Errorf("synthetic spawn failure")
	_, err := m.Spawn(context.Background(), SpawnConfig{})
	if err == nil {
		t.Fatal("expected SpawnErr to propagate")
	}
}

// ===========================================================================
// buildSpawnArgs unit tests (no real docker exec)
// ===========================================================================

func TestBuildSpawnArgs_ContainsKeyFlags(t *testing.T) {
	resetSeccompPathForTesting()
	seccomp, _ := ResolveSeccompPath()
	sc := BuildSpawnConfig(DefaultSandboxConfig, seccomp)

	args := buildSpawnArgs(sc)
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"run", "--detach",
		"--security-opt", "seccomp=",
		"--security-opt", "apparmor=docker-default",
		"--security-opt", "no-new-privileges",
		"--user", "1000:1000",
		"--cap-drop=ALL",
		"--cap-add=NET_BIND_SERVICE",
		"--memory=512m",
		"--cpus=1.0",
		"--pids-limit=64",
		"--read-only",
		"--tmpfs",
		"/workdir:size=512m,uid=1000,gid=1000",
		"--network=none",
		"python:3.11-slim",
		"/bin/sh",
		"sleep 600",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("buildSpawnArgs missing %q in: %s", want, joined)
		}
	}
}

func TestBuildSpawnArgs_NoPidsLimitWhenZero(t *testing.T) {
	sc := SpawnConfig{ImageTag: "x", PIDsLimit: 0, Detached: false}
	args := buildSpawnArgs(sc)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--pids-limit") {
		t.Errorf("--pids-limit should be absent when PIDsLimit=0; got %s", joined)
	}
}

func TestBuildSpawnArgs_NoSecurityOptsWhenEmpty(t *testing.T) {
	sc := SpawnConfig{ImageTag: "x"}
	args := buildSpawnArgs(sc)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--security-opt") {
		t.Errorf("--security-opt should be absent when SecurityOpts empty; got %s", joined)
	}
}
