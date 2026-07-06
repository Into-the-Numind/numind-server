package xhsscript

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

func TestGetAnalyticsSummaryAggregatesMVPFunnel(t *testing.T) {
	db := newAnalyticsSummaryTestDB(t)
	svc := New(store.NewTestStore(db))
	ctx := context.Background()
	now := time.Now()

	userOne := uint(1)
	userTwo := uint(2)
	require.NoError(t, db.Create([]model.XhsScriptAnalyticsEvent{
		{EventID: "evt_page_1", EventName: "script_page_view", AnonymousID: "anon_1", UserID: &userOne, SessionID: "sess_1", Path: "/script/", CreatedAt: now},
		{EventID: "evt_trial", EventName: "trial_started", AnonymousID: "anon_1", UserID: &userOne, SessionID: "sess_1", Path: "/script/", CreatedAt: now},
		{EventID: "evt_profile", EventName: "profile_saved", AnonymousID: "anon_1", UserID: &userOne, SessionID: "sess_1", Path: "/script/", CreatedAt: now},
		{EventID: "evt_ext", EventName: "extension_token_issued", AnonymousID: "anon_1", UserID: &userOne, SessionID: "sess_1", Path: "/script/", CreatedAt: now},
		{EventID: "evt_client_order", EventName: "purchase_order_created", AnonymousID: "anon_1", UserID: &userOne, SessionID: "sess_1", Path: "/script/", CreatedAt: now},
		{EventID: backendEventIDPrefix + ":purchase_order_created:order_1", EventName: "purchase_order_created", UserID: &userOne, CreatedAt: now},
		{EventID: "evt_page_2", EventName: "script_page_view", AnonymousID: "anon_2", UserID: &userTwo, SessionID: "sess_2", Path: "/script/", CreatedAt: now},
	}).Error)

	readyGenerated := model.XhsScriptNote{
		UserID:           userOne,
		SourceNoteID:     "note_ready_generated",
		NoteType:         model.XhsScriptNoteTypeVideo,
		Title:            "已生成",
		TranscribeStatus: model.XhsScriptTranscribeReady,
		GenerateStatus:   model.XhsScriptGenerateGenerated,
		CreatedAt:        now,
	}
	transcribeFailed := model.XhsScriptNote{
		UserID:           userOne,
		SourceNoteID:     "note_transcribe_failed",
		NoteType:         model.XhsScriptNoteTypeVideo,
		Title:            "转写失败",
		TranscribeStatus: model.XhsScriptTranscribeFailed,
		GenerateStatus:   model.XhsScriptGenerateNotReady,
		LastError:        "video download failed",
		CreatedAt:        now,
	}
	generateFailed := model.XhsScriptNote{
		UserID:           userTwo,
		SourceNoteID:     "note_generate_failed",
		NoteType:         model.XhsScriptNoteTypeVideo,
		Title:            "生成失败",
		TranscribeStatus: model.XhsScriptTranscribeReady,
		GenerateStatus:   model.XhsScriptGenerateFailed,
		LastError:        "model timeout",
		CreatedAt:        now,
	}
	require.NoError(t, db.Create(&readyGenerated).Error)
	require.NoError(t, db.Create(&transcribeFailed).Error)
	require.NoError(t, db.Create(&generateFailed).Error)
	require.NoError(t, db.Create(&model.XhsScriptGeneration{
		UserID:     userOne,
		NoteID:     readyGenerated.ID,
		Version:    1,
		ScriptText: "口播稿",
		CreatedAt:  now,
	}).Error)

	require.NoError(t, db.Create([]model.XhsScriptQuotaLedger{
		{UserID: userOne, Delta: -1, Bucket: model.XhsScriptQuotaBucketFree, Reason: model.XhsScriptLedgerReasonGeneration, RefType: model.XhsScriptLedgerRefTypeGeneration, RefID: "generation_1", CreatedAt: now},
		{UserID: userOne, Delta: 10, Bucket: model.XhsScriptQuotaBucketPaid, Reason: model.XhsScriptLedgerReasonPurchase, RefType: model.XhsScriptLedgerRefTypePurchase, RefID: "order_1", CreatedAt: now},
	}).Error)
	paidAt := now
	require.NoError(t, db.Create(&model.Order{
		OrderNo:     "order_1",
		UserID:      userOne,
		PayerID:     userOne,
		ProductType: model.ProductTypeXhsScriptPack,
		Quantity:    1,
		Amount:      PackAmountCents,
		PayChannel:  model.PayChannelWechat,
		PayStatus:   model.OrderStatusPaid,
		PaidAt:      &paidAt,
		ExpiredAt:   now.Add(time.Hour),
		CreatedAt:   now,
	}).Error)

	summary, err := svc.GetAnalyticsSummary(ctx, 7)
	require.NoError(t, err)
	require.NotNil(t, summary)

	assert.Equal(t, 7, summary.Window.Days)
	assert.EqualValues(t, 2, summary.Totals.PageViews)
	assert.EqualValues(t, 2, summary.Totals.UniqueVisitors)
	assert.EqualValues(t, 1, summary.Totals.TrialStarted)
	assert.EqualValues(t, 1, summary.Totals.ProfileSaved)
	assert.EqualValues(t, 1, summary.Totals.ExtensionAuthorized)
	assert.EqualValues(t, 3, summary.Totals.CapturedNotes)
	assert.EqualValues(t, 2, summary.Totals.TranscribeReadyNotes)
	assert.EqualValues(t, 1, summary.Totals.TranscribeFailedNotes)
	assert.EqualValues(t, 1, summary.Totals.GeneratedNotes)
	assert.EqualValues(t, 1, summary.Totals.GenerationFailedNotes)
	assert.EqualValues(t, 1, summary.Totals.Generations)
	assert.EqualValues(t, 1, summary.Totals.GenerationDeductions)
	assert.EqualValues(t, 1, summary.Totals.PurchaseOrderCreated)
	assert.EqualValues(t, 1, summary.Totals.PaidOrders)
	assert.EqualValues(t, PackAmountCents, summary.Totals.RevenueCents)
	assert.EqualValues(t, PackGenerations, summary.Totals.PurchasedGenerations)
	assert.InDelta(t, 0.5, summary.Rates.VisitorToTrial, 0.0001)
	assert.InDelta(t, 3.0, summary.Rates.TrialToCapture, 0.0001)
	assert.InDelta(t, 0.6667, summary.Rates.CaptureToReady, 0.0001)
	assert.InDelta(t, 0.5, summary.Rates.ReadyToGenerated, 0.0001)
	assert.InDelta(t, 1.0, summary.Rates.OrderPayRate, 0.0001)
	assert.Len(t, summary.Daily, 7)

	today := summary.Daily[len(summary.Daily)-1]
	assert.Equal(t, now.Format("2006-01-02"), today.Date)
	assert.EqualValues(t, 2, today.PageViews)
	assert.EqualValues(t, 1, today.Trials)
	assert.EqualValues(t, 3, today.CapturedNotes)
	assert.EqualValues(t, 1, today.Generations)
	assert.EqualValues(t, 1, today.PaidOrders)
	assert.EqualValues(t, PackAmountCents, today.RevenueCents)
	assert.Contains(t, analyticsEventCountMap(summary.EventCounts), "script_page_view")
	assert.Contains(t, analyticsErrorMap(summary.RecentErrors), "transcribe:video download failed")
	assert.Contains(t, analyticsErrorMap(summary.RecentErrors), "generate:model timeout")
}

func newAnalyticsSummaryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/xhs_analytics_test.db?_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.XhsScriptAnalyticsEvent{},
		&model.XhsScriptNote{},
		&model.XhsScriptGeneration{},
		&model.XhsScriptQuotaLedger{},
		&model.XhsScriptQuotaAccount{},
		&model.Order{},
	))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func analyticsEventCountMap(counts []AnalyticsEventCountDTO) map[string]int64 {
	out := make(map[string]int64, len(counts))
	for _, count := range counts {
		out[count.EventName] = count.Count
	}
	return out
}

func analyticsErrorMap(errors []AnalyticsErrorDTO) map[string]int64 {
	out := make(map[string]int64, len(errors))
	for _, item := range errors {
		out[item.Type+":"+item.Message] = item.Count
	}
	return out
}
