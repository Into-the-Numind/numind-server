package feishu

import (
	"context"
	"errors"
)

var errRetiredWorkspaceCleanup = errors.New("feishu retired workspace cleanup unavailable")

// retiredWorkspaceCleanupError means the temporary retired HOME could not be
// safely materialized or removed. Its Error text is deliberately generic: the
// wrapped cause can contain local paths and is never returned by HTTP.
//
// A fixed CLI logout error is intentionally not represented by this type. It
// is advisory because the encrypted vault deletion below is the authoritative
// Numind-side credential removal. In contrast, this type stops unbind before
// vault deletion because a failed cleanup can leave readable local material.
type retiredWorkspaceCleanupError struct{ cause error }

func (e *retiredWorkspaceCleanupError) Error() string {
	return errRetiredWorkspaceCleanup.Error()
}

func (e *retiredWorkspaceCleanupError) Unwrap() error { return e.cause }

func (e *retiredWorkspaceCleanupError) Is(target error) bool {
	return target == errRetiredWorkspaceCleanup
}

// RetiredWorkspaceTeardownResult separates the best-effort CLI logout result
// from errors materializing or cleaning the retired HOME. The latter are
// returned as an error; callers must not delete the vault after one.
type RetiredWorkspaceTeardownResult struct {
	LogoutAttempted bool
	LogoutSucceeded bool
}

// retiredHomeOpener is the deliberately narrow post-retirement vault surface.
// It permits a fixed local logout command but never a reseal of old credentials.
type retiredHomeOpener interface {
	WithRetiredHome(context.Context, uint, uint64, func(string) error) error
}

// fixedLogoutRunner accepts no caller-controlled argv and runs only the pinned
// lark-cli auth logout --json command.
type fixedLogoutRunner interface {
	Logout(context.Context, string) error
}

// RetiredWorkspaceTeardown performs best-effort local logout after the account
// generation has been fenced. It cannot delete the remote self-built app.
type RetiredWorkspaceTeardown struct {
	vault  retiredHomeOpener
	runner fixedLogoutRunner
}

// NewRetiredWorkspaceTeardown validates the fixed teardown dependencies.
func NewRetiredWorkspaceTeardown(vault retiredHomeOpener, runner fixedLogoutRunner) (*RetiredWorkspaceTeardown, error) {
	if vault == nil || runner == nil {
		return nil, errors.New("feishu retired workspace teardown unavailable")
	}
	return &RetiredWorkspaceTeardown{vault: vault, runner: runner}, nil
}

// LogoutRetired runs the fixed logout command in a temporary, retired vault
// HOME. CLI logout failures are recorded in the result and remain advisory.
// Any returned error is structurally a critical retired-HOME materialization or
// cleanup failure, so lifecycle unbind must preserve disconnecting state and
// retry before it can delete the encrypted vault.
func (t *RetiredWorkspaceTeardown) LogoutRetired(
	ctx context.Context,
	userID uint,
	retiredGeneration uint64,
) (RetiredWorkspaceTeardownResult, error) {
	result := RetiredWorkspaceTeardownResult{}
	if t == nil || t.vault == nil || t.runner == nil || userID == 0 || retiredGeneration == 0 {
		return result, &retiredWorkspaceCleanupError{cause: errors.New("retired workspace teardown unavailable")}
	}
	var logoutErr error
	err := t.vault.WithRetiredHome(ctx, userID, retiredGeneration, func(home string) error {
		result.LogoutAttempted = true
		logoutErr = t.runner.Logout(ctx, home)
		// Do not return logoutErr to WithRetiredHome: it is an advisory fixed-CLI
		// failure. Returning nil lets the vault report only materialization and
		// cleanup errors, including a deferred os.RemoveAll failure.
		return nil
	})
	result.LogoutSucceeded = result.LogoutAttempted && logoutErr == nil
	if err != nil {
		return result, &retiredWorkspaceCleanupError{cause: err}
	}
	return result, nil
}
