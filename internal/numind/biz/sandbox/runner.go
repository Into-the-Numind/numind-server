package sandbox

import (
	"bytes"
	"context"
	"fmt"
)

// ExecCommand runs a shell command inside the borrowed sandbox session and
// returns the captured stdout/stderr + exit code. The command is run as
// /bin/sh -c "<cmd>" so callers don't need to handle quoting/splitting.
//
// Workdir is /workdir (the tmpfs declared by BuildSpawnConfig).
// User is sess.Config.UserSpec (defaults to 1000:1000).
// Timeout is sess.Config.Timeout (defaults to 30s).
func ExecCommand(ctx context.Context, sess *Session, cmd string, dc DockerClient) (ExecResult, error) {
	if sess == nil {
		return ExecResult{}, ErrSandboxDisabled
	}
	return dc.Exec(ctx, sess.ContainerID, []string{"/bin/sh", "-c", cmd}, ExecOpts{
		Timeout: sess.Config.Timeout,
		Workdir: "/workdir",
		User:    sess.Config.UserSpec,
	})
}

// WriteFile writes `content` to `path` inside the sandbox container's
// /workdir using docker cp. `path` must be a relative path (e.g. "foo.py");
// it is joined to /workdir/ inside the container. The file is owned by the
// container's user (1000:1000 by default).
//
// This replaces the v1 ErrNotImplemented stub. Required by Track 4 tasks
// (4.5–4.9) which inject generated Python scripts into /workdir via this
// function.
func WriteFile(ctx context.Context, sess *Session, path string, content []byte, dc DockerClient) error {
	if sess == nil {
		return ErrSandboxDisabled
	}
	// Resolve path inside container: always under /workdir/.
	containerPath := "/workdir/" + path
	return dc.CopyToContainer(ctx, sess.ContainerID, containerPath, bytes.NewReader(content))
}

// ReadFile reads `path` from inside the sandbox container's /workdir by
// running "cat <path>" via docker exec. For large files (>= a few MB) callers
// should prefer CopyFromContainer directly; this helper is optimised for small
// text outputs (stdout/logs/JSON results).
//
// `path` must be a relative path (e.g. "result.json"); it is read from
// /workdir/ inside the container.
func ReadFile(ctx context.Context, sess *Session, path string, dc DockerClient) ([]byte, error) {
	if sess == nil {
		return nil, ErrSandboxDisabled
	}
	containerPath := "/workdir/" + path
	res, err := dc.Exec(ctx, sess.ContainerID, []string{"cat", containerPath}, ExecOpts{
		Timeout: sess.Config.Timeout,
		Workdir: "/workdir",
		User:    sess.Config.UserSpec,
	})
	if err != nil {
		return nil, fmt.Errorf("ReadFile %s: %w", path, err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("ReadFile %s: cat exited %d: %s", path, res.ExitCode, res.Stderr)
	}
	return []byte(res.Stdout), nil
}
