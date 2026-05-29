package sandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
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

// TestBuildSingleFileTar_RoundTrip locks the wire format CopyToContainer puts
// on `docker exec -i tar -xf -`'s stdin: a single regular-file entry whose
// extracted path is the basename passed in. This is the regression test for
// the 2026-05-29 incident where `docker cp` was silently broken for tmpfs
// mounts in Docker 28.x — CopyToContainer was rewritten to pipe a tar archive
// through `docker exec` instead, and this test asserts the encoded bytes are
// the contract `tar -xf` will expect.
func TestBuildSingleFileTar_RoundTrip(t *testing.T) {
	payload := []byte("hello, skill files — also non-ascii: 中文 + binary \x00\x01\xff")
	buf, err := buildSingleFileTar("SKILL.md", payload)
	if err != nil {
		t.Fatalf("buildSingleFileTar err = %v", err)
	}

	tr := tar.NewReader(buf)
	hdr, err := tr.Next()
	if err != nil {
		t.Fatalf("tar.Next first entry err = %v; archive must contain one entry", err)
	}
	if hdr.Name != "SKILL.md" {
		t.Errorf("entry name = %q; want SKILL.md (`tar -xf - -C <dir>` lands it at <dir>/SKILL.md)", hdr.Name)
	}
	if hdr.Size != int64(len(payload)) {
		t.Errorf("entry size = %d; want %d", hdr.Size, len(payload))
	}
	if hdr.Mode&0o777 != 0o644 {
		t.Errorf("entry mode = %o; want 0644", hdr.Mode&0o777)
	}
	body, err := io.ReadAll(tr)
	if err != nil {
		t.Fatalf("read entry body err = %v", err)
	}
	if !bytes.Equal(body, payload) {
		t.Errorf("body round-trip mismatch:\n got %q\nwant %q", body, payload)
	}

	if _, err := tr.Next(); err != io.EOF {
		t.Errorf("expected EOF after single entry, got err=%v (archive must contain exactly one file)", err)
	}
}
