//go:build dockerintegration

// Real-Docker integration tests. Run with:
//
//	go test -tags dockerintegration ./internal/numind/biz/sandbox/...
//
// CI does NOT pass this tag — these tests require a running Docker daemon
// and the python:3.11-slim image pulled locally. They are intentionally
// excluded from the regular `task test` matrix because GitHub Actions
// runners and most dev laptops without Docker would fail.
//
// To run locally:
//
//	docker pull python:3.11-slim
//	go test -tags dockerintegration -v ./internal/numind/biz/sandbox/...
package sandbox

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRealDocker_SpawnDestroy(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires Docker; skipped in -short mode")
	}
	resetSeccompPathForTesting()
	seccomp, err := ResolveSeccompPath()
	if err != nil {
		t.Fatalf("ResolveSeccompPath err = %v", err)
	}
	dc := NewDockerCLIClient(nil)
	cfg := BuildSpawnConfig(DefaultSandboxConfig, seccomp)
	cfg.ImageTag = "python:3.11-slim"

	id, err := dc.Spawn(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Spawn err = %v", err)
	}
	t.Logf("spawned container: %s", id)

	if err := dc.Destroy(context.Background(), id); err != nil {
		t.Fatalf("Destroy err = %v", err)
	}
}

func TestRealDocker_ExecEchoHello(t *testing.T) {
	resetSeccompPathForTesting()
	seccomp, _ := ResolveSeccompPath()
	dc := NewDockerCLIClient(nil)
	cfg := BuildSpawnConfig(DefaultSandboxConfig, seccomp)
	cfg.ImageTag = "python:3.11-slim"

	id, err := dc.Spawn(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Spawn err = %v", err)
	}
	t.Cleanup(func() { _ = dc.Destroy(context.Background(), id) })

	res, err := dc.Exec(
		context.Background(),
		id,
		[]string{"/bin/sh", "-c", "echo hello"},
		ExecOpts{Timeout: 5 * time.Second, Workdir: "/workdir", User: "1000:1000"},
	)
	if err != nil {
		t.Fatalf("Exec err = %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d; want 0", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "hello") {
		t.Errorf("Stdout %q should contain 'hello'", res.Stdout)
	}
}

func TestRealDocker_ExecListWorkdir(t *testing.T) {
	resetSeccompPathForTesting()
	seccomp, _ := ResolveSeccompPath()
	dc := NewDockerCLIClient(nil)
	cfg := BuildSpawnConfig(DefaultSandboxConfig, seccomp)
	cfg.ImageTag = "python:3.11-slim"

	id, err := dc.Spawn(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Spawn err = %v", err)
	}
	t.Cleanup(func() { _ = dc.Destroy(context.Background(), id) })

	res, err := dc.Exec(
		context.Background(),
		id,
		[]string{"/bin/sh", "-c", "ls -la /workdir"},
		ExecOpts{Timeout: 5 * time.Second, Workdir: "/workdir", User: "1000:1000"},
	)
	if err != nil {
		t.Fatalf("Exec err = %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d; want 0", res.ExitCode)
	}
	// /workdir is the tmpfs mount; it should exist (ls returns successfully)
	t.Logf("ls /workdir: stdout=%q stderr=%q", res.Stdout, res.Stderr)
}

func TestRealDocker_ExecPythonPrint(t *testing.T) {
	resetSeccompPathForTesting()
	seccomp, _ := ResolveSeccompPath()
	dc := NewDockerCLIClient(nil)
	cfg := BuildSpawnConfig(DefaultSandboxConfig, seccomp)
	cfg.ImageTag = "python:3.11-slim"

	id, err := dc.Spawn(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Spawn err = %v", err)
	}
	t.Cleanup(func() { _ = dc.Destroy(context.Background(), id) })

	res, err := dc.Exec(
		context.Background(),
		id,
		[]string{"/bin/sh", "-c", "python -c 'print(2+2)'"},
		ExecOpts{Timeout: 5 * time.Second, Workdir: "/workdir", User: "1000:1000"},
	)
	if err != nil {
		t.Fatalf("Exec err = %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d; want 0 (stderr=%q)", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "4") {
		t.Errorf("Stdout %q should contain '4' (python 2+2)", res.Stdout)
	}
}

func TestRealDocker_PoolWarmup5(t *testing.T) {
	cfg := DefaultSandboxConfig
	cfg.Backend = BackendDocker
	cfg.PoolMin = 5
	cfg.PoolMaxWaitMs = 30000
	cfg.ImageTag = "python:3.11-slim"

	dc := NewDockerCLIClient(nil)
	p := NewPool(cfg, dc, nil)
	t.Cleanup(func() { _ = p.Close() })

	// Wait up to 30s for the warmup to complete (5 docker run calls)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if p.Size() >= 5 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if p.Size() < 5 {
		t.Fatalf("warmup never reached 5; final size=%d", p.Size())
	}

	// Borrow → exec → Return → refill
	sess, err := p.Borrow(context.Background())
	if err != nil {
		t.Fatalf("Borrow err = %v", err)
	}
	res, err := ExecCommand(context.Background(), sess, "echo pool-works", dc)
	if err != nil {
		t.Fatalf("ExecCommand err = %v", err)
	}
	if !strings.Contains(res.Stdout, "pool-works") {
		t.Errorf("Stdout %q should contain 'pool-works'", res.Stdout)
	}
	if err := p.Return(sess, 0, ""); err != nil {
		t.Errorf("Return err = %v", err)
	}
}
