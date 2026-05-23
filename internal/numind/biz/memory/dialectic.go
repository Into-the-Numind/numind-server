package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/metrics"
	"numind-server/internal/pkg/model"
)

// ─── Tuning constants ─────────────────────────────────────────────────────────

// DefaultDialecticTopFactsLimit caps the candidate fact set passed to the
// dialectic LLM. Spec §关键参数: balances coverage vs prompt token budget
// (20 facts × ~50 chars × 2 tokens/char ≈ 2000 tokens — well under qwen-plus's
// 128K context).
const DefaultDialecticTopFactsLimit = 20

// DefaultDialecticMaxOutputTokens caps the dialectic response. 300-500 中文字
// ≈ 450-750 tokens; pad to 600 to allow brief over-shoot before length-trim.
const DefaultDialecticMaxOutputTokens = 600

// DefaultDialecticTemperature: spec §关键参数 "略稳定 (不要发散) 但保留推理弹性".
const DefaultDialecticTemperature = 0.4

// DefaultDialecticCallTimeout is the upper-bound wall time for one dialectic
// pipeline (list facts → LLM call → validate → write cache). qwen-plus
// averages 4-8s per call; 30s leaves headroom for parallel queue load.
const DefaultDialecticCallTimeout = 30 * time.Second

// dialecticInsightMinRunes / dialecticInsightMaxRunes bound the accepted
// insight length. Spec §关键参数 "validInsight 范围 100-800 runes — 容错,
// 截掉异常超短/超长输出". An insight outside this range is treated as a failed
// LLM response (kept old cache, failed counter +1).
const (
	dialecticInsightMinRunes = 100
	dialecticInsightMaxRunes = 800
)

// ─── DialecticService interface + config ─────────────────────────────────────

// DialecticService is the Layer A dialectic reasoning gate.
//
// Layer A only: every method operates on the agent **user themselves** — the
// sales rep / SOP operator / data analyst / PPT clerk / etc. who is using the
// agent. The cached_insight describes that user, NEVER the customer / dataset
// / document / production-line they happen to be discussing (V2 Layer B
// scope; schema-reserved via subject_id but unused in V1.5).
//
// Lifecycle:
//
//	svc := NewDialecticService(factStore, profileStore, cadenceSvc, cfg)
//	// no Start/Stop — recompute goroutines are short-lived (≤cfg.CallTimeout)
//	svc.MaybeRecompute(ctx, userID)            // called by ExtractorService
//	insight := svc.GetCachedInsight(ctx, userID) // called by AgentRunner
//	block := svc.BuildInsightSection(insight)    // injected into system prompt
//
// Concurrency: MaybeRecompute fires a goroutine with context.Background +
// CallTimeout — caller cancellation does NOT kill mid-flight LLM calls (spec
// §R4). No global concurrency limit (spec §R1 acknowledged risk — add
// semaphore if 1000+ concurrent triggers seen in production).
type DialecticService interface {
	// MaybeRecompute asks CadenceService whether a recompute is due; if so,
	// spawns a background goroutine to run the dialectic LLM pipeline and
	// persist the result. Non-blocking — returns immediately. nil receiver +
	// nil deps short-circuit to no-op.
	//
	// Called by ExtractorService.extract after persistFacts succeeds (and at
	// agent.RunRequest.UserID != 0).
	MaybeRecompute(ctx context.Context, userID uint)

	// GetCachedInsight reads the persisted insight from user_memory_profile.
	// Returns "" when no profile row exists, when cached_insight is empty, or
	// on any DB lookup error (best-effort: never propagates DB errors to the
	// agent runner — missing insight is preferable to a failed user turn).
	//
	// Called synchronously by AgentRunner.Run at system-prompt assembly time.
	GetCachedInsight(ctx context.Context, userID uint) string

	// BuildInsightSection wraps a non-empty insight in the
	// <personal_context data-internal="true"> scrubber-protected block. Empty
	// insight → returns "" (no injection). The "data-internal" attribute pairs
	// with Track 2 task 2.5 Scrubber to strip the tag from any LLM echo.
	//
	// Layer A wording: 【使用者画像】 is the picture of the agent user, not of
	// any customer / subject they discuss.
	BuildInsightSection(insight string) string
}

// DialecticConfig holds the dialectic tuning knobs. Loaded from viper via
// LoadDialecticConfigFromViper; tests can construct it directly to exercise
// boundaries without touching the global viper state.
type DialecticConfig struct {
	TopFactsLimit   int
	MaxOutputTokens int
	Temperature     float64
	CallTimeout     time.Duration
}

// DefaultDialecticConfig returns the spec-defined defaults.
func DefaultDialecticConfig() DialecticConfig {
	return DialecticConfig{
		TopFactsLimit:   DefaultDialecticTopFactsLimit,
		MaxOutputTokens: DefaultDialecticMaxOutputTokens,
		Temperature:     DefaultDialecticTemperature,
		CallTimeout:     DefaultDialecticCallTimeout,
	}
}

// dialecticViperGetter is the narrow surface of *viper.Viper we need.
// Decouples LoadDialecticConfigFromViper from the concrete viper import.
type dialecticViperGetter interface {
	GetInt(key string) int
	GetFloat64(key string) float64
}

// LoadDialecticConfigFromViper reads the dialectic config from viper, falling
// back to DefaultDialecticConfig() for any unset key.
//
// Config layout (config_*.yaml):
//
//	agent:
//	  memory:
//	    dialectic_top_facts_limit: 20
//	    dialectic_max_output_tokens: 600
//	    dialectic_temperature: 0.4
//	    dialectic_call_timeout_seconds: 30
func LoadDialecticConfigFromViper(v dialecticViperGetter) DialecticConfig {
	cfg := DefaultDialecticConfig()
	if n := v.GetInt("agent.memory.dialectic_top_facts_limit"); n > 0 {
		cfg.TopFactsLimit = n
	}
	if n := v.GetInt("agent.memory.dialectic_max_output_tokens"); n > 0 {
		cfg.MaxOutputTokens = n
	}
	if t := v.GetFloat64("agent.memory.dialectic_temperature"); t > 0 {
		cfg.Temperature = t
	}
	if s := v.GetInt("agent.memory.dialectic_call_timeout_seconds"); s > 0 {
		cfg.CallTimeout = time.Duration(s) * time.Second
	}
	return cfg
}

// ─── dialecticService ─────────────────────────────────────────────────────────

// dialecticChatFn is the LLM-call seam — production wires aiservice.Chat;
// tests inject deterministic / failing mocks. Mirrors extractorChatFn +
// selectorChatFn pattern.
type dialecticChatFn func(ctx context.Context, taskID string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error)

// dialecticExecutor is the goroutine-spawn seam — production uses go(); tests
// override to run synchronously for deterministic assertions. The wg parameter
// is for tests that want to await goroutine completion (production wg = nil).
type dialecticExecutor func(fn func())

// dialecticService is the production DialecticService implementation.
type dialecticService struct {
	factStore    store.IUserMemoryFactStore
	profileStore store.IUserMemoryProfileStore
	cadenceSvc   *CadenceService

	chatMu sync.Mutex
	chat   dialecticChatFn

	// executor controls how recomputeInsightSafe is launched. Production wires
	// goExecutor (go func()); tests can swap to syncExecutor for deterministic
	// waiting without sleeps.
	executor dialecticExecutor

	cfg DialecticConfig
}

// DialecticOption configures a dialecticService at construction.
type DialecticOption func(*dialecticService)

// WithDialecticChatFn replaces the LLM-call seam. Production callers omit;
// tests pass a deterministic mock.
func WithDialecticChatFn(fn dialecticChatFn) DialecticOption {
	return func(s *dialecticService) { s.chat = fn }
}

// WithDialecticExecutor replaces the goroutine-spawn seam. Production callers
// omit; tests pass syncDialecticExecutor (run-in-caller) for deterministic
// pipeline assertions.
func WithDialecticExecutor(e dialecticExecutor) DialecticOption {
	return func(s *dialecticService) { s.executor = e }
}

// goDialecticExecutor is the production executor — runs the pipeline in a
// detached goroutine so MaybeRecompute returns immediately to the caller.
func goDialecticExecutor(fn func()) { go fn() }

// SyncDialecticExecutor runs the dialectic pipeline synchronously in the
// caller goroutine — TEST USE ONLY (exported for cross-package tests if ever
// needed; today only this package's tests use it).
func SyncDialecticExecutor(fn func()) { fn() }

// NewDialecticService constructs a DialecticService with production defaults.
// All three deps are required; nil any one of them is a programming error
// (panic) since MaybeRecompute relies on cadenceSvc.ShouldRunDialectic.
func NewDialecticService(
	factStore store.IUserMemoryFactStore,
	profileStore store.IUserMemoryProfileStore,
	cadenceSvc *CadenceService,
	cfg DialecticConfig,
	opts ...DialecticOption,
) DialecticService {
	if factStore == nil || profileStore == nil || cadenceSvc == nil {
		panic("memory.NewDialecticService: factStore, profileStore, cadenceSvc must all be non-nil")
	}
	// Normalise the config — caller may pass partial / zero values when
	// loading from a stripped-down viper (e.g. tests).
	if cfg.TopFactsLimit <= 0 {
		cfg.TopFactsLimit = DefaultDialecticTopFactsLimit
	}
	if cfg.MaxOutputTokens <= 0 {
		cfg.MaxOutputTokens = DefaultDialecticMaxOutputTokens
	}
	if cfg.Temperature <= 0 {
		cfg.Temperature = DefaultDialecticTemperature
	}
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = DefaultDialecticCallTimeout
	}
	s := &dialecticService{
		factStore:    factStore,
		profileStore: profileStore,
		cadenceSvc:   cadenceSvc,
		cfg:          cfg,
		executor:     goDialecticExecutor,
		chat: func(ctx context.Context, taskID string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
			return aiservice.Chat(ctx, taskID, req)
		},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// loadChatFn reads the chat seam under chatMu. Production calls this exactly
// once per recompute; tests may swap mid-flight via swapChatFnForTest.
func (s *dialecticService) loadChatFn() dialecticChatFn {
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	return s.chat
}

// swapChatFnForTest replaces the LLM seam at runtime. Test-only — production
// constructs the final chat fn via WithDialecticChatFn before MaybeRecompute.
//
//nolint:unused // exercised by dialectic_test.go
func (s *dialecticService) swapChatFnForTest(fn dialecticChatFn) {
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	s.chat = fn
}

// ─── MaybeRecompute ──────────────────────────────────────────────────────────

// MaybeRecompute is the public entry point. Cadence-gates the call, then
// fires (via executor) the pipeline in a background goroutine so the caller
// (ExtractorService.extract) returns immediately. Spec §核心算法 step 1-3.
//
// Edge cases:
//   - nil receiver (programmatic misuse) → silent no-op
//   - userID == 0 (unauthenticated) → silent no-op
//   - cadence check errors → log warn + return (don't trigger LLM on error)
//   - cadence says skip → metrics.MemoryDialecticSkipCountInc + return
func (s *dialecticService) MaybeRecompute(ctx context.Context, userID uint) {
	if s == nil {
		return
	}
	if userID == 0 {
		return
	}
	shouldRun, err := s.cadenceSvc.ShouldRunDialectic(ctx, userID)
	if err != nil {
		log.Warnw("memory.dialectic cadence check failed; skipping recompute",
			"user_id", userID, "error", err)
		return
	}
	if !shouldRun {
		metrics.MemoryDialecticSkipCountInc()
		return
	}
	// Capture seam values now so a concurrent swapChatFnForTest after we
	// return doesn't race with the goroutine.
	exec := s.executor
	exec(func() {
		s.recomputeInsightSafe(userID)
	})
}

// recomputeInsightSafe runs one dialectic pipeline end-to-end. Detached from
// the caller — uses context.Background + cfg.CallTimeout so a user-turn
// cancellation doesn't kill the LLM mid-call (spec §R4).
//
// Wrapped in panic-recover so a bad LLM response or DB hiccup doesn't take
// down adjacent goroutines.
func (s *dialecticService) recomputeInsightSafe(userID uint) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorw("memory.dialectic recompute panic recovered",
				"user_id", userID, "panic", fmt.Sprintf("%v", r))
			metrics.MemoryDialecticFailedCountInc()
		}
	}()

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.CallTimeout)
	defer cancel()

	// Step 1 — list candidate facts. ORDER BY importance DESC (then id DESC
	// via store whitelist). The store whitelist maps "importance" →
	// `importance DESC, id DESC` which covers the spec-requested ordering
	// (importance + recency proxy via descending insertion id + confidence is
	// implicit via the confidence-≥0.70 filter at extract time). If the
	// whitelist is extended later to a richer importance+recency+confidence
	// ordering, no caller change required.
	//
	// V1.5 Layer A: store.List does NOT filter on subject_id — but every
	// row written via factStore.Create/BatchCreate is guarded by the
	// SubjectID != nil ⇒ ErrLayerBNotSupported invariant (see
	// internal/numind/store/user_memory.go:282/302). So in V1.5 every row
	// returned here has subject_id IS NULL by construction. V2 will need
	// an explicit `WHERE subject_id IS NULL` filter once Layer B starts
	// writing subject-scoped facts.
	candidates, err := s.factStore.List(ctx, userID, store.ListFactOpts{
		Limit:           s.cfg.TopFactsLimit,
		OrderBy:         "importance",
		IncludeArchived: false,
	})
	if err != nil {
		log.Warnw("memory.dialectic list facts failed",
			"user_id", userID, "error", err)
		metrics.MemoryDialecticFailedCountInc()
		metrics.MemoryDialecticDurationObserve(time.Since(start))
		return
	}
	if len(candidates) == 0 {
		// No facts to reason over yet — do NOT clear cache (preserve any
		// previous insight; nothing better to write).
		log.Debugw("memory.dialectic skip — no facts",
			"user_id", userID)
		metrics.MemoryDialecticDurationObserve(time.Since(start))
		return
	}

	// Step 2 — Langfuse trace + LLM call.
	traceID := langfuse.TraceID()
	langfuse.CreateTrace(traceID, "memory.dialectic",
		langfuse.WithUserID(userID),
		langfuse.WithTraceInput(map[string]any{
			"fact_count":      len(candidates),
			"top_facts_limit": s.cfg.TopFactsLimit,
		}),
		langfuse.WithTraceTags("memory", "dialectic", "layer-a"),
	)
	ctx = langfuse.WithTrace(ctx, traceID)

	prompt := buildDialecticPrompt(candidates)
	genID := langfuse.SpanID()
	langfuse.CreateGeneration(traceID, genID,
		langfuse.WithGenName("memory.dialectic.llm"),
		langfuse.WithGenInput(prompt),
	)

	chatFn := s.loadChatFn()
	resp, err := chatFn(ctx, profile.AgentDialectic, aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: prompt}},
		},
		MaxTokens:   s.cfg.MaxOutputTokens,
		Temperature: s.cfg.Temperature,
	})
	if err != nil {
		// Preserve old cache — caller's next cadence check (within 30 min
		// max-cooldown) will retry; we do NOT want to wipe a valid prior
		// insight just because one call timed out.
		langfuse.EndGeneration(traceID, genID,
			langfuse.WithGenOutput(map[string]string{"error": err.Error()}),
		)
		log.Warnw("memory.dialectic LLM call failed, keeping old cache",
			"user_id", userID, "error", err)
		metrics.MemoryDialecticFailedCountInc()
		metrics.MemoryDialecticDurationObserve(time.Since(start))
		return
	}

	insight := strings.TrimSpace(resp.Content)
	langfuse.EndGeneration(traceID, genID,
		langfuse.WithGenOutput(resp.Content),
		langfuse.WithGenModel(resp.Model),
		langfuse.WithGenUsage(resp.Usage.PromptTokens, resp.Usage.CompletionTokens),
	)

	// Step 3 — validate length. Reject anything outside [100, 800] runes as
	// suspect (markdown header leak / "我不知道" / truncated mid-sentence).
	if !validInsight(insight) {
		log.Warnw("memory.dialectic LLM returned invalid output (length out of range), keeping old cache",
			"user_id", userID, "rune_count", utf8.RuneCountInString(insight),
			"min", dialecticInsightMinRunes, "max", dialecticInsightMaxRunes)
		metrics.MemoryDialecticFailedCountInc()
		metrics.MemoryDialecticDurationObserve(time.Since(start))
		return
	}

	// Step 4 — persist. UpdateCachedInsight returns gorm.ErrRecordNotFound
	// if the profile row doesn't exist. Task 3.3 ExtractorService guarantees
	// IncrementExtractionCount runs before MaybeRecompute can fire (rebuild
	// hook on persistFacts success), so the profile row exists by construction.
	// For defence-in-depth, on NotFound we Upsert a fresh row with the
	// insight + try once more.
	//
	// Note: insight is LLM-generated (not user input); EscapeForStorage is
	// intentionally omitted here. Task 3.3 EscapeForStorage applies to extracted
	// facts because those come from user/assistant turn content where the LLM
	// could be tricked into echoing injection payloads. Dialectic insight is
	// written by our own dialectic LLM call with a tightly-controlled prompt;
	// the Track 2 Scrubber strips the <personal_context> wrapping tags on
	// LLM output before user-facing emit. If V2 ever surfaces insight to UI
	// directly (bypassing the system-prompt-only injection path), revisit.
	if err := s.profileStore.UpdateCachedInsight(ctx, userID, insight, len(candidates)); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Recover via Upsert — write a full profile row carrying the new
			// cached_insight + the same fact_count. Other narrative fields
			// (work_context / personal_context / top_of_mind / total_facts)
			// are left untouched (stay at zero / DB default; RebuildNarrative
			// path will refresh them later).
			now := time.Now()
			row := &model.UserMemoryProfile{
				UserID:                 userID,
				CachedInsight:          insight,
				CachedInsightAt:        &now,
				CachedInsightFactCount: len(candidates),
			}
			if uerr := s.profileStore.Upsert(ctx, row); uerr != nil {
				log.Warnw("memory.dialectic Upsert fallback after NotFound failed",
					"user_id", userID, "error", uerr)
				metrics.MemoryDialecticFailedCountInc()
				metrics.MemoryDialecticDurationObserve(time.Since(start))
				return
			}
			// Upsert succeeded — fall through to success metrics.
		} else {
			log.Warnw("memory.dialectic UpdateCachedInsight failed, keeping old cache",
				"user_id", userID, "error", err)
			metrics.MemoryDialecticFailedCountInc()
			metrics.MemoryDialecticDurationObserve(time.Since(start))
			return
		}
	}

	metrics.MemoryDialecticRunCountInc()
	metrics.MemoryDialecticDurationObserve(time.Since(start))
	log.Infow("memory.dialectic recompute complete",
		"user_id", userID,
		"fact_count", len(candidates),
		"insight_runes", utf8.RuneCountInString(insight),
		"duration_ms", time.Since(start).Milliseconds(),
	)
}

// validInsight applies the spec §关键参数 100–800 rune length gate.
//
// Spec's wider 100–800 range (vs the expected 300–500) is intentional — gives
// the LLM headroom for shorter "low fact base" responses and longer
// "rich-context" responses without false rejections. Outside this range
// almost always indicates parser/markdown leak / refusal text / truncation.
func validInsight(s string) bool {
	rc := utf8.RuneCountInString(s)
	return rc >= dialecticInsightMinRunes && rc <= dialecticInsightMaxRunes
}

// ─── GetCachedInsight ────────────────────────────────────────────────────────

// GetCachedInsight returns the persisted insight or "" on any error / missing
// row. Best-effort by design: AgentRunner's prompt build path tolerates a
// missing personal-context block far better than it tolerates a failing
// system-prompt assembly.
func (s *dialecticService) GetCachedInsight(ctx context.Context, userID uint) string {
	if s == nil || userID == 0 {
		return ""
	}
	prof, err := s.profileStore.Get(ctx, userID)
	if err != nil {
		// NotFound is the common path for fresh users — log at debug to
		// avoid log spam during early adoption.
		if errors.Is(err, gorm.ErrRecordNotFound) {
			log.Debugw("memory.dialectic no profile yet",
				"user_id", userID)
			return ""
		}
		log.Warnw("memory.dialectic GetCachedInsight failed; returning empty",
			"user_id", userID, "error", err)
		return ""
	}
	if prof == nil {
		return ""
	}
	return prof.CachedInsight
}

// ─── BuildInsightSection ─────────────────────────────────────────────────────

// BuildInsightSection wraps a non-empty insight in the
// <personal_context data-internal="true"> scrubber-protected block. The
// 【使用者画像】 prefix is added here (not by the LLM) so the prompt template
// can stay focused on the insight content.
//
// Layer A boundary reminder: the picture is of the **agent user themselves**
// — never of whatever customer / dataset / production-line they happen to be
// discussing in the current session.
//
// Returns "" for empty input so the caller can concatenate unconditionally
// without an extra check.
func (s *dialecticService) BuildInsightSection(insight string) string {
	if strings.TrimSpace(insight) == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(insight) + 96)
	b.WriteString(`<personal_context data-internal="true">` + "\n")
	b.WriteString("【使用者画像】")
	b.WriteString(insight)
	b.WriteString("\n</personal_context>\n")
	return b.String()
}
