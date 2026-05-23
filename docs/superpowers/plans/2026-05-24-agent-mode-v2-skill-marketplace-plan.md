# Plan · agent-mode-v2-skill-marketplace

**Spec**: [2026-05-24-agent-mode-v2-skill-marketplace-design.md](../specs/2026-05-24-agent-mode-v2-skill-marketplace-design.md)
**Requirement**: [agent-mode-v2-skill-marketplace.md](../../../numind-server/requirements/agent-mode-v2-skill-marketplace.md)
**Hard prerequisite**: v2 #1 `agent-mode-v2-skill-as-artifact` must land develop before T1.

---

## Task graph (dependency DAG)

```
T1 (migrations + models + AutoMigrate) [server]
  └─→ T2 (store/marketplace.go + tests) [server]
       └─→ T3 (biz/marketplace/sanitize.go + tests) [server, LLM trace]
            └─→ T4 (biz/marketplace/service.go orchestration + tests) [server]
                 ├─→ T5 (biz/marketplace/clone.go + search.go + admin.go + tests) [server]
                 ├─→ T7 (errno additions) [server, parallelizable with T5]
                 └─→ T6 (controller + admin_controller + router register) [server, after T4+T5+T7]
                       └─→ T8 (api/marketplace.ts + stores/marketplace.ts) [web-v3]
                            └─→ T9 (4 views: Browse / Detail / Subscribed / Publish) [web-v3]
                                 └─→ T10 (SkillEditor button + SkillList badge + AppLayout menu + router guards) [web-v3]
                                      └─→ T11 (Playwright E2E marketplace.spec.ts) [web-v3]
                                           └─→ T12 (S5 validation strategy task — write QA report skeleton) [docs]
```

**Critical path**: T1 → T2 → T3 → T4 → T6 → T8 → T9 → T10 → T11 → T12 (10 sequential steps).

**Parallel opportunities (Tier 3 — same-repo disjoint files, requires `ndf-check-disjoint`)**:
- After T4 lands: T5 + T7 can be implemented in parallel sub-worktrees (`/private/tmp/wt-agent-mode-v2-skill-marketplace-numind-server-task5` etc.)
- After T6 lands: T8 frontend can start while backend testing finalizes (different repo = Tier 2, always parallel-safe)

---

## §T1 — DB migrations + GORM models + AutoMigrate

**Repo**: numind-server
**Files**:
- `migrations/20260524_120000_create_skill_marketplace.sql` (new)
- `migrations/20260524_120000_create_skill_marketplace.rollback.sql` (new)
- `migrations/20260524_120100_create_skill_subscription.sql` (new)
- `migrations/20260524_120100_create_skill_subscription.rollback.sql` (new)
- `migrations/20260524_120200_skill_add_subscribed_source_type.sql` (new)
- `migrations/20260524_120200_skill_add_subscribed_source_type.rollback.sql` (new)
- `internal/pkg/model/skill_marketplace.go` (new)
- `internal/pkg/model/skill_subscription.go` (new)
- `internal/numind/helper.go` (edit — add AutoMigrate for both models)

**Description**: Create the two new tables exactly per [spec §2.1](../specs/2026-05-24-agent-mode-v2-skill-marketplace-design.md). Models follow GORM patterns per [database.md §2](../../../.claude/rules/database.md). AutoMigrate registration coexists with v2 #1's skill table block (rebase merge after #1 lands).

**Acceptance**:
- SQLite in-memory test passes AutoMigrate for both models (no constraint errors)
- Forward migration applied to local MySQL 8 creates 2 tables with expected columns + indexes
- Rollback migration drops cleanly
- `INFORMATION_SCHEMA.STATISTICS` query confirms FULLTEXT(name, description, when_to_use) WITH PARSER ngram exists on skill_marketplace
- `INFORMATION_SCHEMA.STATISTICS` confirms UNIQUE(subscriber_user_id, marketplace_id) on skill_subscription
- `task lint` exits 0
- `go test ./internal/pkg/model/...` passes

**Bug-from-customer note**: not applicable (this is new feature, not customer bug).

---

## §T2 — Store layer

**Repo**: numind-server
**Files**:
- `internal/numind/store/marketplace.go` (new)
- `internal/numind/store/marketplace_test.go` (new)

**Description**: Implement `IMarketplaceStore` interface per [spec §7](../specs/2026-05-24-agent-mode-v2-skill-marketplace-design.md). All methods accept `context.Context`; tx-scoped methods accept `*gorm.DB` parameter. Use SQLite in-memory test DB (`testutil.NewTestDB(t)`) for integration tests covering: Create, GetByID, GetActiveBySourceSkillID, UpdateIsPublic, UpdateRecommended, IncrementSubscribeCount (in tx), List with filter+sort+pagination, CreateSubscription (in tx), DeleteSubscription, GetSubscription, ListMySubscriptions (with JOIN).

**Acceptance**:
- Unit tests cover every interface method (≥ 12 test functions)
- FULLTEXT search test skipped on SQLite (not supported); covered in dev integration test
- Pagination test verifies LIMIT/OFFSET correctness
- Cross-tenant isolation test: ListMySubscriptions for user A does not return user B's subscriptions
- `task lint` exits 0
- `go test ./internal/numind/store/...` passes

---

## §T3 — Sanitize pipeline

**Repo**: numind-server
**Files**:
- `internal/numind/biz/marketplace/sanitize.go` (new)
- `internal/numind/biz/marketplace/sanitize_test.go` (new)
- `internal/numind/biz/marketplace/types.go` (new — shared types like `SanitizeResult`, `PublishRequest`, `BrowseQuery`)

**Description**: Implement two-stage pipeline per [spec §3.2](../specs/2026-05-24-agent-mode-v2-skill-marketplace-design.md). Stage 1: regex blacklist with hardcoded PII patterns + tenant competitor list from `userStore.GetForbiddenCompetitors(ctx, publisherUserID)` (must exist via v1 #5; if missing, add stub method to userStore that reads `agent_permission_config.forbidden_competitor_names`). Stage 2: `aiservice.Chat` with `qwen-turbo` per [ai-service.md §0-2](../../../.claude/rules/ai-service.md). Langfuse generation per [ai-service.md §1](../../../.claude/rules/ai-service.md) — uses `langfuse.FromContext`, `langfuse.CreateGeneration`, `langfuse.EndGeneration`. Failure mode: return error wrapped with `errno.ErrSanitizeUnavailable`.

**Acceptance**:
- Unit test: PII regex covers email/phone/id-card/bank-card with realistic Chinese inputs
- Unit test: tenant competitor list applied case-sensitively
- Unit test: mock `aiservice.Chat` success → returns sanitized body + token counts
- Unit test: mock `aiservice.Chat` failure → returns wrapped `ErrSanitizeUnavailable`
- Unit test: mock `langfuse.FetchPrompt` failure → uses fallback inline prompt
- Unit test: mock `langfuse.CreateGeneration` called exactly once per `callSanitizeLLM` invocation, with `model="qwen-turbo"`, non-empty input string, and `UpdateGeneration` called with `WithGenUsage(prompt>0, completion>0)` on success path; on failure path, `UpdateGeneration` called with output map containing `error` key
- `task lint` exits 0
- `go test ./internal/numind/biz/marketplace/...` passes

**Bug-from-customer note**: not applicable.

---

## §T4 — Service orchestration

**Repo**: numind-server
**Files**:
- `internal/numind/biz/marketplace/service.go` (new — contains all 9 public methods + service struct)
- `internal/numind/biz/marketplace/service_test.go` (new)

**Description**: Implement `Service` interface per [spec §3.1](../specs/2026-05-24-agent-mode-v2-skill-marketplace-design.md). Each method:
- Verify parent account (`user.parent_user_id IS NULL`) — first thing
- Validate input
- Call store / sanitize / cloneToSubscriber as appropriate
- Wrap in transaction where atomicity required (Subscribe, Unsubscribe, Publish if uniqueness check)
- Return domain types

**API contract reference**: every method signature must match [spec §4 API contracts](../specs/2026-05-24-agent-mode-v2-skill-marketplace-design.md) exactly.

**Acceptance**:
- Unit test: every method has at least one happy-path test
- Unit test: cross-tenant scenarios cover all 7 security rules per [spec §10.1](../specs/2026-05-24-agent-mode-v2-skill-marketplace-design.md):
  1. `subscriberUserID` / `publisherUserID` derived from JWT context (never request body) — verify by injecting forged body value, biz layer ignores it
  2. Child account blocked on every method
  3. `Publish`: skill not owned by publisher → `ErrSkillNotOwned`
  4. `Subscribe`: self-subscribe → `ErrSelfSubscribeForbidden`
  5. `Unsubscribe`: subscription not owned by caller → 404
  6. `Get`: unpublished item visible to publisher, hidden from others
  7. `cloneToSubscriber`: writes skill with `parent_user_id=subscriberUserID` (verified via store mock call inspection)
- Unit test: Subscribe transaction atomicity (mock store fails mid-tx → no partial write)
- Unit test: SelfSubscribe blocked (`ErrSelfSubscribeForbidden`)
- Unit test: AlreadySubscribed blocked (`ErrAlreadySubscribed`)
- Unit test: AlreadyPublished blocked (`ErrSkillAlreadyPublished`)
- Unit test: Publish confirmation mismatch blocked (`ErrSanitizeConfirmationMismatch`)
- Unit test: Child account blocked on every method (`ErrChildAccountCannotAccessMarketplace`)
- `task lint` exits 0
- `go test ./internal/numind/biz/marketplace/...` passes (combined with T3)

---

## §T5 — Clone + search + admin

**Repo**: numind-server
**Files**:
- `internal/numind/biz/marketplace/clone.go` (new)
- `internal/numind/biz/marketplace/search.go` (new — list query builder per [spec §3.4](../specs/2026-05-24-agent-mode-v2-skill-marketplace-design.md))
- `internal/numind/biz/marketplace/admin.go` (new — SetRecommended per [spec §3.5](../specs/2026-05-24-agent-mode-v2-skill-marketplace-design.md))
- `internal/numind/biz/marketplace/clone_test.go` (new)
- `internal/numind/biz/marketplace/search_test.go` (new)

**Description**: Three helpers wired into Service. Clone calls `skill.Service.CreateInTx` (or fallback two-phase per [spec §11](../specs/2026-05-24-agent-mode-v2-skill-marketplace-design.md) D-COORD if #1 doesn't expose tx variant). Search builds GORM query for FULLTEXT match + JSON_CONTAINS + sort. Admin SetRecommended is a 3-line method (verify exists + UPDATE).

**Acceptance**:
- Unit test: cloneToSubscriber writes skill with source_type='subscribed' + enriched description
- Unit test: cloneToSubscriber Langfuse span created
- Unit test: search booleanModeQuery escapes special chars correctly
- Unit test: search applies correct ORDER BY for each sort mode
- Unit test: SetRecommended 404 on missing marketplace
- `task lint` exits 0
- `go test ./internal/numind/biz/marketplace/...` passes

**Tier 3 parallel candidate**: T5 + T7 can run in disjoint sub-worktrees (clone/search/admin files vs errno file). Run `ndf-check-disjoint.sh "biz/marketplace/clone.go,biz/marketplace/search.go,biz/marketplace/admin.go" "pkg/errno/marketplace.go"` → OK if exit 0.

---

## §T6 — Controller + router registration

**Repo**: numind-server
**Files**:
- `internal/numind/controller/v1/marketplace.go` (new — user controller with 8 handlers)
- `internal/numind/controller/v1/admin_marketplace.go` (new — admin controller with 1 handler)
- `internal/numind/router.go` (edit — add /v1/marketplace group)
- `internal/numind/admin_router.go` (edit — add /v1/admin/marketplace group)
- `internal/numind/controller/v1/marketplace_test.go` (new — HTTP-level smoke tests)

**Description**: Thin controller layer per [api-design.md](../../../.claude/rules/api-design.md). Each handler does bind → extract auth → call svc → WriteResponse. Router wiring per [spec §6](../specs/2026-05-24-agent-mode-v2-skill-marketplace-design.md), with `/my-subscriptions` registered before `/:id` (Gin path order). Update wire-in in `internal/numind/biz/biz.go` (or equivalent biz initialization) to expose Marketplace service to controller.

**Acceptance**:
- All 9 endpoints registered and respond (smoke test via `httptest`)
- Auth middleware applied (user_token for /v1/marketplace, admin_token for /v1/admin/marketplace)
- Path parameter parsing handles invalid IDs gracefully (returns ErrBind, not 500)
- `task lint` exits 0
- `go test ./internal/numind/controller/v1/...` passes
- `go build ./...` exits 0 (wire-in compile check; S5 covers full app smoke via local server)

---

## §T7 — Errno additions

**Repo**: numind-server
**Files**:
- `internal/pkg/errno/marketplace.go` (new — 9 error codes per [spec §4.3](../specs/2026-05-24-agent-mode-v2-skill-marketplace-design.md))
- `internal/pkg/errno/marketplace_test.go` (new — code uniqueness test)

**Description**: Define 9 errno values in range 401xxx (verify range not in use; pick next available block during S4). Each errno follows existing pattern: `var ErrXxx = NewErrno(code, "中文消息")`.

**Acceptance**:
- All 9 codes defined and unique
- Test: iterate package errno entries, assert no duplicate codes (existing test pattern in errno package; copy)
- Test: each errno's message is non-empty Chinese
- `task lint` exits 0
- `go test ./internal/pkg/errno/...` passes

---

## §T8 — Frontend API + Store

**Repo**: numind-web-v3
**Files**:
- `src/api/marketplace.ts` (new — per [spec §8.2](../specs/2026-05-24-agent-mode-v2-skill-marketplace-design.md))
- `src/stores/marketplace.ts` (new — Pinia setup syntax per [frontend-state.md §1](../../../.claude/rules/frontend-state.md))
- `src/api/marketplace.spec.ts` (new — vitest unit tests with axios mock)
- `src/stores/marketplace.spec.ts` (new — vitest store tests)

**Description**: TypeScript API layer routed through `src/api/request.ts` (no direct axios). Pinia store covers all UI flows: browse list + filtering, detail + subscribe/unsubscribe, my-subscriptions, publish flow (sanitizePreview + publish two-step).

**Acceptance**:
- All API functions typed end-to-end with response interfaces
- Pinia store uses setup syntax, exports refs/computed/functions correctly
- Error handling: 401 redirects (handled by request.ts interceptor); business errors surface user-readable message
- `npm run lint && npm run type-check` exits 0
- `npm run test:unit src/stores/marketplace.spec.ts` passes

---

## §T9 — Frontend Views

**Repo**: numind-web-v3
**Files**:
- `src/views/marketplace/MarketplaceBrowse.vue` (new)
- `src/views/marketplace/MarketplaceDetail.vue` (new)
- `src/views/marketplace/MarketplaceSubscribed.vue` (new)
- `src/views/marketplace/MarketplacePublish.vue` (new — diff view with vue-diff)
- `src/views/marketplace/CategoryMultiSelect.vue` (new — small reusable component)
- `package.json` (edit — add `vue-diff` dependency)
- `package-lock.json` (regenerated)

**Description**: 4 views per [spec §8.3](../specs/2026-05-24-agent-mode-v2-skill-marketplace-design.md). All views handle 4 states (loading / empty / error / success) per [ui-ux.md hard rule 2](../../../.claude/rules/ui-ux.md). MarketplaceSubscribed uses DataTable layout per [ui-ux.md hard rule 1](../../../.claude/rules/ui-ux.md). MarketplaceBrowse uses card grid (PRD-justified exception, see [spec §8.3](../specs/2026-05-24-agent-mode-v2-skill-marketplace-design.md)). Publish flow: gate user behind "我已确认脱敏内容无敏感信息" checkbox.

**Acceptance**:
- All 4 views render without console errors
- Each view exercises its 4 states (visually verified in S5 gstack /qa)
- vue-diff renders left/right diff with highlights
- Subscribe/Unsubscribe twice-confirmation modals fire
- `npm run lint && npm run type-check` exits 0
- `npm run build` produces a valid dist (no broken imports)

---

## §T10 — Cross-cutting UI

**Repo**: numind-web-v3
**Files**:
- `src/views/config/skills/SkillEditor.vue` (edit — add "发布到市场" button)
- `src/views/config/skills/SkillList.vue` (edit — add "已发布" badge)
- `src/layouts/AppLayout.vue` (edit — add "技能市场" menu item for parent accounts)
- `src/router/index.ts` (edit — register 4 marketplace routes + parent-account route guard)

**Description**: Touch-up edits to existing v2 #1 / layout files. All edits guarded by `meta: { requiresParent: true }` route guard (pattern reused from agent-mode-configurator-relocate). Menu item conditionally renders based on user.parent_user_id.

**Acceptance**:
- Click "发布到市场" from SkillEditor → navigates to `/marketplace/publish/:id`
- "已发布" badge appears on SkillList for skills with active marketplace entries
- "技能市场" menu visible to parent accounts, hidden for child/learner accounts
- Route guard redirects child accounts trying to access /marketplace
- `npm run lint && npm run type-check` exits 0

---

## §T11 — Playwright E2E

**Repo**: numind-web-v3
**Files**:
- `e2e/marketplace.spec.ts` (new)
- `e2e/fixtures/parent-account-b.ts` (new — second parent account fixture for cross-tenant tests)

**Description**: E2E coverage of cross-tenant flows per [spec §14](../specs/2026-05-24-agent-mode-v2-skill-marketplace-design.md) S5 validation strategy:
- AC-1: Login as parent A → SkillEditor → "发布到市场" → publish page → check checkbox → click 发布 → marketplace row created (verify via /v1/marketplace/list as A)
- AC-2: Login as parent B → /marketplace → see A's item
- AC-3: B clicks subscribe → /config/skills shows new entry (source_type=subscribed)
- AC-4: B unsubscribes → cloned skill softdeleted, /marketplace/subscribed empty
- AC-6: Login as child account → GET /v1/marketplace/list → 403

**Reuses**: `e2e/auth.setup.ts` for primary parent account; new fixture for second parent + child account (envvar `E2E_USERNAME_B`, `E2E_PASSWORD_B`, `E2E_CHILD_USERNAME`, `E2E_CHILD_PASSWORD`).

**Pre-S4 prerequisite (Pause and Ask gate)**: before T11 starts, user must add the 4 envvars to `.claude/settings.local.json`. Without them, AC-3/AC-4/AC-6 cannot be validated and T12 "5+ spec tests pass" requirement fails. Procedure:
1. When entering S4 T11, AI runs `grep -E 'E2E_USERNAME_B|E2E_CHILD_USERNAME' ~/.claude/settings.json .claude/settings.local.json` (where readable)
2. If absent, **invoke Pause and Ask**: report what's needed (4 envvars, what they represent: second parent account credentials + a child account under one of the parents) and request user to populate
3. Block T11 start until envvars confirmed present
4. **No skip-with-TODO** for AC-6 (child 403) — this is a security gate, must be verified

**Acceptance**:
- 5+ spec tests pass against local server (`E2E_USERNAME=... npm run test:e2e -- marketplace.spec.ts`)
- No test relies on a specific test-DB state (each spec creates its own marketplace item with unique name suffix `_test_${timestamp}`)
- All tests clean up via `afterAll` deleting created marketplace items

---

## §T12 — S5 Validation Strategy Task

**Repo**: docs only (this plan file + future QA report)
**Files**:
- This plan §T12 documents the validation strategy
- S5 produces `.ndf/features/agent-mode-v2-skill-marketplace/qa-report.md` (using templates/ndf/qa-report.md)

**Description** (required by [NDF Rule 10](../../../.claude/rules/ndf-enforcement.md)):

**Validation approach**: **Playwright E2E** for cross-tenant flows (AC-1..AC-6) + **gstack /qa** for visual QA on browse/detail/publish-diff/subscribed views + **Langfuse dashboard inspection** for trace verification.

**Reasoning**:
- Cross-tenant flows are HIGH risk per [spec §10](../specs/2026-05-24-agent-mode-v2-skill-marketplace-design.md). Permanent regression coverage REQUIRED → Playwright E2E (persistent test files, run in CI).
- Per [feedback_review_each_stage memory] and NDF Rule 10 honesty clause: high-risk business logic (cross-tenant sanitize/copy) must have persistent regression tests; gstack /qa alone is insufficient.
- Visual QA on diff view + card layouts: gstack /qa appropriate (one-time human visual inspection, no business logic regression concern).
- Langfuse traces: manual inspection of dashboard for `skill-marketplace-publish` / `sanitize-skill-body` generation rows + `marketplace-subscribe-clone` spans. Single S5 inspection sufficient; auto-monitoring out of scope.

**Key user paths to verify**:
1. Parent A publish flow end-to-end (with diff confirmation)
2. Parent B browse + subscribe flow (verify cloned skill source_type=subscribed)
3. Parent B unsubscribe (verify cloned skill softdeleted, subscription removed)
4. Child account marketplace access (verify 403)
5. Admin SetRecommended (verify sort=recommended ranks correctly)
6. FULLTEXT search "销售" matches A's "销售调研"
7. Sanitize LLM failure mode (mock or actually trip rate limit) → publish disabled with proper error

**Acceptance for T12 (planning task itself)**:
- This T12 section exists in plan
- S5 step in main task list (#7) cites this section

---

## Total task count: 12

| Layer | Tasks | Repo |
|---|---|---|
| Backend DB | 1 (T1) | numind-server |
| Backend store | 1 (T2) | numind-server |
| Backend biz | 3 (T3, T4, T5) | numind-server |
| Backend errno | 1 (T7) | numind-server |
| Backend controller/router | 1 (T6) | numind-server |
| Frontend API/store | 1 (T8) | numind-web-v3 |
| Frontend views | 1 (T9) | numind-web-v3 |
| Frontend cross-cutting | 1 (T10) | numind-web-v3 |
| Frontend E2E | 1 (T11) | numind-web-v3 |
| Docs / validation | 1 (T12) | docs |

Backend-to-frontend ordering ensures API contracts ready before UI consumes.

---

## Manifest update at S4 entry

When T1 starts, update `numind-server/.ndf/manifest.yaml` for this feature:

```yaml
progress:
  total_tasks: 12
  completed_tasks: 0
  reviewed_tasks: 0
  current_task: 'T1 in progress: migrations + models + AutoMigrate'
```

Increment `completed_tasks` and `reviewed_tasks` per NDF Rule 6 — every task's two-stage parallel review (spec compliance + code quality) must PASS before next task starts.

---

## Risk register specific to S4 execution

| Risk | Trigger | Mitigation |
|---|---|---|
| `skill.Service.CreateInTx` not exposed by v2 #1 | T5 implementation | Fallback to two-phase commit (spec §11 Option B); send tiny PR to #1 maintainers post-S4 to add proper tx variant |
| `userStore.GetForbiddenCompetitors` missing | T3 implementation | Add new method to userStore reading agent_permission_config.forbidden_competitor_names; fallback to empty list if config row missing |
| `langfuse.FetchPrompt` returns empty on first call (Langfuse not seeded) | T3 implementation | sanitizeFallbackPrompt inline constant guarantees behavior; manually seed Langfuse before S5 verification |
| Rebase conflicts on router.go / helper.go with #1 | S4 entry | Rebase as first S4 act; resolve manually; run `task lint` + `go build` after rebase before any new code |
| FULLTEXT BOOLEAN MODE behavior different SQLite vs MySQL | T2 store tests | Mark FULLTEXT test as `t.Skip("FULLTEXT requires MySQL; verify in dev integration")` |
| vue-diff dependency pulls in heavy bundle | T9 implementation | Verify bundle size impact during `npm run build`; switch to manual diff implementation if > 100KB delta. **package.json ownership lock**: T9 is the sole owner of `package.json` / `package-lock.json` edits in this feature. T10 must NOT touch package.json (its scope is .vue + router/index.ts only). If T9 switches off vue-diff, regeneration of package-lock.json remains within T9's commit — does not push files into T10. This avoids Tier 4 file overlap. |

---

## Plan sign-off

- [x] Every task has number/title/description/files/acceptance
- [x] No circular dependencies (DAG above)
- [x] Multi-repo ordering: backend tasks T1-T7 precede frontend tasks T8-T11
- [x] API contracts referenced (spec §4)
- [x] LLM trace topology task implicit in T3 (sanitize is the LLM-touching task)
- [x] S5 validation strategy as standalone task T12
- [x] Atomic tasks (each one builds + tests pass independently per acceptance criteria)
- [x] Tier 3 parallel opportunities identified (T5+T7) with file disjointness gate
