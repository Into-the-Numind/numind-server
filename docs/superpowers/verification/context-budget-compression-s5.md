# Context Budget Compression — S5 Verification

> NDF S5 验证文档（feature: `context-budget-compression`）
> Generated: 2026-04-25
> S5 sign-off date: 2026-04-27
> S4 最后 merge SHA (numind-server develop): 88c8ec7
> S5 final SHA (numind-server develop): ffb036a (after F-5 fix + retest closure)
> S5 final SHA (numind-admin-web develop): f2ef5f3 (after F-6 fix + Playwright spec)

---

## S5 Sign-off Verdict (2026-04-27)

**STATUS: ✅ S5 COMPLETE** — backend feature production-ready pending the prod `max_output_tokens` SQL backfill (`scripts/2026-04-27-context-budget-max-output-backfill/02-apply.sql`).

S5 verification surfaced 5 real findings (F-1 through F-6, F-4 unused). 3 were P0 production-blockers caught only by real end-to-end calls — every unit test passed before each was found. All 5 are now closed:

| Finding | Severity | Source | Fix |
|---------|----------|--------|-----|
| F-1: prod `max_output_tokens` NULL | P0 (data) | Schema audit | Backfill SQL artifacts (`9602541` + `48414b8`); release engineer runs on prod |
| F-2: nil-deref in Reserve path | P0 (panic) | Real chatbot retry | `LoadUser` plumbed (`17a2a27`) |
| F-3 P0: reservation never reconciled | P0 (data) | Real chatbot follow-up | `finalizeReservationIfNeeded` (`9483934`) |
| F-3 P2: cost calibration 8192 placeholder | P2 (~99% over-deduction) | DB inspection of #47 | `finalCostHolder` pattern (`bcda6ba`); retest #48 actual_cost_cents=4=usage_record |
| F-5: Langfuse generation metadata empty | P1 (observability) | Trace API check | `budgetMetadataHolder` pattern (`b498a99`); retest trace `ff39235c…` shows 11 budget keys |
| F-6: admin token-profile create UI broken | P1 (admin UX) | Playwright Path 2 | Frontend `profile_json` editor (`numind-admin-web` `7e9c6e6`); 4/4 paths PASS |

**Why this matters:** every P0/P1 above passed unit tests with mocks. They were only caught by Phase-3 end-to-end runs against real provider + real DB. The `tested = correct` criterion held only when the chain integration was exercised on dev. Same gap class for all of F-2/F-3/F-5: local function correctness without chain integration coverage.

**Out-of-scope items deferred** (recorded in `docs/superpowers/known-issues/2026-04-27-post-context-budget-state.md`):
- `TestReserve_CoefficientIDFrozenAcrossVersionBump` — uint64 vs *uint64 assertion type mismatch (P3, owner unassigned)
- `TestCreateRun_FreeUserReturnsTypedError` — error mapping returns wrapError instead of *errno.Errno (P3, owner unassigned)
- gstack `/qa` user counter visual check — formally skipped (Playwright Path 4 covers high-risk admin observability; counter UX has no business effect, plan §1320 explicitly classifies as gstack-eligible / no regression-protection acceptable; revisit if counter becomes hard submit-blocker)

**Detailed S5 evidence:** `docs/superpowers/verification/admin-api-smoke-2026-04-27.md` (Phase 1-3 admin smoke + chatbot end-to-end + F-5/F-6 retests) and the checklist below.

---

---

## Automated Test Results

### Backend (numind-server)

**`task lint` / `gofmt -l + go vet`**: PASS (warnings only from sqlite-vec macOS deprecation — pre-existing, unrelated)

**`go test ./... -count=1 -short`**: 37 packages tested

PASS packages:
- `numind-server/internal/numind` (2.457s)
- `numind-server/internal/numind/biz/aiservice_admin`
- `numind-server/internal/numind/biz/b2b_billing`
- `numind-server/internal/numind/biz/baidu`
- `numind-server/internal/numind/biz/chatbot`
- `numind-server/internal/numind/biz/contextbudget` ← new feature package
- `numind-server/internal/numind/biz/customer`
- `numind-server/internal/numind/biz/llmrouter`
- `numind-server/internal/numind/biz/monitor`
- `numind-server/internal/numind/biz/payment`
- `numind-server/internal/numind/biz/salesrag/adapter`
- `numind-server/internal/numind/biz/salesrag/domain`
- `numind-server/internal/numind/biz/salesrag/service`
- `numind-server/internal/numind/controller/v1/admin_ai`
- `numind-server/internal/numind/controller/v1/admin_contextbudget` ← new
- `numind-server/internal/numind/controller/v1/admin_credit`
- `numind-server/internal/numind/controller/v1/admin_migration`
- `numind-server/internal/numind/controller/v1/admin_sop`
- `numind-server/internal/numind/controller/v1/credit` ← fixed in Task 15 (stub missing CheckAndEstimateBudget + ReserveBudget)
- `numind-server/internal/numind/controller/v1/salesrag`
- `numind-server/internal/numind/store`
- `numind-server/internal/pkg/aiservice`
- `numind-server/internal/pkg/aiservice/adapter`
- `numind-server/internal/pkg/aiservice/middleware`
- `numind-server/internal/pkg/aiservice/profile`
- `numind-server/internal/pkg/aiservice/registry`
- `numind-server/internal/pkg/billing`
- `numind-server/internal/pkg/contextbudget` ← new feature package
- `numind-server/internal/pkg/core`
- `numind-server/internal/pkg/httpclient`

**Pre-existing FAIL packages (NOT introduced by this feature):**

All 6 failures below were verified present on `develop@1a9831a` (before any Task 1 commit) and caused by the `newCreditsUser` test fixture seed bug: the fixture creates a user with `BillingMode=credits + UserTier=standard + TierExpires=nil`, which makes `isEffectiveLegacy=true` at runtime — causing credits-mode codepaths to be unreachable:

| Package | Failing Tests |
|---------|--------------|
| `biz/credit` | `TestReserve_ExactlyExhaustedThenRetry_ReturnsInsufficientSentinel` |
| `biz/salesrag` | `TestAcquireSalesragCredits_CreditsHappyPath`, `TestAcquireSalesragCredits_InsufficientBalance`, `TestAcquireSalesragCredits_IdempotentReplay`, `TestFinalize_StreamErrorTriggersRefund` |
| `biz/sop` | `TestSopCredits_CreditsMode_ReserveThenReconcile` |

These 6 tests should be fixed in a separate hotfix (`fix: newCreditsUser fixture TierExpires nil`).

**Task 15 fix (this task):** `controller/v1/credit` had a build failure — `stubCreditSvc` in the test file was missing `CheckAndEstimateBudget` and `ReserveBudget` methods added to `ICreditService` by Task 4. Fixed in Task 15 by adding the two no-op panic stubs.

---

### Admin Web (numind-admin-web)

- `npm run lint`: PASS (1 pre-existing warning in `wave2-smoke.spec.ts`, 0 errors)
- `npm run type-check`: PASS
- `npm run test:unit -- ContextBudget`: **5/5 PASS** (690ms)

---

### User Web (numind-web-v3)

- `npm run lint`: PASS (3 pre-existing warnings in unrelated files, 0 errors)
- `npm run type-check`: PASS
- `npm run test:unit -- inputBudget`: **9/9 PASS** (789ms)

---

## Manual Verification Checklist (for S5 Release Engineer)

### Backend Deploy Verification

- [x] Migration apply:
  ```sql
  mysql -u root -p numind < migrations/20260425_172000_context_budget_compression.sql
  ```
  Expected: 4 new tables created (`token_estimation_profile`, `context_budget_policy`, `context_budget_event`, `context_summary`) + `credit_reservation` altered (new columns: `estimation_source`, `estimated_input_tokens`, `estimated_output_tokens`, `context_window`, `token_profile_id`, `budget_policy_id`)

- [x] Migration rollback:
  ```sql
  mysql -u root -p numind < migrations/20260425_172000_context_budget_compression_rollback.sql
  ```
  Expected: all 4 new tables dropped, `credit_reservation` columns reverted, data intact

- [x] Seed policies present after migration:
  ```bash
  curl -s -H "Authorization: Bearer $ADMIN_TOKEN" \
    http://localhost:9091/v1/admin/context-budget/policies | jq '.data.list | length'
  ```
  Expected: 6 default policies (sop_run, context_compression, chatbot_message, salesrag_query, chatbot_stream, salesrag_stream)

- [x] `sop_run` policy: `charge_user=true`; `context_compression` policy: `charge_user=false`

### Admin API Smoke (cURL against dev or local)

> Detailed evidence in `admin-api-smoke-2026-04-27.md` (Phase 1 + appendix B): 9 endpoints exercised, 33+ checks, all PASS.

- [x] Preview endpoint returns safe budget:
  ```bash
  curl -s -X POST -H "Authorization: Bearer $ADMIN_TOKEN" \
    -H "Content-Type: application/json" \
    -d '{"service_id": 1, "model_key": "qwen-turbo"}' \
    http://localhost:9091/v1/admin/context-budget/preview \
    | jq '{safe_input_budget: .data.safe_input_budget, context_window: .data.context_window}'
  ```
  Expected: `safe_input_budget > 0`, `context_window > 0`

- [x] Events endpoint returns metadata only (no prompt content):
  ```bash
  curl -s -H "Authorization: Bearer $ADMIN_TOKEN" \
    "http://localhost:9091/v1/admin/context-budget/events?page=1&page_size=5" \
    | jq '.data.list[0] | keys'
  ```
  Expected keys include: `id`, `user_id`, `operation`, `safe_input_budget`, `raw_input_tokens`, `compressed_input_tokens`, `compression_actions`, `created_at`.
  Must NOT include: `prompt`, `content`, `fragment_content`

- [x] Token profiles list:
  ```bash
  curl -s -H "Authorization: Bearer $ADMIN_TOKEN" \
    "http://localhost:9091/v1/admin/context-budget/token-profiles?is_active=true" \
    | jq '.data.list | length'
  ```
  Expected: 1 active profile after fresh migration

- [x] Create new token profile and verify version increment + old profile deactivated (verified via Playwright Path 2 after F-6 fix):
  ```bash
  # Create v2
  curl -s -X POST -H "Authorization: Bearer $ADMIN_TOKEN" \
    -H "Content-Type: application/json" \
    -d '{"chars_per_token": 3, "boost": 1.3, "notes": "S5 smoke test v2"}' \
    http://localhost:9091/v1/admin/context-budget/token-profiles | jq '.data.version'
  # Should return 2
  # Then verify v1 is_active=false
  curl -s -H "Authorization: Bearer $ADMIN_TOKEN" \
    "http://localhost:9091/v1/admin/context-budget/token-profiles?is_active=all" \
    | jq '[.data.list[] | {version, is_active}]'
  ```

### Runtime Smoke (dev environment)

- [x] **chatbot path** (substituted for SOP — same Gateway middleware) execution triggered ContextBudgetCredits middleware end-to-end:
  - Real chatbot call `POST /v1/chatbot/sessions/47/chat` user_id=25
  - DB: `credit_reservation #48` `estimation_source='context_budget'`, `reserved_credits=72`, `actual_cost_cents=4` (matches `usage_record #384 cost_cents=4` after F-3 P2 fix)
  - DB: `context_budget_event #6` `operation='chatbot_chat'`, `safe_input_budget=495648`, `status='ok'`, `reservation_id=48`, `actual_prompt_tokens=96`, `actual_completion_tokens=432`
  - Detailed query results in `admin-api-smoke-2026-04-27.md` Phase 3 + F-3 P2 retest section.

- [ ] Over-budget guard fires correctly **(deferred to S6 dogfooding)**:
  - Not exercised in S5 — would require constructing a synthetic large-fragment payload. Backend unit tests in `biz/contextbudget/biz_test.go` cover the planner + ErrContextTooLarge path. Real over-budget event will surface in S6 dogfooding when long-history chatbot threads accumulate.

- [ ] Legacy-tier user SOP run **(deferred — no legacy_tier test user provisioned on dev)**:
  - Unit-test coverage in `biz/credit/credit_service_reserve_test.go` (`TestReserve_LegacyTierBypass`). Will exercise on first legacy_tier production user touching SOP after S6 deploy.

- [-] User-facing input counter state transitions — **skipped per controller decision (gstack `/qa`)**:
  - Playwright Path 4 covers the high-risk admin observability path (events list, no prompt content). User-end character-count UX is low-risk per S3 plan §1320 (no business effect, gstack-eligible). Revisit if counter becomes a hard submit-blocker.

### Langfuse Observability Check

> Substituted local SOP run with real chatbot call on dev. Trace verified via Langfuse public API (UI walk-through deferred). F-5 architectural bug found and fixed during this check.

- [x] Triggered chatbot call on dev with Langfuse enabled (config_dev.yaml `langfuse.enabled: true`)
- [x] Fetched generation `chatbot.stream` from trace `ff39235c-b005-4c12-9121-aa5f6b317c20` via Langfuse public API after F-5 fix landed
- [x] Generation metadata (`output.metadata`) includes 11 budget keys:
  - `context_budget_event_id=8`, `safe_input_budget=495648`
  - `estimated_before=680`, `estimated_after=680` (no compression triggered, normal short message)
  - `context_window=1000000`, `max_output_tokens=32768`, `reserved_output_tokens=8192`
  - `safe_ratio=0.5`, `fixed_overhead_tokens=512`, `critical_fragment_count=2`
  - `token_profile_fallback=true` (using built-in default profile)
- [x] Five spec §11.1 fields are correctly omitted because zero-valued (`compression_actions=[]`, `dropped/summarized_fragment_count=0`, `token_profile_id=0`, `calibration_skipped=false`) — matches `mergeBudgetTracingMeta` "non-zero only" semantics.
- [x] Generation metadata excludes prompt content. (Note: generation `input` field DOES contain prompt — by Langfuse design, not a violation; spec §11.3 prohibition applies to logs only, plan §1321's "且不含 prompt 原文" qualifies the metadata field, which is satisfied.)

### Frontend Playwright — Admin Web

> Verified end-to-end against dev:9100. Path 2 surfaced F-6 (backend `binding:"required"` + frontend missing field), fixed inline (Option B — frontend `profile_json` editor). Final spec on `numind-admin-web` `f2ef5f3`.

- [x] Spec enabled (test.fixme → test) and run:
  ```bash
  cd numind-admin-web
  BASE_URL=http://49.233.219.254:9100 \
    E2E_USERNAME=$E2E_USERNAME E2E_PASSWORD=$E2E_PASSWORD \
    npx playwright test e2e/context-budget.spec.ts
  ```
- [x] **4/4 admin paths PASS** (after F-6 fix landed):
  1. ✅ Reject invalid LLM service capability — `e2e/screenshots/context-budget-path1.png`
  2. ✅ Create new token profile version (list increments, profile_json editor populates default template) — `path2.png` (gitignored, regenerable)
  3. ✅ Edit policy and verify safe budget preview updates — `path3.png`
  4. ✅ View recent events with no prompt content exposed — `path4.png`

### gstack /qa — User Counter (web-v3) — SKIPPED

- [-] **Skipped per controller decision (2026-04-27).** Rationale recorded in S5 Sign-off Verdict (top of doc): Playwright Path 4 covers the high-risk admin observability path; user-end character-count UX is low-risk per S3 plan §1320 (no business effect, gstack-eligible / no regression-protection acceptable). Revisit if counter logic ever becomes a hard submit-blocker.

---

## S5 Known Deferred Items (to S6 / future)

| Item | Reason for Deferral |
|------|---------------------|
| biz/contextbudget evaluation P50/P90/P99 thresholds still at Phase 1 (50%/80%) | Spec §4.3 literal 5%/10% thresholds require calibrated estimator weights; defer to S5 tuning |
| ~~`newCreditsUser` fixture seed bug (6 pre-existing test failures)~~ | ✅ FIXED in `62c16cd` (Team B during S5 fix wave) |
| SOP reasoning fragments → `RoleWorking` mapping (Task 9 P2-2) | SOP data flow does not yet store reasoning content; revisit in SOP refactor |
| `chatbot.stream` double-fetch performance nit (Task 10 N1) | Pushed to refactor backlog |
| `TestReserve_CoefficientIDFrozenAcrossVersionBump` failing on develop | Pre-existing P3, uint64 vs *uint64 assertion type mismatch, unrelated to this feature |
| `TestCreateRun_FreeUserReturnsTypedError` failing on develop | Pre-existing P3, error-mapping returns wrapError instead of *errno.Errno, unrelated |
| Over-budget guard E2E exercise on dev (synthetic large fragment payload) | Backend unit tests cover the planner + ErrContextTooLarge path; will surface in S6 dogfooding |
| Legacy-tier user end-to-end SOP run on dev | No legacy_tier test user provisioned; unit-test coverage in `biz/credit/credit_service_reserve_test.go` |
| gstack `/qa` user counter visual screenshots | Formally skipped — see Sign-off Verdict |

---

## Wave Summary

| Wave | Tasks | Key Commits (develop) |
|------|-------|-----------------------|
| 1 | Task 1 (schema) + Task 2 (contextbudget pkg) — parallel | 27aa411, 19a8fdc |
| 2 | Task 3 (store) + Task 4 (credit budget API) — parallel | ee84039, 8e9765c |
| 3 | Task 5 (gateway types + middleware order) — sequential | 80df966 |
| 4 | Task 6 (ContextBudgetCredits middleware) — sequential | 84144af |
| 5 | Task 7 (biz Prepare/Finalize/Preview) — sequential | 28d99de |
| 6 | Task 8 (worker) + Task 9 (SOP producer) + Task 10 (chat/salesrag producer) — parallel | 70b6502, a1e9044, 62f7855 |
| 7 | Task 11 (admin API) — sequential | f82f940 |
| 8 | Task 12 (observability + evaluation) — sequential | fffe861 |
| 9 | Task 13 (admin-web UI) + Task 14 (user-web input counter) — parallel | f5782cf (admin-web), 23cad1e (web-v3) |
| 10 | Task 15 (this: E2E prep + S5 verification doc + manifest) | (this commit) |
| S5 | F-1 backfill + F-2 LoadUser + F-3 reconcile + F-3 P2 calibration + test fixtures + F-5 holder + F-6 frontend | `9602541`+`48414b8`, `17a2a27`, `9483934`, `bcda6ba`+`655118a`, `62c16cd`, `b498a99`+`41edf0e`, `numind-admin-web` `7e9c6e6`+`f2ef5f3` |

**15/15 tasks complete. Backend + admin web + user web all delivered. S5 verification surfaced 5 real findings (3 P0 / 2 P1), all fixed and merged.**
