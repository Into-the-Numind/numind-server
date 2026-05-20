package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/model"
)

// newTestAgentPermissionStore returns a fresh in-memory SQLite-backed store.
func newTestAgentPermissionStore(t *testing.T) IAgentPermissionStore {
	t.Helper()
	db := newTestDB(t, &model.AgentPermissionConfig{}, &model.AgentPermissionDecisionLog{})
	return newAgentPermissionStore(db)
}

// sampleRule constructs an active deny rule by parent.
func sampleRule(parentID uint, ruleType, ruleKey, ruleValue string) *model.AgentPermissionConfig {
	return &model.AgentPermissionConfig{
		ParentUserID: parentID,
		RuleType:     ruleType,
		RuleKey:      ruleKey,
		RuleValue:    ruleValue,
		Action:       "deny",
		Message:      "禁止操作",
		IsActive:     true,
	}
}

func TestStore_AgentPermission_ListActiveByParent_Empty(t *testing.T) {
	s := newTestAgentPermissionStore(t)
	ctx := context.Background()

	rules, err := s.ListActiveByParent(ctx, 1)
	require.NoError(t, err)
	assert.Empty(t, rules)
}

func TestStore_AgentPermission_ListActiveByParent_HasRules(t *testing.T) {
	s := newTestAgentPermissionStore(t)
	ctx := context.Background()

	r1 := sampleRule(1, "tool_blacklist", "web_search", "")
	require.NoError(t, s.CreateRule(ctx, r1))
	r2 := sampleRule(1, "topic_blacklist", "X 平台", "")
	require.NoError(t, s.CreateRule(ctx, r2))

	rules, err := s.ListActiveByParent(ctx, 1)
	require.NoError(t, err)
	assert.Len(t, rules, 2)
	// 按 id 升序
	assert.Equal(t, "tool_blacklist", rules[0].RuleType)
	assert.Equal(t, "topic_blacklist", rules[1].RuleType)
}

func TestStore_AgentPermission_ListActiveByParent_CrossParentIsolation(t *testing.T) {
	s := newTestAgentPermissionStore(t)
	ctx := context.Background()

	require.NoError(t, s.CreateRule(ctx, sampleRule(1, "tool_blacklist", "x", "")))
	require.NoError(t, s.CreateRule(ctx, sampleRule(2, "tool_blacklist", "y", "")))

	rules1, err := s.ListActiveByParent(ctx, 1)
	require.NoError(t, err)
	assert.Len(t, rules1, 1)
	assert.Equal(t, uint(1), rules1[0].ParentUserID)

	rules2, err := s.ListActiveByParent(ctx, 2)
	require.NoError(t, err)
	assert.Len(t, rules2, 1)
	assert.Equal(t, uint(2), rules2[0].ParentUserID)
}

func TestStore_AgentPermission_ListActiveByParent_FiltersInactive(t *testing.T) {
	s := newTestAgentPermissionStore(t)
	ctx := context.Background()

	active := sampleRule(1, "tool_blacklist", "active", "")
	require.NoError(t, s.CreateRule(ctx, active))

	inactive := sampleRule(1, "tool_blacklist", "inactive", "")
	inactive.IsActive = false
	require.NoError(t, s.CreateRule(ctx, inactive))

	rules, err := s.ListActiveByParent(ctx, 1)
	require.NoError(t, err)
	assert.Len(t, rules, 1)
	assert.Equal(t, "active", rules[0].RuleKey)
	assert.True(t, rules[0].IsActive)
}

// TestStore_AgentPermission_CreateRule_IsActiveFalseFixup 验证 default:true bool 坑修复
// (database.md §6) — Create 时 wantActive=false 必须正确持久化。
func TestStore_AgentPermission_CreateRule_IsActiveFalseFixup(t *testing.T) {
	s := newTestAgentPermissionStore(t)
	ctx := context.Background()

	rule := sampleRule(1, "tool_blacklist", "test", "")
	rule.IsActive = false

	require.NoError(t, s.CreateRule(ctx, rule))
	require.NotZero(t, rule.ID)
	assert.False(t, rule.IsActive, "struct.IsActive must be false after Create fixup")

	// 再次从 DB 取，确认持久化层也是 false
	var fromDB model.AgentPermissionConfig
	require.NoError(t, s.(*agentPermissionStore).db.WithContext(ctx).First(&fromDB, rule.ID).Error)
	assert.False(t, fromDB.IsActive, "DB row.is_active must be false (not GORM default true)")
}

// TestStore_AgentPermission_CreateRule_IsActiveTrue 验证默认 true 路径不动旧行为。
func TestStore_AgentPermission_CreateRule_IsActiveTrue(t *testing.T) {
	s := newTestAgentPermissionStore(t)
	ctx := context.Background()

	rule := sampleRule(1, "tool_blacklist", "test", "")
	// IsActive=true (sampleRule default)
	require.NoError(t, s.CreateRule(ctx, rule))
	assert.True(t, rule.IsActive)

	var fromDB model.AgentPermissionConfig
	require.NoError(t, s.(*agentPermissionStore).db.WithContext(ctx).First(&fromDB, rule.ID).Error)
	assert.True(t, fromDB.IsActive)
}

func TestStore_AgentPermission_CreateDecisionLog(t *testing.T) {
	s := newTestAgentPermissionStore(t)
	ctx := context.Background()

	row := &model.AgentPermissionDecisionLog{
		AgentRunID:        100,
		UserID:            5,
		ParentUserID:      1,
		AgentDefinitionID: 42,
		ToolName:          "bash_exec",
		ToolInputDigest:   "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		Behavior:          "deny",
		DecisionReason:    "rule",
		ValidatorID:       "PlatformHardRule:ControlChar",
		Message:           "命令含控制字符",
		LatencyMs:         3,
	}
	require.NoError(t, s.CreateDecisionLog(ctx, row))
	require.NotZero(t, row.ID)
	require.False(t, row.CreatedAt.IsZero(), "autoCreateTime should fill created_at")
}

func TestStore_AgentPermission_CreateDecisionLog_MultipleEntries(t *testing.T) {
	s := newTestAgentPermissionStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		row := &model.AgentPermissionDecisionLog{
			AgentRunID:        uint64(100 + i),
			UserID:            5,
			ParentUserID:      1,
			AgentDefinitionID: 42,
			ToolName:          "web_search",
			ToolInputDigest:   "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
			Behavior:          "allow",
			DecisionReason:    "other",
			ValidatorID:       "DefaultAllow",
			LatencyMs:         1,
		}
		require.NoError(t, s.CreateDecisionLog(ctx, row))
	}
	// 不直接断言 INDEX 工作（SQLite 不执行 EXPLAIN）；只验证多条插入不冲突。
}
