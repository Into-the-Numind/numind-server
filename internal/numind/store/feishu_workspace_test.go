package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
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

func requireGormTagContains(t *testing.T, modelType reflect.Type, fieldName string, values ...string) {
	t.Helper()
	field, ok := modelType.FieldByName(fieldName)
	require.True(t, ok, "%s.%s must exist", modelType.Name(), fieldName)
	tag := field.Tag.Get("gorm")
	for _, value := range values {
		require.Contains(t, tag, value, "%s.%s gorm tag", modelType.Name(), fieldName)
	}
}

func readFeishuWorkspaceMigration(t *testing.T, filename string) string {
	t.Helper()
	_, testFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime must report the current test file")
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", "..", ".."))
	contents, err := os.ReadFile(filepath.Join(repoRoot, "migrations", filename))
	require.NoError(t, err)
	return string(contents)
}

func TestFeishuWorkspaceMigration_ExplicitlyMigratesExistingAccountUserID(t *testing.T) {
	forward := readFeishuWorkspaceMigration(t, "20260713_130000_feishu_personal_workspace.sql")
	rollback := readFeishuWorkspaceMigration(t, "20260713_130000_feishu_personal_workspace_rollback.sql")

	t.Run("forward to bigint unsigned", func(t *testing.T) {
		require.Contains(t, forward, "MODIFY COLUMN `user_id` BIGINT UNSIGNED NOT NULL")
	})
	t.Run("local rollback to int unsigned", func(t *testing.T) {
		require.Contains(t, rollback, "MODIFY COLUMN `user_id` INT UNSIGNED NOT NULL")
	})
	t.Run("rollback warns about uint32 overflow", func(t *testing.T) {
		require.Contains(t, rollback, "UINT32")
	})
}

func TestFeishuWorkspaceModels_MySQLSchemaContract(t *testing.T) {
	requireGormTagContains(t, reflect.TypeOf(model.UserThirdPartyAccount{}), "UserID", "type:bigint unsigned")
	requireGormTagContains(t, reflect.TypeOf(model.FeishuCLIVault{}), "UserID", "type:bigint unsigned", "primaryKey", "autoIncrement:false")
	requireGormTagContains(t, reflect.TypeOf(model.FeishuAuthSession{}), "UserID", "type:bigint unsigned")
	requireGormTagContains(t, reflect.TypeOf(model.FeishuOperation{}), "UserID", "type:bigint unsigned")
	requireGormTagContains(t, reflect.TypeOf(model.FeishuOperation{}), "AttemptCount", "type:int unsigned")

	for _, item := range []struct {
		modelType reflect.Type
		fields    []string
	}{
		{reflect.TypeOf(model.FeishuCLIVault{}), []string{"CreatedAt", "UpdatedAt"}},
		{reflect.TypeOf(model.FeishuAuthSession{}), []string{"ExpiresAt", "CreatedAt", "UpdatedAt"}},
		{reflect.TypeOf(model.FeishuOperation{}), []string{"CreatedAt", "UpdatedAt"}},
	} {
		for _, field := range item.fields {
			requireGormTagContains(t, item.modelType, field, "not null")
		}
	}
}

func TestFeishuWorkspaceStore_IdempotencyIsPerUser(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 1)
	createFeishuAccount(t, s, 8, 1)

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

func TestFeishuWorkspaceStore_CreateOperationRejectsStaleAccountGenerationBeforeInsert(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 2)

	stale := newFeishuOperation("operation-stale-create", 7, 1, "key-stale-create")
	stored, err := s.CreateOrGetOperation(ctx, stale)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.Nil(t, stored)

	var count int64
	require.NoError(t, s.db.Model(&model.FeishuOperation{}).
		Where("id = ? OR (user_id = ? AND idempotency_key = ?)", stale.ID, stale.UserID, stale.IdempotencyKey).
		Count(&count).Error)
	require.Zero(t, count, "stale generation must be rejected before any operation row is inserted")
}

func TestFeishuWorkspaceStore_ListSucceededCreatesForRunIsBoundedAndDeterministic(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	baseTime := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)

	for index := 0; index < 35; index++ {
		op := newFeishuOperation(fmt.Sprintf("run-create-%02d", index), 7, 3, fmt.Sprintf("run-key-%02d", index))
		op.AgentRunID = 77
		op.State = model.FeishuOperationSucceeded
		op.CommandPath = "docs +create"
		if index%2 == 1 {
			op.CommandPath = "wiki +node-create"
		}
		op.CreatedAt = baseTime.Add(time.Duration(index) * time.Second)
		require.NoError(t, s.db.Create(op).Error)
	}

	for index, mutate := range []func(*model.FeishuOperation){
		func(op *model.FeishuOperation) { op.UserID = 8 },
		func(op *model.FeishuOperation) { op.Generation = 4 },
		func(op *model.FeishuOperation) { op.AgentRunID = 78 },
		func(op *model.FeishuOperation) { op.State = model.FeishuOperationFailed },
		func(op *model.FeishuOperation) { op.CommandPath = "docs +fetch" },
	} {
		op := newFeishuOperation(fmt.Sprintf("filtered-create-%d", index), 7, 3, fmt.Sprintf("filtered-key-%d", index))
		op.AgentRunID = 77
		op.State = model.FeishuOperationSucceeded
		op.CommandPath = "docs +create"
		op.CreatedAt = baseTime.Add(time.Hour + time.Duration(index)*time.Second)
		mutate(op)
		require.NoError(t, s.db.Create(op).Error)
	}

	got, err := s.ListSucceededCreatesForRun(ctx, 7, 3, 77)
	require.NoError(t, err)
	require.Len(t, got, 32)
	require.Equal(t, "run-create-34", got[0].ID)
	require.Equal(t, "run-create-03", got[len(got)-1].ID)
	for _, operation := range got {
		require.Equal(t, uint(7), operation.UserID)
		require.EqualValues(t, 3, operation.Generation)
		require.EqualValues(t, 77, operation.AgentRunID)
		require.Equal(t, model.FeishuOperationSucceeded, operation.State)
		require.Contains(t, []string{"docs +create", "wiki +node-create"}, operation.CommandPath)
	}
}

func TestFeishuWorkspaceStore_VaultRevisionCASAndOwnership(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 1)
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

func TestFeishuWorkspaceStore_VaultRejectsStaleGenerationBeforeFirstWrite(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 2)

	stale := &model.FeishuCLIVault{
		UserID:     7,
		Generation: 1,
		Ciphertext: []byte("stale-vault"),
		KeyVersion: "v1",
		Checksum:   "stale-checksum",
	}
	require.ErrorIs(t, s.PutVaultCAS(ctx, stale, 0), gorm.ErrRecordNotFound)
	var count int64
	require.NoError(t, s.db.Model(&model.FeishuCLIVault{}).Where("user_id = ?", 7).Count(&count).Error)
	require.Zero(t, count, "a stale first write must not occupy the user's vault primary key")

	current := &model.FeishuCLIVault{
		UserID:     7,
		Generation: 2,
		Ciphertext: []byte("current-vault"),
		KeyVersion: "v1",
		Checksum:   "current-checksum",
	}
	require.NoError(t, s.PutVaultCAS(ctx, current, 0))
	require.EqualValues(t, 1, current.Revision)
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

	claimed, err := s.ClaimSession(ctx, 7, 3, "session-1", "worker-a", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = s.ClaimSession(ctx, 7, 3, "session-1", "worker-b", now.Add(10*time.Second), now.Add(2*time.Minute))
	require.NoError(t, err)
	require.False(t, claimed)
	claimed, err = s.ClaimSession(ctx, 7, 3, "session-1", "worker-a", now.Add(20*time.Second), now.Add(3*time.Minute))
	require.NoError(t, err)
	require.True(t, claimed, "the current owner may renew an unexpired lease")
	claimed, err = s.ClaimSession(ctx, 7, 3, "session-1", "worker-b", now.Add(4*time.Minute), now.Add(5*time.Minute))
	require.NoError(t, err)
	require.True(t, claimed, "another owner may claim an expired lease")

	updateNow := now.Add(4*time.Minute + 30*time.Second)
	require.ErrorIs(t, s.UpdateSessionState(ctx, 7, 3, "session-1", "worker-a", model.FeishuAuthSessionCompleted, updateNow, &updateNow), gorm.ErrRecordNotFound)
	require.NoError(t, s.UpdateSessionState(ctx, 7, 3, "session-1", "worker-b", model.FeishuAuthSessionCompleted, updateNow, &updateNow))
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

	claimed, err := s.ClaimOperation(ctx, 7, 1, op.ID, "worker-a", []string{model.FeishuOperationNotStarted}, now, now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = s.ClaimOperation(ctx, 7, 1, op.ID, "worker-b", []string{model.FeishuOperationNotStarted}, now.Add(10*time.Second), now.Add(2*time.Minute))
	require.NoError(t, err)
	require.False(t, claimed)
	claimed, err = s.ClaimOperation(ctx, 7, 1, op.ID, "worker-a", []string{model.FeishuOperationNotStarted}, now.Add(20*time.Second), now.Add(3*time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = s.ClaimOperation(ctx, 7, 1, op.ID, "worker-b", []string{model.FeishuOperationNotStarted}, now.Add(4*time.Minute), now.Add(5*time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)

	require.NoError(t, s.db.Model(&model.UserThirdPartyAccount{}).
		Where("user_id = ? AND provider = ?", 7, "lark").
		Update("generation", 2).Error)
	claimed, err = s.ClaimOperation(ctx, 7, 1, op.ID, "worker-c", []string{model.FeishuOperationNotStarted}, now.Add(6*time.Minute), now.Add(7*time.Minute))
	require.NoError(t, err)
	require.False(t, claimed, "an operation from a previous account generation must never be revived")
}

func TestFeishuWorkspaceStore_OperationClaimBindsExpectedSourceState(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 1)
	now := time.Now().UTC().Truncate(time.Millisecond)

	for index, state := range []string{
		model.FeishuOperationWaitingConnection,
		model.FeishuOperationSucceeded,
	} {
		op, err := s.CreateOrGetOperation(ctx, newFeishuOperation(
			fmt.Sprintf("operation-state-bound-%d", index), 7, 1, fmt.Sprintf("key-state-bound-%d", index),
		))
		require.NoError(t, err)
		require.NoError(t, s.db.Model(&model.FeishuOperation{}).Where("id = ?", op.ID).Update("state", state).Error)

		claimed, err := s.ClaimOperation(
			ctx, 7, 1, op.ID, "stale-worker",
			[]string{model.FeishuOperationNotStarted}, now, now.Add(time.Minute),
		)
		require.NoError(t, err)
		require.False(t, claimed)

		stored, err := s.GetOperationForUser(ctx, 7, 1, op.ID)
		require.NoError(t, err)
		require.Equal(t, state, stored.State)
		require.Empty(t, stored.LeaseOwner)
		require.Nil(t, stored.LeaseUntil)
	}
}

func TestFeishuWorkspaceStore_TransitionAndCancelAreConditional(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 1)
	now := time.Now().UTC().Truncate(time.Millisecond)

	first, err := s.CreateOrGetOperation(ctx, newFeishuOperation("operation-first", 7, 1, "key-first"))
	require.NoError(t, err)
	claimed, err := s.ClaimOperation(ctx, 7, 1, first.ID, "worker-a", []string{model.FeishuOperationNotStarted}, now, now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)
	transitionNow := now.Add(30 * time.Second)
	require.ErrorIs(t, s.TransitionOperation(ctx, 7, 1, first.ID, "worker-b", []string{model.FeishuOperationNotStarted}, model.FeishuOperationExecuting, transitionNow, nil), gorm.ErrRecordNotFound)
	require.ErrorIs(t, s.TransitionOperation(ctx, 7, 1, first.ID, "worker-a", []string{model.FeishuOperationWaitingConnection}, model.FeishuOperationExecuting, transitionNow, nil), gorm.ErrRecordNotFound)
	require.NoError(t, s.TransitionOperation(ctx, 7, 1, first.ID, "worker-a", []string{model.FeishuOperationNotStarted}, model.FeishuOperationExecuting, transitionNow, map[string]any{
		"started_at":    now,
		"attempt_count": 1,
	}))
	require.NoError(t, s.TransitionOperation(ctx, 7, 1, first.ID, "worker-a", []string{model.FeishuOperationExecuting}, model.FeishuOperationWaitingConnection, transitionNow, nil))

	second, err := s.CreateOrGetOperation(ctx, newFeishuOperation("operation-second", 7, 1, "key-second"))
	require.NoError(t, err)
	require.NoError(t, s.db.Model(&model.FeishuOperation{}).Where("id = ?", second.ID).Updates(map[string]any{
		"lease_owner": "stale-worker", "lease_until": now.Add(time.Minute),
	}).Error)
	terminal, err := s.CreateOrGetOperation(ctx, newFeishuOperation("operation-terminal", 7, 1, "key-terminal"))
	require.NoError(t, err)
	require.NoError(t, s.db.Model(&model.FeishuOperation{}).Where("id = ?", terminal.ID).Update("state", model.FeishuOperationSucceeded).Error)
	otherGeneration := newFeishuOperation("operation-new-generation", 7, 2, "key-new-generation")
	require.NoError(t, s.db.Create(otherGeneration).Error)

	require.NoError(t, s.CancelPendingForGeneration(ctx, 7, 1))
	for _, id := range []string{first.ID, second.ID} {
		var got model.FeishuOperation
		require.NoError(t, s.db.First(&got, "id = ?", id).Error)
		require.Equal(t, model.FeishuOperationCancelled, got.State)
		require.Empty(t, got.LeaseOwner)
		require.Nil(t, got.LeaseUntil)
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

	err := s.UpdateSessionState(ctx, 7, 1, "missing", "worker", model.FeishuAuthSessionFailed, now, &now)
	require.True(t, errors.Is(err, gorm.ErrRecordNotFound))
	err = s.TransitionOperation(ctx, 7, 1, "missing", "worker", []string{model.FeishuOperationExecuting}, model.FeishuOperationFailed, now, nil)
	require.True(t, errors.Is(err, gorm.ErrRecordNotFound))
}

func TestFeishuWorkspaceStore_SessionMutationsFenceTenantGenerationAndExpiredLease(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 3)
	now := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, s.CreateSession(ctx, newFeishuSession("session-fenced", 7, 3)))

	claimed, err := s.ClaimSession(ctx, 8, 3, "session-fenced", "worker-a", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.False(t, claimed)
	claimed, err = s.ClaimSession(ctx, 7, 4, "session-fenced", "worker-a", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.False(t, claimed)
	claimed, err = s.ClaimSession(ctx, 7, 3, "session-fenced", "worker-a", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)

	completedAt := now.Add(30 * time.Second)
	require.ErrorIs(t, s.UpdateSessionState(ctx, 8, 3, "session-fenced", "worker-a", model.FeishuAuthSessionCompleted, completedAt, &completedAt), gorm.ErrRecordNotFound)
	require.ErrorIs(t, s.UpdateSessionState(ctx, 7, 4, "session-fenced", "worker-a", model.FeishuAuthSessionCompleted, completedAt, &completedAt), gorm.ErrRecordNotFound)
	expiredNow := now.Add(2 * time.Minute)
	require.ErrorIs(t, s.UpdateSessionState(ctx, 7, 3, "session-fenced", "worker-a", model.FeishuAuthSessionCompleted, expiredNow, &expiredNow), gorm.ErrRecordNotFound)

	got, err := s.GetSessionForUser(ctx, 7, 3, "session-fenced")
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthSessionPending, got.State)
}

func TestFeishuWorkspaceStore_OperationMutationsFenceTenantGenerationAndExpiredLease(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 3)
	now := time.Now().UTC().Truncate(time.Millisecond)
	op, err := s.CreateOrGetOperation(ctx, newFeishuOperation("operation-fenced", 7, 3, "key-fenced"))
	require.NoError(t, err)

	claimed, err := s.ClaimOperation(ctx, 8, 3, op.ID, "worker-a", []string{model.FeishuOperationNotStarted}, now, now.Add(time.Minute))
	require.NoError(t, err)
	require.False(t, claimed)
	claimed, err = s.ClaimOperation(ctx, 7, 4, op.ID, "worker-a", []string{model.FeishuOperationNotStarted}, now, now.Add(time.Minute))
	require.NoError(t, err)
	require.False(t, claimed)
	claimed, err = s.ClaimOperation(ctx, 7, 3, op.ID, "worker-a", []string{model.FeishuOperationNotStarted}, now, now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)

	transitionNow := now.Add(30 * time.Second)
	require.ErrorIs(t, s.TransitionOperation(ctx, 8, 3, op.ID, "worker-a", []string{model.FeishuOperationNotStarted}, model.FeishuOperationExecuting, transitionNow, nil), gorm.ErrRecordNotFound)
	require.ErrorIs(t, s.TransitionOperation(ctx, 7, 4, op.ID, "worker-a", []string{model.FeishuOperationNotStarted}, model.FeishuOperationExecuting, transitionNow, nil), gorm.ErrRecordNotFound)
	expiredNow := now.Add(2 * time.Minute)
	require.ErrorIs(t, s.TransitionOperation(ctx, 7, 3, op.ID, "worker-a", []string{model.FeishuOperationNotStarted}, model.FeishuOperationExecuting, expiredNow, nil), gorm.ErrRecordNotFound)

	got, err := s.GetOperationForUser(ctx, 7, 3, op.ID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationNotStarted, got.State)
}

func TestFeishuWorkspaceStore_ExpiredSessionLeaseCannotComplete(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 1)
	now := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, s.CreateSession(ctx, newFeishuSession("session-expired", 7, 1)))
	claimed, err := s.ClaimSession(ctx, 7, 1, "session-expired", "worker-a", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)

	expiredNow := now.Add(2 * time.Minute)
	err = s.UpdateSessionState(ctx, 7, 1, "session-expired", "worker-a", model.FeishuAuthSessionCompleted, expiredNow, &expiredNow)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestFeishuWorkspaceStore_ExpiredOperationLeaseCannotTransition(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 1)
	now := time.Now().UTC().Truncate(time.Millisecond)
	op, err := s.CreateOrGetOperation(ctx, newFeishuOperation("operation-expired", 7, 1, "key-expired"))
	require.NoError(t, err)
	claimed, err := s.ClaimOperation(ctx, 7, 1, op.ID, "worker-a", []string{model.FeishuOperationNotStarted}, now, now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)

	expiredNow := now.Add(2 * time.Minute)
	err = s.TransitionOperation(ctx, 7, 1, op.ID, "worker-a", []string{model.FeishuOperationNotStarted}, model.FeishuOperationExecuting, expiredNow, nil)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestFeishuWorkspaceStore_TransitionRejectsFieldsOutsideAllowlist(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 1)
	now := time.Now().UTC().Truncate(time.Millisecond)

	for i, field := range []string{
		"id", "user_id", "generation", "lease_owner", "lease_until", "state",
		"command_path", "request_ciphertext", "idempotency_key",
	} {
		t.Run(field, func(t *testing.T) {
			op := newFeishuOperation(fmt.Sprintf("operation-protected-%d", i), 7, 1, fmt.Sprintf("key-protected-%d", i))
			stored, err := s.CreateOrGetOperation(ctx, op)
			require.NoError(t, err)
			claimed, err := s.ClaimOperation(ctx, 7, 1, stored.ID, "worker-a", []string{model.FeishuOperationNotStarted}, now, now.Add(time.Minute))
			require.NoError(t, err)
			require.True(t, claimed)

			err = s.TransitionOperation(ctx, 7, 1, stored.ID, "worker-a", []string{model.FeishuOperationNotStarted}, model.FeishuOperationExecuting, now.Add(time.Second), map[string]any{field: "mutated"})
			require.Error(t, err)
			require.ErrorContains(t, err, field)
			got, getErr := s.GetOperationForUser(ctx, 7, 1, stored.ID)
			require.NoError(t, getErr)
			require.Equal(t, model.FeishuOperationNotStarted, got.State)
		})
	}
}

func TestFeishuWorkspaceStore_TransitionAllowsBusinessResultFields(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 1)
	now := time.Now().UTC().Truncate(time.Millisecond)
	op, err := s.CreateOrGetOperation(ctx, newFeishuOperation("operation-result", 7, 1, "key-result"))
	require.NoError(t, err)
	claimed, err := s.ClaimOperation(ctx, 7, 1, op.ID, "worker-a", []string{model.FeishuOperationNotStarted}, now, now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)

	finishedAt := now.Add(10 * time.Second)
	require.NoError(t, s.TransitionOperation(ctx, 7, 1, op.ID, "worker-a", []string{model.FeishuOperationNotStarted}, model.FeishuOperationFailed, finishedAt, map[string]any{
		"attempt_count":       uint(1),
		"started_at":          now,
		"finished_at":         finishedAt,
		"error_type":          "permission",
		"error_subtype":       "missing_scope",
		"error_code":          "99991672",
		"result_ciphertext":   []byte("encrypted-result"),
		"result_summary_json": []byte(`{"ok":false}`),
	}))

	got, err := s.GetOperationForUser(ctx, 7, 1, op.ID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationFailed, got.State)
	require.EqualValues(t, 1, got.AttemptCount)
	require.Equal(t, []byte("encrypted-result"), got.ResultCiphertext)
}

func TestFeishuWorkspaceStore_TransitionReleasesLeaseOutsideExecuting(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 1)
	now := time.Now().UTC().Truncate(time.Millisecond)

	for index, target := range []string{
		model.FeishuOperationWaitingConnection,
		model.FeishuOperationWaitingAppScope,
		model.FeishuOperationWaitingUserAuth,
		model.FeishuOperationWaitingConfirmation,
		model.FeishuOperationSucceeded,
		model.FeishuOperationFailed,
		model.FeishuOperationUnknown,
		model.FeishuOperationCancelled,
	} {
		t.Run(target, func(t *testing.T) {
			op := newFeishuOperation(
				fmt.Sprintf("operation-release-%d", index),
				7,
				1,
				fmt.Sprintf("key-release-%d", index),
			)
			stored, err := s.CreateOrGetOperation(ctx, op)
			require.NoError(t, err)
			claimed, err := s.ClaimOperation(ctx, 7, 1, stored.ID, "worker-a", []string{model.FeishuOperationNotStarted}, now, now.Add(time.Minute))
			require.NoError(t, err)
			require.True(t, claimed)

			require.NoError(t, s.TransitionOperation(
				ctx,
				7,
				1,
				stored.ID,
				"worker-a",
				[]string{model.FeishuOperationNotStarted},
				target,
				now.Add(time.Second),
				nil,
			))

			got, err := s.GetOperationForUser(ctx, 7, 1, stored.ID)
			require.NoError(t, err)
			require.Empty(t, got.LeaseOwner)
			require.Nil(t, got.LeaseUntil)
		})
	}

	t.Run("executing retains lease", func(t *testing.T) {
		op := newFeishuOperation("operation-retain", 7, 1, "key-retain")
		stored, err := s.CreateOrGetOperation(ctx, op)
		require.NoError(t, err)
		leaseUntil := now.Add(time.Minute)
		claimed, err := s.ClaimOperation(ctx, 7, 1, stored.ID, "worker-a", []string{model.FeishuOperationNotStarted}, now, leaseUntil)
		require.NoError(t, err)
		require.True(t, claimed)
		require.NoError(t, s.TransitionOperation(
			ctx,
			7,
			1,
			stored.ID,
			"worker-a",
			[]string{model.FeishuOperationNotStarted},
			model.FeishuOperationExecuting,
			now.Add(time.Second),
			nil,
		))

		got, err := s.GetOperationForUser(ctx, 7, 1, stored.ID)
		require.NoError(t, err)
		require.Equal(t, "worker-a", got.LeaseOwner)
		require.NotNil(t, got.LeaseUntil)
		require.WithinDuration(t, leaseUntil, *got.LeaseUntil, time.Millisecond)
	})
}
