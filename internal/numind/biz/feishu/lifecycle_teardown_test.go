package feishu

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/model"
)

type retiredHomeFake struct {
	calls    int
	critical error
}

func (f *retiredHomeFake) WithRetiredHome(_ context.Context, _ uint, _ uint64, callback func(string) error) error {
	f.calls++
	if err := callback("/tmp/retired-home"); err != nil {
		return err
	}
	return f.critical
}

type logoutFake struct {
	calls int
	home  string
	err   error
}

func (f *logoutFake) Logout(_ context.Context, home string) error {
	f.calls++
	f.home = home
	return f.err
}

func TestRetiredWorkspaceTeardownRunsOnlyFixedLogoutInsideRetiredHome(t *testing.T) {
	vault := &retiredHomeFake{}
	runner := &logoutFake{}
	teardown, err := NewRetiredWorkspaceTeardown(vault, runner)
	require.NoError(t, err)
	result, err := teardown.LogoutRetired(context.Background(), 7, 4)
	require.NoError(t, err)
	require.True(t, result.LogoutAttempted)
	require.True(t, result.LogoutSucceeded)
	require.Equal(t, 1, vault.calls)
	require.Equal(t, 1, runner.calls)
	require.Equal(t, "/tmp/retired-home", runner.home)
}

func TestRetiredWorkspaceTeardownKeepsCLILogoutFailureAdvisory(t *testing.T) {
	vault := &retiredHomeFake{}
	runner := &logoutFake{err: errors.New("simulated CLI logout failure")}
	teardown, err := NewRetiredWorkspaceTeardown(vault, runner)
	require.NoError(t, err)

	result, err := teardown.LogoutRetired(context.Background(), 7, 4)
	require.NoError(t, err)
	require.True(t, result.LogoutAttempted)
	require.False(t, result.LogoutSucceeded)
	require.Equal(t, 1, vault.calls)
	require.Equal(t, 1, runner.calls)
}

func TestRetiredWorkspaceTeardownSkipsLogoutWhenRetiredVaultWasNeverMaterialized(t *testing.T) {
	fixture := newTask2VaultFixture(t, 7, 4)
	fixture.accounts.accounts[fixture.userID].Generation = fixture.generation + 1
	fixture.accounts.accounts[fixture.userID].ConnectionState = model.FeishuConnectionDisconnecting
	runner := &logoutFake{}
	teardown, err := NewRetiredWorkspaceTeardown(fixture.vault, runner)
	require.NoError(t, err)

	for range 2 {
		result, err := teardown.LogoutRetired(context.Background(), fixture.userID, fixture.generation)
		require.NoError(t, err, "a fenced generation without a credential snapshot is a safe teardown no-op")
		require.False(t, result.LogoutAttempted)
		require.False(t, result.LogoutSucceeded)
	}
	require.Zero(t, runner.calls, "there is no materialized HOME in which to run logout")
	task2RequireNoRuntimeHomes(t, fixture.runtimeBase)
}

func TestRetiredWorkspaceTeardownReturnsTypedCriticalFailureForHomeCleanup(t *testing.T) {
	vault := &retiredHomeFake{critical: errors.New("simulated retired HOME cleanup failure")}
	runner := &logoutFake{err: errors.New("simulated CLI logout failure")}
	teardown, err := NewRetiredWorkspaceTeardown(vault, runner)
	require.NoError(t, err)

	result, err := teardown.LogoutRetired(context.Background(), 7, 4)
	require.Error(t, err)
	require.ErrorIs(t, err, errRetiredWorkspaceCleanup)
	var critical *retiredWorkspaceCleanupError
	require.ErrorAs(t, err, &critical)
	require.True(t, result.LogoutAttempted)
	require.False(t, result.LogoutSucceeded, "critical HOME cleanup failure takes precedence over advisory CLI logout failure")
}
