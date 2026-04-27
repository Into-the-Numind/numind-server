# Context Budget Compression — S5 Verification

> NDF S5 验证文档（feature: `context-budget-compression`）
> Generated: 2026-04-25
> S4 最后 merge SHA (numind-server develop): 88c8ec7

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

- [ ] Migration apply:
  ```sql
  mysql -u root -p numind < migrations/20260425_172000_context_budget_compression.sql
  ```
  Expected: 4 new tables created (`token_estimation_profile`, `context_budget_policy`, `context_budget_event`, `context_summary`) + `credit_reservation` altered (new columns: `estimation_source`, `estimated_input_tokens`, `estimated_output_tokens`, `context_window`, `token_profile_id`, `budget_policy_id`)

- [ ] Migration rollback:
  ```sql
  mysql -u root -p numind < migrations/20260425_172000_context_budget_compression_rollback.sql
  ```
  Expected: all 4 new tables dropped, `credit_reservation` columns reverted, data intact

- [ ] Seed policies present after migration:
  ```bash
  curl -s -H "Authorization: Bearer $ADMIN_TOKEN" \
    http://localhost:9091/v1/admin/context-budget/policies | jq '.data.list | length'
  ```
  Expected: 6 default policies (sop_run, context_compression, chatbot_message, salesrag_query, chatbot_stream, salesrag_stream)

- [ ] `sop_run` policy: `charge_user=true`; `context_compression` policy: `charge_user=false`

### Admin API Smoke (cURL against dev or local)

- [ ] Preview endpoint returns safe budget:
  ```bash
  curl -s -X POST -H "Authorization: Bearer $ADMIN_TOKEN" \
    -H "Content-Type: application/json" \
    -d '{"service_id": 1, "model_key": "qwen-turbo"}' \
    http://localhost:9091/v1/admin/context-budget/preview \
    | jq '{safe_input_budget: .data.safe_input_budget, context_window: .data.context_window}'
  ```
  Expected: `safe_input_budget > 0`, `context_window > 0`

- [ ] Events endpoint returns metadata only (no prompt content):
  ```bash
  curl -s -H "Authorization: Bearer $ADMIN_TOKEN" \
    "http://localhost:9091/v1/admin/context-budget/events?page=1&page_size=5" \
    | jq '.data.list[0] | keys'
  ```
  Expected keys include: `id`, `user_id`, `operation`, `safe_input_budget`, `raw_input_tokens`, `compressed_input_tokens`, `compression_actions`, `created_at`.
  Must NOT include: `prompt`, `content`, `fragment_content`

- [ ] Token profiles list:
  ```bash
  curl -s -H "Authorization: Bearer $ADMIN_TOKEN" \
    "http://localhost:9091/v1/admin/context-budget/token-profiles?is_active=true" \
    | jq '.data.list | length'
  ```
  Expected: 1 active profile after fresh migration

- [ ] Create new token profile and verify version increment + old profile deactivated:
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

- [ ] SOP node execution triggers Gateway ContextBudgetCredits middleware:
  - Run a SOP in dev
  - Check DB: `SELECT id, estimation_source, estimated_input_tokens FROM credit_reservation ORDER BY id DESC LIMIT 1`
  - Expected: `estimation_source='context_budget'`, `estimated_input_tokens > 0`
  - Check DB: `SELECT id, operation, safe_input_budget FROM context_budget_event ORDER BY id DESC LIMIT 1`
  - Expected: row exists with `operation='sop_run'`, `safe_input_budget > 0`

- [ ] Over-budget guard fires correctly:
  - Construct a SOP request where total fragment tokens exceed `safe_input_budget`
  - Expected: API returns `ErrContextTooLarge` (errno code defined in `pkg/errno/`)
  - Expected: **no** LLM provider call is made (Langfuse shows no generation)

- [ ] Legacy-tier user SOP run:
  - Use an account with `billing_mode=legacy_tier`
  - Run a SOP node
  - Expected: `PreCheckResult.SkipDeduction=true` → no `credit_reservation` row created
  - Expected: `monthly_sop_runs` still increments (legacy tier counting continues)

- [ ] User-facing input counter state transitions (manual browser test):
  - SOP input (`/sop/*`): 33999 chars → normal (grey), 34000 chars → warning (yellow), 40001 chars → error (red + inline hint)
  - Chatbot input: same thresholds
  - SalesRAG input: same thresholds
  - Submit is NOT blocked by counter state (backend is authoritative per spec §8.2)

### Langfuse Observability Check

- [ ] Run a local SOP node execution with Langfuse enabled (`config_local.yaml langfuse.enabled: true`)
- [ ] In Langfuse UI, open the generation for that run
- [ ] Verify generation metadata includes:
  - `context_budget_event_id` (non-zero integer)
  - `safe_input_budget` (non-zero integer, same as event row)
  - `estimated_before` (integer, estimated tokens before compression)
  - `estimated_after` (integer, ≤ `estimated_before`)
  - `compression_actions` (array, may be empty if no compression needed)
- [ ] Verify generation metadata does NOT include:
  - `fragment.content` or any raw text from the user's prompt
  - `prompt_text`, `system_prompt`, `messages` content (privacy contract per `docs/context-budget-observability.md`)

### Frontend Playwright — Admin Web

- [ ] Enable the spec for S5 run:
  ```bash
  cd numind-admin-web
  # Remove test.fixme() wrappers from e2e/context-budget.spec.ts
  E2E_USERNAME=$E2E_USERNAME E2E_PASSWORD=$E2E_PASSWORD \
    BASE_URL=http://49.233.219.254:9100 \
    npm run test:e2e -- --grep "Context Budget"
  ```
- [ ] 4 admin paths must all PASS:
  1. Reject invalid LLM service capability (client-side validation)
  2. Create new token profile version (list increments, old deactivated)
  3. Edit policy and verify safe budget preview updates
  4. View recent events with no prompt content exposed

### gstack /qa — User Counter (web-v3)

- [ ] Run `gstack /qa` against local or dev web-v3
- [ ] Verify SOP input box counter behaviour:
  - Input 10 characters → counter grey (normal state)
  - Input 34000 characters → counter yellow with `34000/40000` display (warning state)
  - Input 40001 characters → counter red + inline warning hint visible (error state)
- [ ] Repeat for chatbot and SalesRAG input boxes
- [ ] Save screenshots as evidence under `numind-admin-web/e2e/screenshots/context-budget-s5/`

---

## S5 Known Deferred Items (to S6)

| Item | Reason for Deferral |
|------|---------------------|
| biz/contextbudget evaluation P50/P90/P99 thresholds still at Phase 1 (50%/80%) | Spec §4.3 literal 5%/10% thresholds require calibrated estimator weights; defer to S5 tuning |
| `newCreditsUser` fixture seed bug (6 pre-existing test failures) | Separate hotfix, unrelated to this feature |
| SOP reasoning fragments → `RoleWorking` mapping (Task 9 P2-2) | SOP data flow does not yet store reasoning content; revisit in SOP refactor |
| `chatbot.stream` double-fetch performance nit (Task 10 N1) | Pushed to refactor backlog |

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

**15/15 tasks complete. Backend + admin web + user web all delivered.**
