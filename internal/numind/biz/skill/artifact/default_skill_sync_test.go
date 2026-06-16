package artifact

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/model"
)

// TestDefaultSkillSyncer_SyncsBoundDefaultSkill verifies the write-through: a v1
// questionnaire edit's rebuilt body propagates to the agent's bound "默认技能" so the
// runtime no longer loads a stale body (the v1↔v2 divergence the audit found).
func TestDefaultSkillSyncer_SyncsBoundDefaultSkill(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, db.Exec(
		"INSERT INTO agent_definition (id, parent_user_id, name, is_active, created_by) VALUES (?, ?, ?, ?, ?)",
		1, 100, "我的助手", 1, 100).Error)

	svc := NewService(db)
	bind := NewBindingService(db)
	sk, err := svc.Create(context.Background(), 100, 100, CreateRequest{
		Name: "我的助手 的默认技能", BodyMd: "旧内容", SourceType: "generated",
	})
	require.NoError(t, err)
	require.NoError(t, bind.Attach(context.Background(), 100, 1, sk.ID, 0))
	oldVersion := sk.Version

	syncer := NewDefaultSkillSyncer(db)
	require.NoError(t, syncer.SyncAgentDefaultSkill(context.Background(), 100, 1, "新内容"))

	var got model.Skill
	require.NoError(t, db.First(&got, sk.ID).Error)
	assert.Equal(t, "新内容", got.BodyMd, "default skill body must be synced")
	assert.Greater(t, got.Version, oldVersion, "version must bump on a real change")

	// Idempotent: a second sync with the same body is a no-op (no version churn).
	require.NoError(t, syncer.SyncAgentDefaultSkill(context.Background(), 100, 1, "新内容"))
	var got2 model.Skill
	require.NoError(t, db.First(&got2, sk.ID).Error)
	assert.Equal(t, got.Version, got2.Version, "no version bump when already in sync")
}

// TestDefaultSkillSyncer_NoDefaultSkill_NoOp verifies a non-default-named bound skill
// is never touched (and the common un-migrated case is a clean no-op).
func TestDefaultSkillSyncer_NoDefaultSkill_NoOp(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, db.Exec(
		"INSERT INTO agent_definition (id, parent_user_id, name, is_active, created_by) VALUES (?, ?, ?, ?, ?)",
		2, 100, "助手", 1, 100).Error)

	svc := NewService(db)
	bind := NewBindingService(db)
	sk, err := svc.Create(context.Background(), 100, 100, CreateRequest{
		Name: "普通技能", BodyMd: "x", SourceType: "generated",
	})
	require.NoError(t, err)
	require.NoError(t, bind.Attach(context.Background(), 100, 2, sk.ID, 0))

	syncer := NewDefaultSkillSyncer(db)
	require.NoError(t, syncer.SyncAgentDefaultSkill(context.Background(), 100, 2, "新内容"))

	var got model.Skill
	require.NoError(t, db.First(&got, sk.ID).Error)
	assert.Equal(t, "x", got.BodyMd, "a non-default-named skill must not be touched")
}
