package sop

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"numind-server/internal/numind/biz/customer"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// IsSopVisibleToUser 判断 SOP 是否对给定用户在工作区列表可见.
//
// 判断逻辑:
//   - caller 是父账户 (parent_user_id IS NULL) → true (父账户总是可见自己的实体)
//   - SOP.visibility_restricted == false → true (开关未启用, 全部子用户可见)
//   - SOP.visibility_restricted == true → 查 grant 表, sub_user_id 在白名单则 true, 否则 false
//
// 详见 spec §4.1.1 (sop-chatbot-visibility-scope).
func IsSopVisibleToUser(ctx context.Context, s store.IStore, userID, sopID uint) (bool, error) {
	user, err := s.Users().GetByID(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("IsSopVisibleToUser: get user: %w", err)
	}
	if user.ParentUserID == nil {
		return true, nil // 父账户 bypass
	}
	sop, err := s.Sop().GetTemplate(sopID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, errno.ErrSopTemplateNotFound
		}
		return false, fmt.Errorf("IsSopVisibleToUser: get sop: %w", err)
	}
	if !sop.VisibilityRestricted {
		return true, nil // 短路: 未启用限制
	}
	count, err := s.SopVisibilityGrant().CountBySubUserAndSop(ctx, userID, sopID)
	if err != nil {
		return false, fmt.Errorf("IsSopVisibleToUser: count grant: %w", err)
	}
	return count > 0, nil
}

// ListSubUserVisibleSopIDs 返回该子用户在 sop_visibility_grant 表中所有未软删的 sop_template_id 集合.
//
// 返回的 set 包含: 当前 restricted=true 的 SOP 中的 grant + D3 保留语义下 restricted=false 的 SOP 的历史 grant.
// 过滤逻辑由调用方 (§4.2.1) 结合 sop.visibility_restricted 字段判断:
//   - sop.visibility_restricted=false → 全部子用户可见 (不查 set)
//   - sop.visibility_restricted=true 且 sopID 不在 set → 该子用户看不到此 SOP
//   - sop.visibility_restricted=true 且 sopID 在 set → 该子用户可见
//
// 父账户调用此函数无意义 (应在调用前判断 ParentUserID == nil 直接放行).
// 详见 spec §4.1.3.
func ListSubUserVisibleSopIDs(ctx context.Context, s store.IStore, subUserID uint) (map[uint]struct{}, error) {
	return s.SopVisibilityGrant().ListVisibleSopIDsBySubUser(ctx, subUserID)
}

// GetSopVisibility 返回 SOP 的可见范围配置 (restricted, subUserIDs, error).
//
// subUserIDs 始终从 grant 表返回 (D3 保留语义: restricted=false 时也返回历史名单,
// 前端用于 "上次已配置 N 位" 提示).
//
// 接收 callerID 用于 owner 校验 (业务逻辑统一在 biz 层, controller 不重复).
// 校验顺序: 先身份后资源 (身份非父账户 → 直接拒绝, 不暴露 SOP 是否存在):
//   - caller 是子账户 → ErrVisibilityPermissionDenied
//   - SOP 不存在 → ErrSopTemplateNotFound
//   - SOP.creator_user_id != callerID → ErrEntityNotOwnedByCaller
//
// 详见 spec §4.1.5.
func GetSopVisibility(ctx context.Context, s store.IStore, callerID, sopID uint) (bool, []uint, error) {
	caller, err := s.Users().GetByID(ctx, callerID)
	if err != nil {
		return false, nil, fmt.Errorf("GetSopVisibility: get caller: %w", err)
	}
	if caller.ParentUserID != nil {
		return false, nil, errno.ErrVisibilityPermissionDenied
	}
	sop, err := s.Sop().GetTemplate(sopID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil, errno.ErrSopTemplateNotFound
		}
		return false, nil, fmt.Errorf("GetSopVisibility: get sop: %w", err)
	}
	if sop.CreatorUserID == nil || *sop.CreatorUserID != callerID {
		return false, nil, errno.ErrEntityNotOwnedByCaller
	}
	ids, err := s.SopVisibilityGrant().ListSubUserIDsBySopID(ctx, sopID)
	if err != nil {
		return false, nil, fmt.Errorf("GetSopVisibility: list grants: %w", err)
	}
	return sop.VisibilityRestricted, ids, nil
}

// UpdateSopVisibility 更新 SOP 的可见范围配置 (D3 + 双路径删除模式).
//
// 当 restricted=true 时, 全删全插 grant (用 Unscoped 物理删避免唯一索引冲突);
// restricted=false 时, 不动 grant 表 (D3 保留名单, 下次打开恢复同一名单).
//
// 详见 spec §4.1.6.
func UpdateSopVisibility(ctx context.Context, s store.IStore, callerID, sopID uint, restricted bool, subUserIDs []uint) error {
	// 身份校验先于资源查询 (避免暴露 SOP 是否存在给非父账户).
	caller, err := s.Users().GetByID(ctx, callerID)
	if err != nil {
		return fmt.Errorf("UpdateSopVisibility: get caller: %w", err)
	}
	if caller.ParentUserID != nil {
		return errno.ErrVisibilityPermissionDenied
	}
	sop, err := s.Sop().GetTemplate(sopID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errno.ErrSopTemplateNotFound
		}
		return fmt.Errorf("UpdateSopVisibility: get sop: %w", err)
	}
	if sop.CreatorUserID == nil || *sop.CreatorUserID != callerID {
		return errno.ErrEntityNotOwnedByCaller
	}
	if restricted {
		if err := customer.ValidateSubUsersBelongToCaller(ctx, s, callerID, subUserIDs); err != nil {
			return err
		}
	}

	// 事务: 使用项目既有 b.ds.DB().WithContext(ctx).Transaction(...) 模式
	// (IStore 接口只暴露 DB() *gorm.DB, 不存在 WithTx 包装)
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if restricted {
			// restricted=true 路径: 物理删全部旧 grant (含软删) + 插新 grant
			// Unscoped() 关键: 避免 (sub_user_id, sop_template_id) 唯一索引与残留软删记录冲突
			if err := s.SopVisibilityGrant().ReplaceGrantsTx(ctx, tx, sopID, callerID, subUserIDs); err != nil {
				return fmt.Errorf("UpdateSopVisibility: replace grants: %w", err)
			}
		}
		// restricted=false 路径: D3 锁定 — 不动 grant 表, 仅切换短路字段
		// 重新打开开关时 GetSopVisibility 仍能返回历史 sub_user_ids

		// 更新 entity 短路字段 (两路径都执行)
		// 用 Update("column_name", val) 而非 Updates(struct), 避免 GORM default:true bool gotcha
		// (database.md §6; 本字段 default:0 实际无此风险, 但保持代码模式一致)
		return tx.Model(&model.SopTemplate{}).Where("id = ?", sopID).
			Update("visibility_restricted", restricted).Error
	})
}
