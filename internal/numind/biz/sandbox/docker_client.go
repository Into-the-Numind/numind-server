package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	// Implemented via "docker cp - <containerID>:<dstPath>" (tar stream).
	// Track 4: used by CopyFileIn to inject input files into /input/.
	CopyToContainer(ctx context.Context, containerID, dstPath string, content io.Reader) error

	// CopyFromContainer reads `srcPath` from the container into a temp dir on
	// the host, returning the path of the written file(s). The caller is
	// responsible for removing the temp dir when done.
	// Implemented via "docker cp <containerID>:<srcPath> <tmpDir>".
	// Track 4: used by CollectOutputs to pull /workdir/output/ files.
	CopyFromContainer(ctx context.Context, containerID, srcPath, hostDstDir string) error

	// ExecMkdir creates a directory inside the container via docker exec.
	// Used by AcquireForSkill to prepare /input and /output under /workdir.
	ExecMkdir(ctx context.Context, containerID string, dirs ...string) error
}

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
	args = append(args, cfg.ImageTag)
	// Hold the container alive so subsequent docker exec calls can attach.
	// "sleep 600" matches the SandboxConfig.SessionTimeout default (300s)
	// with margin; Pool.Return destroys long before this expires.
	args = append(args, "/bin/sh", "-c", "sleep 600")
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

// Inspect calls "docker inspect" with a Go template to extract the three
// fields the Pool / hooks need.
func (c *dockerCLIClient) Inspect(ctx context.Context, containerID string) (InspectResult, error) {
	cmd := exec.CommandContext(ctx, dockerBinary,
		"inspect",
		"--format", "{{.State.Status}} {{.State.ExitCode}} {{.State.OOMKilled}}",
		containerID,
	)
	out, err := cmd.Output()
	if err != nil {
		return InspectResult{}, fmt.Errorf("docker inspect: %w", err)
	}
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) < 3 {
		return InspectResult{}, fmt.Errorf("docker inspect: unexpected output %q", string(out))
	}
	exit, _ := strconv.Atoi(parts[1])
	return InspectResult{
		Status:    parts[0],
		ExitCode:  exit,
		OOMKilled: parts[2] == "true",
	}, nil
}

// CopyToContainer writes the content of `content` (a tar stream or raw bytes)
// to `dstPath` inside the container by piping through "docker cp - containerID:dstPath".
// The caller provides raw file bytes; this method wraps them in a minimal tar
// archive so docker cp can unpack them correctly.
func (c *dockerCLIClient) CopyToContainer(ctx context.Context, containerID, dstPath string, content io.Reader) error {
	// Write content to a temp file so we can docker cp it.
	tmp, err := os.CreateTemp("", "sandbox-cp-in-*")
	if err != nil {
		return fmt.Errorf("docker cp to container: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	data, err := io.ReadAll(content)
	if err != nil {
		tmp.Close()
		return fmt.Errorf("docker cp to container: read content: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("docker cp to container: write temp: %w", err)
	}
	tmp.Close()

	// docker cp <hostFile> <containerID>:<dstPath>
	cmd := exec.CommandContext(ctx, dockerBinary, "cp", tmpPath, containerID+":"+dstPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker cp to container: %w (output=%s)", err, string(out))
	}
	return nil
}

// CopyFromContainer copies `srcPath` from the container to `hostDstDir` on
// the host via "docker cp <containerID>:<srcPath> <hostDstDir>". The caller
// owns the destination directory and is responsible for cleanup.
func (c *dockerCLIClient) CopyFromContainer(ctx context.Context, containerID, srcPath, hostDstDir string) error {
	// docker cp <containerID>:<srcPath> <hostDstDir>
	cmd := exec.CommandContext(ctx, dockerBinary, "cp", containerID+":"+srcPath, hostDstDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		s := string(out)
		// Treat "no such file" as empty (not an error) — output dir may be
		// legitimately empty when the Python script produced nothing.
		if strings.Contains(s, "No such file") || strings.Contains(s, "no such file") {
			return nil
		}
		return fmt.Errorf("docker cp from container: %w (output=%s)", err, s)
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
