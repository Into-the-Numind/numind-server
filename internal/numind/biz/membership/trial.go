package membership

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/pkg/errno"
	model "numind-server/internal/pkg/model/membership"
)

const (
	trialCredits = 200
	trialDays    = 3
)

// GrantTrialRequest carries the parameters for opening a trial grant.
type GrantTrialRequest struct {
	// UserID is the target (child) user who receives the trial.
	UserID uint64
	// GranterUserID is the parent user performing the grant (B2B2C context).
	// May be nil for system-initiated grants.
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

// GrantTrialResult is returned on a successful (or idempotent replay) GrantTrial call.
type GrantTrialResult struct {
	TrialGrant *model.TrialGrant
	// Replayed is true when IdempotencyKey was already present in the event log
	// and the result was returned from the existing event (no writes performed).
	Replayed bool
}

// GrantTrial opens a 200-credit / 3-day trial grant for req.UserID.
//
// Business rules (§3.3):
//  1. A user may only ever have one trial grant (lifetime UNIQUE on user_id).
//     A second attempt returns ErrTrialAlreadyGranted.
//  2. If the user already has an active Pro subscription, the trial is blocked
//     and ErrTrialNotAllowedForActivePro is returned.
//  3. An IdempotencyKey may be provided; same key + same UserID replays the
//     original event (returns Replayed=true, no DB writes). Same key + different
//     UserID returns ErrIdempotencyKeyConflict.
//
// Locking order (§4.1): subscription row first (alphabetically earlier table
// name), then trial_grant row — consistent with all sibling biz functions to
// prevent deadlocks.
func (s *MembershipService) GrantTrial(ctx context.Context, req GrantTrialRequest) (*GrantTrialResult, error) {
	now := req.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.Truncate(time.Second)

	// ── Idempotency pre-check (outside tx, read-only) ──────────────────────
	if req.IdempotencyKey != nil && *req.IdempotencyKey != "" {
		existing, err := s.store.Events().GetByIdempotencyKey(ctx, *req.IdempotencyKey)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			// Same key found — verify the body matches (same UserID).
			if existing.UserID != req.UserID {
				return nil, errno.ErrIdempotencyKeyConflict
			}
			// Replay: fetch the trial grant and return it.
			tg, err := s.store.TrialGrants().Get(ctx, req.UserID)
			if err != nil {
				return nil, err
			}
			return &GrantTrialResult{TrialGrant: tg, Replayed: true}, nil
		}
	}

	// ── Transactional write ────────────────────────────────────────────────
	var result GrantTrialResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock order §4.1: subscription (s) < trial_grant (t) alphabetically.

		// 1. Lock subscription row (may not exist).
		sub, err := s.store.Subscriptions().GetForUpdate(ctx, tx, req.UserID)
		if err != nil {
			return err
		}

		// 2. Lock trial_grant row (may not exist).
		tg, err := s.store.TrialGrants().GetForUpdate(ctx, tx, req.UserID)
		if err != nil {
			return err
		}

		// ── Semantic checks ────────────────────────────────────────────────

		// Check 1: trial already granted (lifetime uniqueness).
		if tg != nil {
			return errno.ErrTrialAlreadyGranted
		}

		// Check 2: active Pro subscription blocks trial.
		if sub != nil && sub.ExpiresAt.After(now) {
			return errno.ErrTrialNotAllowedForActivePro
		}

		// ── Writes ────────────────────────────────────────────────────────

		grantedAt := now
		expiresAt := now.AddDate(0, 0, trialDays)

		newTG := &model.TrialGrant{
			UserID:           req.UserID,
			GrantedAt:        grantedAt,
			ExpiresAt:        expiresAt,
			CreditsRemaining: trialCredits,
			Source:           model.SourceB2BGrant,
			GranterUserID:    req.GranterUserID,
			CreatedAt:        now,
		}

		if err := s.store.TrialGrants().Create(ctx, tx, newTG); err != nil {
			// UNIQUE violation on user_id → concurrent grant race.
			if isUniqueViolation(err, "uniq_trial_user_id") {
				return errno.ErrTrialAlreadyGranted
			}
			return err
		}

		// Append audit event.
		event := &model.MembershipEvent{
			UserID:         req.UserID,
			EventType:      model.EventTypeTrialGranted,
			ProductType:    model.ProductTypeTrial,
			AmountCents:    0,
			Source:         model.SourceB2BGrant,
			GranterUserID:  req.GranterUserID,
			IdempotencyKey: req.IdempotencyKey,
			OccurredAt:     now,
		}
		if err := s.store.Events().Create(ctx, tx, event); err != nil {
			// UNIQUE violation on idempotency_key → concurrent request with same key.
			// Re-read to check body match.
			if isUniqueViolation(err, "uniq_event_idempotency_key") {
				// We already checked above (pre-tx read) — a concurrent write snuck
				// in. Treat as conflict; the caller should retry.
				return errno.ErrIdempotencyKeyConflict
			}
			return err
		}

		result.TrialGrant = newTG
		return nil
	})

	if err != nil {
		return nil, err
	}
	return &result, nil
}

// isUniqueViolation reports whether err is a UNIQUE constraint violation
// involving indexName. It handles both MySQL (error number 1062 with "Duplicate
// entry … for key 'indexName'") and GORM's ErrDuplicatedKey wrapper that SQLite
// tests surface.
func isUniqueViolation(err error, indexName string) bool {
	if err == nil {
		return false
	}
	// GORM v2 wraps duplicate-key errors in gorm.ErrDuplicatedKey for some drivers.
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		// If an index name is given, verify the index name appears in the message.
		if indexName == "" {
			return true
		}
		return strings.Contains(err.Error(), indexName)
	}
	// MySQL surfaces the raw error string containing "1062" or "Duplicate entry".
	msg := err.Error()
	if !strings.Contains(msg, "1062") && !strings.Contains(msg, "Duplicate entry") {
		return false
	}
	if indexName == "" {
		return true
	}
	return strings.Contains(msg, indexName)
}
