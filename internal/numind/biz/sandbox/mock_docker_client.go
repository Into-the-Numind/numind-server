package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MockDockerClient is an in-memory DockerClient impl for unit tests across
// packages (sandbox, agent, biz). Lives in a non-_test.go file so other
// packages can import and reuse it (Go's test packages can't expose
// symbols across package boundaries).
//
// Production code MUST NOT use MockDockerClient. It is only intended for
// test fixtures; the real impl is dockerCLIClient.
type MockDockerClient struct {
	mu          sync.Mutex
	nextID      atomic.Uint64
	containers  map[string]string // id → image_tag
	execResults map[string]ExecResult

	// CopiedFiles tracks files written via CopyToContainer (dstPath → bytes).
	CopiedFiles map[string][]byte
	// CopiedFromDirs tracks (containerID+srcPath → hostDstDir) calls for verification.
	CopiedFromDirs []string

	// SpawnErr, ExecErr, DestroyErr: when non-nil, the corresponding
	// method returns this error instead of the normal mocked behavior.
	SpawnErr     error
	ExecErr      error
	DestroyErr   error
	CopyToErr    error
	CopyFromErr  error
	ExecMkdirErr error

	// CopyFromFiles: if non-nil, CopyFromContainer copies these files into hostDstDir.
	// map key is filename, value is content.
	CopyFromFiles map[string][]byte
}

// NewMockDockerClient returns a fresh MockDockerClient with empty state.
func NewMockDockerClient() *MockDockerClient {
	return &MockDockerClient{
		containers:    make(map[string]string),
		execResults:   make(map[string]ExecResult),
		CopiedFiles:   make(map[string][]byte),
		CopyFromFiles: make(map[string][]byte),
	}
}

var _ DockerClient = (*MockDockerClient)(nil)

// Spawn records a new container and returns a deterministic "mock-N" ID.
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

// Exec returns a pre-registered result if available, otherwise echoes the
// joined command in stdout with exit code 0.
func (m *MockDockerClient) Exec(_ context.Context, containerID string, cmd []string, _ ExecOpts) (ExecResult, error) {
	if m.ExecErr != nil {
		return ExecResult{}, m.ExecErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.containers[containerID]; !ok {
		return ExecResult{}, fmt.Errorf("container not found: %s", containerID)
	}
	key := containerID + "|" + strings.Join(cmd, " ")
	if r, ok := m.execResults[key]; ok {
		return r, nil
	}
	return ExecResult{
		Stdout:   "mock-stdout: " + strings.Join(cmd, " "),
		Stderr:   "",
		ExitCode: 0,
		Duration: 5 * time.Millisecond,
	}, nil
}

// Destroy is idempotent: deleting a non-existent container is a no-op.
func (m *MockDockerClient) Destroy(_ context.Context, containerID string) error {
	if m.DestroyErr != nil {
		return m.DestroyErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.containers, containerID)
	return nil
}

// Inspect reports running for any container present in the map, otherwise
// exited with code 0.
func (m *MockDockerClient) Inspect(_ context.Context, containerID string) (InspectResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.containers[containerID]; !ok {
		return InspectResult{Status: "exited", ExitCode: 0}, nil
	}
	return InspectResult{Status: "running", ExitCode: 0}, nil
}

// RegisterExecResult pre-cans a particular (containerID, cmd) tuple so
// the next Exec on that key returns the canned result instead of the
// default echo behavior.
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

// CopyToContainer records the bytes written into CopiedFiles[dstPath].
func (m *MockDockerClient) CopyToContainer(_ context.Context, _ string, dstPath string, content io.Reader) error {
	if m.CopyToErr != nil {
		return m.CopyToErr
	}
	data, _ := io.ReadAll(content)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CopiedFiles[dstPath] = data
	return nil
}

// CopyFromContainer writes CopyFromFiles entries into hostDstDir for testing.
func (m *MockDockerClient) CopyFromContainer(_ context.Context, _ string, _ string, hostDstDir string) error {
	if m.CopyFromErr != nil {
		return m.CopyFromErr
	}
	m.mu.Lock()
	files := m.CopyFromFiles
	m.mu.Unlock()

	for filename, data := range files {
		dst := filepath.Join(hostDstDir, filename)
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return fmt.Errorf("mock CopyFromContainer: write %s: %w", dst, err)
		}
	}
	m.mu.Lock()
	m.CopiedFromDirs = append(m.CopiedFromDirs, hostDstDir)
	m.mu.Unlock()
	return nil
}

// ExecMkdir is a no-op in the mock (directories don't exist in memory).
func (m *MockDockerClient) ExecMkdir(_ context.Context, _ string, _ ...string) error {
	return m.ExecMkdirErr
}
