// Package b2b_billing implements the B2B monthly billing report for the
// credits-system Q1 B2B2C grant path.
//
// # Cutover-date three-mode dispatch
//
// Billing has two audit trails for B2B grants:
//   - Legacy: credit_package rows (grant_source='b2b_grant'), keyed by activated_at
//   - New:    membership_event rows (source='b2b_grant'), keyed by occurred_at
//
// chooseSource(monthStart, monthEnd, cutover) selects one of three strategies:
//
//	legacy_only    — entire month precedes cutover date   → scan credit_package only
//	new_only       — entire month is at/after cutover     → scan membership_event only
//	cutover_split  — cutover falls inside the month       → scan both, dedupe by
//	                 composite key (new wins on conflict)
//
// # Amount attribution
//
// trial       → 990 fen (¥9.9)
// subscription→ 9900 fen (¥99/month; each package = 1 month under Q1 grant)
// booster     → 0 (self_purchase only; never reaches this path)
package b2b_billing

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"time"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
	membershipModel "numind-server/internal/pkg/model/membership"
)

// B2BBillingReport is the top-level response for a single month.
type B2BBillingReport struct {
	Month              string             `json:"month"`
	CutoverDate        time.Time          `json:"cutover_date"`
	Source             string             `json:"source"` // legacy_only / cutover_split / new_only
	ByParent           []ParentBillingRow `json:"by_parent"`
	TotalAmountCents   int64              `json:"total_amount_cents"`
	TotalEventsCount   int                `json:"total_events_count"`
	ActiveParentsCount int                `json:"active_parents_count"`
}

// ParentBillingRow aggregates every grant made by one parent in the month.
type ParentBillingRow struct {
	ParentUserID   uint          `json:"parent_user_id"`
	ParentUsername string        `json:"parent_username"`
	GrantsCount    int           `json:"grants_count"`
	AmountCents    int64         `json:"amount_cents"`
	Details        []GrantDetail `json:"details"`
}

// GrantDetail is one billing event rendered for UI display.
type GrantDetail struct {
	ChildUserID   uint      `json:"child_user_id"`
	ChildUsername string    `json:"child_username"`
	ProductType   string    `json:"product_type"` // trial / monthly
	Months        int       `json:"months"`       // 1 for subscription; 0 for trial
	AmountCents   int64     `json:"amount_cents"`
	GrantedAt     time.Time `json:"granted_at"`
}

// grantEvent is the internal normalised representation used by all three modes.
type grantEvent struct {
	granterUserID uint
	childUserID   uint
	productType   string
	months        int
	amountCents   int64
	grantedAt     time.Time
	// dedupeKey components (for cutover_split mode)
	dedupeKey string
	// source for conflict resolution in cutover_split: "legacy" or "new"
	dataSource string
}

// IB2BBillingBiz is the business interface.
type IB2BBillingBiz interface {
	GetBillingReport(ctx context.Context, month string) (*B2BBillingReport, error)
}

type b2bBillingBiz struct {
	ds          store.IStore
	cutoverDate time.Time
}

// New constructs a b2bBillingBiz with the given cutover date.
// cutoverDate is the moment from which membership_event is the authoritative source.
// Pass time.Time{} to default to legacy_only behaviour (for backward compat in
// tests that don't provide a cutover).
func New(ds store.IStore) IB2BBillingBiz {
	return &b2bBillingBiz{ds: ds}
}

// NewWithCutover constructs a b2bBillingBiz with an explicit cutover date.
func NewWithCutover(ds store.IStore, cutover time.Time) IB2BBillingBiz {
	return &b2bBillingBiz{ds: ds, cutoverDate: cutover}
}

// monthRegex enforces strict YYYY-MM format (zero-padded month).
var monthRegex = regexp.MustCompile(`^(\d{4})-(0[1-9]|1[0-2])$`)

// chooseSource returns the strategy name for the given month window vs cutover.
//
//	legacy_only   — monthStart >= cutover is NOT true AND monthEnd <= cutover
//	new_only      — monthStart >= cutover
//	cutover_split — cutover falls strictly inside (monthStart, monthEnd)
//
// Zero cutover (time.Time{}) always returns "legacy_only".
func chooseSource(monthStart, monthEnd, cutover time.Time) string {
	if cutover.IsZero() {
		return "legacy_only"
	}
	// ms >= cutover → entire month is in new territory
	if !monthStart.Before(cutover) {
		return "new_only"
	}
	// me <= cutover → entire month is in legacy territory
	if !monthEnd.After(cutover) {
		return "legacy_only"
	}
	// cutover falls strictly inside [ms, me)
	return "cutover_split"
}

// GetBillingReport assembles the monthly B2B grant report using the appropriate
// source strategy based on cutoverDate.
func (b *b2bBillingBiz) GetBillingReport(ctx context.Context, month string) (*B2BBillingReport, error) {
	start, end, err := parseMonth(month)
	if err != nil {
		return nil, err
	}

	source := chooseSource(start, end, b.cutoverDate)

	var events []grantEvent
	switch source {
	case "legacy_only":
		events, err = b.getLegacyEvents(ctx, start, end)
	case "new_only":
		events, err = b.getNewEvents(ctx, start, end)
	case "cutover_split":
		events, err = b.getCutoverSplitEvents(ctx, start, end, b.cutoverDate)
	}
	if err != nil {
		return nil, fmt.Errorf("GetBillingReport month=%s source=%s: %w", month, source, err)
	}

	return b.buildReport(ctx, month, source, events)
}

// buildReport converts a flat []grantEvent into a B2BBillingReport.
func (b *b2bBillingBiz) buildReport(ctx context.Context, month, source string, events []grantEvent) (*B2BBillingReport, error) {
	if len(events) == 0 {
		return &B2BBillingReport{
			Month:       month,
			CutoverDate: b.cutoverDate,
			Source:      source,
			ByParent:    []ParentBillingRow{},
		}, nil
	}

	// Collect user IDs for username lookup.
	userIDSet := make(map[uint]struct{}, len(events)*2)
	for _, e := range events {
		userIDSet[e.granterUserID] = struct{}{}
		userIDSet[e.childUserID] = struct{}{}
	}
	userIDs := make([]uint, 0, len(userIDSet))
	for id := range userIDSet {
		userIDs = append(userIDs, id)
	}

	var users []model.User
	if err := b.ds.DB().WithContext(ctx).
		Select("id, username").
		Where("id IN ?", userIDs).
		Find(&users).Error; err != nil {
		return nil, fmt.Errorf("buildReport: lookup usernames: %w", err)
	}
	usernameByID := make(map[uint]string, len(users))
	for _, u := range users {
		usernameByID[u.ID] = u.Username
	}

	// Group by parent.
	rowByParent := make(map[uint]*ParentBillingRow)
	var total int64

	for _, e := range events {
		row, ok := rowByParent[e.granterUserID]
		if !ok {
			row = &ParentBillingRow{
				ParentUserID:   e.granterUserID,
				ParentUsername: usernameByID[e.granterUserID],
				Details:        []GrantDetail{},
			}
			rowByParent[e.granterUserID] = row
		}
		row.GrantsCount++
		row.AmountCents += e.amountCents
		row.Details = append(row.Details, GrantDetail{
			ChildUserID:   e.childUserID,
			ChildUsername: usernameByID[e.childUserID],
			ProductType:   e.productType,
			Months:        e.months,
			AmountCents:   e.amountCents,
			GrantedAt:     e.grantedAt,
		})
		total += e.amountCents
	}

	rows := make([]ParentBillingRow, 0, len(rowByParent))
	for _, r := range rowByParent {
		rows = append(rows, *r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ParentUserID < rows[j].ParentUserID })

	return &B2BBillingReport{
		Month:              month,
		CutoverDate:        b.cutoverDate,
		Source:             source,
		ByParent:           rows,
		TotalAmountCents:   total,
		TotalEventsCount:   len(events),
		ActiveParentsCount: len(rows),
	}, nil
}

// --------------------------------------------------------------------------
// getLegacyEvents: credit_package source
// --------------------------------------------------------------------------

// getLegacyEvents queries credit_package rows where grant_source='b2b_grant'
// and activated_at falls in [start, end).
func (b *b2bBillingBiz) getLegacyEvents(ctx context.Context, start, end time.Time) ([]grantEvent, error) {
	var pkgs []model.CreditPackage
	if err := b.ds.DB().WithContext(ctx).
		Where("grant_source = ? AND activated_at >= ? AND activated_at < ?",
			model.GrantSourceB2BGrant, start, end).
		Order("activated_at ASC").
		Find(&pkgs).Error; err != nil {
		return nil, fmt.Errorf("getLegacyEvents: query packages: %w", err)
	}

	events := make([]grantEvent, 0, len(pkgs))
	for _, p := range pkgs {
		if p.GranterUserID == nil {
			continue // integrity issue; skip
		}
		amount := amountForPackage(p.Type)
		productType := productTypeForPackage(p.Type)
		months := 0
		if p.Type == model.CreditTypeSubscription {
			months = 1
		}
		events = append(events, grantEvent{
			granterUserID: *p.GranterUserID,
			childUserID:   p.UserID,
			productType:   productType,
			months:        months,
			amountCents:   amount,
			grantedAt:     p.ActivatedAt,
			dataSource:    "legacy",
			dedupeKey:     legacyDedupeKey(p),
		})
	}
	return events, nil
}

// legacyDedupeKey builds the composite deduplication key for a credit_package row.
func legacyDedupeKey(p model.CreditPackage) string {
	// truncate to-second precision to match membership_event composite key
	ts := p.ActivatedAt.UTC().Truncate(time.Second).Unix()
	months := -1
	if p.Type == model.CreditTypeSubscription {
		months = 1
	}
	// key: granter|child|ts|productType|months|quantity(-1 means N/A)
	granterID := uint64(0)
	if p.GranterUserID != nil {
		granterID = uint64(*p.GranterUserID)
	}
	return fmt.Sprintf("%d|%d|%d|%s|%d|-1", granterID, p.UserID, ts, productTypeForPackage(p.Type), months)
}

// --------------------------------------------------------------------------
// getNewEvents: membership_event source
// --------------------------------------------------------------------------

// getNewEvents queries membership_event rows where source='b2b_grant' and
// occurred_at falls in [start, end).
func (b *b2bBillingBiz) getNewEvents(ctx context.Context, start, end time.Time) ([]grantEvent, error) {
	var mevs []membershipModel.MembershipEvent
	if err := b.ds.DB().WithContext(ctx).
		Where("source = ? AND occurred_at >= ? AND occurred_at < ?",
			membershipModel.SourceB2BGrant, start, end).
		Order("occurred_at ASC").
		Find(&mevs).Error; err != nil {
		return nil, fmt.Errorf("getNewEvents: query membership_event: %w", err)
	}

	events := make([]grantEvent, 0, len(mevs))
	for _, ev := range mevs {
		if ev.GranterUserID == nil {
			continue
		}
		months := 0
		if ev.Months != nil {
			months = int(*ev.Months)
		}
		events = append(events, grantEvent{
			granterUserID: uint(*ev.GranterUserID),
			childUserID:   uint(ev.UserID),
			productType:   ev.ProductType,
			months:        months,
			amountCents:   ev.AmountCents,
			grantedAt:     ev.OccurredAt,
			dataSource:    "new",
			dedupeKey:     newEventDedupeKey(ev),
		})
	}
	return events, nil
}

// newEventDedupeKey builds the composite deduplication key for a MembershipEvent row.
func newEventDedupeKey(ev membershipModel.MembershipEvent) string {
	ts := ev.OccurredAt.UTC().Truncate(time.Second).Unix()
	months := -1
	if ev.Months != nil {
		months = int(*ev.Months)
	}
	quantity := -1
	if ev.Quantity != nil {
		quantity = int(*ev.Quantity)
	}
	granterID := uint64(0)
	if ev.GranterUserID != nil {
		granterID = *ev.GranterUserID
	}
	return fmt.Sprintf("%d|%d|%d|%s|%d|%d", granterID, ev.UserID, ts, ev.ProductType, months, quantity)
}

// --------------------------------------------------------------------------
// getCutoverSplitEvents: merge both sources, new wins on conflict
// --------------------------------------------------------------------------

// getCutoverSplitEvents queries both credit_package (for [start, cutover)) and
// membership_event (for [cutover, end)), then deduplicates by composite key
// with "new" winning over "legacy" on conflict.
func (b *b2bBillingBiz) getCutoverSplitEvents(ctx context.Context, start, end, cutover time.Time) ([]grantEvent, error) {
	// Legacy events: occurred before cutover
	legacyEvents, err := b.getLegacyEvents(ctx, start, cutover)
	if err != nil {
		return nil, fmt.Errorf("getCutoverSplitEvents: legacy leg: %w", err)
	}

	// New events: occurred from cutover onward
	newEvents, err := b.getNewEvents(ctx, cutover, end)
	if err != nil {
		return nil, fmt.Errorf("getCutoverSplitEvents: new leg: %w", err)
	}

	// Merge with deduplication: build a map keyed by dedupeKey.
	// Insert legacy first, then new overwrites on conflict.
	eventMap := make(map[string]grantEvent, len(legacyEvents)+len(newEvents))
	// Track insertion order for stable output.
	var keys []string

	for _, e := range legacyEvents {
		if _, exists := eventMap[e.dedupeKey]; !exists {
			keys = append(keys, e.dedupeKey)
		}
		eventMap[e.dedupeKey] = e
	}
	for _, e := range newEvents {
		if _, exists := eventMap[e.dedupeKey]; !exists {
			// new key not seen in legacy
			keys = append(keys, e.dedupeKey)
		}
		// new always overwrites legacy on same key
		eventMap[e.dedupeKey] = e
	}

	// Reconstruct ordered slice.
	merged := make([]grantEvent, 0, len(keys))
	for _, k := range keys {
		merged = append(merged, eventMap[k])
	}
	// Sort by grantedAt for deterministic output.
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].grantedAt.Before(merged[j].grantedAt)
	})
	return merged, nil
}

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// parseMonth validates and returns the half-open [start, end) bounds for "YYYY-MM".
func parseMonth(month string) (time.Time, time.Time, error) {
	m := monthRegex.FindStringSubmatch(month)
	if m == nil {
		return time.Time{}, time.Time{}, fmt.Errorf("GetBillingReport: invalid month format %q (expected YYYY-MM)", month)
	}
	y, _ := strconv.Atoi(m[1])
	mo, _ := strconv.Atoi(m[2])
	start := time.Date(y, time.Month(mo), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	return start, end, nil
}

// amountForPackage mirrors GetProductAmount pricing at the per-package level.
// trial → 990 (¥9.9 one-off)
// subscription → 9900 (¥99 per month; each package = 1 month under Q1 grant)
// booster → 0 (self_purchase only; never reaches this path but defensive)
func amountForPackage(pkgType string) int64 {
	switch pkgType {
	case model.CreditTypeTrial:
		return 990
	case model.CreditTypeSubscription:
		return 9900
	default:
		return 0
	}
}

// productTypeForPackage maps credit_package.type to the logical product label
// shown in the admin UI.
func productTypeForPackage(pkgType string) string {
	switch pkgType {
	case model.CreditTypeTrial:
		return model.ProductTypeTrial
	case model.CreditTypeSubscription:
		return model.ProductTypeMonthly
	default:
		return pkgType
	}
}
