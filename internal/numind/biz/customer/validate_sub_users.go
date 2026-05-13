package customer

import (
	"context"
	"fmt"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// ValidateSubUsersBelongToCaller 两步校验 subUserIDs:
//
//	Step 1: 全部 ID 在 user 表中存在 (不含软删) → 否则 ErrSubUserNotFound
//	Step 2: 全部 ID 的 parent_user_id 等于 callerID → 否则 ErrCrossParentSubUser
//
// 接收 store.IStore (而非裸 *gorm.DB) 以符合三层架构 (biz 层通过 store 接口访问数据);
// 单次性 COUNT 查询走 s.DB() 与项目既有 biz 事务模式一致, 不污染 UserStore 接口.
// 两步分离, 让前端能精准展示 "用户不存在" vs "用户存在但不属于你" 两种错误.
//
// 详见 spec §4.1.8 (sop-chatbot-visibility-scope).
func ValidateSubUsersBelongToCaller(ctx context.Context, s store.IStore, callerID uint, subUserIDs []uint) error {
	if len(subUserIDs) == 0 {
		return nil
	}
	db := s.DB().WithContext(ctx)

	// Step 1: 存在性 (GORM 默认 scope 自动过滤 deleted_at IS NULL)
	var existCount int64
	if err := db.Model(&model.User{}).
		Where("id IN ?", subUserIDs).Count(&existCount).Error; err != nil {
		return fmt.Errorf("ValidateSubUsersBelongToCaller: count exist: %w", err)
	}
	if existCount != int64(len(subUserIDs)) {
		return errno.ErrSubUserNotFound
	}

	// Step 2: 归属
	var belongCount int64
	if err := db.Model(&model.User{}).
		Where("id IN ? AND parent_user_id = ?", subUserIDs, callerID).Count(&belongCount).Error; err != nil {
		return fmt.Errorf("ValidateSubUsersBelongToCaller: count belong: %w", err)
	}
	if belongCount != int64(len(subUserIDs)) {
		return errno.ErrCrossParentSubUser
	}
	return nil
}
