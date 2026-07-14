package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// IThirdPartyAccountStore 定义第三方平台账号凭据的数据库操作接口
// （feishu-integration feature）。
//
// 加解密约定（design.md §4）：本接口收发的 token/secret 字段一律是 AES-256-GCM
// **密文**（[]byte）；加解密在调用方（biz/feishu 服务、provisioner 等 store 边界）完成，
// store 层只做纯持久化，绝不在此明文落库或落日志。
type IThirdPartyAccountStore interface {
	// Get 按 (userID, provider) 查凭据；不存在返回 gorm.ErrRecordNotFound，
	// 由 biz 层显式处理（未连接分支 → ErrLarkNotConnected）。
	Get(ctx context.Context, userID uint, provider string) (*model.UserThirdPartyAccount, error)
	// Upsert 按唯一键 (user_id, provider) 创建或更新（幂等）。重复授权 = 更新现有行。
	Upsert(ctx context.Context, acc *model.UserThirdPartyAccount) error
	// EnsurePlaceholder atomically creates a generation-1 disconnected row when
	// none exists and never mutates an existing account.
	EnsurePlaceholder(ctx context.Context, userID uint, provider string) (*model.UserThirdPartyAccount, error)
	// Delete 删除某用户某 provider 的连接（解绑）。目标不存在时幂等无错。
	Delete(ctx context.Context, userID uint, provider string) error
	// UpdateTokens 仅更新 token 三元组（access/refresh 密文 + 过期时间），用于刷新流程。
	// 目标不存在返回 gorm.ErrRecordNotFound（避免静默 no-op）。
	// refreshEnc/exp 传 nil 时写入 NULL（飞书未返回 refresh_token / 过期时间）。
	//
	// Deprecated（G2-authorize device-code）：token 不再入库，本方法仅为兼容历史调用保留，
	// 新代码不应使用。
	UpdateTokens(ctx context.Context, userID uint, provider string, accessEnc, refreshEnc []byte, exp *time.Time) error
	// MarkConnected 将 (userID, provider) 标记为已完成 device-code 授权：
	// connected=true + connected_at=connectedAt。目标不存在返回 gorm.ErrRecordNotFound
	// （避免静默 no-op）。device-code 方案下连接状态的权威写入口。
	MarkConnected(ctx context.Context, userID uint, provider string, connectedAt time.Time) error
	// RetireGeneration atomically fences the current workspace generation before
	// local teardown: waiting sessions are superseded, waiting operations are
	// cancelled, and executing operations are closed as unknown. The returned
	// next generation is the sole generation FinalizeDisconnect may clear.
	RetireGeneration(ctx context.Context, userID uint, provider string) (retiredGeneration, nextGeneration uint64, err error)
	// FinalizeDisconnect clears local connection metadata only for the generation
	// created by RetireGeneration. It never deletes or claims to delete the
	// remote third-party application.
	FinalizeDisconnect(ctx context.Context, userID uint, provider string, generation uint64) error
}

// thirdPartyAccountStore 是 IThirdPartyAccountStore 的 GORM 实现。
type thirdPartyAccountStore struct {
	db *gorm.DB
}

// newThirdPartyAccountStore 创建一个 IThirdPartyAccountStore 实例。
func newThirdPartyAccountStore(db *gorm.DB) *thirdPartyAccountStore {
	return &thirdPartyAccountStore{db: db}
}

// Get 按 (userID, provider) 查凭据。
func (s *thirdPartyAccountStore) Get(ctx context.Context, userID uint, provider string) (*model.UserThirdPartyAccount, error) {
	var acc model.UserThirdPartyAccount
	if err := s.db.WithContext(ctx).
		Where("user_id = ? AND provider = ?", userID, provider).
		First(&acc).Error; err != nil {
		return nil, err
	}
	return &acc, nil
}

// Upsert 按唯一键 (user_id, provider) 创建或更新。
// 使用 ON CONFLICT DO UPDATE 保证重复连接幂等（design §3）。
// device-code 方案下只更新连接元信息（app_id 等），不更新 connected/connected_at
// （连接状态由 MarkConnected 单独写，避免 Upsert app 元信息时误清连接标志）。
func (s *thirdPartyAccountStore) Upsert(ctx context.Context, acc *model.UserThirdPartyAccount) error {
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "user_id"}, {Name: "provider"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"app_id", "app_secret_enc", "access_token_enc", "refresh_token_enc",
				"token_expires_at", "scopes", "updated_at",
			}),
		}).
		Create(acc).Error
}

// EnsurePlaceholder creates the minimum row required by generation-fenced
// Feishu operation claims. ON CONFLICT DO NOTHING is intentional: a concurrent
// connection flow may have populated app and generation metadata after the
// caller's initial read, and the placeholder path must never overwrite it.
func (s *thirdPartyAccountStore) EnsurePlaceholder(ctx context.Context, userID uint, provider string) (*model.UserThirdPartyAccount, error) {
	placeholder := &model.UserThirdPartyAccount{
		UserID:          userID,
		Provider:        provider,
		ConnectionState: model.FeishuConnectionNone,
		Generation:      1,
	}
	if err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(placeholder).Error; err != nil {
		return nil, err
	}
	return s.Get(ctx, userID, provider)
}

// MarkConnected 标记 (userID, provider) 已完成 device-code 授权。
// 用 map 形式 Updates 显式包含每个键，确保 connected=true / connected_at 写入
// （bool 零值/nil 不被 GORM 跳过，见 .claude/rules/database.md §6b）。
func (s *thirdPartyAccountStore) MarkConnected(ctx context.Context, userID uint, provider string, connectedAt time.Time) error {
	res := s.db.WithContext(ctx).
		Model(&model.UserThirdPartyAccount{}).
		Where("user_id = ? AND provider = ?", userID, provider).
		Updates(map[string]interface{}{
			"connected":    true,
			"connected_at": connectedAt,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// RetireGeneration serializes secure workspace unbinding under the account row
// lock. The lock order is account → session/operation; it deliberately never
// touches the vault here, because callers first need the generation fence to
// make all in-flight vault writes fail closed before deleting ciphertext.
func (s *thirdPartyAccountStore) RetireGeneration(
	ctx context.Context,
	userID uint,
	provider string,
) (retiredGeneration, nextGeneration uint64, retErr error) {
	if userID == 0 || provider != "lark" {
		return 0, 0, gorm.ErrRecordNotFound
	}
	now := time.Now().UTC()
	retErr = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account model.UserThirdPartyAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND provider = ?", userID, provider).
			Take(&account).Error; err != nil {
			return err
		}
		if account.Generation == 0 {
			return gorm.ErrRecordNotFound
		}
		// A prior unbind may have fenced successfully but failed while deleting
		// the retired vault. Reuse that exact retired generation on retry rather
		// than advancing again and making the remaining ciphertext unreachable.
		if account.ConnectionState == model.FeishuConnectionDisconnecting {
			if account.Generation <= 1 {
				return gorm.ErrRecordNotFound
			}
			retiredGeneration = account.Generation - 1
			nextGeneration = account.Generation
			return nil
		}
		retiredGeneration = account.Generation
		nextGeneration = retiredGeneration + 1
		if nextGeneration == 0 { // uint64 overflow is an invalid lifecycle state.
			return fmt.Errorf("retire third-party account generation overflow")
		}

		// The generation update is the cross-instance fence. Operations use an
		// account-generation EXISTS predicate for every claim/transition, so any
		// old worker that already started can no longer commit a success result.
		if err := tx.Model(&model.UserThirdPartyAccount{}).
			Where("user_id = ? AND provider = ? AND generation = ?", userID, provider, retiredGeneration).
			Updates(map[string]any{
				"generation":       nextGeneration,
				"connection_state": model.FeishuConnectionDisconnecting,
				"connected":        false,
				"connected_at":     nil,
			}).Error; err != nil {
			return err
		}

		if err := tx.Model(&model.FeishuAuthSession{}).
			Where("user_id = ? AND generation = ? AND state = ?", userID, retiredGeneration, model.FeishuAuthSessionPending).
			Updates(map[string]any{
				"state": model.FeishuAuthSessionSuperseded, "completed_at": now,
				"lease_owner": "", "lease_until": nil,
			}).Error; err != nil {
			return fmt.Errorf("retire feishu authorization sessions: %w", err)
		}

		pendingStates := []string{
			model.FeishuOperationNotStarted,
			model.FeishuOperationWaitingConnection,
			model.FeishuOperationWaitingAppScope,
			model.FeishuOperationWaitingUserAuth,
			model.FeishuOperationWaitingConfirmation,
		}
		if err := tx.Model(&model.FeishuOperation{}).
			Where("user_id = ? AND generation = ? AND state IN ?", userID, retiredGeneration, pendingStates).
			Updates(map[string]any{
				"state": model.FeishuOperationCancelled, "finished_at": now,
				"lease_owner": "", "lease_until": nil,
			}).Error; err != nil {
			return fmt.Errorf("cancel pending feishu operations: %w", err)
		}

		unknownSummary, err := json.Marshal(map[string]string{
			"status": model.FeishuOperationUnknown, "public_code": "unknown_result",
		})
		if err != nil {
			return fmt.Errorf("encode retired feishu unknown result: %w", err)
		}
		if err := tx.Model(&model.FeishuOperation{}).
			Where("user_id = ? AND generation = ? AND state = ?", userID, retiredGeneration, model.FeishuOperationExecuting).
			Updates(map[string]any{
				"state": model.FeishuOperationUnknown, "finished_at": now,
				"lease_owner": "", "lease_until": nil,
				"error_type": "classified", "error_subtype": "unknown_result", "error_code": "",
				"result_summary_json": unknownSummary,
			}).Error; err != nil {
			return fmt.Errorf("close executing feishu operations as unknown: %w", err)
		}
		return nil
	})
	if retErr != nil {
		return 0, 0, retErr
	}
	return retiredGeneration, nextGeneration, nil
}

// FinalizeDisconnect clears only Numind-side metadata after RetireGeneration
// has fenced all old work and the caller has stopped local workers/deleted the
// retired vault. The conditional generation protects a concurrent reconnect.
func (s *thirdPartyAccountStore) FinalizeDisconnect(ctx context.Context, userID uint, provider string, generation uint64) error {
	if userID == 0 || provider != "lark" || generation == 0 {
		return gorm.ErrRecordNotFound
	}
	result := s.db.WithContext(ctx).Model(&model.UserThirdPartyAccount{}).
		Where("user_id = ? AND provider = ? AND generation = ? AND connection_state = ?", userID, provider, generation, model.FeishuConnectionDisconnecting).
		Updates(map[string]any{
			"app_id":                "",
			"app_secret_enc":        nil,
			"access_token_enc":      nil,
			"refresh_token_enc":     nil,
			"token_expires_at":      nil,
			"scopes":                "",
			"connected":             false,
			"connected_at":          nil,
			"connection_state":      model.FeishuConnectionNone,
			"lark_cli_version":      "",
			"granted_scopes_json":   nil,
			"capability_state_json": nil,
			"last_success_at":       nil,
			"last_error_code":       nil,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// Delete 删除某用户某 provider 的连接（解绑）；目标不存在时幂等无错。
func (s *thirdPartyAccountStore) Delete(ctx context.Context, userID uint, provider string) error {
	return s.db.WithContext(ctx).
		Where("user_id = ? AND provider = ?", userID, provider).
		Delete(&model.UserThirdPartyAccount{}).Error
}

// UpdateTokens 仅更新 token 三元组（刷新流程）。
// 用 map 形式 Updates 显式包含每个键，确保 nil refresh/exp 能写入 NULL，
// 不被 GORM 零值跳过（见 .claude/rules/database.md §6b）。
func (s *thirdPartyAccountStore) UpdateTokens(ctx context.Context, userID uint, provider string, accessEnc, refreshEnc []byte, exp *time.Time) error {
	res := s.db.WithContext(ctx).
		Model(&model.UserThirdPartyAccount{}).
		Where("user_id = ? AND provider = ?", userID, provider).
		Updates(map[string]interface{}{
			"access_token_enc":  accessEnc,
			"refresh_token_enc": refreshEnc,
			"token_expires_at":  exp,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
