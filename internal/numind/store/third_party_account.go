package store

import (
	"context"
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
