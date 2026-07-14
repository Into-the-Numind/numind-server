package feishu

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type retiredHomeFake struct{ calls int }

func (f *retiredHomeFake) WithRetiredHome(_ context.Context, _ uint, _ uint64, callback func(string) error) error {
	f.calls++
	return callback("/tmp/retired-home")
}

type logoutFake struct {
	calls int
	home  string
}

func (f *logoutFake) Logout(_ context.Context, home string) error {
	f.calls++
	f.home = home
	return nil
}

func TestRetiredWorkspaceTeardownRunsOnlyFixedLogoutInsideRetiredHome(t *testing.T) {
	vault := &retiredHomeFake{}
	runner := &logoutFake{}
	teardown, err := NewRetiredWorkspaceTeardown(vault, runner)
	require.NoError(t, err)
	require.NoError(t, teardown.LogoutRetired(context.Background(), 7, 4))
	require.Equal(t, 1, vault.calls)
	require.Equal(t, 1, runner.calls)
	require.Equal(t, "/tmp/retired-home", runner.home)
}
