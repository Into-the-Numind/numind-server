package credit

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/pricing"
)

// IEstimationBiz is the biz-layer contract for R2 credit estimation. It is
// used by ICreditService.CheckAndEstimate (Task C.5 wiring) and by the admin
// CRUD endpoints that manage credit_estimation_coefficient.
type IEstimationBiz interface {
	// EstimateCredits returns the R2 conservative estimate (in credits) for a
	// single LLM call. Formula per spec §3.11:
	//
	//   promptTokens      = ceil(promptChars × coef.CharToTokenRatio)
	//   completionTokens  = ceil(promptTokens × coef.CompletionPromptRatio)
	//   costCents         = round((promptTokens × inputPrice + completionTokens × outputPrice) / 1e6 × 100)
	//   estimatedCredits  = ceil(costCents × (1 + coef.SafetyBufferPct))
	//
	// The returned CoefficientID is the snapshot used — Reserve records this
	// so Reconcile can (later) compare against the exact same version.
	//
	// Lookup strategy (implements the fallback contract from
	// docs/credits-system-r2-spike-report.md §7):
	//  1. exact match WHERE provider=? AND model=? AND operation=? AND is_active=1
	//  2. global fallback WHERE provider='' AND model='' AND operation='' AND is_active=1
	//  3. neither → ErrCoefficientNotFound
	//
	// Reasoning-mode caveat (Track G concern):
	//   The seed migration uses conservative defaults (buffer=0.3) because the
	//   dev sample lacked semantic-op-level coverage. If/when future seeds
	//   add reasoning-mode models (e.g. claude-sonnet-4-6-thinking), their
	//   completion/prompt ratio can exceed the spec §5.5 [0.05, 3.0] range;
	//   safe values for those rows are 2.5–3.0 rather than the default 0.5.
	EstimateCredits(ctx context.Context, op Operation, promptChars int,
		modelName, provider string) (int64, uint64, error)

	// UpdateCoefficient inserts a new version for the given
	// (provider, model, operation), demotes any prior active row to
	// is_active=0, and activates the new row in a single transaction. The
	// new row's Version is auto-derived as max(existing version)+1.
	//
	// Concurrency: SELECT ... FOR UPDATE locks the existing max-version row
	// so two concurrent writers serialise. Duplicate-key race (both writers
	// computing the same next-version) is resolved via 3 exponential-backoff
	// retries (50 / 100 / 200 ms). After 3 failures returns
	// ErrCoefficientConcurrent (HTTP 503 per spec §2.11.6).
	UpdateCoefficient(ctx context.Context, next *model.CreditEstimationCoefficient) (uint64, error)
}

type estimationBiz struct {
	ds      store.IStore
	pricing pricing.ICalculator
}

// NewEstimationBiz constructs the IEstimationBiz singleton.
// pricing must be a valid ICalculator — EstimateCredits requires it to resolve
// pricing_rule rows.
func NewEstimationBiz(ds store.IStore, pc pricing.ICalculator) IEstimationBiz {
	return &estimationBiz{ds: ds, pricing: pc}
}

// serviceTypeLLMChat is the pricing_rule.service_type used by all LLM-call
// operations. Embedding / OCR / etc. would use different types and are not
// yet wired into ICreditService.
const serviceTypeLLMChat = "llm_chat"

func (b *estimationBiz) EstimateCredits(
	ctx context.Context, op Operation, promptChars int,
	modelName, provider string,
) (int64, uint64, error) {
	coef, err := b.getActiveCoefficient(ctx, provider, modelName, string(op))
	if err != nil {
		return 0, 0, err
	}

	promptTokens := int(math.Ceil(float64(promptChars) * coef.CharToTokenRatio))
	completionTokens := int(math.Ceil(float64(promptTokens) * coef.CompletionPromptRatio))

	// Delegate to pricing.CalculateCost so the same formula drives Estimate
	// and Reconcile (single source of truth). pricing.CalculateCost returns
	// costCents using math.Round on the yuan→cents conversion; that tiny
	// (≤ 1¢) rounding difference against a manual ceil is swallowed by the
	// safety buffer (≥ 20% typical).
	costCents, err := b.pricing.CalculateCost(ctx, serviceTypeLLMChat,
		provider, modelName, promptTokens, completionTokens)
	if err != nil {
		return 0, 0, fmt.Errorf("estimate: pricing lookup: %w", err)
	}

	// Apply safety buffer with ceil (conservative — over-reserve, refund
	// delta at Reconcile).
	estimated := int64(math.Ceil(float64(costCents) * (1 + coef.SafetyBufferPct)))
	return estimated, coef.ID, nil
}

// getActiveCoefficient implements the two-step lookup: exact match, then
// global fallback. Miss on fallback → ErrCoefficientNotFound.
func (b *estimationBiz) getActiveCoefficient(
	ctx context.Context, provider, modelName, op string,
) (*model.CreditEstimationCoefficient, error) {
	var row model.CreditEstimationCoefficient

	// 1. exact match
	err := b.ds.DB().WithContext(ctx).
		Where("provider = ? AND model = ? AND operation = ? AND is_active = ?",
			provider, modelName, op, true).
		First(&row).Error
	if err == nil {
		return &row, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("coefficient exact lookup: %w", err)
	}

	// 2. global fallback
	err = b.ds.DB().WithContext(ctx).
		Where("provider = ? AND model = ? AND operation = ? AND is_active = ?",
			"", "", "", true).
		First(&row).Error
	if err == nil {
		log.Warnw("credit.coefficient.fallback_hit — exact row missing, using global default",
			"provider", provider, "model", modelName, "operation", op,
			"fallback_id", row.ID)
		return &row, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("%w: provider=%s model=%s operation=%s",
			ErrCoefficientNotFound, provider, modelName, op)
	}
	return nil, fmt.Errorf("coefficient fallback lookup: %w", err)
}

// UpdateCoefficient — spec §2.11.6 retry with exponential backoff.
// Attempts: 50 / 100 / 200 ms backoffs (total 3 tries).
func (b *estimationBiz) UpdateCoefficient(ctx context.Context, next *model.CreditEstimationCoefficient) (uint64, error) {
	backoffs := []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond}
	var lastErr error
	for attempt := 0; attempt < len(backoffs)+1; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(backoffs[attempt-1]):
			}
		}
		id, err := b.tryUpdateCoefficientOnce(ctx, next)
		if err == nil {
			return id, nil
		}
		lastErr = err
		if !isUniqueKeyViolation(err) {
			// non-retryable (e.g. validation, real DB error)
			return 0, err
		}
	}
	return 0, fmt.Errorf("%w: last=%v", ErrCoefficientConcurrent, lastErr)
}

// tryUpdateCoefficientOnce is one attempt of the SELECT FOR UPDATE + insert
// pattern. Returns the inserted row's ID.
func (b *estimationBiz) tryUpdateCoefficientOnce(
	ctx context.Context, next *model.CreditEstimationCoefficient,
) (uint64, error) {
	var newID uint64
	err := b.ds.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. Lock the current max-version row (if any) to serialize concurrent
		//    writers against the same (provider, model, operation).
		var current model.CreditEstimationCoefficient
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("provider = ? AND model = ? AND operation = ?",
				next.Provider, next.Model, next.Operation).
			Order("version DESC").
			Limit(1).
			Take(&current).Error

		nextVersion := uint(1)
		if err == nil {
			nextVersion = current.Version + 1
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("lock prior version: %w", err)
		}

		// 2. Demote all prior active rows in the (provider, model, operation)
		//    key to is_active=0 — the new row is the only is_active=1.
		if err := tx.Model(&model.CreditEstimationCoefficient{}).
			Where("provider = ? AND model = ? AND operation = ? AND is_active = ?",
				next.Provider, next.Model, next.Operation, true).
			Update("is_active", false).Error; err != nil {
			return fmt.Errorf("demote prior active: %w", err)
		}

		// 3. Insert the new row.
		insert := *next // copy to avoid mutating the caller's pointer
		insert.ID = 0
		insert.Version = nextVersion
		insert.IsActive = true
		if err := tx.Create(&insert).Error; err != nil {
			return err // bubble duplicate-key to the retry loop
		}
		newID = insert.ID
		return nil
	})
	if err != nil {
		return 0, err
	}
	return newID, nil
}
