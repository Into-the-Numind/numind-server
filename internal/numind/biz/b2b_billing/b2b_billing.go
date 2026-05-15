// Package b2b_billing implements the B2B monthly billing report for the
// credits-system Q1 B2B2C grant path.
//
// # Source strategy (T9 simplification)
//
// After T9 cleanup, billing always uses new_only mode — reading from
// membership_event rows (source='b2b_grant'), keyed by occurred_at.
//
// The legacy credit_package read path (getLegacyEvents) is kept as a
// deprecated stub for historical context. T11 will repurpose it to read
// the legacy_credit_package_archive_20260515 table for pre-cutover history.
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
	Source             string             `json:"source"` // always "new_only" post-T9 (legacy_only / cutover_split removed)
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

// grantEvent is the internal normalised representation for billing events.
type grantEvent struct {
	granterUserID uint
	childUserID   uint
	productType   string
	months        int
	amountCents   int64
	grantedAt     time.Time
}

// IB2BBillingBiz is the business interface.
type IB2BBillingBiz interface {
	GetBillingReport(ctx context.Context, month string) (*B2BBillingReport, error)
}

type b2bBillingBiz struct {
	ds          store.IStore
	cutoverDate time.Time
}

// New constructs a b2bBillingBiz without an explicit cutover date.
// Post-T9 (credits-cleanup): chooseSource always returns new_only regardless of
// cutoverDate, so this constructor and NewWithCutover are functionally equivalent.
// Retained as a separate entry point for backward compat with existing callers/tests.
func New(ds store.IStore) IB2BBillingBiz {
	return &b2bBillingBiz{ds: ds}
}

// NewWithCutover constructs a b2bBillingBiz with an explicit cutover date.
func NewWithCutover(ds store.IStore, cutover time.Time) IB2BBillingBiz {
	return &b2bBillingBiz{ds: ds, cutoverDate: cutover}
}

// monthRegex enforces strict YYYY-MM format (zero-padded month).
var monthRegex = regexp.MustCompile(`^(\d{4})-(0[1-9]|1[0-2])$`)

// chooseSource always returns "new_only" after T9 cleanup.
//
// T9 (credits-cleanup): the legacy_only and cutover_split branches have been
// removed. Prod cutover_date=2026-04-20 + 0 B2B business pre-cutover means
// all relevant months are in new_only territory. The unused parameters
// (monthStart, monthEnd, cutover) are retained to keep callers unchanged.
func chooseSource(_, _, _ time.Time) string {
	return "new_only"
}

// GetBillingReport assembles the monthly B2B grant report using the appropriate
// source strategy based on cutoverDate.
func (b *b2bBillingBiz) GetBillingReport(ctx context.Context, month string) (*B2BBillingReport, error) {
	start, end, err := parseMonth(month)
	if err != nil {
		return nil, err
	}

	// T9: chooseSource always returns new_only; legacy/cutover_split branches removed.
	source := chooseSource(start, end, b.cutoverDate)

	events, err := b.getNewEvents(ctx, start, end)
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
// getLegacyEvents: archive table reader (T11)
// --------------------------------------------------------------------------

// getLegacyEvents queries the legacy_credit_package_archive_20260515 table for
// B2B grant events (grant_source='b2b_grant') in the given [start, end) window.
//
// T11 (credits-cleanup): credit_package was archived to this table on 2026-05-15
// and then dropped. This function is wired to read the archive for any historical
// month that predates the cutover_date (2026-04-20). In practice prod had 0 B2B
// business before cutover, so this function will return empty for all pre-cutover
// months. It is preserved for completeness and future audit tooling.
//
// chooseSource() always returns "new_only" post-T9, so this function is not called
// from GetBillingReport. It remains available for:
//   - Direct audit queries via tooling
//   - Future reactivation of cutover_split mode if ever needed
//   - Historical month verification scripts
//
// TODO(T+future): Reactivate this code path when cutover_split mode is needed for
// backward-history queries (i.e. when chooseSource() returns "cutover_split"). At
// that point, wire it into GetBillingReport alongside getNewEvents and remove the
// linter suppression on the function signature below.
func (b *b2bBillingBiz) getLegacyEvents(ctx context.Context, start, end time.Time) ([]grantEvent, error) { //nolint:unused // preserved for historical audit tooling; not called by GetBillingReport post-T9
	type archiveRow struct {
		GranterUserID uint
		UserID        uint
		Type          string // trial / subscription
		ActivatedAt   time.Time
	}

	var rows []archiveRow
	if err := b.ds.DB().WithContext(ctx).
		Table("legacy_credit_package_archive_20260515").
		Select("granter_user_id, user_id, type, activated_at").
		Where("grant_source = ? AND activated_at >= ? AND activated_at < ?",
			"b2b_grant", start, end).
		Order("activated_at ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("getLegacyEvents: query archive: %w", err)
	}

	events := make([]grantEvent, 0, len(rows))
	for _, r := range rows {
		if r.GranterUserID == 0 {
			continue
		}
		// Reconstruct billing amounts from type (mirror of original getLegacyEvents logic).
		var amountCents int64
		var productType string
		var months int
		switch r.Type {
		case "trial":
			amountCents = 990 // ¥9.9
			productType = "trial"
		case "subscription":
			amountCents = 9900 // ¥99/month per Q1 grant
			productType = "monthly"
			months = 1
		default:
			continue // skip booster (self_purchase only)
		}
		events = append(events, grantEvent{
			granterUserID: r.GranterUserID,
			childUserID:   r.UserID,
			productType:   productType,
			months:        months,
			amountCents:   amountCents,
			grantedAt:     r.ActivatedAt,
		})
	}
	return events, nil
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
		})
	}
	return events, nil
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
