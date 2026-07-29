package membership

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"numind-server/internal/pkg/errno"
	creditmodel "numind-server/internal/pkg/model"
	model "numind-server/internal/pkg/model/membership"
)

const (
	// cycleCredits is the legacy monthly default kept for older tests and
	// callers; plan-specific code should use subscriptionCycleCredits.
	cycleCredits = model.MonthlyCycleCredits
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
	// ExpiresAt is the expiry of the deducted pool — populated for trial and
	// cycle (real expiry) and as a far-future sentinel for booster (the
	// per-user aggregate row has no expiry). Used by callers to populate the
	// legacy credit_reservation_item.package_expires_at NOT NULL column.
	ExpiresAt time.Time
}

// ensureCurrentCycle lazily creates or fetches the credit cycle row for the
// billing period that contains txNow, anchored on sub.CurrentStartedAt (§3.4).
//
// Algorithm:
//  1. Compute the current plan-specific cycle bounds.
//     Monthly: anchored calendar months. Weekly: anchored 7-day periods.
//  4. Defensive check: if txNow ≥ cycleEnd → subscription has effectively expired.
//  5. INSERT … ON CONFLICT DO NOTHING (idempotent).
//  6. SELECT FOR UPDATE on (user_id, cycleStart) and return the authoritative row.
//
// The function must be called inside an open transaction (tx).
func (s *MembershipService) ensureCurrentCycle(ctx context.Context, tx *gorm.DB, sub *model.Subscription, txNow time.Time) (*model.CreditCycle, error) {
	cycleStart, cycleEnd := currentCycleBounds(sub, txNow)

	// Step 4: defensive check.
	if !txNow.Before(cycleEnd) {
		return nil, errno.ErrSubscriptionExpired
	}

	// Step 5: INSERT … ON CONFLICT DO NOTHING.
	now := time.Now().UTC()
	credits := subscriptionCycleCredits(sub)
	candidate := &model.CreditCycle{
		UserID:           sub.UserID,
		SubscriptionID:   sub.ID,
		CycleStart:       cycleStart,
		CycleEnd:         cycleEnd,
		CreditsGranted:   credits,
		CreditsRemaining: credits,
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
// Lock order (must be consistent across all mutators to avoid deadlock):
//  1. credit_cycle (via ensureCurrentCycle's SELECT FOR UPDATE)
//  2. trial_grant (FOR UPDATE)
//  3. user_booster_balance (FOR UPDATE)
//
// subscription is read-only here (already locked by GrantOrRenewSubscription).
//
// Sibling locks for reference:
//   - GrantTrial:                subscription → trial_grant
//   - GrantOrRenewSubscription:  subscription only
//   - RefundCreditsTx:           per-target (booster/cycle/trial), no chain
//
// If a future mutator adds a different ordering, deadlock risk increases.
//
// The method opens its own transaction. Returns ErrInsufficientCredits when
// total available credits < amount.
func (s *MembershipService) DeductCredits(ctx context.Context, userID uint64, amount int64) (*DeductionResult, error) {
	var deduction *DeductionResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r, e := s.DeductCreditsTx(ctx, tx, userID, amount, "deduct", time.Now().UTC())
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
// The operation parameter is recorded in credit_transaction.operation for each
// pool deduction row written by this function. Callers should pass a meaningful
// operation string (e.g. "reserve:sop_run", "reconcile:sop_run") so the ledger
// is useful for T7/T8 calibration and ops audits.
//
// Returns DeductionResult with FromTrial/FromCycle/FromBooster aggregates AND
// per-pool Items (SourceType + SourceID + Amount) so the caller can record
// detailed per-source reservation_item rows for accurate refund routing.
//
// LEDGER CONTRACT (T1): For every pool deduction, this function writes a
// credit_transaction row with:
//   - amount = -take (negative = debit)
//   - source_type = "trial" | "cycle" | "booster"
//   - source_id  = trial_grant.id | credit_cycle.id | userID (booster PK)
//   - package_id = 0 (new path has no credit_package)
//
// This makes credit_transaction self-contained for post-T11 forensics and
// T7/T8 calibration without joining the (eventually dropped) credit_package.
//
// Caller is responsible for tx lifecycle (open + commit/rollback). This
// function only writes; on ErrInsufficientCredits the caller must rollback.
func (s *MembershipService) DeductCreditsTx(ctx context.Context, tx *gorm.DB, userID uint64, amount int64, operation string, now time.Time) (*DeductionResult, error) {
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

	// ── Lock in fixed order: credit_cycle → trial_grant → user_booster_balance (§4.1) ──

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
		d.Items = append(d.Items, DeductItem{SourceType: DeductSourceTrial, SourceID: trialLocked.ID, Amount: take, ExpiresAt: trialLocked.ExpiresAt})
		remaining -= take
		trialLocked.CreditsRemaining -= int(take)
		if err := s.store.TrialGrants().Update(ctx, tx, trialLocked); err != nil {
			return nil, fmt.Errorf("DeductCreditsTx: update trial_grant: %w", err)
		}
		// T1 ledger: write credit_transaction row for trial pool debit.
		sourceType := string(DeductSourceTrial)
		sourceID := trialLocked.ID
		ct := &creditmodel.CreditTransaction{
			UserID:     uint(userID),
			PackageID:  0, // new-path: no credit_package
			SourceType: &sourceType,
			SourceID:   &sourceID,
			Amount:     -take,
			Operation:  operation,
			CreatedAt:  time.Now().UTC(),
		}
		if err := tx.WithContext(ctx).Create(ct).Error; err != nil {
			return nil, fmt.Errorf("DeductCreditsTx: write trial credit_transaction: %w", err)
		}
	}

	// Pool 2: cycle
	if subActive && cycle != nil && remaining > 0 {
		take := int64(cycle.CreditsRemaining)
		if take > remaining {
			take = remaining
		}
		d.FromCycle = take
		d.Items = append(d.Items, DeductItem{SourceType: DeductSourceCycle, SourceID: cycle.ID, Amount: take, ExpiresAt: cycle.CycleEnd})
		remaining -= take
		cycle.CreditsRemaining -= int(take)
		cycle.UpdatedAt = now
		if err := s.store.CreditCycles().Update(ctx, tx, cycle); err != nil {
			return nil, fmt.Errorf("DeductCreditsTx: update credit_cycle: %w", err)
		}
		// T1 ledger: write credit_transaction row for cycle pool debit.
		sourceType := string(DeductSourceCycle)
		sourceID := cycle.ID
		ct := &creditmodel.CreditTransaction{
			UserID:     uint(userID),
			PackageID:  0, // new-path: no credit_package
			SourceType: &sourceType,
			SourceID:   &sourceID,
			Amount:     -take,
			Operation:  operation,
			CreatedAt:  time.Now().UTC(),
		}
		if err := tx.WithContext(ctx).Create(ct).Error; err != nil {
			return nil, fmt.Errorf("DeductCreditsTx: write cycle credit_transaction: %w", err)
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
		// Booster has no expiry (per-user aggregate row, spec §2.4) — use a
		// far-future sentinel to satisfy NOT NULL column constraint downstream.
		boosterSentinel := time.Date(2099, 12, 31, 23, 59, 59, 0, time.UTC)
		d.Items = append(d.Items, DeductItem{SourceType: DeductSourceBooster, SourceID: userID, Amount: take, ExpiresAt: boosterSentinel})
		remaining -= take
		if err := s.store.BoosterBalances().Decrement(ctx, tx, userID, take); err != nil {
			return nil, fmt.Errorf("DeductCreditsTx: decrement booster: %w", err)
		}
		// T1 ledger: write credit_transaction row for booster pool debit.
		// SourceID = userID (user_booster_balance PK).
		sourceType := string(DeductSourceBooster)
		sourceID := userID
		ct := &creditmodel.CreditTransaction{
			UserID:     uint(userID),
			PackageID:  0, // new-path: no credit_package
			SourceType: &sourceType,
			SourceID:   &sourceID,
			Amount:     -take,
			Operation:  operation,
			CreatedAt:  time.Now().UTC(),
		}
		if err := tx.WithContext(ctx).Create(ct).Error; err != nil {
			return nil, fmt.Errorf("DeductCreditsTx: write booster credit_transaction: %w", err)
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
// LEDGER CONTRACT (T1): For every successful refund (amount > 0), this function
// writes a positive credit_transaction row with the original source_type/source_id
// from the deduction, making the ledger self-contained for post-T11 forensics.
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

	// writeLedgerRefund writes a positive credit_transaction row for the refund.
	// actualSource and actualSourceID may differ from the original (source, sourceID)
	// when a fallback pool received the credit — we record the actual destination.
	writeLedgerRefund := func(actualSource DeductSource, actualSourceID uint64) error {
		st := string(actualSource)
		sid := actualSourceID
		ct := &creditmodel.CreditTransaction{
			UserID:     uint(userID),
			PackageID:  0, // new-path: no credit_package
			SourceType: &st,
			SourceID:   &sid,
			Amount:     amount, // positive = refund
			Operation:  "refund",
			CreatedAt:  time.Now().UTC(),
		}
		return tx.WithContext(ctx).Create(ct).Error
	}

	// ── Step 1: Try original source if still active ─────────────────────────
	switch source {
	case DeductSourceTrial:
		// NOTE: When the original source pool is trial and the trial has expired,
		// the refund is routed to the fallback chain (booster → cycle). This means
		// the user effectively gets refunded against a different pool than they
		// were debited from. Because trial credits use a 0.5x user_type_multiplier
		// (credit_service.go classifyDeductedFrom), this is a moneywise asymmetric
		// refund — favorable to the user (booster/cycle credits are full-value).
		// Acceptable per spec; flagged in audit P2#12.
		var origTrial model.TrialGrant
		queryErr := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&origTrial, sourceID).Error
		if queryErr == nil && origTrial.UserID == userID && origTrial.ExpiresAt.After(now) {
			origTrial.CreditsRemaining += int(amount)
			if updateErr := s.store.TrialGrants().Update(ctx, tx, &origTrial); updateErr != nil {
				return "", 0, 0, fmt.Errorf("RefundCreditsTx: update trial: %w", updateErr)
			}
			if ledgerErr := writeLedgerRefund(DeductSourceTrial, origTrial.ID); ledgerErr != nil {
				return "", 0, 0, fmt.Errorf("RefundCreditsTx: write trial refund ledger: %w", ledgerErr)
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
			if ledgerErr := writeLedgerRefund(DeductSourceCycle, cycle.ID); ledgerErr != nil {
				return "", 0, 0, fmt.Errorf("RefundCreditsTx: write cycle refund ledger: %w", ledgerErr)
			}
			return DeductSourceCycle, cycle.ID, amount, nil
		}

	case DeductSourceBooster:
		// Booster active iff sub or trial active (INV-15).
		if subActive || trialActive {
			if incErr := s.store.BoosterBalances().Increment(ctx, tx, userID, amount); incErr != nil {
				return "", 0, 0, fmt.Errorf("RefundCreditsTx: increment booster: %w", incErr)
			}
			if ledgerErr := writeLedgerRefund(DeductSourceBooster, userID); ledgerErr != nil {
				return "", 0, 0, fmt.Errorf("RefundCreditsTx: write booster refund ledger: %w", ledgerErr)
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
			if ledgerErr := writeLedgerRefund(DeductSourceBooster, userID); ledgerErr != nil {
				return "", 0, 0, fmt.Errorf("RefundCreditsTx: write fallback booster refund ledger: %w", ledgerErr)
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
			if ledgerErr := writeLedgerRefund(DeductSourceCycle, cycle.ID); ledgerErr != nil {
				return "", 0, 0, fmt.Errorf("RefundCreditsTx: write fallback cycle refund ledger: %w", ledgerErr)
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

	// P2#11: also write a zero-amount credit_transaction row so the audit
	// invariant SUM(credit_transaction) == net flow per user holds even when
	// the refund cannot be routed to any pool. The lost amount is recorded
	// on the membership_event above; this row exists purely for ledger
	// completeness and reconciliation joins. amount=0 ensures the row does
	// not affect balance sums.
	lostSourceType := string(source)
	lostSourceID := sourceID
	lostCT := &creditmodel.CreditTransaction{
		UserID:     uint(userID),
		PackageID:  0, // new-path: no credit_package
		SourceType: &lostSourceType,
		SourceID:   &lostSourceID,
		Amount:     0, // zero so balance sums are unaffected; lost amount on membership_event
		Operation:  "refund_lost",
		CreatedAt:  time.Now().UTC(),
	}
	if ctErr := tx.WithContext(ctx).Create(lostCT).Error; ctErr != nil {
		return "", 0, 0, fmt.Errorf("RefundCreditsTx: write refund_lost credit_transaction: %w", ctErr)
	}

	return "", 0, 0, nil
}
