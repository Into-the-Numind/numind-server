package membership

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"numind-server/internal/pkg/errno"
	model "numind-server/internal/pkg/model/membership"
	"numind-server/internal/pkg/util"
)

const (
	// cycleCredits is the number of credits granted per monthly billing cycle.
	cycleCredits = 2000
)

// DeductionResult describes how many credits were drawn from each pool during a
// DeductCredits call. All values are non-negative; their sum equals amount.
type DeductionResult struct {
	// FromTrial is the number of credits deducted from the trial grant pool.
	FromTrial int64
	// FromCycle is the number of credits deducted from the current billing cycle pool.
	FromCycle int64
	// FromBooster is the number of credits deducted from the booster balance pool.
	FromBooster int64
	// Items describes per-pool allocation in deduction order. Populated by
	// DeductCreditsTx (T3 onwards) so callers can write per-source
	// credit_reservation_item rows for accurate refund routing.
	Items []DeductItem
}

// DeductSource identifies which pool a deduction or refund targets.
type DeductSource string

const (
	DeductSourceTrial   DeductSource = "trial"
	DeductSourceCycle   DeductSource = "cycle"
	DeductSourceBooster DeductSource = "booster"
)

// DeductItem records a single per-pool allocation within a DeductionResult.
type DeductItem struct {
	SourceType DeductSource
	SourceID   uint64 // credit_cycle.id / user_booster_balance.user_id / trial_grant.id
	Amount     int64
}

// ensureCurrentCycle lazily creates or fetches the credit cycle row for the
// billing month that contains txNow, anchored on sub.CurrentStartedAt (§3.4).
//
// Algorithm:
//  1. Binary-search the number of complete months elapsed since CurrentStartedAt
//     (monthsSinceStart) so that cycleStart ≤ txNow < cycleEnd.
//  2. cycleStart = AnchorAddMonths(sub.CurrentStartedAt, monthsSinceStart)
//  3. cycleEnd   = min(AnchorAddMonths(sub.CurrentStartedAt, monthsSinceStart+1), sub.ExpiresAt)
//  4. Defensive check: if txNow ≥ cycleEnd → subscription has effectively expired.
//  5. INSERT … ON CONFLICT DO NOTHING (idempotent).
//  6. SELECT FOR UPDATE on (user_id, cycleStart) and return the authoritative row.
//
// The function must be called inside an open transaction (tx).
func (s *MembershipService) ensureCurrentCycle(ctx context.Context, tx *gorm.DB, sub *model.Subscription, txNow time.Time) (*model.CreditCycle, error) {
	// Step 1: compute monthsSinceStart via simple linear search.
	// In the worst case (12-month subscription) this loops at most 12 times.
	anchor := sub.CurrentStartedAt
	monthsSinceStart := 0
	for {
		nextStart := util.AnchorAddMonths(anchor, monthsSinceStart+1)
		if !txNow.Before(nextStart) {
			// txNow >= nextStart → not yet in this cycle, advance
			monthsSinceStart++
			// Safety: cap at TotalMonthsPurchased to avoid infinite loop on
			// programming errors (shouldn't happen given defensive check below).
			if monthsSinceStart >= sub.TotalMonthsPurchased {
				break
			}
		} else {
			break
		}
	}

	// Step 2-3: compute cycle boundaries.
	cycleStart := util.AnchorAddMonths(anchor, monthsSinceStart)
	cycleEndRaw := util.AnchorAddMonths(anchor, monthsSinceStart+1)
	cycleEnd := cycleEndRaw
	if sub.ExpiresAt.Before(cycleEndRaw) {
		cycleEnd = sub.ExpiresAt
	}

	// Step 4: defensive check.
	if !txNow.Before(cycleEnd) {
		return nil, errno.ErrSubscriptionExpired
	}

	// Step 5: INSERT … ON CONFLICT DO NOTHING.
	now := time.Now().UTC()
	candidate := &model.CreditCycle{
		UserID:           sub.UserID,
		SubscriptionID:   sub.ID,
		CycleStart:       cycleStart,
		CycleEnd:         cycleEnd,
		CreditsGranted:   cycleCredits,
		CreditsRemaining: cycleCredits,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := s.store.CreditCycles().InsertOrIgnore(ctx, tx, candidate); err != nil {
		return nil, fmt.Errorf("ensureCurrentCycle: InsertOrIgnore: %w", err)
	}

	// Step 6: SELECT FOR UPDATE to obtain the authoritative row (may have been
	// created by a concurrent request; InsertOrIgnore silently lost the race).
	cycle, err := s.store.CreditCycles().GetByUserAndStartForUpdate(ctx, tx, sub.UserID, cycleStart)
	if err != nil {
		return nil, fmt.Errorf("ensureCurrentCycle: GetByUserAndStartForUpdate: %w", err)
	}
	if cycle == nil {
		// Should never happen: we just inserted or there was an existing row.
		return nil, fmt.Errorf("ensureCurrentCycle: cycle row not found after InsertOrIgnore for user_id=%d cycle_start=%s", sub.UserID, cycleStart)
	}
	return cycle, nil
}

// DeductCredits deducts amount credits from userID using the three-pool
// priority ordering defined in spec §3.5:
//
//  1. Trial grant (if active)
//  2. Current billing cycle (if active subscription, via ensureCurrentCycle)
//  3. Booster balance (if sub or trial is active — booster frozen otherwise)
//
// Locking order (§4.1, alphabetical by table name):
//
//	credit_cycle → subscription (read-only, no lock needed at deduct time) →
//	trial_grant → user_booster_balance
//
// The method opens its own transaction. Returns ErrInsufficientCredits when
// total available credits < amount.
func (s *MembershipService) DeductCredits(ctx context.Context, userID uint64, amount int64) (*DeductionResult, error) {
	var deduction *DeductionResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r, e := s.DeductCreditsTx(ctx, tx, userID, amount, time.Now().UTC())
		deduction = r
		return e
	})
	if err != nil {
		return nil, err
	}
	return deduction, nil
}

// DeductCreditsTx performs the same deduction as DeductCredits but inside a
// caller-managed transaction. Required when the caller (e.g. credit.Reserve)
// needs to atomically combine the deduction with other writes (reservation
// inserts) in one tx — MySQL doesn't support nested transactions, so we expose
// a tx-aware variant.
//
// Returns DeductionResult with FromTrial/FromCycle/FromBooster aggregates AND
// per-pool Items (SourceType + SourceID + Amount) so the caller can record
// detailed per-source reservation_item rows for accurate refund routing.
//
// Caller is responsible for tx lifecycle (open + commit/rollback). This
// function only writes; on ErrInsufficientCredits the caller must rollback.
func (s *MembershipService) DeductCreditsTx(ctx context.Context, tx *gorm.DB, userID uint64, amount int64, now time.Time) (*DeductionResult, error) {
	if amount <= 0 {
		return nil, errno.ErrInvalidParameter
	}

	// ── Pre-read subscription state (no lock; informational only) ──────────
	sub, err := s.store.Subscriptions().Get(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("DeductCreditsTx: get subscription: %w", err)
	}
	subActive := sub != nil && sub.ExpiresAt.After(now)

	// ── Pre-read trial state (no lock; informational only) ─────────────────
	trial, err := s.store.TrialGrants().Get(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("DeductCreditsTx: get trial: %w", err)
	}
	trialActive := trial != nil && trial.ExpiresAt.After(now) && trial.CreditsRemaining > 0

	// ── Lock in alphabetical table order (§4.1) ─────────────────────────────

	// 1. credit_cycle (if sub active — ensureCurrentCycle acquires FOR UPDATE internally)
	var cycle *model.CreditCycle
	if subActive {
		cycle, err = s.ensureCurrentCycle(ctx, tx, sub, now)
		if err != nil {
			if err == errno.ErrSubscriptionExpired {
				subActive = false
			} else {
				return nil, fmt.Errorf("DeductCreditsTx: ensureCurrentCycle: %w", err)
			}
		}
	}

	// 2. trial_grant FOR UPDATE
	var trialLocked *model.TrialGrant
	if trialActive {
		trialLocked, err = s.store.TrialGrants().GetForUpdate(ctx, tx, userID)
		if err != nil {
			return nil, fmt.Errorf("DeductCreditsTx: lock trial_grant: %w", err)
		}
		if trialLocked == nil || !trialLocked.ExpiresAt.After(now) || trialLocked.CreditsRemaining <= 0 {
			trialActive = false
			trialLocked = nil
		}
	}

	// 3. user_booster_balance FOR UPDATE (only when sub or trial active — INV-15)
	boosterActive := subActive || trialActive
	var booster *model.UserBoosterBalance
	if boosterActive {
		booster, err = s.store.BoosterBalances().GetForUpdate(ctx, tx, userID)
		if err != nil {
			return nil, fmt.Errorf("DeductCreditsTx: lock user_booster_balance: %w", err)
		}
	}

	// ── Priority deduction: trial > cycle > booster (§3.5 INV-3) ────────────
	remaining := amount
	d := &DeductionResult{}

	// Pool 1: trial
	if trialActive && trialLocked != nil && remaining > 0 {
		take := int64(trialLocked.CreditsRemaining)
		if take > remaining {
			take = remaining
		}
		d.FromTrial = take
		d.Items = append(d.Items, DeductItem{SourceType: DeductSourceTrial, SourceID: trialLocked.ID, Amount: take})
		remaining -= take
		trialLocked.CreditsRemaining -= int(take)
		if err := s.store.TrialGrants().Update(ctx, tx, trialLocked); err != nil {
			return nil, fmt.Errorf("DeductCreditsTx: update trial_grant: %w", err)
		}
	}

	// Pool 2: cycle
	if subActive && cycle != nil && remaining > 0 {
		take := int64(cycle.CreditsRemaining)
		if take > remaining {
			take = remaining
		}
		d.FromCycle = take
		d.Items = append(d.Items, DeductItem{SourceType: DeductSourceCycle, SourceID: cycle.ID, Amount: take})
		remaining -= take
		cycle.CreditsRemaining -= int(take)
		cycle.UpdatedAt = now
		if err := s.store.CreditCycles().Update(ctx, tx, cycle); err != nil {
			return nil, fmt.Errorf("DeductCreditsTx: update credit_cycle: %w", err)
		}
	}

	// Pool 3: booster (frozen when !subActive && !trialActive — INV-15)
	if boosterActive && booster != nil && booster.CreditsRemaining > 0 && remaining > 0 {
		take := booster.CreditsRemaining
		if take > remaining {
			take = remaining
		}
		d.FromBooster = take
		// SourceID for booster = user_id (user_booster_balance PK is user_id).
		d.Items = append(d.Items, DeductItem{SourceType: DeductSourceBooster, SourceID: userID, Amount: take})
		remaining -= take
		if err := s.store.BoosterBalances().Decrement(ctx, tx, userID, take); err != nil {
			return nil, fmt.Errorf("DeductCreditsTx: decrement booster: %w", err)
		}
	}

	if remaining > 0 {
		return nil, fmt.Errorf("%w: requested %d, shortfall %d", errno.ErrInsufficientCredits, amount, remaining)
	}

	return d, nil
}

// RefundCreditsTx refunds `amount` credits back to the user, attempting the
// original source first (per D4 item-level routing) then falling back per D2:
//
//	original active source → any active booster → active cycle → ledger lost
//
// Returns the source actually credited (refundedTo, refundedID, refundedAmount).
// When all fallbacks fail (no active pool to receive credit), writes a
// membership_event "refund_lost" ledger entry and returns refundedAmount=0
// (no error — caller distinguishes via amount==0 vs err≠nil).
//
// Caller is responsible for tx lifecycle (open + commit/rollback).
func (s *MembershipService) RefundCreditsTx(
	ctx context.Context,
	tx *gorm.DB,
	userID uint64,
	source DeductSource,
	sourceID uint64,
	amount int64,
	now time.Time,
) (refundedTo DeductSource, refundedID uint64, refundedAmount int64, err error) {
	if amount <= 0 {
		return "", 0, 0, errno.ErrInvalidParameter
	}

	// ── Pre-fetch sub/trial once (P1 fix: avoid duplicate fetches across paths) ──
	sub, _ := s.store.Subscriptions().Get(ctx, userID)
	trial, _ := s.store.TrialGrants().Get(ctx, userID)
	subActive := sub != nil && sub.ExpiresAt.After(now)
	trialActive := trial != nil && trial.ExpiresAt.After(now) && trial.CreditsRemaining > 0

	// ── Step 1: Try original source if still active ─────────────────────────
	switch source {
	case DeductSourceTrial:
		var origTrial model.TrialGrant
		queryErr := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&origTrial, sourceID).Error
		if queryErr == nil && origTrial.UserID == userID && origTrial.ExpiresAt.After(now) {
			origTrial.CreditsRemaining += int(amount)
			if updateErr := s.store.TrialGrants().Update(ctx, tx, &origTrial); updateErr != nil {
				return "", 0, 0, fmt.Errorf("RefundCreditsTx: update trial: %w", updateErr)
			}
			return DeductSourceTrial, origTrial.ID, amount, nil
		}

	case DeductSourceCycle:
		var cycle model.CreditCycle
		queryErr := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&cycle, sourceID).Error
		if queryErr == nil && cycle.UserID == userID && cycle.CycleEnd.After(now) {
			cycle.CreditsRemaining += int(amount)
			cycle.UpdatedAt = now
			if updateErr := s.store.CreditCycles().Update(ctx, tx, &cycle); updateErr != nil {
				return "", 0, 0, fmt.Errorf("RefundCreditsTx: update cycle: %w", updateErr)
			}
			return DeductSourceCycle, cycle.ID, amount, nil
		}

	case DeductSourceBooster:
		// Booster active iff sub or trial active (INV-15).
		if subActive || trialActive {
			if incErr := s.store.BoosterBalances().Increment(ctx, tx, userID, amount); incErr != nil {
				return "", 0, 0, fmt.Errorf("RefundCreditsTx: increment booster: %w", incErr)
			}
			return DeductSourceBooster, userID, amount, nil
		}
	}

	// ── Step 2: Fallback chain (original source unavailable) ───────────────

	// Fallback 1: any active booster (requires sub or trial active)
	if subActive || trialActive {
		// Booster row may not exist yet — Increment is an upsert-style op.
		// We require the row to already exist to mirror INV-15 strictly: only
		// refund to booster if user already has a (frozen-or-active) booster row.
		booster, _ := s.store.BoosterBalances().GetForUpdate(ctx, tx, userID)
		if booster != nil {
			if incErr := s.store.BoosterBalances().Increment(ctx, tx, userID, amount); incErr != nil {
				return "", 0, 0, fmt.Errorf("RefundCreditsTx: fallback booster increment: %w", incErr)
			}
			return DeductSourceBooster, userID, amount, nil
		}
	}

	// Fallback 2: active cycle (requires sub active)
	if subActive && sub != nil {
		cycle, _ := s.ensureCurrentCycle(ctx, tx, sub, now)
		if cycle != nil {
			cycle.CreditsRemaining += int(amount)
			cycle.UpdatedAt = now
			if updateErr := s.store.CreditCycles().Update(ctx, tx, cycle); updateErr != nil {
				return "", 0, 0, fmt.Errorf("RefundCreditsTx: fallback cycle update: %w", updateErr)
			}
			return DeductSourceCycle, cycle.ID, amount, nil
		}
	}

	// Fallback 3: ledger lost — write event, return amount=0
	// P1 fix: use nil pointer for SubscriptionID when sub is absent (FK=0 is invalid).
	var subscriptionIDPtr *uint64
	if sub != nil {
		id := sub.ID
		subscriptionIDPtr = &id
	}
	event := &model.MembershipEvent{
		UserID:         userID,
		EventType:      model.EventTypeRefundLost,
		ProductType:    string(source),
		AmountCents:    amount,             // re-purposing amount_cents to store credits amount for the lost refund
		Source:         model.SourceSystem, // P2 fix: system event, not B2B
		SubscriptionID: subscriptionIDPtr,
		OccurredAt:     now,
	}
	if eventErr := s.store.Events().Create(ctx, tx, event); eventErr != nil {
		return "", 0, 0, fmt.Errorf("RefundCreditsTx: write refund_lost event: %w", eventErr)
	}
	return "", 0, 0, nil
}
