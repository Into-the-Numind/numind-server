package membership

import (
	"context"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/pkg/errno"
	model "numind-server/internal/pkg/model/membership"
	"numind-server/internal/pkg/util"
)

// Pricing constants live in model/membership/constants.go
// (model.MonthlyPriceCents, model.AnnualPriceCents, model.PriceForMonths)
// so the grant write path and the b2b_billing read path share a single
// source of truth. The previous package-private `monthlyPriceCents = 9900`
// was removed in the b2b-billing-rules-rewrite hotfix (2026-05-20).

// GrantSubscriptionRequest carries the parameters for opening or renewing a
// subscription grant (B2B2C path).
type GrantSubscriptionRequest struct {
	// ParentUserID is the parent account initiating the grant. Must differ from
	// UserID (self-purchase is disabled for B2B2C).
	ParentUserID uint64
	// UserID is the target (child) user who receives the subscription.
	UserID uint64
	// ProductType must be "monthly" or "yearly" (spec §3.2).
	ProductType string
	// Months is the subscription duration in months [1, 12].
	Months int
	// GranterUserID is the granter identity recorded on the subscription and event.
	// Typically equals ParentUserID.
	GranterUserID *uint64
	// IdempotencyKey is an optional caller-supplied key enabling at-most-once
	// semantics. If the same key is replayed with the same UserID, the original
	// result is returned. If the same key is replayed with a different UserID,
	// ErrIdempotencyKeyConflict is returned.
	IdempotencyKey *string
	// Now overrides the current time (useful in tests). Zero-value means use
	// time.Now().UTC().
	Now time.Time
}

// GrantWeeklySubscriptionRequest carries the parameters for a 7-day weekly
// subscription grant (B2B2C path).
type GrantWeeklySubscriptionRequest struct {
	ParentUserID   uint64
	UserID         uint64
	GranterUserID  *uint64
	IdempotencyKey *string
	Now            time.Time
}

// GrantResult is returned on a successful (or idempotent replay) GrantOrRenewSubscription call.
type GrantResult struct {
	EventID              uint64
	SubscriptionID       uint64
	FirstStartedAt       time.Time
	CurrentStartedAt     time.Time
	ExpiresAt            time.Time
	TotalMonthsPurchased int
	Scenario             string // "new" / "renew" / "reopen"
	Replayed             bool   // true when idempotency key was replayed
}

// GrantOrRenewSubscription opens a new subscription or extends/reopens an existing
// one for req.UserID on behalf of req.ParentUserID (B2B2C grant path).
//
// Three scenarios (spec §3.2):
//   - new:    No subscription row exists yet → create.
//   - renew:  Subscription exists and is still active → extend.
//   - reopen: Subscription exists but has expired → start fresh cycle, clean stale
//     credit_cycle rows, keep first_started_at.
//
// Invariants:
//   - INV-4: expires_at == AnchorAddMonths(current_started_at, total_months_purchased)
//   - INV-5: first_started_at <= current_started_at
//
// Idempotency (§4.2): if IdempotencyKey is provided and was already processed for
// the same UserID, the original GrantResult is reconstructed and returned with
// Replayed=true. Same key + different UserID → ErrIdempotencyKeyConflict.
//
// Locking order (§4.3): subscription SELECT FOR UPDATE acquired before any write
// to prevent lost-update races on concurrent grants.
func (s *MembershipService) GrantOrRenewSubscription(ctx context.Context, req GrantSubscriptionRequest) (*GrantResult, error) {
	now := req.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.Truncate(time.Second)

	// ── Input validation ──────────────────────────────────────────────────────
	if err := validateSubscriptionInput(req); err != nil {
		return nil, err
	}

	// ── Idempotency pre-check (outside tx, read-only) ─────────────────────────
	if req.IdempotencyKey != nil && *req.IdempotencyKey != "" {
		existing, err := s.store.Events().GetByIdempotencyKey(ctx, *req.IdempotencyKey)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			if existing.UserID != req.UserID {
				return nil, errno.ErrIdempotencyKeyConflict
			}
			// Replay: reconstruct GrantResult from current subscription state.
			return s.replayGrantResult(ctx, existing)
		}
	}

	// ── Transactional write ────────────────────────────────────────────────────
	var result GrantResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock the subscription row (may not exist) — SELECT FOR UPDATE.
		sub, err := s.store.Subscriptions().GetForUpdate(ctx, tx, req.UserID)
		if err != nil {
			return err
		}

		// Determine scenario.
		var scenario string
		switch {
		case sub == nil:
			scenario = "new"
		case sub.ExpiresAt.After(now):
			scenario = "renew"
		default:
			scenario = "reopen"
		}

		// Map scenario to event_type. reopen uses sub_granted (no sub_reopened ENUM).
		eventTypeMap := map[string]string{
			"new":    model.EventTypeSubGranted,
			"renew":  model.EventTypeSubRenewed,
			"reopen": model.EventTypeSubGranted,
		}
		eventType := eventTypeMap[scenario]

		months := req.Months

		// ── Apply scenario ────────────────────────────────────────────────────

		switch scenario {
		case "new":
			expiresAt := util.AnchorAddMonths(now, months)
			newSub := &model.Subscription{
				UserID:               req.UserID,
				FirstStartedAt:       now,
				CurrentStartedAt:     now,
				ExpiresAt:            expiresAt,
				TotalMonthsPurchased: months,
				Source:               model.SourceB2BGrant,
				GranterUserID:        req.GranterUserID,
				CreatedAt:            now,
				UpdatedAt:            now,
			}
			if err := s.store.Subscriptions().Create(ctx, tx, newSub); err != nil {
				if isUniqueViolation(err, "uniq_sub_user_id") {
					// Concurrent grant race: re-read and treat as renew/reopen.
					// Safest: return a retryable error; caller can retry.
					return err
				}
				return err
			}
			sub = newSub

		case "renew":
			sub.TotalMonthsPurchased += months
			// INV-4: anchor on current_started_at (unchanged), add new total.
			sub.ExpiresAt = util.AnchorAddMonths(sub.CurrentStartedAt, sub.TotalMonthsPurchased)
			sub.UpdatedAt = now
			if err := s.store.Subscriptions().Update(ctx, tx, sub); err != nil {
				return err
			}

		case "reopen":
			// Delete stale credit cycles (cycle_end <= now).
			if err := s.store.CreditCycles().DeleteExpired(ctx, tx, req.UserID, now); err != nil {
				return err
			}
			// Reset subscription for new cycle; preserve first_started_at (INV-5).
			sub.CurrentStartedAt = now
			sub.TotalMonthsPurchased = months
			sub.ExpiresAt = util.AnchorAddMonths(now, months)
			sub.Source = model.SourceB2BGrant
			sub.GranterUserID = req.GranterUserID
			sub.UpdatedAt = now
			if err := s.store.Subscriptions().Update(ctx, tx, sub); err != nil {
				return err
			}
		}

		// ── Append audit event ────────────────────────────────────────────────
		months_uint8 := uint8(months)
		evt := &model.MembershipEvent{
			UserID:         req.UserID,
			EventType:      eventType,
			ProductType:    model.ProductTypeMonthly,
			Months:         &months_uint8,
			AmountCents:    model.PriceForMonths(months),
			Source:         model.SourceB2BGrant,
			GranterUserID:  req.GranterUserID,
			IdempotencyKey: req.IdempotencyKey,
			SubscriptionID: &sub.ID,
			OccurredAt:     now,
		}
		if err := s.store.Events().Create(ctx, tx, evt); err != nil {
			if isUniqueViolation(err, "uniq_event_idempotency_key") {
				return errno.ErrIdempotencyKeyConflict
			}
			return err
		}

		result = GrantResult{
			EventID:              evt.ID,
			SubscriptionID:       sub.ID,
			FirstStartedAt:       sub.FirstStartedAt,
			CurrentStartedAt:     sub.CurrentStartedAt,
			ExpiresAt:            sub.ExpiresAt,
			TotalMonthsPurchased: sub.TotalMonthsPurchased,
			Scenario:             scenario,
			Replayed:             false,
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return &result, nil
}

// GrantWeeklySubscription opens, renews, or reopens a 7-day weekly subscription.
//
// Weekly is a paid subscription plan:
//   - duration: 7 days
//   - cycle credits: 500
//   - amount: RMB 25
//
// Active monthly and weekly subscriptions are not stacked in this release. A
// same-plan weekly renewal extends expires_at by 7 days from the current expiry.
func (s *MembershipService) GrantWeeklySubscription(ctx context.Context, req GrantWeeklySubscriptionRequest) (*GrantResult, error) {
	now := req.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.Truncate(time.Second)

	if err := validateWeeklySubscriptionInput(req); err != nil {
		return nil, err
	}

	if req.IdempotencyKey != nil && *req.IdempotencyKey != "" {
		existing, err := s.store.Events().GetByIdempotencyKey(ctx, *req.IdempotencyKey)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			if existing.UserID != req.UserID {
				return nil, errno.ErrIdempotencyKeyConflict
			}
			return s.replayGrantResult(ctx, existing)
		}
	}

	var result GrantResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		sub, err := s.store.Subscriptions().GetForUpdate(ctx, tx, req.UserID)
		if err != nil {
			return err
		}

		var scenario string
		switch {
		case sub == nil:
			scenario = "new"
		case sub.ExpiresAt.After(now):
			if subscriptionPlanType(sub) != model.ProductTypeWeekly {
				return errno.ErrInvalidParameter.SetMessage("用户已有在期月度会员，暂不支持叠加周度会员")
			}
			scenario = "renew"
		default:
			scenario = "reopen"
		}

		eventTypeMap := map[string]string{
			"new":    model.EventTypeSubGranted,
			"renew":  model.EventTypeSubRenewed,
			"reopen": model.EventTypeSubGranted,
		}
		eventType := eventTypeMap[scenario]

		switch scenario {
		case "new":
			expiresAt := now.AddDate(0, 0, model.WeeklyDurationDays)
			newSub := &model.Subscription{
				UserID:               req.UserID,
				FirstStartedAt:       now,
				CurrentStartedAt:     now,
				ExpiresAt:            expiresAt,
				TotalMonthsPurchased: 0,
				PlanType:             model.ProductTypeWeekly,
				CycleCredits:         model.WeeklyCycleCredits,
				Source:               model.SourceB2BGrant,
				GranterUserID:        req.GranterUserID,
				CreatedAt:            now,
				UpdatedAt:            now,
			}
			if err := s.store.Subscriptions().Create(ctx, tx, newSub); err != nil {
				if isUniqueViolation(err, "uniq_sub_user_id") {
					return err
				}
				return err
			}
			sub = newSub

		case "renew":
			sub.ExpiresAt = sub.ExpiresAt.AddDate(0, 0, model.WeeklyDurationDays)
			sub.PlanType = model.ProductTypeWeekly
			sub.CycleCredits = model.WeeklyCycleCredits
			sub.Source = model.SourceB2BGrant
			sub.GranterUserID = req.GranterUserID
			sub.UpdatedAt = now
			if err := s.store.Subscriptions().Update(ctx, tx, sub); err != nil {
				return err
			}

		case "reopen":
			if err := s.store.CreditCycles().DeleteExpired(ctx, tx, req.UserID, now); err != nil {
				return err
			}
			sub.CurrentStartedAt = now
			sub.TotalMonthsPurchased = 0
			sub.ExpiresAt = now.AddDate(0, 0, model.WeeklyDurationDays)
			sub.PlanType = model.ProductTypeWeekly
			sub.CycleCredits = model.WeeklyCycleCredits
			sub.Source = model.SourceB2BGrant
			sub.GranterUserID = req.GranterUserID
			sub.UpdatedAt = now
			if err := s.store.Subscriptions().Update(ctx, tx, sub); err != nil {
				return err
			}
		}

		quantity := uint16(1)
		evt := &model.MembershipEvent{
			UserID:         req.UserID,
			EventType:      eventType,
			ProductType:    model.ProductTypeWeekly,
			Quantity:       &quantity,
			AmountCents:    model.WeeklyPriceCents,
			Source:         model.SourceB2BGrant,
			GranterUserID:  req.GranterUserID,
			IdempotencyKey: req.IdempotencyKey,
			SubscriptionID: &sub.ID,
			OccurredAt:     now,
		}
		if err := s.store.Events().Create(ctx, tx, evt); err != nil {
			if isUniqueViolation(err, "uniq_event_idempotency_key") {
				return errno.ErrIdempotencyKeyConflict
			}
			return err
		}

		result = GrantResult{
			EventID:              evt.ID,
			SubscriptionID:       sub.ID,
			FirstStartedAt:       sub.FirstStartedAt,
			CurrentStartedAt:     sub.CurrentStartedAt,
			ExpiresAt:            sub.ExpiresAt,
			TotalMonthsPurchased: sub.TotalMonthsPurchased,
			Scenario:             scenario,
			Replayed:             false,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// replayGrantResult reconstructs a GrantResult from the existing event and the
// current subscription state for idempotency replay.
func (s *MembershipService) replayGrantResult(ctx context.Context, evt *model.MembershipEvent) (*GrantResult, error) {
	sub, err := s.store.Subscriptions().Get(ctx, evt.UserID)
	if err != nil {
		return nil, err
	}

	res := &GrantResult{
		EventID:  evt.ID,
		Replayed: true,
	}
	if sub != nil {
		res.SubscriptionID = sub.ID
		res.FirstStartedAt = sub.FirstStartedAt
		res.CurrentStartedAt = sub.CurrentStartedAt
		res.ExpiresAt = sub.ExpiresAt
		res.TotalMonthsPurchased = sub.TotalMonthsPurchased
	}
	// Infer scenario from event_type.
	switch evt.EventType {
	case model.EventTypeSubRenewed:
		res.Scenario = "renew"
	default:
		res.Scenario = "new"
	}
	return res, nil
}

// validateSubscriptionInput validates the GrantSubscriptionRequest before any DB
// operations are performed.
//
// Rules:
//   - ParentUserID == UserID → ErrMembershipSelfPurchaseDisabled (B2B2C constraint)
//   - ProductType ∉ {"monthly", "yearly"} → ErrInvalidParameter
//   - Months ∉ [1, 12] → ErrInvalidParameter
//
// ParentUserID == 0 is a **sentinel value** indicating "bypass self-purchase check".
// Used by the B2B admin grant path (creditBiz.GrantMembership) where a parent
// account legitimately self-grants — parent accounts have parent_user_id IS NULL
// in the user table, so the request comes in with ParentUserID==ChildUserID and
// the biz layer rewrites it to 0 before calling here, to bypass the C-end
// self-purchase guard while still preserving the guard for all real C-end paths.
func validateSubscriptionInput(req GrantSubscriptionRequest) error {
	if req.ParentUserID == req.UserID {
		return errno.ErrMembershipSelfPurchaseDisabled
	}
	if req.ProductType != "monthly" && req.ProductType != "yearly" {
		return errno.ErrInvalidParameter
	}
	if req.Months < 1 || req.Months > 12 {
		return errno.ErrInvalidParameter
	}
	return nil
}

func validateWeeklySubscriptionInput(req GrantWeeklySubscriptionRequest) error {
	if req.ParentUserID == req.UserID {
		return errno.ErrMembershipSelfPurchaseDisabled
	}
	if req.UserID == 0 {
		return errno.ErrInvalidParameter
	}
	return nil
}
