package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// ─── Tuning constants ────────────────────────────────────────────────────────

// DefaultDigestMaxOutputTokens caps digest LLM output. summary ≤ 300 chinese
// chars ≈ 450 tokens + key_topics array ≈ 150 tokens ≈ 600 total. Pad to 800.
const DefaultDigestMaxOutputTokens = 800

// DefaultDigestTemperature: low-randomness for structured JSON output.
const DefaultDigestTemperature = 0.3

// DefaultDigestCallTimeout is the upper-bound wall time for one LLM call.
// qwen-plus averages 4-8s for 600-800 token outputs; 30s gives headroom for
// parallel worker pool pressure.
const DefaultDigestCallTimeout = 30 * time.Second

// DigestMaxSessionsBriefPerDay caps the sessions included in the daily prompt.
// The store layer's digestMaxRunsPerUserPerDay (200) is the absolute upper
// bound; this 30-session prompt cap shrinks it further so we don't pay for
// LLM tokens on 200 trivial sessions. The cron rotates the most recent 30
// (started_at DESC truncation happens here, not in store).
const DigestMaxSessionsBriefPerDay = 30

// ─── DigestGenerator interface + config ──────────────────────────────────────

// DigestGenerator runs one digest-pipeline end-to-end for a single user/period.
//
// All Generate* methods are synchronous and intended to be called by the cron
// runner inside a worker-pool goroutine. They:
//
//  1. fetch the period's lower-level inputs (agent_runs for daily; lower digests
//     for weekly/monthly/quarterly)
//  2. build the digest prompt
//  3. call aiservice.Chat(ctx, profile.AgentDigest, …)
//  4. parse JSON, retry once on parse error, fall back to digestParseFallback
//     after second parse failure
//  5. return a populated digest row ready for store.Upsert*
//
// Layer A only: all digests describe the agent user themselves (sessions they
// ran, facts about them). No Layer B subject_id involvement.
//
// Errors are returned to the caller (cron); per-user failures are logged + counted
// but do not abort the whole cron run.
type DigestGenerator interface {
	// GenerateDaily builds the daily digest for (userID, date) by pulling
	// yesterday's agent_runs and asking the LLM to summarise. Returns the
	// digest model ready for Upsert; never nil + nil err.
	GenerateDaily(ctx context.Context, userID uint, date time.Time) (*model.UserMemoryDigestDaily, error)
	// GenerateWeekly builds the weekly digest from the 7-day daily digest range
	// covering the specified ISO week.
	GenerateWeekly(ctx context.Context, userID uint, isoYear, isoWeek int) (*model.UserMemoryDigestWeekly, error)
	// GenerateMonthly builds the monthly digest from the weekly digests that
	// fall within the specified calendar month.
	GenerateMonthly(ctx context.Context, userID uint, year, month int) (*model.UserMemoryDigestMonthly, error)
	// GenerateQuarterly builds the quarterly digest from the 3 monthly digests
	// covering the specified calendar quarter.
	GenerateQuarterly(ctx context.Context, userID uint, year, quarter int) (*model.UserMemoryDigestQuarterly, error)
}

// DigestConfig holds the digest tuning knobs.
type DigestConfig struct {
	MaxOutputTokens int
	Temperature     float64
	CallTimeout     time.Duration
}

// DefaultDigestConfig returns the spec-defined defaults.
func DefaultDigestConfig() DigestConfig {
	return DigestConfig{
		MaxOutputTokens: DefaultDigestMaxOutputTokens,
		Temperature:     DefaultDigestTemperature,
		CallTimeout:     DefaultDigestCallTimeout,
	}
}

// LoadDigestConfigFromViper reads the digest config from viper, falling back
// to DefaultDigestConfig values for any unset key.
//
// Config layout (config_*.yaml):
//
//	agent:
//	  memory:
//	    digest:
//	      max_output_tokens: 800
//	      temperature: 0.3
//	      call_timeout_seconds: 30
type digestViperGetter interface {
	GetInt(key string) int
	GetFloat64(key string) float64
}

func LoadDigestConfigFromViper(v digestViperGetter) DigestConfig {
	cfg := DefaultDigestConfig()
	if n := v.GetInt("agent.memory.digest.max_output_tokens"); n > 0 {
		cfg.MaxOutputTokens = n
	}
	if t := v.GetFloat64("agent.memory.digest.temperature"); t > 0 {
		cfg.Temperature = t
	}
	if s := v.GetInt("agent.memory.digest.call_timeout_seconds"); s > 0 {
		cfg.CallTimeout = time.Duration(s) * time.Second
	}
	return cfg
}

// ─── digestGenerator ─────────────────────────────────────────────────────────

// digestChatFn is the LLM-call seam — production wires aiservice.Chat; tests
// inject deterministic mocks.
type digestChatFn func(ctx context.Context, taskID string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error)

// digestGenerator is the production DigestGenerator implementation.
type digestGenerator struct {
	digestStore store.IMemoryDigestStore
	factStore   store.IUserMemoryFactStore // for daily extracted_facts_count

	chatMu sync.Mutex
	chat   digestChatFn

	cfg DigestConfig
}

// DigestGeneratorOption configures a digestGenerator at construction.
type DigestGeneratorOption func(*digestGenerator)

// WithDigestChatFn replaces the LLM-call seam (TEST USE).
func WithDigestChatFn(fn digestChatFn) DigestGeneratorOption {
	return func(g *digestGenerator) { g.chat = fn }
}

// NewDigestGenerator constructs a DigestGenerator with production defaults.
// digestStore is required; factStore may be nil (extracted_facts_count will be 0).
func NewDigestGenerator(
	digestStore store.IMemoryDigestStore,
	factStore store.IUserMemoryFactStore,
	cfg DigestConfig,
	opts ...DigestGeneratorOption,
) DigestGenerator {
	if digestStore == nil {
		panic("memory.NewDigestGenerator: digestStore must be non-nil")
	}
	if cfg.MaxOutputTokens <= 0 {
		cfg.MaxOutputTokens = DefaultDigestMaxOutputTokens
	}
	if cfg.Temperature <= 0 {
		cfg.Temperature = DefaultDigestTemperature
	}
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = DefaultDigestCallTimeout
	}
	g := &digestGenerator{
		digestStore: digestStore,
		factStore:   factStore,
		cfg:         cfg,
		chat: func(ctx context.Context, taskID string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
			return aiservice.Chat(ctx, taskID, req)
		},
	}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

func (g *digestGenerator) loadChatFn() digestChatFn {
	g.chatMu.Lock()
	defer g.chatMu.Unlock()
	return g.chat
}

// ─── GenerateDaily ───────────────────────────────────────────────────────────

// GenerateDaily pulls yesterday's agent_runs (Asia/Shanghai bounds), builds
// the daily prompt, calls the LLM with retry, and returns a populated digest row.
//
// Empty-day handling: if no agent_runs exist for the date (cron stage 1 should
// already have filtered these), the digest is generated with the canned "no
// activity" summary and 0 counts — never returns nil digest.
func (g *digestGenerator) GenerateDaily(ctx context.Context, userID uint, date time.Time) (*model.UserMemoryDigestDaily, error) {
	if userID == 0 {
		return nil, fmt.Errorf("digestGenerator.GenerateDaily: userID required")
	}
	// Resolve the [start, end) window in Asia/Shanghai.
	dateSH := date.In(shanghaiLoc)
	start := time.Date(dateSH.Year(), dateSH.Month(), dateSH.Day(), 0, 0, 0, 0, shanghaiLoc)
	end := start.Add(24 * time.Hour)

	runs, err := g.digestStore.ListAgentRunsByUserDateRange(ctx, userID, start, end)
	if err != nil {
		return nil, fmt.Errorf("digestGenerator.GenerateDaily list runs: %w", err)
	}

	// Compute counts.
	sessionCount := countDistinctSessionIDs(runs)
	messageCount := countMessages(runs)
	factsCount := 0
	if g.factStore != nil {
		// Best-effort: count facts created in the window (use source_extracted_at).
		// We compute via fact list + filter to avoid adding a dedicated CountByRange
		// method to the store interface for this single use.
		factsCount = countFactsInWindow(ctx, g.factStore, userID, start, end)
	}

	// Build prompt input.
	briefs := sessionsFromAgentRuns(runs)
	// Cap to DigestMaxSessionsBriefPerDay to bound prompt size — the store
	// already capped at digestMaxRunsPerUserPerDay (200), but the LLM only
	// needs the most-recent N to summarise.
	if len(briefs) > DigestMaxSessionsBriefPerDay {
		briefs = briefs[len(briefs)-DigestMaxSessionsBriefPerDay:]
	}

	out := g.callDigestLLM(
		ctx,
		"memory.digest.daily",
		BuildDailyDigestPrompt(start, len(runs), messageCount, factsCount, briefs),
		userID,
	)

	return &model.UserMemoryDigestDaily{
		UserID:              userID,
		DigestDate:          start, // stored as DATE; truncation OK
		SessionCount:        sessionCount,
		MessageCount:        messageCount,
		ExtractedFactsCount: factsCount,
		Summary:             out.Summary,
		KeyTopics:           keyTopicsToJSON(out.KeyTopics),
		LLMCostCredits:      0, // cost tracking via Langfuse — not surfaced here
		GeneratedAt:         time.Now(),
	}, nil
}

// ─── GenerateWeekly ──────────────────────────────────────────────────────────

func (g *digestGenerator) GenerateWeekly(ctx context.Context, userID uint, isoYear, isoWeek int) (*model.UserMemoryDigestWeekly, error) {
	if userID == 0 {
		return nil, fmt.Errorf("digestGenerator.GenerateWeekly: userID required")
	}
	// Resolve the ISO week's Monday + Sunday bounds.
	monday := isoWeekStart(isoYear, isoWeek)
	sunday := monday.AddDate(0, 0, 6)

	dailies, err := g.digestStore.GetDailyRange(ctx, userID, monday, sunday)
	if err != nil {
		return nil, fmt.Errorf("digestGenerator.GenerateWeekly list dailies: %w", err)
	}

	items := make([]lowerDigestItem, 0, len(dailies))
	for _, d := range dailies {
		if d == nil || strings.TrimSpace(d.Summary) == "" {
			continue
		}
		items = append(items, lowerDigestItem{
			Label:   d.DigestDate.Format("01-02"),
			Summary: d.Summary,
			Topics:  parseKeyTopicsList(d.KeyTopics),
		})
	}

	out := g.callDigestLLM(
		ctx,
		"memory.digest.weekly",
		BuildWeeklyDigestPrompt(items),
		userID,
	)

	return &model.UserMemoryDigestWeekly{
		UserID:        userID,
		ISOYear:       isoYear,
		ISOWeek:       isoWeek,
		WeekStartDate: monday,
		WeekEndDate:   sunday,
		Summary:       out.Summary,
		KeyTopics:     keyTopicsToJSON(out.KeyTopics),
		GeneratedAt:   time.Now(),
	}, nil
}

// ─── GenerateMonthly ─────────────────────────────────────────────────────────

func (g *digestGenerator) GenerateMonthly(ctx context.Context, userID uint, year, month int) (*model.UserMemoryDigestMonthly, error) {
	if userID == 0 {
		return nil, fmt.Errorf("digestGenerator.GenerateMonthly: userID required")
	}
	if month < 1 || month > 12 {
		return nil, fmt.Errorf("digestGenerator.GenerateMonthly: invalid month %d", month)
	}
	// Resolve all weekly digests whose Monday falls in the calendar month.
	monthStart := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, shanghaiLoc)
	monthEnd := monthStart.AddDate(0, 1, -1) // last day of the month
	fromYW := [2]int{}
	toYW := [2]int{}
	fromYW[0], fromYW[1] = monthStart.ISOWeek()
	toYW[0], toYW[1] = monthEnd.ISOWeek()

	weeklies, err := g.digestStore.GetWeeklyRange(ctx, userID, fromYW, toYW)
	if err != nil {
		return nil, fmt.Errorf("digestGenerator.GenerateMonthly list weeklies: %w", err)
	}

	items := make([]lowerDigestItem, 0, len(weeklies))
	for _, w := range weeklies {
		if w == nil || strings.TrimSpace(w.Summary) == "" {
			continue
		}
		items = append(items, lowerDigestItem{
			Label:   fmt.Sprintf("%d-W%02d", w.ISOYear, w.ISOWeek),
			Summary: w.Summary,
			Topics:  parseKeyTopicsList(w.KeyTopics),
		})
	}

	out := g.callDigestLLM(
		ctx,
		"memory.digest.monthly",
		BuildMonthlyDigestPrompt(items),
		userID,
	)

	return &model.UserMemoryDigestMonthly{
		UserID:      userID,
		Year:        year,
		Month:       month,
		Summary:     out.Summary,
		KeyTopics:   keyTopicsToJSON(out.KeyTopics),
		GeneratedAt: time.Now(),
	}, nil
}

// ─── GenerateQuarterly ───────────────────────────────────────────────────────

func (g *digestGenerator) GenerateQuarterly(ctx context.Context, userID uint, year, quarter int) (*model.UserMemoryDigestQuarterly, error) {
	if userID == 0 {
		return nil, fmt.Errorf("digestGenerator.GenerateQuarterly: userID required")
	}
	if quarter < 1 || quarter > 4 {
		return nil, fmt.Errorf("digestGenerator.GenerateQuarterly: invalid quarter %d", quarter)
	}

	firstMonth := (quarter-1)*3 + 1
	fromYM := [2]int{year, firstMonth}
	toYM := [2]int{year, firstMonth + 2}

	monthlies, err := g.digestStore.GetMonthlyRange(ctx, userID, fromYM, toYM)
	if err != nil {
		return nil, fmt.Errorf("digestGenerator.GenerateQuarterly list monthlies: %w", err)
	}

	items := make([]lowerDigestItem, 0, len(monthlies))
	for _, m := range monthlies {
		if m == nil || strings.TrimSpace(m.Summary) == "" {
			continue
		}
		items = append(items, lowerDigestItem{
			Label:   fmt.Sprintf("%d-%02d", m.Year, m.Month),
			Summary: m.Summary,
			Topics:  parseKeyTopicsList(m.KeyTopics),
		})
	}

	out := g.callDigestLLM(
		ctx,
		"memory.digest.quarterly",
		BuildQuarterlyDigestPrompt(items),
		userID,
	)

	return &model.UserMemoryDigestQuarterly{
		UserID:      userID,
		Year:        year,
		Quarter:     quarter,
		Summary:     out.Summary,
		KeyTopics:   keyTopicsToJSON(out.KeyTopics),
		GeneratedAt: time.Now(),
	}, nil
}

// ─── callDigestLLM (with parse retry) ────────────────────────────────────────

// callDigestLLM wraps the LLM call + parse + 1-retry pipeline. Returns a populated
// digestLLMOutput; never returns an error — final fallback is digestFallbackOutput().
// Errors are logged + tracked via Langfuse.
func (g *digestGenerator) callDigestLLM(ctx context.Context, spanName, prompt string, userID uint) digestLLMOutput {
	// Apply per-call timeout — independent of caller context (cron may give
	// us 1h, but we want individual LLM calls to fail fast).
	callCtx, cancel := context.WithTimeout(ctx, g.cfg.CallTimeout)
	defer cancel()

	traceID := langfuse.FromContext(ctx)
	var tid string
	if traceID != nil {
		tid = traceID.TraceID
	} else {
		tid = langfuse.TraceID()
		langfuse.CreateTrace(tid, spanName,
			langfuse.WithUserID(userID),
			langfuse.WithTraceTags("memory", "digest"),
		)
		callCtx = langfuse.WithTrace(callCtx, tid)
	}

	chatFn := g.loadChatFn()

	// Try 1: original prompt.
	out, ok := g.tryOnceLLM(callCtx, chatFn, tid, spanName, prompt, "attempt-1")
	if ok {
		return out
	}

	// Try 2: append explicit JSON-only reminder.
	retryPrompt := prompt + "\n\n[重要] 上次输出无法解析为 JSON; 请只输出 JSON 对象, 不要任何额外文字."
	out, ok = g.tryOnceLLM(callCtx, chatFn, tid, spanName, retryPrompt, "attempt-2")
	if ok {
		return out
	}

	// Final fallback.
	log.Warnw("memory.digest LLM parse failed twice; using fallback",
		"user_id", userID, "span", spanName)
	return digestFallbackOutput()
}

// tryOnceLLM is one prompt attempt. Returns (parsed output, ok). On any
// LLM/parse failure logs at warn + emits the failed-generation langfuse event.
func (g *digestGenerator) tryOnceLLM(
	ctx context.Context,
	chatFn digestChatFn,
	traceID, spanName, prompt, attemptLabel string,
) (digestLLMOutput, bool) {
	genID := langfuse.SpanID()
	langfuse.CreateGeneration(traceID, genID,
		langfuse.WithGenName(spanName+"."+attemptLabel),
		langfuse.WithGenInput(prompt),
	)
	resp, err := chatFn(ctx, profile.AgentDigest, aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: prompt}},
		},
		MaxTokens:      g.cfg.MaxOutputTokens,
		Temperature:    g.cfg.Temperature,
		ResponseFormat: aiservice.ResponseFormatJSONObject,
	})
	if err != nil {
		langfuse.EndGeneration(traceID, genID,
			langfuse.WithGenOutput(map[string]string{"error": err.Error()}),
		)
		return digestLLMOutput{}, false
	}
	out, perr := parseDigestLLMOutput(resp.Content)
	if perr != nil {
		langfuse.EndGeneration(traceID, genID,
			langfuse.WithGenOutput(resp.Content),
			langfuse.WithGenModel(resp.Model),
			langfuse.WithGenUsage(resp.Usage.PromptTokens, resp.Usage.CompletionTokens),
		)
		return digestLLMOutput{}, false
	}
	langfuse.EndGeneration(traceID, genID,
		langfuse.WithGenOutput(resp.Content),
		langfuse.WithGenModel(resp.Model),
		langfuse.WithGenUsage(resp.Usage.PromptTokens, resp.Usage.CompletionTokens),
	)
	return out, true
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// countDistinctSessionIDs returns the number of unique session_id values
// across the runs slice.
func countDistinctSessionIDs(runs []*model.AgentRun) int {
	if len(runs) == 0 {
		return 0
	}
	seen := make(map[string]struct{}, len(runs))
	for _, r := range runs {
		if r == nil || r.SessionID == "" {
			continue
		}
		seen[r.SessionID] = struct{}{}
	}
	return len(seen)
}

// countMessages totals the message count across all runs by unmarshalling
// messages JSON. Best-effort: parse errors count as 0 for that run.
func countMessages(runs []*model.AgentRun) int {
	total := 0
	for _, r := range runs {
		if r == nil || len(r.Messages) == 0 {
			continue
		}
		var msgs []any
		if err := json.Unmarshal(r.Messages, &msgs); err == nil {
			total += len(msgs)
		}
	}
	return total
}

// countFactsInWindow lists facts for the user in the window and returns the
// count. Best-effort: store errors return 0. Uses a single List call with a
// large limit (1000) since fact creation rate per user per day is bounded by
// extractor cadence (≤ few dozen).
func countFactsInWindow(ctx context.Context, factStore store.IUserMemoryFactStore, userID uint, from, to time.Time) int {
	facts, err := factStore.List(ctx, userID, store.ListFactOpts{
		OrderBy: "recency",
		Limit:   1000,
	})
	if err != nil {
		return 0
	}
	count := 0
	for _, f := range facts {
		if !f.SourceExtractedAt.Before(from) && f.SourceExtractedAt.Before(to) {
			count++
		}
	}
	return count
}

// isoWeekStart returns the Monday 00:00:00 (Asia/Shanghai) of the given ISO
// week. ISO week 1 is the week containing the first Thursday of the year;
// this helper walks Jan 4 (always in W1) and steps to its Monday.
func isoWeekStart(isoYear, isoWeek int) time.Time {
	// Start from January 4 of isoYear (always in W1) and find its Monday.
	jan4 := time.Date(isoYear, time.January, 4, 0, 0, 0, 0, shanghaiLoc)
	jan4Mon := mondayOfWeek(jan4)
	return jan4Mon.AddDate(0, 0, 7*(isoWeek-1))
}

// fetchAndIgnoreNotFound is a small helper for fetching an existing digest as
// part of cron's "skip if already complete" pre-check. Unused in v1 (cron always
// re-runs and Upsert handles dedup), but kept here in case admins want a
// "force=false" cron mode that early-exits when row exists.
//
//nolint:unused // exposed for future cron toggle
func fetchAndIgnoreNotFound[T any](err error, row *T) (*T, error) {
	if err == nil {
		return row, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return nil, err
}
