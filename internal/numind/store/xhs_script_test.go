package store

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/pkg/model"
)

func newXhsScriptStoreTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.XhsScriptUserProfile{},
		&model.XhsScriptQuotaAccount{},
		&model.XhsScriptNote{},
		&model.XhsScriptGeneration{},
		&model.XhsScriptQuotaLedger{},
		&model.XhsScriptAnalyticsEvent{},
	), "auto-migrate")

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func newXhsScriptNote(userID uint, sourceNoteID string) *model.XhsScriptNote {
	return &model.XhsScriptNote{
		UserID:           userID,
		SourceNoteID:     sourceNoteID,
		NoteType:         model.XhsScriptNoteTypeVideo,
		Title:            "口播视频",
		Description:      "适合仿写的视频笔记",
		Tags:             datatypes.JSON([]byte(`["口播"]`)),
		TranscribeStatus: model.XhsScriptTranscribePending,
		GenerateStatus:   model.XhsScriptGenerateNotReady,
	}
}

func TestXhsScriptStore_DefaultQuotaIsThreeFree(t *testing.T) {
	s := NewXhsScriptStore(newXhsScriptStoreTestDB(t))
	ctx := context.Background()

	account, err := s.CreateOrGetQuotaAccount(ctx, 11)
	require.NoError(t, err)
	assert.Equal(t, uint(11), account.UserID)
	assert.EqualValues(t, 3, account.FreeRemaining)
	assert.EqualValues(t, 0, account.PaidRemaining)

	got, err := s.GetQuotaAccount(ctx, 11)
	require.NoError(t, err)
	assert.EqualValues(t, 3, got.FreeRemaining)
}

func TestXhsScriptStore_DeductConsumesFreeBeforePaidAndWritesLedger(t *testing.T) {
	db := newXhsScriptStoreTestDB(t)
	s := NewXhsScriptStore(db)
	ctx := context.Background()

	_, err := s.CreateOrGetQuotaAccount(ctx, 12)
	require.NoError(t, err)
	require.NoError(t, s.AddPaidQuota(ctx, 12, 2, 9001))

	for i := uint64(1); i <= 5; i++ {
		require.NoError(t, s.DeductOneGeneration(ctx, 12, i))
	}

	account, err := s.GetQuotaAccount(ctx, 12)
	require.NoError(t, err)
	assert.EqualValues(t, 0, account.FreeRemaining)
	assert.EqualValues(t, 0, account.PaidRemaining)

	var ledgers []model.XhsScriptQuotaLedger
	require.NoError(t, db.Order("id ASC").Find(&ledgers, "user_id = ?", 12).Error)
	require.Len(t, ledgers, 6, "one purchase ledger plus five generation ledgers")
	assert.Equal(t, model.XhsScriptQuotaBucketPaid, ledgers[0].Bucket)
	assert.EqualValues(t, 2, ledgers[0].Delta)
	assert.Equal(t, model.XhsScriptQuotaBucketFree, ledgers[1].Bucket)
	assert.Equal(t, model.XhsScriptQuotaBucketFree, ledgers[2].Bucket)
	assert.Equal(t, model.XhsScriptQuotaBucketFree, ledgers[3].Bucket)
	assert.Equal(t, model.XhsScriptQuotaBucketPaid, ledgers[4].Bucket)
	assert.Equal(t, model.XhsScriptQuotaBucketPaid, ledgers[5].Bucket)
	for _, ledger := range ledgers[1:] {
		assert.EqualValues(t, -1, ledger.Delta)
		assert.Equal(t, model.XhsScriptLedgerReasonGeneration, ledger.Reason)
		assert.Equal(t, model.XhsScriptLedgerRefTypeGeneration, ledger.RefType)
	}
}

func TestXhsScriptStore_DeductInsufficientQuotaDoesNotGoNegative(t *testing.T) {
	s := NewXhsScriptStore(newXhsScriptStoreTestDB(t))
	ctx := context.Background()

	_, err := s.CreateOrGetQuotaAccount(ctx, 13)
	require.NoError(t, err)
	for i := uint64(1); i <= 3; i++ {
		require.NoError(t, s.DeductOneGeneration(ctx, 13, i))
	}

	err = s.DeductOneGeneration(ctx, 13, 4)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrXhsScriptQuotaInsufficient))

	account, err := s.GetQuotaAccount(ctx, 13)
	require.NoError(t, err)
	assert.EqualValues(t, 0, account.FreeRemaining)
	assert.EqualValues(t, 0, account.PaidRemaining)
}

func TestXhsScriptStore_DuplicateAnalyticsEventIsIdempotent(t *testing.T) {
	db := newXhsScriptStoreTestDB(t)
	s := NewXhsScriptStore(db)
	ctx := context.Background()

	event := &model.XhsScriptAnalyticsEvent{
		EventID:     "evt_same",
		EventName:   "open_page",
		AnonymousID: "anon",
		Path:        "/xhs/script",
	}
	require.NoError(t, s.InsertAnalyticsEvent(ctx, event))
	require.NoError(t, s.InsertAnalyticsEvent(ctx, event))

	var count int64
	require.NoError(t, db.Model(&model.XhsScriptAnalyticsEvent{}).Where("event_id = ?", "evt_same").Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestXhsScriptStore_CreateGenerationIncrementsVersionsPerNote(t *testing.T) {
	s := NewXhsScriptStore(newXhsScriptStoreTestDB(t))
	ctx := context.Background()

	noteA, err := s.CreateOrUpsertCapturedNote(ctx, newXhsScriptNote(14, "source-a"))
	require.NoError(t, err)
	noteB, err := s.CreateOrUpsertCapturedNote(ctx, newXhsScriptNote(14, "source-b"))
	require.NoError(t, err)

	gen1, err := s.CreateGeneration(ctx, 14, noteA.ID, "第一版", 10, 20)
	require.NoError(t, err)
	gen2, err := s.CreateGeneration(ctx, 14, noteA.ID, "第二版", 11, 21)
	require.NoError(t, err)
	genOther, err := s.CreateGeneration(ctx, 14, noteB.ID, "另一条笔记第一版", 12, 22)
	require.NoError(t, err)

	assert.Equal(t, 1, gen1.Version)
	assert.Equal(t, 2, gen2.Version)
	assert.Equal(t, 1, genOther.Version)
}
