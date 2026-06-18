# Native Provider Cache Adapters — Spec + Task Plan (S0–S3)

- **Feature id**: `native-cache-adapters`
- **Track**: Standard
- **Repo**: numind-server
- **Branch**: `feature/native-cache-adapters`
- **Worktree**: `/private/tmp/wt-native-cache-adapters-numind-server`
- **Stage at write time**: S3 (spec + plan ready; S4 implementation pending)

All file paths below are relative to the worktree root unless noted absolute.

---

## 1. Goal

Add a **native provider adapter** capability so Claude and Gemini calls can be issued in each
provider's *native* request format (Anthropic `/v1/messages`, Gemini `:generateContent`) in
order to ACTUALLY obtain prompt-cache hits and the cache token counts needed for billing —
which the current OpenAI-compatible `/chat/completions` path cannot surface. Plus an **internal
on/off toggle** for caching (controlled in our system, never exposed to the frontend), because
Claude cache *creation* costs a premium and is not worth it for every task.

### Why native is required (proven this session)
- DMXAPI Claude native = `POST https://www.dmxapi.cn/v1/messages` (Anthropic Messages format).
  `cache_control:{"type":"ephemeral"}` on a system block → response `usage` carries
  `cache_creation_input_tokens` + `cache_read_input_tokens` + `cache_creation:{ephemeral_5m_input_tokens, ephemeral_1h_input_tokens}`.
  TTL 5 min, min ~1024 tokens. **Proven 100% read hit** (call2 `cache_read=2763/2763`).
  Header `anthropic-version: 2023-06-01`.
- DMXAPI Gemini native = `POST https://www.dmxapi.cn/v1beta/models/{model}:generateContent?key={KEY}`
  (and `:streamGenerateContent?key=...&alt=sse` for streaming). **Implicit** caching is automatic
  on 2.5+; response `usageMetadata.cachedContentTokenCount`. **Proven 97% hit**
  (call2 `cachedContentTokenCount=6122/6308`). Explicit `cachedContents` API is 404 on DMXAPI →
  implicit only. Auth is `?key=` query param, **NOT** Bearer.
- The OpenAI-compat `/chat/completions` path does NOT surface Claude/Gemini cache. That is exactly
  why native adapter paths are needed.
- GPT/OpenAI cache is NOT achievable via DMXAPI (per-org cache × aggregator account rotation) →
  **out of scope**.

---

## 2. Scope and Zero-Regression Contract

### In scope
1. Native-adapter framework: how Claude-native & Gemini-native adapters register + get routed per `ai_service_route`.
2. Claude-native adapter (chat + streaming + tool-calling + `cache_control`).
3. Gemini-native adapter (chat + streaming + tool-calling + implicit cache).
4. Cache billing: cache-write (creation) price column for Claude + read price; map native cache tokens into a 3-bucket cost formula.
5. The internal toggle (policy home, caller signal, gating of `cache_control`).
6. Routing/seed config to point specific Claude/Gemini routes at the native adapters + seed their cache prices.

### Out of scope
- GPT caching (impossible via DMXAPI).
- Any frontend change.
- `#2` context windowing.
- Editing `config_prod.yaml` (prod config handled by ops out-of-band).

### PRIME DIRECTIVE — ZERO REGRESSION
Every model/flow currently working through the OpenAI-compat dmxapi adapter (DeepSeek default,
all chatbot/SOP/agent/salesrag) MUST be byte-identical after this. The native adapters are
ADDITIVE and OPT-IN per route — nothing routes to them unless a route's `llm_provider.name`
explicitly points at them. Caching toggle OFF or price NULL ⇒ behaves exactly like today.

The zero-regression invariant is enforced at FIVE independent layers, each defaulting to "off/today":
1. **Routing**: nothing reaches a native adapter unless an admin creates a route whose
   `llm_provider.name` matches the native adapter `Name()`. Every existing route keeps
   `provider.name='dmxapi'` → still resolves to the dmxapi OAI-compat adapter.
2. **Token field default 0**: `TokenUsage.CacheCreationTokens` defaults 0 and is set ONLY by the
   native Claude adapter. The OAI-compat adapter never touches it.
3. **Price column NULL**: `cache_creation_input_price_per_m_tok` NULL ⇒ creation tokens billed at
   full input price (no overcharge, no premium).
4. **Cost formula collapse**: `CalculateCostWithCacheRW(..., cacheWriteTokens=0)` is byte-identical to
   the existing `CalculateCostWithCache(..., cachedTokens)`; the existing 3-arg signature is preserved.
5. **Toggle default off**: no `cache_control` emitted unless all toggle layers agree.

---

## 3. Ground-Truth File:Line Map (verified in worktree)

### Gateway / routing
- `internal/pkg/aiservice/gateway.go:52-56` — `Provider` interface (`Name`/`ProviderType`/`Capabilities`).
- `internal/pkg/aiservice/gateway.go:59-62` — `ChatProvider` interface (`Chat`/`ChatStream`).
- `internal/pkg/aiservice/gateway.go:120` — `providers map[string]Provider` keyed by `Provider.Name()`.
- `internal/pkg/aiservice/gateway.go:154-158` — `RegisterProvider`.
- `internal/pkg/aiservice/gateway.go:163-169` — `RegisterProviderAlias`.
- `internal/pkg/aiservice/gateway.go:174-185` — `findAdapterByPrefix`: matches when a registered
  adapter name is a **strict prefix** of `providerName` (`len(providerName) > len(name)`), else
  **falls back to the `dmxapi` adapter**. THIS is the zero-regression lever AND the naming hazard.
- `internal/pkg/aiservice/gateway.go:266-274` — `lookupProvider`: **exact map hit FIRST** (`g.providers[name]`),
  only on miss calls `findAdapterByPrefix`. So an exact-registered native adapter beats the prefix fallback.
- `internal/pkg/aiservice/gateway.go:202-265` — `resolveAndRun`: resolves provider TWICE (fail-fast on
  primary + AGAIN inside handler closure via `lookupProvider(r.Provider.Name)`), which is what lets
  cross-provider fallback dispatch to each route's OWN adapter.
- `internal/pkg/aiservice/gateway.go:281-305` — `Chat`; `:308-365` — `ChatStream`.
- `internal/numind/numind.go:200-213` — adapter registration loop + aliases (`dmxapi-ssvip`, `aihubmix`).

### Adapter contract
- `internal/pkg/aiservice/adapter/adapter.go:42-52` — base `Adapter`.
- `internal/pkg/aiservice/adapter/adapter.go:59-68` — `ChatAdapter`.
- `internal/pkg/aiservice/adapter/adapter.go:414-472` — `buildOAIMessages` (OAI-shaped, NOT reused by native).
- `internal/pkg/aiservice/adapter/adapter.go:480-496` — `buildOAITools` (OAI-shaped).
- `internal/pkg/aiservice/adapter/adapter.go:501-517` — `extractToolCalls` (OAI-shaped).
- `internal/pkg/aiservice/adapter/stream.go:45+` — `runOAIStream` (idle watchdog, 1MB scanner buffer,
  `data: ` lines, `[DONE]` terminator, single terminal `IsFinal` chunk). Native streams write siblings.
- `internal/pkg/aiservice/adapter/dmxapi.go:77-114` — two-client split: `client` (LLMConfig 600s) +
  `streamClient` (LLMStreamConfig, NO total timeout — long-thinking-stream incident fix).
- `internal/pkg/aiservice/adapter/dmxapi.go:549-574` — `doPost` (injects `Authorization: Bearer`).
- `internal/pkg/aiservice/adapter/dmxapi.go:583-609` — `doStream` (injects `Authorization: Bearer`).

### Types
- `internal/pkg/aiservice/types.go:122-135` — `aiservice.TokenUsage` (has `CachedPromptTokens`, omitempty).
- `internal/pkg/aiservice/types.go:157-192` — `ChatRequest` (ends with `Thinking bool json:"thinking"` — NO omitempty).
- `internal/pkg/aiservice/types.go:194-214` — `ChatResponse`; `:238-287` — `ChatChunk`.
- `internal/pkg/billing/types.go:14-44` — `billing.TokenUsage` (has `CachedPromptTokens`, NO omitempty); `:50-64` — `Normalize()`.

### Routing schema mirror
- `internal/pkg/aiservice/registry/registry.go:51-72` — `ResolvedRoute` (has `SupportsThinking`/`ThinkingOnly`/`ThinkingStyle`).
- `internal/pkg/aiservice/registry/registry.go:458-485` — `buildResolvedRoute`.
- `internal/pkg/aiservice/registry/store.go:93-113` — `resolvedRouteRow`.
- `internal/pkg/aiservice/registry/store.go:317-340, 416-439, 513-535` — THREE raw JOIN SELECTs
  (`GetResolvedRoute`, `GetResolvedRouteByModelKey`, `ListResolvedRoutesByModel`), each with its own
  SELECT column list + inline rawRow struct + struct-copy mapping (lines ~347, ~445, ~540).
- `internal/pkg/model/ai_service.go:86-115` — `AIService` model (`ThinkingStyle` at :106, `TableName()` at :115).

### Billing
- `internal/pkg/model/billing.go:6-21` — `UsageRecord` (`CachedPromptTokens` at :21).
- `internal/pkg/model/billing.go:64-95` — `PricingRule` (`CachedInputPricePerMTok` *float64 :81,
  `SellCachedInputPricePerMTok` *float64 :85).
- `internal/pkg/pricing/pricing.go:59-97` — `ICalculator`.
- `internal/pkg/pricing/pricing.go:144-148` — `CalculateCost` delegates to `CalculateCostWithCache(...,0)`.
- `internal/pkg/pricing/pricing.go:166-214` — `CalculateCostWithCache` (flat-mode body :194-201, tiered :186-192).
- `internal/pkg/aiservice/middleware/billing.go:104-121` (stream copy) — `record.CachedPromptTokens = chunk.Usage.CachedPromptTokens`.
- `internal/pkg/aiservice/middleware/billing.go:487-499` (non-stream copy) — `r.CachedPromptTokens = chatResp.Usage.CachedPromptTokens`.
- `internal/pkg/aiservice/middleware/billing.go:553-569` — `publishCostToHolder` → `CalculateCostWithCache(...)`.
- `internal/pkg/billing/recorder.go:244-296` — `buildRecord` (Prebuilt path + non-prebuilt `record.CachedPromptTokens = event.Usage.CachedPromptTokens` :296).
- `internal/pkg/billing/recorder.go:367-382` — `computeCost` → `CalculateCostWithCache(...)`.
- `internal/pkg/billing/recorder.go:411-453` — `computeRevenue` (flat sell block :429-445, uses `SellCachedInputPricePerMTok`).

### Caller sites (toggle opt-in)
- `internal/numind/biz/sop/executor.go:573-593` — SOP builds `ContextFragments` (RoleImmutable system/node = stable cacheable prefix), then `ChatStream`. NOTE: SOP uses `ContextFragments`, rendered to `req.Messages` by middleware (`context_budget.go:495`) BEFORE the adapter sees it.
- `internal/numind/biz/agent/adapter.go:113-163` — `aiserviceAdapter` (immutable fields incl. `maxOutputTokens`, preserved across `WithTools`); `:275-330` — `convertToAiserviceRequest` (system prompt prepended as `messages[0]`).
- `internal/numind/biz/chatbot/stream.go:448-470` — chatbot single-shot (leave toggle OFF).
- `internal/numind/biz/salesrag/salesrag.go` (5 sites) — mostly single-shot (leave OFF).

### Migration templates (idempotent, MySQL 8.4)
- `migrations/20260609_121500_add_cached_input_price.sql` — information_schema + PREPARE column-add pattern (NO `ADD COLUMN IF NOT EXISTS`).
- `migrations/20260609_121500_seed_cached_input_pricing.sql` — idempotent `WHERE ... IS NULL` data UPDATE pattern.
- `migrations/20260609_121500_add_cached_prompt_tokens_to_usage_record.sql` — usage_record column-add pattern.

---

## 4. DECISIONS (resolved, with rationale)

### D1 — Routing: NEW `llm_provider` rows named exactly `claude-native` / `gemini-native` (NOT a route adapter column)
Use new `llm_provider` rows whose `name` matches the native adapter `Name()` exactly. The gateway
already keys on `route.Provider.Name`; an opt-in route is created by pointing a chosen
`ai_service_route` at the native provider. This needs **ZERO** gateway/registry/store code changes
for adapter selection. Rejected: a route-level `native_format` column would require touching
`ResolvedRoute` + `resolvedRouteRow` + 3 JOIN SELECTs + a new branch in `lookupProvider` — strictly
more code and more regression surface.

**Naming hazard mitigation**: names MUST NOT prefix-collide with `dmxapi`. `dmxapi` (6 chars) IS a
strict prefix of `dmxapi-claude` (13 chars), so a name like `dmxapi-claude` would prefix-fall-back
to the dmxapi adapter IF the native adapter were ever un-registered. Use `claude-native` /
`gemini-native` (which `dmxapi` is NOT a prefix of) AND register them exactly. `lookupProvider`'s
exact-first lookup makes registered native adapters win regardless; the non-colliding name is
belt-and-suspenders. A compile-time `var _ aiservice.ChatProvider = (*ClaudeNativeAdapter)(nil)` guard
prevents a missing-registration silent degrade.

**P0 mitigation — startup registration assertion + two-step activation (resolves review finding #1).**
The compile-time guard only proves the struct *satisfies the interface*; it does NOT prove the
adapter was actually `RegisterProvider`-ed into the running gateway. There remains a TOCTOU window:
the T8 migration inserts permanent `llm_provider` rows, but the binary deploy is a SEPARATE,
non-atomic step. If a route points at `claude-native` while the deployed binary lacks the adapter
in its provider map, `lookupProvider("claude-native")` misses the exact map, `findAdapterByPrefix`
fails to prefix-match, and the **hard-coded `dmxapi` fallback at `gateway.go:181` silently routes an
Anthropic-format body to `/chat/completions`** → malformed 400 or silent partial response with lost
cache tokens. Two defenses, BOTH required:

1. **Startup assertion (NEW `assertNativeAdaptersRegistered`)** — in `numind.go` AFTER the
   `RegisterProvider` loop and `SetDefault(gateway)` (i.e. after `numind.go:215`, BEFORE
   `SyncProviderCredentials` at `:219`): query `llm_provider` for any row whose `name` is one of the
   known native names (`claude-native`, `gemini-native`) **with `is_active = true`**, and for each
   such row assert `gateway.lookupProviderExact(name) != nil` (a new exported helper that does ONLY
   the exact-map lookup, NO prefix fallback — so the dmxapi fallback can never mask the gap). If any
   active native provider row exists in the DB but its adapter is NOT registered ⇒ **`log.Fatalw` and
   refuse to start** (panic-at-boot is correct: a half-deployed binary serving Anthropic bodies to
   the OAI endpoint is worse than downtime). When NO native rows are active (the default state, and
   the state during the deploy-before-activate window), the assertion is a no-op ⇒ zero impact on
   every existing deploy. Add `gateway.KnownNativeProviderNames()` returning the static list so the
   assertion and tests share one source of truth.
2. **Two-step DB activation** — the T8 `add_native_provider_rows.sql` migration MUST insert the
   `claude-native`/`gemini-native` rows with **`is_active = 0`** and MUST NOT repoint any
   `ai_service_route` in the same statement that creates them. Activation (flip `is_active = 1` AND
   repoint the chosen route) is a SECOND, manual admin operation run ONLY after the new binary is
   confirmed healthy (`/healthz/ai` shows both `claude-native` and `gemini-native` in `AdapterNames()`).
   This guarantees the binary-with-adapters is live before any route can resolve to a native name, and
   the startup assertion (which checks `is_active = true` rows) stays a no-op until that deliberate flip.
   The migration file header documents this two-step sequence as a runbook.

### D2 — One shared `native` sub-package vs two adapters: TWO adapter structs, ONE package (`adapter/`), shared native helpers in a new `adapter/native_*.go`
Both adapters live in the existing `internal/pkg/aiservice/adapter/` package (so they can reuse the
two-client http pattern, idle watchdog, `aierr` wrappers, and `InferModelFamily`). They are SEPARATE
structs (`ClaudeNativeAdapter`, `GeminiNativeAdapter`) in SEPARATE files (`anthropic_native.go`,
`gemini_native.go`) because their wire formats, auth, and streaming SSE grammars are fundamentally
different. Shared scaffolding (token-usage normalization helper, a key-redacting URL logger) goes in
a small `native_shared.go`. Rejected a single mega-adapter (the two providers share almost no wire code).

### D3 — Cache billing model: 3-bucket additive, NEW `CalculateCostWithCacheRW` method (do NOT mutate the 3-arg signature)
Claude returns THREE disjoint token buckets: `input_tokens` (uncached), `cache_read_input_tokens`
(read at ~0.1×), `cache_creation_input_tokens` (write at a PREMIUM — opus ~1.84×, sonnet ~1.25×
over input). Creation is a PREMIUM, not a discount, so it CANNOT reuse `CachedPromptTokens` (which
the existing formula clamps and bills at a discount → would silently UNDER-bill).

Add a NEW method `CalculateCostWithCacheRW(ctx, serviceType, provider, model, prompt, completion,
cachedTokens, cacheWriteTokens)` on `ICalculator`. Refactor so the EXISTING `CalculateCostWithCache`
delegates to `...RW(..., cacheWriteTokens=0)` — formula stays single-source (exact same discipline
as `CalculateCost` → `CalculateCostWithCache` today). The existing 3-arg signature is UNCHANGED so
all current callers stay byte-identical.

New flat-mode formula (replaces pricing.go:194-201 body, moved into `...RW`):
```
cw := clamp(cacheWriteTokens, 0, promptTokens)        // carve write FIRST
cr := clamp(cachedTokens, 0, promptTokens - cw)       // then read, so read+write never exceed prompt
normal := promptTokens - cw - cr
writePrice  = rule.CacheCreationInputPricePerMTok  if non-nil else rule.InputPricePerMTok
cachedPrice = rule.CachedInputPricePerMTok         if non-nil else rule.InputPricePerMTok
costYuan = cw/1e6*writePrice + cr/1e6*cachedPrice + normal/1e6*InputPricePerMTok + completion/1e6*OutputPricePerMTok
costCents = round(costYuan * CreditMultiplier * 100)
```
Tiered mode UNCHANGED (ignores both cache buckets).

**Revenue side MUST mirror the 3-bucket carve (resolves review finding #2 — P0 sell-side undercount).**
`recorder.computeRevenue` (recorder.go:411-453) today carves ONLY the read bucket
(`cached`/`nonCached`). If `UsageRecord.CacheCreationTokens` is populated by the Claude adapter but
`computeRevenue` is NOT updated, every creation token is billed to the USER at the full
`SellInputPricePerMTok` — i.e. NO creation premium on the revenue side ⇒ silent revenue undercount on
exactly the calls that cost us the most. The flat block (recorder.go:436-443) MUST be rewritten to a
3-bucket carve symmetric with cost:
```
cw := clamp(record.CacheCreationTokens, 0, record.PromptTokens)          // carve write FIRST
cr := clamp(record.CachedPromptTokens,  0, record.PromptTokens - cw)     // then read
nonCached := record.PromptTokens - cw - cr
cachedWriteSell = rule.SellCacheCreationInputPricePerMTok if non-nil else rule.SellInputPricePerMTok
cachedReadSell  = rule.SellCachedInputPricePerMTok        if non-nil else rule.SellInputPricePerMTok
revenueYuan = cw/1e6*cachedWriteSell + cr/1e6*cachedReadSell
            + nonCached/1e6*rule.SellInputPricePerMTok
            + completion/1e6*rule.SellOutputPricePerMTok
```
Zero-regression: `CacheCreationTokens==0` (every non-Claude call) ⇒ `cw=0` ⇒ collapses to the current
read-only carve byte-identically; `SellCacheCreationInputPricePerMTok==nil` ⇒ `cachedWriteSell ==
SellInputPricePerMTok` ⇒ write bucket priced exactly as it is today. This carve MUST use the SAME
write-FIRST ordering as cost so `nonCached` can never go negative. The `SellCacheCreationInputPricePerMTok
*float64` column on `PricingRule` (added in §5D) is the source.

Zero-regression proof:
- `cw==0` (every non-Claude call) → write term 0, `normal = prompt - cr` → collapses to the current
  Batch-A 2-bucket formula exactly.
- `CacheCreationInputPricePerMTok==nil` (every non-Claude / un-seeded row) → `writePrice ==
  InputPricePerMTok` → `cw+cr+normal` all priced at input where applicable → sum invariant holds.

### D4 — Anthropic PromptTokens normalization: `PromptTokens = input_tokens + cache_read + cache_creation`
Anthropic reports the three buckets DISJOINT (`input_tokens` is ONLY the non-cached tail). The
3-bucket formula carves `cw + cr` OUT of `PromptTokens`, so the Claude adapter MUST set
`PromptTokens = input_tokens + cache_read_input_tokens + cache_creation_input_tokens`,
`CachedPromptTokens = cache_read_input_tokens`, `CacheCreationTokens = cache_creation_input_tokens`.
Otherwise cost double-discounts or under-bills. Adapter-local arithmetic; covered by a unit test
asserting `PromptTokens == cw + cr + normal`.

### D5 — Gemini token semantics: `promptTokenCount` ALREADY INCLUDES `cachedContentTokenCount`
Gemini's `promptTokenCount` is the full input; `cachedContentTokenCount` is a subset of it (same
semantics as Batch-A `CachedPromptTokens`). So Gemini maps `PromptTokens = promptTokenCount`,
`CachedPromptTokens = cachedContentTokenCount`, `CacheCreationTokens = 0` (implicit creation billed
at standard input price, no premium per Google). Do NOT add cached to prompt for Gemini (would
double-count). This per-provider divergence is encoded per-adapter, NOT in a shared helper.

### D6 — Internal toggle: THREE-layer AND gate, default-deny; policy on `ai_service` + global flag + per-call `ChatRequest` field
- **Layer 1 (global kill-switch)**: viper `features.provider_prompt_cache.enabled` (absent ⇒ false),
  read in the native adapter. Operator panic button.
- **Layer 2 (per-model DB policy)**: new column `ai_service.prompt_cache_policy VARCHAR(16) DEFAULT 'off'`,
  enum `off | claude_ephemeral | gemini_implicit | auto`. Lives next to `thinking_style`; mirrored
  onto `ResolvedRoute` via the SAME pattern the thinking columns already follow. Caching economics are
  a property of the MODEL, so the durable knob lives on `ai_service`.
- **Layer 3 (per-call intent)**: new field `aiservice.ChatRequest.EnablePromptCache bool` (json
  `enable_prompt_cache`, NO omitempty — matches `Thinking` so traces record explicit false). Carries
  the "this specific call has a reused prefix" signal. Set true by SOP + agent (R≥2 reuse); left false
  by chatbot/salesrag single-shot.

Decision function inside the native Claude adapter (the single gating point):
```
globalOn        = viper.GetBool("features.provider_prompt_cache.enabled")
useClaudeCache  = globalOn && route.PromptCachePolicy ∈ {claude_ephemeral, auto} && req.EnablePromptCache
useGeminiNative = globalOn && route.PromptCachePolicy ∈ {gemini_implicit, auto}  // implicit; req flag advisory
```
- Claude: ALL three layers must agree (creation premium → caller asserts reuse). False ⇒ omit
  `cache_control` entirely ⇒ wire-identical to a non-cached Claude call.
- Gemini: implicit cache is automatic and cannot be disabled server-side, so the toggle = whether to
  ROUTE to the native adapter at all (policy=off ⇒ model stays on dmxapi OAI-compat ⇒ today's behavior).
  `EnablePromptCache` is advisory for Gemini (creation has no premium).

**NEVER frontend-exposed**: `ChatRequest` is an internal type never bound from HTTP; the policy column
is admin-DB only; the global flag is config only. No path from any user request can flip caching.

### D7 — Streaming Fallback/Retry interaction: native adapters participate AUTOMATICALLY, with two cautions
The Fallback middleware (`middleware/fallback.go`) re-resolves each cascade route to its OWN adapter
via the gateway's execution-time `lookupProvider(r.Provider.Name)` (gateway.go:287, 354/362). So:
- A primary native route that fails cascades to its same-model alternate-provider routes, each
  dispatched to THEIR own adapter (could be dmxapi OAI-compat) — correct and automatic.
- Caution A: the native Claude `ChatStream` MUST emit exactly ONE terminal `IsFinal` chunk and
  always `close(ch)` (the Fallback streaming cascade keys on the first-content-chunk / terminal-Err
  contract). Reuse the `runOAIStream` terminal-chunk discipline in `runAnthropicStream`/`runGeminiStream`.
- Caution B: native adapters MUST use the `streamClient` (LLMStreamConfig, no total timeout) for
  streaming, else Claude thinking streams die at 600s (prod incident). The Retry middleware's
  same-provider stream retry only re-attempts before first content; native adapters do not special-case it.

### D8 — Min-1024-token economic guard lives in the Claude adapter (cheap), not biz
The Claude adapter attaches `cache_control` to the stable prefix (last system block) only when the
toggle is ON AND a cheap char-based heuristic estimates the prefix ≥ ~1024 tokens (≈ 3000 chars CJK-aware).
Below that, Anthropic silently no-ops the cache but the FIRST call still pays the creation premium →
pure loss. Adapter-local guard avoids the loss; operator toggle remains the primary control.

### D9 — Persist creation tokens on `usage_record`
Add `usage_record.cache_creation_tokens INT DEFAULT 0` so B2B billing reconciliation and audit can see
the creation split, mirroring `cached_prompt_tokens`. `int default:0` is NOT subject to the
`default:true` bool GORM gotcha (database.md §6). Cost is already captured via the calculator, but the
token column is needed for the b2b-billing-report audit trail and per-call observability.

---

## 5. Per-Area Design

### 5A. Native-adapter framework (registration + per-route routing)
- New files `internal/pkg/aiservice/adapter/anthropic_native.go`, `gemini_native.go`, `native_shared.go`.
- Each adapter implements `Adapter` + `ChatProvider`/`ChatAdapter`: `Name()`, `ProviderType()`,
  `Capabilities() []string{"chat"}`, `Chat`, `ChatStream`.
  - `ClaudeNativeAdapter.Name() = "claude-native"`, `ProviderType() = "anthropic"`.
  - `GeminiNativeAdapter.Name() = "gemini-native"`, `ProviderType() = "gemini"`.
- Constructors `NewClaudeNativeAdapter()` / `NewGeminiNativeAdapter()` build the two-client http split
  (`httpclient.NewClient(httpclient.LLMConfig())` + `...LLMStreamConfig()`), copied from
  `dmxapi.go:105-114`.
- Compile-time guards at file bottom:
  `var _ aiservice.ChatProvider = (*ClaudeNativeAdapter)(nil)` and `var _ adapter.ChatAdapter = ...`.
- Register at `internal/numind/numind.go:201-210` — append both constructors to the `[]aiservice.Provider`
  slice. NO alias needed (provider rows reference the exact `Name()`).
- **Startup registration assertion (D1 finding #1 mitigation)**: add two gateway helpers and one
  numind.go call:
  - `gateway.KnownNativeProviderNames() []string` → static `["claude-native","gemini-native"]` (single
    source of truth shared by the assertion and tests).
  - `gateway.lookupProviderExact(name) Provider` (or reuse the exact-map portion of `lookupProvider`
    without the `findAdapterByPrefix` fallback) → returns nil when not registered, so the dmxapi
    fallback can NEVER mask a missing native adapter.
  - new `assertNativeAdaptersRegistered(g, db)` called in numind.go AFTER `SetDefault(gateway)`
    (after :215, before `SyncProviderCredentials` :219): for each `KnownNativeProviderNames()` entry that
    has an `is_active=true` row in `llm_provider`, assert `lookupProviderExact(name) != nil`, else
    `log.Fatalw` and refuse to start. No active native rows ⇒ no-op (the default state, so zero impact
    on every existing deploy).
- Routing (two-step, finding #1): STEP 1 the T8 migration inserts `llm_provider` rows
  `claude-native` / `gemini-native` (base `https://www.dmxapi.cn`, same DMXAPI key) with `is_active=0`;
  STEP 2 deploy the binary; STEP 3 verify `/healthz/ai`; STEP 4 a manual admin op flips `is_active=1`,
  repoints the chosen `ai_service_route`, and sets `prompt_cache_policy`. Until STEP 4, NOTHING changes
  and the startup assertion is a no-op.

### 5B. Claude-native adapter (`anthropic_native.go`)
**Endpoint/auth**: `POST {route.Provider.BaseURL}/v1/messages`. Headers: `content-type: application/json`,
`x-api-key: {route.Provider.APIKey}` (NOT Authorization Bearer — DMXAPI uses x-api-key on this path),
`anthropic-version: 2023-06-01`. Streaming = same path with `"stream": true` in body.

**Request body** (`buildAnthropicRequest(route, req, cacheOn bool)`):
- `model` = `route.ProviderModelID`.
- `max_tokens` = `req.MaxTokens` (REQUIRED by Anthropic; gateway pre-fills from capability when 0).
- `temperature` = `req.Temperature` when > 0 (omit when 0). When thinking enabled, force `temperature=1`
  (mirror dmxapi Claude+thinking rule).
- `system`: ARRAY of text blocks. Concatenate all `Messages` with `Role==system`. When `cacheOn` AND the
  concatenated text passes the ~1024-token heuristic, add `cache_control:{"type":"ephemeral"}` to the
  LAST system block.
- `messages`: map every non-system `ChatMessage` to `{role: "user"|"assistant", content: [...blocks]}`:
  - text part → `{"type":"text","text":t}`
  - image_url part → `{"type":"image","source":{...}}` (base64 data URI → split media_type+data; plain
    URL → `{"type":"url","url":u}`).
  - assistant `ToolCalls` → one `{"type":"tool_use","id":tc.ID,"name":tc.Function.Name,"input":<json.Unmarshal(tc.Function.Arguments)>}` block per call (Anthropic input is an OBJECT).
  - role=tool result → fold into a `user` turn as `{"type":"tool_result","tool_use_id":msg.ToolCallID,"content":msg.text}`. Merge consecutive tool messages into ONE user turn's content array (Anthropic ordering: tool_result must immediately follow the assistant tool_use turn).
- `tools`: `[{"name":n,"description":d,"input_schema":<Tool.Function.Parameters>}]` (Anthropic uses
  `input_schema`, not `function.parameters`). When `cacheOn`, optionally add `cache_control` to the LAST
  tool (Phase 1: system-block-only is sufficient — keep tool cache_control off unless needed).

**Non-stream response parse**:
- `Content` = concat of all `content[].text` where `type=="text"`.
- `ToolCalls` = per `type=="tool_use"`: `{ID:block.id, Type:"function", Function:{Name:block.name, Arguments:string(json.Marshal(block.input))}}` (re-serialize input OBJECT back to OAI Arguments STRING).
- `FinishReason` map: `end_turn`→`stop`, `max_tokens`→`length`, `tool_use`→`tool_calls`, `stop_sequence`→`stop`.
- `TokenUsage.PromptTokens = input_tokens + cache_read_input_tokens + cache_creation_input_tokens` (D4).
- `TokenUsage.CompletionTokens = output_tokens`; `TotalTokens = Prompt+Completion`.
- `TokenUsage.CachedPromptTokens = cache_read_input_tokens`.
- `TokenUsage.CacheCreationTokens = cache_creation_input_tokens`.

**Streaming (`runAnthropicStream`)** — SSE event grammar. **Token-usage capture is DEFENSIVE
(resolves review finding #3 — P0). Do NOT assume each field appears in exactly one event.** Maintain
running accumulators (`capPrompt, capCreation, capRead int` for the prompt-side buckets and
`capOutput int` for completion) and apply `max()` semantics on every chunk that carries a `usage`
object — the largest non-zero value wins. This survives both the standard protocol (prompt fields at
`message_start`, output at `message_delta`) AND DMXAPI proxy wrappers that re-send or correct usage in
later chunks.
1. `message_start` → `usage.input_tokens / cache_creation_input_tokens / cache_read_input_tokens` are
   normally present HERE; capture each via `max(cap*, value)` (do NOT overwrite a non-zero accumulator
   with a 0 from a later chunk).
2. `content_block_start` → text or tool_use (capture tool id+name at index).
3. `content_block_delta` → `text_delta`→`Delta`; `input_json_delta.partial_json`→ACCUMULATE per index into
   tool Arguments (keep as concatenated JSON string, do NOT re-parse); `thinking_delta`→`ReasoningDelta`;
   `signature_delta`→ignore.
4. `content_block_stop` → close block.
5. `message_delta` → capture `delta.stop_reason`. `usage.output_tokens` here is the message total in
   standard Anthropic API (a single final value), but DMXAPI MAY differ — so apply `capOutput =
   max(capOutput, usage.output_tokens)` (last-largest), and ALSO `max()`-fold any prompt-side fields
   if a proxy echoes them here. Do NOT sum across `message_delta` events.
6. `message_stop` → end. Ignore `ping`; `error` event → terminal chunk with `Err` wrapping `aierr`.
- **Post-stream validation**: after the stream completes (message_stop or terminal error), if
  `capPrompt == 0 && capCreation == 0 && capRead == 0` (no prompt-side usage ever observed), log a
  WARN with the request model + trace id, and set `PromptTokens = 0` EXPLICITLY (do not leave the
  Usage pointer nil and do not let downstream billing infer a non-zero figure). A 0-token prompt then
  bills at 0 — never at full price on phantom tokens.
- Terminal `IsFinal=true` chunk carries mapped FinishReason, assembled ToolCalls, and Usage assembled
  via the SAME `PromptTokens = capPrompt + capCreation + capRead` formula (D4),
  `CompletionTokens = capOutput`, `CachedPromptTokens = capRead`, `CacheCreationTokens = capCreation`.
  Reuse idle-watchdog + 1MB scanner + single-terminal-chunk pattern from `stream.go`. Use
  `streamClient` (no total timeout). The streaming-replay unit test (T5) MUST include a transcript
  where a corrected/duplicated usage chunk arrives after `message_start` to lock the `max()` semantics,
  and a transcript with NO usage to lock the post-stream `PromptTokens=0` path.

### 5C. Gemini-native adapter (`gemini_native.go`)
**Endpoint/auth**: non-stream `POST {BaseURL}/v1beta/models/{model}:generateContent?key={APIKey}`;
stream `:streamGenerateContent?key={APIKey}&alt=sse`. Auth is the `?key=` QUERY param, NOT Bearer
(proven: DMXAPI Gemini rejects Bearer, requires `?key=`). `model` is `route.ProviderModelID` in the
PATH. Build URL+request manually (do NOT reuse `doPost`/`doStream`, which inject Bearer and would log
the full URL).

**Key-in-URL redaction mechanism (resolves review finding #4 — P0 key leak).** The existing
`httpclient`/`aierr` error path embeds the request URL in the error string, which on the Gemini path
would carry the live `?key=<APIKey>`, leaking it into error logs AND Langfuse traces. The adapter MUST
construct TWO URLs and never let the real key reach an error/log surface:
- `fullURL` — `{BaseURL}/v1beta/models/{model}:{method}?key={APIKey}[&alt=sse]`, used ONLY as the
  argument to `http.NewRequestWithContext` (the actual wire call).
- `redactedURL` — identical but with the key replaced by the literal `REDACTED`
  (`?key=REDACTED`), used in EVERY error-wrap, log line, and any Langfuse span/generation URL field.
A shared helper `redactGeminiURL(u string) string` (in `native_shared.go`) does a regex/`url.Values`
replacement of the `key` query param → `REDACTED`. The adapter wraps HTTP errors with
`aierr.Wrap(err, redactedURL)` (NEVER `fullURL`). A T6 unit test asserts that, given a `fullURL`
containing a fake key, `redactGeminiURL` returns a string that does NOT contain the key and DOES
contain `key=REDACTED`, and that a forced HTTP error surfaces only the redacted form. Implicit cache
only — do NOT send `cachedContent` (DMXAPI 404s the explicit `cachedContents` API).

**Request body**:
- `systemInstruction`: `{"parts":[{"text":<concat system text>}]}`.
- `contents`: `[{role:"user"|"model", parts:[...]}]`. Role map: assistant→`model`, user→`user`.
  - text → `{"text":t}`; image → `{"inlineData":{"mimeType":...,"data":base64}}` (reuse imageutil normalization).
  - assistant `ToolCalls` → `{"functionCall":{"name":n,"args":<json.Unmarshal(Arguments)>}}` on a `model` content.
  - role=tool result → `{"functionResponse":{"name":<tool name>,"response":{"result":<text>}}}` on a `user`
    content. Gemini matches by NAME (not id), and our synthesized IDs (`gemini-call-<name><index>`) are
    NOT stable across turns, so the name MUST be recovered **statelessly from the message history**, not
    from an ephemeral per-request map (resolves review finding #6 — P1 cross-turn fragility):
    > For each `role=tool` message, scan the preceding `req.Messages` for the nearest preceding
    > `role=assistant` message and find the `ToolCall` whose `ID == msg.ToolCallID`; use that call's
    > `Function.Name`. This is the same list the adapter is already iterating, so the lookup is a
    > backward scan over `req.Messages` — no shared mutable state, thread-safe, and correct across an
    > arbitrary number of agent turns and out-of-order tool results within a turn (each tool result is
    > matched by its own `ToolCallID`). Fallback if no match (defensive, should not happen in a
    > well-formed loop): use `msg.Name` if the tool message carries one, else emit a WARN and use the
    > literal `"unknown_tool"` (so the request still serializes rather than panicking). A T6 unit test
    > covers a 2-turn transcript with two parallel tool calls resolved out of order, asserting each
    > `functionResponse.name` matches the original `functionCall.name`.
- `tools`: `[{"functionDeclarations":[{"name","description","parameters":<Tool.Function.Parameters>}]}]`.
- `generationConfig`: `{"temperature":t when >0, "maxOutputTokens":req.MaxTokens}`. Thinking:
  `thinkingConfig` only when `route.SupportsThinking && req.Thinking && !route.ThinkingOnly`.

**Non-stream response parse**:
- `Content` = concat `candidates[0].content.parts[].text`.
- `ToolCalls` = per `part.functionCall`: synthesize stable `ID` (e.g. `"gemini-call-"+name+index`),
  `{Type:"function", Function:{Name:fc.name, Arguments:string(json.Marshal(fc.args))}}`.
- `FinishReason` map: `STOP`→`stop`, `MAX_TOKENS`→`length`, any functionCall present→`tool_calls`,
  `SAFETY`/`RECITATION`→`stop`.
- `PromptTokens = usageMetadata.promptTokenCount` (ALREADY includes cached — D5).
- `CompletionTokens = candidatesTokenCount`; `TotalTokens = totalTokenCount`; `ReasoningTokens = thoughtsTokenCount`.
- `CachedPromptTokens = cachedContentTokenCount`; `CacheCreationTokens = 0`.

**Streaming (`runGeminiStream`)**: `:streamGenerateContent?alt=sse` → `data: {GenerateContentResponse}`
lines. Accumulate `candidates[0].content.parts[].text`→`Delta`; functionCall parts (often whole)→assemble
ToolCalls; `usageMetadata` on the FINAL chunk→terminal Usage. Reuse idle-watchdog + 1MB buffer +
single-terminal-chunk. A single Gemini SSE line can be large multi-part JSON (1MB buffer covers it).

### 5D. Cache billing
- `model.PricingRule` (model/billing.go after :85): add `CacheCreationInputPricePerMTok *float64`
  (gorm `column:cache_creation_input_price_per_m_tok;type:decimal(10,4)`, NULLABLE) +
  `SellCacheCreationInputPricePerMTok *float64` (paired). NULL ⇒ creation billed at full input.
- `aiservice.TokenUsage` (types.go after :134): add `CacheCreationTokens int json:"cache_creation_tokens,omitempty"`.
- `billing.TokenUsage` (billing/types.go after :44): add `CacheCreationTokens int json:"cache_creation_tokens"`
  (NO omitempty, round-trip stable like CachedPromptTokens). **`Normalize()` is a deliberate NO-OP for
  this field (resolves review finding #7 — P1):** unlike `CachedPromptTokens` (which Normalize promotes
  from nested `prompt_tokens_details.cached_tokens` / DeepSeek `prompt_cache_hit_tokens`), Claude reports
  `cache_creation_input_tokens` as a TOP-LEVEL field that the Claude adapter writes directly into
  `aiservice.TokenUsage.CacheCreationTokens`. There is no nested/aliased source to promote, so
  Normalize() must NOT touch it — add a one-line comment in `Normalize()` documenting this intentional
  omission so a future reader does not "fix" it. The value reaches `record.CacheCreationTokens` only via
  the explicit copy sites below, NOT via Normalize.
- `model.UsageRecord` (model/billing.go after :21): add `CacheCreationTokens int gorm:"column:cache_creation_tokens;default:0" json:"cache_creation_tokens"`.
- `pricing.ICalculator`: add `CalculateCostWithCacheRW(...)`; refactor `CalculateCostWithCache` to delegate
  with `cacheWriteTokens=0`. Move flat-mode body into `...RW` with the D3 formula.
- `recorder.computeRevenue` flat block (recorder.go:436-443): replace the read-only carve with the
  3-bucket write+read+nonCached carve from D3 (sell side), using `SellCacheCreationInputPricePerMTok`.
- **Explicit copy-site extension (resolves review finding #7 — P1; all pass 0 today → byte-identical
  until a native Claude route writes non-zero):**
  - middleware/billing.go stream copy (immediately after `record.CachedPromptTokens = chunk.Usage.CachedPromptTokens`
    at :113): add `record.CacheCreationTokens = chunk.Usage.CacheCreationTokens`.
  - middleware/billing.go non-stream copy (immediately after `r.CachedPromptTokens = chatResp.Usage.CachedPromptTokens`
    at :499): add `r.CacheCreationTokens = chatResp.Usage.CacheCreationTokens`.
  - recorder.go:296 (non-prebuilt buildRecord, after `record.CachedPromptTokens = event.Usage.CachedPromptTokens`):
    add `record.CacheCreationTokens = event.Usage.CacheCreationTokens`.
  - middleware/billing.go:568 (publishCostToHolder) + recorder.go:376 (computeCost): switch to
    `CalculateCostWithCacheRW(..., record.CachedPromptTokens, record.CacheCreationTokens)`.
  - NOTE the propagation chain (`aiservice.TokenUsage.CacheCreationTokens` → copied to
    `model.UsageRecord.CacheCreationTokens` at the four sites above) does NOT pass through
    `billing.TokenUsage.Normalize()`; Normalize only operates on the `billing.TokenUsage` JSON-marshalled
    event payload where the field round-trips verbatim. A T2 test asserts a record with
    `CacheCreationTokens>0` arrives at `computeCost`/`computeRevenue` and is billed at the creation premium.

### 5E. Internal toggle
- `internal/pkg/model/ai_service.go:106` (after ThinkingStyle): add
  `PromptCachePolicy string gorm:"size:16;not null;default:'off'" json:"prompt_cache_policy"`.
  **`not null` is required (resolves review finding #8 — P1):** without it, GORM AutoMigrate adds the
  column NULLable, existing rows get NULL, and a NULL/`''` `PromptCachePolicy` reads as "not in
  {claude_ephemeral, gemini_implicit, auto}" — which is the SAFE default (cache stays off), but
  `not null;default:'off'` makes intent explicit and prevents NULL ever appearing in admin reads.
- `registry/store.go`: add `PromptCachePolicy string` to `resolvedRouteRow` (:93-113) AND to all THREE
  raw JOIN SELECT paths — **explicit checklist (resolves review finding #5 — P1; missing ANY one leaves
  that routing path with `PromptCachePolicy=''` and silently disables caching for it):**
  1. `GetResolvedRoute` (SELECT ~:317-340): add `s.prompt_cache_policy AS prompt_cache_policy` to the
     SELECT list, add the field to that path's inline rawRow struct, and add it to the struct-copy at ~:347.
  2. `GetResolvedRouteByModelKey` (SELECT ~:416-439): same three edits, copy at ~:445.
  3. `ListResolvedRoutesByModel` (SELECT ~:513-535): same three edits, copy at ~:540.
  The source column is on `ai_service` (aliased `s`), NOT `ai_service_route` (aliased `r`); all three
  queries already JOIN `ai_service s`, so `s.prompt_cache_policy` is in scope for each.
- `registry/registry.go`: add `PromptCachePolicy string` to `ResolvedRoute` (:51-72); set it in
  `buildResolvedRoute` (:458-485). T3 MUST include an integration test (also part of S5) that seeds an
  `ai_service` row with `prompt_cache_policy='claude_ephemeral'`, resolves it through EACH of the three
  store paths (`GetResolvedRoute`, `GetResolvedRouteByModelKey`, `ListResolvedRoutesByModel`), and
  asserts `route.PromptCachePolicy == 'claude_ephemeral'` for all three — this is the compile-and-runtime
  proof that `buildResolvedRoute` assigns the field and that no SELECT path was missed.
- `internal/pkg/aiservice/types.go:192` (after Thinking): add `EnablePromptCache bool json:"enable_prompt_cache"` (NO omitempty).
- Global flag helper: new tiny `internal/pkg/aiservice/cache_policy.go` →
  `func PromptCacheGloballyEnabled() bool { return viper.GetBool("features.provider_prompt_cache.enabled") }`.
- Caller opt-in: SOP `executor.go:587` set `EnablePromptCache: true`; agent `adapter.go` add immutable
  `enablePromptCache bool` field (set in `NewAiserviceAdapter`, preserved in `WithTools`), set
  `req.EnablePromptCache` in `convertToAiserviceRequest`. chatbot/salesrag leave false.
- config_dev.yaml `features:` block: add `provider_prompt_cache: { enabled: true }` (dev). Prod absent ⇒ OFF.

### 5F. Routing/seed config
- Migration `*_add_cache_creation_price.sql` (+rollback): add the 2 PricingRule columns + the
  `usage_record.cache_creation_tokens` column, idempotent information_schema+PREPARE.
- Migration `*_add_ai_service_prompt_cache_policy.sql` (+rollback): add
  `ai_service.prompt_cache_policy VARCHAR(16) NOT NULL DEFAULT 'off'` via the information_schema+PREPARE
  pattern. **MUST follow the ADD COLUMN with a backfill (resolves review finding #8 — P1):**
  `UPDATE ai_service SET prompt_cache_policy = 'off' WHERE prompt_cache_policy IS NULL OR prompt_cache_policy = '';`
  This guarantees pre-existing rows carry the safe `'off'` value whether the column was added by this
  migration or earlier by GORM AutoMigrate (which, if it ran first and created the column NULLable,
  would leave existing rows NULL). The `NOT NULL DEFAULT 'off'` in the ADD COLUMN handles new inserts;
  the UPDATE handles rows that predate the column. Idempotent (UPDATE on already-'off' rows is a no-op).
- Migration `*_seed_native_cache_pricing.sql`: seed read price (both) + Claude creation price; idempotent
  `WHERE ... IS NULL`, guarded by `billing_mode='flat'` + native provider name + `LOWER(model)`. Do NOT seed
  Gemini creation (leave NULL → standard input price). Verify real DMXAPI ¥/MTok at seed time.
- Migration `*_add_native_provider_rows.sql`: INSERT `llm_provider` rows `claude-native`/`gemini-native`
  (base host + DMXAPI key) **with `is_active = 0`** (resolves review finding #1 — P0 two-step activation).
  This migration MUST NOT repoint any `ai_service_route` and MUST NOT flip `prompt_cache_policy`. The
  file header documents the runbook: STEP 1 = SSH-apply this migration (rows exist but inert, startup
  assertion stays a no-op); STEP 2 = deploy the adapter-carrying binary; STEP 3 = confirm `/healthz/ai`
  lists `claude-native` + `gemini-native`; STEP 4 = manual admin op flips `is_active = 1`, repoints the
  chosen `ai_service_route`, and sets the service's `prompt_cache_policy`. Only after STEP 4 can a route
  resolve to a native adapter, by which point the binary is proven healthy. SSH-apply (CI does not run
  migrations).

---

## 6. Ordered TASK PLAN

> Order: framework + billing land first (additive, no behavior change), then each adapter is
> independently shippable. Each task is independently buildable/verifiable.

### T1 — Cache-billing schema + 3-bucket formula (foundation, no behavior change)
- **What**: add `CacheCreationInputPricePerMTok` + `SellCacheCreationInputPricePerMTok` (paired
  `*float64`, NULLABLE) to `PricingRule`; add `CacheCreationTokens` to `aiservice.TokenUsage`,
  `billing.TokenUsage` (Normalize NO-OP for this field, with documenting comment — finding #7),
  `model.UsageRecord`; add `CalculateCostWithCacheRW` and refactor `CalculateCostWithCache` to delegate
  with `cacheWriteTokens=0`; rewrite `recorder.computeRevenue` flat block to the 3-bucket
  write+read+nonCached carve using `SellCacheCreationInputPricePerMTok` (finding #2 — sell-side premium).
  Two migrations: pricing columns + usage_record column.
- **Files**: `internal/pkg/model/billing.go`, `internal/pkg/aiservice/types.go`, `internal/pkg/billing/types.go`,
  `internal/pkg/pricing/pricing.go`, `internal/pkg/billing/recorder.go`,
  `migrations/YYYYMMDD_HHMMSS_add_cache_creation_price.sql` (+rollback),
  `migrations/YYYYMMDD_HHMMSS_add_cache_creation_tokens_to_usage_record.sql` (+rollback).
- **Acceptance**: `go build ./...` + `go test ./internal/pkg/pricing/... ./internal/pkg/billing/...`;
  `CalculateCostWithCacheRW(...,0)` == `CalculateCostWithCache(...)` for a battery of inputs;
  creation price NULL ⇒ creation tokens billed at full input; `computeRevenue` with
  `CacheCreationTokens=0` byte-identical to legacy, with `>0` + `SellCacheCreationInputPricePerMTok`
  set ⇒ creation premium charged.
- **Test**: table-driven `pricing_test.go` — (a) `cw=0` equals legacy; (b) NULL creation price;
  (c) premium creation price; (d) read+write carve ordering (no negative normal); (e) tiered ignores both.
  PLUS `recorder` revenue test — (f) `CacheCreationTokens=0` revenue equals legacy; (g) `>0` with
  `SellCacheCreationInputPricePerMTok` non-nil charges the write premium; (h) NULL sell-creation price ⇒
  write bucket at `SellInputPricePerMTok` (no over/under charge).
- **dependsOn**: none.

### T2 — Billing wiring for creation tokens (copy sites)
- **What**: copy `CacheCreationTokens` from `chunk.Usage`/`chatResp.Usage` into `record` at ALL FOUR
  sites (finding #7 explicit list): middleware stream (after :113), middleware non-stream (after :499),
  recorder non-prebuilt buildRecord (after :296); switch `publishCostToHolder` (:568) + `computeCost`
  (:376) to `CalculateCostWithCacheRW(..., record.CachedPromptTokens, record.CacheCreationTokens)`. All
  sources are 0 today ⇒ byte-identical until a native Claude route writes non-zero.
- **Files**: `internal/pkg/aiservice/middleware/billing.go`, `internal/pkg/billing/recorder.go`.
- **Acceptance**: `go build ./...` + existing billing/middleware tests pass unchanged (every record has
  CacheCreationTokens=0 → byte-identical cost AND revenue).
- **Test**: middleware billing test asserting a record with CacheCreationTokens>0 routes through `...RW`
  (cost) and through the 3-bucket `computeRevenue` (sell) and is billed/charged at the creation premium;
  CacheCreationTokens=0 path unchanged. Verify the value reaches `record` via the explicit copy, NOT via
  `billing.TokenUsage.Normalize()` (which is a no-op for this field).
- **dependsOn**: T1.

### T3 — Internal toggle: schema + ResolvedRoute mirror + ChatRequest field + global flag
- **What**: add `ai_service.prompt_cache_policy VARCHAR(16) NOT NULL DEFAULT 'off'` column + model field
  (`gorm:"size:16;not null;default:'off'"` — finding #8); thread `PromptCachePolicy` through
  `resolvedRouteRow` + **all 3 JOIN SELECT paths via the explicit checklist in §5E** (each path: SELECT
  `s.prompt_cache_policy`, inline rawRow field, struct-copy mapping — finding #5) + `ResolvedRoute` +
  `buildResolvedRoute`; add `ChatRequest.EnablePromptCache`; add `PromptCacheGloballyEnabled()` helper;
  migration with NOT NULL DEFAULT + backfill UPDATE for existing rows (finding #8); config_dev flag.
- **Files**: `internal/pkg/model/ai_service.go`, `internal/pkg/aiservice/registry/store.go`,
  `internal/pkg/aiservice/registry/registry.go`, `internal/pkg/aiservice/types.go`,
  `internal/pkg/aiservice/cache_policy.go` (new), `config_dev.yaml`,
  `migrations/YYYYMMDD_HHMMSS_add_ai_service_prompt_cache_policy.sql` (+rollback).
- **Acceptance**: `go build ./...` + registry tests pass; a resolved route surfaces `PromptCachePolicy`
  from a seeded `ai_service` row through ALL THREE store paths; existing rows read `'off'` (NOT NULL).
- **Test**: registry store integration test (finding #5) seeding `prompt_cache_policy='claude_ephemeral'`
  and asserting `route.PromptCachePolicy == 'claude_ephemeral'` via EACH of `GetResolvedRoute`,
  `GetResolvedRouteByModelKey`, `ListResolvedRoutesByModel` (proves `buildResolvedRoute` assigns it and
  no SELECT path was missed); `PromptCacheGloballyEnabled()` reads viper.
- **dependsOn**: none (parallel-safe with T1/T2 — disjoint files except types.go; sequence types.go edits).

### T4 — Native-adapter framework scaffold + registration + startup assertion
- **What**: create `native_shared.go` (token-usage helpers, `redactGeminiURL` key-redacting URL helper —
  finding #4), empty `ClaudeNativeAdapter`/`GeminiNativeAdapter` structs with the two-client http split +
  `Name`/`ProviderType`/`Capabilities` + compile-time guards; register both in numind.go. Add gateway
  helpers `KnownNativeProviderNames()` + `lookupProviderExact()` and the `assertNativeAdaptersRegistered`
  startup check called after `SetDefault` / before `SyncProviderCredentials` (finding #1). (Chat/ChatStream
  may return `errno.ErrAICapabilityMismatch` stub until T5/T6 — but nothing routes to them yet.)
- **Files**: `internal/pkg/aiservice/adapter/native_shared.go` (new),
  `internal/pkg/aiservice/adapter/anthropic_native.go` (new, scaffold),
  `internal/pkg/aiservice/adapter/gemini_native.go` (new, scaffold),
  `internal/pkg/aiservice/gateway.go` (helpers), `internal/numind/numind.go` (register + assertion).
- **Acceptance**: `go build ./...`; `gateway.AdapterNames()` includes `claude-native` + `gemini-native`;
  compile-time guards satisfied; `lookupProviderExact("claude-native")` non-nil while
  `lookupProviderExact("nonexistent")` nil; existing routes still resolve to dmxapi (no native provider
  rows active yet); startup assertion is a no-op when no native `llm_provider` row is `is_active`.
- **Test**: gateway test asserting `lookupProviderExact("claude-native")` returns the Claude adapter
  (exact hit, NOT the dmxapi prefix fallback) and `lookupProvider("dmxapi")` still returns dmxapi;
  `assertNativeAdaptersRegistered` test — (a) no active native rows ⇒ passes; (b) an active `claude-native`
  row WITHOUT the adapter registered ⇒ fatal/error (table-driven with an injected fake gateway+db).
- **dependsOn**: T3 (needs `ResolvedRoute.PromptCachePolicy` for the adapter signature).

### T5 — Claude-native adapter: chat + stream + tools + cache_control (independently shippable)
- **What**: implement `buildAnthropicRequest` (incl. cache_control gating via D6 decision function + D8
  min-token guard), non-stream parse (D4 usage), `runAnthropicStream` (5B SSE grammar), tool-call
  translation (tool_use/tool_result, OAI↔Anthropic).
- **Files**: `internal/pkg/aiservice/adapter/anthropic_native.go`.
- **Acceptance**: `go build ./...` + `go test ./internal/pkg/aiservice/adapter/...`; golden-marshal test of
  the Anthropic body (toggle ON adds cache_control to last system block; toggle OFF omits it); usage parse
  asserts `PromptTokens = input+creation+read`, `CachedPromptTokens=read`, `CacheCreationTokens=creation`;
  streaming test replays recorded SSE transcripts asserting the finding #3 capture semantics: (i)
  `max()` prompt-side capture survives a duplicated/corrected usage chunk after `message_start`; (ii)
  `output_tokens` last-largest (no summing); (iii) a NO-usage transcript ⇒ post-stream `PromptTokens=0`;
  single terminal chunk in every case.
- **Test**: Go TDD — recorded-transcript table tests for chat, stream (incl. the 3 finding #3 cases),
  tool round-trip; no live calls.
- **dependsOn**: T1, T2, T4.

### T6 — Gemini-native adapter: chat + stream + tools + implicit cache (independently shippable)
- **What**: implement Gemini request build (`?key=` URL with `fullURL`/`redactedURL` split — finding #4,
  systemInstruction/contents/tools), non-stream parse (D5 usage), `runGeminiStream`,
  functionCall/functionResponse translation with **stateless name recovery via backward scan of
  `req.Messages`** (finding #6 — NOT an ephemeral per-request map), key redaction in all error/log paths.
- **Files**: `internal/pkg/aiservice/adapter/gemini_native.go`.
- **Acceptance**: `go build ./...` + adapter tests; body golden-marshal (key in URL not header; no
  cachedContent sent); usage parse asserts `PromptTokens=promptTokenCount` (no double-count),
  `CachedPromptTokens=cachedContentTokenCount`, `CacheCreationTokens=0`; stream test replays recorded SSE.
- **Test**: Go TDD — recorded-transcript table tests; URL-redaction unit test (fake key never appears,
  `key=REDACTED` does); 2-turn parallel-tool-call transcript asserting each `functionResponse.name`
  matches its `functionCall.name` resolved out of order (finding #6).
- **dependsOn**: T4 (independent of T5).

### T7 — Caller opt-in (SOP + agent EnablePromptCache)
- **What**: set `EnablePromptCache: true` in SOP `executor.go` req; add immutable `enablePromptCache` to the
  agent adapter (NewAiserviceAdapter + WithTools + convertToAiserviceRequest). chatbot/salesrag untouched.
- **Files**: `internal/numind/biz/sop/executor.go`, `internal/numind/biz/agent/adapter.go`.
- **Acceptance**: `go build ./...` + agent/sop tests; agent req carries `EnablePromptCache=true` across
  WithTools clone; chatbot/salesrag reqs remain false.
- **Test**: agent adapter test asserting the flag survives `WithTools`.
- **dependsOn**: T3.

### T8 — Seed migrations + native provider rows (inert) + two-step activation runbook
- **What**: write the seed pricing migration (read + Claude creation) and the provider-row migration that
  INSERTs `claude-native`/`gemini-native` with **`is_active=0`** and does NOT repoint any route or set
  `prompt_cache_policy` (finding #1 — two-step activation). The file header documents the 4-step runbook
  (apply rows inert → deploy binary → verify /healthz/ai → manual admin flip is_active+repoint+policy).
  The `prompt_cache_policy` migration (T3) includes the NOT NULL DEFAULT + backfill UPDATE (finding #8).
  Verify real DMXAPI prices at write time.
- **Files**: `migrations/YYYYMMDD_HHMMSS_seed_native_cache_pricing.sql`,
  `migrations/YYYYMMDD_HHMMSS_add_native_provider_rows.sql` (+rollbacks).
- **Acceptance**: SQL is idempotent (re-run = no-op); applies cleanly on a dev DB; `SELECT` confirms
  native provider rows exist with `is_active=0`, NO route repointed by the migration, seeded prices
  present; the startup assertion stays a no-op (rows inert).
- **Test**: dry-run on dev DB (SSH); confirm DeepSeek pricing rows + all existing routes untouched; the
  STEP-4 manual activation is performed during S5 live verification, not in the migration.
- **dependsOn**: T1, T3, T5, T6 (binary carrying native adapters must deploy BEFORE rows are activated).

### T9 — S5 verification strategy (see §7) — written into the plan, executed at S5
- **dependsOn**: T5, T6, T7, T8.

**taskCount = 9.**

---

## 7. S5 Verification Strategy

**Method: Go TDD for billing/translation (regression-protected) + dev live routing verification.**
Rationale: billing + wire translation are pure-logic and high-risk (money) → unit tests give permanent
regression protection; the cache-hit behavior is provider-side → must be confirmed against a real DMXAPI
call on dev. This feature touches billing (high-risk per Rule 10) → Playwright is N/A (no frontend), so
Go tests + a scripted dev call are the regression surface.

Key paths to verify at S5:
1. **Billing math** (Go, permanent): the T1/T5/T6 table tests above — creation premium, read discount,
   zero-collapse, per-provider PromptTokens normalization.
2. **Claude native live** (dev, SSH): perform the STEP-4 manual activation (flip the `claude-native`
   `llm_provider` row `is_active=1`, repoint a chosen Claude `ai_service_route` at it, set that service's
   `prompt_cache_policy='claude_ephemeral'`) AFTER confirming `/healthz/ai` lists `claude-native`; global
   flag ON; issue TWO identical SOP runs within 5 min (same large system prefix). Confirm:
   - call 1 records `cache_creation_tokens > 0`, `cached_prompt_tokens == 0`;
   - call 2 records `cached_prompt_tokens > 0` (read hit), creation small/zero;
   - `usage_record.cost_cents` for call 2 < call 1 (cache discount applied);
   - the credit deducted reflects the discounted/premium split.
3. **Gemini native live** (dev, SSH): STEP-4 activate the `gemini-native` row (`is_active=1`), repoint a
   Gemini route at it, set `prompt_cache_policy='gemini_implicit'`; issue two identical calls; confirm
   `cached_prompt_tokens` (`cachedContentTokenCount`) recorded on call 2 and discounted;
   `cache_creation_tokens == 0`.
4. **DeepSeek unaffected** (dev, SSH + Go): run an existing DeepSeek SOP/chatbot/agent flow; confirm the
   request body, response, and `usage_record` (cost_cents, tokens) are byte-identical to pre-feature
   (compare against a captured baseline). Confirm no DeepSeek route ever resolves to a native adapter.
5. **Toggle OFF** (dev): set a Claude route's `prompt_cache_policy='off'` (or global flag OFF); confirm the
   Anthropic body has NO `cache_control` and billing is full-price.
6. **DMXAPI billing verification** (dev, observational): measure whether DMXAPI's invoice actually surfaces
   the Anthropic creation premium / Gemini discount, to confirm seed cost prices (our sell price we control).
7. **Startup assertion (finding #1)** (dev, SSH): with the native rows inert (`is_active=0`), confirm the
   binary boots cleanly (assertion no-op). Then temporarily mark a native row `is_active=1` WITHOUT the
   adapter registered (simulate a stale binary) and confirm the service refuses to start (`log.Fatalw`) —
   then restore. Confirms the half-deploy TOCTOU window is closed.

---

## 8. Risks (carried from recon, mitigations baked into the plan)

- **P0 silent prefix fallback** (review finding #1): an `ai_service_route` pointing at `claude-native`
  while the adapter is un-registered → `findAdapterByPrefix` does NOT match → falls back to the hard-coded
  `dmxapi` adapter (gateway.go:181) → Anthropic body to `/chat/completions` → wrong/200 with lost cache
  tokens. Mitigation (BOTH required): (1) startup `assertNativeAdaptersRegistered` — query `llm_provider`
  for `is_active=true` native rows and `log.Fatalw` if `lookupProviderExact(name)==nil` (no dmxapi
  fallback masking); (2) two-step activation — T8 inserts rows with `is_active=0`, activation is a
  separate manual admin flip AFTER `/healthz/ai` confirms both adapters registered. No active native rows
  ⇒ assertion is a no-op ⇒ zero impact on existing deploys.
- **P0 creation-as-discount under-bill** (cost AND sell): feeding creation tokens into `CachedPromptTokens`
  bills them at a discount; omitting them from `computeRevenue` undercounts sell-side revenue (review
  finding #2). Mitigation: separate additive `CacheCreationTokens` + paired
  `CacheCreationInputPricePerMTok`/`SellCacheCreationInputPricePerMTok` columns + the `...RW` cost formula
  AND the symmetric 3-bucket carve in `computeRevenue` (D3).
- **P0 Anthropic token capture** (review finding #3): defensive `max()` capture of input/cache_creation/
  cache_read on EVERY usage-bearing chunk (not only `message_start`), last-largest `output_tokens` from
  `message_delta` (do not sum), and a post-stream guard that sets `PromptTokens=0` explicitly + WARNs when
  no prompt-side usage was ever seen (never bill full price on phantom tokens). D4 carve math unchanged.
- **P1 Gemini functionResponse name-match** (review finding #6): stateless backward scan of `req.Messages`
  for the matching `ToolCallID` in the nearest preceding assistant turn — no ephemeral per-request map,
  thread-safe, correct across turns and out-of-order parallel tool results.
- **P0 Gemini key-in-URL leak** (review finding #4): `fullURL` (with key) used ONLY on the wire; every
  error-wrap/log/trace uses `redactGeminiURL(...)` → `key=REDACTED`.
- **P1 Gemini promptTokenCount double-count**: D5 — do NOT add cached to prompt for Gemini.
- **P1 PromptCachePolicy propagation gap** (review finding #5): explicit 3-path SELECT checklist in §5E +
  an integration test resolving a seeded policy through all three store paths.
- **P1 prompt_cache_policy NULL on existing rows** (review finding #8): `NOT NULL DEFAULT 'off'` GORM tag +
  column DDL + a backfill `UPDATE ... WHERE prompt_cache_policy IS NULL OR = ''` in the migration.
- **P1 CacheCreationTokens not promoted by Normalize** (review finding #7): intentional — Claude reports it
  top-level, written directly by the adapter; Normalize() stays a no-op for it (documented), value reaches
  `record` only via the four explicit copy sites.
- **P2 Gemini creation cost unmeasured on DMXAPI**: leave Gemini creation price NULL; Gemini reports no
  separate creation bucket so CacheCreationTokens=0 ⇒ no exposure. Verify at S5.
- **P2 min-1024 token cache no-op + creation premium**: D8 adapter-local guard.
- **P2 ResolvedRoute 30s cache**: admin policy edits propagate in ≤30s; global viper flag is the instant override.
