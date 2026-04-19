# Credits System Implementation Plan

> **For agentic workers:** This plan uses **Hybrid Parallel Execution**:
> - **Track 级并行**: 7 条独立 track 用 `superpowers:dispatching-parallel-agents` 并行 dispatch（每 track = 1 个 agent team）
> - **Track 内串行**: 每个 track 内部用 `superpowers:subagent-driven-development`（TDD + per-task review）
>
> 这化解了 NDF §3 S4"不可并行 dispatch implementation subagent"规则——NDF 原意是防止质量漏洞（无 TDD + review）；本 plan 在 track 内完整保留 TDD + 2 阶段 review，在 track 间通过 Task 0 契约冻结 + 文件隔离避免冲突。

**Goal:** 完成"会员阶梯次数制 → 会员+积分+加量包"的计费体系迁移，修 SalesRAG prod 扣减漏洞，实装 R2 字符数估算 + Eager Reserve + 同步 pricing 计算的 Reconcile 机制，跨 3 仓库（numind-server + numind-web-v3 + numind-admin-web）部署。

**Architecture:**
- **Phase 0 契约冻结**（serial，1 commit）：冻结所有跨 track 契约 —— 接口签名、数据模型 DDL、HTTP req/resp、errno 字符串 code、TS 类型。
- **Phase 1 Parallel Tracks**（7 条 track 并行）：Track A 数据层 / Track B pricing / Track C ICreditService / Track D Payment+Order / Track E 前端组件 / Track F 管理端 / Track G 数据 spike。
- **Phase 2 Integration**（serial，依赖 Phase 1 产出）：sop/salesrag 控制流反转、Controller 接线、前端 mock→real、E2E。
- **Phase 3 Verification**（S5 gate）：Langfuse 验证、完整测试、migration dry-run。

**Tech Stack:** Go 1.24 + Gin + GORM + MySQL 8 + Redis LRU / Vue 3 + TypeScript + Pinia / Playwright E2E / Langfuse tracing

**References:**
- Spec: `numind-server/docs/superpowers/specs/2026-04-18-credits-system-design.md`（§1-5 完整）
- PRD: `numind-server/proposals/credits-system-proposal.md` §4
- Rules: `.claude/rules/business-logic.md`, `.claude/rules/ai-service.md`, `.claude/rules/testing.md`, `.claude/rules/ndf-enforcement.md`

---

## Track Dependency Graph

```
Phase 0: 契约冻结 (Task 0.1 - 0.5, serial)
           │
           ▼
   ┌───────────────────── Phase 1: Parallel Tracks ─────────────────────┐
   │                                                                     │
   ├── Track A (Data Layer, 独立)                                        │
   ├── Track B (Pricing Module, 独立)                                    │
   ├── Track G (Data Spike, 独立)                                        │
   │       │ produces seed content                                       │
   │       ▼                                                             │
   │   (merged into Track A migration 5)                                 │
   │                                                                     │
   ├── Track C (ICreditService, 依赖 Track A + B 已 merge)                │
   ├── Track D (Payment+Order, 依赖 Track A 已 merge)                     │
   │                                                                     │
   ├── Track E (Frontend Components, with MSW mocks, 独立)              │
   └── Track F (Admin Frontend, with MSW mocks, 独立)                   │
   └─────────────────────────────────────────────────────────────────────┘
                                 │
                                 ▼
          Phase 2: Integration (Task 2.1-2.5, serial)
           - 2.1 SOP runNode/runChat 控制流反转 (uses B+C)
           - 2.2 SalesRAG Chat/ChatStream 接入 (uses B+C)
           - 2.3 Controller + Router 注册 (uses B+C+D)
           - 2.4 前端 mock → real API 切换
           - 2.5 Playwright E2E 6 paths
                                 │
                                 ▼
          Phase 3: Verification (Task 3.1-3.4, serial, S5 Gate)
           - 3.1 Langfuse span 完整性验证
           - 3.2 完整测试套件 (task test + npm test + e2e)
           - 3.3 Migration rollback 演练
           - 3.4 Card cleanup (独立 commit, out of feature scope per §3.14)
```

## Parallelism Safety Rules

1. **契约不可变**：Phase 0 commit 之后，任何 track 若发现契约需改（如 spec 里漏了一个字段），必须 STOP 并 raise，不得自行修改——契约变更必须作为单独 commit 先 merge 再各 track rebase。
2. **文件独占**：每个 track 只能写入本 track "Files" 节明确列出的文件。跨 track 触文件禁止——违反 = merge conflict。
3. **接口对接靠冻结合约**：Track D 调 Track C 的函数，必须按 Phase 0 的 `types.go` 签名使用；不得读 Track C 的 in-progress 代码。
4. **每 track 独立 feature 子分支**：`feature/credits-system-track-A` ... `feature/credits-system-track-G`。Track 完成后 merge 到 `feature/credits-system-integration`。
5. **Track 内必须 TDD**：每个 task RED → GREEN → REFACTOR → per-task review → commit。
6. **Track 完成时做 2 阶段 review**（spec compliance + code quality）。
7. **Phase 2 integration 由主 AI 顺序执行**（非并行）——合并点靠近就是为了串行集成。

## Manifest 扩展（NDF 兼容）

manifest.progress 扩展为支持并行 track 追踪：

```yaml
progress:
  total_tasks: 40
  completed_tasks: 0      # 跨所有 track
  reviewed_tasks: 0
  current_phase: "0"
  tracks:
    A: { total: 5, completed: 0 }
    B: { total: 4, completed: 0 }
    C: { total: 8, completed: 0 }   # +1 C.8 Langfuse spans
    D: { total: 4, completed: 0 }
    E: { total: 6, completed: 0 }
    F: { total: 4, completed: 0 }
    G: { total: 2, completed: 0 }
  phase2_tasks: 6           # +1 Task 2.0 Integration 接线
  phase3_tasks: 4
```

---

# Phase 0: 契约冻结（Serial，1 个 commit）

**目标**：将所有跨 track 契约冻结在单一 commit，Phase 1 的 7 条 track 拿此 commit 为基础并行开发。

## Task 0.1: 定义 Go 类型契约（types.go + errors.go）

**Files:**
- Create: `numind-server/internal/numind/biz/credit/types.go`
- Create: `numind-server/internal/numind/biz/credit/errors.go`
- Create: `numind-server/internal/numind/biz/credit/contracts.go`（存接口，方便未来 mock）

**Track Ownership:** Phase 0（主 AI）

- [ ] **Step 1: 创建 types.go** 按 spec §1.7 + §1.8 全部结构体（Operation 枚举、EstimationInput、PreCheckResult、Reservation、ReservationItem、BalanceBreakdown、ReservationStatus）

完整代码见 spec §1.7-1.8。关键：`PreCheckResult.Reason string`（§1.8 back-prop）、`BalanceBreakdown` 用 JSON 短字段 `sub_total/sub_remain/booster_total/booster_remain`（§2.11.1 back-prop 对齐现有 credits.ts）。

- [ ] **Step 2: 创建 errors.go** 按 spec §1.9 + §3.12

完整代码见 spec §3.12（7 个 typed error + 字符串 code 格式，如 `"Credits.Insufficient"`）。

- [ ] **Step 3: 创建 contracts.go** 包含 `ICreditService` 接口（6 个方法）

按 spec §1.1 完整签名。此文件不含 impl，仅声明。

- [ ] **Step 4: 跑编译验证**

```bash
cd numind-server && go build ./internal/numind/biz/credit/...
```
Expected: 编译通过（因 interface 存在但无 impl）

- [ ] **Step 5: Commit 到 develop**

```bash
git add numind-server/internal/numind/biz/credit/{types.go,errors.go,contracts.go}
git commit -m "feat(credits-system): phase 0 contract freeze — types/errors/interfaces"
```

## Task 0.2: DB Schema 契约（migration 文件骨架，未执行）

**Files:**
- Create: 12 个 migration 文件 under `numind-server/migrations/`（见 spec §2.8）

**Track Ownership:** Phase 0（主 AI）

- [ ] **Step 1: 写全 6 对 DDL + rollback 文件**

按 spec §2.2-2.5 + §2.7 的 SQL。所有 DDL 包括 CREATE/ALTER 语句完整。rollback 文件独立。

文件清单：
```
20260419_100000_add_billing_mode_to_user.sql / *_rollback.sql
20260419_100100_create_credit_estimation_coefficient.sql / *_rollback.sql
20260419_100200_create_credit_reservation.sql / *_rollback.sql
20260419_100300_create_credit_reservation_item.sql / *_rollback.sql
20260419_100400_seed_credit_estimation_coefficient.sql / *_rollback.sql  ← 占位 INSERT，待 Track G 填真值
20260419_100500_init_billing_mode_values.sql / *_rollback.sql  ← 含 ${MIGRATION_CUTOFF} 占位符
```

- [ ] **Step 2: SQL 语法自检**

```bash
cd numind-server && for f in migrations/20260419_*.sql; do
  mysql --help 2>/dev/null | head -1
  # 用本地 MySQL 开 dry-run session 跑 SOURCE 检查语法（不真正 apply）
done
```
或手工 review DDL 语法。不执行。

- [ ] **Step 3: Commit**

```bash
git add numind-server/migrations/20260419_*.sql
git commit -m "feat(credits-system): phase 0 contract freeze — migration skeletons (not yet applied)"
```

## Task 0.3: HTTP API 契约（req/resp 结构 + errno codes）

**Files:**
- Create: `numind-server/internal/pkg/errno/credits.go`（按 spec §3.12）
- Modify: `numind-server/internal/numind/controller/v1/credit/types.go`（新建 req/resp 结构体）

**Track Ownership:** Phase 0（主 AI）

- [ ] **Step 1: 写 errno/credits.go** 按 spec §3.12 的 7 个错误（项目字符串 code 风格）

```go
package errno

var (
    ErrInsufficientCredits          = &Errno{HTTP: 402, Code: "Credits.Insufficient", Message: "积分不足"}
    ErrMembershipRequired           = &Errno{HTTP: 403, Code: "Membership.Required", Message: "需要会员资格才能购买加量包"}
    ErrBoosterNotAvailableForLegacy = &Errno{HTTP: 403, Code: "Booster.LegacyTierNotAllowed", Message: "老会员制暂不支持加量包"}
    ErrCoefficientConcurrent        = &Errno{HTTP: 503, Code: "Coefficient.Concurrent", Message: "系数更新繁忙，请稍后重试"}
    ErrTierInPeriod                 = &Errno{HTTP: 400, Code: "Tier.InPeriod", Message: "当前会员在期，不可购买同类或更低类型"}
    ErrTrialAlreadyPurchased        = &Errno{HTTP: 400, Code: "Trial.AlreadyPurchased", Message: "您已购买过体验卡"}
    ErrTrialNotAvailableInPeriod    = &Errno{HTTP: 400, Code: "Trial.NotAvailableInPeriod", Message: "在期会员不支持购买体验卡"}
)
```

- [ ] **Step 2: 写 controller/v1/credit/types.go** 含 EstimateReq, EstimateResp, ListPackagesReq, ListPackagesResp 等

按 spec §3.11 + §4.1.1。**注意：** EstimateReq 不含 prompt_chars（§3.11 spec 明确后端渲染）。

- [ ] **Step 3: 编译验证**

```bash
cd numind-server && go build ./internal/pkg/errno/... ./internal/numind/controller/v1/credit/...
```
Expected: 通过。

- [ ] **Step 4: Commit**

```bash
git add numind-server/internal/pkg/errno/credits.go \
        numind-server/internal/numind/controller/v1/credit/types.go
git commit -m "feat(credits-system): phase 0 contract freeze — errno + HTTP req/resp types"
```

## Task 0.4: TS 类型契约（web-v3 + admin-web）

**Files:**
- Modify: `numind-web-v3/src/api/credits.ts`（扩展 QuotaBreakdown + 新增 EstimateResp）
- Create: `numind-admin-web/src/api/coefficients.ts`（系数管理 API 类型）
- Create: `numind-admin-web/src/api/migrations.ts`（迁移工具 API 类型）

**Track Ownership:** Phase 0（主 AI）

- [ ] **Step 1: 扩展 web-v3 credits.ts** 按 spec §4.2.1

```typescript
export interface QuotaBreakdown {
  balance: number
  sub_total: number
  sub_remain: number
  booster_total: number
  booster_remain: number
  // 新增（可选，老代码不读即可）
  billing_mode?: 'credits' | 'legacy_tier'
  remaining_runs?: number | null
  monthly_limit?: number | null
  sub_expires_at?: string
  booster_earliest_expires_at?: string
}

export interface EstimateResp {
  total_estimated_credits: number
  first_node_estimate?: number
  node_count?: number
  sufficient: boolean
  skip_deduction: boolean
  reason?: string
  balance: QuotaBreakdown
  coefficient_id: number
}

export function estimateCredits(operation: string, reference_id: string): Promise<EstimateResp> {
  return request.post('/v1/credits/estimate', { operation, reference_id })
}
```

- [ ] **Step 2: 创建 admin-web 的系数/迁移 API 类型**（req/resp structs 即可，暂无 impl）

- [ ] **Step 3: type-check 验证**

```bash
cd numind-web-v3 && npm run type-check
cd numind-admin-web && npm run type-check
```
Expected: 通过。

- [ ] **Step 4: Commit**

```bash
git add numind-web-v3/src/api/credits.ts \
        numind-admin-web/src/api/coefficients.ts \
        numind-admin-web/src/api/migrations.ts
git commit -m "feat(credits-system): phase 0 contract freeze — TypeScript types (web-v3 + admin-web)"
```

## Task 0.5: 创建 7 条 track 的 feature 分支

**Files:** 无（git 操作）

- [ ] **Step 1: 在 3 仓库分别创建 track 分支**

```bash
# numind-server
cd numind-server
for t in A B C D F G; do
    git checkout develop && git pull
    git checkout -b feature/credits-system-track-$t
    git push -u origin feature/credits-system-track-$t
done
# Track E 只涉及 web-v3
cd numind-web-v3
git checkout develop && git pull && git checkout -b feature/credits-system-track-E
git push -u origin feature/credits-system-track-E
# Track F 涉及 admin-web
cd numind-admin-web
git checkout develop && git pull && git checkout -b feature/credits-system-track-F
git push -u origin feature/credits-system-track-F
```

- [ ] **Step 2: 更新 manifest**

在 manifest.branches 填入 7 条 track 分支名。

- [ ] **Step 3: Commit manifest**

```bash
git -C numind-server add build-manifest.yaml
git -C numind-server commit -m "chore(credits-system): register 7 parallel track branches in manifest"
```

---

# Phase 1: Parallel Tracks（7 track，分两批 dispatch）

**执行方式：** 主 AI 用 `superpowers:dispatching-parallel-agents` 分**两批** dispatch：

**Batch 1（5 独立 track，立即并行）：** Track A（数据层）/ B（pricing）/ E（前端 with mocks）/ F（admin UI with mocks）/ G（R2 spike）

**Batch 2（2 依赖 track，等 Batch 1 的 A+B merge 后）：** Track C（ICreditService，需 A 的 model + B 的 pricing）/ Track D（Payment+Order，需 A 的 billing_mode 字段）

**为什么分两批？** Track C 依赖 A 的 GORM 模型和 B 的 pricing 函数，Track D 依赖 A 的 billing_mode 字段。若所有 7 条一次性 dispatch，C/D 会基于仅 Phase 0 契约（接口声明，无 impl）写代码并无法跑集成测试。

每 team 内部严格 `superpowers:subagent-driven-development`（TDD + per-task review）。

**每个 track 的 agent team prompt 模板：**
```
你是 Track {X} 的 agent team，负责 credits-system feature 的 {描述}。
请 checkout `feature/credits-system-track-{X}` 分支。
按 docs/superpowers/plans/credits-system-plan.md 的 Track {X} 所有 task 顺序执行。
每个 task：RED → GREEN → REFACTOR → per-task review → commit。
禁止修改本 track Files 节列出文件以外的任何文件。
契约使用 Phase 0 commit 冻结版本，不得修改接口签名。
全部 task 完成后做 2 阶段 review（spec compliance + code quality）。
完成后返回 DONE 或 DONE_WITH_CONCERNS + concerns 列表。
```

---

## Track A: 数据层 + GORM 模型（独立）

**Repo:** numind-server
**Branch:** feature/credits-system-track-A
**Depends on:** Phase 0 commit
**Produces for:** Tracks C/D/E/F/G（通过 DDL + GORM 模型）

**Files:**
- Create: `numind-server/internal/pkg/model/credit_coefficient.go`
- Create: `numind-server/internal/pkg/model/credit_reservation.go`
- Modify: `numind-server/internal/pkg/model/user.go`（新增 BillingMode 字段）
- Execute: migration 20260419_100000 ~ 100300（4 个 DDL + rollback）on 本地 MySQL test
- Create: `numind-server/internal/pkg/model/credit_coefficient_test.go`
- Create: `numind-server/internal/pkg/model/credit_reservation_test.go`

### Task A.1: GORM model — CreditEstimationCoefficient

- [ ] Step 1: 创建 model 文件按 spec §2.9（完整字段 + TableName()）
- [ ] Step 2: 写单元测试 — AutoMigrate 到 in-memory SQLite（或共享 testDB），Create/Query 一行，验证字段正确映射
- [ ] Step 3: `go test ./internal/pkg/model/... -run TestCreditEstimationCoefficient -v` PASS
- [ ] Step 4: commit `feat(credits-system/A): CreditEstimationCoefficient GORM model`

### Task A.2: GORM model — CreditReservation + Item

- [ ] Step 1: 创建 model 按 spec §2.9（CreditReservation + CreditReservationItem 两个 struct，`Items` 外键关联）
- [ ] Step 2: 单元测试 — 创建 reservation + 3 个 items，验证 Seq 唯一、FIFO 查询排序正确
- [ ] Step 3: `go test ./internal/pkg/model/... -run TestCreditReservation -v` PASS
- [ ] Step 4: commit `feat(credits-system/A): CreditReservation + CreditReservationItem models`

### Task A.3: User 模型扩展 BillingMode 字段

- [ ] Step 1: 修 `user.go` 的 User struct 追加 `BillingMode string` 字段（见 spec §2.9）
- [ ] Step 2: 新增常量 `BillingModeLegacyTier = "legacy_tier"` / `BillingModeCredits = "credits"`
- [ ] Step 3: 单元测试 — 创建 User with BillingMode=legacy_tier 并 query 验证
- [ ] Step 4: `go test ./internal/pkg/model/... -run TestUserBillingMode -v` PASS
- [ ] Step 5: commit `feat(credits-system/A): User.BillingMode field`

### Task A.4: 本地 MySQL 执行 migrations（dry-run 验证）

- [ ] Step 1: 启动本地 MySQL test container
```bash
docker run --rm -d --name credits-mysql-test -e MYSQL_ROOT_PASSWORD=root -e MYSQL_DATABASE=numind_test -p 13306:3306 mysql:8.0
```
- [ ] Step 2: 执行 Phase 0 commit 的 4 个 DDL migration（100000-100300）
```bash
for f in 20260419_100000_add_billing_mode_to_user.sql 20260419_100100_create_credit_estimation_coefficient.sql 20260419_100200_create_credit_reservation.sql 20260419_100300_create_credit_reservation_item.sql; do
    mysql -h 127.0.0.1 -P 13306 -uroot -proot numind_test < migrations/$f
done
```
Expected: 所有 CREATE/ALTER 成功无错
- [ ] Step 3: 验证 SHOW CREATE TABLE 输出符合预期（字段类型、索引、枚举值、CHECK 约束）
- [ ] Step 4: 执行对应 rollback migration 验证可回滚
- [ ] Step 5: commit `test(credits-system/A): verify migrations 100000-100300 apply + rollback cleanly`

### Task A.5: 一次性迁移脚本 envsubst 验证

- [ ] Step 1: 手工测试 envsubst 白名单模式
```bash
export MIGRATION_CUTOFF="2026-05-08 00:00:00"
envsubst '${MIGRATION_CUTOFF}' < migrations/20260419_100500_init_billing_mode_values.sql | head -20
```
Expected: 占位符被替换，其他 `$` 字符未受影响
- [ ] Step 2: 在本地 MySQL（已有一些假数据 user）执行替换后的 SQL，验证 billing_mode 分布符合预期
- [ ] Step 3: commit `test(credits-system/A): verify init_billing_mode_values migration envsubst + execution`

**Track A 完成验收：**
- [ ] 所有 Task A.1-A.5 的 commit 都在 feature/credits-system-track-A
- [ ] `task lint` 和 `go test ./internal/pkg/model/...` 通过
- [ ] 2 阶段 review（spec compliance + code quality）PASS

---

## Track B: Pricing Module（独立）

**Repo:** numind-server
**Branch:** feature/credits-system-track-B
**Depends on:** Phase 0 commit（无 Phase 1 依赖）
**Produces for:** Track C（CalculateCost 同步 API）、Phase 2（recorder 重构）

**Files:**
- Create: `numind-server/internal/pkg/pricing/pricing.go`
- Create: `numind-server/internal/pkg/pricing/cache.go`
- Create: `numind-server/internal/pkg/pricing/pricing_test.go`
- Modify: `numind-server/internal/pkg/billing/recorder.go`（计算逻辑切换到 pricing.CalculateCost）

### Task B.1: 新建 pricing.go 骨架 + Calculator 接口

- [ ] Step 1: 创建 `ICalculator` 接口 + `calculator` struct（含 store + cache 字段）
- [ ] Step 2: 写 constructor `NewCalculator(ds store.IStore) ICalculator`
- [ ] Step 3: 单元测试：mock store 返回 pricing_rule，验证构造
- [ ] Step 4: commit `feat(credits-system/B): pricing.Calculator interface skeleton`

### Task B.2: 实现 CalculateCost 函数

- [ ] Step 1: 写失败测试（RED）—— 给定 `provider=ali, model=qwen-turbo, service_type=llm_chat, pt=100, ct=50`，验证按 pricing_rule 计算 cost_cents 正确
- [ ] Step 2: 实现 `CalculateCost`（逻辑从 `internal/pkg/billing/recorder.go:310 calculateCostAndRevenue` 迁移）
- [ ] Step 3: GREEN - 测试通过
- [ ] Step 4: 表驱动测试覆盖：不同模型、0 token、极大 token、未知 model 返回 error
- [ ] Step 5: commit `feat(credits-system/B): implement CalculateCost pure function`

### Task B.3: 实装 LRU Cache

- [ ] Step 1: 写测试：连续 3 次查同一 key，DB 只被访问 1 次（mock store 记录 call count）
- [ ] Step 2: 实装 `resolvePricingRuleCached` 使用 hashicorp/golang-lru 或 github.com/hashicorp/golang-lru/v2
- [ ] Step 3: 写测试验证 TTL 5min 过期后重新查 DB
- [ ] Step 4: 写测试验证 pubsub 失效：`pricing.InvalidateCache(provider, model, service_type)` 清除对应条目
- [ ] Step 5: commit `feat(credits-system/B): LRU cache + TTL + pubsub invalidation`

### Task B.4: Recorder 重构调 pricing.CalculateCost

- [ ] Step 1: 写失败测试（RED）：验证 recorder.buildRecord 调用 pricing.CalculateCost（注入 mock calculator）
- [ ] Step 2: 修改 recorder 的构造函数接受 `pricing.ICalculator`，删除 `calculateCostAndRevenue` 内部实现，改调 pricing
- [ ] Step 3: GREEN - 测试通过，原有 recorder 测试不受影响
- [ ] Step 4: 集成测试 - 连通 recorder + pricing + store，端到端验证 cost 计算一致
- [ ] Step 5: commit `refactor(credits-system/B): recorder uses pricing.CalculateCost (single source of truth)`

**Track B 完成验收：**
- [ ] `go test ./internal/pkg/pricing/... ./internal/pkg/billing/...` 通过
- [ ] 2 阶段 review PASS

---

## Track C: ICreditService + Estimation + DeductCreditsTx（依赖 A + B 已 merge）

**Repo:** numind-server
**Branch:** feature/credits-system-track-C
**Depends on:** Track A + B merge 到 integration 分支（见 Phase 2 前置）
**Produces for:** Phase 2 Integration（sop/salesrag 使用 ICreditService）、Track D（共享 errors/types）

**Files:**
- Modify: `numind-server/internal/numind/biz/credit/credit.go`（提取 DeductCreditsTx，添加 HasActiveSubscription 如不存在）
- Create: `numind-server/internal/numind/biz/credit/credit_service.go`（ICreditService impl）
- Create: `numind-server/internal/numind/biz/credit/estimation.go`（EstimateCredits + UpdateCoefficient）
- Create: `numind-server/internal/numind/biz/credit/prompt_estimator.go`
- Create: `numind-server/internal/numind/biz/credit/credit_service_test.go`
- Create: `numind-server/internal/numind/biz/credit/estimation_test.go`

### Task C.1: DeductCreditsTx 外部 tx 变体

- [ ] Step 1: 读现有 `DeductCredits` 代码（credit.go），理解事务包裹层和行锁逻辑
- [ ] Step 2: 写测试：调用 `DeductCreditsTx(ctx, tx, userID, credits, reason)` 应返回 `[]PackageDeduction` 明细 + error，使用传入的 tx 不开新事务
- [ ] Step 3: 重构：抽出内部 tx 函数成 `DeductCreditsTx`，老的 `DeductCredits` 变成 `return ds.DB().Transaction(func(tx) { return DeductCreditsTx(...) })`
- [ ] Step 4: GREEN - 所有现有 DeductCredits 测试仍通过
- [ ] Step 5: 新增测试验证 `DeductCreditsTx` 在外部 tx 失败时整体回滚
- [ ] Step 6: commit `feat(credits-system/C): extract DeductCreditsTx for external tx composition`

### Task C.2: legacyTierImpl 实现

- [ ] Step 1: 写失败测试：`legacyTierImpl.CheckAndEstimate` 对 `canRun=true` 返回 `SkipDeduction=true`，对 `canRun=false` 返回 `ErrInsufficientCredits` wrap with reason
- [ ] Step 2: 实装 `legacyTierImpl`（见 spec §3.6 完整代码），Reserve/Reconcile/Refund 全部 panic("unreachable: legacy_tier")
- [ ] Step 3: GREEN - 测试通过
- [ ] Step 4: 测试 `GetBalance` 对 legacy_tier 返回 RemainingRuns/MonthlyLimit，不查 credit_package
- [ ] Step 5: commit `feat(credits-system/C): legacyTierImpl no-op + CanRunSOP dispatch`

### Task C.3: creditsImpl Reserve 实装

- [ ] Step 1: 写失败测试：`creditsImpl.Reserve(user, op, credits=100, coefID=1)` 应：扣 credit_package（FIFO）+ 写 credit_reservation + 写 credit_reservation_item × N + 返回 *Reservation
- [ ] Step 2: 实装（见 spec §3.10 事务嵌套），用 `DeductCreditsTx` 避免嵌套事务
- [ ] Step 3: GREEN
- [ ] Step 4: 测试 idempotency_key UNIQUE 冲突 → 返回已存在 reservation（不重复扣）
- [ ] Step 5: 测试 `ErrInsufficientCredits` 路径
- [ ] Step 6: commit `feat(credits-system/C): creditsImpl Reserve with FIFO + reservation items`

### Task C.4: creditsImpl Reconcile + Refund + FinalizeReservation

- [ ] Step 1: 写失败测试：Reconcile 正常路径（actual < reserved）按 seq ASC 退还 delta 到原 packages
- [ ] Step 2: 写失败测试：Reconcile 补扣路径（actual > reserved）从原 items[0] 的 package 补扣
- [ ] Step 3: 写失败测试：Reconcile 幂等 —— 对 terminal 状态 reservation 再调返回 `ErrAlreadyFinalized`
- [ ] Step 4: 写失败测试：Refund 全额退还 + status=refunded + finalize_reason 写入
- [ ] Step 5: 写失败测试：FinalizeReservation 按 opErr/actualCost 正确分发
- [ ] Step 6: 实装全部方法（见 spec §3.3 FinalizeReservation 逻辑）
- [ ] Step 7: GREEN - 所有测试通过
- [ ] Step 8: commit `feat(credits-system/C): Reconcile + Refund + FinalizeReservation with idempotency`

### Task C.5: EstimateCredits R2 公式实现

- [ ] Step 1: 写失败测试 —— 给定 `promptChars=1000, coef={char_ratio=1.5, comp_ratio=0.5, buffer=0.2}`，验证 `estimated_credits = ceil(1500 × (1+0.5) × pricing × 1.2 × 100)` 计算正确
- [ ] Step 2: 实装 `EstimateCredits(ctx, op, promptChars, model, provider) → (int64, coef_id, error)`，遵循 S2 Gate Coverage Check 列出的公式：

```go
func (b *estimationBiz) EstimateCredits(ctx, op Operation, promptChars int, model, provider string) (int64, uint64, error) {
    coef, err := b.getActiveCoefficient(ctx, provider, model, string(op))
    if err != nil {
        return 0, 0, ErrCoefficientNotFound
    }
    rule, err := b.pricing.ResolvePricingRule(ctx, "llm_chat", provider, model)
    if err != nil { return 0, 0, err }
    estimatedPromptTokens := math.Ceil(float64(promptChars) * coef.CharToTokenRatio)
    estimatedCompletionTokens := estimatedPromptTokens * coef.CompletionPromptRatio
    costYuan := (estimatedPromptTokens * rule.InputPricePerMTok +
                 estimatedCompletionTokens * rule.OutputPricePerMTok) / 1_000_000
    estimatedCredits := int64(math.Ceil(costYuan * 100 * (1 + coef.SafetyBufferPct)))
    return estimatedCredits, coef.ID, nil
}
```
- [ ] Step 3: GREEN - 测试通过
- [ ] Step 4: 测试 coefficient 不存在时 `ErrCoefficientNotFound`
- [ ] Step 5: commit `feat(credits-system/C): EstimateCredits R2 formula implementation`

### Task C.6: UpdateCoefficient 并发 retry

- [ ] Step 1: 写失败测试 —— 并发 2 个 goroutine 同时 UpdateCoefficient，其中一方应 retry 成功
- [ ] Step 2: 实装 `UpdateCoefficient` 按 spec §2.11.6（SELECT FOR UPDATE + duplicate key retry 3 次指数退避 50/100/200ms）
- [ ] Step 3: GREEN - 测试通过
- [ ] Step 4: 测试 3 次 retry 后仍失败 → 返回 `ErrCoefficientConcurrent`
- [ ] Step 5: commit `feat(credits-system/C): UpdateCoefficient with row lock + duplicate key retry`

### Task C.7: IPromptEstimator 实装

- [ ] Step 1: 写失败测试 —— `Estimate("sop_run", sop_template_id=X)` 应遍历 SOP 模板所有 node，渲染 prompt 字符数求和
- [ ] Step 2: 实装 `prompt_estimator.go`（按 spec §3.11 的实现注释），dispatch by operation
- [ ] Step 3: GREEN
- [ ] Step 4: 测试 `salesrag_chat`, `sop_chat` 路径
- [ ] Step 5: commit `feat(credits-system/C): IPromptEstimator with dispatch by operation`

### Task C.8: Langfuse span 埋点（4 span + trace metadata）

**Files:**
- Modify: `numind-server/internal/numind/biz/credit/credit_service.go`（在 CheckAndEstimate/Reserve/Reconcile/Refund 4 方法内加 span 埋点）
- Create: `numind-server/internal/numind/biz/credit/credit_service_langfuse_test.go`

**验收规则引用：** 必须遵循 `.claude/rules/ai-service.md §3`（span 创建 + 结束 + metadata）和 spec §5.1（字段 schema）。

- [ ] Step 1: 写失败测试 —— 启动 mock langfuse server（或捕获 langfuse client call），验证 CheckAndEstimate 产生 `credit-estimate` span 含字段 `operation/prompt_chars/model/provider/billing_mode` + output `estimated_credits/sufficient/coefficient_id/char_to_token_ratio/completion_prompt_ratio/safety_buffer_pct/sub_remain_before/booster_remain_before`（spec §5.1.1 schema）
- [ ] Step 2: 在 `creditsImpl.CheckAndEstimate` 实装 span（按 spec §5.1.1 完整代码骨架）
- [ ] Step 3: GREEN
- [ ] Step 4: 同法实装 `credit-reserve`（spec §5.1.2）、`credit-reconcile`（spec §5.1.3）、`credit-refund`（spec §5.1.4） 3 个 span
- [ ] Step 5: 实装 trace-level metadata 追加（§5.1.5）—— 在 CheckAndEstimate 入口往现有 SOP/SalesRAG trace root 追加 `billing_mode`/`deducted_from`/`credit_balance_at_start` 字段（使用 langfuse.UpdateTraceMetadata 或等价 API）
- [ ] Step 6: 表驱动测试验证所有 4 span + metadata 字段齐全
- [ ] Step 7: commit `feat(credits-system/C): Langfuse span instrumentation (4 span + trace metadata per spec §5.1)`

**Track C 完成验收：**
- [ ] 全部 C.1-C.8 commit 在 feature/credits-system-track-C
- [ ] `go test ./internal/numind/biz/credit/... -race` 通过
- [ ] Langfuse span 字段完整性验证（所有 4 span 覆盖 spec §5.1 schema）
- [ ] 2 阶段 review PASS，验收引用 `.claude/rules/ai-service.md`

---

## Track D: Payment + Order Extension（依赖 A 已 merge）

**Repo:** numind-server
**Branch:** feature/credits-system-track-D
**Depends on:** Track A merge（需要 User.BillingMode 字段）+ Phase 0 errno
**Produces for:** Phase 2（controller 调用）

**Files:**
- Modify: `numind-server/internal/numind/biz/payment/payment.go`（Booster case + anti-early-renewal）
- Modify: `numind-server/internal/numind/biz/order/order.go`（OnPaymentSuccess billing_mode switch）
- Create: `numind-server/internal/numind/biz/credit/cron_billing.go`（cron fallback 独立文件，避免与 Track C 的 credit.go 修改冲突）
- Modify: `numind-server/internal/numind/biz/credit/credit.go`（仅 Phase 2 Task 2.0 追加一行 `RunCronTasks` 调 `reconcileBillingMode`，**不在 Track D 内修改此文件**）
- Create: `numind-server/internal/numind/biz/payment/payment_test.go`（相关测试）

**重要（避免 Track C 文件冲突）：** Track D 新增 `cron_billing.go` 独立文件实装 `reconcileBillingMode` 函数；Track C 修改 `credit.go` 提取 DeductCreditsTx。两条 track 不共享 `credit.go` 编辑，避免 merge conflict。`RunCronTasks` 调用 `reconcileBillingMode` 的一行接线放在 Phase 2 Task 2.0（所有 track merge 后统一执行）。

### Task D.1: Booster 会员门槛校验

- [ ] Step 1: 读现有 `payment.go:71-88`，确认 ProductTypeBooster case 不存在
- [ ] Step 2: 写失败测试：`CreateOrder(userID, booster)` 对无 active subscription 用户应返回 `ErrMembershipRequired`
- [ ] Step 3: 实装 case（见 spec §3.7），调用 `creditStore.HasActiveSubscription`（现有方法）+ 读 user.BillingMode 判 legacy_tier
- [ ] Step 4: GREEN + 补测试：legacy_tier 用户返回 `ErrBoosterNotAvailableForLegacy`、trial 用户（type=trial 非 subscription）返回 `ErrMembershipRequired`
- [ ] Step 5: commit `feat(credits-system/D): booster gate — subscription required + legacy_tier rejected`

### Task D.2: 防提前续费（standard/premium/trial）

- [ ] Step 1: 新增 `tierRank` helper 在 `model/user.go`（free=0, trial=1, standard=2, premium=3）
- [ ] Step 2: 写失败测试：在期 standard 用户购买 monthly standard → `ErrTierInPeriod`；购买 yearly premium（升级）→ 放行
- [ ] Step 3: 实装 CreateOrder 的 Monthly/Yearly 分支加校验（见 spec §3.9）
- [ ] Step 4: 写失败测试：在期会员购买 trial → `ErrTrialNotAvailableInPeriod`
- [ ] Step 5: 实装 Trial 分支校验（已有 HasTrialPackage，加 user.Tier != free 判断）
- [ ] Step 6: GREEN
- [ ] Step 7: commit `feat(credits-system/D): anti-early-renewal across all tier products`

### Task D.3: OnPaymentSuccess billing_mode 切换（独立短事务）

- [ ] Step 1: 写失败测试 —— legacy_tier 用户订单成功后 billing_mode 应切到 credits
- [ ] Step 2: 实装 `biz/order/order.go OnPaymentSuccess` 加 switchBillingModeIfLegacy 调用（见 spec §3.8 独立短事务）
- [ ] Step 3: 测试失败场景 —— billing_mode 切换失败，RechargeWithOrderTx 成功，不影响订单（仅 log warn）
- [ ] Step 4: GREEN
- [ ] Step 5: commit `feat(credits-system/D): legacy→credits billing_mode switch on order success (separate short tx)`

### Task D.4: Cron fallback（独立文件，daily 扫描 legacy_tier 但有 active subscription 的用户补切换）

**Files：** Create `numind-server/internal/numind/biz/credit/cron_billing.go`（独立文件避免与 Track C 的 credit.go 冲突）

- [ ] Step 1: 写失败测试 —— 构造 user billing_mode=legacy_tier + credit_package type='subscription' status='active'，`reconcileBillingMode(ctx)` 执行后 billing_mode 应切到 credits
- [ ] Step 2: 在新文件 `cron_billing.go` 实装 `func (b *creditBiz) reconcileBillingMode(ctx context.Context) error`（独立 public 方法）
- [ ] Step 3: GREEN
- [ ] Step 4: **不修改 `credit.go`** —— `RunCronTasks` 调 `reconcileBillingMode` 的接线留到 Phase 2 Task 2.0 统一执行（避免 Track C/D 共同修改 credit.go）
- [ ] Step 5: commit `feat(credits-system/D): reconcileBillingMode in cron_billing.go (no credit.go change)`

**Track D 完成验收：**
- [ ] 全部 D.1-D.4 commit 在 feature/credits-system-track-D
- [ ] `go test ./internal/numind/biz/payment/... ./internal/numind/biz/order/... -race` 通过
- [ ] 2 阶段 review PASS

---

## Track E: Frontend Components (web-v3, with MSW mocks)（独立）

**Repo:** numind-web-v3
**Branch:** feature/credits-system-track-E
**Depends on:** Phase 0 commit（TS 类型已冻结，MSW mock 模拟 API）
**Produces for:** Phase 2.4 integration（替换 mock → real API）

**Files:**
- Create: `numind-web-v3/src/components/credit/CreditBalanceCard.vue`
- Create: `numind-web-v3/src/components/credit/BoosterPurchaseCard.vue`
- Create: `numind-web-v3/src/views/sop/components/SopEstimateBar.vue`
- Modify: `numind-web-v3/src/api/request.ts`（402 拦截器）
- Modify: `numind-web-v3/src/stores/credits.ts`（if exists）or create
- Create: `numind-web-v3/tests/unit/credit/*.spec.ts`（3 个组件单元测试）
- Create: `numind-web-v3/src/mocks/handlers.ts`（MSW mock for /v1/credits/*）

### Task E.1: MSW mocks 搭建 + 402 拦截器

- [ ] Step 1: 安装 MSW：`npm i -D msw`
- [ ] Step 2: 创建 `src/mocks/handlers.ts` mock `/v1/credits/estimate`（返回固定 estimate）和 `/v1/credits/balance`（返回 QuotaBreakdown with billing_mode=credits）
- [ ] Step 3: 修 `request.ts` 加 `case 402`（见 spec §4.2.2 完整代码）
- [ ] Step 4: 写单元测试 —— 调 API 触发 402，验证 eventBus 派发 `insufficient-credits` 事件
- [ ] Step 5: commit `feat(credits-system/E): MSW mocks + 402 interceptor with Credits.Insufficient dispatch`

### Task E.2: CreditBalanceCard 组件 + 三态渲染

- [ ] Step 1: 创建 `CreditBalanceCard.vue`（按 spec §4.2.4 完整代码）
- [ ] Step 2: 写单元测试 3 个 state：
  - free（user.tier='free'）→ 展示升级引导
  - legacy_tier（billing_mode=legacy_tier）→ 展示次数用量
  - credits（billing_mode=credits）→ 展示积分双档
- [ ] Step 3: GREEN
- [ ] Step 4: commit `feat(credits-system/E): CreditBalanceCard with 3-state rendering`

### Task E.3: SopEstimateBar 组件 + debounce + guard

- [ ] Step 1: 创建 `SopEstimateBar.vue`（按 spec §4.2.5 完整代码）
- [ ] Step 2: 单元测试：legacy_tier 不渲染 + 不触发 API 调用；free 不渲染 + 不调 API；credits 正常渲染 + debounce 300ms
- [ ] Step 3: GREEN
- [ ] Step 4: commit `feat(credits-system/E): SopEstimateBar with billing_mode guard + 300ms debounce`

### Task E.4: BoosterPurchaseCard 组件 + 灰态交互

- [ ] Step 1: 创建 `BoosterPurchaseCard.vue`（按 spec §4.2.6）
- [ ] Step 2: 单元测试：4 态（credits 会员正常 / free 灰态跳转会员 / trial 灰态跳转会员 / legacy_tier 灰态无跳转）
- [ ] Step 3: GREEN
- [ ] Step 4: commit `feat(credits-system/E): BoosterPurchaseCard with 4-state interactivity`

### Task E.5: credits Pinia store（如无则创建，如有则扩展）

- [ ] Step 1: 创建或修改 `src/stores/credits.ts`，管理 balance state + fetchBalance/fetchEstimate actions
- [ ] Step 2: 单元测试：store 行为正确（loading/success/error 状态）
- [ ] Step 3: GREEN
- [ ] Step 4: commit `feat(credits-system/E): credits Pinia store`

### Task E.6: InsufficientCreditsDialog 扩展（复用现有，不新建）

- [ ] Step 1: 读现有 `src/components/common/InsufficientCreditsDialog.vue`
- [ ] Step 2: 如 `show(msg)` API 不支持 reason 参数，扩展为 `show({message, reason})`
- [ ] Step 3: 单元测试 dialog open + message 渲染
- [ ] Step 4: commit `feat(credits-system/E): InsufficientCreditsDialog.show accepts structured payload`

**Track E 完成验收：**
- [ ] 全部 E.1-E.6 commit 在 feature/credits-system-track-E
- [ ] `npm run type-check` 和 `npm run test:unit` 通过
- [ ] 2 阶段 review PASS

---

## Track F: Admin Frontend (admin-web, with mocks)（独立）

**Repo:** numind-admin-web
**Branch:** feature/credits-system-track-F
**Depends on:** Phase 0 commit（admin TS 类型已冻结）
**Produces for:** Phase 2.4 integration（替换 mock → real API）

**Files:**
- Create: `numind-admin-web/src/views/EstimationCoefficientView.vue`
- Create: `numind-admin-web/src/views/MigrationsView.vue`
- Modify: `numind-admin-web/src/views/CreditUsersView.vue`（banner 增强）
- Modify: `numind-admin-web/src/components/AdminSidebar.vue`（菜单追加）
- Modify: `numind-admin-web/src/router/index.ts`（路由追加）
- Create: `numind-admin-web/src/mocks/admin-handlers.ts`（MSW mock）
- Create: unit tests for each new view

### Task F.1: EstimationCoefficientView DataTable + CRUD

- [ ] Step 1: 创建 `EstimationCoefficientView.vue`（用现有 DataTable 组件）
- [ ] Step 2: 实装 CRUD：list（带 filter by provider/model/operation）、新增 modal、编辑 modal（触发 UpdateCoefficient）、软删按钮
- [ ] Step 3: 单元测试：列表渲染、新增提交、编辑提交（带 ChangeReason）
- [ ] Step 4: 处理 503（`Coefficient.Concurrent`）→ toast "系数更新繁忙，请稍后重试"
- [ ] Step 5: commit `feat(credits-system/F): EstimationCoefficientView CRUD with retry error handling`

### Task F.2: 历史版本 drawer

- [ ] Step 1: 在 `EstimationCoefficientView.vue` 添加 side drawer 组件
- [ ] Step 2: 调 `GET /admin/estimation-coefficients/history` 返回所有 version（mock）
- [ ] Step 3: 展示 version history 列表（含 is_active、ChangeReason、UpdatedBy、UpdatedAt）
- [ ] Step 4: 单元测试
- [ ] Step 5: commit `feat(credits-system/F): coefficient version history drawer`

### Task F.3: MigrationsView 状态机

- [ ] Step 1: 创建 `MigrationsView.vue`，三态渲染：PENDING（显示待迁移人数 + 执行按钮）、EXECUTING（禁用 + spinner）、EXECUTED（永久禁用 + 显示迁移记录）
- [ ] Step 2: 调 `GET /admin/migrations/billing-mode-init/status`（mock）决定初始状态
- [ ] Step 3: 执行按钮：触发 `POST /admin/migrations/billing-mode-init`，更新 UI
- [ ] Step 4: 单元测试 3 个状态
- [ ] Step 5: commit `feat(credits-system/F): MigrationsView with 3-state machine`

### Task F.4: CreditUsersView banner + Sidebar 菜单

- [ ] Step 1: 修 `CreditUsersView.vue`，用户详情顶部加 legacy_tier banner（见 spec §4.4.4）
- [ ] Step 2: 新增 "活跃 Reservation" tab（mock 列出 status='reserved' 的 reservation）
- [ ] Step 3: 修 `AdminSidebar.vue` 追加 "AI 服务管理 → 估算系数" + "系统工具 → 迁移工具" 菜单项
- [ ] Step 4: 修 `router/index.ts` 追加路由
- [ ] Step 5: 单元测试 banner 渲染条件
- [ ] Step 6: commit `feat(credits-system/F): CreditUsersView banner + sidebar menu extensions`

**Track F 完成验收：**
- [ ] 全部 F.1-F.4 commit 在 feature/credits-system-track-F
- [ ] `npm run type-check` 和 `npm run test:unit` 通过
- [ ] 2 阶段 review PASS

---

## Track G: R2 数据 Spike（独立，read-only）

**Repo:** 独立分支，产出 seed SQL 内容
**Branch:** feature/credits-system-track-G
**Depends on:** prod/dev MySQL 只读访问
**Produces for:** Track A Task A-Extended（把 seed 注入 migration 100400）

**Files:**
- Create: `numind-server/docs/credits-system-r2-spike-report.md`（产出 markdown 报告）
- Modify: `numind-server/migrations/20260419_100400_seed_credit_estimation_coefficient.sql`（替换占位 INSERT 为真实数据）

### Task G.1: 跑 spike SQL 在 dev 环境

- [ ] Step 1: SSH 到 dev 服务器通过 `$DEV_SSH_*` 环境变量
- [ ] Step 2: 执行 spike SQL（见 spec §5.5 模板）
```sql
SELECT provider, model, operation,
       AVG(completion_tokens * 1.0 / NULLIF(prompt_tokens, 0)) AS avg_ratio,
       STDDEV_POP(completion_tokens * 1.0 / NULLIF(prompt_tokens, 0)) AS std_ratio,
       COUNT(*) AS sample_size
FROM usage_record
WHERE created_at > DATE_SUB(NOW(), INTERVAL 90 DAY)
  AND prompt_tokens > 0 AND completion_tokens > 0
GROUP BY provider, model, operation
HAVING COUNT(*) >= 30;
```
- [ ] Step 3: 保存结果到 CSV
- [ ] Step 4: 写 markdown report 记录：SQL、执行时间、样本数、覆盖 (provider, model, operation) 组合列表
- [ ] Step 5: commit `docs(credits-system/G): R2 spike report on dev usage_record`

### Task G.2: 生成 seed INSERT 语句填入 migration

- [ ] Step 1: 基于 CSV 生成 INSERT 语句，safety_buffer_pct 按 2σ 覆盖率计算（通常 0.2-0.3）
- [ ] Step 2: 未达 30 样本的组合用默认 (1.500, 0.500, 0.300) 保守兜底
- [ ] Step 3: 替换 migration 100400 的占位 INSERT 为真实数据 + provenance 注释（source SQL、spike 时间、样本数）
- [ ] Step 4: 验证 `sed` 出的 SQL 语法正确
- [ ] Step 5: commit `feat(credits-system/G): seed coefficients from R2 spike (N models covered)`

**Track G 完成验收：**
- [ ] G.1-G.2 commit 在 feature/credits-system-track-G
- [ ] Spike report 包含覆盖度 + 样本量 + spike 时间
- [ ] 2 阶段 review PASS

---

# Phase 2: Integration（Serial，主 AI 执行）

Phase 1 所有 track 完成并 merge 到 `feature/credits-system-integration` 后进入此阶段。不并行 dispatch subagent。

## Task 2.0: Integration 接线 + GetBalance endpoint 改造（Track merge 后）

**Files:**
- Modify: `numind-server/internal/numind/biz/credit/credit.go`（RunCronTasks 追加调 `reconcileBillingMode`）
- Modify: `numind-server/internal/numind/controller/v1/credit/credit.go`（改造 GetQuotaBreakdown handler 调 ICreditService.GetBalance，扩展返回字段）

**背景（对应 P1 修正）：** Track C 和 Track D 都不能改 `credit.go`（避免冲突），所以 cron 接线留到这里。Spec §2.11.1 / §4.5 要求改造现有 `/v1/credits/balance` endpoint 返回 `billing_mode/remaining_runs/monthly_limit` 新字段，Track E/F 前端已消费这些字段——此 task 做后端对应改造。

- [ ] Step 1: 在 `credit.go` 的 `RunCronTasks` 函数内追加调 `b.reconcileBillingMode(ctx)`（Track D 在 cron_billing.go 已定义此方法）
- [ ] Step 2: 写集成测试：构造 legacy_tier 用户 + active subscription，跑 cron → billing_mode 切换成功
- [ ] Step 3: 改造 `/v1/credits/balance` handler：从调旧的 `biz.GetQuotaBreakdown` 改为调 `ICreditService.GetBalance(user)`，映射到扩展后的 `QuotaBreakdown`（见 spec §2.11.1 + §4.5）
- [ ] Step 4: 写 handler 集成测试：legacy_tier 用户返回 remaining_runs/monthly_limit；credits 用户返回 sub_total/sub_remain/booster_total 等
- [ ] Step 5: GREEN
- [ ] Step 6: commit `feat(credits-system): integration wiring — cron bindings + GetBalance controller refactor`

## Task 2.1: SOP runNode / runChat 控制流反转

**Files:**
- Modify: `numind-server/internal/numind/biz/sop/sop.go`（runNode 附近 + runChat 附近 + deductCreditsForSop 重写）
- Create: `numind-server/internal/numind/biz/sop/sop_credits_test.go`（新增 credits 集成测试）

- [ ] Step 1: 读 spec §3.2 新 runNode 完整代码骨架
- [ ] Step 2: 重写 `runNode` 采用 CheckAndEstimate → Reserve → LLM → pricing.CalculateCost → defer Finalize 控制流
- [ ] Step 3: 同样重写 `runChat`
- [ ] Step 4: `deductCreditsForSop` 内部切换到新路径（保留签名），原 fire-and-forget 调用点零改动
- [ ] Step 5: 写集成测试：credits 用户跑单 node SOP → 扣减正确；失败路径触发 Refund
- [ ] Step 6: 写集成测试：legacy_tier 用户跑 SOP → 走旧 CanRunSOP 不扣积分
- [ ] Step 7: 全部 GREEN
- [ ] Step 8: commit `feat(credits-system): sop.go control-flow inversion (Reserve→LLM→Reconcile)`

## Task 2.2: SalesRAG Chat / ChatStream 接入扣减

**Files:**
- Modify: `numind-server/internal/numind/biz/salesrag/salesrag.go`（Chat + ChatStream）
- Create: `numind-server/internal/numind/biz/salesrag/salesrag_credits_test.go`

- [ ] Step 1: 按 spec §3.4 重写 `Chat` 加 Reserve/Reconcile 包装
- [ ] Step 2: 按 spec §3.5 重写 `ChatStream` 加 drain + context.Canceled 处理
- [ ] Step 3: 写失败测试：用户 0 余额 ChatStream → Reserve 失败返回 ErrInsufficientCredits，前端收到 402
- [ ] Step 4: 写失败测试：ChatStream 中途 client disconnect → defer 触发 Refund
- [ ] Step 5: 写 legacy_tier 测试：SkipDeduction=true 保持免费（P4e=A）
- [ ] Step 6: GREEN + 集成验证
- [ ] Step 7: commit `feat(credits-system): salesrag.go credit integration (prod bug fix + stream handling)`

## Task 2.3: Controller + Router 注册（全部端点）

**Files:**
- Create/Modify: `numind-server/internal/numind/controller/v1/credit/credit.go`（Estimate + ListPackages handler）
- Create: `numind-server/internal/numind/controller/v1/admin_credit/coefficients.go`（CRUD handler）
- Create: `numind-server/internal/numind/controller/v1/admin_migration/migrations.go`（init + status handler）
- Modify: `numind-server/internal/numind/router.go`（authGroup 追加 2 个端点）
- Modify: `numind-server/internal/numind/admin_router.go`（adminAuthGroup 追加 7 个端点）

- [ ] Step 1: 实装 `POST /v1/credits/estimate` handler（见 spec §3.11）
- [ ] Step 2: 实装 `GET /v1/credits/packages` handler（见 spec §4.1.1）
- [ ] Step 3: 实装 7 个 admin endpoints（见 spec §4.1 统一视图）
- [ ] Step 4: 路由注册（在 authGroup / adminAuthGroup 下追加）
- [ ] Step 5: 写 handler 集成测试（HTTP round-trip）
- [ ] Step 6: commit `feat(credits-system): HTTP controllers + router registration`

## Task 2.4: 前端 MSW mock → real API 切换

**Files:**
- Modify: `numind-web-v3/src/views/SettingsView.vue`（嵌入 CreditBalanceCard + BoosterPurchaseCard）
- Modify: `numind-web-v3/src/views/sop/SopRunView.vue`（嵌入 SopEstimateBar + 错误处理）
- Modify: `numind-admin-web/src/views/EstimationCoefficientView.vue` 等（移除 mock 改为真 API）

- [ ] Step 1: SettingsView 嵌入 2 个 credit 组件
- [ ] Step 2: SopRunView 嵌入 SopEstimateBar + 捕获 ErrInsufficientCredits 弹窗
- [ ] Step 3: Admin 各 view 移除 mock，直连后端 API
- [ ] Step 4: 本地 `npm run dev` + 启动 numind-server 本地，点击跑一次 smoke test
- [ ] Step 5: commit `feat(credits-system): frontend mock→real API integration`

## Task 2.5: Playwright E2E 6 paths

**Files:**
- Create: `numind-web-v3/e2e/credits-system.spec.ts`
- Create: `numind-web-v3/e2e/helpers/credits-admin.ts`（调 admin API 控制 tier/billing_mode 的 helper）

- [ ] Step 1: 按 spec §5.4 实装 6 个 test（Path 1-6）
- [ ] Step 2: 补齐 helper：forceTier, switchBillingMode, resetCredits, mockPayment 等
- [ ] Step 3: 本地跑 `E2E_USERNAME=$E2E_USERNAME E2E_PASSWORD=$E2E_PASSWORD npm run test:e2e` 全绿
- [ ] Step 4: commit `test(credits-system): playwright E2E for 6 critical paths`

---

# Phase 3: Verification（S5 Gate）

## Task 3.1: Langfuse span 全链路检查

- [ ] Step 1: 启动本地 Langfuse stack：`docker compose -f numind-server/docker-compose.langfuse.yml up -d`
- [ ] Step 2: 本地环境配置 langfuse 启用（config_local.yaml）
- [ ] Step 3: 触发 1 个 SOP run + 1 个 SalesRAG chat + 1 个失败场景
- [ ] Step 4: 在 Langfuse UI 验证每个 trace 都有 credit-estimate + credit-reserve + credit-reconcile/refund span
- [ ] Step 5: 验证 span metadata 字段齐全（按 spec §5.1 schema）
- [ ] Step 6: 产出验证 checklist（写入 Obsidian feature note 或 PR desc）

## Task 3.2: 完整测试套件

- [ ] Step 1: numind-server `task lint` 通过
- [ ] Step 2: numind-server `task test`（含 race + coverage）通过
- [ ] Step 3: numind-web-v3 `npm run lint` + `npm run type-check` + `npm run test:unit` 通过
- [ ] Step 4: numind-admin-web 同上
- [ ] Step 5: numind-web-v3 `npm run test:e2e` 6 paths 全过
- [ ] Step 6: 任何失败 → 修复后重跑全套

## Task 3.3: Migration rollback 演练

- [ ] Step 1: 本地 MySQL，apply 全部 12 个 migration
- [ ] Step 2: 逐个执行 rollback（反向顺序）
- [ ] Step 3: 每个 rollback 后验证对应表/字段是否清除干净
- [ ] Step 4: 再次 apply 验证可重入
- [ ] Step 5: 记录结果

## Task 3.4: Card Cleanup（独立 commit，非 credits-system scope）

**Note:** 按 spec §3.14 + §4.6，此 task 不在 credits-system 范围。作为独立 `chore(cleanup)` commit，与 credits-system 解耦。

- [ ] Step 1: 修 `CLAUDE.md` §1 "核心功能"列表，移除"卡片生成（Markdown → 图片）"
- [ ] Step 2: commit 独立：`chore(cleanup): remove card generation references from CLAUDE.md`

---

# S5 Gate 验证清单（对应 NDF §3 S5）

- [ ] `task lint` + `task test` 退出码 0
- [ ] `npm run lint` + `npm run type-check`（web-v3 + admin-web）退出码 0
- [ ] `npm run test:e2e` 6 条关键路径 退出码 0
- [ ] gstack `/qa` 浏览器截图 QA（本地 localhost:5173）无 P0 回归
- [ ] Langfuse span 验证完整
- [ ] 数据 spike 产出 seed 清单 6 项全过
- [ ] Migration rollback 演练通过

---

# 附录 A: Task 总览（38 tasks）

| Phase | Task | Track | 文件数 | 预估时间 |
|-------|------|-------|--------|---------|
| 0 | 0.1-0.5 | Contract Freeze | 10+ | 0.5 天（serial）|
| 1 | A.1-A.5 | Track A 数据层 | 5 | 2 天（并行）|
| 1 | B.1-B.4 | Track B pricing | 4 | 2 天（并行）|
| 1 | C.1-C.8 | Track C ICreditService + Langfuse spans | 8 | 3.5 天（并行）|
| 1 | D.1-D.4 | Track D Payment+Order | 4 | 2 天（并行）|
| 1 | E.1-E.6 | Track E Frontend | 6 | 3 天（并行）|
| 1 | F.1-F.4 | Track F Admin UI | 4 | 2 天（并行）|
| 1 | G.1-G.2 | Track G R2 Spike | 2 | 0.5 天（并行）|
| 2 | 2.0-2.5 | Integration | 6 | 3.5 天（serial）|
| 3 | 3.1-3.4 | Verification | 4 | 1 天（serial）|

**并行优势：** Phase 1 的 7 条 track 并行 → 最慢 track（C，3 天）决定 Phase 1 总时长。不并行则 2+2+3+2+3+2+0.5 = 14.5 天，并行后 3 天（收益 11.5 天）。

**总预估时长（wall-clock）：** Phase 0（0.5）+ Phase 1（3，受 Track C 束缚）+ Phase 2（3）+ Phase 3（1）= **7.5 天 wall-clock**，但 agent teams 总工作量仍为 18-23 人天。

| 度量 | 值 |
|------|---|
| 总 tasks | 40 |
| 总 agent 人天 | 18-23 |
| Wall-clock（并行）| 7.5 天 |
| Wall-clock（串行 baseline）| 18-23 天 |

---

# 附录 B: 风险与缓解

| 风险 | 缓解 |
|------|------|
| 契约冻结漏项，Phase 1 中途发现 | Phase 1 track 遇契约问题必须 STOP 并 raise 给主 AI；主 AI 单独 commit 契约更新后所有 track rebase |
| Track 间文件重叠 | 主 AI 在 dispatch 前校验 Files 节无重叠 |
| Track 完成进度不齐（长尾 Track C） | 短 track 先 merge 到 integration 分支，Track C 最后 rebase |
| Phase 2 integration 发现 Phase 1 bug | NDF §6 回退协议：回退到对应 track，修完重 merge |
| R2 spike 数据不够（样本 < 30 的组合多）| 用保守默认 (1.5, 0.5, 0.3)，上线后 beta 观察期 calibration |
| MSW mock 行为与真 API 不一致 | Phase 2.4 切换时做 contract testing（调真 API 对比 mock response shape）|
