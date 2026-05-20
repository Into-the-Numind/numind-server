package sandbox

import (
	"context"
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
// /workdir. v1 stub returning ErrNotImplemented; full impl deferred to
// the file-management follow-up feature (learner upload / artifact
// download flow described in blueprint §4.6.5).
func WriteFile(_ context.Context, _ *Session, _ string, _ []byte, _ DockerClient) error {
	return ErrNotImplemented
}

// ReadFile reads `path` from inside the sandbox container's /workdir.
// v1 stub; see WriteFile note.
func ReadFile(_ context.Context, _ *Session, _ string, _ DockerClient) ([]byte, error) {
	return nil, ErrNotImplemented
}
