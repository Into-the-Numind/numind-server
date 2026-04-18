package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// migrateUserSchema 为 SQLite 创建 user 表（ENUM -> TEXT 退化）。
// 只覆盖 Track A 的 BillingMode 字段测试需要的最小列集合（真实 DDL 见
// migrations/20260419_100000_add_billing_mode_to_user.sql）。
func migrateUserSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	ddl := `CREATE TABLE user (
		id               INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at       DATETIME,
		updated_at       DATETIME,
		deleted_at       DATETIME,
		phone            TEXT,
		nickname         TEXT,
		avatar_url       TEXT,
		parent_user_id   INTEGER,
		total_sop_runs   INTEGER DEFAULT 0,
		monthly_sop_runs INTEGER DEFAULT 0,
		monthly_reset_at DATETIME,
		user_tier        TEXT DEFAULT 'free',
		tier_expires     DATETIME,
		billing_mode     TEXT NOT NULL DEFAULT 'credits',
		username         TEXT,
		password         TEXT,
		is_admin         INTEGER DEFAULT 0,
		status           INTEGER DEFAULT 0,
		last_login       DATETIME
	)`
	require.NoError(t, db.Exec(ddl).Error)
}

func TestUserBillingMode_Constants(t *testing.T) {
	assert.Equal(t, "legacy_tier", BillingModeLegacyTier)
	assert.Equal(t, "credits", BillingModeCredits)
}

func TestUserBillingMode_FieldDefault(t *testing.T) {
	// 验证未显式赋值时 DB 默认值为 'credits'（由 DDL DEFAULT 约束）
	db := newTestDB(t)
	migrateUserSchema(t, db)

	// 插入一行不带 billing_mode
	require.NoError(t, db.Exec(`INSERT INTO user (phone, user_tier) VALUES (?, ?)`, "13800000001", "free").Error)

	var u User
	require.NoError(t, db.Where("phone = ?", "13800000001").First(&u).Error)
	assert.Equal(t, BillingModeCredits, u.BillingMode, "默认 billing_mode 应为 'credits'")
}

func TestUserBillingMode_CreateAndQueryLegacyTier(t *testing.T) {
	db := newTestDB(t)
	migrateUserSchema(t, db)

	u := User{
		Phone:       "13800000002",
		Nickname:    "Alice",
		UserTier:    UserTierStandard,
		BillingMode: BillingModeLegacyTier,
	}
	require.NoError(t, db.Create(&u).Error)
	assert.NotZero(t, u.ID)

	var got User
	require.NoError(t, db.First(&got, u.ID).Error)
	assert.Equal(t, BillingModeLegacyTier, got.BillingMode)
	assert.Equal(t, UserTierStandard, got.UserTier)
	assert.Equal(t, "Alice", got.Nickname)
}

func TestUserBillingMode_CreateAndQueryCredits(t *testing.T) {
	db := newTestDB(t)
	migrateUserSchema(t, db)

	u := User{
		Phone:       "13800000003",
		UserTier:    UserTierFree,
		BillingMode: BillingModeCredits,
	}
	require.NoError(t, db.Create(&u).Error)

	var got User
	require.NoError(t, db.First(&got, u.ID).Error)
	assert.Equal(t, BillingModeCredits, got.BillingMode)
}

func TestUserBillingMode_UpdateLegacyToCredits(t *testing.T) {
	// 模拟"老会员到期升级后由 legacy_tier 切换到 credits"的路径（spec §2.11.3 / §5.4）
	db := newTestDB(t)
	migrateUserSchema(t, db)

	u := User{Phone: "13800000004", UserTier: UserTierPremium, BillingMode: BillingModeLegacyTier}
	require.NoError(t, db.Create(&u).Error)

	require.NoError(t, db.Model(&User{}).
		Where("id = ? AND billing_mode = ?", u.ID, BillingModeLegacyTier).
		Update("billing_mode", BillingModeCredits).Error)

	var after User
	require.NoError(t, db.First(&after, u.ID).Error)
	assert.Equal(t, BillingModeCredits, after.BillingMode)
}
