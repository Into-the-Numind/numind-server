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

	session, err := svc.Register(ctx, nil, "creator_account", "secret123")
	require.NoError(t, err)
	assert.NotEmpty(t, session.AccessToken)
	assert.Equal(t, "creator_account", session.User.Username)

	var user model.User
	require.NoError(t, db.Where("username = ?", "creator_account").First(&user).Error)
	assert.NotEqual(t, "secret123", user.Password)
	assert.NoError(t, passwordauth.Compare(user.Password, "secret123"))

	loginSession, err := svc.Login(ctx, "creator_account", "secret123")
	require.NoError(t, err)
	assert.NotEmpty(t, loginSession.AccessToken)
	assert.Equal(t, user.ID, loginSession.User.ID)
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
		&model.XhsScriptQuotaLedger{},
	))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	return New(store.NewTestStore(db)), db
}
