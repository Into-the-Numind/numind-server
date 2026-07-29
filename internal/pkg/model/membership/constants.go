package membership

const (
	SourceSelfPurchase = "self_purchase"
	SourceB2BGrant     = "b2b_grant"
	// SourceSystem marks events created by automatic system actions (e.g.
	// refund_lost events from RefundCreditsTx when no active pool can accept
	// the credits). Used to keep B2B and self-purchase audit reports clean.
	SourceSystem = "system"

	EventTypeTrialGranted   = "trial_granted"
	EventTypeSubGranted     = "sub_granted"
	EventTypeSubRenewed     = "sub_renewed"
	EventTypeBoosterGranted = "booster_granted"
	// EventTypeRefundLost records a credit refund that could not be returned to
	// any active pool (original source expired AND no active booster/cycle pool
	// to fallback to). Amount tracked in amount_cents; product_type is the
	// original DeductSource for auditing.
	EventTypeRefundLost = "refund_lost"

	ProductTypeTrial   = "trial"
	ProductTypeWeekly  = "weekly"
	ProductTypeMonthly = "monthly"
	ProductTypeBooster = "booster"
)

// Pricing constants for B2B billing report and grant amount attribution.
//
// Source of truth for the moxiaopai→parent settlement amounts. Used by:
//   - biz/membership/subscription.go to write membership_event.amount_cents on grant
//   - biz/b2b_billing to derive monthly settlement amounts (does not read
//     amount_cents directly; recomputes from product_type + months for safety
//     against historical data bugs).
//
// Pricing tiers (confirmed by product owner 2026-05-20):
//   - 1 month grant     = ¥99   (9900 fen)
//   - N month grant     = N × ¥99  (2 ≤ N ≤ 11)
//   - 12 month grant    = ¥949 (94900 fen)  — annual discount, NOT 12 × ¥99
//   - Weekly grant      = ¥25   (2500 fen), 7 days, 500 credits
//   - Trial grant       = ¥9.9  (990 fen)
//   - Booster           = excluded from settlement (self-purchase by user)
const (
	MonthlyPriceCents = 9900  // ¥99 per month
	AnnualPriceCents  = 94900 // ¥949 for a single 12-month batch grant
	WeeklyPriceCents  = 2500  // ¥25 per 7-day weekly grant
	TrialPriceCents   = 990   // ¥9.9 per trial grant

	MonthlyCycleCredits = 2000
	WeeklyCycleCredits  = 500
	WeeklyDurationDays  = 7
)

// PriceForMonths returns the settlement amount in cents for a subscription
// grant of the given month count. Centralised here so both the grant write
// path (subscription.go) and the billing read path (b2b_billing.go) stay
// consistent.
func PriceForMonths(months int) int64 {
	if months == 12 {
		return AnnualPriceCents
	}
	return int64(months) * MonthlyPriceCents
}
