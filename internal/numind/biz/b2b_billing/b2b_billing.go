// Package b2b_billing implements the B2B monthly billing report for the
// credits-system Q1 B2B2C grant path.
//
// Report semantics:
//   - Pull all credit_package rows where grant_source='b2b_grant' AND
//     activated_at falls in the requested month [YYYY-MM-01, YYYY-MM+1-01).
//   - Group by granter_user_id (= parent); join user for usernames.
//   - Amount = per-package unit price (trial=990, subscription=9900 per month).
//     This mirrors GetProductAmount and gives a deterministic per-package
//     attribution that doesn't rely on reassembling the original grant call.
//
// Output consumed by admin UI + offline reconciliation for monthly
// corporate invoicing.
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
)

// B2BBillingReport is the top-level response for a single month.
type B2BBillingReport struct {
	Month            string             `json:"month"`
	ByParent         []ParentBillingRow `json:"by_parent"`
	TotalAmountCents int64              `json:"total_amount_cents"`
}

// ParentBillingRow aggregates every grant made by one parent in the month.
type ParentBillingRow struct {
	ParentUserID   uint          `json:"parent_user_id"`
	ParentUsername string        `json:"parent_username"`
	GrantsCount    int           `json:"grants_count"`
	AmountCents    int64         `json:"amount_cents"`
	Details        []GrantDetail `json:"details"`
}

// GrantDetail is one credit_package row rendered for UI display.
type GrantDetail struct {
	ChildUserID   uint      `json:"child_user_id"`
	ChildUsername string    `json:"child_username"`
	ProductType   string    `json:"product_type"` // trial / monthly (derived from package.type)
	Months        int       `json:"months"`       // always 1 per package for subscription; 0 for trial
	AmountCents   int64     `json:"amount_cents"`
	GrantedAt     time.Time `json:"granted_at"`
}

// IB2BBillingBiz is the business interface.
type IB2BBillingBiz interface {
	GetBillingReport(ctx context.Context, month string) (*B2BBillingReport, error)
}

type b2bBillingBiz struct {
	ds store.IStore
}

// New constructs a b2bBillingBiz.
func New(ds store.IStore) IB2BBillingBiz {
	return &b2bBillingBiz{ds: ds}
}

// monthRegex enforces strict YYYY-MM format (zero-padded month).
var monthRegex = regexp.MustCompile(`^(\d{4})-(0[1-9]|1[0-2])$`)

// GetBillingReport assembles the monthly B2B grant report.
func (b *b2bBillingBiz) GetBillingReport(ctx context.Context, month string) (*B2BBillingReport, error) {
	start, end, err := parseMonth(month)
	if err != nil {
		return nil, err
	}

	// Query all b2b_grant packages activated in [start, end).
	var pkgs []model.CreditPackage
	if err := b.ds.DB().WithContext(ctx).
		Where("grant_source = ? AND activated_at >= ? AND activated_at < ?",
			model.GrantSourceB2BGrant, start, end).
		Order("activated_at ASC").
		Find(&pkgs).Error; err != nil {
		return nil, fmt.Errorf("GetBillingReport: query packages: %w", err)
	}

	if len(pkgs) == 0 {
		return &B2BBillingReport{Month: month, ByParent: []ParentBillingRow{}}, nil
	}

	// Collect user IDs for username lookup (parents + children).
	userIDSet := make(map[uint]struct{}, len(pkgs)*2)
	for _, p := range pkgs {
		if p.GranterUserID != nil {
			userIDSet[*p.GranterUserID] = struct{}{}
		}
		userIDSet[p.UserID] = struct{}{}
	}
	userIDs := make([]uint, 0, len(userIDSet))
	for id := range userIDSet {
		userIDs = append(userIDs, id)
	}

	// Resolve usernames.
	var users []model.User
	if err := b.ds.DB().WithContext(ctx).
		Select("id, username").
		Where("id IN ?", userIDs).
		Find(&users).Error; err != nil {
		return nil, fmt.Errorf("GetBillingReport: lookup usernames: %w", err)
	}
	usernameByID := make(map[uint]string, len(users))
	for _, u := range users {
		usernameByID[u.ID] = u.Username
	}

	// Group packages by granter.
	rowByParent := make(map[uint]*ParentBillingRow)
	var total int64

	for _, p := range pkgs {
		if p.GranterUserID == nil {
			// Defensive: grant_source=b2b_grant must have granter_user_id.
			// Skip + ignore (integrity issue to surface elsewhere).
			continue
		}
		granter := *p.GranterUserID
		amount := amountForPackage(p.Type)
		productType := productTypeForPackage(p.Type)
		months := 0
		if p.Type == model.CreditTypeSubscription {
			months = 1
		}

		row, ok := rowByParent[granter]
		if !ok {
			row = &ParentBillingRow{
				ParentUserID:   granter,
				ParentUsername: usernameByID[granter],
				Details:        []GrantDetail{},
			}
			rowByParent[granter] = row
		}
		row.GrantsCount++
		row.AmountCents += amount
		row.Details = append(row.Details, GrantDetail{
			ChildUserID:   p.UserID,
			ChildUsername: usernameByID[p.UserID],
			ProductType:   productType,
			Months:        months,
			AmountCents:   amount,
			GrantedAt:     p.ActivatedAt,
		})
		total += amount
	}

	// Deterministic output: sort by ParentUserID ascending.
	rows := make([]ParentBillingRow, 0, len(rowByParent))
	for _, r := range rowByParent {
		rows = append(rows, *r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ParentUserID < rows[j].ParentUserID })

	return &B2BBillingReport{
		Month:            month,
		ByParent:         rows,
		TotalAmountCents: total,
	}, nil
}

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
// shown in the admin UI (matches the Order.ProductType constants where they exist).
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
