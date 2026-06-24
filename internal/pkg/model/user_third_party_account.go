package model

import "time"

// UserThirdPartyAccount 保存某个有数用户对接的第三方平台账号连接元信息
// （feishu-integration feature；首批 provider="lark"）。
//
// 设计要点（G2-authorize device-code 重做，2026-06-24）：
//   - 每个 (user_id, provider) 唯一（uniq_user_provider）→ 重复连接走 UPSERT 幂等更新。
//   - **本表只存连接元信息（app_id + connected + connected_at）**：device-code 方案下
//     token 由 lark-cli 在每用户持久 HOME（<feishu.home_base>/u<userID>/.lark-cli/）里保管
//     并自动刷新，绝不入库。下方 *Enc 密文列与 TokenExpiresAt/Scopes 为历史遗留，device-code
//     方案不再写入（保留以避免 migration 复杂度）。
//   - Connected/ConnectedAt：device-code 授权完成后置 true + 记录时间，作为连接状态的
//     权威 DB 标志（Status 据此判断，无需解密任何 token）。
//
// 隔离：本表仅在 feishu-integration feature 启用时参与 AutoMigrate（见 helper.go 条件迁移），
// 权威 schema + 显式 ROW_FORMAT 见 migrations/20260624_120000_create_user_third_party_account.sql；
// connected/connected_at 列由 migrations/20260624_140000_add_connected_to_third_party_account.sql 追加。
type UserThirdPartyAccount struct {
	ID uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	// UserID 凭据归属用户。(user_id, provider) 唯一。
	UserID uint `gorm:"not null;uniqueIndex:uniq_user_provider,priority:1" json:"user_id"`
	// Provider 第三方平台标识，首批仅 "lark"（飞书）。
	Provider string `gorm:"size:32;not null;uniqueIndex:uniq_user_provider,priority:2" json:"provider"`
	// AppID 用户自建应用的 app_id（非敏感，明文存储）。
	AppID string `gorm:"size:64;not null" json:"app_id"`
	// Connected 是否已完成 device-code 授权（连接状态的权威 DB 标志）。
	Connected bool `gorm:"not null;default:false" json:"connected"`
	// ConnectedAt 完成授权的时间；未连接时为 nil。
	ConnectedAt *time.Time `gorm:"column:connected_at" json:"connected_at,omitempty"`
	// AppSecretEnc 自建应用 app_secret 的 AES-256-GCM 密文（device-code 方案遗留，不再写入）。
	AppSecretEnc []byte `gorm:"type:blob" json:"-"`
	// AccessTokenEnc user_access_token 的 AES-256-GCM 密文（device-code 方案遗留，不再写入）。
	AccessTokenEnc []byte `gorm:"type:blob" json:"-"`
	// RefreshTokenEnc refresh_token 的 AES-256-GCM 密文（device-code 方案遗留，不再写入）。
	RefreshTokenEnc []byte `gorm:"type:blob" json:"-"`
	// TokenExpiresAt access_token 过期时间（device-code 方案遗留，不再写入；token 由 lark-cli 管）。
	TokenExpiresAt *time.Time `gorm:"column:token_expires_at" json:"token_expires_at,omitempty"`
	// Scopes 授权时请求的全部 scope（空格分隔；device-code 方案遗留，不再写入）。
	Scopes    string    `gorm:"size:512" json:"scopes"`
	CreatedAt time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName 指定表名。
func (UserThirdPartyAccount) TableName() string { return "user_third_party_account" }
