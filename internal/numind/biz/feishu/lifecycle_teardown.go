package feishu

import (
	"context"
	"errors"
)

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
// HOME. The caller owns whether a failure is advisory or terminal; lifecycle
// unbind treats it as best effort because vault deletion remains authoritative.
func (t *RetiredWorkspaceTeardown) LogoutRetired(ctx context.Context, userID uint, retiredGeneration uint64) error {
	if t == nil || t.vault == nil || t.runner == nil || userID == 0 || retiredGeneration == 0 {
		return errors.New("feishu retired workspace teardown unavailable")
	}
	return t.vault.WithRetiredHome(ctx, userID, retiredGeneration, func(home string) error {
		return t.runner.Logout(ctx, home)
	})
}
