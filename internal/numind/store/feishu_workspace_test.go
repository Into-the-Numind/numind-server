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
	"sync"
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
		&model.FeishuOperationProofConsumption{},
		&model.FeishuOperationExecutionGate{},
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

func TestFeishuOperationProofConsumptionMigrationHasForwardAndRollback(t *testing.T) {
	forward := readFeishuWorkspaceMigration(t, "20260713_210000_feishu_operation_proof_consumption.sql")
	rollback := readFeishuWorkspaceMigration(t, "20260713_210000_feishu_operation_proof_consumption_rollback.sql")

	for _, fragment := range []string{
		"CREATE TABLE `feishu_operation_proof_consumption`",
		"`source_operation_id` CHAR(36) NOT NULL",
		"`consumer_operation_id` CHAR(36) NOT NULL",
		"PRIMARY KEY (`source_operation_id`)",
		"UNIQUE KEY `uniq_feishu_proof_consumer` (`consumer_operation_id`)",
		"`user_id` BIGINT UNSIGNED NOT NULL",
		"`generation` BIGINT UNSIGNED NOT NULL",
		"`agent_run_id` BIGINT UNSIGNED NOT NULL",
		"FOREIGN KEY (`source_operation_id`) REFERENCES `feishu_operation` (`id`)",
		"FOREIGN KEY (`consumer_operation_id`) REFERENCES `feishu_operation` (`id`)",
	} {
		require.Contains(t, forward, fragment)
	}
	require.Contains(t, rollback, "DROP TABLE IF EXISTS `feishu_operation_proof_consumption`")
}

func TestFeishuOperationExecutionGateMigrationHasForwardAndRollback(t *testing.T) {
	forward := readFeishuWorkspaceMigration(t, "20260713_220000_feishu_operation_execution_gate.sql")
	rollback := readFeishuWorkspaceMigration(t, "20260713_220000_feishu_operation_execution_gate_rollback.sql")

	for _, fragment := range []string{
		"CREATE TABLE `feishu_operation_execution_gate`",
		"`user_id` BIGINT UNSIGNED NOT NULL",
		"`generation` BIGINT UNSIGNED NOT NULL",
		"`lease_owner` VARCHAR(128) NOT NULL",
		"`operation_id` CHAR(36) NOT NULL",
		"`lease_until` DATETIME(3) NULL",
		"PRIMARY KEY (`user_id`)",
	} {
		require.Contains(t, forward, fragment)
	}
	require.Contains(t, rollback, "DROP TABLE IF EXISTS `feishu_operation_execution_gate`")
}

func TestFeishuWorkspaceModels_MySQLSchemaContract(t *testing.T) {
	requireGormTagContains(t, reflect.TypeOf(model.UserThirdPartyAccount{}), "UserID", "type:bigint unsigned")
	requireGormTagContains(t, reflect.TypeOf(model.FeishuCLIVault{}), "UserID", "type:bigint unsigned", "primaryKey", "autoIncrement:false")
	requireGormTagContains(t, reflect.TypeOf(model.FeishuAuthSession{}), "UserID", "type:bigint unsigned")
	requireGormTagContains(t, reflect.TypeOf(model.FeishuOperation{}), "UserID", "type:bigint unsigned")
	requireGormTagContains(t, reflect.TypeOf(model.FeishuOperation{}), "AttemptCount", "type:int unsigned")
	requireGormTagContains(t, reflect.TypeOf(model.FeishuOperationProofConsumption{}), "SourceOperationID", "primaryKey", "type:char(36)")
	requireGormTagContains(t, reflect.TypeOf(model.FeishuOperationProofConsumption{}), "ConsumerOperationID", "uniqueIndex:uniq_feishu_proof_consumer")
	requireGormTagContains(t, reflect.TypeOf(model.FeishuOperationExecutionGate{}), "UserID", "primaryKey", "autoIncrement:false", "type:bigint unsigned")

	for _, item := range []struct {
		modelType reflect.Type
		fields    []string
	}{
		{reflect.TypeOf(model.FeishuCLIVault{}), []string{"CreatedAt", "UpdatedAt"}},
		{reflect.TypeOf(model.FeishuAuthSession{}), []string{"ExpiresAt", "CreatedAt", "UpdatedAt"}},
		{reflect.TypeOf(model.FeishuOperation{}), []string{"CreatedAt", "UpdatedAt"}},
		{reflect.TypeOf(model.FeishuOperationProofConsumption{}), []string{"CreatedAt"}},
		{reflect.TypeOf(model.FeishuOperationExecutionGate{}), []string{"UpdatedAt"}},
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

func TestFeishuWorkspaceStore_ProofReservationIsAtomicSingleUseAndIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 1)
	source := newFeishuOperation("proof-source", 7, 1, "proof-source-key")
	source.AgentRunID = 700
	source.CommandPath = "docs +create"
	storedSource, err := s.CreateOrGetOperation(ctx, source)
	require.NoError(t, err)
	sourceFinishedAt := storedSource.CreatedAt.Add(time.Millisecond)
	require.NoError(t, s.db.Model(&model.FeishuOperation{}).Where("id = ?", storedSource.ID).
		Updates(map[string]any{
			"state": model.FeishuOperationSucceeded, "command_path": "docs +create", "finished_at": sourceFinishedAt,
		}).Error)
	storedSource.FinishedAt = &sourceFinishedAt

	first := newFeishuOperation("proof-consumer-first", 7, 1, "proof-consumer-key")
	first.AgentRunID = 700
	first.CommandPath = "docs +update"
	storedFirst, err := s.CreateOrGetOperationWithProof(ctx, first, storedSource.ID)
	require.NoError(t, err)
	require.Equal(t, first.ID, storedFirst.ID)
	usable, err := s.IsOperationProofUsable(ctx, 7, 1, 700, storedSource.ID, storedFirst.ID)
	require.NoError(t, err)
	require.True(t, usable, "the proof consumer itself is excluded from intermediate writes")

	retry := newFeishuOperation("proof-consumer-retry", 7, 1, "proof-consumer-key")
	retry.AgentRunID = 700
	storedRetry, err := s.CreateOrGetOperationWithProof(ctx, retry, storedSource.ID)
	require.NoError(t, err)
	require.Equal(t, storedFirst.ID, storedRetry.ID)

	second := newFeishuOperation("proof-consumer-second", 7, 1, "proof-consumer-second-key")
	second.AgentRunID = 700
	storedSecond, err := s.CreateOrGetOperationWithProof(ctx, second, storedSource.ID)
	require.ErrorIs(t, err, ErrFeishuProofReservationUnavailable)
	require.Nil(t, storedSecond)

	var secondCount int64
	require.NoError(t, s.db.Model(&model.FeishuOperation{}).Where("id = ?", second.ID).Count(&secondCount).Error)
	require.Zero(t, secondCount, "reservation conflict must roll back the candidate operation")
	var consumptionCount int64
	require.NoError(t, s.db.Table("feishu_operation_proof_consumption").Count(&consumptionCount).Error)
	require.EqualValues(t, 1, consumptionCount)

	intermediate := newFeishuOperation("proof-intermediate", 7, 1, "proof-intermediate-key")
	intermediate.AgentRunID = 700
	intermediate.CommandPath = "docs +update"
	intermediate.State = model.FeishuOperationExecuting
	intermediate.CreatedAt = storedSource.CreatedAt.Add(time.Millisecond)
	require.NoError(t, s.db.Create(intermediate).Error)
	usable, err = s.IsOperationProofUsable(ctx, 7, 1, 700, storedSource.ID, storedFirst.ID)
	require.NoError(t, err)
	require.False(t, usable)
}

func TestFeishuWorkspaceStore_ProofReservationRejectsIneligibleSourceWithoutGhostRows(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(s *feishuWorkspaceStore, source, consumer *model.FeishuOperation)
	}{
		{name: "different user", mutate: func(s *feishuWorkspaceStore, _ *model.FeishuOperation, consumer *model.FeishuOperation) {
			createFeishuAccount(t, s, 8, 1)
			consumer.UserID = 8
		}},
		{name: "different generation", mutate: func(s *feishuWorkspaceStore, _ *model.FeishuOperation, consumer *model.FeishuOperation) {
			require.NoError(t, s.db.Model(&model.UserThirdPartyAccount{}).
				Where("user_id = ? AND provider = ?", 7, "lark").Update("generation", 2).Error)
			consumer.Generation = 2
		}},
		{name: "different run", mutate: func(_ *feishuWorkspaceStore, _ *model.FeishuOperation, consumer *model.FeishuOperation) {
			consumer.AgentRunID++
		}},
		{name: "non create source", mutate: func(s *feishuWorkspaceStore, source, _ *model.FeishuOperation) {
			require.NoError(t, s.db.Model(&model.FeishuOperation{}).Where("id = ?", source.ID).
				Update("command_path", "docs +fetch").Error)
		}},
		{name: "non succeeded source", mutate: func(s *feishuWorkspaceStore, source, _ *model.FeishuOperation) {
			require.NoError(t, s.db.Model(&model.FeishuOperation{}).Where("id = ?", source.ID).
				Update("state", model.FeishuOperationFailed).Error)
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			s := newFeishuWorkspaceTestStore(t)
			createFeishuAccount(t, s, 7, 1)
			source := newFeishuOperation("ineligible-source", 7, 1, "ineligible-source-key")
			source.AgentRunID = 701
			storedSource, err := s.CreateOrGetOperation(ctx, source)
			require.NoError(t, err)
			sourceFinishedAt := storedSource.CreatedAt.Add(time.Millisecond)
			require.NoError(t, s.db.Model(&model.FeishuOperation{}).Where("id = ?", storedSource.ID).
				Updates(map[string]any{
					"state": model.FeishuOperationSucceeded, "command_path": "docs +create", "finished_at": sourceFinishedAt,
				}).Error)
			storedSource.FinishedAt = &sourceFinishedAt
			consumer := newFeishuOperation("ineligible-consumer", 7, 1, "ineligible-consumer-key")
			consumer.AgentRunID = 701
			testCase.mutate(s, storedSource, consumer)

			stored, err := s.CreateOrGetOperationWithProof(ctx, consumer, storedSource.ID)
			require.ErrorIs(t, err, ErrFeishuProofReservationUnavailable)
			require.Nil(t, stored)
			var operationCount int64
			require.NoError(t, s.db.Model(&model.FeishuOperation{}).Where("id = ?", consumer.ID).Count(&operationCount).Error)
			require.Zero(t, operationCount)
			var consumptionCount int64
			require.NoError(t, s.db.Table("feishu_operation_proof_consumption").Count(&consumptionCount).Error)
			require.Zero(t, consumptionCount)
		})
	}
}

func TestFeishuWorkspaceStore_ProofReservationRejectsSucceededIntermediateUpdate(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 1)
	baseTime := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	source := newFeishuOperation("intermediate-source", 7, 1, "intermediate-source-key")
	source.AgentRunID = 702
	source.CommandPath = "docs +create"
	source.State = model.FeishuOperationSucceeded
	source.CreatedAt = baseTime
	source.FinishedAt = &baseTime
	require.NoError(t, s.db.Create(source).Error)
	intermediate := newFeishuOperation("intermediate-update", 7, 1, "intermediate-update-key")
	intermediate.AgentRunID = 702
	intermediate.CommandPath = "docs +update"
	intermediate.State = model.FeishuOperationSucceeded
	intermediate.CreatedAt = baseTime.Add(time.Second)
	require.NoError(t, s.db.Create(intermediate).Error)
	consumer := newFeishuOperation("intermediate-consumer", 7, 1, "intermediate-consumer-key")
	consumer.AgentRunID = 702

	stored, err := s.CreateOrGetOperationWithProof(ctx, consumer, source.ID)
	require.ErrorIs(t, err, ErrFeishuProofReservationUnavailable)
	require.Nil(t, stored)
	var operationCount int64
	require.NoError(t, s.db.Model(&model.FeishuOperation{}).Where("id = ?", consumer.ID).Count(&operationCount).Error)
	require.Zero(t, operationCount)
}

func TestFeishuWorkspaceStore_ProofReservationRejectsSameMillisecondLowerUUIDUpdate(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 1)
	sameMillisecond := time.Date(2026, 7, 13, 12, 0, 0, 123000000, time.UTC)
	source := newFeishuOperation("ffffffff-ffff-4fff-bfff-ffffffffffff", 7, 1, "same-ms-source-key")
	source.AgentRunID = 703
	source.CommandPath = "docs +create"
	source.State = model.FeishuOperationSucceeded
	source.CreatedAt = sameMillisecond
	source.FinishedAt = &sameMillisecond
	require.NoError(t, s.db.Create(source).Error)
	intermediate := newFeishuOperation("00000000-0000-4000-8000-000000000000", 7, 1, "same-ms-update-key")
	intermediate.AgentRunID = 703
	intermediate.CommandPath = "docs +update"
	intermediate.State = model.FeishuOperationSucceeded
	intermediate.CreatedAt = sameMillisecond
	require.Less(t, intermediate.ID, source.ID, "the later UUID must sort before the source UUID")
	require.NoError(t, s.db.Create(intermediate).Error)
	consumer := newFeishuOperation("same-ms-consumer", 7, 1, "same-ms-consumer-key")
	consumer.AgentRunID = 703

	stored, err := s.CreateOrGetOperationWithProof(ctx, consumer, source.ID)
	require.ErrorIs(t, err, ErrFeishuProofReservationUnavailable)
	require.Nil(t, stored)
	var operationCount int64
	require.NoError(t, s.db.Model(&model.FeishuOperation{}).Where("id = ?", consumer.ID).Count(&operationCount).Error)
	require.Zero(t, operationCount)
}

func TestFeishuWorkspaceStore_ProofReservationRejectsIntermediateUpdateInEveryState(t *testing.T) {
	states := []string{
		model.FeishuOperationNotStarted,
		model.FeishuOperationExecuting,
		model.FeishuOperationWaitingConnection,
		model.FeishuOperationWaitingAppScope,
		model.FeishuOperationWaitingUserAuth,
		model.FeishuOperationWaitingConfirmation,
		model.FeishuOperationUnknown,
		model.FeishuOperationSucceeded,
		model.FeishuOperationFailed,
		model.FeishuOperationCancelled,
	}
	for index, state := range states {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			s := newFeishuWorkspaceTestStore(t)
			createFeishuAccount(t, s, 7, 1)
			baseTime := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
			source := newFeishuOperation(fmt.Sprintf("state-source-%d", index), 7, 1, fmt.Sprintf("state-source-key-%d", index))
			source.AgentRunID = 704
			source.CommandPath = "docs +create"
			source.State = model.FeishuOperationSucceeded
			source.CreatedAt = baseTime
			source.FinishedAt = &baseTime
			require.NoError(t, s.db.Create(source).Error)
			intermediate := newFeishuOperation(fmt.Sprintf("state-update-%d", index), 7, 1, fmt.Sprintf("state-update-key-%d", index))
			intermediate.AgentRunID = 704
			intermediate.CommandPath = "docs +update"
			intermediate.State = state
			intermediate.CreatedAt = baseTime.Add(time.Millisecond)
			require.NoError(t, s.db.Create(intermediate).Error)
			consumer := newFeishuOperation(fmt.Sprintf("state-consumer-%d", index), 7, 1, fmt.Sprintf("state-consumer-key-%d", index))
			consumer.AgentRunID = 704

			stored, err := s.CreateOrGetOperationWithProof(ctx, consumer, source.ID)
			require.ErrorIs(t, err, ErrFeishuProofReservationUnavailable)
			require.Nil(t, stored)
			var operationCount int64
			require.NoError(t, s.db.Model(&model.FeishuOperation{}).Where("id = ?", consumer.ID).Count(&operationCount).Error)
			require.Zero(t, operationCount)
		})
	}
}

func TestFeishuWorkspaceStore_ProofRejectsAnyOtherUpdateRegardlessOfTimestamps(t *testing.T) {
	finishedAt := time.Date(2026, 7, 13, 12, 0, 0, 123000000, time.UTC)
	testCases := []struct {
		name      string
		createdAt time.Time
		startedAt *time.Time
	}{
		{
			name:      "created before source but started after finish",
			createdAt: finishedAt.Add(-2 * time.Second),
			startedAt: timePointer(finishedAt.Add(time.Millisecond)),
		},
		{
			name:      "started at exact finish millisecond",
			createdAt: finishedAt.Add(-2 * time.Second),
			startedAt: timePointer(finishedAt),
		},
		{
			name:      "nil start created at exact finish millisecond",
			createdAt: finishedAt,
		},
		{
			name:      "nil start created after finish",
			createdAt: finishedAt.Add(time.Millisecond),
		},
		{
			name:      "created and started before finish",
			createdAt: finishedAt.Add(-2 * time.Second),
			startedAt: timePointer(finishedAt.Add(-time.Millisecond)),
		},
	}

	for caseIndex, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			for _, phase := range []string{"reservation", "revalidation"} {
				t.Run(phase, func(t *testing.T) {
					ctx := context.Background()
					s := newFeishuWorkspaceTestStore(t)
					createFeishuAccount(t, s, 7, 1)
					source := newFeishuOperation(fmt.Sprintf("finish-source-%d", caseIndex), 7, 1, fmt.Sprintf("finish-source-key-%d", caseIndex))
					source.AgentRunID = 705
					source.CommandPath = "docs +create"
					source.State = model.FeishuOperationSucceeded
					source.CreatedAt = finishedAt.Add(-10 * time.Second)
					source.FinishedAt = timePointer(finishedAt)
					require.NoError(t, s.db.Create(source).Error)

					consumer := newFeishuOperation(fmt.Sprintf("finish-consumer-%d", caseIndex), 7, 1, fmt.Sprintf("finish-consumer-key-%d", caseIndex))
					consumer.AgentRunID = 705
					consumer.CommandPath = "docs +update"
					blocker := newFeishuOperation(fmt.Sprintf("finish-blocker-%d", caseIndex), 7, 1, fmt.Sprintf("finish-blocker-key-%d", caseIndex))
					blocker.AgentRunID = 705
					blocker.CommandPath = "docs +update"
					blocker.State = model.FeishuOperationSucceeded
					blocker.CreatedAt = testCase.createdAt
					blocker.StartedAt = testCase.startedAt

					if phase == "reservation" {
						require.NoError(t, s.db.Create(blocker).Error)
						stored, err := s.CreateOrGetOperationWithProof(ctx, consumer, source.ID)
						require.ErrorIs(t, err, ErrFeishuProofReservationUnavailable)
						require.Nil(t, stored)
						return
					}

					stored, err := s.CreateOrGetOperationWithProof(ctx, consumer, source.ID)
					require.NoError(t, err)
					require.NoError(t, s.db.Create(blocker).Error)
					usable, err := s.IsOperationProofUsable(ctx, 7, 1, 705, source.ID, stored.ID)
					require.NoError(t, err)
					require.False(t, usable)
				})
			}
		})
	}
}

func TestFeishuWorkspaceStore_ProofSourceWithoutFinishFailsClosed(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 1)
	source := newFeishuOperation("unfinished-proof-source", 7, 1, "unfinished-proof-source-key")
	source.AgentRunID = 706
	source.CommandPath = "docs +create"
	source.State = model.FeishuOperationSucceeded
	require.Nil(t, source.FinishedAt)
	require.NoError(t, s.db.Create(source).Error)

	consumer := newFeishuOperation("unfinished-proof-consumer", 7, 1, "unfinished-proof-consumer-key")
	consumer.AgentRunID = 706
	consumer.CommandPath = "docs +update"
	stored, err := s.CreateOrGetOperationWithProof(ctx, consumer, source.ID)
	require.ErrorIs(t, err, ErrFeishuProofReservationUnavailable)
	require.Nil(t, stored)

	require.NoError(t, s.db.Create(consumer).Error)
	require.NoError(t, s.db.Create(&model.FeishuOperationProofConsumption{
		SourceOperationID: source.ID, ConsumerOperationID: consumer.ID,
		UserID: 7, Generation: 1, AgentRunID: 706,
	}).Error)
	usable, err := s.IsOperationProofUsable(ctx, 7, 1, 706, source.ID, consumer.ID)
	require.NoError(t, err)
	require.False(t, usable)
}

func timePointer(value time.Time) *time.Time {
	return &value
}

func TestFeishuWorkspaceStore_ExecutionGateClaimExpiryReleaseAndGenerationFence(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 1)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)

	claimed, err := s.TryClaimExecutionGate(ctx, 7, 1, "owner-a", "operation-a", now, now.Add(2*time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = s.TryClaimExecutionGate(ctx, 7, 1, "owner-a", "operation-b", now.Add(time.Second), now.Add(2*time.Minute))
	require.NoError(t, err)
	require.False(t, claimed, "an active owner cannot move its lease to another operation")
	claimed, err = s.TryClaimExecutionGate(ctx, 7, 1, "owner-b", "operation-b", now.Add(time.Second), now.Add(2*time.Minute))
	require.NoError(t, err)
	require.False(t, claimed)
	claimed, err = s.TryClaimExecutionGate(ctx, 7, 1, "owner-a", "operation-a", now.Add(2*time.Second), now.Add(2*time.Minute))
	require.NoError(t, err)
	require.True(t, claimed, "the same owner may renew its gate")
	claimed, err = s.TryClaimExecutionGate(ctx, 7, 1, "owner-b", "operation-b", now.Add(3*time.Minute), now.Add(5*time.Minute))
	require.NoError(t, err)
	require.True(t, claimed, "an expired gate must be crash-recoverable")

	released, err := s.ReleaseExecutionGate(ctx, 7, 1, "owner-a", now.Add(3*time.Minute))
	require.NoError(t, err)
	require.False(t, released, "a stale owner cannot release the current gate")
	released, err = s.ReleaseExecutionGate(ctx, 7, 1, "owner-b", now.Add(3*time.Minute))
	require.NoError(t, err)
	require.True(t, released)

	claimed, err = s.TryClaimExecutionGate(ctx, 7, 1, "owner-old-generation", "operation-old", now.Add(4*time.Minute), now.Add(6*time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, s.db.Model(&model.UserThirdPartyAccount{}).
		Where("user_id = ? AND provider = ?", 7, "lark").Update("generation", 2).Error)
	claimed, err = s.TryClaimExecutionGate(ctx, 7, 1, "owner-stale", "operation-stale", now.Add(4*time.Minute), now.Add(6*time.Minute))
	require.NoError(t, err)
	require.False(t, claimed, "a retired generation cannot claim or renew the gate")
	claimed, err = s.TryClaimExecutionGate(ctx, 7, 2, "owner-current", "operation-current", now.Add(4*time.Minute), now.Add(6*time.Minute))
	require.NoError(t, err)
	require.False(t, claimed, "a current generation cannot overlap an active retired-generation CLI")
	claimed, err = s.TryClaimExecutionGate(ctx, 7, 2, "owner-current", "operation-current", now.Add(7*time.Minute), now.Add(9*time.Minute))
	require.NoError(t, err)
	require.True(t, claimed, "the current generation may recover an expired retired-generation gate")
}

func TestFeishuWorkspaceStore_ExecutionGateClaimUsesFloorAtExpiryMillisecond(t *testing.T) {
	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	expiresAt := base.Add(time.Second + 123*time.Millisecond)
	for _, testCase := range []struct {
		name    string
		now     time.Time
		claimed bool
	}{
		{name: "500 microseconds before expiry", now: expiresAt.Add(-500 * time.Microsecond), claimed: false},
		{name: "at expiry", now: expiresAt, claimed: true},
		{name: "500 microseconds after expiry", now: expiresAt.Add(500 * time.Microsecond), claimed: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			s := newFeishuWorkspaceTestStore(t)
			createFeishuAccount(t, s, 7, 1)
			claimed, err := s.TryClaimExecutionGate(ctx, 7, 1, "owner-a", "operation-a", base, expiresAt)
			require.NoError(t, err)
			require.True(t, claimed)

			claimed, err = s.TryClaimExecutionGate(
				ctx, 7, 1, "owner-b", "operation-b", testCase.now, testCase.now.Add(time.Minute),
			)
			require.NoError(t, err)
			require.Equal(t, testCase.claimed, claimed)
			var gate model.FeishuOperationExecutionGate
			require.NoError(t, s.db.First(&gate, "user_id = ?", 7).Error)
			if testCase.claimed {
				require.Equal(t, "owner-b", gate.LeaseOwner)
			} else {
				require.Equal(t, "owner-a", gate.LeaseOwner)
			}
		})
	}
}

func TestFeishuWorkspaceStore_RenewExecutionGateRequiresActiveExactLeaseTuple(t *testing.T) {
	type executionGateRenewer interface {
		RenewExecutionGate(
			context.Context,
			uint,
			uint64,
			string,
			string,
			time.Time,
			time.Time,
		) (bool, error)
	}

	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 1)
	renewer, ok := any(s).(executionGateRenewer)
	if !ok {
		t.Fatal("workspace store must expose exact execution-gate renewal")
	}
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	claimed, err := s.TryClaimExecutionGate(ctx, 7, 1, "owner-a", "operation-a", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)

	renewedUntil := now.Add(2 * time.Minute)
	renewed, err := renewer.RenewExecutionGate(ctx, 7, 1, "owner-a", "operation-a", now.Add(30*time.Second), renewedUntil)
	require.NoError(t, err)
	require.True(t, renewed)
	var gate model.FeishuOperationExecutionGate
	require.NoError(t, s.db.First(&gate, "user_id = ?", 7).Error)
	require.NotNil(t, gate.LeaseUntil)
	require.Equal(t, renewedUntil, gate.LeaseUntil.UTC())

	for _, mismatch := range []struct {
		name       string
		generation uint64
		owner      string
		operation  string
	}{
		{name: "generation", generation: 2, owner: "owner-a", operation: "operation-a"},
		{name: "owner", generation: 1, owner: "owner-b", operation: "operation-a"},
		{name: "operation", generation: 1, owner: "owner-a", operation: "operation-b"},
	} {
		t.Run(mismatch.name, func(t *testing.T) {
			renewed, err := renewer.RenewExecutionGate(
				ctx, 7, mismatch.generation, mismatch.owner, mismatch.operation,
				now.Add(40*time.Second), now.Add(3*time.Minute),
			)
			require.NoError(t, err)
			require.False(t, renewed)
		})
	}

	expiredNow := renewedUntil.Add(time.Millisecond)
	renewed, err = renewer.RenewExecutionGate(ctx, 7, 1, "owner-a", "operation-a", expiredNow, expiredNow.Add(time.Minute))
	require.NoError(t, err)
	require.False(t, renewed, "an expired owner must never resurrect its lease")
	claimed, err = s.TryClaimExecutionGate(ctx, 7, 1, "owner-b", "operation-b", expiredNow, expiredNow.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)
	renewed, err = renewer.RenewExecutionGate(ctx, 7, 1, "owner-a", "operation-a", expiredNow.Add(time.Second), expiredNow.Add(2*time.Minute))
	require.NoError(t, err)
	require.False(t, renewed, "a stale owner must not renew over the new owner")
}

func TestFeishuWorkspaceStore_RenewExecutionGateFailsClosedInsideExpiryMillisecond(t *testing.T) {
	type executionGateRenewer interface {
		RenewExecutionGate(
			context.Context,
			uint,
			uint64,
			string,
			string,
			time.Time,
			time.Time,
		) (bool, error)
	}

	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 1)
	renewer, ok := any(s).(executionGateRenewer)
	if !ok {
		t.Fatal("workspace store must expose exact execution-gate renewal")
	}
	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	expiresAt := base.Add(time.Second + 123*time.Millisecond)
	claimed, err := s.TryClaimExecutionGate(ctx, 7, 1, "owner-a", "operation-a", base, expiresAt)
	require.NoError(t, err)
	require.True(t, claimed)

	renewed, err := renewer.RenewExecutionGate(
		ctx,
		7,
		1,
		"owner-a",
		"operation-a",
		expiresAt.Add(-500*time.Microsecond),
		expiresAt.Add(time.Minute),
	)
	require.NoError(t, err)
	require.False(t, renewed, "sub-millisecond time must fail closed at the DATETIME(3) boundary")
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
	renewed, err := s.RenewSession(ctx, 7, 3, "session-1", "worker-a", now.Add(20*time.Second), now.Add(3*time.Minute))
	require.NoError(t, err)
	require.True(t, renewed, "the exact current token may renew an unexpired pending lease")
	claimed, err = s.ClaimSession(ctx, 7, 3, "session-1", "worker-b", now.Add(4*time.Minute), now.Add(5*time.Minute))
	require.NoError(t, err)
	require.True(t, claimed, "another owner may claim an expired lease")
	renewed, err = s.RenewSession(ctx, 7, 3, "session-1", "worker-a", now.Add(4*time.Minute), now.Add(6*time.Minute))
	require.NoError(t, err)
	require.False(t, renewed, "an expired/taken-over token must never revive")
	claimed, err = s.ClaimSession(ctx, 7, 3, "session-1", "worker-a", now.Add(4*time.Minute), now.Add(6*time.Minute))
	require.NoError(t, err)
	require.False(t, claimed, "a stale token must not reacquire the session it previously owned")
	claimed, err = s.ClaimSession(ctx, 7, 3, "session-1", "worker-b", now.Add(6*time.Minute), now.Add(7*time.Minute))
	require.NoError(t, err)
	require.False(t, claimed, "an expired token must not revive its own lease")
	claimed, err = s.ClaimSession(ctx, 7, 3, "session-1", "worker-c", now.Add(6*time.Minute), now.Add(7*time.Minute))
	require.NoError(t, err)
	require.True(t, claimed, "a fresh token may take over an expired lease")

	updateNow := now.Add(6*time.Minute + 30*time.Second)
	require.ErrorIs(t, s.UpdateSessionState(ctx, 7, 3, "session-1", "worker-a", model.FeishuAuthSessionCompleted, updateNow, &updateNow), gorm.ErrRecordNotFound)
	require.ErrorIs(t, s.UpdateSessionState(ctx, 7, 3, "session-1", "worker-b", model.FeishuAuthSessionCompleted, updateNow, &updateNow), gorm.ErrRecordNotFound)
	require.NoError(t, s.UpdateSessionState(ctx, 7, 3, "session-1", "worker-c", model.FeishuAuthSessionCompleted, updateNow, &updateNow))
	got, err := s.GetSessionForUser(ctx, 7, 3, "session-1")
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthSessionCompleted, got.State)
	require.NotNil(t, got.CompletedAt)
	require.Empty(t, got.LeaseOwner)
	require.Nil(t, got.LeaseUntil)
	renewed, err = s.RenewSession(ctx, 7, 3, "session-1", "worker-c", updateNow, updateNow.Add(time.Minute))
	require.NoError(t, err)
	require.False(t, renewed, "terminal sessions cannot be renewed")
	require.ErrorIs(t, s.UpdateSessionState(ctx, 7, 3, "session-1", "worker-c", model.FeishuAuthSessionSuperseded, updateNow, nil), gorm.ErrRecordNotFound)
}

func TestFeishuWorkspaceStore_AuthSessionMutationsLockAccountBeforeSessionCAS(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 3)
	now := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, s.CreateSession(ctx, newFeishuSession("session-lock-order", 7, 3)))

	var mu sync.Mutex
	events := make([]string, 0, 4)
	record := func(kind string, tx *gorm.DB) {
		table := tx.Statement.Table
		if table == "" && tx.Statement.Schema != nil {
			table = tx.Statement.Schema.Table
		}
		mu.Lock()
		events = append(events, kind+":"+table)
		mu.Unlock()
	}
	require.NoError(t, s.db.Callback().Query().Before("gorm:query").Register(
		"test:feishu_auth_account_lock_order", func(tx *gorm.DB) { record("query", tx) },
	))
	require.NoError(t, s.db.Callback().Update().Before("gorm:update").Register(
		"test:feishu_auth_session_cas_order", func(tx *gorm.DB) { record("update", tx) },
	))
	resetEvents := func() {
		mu.Lock()
		events = events[:0]
		mu.Unlock()
	}
	requireAccountThenSession := func(label string) {
		mu.Lock()
		got := append([]string(nil), events...)
		mu.Unlock()
		require.NotEmpty(t, got, label)
		require.Equal(t, "query:user_third_party_account", got[0], label)
		require.Contains(t, got, "update:feishu_auth_session", label)
	}

	claimed, err := s.ClaimSession(ctx, 7, 3, "session-lock-order", "token-lock-order", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)
	requireAccountThenSession("claim")

	resetEvents()
	renewed, err := s.RenewSession(ctx, 7, 3, "session-lock-order", "token-lock-order", now.Add(time.Second), now.Add(2*time.Minute))
	require.NoError(t, err)
	require.True(t, renewed)
	requireAccountThenSession("renew")

	resetEvents()
	completedAt := now.Add(2 * time.Second)
	require.NoError(t, s.UpdateSessionState(ctx, 7, 3, "session-lock-order", "token-lock-order",
		model.FeishuAuthSessionCompleted, completedAt, &completedAt))
	requireAccountThenSession("terminal update")
	// Task 21 owns real-MySQL interleaving coverage; this SQLite callback test
	// protects the explicit account -> session mutation order in every dialect.
}

func TestFeishuWorkspaceStore_FinalizeSessionCompletedAtomicallyFencesAccountAndPendingState(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 3)
	require.NoError(t, s.db.Model(&model.UserThirdPartyAccount{}).
		Where("user_id = ? AND provider = ?", 7, "lark").
		Updates(map[string]any{"connection_state": model.FeishuConnectionWaitingUserAuth, "connected": false}).Error)
	now := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, s.CreateSession(ctx, newFeishuSession("session-finalize", 7, 3)))
	claimed, err := s.ClaimSession(ctx, 7, 3, "session-finalize", "token-finalize", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)

	err = s.FinalizeSessionCompleted(ctx, 7, 3, "session-finalize", "stale-token", model.FeishuConnectionConnected, true, now.Add(time.Second), model.FeishuConnectionEvidence{})
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	pending, err := s.GetSessionForUser(ctx, 7, 3, "session-finalize")
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthSessionPending, pending.State)
	var unchanged model.UserThirdPartyAccount
	require.NoError(t, s.db.Where("user_id = ? AND provider = ?", 7, "lark").Take(&unchanged).Error)
	require.NotEqual(t, model.FeishuConnectionConnected, unchanged.ConnectionState)

	completedAt := now.Add(2 * time.Second)
	require.NoError(t, s.FinalizeSessionCompleted(
		ctx, 7, 3, "session-finalize", "token-finalize", model.FeishuConnectionConnected, true, completedAt,
		model.FeishuConnectionEvidence{},
	))
	completed, err := s.GetSessionForUser(ctx, 7, 3, "session-finalize")
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthSessionCompleted, completed.State)
	require.Empty(t, completed.LeaseOwner)
	require.Nil(t, completed.LeaseUntil)
	var connected model.UserThirdPartyAccount
	require.NoError(t, s.db.Where("user_id = ? AND provider = ?", 7, "lark").Take(&connected).Error)
	require.Equal(t, model.FeishuConnectionConnected, connected.ConnectionState)
	require.True(t, connected.Connected)
}

func TestFeishuWorkspaceStore_CreateOrGetPendingSessionSerializesIntentAcrossInstances(t *testing.T) {
	ctx := context.Background()
	firstStore := newFeishuWorkspaceTestStore(t)
	secondStore := newFeishuWorkspaceStore(firstStore.db)
	createFeishuAccount(t, firstStore, 7, 3)
	operationID := "operation-1"
	now := time.Now().UTC().Truncate(time.Millisecond)

	first := newFeishuSession("session-first", 7, 3)
	first.OperationID = &operationID
	first.RequestedScopesJSON = []byte(`["docx:document:readonly","offline_access"]`)
	first.CreatedAt = now
	stored, created, err := firstStore.CreateOrGetPendingSession(ctx, first)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "session-first", stored.ID)

	duplicate := newFeishuSession("session-duplicate", 7, 3)
	duplicate.OperationID = &operationID
	duplicate.RequestedScopesJSON = []byte(`[ "docx:document:readonly" , "offline_access" ]`)
	got, created, err := secondStore.CreateOrGetPendingSession(ctx, duplicate)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, "session-first", got.ID, "the durable pending session is the cross-instance SOT")

	claimed, err := firstStore.ClaimSession(ctx, 7, 3, stored.ID, "worker-complete", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)
	completedAt := now.Add(time.Second)
	require.NoError(t, firstStore.UpdateSessionState(
		ctx, 7, 3, stored.ID, "worker-complete", model.FeishuAuthSessionCompleted, completedAt, &completedAt,
	))
	afterCompletion := newFeishuSession("session-after-completion", 7, 3)
	afterCompletion.OperationID = &operationID
	afterCompletion.RequestedScopesJSON = append([]byte(nil), first.RequestedScopesJSON...)
	got, created, err = secondStore.CreateOrGetPendingSession(ctx, afterCompletion)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, stored.ID, got.ID, "an operation recovery must observe its durable completion after restart")
	require.Equal(t, model.FeishuAuthSessionCompleted, got.State)

	wrongGeneration := newFeishuSession("session-stale", 7, 2)
	wrongGeneration.OperationID = &operationID
	wrongGeneration.RequestedScopesJSON = append([]byte(nil), first.RequestedScopesJSON...)
	_, _, err = secondStore.CreateOrGetPendingSession(ctx, wrongGeneration)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	manual := newFeishuSession("session-manual", 7, 3)
	manual.OperationID = nil
	manual.RequestedScopesJSON = []byte(`["offline_access"]`)
	manualStored, created, err := secondStore.CreateOrGetPendingSession(ctx, manual)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "session-manual", manualStored.ID, "manual intent must not alias an operation recovery")
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

func TestFeishuWorkspaceStore_RefreshOperationSession(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 1)
	op, err := s.CreateOrGetOperation(ctx, newFeishuOperation("operation-rebind", 7, 1, "key-rebind"))
	require.NoError(t, err)
	oldSummary := []byte(`{"status":"waiting_connection","phase":"create_app","session_id":"session-old","recovery_kind":"create_app"}`)
	replacementSummary := []byte(`{"status":"waiting_connection","phase":"create_app","session_id":"session-new","recovery_kind":"create_app"}`)
	require.NoError(t, s.db.Model(&model.FeishuOperation{}).Where("id = ?", op.ID).Updates(map[string]any{
		"state": model.FeishuOperationWaitingConnection, "result_summary_json": oldSummary,
	}).Error)
	oldSession := newFeishuSession("session-old", 7, 1)
	oldSession.OperationID = &op.ID
	oldSession.Phase = model.FeishuAuthPhaseCreateApp
	oldSession.RequestedScopesJSON = []byte(`["offline_access"]`)
	require.NoError(t, s.CreateSession(ctx, oldSession))
	replacement := newFeishuSession("session-new", 7, 1)
	replacement.OperationID = &op.ID
	replacement.Phase = model.FeishuAuthPhaseCreateApp
	replacement.RequestedScopesJSON = []byte(`["offline_access"]`)

	refreshed, err := s.RefreshOperationSession(
		ctx, 7, 1, "session-old", op.ID, model.FeishuOperationWaitingConnection, model.FeishuConnectionCreatingApp, replacement, replacementSummary, time.Now().UTC(),
	)
	require.NoError(t, err)
	require.Equal(t, replacement.ID, refreshed.ID)
	stored, err := s.GetOperationForUser(ctx, 7, 1, op.ID)
	require.NoError(t, err)
	require.JSONEq(t, string(replacementSummary), string(stored.ResultSummaryJSON))
	storedOld, err := s.GetSessionForUser(ctx, 7, 1, oldSession.ID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthSessionSuperseded, storedOld.State)
	storedReplacement, err := s.GetSessionForUser(ctx, 7, 1, replacement.ID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthSessionPending, storedReplacement.State)
	var account model.UserThirdPartyAccount
	require.NoError(t, s.db.Where("user_id = ? AND provider = ?", 7, "lark").Take(&account).Error)
	require.Equal(t, model.FeishuConnectionCreatingApp, account.ConnectionState)
	require.False(t, account.Connected)
}

func TestFeishuWorkspaceStore_RefreshOperationSessionRejectsLegacySourceWithLiveOrphanReplacement(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 1)
	op, err := s.CreateOrGetOperation(ctx, newFeishuOperation("operation-legacy-live-orphan", 7, 1, "key-legacy-live-orphan"))
	require.NoError(t, err)
	oldSummary := []byte(`{"status":"waiting_connection","phase":"create_app","session_id":"session-legacy-source","recovery_kind":"create_app"}`)
	replacementSummary := []byte(`{"status":"waiting_connection","phase":"create_app","session_id":"session-legacy-next","recovery_kind":"create_app"}`)
	require.NoError(t, s.db.Model(&model.FeishuOperation{}).Where("id = ?", op.ID).Updates(map[string]any{
		"state": model.FeishuOperationWaitingConnection, "result_summary_json": oldSummary,
	}).Error)
	old := newFeishuSession("session-legacy-source", 7, 1)
	old.OperationID = &op.ID
	old.Phase = model.FeishuAuthPhaseCreateApp
	old.RequestedScopesJSON = []byte(`["offline_access"]`)
	old.State = model.FeishuAuthSessionSuperseded
	require.NoError(t, s.CreateSession(ctx, old))
	orphan := newFeishuSession("session-legacy-orphan", 7, 1)
	orphan.OperationID = &op.ID
	orphan.Phase = model.FeishuAuthPhaseCreateApp
	orphan.RequestedScopesJSON = []byte(`["offline_access"]`)
	orphan.LeaseOwner = "legacy-worker"
	leaseUntil := time.Now().UTC().Add(time.Minute)
	orphan.LeaseUntil = &leaseUntil
	require.NoError(t, s.CreateSession(ctx, orphan))
	candidate := newFeishuSession("session-legacy-next", 7, 1)
	candidate.OperationID = &op.ID
	candidate.Phase = model.FeishuAuthPhaseCreateApp
	candidate.RequestedScopesJSON = []byte(`["offline_access"]`)

	_, err = s.RefreshOperationSession(
		ctx, 7, 1, old.ID, op.ID, model.FeishuOperationWaitingConnection, model.FeishuConnectionCreatingApp, candidate, replacementSummary, time.Now().UTC(),
	)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	stored, err := s.GetOperationForUser(ctx, 7, 1, op.ID)
	require.NoError(t, err)
	require.JSONEq(t, string(oldSummary), string(stored.ResultSummaryJSON))
	storedOrphan, err := s.GetSessionForUser(ctx, 7, 1, orphan.ID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthSessionPending, storedOrphan.State)
	require.Equal(t, "legacy-worker", storedOrphan.LeaseOwner)
	_, err = s.GetSessionForUser(ctx, 7, 1, candidate.ID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestFeishuWorkspaceStore_RestoreOperationSessionRefresh(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 1)
	op, err := s.CreateOrGetOperation(ctx, newFeishuOperation("operation-rebind-restore", 7, 1, "key-rebind-restore"))
	require.NoError(t, err)
	oldSummary := []byte(`{"status":"waiting_connection","phase":"create_app","session_id":"session-old-restore","recovery_kind":"create_app"}`)
	replacementSummary := []byte(`{"status":"waiting_connection","phase":"create_app","session_id":"session-new-restore","recovery_kind":"create_app"}`)
	require.NoError(t, s.db.Model(&model.FeishuOperation{}).Where("id = ?", op.ID).Updates(map[string]any{
		"state": model.FeishuOperationWaitingConnection, "result_summary_json": oldSummary,
	}).Error)
	oldSession := newFeishuSession("session-old-restore", 7, 1)
	oldSession.OperationID = &op.ID
	oldSession.Phase = model.FeishuAuthPhaseCreateApp
	oldSession.RequestedScopesJSON = []byte(`["offline_access"]`)
	require.NoError(t, s.CreateSession(ctx, oldSession))
	replacement := newFeishuSession("session-new-restore", 7, 1)
	replacement.OperationID = &op.ID
	replacement.Phase = model.FeishuAuthPhaseCreateApp
	replacement.RequestedScopesJSON = []byte(`["offline_access"]`)
	now := time.Now().UTC()
	_, err = s.RefreshOperationSession(
		ctx, 7, 1, oldSession.ID, op.ID, model.FeishuOperationWaitingConnection, model.FeishuConnectionCreatingApp, replacement, replacementSummary, now,
	)
	require.NoError(t, err)

	require.NoError(t, s.RestoreOperationSessionRefresh(
		ctx, 7, 1, oldSession.ID, replacement.ID, op.ID, model.FeishuOperationWaitingConnection, oldSummary, now.Add(time.Second),
	))
	stored, err := s.GetOperationForUser(ctx, 7, 1, op.ID)
	require.NoError(t, err)
	require.JSONEq(t, string(oldSummary), string(stored.ResultSummaryJSON))
	storedOld, err := s.GetSessionForUser(ctx, 7, 1, oldSession.ID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthSessionPending, storedOld.State)
	require.Empty(t, storedOld.LeaseOwner)
	require.Nil(t, storedOld.LeaseUntil)
	storedReplacement, err := s.GetSessionForUser(ctx, 7, 1, replacement.ID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthSessionSuperseded, storedReplacement.State)
}

func TestFeishuWorkspaceStore_RestoreOperationSessionRefreshRejectsLiveReplacementLease(t *testing.T) {
	ctx := context.Background()
	s := newFeishuWorkspaceTestStore(t)
	createFeishuAccount(t, s, 7, 1)
	op, err := s.CreateOrGetOperation(ctx, newFeishuOperation("operation-rebind-live-lease", 7, 1, "key-rebind-live-lease"))
	require.NoError(t, err)
	oldSummary := []byte(`{"status":"waiting_connection","phase":"create_app","session_id":"session-old-live-lease","recovery_kind":"create_app"}`)
	replacementSummary := []byte(`{"status":"waiting_connection","phase":"create_app","session_id":"session-new-live-lease","recovery_kind":"create_app"}`)
	require.NoError(t, s.db.Model(&model.FeishuOperation{}).Where("id = ?", op.ID).Updates(map[string]any{
		"state": model.FeishuOperationWaitingConnection, "result_summary_json": oldSummary,
	}).Error)
	oldSession := newFeishuSession("session-old-live-lease", 7, 1)
	oldSession.OperationID = &op.ID
	oldSession.Phase = model.FeishuAuthPhaseCreateApp
	oldSession.RequestedScopesJSON = []byte(`["offline_access"]`)
	require.NoError(t, s.CreateSession(ctx, oldSession))
	replacement := newFeishuSession("session-new-live-lease", 7, 1)
	replacement.OperationID = &op.ID
	replacement.Phase = model.FeishuAuthPhaseCreateApp
	replacement.RequestedScopesJSON = []byte(`["offline_access"]`)
	now := time.Now().UTC()
	_, err = s.RefreshOperationSession(
		ctx, 7, 1, oldSession.ID, op.ID, model.FeishuOperationWaitingConnection, model.FeishuConnectionCreatingApp, replacement, replacementSummary, now,
	)
	require.NoError(t, err)
	require.NoError(t, s.db.Model(&model.FeishuAuthSession{}).Where("id = ?", replacement.ID).Updates(map[string]any{
		"lease_owner": "another-service", "lease_until": now.Add(time.Minute),
	}).Error)

	err = s.RestoreOperationSessionRefresh(
		ctx, 7, 1, oldSession.ID, replacement.ID, op.ID, model.FeishuOperationWaitingConnection, oldSummary, now,
	)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	stored, err := s.GetOperationForUser(ctx, 7, 1, op.ID)
	require.NoError(t, err)
	require.JSONEq(t, string(replacementSummary), string(stored.ResultSummaryJSON))
	storedOld, err := s.GetSessionForUser(ctx, 7, 1, oldSession.ID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthSessionSuperseded, storedOld.State)
	storedReplacement, err := s.GetSessionForUser(ctx, 7, 1, replacement.ID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthSessionPending, storedReplacement.State)
	require.Equal(t, "another-service", storedReplacement.LeaseOwner)
}

func TestFeishuWorkspaceStore_RefreshOperationSessionRejectsStaleOrRetiredBindingWithoutPartialWrite(t *testing.T) {
	ctx := context.Background()
	newWaitingOperation := func(t *testing.T) (*feishuWorkspaceStore, *model.FeishuOperation, *model.FeishuAuthSession, *model.FeishuAuthSession, []byte, []byte) {
		t.Helper()
		s := newFeishuWorkspaceTestStore(t)
		createFeishuAccount(t, s, 7, 1)
		op, err := s.CreateOrGetOperation(ctx, newFeishuOperation("operation-rebind-"+strings.ReplaceAll(t.Name(), "/", "-"), 7, 1, "key-rebind-"+strings.ReplaceAll(t.Name(), "/", "-")))
		require.NoError(t, err)
		oldSession := newFeishuSession("session-old-"+strings.ReplaceAll(t.Name(), "/", "-"), 7, 1)
		oldSession.OperationID = &op.ID
		oldSession.Phase = model.FeishuAuthPhaseCreateApp
		oldSession.RequestedScopesJSON = []byte(`["offline_access"]`)
		require.NoError(t, s.CreateSession(ctx, oldSession))
		oldSummary := []byte(`{"status":"waiting_connection","phase":"create_app","session_id":"` + oldSession.ID + `","recovery_kind":"create_app"}`)
		require.NoError(t, s.db.Model(&model.FeishuOperation{}).Where("id = ?", op.ID).Updates(map[string]any{
			"state": model.FeishuOperationWaitingConnection, "result_summary_json": oldSummary,
		}).Error)
		replacement := newFeishuSession("session-new-"+strings.ReplaceAll(t.Name(), "/", "-"), 7, 1)
		replacement.OperationID = &op.ID
		replacement.Phase = model.FeishuAuthPhaseCreateApp
		replacement.RequestedScopesJSON = []byte(`["offline_access"]`)
		replacementSummary := []byte(`{"status":"waiting_connection","phase":"create_app","session_id":"` + replacement.ID + `","recovery_kind":"create_app"}`)
		return s, op, oldSession, replacement, oldSummary, replacementSummary
	}

	t.Run("old session mismatch", func(t *testing.T) {
		s, op, oldSession, replacement, oldSummary, replacementSummary := newWaitingOperation(t)
		_, err := s.RefreshOperationSession(ctx, 7, 1, "session-other", op.ID, model.FeishuOperationWaitingConnection, model.FeishuConnectionCreatingApp, replacement, replacementSummary, time.Now().UTC())
		require.ErrorIs(t, err, gorm.ErrRecordNotFound)
		stored, readErr := s.GetOperationForUser(ctx, 7, 1, op.ID)
		require.NoError(t, readErr)
		require.JSONEq(t, string(oldSummary), string(stored.ResultSummaryJSON))
		storedOld, readErr := s.GetSessionForUser(ctx, 7, 1, oldSession.ID)
		require.NoError(t, readErr)
		require.Equal(t, model.FeishuAuthSessionPending, storedOld.State)
		_, readErr = s.GetSessionForUser(ctx, 7, 1, replacement.ID)
		require.ErrorIs(t, readErr, gorm.ErrRecordNotFound)
	})

	t.Run("operation state mismatch", func(t *testing.T) {
		s, op, oldSession, replacement, oldSummary, replacementSummary := newWaitingOperation(t)
		require.NoError(t, s.db.Model(&model.FeishuOperation{}).Where("id = ?", op.ID).Update("state", model.FeishuOperationWaitingUserAuth).Error)
		_, err := s.RefreshOperationSession(ctx, 7, 1, oldSession.ID, op.ID, model.FeishuOperationWaitingConnection, model.FeishuConnectionCreatingApp, replacement, replacementSummary, time.Now().UTC())
		require.ErrorIs(t, err, gorm.ErrRecordNotFound)
		stored, readErr := s.GetOperationForUser(ctx, 7, 1, op.ID)
		require.NoError(t, readErr)
		require.JSONEq(t, string(oldSummary), string(stored.ResultSummaryJSON))
		storedOld, readErr := s.GetSessionForUser(ctx, 7, 1, oldSession.ID)
		require.NoError(t, readErr)
		require.Equal(t, model.FeishuAuthSessionPending, storedOld.State)
		_, readErr = s.GetSessionForUser(ctx, 7, 1, replacement.ID)
		require.ErrorIs(t, readErr, gorm.ErrRecordNotFound)
	})

	t.Run("retired account", func(t *testing.T) {
		s, op, oldSession, replacement, oldSummary, replacementSummary := newWaitingOperation(t)
		require.NoError(t, s.db.Model(&model.UserThirdPartyAccount{}).
			Where("user_id = ? AND provider = ?", 7, "lark").
			Update("connection_state", model.FeishuConnectionDisconnecting).Error)
		_, err := s.RefreshOperationSession(ctx, 7, 1, oldSession.ID, op.ID, model.FeishuOperationWaitingConnection, model.FeishuConnectionCreatingApp, replacement, replacementSummary, time.Now().UTC())
		require.ErrorIs(t, err, gorm.ErrRecordNotFound)
		stored, readErr := s.GetOperationForUser(ctx, 7, 1, op.ID)
		require.NoError(t, readErr)
		require.JSONEq(t, string(oldSummary), string(stored.ResultSummaryJSON))
		storedOld, readErr := s.GetSessionForUser(ctx, 7, 1, oldSession.ID)
		require.NoError(t, readErr)
		require.Equal(t, model.FeishuAuthSessionPending, storedOld.State)
		_, readErr = s.GetSessionForUser(ctx, 7, 1, replacement.ID)
		require.ErrorIs(t, readErr, gorm.ErrRecordNotFound)
	})
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
