package xhsscript

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

func TestGetAnalyticsSummaryCountsCanonicalMVPEventNames(t *testing.T) {
	db := newAnalyticsSummaryTestDB(t)
	svc := New(store.NewTestStore(db))
	ctx := context.Background()
	now := time.Now()
	userID := uint(11)

	require.NoError(t, db.Create([]model.XhsScriptAnalyticsEvent{
		{EventID: "evt_page", EventName: "script_page_view", AnonymousID: "anon_11", UserID: &userID, SessionID: "sess_11", Path: "/script/", CreatedAt: now},
		{EventID: "evt_trial_canonical", EventName: "trial_user_created", AnonymousID: "anon_11", UserID: &userID, SessionID: "sess_11", Path: "/script/", CreatedAt: now},
		{EventID: "evt_ext_canonical", EventName: "extension_authorize_success", AnonymousID: "anon_11", UserID: &userID, SessionID: "sess_11", Path: "/script/", CreatedAt: now},
		{EventID: backendEventIDPrefix + ":order_created:order_11", EventName: "order_created", UserID: &userID, CreatedAt: now},
	}).Error)

	summary, err := svc.GetAnalyticsSummary(ctx, 7)
	require.NoError(t, err)

	assert.EqualValues(t, 1, summary.Totals.TrialStarted)
	assert.EqualValues(t, 1, summary.Totals.ExtensionAuthorized)
	assert.EqualValues(t, 1, summary.Totals.PurchaseOrderCreated)

	today := summary.Daily[len(summary.Daily)-1]
	assert.Equal(t, now.Format("2006-01-02"), today.Date)
	assert.EqualValues(t, 1, today.Trials)
	assert.Contains(t, analyticsEventCountMap(summary.EventCounts), "trial_user_created")
	assert.Contains(t, analyticsEventCountMap(summary.EventCounts), "extension_authorize_success")
	assert.Contains(t, analyticsEventCountMap(summary.EventCounts), "order_created")
}

func TestTrackEventsDoesNotAcceptReservedBackendEventIDPrefix(t *testing.T) {
	db := newAnalyticsSummaryTestDB(t)
	svc := New(store.NewTestStore(db))
	ctx := context.Background()
	userID := uint(12)

	accepted, err := svc.TrackEvents(ctx, &userID, []AnalyticsEventInput{
		{
			EventID:   backendEventIDPrefix + ":order_created:spoofed",
			EventName: "order_created",
			Path:      "/script/",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, accepted)

	var event model.XhsScriptAnalyticsEvent
	require.NoError(t, db.Where("event_name = ?", "order_created").First(&event).Error)
	assert.NotContains(t, event.EventID, backendEventIDPrefix+":")

	summary, err := svc.GetAnalyticsSummary(ctx, 7)
	require.NoError(t, err)
	assert.EqualValues(t, 0, summary.Totals.PurchaseOrderCreated)
}

func TestTrackEventsRedactsSensitivePropertiesAndLimitsPayload(t *testing.T) {
	db := newAnalyticsSummaryTestDB(t)
	svc := New(store.NewTestStore(db))
	ctx := context.Background()
	userID := uint(14)

	props := map[string]interface{}{
		"profile_text":    "我的完整产品定位正文",
		"transcript":      "完整视频转写正文",
		"script_text":     "生成后的完整口播稿",
		"title":           "用户采集的原始标题",
		"error.message":   "LLM upstream raw error with prompt text",
		"profileText":     "camel 产品定位正文",
		"scriptText":      "camel 口播稿",
		"generatedScript": "camel 生成稿",
		"hotComments":     "camel 高赞评论",
		"rawError":        "camel raw error",
		"errorMessage":    "camel error message",
		"videoTranscript": "camel 视频转写",
		" note_id ":       float64(123),
		"count":           float64(9),
		"status":          "failed",
		"stage":           "generate",
		"category":        "llm",
		"channel":         "web",
		"product_type":    model.ProductTypeXhsScriptPack,
		"amount":          float64(1990),
		"ids":             strings.Repeat("a", 260),
	}
	for i := 0; i < 60; i++ {
		props[fmt.Sprintf("extra_%02d", i)] = "safe"
	}

	accepted, err := svc.TrackEvents(ctx, &userID, []AnalyticsEventInput{
		{
			EventID:    "evt_redact",
			EventName:  "generation_fail",
			Properties: props,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, accepted)

	var event model.XhsScriptAnalyticsEvent
	require.NoError(t, db.Where("event_id = ?", "evt_redact").First(&event).Error)
	assert.NotContains(t, string(event.Properties), "完整产品定位正文")
	assert.NotContains(t, string(event.Properties), "完整视频转写正文")
	assert.NotContains(t, string(event.Properties), "完整口播稿")
	assert.NotContains(t, string(event.Properties), "用户采集的原始标题")
	assert.NotContains(t, string(event.Properties), "LLM upstream raw error")
	assert.NotContains(t, string(event.Properties), "camel 产品定位正文")
	assert.NotContains(t, string(event.Properties), "camel 口播稿")
	assert.NotContains(t, string(event.Properties), "camel 生成稿")
	assert.NotContains(t, string(event.Properties), "camel 高赞评论")
	assert.NotContains(t, string(event.Properties), "camel raw error")
	assert.NotContains(t, string(event.Properties), "camel error message")
	assert.NotContains(t, string(event.Properties), "camel 视频转写")

	var stored map[string]interface{}
	require.NoError(t, json.Unmarshal(event.Properties, &stored))
	assert.LessOrEqual(t, len(stored), 40)
	assert.NotContains(t, stored, "profile_text")
	assert.NotContains(t, stored, "transcript")
	assert.NotContains(t, stored, "script_text")
	assert.NotContains(t, stored, "title")
	assert.NotContains(t, stored, "error.message")
	assert.NotContains(t, stored, "profileText")
	assert.NotContains(t, stored, "scriptText")
	assert.NotContains(t, stored, "generatedScript")
	assert.NotContains(t, stored, "hotComments")
	assert.NotContains(t, stored, "rawError")
	assert.NotContains(t, stored, "errorMessage")
	assert.NotContains(t, stored, "videoTranscript")
	assert.Equal(t, float64(123), stored["note_id"])
	assert.Equal(t, "generate", stored["stage"])
	assert.Equal(t, "failed", stored["status"])
	assert.LessOrEqual(t, len(stored["ids"].(string)), 200)
}

func TestTrackEventsRedactsNestedSensitiveProperties(t *testing.T) {
	db := newAnalyticsSummaryTestDB(t)
	svc := New(store.NewTestStore(db))
	ctx := context.Background()
	userID := uint(16)

	accepted, err := svc.TrackEvents(ctx, &userID, []AnalyticsEventInput{
		{
			EventID:   "evt_nested_redact",
			EventName: "generation_fail",
			Properties: map[string]interface{}{
				"nested": map[string]interface{}{
					"scriptText": "nested 口播稿",
					"safe_count": float64(3),
					"child": map[string]interface{}{
						"rawError": "nested raw error",
						"status":   "failed",
						"too_deep": map[string]interface{}{
							"status": "dropped",
						},
					},
				},
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, accepted)

	var event model.XhsScriptAnalyticsEvent
	require.NoError(t, db.Where("event_id = ?", "evt_nested_redact").First(&event).Error)
	assert.NotContains(t, string(event.Properties), "nested 口播稿")
	assert.NotContains(t, string(event.Properties), "nested raw error")

	var stored map[string]interface{}
	require.NoError(t, json.Unmarshal(event.Properties, &stored))
	nested := stored["nested"].(map[string]interface{})
	assert.Equal(t, float64(3), nested["safe_count"])
	assert.NotContains(t, nested, "scriptText")
	child := nested["child"].(map[string]interface{})
	assert.Equal(t, "failed", child["status"])
	assert.NotContains(t, child, "rawError")
	assert.NotContains(t, child, "too_deep")
}

func TestTrackEventsRedactsPrefixedSensitiveTextFieldsButKeepsSafeMetrics(t *testing.T) {
	db := newAnalyticsSummaryTestDB(t)
	svc := New(store.NewTestStore(db))
	ctx := context.Background()
	userID := uint(17)

	accepted, err := svc.TrackEvents(ctx, &userID, []AnalyticsEventInput{
		{
			EventID:   "evt_prefixed_redact",
			EventName: "video_note_captured",
			Properties: map[string]interface{}{
				"noteTitle":           "带前缀的标题全文",
				"noteDescription":     "带前缀的描述全文",
				"commentContent":      "评论正文",
				"capturedHotComments": "高赞评论正文",
				"noteContent":         "笔记正文",
				"title_length":        float64(12),
				"description_length":  float64(34),
				"hot_comments_count":  float64(2),
				"script_length":       float64(56),
				"transcript_length":   float64(78),
				"error_category":      "generation_failed",
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, accepted)

	var event model.XhsScriptAnalyticsEvent
	require.NoError(t, db.Where("event_id = ?", "evt_prefixed_redact").First(&event).Error)
	assert.NotContains(t, string(event.Properties), "带前缀的标题全文")
	assert.NotContains(t, string(event.Properties), "带前缀的描述全文")
	assert.NotContains(t, string(event.Properties), "评论正文")
	assert.NotContains(t, string(event.Properties), "高赞评论正文")
	assert.NotContains(t, string(event.Properties), "笔记正文")

	var stored map[string]interface{}
	require.NoError(t, json.Unmarshal(event.Properties, &stored))
	assert.NotContains(t, stored, "noteTitle")
	assert.NotContains(t, stored, "noteDescription")
	assert.NotContains(t, stored, "commentContent")
	assert.NotContains(t, stored, "capturedHotComments")
	assert.NotContains(t, stored, "noteContent")
	assert.Equal(t, float64(12), stored["title_length"])
	assert.Equal(t, float64(34), stored["description_length"])
	assert.Equal(t, float64(2), stored["hot_comments_count"])
	assert.Equal(t, float64(56), stored["script_length"])
	assert.Equal(t, float64(78), stored["transcript_length"])
	assert.Equal(t, "generation_failed", stored["error_category"])
}

func TestTrackEventsLimitsBatchAndStringFieldLengths(t *testing.T) {
	db := newAnalyticsSummaryTestDB(t)
	svc := New(store.NewTestStore(db))
	ctx := context.Background()
	userID := uint(15)
	events := make([]AnalyticsEventInput, 0, 55)
	for i := 0; i < 55; i++ {
		events = append(events, AnalyticsEventInput{
			EventID:     fmt.Sprintf("evt_%02d_%s", i, strings.Repeat("e", 200)),
			EventName:   "event_" + strings.Repeat("n", 120),
			AnonymousID: "anon_" + strings.Repeat("a", 200),
			SessionID:   "sess_" + strings.Repeat("s", 200),
			Path:        "/script/" + strings.Repeat("p", 400),
			Properties:  map[string]interface{}{"status": "ok"},
		})
	}

	accepted, err := svc.TrackEvents(ctx, &userID, events)
	require.NoError(t, err)
	assert.Equal(t, 50, accepted)

	var count int64
	require.NoError(t, db.Model(&model.XhsScriptAnalyticsEvent{}).Count(&count).Error)
	assert.EqualValues(t, 50, count)

	var stored []model.XhsScriptAnalyticsEvent
	require.NoError(t, db.Order("id ASC").Find(&stored).Error)
	require.Len(t, stored, 50)
	for _, event := range stored {
		assert.LessOrEqual(t, len([]rune(event.EventID)), 128)
		assert.LessOrEqual(t, len([]rune(event.EventName)), 80)
		assert.LessOrEqual(t, len([]rune(event.AnonymousID)), 128)
		assert.LessOrEqual(t, len([]rune(event.SessionID)), 128)
		assert.LessOrEqual(t, len([]rune(event.Path)), 256)
	}
}

func TestAnalyticsSummaryDoesNotCountLikeWildcardBackendPrefixSpoof(t *testing.T) {
	db := newAnalyticsSummaryTestDB(t)
	svc := New(store.NewTestStore(db))
	ctx := context.Background()
	now := time.Now()
	userID := uint(13)

	require.NoError(t, db.Create(&model.XhsScriptAnalyticsEvent{
		EventID:   "backend:xhsAscript:orderXcreated:spoofed",
		EventName: "order_created",
		UserID:    &userID,
		CreatedAt: now,
	}).Error)

	summary, err := svc.GetAnalyticsSummary(ctx, 7)
	require.NoError(t, err)
	assert.EqualValues(t, 0, summary.Totals.PurchaseOrderCreated)
}

func newAnalyticsSummaryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/xhs_analytics_test.db?_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.XhsScriptAnalyticsEvent{},
		&model.XhsScriptUserProfile{},
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
