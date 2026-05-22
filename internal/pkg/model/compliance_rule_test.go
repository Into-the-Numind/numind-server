package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestComplianceRule_TableName(t *testing.T) {
	assert.Equal(t, "compliance_rule", ComplianceRule{}.TableName())
}

func TestComplianceRule_AutoMigrate(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(SQLiteCreateComplianceRuleDDL).Error)

	// Verify the table exists with the right columns
	var tableInfo []map[string]interface{}
	require.NoError(t, db.Raw("PRAGMA table_info(compliance_rule)").Scan(&tableInfo).Error)
	require.NotEmpty(t, tableInfo)
}

// TestComplianceRule_CreateWithIsActiveFalse is a regression test documenting the
// GORM default:true bool gotcha described in database.md §6.
//
// When a model field has `gorm:"default:true"`, GORM v2 treats a struct's zero-value
// false as "not set" during db.Create(), so the DB DEFAULT (true) takes effect even
// when the caller explicitly passed IsActive=false.
//
// This test INTENTIONALLY asserts the BUG behavior (IsActive=true after inserting with
// IsActive=false). If this assertion ever flips to require.False, it means GORM changed
// behavior — re-evaluate whether the store-layer UpdateColumn fixup is still needed.
//
// The correct fix at the store layer is the UpdateColumn fixup pattern (see database.md §6):
//
//	wantActive := rule.IsActive
//	db.Create(&rule)
//	if !wantActive && rule.IsActive {
//	    db.Model(&rule).UpdateColumn("is_active", false)
//	    rule.IsActive = false
//	}
func TestComplianceRule_CreateWithIsActiveFalse(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(SQLiteCreateComplianceRuleDDL).Error)

	rule := &ComplianceRule{
		ParentUserID: 1,
		RuleType:     ComplianceRuleTypeForbidTopic,
		RuleText:     "no politics",
		Priority:     100,
		IsActive:     false, // explicitly false — but GORM default:true will override this on Create
	}
	require.NoError(t, db.Create(rule).Error)
	require.NotZero(t, rule.ID)

	// Re-read from DB to see what was actually persisted
	var persisted ComplianceRule
	require.NoError(t, db.First(&persisted, rule.ID).Error)

	t.Logf("Inserted IsActive=false; DB persisted IsActive=%v (expected true due to GORM default:true bug)", persisted.IsActive)

	// INTENTIONAL: assert the BUG behavior. GORM default:true causes IsActive=false
	// to be silently overridden to true during db.Create(). The store layer MUST use
	// the UpdateColumn fixup pattern (database.md §6) to correctly persist false.
	// If this test fails (i.e., persisted.IsActive is actually false), it means GORM
	// changed behavior and the fixup may no longer be necessary — re-evaluate.
	require.True(t, persisted.IsActive, "GORM default:true bug: db.Create() with IsActive=false persists as true; store layer must use UpdateColumn fixup")
}
