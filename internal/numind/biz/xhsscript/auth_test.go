package xhsscript

import (
	"context"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
	passwordauth "numind-server/pkg/auth"
)

func TestRegisterStoresHashedPasswordAndLogin(t *testing.T) {
	svc, db := newAuthTestService(t)
	ctx := context.Background()

	session, err := svc.Register(ctx, nil, "Creator2026", "secret123")
	require.NoError(t, err)
	assert.NotEmpty(t, session.AccessToken)
	assert.Equal(t, "creator2026", session.User.Username)
	assert.False(t, session.User.IsAnonymous)
	assert.EqualValues(t, 3, session.Quota.FreeRemaining)
	assert.EqualValues(t, 3, session.Quota.Remaining)

	var user model.User
	require.NoError(t, db.Where("username = ?", "creator2026").First(&user).Error)
	assert.NotEqual(t, "secret123", user.Password)
	assert.NoError(t, passwordauth.Compare(user.Password, "secret123"))

	loginSession, err := svc.Login(ctx, "Creator2026", "secret123")
	require.NoError(t, err)
	assert.NotEmpty(t, loginSession.AccessToken)
	assert.Equal(t, user.ID, loginSession.User.ID)
}

func TestEnsureTrialIsDisabledForForcedRegistration(t *testing.T) {
	svc, db := newAuthTestService(t)
	ctx := context.Background()

	session, err := svc.EnsureTrial(ctx, "browser-anon-id")

	require.Error(t, err)
	assert.Nil(t, session)
	assert.Contains(t, err.Error(), "请先注册账号")

	var count int64
	require.NoError(t, db.Model(&model.User{}).
		Where("username LIKE ?", AnonymousPrefix+"%").
		Count(&count).Error)
	assert.EqualValues(t, 0, count)
}

func TestRegisterGrantsFreeQuotaOnlyOncePerTrialClaim(t *testing.T) {
	svc, _ := newAuthTestService(t)
	ctx := context.Background()

	first, err := svc.Register(ctx, nil, "firstuser", "secret123",
		TrialClaimInput{Type: "visitor", Value: "visitor-a"},
		TrialClaimInput{Type: "ip", Value: "203.0.113.10"},
	)
	require.NoError(t, err)
	assert.EqualValues(t, 3, first.Quota.FreeRemaining)

	sameVisitor, err := svc.Register(ctx, nil, "seconduser", "secret123",
		TrialClaimInput{Type: "visitor", Value: "visitor-a"},
		TrialClaimInput{Type: "ip", Value: "203.0.113.11"},
	)
	require.NoError(t, err)
	assert.EqualValues(t, 0, sameVisitor.Quota.FreeRemaining)
	assert.EqualValues(t, 0, sameVisitor.Quota.Remaining)

	sameIP, err := svc.Register(ctx, nil, "thirduser", "secret123",
		TrialClaimInput{Type: "visitor", Value: "visitor-c"},
		TrialClaimInput{Type: "ip", Value: "203.0.113.10"},
	)
	require.NoError(t, err)
	assert.EqualValues(t, 0, sameIP.Quota.FreeRemaining)

	freshClaims, err := svc.Register(ctx, nil, "fourthuser", "secret123",
		TrialClaimInput{Type: "visitor", Value: "visitor-d"},
		TrialClaimInput{Type: "ip", Value: "203.0.113.12"},
	)
	require.NoError(t, err)
	assert.EqualValues(t, 3, freshClaims.Quota.FreeRemaining)
}

func TestRegisterRejectsNonAlphanumericUsername(t *testing.T) {
	svc, _ := newAuthTestService(t)
	ctx := context.Background()

	for _, username := range []string{"creator_account", "中文账号", "creator-01", "ab", "abcdefghijklmnopqrstu"} {
		t.Run(username, func(t *testing.T) {
			_, err := svc.Register(ctx, nil, username, "secret123")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "账号只能使用 3-20 位英文或数字")
		})
	}
}

func TestRegisterRejectsPasswordOutsideAllowedLength(t *testing.T) {
	svc, _ := newAuthTestService(t)
	ctx := context.Background()

	for name, password := range map[string]string{
		"too_short": "12345",
		"too_long":  "123456789012345678901",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.Register(ctx, nil, "creator2026", password)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "密码需要 6-20 个字符")
		})
	}
}

func TestLoginUpgradesLegacyPlaintextPassword(t *testing.T) {
	svc, db := newAuthTestService(t)
	ctx := context.Background()
	user := model.User{
		Username: "legacy_creator",
		Password: "legacy123",
		Nickname: "legacy_creator",
		Status:   1,
	}
	require.NoError(t, db.Create(&user).Error)

	session, err := svc.Login(ctx, "legacy_creator", "legacy123")
	require.NoError(t, err)
	assert.NotEmpty(t, session.AccessToken)

	var reloaded model.User
	require.NoError(t, db.First(&reloaded, user.ID).Error)
	assert.NotEqual(t, "legacy123", reloaded.Password)
	assert.NoError(t, passwordauth.Compare(reloaded.Password, "legacy123"))
}

func newAuthTestService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	viper.Set("jwt.secret", "xhs-script-auth-test-secret")

	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/xhs_auth_test.db?_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{},
		&model.XhsScriptQuotaAccount{},
		&model.XhsScriptTrialClaim{},
		&model.XhsScriptQuotaLedger{},
	))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	return New(store.NewTestStore(db)), db
}
