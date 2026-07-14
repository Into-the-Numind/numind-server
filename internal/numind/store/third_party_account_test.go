package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"numind-server/internal/pkg/crypto"
	"numind-server/internal/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newThirdPartyAccountTestDB 创建 user_third_party_account store 测试用的内存 SQLite DB。
// blob 列在 sqlite 下退化为 BLOB，[]byte 往返兼容；唯一索引由 GORM tag 在 AutoMigrate 建出。
func newThirdPartyAccountTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared&_busy_timeout=5000"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.UserThirdPartyAccount{}))

	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1) // 同名内存库多连接会锁，限单连接
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// testCipher 构造一个固定 32 字节密钥的 crypto.Cipher，供加密往返断言用。
func testCipher(t *testing.T) *crypto.Cipher {
	t.Helper()
	// base64 of 32 bytes "0123456789abcdef0123456789abcdef"
	c, err := crypto.NewCipher("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	require.NoError(t, err)
	return c
}

func TestThirdPartyAccountStore_UpsertAndGet_CryptoRoundTrip(t *testing.T) {
	db := newThirdPartyAccountTestDB(t)
	s := newThirdPartyAccountStore(db)
	ctx := context.Background()
	cph := testCipher(t)

	// store 持久化的是密文（Enc 字段）。biz/store 边界负责加密 → 此处模拟边界。
	secretEnc, err := cph.Encrypt([]byte("app-secret-plain"))
	require.NoError(t, err)
	accessEnc, err := cph.Encrypt([]byte("user-access-token-plain"))
	require.NoError(t, err)
	refreshEnc, err := cph.Encrypt([]byte("refresh-token-plain"))
	require.NoError(t, err)

	exp := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	acc := &model.UserThirdPartyAccount{
		UserID:          7,
		Provider:        "lark",
		AppID:           "cli_app_001",
		AppSecretEnc:    secretEnc,
		AccessTokenEnc:  accessEnc,
		RefreshTokenEnc: refreshEnc,
		TokenExpiresAt:  &exp,
		Scopes:          "docx:document im:message bitable:app:readonly",
	}
	require.NoError(t, s.Upsert(ctx, acc))
	assert.NotZero(t, acc.ID, "Upsert 应回填自增 ID")

	got, err := s.Get(ctx, 7, "lark")
	require.NoError(t, err)
	assert.Equal(t, "cli_app_001", got.AppID)
	assert.Equal(t, "docx:document im:message bitable:app:readonly", got.Scopes)
	require.NotNil(t, got.TokenExpiresAt)
	assert.WithinDuration(t, exp, got.TokenExpiresAt.UTC(), time.Second)

	// 加密往返：读出的密文用 crypto 解密应得原文。
	gotSecret, err := cph.Decrypt(got.AppSecretEnc)
	require.NoError(t, err)
	assert.Equal(t, "app-secret-plain", string(gotSecret))
	gotAccess, err := cph.Decrypt(got.AccessTokenEnc)
	require.NoError(t, err)
	assert.Equal(t, "user-access-token-plain", string(gotAccess))
	gotRefresh, err := cph.Decrypt(got.RefreshTokenEnc)
	require.NoError(t, err)
	assert.Equal(t, "refresh-token-plain", string(gotRefresh))
}

func TestThirdPartyAccountStore_RetireGenerationFencesOldWorkAndFinalizesLocalState(t *testing.T) {
	db := newThirdPartyAccountTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.FeishuAuthSession{}, &model.FeishuOperation{}))
	s := newThirdPartyAccountStore(db)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.UserThirdPartyAccount{
		UserID: 7, Provider: "lark", AppID: "cli_local", Generation: 4, Connected: true,
		AppSecretEnc: []byte("legacy-app-secret"), AccessTokenEnc: []byte("legacy-access"),
		RefreshTokenEnc: []byte("legacy-refresh"), TokenExpiresAt: &now, Scopes: "legacy:scope",
		ConnectionState: model.FeishuConnectionConnected, CapabilityStateJSON: []byte(`{"docs":{"state":"available"}}`),
	}).Error)
	require.NoError(t, db.Create(&model.FeishuAuthSession{
		ID: "session-old", UserID: 7, Generation: 4, Phase: model.FeishuAuthPhaseUserAuth,
		RequestedScopesJSON: []byte(`[]`), State: model.FeishuAuthSessionPending, ExpiresAt: now.Add(time.Minute),
	}).Error)
	require.NoError(t, db.Create(&model.FeishuOperation{
		ID: "op-executing", UserID: 7, Generation: 4, AgentRunID: 1, ToolCallID: "tool-1",
		IdempotencyKey: "key-1", CommandPath: "docs +create", Domain: "docs", RiskLevel: "write",
		RequestCiphertext: []byte("cipher"), KeyVersion: "v1", RequestFingerprint: "fingerprint", State: model.FeishuOperationExecuting,
	}).Error)

	oldGeneration, newGeneration, err := s.RetireGeneration(ctx, 7, "lark")
	require.NoError(t, err)
	require.EqualValues(t, 4, oldGeneration)
	require.EqualValues(t, 5, newGeneration)

	var oldSession model.FeishuAuthSession
	require.NoError(t, db.Where("id = ?", "session-old").Take(&oldSession).Error)
	require.Equal(t, model.FeishuAuthSessionSuperseded, oldSession.State)
	var oldOperation model.FeishuOperation
	require.NoError(t, db.Where("id = ?", "op-executing").Take(&oldOperation).Error)
	require.Equal(t, model.FeishuOperationUnknown, oldOperation.State)

	// A retry after vault cleanup failure must continue cleaning the same retired
	// generation, never advance again and strand its ciphertext permanently.
	retryOld, retryNew, err := s.RetireGeneration(ctx, 7, "lark")
	require.NoError(t, err)
	require.EqualValues(t, 4, retryOld)
	require.EqualValues(t, 5, retryNew)

	require.NoError(t, s.FinalizeDisconnect(ctx, 7, "lark", newGeneration))
	account, err := s.Get(ctx, 7, "lark")
	require.NoError(t, err)
	require.Equal(t, model.FeishuConnectionNone, account.ConnectionState)
	require.False(t, account.Connected)
	require.Empty(t, account.AppID)
	require.Empty(t, account.CapabilityStateJSON)
	require.Empty(t, account.AppSecretEnc)
	require.Empty(t, account.AccessTokenEnc)
	require.Empty(t, account.RefreshTokenEnc)
	require.Nil(t, account.TokenExpiresAt)
	require.Empty(t, account.Scopes)
}

func TestThirdPartyAccountStore_Upsert_Idempotent(t *testing.T) {
	db := newThirdPartyAccountTestDB(t)
	s := newThirdPartyAccountStore(db)
	ctx := context.Background()

	first := &model.UserThirdPartyAccount{
		UserID: 7, Provider: "lark", AppID: "cli_app_001",
		AppSecretEnc: []byte("e1"), AccessTokenEnc: []byte("a1"), Scopes: "docx:document",
	}
	require.NoError(t, s.Upsert(ctx, first))
	firstID := first.ID
	require.NotZero(t, firstID)

	// 同 (user, provider) 二次 Upsert = 更新（幂等），不应新建行。
	second := &model.UserThirdPartyAccount{
		UserID: 7, Provider: "lark", AppID: "cli_app_002",
		AppSecretEnc: []byte("e2"), AccessTokenEnc: []byte("a2"), Scopes: "docx:document im:message",
	}
	require.NoError(t, s.Upsert(ctx, second))

	// 只有一行
	var count int64
	require.NoError(t, db.Model(&model.UserThirdPartyAccount{}).
		Where("user_id = ? AND provider = ?", 7, "lark").Count(&count).Error)
	assert.EqualValues(t, 1, count, "重复授权应 UPSERT 更新，不新建行")

	got, err := s.Get(ctx, 7, "lark")
	require.NoError(t, err)
	assert.Equal(t, "cli_app_002", got.AppID, "Upsert 应更新 AppID")
	assert.Equal(t, "docx:document im:message", got.Scopes, "Upsert 应更新 Scopes")
	assert.Equal(t, []byte("a2"), got.AccessTokenEnc, "Upsert 应更新 access token 密文")
}

func TestThirdPartyAccountStore_Get_Miss(t *testing.T) {
	db := newThirdPartyAccountTestDB(t)
	s := newThirdPartyAccountStore(db)
	ctx := context.Background()

	_, err := s.Get(ctx, 7, "lark")
	require.Error(t, err)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound),
		"未连接应返回 gorm.ErrRecordNotFound 供 biz 走未连接分支")
}

func TestThirdPartyAccountStore_EnsurePlaceholderNeverOverwritesExistingAccount(t *testing.T) {
	db := newThirdPartyAccountTestDB(t)
	s := newThirdPartyAccountStore(db)
	ctx := context.Background()

	placeholder, err := s.EnsurePlaceholder(ctx, 7, "lark")
	require.NoError(t, err)
	require.Equal(t, model.FeishuConnectionNone, placeholder.ConnectionState)
	require.EqualValues(t, 1, placeholder.Generation)
	require.Empty(t, placeholder.AppID)

	require.NoError(t, db.Model(&model.UserThirdPartyAccount{}).
		Where("user_id = ? AND provider = ?", 7, "lark").
		Updates(map[string]any{
			"app_id": "cli_keep", "connection_state": model.FeishuConnectionConnected,
			"connected": true, "generation": 8,
		}).Error)

	const workers = 20
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ensureErr := s.EnsurePlaceholder(ctx, 7, "lark")
			errs <- ensureErr
		}()
	}
	wg.Wait()
	close(errs)
	for ensureErr := range errs {
		require.NoError(t, ensureErr)
	}

	got, err := s.Get(ctx, 7, "lark")
	require.NoError(t, err)
	require.Equal(t, "cli_keep", got.AppID)
	require.Equal(t, model.FeishuConnectionConnected, got.ConnectionState)
	require.True(t, got.Connected)
	require.EqualValues(t, 8, got.Generation)
	var count int64
	require.NoError(t, db.Model(&model.UserThirdPartyAccount{}).
		Where("user_id = ? AND provider = ?", 7, "lark").Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestThirdPartyAccountStore_Get_IsolatedByUserAndProvider(t *testing.T) {
	db := newThirdPartyAccountTestDB(t)
	s := newThirdPartyAccountStore(db)
	ctx := context.Background()

	require.NoError(t, s.Upsert(ctx, &model.UserThirdPartyAccount{
		UserID: 7, Provider: "lark", AppID: "a", AccessTokenEnc: []byte("x"),
	}))

	// 不同 user 不命中
	_, err := s.Get(ctx, 8, "lark")
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound), "跨用户应隔离")

	// 不同 provider 不命中
	_, err = s.Get(ctx, 7, "dingtalk")
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound), "跨 provider 应隔离")
}

func TestThirdPartyAccountStore_Delete(t *testing.T) {
	db := newThirdPartyAccountTestDB(t)
	s := newThirdPartyAccountStore(db)
	ctx := context.Background()

	require.NoError(t, s.Upsert(ctx, &model.UserThirdPartyAccount{
		UserID: 7, Provider: "lark", AppID: "a", AccessTokenEnc: []byte("x"),
	}))

	require.NoError(t, s.Delete(ctx, 7, "lark"))

	_, err := s.Get(ctx, 7, "lark")
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound), "Delete 后应查不到")
}

func TestThirdPartyAccountStore_Delete_NotFound(t *testing.T) {
	db := newThirdPartyAccountTestDB(t)
	s := newThirdPartyAccountStore(db)
	ctx := context.Background()

	// 解绑幂等：删不存在的连接不报错（design §5 callback/解绑幂等心智）。
	err := s.Delete(ctx, 7, "lark")
	assert.NoError(t, err, "解绑不存在的连接应幂等无错")
}

func TestThirdPartyAccountStore_UpdateTokens(t *testing.T) {
	db := newThirdPartyAccountTestDB(t)
	s := newThirdPartyAccountStore(db)
	ctx := context.Background()

	require.NoError(t, s.Upsert(ctx, &model.UserThirdPartyAccount{
		UserID: 7, Provider: "lark", AppID: "a",
		AccessTokenEnc: []byte("old-access"), RefreshTokenEnc: []byte("old-refresh"),
	}))

	newExp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	require.NoError(t, s.UpdateTokens(ctx, 7, "lark",
		[]byte("new-access"), []byte("new-refresh"), &newExp))

	got, err := s.Get(ctx, 7, "lark")
	require.NoError(t, err)
	assert.Equal(t, []byte("new-access"), got.AccessTokenEnc)
	assert.Equal(t, []byte("new-refresh"), got.RefreshTokenEnc)
	require.NotNil(t, got.TokenExpiresAt)
	assert.WithinDuration(t, newExp, got.TokenExpiresAt.UTC(), time.Second)
	assert.Equal(t, "a", got.AppID, "UpdateTokens 不应改 AppID")
}

func TestThirdPartyAccountStore_UpdateTokens_NotFound(t *testing.T) {
	db := newThirdPartyAccountTestDB(t)
	s := newThirdPartyAccountStore(db)
	ctx := context.Background()

	err := s.UpdateTokens(ctx, 7, "lark", []byte("a"), []byte("r"), nil)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound),
		"刷新不存在的连接应返回 ErrRecordNotFound 而非静默成功")
}

func TestThirdPartyAccountStore_UpdateTokens_NilRefreshAndExp(t *testing.T) {
	db := newThirdPartyAccountTestDB(t)
	s := newThirdPartyAccountStore(db)
	ctx := context.Background()

	require.NoError(t, s.Upsert(ctx, &model.UserThirdPartyAccount{
		UserID: 7, Provider: "lark", AppID: "a", AccessTokenEnc: []byte("old"),
	}))

	// 飞书未返回 refresh_token / 过期时间时，传 nil 应写入 NULL（指针字段不被误判过期）。
	require.NoError(t, s.UpdateTokens(ctx, 7, "lark", []byte("new"), nil, nil))

	got, err := s.Get(ctx, 7, "lark")
	require.NoError(t, err)
	assert.Equal(t, []byte("new"), got.AccessTokenEnc)
	assert.Nil(t, got.TokenExpiresAt, "传 nil exp 应写入 NULL")
}

// MarkConnected：已存在的行被置 connected=true + connected_at；不存在则 ErrRecordNotFound。
func TestThirdPartyAccountStore_MarkConnected(t *testing.T) {
	db := newThirdPartyAccountTestDB(t)
	s := newThirdPartyAccountStore(db)
	ctx := context.Background()

	// 目标不存在 → ErrRecordNotFound（避免静默 no-op）。
	at := time.Now().UTC().Truncate(time.Second)
	err := s.MarkConnected(ctx, 7, "lark", at)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound, "目标不存在应返回 ErrRecordNotFound")

	// 先建一行（仅 app 元信息，未连接）。
	require.NoError(t, s.Upsert(ctx, &model.UserThirdPartyAccount{
		UserID: 7, Provider: "lark", AppID: "cli_app_x",
	}))
	pre, err := s.Get(ctx, 7, "lark")
	require.NoError(t, err)
	assert.False(t, pre.Connected, "初始应未连接")
	assert.Nil(t, pre.ConnectedAt)

	// 标记连接。
	require.NoError(t, s.MarkConnected(ctx, 7, "lark", at))
	got, err := s.Get(ctx, 7, "lark")
	require.NoError(t, err)
	assert.True(t, got.Connected, "MarkConnected 后 connected 应为 true")
	require.NotNil(t, got.ConnectedAt)
	assert.WithinDuration(t, at, *got.ConnectedAt, time.Second)
}

// 验证 datastore.ThirdPartyAccounts() 返回的实例可正常工作（IStore 扩展接通）。
func TestDatastore_ThirdPartyAccounts(t *testing.T) {
	db := newThirdPartyAccountTestDB(t)
	ds := NewTestStore(db)
	ctx := context.Background()

	require.NoError(t, ds.ThirdPartyAccounts().Upsert(ctx, &model.UserThirdPartyAccount{
		UserID: 7, Provider: "lark", AppID: "a", AccessTokenEnc: []byte("x"),
	}))
	got, err := ds.ThirdPartyAccounts().Get(ctx, 7, "lark")
	require.NoError(t, err)
	assert.Equal(t, "a", got.AppID)
}
