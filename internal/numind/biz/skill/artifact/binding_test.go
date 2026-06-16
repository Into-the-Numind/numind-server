package artifact

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// setupBindingFixture 创建：
//   - parent_user_id=100 下的 agent_id=1 和 agent_id=2
//   - parent_user_id=200 下的 agent_id=3
//   - parent_user_id=100 下的 skill_id（返回 ID 给测试用）
func setupBindingFixture(t *testing.T, db *gorm.DB) (skillID uint) {
	t.Helper()
	for _, id := range []int{1, 2} {
		require.NoError(t, db.Exec(
			"INSERT INTO agent_definition (id, parent_user_id, name, is_active, created_by) VALUES (?, ?, ?, ?, ?)",
			id, 100, "agent-100", 1, 100).Error)
	}
	require.NoError(t, db.Exec(
		"INSERT INTO agent_definition (id, parent_user_id, name, is_active, created_by) VALUES (?, ?, ?, ?, ?)",
		3, 200, "agent-200", 1, 200).Error)

	// 创建 skill 直接走 svc
	svc := NewService(db)
	sk, err := svc.Create(context.Background(), 100, 100, CreateRequest{Name: "sk", BodyMd: "x"})
	require.NoError(t, err)
	return sk.ID
}

func TestBinding_Attach_ChildAccountDenied(t *testing.T) {
	db := newTestDB(t)
	bsvc := NewBindingService(db)

	err := bsvc.Attach(context.Background(), 0, 1, 1, 0)
	require.ErrorIs(t, err, errno.ErrPermissionDenied)
}

func TestBinding_Attach_CrossTenantAgentRejected(t *testing.T) {
	db := newTestDB(t)
	bsvc := NewBindingService(db)
	skillID := setupBindingFixture(t, db)

	// agent 3 属于 parent=200，但 parent=100 尝试 attach
	err := bsvc.Attach(context.Background(), 100, 3, skillID, 0)
	require.ErrorIs(t, err, errno.ErrSkillArtifactNotFound)
}

// TestBinding_Attach_DuplicateNameRejected reproduces the agent-bricking bug: the
// skill table has no (parent_user_id, name) UNIQUE, so a tenant can hold two skills
// named the same. The runtime resolves an agent's bound skills BY NAME and returns a
// hard error if two active bindings collide — bricking every run before any LLM call.
// Attach must reject binding a second skill whose name duplicates an already-bound one.
func TestBinding_Attach_DuplicateNameRejected(t *testing.T) {
	db := newTestDB(t)
	bsvc := NewBindingService(db)
	svc := NewService(db)

	require.NoError(t, db.Exec(
		"INSERT INTO agent_definition (id, parent_user_id, name, is_active, created_by) VALUES (?, ?, ?, ?, ?)",
		1, 100, "agent-100", 1, 100).Error)

	// Two same-named skills in the same tenant (allowed — no UNIQUE constraint).
	s1, err := svc.Create(context.Background(), 100, 100, CreateRequest{Name: "dup", BodyMd: "a"})
	require.NoError(t, err)
	s2, err := svc.Create(context.Background(), 100, 100, CreateRequest{Name: "dup", BodyMd: "b"})
	require.NoError(t, err)

	require.NoError(t, bsvc.Attach(context.Background(), 100, 1, s1.ID, 0))

	err = bsvc.Attach(context.Background(), 100, 1, s2.ID, 1)
	require.ErrorIs(t, err, errno.ErrSkillArtifactNameConflict)
}

func TestBinding_Attach_NewBindingCreated(t *testing.T) {
	db := newTestDB(t)
	bsvc := NewBindingService(db)
	skillID := setupBindingFixture(t, db)

	require.NoError(t, bsvc.Attach(context.Background(), 100, 1, skillID, 5))

	var b model.AgentSkillBinding
	require.NoError(t, db.Where("agent_id = ? AND skill_id = ?", 1, skillID).First(&b).Error)
	assert.True(t, b.IsActive)
	assert.Equal(t, int16(5), b.SortOrder)
	assert.Nil(t, b.UnboundAt)
}

func TestBinding_Attach_RepeatActiveReturnsConflict(t *testing.T) {
	db := newTestDB(t)
	bsvc := NewBindingService(db)
	skillID := setupBindingFixture(t, db)
	ctx := context.Background()

	require.NoError(t, bsvc.Attach(ctx, 100, 1, skillID, 0))
	err := bsvc.Attach(ctx, 100, 1, skillID, 1)
	require.ErrorIs(t, err, errno.ErrSkillArtifactBindingExists)
}

func TestBinding_Attach_RebindAfterDetach(t *testing.T) {
	db := newTestDB(t)
	bsvc := NewBindingService(db)
	skillID := setupBindingFixture(t, db)
	ctx := context.Background()

	// Attach → Detach → 复装
	require.NoError(t, bsvc.Attach(ctx, 100, 1, skillID, 0))
	require.NoError(t, bsvc.Detach(ctx, 100, 1, skillID))

	// 复装应该改 is_active=1 + 更新 sort_order + 清 unbound_at
	require.NoError(t, bsvc.Attach(ctx, 100, 1, skillID, 99))

	var rows []model.AgentSkillBinding
	require.NoError(t, db.Where("agent_id = ? AND skill_id = ?", 1, skillID).Find(&rows).Error)
	require.Len(t, rows, 1, "uk_agent_skill should keep single row")
	assert.True(t, rows[0].IsActive)
	assert.Equal(t, int16(99), rows[0].SortOrder)
	assert.Nil(t, rows[0].UnboundAt)
}

func TestBinding_Detach_NotExistsReturnsNotFound(t *testing.T) {
	db := newTestDB(t)
	bsvc := NewBindingService(db)
	skillID := setupBindingFixture(t, db)

	err := bsvc.Detach(context.Background(), 100, 1, skillID)
	require.ErrorIs(t, err, errno.ErrSkillArtifactNotFound)
}

func TestBinding_Detach_ChildAccountDenied(t *testing.T) {
	db := newTestDB(t)
	bsvc := NewBindingService(db)

	err := bsvc.Detach(context.Background(), 0, 1, 1)
	require.ErrorIs(t, err, errno.ErrPermissionDenied)
}

func TestBinding_Detach_CrossTenantAgentRejected(t *testing.T) {
	db := newTestDB(t)
	bsvc := NewBindingService(db)
	setupBindingFixture(t, db)

	// agent 3 不属于 parent=100
	err := bsvc.Detach(context.Background(), 100, 3, 999)
	require.ErrorIs(t, err, errno.ErrSkillArtifactNotFound)
}

func TestBinding_Reorder_UpdatesSortOrderTransactionally(t *testing.T) {
	db := newTestDB(t)
	bsvc := NewBindingService(db)
	ctx := context.Background()
	skillA := setupBindingFixture(t, db) // 1 个 agent + 1 个 skill

	// 再创建 2 个 skill 给 agent 1 装载
	svc := NewService(db)
	skB, err := svc.Create(ctx, 100, 100, CreateRequest{Name: "skB", BodyMd: "x"})
	require.NoError(t, err)
	skC, err := svc.Create(ctx, 100, 100, CreateRequest{Name: "skC", BodyMd: "x"})
	require.NoError(t, err)

	// 装载 A, B, C 给 agent 1（顺序 0,1,2）
	require.NoError(t, bsvc.Attach(ctx, 100, 1, skillA, 0))
	require.NoError(t, bsvc.Attach(ctx, 100, 1, skB.ID, 1))
	require.NoError(t, bsvc.Attach(ctx, 100, 1, skC.ID, 2))

	// 重排 → [C, A, B]
	require.NoError(t, bsvc.Reorder(ctx, 100, 1, []uint{skC.ID, skillA, skB.ID}))

	// 验证 sort_order
	var rows []model.AgentSkillBinding
	require.NoError(t, db.Where("agent_id = ?", 1).Order("sort_order ASC").Find(&rows).Error)
	require.Len(t, rows, 3)
	assert.Equal(t, skC.ID, rows[0].SkillID)
	assert.Equal(t, int16(0), rows[0].SortOrder)
	assert.Equal(t, skillA, rows[1].SkillID)
	assert.Equal(t, int16(1), rows[1].SortOrder)
	assert.Equal(t, skB.ID, rows[2].SkillID)
	assert.Equal(t, int16(2), rows[2].SortOrder)
}

func TestBinding_Reorder_ChildAccountDenied(t *testing.T) {
	db := newTestDB(t)
	bsvc := NewBindingService(db)

	err := bsvc.Reorder(context.Background(), 0, 1, []uint{1, 2})
	require.ErrorIs(t, err, errno.ErrPermissionDenied)
}

func TestBinding_Reorder_CrossTenantAgentRejected(t *testing.T) {
	db := newTestDB(t)
	bsvc := NewBindingService(db)
	setupBindingFixture(t, db)

	err := bsvc.Reorder(context.Background(), 100, 3, []uint{1, 2})
	require.ErrorIs(t, err, errno.ErrSkillArtifactNotFound)
}

func TestBinding_ListByAgent_ReturnsSortedActiveSkills(t *testing.T) {
	db := newTestDB(t)
	bsvc := NewBindingService(db)
	ctx := context.Background()
	skA := setupBindingFixture(t, db)

	svc := NewService(db)
	skB, err := svc.Create(ctx, 100, 100, CreateRequest{Name: "skB", BodyMd: "x"})
	require.NoError(t, err)

	// 装载 skA (sort=2), skB (sort=0)
	require.NoError(t, bsvc.Attach(ctx, 100, 1, skA, 2))
	require.NoError(t, bsvc.Attach(ctx, 100, 1, skB.ID, 0))

	skills, err := bsvc.ListByAgent(ctx, 100, 1)
	require.NoError(t, err)
	require.Len(t, skills, 2)
	// 按 sort_order ASC，skB(0) 在前
	assert.Equal(t, skB.ID, skills[0].ID)
	assert.Equal(t, skA, skills[1].ID)
}

func TestBinding_ListByAgent_DetachedNotInResults(t *testing.T) {
	db := newTestDB(t)
	bsvc := NewBindingService(db)
	ctx := context.Background()
	skA := setupBindingFixture(t, db)

	require.NoError(t, bsvc.Attach(ctx, 100, 1, skA, 0))
	require.NoError(t, bsvc.Detach(ctx, 100, 1, skA))

	skills, err := bsvc.ListByAgent(ctx, 100, 1)
	require.NoError(t, err)
	assert.Len(t, skills, 0)
}

func TestBinding_ListByAgent_ChildAccountDenied(t *testing.T) {
	db := newTestDB(t)
	bsvc := NewBindingService(db)

	_, err := bsvc.ListByAgent(context.Background(), 0, 1)
	require.ErrorIs(t, err, errno.ErrPermissionDenied)
}

func TestBinding_AllMethods_RejectZeroParentUserID(t *testing.T) {
	db := newTestDB(t)
	bsvc := NewBindingService(db)
	ctx := context.Background()

	assert.ErrorIs(t, bsvc.Attach(ctx, 0, 1, 1, 0), errno.ErrPermissionDenied)
	assert.ErrorIs(t, bsvc.Detach(ctx, 0, 1, 1), errno.ErrPermissionDenied)
	assert.ErrorIs(t, bsvc.Reorder(ctx, 0, 1, nil), errno.ErrPermissionDenied)
	_, err := bsvc.ListByAgent(ctx, 0, 1)
	assert.ErrorIs(t, err, errno.ErrPermissionDenied)
}
