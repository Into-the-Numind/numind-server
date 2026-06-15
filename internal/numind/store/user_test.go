package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/model"
)

// TestStore_UpdateUser_ClearCompanyName 直打 store 层验证 GORM 零值落库行为（org-branding AC3）。
// 隔离的 store 级回归守卫：UpdateUser 改用 map 形式后，company_name="" 必须能真正写入 DB
// （struct 形式会跳过零值，见 .claude/rules/database.md §6b 的 TestStore_SaveService_UpdateIsActiveFalse 模式）。
func TestStore_UpdateUser_ClearCompanyName(t *testing.T) {
	db := newTestDB(t, &model.User{})
	st := NewUserStore(db)
	ctx := context.Background()

	// 先建一个带非空 company_name 的用户。
	u := &model.User{Username: "clear_target", CompanyName: "老名字"}
	require.NoError(t, db.Create(u).Error)

	// 清空 company_name（零值）。
	u.CompanyName = ""
	require.NoError(t, st.UpdateUser(ctx, u))

	// DB 行必须真的是空串，而不是被 GORM 零值跳过保留"老名字"。
	var got model.User
	require.NoError(t, db.First(&got, u.ID).Error)
	assert.Equal(t, "", got.CompanyName, "map 形式 UpdateUser 应把 company_name 清成空串")
}
