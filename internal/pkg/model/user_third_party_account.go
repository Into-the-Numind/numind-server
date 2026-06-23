package model

import "time"

// UserThirdPartyAccount 保存某个有数用户对接的第三方平台账号凭据
// （feishu-integration feature；首批 provider="lark"）。
//
// 设计要点（见 design.md §3 §4）：
//   - 每个 (user_id, provider) 唯一（uniq_user_provider）→ 重复授权走 UPSERT 幂等更新。
//   - app_secret / access_token / refresh_token 一律 AES-256-GCM 密文存储（[]byte blob），
//     绝不明文落库；加解密只在 store 读写边界进行（internal/pkg/crypto），biz 层拿明文。
//   - TokenExpiresAt 用指针：避免 GORM 把 time.Time 零值写成 0001-01-01 被误判为已过期；
//     飞书未返回过期时间时留 nil（视为未知/不主动刷新）。
//
// 隔离：本表仅在 feishu-integration feature 启用时参与 AutoMigrate（见 helper.go 条件迁移），
// 权威 schema + 显式 ROW_FORMAT 见 migrations/20260624_120000_create_user_third_party_account.sql。
type UserThirdPartyAccount struct {
	ID uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	// UserID 凭据归属用户。(user_id, provider) 唯一。
	UserID uint `gorm:"not null;uniqueIndex:uniq_user_provider,priority:1" json:"user_id"`
	// Provider 第三方平台标识，首批仅 "lark"（飞书）。
	Provider string `gorm:"size:32;not null;uniqueIndex:uniq_user_provider,priority:2" json:"provider"`
	// AppID 用户自建应用的 app_id（非敏感，明文存储）。
	AppID string `gorm:"size:64;not null" json:"app_id"`
	// AppSecretEnc 自建应用 app_secret 的 AES-256-GCM 密文。
	AppSecretEnc []byte `gorm:"type:blob" json:"-"`
	// AccessTokenEnc user_access_token 的 AES-256-GCM 密文。
	AccessTokenEnc []byte `gorm:"type:blob" json:"-"`
	// RefreshTokenEnc refresh_token 的 AES-256-GCM 密文（飞书提供时才有，否则为空）。
	RefreshTokenEnc []byte `gorm:"type:blob" json:"-"`
	// TokenExpiresAt access_token 过期时间；指针避免零值误判过期，飞书未返回时为 nil。
	TokenExpiresAt *time.Time `gorm:"column:token_expires_at" json:"token_expires_at,omitempty"`
	// Scopes 授权时一次性请求的全部 scope（空格分隔）。
	Scopes    string    `gorm:"size:512" json:"scopes"`
	CreatedAt time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName 指定表名。
func (UserThirdPartyAccount) TableName() string { return "user_third_party_account" }
