package xhsscript

import (
	"context"
	"database/sql"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"numind-server/internal/pkg/model"
)

type AnalyticsEventInput struct {
	EventID     string                 `json:"event_id"`
	EventName   string                 `json:"event_name"`
	AnonymousID string                 `json:"anonymous_id"`
	SessionID   string                 `json:"session_id"`
	Path        string                 `json:"path"`
	Properties  map[string]interface{} `json:"properties"`
	OccurredAt  string                 `json:"occurred_at"`
}

type AnalyticsSummaryDTO struct {
	Window       AnalyticsWindowDTO       `json:"window"`
	Totals       AnalyticsTotalsDTO       `json:"totals"`
	Rates        AnalyticsRatesDTO        `json:"rates"`
	EventCounts  []AnalyticsEventCountDTO `json:"event_counts"`
	Daily        []AnalyticsDailyDTO      `json:"daily"`
	RecentErrors []AnalyticsErrorDTO      `json:"recent_errors"`
}

type AnalyticsWindowDTO struct {
	Days int       `json:"days"`
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

type AnalyticsTotalsDTO struct {
	PageViews             int64 `json:"page_views"`
	UniqueVisitors        int64 `json:"unique_visitors"`
	TrialStarted          int64 `json:"trial_started"`
	ProfileSaved          int64 `json:"profile_saved"`
	ExtensionAuthorized   int64 `json:"extension_authorized"`
	AccountRegistered     int64 `json:"account_registered"`
	AccountLoggedIn       int64 `json:"account_logged_in"`
	CapturedNotes         int64 `json:"captured_notes"`
	TranscribeReadyNotes  int64 `json:"transcribe_ready_notes"`
	TranscribeFailedNotes int64 `json:"transcribe_failed_notes"`
	GeneratedNotes        int64 `json:"generated_notes"`
	GenerationFailedNotes int64 `json:"generation_failed_notes"`
	Generations           int64 `json:"generations"`
	GenerationDeductions  int64 `json:"generation_deductions"`
	PurchaseOrderCreated  int64 `json:"purchase_order_created"`
	PaidOrders            int64 `json:"paid_orders"`
	RevenueCents          int64 `json:"revenue_cents"`
	PurchasedGenerations  int64 `json:"purchased_generations"`
}

type AnalyticsRatesDTO struct {
	VisitorToTrial   float64 `json:"visitor_to_trial"`
	TrialToCapture   float64 `json:"trial_to_capture"`
	CaptureToReady   float64 `json:"capture_to_ready"`
	ReadyToGenerated float64 `json:"ready_to_generated"`
	OrderPayRate     float64 `json:"order_pay_rate"`
	PaidConversion   float64 `json:"paid_conversion"`
}

type AnalyticsEventCountDTO struct {
	EventName string `json:"event_name"`
	Count     int64  `json:"count"`
}

type AnalyticsDailyDTO struct {
	Date          string `json:"date"`
	PageViews     int64  `json:"page_views"`
	Trials        int64  `json:"trials"`
	CapturedNotes int64  `json:"captured_notes"`
	Generations   int64  `json:"generations"`
	PaidOrders    int64  `json:"paid_orders"`
	RevenueCents  int64  `json:"revenue_cents"`
}

type AnalyticsErrorDTO struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Count   int64  `json:"count"`
}

func (s *Service) TrackEvents(ctx context.Context, userID *uint, events []AnalyticsEventInput) (int, error) {
	if len(events) > maxAnalyticsEventsPerRequest {
		events = events[:maxAnalyticsEventsPerRequest]
	}
	accepted := 0
	for _, input := range events {
		eventID := limitRunes(strings.TrimSpace(input.EventID), maxAnalyticsEventIDRunes)
		eventName := limitRunes(strings.TrimSpace(input.EventName), maxAnalyticsEventNameRunes)
		if eventID == "" || eventName == "" {
			continue
		}
		eventID = limitRunes(clientAnalyticsEventID(eventID), maxAnalyticsEventIDRunes)
		createdAt := time.Now()
		if input.OccurredAt != "" {
			if parsed, err := time.Parse(time.RFC3339Nano, input.OccurredAt); err == nil {
				createdAt = parsed
			}
		}
		event := &model.XhsScriptAnalyticsEvent{
			EventID:     eventID,
			EventName:   eventName,
			AnonymousID: limitRunes(strings.TrimSpace(input.AnonymousID), maxAnalyticsAnonymousIDRunes),
			UserID:      userID,
			SessionID:   limitRunes(strings.TrimSpace(input.SessionID), maxAnalyticsSessionIDRunes),
			Path:        limitRunes(strings.TrimSpace(input.Path), maxAnalyticsPathRunes),
			Properties:  mustJSON(sanitizeAnalyticsProperties(input.Properties)),
			CreatedAt:   createdAt,
		}
		if err := s.ds.XhsScript().InsertAnalyticsEvent(ctx, event); err != nil {
			return accepted, err
		}
		accepted++
	}
	return accepted, nil
}

const (
	maxAnalyticsPropertyKeys        = 40
	maxAnalyticsPropertyStringRunes = 200
	maxAnalyticsPropertyDepth       = 3
	maxAnalyticsEventsPerRequest    = 50
	maxAnalyticsEventIDRunes        = 128
	maxAnalyticsEventNameRunes      = 80
	maxAnalyticsAnonymousIDRunes    = 128
	maxAnalyticsSessionIDRunes      = 128
	maxAnalyticsPathRunes           = 256
)

var sensitiveAnalyticsPropertyKeys = map[string]struct{}{
	"profile_text":     {},
	"profile":          {},
	"transcript":       {},
	"video_transcript": {},
	"script_text":      {},
	"script":           {},
	"generated_script": {},
	"description":      {},
	"title":            {},
	"content":          {},
	"comments":         {},
	"hot_comments":     {},
	"prompt":           {},
	"raw_error":        {},
	"error_message":    {},
	"message":          {},
}

var preferredAnalyticsPropertyKeys = map[string]struct{}{
	"id":                {},
	"ids":               {},
	"note_id":           {},
	"source_note_id":    {},
	"generation_id":     {},
	"order_id":          {},
	"order_no":          {},
	"count":             {},
	"counts":            {},
	"quantity":          {},
	"status":            {},
	"pay_status":        {},
	"length":            {},
	"text_length":       {},
	"script_length":     {},
	"transcript_length": {},
	"stage":             {},
	"category":          {},
	"error_category":    {},
	"channel":           {},
	"product_type":      {},
	"amount":            {},
	"amount_cents":      {},
}

func sanitizeAnalyticsProperties(input map[string]interface{}) map[string]interface{} {
	return sanitizeAnalyticsPropertiesDepth(input, 0)
}

func sanitizeAnalyticsPropertiesDepth(input map[string]interface{}, depth int) map[string]interface{} {
	if len(input) == 0 {
		return map[string]interface{}{}
	}
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		leftPreferred := isPreferredAnalyticsPropertyKey(keys[i])
		rightPreferred := isPreferredAnalyticsPropertyKey(keys[j])
		if leftPreferred != rightPreferred {
			return leftPreferred
		}
		return keys[i] < keys[j]
	})

	out := make(map[string]interface{}, minInt(len(keys), maxAnalyticsPropertyKeys))
	for _, originalKey := range keys {
		storedKey := strings.TrimSpace(originalKey)
		if storedKey == "" || isSensitiveAnalyticsPropertyKey(storedKey) {
			continue
		}
		value, ok := sanitizeAnalyticsPropertyValue(input[originalKey], depth)
		if !ok {
			continue
		}
		out[storedKey] = value
		if len(out) >= maxAnalyticsPropertyKeys {
			break
		}
	}
	return out
}

func isSensitiveAnalyticsPropertyKey(key string) bool {
	normalized := normalizeAnalyticsPropertyKey(key)
	if isSafeAnalyticsMetricKey(normalized) {
		return false
	}
	if _, ok := sensitiveAnalyticsPropertyKeys[normalized]; ok {
		return true
	}
	parts := strings.FieldsFunc(normalized, func(r rune) bool { return r == '_' })
	for _, part := range parts {
		switch part {
		case "title", "description", "content", "prompt", "message", "script", "transcript", "profile":
			return true
		case "comment", "comments":
			return true
		case "error":
			if strings.Contains(normalized, "raw_error") || strings.Contains(normalized, "error_message") {
				return true
			}
		}
	}
	for i := 0; i < len(parts)-1; i++ {
		pair := parts[i] + "_" + parts[i+1]
		switch pair {
		case "hot_comments", "profile_text", "video_transcript", "script_text", "generated_script", "raw_error", "error_message":
			return true
		}
		if _, ok := sensitiveAnalyticsPropertyKeys[pair]; ok {
			return true
		}
	}
	return false
}

func isSafeAnalyticsMetricKey(normalized string) bool {
	switch normalized {
	case "title_length", "description_length", "hot_comments_count", "comment_count", "script_length", "transcript_length", "prompt_tokens", "completion_tokens", "error_category":
		return true
	default:
		return false
	}
}

func isPreferredAnalyticsPropertyKey(key string) bool {
	_, ok := preferredAnalyticsPropertyKeys[normalizeAnalyticsPropertyKey(key)]
	return ok
}

func normalizeAnalyticsPropertyKey(key string) string {
	key = strings.TrimSpace(key)
	replacer := strings.NewReplacer(".", "_", "-", "_", " ", "_")
	key = replacer.Replace(key)
	var b strings.Builder
	var prevUnderscore bool
	var prevLowerOrDigit bool
	for _, r := range key {
		if r == '_' {
			if !prevUnderscore && b.Len() > 0 {
				b.WriteRune('_')
				prevUnderscore = true
			}
			prevLowerOrDigit = false
			continue
		}
		if unicode.IsUpper(r) && prevLowerOrDigit && !prevUnderscore {
			b.WriteRune('_')
		}
		lower := unicode.ToLower(r)
		b.WriteRune(lower)
		prevUnderscore = false
		prevLowerOrDigit = unicode.IsLower(lower) || unicode.IsDigit(lower)
	}
	return strings.Trim(b.String(), "_")
}

func sanitizeAnalyticsPropertyValue(value interface{}, depth int) (interface{}, bool) {
	switch v := value.(type) {
	case nil:
		return nil, true
	case string:
		return limitRunes(strings.TrimSpace(v), maxAnalyticsPropertyStringRunes), true
	case bool:
		return v, true
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return v, true
	case map[string]interface{}:
		if depth+1 >= maxAnalyticsPropertyDepth {
			return nil, false
		}
		sanitized := sanitizeAnalyticsPropertiesDepth(v, depth+1)
		return sanitized, len(sanitized) > 0
	default:
		return nil, false
	}
}

func limitRunes(value string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func clientAnalyticsEventID(eventID string) string {
	eventID = strings.TrimSpace(eventID)
	if strings.HasPrefix(eventID, backendEventIDPrefix+":") {
		return newClientEventID(eventID)
	}
	return eventID
}

func newClientEventID(original string) string {
	sum := shortHash(original)
	compactUUID := strings.ReplaceAll(uuid.NewString(), "-", "")
	return "client:xhs:" + sum + ":" + compactUUID
}

func (s *Service) GetAnalyticsSummary(ctx context.Context, days int) (*AnalyticsSummaryDTO, error) {
	if days <= 0 {
		days = 30
	}
	if days > 90 {
		days = 90
	}

	now := time.Now()
	from := beginningOfDay(now).AddDate(0, 0, -(days - 1))
	window := AnalyticsWindowDTO{Days: days, From: from, To: now}
	db := s.ds.DB().WithContext(ctx)

	totals := AnalyticsTotalsDTO{}
	var err error
	if totals.PageViews, err = countAnalyticsEvents(ctx, s, from, "script_page_view"); err != nil {
		return nil, err
	}
	if totals.TrialStarted, err = countAnalyticsEventNames(ctx, s, from, []string{"trial_user_created", "trial_started"}); err != nil {
		return nil, err
	}
	if totals.ProfileSaved, err = countAnalyticsEvents(ctx, s, from, "profile_saved"); err != nil {
		return nil, err
	}
	if totals.ExtensionAuthorized, err = countAnalyticsEventNames(ctx, s, from, []string{"extension_authorize_success", "extension_token_issued"}); err != nil {
		return nil, err
	}
	if totals.AccountRegistered, err = countAnalyticsEvents(ctx, s, from, "account_registered"); err != nil {
		return nil, err
	}
	if totals.AccountLoggedIn, err = countAnalyticsEvents(ctx, s, from, "account_logged_in"); err != nil {
		return nil, err
	}
	if totals.PurchaseOrderCreated, err = countBackendPurchaseOrderCreated(ctx, s, from); err != nil {
		return nil, err
	}
	if totals.UniqueVisitors, err = countUniqueVisitors(ctx, s, from); err != nil {
		return nil, err
	}

	if err = db.Model(&model.XhsScriptNote{}).
		Where("created_at >= ?", from).
		Count(&totals.CapturedNotes).Error; err != nil {
		return nil, err
	}
	if err = db.Model(&model.XhsScriptNote{}).
		Where("created_at >= ? AND transcribe_status = ?", from, model.XhsScriptTranscribeReady).
		Count(&totals.TranscribeReadyNotes).Error; err != nil {
		return nil, err
	}
	if err = db.Model(&model.XhsScriptNote{}).
		Where("created_at >= ? AND transcribe_status IN ?", from, []string{model.XhsScriptTranscribeFailed, model.XhsScriptTranscribeEmpty}).
		Count(&totals.TranscribeFailedNotes).Error; err != nil {
		return nil, err
	}
	if err = db.Model(&model.XhsScriptNote{}).
		Where("created_at >= ? AND generate_status = ?", from, model.XhsScriptGenerateGenerated).
		Count(&totals.GeneratedNotes).Error; err != nil {
		return nil, err
	}
	if err = db.Model(&model.XhsScriptNote{}).
		Where("created_at >= ? AND generate_status = ?", from, model.XhsScriptGenerateFailed).
		Count(&totals.GenerationFailedNotes).Error; err != nil {
		return nil, err
	}
	if err = db.Model(&model.XhsScriptGeneration{}).
		Where("created_at >= ?", from).
		Count(&totals.Generations).Error; err != nil {
		return nil, err
	}
	if err = db.Model(&model.XhsScriptQuotaLedger{}).
		Where("created_at >= ? AND reason = ? AND delta < 0", from, model.XhsScriptLedgerReasonGeneration).
		Count(&totals.GenerationDeductions).Error; err != nil {
		return nil, err
	}

	var revenue sql.NullInt64
	if err = db.Model(&model.Order{}).
		Select("COALESCE(SUM(amount), 0)").
		Where("created_at >= ? AND product_type = ? AND pay_status = ?", from, model.ProductTypeXhsScriptPack, model.OrderStatusPaid).
		Scan(&revenue).Error; err != nil {
		return nil, err
	}
	totals.RevenueCents = revenue.Int64
	if err = db.Model(&model.Order{}).
		Where("created_at >= ? AND product_type = ? AND pay_status = ?", from, model.ProductTypeXhsScriptPack, model.OrderStatusPaid).
		Count(&totals.PaidOrders).Error; err != nil {
		return nil, err
	}
	var purchased sql.NullInt64
	if err = db.Model(&model.XhsScriptQuotaLedger{}).
		Select("COALESCE(SUM(delta), 0)").
		Where("created_at >= ? AND reason = ? AND delta > 0", from, model.XhsScriptLedgerReasonPurchase).
		Scan(&purchased).Error; err != nil {
		return nil, err
	}
	totals.PurchasedGenerations = purchased.Int64

	eventRows, err := eventCounts(ctx, s, from)
	if err != nil {
		return nil, err
	}
	daily, err := dailyAnalytics(ctx, s, from, days)
	if err != nil {
		return nil, err
	}
	recentErrors, err := recentAnalyticsErrors(ctx, s, from)
	if err != nil {
		return nil, err
	}

	return &AnalyticsSummaryDTO{
		Window:       window,
		Totals:       totals,
		Rates:        ratesFromTotals(totals),
		EventCounts:  eventRows,
		Daily:        daily,
		RecentErrors: recentErrors,
	}, nil
}

func beginningOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func countAnalyticsEvents(ctx context.Context, s *Service, from time.Time, eventName string) (int64, error) {
	return countAnalyticsEventNames(ctx, s, from, []string{eventName})
}

func countAnalyticsEventNames(ctx context.Context, s *Service, from time.Time, eventNames []string) (int64, error) {
	var count int64
	err := s.ds.DB().WithContext(ctx).Model(&model.XhsScriptAnalyticsEvent{}).
		Where("created_at >= ? AND event_name IN ?", from, eventNames).
		Count(&count).Error
	return count, err
}

func countBackendPurchaseOrderCreated(ctx context.Context, s *Service, from time.Time) (int64, error) {
	var count int64
	err := s.ds.DB().WithContext(ctx).Model(&model.XhsScriptAnalyticsEvent{}).
		Where(
			"created_at >= ? AND ((event_name = ? AND event_id LIKE ? ESCAPE '\\') OR (event_name = ? AND event_id LIKE ? ESCAPE '\\'))",
			from,
			"order_created",
			escapedSQLLikePrefix(backendEventIDPrefix+":order_created:"),
			"purchase_order_created",
			escapedSQLLikePrefix(backendEventIDPrefix+":purchase_order_created:"),
		).
		Count(&count).Error
	return count, err
}

func escapedSQLLikePrefix(prefix string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `_`, `\_`, `%`, `\%`)
	return replacer.Replace(prefix) + "%"
}

func countUniqueVisitors(ctx context.Context, s *Service, from time.Time) (int64, error) {
	rows, err := s.ds.DB().WithContext(ctx).Model(&model.XhsScriptAnalyticsEvent{}).
		Select("anonymous_id, user_id").
		Where("created_at >= ?", from).
		Rows()
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	visitors := map[string]struct{}{}
	for rows.Next() {
		var anonymousID sql.NullString
		var userID sql.NullInt64
		if err := rows.Scan(&anonymousID, &userID); err != nil {
			return 0, err
		}
		if userID.Valid && userID.Int64 > 0 {
			visitors["u:"+strconvInt(userID.Int64)] = struct{}{}
			continue
		}
		if anonymousID.Valid && strings.TrimSpace(anonymousID.String) != "" {
			visitors["a:"+strings.TrimSpace(anonymousID.String)] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return int64(len(visitors)), nil
}

func eventCounts(ctx context.Context, s *Service, from time.Time) ([]AnalyticsEventCountDTO, error) {
	var rows []struct {
		EventName string
		Count     int64
	}
	err := s.ds.DB().WithContext(ctx).Model(&model.XhsScriptAnalyticsEvent{}).
		Select("event_name, COUNT(*) AS count").
		Where("created_at >= ?", from).
		Group("event_name").
		Order("count DESC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	counts := make([]AnalyticsEventCountDTO, 0, len(rows))
	for _, row := range rows {
		counts = append(counts, AnalyticsEventCountDTO{EventName: row.EventName, Count: row.Count})
	}
	return counts, nil
}

func dailyAnalytics(ctx context.Context, s *Service, from time.Time, days int) ([]AnalyticsDailyDTO, error) {
	byDate := make(map[string]*AnalyticsDailyDTO, days)
	daily := make([]AnalyticsDailyDTO, 0, days)
	for i := 0; i < days; i++ {
		date := from.AddDate(0, 0, i).Format("2006-01-02")
		row := AnalyticsDailyDTO{Date: date}
		daily = append(daily, row)
		byDate[date] = &daily[len(daily)-1]
	}

	if err := applyDailyEventCounts(ctx, s, from, byDate, "script_page_view", func(row *AnalyticsDailyDTO, count int64) {
		row.PageViews = count
	}); err != nil {
		return nil, err
	}
	if err := applyDailyEventNameCounts(ctx, s, from, byDate, []string{"trial_user_created", "trial_started"}, func(row *AnalyticsDailyDTO, count int64) {
		row.Trials = count
	}); err != nil {
		return nil, err
	}
	if err := applyDailyTableCounts(ctx, s, from, byDate, &model.XhsScriptNote{}, "", nil, func(row *AnalyticsDailyDTO, count int64) {
		row.CapturedNotes = count
	}); err != nil {
		return nil, err
	}
	if err := applyDailyTableCounts(ctx, s, from, byDate, &model.XhsScriptGeneration{}, "", nil, func(row *AnalyticsDailyDTO, count int64) {
		row.Generations = count
	}); err != nil {
		return nil, err
	}

	var paidRows []struct {
		Bucket       string
		PaidOrders   int64
		RevenueCents int64
	}
	db := s.ds.DB().WithContext(ctx)
	bucketExpression := analyticsDateBucketExpression(db.Dialector.Name())
	err := db.Model(&model.Order{}).
		Select(bucketExpression+" AS bucket, COUNT(*) AS paid_orders, COALESCE(SUM(amount), 0) AS revenue_cents").
		Where("created_at >= ? AND product_type = ? AND pay_status = ?", from, model.ProductTypeXhsScriptPack, model.OrderStatusPaid).
		Group(bucketExpression).
		Scan(&paidRows).Error
	if err != nil {
		return nil, err
	}
	for _, item := range paidRows {
		if row := byDate[item.Bucket]; row != nil {
			row.PaidOrders = item.PaidOrders
			row.RevenueCents = item.RevenueCents
		}
	}

	return daily, nil
}

func applyDailyEventCounts(ctx context.Context, s *Service, from time.Time, byDate map[string]*AnalyticsDailyDTO, eventName string, apply func(*AnalyticsDailyDTO, int64)) error {
	return applyDailyEventNameCounts(ctx, s, from, byDate, []string{eventName}, apply)
}

func applyDailyEventNameCounts(ctx context.Context, s *Service, from time.Time, byDate map[string]*AnalyticsDailyDTO, eventNames []string, apply func(*AnalyticsDailyDTO, int64)) error {
	return applyDailyTableCounts(ctx, s, from, byDate, &model.XhsScriptAnalyticsEvent{}, "event_name IN ?", []interface{}{eventNames}, apply)
}

func applyDailyTableCounts(ctx context.Context, s *Service, from time.Time, byDate map[string]*AnalyticsDailyDTO, modelValue interface{}, clause string, args []interface{}, apply func(*AnalyticsDailyDTO, int64)) error {
	var rows []struct {
		Bucket string
		Count  int64
	}
	db := s.ds.DB().WithContext(ctx)
	bucketExpression := analyticsDateBucketExpression(db.Dialector.Name())
	query := db.Model(modelValue).
		Select(bucketExpression+" AS bucket, COUNT(*) AS count").
		Where("created_at >= ?", from)
	if clause != "" {
		query = query.Where(clause, args...)
	}
	if err := query.Group(bucketExpression).Scan(&rows).Error; err != nil {
		return err
	}

	for _, row := range rows {
		if dailyRow := byDate[row.Bucket]; dailyRow != nil {
			apply(dailyRow, row.Count)
		}
	}
	return nil
}

func analyticsDateBucketExpression(dialect string) string {
	if dialect == "sqlite" {
		// SQLite normalizes RFC3339 timestamps to UTC before DATE(), which moves
		// China-local events from 00:00-07:59 into the previous day. Production
		// MySQL stores DATETIME in the configured China-local session timezone,
		// so keep its existing DATE(created_at) behavior unchanged.
		return "DATE(created_at, '+8 hours')"
	}
	return "DATE(created_at)"
}

func recentAnalyticsErrors(ctx context.Context, s *Service, from time.Time) ([]AnalyticsErrorDTO, error) {
	var transcribeRows []struct {
		Message string
		Count   int64
	}
	if err := s.ds.DB().WithContext(ctx).Model(&model.XhsScriptNote{}).
		Select("last_error AS message, COUNT(*) AS count").
		Where("created_at >= ? AND transcribe_status IN ? AND last_error <> ?", from, []string{model.XhsScriptTranscribeFailed, model.XhsScriptTranscribeEmpty}, "").
		Group("last_error").
		Order("count DESC").
		Limit(5).
		Scan(&transcribeRows).Error; err != nil {
		return nil, err
	}
	var generateRows []struct {
		Message string
		Count   int64
	}
	if err := s.ds.DB().WithContext(ctx).Model(&model.XhsScriptNote{}).
		Select("last_error AS message, COUNT(*) AS count").
		Where("created_at >= ? AND generate_status = ? AND last_error <> ?", from, model.XhsScriptGenerateFailed, "").
		Group("last_error").
		Order("count DESC").
		Limit(5).
		Scan(&generateRows).Error; err != nil {
		return nil, err
	}

	errors := make([]AnalyticsErrorDTO, 0, len(transcribeRows)+len(generateRows))
	for _, row := range transcribeRows {
		errors = append(errors, AnalyticsErrorDTO{Type: "transcribe", Message: row.Message, Count: row.Count})
	}
	for _, row := range generateRows {
		errors = append(errors, AnalyticsErrorDTO{Type: "generate", Message: row.Message, Count: row.Count})
	}
	sort.SliceStable(errors, func(i, j int) bool {
		return errors[i].Count > errors[j].Count
	})
	if len(errors) > 8 {
		errors = errors[:8]
	}
	return errors, nil
}

func ratesFromTotals(t AnalyticsTotalsDTO) AnalyticsRatesDTO {
	return AnalyticsRatesDTO{
		VisitorToTrial:   ratio(t.TrialStarted, t.UniqueVisitors),
		TrialToCapture:   ratio(t.CapturedNotes, t.TrialStarted),
		CaptureToReady:   ratio(t.TranscribeReadyNotes, t.CapturedNotes),
		ReadyToGenerated: ratio(t.GeneratedNotes, t.TranscribeReadyNotes),
		OrderPayRate:     ratio(t.PaidOrders, t.PurchaseOrderCreated),
		PaidConversion:   ratio(t.PaidOrders, t.UniqueVisitors),
	}
}

func ratio(numerator, denominator int64) float64 {
	if denominator <= 0 {
		return 0
	}
	value := float64(numerator) / float64(denominator)
	return math.Round(value*10000) / 10000
}

func strconvInt(value int64) string {
	return strconv.FormatInt(value, 10)
}
