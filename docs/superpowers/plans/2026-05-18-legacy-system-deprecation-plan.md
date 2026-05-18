# Legacy System Deprecation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the `legacy_tier` billing system entirely from numind-server / numind-web-v3 / numind-admin-web codebases, leaving credits three-pool SOT (trial_grant + credit_cycle + user_booster_balance) as the only billing path. Schema cleanup (DROP 5 columns) is the final step after ≥1 week prod soak per sub-task.

**Architecture:** Pure deletion refactor. No new components introduced. Each sub-task removes one slice (T1 core dispatch / T2 边界 + admin + frontend / T3 user model + tests / T4 schema). All 4 sub-tasks execute in a single all-in-1-day session — prod 0 users on legacy path means T1-T3 are dead-code deletions with `git revert` as fast rollback; T4 schema DROP retains backup + dry-run ceremony.

**Tech Stack:** Go 1.21 + GORM + Gin (numind-server), Vue 3 + TypeScript + Pinia (numind-web-v3 + numind-admin-web), MySQL 8

---

## Pre-flight: Audit Snapshot

Run these once before T1 begins. They lock in the current scope so divergences during execution are flagged.

- [ ] **Step 0.1: Confirm prod data still 100% credits + free**

Run via SSH (need `PROD_SSH_PASS` env var):

```bash
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no root@129.28.125.51 \
  "docker exec -i \$(docker ps --format '{{.Names}}' | grep mysql | head -1) \
   mysql -u root -p\$PROD_SSH_PASS numind-prod -N -e \
   'SELECT billing_mode, user_tier, COUNT(*) FROM user GROUP BY billing_mode, user_tier;'"
```

Expected output: only rows with `credits` + `free`. If any row has `legacy_tier` or non-`free` user_tier, STOP and update spec §6 before proceeding.

- [ ] **Step 0.2: Grep snapshot for scope drift detection**

Run from `/Users/zhiyuchen/Documents/10_跃迁有数/有数AI工作台/莫小派/Codes/numind-server`:

```bash
git grep -nE "isEffectiveLegacy|legacyTierImpl|HasActiveMembership|CanRunSOP|GetRemainingSOPRuns|GetActualUserTier|IsInNewSOPMonth|UserTierFree|UserTierTrial|UserTierStandard|UserTierPremium|BillingModeLegacyTier" -- ':!*_test.go' ':!archive/*' ':!.claude/*' ':!*.md' > /tmp/legacy-audit-prod.txt
wc -l /tmp/legacy-audit-prod.txt
```

Expected: ~50-60 lines. Save this file; T1-T4 verification will rerun the same grep and compare counts.

---

## T1: Backend Core Dispatch Cleanup

**Goal:** Remove `isEffectiveLegacy`, `legacyTierImpl`, and all dispatch branches in `biz/credit/`. After T1, all credit flow operations go through credits-only paths.

**Scope:** numind-server only.

**Branch:** `fix/legacy-deprecation-t1-dispatch`

### Task T1.1: Confirm `isEffectiveLegacy` callsites

**Files:**
- Read: `internal/numind/biz/credit/credit_service.go:87-116`
- Read: `internal/numind/biz/credit/credit_service.go:140-220`
- Read: `internal/numind/biz/credit/credit.go:60-110`

- [ ] **Step 1: Snapshot callsites**

```bash
git grep -n "isEffectiveLegacy" -- 'internal/' ':!*_test.go'
```

Expected 6 hits across 2 files: `credit_service.go` (5) + `credit.go` (1).

### Task T1.2: Delete `legacyTierImpl` struct + 4 methods

**Files:**
- Modify: `internal/numind/biz/credit/credit_service.go`

- [ ] **Step 1: Remove struct definition and methods (line 296-358 region)**

Open `internal/numind/biz/credit/credit_service.go`, delete:

```go
// legacyTierImpl handles billing for users on the next-count-based legacy tier.
type legacyTierImpl struct {
    biz ICreditBiz
}

func (l *legacyTierImpl) CheckAndEstimate(ctx context.Context, user *model.User, op Operation, in EstimationInput) (*PreCheckResult, error) {
    // … existing implementation …
}

func (l *legacyTierImpl) Reserve(ctx context.Context, user *model.User, op Operation, estimated int64, coefID uint64, idempotencyKey *string) (*Reservation, error) {
    // … panics by design …
}

func (l *legacyTierImpl) GetBalance(ctx context.Context, user *model.User) (*BalanceBreakdown, error) {
    return l.buildLegacyBalance(user), nil
}

func (l *legacyTierImpl) buildLegacyBalance(user *model.User) *BalanceBreakdown {
    // … computes RemainingRuns/MonthlyLimit snapshot …
}
```

Read the lines carefully — preserve the rest of the file.

- [ ] **Step 2: Remove field + initialization in creditService**

In the same file, locate the `creditService` struct (~line 50) and:

- Remove the `legacy *legacyTierImpl` field
- In `NewCreditService` (~line 80), remove the `legacy: &legacyTierImpl{biz: biz},` initialization line

- [ ] **Step 3: Verify build**

```bash
go build ./...
```

Expected: PASS (no references to legacyTierImpl remain in this file).

### Task T1.3: Simplify dispatch methods on `creditService`

**Files:**
- Modify: `internal/numind/biz/credit/credit_service.go`

Each dispatch method currently has `if isEffectiveLegacy(user) { return s.legacy.X(...) }`. After T1.2 the legacy branch references a deleted field. Now collapse each method into a direct call to credits-only impl.

- [ ] **Step 1: Simplify `CheckAndEstimate` (line ~110)**

Before:
```go
func (s *creditService) CheckAndEstimate(ctx context.Context, user *model.User, op Operation, in EstimationInput) (*PreCheckResult, error) {
    if isEffectiveLegacy(user) {
        return s.legacy.CheckAndEstimate(ctx, user, op, in)
    }
    return s.credits.CheckAndEstimate(ctx, user, op, in)
}
```

After:
```go
func (s *creditService) CheckAndEstimate(ctx context.Context, user *model.User, op Operation, in EstimationInput) (*PreCheckResult, error) {
    return s.credits.CheckAndEstimate(ctx, user, op, in)
}
```

- [ ] **Step 2: Simplify `Reserve` (line ~120)**

Before:
```go
func (s *creditService) Reserve(ctx context.Context, user *model.User, op Operation, estimated int64, coefID uint64, idempotencyKey *string) (*Reservation, error) {
    if isEffectiveLegacy(user) {
        return s.legacy.Reserve(ctx, user, op, estimated, coefID, idempotencyKey)
    }
    return s.credits.Reserve(ctx, user, op, estimated, coefID, idempotencyKey)
}
```

After:
```go
func (s *creditService) Reserve(ctx context.Context, user *model.User, op Operation, estimated int64, coefID uint64, idempotencyKey *string) (*Reservation, error) {
    return s.credits.Reserve(ctx, user, op, estimated, coefID, idempotencyKey)
}
```

- [ ] **Step 3: Simplify `GetBalance` (line ~152)**

Before:
```go
func (s *creditService) GetBalance(ctx context.Context, user *model.User) (*BalanceBreakdown, error) {
    if isEffectiveLegacy(user) {
        return s.legacy.GetBalance(ctx, user)
    }
    return s.credits.GetBalance(ctx, user)
}
```

After:
```go
func (s *creditService) GetBalance(ctx context.Context, user *model.User) (*BalanceBreakdown, error) {
    return s.credits.GetBalance(ctx, user)
}
```

- [ ] **Step 4: Simplify `CheckAndEstimateBudget` (line ~205)**

Same pattern — remove the `if isEffectiveLegacy(user)` block and call `s.credits` directly.

- [ ] **Step 5: Verify build + lint**

```bash
go build ./... && task lint
```

Expected: PASS.

### Task T1.4: Delete `isEffectiveLegacy` function itself

**Files:**
- Modify: `internal/numind/biz/credit/credit_service.go`

- [ ] **Step 1: Delete function (line 87-105)**

Delete the entire block:

```go
// isEffectiveLegacy returns true ONLY when billing_mode is explicitly
// legacy_tier. The previous HasActiveMembership() fallthrough caused
// credits-mode users with a non-expired user_tier (legacy field) to be
// incorrectly routed to the legacy path, bypassing trial_grant / credit_cycle
// / user_booster_balance entirely. Per product directive, legacy is fully
// deprecated and credits-mode users must always read the three-pool SOT.
func isEffectiveLegacy(user *model.User) bool {
    if user == nil {
        return false
    }
    return user.BillingMode == model.BillingModeLegacyTier
}
```

- [ ] **Step 2: Verify zero references**

```bash
git grep -n "isEffectiveLegacy" -- 'internal/' ':!*_test.go'
```

Expected: zero hits.

- [ ] **Step 3: Build**

```bash
go build ./...
```

Expected: PASS.

### Task T1.5: Simplify `CanPerformAIOperation`

**Files:**
- Modify: `internal/numind/biz/credit/credit.go:60-110`

- [ ] **Step 1: Replace legacy branch + fallback**

Before:
```go
func (b *creditBiz) CanPerformAIOperation(ctx context.Context, user *model.User, operation string) (bool, string) {
    if isEffectiveLegacy(user) {
        if IsSopOperation(operation) {
            return user.CanRunSOP()
        }
        return true, ""
    }

    estimated := GetEstimatedCredits(operation)

    if b.membershipSvc != nil {
        view, err := b.membershipSvc.GetBalance(ctx, uint64(user.ID), time.Now().UTC())
        if err != nil {
            log.Errorw("CanPerformAIOperation: membershipSvc.GetBalance failed", "user_id", user.ID, "err", err)
            return false, "积分余额查询失败，请稍后重试"
        }
        total := view.CycleRemaining + view.TrialRemaining + view.BoosterUsable
        if total < int64(estimated) {
            return false, "积分不足，请充值积分"
        }
        return true, ""
    }

    balance, err := b.ds.Credits().GetBalance(ctx, user.ID)
    if err != nil {
        log.Errorw("Failed to get credit balance", "user_id", user.ID, "error", err)
        return false, "积分余额查询失败，请稍后重试"
    }
    if balance < estimated {
        return false, "积分不足，请充值积分"
    }
    return true, ""
}
```

After:
```go
func (b *creditBiz) CanPerformAIOperation(ctx context.Context, user *model.User, operation string) (bool, string) {
    if b.membershipSvc == nil {
        log.Errorw("CanPerformAIOperation: membershipSvc not wired", "user_id", user.ID)
        return false, "积分系统初始化错误，请联系管理员"
    }

    estimated := GetEstimatedCredits(operation)
    view, err := b.membershipSvc.GetBalance(ctx, uint64(user.ID), time.Now().UTC())
    if err != nil {
        log.Errorw("CanPerformAIOperation: membershipSvc.GetBalance failed", "user_id", user.ID, "err", err)
        return false, "积分余额查询失败，请稍后重试"
    }
    total := view.CycleRemaining + view.TrialRemaining + view.BoosterUsable
    if total < int64(estimated) {
        return false, "积分不足，请充值积分"
    }
    return true, ""
}
```

- [ ] **Step 2: Build + verify**

```bash
go build ./... && git grep -n "user.CanRunSOP\|user.HasActiveMembership" -- 'internal/numind/biz/' ':!*_test.go'
```

Expected: build PASS; grep returns 0 hits in biz/.

### Task T1.6: Clean langfuse spans

**Files:**
- Modify: `internal/numind/biz/credit/credit_service_langfuse.go`

- [ ] **Step 1: Delete `BillingMode` span input field at line ~40**

Locate the trace input construction and remove the `BillingMode: user.BillingMode` field from the input struct.

- [ ] **Step 2: Delete `BillingMode` metadata field at line ~189**

Locate the trace metadata construction and remove the `"billing_mode": user.BillingMode` map entry.

- [ ] **Step 3: Simplify `classifyDeductedFrom` at line ~203**

Before:
```go
func classifyDeductedFrom(balance *BalanceBreakdown) string {
    if balance.BillingMode == model.BillingModeLegacyTier {
        return "none(legacy)"
    }
    // … existing credits logic …
}
```

After: delete the if branch entirely. The function should start directly with the credits logic.

- [ ] **Step 4: Build + lint**

```bash
go build ./... && task lint
```

Expected: PASS.

### Task T1.7: Update T1 tests

**Files:**
- Modify: `internal/numind/biz/credit/credit_service_test.go`
- Modify: `internal/numind/biz/credit/credit_service_boundary_test.go`
- Modify: `internal/numind/biz/credit/credit_service_reserve_test.go`
- Modify: `internal/numind/biz/credit/credit_service_langfuse_test.go` (if exists)

- [ ] **Step 1: Find legacy-only test cases**

```bash
git grep -nE "isEffectiveLegacy|legacyTierImpl|CanRunSOP|user_tier.*premium|tier_expires" -- '*_test.go' internal/numind/biz/credit/
```

Save output. For each hit:

- [ ] **Step 2: For each test using `billing_mode='legacy_tier'`, delete the test case**

A pattern to look for (delete the entire `t.Run(...)` block):
```go
t.Run("legacy_tier user gets RemainingRuns response", func(t *testing.T) {
    user := &model.User{BillingMode: model.BillingModeLegacyTier, UserTier: model.UserTierPremium, ...}
    // …
})
```

- [ ] **Step 3: For dual-mode tests, keep only the credits branch**

If a test has multiple `t.Run` blocks for credits/legacy/free, keep only credits + free, delete legacy.

- [ ] **Step 4: Run test suite**

```bash
task test 2>&1 | tail -30
```

Expected: PASS. If any test fails because of a missing dependency on `isEffectiveLegacy` or `legacyTierImpl`, delete that test.

### Task T1.8: Final verification + commit + deploy

- [ ] **Step 1: Grep audit**

```bash
git grep -nE "isEffectiveLegacy|legacyTierImpl" -- ':!*_test.go' ':!archive/*' ':!.claude/*' ':!*.md'
```

Expected: zero hits.

- [ ] **Step 2: Full build + lint + test**

```bash
go build ./... && task lint && task test 2>&1 | tail -20
```

Expected: all PASS.

- [ ] **Step 3: Commit on fix branch**

```bash
git checkout -b fix/legacy-deprecation-t1-dispatch
git add internal/numind/biz/credit/
git commit -m "refactor(credits): T1 remove isEffectiveLegacy + legacyTierImpl dispatch

Legacy system deprecation T1 of 4. Removes:
- isEffectiveLegacy function (5 callsites in credit_service.go + 1 in credit.go)
- legacyTierImpl struct + 4 methods (CheckAndEstimate / Reserve / GetBalance / buildLegacyBalance)
- creditService.legacy field + initialization
- CanPerformAIOperation legacy branch + ds.Credits().GetBalance fallback
- BillingMode field in langfuse span input + metadata
- classifyDeductedFrom 'none(legacy)' branch
- Dual-mode tests reduced to credits-only

All 5 dispatch methods (CheckAndEstimate / Reserve / GetBalance /
CheckAndEstimateBudget / CanPerformAIOperation) now route directly
to credits impl. Prod audit confirmed 0 users on legacy path.

Spec: docs/superpowers/specs/2026-05-18-legacy-system-deprecation-design.md
Plan: docs/superpowers/plans/2026-05-18-legacy-system-deprecation-plan.md T1

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 4: Merge to develop + push + tag**

```bash
git checkout develop && git merge --no-ff fix/legacy-deprecation-t1-dispatch -m "Merge T1 dispatch cleanup"
git push origin develop
# Compute next tag: get current latest tag, bump patch
LATEST=$(git tag --sort=-v:refname | head -1)  # e.g. v2.1.22
NEXT_TAG="v2.1.23"  # adjust if more tags landed between
git tag -a "$NEXT_TAG" -m "T1: remove isEffectiveLegacy + legacyTierImpl"
git push origin "$NEXT_TAG"
```

- [ ] **Step 5: Monitor CI + prod deploy**

```bash
gh run list --limit 2 --workflow=ci-cd.yaml
# watch the tag run; if Docker Hub GFW fails, use dockerproxy.net manual recovery per hotfix runbook
```

Expected: CI deploys to prod. healthz green. Update manifest:
- progress.completed_tasks: 1
- decisions: add "2026-MM-DD (T1 prod tag $NEXT_TAG): …"

### T1 → T2 Gate: smoke verify (~10-15min)

1. `curl -s https://youshu.asia/healthz` → "healthy"
2. `curl -s -H "Authorization: Bearer $TOKEN" https://youshu.asia/v1/credits/balance` → response without `billing_mode` / `sub_total` / `sub_remain` / `booster_remain` / `balance` fields
3. SSH prod: `docker logs --since 5m numind-server-prod 2>&1 | grep -i "panic\|error.*credit" | head -20` → no critical errors
4. Proceed to T2 once green

---

## T2: 边界 Caller + Admin + Frontend Cleanup

**Goal:** Remove all remaining legacy reads/writes from controller layer, payment / grant logic, admin migration tool, and both frontends.

**Scope:** numind-server + numind-web-v3 + numind-admin-web.

**Branch:** `fix/legacy-deprecation-t2-boundary`

### Task T2.1: Delete `IncrementSopRunCount` + callers (numind-server)

**Files:**
- Modify: `internal/numind/store/customer.go:299-339`
- Modify: any file calling `IncrementSopRunCount` (locate via grep)

- [ ] **Step 1: Find callers**

```bash
git grep -n "IncrementSopRunCount" -- ':!*_test.go' ':!archive/*'
```

Note each `path:line`.

- [ ] **Step 2: Delete the method definition**

In `internal/numind/store/customer.go`, find and delete:

```go
func (s *customerStore) IncrementSopRunCount(ctx context.Context, userID uint, ...) error {
    // … updates monthly_sop_runs / monthly_reset_at …
}
```

And remove the method from the interface definition (look for `IncrementSopRunCount` in `internal/numind/store/customer.go` or interface file at top of same file).

- [ ] **Step 3: Delete each caller line**

For each grep hit from Step 1, delete the calling line (or the surrounding if/return statement).

- [ ] **Step 4: Build**

```bash
go build ./...
```

Expected: PASS. If any file doesn't compile, check for unused imports and clean them.

### Task T2.2: Delete admin tier endpoint (numind-server)

**Files:**
- Modify: `internal/numind/controller/v1/admin_user/user.go:~240-305`
- Modify: `internal/numind/admin_router.go:93`

- [ ] **Step 1: Delete `UpdateUserTier` handler**

In `internal/numind/controller/v1/admin_user/user.go`, delete the entire `UpdateUserTier` function (the body shown in S2 spec §6 T2, including its req struct).

- [ ] **Step 2: Delete the route**

In `internal/numind/admin_router.go`, delete line 93:

```go
adminGroup.PUT("/users/:id/tier", userCtrl.UpdateUserTier)
```

- [ ] **Step 3: Build**

```bash
go build ./...
```

Expected: PASS. Remove unused imports (`time`, etc.) if any complaint.

### Task T2.3: Delete entire `admin_migration` controller (numind-server)

**Files:**
- Delete: `internal/numind/controller/v1/admin_migration/migrations.go`
- Delete: `internal/numind/controller/v1/admin_migration/*.go` (entire directory)
- Modify: `internal/numind/admin_router.go` (remove migration routes)

- [ ] **Step 1: Find routes registered for admin_migration**

```bash
git grep -n "admin_migration\|migrationCtrl\|migrations\.New" -- 'internal/numind/admin_router.go'
```

- [ ] **Step 2: Delete the directory**

```bash
rm -rf internal/numind/controller/v1/admin_migration/
```

- [ ] **Step 3: Remove imports + routes from admin_router.go**

Delete the import line `"numind-server/internal/numind/controller/v1/admin_migration"` and any line registering migration handlers.

- [ ] **Step 4: Build**

```bash
go build ./...
```

Expected: PASS.

### Task T2.4: Simplify `customer.go` boundary responses (numind-server)

**Files:**
- Modify: `internal/numind/biz/customer/customer.go:~171,230,270`

- [ ] **Step 1: ListSubUsers (line ~171)**

Locate the response field assignment. Before:
```go
result[i] = SubUserListItem{
    // … other fields …
    RemainingRuns: user.GetRemainingSOPRuns(),
}
```

Delete the `RemainingRuns: user.GetRemainingSOPRuns(),` line.

Also delete `RemainingRuns int` from the `SubUserListItem` struct definition (search same file for struct).

- [ ] **Step 2: GetSubUserDetail (line ~230) — same pattern**

Delete the `RemainingRuns` field assignment and the struct field definition.

- [ ] **Step 3: GetCustomerStatistics (line ~270) — same pattern**

Delete the `RemainingRuns` field assignment and the struct field definition.

- [ ] **Step 4: Build**

```bash
go build ./...
```

Expected: PASS.

### Task T2.5: Simplify `user/get.go` response (numind-server)

**Files:**
- Modify: `internal/numind/controller/v1/user/get.go:60-90`

- [ ] **Step 1: Delete legacy fields from response**

Locate the response composition (`gin.H{...}` or similar). Delete entries:
- `"user_tier": user.UserTier`
- `"tier_expires": user.TierExpires`
- `"remaining_runs": user.GetRemainingSOPRuns()`
- `"monthly_limit": …`

Also remove the `if user.BillingMode == model.BillingModeCredits` dispatch block at line ~60 — call `getCurrentUserFromMembership` directly (since all users are credits-mode).

- [ ] **Step 2: Build**

```bash
go build ./...
```

Expected: PASS.

### Task T2.6: Delete payment.go legacy guard (numind-server)

**Files:**
- Modify: `internal/numind/biz/payment/payment.go:140-150`

- [ ] **Step 1: Delete the legacy rejection branch**

Before (line ~142):
```go
if beneficiary.BillingMode == model.BillingModeLegacyTier {
    return nil, fmt.Errorf("%w: legacy_tier users cannot buy booster", ErrLegacyBoosterDenied)
}
```

Delete this if statement entirely. Also check if `ErrLegacyBoosterDenied` error definition is now unused — if so, delete it.

- [ ] **Step 2: Build**

```bash
go build ./...
```

Expected: PASS.

### Task T2.7: Delete grant_membership Step A (numind-server)

**Files:**
- Modify: `internal/numind/biz/credit/grant_membership.go:~165-180`

- [ ] **Step 1: Find Step A**

The block looks like (around line 172):
```go
// Step A: flip billing_mode to credits for legacy users
if err := s.db.Model(&model.User{}).
    Where("id = ? AND billing_mode = ?", req.ChildUserID, model.BillingModeLegacyTier).
    Update("billing_mode", model.BillingModeCredits).Error; err != nil {
    return fmt.Errorf("flip billing_mode: %w", err)
}
```

Delete this entire block.

- [ ] **Step 2: Build**

```bash
go build ./...
```

Expected: PASS.

### Task T2.8: Frontend numind-web-v3 cleanup

**Files:**
- Modify: `numind-web-v3/src/api/credits.ts`
- Modify: `numind-web-v3/src/stores/credits.ts`
- Modify: `numind-web-v3/src/components/credit/CreditBalanceCard.vue`
- Modify: `numind-web-v3/src/components/credit/BoosterPurchaseCard.vue`

- [ ] **Step 1: api/credits.ts — remove legacy fields from QuotaBreakdown**

In `numind-web-v3/src/api/credits.ts`, find the `QuotaBreakdown` interface and delete these fields:
- `billing_mode?: 'credits' | 'legacy_tier'`
- `remaining_runs?: number | null`
- `monthly_limit?: number | null`

- [ ] **Step 2: stores/credits.ts — remove billingMode computed + legacy displayState**

In `numind-web-v3/src/stores/credits.ts`:

- Line ~52: delete `const billingMode = computed(() => balance.value?.billing_mode)`
- Line ~85: in `displayState` computed, delete the `if (b.billing_mode === 'legacy_tier') return 'legacy'` branch

If `billingMode` is exported, also remove from the return statement and from any importing component.

- [ ] **Step 3: CreditBalanceCard.vue — remove legacy template + cardState branch**

Open `numind-web-v3/src/components/credit/CreditBalanceCard.vue`:

- Delete the `<template v-else-if="cardState === 'legacy'">…</template>` block (the legacy 月度次数 display)
- Delete the `legacyUsed` computed
- In `cardState` computed, delete the `if (balance.value?.billing_mode === 'legacy_tier') return 'legacy'` line
- In the credits template, change `balance?.sub_remain` to `balance?.cycle_remaining` (which is the actual current field). Also delete the `/{{ balance?.sub_total ?? 0 }}` total display.

- [ ] **Step 4: BoosterPurchaseCard.vue — remove legacy guard**

At line ~92 in `numind-web-v3/src/components/credit/BoosterPurchaseCard.vue`, delete:

```typescript
if (bal?.billing_mode === 'legacy_tier') {
    // … rejection / disabled state …
}
```

- [ ] **Step 5: Lint + type-check**

```bash
cd /Users/zhiyuchen/Documents/10_跃迁有数/有数AI工作台/莫小派/Codes/numind-web-v3
npm run lint && npm run type-check
```

Expected: PASS (warnings about unused vars are OK, but no errors).

### Task T2.9: Frontend numind-admin-web cleanup

**Files:**
- Modify: `numind-admin-web/src/api/users.ts`
- Modify: `numind-admin-web/src/api/credits.ts`
- Delete: `numind-admin-web/src/views/MigrationsView.vue` (or its dir)
- Modify: `numind-admin-web/src/views/UsersView.vue`
- Modify: `numind-admin-web/src/views/CreditUsersView.vue`
- Modify: `numind-admin-web/src/router/index.ts` (remove MigrationsView route)
- Modify: any layout/nav component referencing MigrationsView

- [ ] **Step 1: api/users.ts — delete `updateUserTierApi` + types**

Delete `updateUserTierApi` function (line ~48), and remove `user_tier: string` from any interface (line ~8).

- [ ] **Step 2: api/credits.ts — remove legacy enum**

In the interface, change `billing_mode?: "credits" | "legacy_tier"` to just remove the field (or change to `billing_mode?: "credits"`).

- [ ] **Step 3: UsersView.vue — remove tier column + edit**

In `numind-admin-web/src/views/UsersView.vue`:

- Line ~51: delete `{ key: "user_tier", title: "等级" }` from columns array
- Line ~130: delete `selectedTier.value = user.user_tier` (and the selectedTier ref + watcher if no longer used)
- Line ~224-234: delete the user_tier badge rendering block
- Delete any "等级" related controls: dropdown, dialog, form, button

- [ ] **Step 4: CreditUsersView.vue — remove legacy banner**

In `numind-admin-web/src/views/CreditUsersView.vue`:

Delete the `<div v-if="detail.billing_mode === 'legacy_tier'">…</div>` block at line ~197-201.

- [ ] **Step 5: Delete MigrationsView**

```bash
cd /Users/zhiyuchen/Documents/10_跃迁有数/有数AI工作台/莫小派/Codes/numind-admin-web/src/views
rm MigrationsView.vue
```

(If it's in a directory, `rm -rf` the directory.)

- [ ] **Step 6: Remove MigrationsView route**

In `numind-admin-web/src/router/index.ts`, delete the route entry pointing to MigrationsView. Also search for any nav menu items referencing migrations and delete them.

- [ ] **Step 7: Lint + type-check**

```bash
cd /Users/zhiyuchen/Documents/10_跃迁有数/有数AI工作台/莫小派/Codes/numind-admin-web
npm run lint && npm run type-check 2>&1 | tail -20
```

Expected: PASS.

### Task T2.10: Documentation inline rewrite

**Files:**
- Modify: `CLAUDE.md` (root)
- Modify: `numind-server/CLAUDE.md`
- Modify: `numind-server/docs/dev-environment-setup.md`
- Modify: `numind-server/docs/credits-system-smoke-test.md`
- Modify: `numind-server/docs/credits-system-data-consistency-audit.md`

- [ ] **Step 1: CLAUDE.md (root) — replace dual-mode paragraph**

In `/Users/zhiyuchen/Documents/10_跃迁有数/有数AI工作台/莫小派/Codes/CLAUDE.md`, find the section describing the dual `billing_mode` system (around line 18). Replace the dual-mode explanation with:

```markdown
### 计费体系（credits 唯一）

每个用户使用积分制计费。每次 LLM 调用先 Reserve 预扣 → 实际完成后 Reconcile 对账（多退少补）。三池 SOT：trial_grant（试用积分）+ credit_cycle（月订阅周期）+ user_booster_balance（加量包）。

> Note: `legacy_tier` billing mode removed in 2026-05 (legacy-system-deprecation feature). Historical commits before v2.1.{T1-tag} may reference the old dual-mode system.
```

- [ ] **Step 2: numind-server/CLAUDE.md — same pattern**

Search for `legacy_tier` references in `numind-server/CLAUDE.md` and replace with the deprecation note above.

- [ ] **Step 3: docs/dev-environment-setup.md**

Search for `legacy_tier` mentions. Replace with deprecation note. Do not delete setup instructions for other billing concepts.

- [ ] **Step 4: docs/credits-system-smoke-test.md — delete Path 4 (legacy test plan)**

Find the section titled "Path 4" or similar covering legacy_tier smoke tests. Delete the section. Renumber subsequent paths if needed.

- [ ] **Step 5: docs/credits-system-data-consistency-audit.md**

Find legacy paths discussion. Add a header note at the top of the relevant section:

```markdown
> **Note**: As of 2026-05 (legacy-system-deprecation feature), `legacy_tier` billing mode no longer exists. The audit findings below are historical and reference code paths that have been removed.
```

### Task T2.11: T2 full verification

- [ ] **Step 1: Grep audit (server)**

```bash
cd numind-server
git grep -nE "GetRemainingSOPRuns|monthly_limit|remaining_runs|MonthlyLimit|RemainingRuns" -- ':!*_test.go' ':!archive/*' ':!.claude/*' ':!*.md'
git grep -nE "BillingModeLegacyTier|legacy_tier" -- 'internal/' ':!*_test.go' ':!archive/*'
```

Expected: both should return 0 hits (or only inside migrations/ SQL files, which is OK per decision 5).

- [ ] **Step 2: Grep audit (web-v3 + admin-web)**

```bash
cd numind-web-v3 && git grep -n "legacy_tier\|monthly_limit\|remaining_runs\|user_tier" -- src/
cd ../numind-admin-web && git grep -n "legacy_tier\|monthly_limit\|remaining_runs\|user_tier" -- src/
```

Expected: 0 hits in `src/` for both.

- [ ] **Step 3: Build + test all**

```bash
cd numind-server && go build ./... && task lint && task test 2>&1 | tail -10
cd ../numind-web-v3 && npm run lint && npm run type-check
cd ../numind-admin-web && npm run lint && npm run type-check
```

Expected: all PASS.

- [ ] **Step 4: Commit all three repos**

In each repo (numind-server, numind-web-v3, numind-admin-web):

```bash
git checkout -b fix/legacy-deprecation-t2-boundary
git add -A
git commit -m "refactor(legacy-deprecation): T2 boundary callers + admin + frontend

T2 of 4 for legacy-system-deprecation feature. Removes:

numind-server:
- IncrementSopRunCount method + all callers (store/customer.go)
- UpdateUserTier admin endpoint (controller + route)
- admin_migration controller (entire dir + routes)
- customer.go boundary: RemainingRuns fields removed from 3 endpoints
- user/get.go: user_tier/tier_expires/remaining_runs/monthly_limit removed
- payment.go: legacy_tier booster guard removed
- grant_membership.go: Step A billing_mode flip removed

numind-web-v3:
- api/credits.ts: QuotaBreakdown legacy fields removed
- stores/credits.ts: billingMode computed + legacy displayState removed
- CreditBalanceCard.vue: legacy template + cardState branch removed
- BoosterPurchaseCard.vue: legacy_tier guard removed

numind-admin-web:
- api/users.ts: updateUserTierApi removed
- UsersView.vue: 等级 column + edit removed
- CreditUsersView.vue: legacy banner removed
- MigrationsView.vue: entire view deleted + route removed

Plan: docs/superpowers/plans/2026-05-18-legacy-system-deprecation-plan.md T2

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"

git checkout develop && git merge --no-ff fix/legacy-deprecation-t2-boundary
git push origin develop
```

- [ ] **Step 5: Tag + deploy**

```bash
# numind-server
git tag -a v2.1.24 -m "T2: legacy deprecation - boundary callers + admin"
git push origin v2.1.24

# numind-web-v3
git tag -a v1.0.23 -m "T2: legacy deprecation - frontend cleanup"
git push origin v1.0.23

# numind-admin-web
# check that repo's tag pattern; if uses admin-v* then:
git tag -a admin-v0.0.X -m "T2: legacy deprecation - admin UI cleanup"
git push origin admin-v0.0.X
```

Tags choose next patch from `git tag --sort=-v:refname | head -1`. server first, then web-v3 + admin-web ≥1 day later per spec §9.

- [ ] **Step 6: Monitor CI + recover from GFW if needed**

Use the same `dockerproxy.net` manual recovery procedure from the hotfix runbook if `deploy_product` fails with Docker Hub timeout.

### T2 → T3 Gate: smoke verify (~15-30min)

1. Admin login → CustomerList → 子用户列表（确认 remaining_runs 字段消失但页面正常渲染）
2. Admin login → CreditUsersView（确认 legacy banner 不再出现）
3. User login → 设置页（确认 trial/cycle/booster 三池正常显示）
4. User → 触发一次 SOP 运行（确认 credit 扣减成功，不再有"积分不足"误报）
5. Admin → MigrationsView 路由（确认 404）
6. Proceed to T3 once green

---

## T3: User Model + Tests Cleanup

**Goal:** Remove methods, constants, and legacy-only tests from `pkg/model/user.go`. Struct fields STAY until T4 schema DROP.

**Scope:** numind-server only.

**Branch:** `fix/legacy-deprecation-t3-model`

### Task T3.1: Delete legacy methods on User struct

**Files:**
- Modify: `internal/pkg/model/user.go:82-219`

- [ ] **Step 1: Delete methods**

Open `internal/pkg/model/user.go` and delete these method blocks:

```go
func (u *User) GetActualUserTier() string { ... }       // ~line 82-92
func (u *User) HasActiveMembership() bool { ... }       // ~line 101-102
func (u *User) CanRunSOP() (bool, string) { ... }       // ~line 107-152
func (u *User) GetRemainingSOPRuns() int { ... }        // ~line 155-199
func (u *User) IsInNewSOPMonth() bool { ... }           // ~line 203-219
```

- [ ] **Step 2: Delete legacy constants**

In the same file (~line 50-54):

```go
const (
    UserTierFree     = "free"
    UserTierTrial    = "trial"
    UserTierStandard = "standard"
    UserTierPremium  = "premium"
)
```

Delete this block. Also delete the `TrialUserSOPLimit`, `TrialDurationDays`, `StandardUserMonthlySOPLimit` constants (~line 70-76).

- [ ] **Step 3: Delete `BillingModeLegacyTier` constant**

Same file, find:

```go
const (
    BillingModeLegacyTier = "legacy_tier"
    BillingModeCredits    = "credits"
)
```

Remove `BillingModeLegacyTier`. Keep `BillingModeCredits` for now (T4 will remove).

- [ ] **Step 4: Build**

```bash
go build ./...
```

Expected: PASS. If anything imports `model.UserTierFree` etc., revisit T1/T2 — caller should have been deleted already. Use grep to find:

```bash
git grep -n "UserTierFree\|UserTierTrial\|UserTierStandard\|UserTierPremium\|BillingModeLegacyTier" -- ':!*_test.go'
```

Expected: 0 hits.

### Task T3.2: Delete legacy-only tests

**Files:**
- Delete: `internal/pkg/model/user_billing_mode_test.go` (entire file)
- Modify: other `*_test.go` files identified via grep

- [ ] **Step 1: Delete user_billing_mode_test.go**

```bash
rm internal/pkg/model/user_billing_mode_test.go
```

- [ ] **Step 2: Find remaining legacy test references**

```bash
git grep -nE "UserTierFree|UserTierTrial|UserTierStandard|UserTierPremium|BillingModeLegacyTier|CanRunSOP|GetRemainingSOPRuns|HasActiveMembership|IsInNewSOPMonth|GetActualUserTier" -- '*_test.go'
```

- [ ] **Step 3: For each hit, delete the test or the legacy branch**

If the entire test function is about legacy behavior, delete the function.

If the test has dual-mode branches (`t.Run("credits", ...)`, `t.Run("legacy", ...)`), keep credits branch, delete legacy branch.

If the test uses legacy constants only as setup data (e.g., creating a user with UserTier=premium), replace with credits-mode setup or delete the test if it's testing legacy semantics.

- [ ] **Step 4: Run full test suite**

```bash
task test 2>&1 | tail -30
```

Expected: all PASS.

### Task T3.3: T3 verification + commit + deploy

- [ ] **Step 1: Grep audit (full)**

```bash
git grep -nE "isEffectiveLegacy|legacyTierImpl|HasActiveMembership|CanRunSOP|GetRemainingSOPRuns|GetActualUserTier|IsInNewSOPMonth|UserTierFree|UserTierTrial|UserTierStandard|UserTierPremium|BillingModeLegacyTier" -- ':!*.md' ':!archive/*' ':!.claude/*'
```

Expected: 0 hits across all files including tests.

- [ ] **Step 2: Full build + lint + test**

```bash
go build ./... && task lint && task test 2>&1 | tail -10
```

Expected: all PASS.

- [ ] **Step 3: Commit + tag + deploy (server only)**

```bash
git checkout -b fix/legacy-deprecation-t3-model
git add -A
git commit -m "refactor(legacy-deprecation): T3 remove legacy User methods + tests

T3 of 4. Removes from internal/pkg/model/user.go:
- GetActualUserTier / HasActiveMembership / CanRunSOP /
  GetRemainingSOPRuns / IsInNewSOPMonth methods
- UserTier{Free,Trial,Standard,Premium} constants
- TrialUserSOPLimit / TrialDurationDays / StandardUserMonthlySOPLimit
- BillingModeLegacyTier constant

Struct fields (UserTier, TierExpires, MonthlySopRuns, MonthlyResetAt,
BillingMode) retained until T4 schema DROP.

Deletes user_billing_mode_test.go and prunes legacy branches from
remaining dual-mode tests.

Plan: docs/superpowers/plans/2026-05-18-legacy-system-deprecation-plan.md T3

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"

git checkout develop && git merge --no-ff fix/legacy-deprecation-t3-model
git push origin develop
git tag -a v2.1.25 -m "T3: legacy deprecation - user model cleanup"
git push origin v2.1.25
```

### T3 → T4 Gate: smoke verify (~10min)

T3 is internal cleanup, no runtime behavior change. Verify:
1. `curl -s https://youshu.asia/healthz` → "healthy"
2. SSH prod docker logs no `nil pointer` / `undefined` errors
3. Proceed to T4 prep (backup + dry-run) once green

---

## T4: Schema DROP Migration

**Goal:** Drop 5 columns from `user` table; rename `tier_change_log` to `legacy_tier_change_log`; remove struct fields from `model.User`.

**Scope:** numind-server only + DB.

**Branch:** `fix/legacy-deprecation-t4-schema`

### Task T4.1: Pre-flight prod backup

- [ ] **Step 1: Trigger prod DB backup**

```bash
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no root@129.28.125.51 << 'EOF'
MYSQL_CONTAINER=$(docker ps --format '{{.Names}}' | grep mysql | head -1)
TIMESTAMP=$(date -u +%Y%m%dT%H%M%SZ)
docker exec $MYSQL_CONTAINER mysqldump -u root -pNumind2025 numind-prod \
  --single-transaction --quick > /root/backups/numind-prod-pre-t4-$TIMESTAMP.sql
ls -lh /root/backups/numind-prod-pre-t4-$TIMESTAMP.sql
EOF
```

Expected: backup file > 50MB (sanity).

- [ ] **Step 2: Verify backup restorable in dev DB**

```bash
sshpass -p "$DEV_SSH_PASS" ssh -o StrictHostKeyChecking=no root@49.233.219.254 << 'EOF'
# scp backup over (or use rsync)
# then:
docker exec mysql-dev mysql -u root -pNumind2025 -e "CREATE DATABASE IF NOT EXISTS numind_t4_drill"
docker exec -i mysql-dev mysql -u root -pNumind2025 numind_t4_drill < /tmp/numind-prod-pre-t4-*.sql
docker exec mysql-dev mysql -u root -pNumind2025 numind_t4_drill -e "SELECT COUNT(*) FROM user"
EOF
```

Expected: 246 users restored.

### Task T4.2: Audit data state (final check before DROP)

- [ ] **Step 1: Confirm 0 users in legacy state**

```bash
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no root@129.28.125.51 \
  "docker exec -i \$(docker ps --format '{{.Names}}' | grep mysql | head -1) \
   mysql -u root -pNumind2025 numind-prod -N -e \
   'SELECT COUNT(*) FROM user WHERE user_tier IS NOT NULL AND user_tier != \"free\" OR billing_mode != \"credits\"'"
```

Expected: `0`. If non-zero, STOP and update T4 plan.

### Task T4.3: Write migration SQL

**Files:**
- Create: `internal/numind/migrations/{date}_{time}_drop_legacy_user_columns.sql`
- Create: `internal/numind/migrations/{date}_{time}_drop_legacy_user_columns_rollback.sql`

- [ ] **Step 1: Create forward migration**

```bash
DATE=$(date +%Y%m%d_%H%M%S)
cat > "migrations/${DATE}_drop_legacy_user_columns.sql" <<'EOF'
-- T4 of legacy-system-deprecation feature.
-- Pre-conditions verified:
--   1. server / web-v3 / admin-web all >= T3 tags, soaked 3+ days
--   2. SELECT COUNT(*) FROM user WHERE user_tier!='free' OR billing_mode!='credits' = 0
--   3. Prod DB backup verified, dev restore drill passed

ALTER TABLE `user`
  DROP COLUMN `user_tier`,
  DROP COLUMN `tier_expires`,
  DROP COLUMN `monthly_sop_runs`,
  DROP COLUMN `monthly_reset_at`,
  DROP COLUMN `billing_mode`;

-- Rename audit table (retention 1 year per spec §12 deferred-decision (b))
RENAME TABLE `tier_change_log` TO `legacy_tier_change_log`;
EOF
```

- [ ] **Step 2: Create rollback migration**

```bash
cat > "migrations/${DATE}_drop_legacy_user_columns_rollback.sql" <<'EOF'
-- Rollback for T4 schema DROP.
-- NOTE: DROP loses data. This rollback only restores schema. To restore data,
-- run mysqldump restore from the pre-T4 backup at /root/backups/numind-prod-pre-t4-*.sql.

ALTER TABLE `user`
  ADD COLUMN `billing_mode` ENUM('legacy_tier','credits') NOT NULL DEFAULT 'credits',
  ADD COLUMN `monthly_sop_runs` INT DEFAULT 0,
  ADD COLUMN `monthly_reset_at` TIMESTAMP NULL DEFAULT NULL,
  ADD COLUMN `user_tier` VARCHAR(20) DEFAULT 'free',
  ADD COLUMN `tier_expires` TIMESTAMP NULL DEFAULT NULL;

ALTER TABLE `user`
  ADD INDEX `idx_user_billing_mode` (`billing_mode`),
  ADD INDEX `idx_user_tier` (`user_tier`);

RENAME TABLE `legacy_tier_change_log` TO `tier_change_log`;
EOF
```

### Task T4.4: Dry-run migration on dev DB

- [ ] **Step 1: Run forward migration in dev**

```bash
sshpass -p "$DEV_SSH_PASS" ssh -o StrictHostKeyChecking=no root@49.233.219.254 << 'EOF'
MYSQL_CONTAINER=$(docker ps --format '{{.Names}}' | grep mysql | head -1)
docker cp migrations/${DATE}_drop_legacy_user_columns.sql ${MYSQL_CONTAINER}:/tmp/
docker exec -i $MYSQL_CONTAINER mysql -u root -pNumind2025 numind-dev < /tmp/${DATE}_drop_legacy_user_columns.sql
docker exec $MYSQL_CONTAINER mysql -u root -pNumind2025 numind-dev -e "DESCRIBE user"
EOF
```

Expected: `DESCRIBE user` no longer shows user_tier / tier_expires / monthly_sop_runs / monthly_reset_at / billing_mode columns.

- [ ] **Step 2: Run dev smoke tests**

Trigger smoke test suite or manual curl tests against dev backend.

Expected: all PASS. If anything fails, fix and re-test before prod.

- [ ] **Step 3: Run rollback in dev (verify reversibility)**

```bash
sshpass -p "$DEV_SSH_PASS" ssh -o StrictHostKeyChecking=no root@49.233.219.254 \
  "docker exec -i \$(docker ps --format '{{.Names}}' | grep mysql | head -1) \
   mysql -u root -pNumind2025 numind-dev < /tmp/${DATE}_drop_legacy_user_columns_rollback.sql"
```

Then:

```bash
sshpass -p "$DEV_SSH_PASS" ssh -o StrictHostKeyChecking=no root@49.233.219.254 \
  "docker exec \$(docker ps --format '{{.Names}}' | grep mysql | head -1) \
   mysql -u root -pNumind2025 numind-dev -e 'DESCRIBE user'"
```

Expected: 5 columns back, columns are NULL/default for all rows.

- [ ] **Step 4: Re-run forward migration to bring dev to final state**

```bash
sshpass -p "$DEV_SSH_PASS" ssh -o StrictHostKeyChecking=no root@49.233.219.254 \
  "docker exec -i \$(docker ps --format '{{.Names}}' | grep mysql | head -1) \
   mysql -u root -pNumind2025 numind-dev < /tmp/${DATE}_drop_legacy_user_columns.sql"
```

### Task T4.5: Remove struct fields from model.User

**Files:**
- Modify: `internal/pkg/model/user.go`

- [ ] **Step 1: Delete fields from struct**

In `internal/pkg/model/user.go`, in the `type User struct` block, delete these 5 fields (matching spec §5 DROP list):

```go
MonthlySopRuns int        `gorm:"default:0" json:"monthly_sop_runs"`
MonthlyResetAt *time.Time `gorm:"index" json:"monthly_reset_at"`
UserTier    string     `gorm:"size:20;default:'free';index" json:"user_tier"`
TierExpires *time.Time `gorm:"index" json:"tier_expires"`
BillingMode string `gorm:"column:billing_mode;type:enum('legacy_tier','credits');not null;default:'credits';index:idx_user_billing_mode,priority:1" json:"billing_mode"`
```

**Keep** `TotalSopRuns` (used for cumulative analytics, not a legacy field).

- [ ] **Step 2: Delete `BillingModeCredits` constant**

Same file:

```go
const (
    BillingModeCredits = "credits"
)
```

Delete this. Also remove any remaining references via grep.

- [ ] **Step 3: Build**

```bash
go build ./...
```

Expected: PASS. If callers still reference `user.UserTier` / `user.BillingMode` / etc., go back to T1/T2/T3 — those callers should already have been removed.

### Task T4.6: T4 verification

- [ ] **Step 1: Final grep audit**

```bash
git grep -nE "user_tier|tier_expires|monthly_sop_runs|monthly_reset_at|billing_mode|BillingMode|UserTier|TierExpires|MonthlySopRuns|MonthlyResetAt" -- ':!*.md' ':!archive/*' ':!.claude/*' ':!migrations/*'
```

Expected: 0 hits.

- [ ] **Step 2: Build + test**

```bash
go build ./... && task lint && task test 2>&1 | tail -10
```

Expected: PASS.

### Task T4.7: Deploy T4 with announcement

- [ ] **Step 1: Commit code change**

```bash
git checkout -b fix/legacy-deprecation-t4-schema
git add internal/pkg/model/user.go migrations/${DATE}_drop_legacy_user_columns*.sql
git commit -m "refactor(legacy-deprecation): T4 DROP user_tier/tier_expires/monthly_sop_runs/monthly_reset_at/billing_mode

T4 of 4 (final). Schema migration: DROP 5 columns from user table,
rename tier_change_log to legacy_tier_change_log (1-year retention).
Model struct fields removed.

Pre-conditions verified:
- T1/T2/T3 prod soaked 7+ days each, no regressions
- Prod backup at /root/backups/numind-prod-pre-t4-{ts}.sql
- Dev dry-run forward + rollback + forward all PASS

Rollback: ALTER TABLE ADD COLUMN restores schema; data restore from
backup file. Procedure in spec §8.2.

Plan: docs/superpowers/plans/2026-05-18-legacy-system-deprecation-plan.md T4

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"

git checkout develop && git merge --no-ff fix/legacy-deprecation-t4-schema
git push origin develop
```

- [ ] **Step 2: 5-min announcement + tag**

Send announcement to admins (manual). Wait 5 minutes.

```bash
git tag -a v2.1.26 -m "T4: legacy deprecation - schema DROP (final)"
git push origin v2.1.26
```

- [ ] **Step 3: After CI deploys T4 server image, run migration on prod**

Migration via GORM AutoMigrate (preferred if framework auto-runs) OR manual SQL execution:

```bash
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no root@129.28.125.51 << EOF
MYSQL_CONTAINER=\$(docker ps --format '{{.Names}}' | grep mysql | head -1)
docker cp /tmp/${DATE}_drop_legacy_user_columns.sql \${MYSQL_CONTAINER}:/tmp/
docker exec -i \$MYSQL_CONTAINER mysql -u root -pNumind2025 numind-prod < /tmp/${DATE}_drop_legacy_user_columns.sql
docker exec \$MYSQL_CONTAINER mysql -u root -pNumind2025 numind-prod -e "DESCRIBE user"
EOF
```

Expected: DESCRIBE user no longer shows the 5 dropped columns.

- [ ] **Step 4: Smoke test prod**

```bash
curl -s https://youshu.asia/healthz   # expect: healthy
# Hit a credits-mode endpoint via gh / Playwright; verify response.
```

- [ ] **Step 5: Update manifest to completed**

In `build-manifest.yaml`, mark `legacy-system-deprecation`:
- `stage: "completed"`
- `progress.completed_tasks: 4`
- `completed_at: "{date}T{time}+08:00"`

Commit:

```bash
git add build-manifest.yaml
git commit -m "chore(manifest): legacy-system-deprecation completed (T1-T4 all prod)"
git push origin develop
```

---

## Final Acceptance (after all 4 tasks)

Run from `numind-server`:

- [ ] **Step A1: Spec §11 verification checklist**

Walk through all 12 acceptance criteria in spec §11. Each should be a green check.

- [ ] **Step A2: Prod schema audit**

```bash
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no root@129.28.125.51 \
  "docker exec \$(docker ps --format '{{.Names}}' | grep mysql | head -1) \
   mysql -u root -pNumind2025 numind-prod -e 'DESCRIBE user; SHOW TABLES LIKE \"%tier%\";'"
```

Expected:
- `user` table no longer has 5 columns
- `legacy_tier_change_log` table exists

- [ ] **Step A3: Prod e2e Playwright smoke**

Trigger the existing Playwright suite (with cookies imported, see `setup-browser-cookies` skill if needed):

```bash
cd numind-web-v3 && npx playwright test e2e/critical-paths.spec.ts
```

Expected: all PASS.

---

## Open Questions Resolution (deferred from S2 §12)

(a) **`admin_user/user.go:273-293` caller grep** — RESOLVED in pre-T2: endpoint is the dedicated `UpdateUserTier`, called only by admin-web `updateUserTierApi`. T2.2 deletes the entire endpoint + route + frontend caller. Plan reflects this.

(b) **`legacy_tier_change_log` retention timing** — Deferred: 1 year from T4 deploy. Add calendar reminder for `{T4-deploy-date + 1 year}` to evaluate DROP. Spec §10 risk register tracks.

(c) **CLAUDE.md rewrite timing** — RESOLVED: T2.10 covers inline rewrite during T2 (server first wave). Plan reflects this.

---

## Verification Commands Cheat Sheet

```bash
# Full audit (run after each task; expect 0 hits when complete)
cd numind-server
git grep -nE "isEffectiveLegacy|legacyTierImpl|HasActiveMembership|CanRunSOP|GetRemainingSOPRuns|GetActualUserTier|IsInNewSOPMonth|UserTierFree|UserTierTrial|UserTierStandard|UserTierPremium|BillingModeLegacyTier" -- ':!*.md' ':!archive/*' ':!.claude/*' ':!migrations/*'

# After T4
git grep -nE "user_tier|tier_expires|monthly_sop_runs|monthly_reset_at|BillingMode|UserTier" -- ':!*.md' ':!archive/*' ':!.claude/*' ':!migrations/*'

# Frontends
cd ../numind-web-v3 && git grep -n "legacy_tier\|monthly_limit\|remaining_runs\|user_tier" -- src/
cd ../numind-admin-web && git grep -n "legacy_tier\|monthly_limit\|remaining_runs\|user_tier" -- src/
```

All should return 0 after T4.
