package sandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"
)

// DockerClient is the minimal Docker primitive interface. The real impl
// (dockerCLIClient) shells out to the host "docker" binary; tests use the
// in-memory mockDockerClient defined in docker_client_test.go.
type DockerClient interface {
	Spawn(ctx context.Context, cfg SpawnConfig) (containerID string, err error)
	Exec(ctx context.Context, containerID string, cmd []string, opts ExecOpts) (ExecResult, error)
	Destroy(ctx context.Context, containerID string) error
	Inspect(ctx context.Context, containerID string) (InspectResult, error)

	// CopyToContainer writes `content` into the container at `dstPath`.
	// Implemented via `docker exec -i <CID> tar -xf - -C <dir>` (NOT `docker cp` —
	// see the impl comment for the Docker 28.x tmpfs bug). Track 4: used by
	// CopyFileIn to inject input files into /workdir/input/ and by AcquireForSkill
	// to stage skill files into /skills/<name>/.
	CopyToContainer(ctx context.Context, containerID, dstPath string, content io.Reader) error

	// CopyFromContainer copies `srcPath` from the container into `hostDstDir`.
	// Implemented via `docker exec <CID> tar -cf -` piped through host `tar -xf -`
	// (NOT `docker cp` — same Docker 28.x tmpfs bug applies in both directions).
	// Track 4: used by CollectOutputs to pull /workdir/output/ files.
	CopyFromContainer(ctx context.Context, containerID, srcPath, hostDstDir string) error

	// ExecMkdir creates a directory inside the container via docker exec.
	// Used by AcquireForSkill to prepare /input and /output under /workdir.
	ExecMkdir(ctx context.Context, containerID string, dirs ...string) error

	// ListByLabel returns the IDs of ALL containers (running or stopped)
	// carrying the given "key=value" label. Used by the pool's startup reaper
	// to clean up orphaned sandbox containers left by a previous process.
	ListByLabel(ctx context.Context, label string) ([]string, error)
}

// SandboxContainerLabel is stamped on every pool-spawned sandbox container so
// the startup reaper can find and destroy orphans left by a previous process
// run. Format is "key=value" as docker --label / --filter label= expects.
const SandboxContainerLabel = "numind.sandbox=1"

// SandboxContainerOwnerLabelKey stores the owning numind-server container /
// process identity. Startup cleanup uses it to avoid deleting sandbox
// containers that belong to another live pool in the same Docker daemon.
const SandboxContainerOwnerLabelKey = "numind.sandbox.owner"

// SandboxContainerOwnerBootLabelKey stores this numind-server process boot id.
// It lets a restarted process in the same Docker container reap stale children
// from its previous boot while still keeping peer pools in the current boot.
const SandboxContainerOwnerBootLabelKey = "numind.sandbox.owner_boot"

// SpawnConfig is the set of docker run flags BuildSpawnConfig assembles
// from SandboxConfig + V5 ADR Q2 hardening list (see security.go).
type SpawnConfig struct {
	ImageTag     string
	SecurityOpts []string // ["seccomp=<path>","apparmor=docker-default","no-new-privileges"]
	User         string   // "1000:1000"
	CapDrop      []string // ["ALL"]
	CapAdd       []string // ["NET_BIND_SERVICE"]
	Memory       string   // "512m"
	CPUs         string   // "1.0"
	PIDsLimit    int      // 64
	ReadOnly     bool
	Tmpfs        []string // ["/workdir:size=512m,uid=1000,gid=1000"]
	Network      string   // "none"
	Detached     bool     // true for pool (sleep loop holds container alive)
	// Volumes holds bind-mount specs in "host:container:options" form.
	// Track 4: AcquireForSkill appends skill read-only mounts here.
	// Example: "/app/skills/xlsx-author:/skills/xlsx-author:ro"
	Volumes []string
	// Labels are stamped via docker run --label (each "key=value"). The pool
	// sets SandboxContainerLabel so its startup reaper can find orphans.
	Labels []string
}

// ExecOpts describes the runtime parameters for a single docker exec call.
type ExecOpts struct {
	Timeout time.Duration
	Workdir string // "/workdir"
	User    string // "1000:1000"
	Env     []string
}

// ExecResult is the output captured from docker exec.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration
}

// InspectResult mirrors the subset of "docker inspect" output the Pool /
// PostToolCall hook needs to make lifecycle decisions.
type InspectResult struct {
	Status    string // "running" | "exited" | "dead" | ...
	ExitCode  int
	OOMKilled bool
	Labels    map[string]string
}

// dockerBinary returns the docker CLI binary name; var so tests can override.
var dockerBinary = "docker"

// dockerCLIClient implements DockerClient by exec'ing the host "docker"
// binary. Requires:
//   - docker CLI present on PATH inside the numind-server container
//     (dev Dockerfile WITH_DOCKER_CLI=true)
//   - /var/run/docker.sock bind-mounted from host
//
// prod containers don't have docker CLI / socket → users must not call
// this client; SandboxConfig.Backend=disabled returns the no-op disabledPool
// instead.
type dockerCLIClient struct {
	logger Logger
}

// Logger is the minimal log surface dockerCLIClient needs; decouples the
// sandbox package from the project's zap-based logger.
type Logger interface {
	Warnw(msg string, kv ...interface{})
	Infow(msg string, kv ...interface{})
}

// nopLogger is the no-op Logger used when nil is supplied.
type nopLogger struct{}

func (nopLogger) Warnw(string, ...interface{}) {}
func (nopLogger) Infow(string, ...interface{}) {}

// NewDockerCLIClient returns a DockerClient that shells out to the host
// docker binary. Pass nil for a no-op logger.
func NewDockerCLIClient(logger Logger) DockerClient {
	if logger == nil {
		logger = nopLogger{}
	}
	return &dockerCLIClient{logger: logger}
}

// buildSpawnArgs returns the docker run argv (without the binary itself).
// Separated for testability — tests can verify the assembled flag set
// without actually invoking docker.
func buildSpawnArgs(cfg SpawnConfig) []string {
	args := []string{"run"}
	if cfg.Detached {
		args = append(args, "--detach")
	}
	for _, opt := range cfg.SecurityOpts {
		args = append(args, "--security-opt", opt)
	}
	if cfg.User != "" {
		args = append(args, "--user", cfg.User)
	}
	for _, c := range cfg.CapDrop {
		args = append(args, "--cap-drop="+c)
	}
	for _, c := range cfg.CapAdd {
		args = append(args, "--cap-add="+c)
	}
	if cfg.Memory != "" {
		args = append(args, "--memory="+cfg.Memory)
	}
	if cfg.CPUs != "" {
		args = append(args, "--cpus="+cfg.CPUs)
	}
	if cfg.PIDsLimit > 0 {
		args = append(args, "--pids-limit="+strconv.Itoa(cfg.PIDsLimit))
	}
	if cfg.ReadOnly {
		args = append(args, "--read-only")
	}
	for _, m := range cfg.Tmpfs {
		args = append(args, "--tmpfs", m)
	}
	if cfg.Network != "" {
		args = append(args, "--network="+cfg.Network)
	}
	for _, v := range cfg.Volumes {
		args = append(args, "--volume", v)
	}
	for _, l := range cfg.Labels {
		args = append(args, "--label", l)
	}
	args = append(args, cfg.ImageTag)
	// Hold the container alive so subsequent docker exec calls can attach.
	// Use "sleep infinity" (not a fixed duration): a warm container may sit idle
	// in the pool for hours/overnight. The previous "sleep 600" meant idle warm
	// containers EXITED after 10 min, and Borrow then handed out dead containers
	// ("container is not running" on every exec) — the root cause of the
	// 2026-05-29 "sandbox completely unavailable" incident. Lifecycle is now
	// owned explicitly: Pool.Return destroys borrowed containers, Borrow's
	// liveness check discards any that died unexpectedly, and the startup reaper
	// (SandboxContainerLabel) cleans orphans left by a crashed/restarted process.
	args = append(args, "/bin/sh", "-c", "sleep infinity")
	return args
}

// Spawn launches a detached container per the SpawnConfig and returns the
// 64-char container ID printed by docker run --detach.
func (c *dockerCLIClient) Spawn(ctx context.Context, cfg SpawnConfig) (string, error) {
	args := buildSpawnArgs(cfg)
	cmd := exec.CommandContext(ctx, dockerBinary, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("docker spawn: %w (stderr=%s)", err, captureStderr(err))
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", fmt.Errorf("docker spawn: empty container id from docker run")
	}
	return id, nil
}

// Exec runs a command inside the given container; returns stdout/stderr +
// exit code. The command argv is the literal cmd slice; the workdir + user
// + env come from opts.
func (c *dockerCLIClient) Exec(ctx context.Context, containerID string, cmd []string, opts ExecOpts) (ExecResult, error) {
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	args := []string{"exec"}
	if opts.Workdir != "" {
		args = append(args, "--workdir", opts.Workdir)
	}
	if opts.User != "" {
		args = append(args, "--user", opts.User)
	}
	for _, e := range opts.Env {
		args = append(args, "--env", e)
	}
	args = append(args, containerID)
	args = append(args, cmd...)

	start := time.Now()
	execCmd := exec.CommandContext(ctx, dockerBinary, args...)
	stdoutBuf, stderrBuf, exitCode, runErr := runWithCapture(execCmd)
	dur := time.Since(start)

	res := ExecResult{
		Stdout:   stdoutBuf,
		Stderr:   stderrBuf,
		ExitCode: exitCode,
		Duration: dur,
	}
	if runErr != nil && exitCode == 0 {
		// runErr is something other than a non-zero exit (e.g. ctx cancel)
		return res, fmt.Errorf("docker exec: %w", runErr)
	}
	return res, nil
}

// Destroy removes the container forcefully. Swallows "No such container"
// to keep the call idempotent (PostToolCall + defer Return double-fires
// when bash_exec.Execute panics).
func (c *dockerCLIClient) Destroy(ctx context.Context, containerID string) error {
	cmd := exec.CommandContext(ctx, dockerBinary, "rm", "-f", containerID)
	out, err := cmd.CombinedOutput()
	if err != nil {
		s := string(out)
		if strings.Contains(s, "No such container") {
			return nil // idempotent
		}
		return fmt.Errorf("docker destroy: %w (output=%s)", err, s)
	}
	return nil
}

// Inspect calls "docker inspect" and extracts the fields the Pool / hooks need.
func (c *dockerCLIClient) Inspect(ctx context.Context, containerID string) (InspectResult, error) {
	cmd := exec.CommandContext(ctx, dockerBinary,
		"inspect",
		"--format", "{{json .}}",
		containerID,
	)
	out, err := cmd.Output()
	if err != nil {
		return InspectResult{}, fmt.Errorf("docker inspect: %w", err)
	}

	var raw struct {
		State struct {
			Status    string `json:"Status"`
			ExitCode  int    `json:"ExitCode"`
			OOMKilled bool   `json:"OOMKilled"`
		} `json:"State"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &raw); err != nil {
		return InspectResult{}, fmt.Errorf("docker inspect: decode: %w", err)
	}
	return InspectResult{
		Status:    raw.State.Status,
		ExitCode:  raw.State.ExitCode,
		OOMKilled: raw.State.OOMKilled,
		Labels:    raw.Config.Labels,
	}, nil
}

// ListByLabel returns the IDs of all containers (running or stopped) carrying
// the given "key=value" label, via "docker ps -aq --filter label=<label>".
func (c *dockerCLIClient) ListByLabel(ctx context.Context, label string) ([]string, error) {
	cmd := exec.CommandContext(ctx, dockerBinary,
		"ps", "-aq", "--no-trunc",
		"--filter", "label="+label,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker ps --filter label=%s: %w (stderr=%s)", label, err, captureStderr(err))
	}
	ids := make([]string, 0)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if id := strings.TrimSpace(line); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// CopyToContainer writes `content` as a file at `dstPath` inside the container.
//
// Implementation uses `docker exec -i <CID> tar -xf - -C <dir>` with an in-memory
// tar archive on stdin. This deliberately AVOIDS `docker cp`, which in Docker 28.x
// is broken for tmpfs mounts under `--read-only` rootfs: it fails with
// `Error response from daemon: Could not find the file <path> in container`
// even when the path exists and is writable via `docker exec`. Verified on dev
// 2026-05-29 — both /workdir and /skills (the sandbox's writable tmpfs mounts)
// trigger the bug. `docker exec` uses a different daemon code path and works.
//
// The parent directory of dstPath must already exist in the container; callers
// stage it via ExecMkdir.
func (c *dockerCLIClient) CopyToContainer(ctx context.Context, containerID, dstPath string, content io.Reader) error {
	data, err := io.ReadAll(content)
	if err != nil {
		return fmt.Errorf("docker cp to container: read content: %w", err)
	}

	buf, err := buildSingleFileTar(path.Base(dstPath), data)
	if err != nil {
		return fmt.Errorf("docker cp to container: %w", err)
	}

	cmd := exec.CommandContext(ctx, dockerBinary,
		"exec", "-i", containerID,
		"tar", "-xf", "-", "-C", path.Dir(dstPath),
	)
	cmd.Stdin = buf
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker cp to container: exec tar: %w (output=%s)", err, string(out))
	}
	return nil
}

// buildSingleFileTar returns a tar archive containing one regular file at
// `name` (relative path) with the given bytes. Extracted via `tar -xf - -C <dir>`,
// the file lands at <dir>/<name>. Extracted for unit-test reachability of the
// tar-encoding logic without invoking docker.
func buildSingleFileTar(name string, data []byte) (*bytes.Buffer, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name:    name,
		Mode:    0o644,
		Size:    int64(len(data)),
		ModTime: time.Now(),
	}); err != nil {
		return nil, fmt.Errorf("tar header: %w", err)
	}
	if _, err := tw.Write(data); err != nil {
		return nil, fmt.Errorf("tar body: %w", err)
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("tar close: %w", err)
	}
	return &buf, nil
}

// CopyFromContainer copies `srcPath` from the container into `hostDstDir`.
//
// Like CopyToContainer, this uses `docker exec <CID> tar -cf -` + a host-side
// `tar -xf -` instead of `docker cp`, which is broken for tmpfs mounts under
// `--read-only` rootfs in Docker 28.x (fails in BOTH directions — verified on
// dev 2026-05-29 with `/workdir/output` and `/skills/*`).
//
// Mirrors docker cp's "/." suffix semantics:
//   - srcPath = "/workdir/output"   → hostDstDir/output/<files>  (dir wrapped)
//   - srcPath = "/workdir/output/." → hostDstDir/<files>         (contents only)
//
// A missing srcPath returns nil (the caller — eg CollectOutputs — treats an
// empty output directory as legitimate when the Python script produced nothing).
func (c *dockerCLIClient) CopyFromContainer(ctx context.Context, containerID, srcPath, hostDstDir string) error {
	var tarSrcDir, tarSrcEntry string
	if strings.HasSuffix(srcPath, "/.") {
		tarSrcDir = strings.TrimSuffix(srcPath, "/.")
		tarSrcEntry = "."
	} else {
		tarSrcDir = path.Dir(srcPath)
		tarSrcEntry = path.Base(srcPath)
	}

	// Produce the tar stream inside the container.
	var stdoutBuf, stderrBuf bytes.Buffer
	produce := exec.CommandContext(ctx, dockerBinary,
		"exec", containerID,
		"tar", "-cf", "-", "-C", tarSrcDir, tarSrcEntry,
	)
	produce.Stdout = &stdoutBuf
	produce.Stderr = &stderrBuf
	if err := produce.Run(); err != nil {
		s := stderrBuf.String()
		if strings.Contains(s, "No such file") || strings.Contains(s, "no such file") {
			return nil
		}
		return fmt.Errorf("docker cp from container: source tar: %w (stderr=%s)", err, s)
	}

	// Empty stdout = nothing to extract. Avoids invoking host tar with empty input,
	// which on some implementations errors with "This does not look like a tar archive".
	if stdoutBuf.Len() == 0 {
		return nil
	}

	// Extract the tar stream into hostDstDir using the host's `tar` binary
	// (present in both Ubuntu base images we run — numind-server runtime + dev/qa/prod hosts).
	extract := exec.CommandContext(ctx, "tar", "-xf", "-", "-C", hostDstDir)
	extract.Stdin = &stdoutBuf
	if out, err := extract.CombinedOutput(); err != nil {
		return fmt.Errorf("docker cp from container: extract tar: %w (output=%s)", err, string(out))
	}
	return nil
}

// ExecMkdir creates `dirs` inside the container using docker exec mkdir -p.
func (c *dockerCLIClient) ExecMkdir(ctx context.Context, containerID string, dirs ...string) error {
	if len(dirs) == 0 {
		return nil
	}
	args := append([]string{"exec", containerID, "mkdir", "-p"}, dirs...)
	cmd := exec.CommandContext(ctx, dockerBinary, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker exec mkdir: %w (output=%s)", err, string(out))
	}
	return nil
}

// captureStderr extracts the Stderr field from an *exec.ExitError if present.
func captureStderr(err error) string {
	if ee, ok := err.(*exec.ExitError); ok {
		return string(ee.Stderr)
	}
	return ""
}

// runWithCapture runs cmd, captures stdout/stderr separately, and returns
// the exit code. A non-zero exit code is NOT returned as an error from
// this helper; the caller treats it as data.
func runWithCapture(cmd *exec.Cmd) (stdout, stderr string, exitCode int, err error) {
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	stdout = outBuf.String()
	stderr = errBuf.String()
	if err == nil {
		exitCode = 0
		return
	}
	if ee, ok := err.(*exec.ExitError); ok {
		exitCode = ee.ExitCode()
		// non-zero exit is data, not an error
		err = nil
		return
	}
	// Some other runtime error (ctx cancel, binary missing, ...) — return
	// it as-is. exitCode is irrelevant in that case.
	exitCode = -1
	return
}
