package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/metrics"
	"numind-server/internal/pkg/model"
)

// ExtractionJob is one unit of background work pulled by a worker.
type ExtractionJob struct {
	UserID    uint
	SessionID string
	Messages  []ChatMessage
	EnqueueAt time.Time
}

// ─── Tuning constants ─────────────────────────────────────────────────────────

// DefaultExtractorWorkers is the worker-pool size for ExtractorService.
const DefaultExtractorWorkers = 5

// DefaultExtractorQueueCap caps in-flight jobs; over-cap → drop with warn log.
const DefaultExtractorQueueCap = 1024

// DefaultDebounceWindow is the per-user debounce — same user re-enqueues within
// this window are collapsed (newer wins via two-stage worker-side check).
const DefaultDebounceWindow = 30 * time.Second

// WorkerStaleSkipDelta is the secondary debounce skip threshold: if the
// debounce map's "last enqueue at" entry is more than this much newer than
// the job's EnqueueAt, the worker skips this job in favour of the upcoming
// fresher one. Spec §"30s Debounce 算法" — drop newest + reschedule.
const WorkerStaleSkipDelta = 25 * time.Second

// DefaultMinConfidence is the threshold for fact persistence (spec D3).
const DefaultMinConfidence = 0.70

// ExtractionCountRebuildThreshold is the per-user counter that triggers
// RebuildNarrative. Spec §"Narrative 更新": 简化版 5 次后跑一次 profile rebuild.
const ExtractionCountRebuildThreshold = 5

// extractorLLMMaxTokens caps generation budget for the JSON-array output.
// Conservative: 6 facts × ~80 chars × 2 tokens/char ≈ 960. Pad to 800.
const extractorLLMMaxTokens = 800

// extractorLLMTemperature: low randomness for structured extraction.
const extractorLLMTemperature = 0.2

// rebuilderLLMMaxTokens caps narrative generation (3 short text fields).
const rebuilderLLMMaxTokens = 600

// rebuilderLLMTemperature: equally low.
const rebuilderLLMTemperature = 0.2

// ─── ExtractorService ────────────────────────────────────────────────────────

// ExtractorService is the async LLM-driven memory extraction pipeline.
//
// Lifecycle:
//
//	svc := NewExtractorService(factStore, profileStore)
//	svc.Start(ctx)            // launches DefaultExtractorWorkers goroutines
//	defer svc.Stop()          // graceful drain on shutdown
//	svc.Enqueue(uid, sid, msgs, false)   // called from AgentRunner.handleTerminated
//
// Invariants:
//   - Enqueue is non-blocking — queue full = log warn + drop (never block runner)
//   - isTrivial=true bypasses entirely (spec §设计要点)
//   - Workers panic-recover individually; one crash does not kill the pool
//   - All LLM calls go through aiservice.Chat with profile.AgentMemoryExtract
//
// Concurrency safety: jobQueue is a buffered chan; debounceMap is sync.Map;
// workerCount + storeRefs are immutable after construction. The `chat` seam is
// guarded by chatMu so tests can hot-swap mid-flight via swapChatFnForTest
// without racing the workers.
type ExtractorService struct {
	factStore    store.IUserMemoryFactStore
	profileStore store.IUserMemoryProfileStore

	jobQueue    chan ExtractionJob
	debounceMap sync.Map // uint(userID) -> time.Time (last enqueue at)
	workerCount int

	// chat is the LLM-call seam — production wires aiservice.Chat; tests inject a mock.
	// Guarded by chatMu (P2.C: tests may swap mid-flight; workers read concurrently).
	chatMu sync.Mutex
	chat   extractorChatFn

	// dialecticSvc is the Task 3.7 Layer A dialectic hook — when set, extract()
	// fires MaybeRecompute(userID) after a successful persistFacts so the
	// background dialectic pipeline can refresh cached_insight per cadence
	// gating. Read under dialecticMu (lock-free for production where the
	// service is wired once at startup; mu only matters for tests that swap).
	// nil = no dialectic refresh (Task 3.3 standalone callers preserve their
	// existing behaviour).
	dialecticMu  sync.Mutex
	dialecticSvc DialecticService

	// debounceWindow / minConfidence / rebuildThreshold are settable via With* opts.
	debounceWindow   time.Duration
	minConfidence    float64
	rebuildThreshold int

	stopCtx    context.Context
	stopCancel context.CancelFunc
	wg         sync.WaitGroup
	startOnce  sync.Once
	stopOnce   sync.Once
}

// loadChatFn reads the chat seam under chatMu. Called by workers each job.
func (s *ExtractorService) loadChatFn() extractorChatFn {
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	return s.chat
}

// SetDialecticService installs (or replaces) the Task 3.7 dialectic hook
// after construction. Useful when the dialectic service depends on the
// extractor's stores (forward-declaration ordering issue in biz.go wiring).
// Concurrency-safe — workers read through loadDialecticSvc on every extract.
func (s *ExtractorService) SetDialecticService(svc DialecticService) {
	s.dialecticMu.Lock()
	defer s.dialecticMu.Unlock()
	s.dialecticSvc = svc
}

// loadDialecticSvc reads the dialectic hook under dialecticMu so SetDialecticService
// can swap without racing the worker pool.
func (s *ExtractorService) loadDialecticSvc() DialecticService {
	s.dialecticMu.Lock()
	defer s.dialecticMu.Unlock()
	return s.dialecticSvc
}

// swapChatFnForTest replaces the LLM seam at runtime. Test-only — production
// constructs the final chat fn via WithExtractorChatFn before Start.
// Lower-case unexported on purpose: outside callers should use With* options
// at construction, not mutate a running service.
func (s *ExtractorService) swapChatFnForTest(fn extractorChatFn) {
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	s.chat = fn
}

// extractorChatFn is the seam used by ExtractorService to call aiservice.Chat.
// Tests override via WithChatFn to inject deterministic / failing responses
// without dialling a real LLM gateway.
type extractorChatFn func(ctx context.Context, taskID string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error)

// ExtractorOption configures an ExtractorService at construction (functional opts).
type ExtractorOption func(*ExtractorService)

// WithExtractorChatFn replaces the LLM-call seam. Production callers omit;
// tests pass a deterministic mock.
func WithExtractorChatFn(fn extractorChatFn) ExtractorOption {
	return func(s *ExtractorService) { s.chat = fn }
}

// WithExtractorWorkers overrides DefaultExtractorWorkers (clamped to ≥ 1).
func WithExtractorWorkers(n int) ExtractorOption {
	return func(s *ExtractorService) {
		if n < 1 {
			n = 1
		}
		s.workerCount = n
	}
}

// WithExtractorQueueCap overrides DefaultExtractorQueueCap (clamped ≥ 1).
// Useful for tests that want to exercise the "queue full" branch.
func WithExtractorQueueCap(n int) ExtractorOption {
	return func(s *ExtractorService) {
		if n < 1 {
			n = 1
		}
		s.jobQueue = make(chan ExtractionJob, n)
	}
}

// WithExtractorDebounceWindow overrides DefaultDebounceWindow.
func WithExtractorDebounceWindow(d time.Duration) ExtractorOption {
	return func(s *ExtractorService) { s.debounceWindow = d }
}

// WithExtractorMinConfidence overrides DefaultMinConfidence.
func WithExtractorMinConfidence(c float64) ExtractorOption {
	return func(s *ExtractorService) { s.minConfidence = c }
}

// WithExtractorRebuildThreshold overrides ExtractionCountRebuildThreshold.
func WithExtractorRebuildThreshold(n int) ExtractorOption {
	return func(s *ExtractorService) { s.rebuildThreshold = n }
}

// WithExtractorDialecticService installs a Task 3.7 DialecticService so that
// every successful persistFacts call (≥ 1 fact inserted or deduped) triggers
// dialecticSvc.MaybeRecompute(userID). The dialectic service decides — via
// its embedded CadenceService — whether to actually run the LLM, so calling
// MaybeRecompute on every extraction is cheap (no LLM cost when cadence gate
// says "skip").
//
// nil (default) preserves Task 3.3-only behaviour where no dialectic
// recompute is scheduled. Set this in production wire-up (biz.go).
//
// Layer A only: the recompute targets user_memory_profile.cached_insight for
// the agent **user** themselves — never the customer / subject / dataset they
// happen to be discussing.
func WithExtractorDialecticService(svc DialecticService) ExtractorOption {
	return func(s *ExtractorService) { s.dialecticSvc = svc }
}

// NewExtractorService constructs an ExtractorService with sane production defaults.
// Caller must call Start(ctx) before Enqueue, and Stop() during shutdown.
func NewExtractorService(
	factStore store.IUserMemoryFactStore,
	profileStore store.IUserMemoryProfileStore,
	opts ...ExtractorOption,
) *ExtractorService {
	s := &ExtractorService{
		factStore:        factStore,
		profileStore:     profileStore,
		jobQueue:         make(chan ExtractionJob, DefaultExtractorQueueCap),
		workerCount:      DefaultExtractorWorkers,
		debounceWindow:   DefaultDebounceWindow,
		minConfidence:    DefaultMinConfidence,
		rebuildThreshold: ExtractionCountRebuildThreshold,
		chat: func(ctx context.Context, taskID string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
			return aiservice.Chat(ctx, taskID, req)
		},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Start launches workerCount goroutines that drain jobQueue until Stop or
// ctx cancellation. Idempotent — only the first call has effect.
func (s *ExtractorService) Start(ctx context.Context) {
	s.startOnce.Do(func() {
		s.stopCtx, s.stopCancel = context.WithCancel(ctx)
		for i := 0; i < s.workerCount; i++ {
			s.wg.Add(1)
			go s.worker(s.stopCtx, i)
		}
		log.Infow("memory.ExtractorService started",
			"workers", s.workerCount,
			"queue_cap", cap(s.jobQueue),
			"debounce_window", s.debounceWindow.String(),
			"min_confidence", s.minConfidence,
			"rebuild_threshold", s.rebuildThreshold,
		)
	})
}

// Stop signals all workers to drain and waits for them. Idempotent.
//
// Drain semantics: only the worker goroutines (workerCount of them) are tracked
// by s.wg, so Stop returns once every worker exits. Detached rebuild goroutines
// fired by maybeRebuildProfile (60s timeout each, runs on context.Background)
// are NOT in the wg — they may outlive Stop() by up to 60 s. Acceptable given
// the in-flight count is bounded (at most #users-hitting-threshold-near-shutdown)
// and rebuild does write-side work only (UpdateNarrative / ResetExtractionCount).
// Tightening to a full drain would require coupling the rebuild timeout to a
// shutdown ctx — beyond Task 3.3 scope.
func (s *ExtractorService) Stop() {
	s.stopOnce.Do(func() {
		if s.stopCancel != nil {
			s.stopCancel()
		}
		// Closing the queue lets workers exit their select loops gracefully
		// once any in-flight job completes. The select still has ctx.Done
		// to break early.
		close(s.jobQueue)
		s.wg.Wait()
		log.Infow("memory.ExtractorService stopped")
	})
}

// Enqueue accepts a job for async extraction.
//
// Non-blocking by design — Spec § "队列满 1024 → log.Warnw + drop, 不阻塞 user turn".
//
//   - isTrivial=true → bypass everything (spec § "trivial turn 短路")
//   - Same user within debounceWindow → debounce map updates but worker may
//     still skip the older job at pull time via WorkerStaleSkipDelta check
//   - Queue full → metrics + log warn + drop
//
// Note: We always enqueue and rely on worker-side stale-skip (WorkerStaleSkipDelta=25s)
// for debounce. This is equivalent to spec's "skip enqueue within 30s" semantics:
// same-user rapid enqueues all get stale-skipped at worker time except the most recent.
// Trade-off: a few extra queue slots used briefly (1-2 per user), but simpler than
// fingerprint-based per-user de-queue check.
//
// Callers (AgentRunner.handleTerminated) MUST NOT block on this — the call
// returns immediately even on the drop path.
func (s *ExtractorService) Enqueue(userID uint, sessionID string, msgs []ChatMessage, isTrivial bool) {
	if isTrivial {
		// Spec § 设计要点: trivial turn 直接 return, 不入队.
		return
	}
	if userID == 0 {
		// Unauthenticated context — nothing to do.
		return
	}
	// P1.A: guard against send-on-closed-channel during shutdown. Stop() calls
	// close(s.jobQueue) after cancelling s.stopCtx; any concurrent Enqueue from
	// in-flight agentRunner.Run would otherwise panic with "send on closed
	// channel". The select's `default` branch only catches a *full* open chan,
	// not a closed one. Drop silently — Enqueue contract is best-effort.
	if s.stopCtx != nil && s.stopCtx.Err() != nil {
		return
	}
	now := time.Now()
	s.debounceMap.Store(userID, now)

	select {
	case s.jobQueue <- ExtractionJob{
		UserID:    userID,
		SessionID: sessionID,
		Messages:  msgs,
		EnqueueAt: now,
	}:
		metrics.MemoryQueueDepthSet(int64(len(s.jobQueue)))
	default:
		// Queue full — drop and continue. Runner must never block.
		log.Warnw("memory.ExtractorService queue full, dropping job",
			"user_id", userID, "session_id", sessionID, "queue_cap", cap(s.jobQueue))
	}
}

// ─── Worker loop ──────────────────────────────────────────────────────────────

// worker is one goroutine in the pool. Pulls jobs, runs extract(), and
// recovers from panics individually so a single crash doesn't kill the pool.
func (s *ExtractorService) worker(ctx context.Context, idx int) {
	defer s.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case job, ok := <-s.jobQueue:
			if !ok {
				// Channel closed by Stop — drain done.
				return
			}
			s.runJobSafely(ctx, job, idx)
			metrics.MemoryQueueDepthSet(int64(len(s.jobQueue)))
		}
	}
}

// runJobSafely wraps extract() in a panic recover so one bad job can't
// kill the worker goroutine.
func (s *ExtractorService) runJobSafely(ctx context.Context, job ExtractionJob, workerIdx int) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorw("memory.ExtractorService worker panic recovered",
				"worker_idx", workerIdx,
				"user_id", job.UserID,
				"session_id", job.SessionID,
				"panic", fmt.Sprintf("%v", r))
			metrics.MemoryExtractionRunsInc(metrics.MemoryExtractionLLMError)
		}
	}()
	s.extract(ctx, job)
}

// ─── Extract pipeline ─────────────────────────────────────────────────────────

// extract is the single-job extraction sequence. Errors are logged + swallowed —
// LLM failures do NOT retry (spec § Worker Pool: 下个 turn 自然会再触发).
//
// Sequence:
//  1. Secondary debounce skip (drop-newest pattern from spec)
//  2. Build prompt + call aiservice.Chat with profile.AgentMemoryExtract
//  3. Parse JSON array; on parse error → metric + return (no retry)
//  4. Filter by confidence ≥ minConfidence + category whitelist
//  5. Per-fact hash dedup: same hash hit → UpdateConfidence(max(old, new))
//     ; miss → INSERT
//  6. Increment per-user extraction count; if ≥ rebuildThreshold → fire
//     RebuildNarrative + reset count
func (s *ExtractorService) extract(ctx context.Context, job ExtractionJob) {
	// Step 1 — secondary debounce: if debounceMap shows a newer enqueue,
	// skip this job and let the upcoming fresher one run instead.
	if v, ok := s.debounceMap.Load(job.UserID); ok {
		if latest, ok2 := v.(time.Time); ok2 {
			if latest.Sub(job.EnqueueAt) > WorkerStaleSkipDelta {
				metrics.MemoryExtractionRunsInc(metrics.MemoryExtractionSkippedDebounce)
				log.Debugw("memory.extractor skipped stale job (newer enqueue pending)",
					"user_id", job.UserID, "age_ms", time.Since(job.EnqueueAt).Milliseconds())
				return
			}
		}
	}

	// Step 2 — Langfuse trace + LLM call.
	traceID := langfuse.TraceID()
	langfuse.CreateTrace(traceID, "memory.extract",
		langfuse.WithUserID(job.UserID),
		langfuse.WithTraceInput(map[string]any{
			"session_id":    job.SessionID,
			"messages_len":  len(job.Messages),
			"min_threshold": s.minConfidence,
		}),
		langfuse.WithTraceTags("memory", "extract", "layer-a"),
	)
	ctx = langfuse.WithTrace(ctx, traceID)

	promptUser := buildExtractionPrompt(job.Messages)
	genID := langfuse.SpanID()
	langfuse.CreateGeneration(traceID, genID,
		langfuse.WithGenName("memory.extract.llm"),
		langfuse.WithGenInput(promptUser),
	)

	// P2.C: read chat seam through chatMu so swapChatFnForTest is race-free.
	chatFn := s.loadChatFn()
	resp, err := chatFn(ctx, profile.AgentMemoryExtract, aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleSystem, Content: aiservice.MessageContent{Text: ExtractionPromptSystem}},
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: promptUser}},
		},
		MaxTokens:   extractorLLMMaxTokens,
		Temperature: extractorLLMTemperature,
	})
	if err != nil {
		metrics.MemoryExtractionRunsInc(metrics.MemoryExtractionLLMError)
		langfuse.EndGeneration(traceID, genID,
			langfuse.WithGenOutput(map[string]string{"error": err.Error()}),
		)
		log.Warnw("memory.extractor LLM call failed",
			"user_id", job.UserID, "session_id", job.SessionID, "error", err)
		return
	}

	// Step 3 — Parse JSON array.
	facts, parseErr := parseExtractionResponse(resp.Content)
	langfuse.EndGeneration(traceID, genID,
		langfuse.WithGenOutput(resp.Content),
		langfuse.WithGenModel(resp.Model),
		langfuse.WithGenUsage(resp.Usage.PromptTokens, resp.Usage.CompletionTokens),
	)
	if parseErr != nil {
		metrics.MemoryExtractionRunsInc(metrics.MemoryExtractionParseError)
		log.Warnw("memory.extractor JSON parse failed",
			"user_id", job.UserID, "session_id", job.SessionID,
			"raw_len", len(resp.Content), "error", parseErr)
		return
	}

	// Step 4-5 — Filter + dedup + persist.
	persisted, deduped := s.persistFacts(ctx, job, facts)
	metrics.MemoryFactsExtractedAdd(int64(persisted + deduped))
	metrics.MemoryExtractionRunsInc(metrics.MemoryExtractionSuccess)

	// Step 6 — Increment per-user counter, maybe trigger profile rebuild.
	if persisted+deduped > 0 {
		s.maybeRebuildProfile(ctx, job.UserID)
	}

	// Step 7 (Task 3.7) — notify dialectic service that new facts landed.
	// MaybeRecompute is non-blocking and internally cadence-gated; safe to
	// call on every successful turn. Only fire when at least one fact was
	// persisted or deduped (no-fact turns leave the cached_insight valid).
	//
	// Layer A: the dialectic refresh targets the agent user's cached_insight;
	// no fact written to user_memory_facts can carry a non-nil subject_id in
	// V1.5 (store-layer invariant — see user_memory.go:282/302).
	if persisted+deduped > 0 {
		if dial := s.loadDialecticSvc(); dial != nil {
			dial.MaybeRecompute(ctx, job.UserID)
		}
	}

	log.Infow("memory.extractor turn complete",
		"user_id", job.UserID,
		"session_id", job.SessionID,
		"raw_facts", len(facts),
		"persisted", persisted,
		"deduped", deduped,
	)
}

// parseExtractionResponse takes the raw LLM text and decodes it as a JSON array.
//
// Tolerates two common deviations:
//   - leading/trailing whitespace
//   - markdown code fences (```json ... ```) accidentally emitted despite the prompt
//
// Anything else (object root, garbage text) → returns a parse error.
func parseExtractionResponse(raw string) ([]ExtractedFact, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, errors.New("empty response")
	}
	// Strip markdown fence if present.
	if strings.HasPrefix(s, "```") {
		// Find first newline (drops the language tag like ```json) and the trailing fence.
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
		if j := strings.LastIndex(s, "```"); j >= 0 {
			s = s[:j]
		}
		s = strings.TrimSpace(s)
	}
	// Accept both array root and {"items": [...]} envelope (defensive).
	if strings.HasPrefix(s, "[") {
		var out []ExtractedFact
		if err := json.Unmarshal([]byte(s), &out); err != nil {
			return nil, fmt.Errorf("parse array: %w", err)
		}
		return out, nil
	}
	if strings.HasPrefix(s, "{") {
		var env struct {
			Items []ExtractedFact `json:"items"`
		}
		if err := json.Unmarshal([]byte(s), &env); err != nil {
			return nil, fmt.Errorf("parse object envelope: %w", err)
		}
		return env.Items, nil
	}
	return nil, fmt.Errorf("response is neither array nor object: %.40q", s)
}

// persistFacts walks the LLM facts, validates each, dedup-checks, and writes.
// Returns (insertedCount, dedupedCount). Validation failures are logged at
// debug level (high-volume noise during normal operation).
func (s *ExtractorService) persistFacts(ctx context.Context, job ExtractionJob, facts []ExtractedFact) (inserted, deduped int) {
	for _, f := range facts {
		if err := validateExtractedFact(f, s.minConfidence); err != nil {
			log.Debugw("memory.extractor dropped fact (validation)",
				"user_id", job.UserID, "reason", err.Error())
			continue
		}
		hash := computeContentHash(f.Content)

		// Dedup check.
		existing, err := s.factStore.FindByEmbedHash(ctx, job.UserID, hash)
		switch {
		case err == nil && existing != nil:
			// Hash hit — promote confidence to max(old, new).
			newConf := f.Confidence
			if existing.Confidence > newConf {
				newConf = existing.Confidence
			}
			if uerr := s.factStore.UpdateConfidence(ctx, existing.ID, newConf); uerr != nil {
				log.Warnw("memory.extractor UpdateConfidence failed",
					"user_id", job.UserID, "fact_id", existing.ID, "error", uerr)
				continue
			}
			deduped++
			metrics.MemoryDedupHitsInc()
		case errors.Is(err, gorm.ErrRecordNotFound):
			// Miss — INSERT new fact.
			// P1.C: EscapeForStorage matches the L1 sync-turn invariant
			// (provider.go:156). FenceRenderer.RenderMemoryBlock assumes DB
			// content is already escaped — when future code wires this L2
			// store into the system-prompt memory-context fence, unescaped
			// `<`/`>`/`&` from LLM-derived fact content would break the XML
			// structure (or enable prompt injection). Apply at the write
			// boundary so the invariant holds regardless of consumer wiring.
			row := &model.UserMemoryFact{
				UUID:              uuid.New().String(),
				UserID:            job.UserID,
				SubjectID:         nil, // V1.5 Layer A: always nil
				Content:           EscapeForStorage(strings.TrimSpace(f.Content)),
				Category:          f.Category,
				Confidence:        f.Confidence,
				Importance:        0.50,
				SourceSessionID:   job.SessionID,
				SourceMessageUUID: f.SourceMessageUUID,
				SourceExtractedAt: time.Now(),
				EmbeddingHash:     hash,
				IsArchived:        false,
			}
			if cerr := s.factStore.Create(ctx, row); cerr != nil {
				log.Warnw("memory.extractor Create fact failed",
					"user_id", job.UserID, "error", cerr)
				continue
			}
			inserted++
		default:
			// Unknown error — log and skip this fact.
			log.Warnw("memory.extractor FindByEmbedHash failed",
				"user_id", job.UserID, "error", err)
			continue
		}
	}
	return inserted, deduped
}

// computeContentHash normalises the text and returns the lowercase hex SHA-256.
//
// normalisation rules (spec §"Hash Dedup"):
//   - lowercase (ASCII)
//   - strip leading/trailing whitespace
//   - collapse internal whitespace runs to a single space
//   - drop ASCII punctuation that doesn't carry semantic meaning ( . , ! ? ; : ' " )
//
// Chinese-only punctuation (，。！？) is also normalised to keep
// "用户是销售，喜欢简洁" == "用户是销售喜欢简洁" for dedup purposes.
func computeContentHash(s string) string {
	norm := normalizeForHash(s)
	if norm == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])
}

// normalizeForHash applies the punctuation + whitespace + case normalisation.
func normalizeForHash(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := true // suppress leading whitespace
	for _, r := range s {
		// Map both ASCII and CJK punctuation to "skip".
		// CJK smart quotes (U+201C/D/8/9) listed via numeric escapes to avoid
		// look-alike characters in source.
		switch r {
		case '.', ',', '!', '?', ';', ':', '\'', '"',
			'。', '，', '！', '？', '；', '：',
			'“', '”', '‘', '’':
			continue
		case ' ', '\t', '\n', '\r', '　':
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		// Lowercase ASCII; leave CJK / other Unicode untouched.
		if r >= 'A' && r <= 'Z' {
			r = r + ('a' - 'A')
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return strings.TrimRight(b.String(), " ")
}

// maybeRebuildProfile increments the per-user extraction counter; when it
// reaches rebuildThreshold, fires RebuildNarrative in a detached goroutine
// (so it does not block the current job).
//
// Reset is done by RebuildNarrative on success — if it fails, the counter
// keeps growing and the next extract() will try again (degrade gracefully).
func (s *ExtractorService) maybeRebuildProfile(ctx context.Context, userID uint) {
	newCount, err := s.profileStore.IncrementExtractionCount(ctx, userID)
	if err != nil {
		log.Warnw("memory.extractor IncrementExtractionCount failed",
			"user_id", userID, "error", err)
		return
	}
	if newCount < s.rebuildThreshold {
		return
	}
	// Threshold hit — fire rebuild in detached goroutine. Use background ctx so
	// caller cancellation doesn't kill the rebuild mid-flight.
	// P2.C: snapshot chat seam through chatMu before goroutine launch so the
	// detached call doesn't race with swapChatFnForTest.
	rebuildChatFn := s.loadChatFn()
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				log.Errorw("memory.extractor RebuildNarrative panic recovered",
					"user_id", userID, "panic", fmt.Sprintf("%v", r))
			}
		}()
		if rerr := RebuildNarrative(bgCtx, s.factStore, s.profileStore, rebuildChatFn, userID); rerr != nil {
			log.Warnw("memory.extractor RebuildNarrative failed",
				"user_id", userID, "error", rerr)
			// Do NOT reset counter on failure — retry on next extraction.
			return
		}
		if rerr := s.profileStore.ResetExtractionCount(bgCtx, userID); rerr != nil {
			log.Warnw("memory.extractor ResetExtractionCount failed",
				"user_id", userID, "error", rerr)
		}
	}()
}
