package compliance

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"numind-server/internal/pkg/compliance_scope"
	"numind-server/internal/pkg/model"
)

func newScopeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ComplianceRule{}))
	return db
}

func newTestAuditLogger(t *testing.T) (*AuditLogger, *fakeStore) {
	t.Helper()
	fs := &fakeStore{}
	l := NewAuditLogger(fs)
	l.Start()
	// No t.Cleanup here: individual tests call l.Stop() explicitly when needed.
	// Double-close of stopCh panics, so callers own the lifecycle.
	return l, fs
}

func TestScopeValidator_NonWhitelistTable_NotChecked(t *testing.T) {
	// Use a non-whitelist table; should not produce audit
	db := newScopeTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.AgentDefinitionHistory{}))
	audit, fs := newTestAuditLogger(t)
	v := NewScopeValidator(audit)
	require.NoError(t, v.Install(db))

	var rows []model.AgentDefinitionHistory
	_ = db.Find(&rows).Error // no WHERE clause; non-whitelist table → no audit

	// give async drain a moment
	_ = audit.Stop(context.Background())
	assert.Equal(t, 0, fs.count(), "non-whitelist table should not produce audit entry")
}

func TestScopeValidator_WhitelistTable_WithSkipScope_Passthrough(t *testing.T) {
	db := newScopeTestDB(t)
	audit, fs := newTestAuditLogger(t)
	v := NewScopeValidator(audit)
	require.NoError(t, v.Install(db))

	ctx := compliance_scope.WithSkipScope(context.Background(), "test_skip")
	var rules []model.ComplianceRule
	_ = db.WithContext(ctx).Find(&rules).Error // whitelist table + skip → passthrough audit

	_ = audit.Stop(context.Background())
	require.Equal(t, 1, fs.count())
	assert.Equal(t, model.RuleLayerScope, fs.written[0].RuleLayer)
	assert.Equal(t, model.DecisionPassthrough, fs.written[0].Decision)
}

func TestScopeValidator_WhitelistTable_WithFilter_NoWarn(t *testing.T) {
	db := newScopeTestDB(t)
	audit, fs := newTestAuditLogger(t)
	v := NewScopeValidator(audit)
	require.NoError(t, v.Install(db))

	var rules []model.ComplianceRule
	_ = db.Where("parent_user_id = ?", 42).Find(&rules).Error // proper filter → no audit

	_ = audit.Stop(context.Background())
	assert.Equal(t, 0, fs.count(), "scoped query should produce no audit")
}

func TestScopeValidator_WhitelistTable_NoFilter_WarnsAndAudits(t *testing.T) {
	db := newScopeTestDB(t)
	audit, fs := newTestAuditLogger(t)
	v := NewScopeValidator(audit)
	require.NoError(t, v.Install(db))

	var rules []model.ComplianceRule
	err := db.Find(&rules).Error // no WHERE clause on whitelist table → warn + deny audit
	require.NoError(t, err, "v1 fail-open: query still succeeds")

	_ = audit.Stop(context.Background())
	require.Equal(t, 1, fs.count())
	assert.Equal(t, model.RuleLayerScope, fs.written[0].RuleLayer)
	assert.Equal(t, model.DecisionDeny, fs.written[0].Decision)
	assert.Contains(t, fs.written[0].Reason, "fail-open")
}

func TestHasScopeFilter_BareColumns(t *testing.T) {
	assert.True(t, hasScopeFilter("SELECT * FROM compliance_rule WHERE parent_user_id = 42"))
	assert.True(t, hasScopeFilter("SELECT * FROM agent_run WHERE user_id = 1"))
}

func TestHasScopeFilter_BacktickQuoted(t *testing.T) {
	assert.True(t, hasScopeFilter("SELECT * FROM `compliance_rule` WHERE `parent_user_id` = 42"))
	assert.True(t, hasScopeFilter("SELECT * FROM `agent_run` WHERE `user_id` = 1"))
}

func TestHasScopeFilter_DoubleQuoted(t *testing.T) {
	assert.True(t, hasScopeFilter(`SELECT * FROM "compliance_rule" WHERE "parent_user_id" = 42`))
	assert.True(t, hasScopeFilter(`SELECT * FROM "agent_run" WHERE "user_id" = 1`))
}

func TestHasScopeFilter_NoFilter(t *testing.T) {
	assert.False(t, hasScopeFilter("SELECT * FROM agent_run"))
	assert.False(t, hasScopeFilter("SELECT * FROM compliance_rule WHERE id = 1"))
}

func TestScopeValidator_NilAuditLogger_DoesNotPanic(t *testing.T) {
	db := newScopeTestDB(t)
	v := NewScopeValidator(nil)
	require.NoError(t, v.Install(db))
	var rules []model.ComplianceRule
	_ = db.Find(&rules).Error // no panic even though audit is nil
}

func TestScopeValidator_Whitelist7Tables(t *testing.T) {
	// regression for S3 P2-1 fix (7 tables, not 6)
	expected := []string{
		"agent_run", "agent_session", "agent_session_memory", "user_global_memory",
		"agent_definition", "compliance_rule", "compliance_audit_log",
	}
	assert.Len(t, scopeWhitelistTables, 7)
	for _, table := range expected {
		assert.True(t, scopeWhitelistTables[table], "table %q should be in whitelist", table)
	}
}
