//go:build mysqlintegration

package store

import (
	"context"
	_ "embed"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"

	"numind-server/internal/pkg/model"
)

// feishuWorkspaceMySQLBaseline is the exact pre-feature compatibility surface
// that the three Feishu migrations alter. It is intentionally embedded so the
// opt-in test starts from a genuinely empty disposable database rather than
// relying on an operator to prepare a hidden partial schema.
//
//go:embed testdata/feishu_workspace_mysql_baseline.sql
var feishuWorkspaceMySQLBaseline string

// TestFeishuWorkspaceMySQLIntegration_MigrationAutoMigrateAndLocks is an
// opt-in MySQL 8 gate. CI remains SQLite-first; release verification supplies
// NUMIND_FEISHU_MYSQL_DSN and NUMIND_FEISHU_MIGRATIONS_DIR for an empty
// disposable, otherwise-empty database. It installs the explicit pre-feature
// baseline, applies checked-in migrations with multiStatements forced by the
// test, validates information_schema before AutoMigrate, and validates again
// afterwards. AutoMigrate therefore cannot mask a migration defect.
func TestFeishuWorkspaceMySQLIntegration_MigrationAutoMigrateAndLocks(t *testing.T) {
	dsn := os.Getenv("NUMIND_FEISHU_MYSQL_DSN")
	if dsn == "" {
		t.Skip("NUMIND_FEISHU_MYSQL_DSN is required for the MySQL 8 integration gate")
	}
	migrationsDir := os.Getenv("NUMIND_FEISHU_MIGRATIONS_DIR")
	require.NotEmpty(t, migrationsDir, "NUMIND_FEISHU_MIGRATIONS_DIR is required for the MySQL 8 integration gate")
	driverConfig, err := drivermysql.ParseDSN(dsn)
	require.NoError(t, err, "NUMIND_FEISHU_MYSQL_DSN must be a valid MySQL DSN")
	driverConfig.MultiStatements = true
	db, err := gorm.Open(mysql.Open(driverConfig.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	requireMySQLFeishuTablesAbsent(t, db)
	prepareMySQLFeishuBaseline(t, db)
	t.Cleanup(func() { dropMySQLFeishuGateTables(db) })
	applyFeishuWorkspaceMigrations(t, db, migrationsDir)
	requireMySQLFeishuSchema(t, db)
	require.NoError(t, db.AutoMigrate(
		&model.UserThirdPartyAccount{}, &model.AgentRun{}, &model.FeishuCLIVault{},
		&model.FeishuAuthSession{}, &model.FeishuOperation{}, &model.FeishuOperationProofConsumption{},
		&model.FeishuOperationExecutionGate{},
	))
	requireMySQLFeishuSchema(t, db)

	userID := uint(time.Now().UnixNano() & 0x7fffffff)
	accounts := newThirdPartyAccountStore(db)
	workspace := newFeishuWorkspaceStore(db)
	t.Cleanup(func() {
		_ = db.Where("user_id = ?", userID).Delete(&model.FeishuCLIVault{}).Error
		_ = db.Where("user_id = ? AND provider = ?", userID, "lark").Delete(&model.UserThirdPartyAccount{}).Error
	})
	require.NoError(t, db.Create(&model.UserThirdPartyAccount{
		UserID: userID, Provider: "lark", AppID: "cli_mysql_gate", Connected: true,
		ConnectionState: model.FeishuConnectionConnected, Generation: 1,
	}).Error)
	initial := &model.FeishuCLIVault{
		UserID: userID, Generation: 1, Ciphertext: []byte("initial"), KeyVersion: "v1", Checksum: "initial",
	}
	require.NoError(t, workspace.PutVaultCAS(context.Background(), initial, 0))
	require.EqualValues(t, 1, initial.Revision)

	// Hold the account row, prove PutVaultCAS is waiting on that same row, then
	// run the real RetireGeneration inside the locking transaction. If
	// PutVaultCAS ever loses its SELECT ... FOR UPDATE, the stale writer can
	// finish before the fence and this deterministic gate fails.
	barrier := db.Begin()
	require.NoError(t, barrier.Error)
	committedBarrier := false
	t.Cleanup(func() {
		if !committedBarrier {
			_ = barrier.Rollback().Error
		}
	})
	var locked model.UserThirdPartyAccount
	require.NoError(t, barrier.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_id = ? AND provider = ?", userID, "lark").Take(&locked).Error)
	writeDone := make(chan error, 1)
	go func() {
		candidate := &model.FeishuCLIVault{
			UserID: userID, Generation: 1, Ciphertext: []byte("racing-old-generation"), KeyVersion: "v1", Checksum: "race",
		}
		writeDone <- workspace.PutVaultCAS(context.Background(), candidate, 1)
	}()
	// Do not rely on scheduler timing: MySQL must report that the new writer has
	// reached, and is waiting on, the exact account row lock before the retire
	// transaction commits its generation bump.
	require.Eventually(t, func() bool {
		count, waitErr := mysqlAccountLockWaitCount(db)
		return waitErr == nil && count > 0
	}, time.Second, 5*time.Millisecond, "PutVaultCAS must reach the account FOR UPDATE wait before RetireGeneration")
	retired, next, err := newThirdPartyAccountStore(barrier).RetireGeneration(context.Background(), userID, "lark")
	require.NoError(t, err)
	require.EqualValues(t, 1, retired)
	require.EqualValues(t, 2, next)
	require.NoError(t, barrier.Commit().Error)
	committedBarrier = true
	require.ErrorIs(t, <-writeDone, gorm.ErrRecordNotFound, "old-generation CAS must observe the committed retire fence")

	account, err := accounts.Get(context.Background(), userID, "lark")
	require.NoError(t, err)
	require.EqualValues(t, 2, account.Generation)
	require.Equal(t, model.FeishuConnectionDisconnecting, account.ConnectionState)
	stale := &model.FeishuCLIVault{
		UserID: userID, Generation: 1, Ciphertext: []byte("stale"), KeyVersion: "v1", Checksum: "stale",
	}
	require.ErrorIs(t, workspace.PutVaultCAS(context.Background(), stale, 1), gorm.ErrRecordNotFound)
}

func requireMySQLFeishuTablesAbsent(t *testing.T, db *gorm.DB) {
	t.Helper()
	var count int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME IN ('user_third_party_account', 'agent_run', 'feishu_cli_vault', 'feishu_auth_session', 'feishu_operation', 'feishu_operation_proof_consumption', 'feishu_operation_execution_gate')`).Scan(&count).Error)
	require.Zero(t, count, "MySQL integration gate requires an otherwise-empty disposable database")
}

func prepareMySQLFeishuBaseline(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(feishuWorkspaceMySQLBaseline).Error, "create explicit pre-Feishu baseline")
}

func dropMySQLFeishuGateTables(db *gorm.DB) {
	for _, table := range []string{
		"feishu_operation_execution_gate", "feishu_operation_proof_consumption", "feishu_operation",
		"feishu_auth_session", "feishu_cli_vault", "agent_run", "user_third_party_account",
	} {
		_ = db.Exec("DROP TABLE IF EXISTS `" + table + "`").Error
	}
}

func mysqlAccountLockWaitCount(db *gorm.DB) (int64, error) {
	var count int64
	err := db.Raw(`SELECT COUNT(*)
		FROM performance_schema.data_lock_waits waits
		JOIN performance_schema.data_locks requested
		  ON requested.ENGINE = waits.ENGINE
		 AND requested.ENGINE_LOCK_ID = waits.REQUESTING_ENGINE_LOCK_ID
		WHERE requested.OBJECT_SCHEMA = DATABASE()
		  AND requested.OBJECT_NAME = 'user_third_party_account'
		  AND requested.LOCK_TYPE = 'RECORD'`).Scan(&count).Error
	return count, err
}

func applyFeishuWorkspaceMigrations(t *testing.T, db *gorm.DB, migrationsDir string) {
	t.Helper()
	for _, filename := range []string{
		"20260713_130000_feishu_personal_workspace.sql",
		"20260713_210000_feishu_operation_proof_consumption.sql",
		"20260713_220000_feishu_operation_execution_gate.sql",
	} {
		contents, err := os.ReadFile(filepath.Join(migrationsDir, filename))
		require.NoErrorf(t, err, "read checked-in migration %s", filename)
		require.NoErrorf(t, db.Exec(string(contents)).Error, "apply checked-in migration %s", filename)
	}
}

func requireMySQLFeishuSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	for table, columns := range mysqlFeishuExpectedColumns {
		for column, expected := range columns {
			requireMySQLColumn(t, db, table, column, expected)
		}
	}
	for _, expected := range mysqlFeishuExpectedIndexes {
		requireMySQLIndex(t, db, expected)
	}
	requireMySQLFeishuConstraints(t, db)
}

type mysqlColumnExpectation struct {
	columnType string
	nullable   string
	defaultVal *string
}

func mysqlDefault(value string) *string { return &value }

var mysqlFeishuExpectedColumns = map[string]map[string]mysqlColumnExpectation{
	"user_third_party_account": {
		"user_id":               {"bigint unsigned", "NO", nil},
		"connection_state":      {"varchar(32)", "NO", mysqlDefault("none")},
		"lark_cli_version":      {"varchar(32)", "YES", nil},
		"granted_scopes_json":   {"json", "YES", nil},
		"capability_state_json": {"json", "YES", nil},
		"last_success_at":       {"datetime(3)", "YES", nil},
		"last_error_code":       {"varchar(128)", "YES", nil},
		"generation":            {"bigint unsigned", "NO", mysqlDefault("1")},
	},
	"feishu_cli_vault": {
		"user_id": {"bigint unsigned", "NO", nil}, "generation": {"bigint unsigned", "NO", nil},
		"ciphertext": {"longblob", "NO", nil}, "key_version": {"varchar(32)", "NO", nil},
		"checksum": {"varchar(64)", "NO", nil}, "revision": {"bigint unsigned", "NO", nil},
		"created_at": {"datetime(3)", "NO", nil}, "updated_at": {"datetime(3)", "NO", nil},
	},
	"feishu_auth_session": {
		"id": {"char(36)", "NO", nil}, "user_id": {"bigint unsigned", "NO", nil}, "generation": {"bigint unsigned", "NO", nil},
		"operation_id": {"char(36)", "YES", nil}, "phase": {"varchar(32)", "NO", nil}, "requested_scopes_json": {"json", "NO", nil},
		"state": {"varchar(32)", "NO", nil}, "lease_owner": {"varchar(128)", "YES", nil}, "lease_until": {"datetime(3)", "YES", nil},
		"expires_at": {"datetime(3)", "NO", nil}, "created_at": {"datetime(3)", "NO", nil}, "updated_at": {"datetime(3)", "NO", nil},
		"completed_at": {"datetime(3)", "YES", nil},
	},
	"feishu_operation": {
		"id": {"char(36)", "NO", nil}, "user_id": {"bigint unsigned", "NO", nil}, "generation": {"bigint unsigned", "NO", nil},
		"agent_run_id": {"bigint unsigned", "NO", nil}, "tool_call_id": {"varchar(128)", "NO", nil}, "idempotency_key": {"varchar(191)", "NO", nil},
		"command_path": {"varchar(255)", "NO", nil}, "domain": {"varchar(32)", "NO", nil}, "risk_level": {"varchar(32)", "NO", nil},
		"request_ciphertext": {"longblob", "NO", nil}, "key_version": {"varchar(32)", "NO", nil}, "request_fingerprint": {"varchar(64)", "NO", nil},
		"state": {"varchar(32)", "NO", nil}, "attempt_count": {"int unsigned", "NO", mysqlDefault("0")},
		"lease_owner": {"varchar(128)", "YES", nil}, "lease_until": {"datetime(3)", "YES", nil},
		"error_type": {"varchar(64)", "YES", nil}, "error_subtype": {"varchar(128)", "YES", nil}, "error_code": {"varchar(128)", "YES", nil},
		"result_ciphertext": {"longblob", "YES", nil}, "result_summary_json": {"json", "YES", nil},
		"created_at": {"datetime(3)", "NO", nil}, "started_at": {"datetime(3)", "YES", nil}, "updated_at": {"datetime(3)", "NO", nil},
		"finished_at": {"datetime(3)", "YES", nil},
	},
	"feishu_operation_proof_consumption": {
		"source_operation_id": {"char(36)", "NO", nil}, "consumer_operation_id": {"char(36)", "NO", nil},
		"user_id": {"bigint unsigned", "NO", nil}, "generation": {"bigint unsigned", "NO", nil},
		"agent_run_id": {"bigint unsigned", "NO", nil}, "created_at": {"datetime(3)", "NO", nil},
	},
	"feishu_operation_execution_gate": {
		"user_id": {"bigint unsigned", "NO", nil}, "generation": {"bigint unsigned", "NO", nil},
		"lease_owner": {"varchar(128)", "NO", mysqlDefault("")}, "operation_id": {"char(36)", "NO", mysqlDefault("")},
		"lease_until": {"datetime(3)", "YES", nil}, "updated_at": {"datetime(3)", "NO", nil},
	},
	"agent_run": {
		"pending_external_action_json": {"json", "YES", nil}, "pending_external_action_at": {"datetime(3)", "YES", nil},
	},
}

type mysqlIndexExpectation struct {
	table     string
	name      string
	nonUnique bool
	columns   []string
}

var mysqlFeishuExpectedIndexes = []mysqlIndexExpectation{
	{"user_third_party_account", "PRIMARY", false, []string{"id"}},
	{"user_third_party_account", "uniq_user_provider", false, []string{"user_id", "provider"}},
	{"feishu_cli_vault", "PRIMARY", false, []string{"user_id"}},
	{"feishu_auth_session", "PRIMARY", false, []string{"id"}},
	{"feishu_auth_session", "idx_feishu_auth_session_user_generation", true, []string{"user_id", "generation"}},
	{"feishu_auth_session", "idx_feishu_auth_session_operation", true, []string{"operation_id"}},
	{"feishu_auth_session", "idx_feishu_auth_session_lease", true, []string{"state", "lease_until"}},
	{"feishu_operation", "PRIMARY", false, []string{"id"}},
	{"feishu_operation", "uniq_feishu_operation_user_key", false, []string{"user_id", "idempotency_key"}},
	{"feishu_operation", "idx_feishu_operation_user_generation", true, []string{"user_id", "generation"}},
	{"feishu_operation", "idx_feishu_operation_agent_tool", true, []string{"agent_run_id", "tool_call_id"}},
	{"feishu_operation", "idx_feishu_operation_lease", true, []string{"state", "lease_until"}},
	{"feishu_operation_proof_consumption", "PRIMARY", false, []string{"source_operation_id"}},
	{"feishu_operation_proof_consumption", "uniq_feishu_proof_consumer", false, []string{"consumer_operation_id"}},
	{"feishu_operation_proof_consumption", "idx_feishu_proof_audit", true, []string{"user_id", "generation", "agent_run_id"}},
	{"feishu_operation_execution_gate", "PRIMARY", false, []string{"user_id"}},
	{"feishu_operation_execution_gate", "idx_feishu_execution_gate_lease", true, []string{"lease_until"}},
}

func requireMySQLColumn(t *testing.T, db *gorm.DB, table, column string, expected mysqlColumnExpectation) {
	t.Helper()
	var got struct {
		ColumnType    string  `gorm:"column:COLUMN_TYPE"`
		IsNullable    string  `gorm:"column:IS_NULLABLE"`
		ColumnDefault *string `gorm:"column:COLUMN_DEFAULT"`
	}
	require.NoError(t, db.Raw(`SELECT COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`, table, column).Scan(&got).Error)
	require.NotEmptyf(t, got.ColumnType, "schema diff: missing %s.%s", table, column)
	require.Equalf(t, expected.columnType, strings.ToLower(got.ColumnType), "schema diff: type %s.%s", table, column)
	require.Equalf(t, expected.nullable, got.IsNullable, "schema diff: nullability %s.%s", table, column)
	if expected.defaultVal == nil {
		require.Nilf(t, got.ColumnDefault, "schema diff: unexpected default %s.%s", table, column)
	} else {
		require.NotNilf(t, got.ColumnDefault, "schema diff: missing default %s.%s", table, column)
		require.Equalf(t, *expected.defaultVal, *got.ColumnDefault, "schema diff: default %s.%s", table, column)
	}
}

func requireMySQLIndex(t *testing.T, db *gorm.DB, expected mysqlIndexExpectation) {
	t.Helper()
	var rows []struct {
		ColumnName string `gorm:"column:COLUMN_NAME"`
		NonUnique  uint8  `gorm:"column:NON_UNIQUE"`
	}
	require.NoError(t, db.Raw(`SELECT COLUMN_NAME, NON_UNIQUE FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND INDEX_NAME = ? ORDER BY SEQ_IN_INDEX`, expected.table, expected.name).Scan(&rows).Error)
	require.Lenf(t, rows, len(expected.columns), "schema diff: index %s.%s", expected.table, expected.name)
	for index, row := range rows {
		require.Equalf(t, expected.columns[index], row.ColumnName, "schema diff: index column %s.%s", expected.table, expected.name)
		require.Equalf(t, expected.nonUnique, row.NonUnique == 1, "schema diff: index uniqueness %s.%s", expected.table, expected.name)
	}
}

func requireMySQLFeishuConstraints(t *testing.T, db *gorm.DB) {
	t.Helper()
	var checkClause string
	require.NoError(t, db.Raw(`SELECT cc.CHECK_CLAUSE FROM information_schema.CHECK_CONSTRAINTS cc JOIN information_schema.TABLE_CONSTRAINTS tc ON tc.CONSTRAINT_SCHEMA = cc.CONSTRAINT_SCHEMA AND tc.CONSTRAINT_NAME = cc.CONSTRAINT_NAME WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'agent_run' AND tc.CONSTRAINT_NAME = 'chk_ar_state_reason'`).Scan(&checkClause).Error)
	checkClause = strings.ToLower(checkClause)
	require.Contains(t, checkClause, "external_resume_ready", "schema diff: agent_run external resume CHECK")
	require.Contains(t, checkClause, "ext_resume:%", "schema diff: agent_run external resume prefix CHECK")

	var foreignKeys []string
	require.NoError(t, db.Raw(`SELECT CONSTRAINT_NAME FROM information_schema.REFERENTIAL_CONSTRAINTS WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'feishu_operation_proof_consumption' ORDER BY CONSTRAINT_NAME`).Scan(&foreignKeys).Error)
	require.Equal(t, []string{"fk_feishu_proof_consumer_operation", "fk_feishu_proof_source_operation"}, foreignKeys, "schema diff: proof ledger foreign keys")
}
