package membership

import (
	"context"
	"fmt"
	"time"

	model "numind-server/internal/pkg/model/membership"
	"numind-server/internal/pkg/util"
)

// MembershipState is the real-time computed membership status for a user.
// It is derived on-the-fly from the trial_grant and subscription tables
// without persisting any derived state.
//
// Spec §3.6.
type MembershipState struct {
	// DisplayState is the UI-facing label: "free" / "trial" / "pro".
	// Rule: TrialActive → "trial" (takes priority even when SubActive=true, US-2);
	//       SubActive   → "pro";
	//       otherwise   → "free".
	DisplayState string
	// TrialActive is true when trial_grant exists, ExpiresAt > now, and
	// CreditsRemaining > 0.
	TrialActive bool
	// SubActive is true when subscription exists and ExpiresAt > now.
	SubActive bool
	// TrialExpiresAt is set when a trial row exists (regardless of active state).
	TrialExpiresAt *time.Time
	// SubExpiresAt is set when a subscription row exists.
	SubExpiresAt *time.Time
	// SubFirstStartedAt is set when a subscription row exists.
	SubFirstStartedAt *time.Time
	// BoosterFrozen is true when neither trial nor subscription is active
	// (INV-17). Booster credits cannot be used while frozen.
	BoosterFrozen bool
}

// BalanceView is a composite balance DTO providing all credit pool values
// alongside the membership state for display or deduction gating.
//
// Spec §3.7 / §5.3.
type BalanceView struct {
	// TrialRemaining is the credits_remaining from the trial_grant row.
	// Zero when no trial is active.
	TrialRemaining int64
	// CycleRemaining is the credits_remaining from the current billing cycle.
	// Zero when no sub is active; defaults to 2000 (INV-20) when sub is active
	// but the cycle row has not yet been lazily created.
	CycleRemaining int64
	// CycleEnd is the end of the current billing cycle; nil when no sub is active.
	CycleEnd *time.Time
	// BoosterTotal — NAMING WARNING (kept for backward compat with JSON tag
	// `booster_total` consumed by both web-v3 and admin frontends): the value is
	// the **raw credits_remaining** from user_booster_balance, NOT a cumulative
	// purchased total. Booster credits are an aggregate balance (each +600 buy
	// is added to credits_remaining; deductions decrement it), so there is no
	// separate "purchased total" concept to expose. The frontend renders this
	// as a single-number "剩余 N 积分" — do NOT treat it as a denominator. Zero
	// when no booster row exists. See store.userBoosterBalanceStore for the SOT
	// schema and Decrement/Increment semantics.
	BoosterTotal int64
	// BoosterUsable is the usable portion of the booster balance.
	// When BoosterFrozen (INV-19), BoosterUsable = 0; otherwise BoosterUsable = BoosterTotal.
	BoosterUsable int64
	// MembershipState is the DisplayState string from GetMembershipState.
	MembershipState string
	// SubExpiresAt is copied from MembershipState.SubExpiresAt.
	SubExpiresAt *time.Time
	// TrialExpiresAt is copied from MembershipState.TrialExpiresAt.
	TrialExpiresAt *time.Time
}

// GetMembershipState computes the real-time membership state for userID at now.
//
// Logic (spec §3.6):
//  1. Fetch trial_grant. TrialActive = row exists && ExpiresAt > now && CreditsRemaining > 0.
//  2. Fetch subscription. SubActive = row exists && ExpiresAt > now.
//  3. DisplayState: TrialActive → "trial" (US-2 priority); SubActive → "pro"; else "free".
//  4. BoosterFrozen = !TrialActive && !SubActive (INV-17).
func (s *MembershipService) GetMembershipState(ctx context.Context, userID uint64, now time.Time) (*MembershipState, error) {
	// Fetch trial grant.
	trial, err := s.store.TrialGrants().Get(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("GetMembershipState: get trial: %w", err)
	}
	trialActive := trial != nil && trial.ExpiresAt.After(now) && trial.CreditsRemaining > 0

	// Fetch subscription.
	sub, err := s.store.Subscriptions().Get(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("GetMembershipState: get subscription: %w", err)
	}
	subActive := sub != nil && sub.ExpiresAt.After(now)

	// Compute display state (US-2: trial takes priority over sub).
	var displayState string
	switch {
	case trialActive:
		displayState = "trial"
	case subActive:
		displayState = "pro"
	default:
		displayState = "free"
	}

	boosterFrozen := !trialActive && !subActive

	state := &MembershipState{
		DisplayState:  displayState,
		TrialActive:   trialActive,
		SubActive:     subActive,
		BoosterFrozen: boosterFrozen,
	}

	if trial != nil {
		t := trial.ExpiresAt
		state.TrialExpiresAt = &t
	}
	if sub != nil {
		e := sub.ExpiresAt
		state.SubExpiresAt = &e
		f := sub.FirstStartedAt
		state.SubFirstStartedAt = &f
	}

	return state, nil
}

// IsActiveMember reports whether userID is a member at `now`, judged purely by
// VALIDITY PERIOD: an unexpired subscription OR an unexpired trial grant.
//
// Unlike GetMembershipState's TrialActive flag, IsActiveMember deliberately
// IGNORES remaining credits — a trial member whose trial credits are exhausted
// (CreditsRemaining == 0) is still a member. This is required by the
// free-model-member-only feature (AC2): a member must be able to use a 0-priced
// model regardless of balance. Store errors are propagated (never swallowed):
// a transient DB failure must not silently downgrade a real member to non-member.
func (s *MembershipService) IsActiveMember(ctx context.Context, userID uint64, now time.Time) (bool, error) {
	sub, err := s.store.Subscriptions().Get(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("IsActiveMember: get subscription: %w", err)
	}
	if sub != nil && sub.ExpiresAt.After(now) {
		return true, nil
	}

	trial, err := s.store.TrialGrants().Get(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("IsActiveMember: get trial: %w", err)
	}
	if trial != nil && trial.ExpiresAt.After(now) {
		return true, nil
	}

	return false, nil
}

// GetBalance returns the composite BalanceView for userID at now.
//
// Logic (spec §3.7):
//  1. Call GetMembershipState.
//  2. TrialRemaining: from trial_grant.CreditsRemaining (only when TrialActive).
//  3. CycleRemaining: only when SubActive.
//     - Compute cycleStart/cycleEnd from sub.CurrentStartedAt + monthsSinceStart.
//     - If no cycle row yet (INV-20) → default to cycleCredits (2000).
//     - If cycle row exists → use cycle.CreditsRemaining.
//  4. BoosterTotal: from user_booster_balance.CreditsRemaining (0 if no row).
//  5. BoosterUsable: 0 when BoosterFrozen (INV-19); otherwise BoosterTotal.
func (s *MembershipService) GetBalance(ctx context.Context, userID uint64, now time.Time) (*BalanceView, error) {
	state, err := s.GetMembershipState(ctx, userID, now)
	if err != nil {
		return nil, fmt.Errorf("GetBalance: get membership state: %w", err)
	}

	view := &BalanceView{
		MembershipState: state.DisplayState,
		SubExpiresAt:    state.SubExpiresAt,
		TrialExpiresAt:  state.TrialExpiresAt,
	}

	// ── Trial remaining ──────────────────────────────────────────────────────
	if state.TrialActive {
		trial, err := s.store.TrialGrants().Get(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("GetBalance: get trial: %w", err)
		}
		if trial != nil {
			view.TrialRemaining = int64(trial.CreditsRemaining)
		}
	}

	// ── Cycle remaining (only when sub is active) ────────────────────────────
	if state.SubActive {
		sub, err := s.store.Subscriptions().Get(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("GetBalance: get subscription: %w", err)
		}
		if sub != nil {
			cycleStart, cycleEnd := currentCycleBounds(sub, now)

			// Read-only fetch of the cycle row (no lazy creation — that happens in DeductCredits).
			cycle, err := s.store.CreditCycles().GetByUserAndStart(ctx, s.db, userID, cycleStart)
			if err != nil {
				return nil, fmt.Errorf("GetBalance: get cycle: %w", err)
			}

			if cycle != nil {
				view.CycleRemaining = int64(cycle.CreditsRemaining)
				t := cycle.CycleEnd
				view.CycleEnd = &t
			} else {
				// INV-20: cycle not yet lazily created → default to monthly quota.
				view.CycleRemaining = cycleCredits
				view.CycleEnd = &cycleEnd
			}
		}
	}

	// ── Booster balance ──────────────────────────────────────────────────────
	booster, err := s.store.BoosterBalances().Get(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("GetBalance: get booster: %w", err)
	}
	if booster != nil {
		view.BoosterTotal = booster.CreditsRemaining
	}

	// INV-19: frozen → BoosterUsable = 0.
	if !state.BoosterFrozen {
		view.BoosterUsable = view.BoosterTotal
	}

	return view, nil
}

// BatchMembershipState is the per-user membership state returned by GetMembershipStateBatch.
type BatchMembershipState struct {
	HasActiveTrial        bool
	HasActiveSubscription bool
	TrialExpiresAt        *string
	SubscriptionExpiresAt *string
	HasUsedTrial          bool
	CycleRemaining        int64
	// BoosterTotal 加量包剩余积分（user_booster_balance.credits_remaining，聚合余额）。
	// 与 CycleRemaining（订阅+trial）分开，前端客户列表「加量包」列展示用。
	BoosterTotal int64
}

// GetMembershipStateBatch computes membership state for multiple users in two
// batch queries (subscriptions + trial_grants) instead of N individual queries.
func (s *MembershipService) GetMembershipStateBatch(ctx context.Context, userIDs []uint64, now time.Time) (map[uint64]*BatchMembershipState, error) {
	if len(userIDs) == 0 {
		return map[uint64]*BatchMembershipState{}, nil
	}

	result := make(map[uint64]*BatchMembershipState, len(userIDs))
	for _, id := range userIDs {
		result[id] = &BatchMembershipState{}
	}

	// Batch-fetch subscriptions
	var subs []model.Subscription
	if err := s.db.WithContext(ctx).Where("user_id IN ?", userIDs).Find(&subs).Error; err != nil {
		return nil, fmt.Errorf("GetMembershipStateBatch: fetch subscriptions: %w", err)
	}
	for i := range subs {
		sub := &subs[i]
		st := result[sub.UserID]
		if st == nil {
			continue
		}
		if sub.ExpiresAt.After(now) {
			st.HasActiveSubscription = true
			exp := sub.ExpiresAt.Format(time.RFC3339)
			st.SubscriptionExpiresAt = &exp

			// Compute cycle remaining (same logic as GetBalance)
			cycleStart, _ := currentCycleBounds(sub, now)
			cycle, _ := s.store.CreditCycles().GetByUserAndStart(ctx, s.db, sub.UserID, cycleStart)
			if cycle != nil {
				st.CycleRemaining = int64(cycle.CreditsRemaining)
			} else {
				st.CycleRemaining = cycleCredits // 2000 default
			}
		}
	}

	// Batch-fetch trial grants
	var trials []model.TrialGrant
	if err := s.db.WithContext(ctx).Where("user_id IN ?", userIDs).Find(&trials).Error; err != nil {
		return nil, fmt.Errorf("GetMembershipStateBatch: fetch trials: %w", err)
	}
	for i := range trials {
		trial := &trials[i]
		st := result[trial.UserID]
		if st == nil {
			continue
		}
		st.HasUsedTrial = true
		if trial.ExpiresAt.After(now) && trial.CreditsRemaining > 0 {
			st.HasActiveTrial = true
			exp := trial.ExpiresAt.Format(time.RFC3339)
			st.TrialExpiresAt = &exp
		}
	}

	// Batch-fetch booster balances（加量包剩余积分，单聚合行 / user）。
	// 缺行视为 0（用户从未购买过加量包），与 GetBalance 语义一致。
	var boosters []model.UserBoosterBalance
	if err := s.db.WithContext(ctx).Where("user_id IN ?", userIDs).Find(&boosters).Error; err != nil {
		return nil, fmt.Errorf("GetMembershipStateBatch: fetch boosters: %w", err)
	}
	for i := range boosters {
		if st := result[boosters[i].UserID]; st != nil {
			st.BoosterTotal = boosters[i].CreditsRemaining
		}
	}

	return result, nil
}

// currentCycleBounds computes (cycleStart, cycleEnd) for the billing cycle
// that contains now, anchored on sub.CurrentStartedAt. Uses the same algorithm
// as ensureCurrentCycle (§3.4) but is read-only and does not require a TX.
func currentCycleBounds(sub *model.Subscription, now time.Time) (cycleStart, cycleEnd time.Time) {
	anchor := sub.CurrentStartedAt

	monthsSinceStart := 0
	for {
		nextStart := util.AnchorAddMonths(anchor, monthsSinceStart+1)
		if !now.Before(nextStart) {
			monthsSinceStart++
			if monthsSinceStart >= sub.TotalMonthsPurchased {
				break
			}
		} else {
			break
		}
	}

	cycleStart = util.AnchorAddMonths(anchor, monthsSinceStart)
	cycleEndRaw := util.AnchorAddMonths(anchor, monthsSinceStart+1)
	cycleEnd = cycleEndRaw
	if sub.ExpiresAt.Before(cycleEndRaw) {
		cycleEnd = sub.ExpiresAt
	}
	return cycleStart, cycleEnd
}
