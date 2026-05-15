# membership-credits-redesign Plan Extension — Cleanup (T1-T12)

**关联 feature**：`membership-credits-redesign`（S5→S3 reopened 2026-05-15）
**关联文档**：
- 原 S3 plan：[`../../plans/2026-04-29-membership-credits-redesign-plan.md`](2026-04-29-membership-credits-redesign-plan.md)
- 原 S2 spec：[`../specs/2026-04-29-membership-credits-redesign-design.md`](../specs/2026-04-29-membership-credits-redesign-design.md)
- S0 现状 audit：[`../../credits-system-data-consistency-audit.md`](../../credits-system-data-consistency-audit.md)
- HTML 讲解图：[`../../credits-refactor-explainer.html`](../../credits-refactor-explainer.html)

**Plan 状态**：S3（writing），待 Sonnet 原子性 reviewer + 人类共审后进 S4
**Prod 起点**：numind-server v2.1.19（2026-05-15）
**部署节奏**：每个 task 部署 dev → 监控 ≥3 天 0 残余写入 → 部署 prod → 下一个 task

---

## §1 总览

12 个 task 分 6 个 Phase，严格依赖顺序：

```
A. 基础设施              B. 拆 4 个老写入路径
  T1 加 source_type      T2 删 admin Recharge
                         T3 删 parent_grant 死包
                         T4 改写 GrantMembership 内部实现
                         T5 切 RechargeWithOrderTx 到新表
                         T6 删 legacy DeductCredits 链

C. 数据校准              D. 切剩余 readers       E. 死字段/死表    F. 收尾
  T7 booster 验算        T9 b2b_billing 切 +     T10 DROP COL     T12 加 FK
  T8 ledger 校准              listPackages 死路由  T11 archive +        + 更新档案
                                                       DROP credit_package
                                                       + DROP balance 字段
```

**依赖图（修订 v2 — 删除错误的 T1→T10 箭头，明确 T2-T6 实际串行）**：

```
T1 (加 source_type 列)
   │
   ├─→ T2 (删 admin Recharge) ─────────────┐
   ├─→ T3 (删 parent_grant 死包) ──────────┤
   │                                       │
   ├─→ T4 (改写 GrantMembership 内部       │
   │       + 切 HasActiveSubscription /    │
   │       HasTrialPackage 两 guard reader)│
   │       │                               │
   │       ▼ dev ≥3 天 0 残余 INSERT       │
   │       T5 (切支付回调到新表)            │
   │       │                               │
   │       ▼ dev ≥3 天 + 主动触发验证      │
   │       T6 (删 legacy DeductCredits)    │
   │       │                               │
   │       ▼                               │
   │       T7 (booster 验算 SQL) ←─────────┘
   │       │
   │       ▼
   │       T8 (ledger 校准，含 T1 source_type)
   │       │
   │       ▼
   │       T9 (切 b2b_billing + 删 listPackages 死路由 BE+FE)
   │       │
   │       ▼
   │       T10 (DROP COLUMN credits_deducted，前置 T6 已删 writer)
   │       │
   │       ▼
   │       T11 (archive + 7 天 backup + DROP credit_package + balance 字段)
   │       │
   │       ▼
   │       T12 (加 FK + 文档收尾 + dev 环境初始化说明)
   │
   └─→ T2, T3 可与 T4 并行；但 T4→T5→T6 严格串行（每个间隔 ≥3 天 dev 监控 + 主动触发）
```

**修订说明**：
- 删除 v1 依赖图中 T1→T10 的直接箭头（v1 暗示 T10 可早于 T6，与正文 "T10 前置 T6" 矛盾）
- T4 显式扩充 scope：含两个 guard reader 切换（reviewer P0-1 修复）
- T2-T6 不是"全部并行"，仅 T2/T3 可与 T4 并行；T4→T5→T6 严格顺序（每个 task 前置上一个 task 的 prod 监控期结束）

---

## §2 Task 详细规格

### T1 — credit_transaction 加 source_type/source_id 列 + 回填

**所属**：Phase A
**仓库**：numind-server
**前置**：无（解锁所有后续 task）
**目的**：让 ledger 自身能分辨 trial/cycle/booster 来源；DROP credit_package 后历史 3075 行扣减不会匿名化。

#### 涉及文件
- 新 migration：`migrations/2026MMDDHHMMSS_add_credit_transaction_source_type.sql`（forward）+ `_rollback.sql`
- `internal/pkg/model/credit.go` CreditTransaction struct：`SourceType *string` / `SourceID *uint64` 字段
- `internal/numind/biz/membership/cycle.go` DeductCreditsTx：写入 transaction 时同时填 source_type/source_id（trial/cycle/booster + 对应表 PK）
- `internal/numind/biz/credit/credit.go` deductCreditsTxFull（legacy）：同样填（T6 后随 legacy 删除一并消失）

#### Migration forward
```sql
ALTER TABLE credit_transaction
  ADD COLUMN source_type VARCHAR(20) NULL AFTER package_id,
  ADD COLUMN source_id BIGINT UNSIGNED NULL AFTER source_type,
  ADD INDEX idx_ct_source (source_type, source_id);

-- 回填：以 credit_package.type 为权威，把 source_type 标进去
UPDATE credit_transaction ct
JOIN credit_package cp ON ct.package_id = cp.id
SET ct.source_type = cp.type,
    ct.source_id = cp.id;

-- 验证：0 行 NULL
SELECT 'T1_verify' AS check_name, COUNT(*) AS null_rows
  FROM credit_transaction WHERE source_type IS NULL;
-- 期望 null_rows = 0
```

#### Migration rollback
```sql
ALTER TABLE credit_transaction
  DROP INDEX idx_ct_source,
  DROP COLUMN source_id,
  DROP COLUMN source_type;
```

#### 验收条件
- migration forward/rollback 在 dev 各跑一次成功
- prod 跑 forward 后 SELECT COUNT(*) FROM credit_transaction WHERE source_type IS NULL = 0
- Go 代码新写入路径同时填 source_type/source_id（biz/membership/cycle.go）
- `go test ./...` PASS + `task lint` PASS

#### 风险
- 3075 行 UPDATE 在 prod 单次事务执行，事务 < 5 秒，可接受。如担心可分批：`UPDATE ... LIMIT 1000` 多轮。
- prod 跑 migration 时 credit_transaction 不能同时被新插入（lock 冲突）→ MAINTENANCE_MODE=true 跑 migration

#### Rollback 条件
仅在 dev 实测中失败时回滚。prod 跑完后**不回滚**（rollback 只删字段，不影响业务，但失去 ledger 区分能力 = 退化）

---

### T2 — 删 admin Recharge（FE 先 BE 后，分两段部署）

**所属**：Phase B
**仓库**：numind-server + numind-admin-web
**前置**：T1
**目的**：删除历史 0 调用的死功能；避免后续 admin 误触 404

> **Reviewer P1-4 修复说明**：v1 plan 把 FE + BE 写为单 task 同步部署，但两个仓库有独立 CI/CD，无法保证部署原子。如果 BE 先上 → FE 按钮还在 → 用户点击 → 404 bomb。v2 拆为 **T2a (FE 先) + T2b (BE 后)** 两段，FE 先把按钮删干净后再删 BE 的 handler。

#### T2a — FE 删按钮 + API client（先部署）

**仓库**：numind-admin-web
**涉及文件**：
- `src/views/CreditUsersView.vue` 删 Recharge 按钮（`:217` 附近 `@click="openRecharge"`）+ 弹窗 form + submitRecharge 方法（约 60 行）
- `src/api/credits.ts:71-91` 删 `rechargeCredits` 函数 + 相关 type

**验收条件**：
- `cd numind-admin-web && npm run lint && npm run type-check` PASS
- 部署 dev 后管理端用户详情页确认 Recharge 按钮已消失
- 部署 dev ≥3 天后无人为触发 BE 的 `/v1/admin/credits/users/:id/recharge`（观察日志）

#### T2b — BE 删 handler + biz + router（FE 部署 ≥3 天后）

**仓库**：numind-server
**涉及文件**：
- `internal/numind/admin_router.go:130` 删 `POST /v1/admin/credits/users/:id/recharge` 注册
- `internal/numind/controller/v1/admin_credit/credit.go:119-172` 删 rechargeRequest struct + Recharge handler（约 50 行）
- `internal/numind/biz/credit/credit.go` 删 `RechargeCredits` 函数 + interface 签名（约 25 行，含 store.GetOrCreateAccount + tx.Create(credit_package)）

**验收条件**：
- `task lint` + `go test ./...` PASS
- Prod `SELECT * FROM credit_package WHERE grant_source='admin_recharge'` = 0（已确认，证据留 commit msg）

#### 总体风险
- 零客诉补偿能力：现在 admin 无法手动给用户加积分。如未来需要，单独开 feature 加 `POST /v1/admin/credits/users/:id/adjust-booster` 限制只调 ubb
- T2a 部署期间如果 FE 部署失败、用户访问的是 cached old FE → 仍可能点 Recharge → BE 处理 → 但 BE 还活着，所以请求成功 → 这是 OK 的（旧行为）
- T2b 部署期间如果有人通过 curl 直接打 BE → 404 → 但 admin 工具历史 0 调用，可接受

---

### T3 — 删 parent_grant 死代码 package

**所属**：Phase B
**仓库**：numind-server
**前置**：无（独立小任务）
**目的**：清理孤儿 controller package

#### 涉及文件
- `internal/numind/controller/v1/parent_grant/` 整个目录删除
- grep 全仓 `parent_grant` 确认无任何 import / dependency

#### 验收条件
- 删除后 `task lint` + `go test ./...` PASS
- **绝对不动 `router.go:276` 的 `/v1/users/children/:child_id/grant-membership` 路由** —— 该路由绑定的是 `creditCtrl.GrantMembership`（不是 parent_grant.Handler），是活路径

#### 风险
- 零（grep 已确认无路由注册无 import）

---

### T4 — 改写 GrantMembership 内部实现 + 切 2 个 guard reader（API 路径不变）

**所属**：Phase B
**仓库**：numind-server
**前置**：T1
**目的**：把 `biz/credit/grant_membership.go GrantMembership` 从「写 credit_package」改为「调 MembershipService」。同时**切两个 guard reader**（`HasActiveSubscription` + `HasTrialPackage`）到读新表，否则 T4 完成后 trial lifetime 保护立即失效，T11 DROP 后 panic。API 契约不变，前端无感知。

> **Reviewer P0-1 修复说明**：v1 plan 把"删除 HasActiveSubscription / HasTrialPackage"含糊放在"涉及文件"列表里，但没说切到读什么。深查 `store/credit.go:148-166` 显示这两个方法读 `credit_package`。如果只删 INSERT 路径（GrantMembership 写新表）但 guard reader 仍查老表 → 新 grant 后 HasTrialPackage 查 credit_package 返回 false → 用户可被重复 grant trial → 这是直接的 prod 数据完整性 bug。T4 必须**作为原子单元**同时完成这两件事。

#### 涉及文件（v2 扩充）
- `internal/numind/biz/credit/grant_membership.go` GrantMembership（约 150 行重写）
  - 旧逻辑：`tx.Create(&credit_package)` × N（按 months 分行）+ `UpdateBalance(credit_account)`
  - 新逻辑：
    - type='trial' → `membershipSvc.GrantTrial(userID, source='b2b_grant', granterUserID)`
    - type='subscription' → `membershipSvc.GrantOrRenewSubscription(userID, months, source='b2b_grant', granterUserID)`
- `internal/numind/biz/credit/grant_membership.go:98,105` 调用点（call sites）改读新表（详见下方）
- `internal/numind/store/credit.go:148-166` **改写** HasActiveSubscription / HasTrialPackage：
  - 旧 `HasActiveSubscription`: `SELECT COUNT(*) FROM credit_package WHERE user_id=? AND type='subscription' AND status='active'`
  - 新 `HasActiveSubscription`: `SELECT COUNT(*) FROM subscription WHERE user_id=? AND expires_at > NOW()`
  - 旧 `HasTrialPackage`: `SELECT COUNT(*) FROM credit_package WHERE user_id=? AND type='trial'`
  - 新 `HasTrialPackage`: `SELECT COUNT(*) FROM trial_grant WHERE user_id=?`（trial_grant 是 UNIQUE per user，存在即代表 lifetime 已用）
- `internal/numind/biz/credit/grant_membership_test.go` 测试更新（含两个 guard 切换的 unit test）
- 调用方 grep 验证：`HasActiveSubscription` 被 grant_membership.go:98 + 其他可能位置调用，`HasTrialPackage` 被 grant_membership.go:105 调用

#### 验收条件（v2 增强）
- `go test ./internal/numind/biz/credit/...` PASS
- `go test ./internal/numind/biz/membership/...` PASS（确保新 guard 读新表后行为不变）
- 测试用例覆盖：
  - trial grant 后再次 grant trial → 必须返回 `ErrTrialNotAllowedForActivePro` 或类似 lifetime 错误
  - subscription 1 月 / 12 月 grant + 重放 idempotency_key
  - HasActiveSubscription 在 subscription.expires_at 已过期时返回 false
- 与 `POST /v1/users/children/:child_id/grant-membership` API 契约对比（请求/响应字段不变）
- Playwright 跑一次 B2B grant 端到端（dev）+ **主动触发** "已 grant trial 的用户再次 grant trial 验证保护生效"

#### 风险
- 父账户 B2B grant 是高频流程（manifest 显示多次客诉相关）
- T4 完成后**保持 dev 运行 ≥3 天**且**主动触发完整 B2B grant 流程** 监控 `INSERT INTO credit_package WHERE grant_source='b2b_grant' AND created_at > T4_deploy` = 0 才进 T5
- Rollback 路径：git revert T4 commit，恢复 GrantMembership 旧实现 + 两个 guard 旧 SQL。前提是 T5 还没动 RechargeWithOrderTx

---

### T5 — RechargeWithOrderTx 支付回调切完全到新表

**所属**：Phase B
**仓库**：numind-server
**前置**：T4 dev ≥3 天 0 残余写入
**目的**：删除支付回调对 credit_package 的最后一个 INSERT 入口

#### 涉及文件
- `internal/numind/biz/credit/credit.go` RechargeWithOrderTx（约 100 行）
  - 旧逻辑（trial/sub 分支）：`tx.Create(&credit_package)` + `UpdateBalance`
  - 新逻辑：
    - 该函数当前 prod 仅 booster 走（payment.go:241-242 验证 product_type='booster'）
    - booster：已切到 `user_booster_balance.Increment` + `membership_event(booster_granted)`，仅需删除 fallback 的 credit_package INSERT 残留
    - trial/sub 分支：完全删除（支付路径不创建 trial/sub）
- `internal/numind/biz/payment/payment.go` fulfillOrder：确认 product_type 守卫仍生效
- 测试更新

#### 验收条件（v2 增强 — Reviewer P1-6 修复）
- `go test ./...` PASS
- **Playwright E2E（必须）**：booster 自购完整端到端测试 — 用户在前端发起支付 → mock 微信支付回调 → 验证 ubb 余额 +600 + membership_event(booster_granted) 新行 + payment_order.status='paid' + credit_package 表无新 INSERT
- prod 跑后 `SELECT * FROM credit_package WHERE created_at > T5_deploy AND grant_source != 'b2b_grant'` = 0
- prod 跑后**主动触发**真实 booster 自购支付一次（admin 自购 ¥29.9）+ 验证三表一致
- biz 单测覆盖：mock RechargeWithOrderTx booster 路径，确认 0 调用 credit_package.Create

#### 风险
- 支付回调失败会导致客户付钱不到账，**MAINTENANCE_MODE=true 不能屏蔽支付回调**，需 spec §10.1 豁免列表实测覆盖
- **Reviewer P1-9 修复**：T5 部署后 dev ≥3 天监控期改为**主动触发**：在 dev 环境用测试支付通道完整跑一次 ¥0.01 booster 自购（绕过真实支付走 dev mock）
- Rollback 路径：git revert T5 commit + 手动补救任何在 T5 部署窗口期内的支付订单（通过 admin 看 payment_order 表）

---

### T6 — 删 legacy DeductCredits 链 + state cron stub

**所属**：Phase B
**仓库**：numind-server
**前置**：T5 dev ≥3 天 0 残余写入 + **主动触发完整 SOP 执行 + Salesrag 聊天验证扣减走 MembershipService**
**目的**：切断 credit_package 的所有 reader/writer，让 T11 DROP 安全

> **Reviewer P1-8 修复说明**：v1 plan 路径 `biz/sales_rag/sales_rag.go` 错误，实际是 `biz/salesrag/salesrag.go`（无下划线）。同时 sop.go:1825 中 `deductCreditsForSop` 已注释 (Phase 2 Task 2.1 已删)，本 task 实际是 grep + 确认无活跃 caller 后删除 credit.go 中的接口和实现。

> **Reviewer P1-9 修复说明**：v1 plan "dev 7 天 0 UPDATE credit_package" 在 dev 用户少的情况下是被动等待，可能产生假阳性。改为**主动触发**完整业务路径验证。

#### 涉及文件（v2 路径修正）
- `internal/numind/biz/sop/sop.go:1825` 确认已无 active legacy DeductCredits 调用（grep 验证 deductCreditsForSop 已注释）；如有残余则切到 MembershipService
- `internal/numind/biz/salesrag/salesrag.go:1170` 同上（注意路径无下划线，v1 plan 写错）
- `internal/numind/biz/credit/credit.go` 删除：
  - `DeductCredits` 函数 + interface 签名（约 60 行）
  - `deductCreditsTxFull` 函数（约 150 行）
  - `GetActivePackagesForUpdate` interface + impl
  - `UpdatePackage` interface + impl
  - `RunCronTasks` no-op stub
- `internal/numind/store/credit.go` 删除对应 store 方法
- 测试更新

#### 验收条件
- `task lint` + `go test ./...` PASS
- Playwright SOP 执行 / Salesrag 聊天 / Chatbot 三场景端到端（dev）确认扣减走 MembershipService
- prod 跑后 `SELECT * FROM credit_package WHERE updated_at > T6_deploy` = 0（无 UpdatePackage 残余）

#### 风险
- credits-deduct-cycle-wiring (v2.1.19) 已切 Reserve/Reconcile，理论上 legacy 链 0 prod 流量
- 但若 sop.go:1825 / sales_rag.go:1170 仍有 fallback 路径触发，需先确认 dead
- Rollback 难（多函数互相依赖），所以 T6 部署前 dev 演练完整 1 周

---

### T7 — booster 余额逐用户验算

**所属**：Phase C
**仓库**：numind-server（仅 SQL，无代码改）
**前置**：T2-T6 全部完成 + dev 监控 0 残余写入
**目的**：用 ledger 算法验证 user_booster_balance 数字是否算对了

#### Pre-condition check（v2 新增 Reviewer P1 修复）

T7 入口必须先确认 T2-T6 全部完成无残余写入：
```sql
-- 必须返回 0 行才能进 T7 校验，否则等 T6 部署完成
SELECT COUNT(*) AS residual_writes
  FROM credit_package
 WHERE created_at > '<T6_deploy_timestamp>' OR updated_at > '<T6_deploy_timestamp>';
-- 期望 0
```

#### SQL 脚本（v2 修订：join payment_order 拿真实金额）

> **Reviewer P1-5 修复说明**：v1 SQL `amount_cents / 2990 * 600` 在 prod 部分 `booster_granted` 事件 `amount_cents=0` 的情况下会算出 total_granted=0，与实际 ubb 值产生假差异（user 30 / 348 都是这个情况）。改为 join payment_order 拿真实订单金额。

```sql
-- 期望：每个用户的 ubb.credits_remaining 应等于
--   Σ(payment_order.amount_cents from booster orders) ÷ 单价 (=29.9 元 = 600 积分 × N 个)
--   -
--   Σ(-amount from credit_transaction where source_type='booster')

-- 校验查询（v2: join payment_order，不再信 membership_event.amount_cents）
WITH granted AS (
  SELECT po.user_id,
         SUM(po.amount_cents / 2990 * 600) AS total_granted
    FROM payment_order po
   WHERE po.product_type = 'booster' AND po.status = 'paid'
   GROUP BY po.user_id
), deducted AS (
  SELECT user_id, SUM(-amount) AS total_deducted
    FROM credit_transaction
   WHERE source_type='booster' AND amount < 0
   GROUP BY user_id
)
SELECT ubb.user_id, ubb.credits_remaining AS actual,
       COALESCE(g.total_granted, 0) - COALESCE(d.total_deducted, 0) AS expected,
       ubb.credits_remaining - (COALESCE(g.total_granted, 0) - COALESCE(d.total_deducted, 0)) AS delta
  FROM user_booster_balance ubb
  LEFT JOIN granted g ON g.user_id = ubb.user_id
  LEFT JOIN deducted d ON d.user_id = ubb.user_id
 WHERE ubb.credits_remaining != COALESCE(g.total_granted, 0) - COALESCE(d.total_deducted, 0);
-- 期望：0 行（user 1=6000, user 30=128, user 348=433 全部对得上）
-- 若有差异：列入 T8 校准目标
```

#### 验收条件
- Pre-condition check 返回 0（T6 已完成）
- 校验 SQL 返回 0 行（已知 user 1/30/348 是合法 self_purchase，预期通过）
- 任何返回的差异行 → 写入 T8 校准清单
- 报告 commit message 记录验算结果证据

#### 风险
- 极低（read-only SQL，无副作用）
- 若 payment_order.amount_cents 字段在某些 booster 订单也是 0（异常订单），需进一步排查 payment 流程，不属本 task 范畴

---

### T8 — Ledger 校准（trial_grant / credit_cycle）

**所属**：Phase C
**仓库**：numind-server（migration SQL + 校验脚本）
**前置**：T7 完成
**目的**：以 credit_transaction ledger 为 SOT 重建 trial_grant.credits_remaining / credit_cycle.credits_remaining；过期 trial 强制归 0

#### Migration forward
```sql
-- 单个事务原子完成
START TRANSACTION;

-- 1. Pre-check invariant（参考 spec §6 03-verify.sql 8 项）
--    a. source_type 100% 回填（T1 已做）
SELECT 'pre_check_source_type_null', COUNT(*) FROM credit_transaction WHERE source_type IS NULL;
-- 期望 0

--    b. 列出待校准用户清单（供人类 review）
CREATE TEMPORARY TABLE t8_calibration_targets AS
SELECT 'trial_grant' AS tbl, user_id, credits_remaining AS current, 0 AS computed
  FROM trial_grant WHERE expires_at < NOW() AND credits_remaining > 0;
SELECT * FROM t8_calibration_targets;  -- 人类目视

-- 2. trial_grant 过期归 0
UPDATE trial_grant SET credits_remaining = 0
 WHERE expires_at < NOW() AND credits_remaining > 0;

-- 3. trial_grant 在期校准（以 ledger 为权威）
UPDATE trial_grant tg
JOIN (
  SELECT user_id,
         GREATEST(200 - COALESCE(SUM(-amount), 0), 0) AS computed_remaining
    FROM credit_transaction
   WHERE source_type='trial' AND amount < 0
   GROUP BY user_id
) calc ON calc.user_id = tg.user_id
SET tg.credits_remaining = calc.computed_remaining
WHERE tg.expires_at >= NOW()
  AND tg.credits_remaining != calc.computed_remaining;

-- 4. credit_cycle 在期校准（同 ledger 算法 — NET 公式，含 refund）
-- ⚠️ 公式说明：必须用 NET sum (credits_granted + SUM(all amounts))，不能用 deduction-only
-- (credits_granted - SUM(-amount WHERE amount<0))。后者会丢失 Reconcile 写入的退款行
-- (positive amount rows, source_type='cycle')，导致 refund 后的 cycle 被错误下调。
-- 已在 dev 上踩过坑：v1 草稿用 deduction-only 把 cycle_id=6 错误地从 1997 改成 1928。
UPDATE credit_cycle cc
JOIN (
  SELECT ct.user_id, ct.source_id AS cycle_id,
         GREATEST(cc2.credits_granted + COALESCE(SUM(ct.amount), 0), 0) AS computed
    FROM credit_transaction ct
    JOIN credit_cycle cc2 ON cc2.id = ct.source_id AND cc2.user_id = ct.user_id
   WHERE ct.source_type='cycle'
     AND cc2.cycle_end > NOW()
   GROUP BY ct.user_id, ct.source_id, cc2.credits_granted
) calc ON calc.user_id = cc.user_id AND calc.cycle_id = cc.id
SET cc.credits_remaining = calc.computed
WHERE cc.cycle_end > NOW()
  AND cc.credits_remaining != calc.computed;

-- 5. Post-check invariant
SELECT 'post_check_trial_drift' AS check_name, COUNT(*) FROM trial_grant tg
  LEFT JOIN (...) calc ON calc.user_id = tg.user_id
 WHERE tg.expires_at >= NOW() AND tg.credits_remaining != calc.computed_remaining;
-- 期望 0

-- 6. Audit log
INSERT INTO membership_event (user_id, event_type, idempotency_key, occurred_at, source, metadata)
SELECT user_id, 'admin_calibration', 't8_calibration_20260515', NOW(), 'system',
       JSON_OBJECT('table','trial_grant','old_remaining',...,'new_remaining',...)
  FROM ... ;

COMMIT;
-- 任意 invariant 失败则上面的 SELECT 返回 > 0 行，DBA 手动 ROLLBACK
```

#### Rollback 策略（v2 修订 — partial recovery，不简单恢复全表）

> **Reviewer P1-7 修复说明**：v1 写"从 mysqldump backup 恢复 trial_grant + credit_cycle 表"会丢 T8 后用户产生的合法新扣减数据（用户多发积分）。改为 partial recovery：仅回滚因 T8 SQL bug 错误修改的行。

**两段式 rollback**：
1. **0-1 小时内发现 bug**（fresh state，无 T8 后新扣减）：
   ```bash
   mysql < t8_pre_calibration.sql  # 恢复 trial_grant + credit_cycle 全表
   ```
2. **1 小时后才发现 bug**（用户已产生新扣减）：
   - 用 T8 写入的 audit row（idempotency_key='t8_calibration_20260515'）反向定位被错误修改的行
   - 仅对这些行 INSERT INTO ... ON DUPLICATE KEY UPDATE 恢复 T8 前状态
   - 跑一次 ledger 重建 SQL 验算（参考 T7）
   - **关键**：rollback SQL 不能覆盖 T8 后用户新产生的合法扣减

**预案保险**：T8 跑之前先 `mysqldump --single-transaction trial_grant credit_cycle > t8_pre_calibration.sql`，保留 30 天

#### 验收条件
- Pre/post invariant 全通过（参考 spec §6 03-verify.sql 8 项）
- prod 跑后 SQL `SELECT * FROM trial_grant WHERE expires_at < NOW() AND credits_remaining > 0` = 0
- audit row 写入 membership_event 成功（idempotency_key='t8_calibration_20260515'）
- **不需要 Go 并发压测**（v2 修订：v1 描述错误，T8 是 SQL UPDATE 事务，与扣减并发无关；正确验证就是上面的 invariant SQL）

---

### T9 — b2b_billing 切 + listPackages 死路由清理（保留历史月份 read 路径）

**所属**：Phase D
**仓库**：numind-server + numind-web-v3
**前置**：T8 完成
**目的**：切 b2b_billing 完全走 membership_event；删除 credit_package 的所有 reader（除了 T11 archive 之前的历史月份 fallback）

> **Reviewer P0-3 修复说明**：v1 plan 写"删 getLegacyEvents 函数"会破坏 cutover_date 之前历史月份报表。实测：prod cutover_date=2026-04-20，`credit_package(grant_source='b2b_grant')` 97 行**全部** created_at >= 2026-04-20 —— cutover 之前 0 B2B 业务发生，spec §7 双口径实际从未触发。**所以 cutover 之前历史月份本来就无数据**。但 4-20 到 T11 DROP 之间的数据呢？这部分会在 T11 归档到 `legacy_credit_package_archive_20260515`，b2b_billing 应该支持从 archive 表读历史月份。
>
> v2 修订：(a) chooseSource() 在 cutover_date 之后强制走 new_only 模式（删 legacy_only + cutover_split 分支） (b) 保留 getLegacyEvents 函数签名，但 SQL 从 credit_package 切到 legacy_credit_package_archive_20260515（注意：T9 在 T11 archive 之前执行，所以 T9 阶段仍可读 credit_package；T11 archive 完成后再切 SQL 到 archive 表，作为 T11 的子步骤）

#### 涉及文件（v2 修订）
**numind-server**：
- `internal/numind/biz/b2b_billing/b2b_billing.go`
  - 删除 `chooseSource()` 的 `legacy_only` + `cutover_split` 分支（约 40 行），强制 `new_only`（cutover_date 之后所有月份走 membership_event）
  - **保留** `getLegacyEvents` 函数签名，但加注释 `// DEPRECATED: 仅 cutover_date 之前历史月份回溯使用（prod 0 数据）；T11 后切到 archive 表`
  - 删除 spec §7 双口径相关代码注释
- `internal/numind/router.go:224` 删除 `GET /v1/credits/packages` 路由
- `internal/numind/controller/v1/credit/credit.go` 删 listPackages handler（约 30 行）
- `internal/numind/admin_router.go` 删 listPackagesByUser 相关路由（如存在）
- `internal/numind/controller/v1/admin_credit/credit.go` 删 ListPackagesByUser handler（约 30 行）
- `internal/numind/store/credit.go` 删 ListPackagesByUser store 方法

**numind-web-v3**（Reviewer P2-12 修复）：
- `src/api/credits.ts:79-111` 删 `listPackages` 函数 + `ListPackagesResp` interface + `CreditPackageItem` interface（约 30 行 dead code）

#### 验收条件
- `task lint` + `go test ./...` PASS
- `cd numind-web-v3 && npm run lint && npm run type-check` PASS
- prod GET `/v1/credits/packages` 返回 404（dev 已验证前端无调用方）
- B2B 月结报表 5 月 `/v1/admin/b2b-billing-report?month=2026-05` 与 T9 前结果字节级一致
- B2B 月结报表 4 月（cutover 之前）`/v1/admin/b2b-billing-report?month=2026-03` 返回空（已确认 cutover 前 0 B2B 业务）

#### 风险
- 月结报表是高价值功能，T9 部署后人工跑 4 月 + 5 月报表对比
- Rollback 路径：git revert T9 commit（保留 getLegacyEvents 是保险，删错可单独回滚 chooseSource 分支删除）

---

### T10 — DROP COLUMN usage_record.credits_deducted

**所属**：Phase E
**仓库**：numind-server
**前置**：T6 完成（legacy DeductCredits 删除后该字段无 writer）
**目的**：清理死字段

#### Migration forward
```sql
ALTER TABLE usage_record DROP COLUMN credits_deducted;
```

#### Migration rollback
```sql
ALTER TABLE usage_record ADD COLUMN credits_deducted BIGINT DEFAULT 0 COMMENT '历史字段已废弃';
-- 数据已丢，恢复后该列全 NULL
```

#### 涉及文件
- 新 migration：`migrations/2026MMDDHHMMSS_drop_usage_record_credits_deducted.sql` + rollback
- `internal/pkg/model/billing.go` UsageRecord struct 删 `CreditsDeducted` 字段（1 行）
- grep 确认无 reader（T6 已删 writer）

#### 验收条件
- `go test ./...` PASS（应无影响，无 reader）
- prod 跑 forward 后 `DESCRIBE usage_record` 不含 credits_deducted

#### 风险
- 零（无 reader，已确认）

---

### T11 — Archive + DROP credit_package + DROP balance 字段

**所属**：Phase E
**仓库**：numind-server
**前置**：T9 完成 + 7 天监控（无 prod credit_package 读写）
**目的**：彻底下线 credit_package 表 + credit_account.balance 字段

#### 步骤
1. **Archive 备份**：
```sql
CREATE TABLE legacy_credit_package_archive_20260515 (
  id BIGINT UNSIGNED PRIMARY KEY,
  user_id BIGINT UNSIGNED,
  type VARCHAR(20),
  total_credits INT,
  remain_credits INT,
  status VARCHAR(20),
  grant_source VARCHAR(50),
  granter_user_id BIGINT UNSIGNED,
  order_id BIGINT UNSIGNED,
  activated_at TIMESTAMP NULL,
  expires_at TIMESTAMP NULL,
  created_at TIMESTAMP,
  updated_at TIMESTAMP,
  archived_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  archive_reason VARCHAR(200),
  INDEX idx_archive_user_type (user_id, type)
) COMMENT='credit_package 表归档，保留 7 年与会计凭证同期。查询见 README_credit_package_archive.md';

INSERT INTO legacy_credit_package_archive_20260515
SELECT id, user_id, type, total_credits, remain_credits, status, grant_source,
       granter_user_id, order_id, activated_at, expires_at, created_at, updated_at,
       NOW(), 'membership-credits-redesign cleanup T11 2026-05-15'
  FROM credit_package;
```

2. **mysqldump hot backup**（额外保险）：
```bash
mysqldump --single-transaction numind-prod credit_package credit_account > backup_T11_pre_drop.sql
# 保留 30 天
```

3. **7 天 hot backup 窗口**：archive 表先建好，T11 实际 DROP 操作放在 archive 后 7 天再做（监控期内可回滚）

4. **DROP**：
```sql
DROP TABLE credit_package;
ALTER TABLE credit_account DROP COLUMN balance;
```

5. **代码层**：
- `internal/pkg/model/credit.go` 删除 CreditPackage struct + CreditAccount.Balance 字段
- `internal/numind/store/credit.go` 删除所有 credit_package 相关 store 方法（已 T6 大半，剩余清理）
- `internal/numind/helper.go:257` AutoMigrate 列表删除 `&model.CreditPackage{}`
- 测试更新

6. **README**：新建 `numind-server/docs/legacy_credit_package_archive_README.md`：
```markdown
# legacy_credit_package_archive_20260515 字段语义

本表是 2026-05-15 credit_package 表下线时的全量归档...
查询示例：
- B2B 月结历史回溯：SELECT ... WHERE grant_source='b2b_grant' AND activated_at BETWEEN ...
- 财务对账：SELECT ... WHERE order_id IS NOT NULL ORDER BY created_at
...
```

#### 验收条件
- archive 表行数 == 原 credit_package 行数（120）
- `go test ./...` + `task lint` PASS
- AutoMigrate 不再创建 credit_package（dev 启动 0 错误）
- prod 部署 7 天后 0 异常报错
- README 写完
- **MAINTENANCE_MODE=true 实测**支付回调豁免（spec §10.1 豁免列表覆盖支付路径），dev 演练通过
- T9 已完成时 `getLegacyEvents` 切到读 `legacy_credit_package_archive_20260515` 表（T9 注释里标的迁移项）

#### 风险
- **最高风险 task**：DROP 不可逆 + 涉及历史财务数据
- 必须 7 天 backup 窗口 + mysqldump hot backup
- 7 天监控期监控**3 类信号**：
  1. application log 中是否有 `Table 'credit_package' doesn't exist` 异常
  2. credit_package 表是否仍有 SELECT 查询（show full processlist + slow log 监控）
  3. b2b_billing 月结报表是否仍能正常返回

#### Rollback 完整可执行 SQL（v2 新增 Reviewer P1-10 修复）

```sql
-- Step 1: 重建 credit_package 表结构（与 archive 表同 schema）
CREATE TABLE credit_package LIKE legacy_credit_package_archive_20260515;
-- 注意：LIKE 会复制结构+索引但不复制数据。archive 表 PK 是 BIGINT UNSIGNED（非 autoincrement），保留原 ID
ALTER TABLE credit_package
  DROP COLUMN archived_at,
  DROP COLUMN archive_reason,
  DROP INDEX idx_archive_user_type;
-- 加回原 credit_package 的索引（参考 migrations/20260430_membership_credits_redesign_01-pre-migration.sql）

-- Step 2: 从 archive 表恢复数据
INSERT INTO credit_package (id, user_id, type, total_credits, remain_credits, status,
                            grant_source, granter_user_id, order_id,
                            activated_at, expires_at, created_at, updated_at)
SELECT id, user_id, type, total_credits, remain_credits, status,
       grant_source, granter_user_id, order_id,
       activated_at, expires_at, created_at, updated_at
  FROM legacy_credit_package_archive_20260515;

-- Step 3: 恢复 credit_account.balance 列
ALTER TABLE credit_account
  ADD COLUMN balance INT NOT NULL DEFAULT 0 AFTER user_id;
-- 注意：balance 数据需从 mysqldump backup_T11_pre_drop.sql 部分提取，单独 UPDATE 回填
-- 否则 balance 全 0，T11 之后用户的实际余额需从三池实时聚合（GetBalance 已支持）

-- Step 4: 恢复 AutoMigrate 行 + GORM model（git revert T11 commit）

-- Step 5: 重启 numind-server，验证 credit_package 查询正常
```

**Rollback 限制**：
- credit_account.balance 历史值需 mysqldump 补充
- 重建后 credit_package 的 status 字段语义需注意：cron 已删除（T6），status 不再被维护，只读
- 重建之后的新写入（如真有 T11 后又调用了 RechargeCredits）会失败因为 T2/T6 已删 caller —— rollback 同时需 git revert T2-T11 全部 commit 回到 T1 状态，是个**重大决定**

---

### T12 — 加硬 FK + 文档收尾 + dev 环境初始化说明

**所属**：Phase F
**仓库**：numind-server
**前置**：T11 完成
**目的**：补硬 FK 防止未来 polymorphic 数据漂移；更新各档案；**修复 dev 环境初始化文档缺失问题**

> **Reviewer P2-11 修复说明**：5 张新表（subscription / trial_grant / credit_cycle / user_booster_balance / membership_event）通过独立 migration SQL 创建，不在 `helper.go` AutoMigrate 列表。当前 dev 开发者 drop DB 重建环境时，AutoMigrate 跑完没这 5 张表 → server 启动后 GetBalance 等方法 panic。T12 应补 dev 环境初始化文档说明。

#### Migration forward
```sql
-- 先清孤儿
DELETE FROM credit_cycle WHERE subscription_id NOT IN (SELECT id FROM subscription);
DELETE FROM credit_reservation_item WHERE reservation_id NOT IN (SELECT id FROM credit_reservation);

-- 加 FK
ALTER TABLE credit_cycle
  ADD CONSTRAINT fk_cycle_subscription
  FOREIGN KEY (subscription_id) REFERENCES subscription(id) ON DELETE CASCADE;

ALTER TABLE credit_reservation_item
  ADD CONSTRAINT fk_item_reservation
  FOREIGN KEY (reservation_id) REFERENCES credit_reservation(id) ON DELETE CASCADE;

-- credit_transaction polymorphic（source_type='trial'|'cycle'|'booster' 对应 source_id 指向不同表）
-- 不直接加 FK，加 CHECK 约束保证 source_type 合法
ALTER TABLE credit_transaction
  ADD CONSTRAINT chk_ct_source_type
  CHECK (source_type IN ('trial', 'cycle', 'booster', 'admin', 'system') OR source_type IS NULL);
```

#### 涉及文件
- 新 migration + rollback
- `DEPRECATED_FEATURES.md` 把 §3.4-3.8 移到 §5（已清理完成）
- `numind-server/CLAUDE.md` §1 计费体系：删 credit_package 状态机的描述
- `.claude/rules/business-logic.md` §4 Credits & Billing：更新引用
- `.claude/rules/database.md` §3 Query Patterns：更新
- **新增** `numind-server/docs/dev-environment-setup.md`（或更新现有 README）：
  - 明确 5 张新表通过独立 migration 创建：`migrations/20260430_membership_credits_redesign_*.sql`
  - dev 环境 drop DB 重建步骤：
    1. `task dev`（启动 AutoMigrate 创建 22 张表）
    2. `mysql < migrations/20260430_membership_credits_redesign_01-pre-migration.sql`
    3. `mysql < migrations/20260430_membership_credits_redesign_02-apply.sql`
    4. `mysql < migrations/20260430_membership_credits_redesign_03-verify.sql`（dry-run，无副作用）
    5. `mysql < migrations/<T1_source_type>.sql`（cumulative T1-T12 migrations）
  - 验证：`task dev` 启动后 server 健康，GetBalance 等方法不 panic

#### 验收条件
- migration forward/rollback PASS
- prod 跑后 `SELECT COUNT(*) FROM credit_cycle WHERE subscription_id NOT IN (SELECT id FROM subscription)` = 0
- 文档更新完成 + git commit
- 新开发者按 dev-environment-setup.md 步骤能成功 drop+rebuild dev DB + server 启动正常

---

## §3 部署节奏与监控

| 阶段 | task | dev 监控期 | prod 部署前置 |
|---|---|---|---|
| A | T1 | 1 天 | 0 NULL source_type |
| B | T2 | 3 天 | 0 admin Recharge call |
| B | T3 | 1 天 | 编译通过 |
| B | T4 | 3 天 | 0 INSERT credit_package WHERE grant_source='b2b_grant' AFTER T4 |
| B | T5 | 3 天 | 0 INSERT credit_package WHERE grant_source != 'b2b_grant' AFTER T5 |
| B | T6 | 7 天 | 0 UPDATE credit_package AFTER T6 |
| C | T7 | 1 天 | SQL 验算 0 差异 |
| C | T8 | 3 天 | invariant 全通过 |
| D | T9 | 3 天 | B2B 月结报表对比一致 |
| E | T10 | 1 天 | 0 query 失败 |
| E | T11 | **7 天** archive 后再 DROP | mysqldump backup OK + 0 异常 |
| F | T12 | 3 天 | FK 约束生效，0 孤儿 |

总预估：**约 5-6 周**（含监控窗口），实际编码时间约 15-20 工作日

---

## §4 S5 验证策略（v2 修订 — Reviewer P1-6 + P2-13 修复）

按 NDF Rule 10，本 plan extension 的 S5 验证策略：

### 4.1 Playwright E2E（5 个关键场景，覆盖高风险 task）

| 场景 | 触发 task | 验证点 |
|---|---|---|
| **B2B grant trial → 二次 grant 拦截** | T4 | trial_grant UNIQUE 保护生效，二次 grant 返回 `ErrTrialNotAllowedForActivePro` |
| **B2B grant subscription 1 月 / 12 月** | T4 | subscription 单行 covers N 月 + credit_cycle lazy create 第一个月 |
| **用户自购 booster 支付端到端** | T5（高金融风险）| mock 支付回调 → ubb +600 + membership_event 新行 + payment_order.status=paid + credit_package 0 写入 |
| **SOP 执行扣减** | T6 | 走 MembershipService 三池扣减（trial→cycle→booster 优先级）+ credit_transaction 写 source_type |
| **Salesrag 聊天扣减** | T6 | 同上 |

### 4.2 gstack `/qa`（浏览器截图 QA）

- admin 端用户详情页确认 Recharge 按钮消失（T2）
- 用户端 CreditsView 余额展示正常（T11 删 balance 字段后走三池实时聚合）
- B2B 月结报表 5 月 + 4 月（历史）数据展示正常（T9）

### 4.3 数据校验 SQL（每个 task 部署后跑）

- **T1**：`SELECT COUNT(*) FROM credit_transaction WHERE source_type IS NULL` = 0
- **T2-T6**：`SELECT * FROM credit_package WHERE created_at OR updated_at > <task_deploy_time>` = 0
- **T7**：booster 验算 SQL（见 T7 §SQL 脚本）= 0 行差异
- **T8**：spec §6 03-verify.sql 8 个 invariant 全过
- **T9**：B2B 月结 5 月报表 T9 前/后字节级一致
- **T10**：`DESCRIBE usage_record` 不含 credits_deducted 列
- **T11**：archive 表行数 == 原 credit_package + 7 天监控 0 异常
- **T12**：4 条 FK 生效 + 0 孤儿数据

### 4.4 可观测性

- 每个 task 部署后跑一次 Langfuse trace 回归（参考 .claude/rules/ai-service.md），确认 trace 链路无破坏
- 部署前后对比 `credit_transaction` 增量行的 source_type 分布（trial / cycle / booster）

### 4.5 dev 环境主动触发验证（Reviewer P1-9 修复）

替代被动等待"7 天 0 update"，每个 task 部署后**主动触发**完整业务路径：

| Task | 主动触发动作 |
|---|---|
| T4 | 在 dev 父账户帮子账户 grant trial → grant trial 二次（验证拦截）→ grant subscription 1 月 |
| T5 | dev 测试账户自购 ¥0.01 booster（mock 支付通道）|
| T6 | dev 用户执行 SOP 节点 + Salesrag 问答 + Chatbot 聊天，确认扣减走新表（credit_transaction.source_type 非 NULL）|

**仅当主动触发后** `credit_package` 表无新 INSERT/UPDATE 才允许进下一个 task。

---

## §5 Rollback 总策略

| Task | Rollback 难度 | 策略 |
|---|---|---|
| T1 | 易 | rollback.sql DROP source_type 列 |
| T2 | 易 | git revert + redeploy |
| T3 | 极易 | git revert |
| T4 | 中 | git revert + 7 天窗口期监控 |
| T5 | 中 | git revert + 补救窗口期支付订单 |
| T6 | 难 | git revert（多函数依赖） |
| T7 | 不适用 | 仅 SQL 查询 |
| T8 | 难 | 从 mysqldump backup 恢复 trial_grant + credit_cycle |
| T9 | 易 | git revert |
| T10 | 不可逆 | 字段重建后数据已丢，但本字段全 0，重建后状态等价 |
| T11 | **不可逆** | 从 archive 表 + mysqldump backup 重建 credit_package |
| T12 | 易 | rollback.sql DROP CONSTRAINT |

**关键不可逆点**：T11。前置必须 7 天 hot backup + mysqldump + archive 表三重保险。

---

## §6 退出条件

完成 T1-T12 后，credits-system 应达到：

- ✅ credit_package 表 DROP（数据已归档 7 年）
- ✅ credit_account.balance 字段 DROP（GetBalance 走三池实时聚合）
- ✅ usage_record.credits_deducted 字段 DROP（表保留）
- ✅ admin Recharge 接口 + UI 删除
- ✅ parent_grant 死代码 package 删除
- ✅ 4 个老写入路径全部停（RechargeCredits / RechargeWithOrderTx / GrantMembership / legacy DeductCredits）
- ✅ b2b_billing.go 完全走 membership_event
- ✅ credit_transaction.source_type 100% 回填
- ✅ trial_grant / credit_cycle / ubb 余额与 ledger 一致
- ✅ 4 条硬 FK 生效
- ✅ DEPRECATED_FEATURES.md §5 归档 5 条
- ✅ 各 CLAUDE.md / .claude/rules/ 文档更新

manifest stage → S4（实施）→ S5（验证）→ S6（merge develop）→ S7（prod tag）→ completed
