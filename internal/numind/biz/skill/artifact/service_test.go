package artifact

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// validBody 返回一段合法的 body markdown，所有"非 body 边界"测试可复用。
func validBody() string {
	return "# Skill body\n\nyou are an agent..."
}

func TestService_Create_ChildAccountDenied(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db)

	_, err := svc.Create(context.Background(), 0, 0, CreateRequest{
		Name:   "x",
		BodyMd: validBody(),
	})
	require.ErrorIs(t, err, errno.ErrPermissionDenied)
}

func TestService_Create_BodyTooLarge(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db)

	huge := strings.Repeat("x", 200*1024+1) // 200KB + 1 byte
	_, err := svc.Create(context.Background(), 100, 100, CreateRequest{
		Name:   "huge",
		BodyMd: huge,
	})
	require.ErrorIs(t, err, errno.ErrSkillArtifactBodyTooLarge)
}

func TestService_Create_Success_WithHistoryV1(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db)

	sk, err := svc.Create(context.Background(), 100, 100, CreateRequest{
		Name:         "S1",
		Description:  "desc",
		WhenToUse:    "always",
		AllowedTools: []string{"web_search"},
		BodyMd:       validBody(),
	})
	require.NoError(t, err)
	require.NotZero(t, sk.ID)
	assert.Equal(t, "custom", sk.SourceType, "default source_type")
	assert.Equal(t, uint(1), sk.Version)
	assert.True(t, sk.IsActive)
	assert.Equal(t, uint(100), sk.ParentUserID)
	assert.Equal(t, uint(100), sk.CreatedBy)

	// history v1 应该有一行
	var histCount int64
	require.NoError(t, db.Model(&model.SkillHistory{}).Where("skill_id = ?", sk.ID).Count(&histCount).Error)
	assert.Equal(t, int64(1), histCount)
}

func TestService_Create_IsActiveTrueDefault(t *testing.T) {
	// 验证 Select("*") 兜底：is_active 显式为 true 时，DB 真实落盘是 true
	// （default:1 陷阱对 true 不会出错，但确保创建路径"想要 true"也能落 true）
	db := newTestDB(t)
	svc := NewService(db)

	sk, err := svc.Create(context.Background(), 200, 200, CreateRequest{
		Name:   "active",
		BodyMd: validBody(),
	})
	require.NoError(t, err)

	var row model.Skill
	require.NoError(t, db.First(&row, sk.ID).Error)
	assert.True(t, row.IsActive)
}

func TestService_Create_StoreCreateUsesSelectStar(t *testing.T) {
	// 验证 store.Create 调用链通过（不报错）：
	// database.md §6 的 default:1 bool 陷阱是 MySQL 行为，SQLite 不复现。
	// 真实 MySQL 验证靠代码静态检查（store.go 显式 `Select("*").Create(m)`）+ deploy 后 e2e。
	// 这里只验证 Create 路径能写入并读回 ID。
	db := newTestDB(t)
	store := NewStore(db)

	sk := &model.Skill{
		ParentUserID: 1,
		Name:         "active-create-path",
		BodyMd:       "x",
		Version:      1,
		IsActive:     true,
		CreatedBy:    1,
		SourceType:   "custom",
	}
	require.NoError(t, store.Create(context.Background(), sk))
	require.NotZero(t, sk.ID, "GORM should populate ID after insert")

	var row model.Skill
	require.NoError(t, db.First(&row, sk.ID).Error)
	assert.Equal(t, "active-create-path", row.Name)
}

func TestService_Get_CrossTenantReturnsNotFound(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db)
	ctx := context.Background()

	// tenant 100 creates a skill
	sk, err := svc.Create(ctx, 100, 100, CreateRequest{Name: "owned", BodyMd: validBody()})
	require.NoError(t, err)

	// tenant 200 attempts to read it → NotFound
	_, err = svc.Get(ctx, 200, sk.ID)
	require.ErrorIs(t, err, errno.ErrSkillArtifactNotFound)
}

func TestService_Get_NotExistsReturnsNotFound(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db)

	_, err := svc.Get(context.Background(), 100, 999999)
	require.ErrorIs(t, err, errno.ErrSkillArtifactNotFound)
}

func TestService_List_Pagination_AndSearch(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db)
	ctx := context.Background()

	// 创建 3 个 skill：sales-analyst, sales-coach, accountant
	for _, name := range []string{"sales-analyst", "sales-coach", "accountant"} {
		_, err := svc.Create(ctx, 100, 100, CreateRequest{Name: name, BodyMd: validBody()})
		require.NoError(t, err)
	}

	// 搜索 "sales" 应返回 2 条
	items, total, err := svc.List(ctx, 100, 1, 20, "sales")
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	assert.Len(t, items, 2)

	// 不带 search 返回全部
	items, total, err = svc.List(ctx, 100, 1, 20, "")
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	assert.Len(t, items, 3)

	// 分页 page=1 size=2 应有 2 条
	items, total, err = svc.List(ctx, 100, 1, 2, "")
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	assert.Len(t, items, 2)
}

func TestService_List_PageBoundaries(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db)
	ctx := context.Background()

	// page < 1 / pageSize < 1 / pageSize > 100 都应被归一化
	items, _, err := svc.List(ctx, 100, 0, 0, "")
	require.NoError(t, err)
	assert.Len(t, items, 0) // 没数据但不应 panic

	_, _, err = svc.List(ctx, 100, -1, 200, "")
	require.NoError(t, err)
}

func TestService_Update_BumpsVersion(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db)
	ctx := context.Background()

	sk, err := svc.Create(ctx, 100, 100, CreateRequest{Name: "v1", BodyMd: validBody()})
	require.NoError(t, err)
	require.Equal(t, uint(1), sk.Version)

	updated, err := svc.Update(ctx, 100, sk.ID, CreateRequest{Name: "v2", BodyMd: "new body"})
	require.NoError(t, err)
	assert.Equal(t, uint(2), updated.Version)
	assert.Equal(t, "v2", updated.Name)
	assert.Equal(t, "new body", updated.BodyMd)

	// history 应该有 2 行
	var histCount int64
	require.NoError(t, db.Model(&model.SkillHistory{}).Where("skill_id = ?", sk.ID).Count(&histCount).Error)
	assert.Equal(t, int64(2), histCount)
}

func TestService_Update_CrossTenantReturnsNotFound(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db)
	ctx := context.Background()

	sk, err := svc.Create(ctx, 100, 100, CreateRequest{Name: "x", BodyMd: validBody()})
	require.NoError(t, err)

	_, err = svc.Update(ctx, 200, sk.ID, CreateRequest{Name: "hijack", BodyMd: validBody()})
	require.ErrorIs(t, err, errno.ErrSkillArtifactNotFound)
}

func TestService_Update_BodyTooLarge(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db)
	ctx := context.Background()

	sk, err := svc.Create(ctx, 100, 100, CreateRequest{Name: "x", BodyMd: validBody()})
	require.NoError(t, err)

	huge := strings.Repeat("x", 200*1024+1)
	_, err = svc.Update(ctx, 100, sk.ID, CreateRequest{Name: "x", BodyMd: huge})
	require.ErrorIs(t, err, errno.ErrSkillArtifactBodyTooLarge)
}

func TestService_Delete_SoftDeletesAndCascadesBindings(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db)
	ctx := context.Background()

	sk, err := svc.Create(ctx, 100, 100, CreateRequest{Name: "to-delete", BodyMd: validBody()})
	require.NoError(t, err)

	// 手工插入 2 个活跃 binding + 1 个已 inactive 的 binding
	// 用 raw exec 绕过 SQLite default:1 bool 陷阱（GORM Select(*) 在 SQLite 上对 tinyint(1)
	// false 不可靠；MySQL 上靠 store.Create 自己保证）
	require.NoError(t, db.Exec(
		"INSERT INTO agent_skill_binding (agent_id, skill_id, is_active) VALUES (?, ?, ?)",
		1, sk.ID, 1).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO agent_skill_binding (agent_id, skill_id, is_active) VALUES (?, ?, ?)",
		2, sk.ID, 1).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO agent_skill_binding (agent_id, skill_id, is_active) VALUES (?, ?, ?)",
		3, sk.ID, 0).Error)

	affected, err := svc.Delete(ctx, 100, sk.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), affected, "only active bindings should be affected")

	// skill is_active=0
	var row model.Skill
	require.NoError(t, db.First(&row, sk.ID).Error)
	assert.False(t, row.IsActive)

	// 全部 binding 都应 is_active=0
	var activeCount int64
	require.NoError(t, db.Model(&model.AgentSkillBinding{}).
		Where("skill_id = ? AND is_active = ?", sk.ID, true).
		Count(&activeCount).Error)
	assert.Equal(t, int64(0), activeCount)
}

func TestService_Delete_CrossTenantNotFound(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db)
	ctx := context.Background()

	sk, err := svc.Create(ctx, 100, 100, CreateRequest{Name: "x", BodyMd: validBody()})
	require.NoError(t, err)

	_, err = svc.Delete(ctx, 200, sk.ID)
	require.ErrorIs(t, err, errno.ErrSkillArtifactNotFound)
}

func TestService_ListBoundAgents_ReturnsActiveAgentsOnly(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db)
	ctx := context.Background()

	sk, err := svc.Create(ctx, 100, 100, CreateRequest{Name: "x", BodyMd: validBody()})
	require.NoError(t, err)

	// 创建 3 个 agent，绑定 skill —— 用 raw exec 强制 IsActive 值（SQLite default:1 兜底）
	for i := 1; i <= 3; i++ {
		agentActive := 1
		if i == 3 {
			agentActive = 0
		}
		bindingActive := 1
		if i == 2 {
			bindingActive = 0
		}
		require.NoError(t, db.Exec(
			"INSERT INTO agent_definition (id, parent_user_id, name, is_active, created_by) VALUES (?, ?, ?, ?, ?)",
			i, 100, "agent", agentActive, 100).Error)
		require.NoError(t, db.Exec(
			"INSERT INTO agent_skill_binding (agent_id, skill_id, is_active) VALUES (?, ?, ?)",
			i, sk.ID, bindingActive).Error)
	}

	agents, err := svc.ListBoundAgents(ctx, 100, sk.ID)
	require.NoError(t, err)
	// 只有 agent 1 满足：agent.is_active=true AND binding.is_active=true
	assert.Len(t, agents, 1)
	if len(agents) > 0 {
		assert.Equal(t, uint64(1), agents[0].ID)
	}
}

func TestService_ListBoundAgents_CrossTenantReturnsNotFound(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db)
	ctx := context.Background()

	sk, err := svc.Create(ctx, 100, 100, CreateRequest{Name: "x", BodyMd: validBody()})
	require.NoError(t, err)

	_, err = svc.ListBoundAgents(ctx, 200, sk.ID)
	require.ErrorIs(t, err, errno.ErrSkillArtifactNotFound)
}

func TestService_AllMethods_RejectZeroParentUserID(t *testing.T) {
	// 子账户兜底：所有方法都必须先校验 parentUserID != 0
	db := newTestDB(t)
	svc := NewService(db)
	ctx := context.Background()

	_, err := svc.Create(ctx, 0, 0, CreateRequest{Name: "x", BodyMd: "x"})
	assert.True(t, errors.Is(err, errno.ErrPermissionDenied))

	_, err = svc.Get(ctx, 0, 1)
	assert.True(t, errors.Is(err, errno.ErrPermissionDenied))

	_, _, err = svc.List(ctx, 0, 1, 10, "")
	assert.True(t, errors.Is(err, errno.ErrPermissionDenied))

	_, err = svc.Update(ctx, 0, 1, CreateRequest{Name: "x", BodyMd: "x"})
	assert.True(t, errors.Is(err, errno.ErrPermissionDenied))

	_, err = svc.Delete(ctx, 0, 1)
	assert.True(t, errors.Is(err, errno.ErrPermissionDenied))

	_, err = svc.ListBoundAgents(ctx, 0, 1)
	assert.True(t, errors.Is(err, errno.ErrPermissionDenied))
}
