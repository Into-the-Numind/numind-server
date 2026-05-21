package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// newTestComplianceStore creates an in-memory SQLite DB and returns an
// IComplianceStore backed by it. Uses the shared newTestDB helper which
// creates a WAL-mode file-backed SQLite DB for goroutine safety.
func newTestComplianceStore(t *testing.T) IComplianceStore {
	t.Helper()
	db := newTestDB(t, &model.ComplianceRule{}, &model.ComplianceAuditLog{})
	return newCompliance(db)
}

// sampleComplianceRule returns a minimal active ComplianceRule for parentUserID.
func sampleComplianceRule(parentUserID uint) *model.ComplianceRule {
	return &model.ComplianceRule{
		ParentUserID: parentUserID,
		RuleType:     model.ComplianceRuleTypeForbidTopic,
		RuleText:     "No competitor mentions",
		Priority:     100,
		IsActive:     true,
	}
}

// TestComplianceStore_ListRulesByParent_ActiveOnly verifies that activeOnly=true
// returns only is_active=1 rules.
func TestComplianceStore_ListRulesByParent_ActiveOnly(t *testing.T) {
	s := newTestComplianceStore(t)
	ctx := context.Background()

	r1 := sampleComplianceRule(1)
	r2 := sampleComplianceRule(1)
	r2.IsActive = false

	require.NoError(t, s.CreateRule(ctx, r1))
	require.NoError(t, s.CreateRule(ctx, r2))

	rules, err := s.ListRulesByParent(ctx, 1, true)
	require.NoError(t, err)
	assert.Len(t, rules, 1, "activeOnly=true must return only 1 active rule")
	assert.True(t, rules[0].IsActive)
}

// TestComplianceStore_ListRulesByParent_IncludeInactive verifies that
// activeOnly=false returns both active and inactive rules.
func TestComplianceStore_ListRulesByParent_IncludeInactive(t *testing.T) {
	s := newTestComplianceStore(t)
	ctx := context.Background()

	r1 := sampleComplianceRule(2)
	r2 := sampleComplianceRule(2)
	r2.IsActive = false

	require.NoError(t, s.CreateRule(ctx, r1))
	require.NoError(t, s.CreateRule(ctx, r2))

	rules, err := s.ListRulesByParent(ctx, 2, false)
	require.NoError(t, err)
	assert.Len(t, rules, 2, "activeOnly=false must return all rules including inactive")
}

// TestComplianceStore_ListRulesByParent_Sorted verifies ordering:
// priority ASC first, then created_at DESC for equal priority.
//
// Note: SQLite DATETIME has 1-second granularity, so we set CreatedAt
// explicitly on same-priority rules to guarantee a distinct ordering signal.
func TestComplianceStore_ListRulesByParent_Sorted(t *testing.T) {
	s := newTestComplianceStore(t)
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// priority=50 — highest priority, comes first
	rHigh := sampleComplianceRule(3)
	rHigh.Priority = 50
	rHigh.RuleText = "high priority"
	rHigh.CreatedAt = base

	// priority=200 — lowest priority, comes last
	rLow := sampleComplianceRule(3)
	rLow.Priority = 200
	rLow.RuleText = "low priority"
	rLow.CreatedAt = base

	// Two rules with same priority=100; created_at DESC → newer first
	rSame1 := sampleComplianceRule(3)
	rSame1.Priority = 100
	rSame1.RuleText = "same priority older"
	rSame1.CreatedAt = base.Add(-2 * time.Second) // older

	rSame2 := sampleComplianceRule(3)
	rSame2.Priority = 100
	rSame2.RuleText = "same priority newer"
	rSame2.CreatedAt = base.Add(2 * time.Second) // newer

	require.NoError(t, s.CreateRule(ctx, rHigh))
	require.NoError(t, s.CreateRule(ctx, rLow))
	require.NoError(t, s.CreateRule(ctx, rSame1))
	require.NoError(t, s.CreateRule(ctx, rSame2))

	rules, err := s.ListRulesByParent(ctx, 3, true)
	require.NoError(t, err)
	require.Len(t, rules, 4)

	// priority=50 must come first
	assert.Equal(t, "high priority", rules[0].RuleText)
	// within priority=100, newer created_at comes first (DESC)
	assert.Equal(t, "same priority newer", rules[1].RuleText)
	assert.Equal(t, "same priority older", rules[2].RuleText)
	// priority=200 must come last
	assert.Equal(t, "low priority", rules[3].RuleText)
}

// TestComplianceStore_GetRule_NotFound verifies errno.ErrComplianceRuleNotFound
// is returned when the ID does not exist.
func TestComplianceStore_GetRule_NotFound(t *testing.T) {
	s := newTestComplianceStore(t)
	ctx := context.Background()

	_, err := s.GetRule(ctx, 99999)
	require.Error(t, err)
	assert.True(t, errors.Is(err, errno.ErrComplianceRuleNotFound),
		"expected ErrComplianceRuleNotFound, got: %v", err)
}

// TestComplianceStore_GetRule_Found verifies a created rule can be fetched.
func TestComplianceStore_GetRule_Found(t *testing.T) {
	s := newTestComplianceStore(t)
	ctx := context.Background()

	r := sampleComplianceRule(1)
	r.RuleText = "test rule text"
	require.NoError(t, s.CreateRule(ctx, r))
	require.NotZero(t, r.ID)

	got, err := s.GetRule(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, "test rule text", got.RuleText)
	assert.Equal(t, uint(1), got.ParentUserID)
	assert.True(t, got.IsActive)
}

// TestComplianceStore_CreateRule_IsActiveTrue verifies the normal (happy-path)
// creation with IsActive=true persists correctly.
func TestComplianceStore_CreateRule_IsActiveTrue(t *testing.T) {
	s := newTestComplianceStore(t)
	ctx := context.Background()

	r := sampleComplianceRule(1)
	r.IsActive = true
	require.NoError(t, s.CreateRule(ctx, r))
	require.NotZero(t, r.ID)

	got, err := s.GetRule(ctx, r.ID)
	require.NoError(t, err)
	assert.True(t, got.IsActive, "is_active=true must persist")
}

// TestComplianceStore_CreateRule_IsActiveFalse_Fixup is the critical regression
// test for database.md §6: GORM default:true bool gotcha.
// When caller sets IsActive=false, the store must apply UpdateColumn fixup so
// the DB row actually has is_active=0 (not the DB default 1).
func TestComplianceStore_CreateRule_IsActiveFalse_Fixup(t *testing.T) {
	db := newTestDB(t, &model.ComplianceRule{})
	s := newCompliance(db)
	ctx := context.Background()

	r := sampleComplianceRule(1)
	r.IsActive = false // caller explicitly wants inactive

	require.NoError(t, s.CreateRule(ctx, r))
	require.NotZero(t, r.ID)
	assert.False(t, r.IsActive, "struct field must be false after fixup")

	// Read back from DB to confirm the fixup wrote is_active=0
	var raw model.ComplianceRule
	require.NoError(t, db.First(&raw, r.ID).Error)
	assert.False(t, raw.IsActive, "DB row must have is_active=0 after fixup")
}

// TestComplianceStore_UpdateRule_MapForm verifies that UpdateRule with a map
// containing is_active=false correctly persists the false value (not skipped).
func TestComplianceStore_UpdateRule_MapForm(t *testing.T) {
	s := newTestComplianceStore(t)
	ctx := context.Background()

	r := sampleComplianceRule(1)
	require.NoError(t, s.CreateRule(ctx, r))

	// Update both rule_text and is_active=false via map form
	err := s.UpdateRule(ctx, r.ID, map[string]interface{}{
		"rule_text": "updated text",
		"is_active": false,
	})
	require.NoError(t, err)

	got, err := s.GetRule(ctx, r.ID)
	// GetRule uses First which doesn't filter is_active, so we can still get it
	require.NoError(t, err)
	assert.Equal(t, "updated text", got.RuleText)
	assert.False(t, got.IsActive, "map-form Updates must persist is_active=false")
}

// TestComplianceStore_SoftDeleteRule verifies that after SoftDeleteRule,
// a ListRulesByParent with activeOnly=true does not see the rule.
func TestComplianceStore_SoftDeleteRule(t *testing.T) {
	s := newTestComplianceStore(t)
	ctx := context.Background()

	r1 := sampleComplianceRule(1)
	r2 := sampleComplianceRule(1)
	require.NoError(t, s.CreateRule(ctx, r1))
	require.NoError(t, s.CreateRule(ctx, r2))

	// Soft-delete r1
	require.NoError(t, s.SoftDeleteRule(ctx, r1.ID))

	rules, err := s.ListRulesByParent(ctx, 1, true) // activeOnly=true
	require.NoError(t, err)
	assert.Len(t, rules, 1, "soft-deleted rule must not appear in activeOnly list")
	assert.Equal(t, r2.ID, rules[0].ID)

	// activeOnly=false should still see both (inactive included)
	allRules, err := s.ListRulesByParent(ctx, 1, false)
	require.NoError(t, err)
	assert.Len(t, allRules, 2)
}

// TestComplianceStore_WriteAuditLog verifies basic audit log write succeeds
// and the row is persisted with expected field values.
func TestComplianceStore_WriteAuditLog(t *testing.T) {
	s := newTestComplianceStore(t)
	ctx := context.Background()

	entry := &model.ComplianceAuditLog{
		ParentUserID:  42,
		RuleLayer:     model.RuleLayerL1,
		Decision:      model.DecisionDeny,
		Reason:        "matched forbid_topic rule",
		TriggeredText: "competitor mention",
	}

	require.NoError(t, s.WriteAuditLog(ctx, entry))
	assert.NotZero(t, entry.ID, "WriteAuditLog must populate ID after insert")
}

// TestComplianceStore_WriteAuditLog_NullableFields verifies that audit log
// entries with nil AgentRunID, AgentDefinitionID, and RuleID are accepted
// (these fields are nullable per spec §1.3: no FK on rule_id).
func TestComplianceStore_WriteAuditLog_NullableFields(t *testing.T) {
	s := newTestComplianceStore(t)
	ctx := context.Background()

	entry := &model.ComplianceAuditLog{
		ParentUserID:      10,
		RuleLayer:         model.RuleLayerL0,
		Decision:          model.DecisionDeny,
		AgentRunID:        nil, // explicitly nil
		AgentDefinitionID: nil, // explicitly nil
		RuleID:            nil, // explicitly nil — intentional: no FK per spec §1.5
	}

	require.NoError(t, s.WriteAuditLog(ctx, entry))
	assert.NotZero(t, entry.ID)
}

// TestComplianceStore_Race_ListRulesByParent verifies that concurrent
// ListRulesByParent calls on the same parent account don't race.
// go test -race will catch any data races.
func TestComplianceStore_Race_ListRulesByParent(t *testing.T) {
	s := newTestComplianceStore(t)
	ctx := context.Background()

	// Pre-populate some rules
	for i := 0; i < 5; i++ {
		require.NoError(t, s.CreateRule(ctx, sampleComplianceRule(1)))
	}

	var wg sync.WaitGroup
	const goroutines = 10
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			rules, err := s.ListRulesByParent(ctx, 1, true)
			assert.NoError(t, err)
			assert.Len(t, rules, 5)
		}()
	}
	wg.Wait()
}

// TestComplianceStore_UpdateRule_Empty verifies that UpdateRule with an empty
// map is a no-op (returns nil without hitting the DB).
func TestComplianceStore_UpdateRule_Empty(t *testing.T) {
	s := newTestComplianceStore(t)
	ctx := context.Background()

	r := sampleComplianceRule(1)
	require.NoError(t, s.CreateRule(ctx, r))

	err := s.UpdateRule(ctx, r.ID, map[string]interface{}{})
	require.NoError(t, err, "UpdateRule with empty map must be a no-op")

	// Verify rule unchanged
	got, err := s.GetRule(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, r.RuleText, got.RuleText)
}
