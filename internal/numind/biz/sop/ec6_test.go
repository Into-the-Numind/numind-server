package sop_test

import (
	"context"
	"testing"

	"numind-server/internal/numind/biz/sop"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeleteTemplate_CleanupVisibility EC-6 验证: 删除 SOP 时同事务清理 visibility grant.
// spec §9 EC-6 锁定行为: 实体删除时, 它的所有 visibility grant 记录被软删,
// 避免 grant 表残留指向不存在 SOP 的孤儿数据.
func TestDeleteTemplate_CleanupVisibility(t *testing.T) {
	db := newListFilterTestDB(t) // 复用 list_filter_test.go 的 helper, 含 SopTemplate + SopVisibilityGrant 表
	ds := store.NewTestStore(db)
	ctx := context.Background()

	parent := insertFilterUser(t, db, nil)
	sub1 := insertFilterUser(t, db, &parent)
	sub2 := insertFilterUser(t, db, &parent)
	sopID := insertFilterTpl(t, db, parent, "to-be-deleted", true)

	// 配置 visibility grants for sopID
	for _, subID := range []uint{sub1, sub2} {
		require.NoError(t, db.Create(&model.SopVisibilityGrant{
			ParentUserID:  parent,
			SubUserID:     subID,
			SopTemplateID: sopID,
		}).Error)
	}

	// 验证 grants 存在
	var beforeCount int64
	db.Model(&model.SopVisibilityGrant{}).Where("sop_template_id = ?", sopID).Count(&beforeCount)
	require.Equal(t, int64(2), beforeCount, "should have 2 grants before delete")

	b := sop.NewSopBiz(ds, nil, nil)
	require.NoError(t, b.DeleteTemplate(ctx, sopID))

	// 验证 SOP 已软删
	var sopCount int64
	db.Model(&model.SopTemplate{}).Where("id = ?", sopID).Count(&sopCount)
	assert.Equal(t, int64(0), sopCount, "SOP should be soft-deleted")

	// 验证 grants 已软删 (default scope 查 0 条)
	var afterActive int64
	db.Model(&model.SopVisibilityGrant{}).Where("sop_template_id = ?", sopID).Count(&afterActive)
	assert.Equal(t, int64(0), afterActive, "visibility grants should be soft-deleted")

	// 验证 unscoped 仍能看到 (软删审计)
	var unscoped int64
	db.Unscoped().Model(&model.SopVisibilityGrant{}).Where("sop_template_id = ?", sopID).Count(&unscoped)
	assert.Equal(t, int64(2), unscoped, "soft-deleted grants preserved for audit")
}

// TestDeleteTemplate_NoGrants 验证 SOP 无 grant 时, DeleteTemplate 仍正常工作 (不报错).
func TestDeleteTemplate_NoGrants(t *testing.T) {
	db := newListFilterTestDB(t)
	ds := store.NewTestStore(db)
	ctx := context.Background()

	parent := insertFilterUser(t, db, nil)
	sopID := insertFilterTpl(t, db, parent, "no-grants", false)

	b := sop.NewSopBiz(ds, nil, nil)
	require.NoError(t, b.DeleteTemplate(ctx, sopID), "delete with no grants should succeed (cleanup is idempotent)")
}
