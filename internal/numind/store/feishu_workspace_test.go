package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/pkg/model"
)

func newFeishuWorkspaceTestStore(t *testing.T) *feishuWorkspaceStore {
	t.Helper()

	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared&_busy_timeout=5000"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.UserThirdPartyAccount{},
		&model.FeishuCLIVault{},
		&model.FeishuAuthSession{},
		&model.FeishuOperation{},
	))

	return newFeishuWorkspaceStore(db)
}

func newFeishuOperation(id string, userID uint, generation uint64, key string) *model.FeishuOperation {
	return &model.FeishuOperation{
		ID:                 id,
		UserID:             userID,
		Generation:         generation,
		AgentRunID:         uint64(userID),
		ToolCallID:         fmt.Sprintf("call-%s", id),
		IdempotencyKey:     key,
		CommandPath:        "docs document get",
		Domain:             "docs",
		RiskLevel:          "read",
		RequestCiphertext:  []byte("encrypted-request"),
		KeyVersion:         "v1",
		RequestFingerprint: "request-fingerprint-" + id,
		State:              model.FeishuOperationNotStarted,
	}
}

func newFeishuSession(id string, userID uint, generation uint64) *model.FeishuAuthSession {
	return &model.FeishuAuthSession{
		ID:                  id,
		UserID:              userID,
		Generation:          generation,
		Phase:               model.FeishuAuthPhaseUserAuth,
		RequestedScopesJSON: []byte(`["docs:document:readonly"]`),
		State:               model.FeishuAuthSessionPending,
		ExpiresAt:           time.Now().UTC().Add(10 * time.Minute),
	}
}

func createFeishuAccount(t *testing.T, s *feishuWorkspaceStore, userID uint, generation uint64) {
	t.Helper()
	require.NoError(t, s.db.Create(&model.UserThirdPartyAccount{
		UserID:          userID,
		Provider:        "lark",
		AppID:           fmt.Sprintf("app-%d", userID),
		ConnectionState: model.FeishuConnectionConnected,
		Generation:      generation,
	}).Error)
}

func TestFeishuWorkspaceStore_IdempotencyIsPerUser(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)

	a, err := s.CreateOrGetOperation(ctx, newFeishuOperation("op-a", 7, 1, "same-key"))
	require.NoError(t, err)
	b, err := s.CreateOrGetOperation(ctx, newFeishuOperation("op-b", 7, 1, "same-key"))
	require.NoError(t, err)
	c, err := s.CreateOrGetOperation(ctx, newFeishuOperation("op-c", 8, 1, "same-key"))
	require.NoError(t, err)

	require.Equal(t, a.ID, b.ID)
	require.NotEqual(t, a.ID, c.ID)
	var count int64
	require.NoError(t, s.db.Model(&model.FeishuOperation{}).Count(&count).Error)
	require.EqualValues(t, 2, count)
}

func TestFeishuWorkspaceStore_VaultRevisionCASAndOwnership(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	vault := &model.FeishuCLIVault{
		UserID:     7,
		Generation: 1,
		Ciphertext: []byte("vault-v1"),
		KeyVersion: "v1",
		Checksum:   "checksum-v1",
	}

	require.NoError(t, s.PutVaultCAS(ctx, vault, 0))
	require.EqualValues(t, 1, vault.Revision)

	got, err := s.GetVault(ctx, 7, 1)
	require.NoError(t, err)
	require.Equal(t, []byte("vault-v1"), got.Ciphertext)
	_, err = s.GetVault(ctx, 7, 2)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	vault.Ciphertext = []byte("vault-v2")
	vault.Checksum = "checksum-v2"
	require.NoError(t, s.PutVaultCAS(ctx, vault, 1))
	require.EqualValues(t, 2, vault.Revision)

	stale := *vault
	stale.Ciphertext = []byte("stale-write")
	require.ErrorIs(t, s.PutVaultCAS(ctx, &stale, 1), gorm.ErrRecordNotFound)
	got, err = s.GetVault(ctx, 7, 1)
	require.NoError(t, err)
	require.Equal(t, []byte("vault-v2"), got.Ciphertext)

	require.ErrorIs(t, s.DeleteVault(ctx, 7, 2), gorm.ErrRecordNotFound)
	require.NoError(t, s.DeleteVault(ctx, 7, 1))
	_, err = s.GetVault(ctx, 7, 1)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestFeishuWorkspaceStore_SessionLeaseAndTenantGeneration(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 3)
	now := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, s.CreateSession(ctx, newFeishuSession("session-1", 7, 3)))

	_, err := s.GetSessionForUser(ctx, 8, 3, "session-1")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = s.GetSessionForUser(ctx, 7, 4, "session-1")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	claimed, err := s.ClaimSession(ctx, "session-1", "worker-a", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = s.ClaimSession(ctx, "session-1", "worker-b", now.Add(10*time.Second), now.Add(2*time.Minute))
	require.NoError(t, err)
	require.False(t, claimed)
	claimed, err = s.ClaimSession(ctx, "session-1", "worker-a", now.Add(20*time.Second), now.Add(3*time.Minute))
	require.NoError(t, err)
	require.True(t, claimed, "the current owner may renew an unexpired lease")
	claimed, err = s.ClaimSession(ctx, "session-1", "worker-b", now.Add(4*time.Minute), now.Add(5*time.Minute))
	require.NoError(t, err)
	require.True(t, claimed, "another owner may claim an expired lease")

	require.ErrorIs(t, s.UpdateSessionState(ctx, "session-1", "worker-a", model.FeishuAuthSessionCompleted, &now), gorm.ErrRecordNotFound)
	require.NoError(t, s.UpdateSessionState(ctx, "session-1", "worker-b", model.FeishuAuthSessionCompleted, &now))
	got, err := s.GetSessionForUser(ctx, 7, 3, "session-1")
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthSessionCompleted, got.State)
	require.NotNil(t, got.CompletedAt)
}

func TestFeishuWorkspaceStore_OperationLeaseRejectsStaleGeneration(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 1)
	now := time.Now().UTC().Truncate(time.Millisecond)
	op, err := s.CreateOrGetOperation(ctx, newFeishuOperation("operation-1", 7, 1, "key-1"))
	require.NoError(t, err)

	_, err = s.GetOperationForUser(ctx, 8, 1, op.ID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = s.GetOperationForUser(ctx, 7, 2, op.ID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	claimed, err := s.ClaimOperation(ctx, op.ID, "worker-a", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = s.ClaimOperation(ctx, op.ID, "worker-b", now.Add(10*time.Second), now.Add(2*time.Minute))
	require.NoError(t, err)
	require.False(t, claimed)
	claimed, err = s.ClaimOperation(ctx, op.ID, "worker-a", now.Add(20*time.Second), now.Add(3*time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = s.ClaimOperation(ctx, op.ID, "worker-b", now.Add(4*time.Minute), now.Add(5*time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)

	require.NoError(t, s.db.Model(&model.UserThirdPartyAccount{}).
		Where("user_id = ? AND provider = ?", 7, "lark").
		Update("generation", 2).Error)
	claimed, err = s.ClaimOperation(ctx, op.ID, "worker-c", now.Add(6*time.Minute), now.Add(7*time.Minute))
	require.NoError(t, err)
	require.False(t, claimed, "an operation from a previous account generation must never be revived")
}

func TestFeishuWorkspaceStore_TransitionAndCancelAreConditional(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 1)
	now := time.Now().UTC().Truncate(time.Millisecond)

	first, err := s.CreateOrGetOperation(ctx, newFeishuOperation("operation-first", 7, 1, "key-first"))
	require.NoError(t, err)
	claimed, err := s.ClaimOperation(ctx, first.ID, "worker-a", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)
	require.ErrorIs(t, s.TransitionOperation(ctx, first.ID, "worker-b", []string{model.FeishuOperationNotStarted}, model.FeishuOperationExecuting, nil), gorm.ErrRecordNotFound)
	require.ErrorIs(t, s.TransitionOperation(ctx, first.ID, "worker-a", []string{model.FeishuOperationWaitingConnection}, model.FeishuOperationExecuting, nil), gorm.ErrRecordNotFound)
	require.NoError(t, s.TransitionOperation(ctx, first.ID, "worker-a", []string{model.FeishuOperationNotStarted}, model.FeishuOperationExecuting, map[string]any{
		"started_at":    now,
		"attempt_count": 1,
	}))
	require.NoError(t, s.TransitionOperation(ctx, first.ID, "worker-a", []string{model.FeishuOperationExecuting}, model.FeishuOperationWaitingConnection, nil))

	second, err := s.CreateOrGetOperation(ctx, newFeishuOperation("operation-second", 7, 1, "key-second"))
	require.NoError(t, err)
	terminal, err := s.CreateOrGetOperation(ctx, newFeishuOperation("operation-terminal", 7, 1, "key-terminal"))
	require.NoError(t, err)
	require.NoError(t, s.db.Model(&model.FeishuOperation{}).Where("id = ?", terminal.ID).Update("state", model.FeishuOperationSucceeded).Error)
	otherGeneration, err := s.CreateOrGetOperation(ctx, newFeishuOperation("operation-new-generation", 7, 2, "key-new-generation"))
	require.NoError(t, err)

	require.NoError(t, s.CancelPendingForGeneration(ctx, 7, 1))
	for _, id := range []string{first.ID, second.ID} {
		var got model.FeishuOperation
		require.NoError(t, s.db.First(&got, "id = ?", id).Error)
		require.Equal(t, model.FeishuOperationCancelled, got.State)
	}
	var gotTerminal model.FeishuOperation
	require.NoError(t, s.db.First(&gotTerminal, "id = ?", terminal.ID).Error)
	require.Equal(t, model.FeishuOperationSucceeded, gotTerminal.State)
	var gotNewGeneration model.FeishuOperation
	require.NoError(t, s.db.First(&gotNewGeneration, "id = ?", otherGeneration.ID).Error)
	require.Equal(t, model.FeishuOperationNotStarted, gotNewGeneration.State)
}

func TestFeishuWorkspaceStore_IsRegisteredOnDatastore(t *testing.T) {
	s := newFeishuWorkspaceTestStore(t)
	ds := NewTestStore(s.db)
	require.NotNil(t, ds.FeishuWorkspace())
}

func TestFeishuWorkspaceStore_MissingConditionalRowsUseRecordNotFound(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	now := time.Now().UTC()

	err := s.UpdateSessionState(ctx, "missing", "worker", model.FeishuAuthSessionFailed, &now)
	require.True(t, errors.Is(err, gorm.ErrRecordNotFound))
	err = s.TransitionOperation(ctx, "missing", "worker", []string{model.FeishuOperationExecuting}, model.FeishuOperationFailed, nil)
	require.True(t, errors.Is(err, gorm.ErrRecordNotFound))
}
