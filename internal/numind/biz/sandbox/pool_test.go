package sandbox

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestNewPool_DisabledBackend(t *testing.T) {
	cfg := DefaultSandboxConfig // Backend = disabled
	p := NewPool(cfg, nil, nil)
	_, err := p.Borrow(context.Background())
	if !errors.Is(err, ErrSandboxDisabled) {
		t.Errorf("Borrow on disabled pool = %v; want ErrSandboxDisabled", err)
	}
	if p.Size() != 0 {
		t.Errorf("Size on disabled pool = %d; want 0", p.Size())
	}
	if p.DockerClient() != nil {
		t.Errorf("DockerClient on disabled pool with nil dc should be nil")
	}
	if err := p.Close(); err != nil {
		t.Errorf("Close on disabled pool err = %v", err)
	}
}

func TestNewPool_DockerBackendWarmupBorrowReturn(t *testing.T) {
	cfg := DefaultSandboxConfig
	cfg.Backend = BackendDocker
	cfg.PoolMin = 2
	cfg.PoolMaxWaitMs = 5000
	mock := NewMockDockerClient()
	p := NewPool(cfg, mock, nil)
	t.Cleanup(func() { _ = p.Close() })

	// Wait briefly for the warm-up to complete.
	waitForSize(t, p, 2, 2*time.Second)

	sess, err := p.Borrow(context.Background())
	if err != nil {
		t.Fatalf("Borrow err = %v", err)
	}
	if sess.ContainerID == "" {
		t.Error("Borrow returned empty ContainerID")
	}
	if p.DockerClient() == nil {
		t.Error("DockerClient on docker pool should not be nil")
	}

	// Pool size should be 1 (one borrowed)
	if p.Size() != 1 {
		t.Errorf("after one Borrow Size = %d; want 1", p.Size())
	}

	// Return — destroys + signals spawn
	if err := p.Return(sess, 0, ""); err != nil {
		t.Errorf("Return err = %v", err)
	}

	// Second Return on same session → ErrSessionReturned
	if err := p.Return(sess, 0, ""); !errors.Is(err, ErrSessionReturned) {
		t.Errorf("double Return = %v; want ErrSessionReturned", err)
	}

	// Pool re-fills to size 2 asynchronously.
	waitForSize(t, p, 2, 2*time.Second)
}

func TestPool_BorrowExhaustedTimeout(t *testing.T) {
	cfg := DefaultSandboxConfig
	cfg.Backend = BackendDocker
	cfg.PoolMin = 0 // no warm-up
	cfg.PoolMaxWaitMs = 50

	mock := NewMockDockerClient()
	// Make Spawn fail so warm pool stays empty
	mock.SpawnErr = errors.New("simulated spawn failure")
	p := NewPool(cfg, mock, nil)
	t.Cleanup(func() { _ = p.Close() })

	start := time.Now()
	_, err := p.Borrow(context.Background())
	dur := time.Since(start)
	if !errors.Is(err, ErrPoolExhausted) {
		t.Errorf("Borrow err = %v; want ErrPoolExhausted", err)
	}
	if dur < 40*time.Millisecond {
		t.Errorf("Borrow returned too fast: %v; expected ≥ ~50ms wait", dur)
	}
}

func TestPool_BorrowCtxCanceled(t *testing.T) {
	cfg := DefaultSandboxConfig
	cfg.Backend = BackendDocker
	cfg.PoolMin = 0
	cfg.PoolMaxWaitMs = 60000 // long wait — relies on ctx cancel

	mock := NewMockDockerClient()
	mock.SpawnErr = errors.New("never spawn")
	p := NewPool(cfg, mock, nil)
	t.Cleanup(func() { _ = p.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := p.Borrow(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Borrow err = %v; want context.DeadlineExceeded", err)
	}
}

func TestPool_ReturnNilSession(t *testing.T) {
	cfg := DefaultSandboxConfig
	cfg.Backend = BackendDocker
	cfg.PoolMin = 0
	mock := NewMockDockerClient()
	p := NewPool(cfg, mock, nil)
	t.Cleanup(func() { _ = p.Close() })

	if err := p.Return(nil, 0, ""); err != nil {
		t.Errorf("Return(nil) err = %v; want nil (no-op)", err)
	}
}

func TestPool_ConcurrentBorrowReturn_RaceDetector(t *testing.T) {
	cfg := DefaultSandboxConfig
	cfg.Backend = BackendDocker
	cfg.PoolMin = 5
	cfg.PoolMaxWaitMs = 2000

	mock := NewMockDockerClient()
	p := NewPool(cfg, mock, nil)
	t.Cleanup(func() { _ = p.Close() })

	waitForSize(t, p, 5, 2*time.Second)

	const goroutines = 10
	const opsPerGoroutine = 20

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				sess, err := p.Borrow(context.Background())
				if err != nil {
					// Tolerate transient exhaustion under heavy contention
					continue
				}
				// Simulate a tiny exec
				time.Sleep(1 * time.Millisecond)
				_ = p.Return(sess, 0, "")
			}
		}()
	}
	wg.Wait()
}

func TestPool_Close_DestroysWarmContainers(t *testing.T) {
	cfg := DefaultSandboxConfig
	cfg.Backend = BackendDocker
	cfg.PoolMin = 3

	mock := NewMockDockerClient()
	p := NewPool(cfg, mock, nil)
	waitForSize(t, p, 3, 2*time.Second)
	beforeAlive := mock.CountAliveContainers()
	if beforeAlive < 3 {
		t.Errorf("before Close: alive = %d; want ≥ 3", beforeAlive)
	}
	if err := p.Close(); err != nil {
		t.Errorf("Close err = %v", err)
	}
	// Allow worker exit
	time.Sleep(50 * time.Millisecond)
	// Most warm containers should now be destroyed by Close. (Worker may
	// have a half-spawned one in flight; we accept ≤ 1 leftover.)
	afterAlive := mock.CountAliveContainers()
	if afterAlive > 1 {
		t.Errorf("after Close: alive = %d; want ≤ 1", afterAlive)
	}
}

func TestPool_Close_Idempotent(t *testing.T) {
	cfg := DefaultSandboxConfig
	cfg.Backend = BackendDocker
	cfg.PoolMin = 0
	mock := NewMockDockerClient()
	p := NewPool(cfg, mock, nil)
	if err := p.Close(); err != nil {
		t.Errorf("first Close err = %v", err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("second Close err = %v; want nil (idempotent)", err)
	}
}

// ===========================================================================
// runner.go tests
// ===========================================================================

func TestExecCommand_DelegatesToDockerClient(t *testing.T) {
	mock := NewMockDockerClient()
	id, _ := mock.Spawn(context.Background(), SpawnConfig{ImageTag: "python:3.11-slim"})
	sess := &Session{
		ContainerID: id,
		Config:      DefaultSandboxConfig,
		BorrowedAt:  time.Now(),
	}
	res, err := ExecCommand(context.Background(), sess, "echo hello", mock)
	if err != nil {
		t.Fatalf("ExecCommand err = %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d; want 0", res.ExitCode)
	}
	if res.Stdout == "" {
		t.Errorf("Stdout empty; want mock echo output")
	}
}

func TestExecCommand_NilSession(t *testing.T) {
	_, err := ExecCommand(context.Background(), nil, "echo", NewMockDockerClient())
	if !errors.Is(err, ErrSandboxDisabled) {
		t.Errorf("nil session = %v; want ErrSandboxDisabled", err)
	}
}

func TestWriteFile_CopiesIntoContainer(t *testing.T) {
	mock := NewMockDockerClient()
	id, _ := mock.Spawn(context.Background(), SpawnConfig{ImageTag: "python:3.11-slim"})
	sess := &Session{
		ContainerID: id,
		Config:      DefaultSandboxConfig,
		BorrowedAt:  time.Now(),
	}
	content := []byte("print('hello')")
	if err := WriteFile(context.Background(), sess, "run.py", content, mock); err != nil {
		t.Fatalf("WriteFile err = %v", err)
	}
	if got, ok := mock.CopiedFiles["/workdir/run.py"]; !ok {
		t.Error("WriteFile did not record file in MockDockerClient.CopiedFiles")
	} else if string(got) != string(content) {
		t.Errorf("WriteFile content = %q; want %q", got, content)
	}
}

func TestWriteFile_NilSession_ReturnsErrSandboxDisabled(t *testing.T) {
	if err := WriteFile(context.Background(), nil, "/x", []byte{}, NewMockDockerClient()); !errors.Is(err, ErrSandboxDisabled) {
		t.Errorf("WriteFile(nil sess) = %v; want ErrSandboxDisabled", err)
	}
}

func TestReadFile_ReadsFromContainer(t *testing.T) {
	mock := NewMockDockerClient()
	id, _ := mock.Spawn(context.Background(), SpawnConfig{ImageTag: "python:3.11-slim"})
	sess := &Session{
		ContainerID: id,
		Config:      DefaultSandboxConfig,
		BorrowedAt:  time.Now(),
	}
	// Pre-register the exec result that cat /workdir/result.json would produce.
	mock.RegisterExecResult(id, []string{"cat", "/workdir/result.json"}, ExecResult{
		Stdout:   `{"ok":true}`,
		ExitCode: 0,
	})
	data, err := ReadFile(context.Background(), sess, "result.json", mock)
	if err != nil {
		t.Fatalf("ReadFile err = %v", err)
	}
	if string(data) != `{"ok":true}` {
		t.Errorf("ReadFile data = %q; want {\"ok\":true}", string(data))
	}
}

func TestReadFile_NilSession_ReturnsErrSandboxDisabled(t *testing.T) {
	if _, err := ReadFile(context.Background(), nil, "/x", NewMockDockerClient()); !errors.Is(err, ErrSandboxDisabled) {
		t.Errorf("ReadFile(nil sess) = %v; want ErrSandboxDisabled", err)
	}
}

// ===========================================================================
// helpers
// ===========================================================================

func waitForSize(t *testing.T, p Pool, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if p.Size() >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Pool size never reached %d (final=%d)", want, p.Size())
}
