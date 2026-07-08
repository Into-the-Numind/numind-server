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
		&model.XhsScriptTrialClaim{},
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
		&model.XhsScriptTrialClaim{},
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

func seedXhsScriptFreeQuota(t *testing.T, db *gorm.DB, userID uint, amount int64) {
	t.Helper()
	require.NoError(t, db.Create(&model.XhsScriptQuotaAccount{
		UserID:        userID,
		FreeRemaining: amount,
		PaidRemaining: 0,
	}).Error)
}

func TestXhsScriptStore_DefaultQuotaIsZeroUntilRegistrationGrant(t *testing.T) {
	s := NewXhsScriptStore(newXhsScriptStoreTestDB(t))
	ctx := context.Background()

	account, err := s.CreateOrGetQuotaAccount(ctx, 11)
	require.NoError(t, err)
	assert.Equal(t, uint(11), account.UserID)
	assert.EqualValues(t, 0, account.FreeRemaining)
	assert.EqualValues(t, 0, account.PaidRemaining)

	got, err := s.GetQuotaAccount(ctx, 11)
	require.NoError(t, err)
	assert.EqualValues(t, 0, got.FreeRemaining)
}

func TestXhsScriptStore_DeductConsumesFreeBeforePaidAndWritesLedger(t *testing.T) {
	db := newXhsScriptStoreTestDB(t)
	s := NewXhsScriptStore(db)
	ctx := context.Background()

	seedXhsScriptFreeQuota(t, db, 12, 3)
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
	db := newXhsScriptStoreTestDB(t)
	s := NewXhsScriptStore(db)
	ctx := context.Background()

	seedXhsScriptFreeQuota(t, db, 13, 3)
	for _, refID := range []string{"gen-1", "gen-2", "gen-3"} {
		require.NoError(t, s.DeductOneGeneration(ctx, 13, refID))
	}

	err := s.DeductOneGeneration(ctx, 13, "gen-4")
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

func TestXhsScriptStore_CreateGenerationAndDeductQuotaCommitsTogether(t *testing.T) {
	db := newXhsScriptStoreTestDB(t)
	s := NewXhsScriptStore(db)
	ctx := context.Background()

	seedXhsScriptFreeQuota(t, db, 24, 3)
	note := newXhsScriptNote(24, "source-commit")
	note.TranscribeStatus = model.XhsScriptTranscribeReady
	note.GenerateStatus = model.XhsScriptGenerateGenerating
	created, err := s.CreateOrUpsertCapturedNote(ctx, note)
	require.NoError(t, err)

	commit, err := s.CreateGenerationAndDeductQuota(ctx, 24, created.ID, "原子提交口播稿", 31, 42)
	require.NoError(t, err)
	require.NotNil(t, commit.Generation)
	assert.Equal(t, model.XhsScriptQuotaBucketFree, commit.Bucket)
	assert.EqualValues(t, 3, commit.FreeBefore)
	assert.EqualValues(t, 0, commit.PaidBefore)
	assert.Equal(t, 1, commit.Generation.Version)

	account, err := s.GetQuotaAccount(ctx, 24)
	require.NoError(t, err)
	assert.EqualValues(t, 2, account.FreeRemaining)

	var ledger model.XhsScriptQuotaLedger
	require.NoError(t, db.Where("user_id = ? AND reason = ?", 24, model.XhsScriptLedgerReasonGeneration).First(&ledger).Error)
	assert.EqualValues(t, -1, ledger.Delta)
	assert.Equal(t, fmt.Sprintf("%d", commit.Generation.ID), ledger.RefID)

	got, err := s.GetNote(ctx, 24, created.ID)
	require.NoError(t, err)
	assert.Equal(t, model.XhsScriptGenerateGenerated, got.GenerateStatus)
	assert.Empty(t, got.LastError)
}

func TestXhsScriptStore_CreateGenerationAndDeductQuotaRollsBackWhenQuotaInsufficient(t *testing.T) {
	db := newXhsScriptStoreTestDB(t)
	s := NewXhsScriptStore(db)
	ctx := context.Background()

	seedXhsScriptFreeQuota(t, db, 25, 3)
	for _, refID := range []string{"gen-used-1", "gen-used-2", "gen-used-3"} {
		require.NoError(t, s.DeductOneGeneration(ctx, 25, refID))
	}
	note := newXhsScriptNote(25, "source-no-quota")
	note.TranscribeStatus = model.XhsScriptTranscribeReady
	note.GenerateStatus = model.XhsScriptGenerateGenerating
	created, err := s.CreateOrUpsertCapturedNote(ctx, note)
	require.NoError(t, err)

	commit, err := s.CreateGenerationAndDeductQuota(ctx, 25, created.ID, "不应该保存", 31, 42)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrXhsScriptQuotaInsufficient))
	assert.Nil(t, commit)

	var generations int64
	require.NoError(t, db.Model(&model.XhsScriptGeneration{}).Where("user_id = ? AND note_id = ?", 25, created.ID).Count(&generations).Error)
	assert.EqualValues(t, 0, generations)

	account, err := s.GetQuotaAccount(ctx, 25)
	require.NoError(t, err)
	assert.EqualValues(t, 0, account.FreeRemaining)
	assert.EqualValues(t, 0, account.PaidRemaining)
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

func TestXhsScriptStoreRecapturePreservesMirroredVideoURL(t *testing.T) {
	db := newXhsScriptConcurrentStoreTestDB(t)
	s := NewXhsScriptStore(db)
	ctx := context.Background()

	note, err := s.CreateOrUpsertCapturedNote(ctx, newXhsScriptNote(15, "source-mirrored-video"))
	require.NoError(t, err)
	cosURL := fmt.Sprintf("https://bucket.cos.ap-beijing.myqcloud.com/xhs-script-media/15/%d/video.mp4", note.ID)
	require.NoError(t, s.UpdateNoteVideoURL(ctx, 15, note.ID, cosURL))
	assert.True(t, isXhsScriptMirroredVideoURL(cosURL, 15, note.ID))

	recapture := newXhsScriptNote(15, "source-mirrored-video")
	recapture.VideoURL = "https://sns-video.xhscdn.com/new.mp4"
	updated, err := s.CreateOrUpsertCapturedNote(ctx, recapture)
	require.NoError(t, err)
	assert.Equal(t, cosURL, updated.VideoURL)

	rawNote := newXhsScriptNote(15, "source-raw-video")
	rawNote.VideoURL = "https://sns-video.xhscdn.com/old.mp4"
	createdRaw, err := s.CreateOrUpsertCapturedNote(ctx, rawNote)
	require.NoError(t, err)
	rawRecapture := newXhsScriptNote(15, "source-raw-video")
	rawRecapture.VideoURL = "https://sns-video.xhscdn.com/new-raw.mp4"
	updatedRaw, err := s.CreateOrUpsertCapturedNote(ctx, rawRecapture)
	require.NoError(t, err)
	assert.Equal(t, createdRaw.ID, updatedRaw.ID)
	assert.Equal(t, rawRecapture.VideoURL, updatedRaw.VideoURL)

	evilHostNote, err := s.CreateOrUpsertCapturedNote(ctx, newXhsScriptNote(15, "source-evil-host-video"))
	require.NoError(t, err)
	evilHostURL := fmt.Sprintf("https://evil.test/xhs-script-media/15/%d/video.mp4", evilHostNote.ID)
	require.NoError(t, s.UpdateNoteVideoURL(ctx, 15, evilHostNote.ID, evilHostURL))
	gotEvilHost, err := s.GetNote(ctx, 15, evilHostNote.ID)
	require.NoError(t, err)
	assert.Equal(t, evilHostURL, gotEvilHost.VideoURL)
	assert.False(t, isXhsScriptMirroredVideoURL(evilHostURL, 15, evilHostNote.ID))
	evilHostRecapture := newXhsScriptNote(15, "source-evil-host-video")
	evilHostRecapture.VideoURL = "https://sns-video.xhscdn.com/evil-host-new.mp4"
	updatedEvilHost, err := s.CreateOrUpsertCapturedNote(ctx, evilHostRecapture)
	require.NoError(t, err)
	assert.Equal(t, evilHostNote.ID, updatedEvilHost.ID)
	assert.Equal(t, evilHostRecapture.VideoURL, updatedEvilHost.VideoURL)

	mismatchedKeyNote, err := s.CreateOrUpsertCapturedNote(ctx, newXhsScriptNote(15, "source-mismatched-key-video"))
	require.NoError(t, err)
	mismatchedKeyURL := fmt.Sprintf("https://bucket.cos.ap-beijing.myqcloud.com/xhs-script-media/14/%d/video.mp4", mismatchedKeyNote.ID)
	require.NoError(t, s.UpdateNoteVideoURL(ctx, 15, mismatchedKeyNote.ID, mismatchedKeyURL))
	gotMismatchedKey, err := s.GetNote(ctx, 15, mismatchedKeyNote.ID)
	require.NoError(t, err)
	assert.Equal(t, mismatchedKeyURL, gotMismatchedKey.VideoURL)
	assert.False(t, isXhsScriptMirroredVideoURL(mismatchedKeyURL, 15, mismatchedKeyNote.ID))
	mismatchedKeyRecapture := newXhsScriptNote(15, "source-mismatched-key-video")
	mismatchedKeyRecapture.VideoURL = "https://sns-video.xhscdn.com/mismatched-key-new.mp4"
	updatedMismatchedKey, err := s.CreateOrUpsertCapturedNote(ctx, mismatchedKeyRecapture)
	require.NoError(t, err)
	assert.Equal(t, mismatchedKeyNote.ID, updatedMismatchedKey.ID)
	assert.Equal(t, mismatchedKeyRecapture.VideoURL, updatedMismatchedKey.VideoURL)
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

	seedXhsScriptFreeQuota(t, db, 18, 3)
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

func TestXhsScriptStore_DuplicateDeductRemainsIdempotentAfterBalanceExhausted(t *testing.T) {
	db := newXhsScriptStoreTestDB(t)
	s := NewXhsScriptStore(db)
	ctx := context.Background()

	seedXhsScriptFreeQuota(t, db, 23, 3)
	require.NoError(t, s.DeductOneGeneration(ctx, 23, "gen-original"))
	require.NoError(t, s.DeductOneGeneration(ctx, 23, "gen-second"))
	require.NoError(t, s.DeductOneGeneration(ctx, 23, "gen-third"))

	account, err := s.GetQuotaAccount(ctx, 23)
	require.NoError(t, err)
	require.EqualValues(t, 0, account.FreeRemaining)
	require.EqualValues(t, 0, account.PaidRemaining)

	require.NoError(t, s.DeductOneGeneration(ctx, 23, "gen-original"))

	account, err = s.GetQuotaAccount(ctx, 23)
	require.NoError(t, err)
	assert.EqualValues(t, 0, account.FreeRemaining)
	assert.EqualValues(t, 0, account.PaidRemaining)

	var count int64
	require.NoError(t, db.Model(&model.XhsScriptQuotaLedger{}).
		Where("user_id = ? AND reason = ? AND ref_type = ? AND ref_id = ?", 23, model.XhsScriptLedgerReasonGeneration, model.XhsScriptLedgerRefTypeGeneration, "gen-original").
		Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestXhsScriptStore_DeductOneGenerationEmptyRefDoesNotChangeBalance(t *testing.T) {
	db := newXhsScriptStoreTestDB(t)
	s := NewXhsScriptStore(db)
	ctx := context.Background()

	seedXhsScriptFreeQuota(t, db, 21, 3)

	err := s.DeductOneGeneration(ctx, 21, "")
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

	seedXhsScriptFreeQuota(t, db, 22, 3)

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
