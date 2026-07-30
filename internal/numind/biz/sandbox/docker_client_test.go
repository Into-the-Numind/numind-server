package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"numind-server/internal/numind/sandboxbroker"
)

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
		"sleep infinity",
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

func TestDockerCopyPathCanonicalization(t *testing.T) {
	for _, allowed := range []string{
		"/workdir/task.py",
		"/workdir/input/data.csv",
		"/skills/document-system/SKILL.md",
	} {
		got, err := canonicalDockerCopyInPath(allowed)
		if err != nil || got != allowed {
			t.Fatalf("canonicalDockerCopyInPath(%q) = %q, %v", allowed, got, err)
		}
	}
	for _, denied := range []string{
		"relative",
		"/etc/passwd",
		"/workdir/../etc/passwd",
		"/skills/document-system",
		"/skills/bad=label/SKILL.md",
	} {
		if _, err := canonicalDockerCopyInPath(denied); !errors.Is(err, ErrSandboxPolicyDenied) {
			t.Fatalf("canonicalDockerCopyInPath(%q) err = %v", denied, err)
		}
	}
}

func TestDockerExecCaptureAppliesCombinedOutputCeiling(t *testing.T) {
	cmd := exec.Command(
		"/bin/sh",
		"-c",
		"printf '1234567890'; printf 'abcdefghij' >&2; while :; do printf x; done",
	)
	stdout, stderr, _, exceeded, err := runWithCapture(cmd, 15)
	if err != nil {
		t.Fatalf("runWithCapture: %v", err)
	}
	if !exceeded {
		t.Fatal("combined stdout/stderr ceiling did not trigger")
	}
	if len(stdout)+len(stderr) != 15 {
		t.Fatalf("captured %d bytes; want exactly 15", len(stdout)+len(stderr))
	}
}

func TestDockerExecCapturePreservesSeparateStreamsAndExitCode(t *testing.T) {
	cmd := exec.Command(
		"/bin/sh",
		"-c",
		"printf stdout; printf stderr >&2; exit 7",
	)
	stdout, stderr, exitCode, exceeded, err := runWithCapture(
		cmd,
		sandboxbroker.MaxExecOutputBytes,
	)
	if err != nil || exceeded || exitCode != 7 ||
		stdout != "stdout" || stderr != "stderr" {
		t.Fatalf(
			"stdout=%q stderr=%q exit=%d exceeded=%v err=%v",
			stdout,
			stderr,
			exitCode,
			exceeded,
			err,
		)
	}
}

func TestDockerLimitedTextBufferDoesNotGrowPastLimit(t *testing.T) {
	buffer := newLimitedTextBuffer(5)
	if _, err := buffer.Write(bytes.Repeat([]byte("x"), 1024)); err != nil {
		t.Fatal(err)
	}
	if got := buffer.String(); got != "xxxxx" {
		t.Fatalf("buffer = %q; want five bytes", got)
	}
}
