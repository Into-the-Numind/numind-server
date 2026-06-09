# LLM Prompt-Cache Foundation + Batch A — Implementation Spec (S0–S3)

- Feature id: `llm-prompt-cache`
- Track: Standard (DB schema + billing high-risk + >3 files)
- Worktree: `/private/tmp/wt-llm-prompt-cache-numind-server`
- Branch: `feature/llm-prompt-cache`
- Stage at write time: S3 (spec + plan ready; S4 implementation next). Revised 2026-06-09 to resolve 7 S3 design-review findings (3 P0 + 4 P1): see §4.2 (legacy SSE-parse path, P0 #3), §4.4 (ICalculator mock updates, P1 #4), §4.5 (salesrag serialization chain, P1 #6), §4.6 (DB-verified ratio-based seed, P0 #1; paired columns, P1 #5), §4.7 (Langfuse dual-channel A+B, P0 #2), §4.8 (Name()==ToolName verified, P1 #7).

---

## 1. Goal

Make the platform bill LLM **prompt-cache HIT tokens at a discounted rate** for Batch A models (DeepSeek V4 Pro / V3.2 and OpenAI GPT-5.4, all reached via the DMXAPI OpenAI-compatible adapter), and make those cache tokens observable — **without changing any request format and without any risk of altering today's non-cache behavior**.

Batch A providers auto-cache (OpenAI-standard prefix caching). We only need to (a) parse the provider-reported cached-token count, (b) thread it into billing, (c) bill the cached portion at a separate (discounted) input price seeded into `pricing_rule`, and (d) keep the prompt prefix byte-stable so the provider's auto prefix-cache actually fires across SOP / chatbot / agent.

## 2. Strict Scope & Prime Directive (zero regression)

PRIME DIRECTIVE — **purely additive, zero regression**. Every change degrades to today's exact behavior when:

1. the provider returns **no cache tokens** (`cachedTokens == 0`), AND
2. the cached price is **unset / NULL**.

When either holds → cost, revenue, usage records, and observability payloads are **byte-identical** to current behavior. This is acceptance criterion #1; a change that can alter non-cache behavior is wrong.

IN SCOPE:
- Parse provider cache-token usage in the OpenAI-compatible adapter.
- Thread cached-token count to the billing/pricing layer.
- Cache-aware cost (`pricing.CalculateCostWithCache`) and revenue (`recorder.computeRevenue`) formulas, both collapsing to today's formula at the fallbacks above.
- Nullable cached input price columns on `pricing_rule` (cost + sell).
- Seed cached prices for DeepSeek/GPT **flat** routes.
- Observability: a `cached_prompt_tokens` column on `usage_record` + a Langfuse cached-usage field.
- Prefix-stability: SOP/chatbot are stable today (regression tests only); the single real fix is **deterministic agent tool ordering**.

OUT OF SCOPE (do NOT touch):
- Claude/Gemini message-format changes (`cache_control` / `cachedContent`).
- The #2 sliding-window / summary work.
- Any frontend.
- `config_prod.yaml`.
- Tiered-token cache pricing (GPT-5.4 via the `aihubmix` tiered route stays unpriced for cache → NULL → full-price fallback → zero regression). Documented as a known Batch-A limitation; extending cache to `pricing_rule_tier` is deferred.
- `prompt_cache_miss_tokens` parsing (redundant: miss = prompt − cached; parsing it invites double counting).

---

## 3. Reconciled Design (contradictions resolved)

The four design specs (D1 adapter-usage, D2 pricing/billing, D3 observability, D4 capability+prefix) overlapped on the cached-token field and on which routes get cached prices. Resolutions:

### R-A. Where the cached-token field lives (D1 vs D3 vs D2)
There are **three** token-usage carriers in this codebase, and the cached field must be added to all of the ones a cost path reads:

| Type | File | Why it needs the field |
| --- | --- | --- |
| `aiservice.TokenUsage` | `internal/pkg/aiservice/types.go:121-128` | adapter output; first surface (D1) |
| `billing.TokenUsage` | `internal/pkg/billing/types.go:6-19` | `sop` aliases this (`executor.go:84 type TokenUsage = billing.TokenUsage`); salesrag returns it; reconcile reads it |
| `model.UsageRecord` | `internal/pkg/model/billing.go:6-51` | the DB billing-audit sink **and** the struct the recorder/middleware compute cost from |

D2's site list claimed reconcile reads `usage.CachedTokens` / `record.CachedTokens`. **Corrected**: the field name is `CachedPromptTokens` everywhere (matching the existing `ReasoningTokens` naming), and the recorder reads `record.CachedPromptTokens`.

### R-B. Primary cost-correctness join = the middleware/recorder path, not the biz call sites (D2 Part 5 corrected)
`publishCostToHolder` (`middleware/billing.go:534-557`) and `recorder.computeCost` (`recorder.go:359-372`) compute cost from `*model.UsageRecord` and **already drive reconciliation**. The cleanest, lowest-risk correctness join is:

1. middleware writes `record.CachedPromptTokens` from `chunk.Usage` / `chatResp.Usage` (D3 write sites) **before** `publishCostToHolder` runs;
2. `publishCostToHolder` and `recorder.computeCost` call `CalculateCostWithCache(..., record.CachedPromptTokens)`.

This makes the cost cache-aware for **every** gateway path (SOP, chatbot, agent, salesrag) through one chokepoint. The biz-layer reconcile call sites (sop.go:978/1736, salesrag.go:413) are upgraded too for consistency, reading `usage.CachedPromptTokens` (sop) and a threaded cached int (salesrag), but the middleware path is the load-bearing one.

### R-C. Which routes get cached prices (D2 vs D4)
Seed cached input prices **only on existing `flat` `pricing_rule` rows** (verified prices below). GPT-5.4 via `aihubmix` is `tiered_token` (head price 0, real price in `pricing_rule_tier`) → out of scope → stays NULL → full price (zero regression). The DMXAPI `gpt-5.4` row IS flat (input ¥10) and IS seeded.

### R-D. No capability flag (D4 minimal option, adopted)
Do **not** add a `supports_cache` boolean. The presence of a cached price on `pricing_rule` IS the per-model capability signal (NULL = not cached-priced = full price). This avoids the GORM `default:true` bool-drop gotcha entirely. If a future out-of-scope batch needs an explicit flag, it goes in `ai_service.capability_json` (additive JSON), never a new SQL bool column.

### R-E. Nullable numeric prices, not `default:0`
Cached price columns are nullable `*float64` (`DECIMAL(10,4) NULL`). NULL = "fall back to full input price". Do NOT use `default 0` (0 would mean "free cached input" — wrong/unsafe). Cached-token observability column on `usage_record` IS `int default:0` (matching sibling `PromptTokens`/`ReasoningTokens`; 0 = no cache).

---

## 4. Per-area change plan (verbatim-followable, with file:line anchors)

> All paths are absolute under `/private/tmp/wt-llm-prompt-cache-numind-server`. Mirror the existing `ReasoningTokens` pattern 1:1 — it is the proven template.

### 4.1 Adapter cache-token parsing (D1)

File `internal/pkg/aiservice/adapter/adapter.go`:

- **`oaiUsage` struct** (lines 300-313, after `ReasoningTokens` at 312, before closing `}` at 313) add:
  ```go
  	// PromptTokensDetails carries the nested cached_tokens field used by OpenAI
  	// (gpt-5.x) and DeepSeek (V4 Pro / V3.2) reached via the DMXAPI
  	// OpenAI-compatible endpoint. cached_tokens is the prefix-cache HIT portion
  	// of prompt_tokens (a subset, never additive). Auto prefix caching; no
  	// request-format change.
  	PromptTokensDetails *oaiPromptTokensDetails `json:"prompt_tokens_details,omitempty"`
  	// PromptCacheHitTokens is DeepSeek's NATIVE flat cache-hit field, used as a
  	// fallback when DMXAPI passes DeepSeek's native usage shape through unchanged.
  	PromptCacheHitTokens int `json:"prompt_cache_hit_tokens,omitempty"`
  ```
- **New nested type** immediately after the `oaiUsage` closing brace (line 313), before `oaiCompletionTokensDetails` (line 315):
  ```go
  // oaiPromptTokensDetails nests the cached_tokens field on providers using the
  // OpenAI-standard wire path `usage.prompt_tokens_details.cached_tokens`.
  type oaiPromptTokensDetails struct {
  	CachedTokens int `json:"cached_tokens,omitempty"`
  }
  ```
- **New helper** immediately after `extractReasoningTokens` (after line 334):
  ```go
  // extractCachedPromptTokens returns the prompt cache-HIT token count from
  // whichever wire path the provider used. Prefers nested
  // (prompt_tokens_details.cached_tokens) over flat (prompt_cache_hit_tokens).
  // Returns 0 when neither is present ⇒ byte-identical to pre-cache code. The
  // value is a SUBSET of PromptTokens, never additive to the total.
  func (u *oaiUsage) extractCachedPromptTokens() int {
  	if u == nil {
  		return 0
  	}
  	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens > 0 {
  		return u.PromptTokensDetails.CachedTokens
  	}
  	return u.PromptCacheHitTokens
  }
  ```

**Do NOT touch** `oaiEmbedResponse.Usage` (adapter.go:357-359, anonymous inline embed usage — embeddings have no cache concept). `oaiChatResponse`/`oaiStreamChunk` already hold `*oaiUsage` by pointer → inherit the new fields automatically.

Mapping sites (add one line each, inside the existing `aiservice.TokenUsage{...}` literal):
- `internal/pkg/aiservice/adapter/dmxapi.go:193-198` (after `ReasoningTokens` at 197) — **primary Batch-A path**:
  `CachedPromptTokens: oaiResp.Usage.extractCachedPromptTokens(),`
- `internal/pkg/aiservice/adapter/stream.go:115-120` (after `ReasoningTokens` at 119, uses `chunk.Usage`):
  `CachedPromptTokens: chunk.Usage.extractCachedPromptTokens(),`
- `internal/pkg/aiservice/adapter/ali.go:111-115` (after `TotalTokens` at 114) — stays 0 (DashScope doesn't emit it); symmetry/future-proof.
- `internal/pkg/aiservice/adapter/volc.go:82-86` (after `TotalTokens` at 85) — stays 0 (Volc not Batch A); symmetry.

### 4.2 Token-usage carriers (D1 + D3 reconciled) — incl. legacy SSE-parse path (P0 fix)

- `internal/pkg/aiservice/types.go` — `TokenUsage` struct (121-128), after `ReasoningTokens` (127):
  ```go
  	// CachedPromptTokens is the prefix-cache HIT portion of PromptTokens (a
  	// subset, never additive). Non-zero only for auto-caching providers on a
  	// cache hit. 0 ⇒ billing falls back to full input price (zero regression).
  	// Downstream non-cached input = PromptTokens - CachedPromptTokens.
  	CachedPromptTokens int `json:"cached_prompt_tokens,omitempty"`
  ```
- `internal/pkg/billing/types.go` — `TokenUsage` struct (6-19). This carrier is unmarshalled in **two** distinct ways and BOTH must capture cache tokens:
  1. **Gateway path**: re-marshal/unmarshal of `aiservice.TokenUsage` JSON (already carries `cached_prompt_tokens`).
  2. **Legacy SSE-parse path** (P0 #3): `billing.ExtractUsageFromSSEData` (`billing/types.go:36-60`) and the executor's inline `tempUsage` unmarshal sites (`executor.go:396/1064/1414`) unmarshal the **raw provider `usage` object** directly into `billing.TokenUsage`. The raw provider object uses the WIRE shape `prompt_tokens_details.cached_tokens` (OpenAI/DeepSeek-V3.2 via DMXAPI) or flat `prompt_cache_hit_tokens` (DeepSeek native) — NEVER `cached_prompt_tokens`. A single flat field would silently read 0 on this path.

  Therefore `billing.TokenUsage` mirrors the existing `CompletionTokensDetails` nested pattern: add a flat field for the Gateway round-trip AND a nested struct + flat alias for the raw wire shape, then flatten in `Normalize()`:
  ```go
  	// CachedPromptTokens is the flat cache-HIT subset of PromptTokens. Populated
  	// either directly (Gateway round-trip via cached_prompt_tokens JSON) or by
  	// Normalize() from the raw provider wire fields below.
  	CachedPromptTokens int `json:"cached_prompt_tokens"` // 命中缓存的输入 tokens（PromptTokens 的子集）
  	// PromptCacheHitTokens is DeepSeek's NATIVE flat field (raw SSE-parse path).
  	PromptCacheHitTokens int `json:"prompt_cache_hit_tokens"`
  	// PromptTokensDetails carries the OpenAI/DeepSeek-V3.2 nested cached_tokens
  	// on the raw SSE-parse path (executor.go inline unmarshal + ExtractUsageFromSSEData).
  	PromptTokensDetails struct {
  		CachedTokens int `json:"cached_tokens"`
  	} `json:"prompt_tokens_details"`
  ```
  Extend `Normalize()` (currently flattens reasoning from `CompletionTokensDetails`) to flatten cache, precedence nested > flat-native, and never overwrite an already-set flat value:
  ```go
  	if u.CachedPromptTokens == 0 {
  		if u.PromptTokensDetails.CachedTokens > 0 {
  			u.CachedPromptTokens = u.PromptTokensDetails.CachedTokens
  		} else if u.PromptCacheHitTokens > 0 {
  			u.CachedPromptTokens = u.PromptCacheHitTokens
  		}
  	}
  ```
  Zero-regression: absent wire fields ⇒ all three stay 0 ⇒ `CachedPromptTokens` stays 0 ⇒ identical to today. The executor inline `tempUsage` sites (396/1064/1414) currently do **not** call `Normalize()`; add a `tempUsage.Normalize()` immediately after the successful unmarshal at each of those three sites so the nested/native wire fields flatten. (`ExtractUsageFromSSEData` already calls `Normalize()` at line 56.) This makes the legacy SOP direct path AND the Ali/Volc/controller SSE paths cache-aware without bypassing aiservice for new calls — purely a parse upgrade.

  > Legacy-path reachability note for Batch A: `SopExecutor.ExecuteNodeStream` routes to the Gateway whenever `modelKey != ""` (executor.go:200-201); the inline-parse legacy path (line 396) is only reached for nodes with a hard-coded `APIKey`/`BaseURL` and `modelKey == ""`. The Ali/Volc deep-thinking paths (1064/1414) are non-Batch-A providers. So for Batch A in production the Gateway path is load-bearing; the legacy-path parse upgrade is defense-in-depth (and the correct fix if a node is ever pointed directly at a DMXAPI endpoint). Task 4 adds a unit test asserting `ExtractUsageFromSSEData` parses both wire shapes into `CachedPromptTokens`, and a test asserting a no-cache SSE blob yields 0 (regression).
- `internal/pkg/model/billing.go` — `UsageRecord` struct (6-51), after `ReasoningTokens` (16). The DB sink + the struct cost is computed from:
  ```go
  	CachedPromptTokens int `gorm:"column:cached_prompt_tokens;default:0" json:"cached_prompt_tokens"`
  ```

### 4.3 Pricing model + migration (D2 + D4)

File `internal/pkg/model/billing.go` — `PricingRule` struct (59-83), immediately after `SellOutputPricePerMTok` (line 71):
```go
	// CachedInputPricePerMTok is the COST price (¥/MTok) for cache-HIT prompt
	// tokens. NULL = not set ⇒ cached portion billed at full InputPricePerMTok.
	CachedInputPricePerMTok *float64 `gorm:"column:cached_input_price_per_m_tok;type:decimal(10,4)" json:"cached_input_price_per_mtok,omitempty"`
	// SellCachedInputPricePerMTok is the SELL price (¥/MTok) for cache-HIT prompt
	// tokens. NULL = not set ⇒ cached portion billed at full SellInputPricePerMTok.
	SellCachedInputPricePerMTok *float64 `gorm:"column:sell_cached_input_price_per_m_tok;type:decimal(10,4)" json:"sell_cached_input_price_per_mtok,omitempty"`
```
Pointer `*float64`, **no `default:` tag** → column nullable, GORM writes SQL NULL when nil.

Migration `migrations/20260609_121500_add_cached_input_price.sql` (+ `_rollback.sql`). Column name convention is `_per_m_tok` (canonical; NOT `_per_mtok`). Idempotent:
```sql
-- Add cache-hit input price columns to pricing_rule. NULLABLE on purpose:
-- NULL = "cached price not configured" ⇒ the cached portion of prompt tokens is
-- billed at the FULL input price (byte-identical to pre-cache behavior). Unit:
-- ¥ per 1,000,000 tokens, matching input_price_per_m_tok.
-- NOTE: CI does NOT auto-run migrations (CLAUDE.md §5.2); apply via SSH before deploy.
ALTER TABLE pricing_rule
  ADD COLUMN IF NOT EXISTS cached_input_price_per_m_tok DECIMAL(10,4) NULL
    COMMENT '成本价：每百万缓存命中输入 tokens（元）。NULL=未设置，按全价 input 计费'
    AFTER sell_output_price_per_m_tok,
  ADD COLUMN IF NOT EXISTS sell_cached_input_price_per_m_tok DECIMAL(10,4) NULL
    COMMENT '售价：每百万缓存命中输入 tokens（元）。NULL=未设置，按全价 sell_input 计费'
    AFTER cached_input_price_per_m_tok;
```
Rollback:
```sql
ALTER TABLE pricing_rule
  DROP COLUMN IF EXISTS sell_cached_input_price_per_m_tok,
  DROP COLUMN IF EXISTS cached_input_price_per_m_tok;
```

Migration `migrations/20260609_121500_add_cached_prompt_tokens_to_usage_record.sql` (+ `_rollback.sql`), idempotent:
```sql
-- Cached prompt-token observability column. Additive: default 0 = no cache =
-- identical to pre-cache billing audit. Source: OpenAI usage.prompt_tokens_details.cached_tokens;
-- DeepSeek prompt_cache_hit_tokens (both via the OpenAI-compatible DMXAPI endpoint).
ALTER TABLE usage_record ADD COLUMN IF NOT EXISTS cached_prompt_tokens INT NOT NULL DEFAULT 0;
```
Rollback:
```sql
ALTER TABLE usage_record DROP COLUMN IF EXISTS cached_prompt_tokens;
```

### 4.4 Cost formula (D2 Part 3)

File `internal/pkg/pricing/pricing.go`:

- Add to `ICalculator` (after `CalculateCost`, ~line 61):
  ```go
  	// CalculateCostWithCache is CalculateCost plus prompt-cache awareness.
  	// cachedTokens is the subset of promptTokens served from the provider's
  	// prompt cache (0 ⇒ identical to CalculateCost). cachedTokens is clamped to
  	// [0, promptTokens]. The cached portion is billed at the rule's cached input
  	// price when set; when NULL it falls back to the full input price, making the
  	// result byte-identical to CalculateCost.
  	CalculateCostWithCache(ctx context.Context, serviceType, provider, model string,
  		promptTokens, completionTokens, cachedTokens int) (costCents int64, err error)
  ```
- Replace `CalculateCost` body (107-138) with a delegate:
  ```go
  func (c *calculator) CalculateCost(ctx context.Context, serviceType, provider, model string,
  	promptTokens, completionTokens int,
  ) (int64, error) {
  	return c.CalculateCostWithCache(ctx, serviceType, provider, model, promptTokens, completionTokens, 0)
  }
  ```
- New real implementation:
  ```go
  func (c *calculator) CalculateCostWithCache(ctx context.Context, serviceType, provider, model string,
  	promptTokens, completionTokens, cachedTokens int,
  ) (int64, error) {
  	rule, err := c.resolvePricingRule(ctx, serviceType, provider, model)
  	if err != nil {
  		return 0, err
  	}
  	if cachedTokens < 0 {
  		cachedTokens = 0
  	}
  	if cachedTokens > promptTokens {
  		cachedTokens = promptTokens
  	}
  	var costYuan float64
  	switch {
  	case promptTokens > 0 || completionTokens > 0:
  		if rule.BillingMode == "tiered_token" {
  			// Tiered mode is NOT cache-aware in Batch A — bill full price (identical to today).
  			costYuan, err = c.calculateTieredCost(ctx, rule.ID, promptTokens, completionTokens)
  			if err != nil {
  				return 0, err
  			}
  		} else {
  			cachedPrice := rule.InputPricePerMTok
  			if rule.CachedInputPricePerMTok != nil {
  				cachedPrice = *rule.CachedInputPricePerMTok
  			}
  			nonCached := promptTokens - cachedTokens
  			costYuan = float64(cachedTokens)/1_000_000*cachedPrice +
  				float64(nonCached)/1_000_000*rule.InputPricePerMTok +
  				float64(completionTokens)/1_000_000*rule.OutputPricePerMTok
  		}
  	default:
  		return 0, nil
  	}
  	multiplier := rule.CreditMultiplier
  	if multiplier <= 0 {
  		multiplier = 1.0
  	}
  	return int64(math.Round(costYuan * multiplier * 100)), nil
  }
  ```
  Zero-regression proof: `cachedTokens==0` ⇒ `nonCached==promptTokens`, cached term 0 ⇒ collapses to `prompt/1e6*input + completion/1e6*output`. `cachedTokens>0` but `CachedInputPricePerMTok==nil` ⇒ `cachedPrice==InputPricePerMTok` ⇒ cached + nonCached term == `prompt/1e6*input`. Both keep `×multiplier×100 round`.

  **Mock impls MUST be updated (P1 #4 — build-breaking otherwise).** Adding `CalculateCostWithCache` to the `ICalculator` interface breaks compilation of every test that defines a mock implementing only `CalculateCost`. `grep -rn 'func ([a-zA-Z ]*) CalculateCost(' internal/ --include='*_test.go'` finds **three** mocks that MUST each gain a `CalculateCostWithCache` method (Task 3 scope, not Task 4 — the interface changes in Task 3):
  - `internal/numind/biz/budget/r2_estimator_test.go:18` (`mockPricer`)
  - `internal/pkg/aiservice/middleware/billing_test.go:28` (`mockPricingCalc`)
  - `internal/pkg/billing/recorder_test.go:288` (`spyCalculator`)

  Minimal mock addition (delegate to the existing `CalculateCost` with cachedTokens ignored, or record the cached arg if the test asserts on it):
  ```go
  func (m *mockX) CalculateCostWithCache(ctx context.Context, st, p, mdl string, pt, ct, cached int) (int64, error) {
  	return m.CalculateCost(ctx, st, p, mdl, pt, ct) // cached ignored in this mock
  }
  ```
  Re-run `grep` before finishing Task 3 to confirm no further mocks were added since this audit. Failing to update mocks fails `go build ./...`, not just `go test`.

### 4.5 Revenue formula + cost-path threading (D2 Part 4/5 + R-B)

File `internal/pkg/billing/recorder.go`:

- `computeCost` (359-372) LLM branch — upgrade the `CalculateCost` call to:
  ```go
  		costCents, err := r.calc.CalculateCostWithCache(ctx, record.ServiceType, record.Provider, record.Model,
  			record.PromptTokens, record.CompletionTokens, record.CachedPromptTokens)
  ```
- `computeRevenue` (401-424) flat branch (replace lines ~408-415, the `case record.PromptTokens > 0 ...` block's flat sub-branch):
  ```go
  	case record.PromptTokens > 0 || record.CompletionTokens > 0:
  		if rule.BillingMode == "tiered_token" {
  			revenueYuan = r.calculateTieredRevenue(ctx, rule.ID, record.PromptTokens, record.CompletionTokens)
  		} else {
  			cached := record.CachedPromptTokens
  			if cached < 0 {
  				cached = 0
  			}
  			if cached > record.PromptTokens {
  				cached = record.PromptTokens
  			}
  			cachedSell := rule.SellInputPricePerMTok
  			if rule.SellCachedInputPricePerMTok != nil {
  				cachedSell = *rule.SellCachedInputPricePerMTok
  			}
  			nonCached := record.PromptTokens - cached
  			revenueYuan = float64(cached)/1_000_000*cachedSell +
  				float64(nonCached)/1_000_000*rule.SellInputPricePerMTok +
  				float64(record.CompletionTokens)/1_000_000*rule.SellOutputPricePerMTok
  		}
  ```
  Do NOT touch the `TotalTokens` / `BytesUploaded` / `default` per-call branches (415-420). Do NOT add a multiplier to revenue.

  **Paired-column guard (P1 #5).** `CachedInputPricePerMTok` (cost) and `SellCachedInputPricePerMTok` (sell) are logically coupled — both set or both NULL. The seed migration (§4.6) always UPDATEs the pair atomically. The Go code is independently NULL-safe on EACH side (cost falls back to `InputPricePerMTok`, revenue to `SellInputPricePerMTok`), so even an accidental partial (one set, one NULL) degrades to full price on the unset side — never a crash, never a free-input bug. No extra Go validation needed; the paired-UPDATE in the migration is the integrity guarantee, with a comment reminding maintainers the columns are paired.

File `internal/pkg/aiservice/middleware/billing.go` (the load-bearing reconcile join, R-B):
- `populateLLMUsage` (non-stream, after `r.ReasoningTokens = chatResp.Usage.ReasoningTokens` at 480):
  `r.CachedPromptTokens = chatResp.Usage.CachedPromptTokens`
- `wrapStreamForBilling` (stream, after `record.ReasoningTokens = chunk.Usage.ReasoningTokens` at 107, BEFORE the `publishCostToHolder` call at 115):
  `record.CachedPromptTokens = chunk.Usage.CachedPromptTokens`
- `publishCostToHolder` (534-557) — upgrade the `CalculateCost` call (546) to:
  ```go
  	costCents, err := calc.CalculateCostWithCache(ctx, record.ServiceType, record.Provider, record.Model,
  		record.PromptTokens, record.CompletionTokens, record.CachedPromptTokens)
  ```

Biz-layer reconcile call sites (upgrade for consistency; middleware path already covers correctness):
- `internal/numind/biz/sop/sop.go:978` and `:1736` — `usage` is `*billing.TokenUsage` (alias), so pass `usage.CachedPromptTokens` to `CalculateCostWithCache`. The Gateway construction in `executor.go:676-680` adds `CachedPromptTokens: chunk.Usage.CachedPromptTokens` to the `billing.TokenUsage` literal. The legacy inline `tempUsage` unmarshal sites at `executor.go:396/1064/1414` get the value via `tempUsage.Normalize()` (added in §4.2) right after the successful unmarshal — they read the nested/native wire fields, NOT a `cached_prompt_tokens` key (those raw provider blobs never contain that key).
- `internal/numind/biz/salesrag/salesrag.go` (**P1 #6 — serialization gap fixed**). The chain is: `RetrieveStream` drains `finalUsage *aiservice.TokenUsage` (1100-1124) → emits an internal `"usage"` event whose payload map (1138-1148) is parsed by the `ChatWithSession` wrapper (`case "usage"` at 2172-2189) into local ints → `recordLLMResult(...)` (called at 2217/2220/2223). The cached count is dropped UNLESS it is added at every hop:
  1. **Emit** (salesrag.go:1138-1148) add to `usagePayload`: `"cached_prompt_tokens": finalUsage.CachedPromptTokens,`
  2. **Parse** (salesrag.go:2172-2189, `case "usage"`) add a new local `streamCachedTokens int` and read it: `if c, ok := usage["cached_prompt_tokens"].(int); ok { streamCachedTokens = c }` (declare `streamCachedTokens` alongside `streamPromptTokens` near 2138).
  3. **Thread** — `recordLLMResult` (391-395) gains a trailing `cachedTokens int` param; all three call sites (2217/2220/2223) pass `streamCachedTokens`; inside, the `pricing.CalculateCost` call (413-414) becomes `pricing.CalculateCostWithCache(ctx, "llm_chat", provider, modelName, promptTokens, completionTokens, cachedTokens)`.
  Zero-regression: the payload is an in-process `map[string]interface{}` (not re-serialized to JSON over the wire), so the `int` type assertion at the parse hop holds; when `finalUsage.CachedPromptTokens == 0` the key is still present as `0` → `streamCachedTokens == 0` → `CalculateCostWithCache(...,0)` == `CalculateCost(...)`.

Estimate/pre-check callers KEEP `CalculateCost` (no cached tokens exist pre-call):
- `biz/credit/credit_service.go:277`, `biz/credit/estimation.go:96`, `biz/budget/r2_estimator.go:51`.

### 4.6 Seed cached prices (D2 Part 6 + D4) — ratio-of-actual-price, DB-verified (P0 #1 fix)

> **P0 #1 root cause.** An earlier draft hardcoded absolute cached prices against assumed base prices, but multiple historical migrations created **distinct row identities with different prices** for what looks like "the same" model. A WHERE clause that targets the wrong `(provider, model)` string UPDATEs 0 rows → no discount fires in prod, silently. The actual rows (from `git grep` over `migrations/*.sql`, table below) are NOT what the earlier draft assumed:
>
> | provider | model | billing_mode | input ¥/MTok | source migration |
> | --- | --- | --- | --- | --- |
> | dmxapi | `deepseek-v3-2-251201` | flat | **1.00** | seed_pricing_rules.sql:24 |
> | volc-ark | `deepseek-v3-2-251201` | flat | 1.2184 | seed_pricing_rules.sql:18 |
> | dmxapi | `deepseek-v4-pro` | flat | 14.00 | 20260424_204500_seed_deepseek_v4_pro.sql:114 |
> | aihubmix | `deepseek-v4-pro` | flat | 14.00 | 20260424_204500_...:120 |
> | dmxapi | `DeepSeek-V3.2` | flat | 2.16 | 20260419_170000_seed_pricing_global_fallback.sql:38 |
> | dmxapi | `DeepSeek-V3.2-Thinking` | flat | 2.16 | 20260419_170000_...:40 |
> | aihubmix | `deepseek-v3.2` | flat | 2.16 | 20260416_100000_seed_aihubmix_provider.sql:139 |
> | aihubmix | `deepseek-v3.2-thinking` | flat | 2.16 | 20260420_030000_seed_drift_pricing_rules.sql:43 |
> | dmxapi | `gpt-5.4` | flat | 10.00 | 20260419_170000_...:46 |
> | aihubmix | `gpt-5.4` | **tiered_token** | (head 0) | 20260416_100000_...:151 — OUT (tier path ignores cached col) |
> | aihubmix | `gpt-5.4-thinking` | tiered_token | (head 0) | derived from aihubmix tiered — OUT |
>
> The `dmxapi/deepseek-v3-2-251201` @ ¥1.00 row was entirely missing from the earlier draft (and its cached price must be **0.10**, not 0.2160). The capitalised `DeepSeek-V3.2*` and lowercase `deepseek-v3.2*` are SEPARATE rows under different providers — both real, both seeded.

**Mandatory pre-implementation step (Task 2):** before writing the seed, the implementer runs on the dev (and confirms against prod) DB to discover the EXACT live `(provider, model, billing_mode, input_price_per_m_tok, sell_input_price_per_m_tok)` tuples — the migration files are the historical record but operators may have edited rows:
```sql
SELECT provider, model, billing_mode, input_price_per_m_tok, sell_input_price_per_m_tok
FROM pricing_rule
WHERE service_type='llm_chat' AND billing_mode='flat'
  AND (model LIKE '%deepseek%' OR model LIKE '%DeepSeek%' OR model LIKE '%gpt-5%');
```
Reconcile the result against the table above; if a row's live price differs, the seed must reflect the LIVE price.

Migration `migrations/20260609_121500_seed_cached_input_pricing.sql` (+ `_rollback.sql`). Idempotent `UPDATE` of existing flat rows, `WHERE ... AND cached_input_price_per_m_tok IS NULL` (never overwrite operator edits).

**Robustness: derive cached price as a RATIO of the row's actual stored price, not a hardcoded constant.** This is correct regardless of the exact base price each row holds, and it can never silently mis-price if an operator changed the base. **Always UPDATE both `cached_input_price_per_m_tok` and `sell_cached_input_price_per_m_tok` in the same statement (P1 #5 — paired columns: cost+sell must be set together or both left NULL).** Round to the column scale (DECIMAL(10,4)).

Ratios: DeepSeek cache-hit input ≈ **0.1×** normal (DeepSeek API context-caching docs: cache-hit input ~1/10 of cache-miss). OpenAI GPT cached input ≈ **0.5×** (OpenAI prompt-caching docs; some tiers 0.25× — seed conservatively at 0.5×).

DeepSeek flat rows (0.1× of the row's own price, paired cost+sell):
```sql
-- DeepSeek cache-hit input ≈ 0.1× normal (DeepSeek API context-caching pricing).
-- Ratio-of-actual-price: robust to whatever the row's current input price is.
-- BOTH columns set together (paired). Targets the EXACT (provider,model) live in DB.
UPDATE pricing_rule
  SET cached_input_price_per_m_tok      = ROUND(input_price_per_m_tok      * 0.1, 4),
      sell_cached_input_price_per_m_tok = ROUND(sell_input_price_per_m_tok * 0.1, 4)
  WHERE service_type='llm_chat' AND billing_mode='flat'
    AND cached_input_price_per_m_tok IS NULL
    AND (provider,model) IN (
      ('dmxapi','deepseek-v3-2-251201'),     -- input 1.00  -> 0.10
      ('volc-ark','deepseek-v3-2-251201'),   -- input 1.2184-> 0.1218 (DeepSeek model; not DMXAPI but same caching family)
      ('dmxapi','deepseek-v4-pro'),          -- input 14.00 -> 1.40
      ('aihubmix','deepseek-v4-pro'),        -- input 14.00 -> 1.40
      ('dmxapi','DeepSeek-V3.2'),            -- input 2.16  -> 0.2160
      ('dmxapi','DeepSeek-V3.2-Thinking'),   -- input 2.16  -> 0.2160
      ('aihubmix','deepseek-v3.2'),          -- input 2.16  -> 0.2160
      ('aihubmix','deepseek-v3.2-thinking')  -- input 2.16  -> 0.2160
    );
```
OpenAI flat row (0.5×, paired cost+sell):
```sql
-- OpenAI cached input ≈ 0.5× normal (OpenAI prompt-caching docs; conservative — some tiers 0.25×).
UPDATE pricing_rule
  SET cached_input_price_per_m_tok      = ROUND(input_price_per_m_tok      * 0.5, 4),
      sell_cached_input_price_per_m_tok = ROUND(sell_input_price_per_m_tok * 0.5, 4)
  WHERE service_type='llm_chat' AND billing_mode='flat'
    AND model='gpt-5.4' AND provider='dmxapi'   -- input 10.00 -> 5.00. aihubmix/gpt-5.4 is tiered_token → excluded by billing_mode='flat'.
    AND cached_input_price_per_m_tok IS NULL;
```
> MySQL tuple-IN `(provider,model) IN ((...),(...))` is supported. If the target deployment's MySQL/MariaDB version balks at tuple-IN, expand to one `UPDATE ... WHERE provider=? AND model=?` per row (same SET expression). The `volc-ark/deepseek-v3-2-251201` row is included because it is a DeepSeek model that auto-caches; it is NOT Batch A delivery (Volc, not DMXAPI) so a cache hit there is unlikely until the Volc adapter emits cache tokens, but seeding it now is harmless (NULL→ratio only fires when `cachedTokens>0`, which Volc reports as 0 today → zero regression). The implementer MAY drop the volc-ark row if they prefer DMXAPI/aihubmix-only scope; document the choice in the migration comment.

Rollback (paired NULL for the same keys):
```sql
UPDATE pricing_rule SET cached_input_price_per_m_tok=NULL, sell_cached_input_price_per_m_tok=NULL
  WHERE service_type='llm_chat' AND billing_mode='flat'
    AND (provider,model) IN (
      ('dmxapi','deepseek-v3-2-251201'),('volc-ark','deepseek-v3-2-251201'),
      ('dmxapi','deepseek-v4-pro'),('aihubmix','deepseek-v4-pro'),
      ('dmxapi','DeepSeek-V3.2'),('dmxapi','DeepSeek-V3.2-Thinking'),
      ('aihubmix','deepseek-v3.2'),('aihubmix','deepseek-v3.2-thinking'),
      ('dmxapi','gpt-5.4')
    );
```

**Post-apply verification (mandatory — proves the WHERE matched real rows):** after running the seed on dev, run
```sql
SELECT provider, model, input_price_per_m_tok, cached_input_price_per_m_tok, sell_cached_input_price_per_m_tok
FROM pricing_rule WHERE cached_input_price_per_m_tok IS NOT NULL ORDER BY provider, model;
```
and confirm the row count == the number of intended targets (≥8 deepseek + 1 gpt). A count of 0 means the WHERE missed → STOP, re-query the live model strings.

DO NOT seed: `aihubmix/gpt-5.4` and `aihubmix/gpt-5.4-thinking` (tiered_token — the cached column is not read by the tier path; excluded by `billing_mode='flat'`). `gpt-5.5` — no `pricing_rule` row exists (test fixture only); seed its cached price when the registry row is added.

Operational note (in migration comment): two pricing caches (`pricing.ruleCache`, `billing` `ResolvePricingRule` cache) have 5-min TTL; after applying the seed in prod, fire `InvalidateCache`/`PurgeAllCaches` or wait 5 min.

### 4.7 Observability — Langfuse (D3 — dual-channel A+B, P0 #2 fix)

> **P0 #2 root cause.** The deployed Langfuse is **v3** (`docker-compose.langfuse.yml:19 langfuse/langfuse:3`). This codebase's `UsageData` maps to Langfuse's simple `usage{input,output,total}` object, NOT the v3 `usageDetails` map. Whether a custom `cached_input` key on the simple `usage` object is honored by the v3 ingestion parser is **unconfirmed** — if it is dropped, Option-A-only gives zero cache observability with no warning. Mitigation: implement **both channels** so cache tokens are visible regardless of Langfuse schema handling. Channel B (output.metadata) is GUARANTEED visible because this file already merges a metadata map into `outputMap` via `WithGenOutput` (see `mergeTraceMetadata`/`buildMeta` at tracing.go:171-200), and Langfuse v3 always renders `output`.

**Channel A — `UsageData.CachedInput` (forward-compatible):**
File `internal/pkg/langfuse/types.go` — `UsageData` (67-71), add:
```go
	CachedInput int `json:"cached_input,omitempty"` // Langfuse cached-input usage (omitempty → absent when 0)
```
File `internal/pkg/langfuse/helpers.go` — add a sibling option (do NOT change `WithGenUsage`'s signature, protecting ~12 legacy callers):
```go
// WithGenCachedUsage is WithGenUsage plus the cached-input token count.
func WithGenCachedUsage(promptTokens, completionTokens, cachedTokens int) GenOption {
	return func(g *GenerationBody) {
		g.Usage = &UsageData{
			Input:       promptTokens,
			Output:      completionTokens,
			Total:       promptTokens + completionTokens,
			CachedInput: cachedTokens,
		}
	}
}
```

**Channel B — `output.metadata` key (guaranteed visible):**
File `internal/pkg/aiservice/middleware/tracing.go` — at the two EndGeneration sites that carry usage (stream close ~138-140; non-stream success ~199-201), gate on cache>0 so non-cache calls keep emitting today's EXACT event, and on a hit set BOTH channels:
```go
if usage != nil {
	if usage.CachedPromptTokens > 0 {
		// Channel A: typed usage field (honored by Langfuse versions that support it).
		opts = append(opts, langfuse.WithGenCachedUsage(usage.PromptTokens, usage.CompletionTokens, usage.CachedPromptTokens))
		// Channel B: output.metadata key — always rendered by Langfuse v3 regardless
		// of whether `cached_input` on the usage object is parsed. outputMap is the
		// same map already passed to WithGenOutput at this site.
		if md, ok := outputMap["metadata"].(map[string]interface{}); ok && md != nil {
			md["cached_input_tokens"] = usage.CachedPromptTokens
		} else {
			outputMap["metadata"] = map[string]interface{}{"cached_input_tokens": usage.CachedPromptTokens}
		}
	} else {
		opts = append(opts, langfuse.WithGenUsage(usage.PromptTokens, usage.CompletionTokens))
	}
}
```
> Apply the same A+B block at BOTH usage-carrying EndGeneration sites. The stream-close site (~138) builds its `outputMap` via `safeOutput(nil, meta)` then `mergeTraceMetadata(outputMap, lastTraceMeta)` and uses `lastUsage`; the non-stream site (~199) builds `outputMap := safeOutput(resp, meta)` and uses `usage := extractUsage(resp)` — adapt the variable names (`lastUsage`/`usage`, `outputMap`) per site but keep identical A+B logic. The else-branch is literally current code → non-cache / non-Batch-A events byte-identical (channel B only mutates `outputMap` inside the `cached>0` branch, so a no-cache event's output bytes are unchanged).

> Implementer action: confirm on dev whether Langfuse v3 renders the channel-A `cached_input` usage field; record the result in the manifest decisions log. Regardless of the answer, channel B is the load-bearing visibility guarantee and stays.

### 4.8 Prefix-stability (D4) — per mode

- **SOP — STABLE today, no code change.** Head is append-only: `buildGatewayMessages` (executor.go ~510-524) merges `node.Prompt` into the FINAL user message; history (`sop.go:631-677`) is deterministic ascending by `Sort`, new turns append at tail; `conversationID` is billing-only metadata, never injected into prompt content. **Action: regression test only** — assert the first rendered message is byte-identical across two builds and growing history by one tail turn does not change the prefix bytes.
- **chatbot — STABLE in production (fragment path), no code change.** `BuildChatContextFragments` (`biz/chatbot/stream.go:59-70`) sets `frag[0]=NewImmutableSystemFragment("sys-0", config.SystemPrompt)`; KB evidence is a SEPARATE fragment placed AFTER history (not prepended to the head). The legacy `buildChatMessages` (stream.go:456-498) that appends KB to the system head is **dead for the LLM call** (middleware overwrites Messages from fragments). **Action: regression test only** — assert `frag[0].Content == config.SystemPrompt` and is unchanged when `kbChunks` differ. (Flag legacy `buildChatMessages` KB-in-head for a later cleanup micro.)
- **agent — system-prompt head STABLE; ONE real fix = deterministic tool ordering.** `assembleSystemPrompt` head (`runner_prompt.go` → `BuildSystemPromptV2` §1 `PlatformBasePrompt`, a constant) is stable; variable content (temporal/memory) lives in §4 (tail), date is delivered via the `get_current_date` tool (not prompt-injected). The real instability: `ListAllTools()` (`biz/agent/registry.go:131-139`) ranges a Go `map` → non-deterministic `tools` array order every call. **Fix (1 line, additive):** in `ListAllTools()` sort `out` by tool name before returning — `sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })` (import `"sort"`).
  - **P1 #7 verification (done):** `FullTool.Name()` returns the `ToolName` string — the SAME identifier the LLM sees: `adapter_full_to_eino.go:295` sets the Eino `ToolName: a.ft.Name()`, and per-tool impls return their canonical name (e.g. `tool_analyze_image.go:87 func (t *analyzeImageTool) Name() string { return "analyze_image" }`). So sorting by `Name()` directly stabilises the prompt's tool identity order.
  - **Both consumers inherit the fix without change:** `runner.go:841` and `runner_runstream.go:354` each `range r.registry.ListAllTools()`, filter by `ft.IsEnabled(fullCfg)`, then `adaptFullToEinoTool(ft)`. Filtering a sorted slice yields a sorted subset → the enabled-tools list fed to the LLM is deterministic.
  - **Action: fix + regression test** `TestListAllTools_DeterministicOrder` — register tools in REVERSE-alphabetical order, call `ListAllTools()` twice, assert both return the SAME ascending `Name()` sequence (fails today due to map iteration, passes after sort).

---

## 5. Five-task plan

Each task is atomic (independently buildable + reviewable), committed with a Conventional Commit + the `Co-Authored-By: Claude Opus 4.8 (1M context)` trailer. TDD: failing test first, especially for billing/pricing. Per-task: `go build ./...` + `go test` on touched packages + `task lint`. Then the NDF dual-parallel Sonnet review (spec-compliance + code-quality) before the next task.

### Task 1 — Adapter cache-token parsing + carriers (D1 + carriers)
- Scope: §4.1 (adapter.go struct/type/helper + 4 mapping sites) and §4.2 (`aiservice.TokenUsage` flat field; `billing.TokenUsage` flat field + nested `PromptTokensDetails` + flat `PromptCacheHitTokens` + extended `Normalize()`; the `model.UsageRecord` column is added in Task 2 with its migration). NOTE: do NOT yet add `tempUsage.Normalize()` to the executor inline sites here — that is Task 4 (it pairs with the legacy-path test and threading).
- Test (TDD): table-driven `extractCachedPromptTokens` (nil→0; nested only; flat only; both→nested wins; neither→0; nested 0 + flat>0→flat); a `dmxapi.Chat` decode test with a JSON fixture containing `prompt_tokens_details.cached_tokens` asserting `TokenUsage.CachedPromptTokens`; a regression fixture with NO cache fields asserting `CachedPromptTokens==0`; a `billing.TokenUsage` `Normalize()` test (nested→flat, native flat→flat, none→0, pre-set flat preserved).
- Acceptance: builds; existing adapter tests unchanged; no-cache fixtures marshal byte-identically (omitempty drops the key on `aiservice.TokenUsage`).
- Deps: none (foundation).

### Task 2 — Pricing model + migrations + seed (D2 model/migration + D4 seed)
- **Pre-step (mandatory, P0 #1):** run the discovery `SELECT` from §4.6 against the dev DB (confirm against prod) to capture the EXACT live `(provider, model, billing_mode, input/sell input price)` tuples; reconcile against the §4.6 table; if any live price differs, the ratio-based seed still produces the right value (it reads the row's stored price), but confirm the `(provider,model)` strings match exactly so the WHERE matches >0 rows.
- Scope: §4.3 (PricingRule `*float64` cached columns + `UsageRecord.CachedPromptTokens` int column), the two schema migrations (`add_cached_input_price`, `add_cached_prompt_tokens_to_usage_record`) + rollbacks, and §4.6 seed migration (ratio-of-actual-price, paired cost+sell columns) + rollback.
- Test (TDD): GORM AutoMigrate in-memory sqlite round-trips a NULL and a set `CachedInputPricePerMTok`/`SellCachedInputPricePerMTok` (T8); a control asserting an unseeded row reads NULL on both.
- Acceptance: builds; schema migrations idempotent (`IF NOT EXISTS`); seed targets the exact verified flat rows (≥8 deepseek + 1 dmxapi/gpt-5.4), sets cost+sell as a pair, `WHERE ... IS NULL` only; post-apply `SELECT COUNT(*) WHERE cached_input_price_per_m_tok IS NOT NULL` on dev returns the intended target count (NOT 0).
- Deps: Task 1 (carriers reference column names; can also land independently since columns are additive).

### Task 3 — Cost formula + ICalculator interface (D2 Part 3)
- Scope: §4.4 (`CalculateCostWithCache` impl + `CalculateCost` delegate + the new method on the `ICalculator` interface). **Also update the 3 mock impls** (`r2_estimator_test.go:18`, `middleware/billing_test.go:28`, `recorder_test.go:288`) to add `CalculateCostWithCache` (delegate to `CalculateCost`, cached ignored) — REQUIRED or `go build ./...` breaks (P1 #4). Re-run `grep -rn 'func .*) CalculateCost(' internal/ --include='*_test.go'` before finishing to confirm all mocks covered.
- Test (TDD, `pricing_test.go`): T1 cachedTokens=0 ⇒ `CalculateCostWithCache == CalculateCost` (byte-identical, flat rule); T2 cached price NULL + cachedTokens>0 ⇒ full-price result (regression guard); T3 cached price set (input=14, cached=1.4, prompt=1000, cached=400) ⇒ exact int64; T4 cachedTokens>promptTokens ⇒ clamped; T5 tiered rule + cachedTokens>0 ⇒ identical to today; T6 CreditMultiplier≠1 still applied on cached path.
- Acceptance: `go build ./...` green (all mocks updated); all 6 tests pass; existing pricing tests unchanged.
- Deps: Task 2 (model field).

### Task 4 — Revenue branch + middleware/biz threading + legacy-path parse (D2 Part 4/5 + R-B + P0 #3 + P1 #6)
- Scope: §4.5 (recorder `computeCost`/`computeRevenue`; middleware `populateLLMUsage`/`wrapStreamForBilling`/`publishCostToHolder`; biz sop reconcile upgrades incl. `executor.go:676` Gateway literal AND `tempUsage.Normalize()` at the 3 inline legacy sites 396/1064/1414; salesrag emit→parse→thread chain: payload key at 1138-1148, `streamCachedTokens` parse at 2172-2189, `recordLLMResult` new param + `CalculateCostWithCache` at 391-414, 3 call sites 2217/2220/2223).
- Test (TDD): T7 `computeRevenue` with `SellCachedInputPricePerMTok` NULL ⇒ unchanged; set ⇒ discounted; cachedTokens=0 ⇒ unchanged. Middleware test feeding a ChatResponse / stream chunk with `CachedPromptTokens=N` asserting persisted `record.CachedPromptTokens==N` and a control `==0`. **Legacy-path test (P0 #3):** `ExtractUsageFromSSEData` parses a blob with `usage.prompt_tokens_details.cached_tokens` AND a blob with native `usage.prompt_cache_hit_tokens` into `CachedPromptTokens>0`, and a no-cache blob into `==0`. **Salesrag test (P1 #6):** drive the emit→parse→thread chain (or a focused unit on the `case "usage"` parser) asserting `streamCachedTokens` reaches `recordLLMResult` and a `cachedTokens=0` control bills full price.
- Acceptance: all tests pass; existing billing tests unchanged; no estimate/pre-check caller changed; estimate callers (`credit_service.go:277`, `estimation.go:96`, `r2_estimator.go:51`) still call plain `CalculateCost`.
- Deps: Tasks 1–3.

### Task 5 — Observability (Langfuse dual-channel) + prefix-stability (D3 Langfuse + D4 prefix)
- Scope: §4.7 (Langfuse channel A: `UsageData.CachedInput` + `WithGenCachedUsage`; channel B: `output.metadata["cached_input_tokens"]` at BOTH tracing.go usage-carrying EndGeneration sites, gated on `cached>0`) and §4.8 (agent `ListAllTools` sort fix; SOP + chatbot + agent-tool-order regression tests).
- Test (TDD): langfuse helpers test asserting `WithGenCachedUsage` sets `UsageData.CachedInput` and `WithGenUsage` still yields `CachedInput==0`; a tracing test asserting on a cache hit BOTH the usage field AND `outputMap["metadata"]["cached_input_tokens"]` are set, and on a no-cache event `outputMap` is byte-identical to today (channel B does not mutate it); `TestListAllTools_DeterministicOrder` (fails today, passes after sort); `TestBuildSOPGatewayFragments_HeadByteStable`; `TestBuildChatContextFragments_HeadStable_KBDoesNotPoisonPrefix`.
- Acceptance: all tests pass; non-cache Langfuse events byte-identical (else-branch == current code; channel B mutation confined to the cache>0 branch).
- Deps: Task 1 (usage field).

---

## 6. S5 Verification Strategy

Confirm three properties on dev; Go TDD provides the persistent regression protection.

1. **Zero regression (covered by Go TDD — permanent).** The pricing/billing regression tests (T1, T2, T5, T7 + middleware control cases asserting `CachedPromptTokens==0`) prove cost/revenue/usage are byte-identical when cachedTokens=0 OR cached price NULL. These are the load-bearing gate for a billing-high-risk feature and must be green before S6. Run `go test ./internal/pkg/pricing/... ./internal/pkg/billing/... ./internal/pkg/aiservice/... ./internal/numind/biz/sop/... ./internal/numind/biz/agent/...` plus `task test` at S5.
2. **Cached tokens observed (real DMXAPI passthrough — dev deploy).** Apply both schema migrations + the seed migration via SSH on dev (CI does not run migrations). **Immediately after the seed, run the §4.6 post-apply COUNT check** to prove the WHERE matched real rows (count == intended targets, NOT 0). Trigger a SOP run / chatbot / agent turn on a Batch-A model (`deepseek-v4-pro` via DMXAPI) twice with the same large prefix so the second call hits the provider cache. Verify: `SELECT cached_prompt_tokens, prompt_tokens FROM usage_record ORDER BY id DESC LIMIT 5;` shows `cached_prompt_tokens > 0` on the 2nd call; the Langfuse generation shows the cache count via channel B (`output.metadata.cached_input_tokens`) and, if the deployed v3 honors it, channel A (`cached_input` usage). Record which channel(s) rendered in the manifest decisions log.
3. **Billing discount applied (dev deploy).** For the same cache-hit call, verify `cost_cents` / `revenue_cents` on `usage_record` are LOWER than an equivalent no-cache call by the expected delta (cached portion at 0.1× / 0.5×), and that a model with NO seeded cached price (or cachedTokens=0) bills at the full rate unchanged. Cross-check the reserve/reconcile credit math nets out (no over/under-refund).

Honesty note: dev deploy is a one-time real-provider confirmation of the passthrough + discount; the permanent regression protection is the Go test suite (no Playwright needed — backend-only feature).

---

## 7. Recorded judgment calls

1. Field name `CachedPromptTokens` everywhere (matches existing `ReasoningTokens`), on all three usage carriers + the DB record (corrects D2's `CachedTokens` naming and D1/D3 overlap).
2. Primary cost-correctness join is the middleware/recorder `model.UsageRecord` path (one chokepoint covers SOP/chatbot/agent/salesrag); biz call sites upgraded for consistency only.
3. Additive `CalculateCostWithCache` method; `CalculateCost` delegates with cachedTokens=0 (lowest churn vs changing arity of 6 prod + ~15 test callers). The 3 mock `ICalculator` impls in tests are updated in the same task to keep `go build ./...` green (P1 #4).
4. Nullable `*float64` cached prices (NULL = full-price fallback); int `default:0` for the observability column. No `supports_cache` bool (avoids GORM default:true gotcha; presence of cached price IS the capability signal).
5. **Seed targets the EXACT live `(provider, model)` flat rows discovered by querying the DB (P0 #1), not assumed names; cached price is computed as a RATIO of the row's own stored price (0.1× DeepSeek / 0.5× GPT) so it is correct whatever the base is and can never silently mis-price.** Rows: dmxapi `deepseek-v3-2-251201` (¥1.00→0.10), volc-ark `deepseek-v3-2-251201` (optional), dmxapi+aihubmix `deepseek-v4-pro` (14→1.40), dmxapi `DeepSeek-V3.2`/`DeepSeek-V3.2-Thinking` (2.16→0.2160), aihubmix `deepseek-v3.2`/`deepseek-v3.2-thinking` (2.16→0.2160), dmxapi `gpt-5.4` (10→5.00). aihubmix `gpt-5.4`(+thinking) tiered + `gpt-5.5` deferred (NULL→full price→zero regression). Cost+sell columns set as a PAIR (P1 #5); post-apply COUNT check proves the WHERE matched real rows (not 0).
6. Clamp cachedTokens to [0, promptTokens] on both cost and revenue (defends a misbehaving provider reporting cached > prompt).
7. **Langfuse dual-channel (P0 #2): channel A `WithGenCachedUsage` (typed usage field, forward-compatible) AND channel B `output.metadata.cached_input_tokens` (guaranteed visible on the deployed Langfuse v3); both gated on cache>0 so non-cache events stay byte-identical. Deployed Langfuse v3's support for the channel-A `cached_input` field is unconfirmed → channel B is the load-bearing visibility guarantee.**
8. **Legacy SSE-parse path made cache-aware (P0 #3): `billing.TokenUsage` mirrors the `CompletionTokensDetails` nested pattern with `PromptTokensDetails.cached_tokens` + native `prompt_cache_hit_tokens`, flattened in `Normalize()`; `tempUsage.Normalize()` added at the 3 inline executor sites. Gateway path is load-bearing for Batch A in prod; this is defense-in-depth (and the correct fix if a node is pointed directly at DMXAPI).**
9. **Salesrag emit→parse→thread chain fixed (P1 #6): `cached_prompt_tokens` added to the internal `usage` event payload, parsed into `streamCachedTokens`, threaded through `recordLLMResult` to `CalculateCostWithCache`. Without this the field was dropped at the in-process event hop.**
10. Prefix-stability: SOP/chatbot test-only; agent `ListAllTools` deterministic sort by `Name()` (== `ToolName`, the LLM-facing identifier, verified P1 #7) is the single real fix; both consumers inherit it.
