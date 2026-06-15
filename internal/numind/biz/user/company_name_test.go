package user

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
	v1 "numind-server/pkg/api/numind/v1"
)

// newCompanyNameTestBiz 在 SQLite 内存 DB 上构建真实 store + user biz。
// 用真实 store（而非 mock）是刻意的：org-branding 的"清空品牌名"路径依赖 GORM
// 落库行为（map 形式 vs struct 形式对零值的处理，见 database.md §6b），mock 测不出。
func newCompanyNameTestBiz(t *testing.T) (*userBiz, *gorm.DB) {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&model.User{}))
	return New(store.NewTestStore(db)), db
}

func strptr(s string) *string { return &s }

// createParent 创建一个父账户（ParentUserID==nil）。
func createParent(t *testing.T, db *gorm.DB, username, companyName string) *model.User {
	t.Helper()
	u := &model.User{Username: username, CompanyName: companyName}
	require.NoError(t, db.Create(u).Error)
	return u
}

// createChild 创建一个子账户（ParentUserID 指向 parentID）。
func createChild(t *testing.T, db *gorm.DB, username string, parentID uint) *model.User {
	t.Helper()
	u := &model.User{Username: username, ParentUserID: &parentID}
	require.NoError(t, db.Create(u).Error)
	return u
}

// AC1：父账户设公司名 → 持久化（含 trim）。
func TestUpdateUserProfile_ParentSetsCompanyName(t *testing.T) {
	b, db := newCompanyNameTestBiz(t)
	p := createParent(t, db, "parent1", "")

	err := b.UpdateUserProfile(context.Background(), p.ID, &v1.UpdateUserProfileRequest{
		CompanyName: strptr("  测试公司A  "),
	})
	require.NoError(t, err)

	var got model.User
	require.NoError(t, db.First(&got, p.ID).Error)
	assert.Equal(t, "测试公司A", got.CompanyName, "父账户设名应落库并 trim")
}

// AC6：子账户传 company_name → 被静默忽略（继承语义守卫）。
func TestUpdateUserProfile_ChildCompanyNameIgnored(t *testing.T) {
	b, db := newCompanyNameTestBiz(t)
	p := createParent(t, db, "parent2", "父公司")
	c := createChild(t, db, "child2", p.ID)

	err := b.UpdateUserProfile(context.Background(), c.ID, &v1.UpdateUserProfileRequest{
		CompanyName: strptr("子账户瞎填"),
	})
	require.NoError(t, err)

	var got model.User
	require.NoError(t, db.First(&got, c.ID).Error)
	assert.Equal(t, "", got.CompanyName, "子账户不应写入自己的 company_name")
}

// AC3：父账户传空串 → 清空（GORM 零值落库关键测试，map 形式才能过）。
func TestUpdateUserProfile_ClearCompanyName(t *testing.T) {
	b, db := newCompanyNameTestBiz(t)
	p := createParent(t, db, "parent3", "老名字")

	err := b.UpdateUserProfile(context.Background(), p.ID, &v1.UpdateUserProfileRequest{
		CompanyName: strptr(""),
	})
	require.NoError(t, err)

	var got model.User
	require.NoError(t, db.First(&got, p.ID).Error)
	assert.Equal(t, "", got.CompanyName, "父账户传空串应清空 company_name（不可被 GORM 零值跳过）")
}

// nil CompanyName → 不改动现有值。
func TestUpdateUserProfile_NilCompanyNameUnchanged(t *testing.T) {
	b, db := newCompanyNameTestBiz(t)
	p := createParent(t, db, "parent4", "保持不变")

	err := b.UpdateUserProfile(context.Background(), p.ID, &v1.UpdateUserProfileRequest{
		Nickname: strptr("新昵称"),
	})
	require.NoError(t, err)

	var got model.User
	require.NoError(t, db.First(&got, p.ID).Error)
	assert.Equal(t, "保持不变", got.CompanyName, "未传 company_name 不应改动")
	assert.Equal(t, "新昵称", got.Nickname)
}

// AC2 后端侧：父账户 ResolveCompanyName 返回自己的名。
func TestResolveCompanyName_Parent(t *testing.T) {
	b, db := newCompanyNameTestBiz(t)
	p := createParent(t, db, "parent5", "父名公司")

	name, err := b.ResolveCompanyName(context.Background(), p)
	require.NoError(t, err)
	assert.Equal(t, "父名公司", name)
}

// AC2 后端侧：子账户 ResolveCompanyName 返回父账户的名（继承）。
func TestResolveCompanyName_Child(t *testing.T) {
	b, db := newCompanyNameTestBiz(t)
	p := createParent(t, db, "parent6", "父名公司6")
	c := createChild(t, db, "child6", p.ID)

	name, err := b.ResolveCompanyName(context.Background(), c)
	require.NoError(t, err)
	assert.Equal(t, "父名公司6", name, "子账户应继承父账户品牌名")
}

// 父账户记录异常缺失 → 返回空串不报错（不阻断 /me）。
func TestResolveCompanyName_ChildParentMissing(t *testing.T) {
	b, db := newCompanyNameTestBiz(t)
	c := createChild(t, db, "child7", 999999) // 指向不存在的父

	name, err := b.ResolveCompanyName(context.Background(), c)
	require.NoError(t, err, "父缺失不应报错")
	assert.Equal(t, "", name)
}
