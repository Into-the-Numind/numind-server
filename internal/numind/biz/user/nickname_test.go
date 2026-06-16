package user

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/model"
	v1 "numind-server/pkg/api/numind/v1"
)

// nickname-edit：昵称更新的 biz 层守卫测试。
// 复用 company_name_test.go 的 SQLite 内存 harness（newCompanyNameTestBiz / createParent / strptr）。

// 正常设置昵称 → 落库（含 trim）。
func TestUpdateUserProfile_SetNickname(t *testing.T) {
	b, db := newCompanyNameTestBiz(t)
	u := createParent(t, db, "nick1", "")

	err := b.UpdateUserProfile(context.Background(), u.ID, &v1.UpdateUserProfileRequest{
		Nickname: strptr("  小明  "),
	})
	require.NoError(t, err)

	var got model.User
	require.NoError(t, db.First(&got, u.ID).Error)
	assert.Equal(t, "小明", got.Nickname, "昵称应落库并 trim")
}

// 空串昵称 → 拒绝（govalidator 跳过空串，biz 层补齐必填守卫），且原昵称不变。
func TestUpdateUserProfile_EmptyNicknameRejected(t *testing.T) {
	b, db := newCompanyNameTestBiz(t)
	u := createParent(t, db, "nick2", "")
	require.NoError(t, db.Model(u).Update("nickname", "原昵称").Error)

	err := b.UpdateUserProfile(context.Background(), u.ID, &v1.UpdateUserProfileRequest{
		Nickname: strptr(""),
	})
	require.Error(t, err, "空串昵称应被拒绝")

	var got model.User
	require.NoError(t, db.First(&got, u.ID).Error)
	assert.Equal(t, "原昵称", got.Nickname, "拒绝后原昵称不变（未落库）")
}

// 纯空白昵称 → trim 后为空 → 拒绝。
func TestUpdateUserProfile_WhitespaceNicknameRejected(t *testing.T) {
	b, db := newCompanyNameTestBiz(t)
	u := createParent(t, db, "nick3", "")

	err := b.UpdateUserProfile(context.Background(), u.ID, &v1.UpdateUserProfileRequest{
		Nickname: strptr("   "),
	})
	require.Error(t, err, "纯空白昵称应被拒绝")
}
