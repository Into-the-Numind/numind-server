package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
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

func newXhsScriptConcurrentStoreTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", dbName)), &gorm.Config{
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
	sqlDB.SetMaxOpenConns(1)
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
	require.NoError(t, s.AddPaidQuota(ctx, 12, 2, "order-9001"))

	for _, refID := range []string{"gen-1", "gen-2", "gen-3", "gen-4", "gen-5"} {
		require.NoError(t, s.DeductOneGeneration(ctx, 12, refID))
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
	for _, refID := range []string{"gen-1", "gen-2", "gen-3"} {
		require.NoError(t, s.DeductOneGeneration(ctx, 13, refID))
	}

	err = s.DeductOneGeneration(ctx, 13, "gen-4")
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

func TestXhsScriptStore_UpsertCapturedNoteUpdatesSameRow(t *testing.T) {
	db := newXhsScriptStoreTestDB(t)
	s := NewXhsScriptStore(db)
	ctx := context.Background()

	first, err := s.CreateOrUpsertCapturedNote(ctx, newXhsScriptNote(15, "source-same"))
	require.NoError(t, err)

	recapture := newXhsScriptNote(15, "source-same")
	recapture.Title = "更新后的标题"
	got, err := s.CreateOrUpsertCapturedNote(ctx, recapture)
	require.NoError(t, err)
	assert.Equal(t, first.ID, got.ID)

	var count int64
	require.NoError(t, db.Model(&model.XhsScriptNote{}).Where("user_id = ? AND source_note_id = ?", 15, "source-same").Count(&count).Error)
	assert.EqualValues(t, 1, count)
	assert.Equal(t, "更新后的标题", got.Title)
}

func TestXhsScriptStore_RecapturePreservesInternalProcessingFields(t *testing.T) {
	s := NewXhsScriptStore(newXhsScriptStoreTestDB(t))
	ctx := context.Background()

	transcript := "已经完成的转写"
	note := newXhsScriptNote(16, "source-ready")
	note.VideoTranscript = &transcript
	note.TranscribeStatus = model.XhsScriptTranscribeReady
	note.GenerateStatus = model.XhsScriptGenerateGenerated
	note.LastError = "historic error"
	created, err := s.CreateOrUpsertCapturedNote(ctx, note)
	require.NoError(t, err)

	incomingTranscript := ""
	recapture := newXhsScriptNote(16, "source-ready")
	recapture.Title = "新版采集标题"
	recapture.VideoTranscript = &incomingTranscript
	recapture.TranscribeStatus = model.XhsScriptTranscribePending
	recapture.GenerateStatus = model.XhsScriptGenerateNotReady
	recapture.LastError = ""
	updated, err := s.CreateOrUpsertCapturedNote(ctx, recapture)
	require.NoError(t, err)

	assert.Equal(t, created.ID, updated.ID)
	assert.Equal(t, "新版采集标题", updated.Title)
	require.NotNil(t, updated.VideoTranscript)
	assert.Equal(t, transcript, *updated.VideoTranscript)
	assert.Equal(t, model.XhsScriptTranscribeReady, updated.TranscribeStatus)
	assert.Equal(t, model.XhsScriptGenerateGenerated, updated.GenerateStatus)
	assert.Equal(t, "historic error", updated.LastError)
}

func TestXhsScriptStore_DuplicateAddPaidQuotaIsIdempotent(t *testing.T) {
	db := newXhsScriptStoreTestDB(t)
	s := NewXhsScriptStore(db)
	ctx := context.Background()

	require.NoError(t, s.AddPaidQuota(ctx, 17, 5, "order-dup"))
	require.NoError(t, s.AddPaidQuota(ctx, 17, 5, "order-dup"))

	account, err := s.GetQuotaAccount(ctx, 17)
	require.NoError(t, err)
	assert.EqualValues(t, 5, account.PaidRemaining)

	var count int64
	require.NoError(t, db.Model(&model.XhsScriptQuotaLedger{}).
		Where("user_id = ? AND reason = ? AND ref_type = ? AND ref_id = ?", 17, model.XhsScriptLedgerReasonPurchase, model.XhsScriptLedgerRefTypePurchase, "order-dup").
		Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestXhsScriptStore_AddPaidQuotaEmptyRefDoesNotChangeBalance(t *testing.T) {
	s := NewXhsScriptStore(newXhsScriptStoreTestDB(t))
	ctx := context.Background()

	_, err := s.CreateOrGetQuotaAccount(ctx, 20)
	require.NoError(t, err)

	err = s.AddPaidQuota(ctx, 20, 5, "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrXhsScriptQuotaRefRequired))

	account, err := s.GetQuotaAccount(ctx, 20)
	require.NoError(t, err)
	assert.EqualValues(t, 0, account.PaidRemaining)
}

func TestXhsScriptStore_DuplicateDeductOneGenerationIsIdempotent(t *testing.T) {
	db := newXhsScriptStoreTestDB(t)
	s := NewXhsScriptStore(db)
	ctx := context.Background()

	_, err := s.CreateOrGetQuotaAccount(ctx, 18)
	require.NoError(t, err)
	require.NoError(t, s.DeductOneGeneration(ctx, 18, "gen-dup"))
	require.NoError(t, s.DeductOneGeneration(ctx, 18, "gen-dup"))

	account, err := s.GetQuotaAccount(ctx, 18)
	require.NoError(t, err)
	assert.EqualValues(t, 2, account.FreeRemaining)

	var count int64
	require.NoError(t, db.Model(&model.XhsScriptQuotaLedger{}).
		Where("user_id = ? AND reason = ? AND ref_type = ? AND ref_id = ?", 18, model.XhsScriptLedgerReasonGeneration, model.XhsScriptLedgerRefTypeGeneration, "gen-dup").
		Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestXhsScriptStore_DeductOneGenerationEmptyRefDoesNotChangeBalance(t *testing.T) {
	s := NewXhsScriptStore(newXhsScriptStoreTestDB(t))
	ctx := context.Background()

	_, err := s.CreateOrGetQuotaAccount(ctx, 21)
	require.NoError(t, err)

	err = s.DeductOneGeneration(ctx, 21, "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrXhsScriptQuotaRefRequired))

	account, err := s.GetQuotaAccount(ctx, 21)
	require.NoError(t, err)
	assert.EqualValues(t, 3, account.FreeRemaining)
}

func TestXhsScriptStore_ConcurrentDuplicateDeductOneGenerationAppliesOnce(t *testing.T) {
	db := newXhsScriptConcurrentStoreTestDB(t)
	s := NewXhsScriptStore(db)
	ctx := context.Background()

	_, err := s.CreateOrGetQuotaAccount(ctx, 22)
	require.NoError(t, err)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.DeductOneGeneration(ctx, 22, "gen-concurrent")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	account, err := s.GetQuotaAccount(ctx, 22)
	require.NoError(t, err)
	assert.EqualValues(t, 2, account.FreeRemaining)

	var count int64
	require.NoError(t, db.Model(&model.XhsScriptQuotaLedger{}).
		Where("user_id = ? AND reason = ? AND ref_type = ? AND ref_id = ?", 22, model.XhsScriptLedgerReasonGeneration, model.XhsScriptLedgerRefTypeGeneration, "gen-concurrent").
		Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestXhsScriptStore_StatusUpdatesAreTenantSafe(t *testing.T) {
	s := NewXhsScriptStore(newXhsScriptStoreTestDB(t))
	ctx := context.Background()

	note, err := s.CreateOrUpsertCapturedNote(ctx, newXhsScriptNote(19, "source-tenant"))
	require.NoError(t, err)

	wrongTranscript := "wrong user transcript"
	require.NoError(t, s.UpdateTranscribeStatus(ctx, 20, note.ID, model.XhsScriptTranscribeReady, &wrongTranscript, "wrong user"))
	require.NoError(t, s.UpdateGenerateStatus(ctx, 20, note.ID, model.XhsScriptGenerateGenerated, "wrong user"))

	got, err := s.GetNote(ctx, 19, note.ID)
	require.NoError(t, err)
	assert.Nil(t, got.VideoTranscript)
	assert.Equal(t, model.XhsScriptTranscribePending, got.TranscribeStatus)
	assert.Equal(t, model.XhsScriptGenerateNotReady, got.GenerateStatus)
	assert.Empty(t, got.LastError)

	ownerTranscript := "owner transcript"
	require.NoError(t, s.UpdateTranscribeStatus(ctx, 19, note.ID, model.XhsScriptTranscribeReady, &ownerTranscript, ""))
	require.NoError(t, s.UpdateGenerateStatus(ctx, 19, note.ID, model.XhsScriptGenerateGenerated, ""))

	got, err = s.GetNote(ctx, 19, note.ID)
	require.NoError(t, err)
	require.NotNil(t, got.VideoTranscript)
	assert.Equal(t, ownerTranscript, *got.VideoTranscript)
	assert.Equal(t, model.XhsScriptTranscribeReady, got.TranscribeStatus)
	assert.Equal(t, model.XhsScriptGenerateGenerated, got.GenerateStatus)
}
