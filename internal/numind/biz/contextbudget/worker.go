package contextbudget

import (
	"context"
	"encoding/json"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/contextbudget"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// SummaryJob
// ---------------------------------------------------------------------------

// SummaryJob carries all fields needed to compress a set of fragments and persist
// the resulting summary. It is enqueued by SOP/chatbot producers (Task 9/10) and
// consumed by the background SummaryWorker.
//
// UserID and OwnerUserID use uint to match the types in model.ContextSummary and
// the biz-layer helper deriveOwnerUserID (which returns uint).
type SummaryJob struct {
	// UserID is the ID of the user who triggered the operation.
	UserID uint
	// OwnerUserID is the tenant key for summary storage and lookup.
	// For B2B2C child accounts this is the parent user's ID (spec §3.4).
	// Must be non-zero; jobs with OwnerUserID=0 are rejected defensively.
	OwnerUserID uint
	// ScopeType classifies the conversation scope (e.g., "sop_run", "chat_session").
	ScopeType string
	// ScopeID is the unique identifier of the scope instance.
	ScopeID string
	// SourceHash is the content-addressable hash of the source fragments,
	// used as the unique key for summary cache lookup (spec §3.4).
	SourceHash string
	// SourceFragmentIDs lists the IDs of the fragments being summarised.
	SourceFragmentIDs []string
	// Fragments contains the actual fragment content for the compressor.
	Fragments []contextbudget.ContextFragment
	// Operation is the billing operation that triggered the summary (e.g., "chatbot_chat").
	Operation string
}

// ---------------------------------------------------------------------------
// WorkerOptions
// ---------------------------------------------------------------------------

// WorkerOptions configures the SummaryWorker at construction time.
type WorkerOptions struct {
	// QueueSize sets the capacity of the buffered job channel.
	// When the channel is full, Enqueue drops the job with a warning log.
	// Default (0 → use defaultQueueSize) produces a 100-slot buffer.
	QueueSize int
	// Logger is a structured logger. When nil all logging is silently dropped.
	Logger interface {
		Warnw(msg string, kv ...interface{})
		Errorw(msg string, kv ...interface{})
	}
}

// defaultQueueSize is the buffered channel capacity used when WorkerOptions.QueueSize is 0.
// 100 slots provides enough headroom for burst traffic (e.g., many sessions generating
// summaries concurrently) without consuming excessive memory. Each SummaryJob is
// small (struct with slices of fragments), so 100 jobs ≈ a few MB at most.
const defaultQueueSize = 100

// ---------------------------------------------------------------------------
// SummaryWorker
// ---------------------------------------------------------------------------

// SummaryWorker is an in-process background worker that consumes SummaryJobs from
// a buffered channel, runs the compressor, and persists the result to context_summary.
//
// It is started once per process via Run and stopped by cancelling the context.
// Enqueue is non-blocking: if the channel is full the job is dropped with a warning.
type SummaryWorker struct {
	store      store.ContextBudgetStore
	compressor Compressor
	queue      chan SummaryJob
	logger     interface {
		Warnw(msg string, kv ...interface{})
		Errorw(msg string, kv ...interface{})
	}
}

// NewSummaryWorker constructs a SummaryWorker. The store must be non-nil.
// If opts.QueueSize is 0, defaultQueueSize is used.
// If opts.Logger is nil, all log output is silently discarded.
func NewSummaryWorker(s store.ContextBudgetStore, compressor Compressor, opts WorkerOptions) *SummaryWorker {
	size := opts.QueueSize
	if size <= 0 {
		size = defaultQueueSize
	}
	lg := opts.logger()
	return &SummaryWorker{
		store:      s,
		compressor: compressor,
		queue:      make(chan SummaryJob, size),
		logger:     lg,
	}
}

// logger resolves the logger from WorkerOptions; falls back to noopLog when nil.
func (o WorkerOptions) logger() interface {
	Warnw(msg string, kv ...interface{})
	Errorw(msg string, kv ...interface{})
} {
	if o.Logger != nil {
		return o.Logger
	}
	return noopLog{}
}

// Enqueue submits a job to the background worker queue. It is non-blocking:
// if the queue is full, the job is dropped and a warning is logged.
// Callers (SOP/chatbot producers) should never block on Enqueue.
func (w *SummaryWorker) Enqueue(job SummaryJob) {
	select {
	case w.queue <- job:
	default:
		w.logger.Warnw("summary worker queue full, dropping job",
			"scope_type", job.ScopeType,
			"scope_id", job.ScopeID,
			"owner_user_id", job.OwnerUserID,
		)
	}
}

// Run starts the worker's processing loop and blocks until ctx is cancelled.
// It should be launched in a goroutine:
//
//	go worker.Run(ctx)
//
// Shutdown is automatic when the context is cancelled; no additional cleanup is needed.
// Run is idempotent in terms of correctness but callers should ensure it is called
// only once (the numind.go wiring wraps it in a sync.Once for safety).
func (w *SummaryWorker) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job, ok := <-w.queue:
			if !ok {
				// Channel was closed — exit gracefully.
				return
			}
			w.processJob(ctx, job)
		}
	}
}

// processJob compresses the job's fragments and persists the result.
// All errors are absorbed: the caller's HTTP path is never affected.
//
// Processing steps:
//  1. Validate OwnerUserID != 0 (cross-tenant protection).
//  2. Validate compressor is non-nil.
//  3. Call compressor.Compress with a target token count of 40 % of source size.
//  4. On success: UpsertSummary with status='ready'.
//  5. On failure: UpsertSummary with status='failed' + error_message (truncated to 500 chars).
func (w *SummaryWorker) processJob(ctx context.Context, job SummaryJob) {
	// Defensive: reject jobs with missing owner to prevent cross-tenant pollution.
	if job.OwnerUserID == 0 {
		w.logger.Warnw("summary worker: rejecting job with OwnerUserID=0 (cross-tenant protection)",
			"scope_type", job.ScopeType,
			"scope_id", job.ScopeID,
		)
		return
	}

	ownerID := job.OwnerUserID
	summary := &model.ContextSummary{
		UserID:             job.UserID,
		OwnerUserID:        &ownerID,
		ScopeType:          job.ScopeType,
		ScopeID:            job.ScopeID,
		SourceHash:         job.SourceHash,
		SourceFragmentIDs:  marshalFragmentIDs(job.SourceFragmentIDs),
		CreatedByOperation: job.Operation,
	}

	// Step 2: require a compressor.
	if w.compressor == nil {
		w.storeFailed(ctx, summary, "compressor not configured")
		return
	}

	// Step 3: estimate target token budget (40 % of source).
	targetTokens := estimateTargetTokens(job.Fragments)

	// Step 4: run compression.
	resultFrag, err := w.compressor.Compress(ctx, job.Fragments, targetTokens)
	if err != nil {
		w.storeFailed(ctx, summary, err.Error())
		return
	}

	// Step 5: persist success.
	summary.Status = "ready"
	summary.SummaryText = resultFrag.Content
	if err := w.store.UpsertSummary(ctx, summary); err != nil {
		// Log but do not propagate — caller is already past the main request path.
		w.logger.Errorw("summary worker: UpsertSummary (ready) failed",
			"scope_type", job.ScopeType,
			"scope_id", job.ScopeID,
			"error", err,
		)
	}
}

// storeFailed persists a context_summary row with status='failed' and the given
// error string. UpsertSummary errors are logged but not returned.
func (w *SummaryWorker) storeFailed(ctx context.Context, summary *model.ContextSummary, errMsg string) {
	summary.Status = "failed"
	summary.SummaryText = "" // empty on failure
	summary.ErrorMessage = truncateError(errMsg, 500)
	if err := w.store.UpsertSummary(ctx, summary); err != nil {
		w.logger.Errorw("summary worker: UpsertSummary (failed) failed",
			"scope_type", summary.ScopeType,
			"scope_id", summary.ScopeID,
			"error", err,
		)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// estimateTargetTokens returns the target token count for a compression pass.
// The target is 40 % of the sum of TokenEstimate values across all fragments
// (matching applyPlan's compression ratio), with a floor of 128.
func estimateTargetTokens(fragments []contextbudget.ContextFragment) int {
	total := 0
	for _, f := range fragments {
		total += f.TokenEstimate
	}
	if total <= 0 {
		return 512 // safe default when fragments carry no pre-estimates
	}
	target := int(float64(total) * 0.40)
	if target < 128 {
		target = 128
	}
	return target
}

// truncateError truncates errMsg to at most maxLen characters, appending "…" when cut.
func truncateError(errMsg string, maxLen int) string {
	if len(errMsg) <= maxLen {
		return errMsg
	}
	// Truncate by rune to avoid splitting multi-byte characters.
	runes := []rune(errMsg)
	if len(runes) <= maxLen {
		return errMsg
	}
	return string(runes[:maxLen-1]) + "…"
}

// marshalFragmentIDs serialises a []string to datatypes.JSON.
// Returns an empty JSON array on error.
func marshalFragmentIDs(ids []string) []byte {
	if len(ids) == 0 {
		return []byte("[]")
	}
	b, err := json.Marshal(ids)
	if err != nil {
		return []byte("[]")
	}
	return b
}

// Compile-time assertion: noopLog satisfies the worker logger interface.
// noopLog is defined in biz.go and reused here since both Biz and SummaryWorker
// share the same logger interface shape.
var _ interface {
	Warnw(msg string, kv ...interface{})
	Errorw(msg string, kv ...interface{})
} = noopLog{}
