// Package b2b_billing implements the B2B monthly billing report for the
// credits-system Q1 B2B2C grant path.
//
// # Attribution strategy (post b2b-billing-action-month hotfix, 2026-06-01)
//
// Settlement is computed PER GRANT ACTION, attributed to the natural month the
// action occurred. Monthly amounts are recomputed from months via PriceForMonths
// (never read from membership_event.amount_cents), robust against historical
// data bugs (doubling-era amount=0 rows, annual stored as 12×¥99, etc.).
// Weekly events are fixed-price single-period actions.
//
// This replaced the earlier subscription-state "Rule A / Rule B" approach, whose
// Rule A "Case 1" attributed the cumulative subscription.total_months_purchased
// to the first_started_at month. Because total grows on later renewals (and resets
// on reopen), that double-counted months actually granted in later months — e.g.
// an account whose April original is a migration placeholder and that renewed in
// May was billed 2 months in April AND the renewal again in May. See computeBilling.
//
// Action sources (see computeBilling for detail):
//   - Real (non-migration) sub_granted / sub_renewed events, billed in their
//     occurred_at month at PriceForMonths(event.months) for monthly or fixed
//     weekly price for weekly.
//   - Migrated packages: the 2026-04-30 migration spread each original package
//     into 1-month-per-event placeholders (a 12-month annual → 1 sub_granted + 11
//     future sub_renewed). They are collapsed back into ONE action of N months
//     (N = the user's migration event count) in the sub_granted month.
//
// Trials are handled separately by querying trial_grant directly (granted_at
// in the month, source = b2b_grant). Trial events in membership_event are
// not consulted because trial_grant.granted_at is more reliable.
//
// Boosters are completely excluded from settlement: they are self-purchased
// directly by users (扫码付款) and never owed by the parent account.
//
// # Pricing
//
// Pricing constants live in internal/pkg/model/membership/constants.go.
// The PriceForMonths helper centralises the annual-discount branch (¥949 for
// months==12) so both the grant write path and this read path stay aligned.
package b2b_billing

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"time"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
	membershipModel "numind-server/internal/pkg/model/membership"

	"gorm.io/gorm"
)

// migrationKeyPrefix identifies idempotency_key values written by the
// 2026-04-30 credit_package → membership_event migration script. Events with
// this prefix are placeholders (future-dated for annual subscriptions or
// historical fill-ins) and must NEVER count toward monthly settlement.
const migrationKeyPrefix = "migration-%"

// ErrNotParentAccount is returned by GetBillingReportForParent when the caller
// is not a parent account (User.ParentUserID != nil). Controllers map this to 403.
var ErrNotParentAccount = errors.New("b2b_billing: not a parent account")

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
	ChildNickname string    `json:"child_nickname"` // populated for the parent self-service view; empty on the admin report
	ProductType   string    `json:"product_type"`   // trial / weekly / monthly
	Months        int       `json:"months"`         // grant batch size for monthly; 0 for trial/weekly
	AmountCents   int64     `json:"amount_cents"`
	GrantedAt     time.Time `json:"granted_at"`
}

// ParentBillingReport is one parent's own monthly settlement view (self-service).
// Unlike B2BBillingReport it is flat (no by-parent grouping) since it is always
// scoped to a single parent.
type ParentBillingReport struct {
	Month            string        `json:"month"`
	ParentUserID     uint          `json:"parent_user_id"`
	GrantsCount      int           `json:"grants_count"`
	TotalAmountCents int64         `json:"total_amount_cents"`
	Details          []GrantDetail `json:"details"`
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
	GetBillingReportForParent(ctx context.Context, month string, parentUserID uint) (*ParentBillingReport, error)
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

// IsValidMonth reports whether s is a valid zero-padded YYYY-MM month string.
// Exported so callers (e.g. controllers) can pre-validate against the single
// canonical pattern instead of duplicating the regex.
func IsValidMonth(s string) bool {
	return monthRegex.MatchString(s)
}

// chooseSource always returns "new_only" after T9 cleanup.
//
// T9 (credits-cleanup): the legacy_only and cutover_split branches have been
// removed. Prod cutover_date=2026-04-20 + 0 B2B business pre-cutover means
// all relevant months are in new_only territory. The unused parameters
// (monthStart, monthEnd, cutover) are retained to keep callers unchanged.
func chooseSource(_, _, _ time.Time) string {
	return "new_only"
}

// GetBillingReport assembles the monthly B2B settlement report.
//
// Returns the per-parent breakdown of moxiaopai-owed amounts for memberships
// granted to child accounts in the given month. See package doc for the
// per-action-month attribution strategy.
func (b *b2bBillingBiz) GetBillingReport(ctx context.Context, month string) (*B2BBillingReport, error) {
	start, end, err := parseMonth(month)
	if err != nil {
		return nil, err
	}

	source := chooseSource(start, end, b.cutoverDate)

	events, err := b.computeBilling(ctx, start, end, nil)
	if err != nil {
		return nil, fmt.Errorf("GetBillingReport month=%s source=%s: %w", month, source, err)
	}

	return b.buildReport(ctx, month, source, events)
}

// computeBilling assembles settlement actions for the half-open [start, end)
// month window, attributing every grant ACTION to the natural month it occurred.
//
// # Standard (product owner, 2026-06)
//
// Each grant action is billed in its own month at PriceForMonths(action months)
// — 1-11 months → months×¥99, 12 months → ¥949. There is deliberately NO
// subscription-state fallback: subscription.total_months_purchased is a
// cumulative value (it grows on later renewals / resets on reopen), so the old
// "Rule A Case 1" that attributed it to the first_started_at month double-counted
// months actually granted in later months (the b2b-billing-action-month bug).
//
// Three action sources:
//
//   - A. Real (non-migration) sub_granted / sub_renewed events whose occurred_at
//     ∈ [start, end): one action each. Monthly events use
//     PriceForMonths(event.months); weekly events use WeeklyPriceCents.
//   - B. Migrated packages: the 2026-04-30 credit_package → membership_event
//     migration spread each original package into 1-month-per-event placeholders
//     (a 12-month annual became 1 sub_granted + 11 future sub_renewed, all
//     months=1). Collapse each migrated user's placeholders back into ONE action
//     of N months (N = count of that user's migration sub_granted+sub_renewed
//     events) attributed to the migration sub_granted month, priced
//     PriceForMonths(N). The spread sub_renewed placeholders are never billed on
//     their own.
//   - C. Trials: trial_grant.granted_at ∈ [start, end) → TrialPriceCents each.
//     Boosters are excluded (self-purchased, not parent-owed).
//
// granterUserID scopes to one parent (nil = all parents / admin report); when set
// an extra `granter_user_id = ?` predicate is ANDed onto every query.
func (b *b2bBillingBiz) computeBilling(ctx context.Context, start, end time.Time, granterUserID *uint) ([]grantEvent, error) {
	var events []grantEvent

	// ── A. Real (non-migration) grant actions in the month ──────────────────────
	realQ := b.ds.DB().WithContext(ctx).
		Where("source = ? AND event_type IN ? AND occurred_at >= ? AND occurred_at < ? AND granter_user_id IS NOT NULL AND (idempotency_key IS NULL OR idempotency_key NOT LIKE ?)",
			membershipModel.SourceB2BGrant,
			[]string{membershipModel.EventTypeSubGranted, membershipModel.EventTypeSubRenewed},
			start, end, migrationKeyPrefix)
	if granterUserID != nil {
		realQ = realQ.Where("granter_user_id = ?", *granterUserID)
	}
	var realEvts []membershipModel.MembershipEvent
	if err := realQ.Order("occurred_at ASC").Find(&realEvts).Error; err != nil {
		return nil, fmt.Errorf("computeBilling: query real grant actions: %w", err)
	}
	for i := range realEvts {
		ev := &realEvts[i]
		if ev.GranterUserID == nil {
			continue
		}
		months := 0
		if ev.Months != nil {
			months = int(*ev.Months)
		}
		amountCents := int64(0)
		switch ev.ProductType {
		case membershipModel.ProductTypeWeekly:
			amountCents = membershipModel.WeeklyPriceCents
		case membershipModel.ProductTypeMonthly:
			if months == 0 {
				// Defensive: a monthly sub_granted/sub_renewed always has months>=1
				// (write path guarantees it). Skip any data-quality zero so it never
				// adds a ¥0 ghost row that would inflate grants_count.
				continue
			}
			amountCents = membershipModel.PriceForMonths(months)
		default:
			continue
		}
		events = append(events, grantEvent{
			granterUserID: uint(*ev.GranterUserID),
			childUserID:   uint(ev.UserID),
			productType:   ev.ProductType,
			months:        months,
			amountCents:   amountCents,
			grantedAt:     ev.OccurredAt,
		})
	}

	// ── B. Migrated packages, collapsed to one action in the sub_granted month ──
	migQ := b.ds.DB().WithContext(ctx).
		Where("source = ? AND event_type = ? AND idempotency_key LIKE ? AND occurred_at >= ? AND occurred_at < ? AND granter_user_id IS NOT NULL",
			membershipModel.SourceB2BGrant, membershipModel.EventTypeSubGranted, migrationKeyPrefix, start, end)
	if granterUserID != nil {
		migQ = migQ.Where("granter_user_id = ?", *granterUserID)
	}
	var migGrants []membershipModel.MembershipEvent
	if err := migQ.Order("occurred_at ASC").Find(&migGrants).Error; err != nil {
		return nil, fmt.Errorf("computeBilling: query migration grants: %w", err)
	}
	if len(migGrants) > 0 {
		// Batch-count each migrated user's placeholder months (= original package size).
		userIDs := make([]uint64, 0, len(migGrants))
		for i := range migGrants {
			userIDs = append(userIDs, migGrants[i].UserID)
		}
		type migCount struct {
			UserID uint64
			N      int
		}
		var counts []migCount
		if err := b.ds.DB().WithContext(ctx).
			Model(&membershipModel.MembershipEvent{}).
			Select("user_id, COUNT(*) AS n").
			Where("user_id IN ? AND source = ? AND event_type IN ? AND idempotency_key LIKE ? AND granter_user_id IS NOT NULL",
				userIDs, membershipModel.SourceB2BGrant,
				[]string{membershipModel.EventTypeSubGranted, membershipModel.EventTypeSubRenewed},
				migrationKeyPrefix).
			Group("user_id").
			Scan(&counts).Error; err != nil {
			return nil, fmt.Errorf("computeBilling: count migration months: %w", err)
		}
		monthsByUser := make(map[uint64]int, len(counts))
		for _, c := range counts {
			monthsByUser[c.UserID] = c.N
		}
		for i := range migGrants {
			ev := &migGrants[i]
			if ev.GranterUserID == nil {
				continue
			}
			months := monthsByUser[ev.UserID]
			if months < 1 {
				months = 1 // defensive: at least the sub_granted itself
			}
			events = append(events, grantEvent{
				granterUserID: uint(*ev.GranterUserID),
				childUserID:   uint(ev.UserID),
				productType:   membershipModel.ProductTypeMonthly,
				months:        months,
				amountCents:   membershipModel.PriceForMonths(months),
				grantedAt:     ev.OccurredAt,
			})
		}
	}

	// ── C. Trial path ───────────────────────────────────────────────────────────
	trialQ := b.ds.DB().WithContext(ctx).
		Where("source = ? AND granted_at >= ? AND granted_at < ? AND granter_user_id IS NOT NULL",
			membershipModel.SourceB2BGrant, start, end)
	if granterUserID != nil {
		trialQ = trialQ.Where("granter_user_id = ?", *granterUserID)
	}
	var trials []membershipModel.TrialGrant
	if err := trialQ.Find(&trials).Error; err != nil {
		return nil, fmt.Errorf("computeBilling: query trials: %w", err)
	}
	for i := range trials {
		t := &trials[i]
		if t.GranterUserID == nil {
			continue
		}
		events = append(events, grantEvent{
			granterUserID: uint(*t.GranterUserID),
			childUserID:   uint(t.UserID),
			productType:   membershipModel.ProductTypeTrial,
			months:        0,
			amountCents:   membershipModel.TrialPriceCents,
			grantedAt:     t.GrantedAt,
		})
	}

	// Stable ordering for deterministic output: by grantedAt ASC.
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].grantedAt.Before(events[j].grantedAt)
	})

	return events, nil
}

// lookupUsernames returns id→username for the given user IDs (Select id,username only).
func (b *b2bBillingBiz) lookupUsernames(ctx context.Context, ids []uint) (map[uint]string, error) {
	out := make(map[uint]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var users []model.User
	if err := b.ds.DB().WithContext(ctx).
		Select("id, username").Where("id IN ?", ids).Find(&users).Error; err != nil {
		return nil, fmt.Errorf("lookupUsernames: %w", err)
	}
	for _, u := range users {
		out[u.ID] = u.Username
	}
	return out, nil
}

// userDisplay carries the display fields (username + nickname) for one user.
type userDisplay struct {
	Username string
	Nickname string
}

// lookupUserDisplay returns id→{username,nickname} for the given user IDs.
// Dedicated to the parent self-service report, which renders a nickname column;
// the admin report keeps lookupUsernames (username-only) so its billing query
// stays byte-identical.
func (b *b2bBillingBiz) lookupUserDisplay(ctx context.Context, ids []uint) (map[uint]userDisplay, error) {
	out := make(map[uint]userDisplay, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var users []model.User
	if err := b.ds.DB().WithContext(ctx).
		Select("id, username, nickname").Where("id IN ?", ids).Find(&users).Error; err != nil {
		return nil, fmt.Errorf("lookupUserDisplay: %w", err)
	}
	for _, u := range users {
		out[u.ID] = userDisplay{Username: u.Username, Nickname: u.Nickname}
	}
	return out, nil
}

// GetBillingReportForParent assembles the self-service monthly settlement view
// for one parent account. Reuses computeBilling with a granter filter so the
// amounts are byte-for-byte consistent with the admin settlement report.
//
// Returns ErrNotParentAccount if parentUserID is not a parent account.
func (b *b2bBillingBiz) GetBillingReportForParent(ctx context.Context, month string, parentUserID uint) (*ParentBillingReport, error) {
	start, end, err := parseMonth(month)
	if err != nil {
		return nil, err
	}

	// Parent-account gate: query parent_user_id only (matches package query style;
	// avoids SELECT * coupling to the full user schema).
	var row struct {
		ParentUserID *uint `gorm:"column:parent_user_id"`
	}
	if err := b.ds.DB().WithContext(ctx).
		Table("user").Select("parent_user_id").
		Where("id = ?", parentUserID).
		Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotParentAccount
		}
		return nil, fmt.Errorf("GetBillingReportForParent: lookup user %d: %w", parentUserID, err)
	}
	if row.ParentUserID != nil {
		return nil, ErrNotParentAccount
	}

	events, err := b.computeBilling(ctx, start, end, &parentUserID)
	if err != nil {
		return nil, fmt.Errorf("GetBillingReportForParent month=%s parent=%d: %w", month, parentUserID, err)
	}

	report := &ParentBillingReport{
		Month:        month,
		ParentUserID: parentUserID,
		Details:      []GrantDetail{},
	}
	if len(events) == 0 {
		return report, nil
	}

	childIDSet := make(map[uint]struct{}, len(events))
	for _, e := range events {
		childIDSet[e.childUserID] = struct{}{}
	}
	childIDs := make([]uint, 0, len(childIDSet))
	for id := range childIDSet {
		childIDs = append(childIDs, id)
	}
	displayByID, err := b.lookupUserDisplay(ctx, childIDs)
	if err != nil {
		return nil, fmt.Errorf("GetBillingReportForParent: %w", err)
	}
	for _, e := range events {
		report.Details = append(report.Details, GrantDetail{
			ChildUserID:   e.childUserID,
			ChildUsername: displayByID[e.childUserID].Username,
			ChildNickname: displayByID[e.childUserID].Nickname,
			ProductType:   e.productType,
			Months:        e.months,
			AmountCents:   e.amountCents,
			GrantedAt:     e.grantedAt,
		})
		report.TotalAmountCents += e.amountCents
	}
	report.GrantsCount = len(report.Details)
	return report, nil
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

	usernameByID, err := b.lookupUsernames(ctx, userIDs)
	if err != nil {
		return nil, fmt.Errorf("buildReport: %w", err)
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
// getLegacyEvents: archive table reader (preserved for historical audit tooling)
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
// that point, wire it into GetBillingReport alongside computeBilling and remove the
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
		// Note: legacy table predates annual pricing; all subscriptions there were
		// 1-month grants priced at ¥99.
		var amountCents int64
		var productType string
		var months int
		switch r.Type {
		case "trial":
			amountCents = membershipModel.TrialPriceCents
			productType = membershipModel.ProductTypeTrial
		case "subscription":
			amountCents = membershipModel.MonthlyPriceCents
			productType = membershipModel.ProductTypeMonthly
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
// Helpers
// --------------------------------------------------------------------------

// parseMonth validates and returns the half-open [start, end) bounds for "YYYY-MM".
func parseMonth(month string) (time.Time, time.Time, error) {
	m := monthRegex.FindStringSubmatch(month)
	if m == nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parseMonth: invalid month format %q (expected YYYY-MM)", month)
	}
	y, _ := strconv.Atoi(m[1])
	mo, _ := strconv.Atoi(m[2])
	start := time.Date(y, time.Month(mo), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	return start, end, nil
}
