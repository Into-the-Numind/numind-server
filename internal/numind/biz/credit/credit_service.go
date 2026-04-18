package credit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/pricing"
)

// creditService is the ICreditService implementation that dispatches by
// user.BillingMode into one of the two internal strategies:
//   - legacyTierImpl  (BillingMode == legacy_tier)
//   - creditsImpl     (BillingMode == credits, default)
//
// See spec §1.2 / §1.3 / §1.4 for the dispatch rules and §1.6 for the caller
// template.
type creditService struct {
	store   store.IStore
	biz     ICreditBiz
	pricing pricing.ICalculator
	// Pre-instantiated legs so each dispatch is a plain method call.
	legacy  *legacyTierImpl
	credits *creditsImpl
}

// NewCreditService constructs the singleton ICreditService used throughout the
// app. Pass the existing store.IStore, an ICreditBiz (for DeductCreditsTx +
// quota queries), and a pricing.ICalculator (used by credits leg for R2
// estimation). pricing may be nil — the legacy leg never touches it, so
// legacy-only callers in tests can pass nil.
func NewCreditService(ds store.IStore, biz ICreditBiz, pc pricing.ICalculator) ICreditService {
	return &creditService{
		store:   ds,
		biz:     biz,
		pricing: pc,
		legacy:  &legacyTierImpl{biz: biz},
		credits: &creditsImpl{store: ds, biz: biz, pricing: pc},
	}
}

// CheckAndEstimate dispatches to legacy or credits leg based on user.BillingMode.
func (s *creditService) CheckAndEstimate(ctx context.Context, user *model.User, op Operation, in EstimationInput) (*PreCheckResult, error) {
	if user.BillingMode == model.BillingModeLegacyTier {
		return s.legacy.CheckAndEstimate(ctx, user, op, in)
	}
	return s.credits.CheckAndEstimate(ctx, user, op, in)
}

// Reserve dispatches by billing_mode. legacy_tier MUST be guarded by the
// caller via SkipDeduction — reaching legacy.Reserve panics by design.
func (s *creditService) Reserve(ctx context.Context, user *model.User, op Operation, estimated int64, coefID uint64, idempotencyKey *string) (*Reservation, error) {
	if user.BillingMode == model.BillingModeLegacyTier {
		return s.legacy.Reserve(ctx, user, op, estimated, coefID, idempotencyKey)
	}
	return s.credits.Reserve(ctx, user, op, estimated, coefID, idempotencyKey)
}

// Reconcile looks up the reservation by ID; there is no user to dispatch on.
// Since legacy_tier never creates reservations, any reservation that reaches
// Reconcile is a credits-mode reservation.
func (s *creditService) Reconcile(ctx context.Context, reservationID uint64, actualCostCents int64) error {
	return s.credits.Reconcile(ctx, reservationID, actualCostCents)
}

// Refund has the same dispatch rationale as Reconcile — reservations only
// exist in credits mode.
func (s *creditService) Refund(ctx context.Context, reservationID uint64, reason string) error {
	return s.credits.Refund(ctx, reservationID, reason)
}

// FinalizeReservation is the single defer-exit point. A nil reservation is
// a safe no-op: legacy_tier callers pass nil because Reserve was skipped.
// Otherwise dispatches to the credits leg (only path that creates reservations).
func (s *creditService) FinalizeReservation(ctx context.Context, rsv *Reservation, actualCostCents *int64, opErr *error) error {
	if rsv == nil {
		return nil
	}
	return s.credits.FinalizeReservation(ctx, rsv, actualCostCents, opErr)
}

// GetBalance dispatches by billing_mode. legacy_tier returns
// RemainingRuns/MonthlyLimit snapshot; credits returns the credit_package
// FIFO breakdown.
func (s *creditService) GetBalance(ctx context.Context, user *model.User) (*BalanceBreakdown, error) {
	if user.BillingMode == model.BillingModeLegacyTier {
		return s.legacy.GetBalance(ctx, user)
	}
	return s.credits.GetBalance(ctx, user)
}

// ---------------------------------------------------------------------------
// legacyTierImpl — Grandfathering Option E (spec §1.3 / §3.6)
//
// Behaviour:
//   - CheckAndEstimate → SkipDeduction=true always; Sufficient derived from
//     user.CanRunSOP(); on !canRun wraps ErrInsufficientCredits with Reason
//   - Reserve / Reconcile / Refund → panic("unreachable: legacy_tier must be
//     guarded by SkipDeduction")
//   - GetBalance → user.GetRemainingSOPRuns() + MonthlyLimit, no credit_package
// ---------------------------------------------------------------------------

type legacyTierImpl struct {
	biz ICreditBiz // unused for now but kept for symmetry with creditsImpl
}

// CheckAndEstimate for legacy tier: delegate to user.CanRunSOP() and translate
// its Chinese denial message into PreCheckResult.Reason so the caller can
// surface the same text the user sees today.
func (l *legacyTierImpl) CheckAndEstimate(_ context.Context, user *model.User, _ Operation, _ EstimationInput) (*PreCheckResult, error) {
	canRun, reason := user.CanRunSOP()
	pre := &PreCheckResult{
		SkipDeduction: true,
		Balance:       l.buildLegacyBalance(user),
	}
	if !canRun {
		pre.Sufficient = false
		pre.Reason = reason
		// Wrap ErrInsufficientCredits so callers can use errors.Is to
		// classify, and log/trace retain the zh reason.
		return pre, fmt.Errorf("%w: %s", ErrInsufficientCredits, reason)
	}
	pre.Sufficient = true
	return pre, nil
}

// Reserve must never be reached on legacy_tier. Callers gate via
// pre.SkipDeduction; any code that misses this is a bug and should blow up
// loudly at dev time rather than silently debit nothing.
func (l *legacyTierImpl) Reserve(_ context.Context, _ *model.User, _ Operation, _ int64, _ uint64, _ *string) (*Reservation, error) {
	panic("unreachable: legacy_tier must be guarded by SkipDeduction")
}

func (l *legacyTierImpl) GetBalance(_ context.Context, user *model.User) (*BalanceBreakdown, error) {
	return l.buildLegacyBalance(user).Ptr(), nil
}

// buildLegacyBalance computes the RemainingRuns/MonthlyLimit snapshot WITHOUT
// touching credit_package. Premium users receive nil/nil (unlimited, no cap).
// Trial users receive their 10-cap; standard users receive the 20/month cap.
func (l *legacyTierImpl) buildLegacyBalance(user *model.User) BalanceBreakdown {
	remaining := user.GetRemainingSOPRuns() // -1 = unlimited (premium)
	actualTier := user.GetActualUserTier()

	bal := BalanceBreakdown{BillingMode: model.BillingModeLegacyTier}
	switch actualTier {
	case model.UserTierPremium:
		// Unlimited: both RemainingRuns and MonthlyLimit are nil.
		// (Spec §1.8: "nil = premium unlimited")
		return bal
	case model.UserTierStandard:
		limit := model.StandardUserMonthlySOPLimit
		bal.MonthlyLimit = &limit
		if remaining >= 0 {
			bal.RemainingRuns = &remaining
		}
		return bal
	case model.UserTierTrial:
		limit := model.TrialUserSOPLimit
		bal.MonthlyLimit = &limit
		if remaining >= 0 {
			bal.RemainingRuns = &remaining
		}
		return bal
	default: // free
		zero := 0
		bal.RemainingRuns = &zero
		zeroLimit := 0
		bal.MonthlyLimit = &zeroLimit
		return bal
	}
}

// Ptr returns a pointer copy of the BalanceBreakdown so callers that require
// *BalanceBreakdown don't need to take the address of a stack value.
func (b BalanceBreakdown) Ptr() *BalanceBreakdown { return &b }

// ---------------------------------------------------------------------------
// creditsImpl — new-system credits mode (spec §1.4)
//
// Stubs here; filled in by Tasks C.3 / C.4 / C.5 / C.8. Each method returns a
// placeholder error for now so the service compiles and legacy-only tests
// can exercise the dispatcher without requiring credits-mode infrastructure.
// ---------------------------------------------------------------------------

type creditsImpl struct {
	store   store.IStore
	biz     ICreditBiz
	pricing pricing.ICalculator
}

// CheckAndEstimate — Task C.5 fills in the R2 formula. For now Task C.3 only
// needs a stub that returns a minimal "sufficient" result so the Reserve
// path can be exercised end-to-end in tests. The real implementation lands
// in Task C.5 and replaces this body.
func (c *creditsImpl) CheckAndEstimate(_ context.Context, _ *model.User, _ Operation, _ EstimationInput) (*PreCheckResult, error) {
	return nil, fmt.Errorf("creditsImpl.CheckAndEstimate: not yet implemented (Task C.5)")
}

// Reserve: same-transaction (DeductCreditsTx + INSERT credit_reservation +
// INSERT credit_reservation_item × N). Idempotency is enforced by the unique
// index uk_idempotency_key — a duplicate key hit dispatches to a lookup of
// the existing reservation and that row is returned unchanged.
//
// Spec refs: §1.4 / §3.10 (tx nesting contract) / §3.2 (runNode sequence).
func (c *creditsImpl) Reserve(
	ctx context.Context, user *model.User, op Operation,
	estimated int64, coefID uint64, idempotencyKey *string,
) (*Reservation, error) {
	if estimated <= 0 {
		return nil, fmt.Errorf("creditsImpl.Reserve: estimated credits must be > 0, got %d", estimated)
	}

	// Idempotency fast path: if the key already exists, return existing row.
	// This short-circuits BEFORE opening a tx — no wasted deduction if
	// caller retries after a successful first attempt.
	if idempotencyKey != nil && *idempotencyKey != "" {
		if existing, err := c.findReservationByIdempKey(ctx, *idempotencyKey); err == nil && existing != nil {
			return existing, nil
		}
	}

	// Ensure credit_account exists BEFORE the tx so that the ensure-call
	// does not race for a connection with the outer tx (spec §3.10 +
	// DeductCreditsTx doc comment).
	if _, err := c.store.Credits().GetOrCreateAccount(ctx, user.ID); err != nil {
		return nil, fmt.Errorf("creditsImpl.Reserve: ensure credit_account: %w", err)
	}

	reference := referenceFromOp(op)
	rsvRow := &model.CreditReservation{
		UserID:          user.ID,
		ReferenceType:   reference.refType,
		ReferenceID:     reference.refID, // filled in from idempotencyKey / "pending"
		Operation:       string(op),
		ReservedCredits: estimated,
		CoefficientID:   coefID,
		Status:          string(StatusReserved),
		IdempotencyKey:  idempotencyKey,
	}
	if idempotencyKey != nil {
		rsvRow.ReferenceID = *idempotencyKey
	} else {
		rsvRow.ReferenceID = ""
	}

	var items []PackageDeduction
	txErr := c.store.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. FIFO deduction inside the outer tx — returns items for seq emission.
		debited, err := c.biz.DeductCreditsTx(ctx, tx, user.ID, estimated, "reserve:"+string(op))
		if err != nil {
			return err // ErrInsufficientCredits bubbles up; tx rolls back.
		}
		items = debited

		// 2. Write the reservation row.
		if err := tx.Create(rsvRow).Error; err != nil {
			// Unique-constraint on idempotency_key means a concurrent writer
			// inserted the same key — look it up and use that row.
			if isUniqueKeyViolation(err) && idempotencyKey != nil {
				existing, lookupErr := c.findReservationByIdempKeyTx(ctx, tx, *idempotencyKey)
				if lookupErr != nil {
					return fmt.Errorf("creditsImpl.Reserve: idempotency lookup: %w", lookupErr)
				}
				if existing != nil {
					// Mutate the outer rsvRow to point at the winner so caller
					// sees a consistent view. Return a sentinel so outer tx
					// aborts and we return the existing row from outside.
					*rsvRow = toDBReservation(existing)
					return errReservationAlreadyExists
				}
			}
			return fmt.Errorf("creditsImpl.Reserve: insert credit_reservation: %w", err)
		}

		// 3. Write reservation items in FIFO order (seq = idx+1).
		itemRows := make([]model.CreditReservationItem, 0, len(items))
		for i, d := range items {
			itemRows = append(itemRows, model.CreditReservationItem{
				ReservationID:    rsvRow.ID,
				PackageID:        d.PackageID,
				Credits:          d.Credits,
				PackageType:      d.PackageType,
				PackageExpiresAt: d.ExpiresAt,
				Seq:              i + 1,
			})
		}
		if len(itemRows) > 0 {
			if err := tx.Create(&itemRows).Error; err != nil {
				return fmt.Errorf("creditsImpl.Reserve: insert reservation items: %w", err)
			}
		}
		return nil
	})

	// If the concurrent-idempotency path triggered, re-fetch and return the
	// winner row. Caller never sees an error.
	if errors.Is(txErr, errReservationAlreadyExists) && idempotencyKey != nil {
		existing, err := c.findReservationByIdempKey(ctx, *idempotencyKey)
		if err != nil {
			return nil, fmt.Errorf("creditsImpl.Reserve: post-conflict lookup: %w", err)
		}
		return existing, nil
	}
	if txErr != nil {
		return nil, txErr
	}

	return &Reservation{
		ID:              rsvRow.ID,
		UserID:          rsvRow.UserID,
		ReferenceType:   rsvRow.ReferenceType,
		ReferenceID:     rsvRow.ReferenceID,
		Operation:       Operation(rsvRow.Operation),
		ReservedCredits: rsvRow.ReservedCredits,
		CoefficientID:   rsvRow.CoefficientID,
		Status:          StatusReserved,
		IdempotencyKey:  rsvRow.IdempotencyKey,
		Items:           toReservationItems(items),
		CreatedAt:       rsvRow.CreatedAt,
	}, nil
}

// Reconcile / Refund / FinalizeReservation — filled in by Task C.4.
func (c *creditsImpl) Reconcile(_ context.Context, _ uint64, _ int64) error {
	return fmt.Errorf("creditsImpl.Reconcile: not yet implemented (Task C.4)")
}

func (c *creditsImpl) Refund(_ context.Context, _ uint64, _ string) error {
	return fmt.Errorf("creditsImpl.Refund: not yet implemented (Task C.4)")
}

func (c *creditsImpl) FinalizeReservation(_ context.Context, _ *Reservation, _ *int64, _ *error) error {
	return fmt.Errorf("creditsImpl.FinalizeReservation: not yet implemented (Task C.4)")
}

// GetBalance returns the credit_package breakdown (sub + booster). The same
// fields are consumed by the frontend credits.ts store (spec §1.8).
func (c *creditsImpl) GetBalance(ctx context.Context, user *model.User) (*BalanceBreakdown, error) {
	subTotal, subRemain, boosterTotal, boosterRemain, err := c.store.Credits().GetQuotaBreakdown(ctx, user.ID)
	if err != nil {
		return nil, fmt.Errorf("creditsImpl.GetBalance: quota breakdown: %w", err)
	}
	bal := &BalanceBreakdown{
		BillingMode:   model.BillingModeCredits,
		SubTotal:      subTotal,
		SubRemain:     subRemain,
		BoosterTotal:  boosterTotal,
		BoosterRemain: boosterRemain,
	}
	return bal, nil
}

// ---------------------------------------------------------------------------
// creditsImpl helpers
// ---------------------------------------------------------------------------

// opReference maps an Operation onto the credit_reservation.reference_type
// column (kept separate from the column name for future-proofing).
type opReference struct {
	refType string
	refID   string // caller-provided at ReferenceID assembly time
}

func referenceFromOp(op Operation) opReference {
	switch op {
	case OpSopRun:
		return opReference{refType: "sop_run"}
	case OpSopChat:
		return opReference{refType: "sop_chat"}
	case OpSalesragChat:
		return opReference{refType: "salesrag_chat"}
	case OpProfileAnalysis:
		return opReference{refType: "profile_analysis"}
	case OpFileParse:
		return opReference{refType: "file_parse"}
	case OpStyleAnalysis:
		return opReference{refType: "style_analysis"}
	case OpOCR:
		return opReference{refType: "ocr"}
	default:
		return opReference{refType: string(op)}
	}
}

// errReservationAlreadyExists is the internal sentinel used to unwind the
// Reserve transaction when a concurrent writer hit the idempotency_key
// unique constraint ahead of us.
var errReservationAlreadyExists = errors.New("reserve: idempotency key winner elsewhere")

// isUniqueKeyViolation detects MySQL/SQLite unique index violations without
// importing driver-specific error types. Both drivers surface the message
// containing the words "UNIQUE constraint failed" (SQLite) or
// "Duplicate entry" (MySQL).
func isUniqueKeyViolation(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return containsAny(s, "UNIQUE constraint failed", "Duplicate entry",
		"duplicate key value", "1062")
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if n == "" {
			continue
		}
		if idx := indexOf(s, n); idx >= 0 {
			return true
		}
	}
	return false
}

// indexOf is a tiny Grep helper to avoid pulling "strings" for one call.
func indexOf(hay, needle string) int {
	if len(needle) == 0 {
		return 0
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// findReservationByIdempKey looks up an existing credit_reservation + its
// items via a plain (non-locking) query. Returns (nil, nil) when no match.
func (c *creditsImpl) findReservationByIdempKey(ctx context.Context, key string) (*Reservation, error) {
	var row model.CreditReservation
	if err := c.store.DB().WithContext(ctx).
		Where("idempotency_key = ?", key).
		First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return c.loadReservationWithItems(ctx, c.store.DB().WithContext(ctx), row.ID)
}

// findReservationByIdempKeyTx is the same lookup but inside a caller-managed
// transaction. Used to recover from a unique-key conflict inside Reserve.
func (c *creditsImpl) findReservationByIdempKeyTx(ctx context.Context, tx *gorm.DB, key string) (*Reservation, error) {
	var row model.CreditReservation
	if err := tx.WithContext(ctx).Where("idempotency_key = ?", key).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return c.loadReservationWithItems(ctx, tx, row.ID)
}

// loadReservationWithItems is the canonical reservation loader used by
// Reconcile/Refund/FinalizeReservation as well as the idempotency path. Use a
// FOR UPDATE variant via SelectForUpdate=true when inside a mutating tx.
func (c *creditsImpl) loadReservationWithItems(ctx context.Context, tx *gorm.DB, reservationID uint64) (*Reservation, error) {
	var row model.CreditReservation
	if err := tx.WithContext(ctx).First(&row, reservationID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrReservationNotFound
		}
		return nil, err
	}
	var items []model.CreditReservationItem
	if err := tx.WithContext(ctx).
		Where("reservation_id = ?", reservationID).
		Order("seq ASC").
		Find(&items).Error; err != nil {
		return nil, err
	}
	return fromDBReservation(&row, items), nil
}

// loadReservationForUpdate is the locking variant (SELECT ... FOR UPDATE).
// Used by Reconcile and Refund to atomically transition state.
func (c *creditsImpl) loadReservationForUpdate(ctx context.Context, tx *gorm.DB, reservationID uint64) (*Reservation, *model.CreditReservation, error) {
	var row model.CreditReservation
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&row, reservationID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrReservationNotFound
		}
		return nil, nil, err
	}
	var items []model.CreditReservationItem
	if err := tx.WithContext(ctx).
		Where("reservation_id = ?", reservationID).
		Order("seq ASC").
		Find(&items).Error; err != nil {
		return nil, nil, err
	}
	return fromDBReservation(&row, items), &row, nil
}

// fromDBReservation maps the GORM model → domain struct.
func fromDBReservation(row *model.CreditReservation, items []model.CreditReservationItem) *Reservation {
	rsv := &Reservation{
		ID:              row.ID,
		UserID:          row.UserID,
		ReferenceType:   row.ReferenceType,
		ReferenceID:     row.ReferenceID,
		Operation:       Operation(row.Operation),
		ReservedCredits: row.ReservedCredits,
		CoefficientID:   row.CoefficientID,
		Status:          ReservationStatus(row.Status),
		ActualCostCents: row.ActualCostCents,
		Delta:           row.Delta,
		FinalizeReason:  row.FinalizeReason,
		IdempotencyKey:  row.IdempotencyKey,
		CreatedAt:       row.CreatedAt,
		ReconciledAt:    row.ReconciledAt,
	}
	if len(items) > 0 {
		rsv.Items = make([]ReservationItem, 0, len(items))
		for _, i := range items {
			rsv.Items = append(rsv.Items, ReservationItem{
				PackageID:        i.PackageID,
				Credits:          i.Credits,
				PackageType:      i.PackageType,
				PackageExpiresAt: i.PackageExpiresAt,
				Seq:              i.Seq,
			})
		}
	}
	return rsv
}

// toDBReservation is the inverse — used only by the idempotency-conflict
// recovery path to rehydrate the outer rsvRow from a domain object.
func toDBReservation(rsv *Reservation) model.CreditReservation {
	return model.CreditReservation{
		ID:              rsv.ID,
		UserID:          rsv.UserID,
		ReferenceType:   rsv.ReferenceType,
		ReferenceID:     rsv.ReferenceID,
		Operation:       string(rsv.Operation),
		ReservedCredits: rsv.ReservedCredits,
		CoefficientID:   rsv.CoefficientID,
		Status:          string(rsv.Status),
		ActualCostCents: rsv.ActualCostCents,
		Delta:           rsv.Delta,
		FinalizeReason:  rsv.FinalizeReason,
		IdempotencyKey:  rsv.IdempotencyKey,
		ReconciledAt:    rsv.ReconciledAt,
		CreatedAt:       rsv.CreatedAt,
	}
}

// toReservationItems maps the FIFO debit []PackageDeduction into the
// domain []ReservationItem (seq is re-derived here from index).
func toReservationItems(items []PackageDeduction) []ReservationItem {
	out := make([]ReservationItem, 0, len(items))
	for i, d := range items {
		out = append(out, ReservationItem{
			PackageID:        d.PackageID,
			Credits:          d.Credits,
			PackageType:      d.PackageType,
			PackageExpiresAt: d.ExpiresAt,
			Seq:              i + 1,
		})
	}
	return out
}

// silence unused-import warnings when the implementation is incomplete.
// Remove once Task C.4 and C.5 wire these up.
var (
	_ = time.Time{}
	_ = log.Infow
)
