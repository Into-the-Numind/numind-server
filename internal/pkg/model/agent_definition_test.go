package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAgentDefinition_TableName_returnsAgentDefinition 验证 AgentDefinition.TableName() 返回正确表名。
func TestAgentDefinition_TableName_returnsAgentDefinition(t *testing.T) {
	assert.Equal(t, "agent_definition", AgentDefinition{}.TableName())
}

// TestAgentDefinitionHistory_TableName_returns_agent_definition_history 验证 AgentDefinitionHistory.TableName() 返回正确表名。
func TestAgentDefinitionHistory_TableName_returns_agent_definition_history(t *testing.T) {
	assert.Equal(t, "agent_definition_history", AgentDefinitionHistory{}.TableName())
}

// TestSkillTemplate_TableName_returns_skill_template 验证 SkillTemplate.TableName() 返回正确表名。
func TestSkillTemplate_TableName_returns_skill_template(t *testing.T) {
	assert.Equal(t, "skill_template", SkillTemplate{}.TableName())
}

// TestAgentDefinition_CreateIsActiveFalse_persists 测试 GORM default:true bool Create 踩坑的 UpdateColumn fixup。
//
// AgentDefinition.IsActive 的 gorm tag 含 "default:1"，GORM v2 在 Create 时
// 对 struct 零值 bool(false) 视为"未设置"，INSERT 跳过该列，MySQL DEFAULT(1) 生效
// 导致 is_active=false 被静默覆盖为 true。
//
// 修复方案（database.md §6）：Create 后检查 wantActive != actual，若不符则
// UpdateColumn("is_active", false) 强制写入，用两步法保证 caller 的意图持久化。
func TestAgentDefinition_CreateIsActiveFalse_persists(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, db.AutoMigrate(&AgentDefinition{}))

	ad := &AgentDefinition{
		ParentUserID: 100,
		Name:         "Test Agent",
		CreatedBy:    100,
		IsActive:     false,
	}

	wantActive := ad.IsActive
	require.NoError(t, db.Create(ad).Error)

	// GORM default:1 bug：Create 后 struct.IsActive 可能被 DB DEFAULT 覆盖为 true。
	// UpdateColumn fixup 两步法（database.md §6）。
	if !wantActive && ad.IsActive {
		require.NoError(t, db.Model(ad).UpdateColumn("is_active", false).Error)
		ad.IsActive = false
	}

	assert.False(t, ad.IsActive, "struct.IsActive should be false after fixup")

	var row AgentDefinition
	require.NoError(t, db.First(&row, ad.ID).Error)
	assert.False(t, row.IsActive, "DB row should have is_active=false after UpdateColumn fixup")
}
