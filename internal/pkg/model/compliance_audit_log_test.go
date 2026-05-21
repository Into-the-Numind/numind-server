package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestComplianceAuditLog_TableName(t *testing.T) {
	assert.Equal(t, "compliance_audit_log", ComplianceAuditLog{}.TableName())
}

func TestComplianceAuditLog_AutoMigrate(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&ComplianceAuditLog{}))

	// Verify the table exists with the right columns
	var tableInfo []map[string]interface{}
	require.NoError(t, db.Raw("PRAGMA table_info(compliance_audit_log)").Scan(&tableInfo).Error)
	require.NotEmpty(t, tableInfo)
}

// TestComplianceAuditLog_NullableFields verifies that the three *uint64 nullable fields
// (AgentRunID, AgentDefinitionID, RuleID) correctly persist as NULL when set to nil.
func TestComplianceAuditLog_NullableFields(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&ComplianceAuditLog{}))

	// Insert with all nullable *uint64 fields set to nil
	entry := &ComplianceAuditLog{
		AgentRunID:        nil,
		ParentUserID:      99,
		AgentDefinitionID: nil,
		RuleLayer:         RuleLayerL1,
		RuleID:            nil,
		Decision:          DecisionDeny,
		TriggeredText:     "sensitive content",
		Reason:            "matched forbid_topic rule",
	}
	require.NoError(t, db.Create(entry).Error)
	require.NotZero(t, entry.ID)

	// Re-read from DB and confirm nil fields are still nil
	var persisted ComplianceAuditLog
	require.NoError(t, db.First(&persisted, entry.ID).Error)

	assert.Nil(t, persisted.AgentRunID, "AgentRunID should remain nil when inserted as nil")
	assert.Nil(t, persisted.AgentDefinitionID, "AgentDefinitionID should remain nil when inserted as nil")
	assert.Nil(t, persisted.RuleID, "RuleID should remain nil when inserted as nil")
	assert.Equal(t, uint(99), persisted.ParentUserID)
	assert.Equal(t, RuleLayerL1, persisted.RuleLayer)
	assert.Equal(t, DecisionDeny, persisted.Decision)
}
