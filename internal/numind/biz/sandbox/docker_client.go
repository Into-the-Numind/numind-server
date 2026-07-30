package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"numind-server/internal/numind/sandboxbroker"
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
	// It streams through a fixed Docker exec command with bounded memory.
	// Track 4: used by CopyFileIn to inject input files into /workdir/input/
	// and by AcquireForSkill to stage skill files into /skills/<name>/.
	CopyToContainer(ctx context.Context, containerID, dstPath string, content io.Reader) error

	// CopyFromContainer copies `srcPath` from the container into `hostDstDir`.
	// It incrementally validates and extracts a bounded tar stream in Go.
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
	stdoutBuf, stderrBuf, exitCode, outputExceeded, runErr := runWithCapture(
		execCmd,
		sandboxbroker.MaxExecOutputBytes,
	)
	dur := time.Since(start)

	res := ExecResult{
		Stdout:   stdoutBuf,
		Stderr:   stderrBuf,
		ExitCode: exitCode,
		Duration: dur,
	}
	if outputExceeded {
		return res, ErrOutputTooLarge
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return res, fmt.Errorf("docker exec: %w", ctxErr)
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
// Implementation streams directly to a fixed shell redirection with a 64 KiB
// buffer. This deliberately avoids both docker cp (broken for Docker 28.x
// tmpfs under a read-only rootfs) and the old io.ReadAll + in-memory tar path.
//
// The parent directory of dstPath must already exist in the container; callers
// stage it via ExecMkdir.
func (c *dockerCLIClient) CopyToContainer(ctx context.Context, containerID, dstPath string, content io.Reader) error {
	cleanPath, err := canonicalDockerCopyInPath(dstPath)
	if err != nil {
		return err
	}
	reader, ok := safeDockerCopyReader(content)
	if !ok {
		return ErrSandboxPolicyDenied
	}

	cmd := exec.CommandContext(ctx, dockerBinary,
		"exec", "-i", containerID, "/bin/sh", "-c",
		`set -C; umask 077; exec cat > "$1"`,
		"numind-copy-in", cleanPath,
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("docker cp to container: stdin: %w", err)
	}
	stderr := newLimitedTextBuffer(64 << 10)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("docker cp to container: start: %w", err)
	}
	_, copyErr := sandboxbroker.CopyBounded(
		ctx,
		stdin,
		reader,
		sandboxbroker.MaxSingleFileBytes,
		sandboxbroker.ErrStreamInputTooLarge,
	)
	closeErr := stdin.Close()
	if copyErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return mapDockerStreamError(copyErr)
	}
	waitErr := cmd.Wait()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if closeErr != nil {
		return fmt.Errorf("docker cp to container: close stream: %w", closeErr)
	}
	if waitErr != nil {
		return fmt.Errorf(
			"docker cp to container: stream: %w (stderr=%s)",
			waitErr,
			stderr.String(),
		)
	}
	return nil
}

// CopyFromContainer copies `srcPath` from the container into `hostDstDir`.
//
// Like CopyToContainer, this avoids docker cp. It streams `docker exec tar`
// directly through the descriptor-relative Go extractor, so output is never
// buffered in memory or delegated to a host tar process.
//
// Mirrors docker cp's "/." suffix semantics:
//   - srcPath = "/workdir/output"   → hostDstDir/output/<files>  (dir wrapped)
//   - srcPath = "/workdir/output/." → hostDstDir/<files>         (contents only)
//
// A missing srcPath returns nil (the caller — eg CollectOutputs — treats an
// empty output directory as legitimate when the Python script produced nothing).
func (c *dockerCLIClient) CopyFromContainer(ctx context.Context, containerID, srcPath, hostDstDir string) error {
	source, err := sandboxbroker.CanonicalCopyOutPath(srcPath)
	if err != nil {
		return ErrSandboxPolicyDenied
	}

	// Produce the tar stream inside the container and extract it incrementally.
	produce := exec.CommandContext(ctx, dockerBinary,
		"exec", containerID,
		"tar", "-cf", "-", "-C", source.Directory, source.Entry,
	)
	stdout, err := produce.StdoutPipe()
	if err != nil {
		return fmt.Errorf("docker cp from container: stdout: %w", err)
	}
	stderr := newLimitedTextBuffer(64 << 10)
	produce.Stderr = stderr
	if err := produce.Start(); err != nil {
		return fmt.Errorf("docker cp from container: start: %w", err)
	}
	_, extractErr := sandboxbroker.ExtractTarStream(
		ctx,
		stdout,
		hostDstDir,
		sandboxbroker.DefaultCopyOutLimits(),
	)
	if extractErr != nil {
		_ = produce.Process.Kill()
	}
	waitErr := produce.Wait()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if extractErr != nil {
		return mapDockerStreamError(extractErr)
	}
	if waitErr != nil {
		message := stderr.String()
		if strings.Contains(message, "No such file") || strings.Contains(message, "no such file") {
			return nil
		}
		return fmt.Errorf(
			"docker cp from container: source tar: %w (stderr=%s)",
			waitErr,
			message,
		)
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

func canonicalDockerCopyInPath(raw string) (string, error) {
	if strings.HasPrefix(path.Clean(raw), "/skills/") {
		parts := strings.Split(strings.TrimPrefix(path.Clean(raw), "/skills/"), "/")
		if len(parts) < 2 {
			return "", ErrSandboxPolicyDenied
		}
		clean, err := sandboxbroker.CanonicalCopyInPath(raw, []string{parts[0]})
		if err != nil {
			return "", ErrSandboxPolicyDenied
		}
		return clean, nil
	}
	clean, err := sandboxbroker.CanonicalCopyInPath(raw, nil)
	if err != nil {
		return "", ErrSandboxPolicyDenied
	}
	return clean, nil
}

func safeDockerCopyReader(reader io.Reader) (io.ReadCloser, bool) {
	switch typed := reader.(type) {
	case *bytes.Buffer:
		return io.NopCloser(typed), true
	case *bytes.Reader:
		return io.NopCloser(typed), true
	case *strings.Reader:
		return io.NopCloser(typed), true
	case *io.PipeReader:
		return typed, true
	default:
		return nil, false
	}
}

func mapDockerStreamError(err error) error {
	switch {
	case errors.Is(err, sandboxbroker.ErrStreamInputTooLarge):
		return ErrInputTooLarge
	case errors.Is(err, sandboxbroker.ErrStreamOutputTooLarge):
		return ErrOutputTooLarge
	case errors.Is(err, sandboxbroker.ErrStreamPolicyDenied):
		return ErrSandboxPolicyDenied
	default:
		return err
	}
}

// runWithCapture runs cmd with a shared stdout+stderr ceiling. A non-zero exit
// code remains result data; output overflow kills the command and is reported
// separately.
func runWithCapture(
	cmd *exec.Cmd,
	maxBytes int64,
) (stdout string, stderr string, exitCode int, exceeded bool, err error) {
	capture := newCombinedCapture(maxBytes, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	cmd.Stdout = capture.stdoutWriter()
	cmd.Stderr = capture.stderrWriter()
	err = cmd.Run()
	stdout, stderr, exceeded = capture.snapshot()
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

type limitedTextBuffer struct {
	mu        sync.Mutex
	remaining int64
	builder   strings.Builder
}

func newLimitedTextBuffer(maxBytes int64) *limitedTextBuffer {
	return &limitedTextBuffer{remaining: maxBytes}
}

func (b *limitedTextBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	allowed := int64(len(p))
	if allowed > b.remaining {
		allowed = b.remaining
	}
	if allowed > 0 {
		_, _ = b.builder.Write(p[:allowed])
		b.remaining -= allowed
	}
	return len(p), nil
}

func (b *limitedTextBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.builder.String()
}

type combinedCapture struct {
	mu        sync.Mutex
	remaining int64
	stdout    strings.Builder
	stderr    strings.Builder
	exceeded  bool
	killOnce  sync.Once
	kill      func()
}

type combinedCaptureWriter struct {
	capture *combinedCapture
	stderr  bool
}

func newCombinedCapture(maxBytes int64, kill func()) *combinedCapture {
	return &combinedCapture{remaining: maxBytes, kill: kill}
}

func (c *combinedCapture) stdoutWriter() io.Writer {
	return combinedCaptureWriter{capture: c}
}

func (c *combinedCapture) stderrWriter() io.Writer {
	return combinedCaptureWriter{capture: c, stderr: true}
}

func (w combinedCaptureWriter) Write(p []byte) (int, error) {
	w.capture.mu.Lock()
	allowed := int64(len(p))
	if allowed > w.capture.remaining {
		allowed = w.capture.remaining
		w.capture.exceeded = true
	}
	if allowed > 0 {
		if w.stderr {
			_, _ = w.capture.stderr.Write(p[:allowed])
		} else {
			_, _ = w.capture.stdout.Write(p[:allowed])
		}
		w.capture.remaining -= allowed
	}
	exceeded := w.capture.exceeded
	w.capture.mu.Unlock()
	if exceeded {
		w.capture.killOnce.Do(w.capture.kill)
	}
	return len(p), nil
}

func (c *combinedCapture) snapshot() (string, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stdout.String(), c.stderr.String(), c.exceeded
}
