package sandboxbroker

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	runtimeDockerInspectTimeout = 10 * time.Second
	runtimeDockerListTimeout    = 10 * time.Second
	runtimeDockerCopyTimeout    = RuntimeExecTimeout
	runtimeTarOverheadBytes     = int64(MaxCopyFiles+16) * StreamBufferSize
)

type dockerRuntimeSpawn func(context.Context, string, string) ([]byte, error)

type dockerRuntimeCommand func(
	context.Context,
	string,
	[]string,
	io.Reader,
	io.Writer,
	io.Writer,
) error

type dockerRuntimeOutput func(context.Context, string, []string) ([]byte, error)

type dockerRuntimeStream func(
	context.Context,
	string,
	[]string,
) (io.ReadCloser, func() error, error)

// DockerRuntimeAdapterConfig contains only root-owned sandboxd settings. None
// of these values can be supplied by broker RPC callers.
type DockerRuntimeAdapterConfig struct {
	Policy          *RuntimePolicy
	BrokerInstance  string
	DockerHost      string
	DockerConfigDir string
}

// DockerRuntimeAdapter is the only production ContainerRuntime backed by the
// dedicated Rootless Docker daemon. RPC input can select only lease/container
// identities, argv/env values already checked by RuntimePolicy, and sandbox
// paths already checked by stream policy.
type DockerRuntimeAdapter struct {
	policy         *RuntimePolicy
	brokerInstance string
	binary         string
	spawn          dockerRuntimeSpawn
	run            dockerRuntimeCommand
	output         dockerRuntimeOutput
	stream         dockerRuntimeStream
}

// NewDockerRuntimeAdapter builds the fixed Docker CLI adapter for sandboxd.
func NewDockerRuntimeAdapter(
	cfg DockerRuntimeAdapterConfig,
) (*DockerRuntimeAdapter, error) {
	if cfg.Policy == nil ||
		!safeRuntimeToken(cfg.BrokerInstance) ||
		!safeDockerHost(cfg.DockerHost) ||
		!safeDockerConfigDir(cfg.DockerConfigDir) {
		return nil, ErrRuntimePolicyDenied
	}
	dockerEnv := dockerCommandEnv(cfg.DockerHost, cfg.DockerConfigDir)
	return &DockerRuntimeAdapter{
		policy:         cfg.Policy,
		brokerInstance: cfg.BrokerInstance,
		binary:         SandboxDockerBinary,
		spawn: func(ctx context.Context, leaseID string, brokerInstance string) ([]byte, error) {
			command, seccompFile, err := cfg.Policy.dockerSpawnCommand(
				ctx,
				SandboxDockerBinary,
				leaseID,
				brokerInstance,
			)
			if err != nil {
				return nil, err
			}
			defer seccompFile.Close()
			command.Env = append([]string(nil), dockerEnv...)
			return command.Output()
		},
		run: func(
			ctx context.Context,
			binary string,
			args []string,
			stdin io.Reader,
			stdout io.Writer,
			stderr io.Writer,
		) error {
			return runDockerRuntimeCommandWithEnv(
				ctx,
				binary,
				args,
				dockerEnv,
				stdin,
				stdout,
				stderr,
			)
		},
		output: func(ctx context.Context, binary string, args []string) ([]byte, error) {
			return outputDockerRuntimeCommandWithEnv(ctx, binary, args, dockerEnv)
		},
		stream: func(ctx context.Context, binary string, args []string) (io.ReadCloser, func() error, error) {
			return streamDockerRuntimeCommandWithEnv(ctx, binary, args, dockerEnv)
		},
	}, nil
}

func (a *DockerRuntimeAdapter) Spawn(
	ctx context.Context,
	leaseID string,
) (string, error) {
	if a == nil || a.spawn == nil || !safeRuntimeToken(leaseID) {
		return "", ErrRuntimePolicyDenied
	}
	output, err := a.spawn(ctx, leaseID, a.brokerInstance)
	if err != nil {
		return "", err
	}
	containerID := strings.TrimSpace(string(output))
	if !safeRuntimeToken(containerID) {
		return "", ErrRPCProtocol
	}
	return containerID, nil
}

func (a *DockerRuntimeAdapter) Exec(
	ctx context.Context,
	containerID string,
	argv []string,
	env []string,
) (ExecRPCResponse, error) {
	if a == nil || a.run == nil || !safeRuntimeToken(containerID) {
		return ExecRPCResponse{}, ErrRuntimePolicyDenied
	}
	spec, err := a.policy.execSpec(argv, env)
	if err != nil {
		return ExecRPCResponse{}, err
	}
	execCtx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()

	limit := &runtimeOutputLimit{remain: spec.OutputMaxBytes}
	stdout := &runtimeBoundedBuffer{limit: limit}
	stderr := &runtimeBoundedBuffer{limit: limit}
	args := []string{
		"exec",
		"--user=" + spec.User,
		"--workdir=" + spec.Workdir,
	}
	for _, item := range spec.Env {
		args = append(args, "--env="+item)
	}
	args = append(args, containerID)
	args = append(args, spec.Argv...)

	started := time.Now()
	runErr := a.run(execCtx, a.binary, args, nil, stdout, stderr)
	duration := time.Since(started)
	response := ExecRPCResponse{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: duration,
	}
	if limit.Err() != nil {
		return response, limit.Err()
	}
	if err := execCtx.Err(); err != nil {
		return response, err
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			response.ExitCode = exitErr.ExitCode()
			return response, nil
		}
		return response, runErr
	}
	return response, nil
}

func (a *DockerRuntimeAdapter) CopyIn(
	ctx context.Context,
	containerID string,
	rawPath string,
	reader io.Reader,
) (int64, error) {
	if a == nil || a.run == nil || reader == nil ||
		!safeRuntimeToken(containerID) {
		return 0, ErrRuntimePolicyDenied
	}
	target, err := a.policy.CopyInPath(rawPath)
	if err != nil {
		return 0, err
	}
	copyCtx, cancel := context.WithTimeout(ctx, runtimeDockerCopyTimeout)
	defer cancel()
	counter := &runtimeCountingReader{
		source: &hardLimitReader{
			source:   reader,
			remain:   MaxSingleFileBytes,
			limitErr: ErrStreamInputTooLarge,
		},
	}
	stderr := &runtimeBoundedBuffer{
		limit: &runtimeOutputLimit{remain: ServerMetadataMaxBytes},
	}
	args := []string{
		"exec",
		"--user=1000:1000",
		"--workdir=/workdir",
		"-i",
		containerID,
		"/bin/sh",
		"-c",
		`set -eu; target="$1"; parent="${target%/*}"; mkdir -p "$parent"; cat > "$target"`,
		"numind-copy-in",
		target,
	}
	if err := a.run(copyCtx, a.binary, args, counter, io.Discard, stderr); err != nil {
		if counter.err != nil {
			return counter.bytes, counter.err
		}
		if ctxErr := copyCtx.Err(); ctxErr != nil {
			return counter.bytes, ctxErr
		}
		return counter.bytes, err
	}
	if counter.err != nil {
		return counter.bytes, counter.err
	}
	return counter.bytes, nil
}

func (a *DockerRuntimeAdapter) CopyOut(
	ctx context.Context,
	containerID string,
	source CopyOutSource,
) (RuntimeCopyOut, error) {
	if a == nil || a.stream == nil || !safeRuntimeToken(containerID) ||
		!validRuntimeCopyOutSource(source) {
		return RuntimeCopyOut{}, ErrRuntimePolicyDenied
	}
	copyCtx, cancel := context.WithTimeout(ctx, runtimeDockerCopyTimeout)
	defer cancel()
	args := runtimeCopyOutArgs(containerID, source)
	stdout, wait, err := a.stream(copyCtx, a.binary, args)
	if err != nil {
		return RuntimeCopyOut{}, err
	}
	temp, err := os.CreateTemp("", ".numind-sandbox-copyout-*")
	if err != nil {
		_ = stdout.Close()
		_ = wait()
		return RuntimeCopyOut{}, fmt.Errorf("%w: create copy-out spool", ErrStreamUnavailable)
	}
	tempPath := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}
	rawMax := DefaultCopyOutLimits().MaxTotalBytes + runtimeTarOverheadBytes
	_, copyErr := CopyBounded(
		copyCtx,
		temp,
		stdout,
		rawMax,
		ErrStreamOutputTooLarge,
	)
	waitErr := wait()
	if copyErr != nil || waitErr != nil {
		cleanup()
		if copyErr != nil {
			return RuntimeCopyOut{}, copyErr
		}
		return RuntimeCopyOut{}, waitErr
	}
	stats, err := validateRuntimeTar(temp, DefaultCopyOutLimits())
	if err != nil {
		cleanup()
		return RuntimeCopyOut{}, err
	}
	if _, err := temp.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return RuntimeCopyOut{}, fmt.Errorf("%w: rewind copy-out spool", ErrStreamUnavailable)
	}
	return RuntimeCopyOut{
		Reader: &removeOnCloseFile{File: temp, path: tempPath},
		Files:  stats.Files,
		Bytes:  stats.Bytes,
	}, nil
}

func (a *DockerRuntimeAdapter) Mkdir(
	ctx context.Context,
	containerID string,
	dirs []string,
) error {
	if a == nil || a.run == nil || !safeRuntimeToken(containerID) ||
		len(dirs) == 0 || len(dirs) > MaxCopyFiles {
		return ErrRuntimePolicyDenied
	}
	cleaned := make([]string, 0, len(dirs))
	seen := make(map[string]struct{}, len(dirs))
	for _, dir := range dirs {
		target, err := a.mkdirPath(dir)
		if err != nil {
			return err
		}
		if _, duplicate := seen[target]; duplicate {
			return ErrRuntimePolicyDenied
		}
		seen[target] = struct{}{}
		cleaned = append(cleaned, target)
	}
	mkdirCtx, cancel := context.WithTimeout(ctx, RuntimeExecTimeout)
	defer cancel()
	args := []string{
		"exec",
		"--user=1000:1000",
		"--workdir=/workdir",
		containerID,
		"mkdir",
		"-p",
		"--",
	}
	args = append(args, cleaned...)
	if err := a.run(mkdirCtx, a.binary, args, nil, io.Discard, io.Discard); err != nil {
		if ctxErr := mkdirCtx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	return nil
}

func (a *DockerRuntimeAdapter) Inspect(
	ctx context.Context,
	containerID string,
) (RuntimeInspect, error) {
	if a == nil || a.output == nil || !safeRuntimeToken(containerID) {
		return RuntimeInspect{}, ErrRuntimePolicyDenied
	}
	inspectCtx, cancel := context.WithTimeout(ctx, runtimeDockerInspectTimeout)
	defer cancel()
	output, err := a.output(inspectCtx, a.binary, []string{"inspect", containerID})
	if err != nil {
		if dockerObjectMissing(output, err) {
			return RuntimeInspect{}, ErrRecoveryContainerMissing
		}
		if ctxErr := inspectCtx.Err(); ctxErr != nil {
			return RuntimeInspect{}, ctxErr
		}
		return RuntimeInspect{}, err
	}
	var values []struct {
		State struct {
			Status    string `json:"Status"`
			ExitCode  int    `json:"ExitCode"`
			OOMKilled bool   `json:"OOMKilled"`
		} `json:"State"`
	}
	if err := json.Unmarshal(output, &values); err != nil || len(values) != 1 ||
		strings.TrimSpace(values[0].State.Status) == "" {
		return RuntimeInspect{}, ErrRPCProtocol
	}
	return RuntimeInspect{
		Status:    values[0].State.Status,
		ExitCode:  values[0].State.ExitCode,
		OOMKilled: values[0].State.OOMKilled,
	}, nil
}

func (a *DockerRuntimeAdapter) Delete(
	ctx context.Context,
	containerID string,
) error {
	if a == nil || a.output == nil || !safeRuntimeToken(containerID) {
		return ErrRuntimePolicyDenied
	}
	deleteCtx, cancel := context.WithTimeout(ctx, RuntimeExecTimeout)
	defer cancel()
	output, err := a.output(deleteCtx, a.binary, []string{"rm", "-f", containerID})
	if err != nil {
		if dockerObjectMissing(output, err) {
			return nil
		}
		if ctxErr := deleteCtx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	return nil
}

func (a *DockerRuntimeAdapter) ListSandboxContainers(
	ctx context.Context,
) ([]RecoveryContainer, error) {
	if a == nil || a.output == nil {
		return nil, ErrRuntimePolicyDenied
	}
	listCtx, cancel := context.WithTimeout(ctx, runtimeDockerListTimeout)
	defer cancel()
	output, err := a.output(listCtx, a.binary, []string{
		"ps",
		"-a",
		"--filter",
		"label=numind.sandbox=1",
		"--format",
		`{{.ID}}	{{.Label "numind.sandbox.lease_id"}}	{{.Label "numind.sandbox.broker_instance"}}`,
	})
	if err != nil {
		if ctxErr := listCtx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 1 && strings.TrimSpace(lines[0]) == "" {
		return nil, nil
	}
	containers := make([]RecoveryContainer, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) != 3 ||
			!safeRuntimeToken(fields[0]) ||
			!safeRuntimeToken(fields[1]) ||
			!safeRuntimeToken(fields[2]) {
			return nil, ErrRPCProtocol
		}
		if _, duplicate := seen[fields[0]]; duplicate {
			return nil, ErrRPCProtocol
		}
		seen[fields[0]] = struct{}{}
		containers = append(containers, RecoveryContainer{
			ContainerID:    fields[0],
			LeaseID:        fields[1],
			BrokerInstance: fields[2],
		})
	}
	return containers, nil
}

func (a *DockerRuntimeAdapter) mkdirPath(raw string) (string, error) {
	if raw == "" || strings.ContainsRune(raw, 0) || !path.IsAbs(raw) ||
		hasParentSegment(raw) {
		return "", ErrStreamPolicyDenied
	}
	clean := path.Clean(raw)
	if clean != raw || clean == "/workdir" || clean == "/skills" {
		return "", ErrStreamPolicyDenied
	}
	probe := path.Join(clean, ".numind-dir-check")
	if _, err := a.policy.CopyInPath(probe); err != nil {
		return "", err
	}
	return clean, nil
}

func runtimeCopyOutArgs(containerID string, source CopyOutSource) []string {
	root := source.Root
	relative := source.Relative
	if source.ArchiveName == "" {
		root = path.Join(source.Root, source.Relative)
		relative = "."
	}
	return []string{
		"exec",
		"--user=1000:1000",
		"--workdir=/workdir",
		containerID,
		"tar",
		"-C",
		root,
		"-cf",
		"-",
		relative,
	}
}

func validRuntimeCopyOutSource(source CopyOutSource) bool {
	if source.Root != "/workdir" ||
		source.Relative == "" ||
		strings.ContainsRune(source.Relative, 0) ||
		path.IsAbs(source.Relative) ||
		hasParentSegment(source.Relative) ||
		(source.Relative != "output" &&
			!strings.HasPrefix(source.Relative, "output/")) {
		return false
	}
	if _, err := safeArchiveParts(source.Relative); err != nil {
		return false
	}
	if source.ArchiveName == "" {
		return true
	}
	return source.ArchiveName == path.Base(source.Relative) &&
		safePathSegment(source.ArchiveName)
}

func validateRuntimeTar(file *os.File, limits StreamLimits) (StreamStats, error) {
	if file == nil {
		return StreamStats{}, ErrStreamUnavailable
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return StreamStats{}, fmt.Errorf("%w: rewind copy-out validation", ErrStreamUnavailable)
	}
	reader := tar.NewReader(file)
	var stats StreamStats
	entries := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return stats, nil
		}
		if err != nil {
			return stats, fmt.Errorf("%w: read copy-out tar", ErrStreamPolicyDenied)
		}
		entries++
		if entries > limits.MaxFiles*4+16 {
			return stats, ErrStreamOutputTooLarge
		}
		parts, err := safeArchiveParts(header.Name)
		if err != nil {
			return stats, err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
			if len(parts) == 0 ||
				header.Size < 0 ||
				header.Size > limits.MaxSingleBytes ||
				stats.Files >= limits.MaxFiles ||
				header.Size > limits.MaxTotalBytes-stats.Bytes {
				return stats, ErrStreamOutputTooLarge
			}
			if _, err := io.CopyBuffer(
				io.Discard,
				reader,
				make([]byte, StreamBufferSize),
			); err != nil {
				return stats, fmt.Errorf("%w: read copy-out payload", ErrStreamPolicyDenied)
			}
			stats.Files++
			stats.Bytes += header.Size
		default:
			return stats, ErrStreamPolicyDenied
		}
	}
}

func runDockerRuntimeCommandWithEnv(
	ctx context.Context,
	binary string,
	args []string,
	env []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) error {
	command := exec.CommandContext(ctx, binary, args...)
	if env != nil {
		command.Env = append([]string(nil), env...)
	}
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

func outputDockerRuntimeCommandWithEnv(
	ctx context.Context,
	binary string,
	args []string,
	env []string,
) ([]byte, error) {
	command := exec.CommandContext(ctx, binary, args...)
	if env != nil {
		command.Env = append([]string(nil), env...)
	}
	return command.CombinedOutput()
}

func streamDockerRuntimeCommandWithEnv(
	ctx context.Context,
	binary string,
	args []string,
	env []string,
) (io.ReadCloser, func() error, error) {
	command := exec.CommandContext(ctx, binary, args...)
	if env != nil {
		command.Env = append([]string(nil), env...)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return nil, nil, err
	}
	return stdout, command.Wait, nil
}

func safeDockerHost(value string) bool {
	if value == "" {
		return true
	}
	const prefix = "unix://"
	if !strings.HasPrefix(value, prefix) || strings.ContainsRune(value, 0) {
		return false
	}
	socketPath := strings.TrimPrefix(value, prefix)
	if !filepath.IsAbs(socketPath) ||
		filepath.Clean(socketPath) != socketPath ||
		!strings.HasSuffix(filepath.Base(socketPath), ".sock") ||
		socketPath == "/var/run/docker.sock" ||
		socketPath == "/run/docker.sock" {
		return false
	}
	return strings.HasPrefix(socketPath, "/run/user/") ||
		strings.HasPrefix(socketPath, "/opt/numind-sandbox/")
}

func safeDockerConfigDir(value string) bool {
	if value == "" {
		return true
	}
	return filepath.IsAbs(value) &&
		filepath.Clean(value) == value &&
		strings.HasPrefix(value, "/opt/numind-sandbox/") &&
		!strings.ContainsRune(value, 0)
}

func dockerCommandEnv(dockerHost string, dockerConfigDir string) []string {
	env := []string{
		"PATH=/usr/bin:/bin",
		"LANG=C",
		"LC_ALL=C",
		"HOME=/nonexistent",
	}
	if dockerHost != "" {
		env = append(env, "DOCKER_HOST="+dockerHost)
	}
	if dockerConfigDir != "" {
		env = append(env, "DOCKER_CONFIG="+dockerConfigDir)
	}
	return env
}

func dockerObjectMissing(output []byte, err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(string(output))
	return strings.Contains(message, "no such container") ||
		strings.Contains(message, "no such object")
}

type runtimeOutputLimit struct {
	mu     sync.Mutex
	remain int64
	err    error
}

func (l *runtimeOutputLimit) Write(
	buffer *bytes.Buffer,
	chunk []byte,
) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return 0, l.err
	}
	if int64(len(chunk)) > l.remain {
		allowed := int(l.remain)
		if allowed > 0 {
			_, _ = buffer.Write(chunk[:allowed])
		}
		l.remain = 0
		l.err = ErrStreamOutputTooLarge
		return allowed, l.err
	}
	l.remain -= int64(len(chunk))
	return buffer.Write(chunk)
}

func (l *runtimeOutputLimit) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

type runtimeBoundedBuffer struct {
	limit  *runtimeOutputLimit
	buffer bytes.Buffer
}

func (b *runtimeBoundedBuffer) Write(chunk []byte) (int, error) {
	if b == nil || b.limit == nil {
		return 0, ErrStreamUnavailable
	}
	return b.limit.Write(&b.buffer, chunk)
}

func (b *runtimeBoundedBuffer) String() string {
	if b == nil || b.limit == nil {
		return ""
	}
	b.limit.mu.Lock()
	defer b.limit.mu.Unlock()
	return b.buffer.String()
}

type runtimeCountingReader struct {
	source io.Reader
	bytes  int64
	err    error
}

func (r *runtimeCountingReader) Read(buffer []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	count, err := r.source.Read(buffer)
	r.bytes += int64(count)
	if err != nil && !errors.Is(err, io.EOF) {
		r.err = err
	}
	return count, err
}

type removeOnCloseFile struct {
	*os.File
	path string
}

func (f *removeOnCloseFile) Close() error {
	closeErr := f.File.Close()
	removeErr := os.Remove(f.path)
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}
