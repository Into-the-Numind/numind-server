package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"numind-server/internal/numind/sandboxbroker"

	"golang.org/x/sys/unix"
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

func TestDockerCopyControlledCLIBehavior(t *testing.T) {
	root := resolvedTestTempDir(t)
	if err := os.MkdirAll(filepath.Join(root, "input"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "output"), 0o700); err != nil {
		t.Fatal(err)
	}
	installCopyHelperDocker(t, root)
	client := &dockerCLIClient{logger: nopLogger{}}

	input := io.NopCloser(strings.NewReader("input-data"))
	if err := client.CopyToContainer(
		context.Background(),
		"container-1",
		"/workdir/input/source.txt",
		input,
	); err != nil {
		t.Fatalf("CopyToContainer: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(root, "input", "source.txt"))
	if err != nil || string(body) != "input-data" {
		t.Fatalf("copied input = %q err=%v", body, err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "output", "report.txt"),
		[]byte("report"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "output", "--checkpoint=1"),
		[]byte("literal filename"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	destination := resolvedTestTempDir(t)
	if err := client.CopyFromContainer(
		context.Background(),
		"container-1",
		"/workdir/output/.",
		destination,
	); err != nil {
		t.Fatalf("CopyFromContainer: %v", err)
	}
	body, err = os.ReadFile(filepath.Join(destination, "report.txt"))
	if err != nil || string(body) != "report" {
		t.Fatalf("copied output = %q err=%v", body, err)
	}
	body, err = os.ReadFile(filepath.Join(destination, "--checkpoint=1"))
	if err != nil || string(body) != "literal filename" {
		t.Fatalf("option-like output = %q err=%v", body, err)
	}
	if err := client.CopyFromContainer(
		context.Background(),
		"container-1",
		"/workdir/output/missing.txt",
		resolvedTestTempDir(t),
	); err != nil {
		t.Fatalf("missing CopyFromContainer should be empty: %v", err)
	}
}

func TestDockerCopyControlledCLIRejectsContainerSymlinkEscape(t *testing.T) {
	root := resolvedTestTempDir(t)
	victim := resolvedTestTempDir(t)
	if err := os.Symlink(victim, filepath.Join(root, "input")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "output"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(root, "output", "linked")); err != nil {
		t.Fatal(err)
	}
	installCopyHelperDocker(t, root)
	client := &dockerCLIClient{logger: nopLogger{}}

	err := client.CopyToContainer(
		context.Background(),
		"container-1",
		"/workdir/input/escape.txt",
		strings.NewReader("no"),
	)
	if err == nil {
		t.Fatal("CopyToContainer followed a container ancestor symlink")
	}
	if _, statErr := os.Stat(filepath.Join(victim, "escape.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("copy-in escaped through symlink: %v", statErr)
	}

	err = client.CopyFromContainer(
		context.Background(),
		"container-1",
		"/workdir/output/.",
		resolvedTestTempDir(t),
	)
	if err == nil {
		t.Fatal("CopyFromContainer accepted a container output symlink")
	}

	if err := os.Remove(filepath.Join(root, "output", "linked")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(root, "output", "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = client.CopyFromContainer(
		context.Background(),
		"container-1",
		"/workdir/output/.",
		resolvedTestTempDir(t),
	)
	if err == nil {
		t.Fatal("CopyFromContainer accepted a container FIFO")
	}

	if err := os.Remove(filepath.Join(root, "output", "fifo")); err != nil {
		t.Fatal(err)
	}
	hardlinkVictim := filepath.Join(root, "hardlink-victim")
	if err := os.WriteFile(hardlinkVictim, []byte("no"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(hardlinkVictim, filepath.Join(root, "output", "hardlink")); err != nil {
		t.Fatal(err)
	}
	err = client.CopyFromContainer(
		context.Background(),
		"container-1",
		"/workdir/output/.",
		resolvedTestTempDir(t),
	)
	if err == nil {
		t.Fatal("CopyFromContainer accepted a container hardlink")
	}
}

func TestDockerCopyControlledCLILimitsAndNoOverwrite(t *testing.T) {
	root := resolvedTestTempDir(t)
	if err := os.Mkdir(filepath.Join(root, "input"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "output"), 0o700); err != nil {
		t.Fatal(err)
	}
	installCopyHelperDocker(t, root)
	client := &dockerCLIClient{logger: nopLogger{}}

	if err := os.WriteFile(
		filepath.Join(root, "input", "existing.txt"),
		[]byte("keep"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	err := client.CopyToContainer(
		context.Background(),
		"container-1",
		"/workdir/input/existing.txt",
		strings.NewReader("replace"),
	)
	if err == nil {
		t.Fatal("CopyToContainer overwrote an existing file")
	}
	body, readErr := os.ReadFile(filepath.Join(root, "input", "existing.txt"))
	if readErr != nil || string(body) != "keep" {
		t.Fatalf("existing input changed to %q err=%v", body, readErr)
	}

	err = client.copyToContainer(
		context.Background(),
		"container-1",
		"/workdir/input/large.txt",
		strings.NewReader("123456"),
		5,
	)
	if !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("copy-in limit err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "input", "large.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("over-limit copy published a final file: %v", statErr)
	}

	if err := os.WriteFile(
		filepath.Join(root, "output", "large.txt"),
		[]byte("123456"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	err = client.copyFromContainer(
		context.Background(),
		"container-1",
		"/workdir/output/.",
		resolvedTestTempDir(t),
		sandboxbroker.StreamLimits{MaxSingleBytes: 5, MaxTotalBytes: 5, MaxFiles: 1},
	)
	if !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("copy-out limit err = %v", err)
	}
}

func TestDockerCopyControlledCLICancellationStopsBlockedInput(t *testing.T) {
	root := resolvedTestTempDir(t)
	if err := os.Mkdir(filepath.Join(root, "input"), 0o700); err != nil {
		t.Fatal(err)
	}
	installCopyHelperDocker(t, root)
	client := &dockerCLIClient{logger: nopLogger{}}
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = writer.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- client.CopyToContainer(
			ctx,
			"container-1",
			"/workdir/input/blocked.txt",
			reader,
		)
	}()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled CopyToContainer err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled CopyToContainer remained blocked")
	}
	if _, statErr := os.Stat(filepath.Join(root, "input", "blocked.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("canceled copy published a final file: %v", statErr)
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

func installCopyHelperDocker(t *testing.T, root string) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required for the fixed Sandbox copy helper")
	}
	wrapper := filepath.Join(resolvedTestTempDir(t), "docker")
	script := `#!/bin/sh
set -eu
[ "$1" = "exec" ]
shift
if [ "${1:-}" = "-i" ]; then
  shift
fi
shift
python_bin="$1"
shift
dash_c="$1"
shift
program="$1"
shift
shift
exec "$python_bin" "$dash_c" "$program" "$NUMIND_FAKE_CONTAINER_ROOT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	previous := dockerBinary
	dockerBinary = wrapper
	t.Cleanup(func() { dockerBinary = previous })
	t.Setenv("NUMIND_FAKE_CONTAINER_ROOT", root)
}

func resolvedTestTempDir(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
