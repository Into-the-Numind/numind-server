package membership

const (
	SourceSelfPurchase = "self_purchase"
	SourceB2BGrant     = "b2b_grant"

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
	ProductTypeMonthly = "monthly"
	ProductTypeBooster = "booster"
)
