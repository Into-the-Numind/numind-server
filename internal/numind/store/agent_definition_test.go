package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/pkg/model"
)

// newTestDB 创建 SQLite 内存 DB 并 AutoMigrate agent_definition / history 模型。
// 使用文件 DB（非 :memory:）保证并发 goroutine 共享同一连接。
func newTestDB(t *testing.T, models ...interface{}) *gorm.DB {
	t.Helper()
	tmp := t.TempDir()
	dsn := tmp + "/test.db?_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(models...))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func newTestAgentDefinitionStore(t *testing.T) IAgentDefinitionStore {
	t.Helper()
	db := newTestDB(t, &model.AgentDefinition{}, &model.AgentDefinitionHistory{})
	return newAgentDefinitionStore(db)
}

// sampleDef 构建一个父 ID=1 的激活 agent 定义。
func sampleDef(parentID uint) *model.AgentDefinition {
	return &model.AgentDefinition{
		ParentUserID: parentID,
		Name:         "Test Agent",
		Description:  "desc",
		IsActive:     true,
		Version:      1,
		CreatedBy:    parentID,
	}
}

// TestStore_Create_persists 验证 Create 后 ID 非零、能 GetByID 取到。
func TestStore_Create_persists(t *testing.T) {
	s := newTestAgentDefinitionStore(t)
	ctx := context.Background()

	def := sampleDef(1)
	require.NoError(t, s.Create(ctx, def))
	require.NotZero(t, def.ID)

	got, err := s.GetByID(ctx, def.ID)
	require.NoError(t, err)
	assert.Equal(t, "Test Agent", got.Name)
	assert.True(t, got.IsActive)
}

// TestStore_Create_isActiveFalseFixup 验证显式 is_active=false 能持久化（database.md §6 fixup）。
func TestStore_Create_isActiveFalseFixup(t *testing.T) {
	s := newTestAgentDefinitionStore(t)
	ctx := context.Background()

	def := sampleDef(1)
	def.IsActive = false
	require.NoError(t, s.Create(ctx, def))
	require.NotZero(t, def.ID)
	assert.False(t, def.IsActive, "struct field must be updated by fixup")

	// GetByID 过滤 is_active=1，应返回 NotFound
	_, err := s.GetByID(ctx, def.ID)
	require.Error(t, err, "is_active=false agent must not be found by GetByID")

	// GetByIDIncludeInactive 应返回，且 is_active=false
	got, err := s.GetByIDIncludeInactive(ctx, def.ID)
	require.NoError(t, err)
	assert.False(t, got.IsActive)
}

// TestStore_Update_SaveHandlesZeroBool 验证 Update 对 is_active=false 的处理（database.md §6b：Save 安全）。
func TestStore_Update_SaveHandlesZeroBool(t *testing.T) {
	s := newTestAgentDefinitionStore(t)
	ctx := context.Background()

	def := sampleDef(1)
	require.NoError(t, s.Create(ctx, def))

	// 改名 + 将 is_active 置为 false
	def.Name = "Updated Name"
	def.IsActive = false
	require.NoError(t, s.Update(ctx, def))

	got, err := s.GetByIDIncludeInactive(ctx, def.ID)
	require.NoError(t, err)
	assert.Equal(t, "Updated Name", got.Name)
	assert.False(t, got.IsActive, "Save must persist is_active=false")
}

// TestStore_GetByID_softDeletedExcluded 验证软删除后 GetByID 返回错误。
func TestStore_GetByID_softDeletedExcluded(t *testing.T) {
	s := newTestAgentDefinitionStore(t)
	ctx := context.Background()

	def := sampleDef(1)
	require.NoError(t, s.Create(ctx, def))
	require.NoError(t, s.SoftDelete(ctx, def.ID))

	_, err := s.GetByID(ctx, def.ID)
	require.Error(t, err, "soft-deleted agent must be excluded from GetByID")
}

// TestStore_GetByIDIncludeInactive_returnsInactive 验证 GetByIDIncludeInactive 返回软删除项。
func TestStore_GetByIDIncludeInactive_returnsInactive(t *testing.T) {
	s := newTestAgentDefinitionStore(t)
	ctx := context.Background()

	def := sampleDef(1)
	require.NoError(t, s.Create(ctx, def))
	require.NoError(t, s.SoftDelete(ctx, def.ID))

	got, err := s.GetByIDIncludeInactive(ctx, def.ID)
	require.NoError(t, err)
	assert.False(t, got.IsActive)
}

// TestStore_ListByParent_filtersOnParentUserID 验证 ListByParent 只返回指定父账户的 agent。
func TestStore_ListByParent_filtersOnParentUserID(t *testing.T) {
	s := newTestAgentDefinitionStore(t)
	ctx := context.Background()

	// 父账户 1：3 个
	for i := 0; i < 3; i++ {
		require.NoError(t, s.Create(ctx, sampleDef(1)))
	}
	// 父账户 2：2 个
	for i := 0; i < 2; i++ {
		require.NoError(t, s.Create(ctx, sampleDef(2)))
	}

	items, total, err := s.ListByParent(ctx, 1, false, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	assert.Len(t, items, 3)
	for _, item := range items {
		assert.Equal(t, uint(1), item.ParentUserID)
	}
}

// TestStore_ListByParent_includeInactiveFlag 验证 includeInactive=false 过滤软删除项，=true 包含。
func TestStore_ListByParent_includeInactiveFlag(t *testing.T) {
	s := newTestAgentDefinitionStore(t)
	ctx := context.Background()

	d1 := sampleDef(1)
	d2 := sampleDef(1)
	require.NoError(t, s.Create(ctx, d1))
	require.NoError(t, s.Create(ctx, d2))
	require.NoError(t, s.SoftDelete(ctx, d1.ID))

	// includeInactive=false：只返回激活的 1 条
	items, total, err := s.ListByParent(ctx, 1, false, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Len(t, items, 1)

	// includeInactive=true：返回全部 2 条
	items, total, err = s.ListByParent(ctx, 1, true, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	assert.Len(t, items, 2)
}

// TestStore_SoftDelete_marksInactive 验证 SoftDelete 将 is_active 置为 0。
func TestStore_SoftDelete_marksInactive(t *testing.T) {
	s := newTestAgentDefinitionStore(t)
	ctx := context.Background()

	def := sampleDef(1)
	require.NoError(t, s.Create(ctx, def))
	require.NoError(t, s.SoftDelete(ctx, def.ID))

	got, err := s.GetByIDIncludeInactive(ctx, def.ID)
	require.NoError(t, err)
	assert.False(t, got.IsActive)
}

// TestStore_WriteHistory_unique 验证相同 (agent_id, version) 重复插入会报错（UNIQUE 约束）。
func TestStore_WriteHistory_unique(t *testing.T) {
	s := newTestAgentDefinitionStore(t)
	ctx := context.Background()

	def := sampleDef(1)
	require.NoError(t, s.Create(ctx, def))

	h := &model.AgentDefinitionHistory{
		AgentID:   def.ID,
		Version:   1,
		Snapshot:  []byte(`{"name":"v1"}`),
		CreatedBy: 1,
	}
	require.NoError(t, s.WriteHistory(ctx, h))

	// 重复 version=1 应报错（UNIQUE 约束）
	h2 := &model.AgentDefinitionHistory{
		AgentID:   def.ID,
		Version:   1,
		Snapshot:  []byte(`{"name":"v1-dup"}`),
		CreatedBy: 1,
	}
	err := s.WriteHistory(ctx, h2)
	require.Error(t, err, "duplicate (agent_id, version) must fail")
}

// TestStore_ListHistory_includesSoftDeleted 验证 ListHistory 不依赖 agent is_active，含已软删除的 agent 版本。
func TestStore_ListHistory_includesSoftDeleted(t *testing.T) {
	s := newTestAgentDefinitionStore(t)
	ctx := context.Background()

	def := sampleDef(1)
	require.NoError(t, s.Create(ctx, def))

	for i := uint(1); i <= 3; i++ {
		require.NoError(t, s.WriteHistory(ctx, &model.AgentDefinitionHistory{
			AgentID:   def.ID,
			Version:   i,
			Snapshot:  []byte(`{}`),
			CreatedBy: 1,
		}))
	}

	// 软删除 agent
	require.NoError(t, s.SoftDelete(ctx, def.ID))

	histories, err := s.ListHistory(ctx, def.ID)
	require.NoError(t, err)
	assert.Len(t, histories, 3, "ListHistory must return all versions even after soft-delete")
	// 按 version DESC 排序
	assert.Equal(t, uint(3), histories[0].Version)
	assert.Equal(t, uint(2), histories[1].Version)
	assert.Equal(t, uint(1), histories[2].Version)
}

// TestStore_MaxVersion_returnsLatest 验证 MaxVersion 返回最大版本号。
func TestStore_MaxVersion_returnsLatest(t *testing.T) {
	s := newTestAgentDefinitionStore(t)
	ctx := context.Background()

	def := sampleDef(1)
	require.NoError(t, s.Create(ctx, def))

	// 无历史记录时应返回 0
	v, err := s.MaxVersion(ctx, def.ID)
	require.NoError(t, err)
	assert.Equal(t, uint(0), v)

	for _, ver := range []uint{1, 2, 5} {
		require.NoError(t, s.WriteHistory(ctx, &model.AgentDefinitionHistory{
			AgentID:   def.ID,
			Version:   ver,
			Snapshot:  []byte(`{}`),
			CreatedBy: 1,
		}))
	}

	v, err = s.MaxVersion(ctx, def.ID)
	require.NoError(t, err)
	assert.Equal(t, uint(5), v)
}

// TestStore_GetHistoryByVersion_returnsSnapshot 验证按 (agent_id, version) 获取正确的快照。
func TestStore_GetHistoryByVersion_returnsSnapshot(t *testing.T) {
	s := newTestAgentDefinitionStore(t)
	ctx := context.Background()

	def := sampleDef(1)
	require.NoError(t, s.Create(ctx, def))

	require.NoError(t, s.WriteHistory(ctx, &model.AgentDefinitionHistory{
		AgentID:        def.ID,
		Version:        2,
		Snapshot:       []byte(`{"name":"v2"}`),
		ChangesSummary: "second revision",
		CreatedBy:      1,
	}))

	h, err := s.GetHistoryByVersion(ctx, def.ID, 2)
	require.NoError(t, err)
	assert.Equal(t, uint(2), h.Version)
	assert.Equal(t, "second revision", h.ChangesSummary)

	// 不存在的版本应返回错误
	_, err = s.GetHistoryByVersion(ctx, def.ID, 99)
	require.Error(t, err)
}

// TestStore_CreateTx_inTransaction_succeeds 验证 CreateTx 在 db.Transaction 包裹中正常工作。
func TestStore_CreateTx_inTransaction_succeeds(t *testing.T) {
	db := newTestDB(t, &model.AgentDefinition{}, &model.AgentDefinitionHistory{})
	s := newAgentDefinitionStore(db)
	ctx := context.Background()

	var createdID uint64
	err := db.Transaction(func(tx *gorm.DB) error {
		def := sampleDef(10)
		def.IsActive = false // 测试 fixup 路径
		if err := s.CreateTx(ctx, tx, def); err != nil {
			return err
		}
		createdID = def.ID
		return nil
	})
	require.NoError(t, err)
	require.NotZero(t, createdID)

	// 事务提交后 GetByIDIncludeInactive 应能取到，且 is_active=false
	got, err := s.GetByIDIncludeInactive(ctx, createdID)
	require.NoError(t, err)
	assert.False(t, got.IsActive, "CreateTx fixup must work inside transaction")
}
