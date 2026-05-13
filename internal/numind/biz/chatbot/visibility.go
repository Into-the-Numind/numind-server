package chatbot

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

// IsChatbotVisibleToUser 判断 chatbot 是否对给定用户在工作区列表可见.
//
// 判断逻辑 (对称 SOP 版本, 见 biz/sop/visibility.go IsSopVisibleToUser):
//   - caller 是父账户 (parent_user_id IS NULL) → true (父账户 bypass)
//   - chatbot.visibility_restricted == false → true (短路)
//   - chatbot.visibility_restricted == true → 查 grant 表
//
// 详见 spec §4.1.2 / §4.1.7.
func IsChatbotVisibleToUser(ctx context.Context, s store.IStore, userID, chatbotID uint) (bool, error) {
	user, err := s.Users().GetByID(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("IsChatbotVisibleToUser: get user: %w", err)
	}
	if user.ParentUserID == nil {
		return true, nil
	}
	chatbot, err := s.ChatbotConfig().Get(ctx, chatbotID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, errno.ErrChatbotNotFound
		}
		return false, fmt.Errorf("IsChatbotVisibleToUser: get chatbot: %w", err)
	}
	if !chatbot.VisibilityRestricted {
		return true, nil
	}
	count, err := s.ChatbotVisibilityGrant().CountBySubUserAndChatbot(ctx, userID, chatbotID)
	if err != nil {
		return false, fmt.Errorf("IsChatbotVisibleToUser: count grant: %w", err)
	}
	return count > 0, nil
}

// ListSubUserVisibleChatbotIDs 返回该子用户在 chatbot_visibility_grant 表中所有未软删的 chatbot_id 集合.
// 用法对称 ListSubUserVisibleSopIDs. 详见 spec §4.1.3.
func ListSubUserVisibleChatbotIDs(ctx context.Context, s store.IStore, subUserID uint) (map[uint]struct{}, error) {
	return s.ChatbotVisibilityGrant().ListVisibleChatbotIDsBySubUser(ctx, subUserID)
}

// GetChatbotVisibility 返回 chatbot 的可见范围配置 (restricted, subUserIDs, error).
//
// ⚠️ Owner 字段差异: ChatbotConfig.UserID 是 uint (非指针非零), 与 SopTemplate.CreatorUserID
// (*uint, 可能为 nil) 不同. 见 spec §4.1.7.
//
// 校验顺序 (与 GetSopVisibility 对称): 身份 → 资源 → 所有权.
func GetChatbotVisibility(ctx context.Context, s store.IStore, callerID, chatbotID uint) (bool, []uint, error) {
	caller, err := s.Users().GetByID(ctx, callerID)
	if err != nil {
		return false, nil, fmt.Errorf("GetChatbotVisibility: get caller: %w", err)
	}
	if caller.ParentUserID != nil {
		return false, nil, errno.ErrVisibilityPermissionDenied
	}
	chatbot, err := s.ChatbotConfig().Get(ctx, chatbotID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil, errno.ErrChatbotNotFound
		}
		return false, nil, fmt.Errorf("GetChatbotVisibility: get chatbot: %w", err)
	}
	if chatbot.UserID != callerID { // 注意: chatbot.UserID 是 uint, 直接比较 (非指针, 无 nil 检查)
		return false, nil, errno.ErrEntityNotOwnedByCaller
	}
	ids, err := s.ChatbotVisibilityGrant().ListSubUserIDsByChatbotID(ctx, chatbotID)
	if err != nil {
		return false, nil, fmt.Errorf("GetChatbotVisibility: list grants: %w", err)
	}
	return chatbot.VisibilityRestricted, ids, nil
}

// UpdateChatbotVisibility 更新 chatbot 的可见范围配置 (对称 UpdateSopVisibility).
//
// D3 + 双路径删除模式 (与 SOP 完全对称, 仅 owner 字段差异):
//   - restricted=true: 全删全插 (Unscoped 物理删 + 重插)
//   - restricted=false: 不动 grant 表 (D3 保留)
//
// 详见 spec §4.1.7.
func UpdateChatbotVisibility(ctx context.Context, s store.IStore, callerID, chatbotID uint, restricted bool, subUserIDs []uint) error {
	caller, err := s.Users().GetByID(ctx, callerID)
	if err != nil {
		return fmt.Errorf("UpdateChatbotVisibility: get caller: %w", err)
	}
	if caller.ParentUserID != nil {
		return errno.ErrVisibilityPermissionDenied
	}
	chatbot, err := s.ChatbotConfig().Get(ctx, chatbotID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errno.ErrChatbotNotFound
		}
		return fmt.Errorf("UpdateChatbotVisibility: get chatbot: %w", err)
	}
	if chatbot.UserID != callerID { // ⚠️ 非指针, 直接比较 (与 SOP CreatorUserID *uint 区分)
		return errno.ErrEntityNotOwnedByCaller
	}
	if restricted {
		if err := customer.ValidateSubUsersBelongToCaller(ctx, s, callerID, subUserIDs); err != nil {
			return err
		}
	}

	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if restricted {
			if err := s.ChatbotVisibilityGrant().ReplaceGrantsTx(ctx, tx, chatbotID, callerID, subUserIDs); err != nil {
				return fmt.Errorf("UpdateChatbotVisibility: replace grants: %w", err)
			}
		}
		// restricted=false: D3 锁定, 不动 grant 表

		return tx.Model(&model.ChatbotConfig{}).Where("id = ?", chatbotID).
			Update("visibility_restricted", restricted).Error
	})
}
