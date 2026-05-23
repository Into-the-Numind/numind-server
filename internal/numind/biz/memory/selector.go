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

	lru "github.com/hashicorp/golang-lru/v2"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/metrics"
	"numind-server/internal/pkg/model"
)

// SelectorService picks the ≤5 user_memory_facts most relevant to the user's
// current turn and renders them into a system-prompt Memories section.
//
// Layer A only — facts here are about the *agent's user themselves* (the sales
// rep, SOP operator, data analyst, PPT clerk, etc.) — never about the customer
// / subject / dataset they discuss. The selector LLM prompt is worded
// accordingly.
//
// Cost: small LLM (qwen-turbo) + 30s LRU cache + ≤5-candidate shortcircuit
// keep the per-turn marginal cost at ~¥0.001.
type SelectorService interface {
	// SelectTop5 returns ≤5 facts relevant to userInput. UpdateUsage is invoked
	// on the returned IDs (last_used_at + use_count). Errors are non-fatal —
	// callers (PromptBuilder) treat nil + len==0 as "no memories to inject".
	SelectTop5(ctx context.Context, userID uint, userInput string) ([]model.UserMemoryFact, error)

	// BuildMemorySection renders facts into the system-prompt Memories block.
	// Wraps content in <personal_context data-internal="true"> for Track 2
	// task 2.5 Scrubber to strip from any LLM echo to the user.
	BuildMemorySection(facts []model.UserMemoryFact) string
}

// ─── Tuning constants ─────────────────────────────────────────────────────────

// SelectorCandidateLimit caps the per-user candidate set fed into the LLM.
// Spec §关键参数: 50 facts × ~40 chars × 2 tokens/char ≈ 4000 tokens prompt budget.
const SelectorCandidateLimit = 50

// SelectorMaxFacts is the upper bound on facts returned and injected.
// Spec §关键参数: ≤5 to keep attention focused (1000 token injection budget).
const SelectorMaxFacts = 5

// SelectorLLMMaxTokens caps the LLM response budget (just a JSON array of IDs).
const SelectorLLMMaxTokens = 100

// SelectorLLMTemperature: low randomness for selection (stable + non-divergent).
const SelectorLLMTemperature = 0.2

// SelectorCacheTTL is the same-input cache lifetime. Spec §关键参数:
// 30s covers follow-up turns inside a single tab; cross-turn freshness wins.
const SelectorCacheTTL = 30 * time.Second

// SelectorCacheSize is the LRU bucket count. Keys are (userID:inputHash) so
// even under heavy multi-user load the cache stays under O(N=1024) RAM.
const SelectorCacheSize = 1024

// selectorMaxUserInputRunes caps the userInput rune count fed into the selector
// LLM prompt. ~1000 runes ≈ ~2000 tokens for Chinese-heavy input, leaving the
// remaining ~2000-token budget for the 50-candidate list (spec §关键参数:
// 4000-token total prompt). Users pasting a long contract/email past this cap
// have the tail elided — selection quality degrades gracefully rather than
// blowing the token budget or triggering provider truncation mid-prompt.
const selectorMaxUserInputRunes = 1000

// ─── selectorService ─────────────────────────────────────────────────────────

// selectorChatFn is the LLM-call seam — production wires aiservice.Chat;
// tests inject a deterministic / failing mock. Mirrors ExtractorService design.
type selectorChatFn func(ctx context.Context, taskID string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error)

// selectorCacheEntry pairs cached fact IDs with an expiry so the LRU can drop
// stale entries lazily on Get (matches pricing.cache pattern in this repo).
type selectorCacheEntry struct {
	factIDs   []uint64
	expiresAt time.Time
}

// selectorService is the SelectorService implementation.
type selectorService struct {
	factStore store.IUserMemoryFactStore

	chatMu sync.Mutex
	chat   selectorChatFn

	cache    *lru.Cache[string, selectorCacheEntry]
	cacheTTL time.Duration

	// Tunables (overridable via With* options for tests).
	candidateLimit int
	maxFacts       int
	llmMaxTokens   int
	llmTemperature float64
}

// SelectorOption configures a selectorService at construction (functional opts).
type SelectorOption func(*selectorService)

// WithSelectorChatFn replaces the LLM-call seam. Production callers omit;
// tests pass a deterministic mock.
func WithSelectorChatFn(fn selectorChatFn) SelectorOption {
	return func(s *selectorService) { s.chat = fn }
}

// WithSelectorCacheTTL overrides SelectorCacheTTL.
func WithSelectorCacheTTL(d time.Duration) SelectorOption {
	return func(s *selectorService) { s.cacheTTL = d }
}

// WithSelectorCandidateLimit overrides SelectorCandidateLimit (clamped ≥ 1).
func WithSelectorCandidateLimit(n int) SelectorOption {
	return func(s *selectorService) {
		if n < 1 {
			n = 1
		}
		s.candidateLimit = n
	}
}

// WithSelectorMaxFacts overrides SelectorMaxFacts (clamped ≥ 1).
func WithSelectorMaxFacts(n int) SelectorOption {
	return func(s *selectorService) {
		if n < 1 {
			n = 1
		}
		s.maxFacts = n
	}
}

// NewSelectorService constructs a SelectorService with production defaults.
//
// On LRU construction failure (cache size ≤ 0), the function panics — this is
// process-init code and misconfiguration is a programming error.
func NewSelectorService(factStore store.IUserMemoryFactStore, opts ...SelectorOption) SelectorService {
	cache, err := lru.New[string, selectorCacheEntry](SelectorCacheSize)
	if err != nil {
		// Only SelectorCacheSize ≤ 0 triggers this. Compile-time guarantee for
		// the constant means we never hit this branch — defensive panic.
		panic("memory.NewSelectorService: invalid LRU size: " + err.Error())
	}
	s := &selectorService{
		factStore:      factStore,
		cache:          cache,
		cacheTTL:       SelectorCacheTTL,
		candidateLimit: SelectorCandidateLimit,
		maxFacts:       SelectorMaxFacts,
		llmMaxTokens:   SelectorLLMMaxTokens,
		llmTemperature: SelectorLLMTemperature,
		chat: func(ctx context.Context, taskID string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
			return aiservice.Chat(ctx, taskID, req)
		},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// loadChatFn reads the chat seam under chatMu so SelectTop5 callers don't race
// with test-only swaps. Mirrors the ExtractorService pattern (P2.C in task 3.3).
func (s *selectorService) loadChatFn() selectorChatFn {
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	return s.chat
}

// ─── SelectTop5 ──────────────────────────────────────────────────────────────

// SelectTop5 picks the ≤5 most-relevant facts to userInput. The algorithm:
//  1. LRU cache lookup keyed by (userID:hash(userInput)) → return cached fact rows
//  2. List candidates (up to candidateLimit, ordered confidence DESC + recency)
//  3. ≤maxFacts candidates → shortcircuit (return all + UpdateUsage + cache)
//  4. Otherwise call agent.memory_select LLM with a tight JSON-array prompt
//  5. On LLM error → fallback to confidence-top-N + warn log
//  6. On JSON parse failure → fallback to confidence-top-N + warn log
//  7. Map LLM-returned IDs back to facts (cap at maxFacts, drop unknown,
//     backfill from candidate top to reach maxFacts if LLM under-returned)
//  8. UpdateUsage + cache write
//
// UpdateUsage failures are non-fatal (warn log, continue) — they delay the
// "least-recently-used" signal by one turn, not a correctness issue.
func (s *selectorService) SelectTop5(ctx context.Context, userID uint, userInput string) ([]model.UserMemoryFact, error) {
	// Step 0 — rune-safe userInput cap. Protects the selector prompt's
	// ~4000-token budget when callers pass multi-thousand-rune pastes
	// (contracts, emails, transcripts). Done before cache key build so
	// truncated and non-truncated inputs sharing a tail still share a key.
	userInput = capUserInputForSelector(userInput)

	// Step 1 — cache.
	cacheKey := buildCacheKey(userID, userInput)
	if entry, ok := s.cache.Get(cacheKey); ok {
		if time.Now().Before(entry.expiresAt) {
			facts, err := s.factStore.GetByIDs(ctx, userID, entry.factIDs)
			if err != nil {
				log.Warnw("memory.selector cache GetByIDs failed; falling through to fresh select",
					"user_id", userID, "error", err)
			} else {
				metrics.MemorySelectRunsInc(metrics.MemorySelectCacheHit)
				metrics.MemorySelectFactsInjectedAdd(int64(len(facts)))
				s.updateUsageBestEffort(ctx, entry.factIDs)
				return facts, nil
			}
		} else {
			// Expired — drop and refetch fresh.
			s.cache.Remove(cacheKey)
		}
	}

	// Step 2 — list candidates.
	candidates, err := s.factStore.List(ctx, userID, store.ListFactOpts{
		OrderBy: "confidence", // includes "confidence DESC, id DESC" by store whitelist
		Limit:   s.candidateLimit,
	})
	if err != nil {
		return nil, fmt.Errorf("selectorService.SelectTop5 list candidates: %w", err)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	// Step 3 — shortcircuit: ≤maxFacts candidates → skip LLM.
	if len(candidates) <= s.maxFacts {
		ids := factIDsOf(candidates)
		s.updateUsageBestEffort(ctx, ids)
		s.cache.Add(cacheKey, selectorCacheEntry{
			factIDs:   ids,
			expiresAt: time.Now().Add(s.cacheTTL),
		})
		metrics.MemorySelectRunsInc(metrics.MemorySelectShortcircuit)
		metrics.MemorySelectFactsInjectedAdd(int64(len(candidates)))
		return candidates, nil
	}

	// Step 4 — LLM call with Langfuse trace.
	traceID := langfuse.TraceID()
	langfuse.CreateTrace(traceID, "memory.select",
		langfuse.WithUserID(userID),
		langfuse.WithTraceInput(map[string]any{
			"candidates_n":  len(candidates),
			"user_input_hd": truncateForTrace(userInput, 200),
		}),
		langfuse.WithTraceTags("memory", "select", "layer-a"),
	)
	ctx = langfuse.WithTrace(ctx, traceID)

	prompt := buildSelectorPrompt(userInput, candidates)
	genID := langfuse.SpanID()
	langfuse.CreateGeneration(traceID, genID,
		langfuse.WithGenName("memory.select.llm"),
		langfuse.WithGenInput(prompt),
	)
	chatFn := s.loadChatFn()
	resp, llmErr := chatFn(ctx, profile.AgentMemorySelect, aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: prompt}},
		},
		MaxTokens:   s.llmMaxTokens,
		Temperature: s.llmTemperature,
	})

	// Step 5 — LLM error → fallback.
	if llmErr != nil {
		langfuse.EndGeneration(traceID, genID,
			langfuse.WithGenOutput(map[string]string{"error": llmErr.Error()}),
		)
		log.Warnw("memory.selector LLM call failed; falling back to confidence top-N",
			"user_id", userID, "error", llmErr, "candidates_n", len(candidates))
		metrics.MemorySelectRunsInc(metrics.MemorySelectLLMFailure)
		return s.fallbackTop(ctx, candidates, cacheKey), nil
	}

	// Step 6 — parse JSON array.
	rawIDs, parseErr := parseSelectorResponse(resp.Content)
	langfuse.EndGeneration(traceID, genID,
		langfuse.WithGenOutput(resp.Content),
		langfuse.WithGenModel(resp.Model),
		langfuse.WithGenUsage(resp.Usage.PromptTokens, resp.Usage.CompletionTokens),
	)
	if parseErr != nil {
		log.Warnw("memory.selector LLM JSON parse failed; falling back to confidence top-N",
			"user_id", userID, "raw_len", len(resp.Content), "error", parseErr)
		metrics.MemorySelectRunsInc(metrics.MemorySelectParseFailure)
		return s.fallbackTop(ctx, candidates, cacheKey), nil
	}

	// Step 7 — map IDs back to facts (cap + drop unknown + backfill).
	selected := pickByIDs(candidates, rawIDs, s.maxFacts)
	ids := factIDsOf(selected)
	s.updateUsageBestEffort(ctx, ids)
	s.cache.Add(cacheKey, selectorCacheEntry{
		factIDs:   ids,
		expiresAt: time.Now().Add(s.cacheTTL),
	})
	metrics.MemorySelectRunsInc(metrics.MemorySelectLLMSuccess)
	metrics.MemorySelectFactsInjectedAdd(int64(len(selected)))
	return selected, nil
}

// fallbackTop returns the first maxFacts candidates (already ordered confidence
// DESC by store.List), records UpdateUsage + cache, and is used by both the
// LLM-error and parse-error paths.
func (s *selectorService) fallbackTop(ctx context.Context, candidates []model.UserMemoryFact, cacheKey string) []model.UserMemoryFact {
	n := s.maxFacts
	if len(candidates) < n {
		n = len(candidates)
	}
	out := candidates[:n]
	ids := factIDsOf(out)
	s.updateUsageBestEffort(ctx, ids)
	s.cache.Add(cacheKey, selectorCacheEntry{
		factIDs:   ids,
		expiresAt: time.Now().Add(s.cacheTTL),
	})
	metrics.MemorySelectFactsInjectedAdd(int64(len(out)))
	return out
}

// updateUsageBestEffort calls factStore.UpdateUsage; failures are logged at
// warn level but never bubble up. Reason: a missed last_used_at update delays
// the LRU-style "freshness" signal by one turn — not a correctness bug.
func (s *selectorService) updateUsageBestEffort(ctx context.Context, ids []uint64) {
	if len(ids) == 0 {
		return
	}
	if err := s.factStore.UpdateUsage(ctx, ids); err != nil {
		log.Warnw("memory.selector UpdateUsage failed (non-fatal)",
			"n", len(ids), "error", err)
	}
}

// ─── BuildMemorySection ──────────────────────────────────────────────────────

// BuildMemorySection renders the selected facts into the Memories segment of
// the system prompt. Wraps content in <personal_context data-internal="true">
// — Track 2 task 2.5 Scrubber filters this tag from any LLM echo so the user
// never sees raw memory tags in responses.
//
// Layer A wording — "用户档案" reads as facts about the agent user themselves,
// not about a customer / dataset they discuss.
func (s *selectorService) BuildMemorySection(facts []model.UserMemoryFact) string {
	if len(facts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<personal_context data-internal="true">` + "\n")
	b.WriteString("【用户档案】基于历史会话总结的用户特征：\n\n")
	for i, f := range facts {
		b.WriteString(fmt.Sprintf("%d. [%s] %s\n", i+1, f.Category, f.Content))
	}
	b.WriteString("</personal_context>\n")
	return b.String()
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// buildCacheKey hashes (userID, userInput) into a stable LRU key. The hash
// truncation (sha256 first 16 hex chars = 64 bits) makes collisions astronomically
// rare under any realistic per-user input volume.
func buildCacheKey(userID uint, userInput string) string {
	return fmt.Sprintf("%d:%s", userID, hashInput(userInput))
}

// hashInput returns the first 16 hex chars of sha256(input). 64-bit entropy
// dominates per-user fact-set entropy by orders of magnitude.
func hashInput(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// factIDsOf extracts the ID slice from a facts list, preserving order.
func factIDsOf(facts []model.UserMemoryFact) []uint64 {
	out := make([]uint64, len(facts))
	for i, f := range facts {
		out[i] = f.ID
	}
	return out
}

// capUserInputForSelector caps userInput at selectorMaxUserInputRunes on a
// rune boundary so the selector LLM prompt stays inside its ~4000-token budget
// (spec §关键参数). Returns input unchanged when ≤ the cap. Appends an
// "…(truncated)" sentinel so prompt readers (and Langfuse traces) can see the
// elision happened.
func capUserInputForSelector(userInput string) string {
	r := []rune(userInput)
	if len(r) <= selectorMaxUserInputRunes {
		return userInput
	}
	return string(r[:selectorMaxUserInputRunes]) + "…(truncated)"
}

// truncateForTrace caps a string at n chars so Langfuse trace inputs don't
// blow up on long user messages. Adds " …(truncated)" suffix.
func truncateForTrace(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Truncate on rune boundary for Chinese-safety.
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + " …(truncated)"
}

// pickByIDs maps the LLM-returned raw ID strings back to facts in the
// candidate set, preserving the LLM-returned order, capping at maxFacts, and
// dropping unknown IDs. If the LLM under-returned (< maxFacts known IDs),
// backfills from the candidate top (which is already confidence DESC) so the
// final result always reaches maxFacts or len(candidates), whichever is smaller.
//
// The LLM may return IDs in several shapes:
//   - "12" / "12345"           — bare integer (preferred — matches DB id)
//   - "f001" / "f042"          — prompt-template-style "fN" label (we strip "f")
//   - "{\"id\":12}"            — defensive: object root → use id field
//
// pickByIDs is permissive: anything not parseable as uint64 is silently dropped.
func pickByIDs(candidates []model.UserMemoryFact, rawIDs []string, maxFacts int) []model.UserMemoryFact {
	byID := make(map[uint64]model.UserMemoryFact, len(candidates))
	for _, f := range candidates {
		byID[f.ID] = f
	}
	seen := make(map[uint64]bool, maxFacts)
	out := make([]model.UserMemoryFact, 0, maxFacts)

	for _, raw := range rawIDs {
		if len(out) >= maxFacts {
			break
		}
		id, ok := parseLLMID(raw)
		if !ok {
			continue
		}
		if seen[id] {
			continue
		}
		fact, present := byID[id]
		if !present {
			continue // unknown ID — silently drop
		}
		seen[id] = true
		out = append(out, fact)
	}

	// Backfill from candidate top if LLM under-returned. Preserves
	// confidence-DESC ordering for the tail.
	if len(out) < maxFacts {
		for _, f := range candidates {
			if len(out) >= maxFacts {
				break
			}
			if seen[f.ID] {
				continue
			}
			out = append(out, f)
			seen[f.ID] = true
		}
	}
	return out
}

// parseLLMID accepts the same raw string formats described on pickByIDs and
// returns the decoded uint64 + whether parsing succeeded.
func parseLLMID(raw string) (uint64, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false
	}
	// Strip "f" / "F" prefix from prompt-template-style "f001".
	if (s[0] == 'f' || s[0] == 'F') && len(s) > 1 {
		s = s[1:]
	}
	// Strip JSON quotes the LLM may keep around the IDs.
	s = strings.Trim(s, `"' `)
	if s == "" {
		return 0, false
	}
	// Strip leading zeros — strconv would accept them but be explicit.
	s = strings.TrimLeft(s, "0")
	if s == "" {
		return 0, false
	}
	var id uint64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		id = id*10 + uint64(r-'0')
	}
	return id, true
}

// parseSelectorResponse extracts the JSON array of IDs from the LLM's raw text.
//
// Tolerates:
//   - leading/trailing whitespace
//   - markdown code fences (```json ... ```)
//   - extra prose around the array (extracts via first '[' to last ']')
//
// Returns the array of raw ID strings (parsing each into uint64 is pickByIDs's job).
func parseSelectorResponse(raw string) ([]string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, errors.New("empty response")
	}
	// Strip markdown fence if present.
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
		if j := strings.LastIndex(s, "```"); j >= 0 {
			s = s[:j]
		}
		s = strings.TrimSpace(s)
	}
	// Extract array via first '[' to last ']' — tolerates prose around it.
	arrayStr := extractJSONArray(s)
	if arrayStr == "" {
		return nil, fmt.Errorf("no JSON array found in response: %.40q", s)
	}
	// Try string array first ("12", "f001", ...).
	var ids []string
	if err := json.Unmarshal([]byte(arrayStr), &ids); err == nil {
		return ids, nil
	}
	// Fall back to integer array — convert each to string for uniform pickByIDs path.
	var intIDs []uint64
	if err := json.Unmarshal([]byte(arrayStr), &intIDs); err == nil {
		out := make([]string, len(intIDs))
		for i, id := range intIDs {
			out[i] = fmt.Sprintf("%d", id)
		}
		return out, nil
	}
	return nil, fmt.Errorf("response array could not be decoded as string[] or int[]: %.80q", arrayStr)
}

// extractJSONArray returns the substring from the first '[' to the last ']' or
// "" if either delimiter is missing. Permissive — does not validate balance.
func extractJSONArray(s string) string {
	start := strings.Index(s, "[")
	if start < 0 {
		return ""
	}
	end := strings.LastIndex(s, "]")
	if end < start {
		return ""
	}
	return s[start : end+1]
}

// buildSelectorPrompt renders the LLM input for selecting top-N facts.
//
// Wording emphasises Layer A: "使用者本人" (the agent's user themselves) — the
// LLM must pick facts about the user, not about customers / datasets they
// discuss. The prompt template is short to keep token cost low (qwen-turbo
// charges by tokens, and the candidate list dominates the input length).
func buildSelectorPrompt(userInput string, candidates []model.UserMemoryFact) string {
	var b strings.Builder
	b.WriteString("你是用户记忆助理。下面是 agent 使用者本人当前提的问题，和该使用者本人的已知 facts 列表。\n")
	b.WriteString("请选出与当前问题最相关的 ≤5 条 facts（按相关度从高到低排序），用于注入到 agent 的 system prompt 辅助回答。\n\n")
	b.WriteString("⚠️ 这些 facts 都是关于该使用者本人的画像（如他的工作背景、偏好、目标），不是关于他讨论的客户 / 数据集 / 对象。选择时聚焦于「哪条 fact 与他当前提的问题最相关」。\n\n")

	b.WriteString("使用者当前问题：\n")
	b.WriteString(userInput)
	b.WriteString("\n\n候选 facts（已按 confidence + 时效 + 重要度排序）：\n")
	for i, f := range candidates {
		b.WriteString(fmt.Sprintf("%d. [id=%d, conf=%.2f, cat=%s] %s\n", i+1, f.ID, f.Confidence, f.Category, f.Content))
	}
	b.WriteString("\n")
	b.WriteString("只输出 JSON 数组形如 [12, 34, 5]（id 为整数），最多 5 个。选不到则输出 []。\n")
	b.WriteString("不要输出任何其它解释文字 / markdown 标记。\n\n")
	b.WriteString("评分要素（权重由你判断）：\n")
	b.WriteString("- 相关度：fact 内容与使用者当前问题的语义关联\n")
	b.WriteString("- 时效性：新 fact 优于旧 fact（除非旧 fact 是 background knowledge）\n")
	b.WriteString("- confidence：高 confidence 优于低 confidence\n")
	return b.String()
}
