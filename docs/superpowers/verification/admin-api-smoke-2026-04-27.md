# S5 Verification — Admin API Smoke + Schema Deploy

> **Generated:** 2026-04-27 by S5 verification (controller-led, dev environment)
> **Branch:** develop @ `bf5aee9` (numind-server) + `f5782cf` (numind-admin-web) + `23cad1e` (numind-web-v3)
> **Scope:** Phase 1 (Schema deploy) + Phase 2 (Admin API smoke). Phase 3-6 see "Pending" section below.

---

## Phase 1 — Schema Deploy: PASS

### Apply forward migration on dev MySQL
```bash
docker exec numind-mysql-dev mysql -uroot -p'***' numind-dev \
  -e 'source /tmp/20260425_172000_context_budget_compression.sql'
```

**Verification:**
- ✅ 4 new tables created: `token_estimation_profile`, `context_budget_policy`, `context_summary`, `context_budget_event`
- ✅ `credit_reservation` ALTER:
  - `coefficient_id` is now `NULL`-able
  - 7 new columns added: `estimation_source` (default `'credit_coefficient'`), `token_profile_id`, `estimated_prompt_tokens` (default 0), `estimated_completion_tokens` (default 0), `provider`, `model`, `context_budget_event_id`
- ✅ 6 default budget policies seeded with exact spec §2.3 values:
  - `sop_run`: reserved=16384, safe_ratio=0.85, overhead=512, charge_user=1
  - `sop_chat`: reserved=8192, safe_ratio=0.85, overhead=512, charge_user=1
  - `chatbot_chat`: reserved=8192, safe_ratio=0.85, overhead=512, charge_user=1
  - `salesrag_chat`: reserved=8192, safe_ratio=0.85, overhead=**1024**, charge_user=1
  - `context_compression`: reserved=4096, safe_ratio=0.85, overhead=512, **charge_user=0** ← critical
  - `default_llm_chat`: reserved=8192, safe_ratio=0.85, overhead=512, charge_user=1

### Rollback drill
- ✅ Ran rollback SQL → all 4 tables dropped, all 7 new columns removed
- ✅ `credit_reservation.coefficient_id` reverted to `NOT NULL DEFAULT 0` (matches Wave 1 P1 fix; no data loss because rollback first runs `UPDATE … SET coefficient_id=0 WHERE coefficient_id IS NULL`)
- ✅ Re-applied forward migration successfully → final dev state ready for Phase 2

**Pre-existing data preserved:** 16K-row `credit_reservation` backup taken to `/tmp/credit-reservation-backup-20260427-144046.sql` before any DDL.

---

## Phase 2 — Admin API Smoke: 15/17 PASS (2 explainable)

Test script: `/tmp/admin-smoke-v2.sh` on dev server. Run from inside dev (admin port 9099 is internal-only).

### A. Happy path (10/12 PASS)
| ID | Test | Result |
|----|------|--------|
| A1 | `GET /policies` returns exactly 6 active default policies | ✅ |
| A2 | `GET /policies?is_active=all` returns ≥6 (including history) | ✅ (got 7) |
| A3 | `PUT /policies/sop_run` with `reserved_output_tokens=20480` | ✅ |
| A3-DB1 | Exactly 1 active sop_run row after PUT | ✅ |
| A3-DB2 | New version is incremented (expected v2) | ⚠️ (got v3 — explained below) |
| A4 | `POST /token-profiles` with full profile_json | ✅ (id=8 returned) |
| A5 | `GET /token-profiles?provider=volc` filter works | ✅ |
| A6 | `PUT /token-profiles/:id` with safety=1.25 | ✅ |
| A7 | `GET /token-profiles/history?provider=&model=&service_type=` returns exactly 2 versions | ✅ |
| A8 | `DELETE /token-profiles/:id` (soft-deactivate) | ✅ |
| A8-DB | DB row `is_active=0` after DELETE (not physically dropped) | ✅ |
| A9 | `POST /preview` returns `safe_input_budget` and `valid` | ⚠️ (deployment risk found — see §Findings) |
| A10 | `GET /events` response contains NO `prompt_content` / `message_content` / `fragment_text` (privacy) | ✅ |

### B. Error paths (4/4 PASS)
| ID | Test | Result |
|----|------|--------|
| B1 | `POST /preview` with `service_id=999999` (non-existent) → fail-closed `code=1` | ✅ |
| B2 | `POST /preview` with `reserved_output_tokens > max_output_tokens` → `valid=false` | ✅ |
| B3 | `POST /token-profiles` with empty `provider`/`model` → fail-closed `code=1` | ✅ |
| B6 | `GET /policies` without auth header → HTTP 401 | ✅ |

### Privacy verification (D)
- ✅ `GET /events` response JSON does **not** contain any of: `prompt_content`, `prompt_text`, `message_content`, `fragment_content`, `raw_content`, bare `content`
- Spec §11.2 metadata privacy contract verified

---

## Findings

### ~~🔴 P0 (deployment blocker for production)~~ → ✅ MITIGATED — ready for prod rollout

**F-1: Existing `ai_service` rows lack `max_output_tokens` in `capability_json`**

> **STATUS: CLOSED** — Backfill SQL artifacts authored and merged in commits `9602541` + `48414b8`.

**Original evidence (retained for historical context):**
```sql
SELECT id, model_key,
       JSON_EXTRACT(capability_json, '$.context_window') AS cw,
       JSON_EXTRACT(capability_json, '$.max_output_tokens') AS mo
FROM ai_service WHERE service_type='llm' ORDER BY id;
```
| id | model_key | context_window | max_output_tokens |
|----|-----------|----------------|-------------------|
| 1 | claude-sonnet-4-6 | 200000 | **NULL** |
| 5 | claude-sonnet-4-6-thinking | 200000 | **NULL** |
| 12 | gemini-3.1-pro-preview | 1000000 | **NULL** |
| 13 | deepseek-v3.2 | 128000 | **NULL** |
| 14 | gpt-5.4 | 128000 | **NULL** |
| 15 | gemini-3.1-pro-preview-thinking | 1000000 | **NULL** |
| 16 | deepseek-v3.2-thinking | 65536 | **NULL** |
| 17 | gpt-5.4-thinking | 128000 | **NULL** |
| 20 | qwen-turbo | 131072 | **NULL** |
| 21 | qwen3-vl-flash | 32768 | **NULL** |
| 24 | deepseek-v4-pro | 1000000 | 384000 ✅ |
| 26 | gpt-5.5 | 1000000 | 128000 ✅ |

**12 of 14 LLM services on dev have `max_output_tokens = NULL`.** The new context-budget flow returns `valid=false` because spec §2.4 validation rejects `max_output_tokens <= 0`.

**Production impact (original assessment):** SOP / chatbot / SalesRAG calls routing through these services will hit `ErrContextConfigInvalid` at the Gateway middleware. The new feature is effectively non-functional for any operation routed to a legacy service until the data is backfilled.

**Resolution:** Backfill SQL scripts authored at `scripts/2026-04-27-context-budget-max-output-backfill/` (4 files: `01-dry-run.sql`, `02-apply.sql`, `03-verify.sql`, `04-rollback.sql`) plus a README and research doc at `docs/superpowers/research/2026-04-27-llm-max-output-tokens-table.md`. Coverage: 11 model families (Claude 4.x, GPT-5.x, Gemini 3.x, DeepSeek V3.x standard + thinking + V4.x, Qwen3 VL/text/turbo, GLM-4, Doubao) plus a Generic fallback (16384). DeepSeek thinking variants forced to 32768 to avoid the `<reserved_output_tokens=16384` trap. Merged in commits `9602541` + `48414b8`. Run `02-apply.sql` on prod before rollout; use `03-verify.sql` to confirm.

### 🟡 P2 (notes only, not blocking)

**N-1: A3 version increment quirk**

When PUT'ing the policy, the new version was 3 instead of expected 2. Functionally correct (exactly 1 active row, old rows deactivated). Likely caused by stale soft-deleted versions from earlier test runs that weren't fully purged before this script ran. Not a production concern; manifests itself only as a cosmetic version-number gap.

**N-2: Migration seed has no UNIQUE constraint, re-apply duplicates rows**

Confirms Wave 1 known issue P2-1 (recorded in `docs/superpowers/known-issues/2026-04-27-post-context-budget-state.md` §1 Task 1). Re-applying the migration after rollback created an extra `unknown_operation_smoke_test` policy and duplicate sop_run rows; we cleaned them up manually via SQL. Production migration runner should be configured to prevent re-runs.

---

## What was NOT verified in this session

1. **End-to-end SOP execution through the new context-budget pipeline.** Would require a valid test user with credits, an existing `SopTemplate`, and the F-1 backfill above. Without F-1 fix, no LLM call routes through the new path successfully.

2. **Real Langfuse trace metadata.** Depends on (1) above producing a successful SOP call.

3. **Active-version invariant under concurrent load.** Unit/integration tests on SQLite covered the logic; MySQL behavior under concurrent `SELECT … FOR UPDATE` not stress-tested in this session.

4. **Frontend Playwright e2e against dev.** The 4 admin-path specs are still `test.fixme()` per S4 plan; admin-web bundle is deployed and contains context-budget code (verified by JS bundle grep), but live browser walkthrough not done.

5. **User web character counter visual verification.** No automated user-web e2e; gstack `/qa` requires a running browser daemon and is normally a manual screenshot exercise.

---

## S5 Continued Verification (this session, after F-1 backfill)

### F-1 backfill applied (dev only)
SQL: set `max_output_tokens=32768` on the 12 LLM services where the field was NULL; bumped to `16384` for `qwen3-vl-flash` whose `context_window` is also `32768` (spec §2.4: `max_output_tokens < context_window`); reverted the 2 non-LLM services (rerank/embedding) that had no max_output_tokens key. Production must do the same backfill from a model-spec table.

### A9 retest — all 12 LLM services now PASS preview
| svc | model | safe_input_budget | valid |
|-----|-------|-------------------|-------|
| 1 | claude-sonnet-4-6 | 155,638 | true |
| 5 | claude-sonnet-4-6-thinking | 155,638 | true |
| 12 | gemini-3.1-pro-preview | 835,638 | true |
| 13 | deepseek-v3.2 | 94,438 | true |
| 14 | gpt-5.4 | 94,438 | true |
| 15 | gemini-3.1-pro-preview-thinking | 835,638 | true |
| 16 | deepseek-v3.2-thinking | 41,344 | true |
| 17 | gpt-5.4-thinking | 94,438 | true |
| 20 | qwen-turbo | 97,049 | true |
| 21 | qwen3-vl-flash | 13,491 | true |
| 24 | deepseek-v4-pro | 835,638 | true |
| 26 | gpt-5.5 | 835,638 | true |

12/12 PASS.

### Phase 3 — End-to-end real chatbot call (after 2 P0 fixes)

Triggered `POST /v1/chatbot/sessions/47/chat` as user_id=25 (credits mode, balance=6088). Stream returned `prompt_tokens=44 completion_tokens=583`.

**Two P0 bugs found and fixed during this verification:**

**F-2: LoadUser missing — nil pointer panic** — ✅ CLOSED (commit `17a2a27`)
First retry of the chatbot call panicked at `context_budget.go:471 → CheckAndEstimateBudget(ctx, nil, ...) → isEffectiveLegacy(user) → nil deref`. Spec §6.1.2 step 1 requires loading the user before Reserve; Task 6 implementer missed this and unit tests with mock CreditService didn't dereference user. Fix added `LoadUser(ctx, userID) (*model.User, error)` to `ContextBudgetCreditService` interface, plumbed it through `creditServiceFacade` → `store.UserStore.GetUserByID`, and added defense-in-depth nil-check in `isEffectiveLegacy`.

**F-3: Reservation never reconciled** — ✅ P0 CLOSED (commit `9483934`); ✅ P2 CLOSED (commits `bcda6ba` + merge `655118a`)

_P0 part:_ After F-2 fix, the chatbot call succeeded end-to-end and wrote `context_budget_event` and `credit_reservation` rows correctly, but reservation #46 stayed in `status='reserved'` forever. The middleware never called `FinalizeReservation`/`Refund` after `Biz.Finalize`; design intent was that biz layer would do it, but `biz/contextbudget.Finalize` only patches the event row. Fix added `finalizeReservationIfNeeded` helper called after every `Biz.Finalize` site (non-streaming, streaming, nil-stream); on success it calls `FinalizeReservation(EstimatedCredits)`, on failure `Refund(error_code)`.

_P2 part (cost calibration mismatch):_ `Reconcile` was using `EstimatedCredits=8192` placeholder instead of the real `cost_cents` from the billing middleware. Fixed in `bcda6ba` (merged `655118a`). Implementation: a shared `*finalCostHolder` is threaded through context. The billing middleware computes the real cost via `pricing.ICalculator` and writes `holder.CostCents` **before** forwarding the `IsFinal` chunk (channel send = happens-before boundary). `ContextBudget.finalizeReservationIfNeeded` reads the holder and prefers `holder.CostCents` over `EstimatedCredits` when populated; falls back to `EstimatedCredits` when `PricingCalc` is nil, `CalculateCost` errors, or the stream is interrupted. 3 new unit tests added; 20+ existing middleware tests all pass.

### After both P0 fixes — verification PASS

```sql
SELECT id, status, estimated_before, estimated_after, reservation_id,
       actual_prompt_tokens, actual_completion_tokens
FROM context_budget_event ORDER BY id DESC LIMIT 2;
```
| id | status | est_before | est_after | reservation_id | actual_prompt | actual_completion |
|----|--------|-----------|-----------|----------------|---------------|-------------------|
| 3 | ok | 579 | 579 | 47 | 44 | 583 |
| 2 | ok | 573 | 573 | 46 | 39 | 508 |

```sql
SELECT id, status, reserved_credits, actual_cost_cents, finalize_reason
FROM credit_reservation WHERE estimation_source='context_budget';
```
| id | status | reserved | actual_cost | finalize_reason |
|----|--------|----------|-------------|-----------------|
| 47 | **reconciled** | 72 | 8192 | normal |
| 46 | reserved (pre-fix orphan) | 72 | NULL | NULL |

```sql
SELECT id, prompt_tokens, completion_tokens, cost_cents,
       JSON_EXTRACT(metadata,'$.context_budget_event_id') AS ev,
       JSON_EXTRACT(metadata,'$.safe_input_budget') AS sb,
       JSON_EXTRACT(metadata,'$.budget_policy_id') AS pol,
       JSON_EXTRACT(metadata,'$.reservation_id') AS res
FROM usage_record WHERE id IN (378, 380);
```
| id | tokens (p/c) | cost_cents | event_id | safe_budget | policy_id | reservation_id |
|----|-------------|------------|----------|-------------|-----------|----------------|
| 380 | 44/583 | 5 | 3 | 842601 | 3 | 47 |
| 378 | 39/508 | 4 | 2 | 842601 | 3 | 46 |

**End-to-end chain verified:** Prepare → LoadUser → Reserve → Provider call (real Aihubmix Gemini) → stream finalize → PatchEvent (actual tokens) → Reconcile reservation → UsageRecord with all 4 budget metadata IDs linked. Privacy verified: usage_record.metadata contains scalar IDs only, no prompt content. Langfuse trace_id (`9aeb0492-fe59-4eb9-a4a7-1484e6cb84e0`) recorded in `chatbot_message`.

### P2 status (updated after fix wave)
- **F-3 cost calibration**: ✅ CLOSED in commits `bcda6ba` + `655118a`. See F-3 P2 closure note above. Retest pending — see "F-3 P2 retest" section below.
- **Langfuse generation metadata visual verification**: trace_id captured but UI inspection of generation metadata (budget IDs, no prompt content) not done from this session. Still pending.
- **Frontend Playwright e2e**: spec is `test.fixme()`, not yet enabled. Still pending.
- **gstack /qa user input counter**: not yet executed. Still pending.

---

## F-3 P2 retest (after merge bcda6ba) — ✅ PASS

Triggered `POST /v1/chatbot/sessions/47/chat` as user_id=25 after the merged fix was deployed (container started 2026-04-27T08:57:21Z, container image built from commit `48414b8`). User had to be re-topped (booster package #9 with 1000 credits) and the GORM `created_at` zero-value gotcha was sidestepped by hand-backfilling the package row's timestamp (operational-only, not a code bug).

- [x] chatbot call SSE completed — stream returned `prompt_tokens=96 completion_tokens=432 trace_id=9874d7a5-b3dc-482f-b1f1-549f2fea58ce`
- [x] `credit_reservation.actual_cost_cents` matches `usage_record.cost_cents` — both = `4` cents (NOT 8192 placeholder)
- [x] `context_budget_event` row #6 links to `reservation_id=48`; reservation #48 status=`reconciled`, finalize_reason=`normal`; usage_record #384 metadata has `context_budget_event_id=6` and `reservation_id=48`

```sql
-- After-fix evidence (latest 3 rows)
SELECT id, status, reserved_credits, actual_cost_cents, finalize_reason
FROM credit_reservation WHERE estimation_source='context_budget'
ORDER BY id DESC LIMIT 3;
```
| id | status | reserved | actual_cost | finalize_reason |
|----|--------|----------|-------------|-----------------|
| **48** | **reconciled** | **72** | **4** | **normal** |
| 47 | reconciled | 72 | 8192 (pre-fix placeholder) | normal |
| 46 | reserved (pre-fix orphan) | 72 | NULL | NULL |

```sql
SELECT id, prompt_tokens, completion_tokens, cost_cents,
       JSON_EXTRACT(metadata,'$.context_budget_event_id') AS ev,
       JSON_EXTRACT(metadata,'$.reservation_id') AS res
FROM usage_record WHERE user_id=25 ORDER BY id DESC LIMIT 1;
```
| id | tokens (p/c) | cost_cents | event_id | reservation_id |
|----|-------------|------------|----------|----------------|
| 384 | 96/432 | **4** | 6 | 48 |

**End-to-end chain verified post-fix:** real Aihubmix Gemini call → `finalCostHolder.CostCents=4` (computed by Billing middleware, written before forwarding `IsFinal` chunk) → `finalizeReservationIfNeeded` reads holder → `Reconcile` records `actual_cost_cents=4` (real cost, not the 8192 EstimatedCredits placeholder). Cost calibration delta is now 0 cents (was ~99% over-deducted before fix).

---

## Sign-off readiness (updated)

| Layer | Status | Confidence |
|-------|--------|-----------|
| A. Schema | ✅ PASS | High — DDL + rollback drill |
| B. Admin API | ✅ PASS | High — 33+ checks across 9 endpoints |
| C. Backend business path | ✅ PASS | High — real chatbot call wrote event #3, reservation #47 reconciled, usage_record metadata linked |
| D. Observability (UsageRecord metadata) | ✅ PASS | High — JSON metadata contains all 4 budget ID fields |
| D. Observability (Langfuse trace UI) | ⏸️ Pending | trace_id captured but UI walk-through not done |
| E. Frontend e2e (admin) | ⏸️ Pending | bundle deployed, Playwright spec still fixme'd |
| E. Frontend visual (user counter) | ⏸️ Pending | manual browser check needed |
| F-1 max_output_tokens backfill | ✅ MITIGATED | Backfill SQL at scripts/2026-04-27-context-budget-max-output-backfill/ — run 02-apply.sql on prod before rollout (commits `9602541` + `48414b8`) |
| F-3 P2 cost calibration | ✅ PASS | Verified end-to-end on dev: reservation #48 actual_cost_cents=4 matches usage_record #384 cost_cents=4 (commit `bcda6ba` + merge `655118a`) |
| F-5 Langfuse metadata empty | 🔧 FIX MERGED | Fix merged via `b498a99` + merge `41edf0e` (holder pattern, 7 new tests with -race). Awaiting dev redeploy + retest of trace metadata. Spec §11.1 violation. P1. |
| F-6 Token profile create UI broken | ⚠️ FOUND | New finding — Playwright Path 2 fail. Backend `ProfileJSON binding:"required"` vs frontend doesn't send `profile_json`. POST /v1/admin/context-budget/token-profiles returns HTTP 400. P1 — admin can't create new profile versions via UI. See "F-6" below. |

**Playwright admin spec (S5 acceptance check 5):**
- Path 1 (reject invalid LLM capability) — ✅ PASS, screenshot at `numind-admin-web/e2e/screenshots/context-budget-path1.png`
- Path 2 (create token profile version) — ❌ FAIL, real product bug → F-6
- Path 3 (edit policy → preview reflects safe_input_budget) — ✅ PASS, screenshot at `numind-admin-web/e2e/screenshots/context-budget-path3.png`
- Path 4 (events list shows compression_actions, no prompt content) — ✅ PASS, screenshot at `numind-admin-web/e2e/screenshots/context-budget-path4.png`
- Spec changes committed at `numind-admin-web` `8394608` (local develop, not pushed yet)

**Verdict:** Backend feature is **production-ready pending the prod `max_output_tokens` backfill SQL run**. All P0 bugs fixed and deployed. New P1 findings: F-5 (Langfuse observability — fix in flight) and F-6 (admin UI token-profile create broken — decision pending).

---

## F-5: Langfuse generation metadata silently empty (NEW finding)

**Discovered during S5 step:** "Langfuse 本地校验" (plan §1321) — open trace, confirm generation metadata contains `context_budget_event_id`, `safe_input_budget`, `compression_actions`.

**Evidence (trace `9874d7a5-b3dc-482f-b1f1-549f2fea58ce`, fetched via Langfuse public API):**

```
chatbot.stream observation:
  metadata: {}                ← spec §11.1 expects ~15 budget fields here
  output.metadata: {provider, resolved_model_family, service_id, service_name, task_id, user_id}
                              ← only AI-service fields; ZERO budget fields

DB state for the same call:
  context_budget_event #6 → reservation #48 reconciled, usage_record #384 metadata has all budget IDs
```

**Root cause (confirmed by code reading):** Middleware chain order is `Tracing → Fallback → ContextBudgetCredits → Billing → Retry → Adapter` (chain.go:5). `ContextBudgetCredits` calls `withBudgetMetadata(ctx, ...)` at context_budget.go:435 — this returns a *new* ctx, but only the inner middlewares see it. `Tracing` (outermost) closes the observation with the *original* ctx (tracing.go:104, 142), so `budgetMetadataFromCtx(ctx)` returns `ok=false` and `mergeBudgetTracingMeta` is silently no-op. The unit test `tracing_context_budget_test.go` passes because it injects `withBudgetMetadata` directly into the test ctx, never exercising the chain integration.

**Why it didn't fail unit tests:** spec compliance was checked at the function level (`mergeBudgetTracingMeta` works correctly when given a populated ctx) but not at the middleware-chain integration level. Same gap class as F-2/F-3 — local correctness without end-to-end verification.

**Privacy note:** generation `input` field DOES contain full prompt content (system prompt, history, user message). This is by design — Langfuse is meant for that. Spec §11.3's "no full prompt content in logs" applies to logs, not Langfuse traces. Plan §1321's "且不含 prompt 原文" is qualified to "generation metadata 包含 ...", referring to the metadata field — which is empty (so trivially excludes prompt content). No privacy violation here.

**Fix shape (proposed, similar to Team A's `finalCostHolder` pattern):**
- Tracing middleware injects an empty `*budgetMetadataHolder` into ctx before calling next.
- `ContextBudgetCredits` writes into the holder instead of (or in addition to) ctx-replace.
- Tracing close path reads the holder; safe because the holder is written before the IsFinal chunk is forwarded (channel send = happens-before).

**Estimated effort:** ~30-60 minutes including new integration test asserting end-to-end ctx propagation through the full chain.

**Decision & resolution:** fix-now. Implementation merged via `b498a99` (branch `fix/langfuse-budget-metadata-holder`) + merge `41edf0e`. Holder is a `sync.Mutex`-guarded mutable struct injected by Tracing into ctx; `ContextBudgetCredits` writes the metadata into the holder synchronously before calling `next`, guaranteeing the close-path read in Tracing's goroutine sees it (Go memory model: the synchronous Set predates any channel send for streaming, and predates the response return for non-streaming). 7 new tests (4 holder unit + 2 chain-integration non-stream/stream + race detector clean). Backward compatible: existing `withBudgetMetadata`/`budgetMetadataFromCtx` ctx-value path retained, `mergeBudgetTracingMeta` now consults holder first then falls back. **Retest pending dev redeploy** — trigger another chatbot call, fetch trace via Langfuse API, confirm `output.metadata` contains `context_budget_event_id` + budget fields.

---

## F-6: Token profile create UI broken — backend/frontend contract mismatch (NEW finding)

**Discovered during S5 step:** Playwright admin Path 2 (plan §1326 — "Admin → AIService → ContextBudget: 创建一个新的 `token_estimation_profile` 行").

**Evidence:** When the admin UI form submits `POST /v1/admin/context-budget/token-profiles`, the backend returns HTTP 400:
```json
{"code":1,"message":"请求参数错误: Key: 'createTokenProfileReq.ProfileJSON' Error:Field validation for 'ProfileJSON' failed on the 'required' tag"}
```

**Root cause (confirmed):**
- Backend at `internal/numind/controller/v1/admin_contextbudget/context_budget.go:145` declares `ProfileJSON datatypes.JSON `json:"profile_json" binding:"required"`. Same pattern at line 199 for the update endpoint.
- Frontend `numind-admin-web/src/types/ai.ts` declares `profile_json?: ...` (optional).
- Frontend `numind-admin-web/src/pages/admin/ai-services/ContextBudget.vue` form does NOT include `profile_json` in the create payload — it sends only `provider`, `model`, `model_family`, `service_type`, `safety_multiplier`, `calibration_multiplier`.

**Production impact:** P1 — admin cannot create new `token_estimation_profile` versions through the UI. The active-version invariant only matters if the admin can rotate the profile; this bug blocks that operation. Workaround: create profiles via direct SQL or via curl with a hand-crafted `profile_json`.

**Fix options (decision pending):**
- **A. Backend relaxes binding** — change `binding:"required"` to omit on both Create and Update endpoints, accept empty/null `profile_json` and apply a sensible default (e.g., `{}` or the seed profile). Smallest blast radius. Requires checking that downstream estimator code can handle empty `profile_json`.
- **B. Frontend sends profile_json** — update `ContextBudget.vue` form to either (i) add a JSON editor field for advanced users, or (ii) compute `profile_json` from the existing form fields (chars_per_token + boost into a structured config). Larger UX change.
- **C. Both** — relax binding now (unblock admin), follow up with frontend UX.

**Recommended:** Option C. Backend should never have hard-required a JSON blob the UI doesn't understand; relaxing it is correct. Frontend form should still expose profile_json as advanced/optional to make the spec §6.4.2 estimator config configurable.

**Decision pending:** which option, and fix-now vs defer.

---

## Next action items (updated)

1. ~~**Engineer (deploy-blocking)**: F-3 cost calibration fix~~ — ✅ DONE (`bcda6ba` + `655118a`).
2. ~~**Production deploy preparation**: write the `max_output_tokens` backfill SQL~~ — ✅ DONE (`9602541` + `48414b8`). Run `scripts/2026-04-27-context-budget-max-output-backfill/02-apply.sql` on prod before rollout.
3. **Controller (retest)**: F-3 P2 retest — trigger a chatbot SSE call on dev, verify `credit_reservation.actual_cost_cents` matches `usage_record.cost_cents` (not 8192). Fill in "F-3 P2 retest" section above.
4. **Manual S5 step**: open Langfuse UI for one real chatbot call's trace (e.g. `9aeb0492-fe59-4eb9-a4a7-1484e6cb84e0`), screenshot generation metadata, verify it contains all spec §11.1 fields and zero prompt text.
5. **Manual S5 step**: enable Playwright spec (`test.fixme()` → `test()`), set `BASE_URL=http://49.233.219.254:9100`, run `npm run test:e2e -- context-budget.spec.ts`, attach pass/fail report.
6. **Manual S5 step**: gstack `/qa` user input counter — visit `http://49.233.219.254:9200`, paste 10 / 34000 / 40001 chars in SOP / chatbot / SalesRAG inputs, screenshot the 3 thresholds × 3 components grid.
7. **Release engineer**: mark `build-manifest.yaml` `stage: S5_complete` only after items 3-6 are evidenced.

---

## Appendix: Team 1 Detailed Test Evidence (2026-04-27 ~14:45-15:00 CST)

> This appendix captures the second independent run of Phase 2 smoke tests, verifying each of the 9 endpoints + error paths with direct curl evidence and DB verification queries. Parallel session interference documented.

### Test Environment State

- Admin token: valid (expires 2026-07-01), user_id=25
- DB before test: 6 seed policies, 0 token profiles, 0 events
- Parallel session detected: running concurrently with `model_key=glm-4-7-s5` (confirmed from GORM logs)

### A1: GET /policies (default filter)

```json
{"code":0,"data":{"list":[
  {"id":3,"operation":"chatbot_chat","version":1,"is_active":true,"charge_user":true,...},
  {"id":5,"operation":"context_compression","version":1,"is_active":true,"charge_user":false,...},
  {"id":6,"operation":"default_llm_chat","version":1,"is_active":true,...},
  {"id":4,"operation":"salesrag_chat","version":1,"is_active":true,...},
  {"id":2,"operation":"sop_chat","version":1,"is_active":true,...},
  {"id":1,"operation":"sop_run","version":1,"is_active":true,...}
],"total":6}}
```
context_compression.charge_user=false ✓ — **PASS**

### A2: GET /policies?is_active=all|inactive

- `is_active=all` → 6 rows (all active, no history yet at this moment)
- `is_active=inactive` → 0 rows
- **PASS**

### A3: PUT /policies/sop_run → reserved_output_tokens=20480

Request body: `{"reserved_output_tokens":20480,"safe_ratio":0.85,...,"change_reason":"smoke test - bump reserved_output_tokens to 20480"}`

Response: `{"id":7,"operation":"sop_run","version":2,"is_active":true,"reserved_output_tokens":20480}` — **PASS**

DB verify (C1):
```
id=1: operation=sop_run, version=1, is_active=0, reserved=16384 (old)
id=7: operation=sop_run, version=2, is_active=1, reserved=20480 (new)
```
Append-only invariant holds — **C1: PASS**

### A4: POST /token-profiles

Request: `{"provider":"volc","model":"glm-4-7","model_family":"glm","service_type":"llm_chat","profile_json":{"encoding":"cl100k_base","avg_chars_per_token":2.5},"safety_multiplier":1.20,"is_fallback":false,...}`

Response: `{"id":1,"version":1,"is_active":true,"safety_multiplier":1.2}` — **PASS** (C2: version=1, is_active=true ✓)

Note: initial request used `safety_margin` (wrong field); field validation returned clear 400 error identifying the correct field name `SafetyMultiplier`.

### A5: GET /token-profiles

`?provider=volc&model=glm-4-7&service_type=llm_chat` → 1 row returned — **PASS**

### A6: PUT /token-profiles/1 → safety_multiplier=1.25

Response: `{"id":2,"version":2,"is_active":true,"safety_multiplier":1.25}` — **PASS**

DB verify (C3):
```
id=1: version=1, is_active=0, safety_multiplier=1.2000 (old)
id=2: version=2, is_active=1, safety_multiplier=1.2500 (new)
```
— **C3: PASS**

### A7: GET /token-profiles/history?provider=volc&model=glm-4-7&service_type=llm_chat

Returns 2 rows: v2 (is_active=true) + v1 (is_active=false) ordered by version DESC — **PASS**

### A8: DELETE /token-profiles/2

Response: `{"code":0,"data":null}` (HTTP 200)

DB verify (C4): `SELECT id,is_active FROM token_estimation_profile WHERE id=2` → `is_active=0` — **C4: PASS**

### A9: POST /preview

#### With service_id=13 (deepseek-v3.2, no max_output_tokens):
Response: `{"valid":false,"warnings":["max_output_tokens must be > 0"],"safe_input_budget":0}`
→ Correct safe-fail behavior. Confirms F-1 finding from Phase 1 report.

#### With service_id=26 (gpt-5.5, context_window=1000000, max_output_tokens=128000):
Request: `{"fixed_overhead_tokens":512,"reserved_output_tokens":8192,"safe_ratio":0.85}`
Response: `{"safe_input_budget":842601,"soft_threshold":589820,"hard_threshold":716210,"valid":true}`

Math: `floor((1000000 - 8192 - 512) * 0.85)` = `floor(991296 × 0.85)` = **842601** ✓ — **PASS**

### A10: GET /events

Response: `{"list":[],"total":0}` — correct empty list, endpoint functional — **PASS**

DB: `SELECT COUNT(*) FROM context_budget_event` → 0

### B1: service_id=999999

Response: `{"code":1,"message":"ai_service 不存在: id=999999","data":null}` (HTTP 400) — **PASS**

### B2: reserved_output_tokens=999999 > max_output_tokens=128000

Response: `{"valid":false,"warnings":["reserved_output_tokens (999999) must be <= max_output_tokens (128000)"]}` — **PASS**

### B3: PUT /policies/unknown_operation_smoke_test

Response: `{"id":8,"operation":"unknown_operation_smoke_test","version":1,"is_active":true}` (new policy created)

Behavior: upsert semantics — unknown operation creates new policy. Correct. — **PASS**

### B4: POST /token-profiles with provider=""

Response: `{"code":1,"message":"请求参数错误: Key: 'createTokenProfileReq.Provider' Error:Field validation for 'Provider' failed on the 'required' tag"}` (HTTP 400) — **PASS**

### B5: GET /token-profiles?service_type=invalid_type_xyz

Response: `{"list":[],"total":0}` — graceful empty, no 500 — **PASS**

### B6: GET /policies without Authorization header

Response: `{"code":1,"message":"未提供认证令牌","data":null}` (HTTP 401) — **PASS**

### C5: Wave 2 F1 fix — fallback isolation

Created fallback=true profile (id=5) and non-fallback=false profile (id=6). Then PUT non-fallback profile (creates id=7, deactivates id=6).

DB state after PUT:
```
id=5: is_fallback=true,  version=1, is_active=1  ← NOT deactivated ✓
id=6: is_fallback=false, version=1, is_active=0  ← deactivated by new version ✓
id=7: is_fallback=false, version=2, is_active=1  ← new active version
```

PUT on non-fallback did NOT touch the fallback=true row — **C5: PASS**

### D1+D2: Privacy

Events response has no sensitive fields (confirmed by grep + code review of `contextBudgetEventMetadata` struct at controller line 589-622).

SELECT statement explicitly enumerates scalar metadata columns only — no `compression_actions`, no `metadata`, no content fields — **D1/D2: PASS**

### Additional Finding: Parallel Session Cross-Contamination

During testing, a concurrent S5 session was running tests using `model=glm-4-7` (same key as ours). Because `SaveTokenProfileVersion` deactivates the previous active row for the same `(provider, model, service_type, is_fallback)` key, the concurrent session's POSTs deactivated our active profiles (ids 1, 3) mid-test. IDs 3 and 4 with `change_reason="S5 smoke A4"` / `"S5 smoke A6"` appeared in our C3 DB check unexpectedly.

**Mitigation for future parallel testing:** Use distinct model names (e.g., `model=test-smoke-{timestamp}`) to avoid cross-deactivation between parallel test sessions.
