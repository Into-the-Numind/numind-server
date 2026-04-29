# 会员积分体系重构 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用 5 张新表（subscription / trial_grant / credit_cycle / user_booster_balance / membership_event）+ 时间驱动 + 懒创建模型替代当前 credit_package 单表 + cron 状态机模型，消除空档、去 cron 化、为 B2B 月度账单提供事件日志真相源。

**Architecture:** Spec §1.1 列出的 13 条 invariant 是设计真相源。核心架构：(1) subscription 单行/用户，续费 += expires_at 不新建行；(2) credit_cycle 懒创建 + ON CONFLICT；(3) 扣减优先级 trial → cycle → booster；(4) 锁顺序 user_id ASC → 表名字典序：credit_cycle < membership_event < subscription < trial_grant < user_booster_balance；(5) LLM OUT-OF-tx 复用现有 Reserve/Reconcile；(6) 客户端 UUID via HTTP `Idempotency-Key` header；(7) anchor=current_started_at + 应用层 anchor_add_months 算法；(8) legacy 字段单向只读；(9) maintenance window 一次性全量切换。

**Tech Stack:** Go 1.24 / Gin / GORM / MySQL 8.0 / Redis / JWT / Vue 3 + Pinia (numind-web-v3) / Vue 3 (numind-admin-web) / Playwright / gstack /qa / dayjs

**关联 Spec:** `numind-server/docs/superpowers/specs/2026-04-29-membership-credits-redesign-design.md`（6559 行 / 10 章节）

---

## File Structure (decomposition)

### numind-server（Go 后端）

#### 新建文件

| 路径 | 职责 |
|---|---|
| `migrations/20260430_membership_credits_redesign.sql` | 5 张新表 schema migration |
| `internal/pkg/model/membership/subscription.go` | subscription GORM model |
| `internal/pkg/model/membership/trial_grant.go` | trial_grant GORM model |
| `internal/pkg/model/membership/credit_cycle.go` | credit_cycle GORM model |
| `internal/pkg/model/membership/user_booster_balance.go` | user_booster_balance GORM model |
| `internal/pkg/model/membership/membership_event.go` | membership_event GORM model + idempotency_key UNIQUE |
| `internal/pkg/util/anchor.go` | anchor_add_months 算法 |
| `internal/pkg/util/anchor_test.go` | anchor 算法 10+ 测试 case |
| `internal/numind/store/membership/subscription.go` | subscription store interface + 实现 |
| `internal/numind/store/membership/trial_grant.go` | trial_grant store |
| `internal/numind/store/membership/credit_cycle.go` | credit_cycle store（懒创建 ON CONFLICT 实现） |
| `internal/numind/store/membership/user_booster_balance.go` | user_booster_balance store |
| `internal/numind/store/membership/membership_event.go` | membership_event append-only store |
| `internal/numind/biz/membership/trial.go` | GrantTrial biz function |
| `internal/numind/biz/membership/subscription.go` | GrantOrRenewSubscription biz function（三场景统一入口） |
| `internal/numind/biz/membership/cycle.go` | ensureCurrentCycle 懒创建 |
| `internal/numind/biz/membership/state.go` | GetMembershipState + GetBalance |
| `internal/pkg/middleware/idempotency.go` | Idempotency-Key HTTP header 中间件 |
| `internal/pkg/middleware/maintenance.go` | MAINTENANCE_MODE=true 503 中间件（含支付回调豁免） |
| `internal/numind/controller/v1/credit/grant_membership.go` | grant-membership endpoint controller |
| `internal/numind/controller/v1/credit/balance.go` | 用户/父账户余额 controller |
| `internal/numind/controller/v1/admin_credit/balance.go` | admin 余额 controller |
| `scripts/2026-04-30-membership-credits-redesign-migration/01-dry-run.sql` | dry-run 对账 |
| `scripts/2026-04-30-membership-credits-redesign-migration/02-apply.sql` | apply（事务 + backup + apply_log） |
| `scripts/2026-04-30-membership-credits-redesign-migration/03-verify.sql` | 迁后对账 invariant 验证 |
| `scripts/2026-04-30-membership-credits-redesign-migration/04-rollback.sql` | 从 apply_log 反向 DELETE |
| `scripts/2026-04-30-membership-credits-redesign-migration/README.md` | 迁移操作手册 |

#### 修改文件

| 路径 | 改动内容 |
|---|---|
| `internal/numind/biz/credit/credit.go` | DeductCredits 重写：调用 ensureCurrentCycle + 锁顺序 + booster 冻结判定 |
| `internal/numind/biz/payment/payment.go` | fulfillOrder 重写：booster quantity 字段 + 调 GrantOrRenewSubscription 替代 RechargeWithOrderTx；移除 tier rank 判断 |
| `internal/numind/biz/b2b_billing/b2b_billing.go` | 改读 membership_event + cutover_date dispatch（legacy_only / new_only / cutover_split 三模式） |
| `internal/numind/biz/sop/sop.go` | CheckSopPermission 改用 HasActiveSubscription / HasActiveTrial 替代 user_tier 判断 |
| `internal/numind/biz/credit/cron_billing.go` | **删除整个文件**（不再需要 cron） |
| `internal/numind/server.go` | 移除 cron ticker 注册 |
| `internal/numind/router.go` | 注册 grant-membership / balance / parent-balance 路由 |
| `internal/numind/admin_router.go` | 注册 admin balance / b2b-billing-report 路由 |
| `config/config.go` | 新增 billing.cutover_date 配置项（必填，零值启动失败） |

### numind-web-v3（用户端 Vue 3）

#### 新建文件

| 路径 | 职责 |
|---|---|
| `src/components/BoosterPurchaseDialog.vue` | booster 购买弹窗（1/5/10 + 自定义 + 支付时序） |
| `src/components/MembershipBadge.vue` | 会员状态徽章（free/trial/pro 3 种渲染） |
| `src/utils/datetime.ts` | dayjs 封装（UTC+8 + YYYY-MM-DD 格式化） |
| `src/utils/idempotency.ts` | UUID v4 生成 + Idempotency-Key header 注入 |

#### 修改文件

| 路径 | 改动内容 |
|---|---|
| `src/api/credits.ts` | 新 BalanceDTO（嵌套 membership_state 对象）+ POST /v1/orders 支持 quantity |
| `src/api/parent.ts` | grant-membership 调用带 Idempotency-Key |
| `src/stores/credits.ts` | Pinia store 重写（state + getters + actions） |
| `src/views/CreditsView.vue` | 三卡片布局（状态 + 余额 + booster 购买入口） |
| `src/views/CustomersView.vue` | 父账户客户管理页双状态显示 + trial 已购置灰 + 不显示子账户 booster |

#### 删除/移除项目

| 路径 | 操作 |
|---|---|
| 老 booster 购买 UI | grep `user_tier` `monthly_sop_runs` `tier_expires` 后逐一移除 |

### numind-admin-web（管理端 Vue 3）

#### 新建文件

| 路径 | 职责 |
|---|---|
| `src/views/B2BBillingView.vue` | B2B 月度账单页（月份选择 + 父账户筛选 + 分组展开 + CSV 导出） |
| `src/api/b2bBilling.ts` | GET /v1/admin/b2b-billing-report 调用 |
| `src/utils/datetime.ts` | dayjs 封装（与 web-v3 同步） |

#### 修改文件

| 路径 | 改动内容 |
|---|---|
| `src/router/index.ts` | 注册 /b2b-billing 路由 |

---

## TOC（23 个原子 task，跨 7 阶段）

### Phase 1：Schema & Foundation（numind-server，无业务行为变更）
- **Task 1：Schema migration — 5 张新表 + 索引**
- **Task 2：GORM Model 5 个**
- **Task 3：anchor_add_months 工具函数**
- **Task 4：Store 层接口 + 实现（5 个 store）**

### Phase 2：算法 biz 函数（numind-server）
- **Task 5：GrantTrial biz 函数**
- **Task 6：GrantOrRenewSubscription biz 函数（三场景统一）**
- **Task 7：ensureCurrentCycle 懒创建 + DeductCredits 改写（含 Reserve/Reconcile 集成）**
- **Task 8：GetMembershipState + GetBalance**

### Phase 3：API 端点（numind-server）
- **Task 9：Idempotency-Key middleware**
- **Task 10：POST /v1/users/children/:child_id/grant-membership 端点**
- **Task 11：POST /v1/orders 改写（booster quantity + payer_id 语义）**
- **Task 12：GET 余额接口（用户/父账户/admin 三变体）**
- **Task 13：GET /v1/admin/b2b-billing-report（cutover_date dispatch）**

### Phase 4：迁移 & 部署（numind-server）
- **Task 14：4 件套迁移脚本（dry-run / apply / verify / rollback）**
- **Task 15：MAINTENANCE_MODE 中间件（含支付回调豁免）**
- **Task 16：cleanup — 删 cron_billing.go、payment.go tier rank 判断、sop.go 老接口替换**

### Phase 5：用户端前端（numind-web-v3）
- **Task 17：Pinia store + API 层（credits / parent）**
- **Task 18：CreditsView 余额组件**
- **Task 19：BoosterPurchaseDialog 购买弹窗**
- **Task 20：CustomersView 双状态显示 + grant 弹窗**

### Phase 6：管理端前端（numind-admin-web）
- **Task 21：B2BBillingView 月度账单页 + CSV 导出**

### Phase 7：cleanup + 验证策略
- **Task 22：用户端旧 UI 移除（基于 grep 结果）**
- **Task 23：S5 验证策略**（NDF Rule 10 强制末尾 task）

---

> **说明**：每 task 含 Files / Steps / Code / Tests / Commit 五段式 TDD 节奏；后端 task（1-16）严格按依赖顺序执行；前端 task（17-22）依赖后端 API（task 9-13）就绪；task 23 是 S5 输入。

---

# Phase 1: Schema & Foundation — Tasks 1-4

> 实施 plan：membership-credits-redesign 数据基础设施层
> Spec：`numind-server/docs/superpowers/specs/2026-04-29-membership-credits-redesign-design.md`
> 范围：Phase 1 共 4 个 task，覆盖 SQL migration、GORM model、anchor 工具函数、store 接口

---

### Task 1：Schema migration — 5 张新表 + 索引

**Files:**
- Create: `numind-server/migrations/20260430_membership_credits_redesign.sql`
- Test: 无（Go 项目无 migration 测试惯例，改用手动 dev DB 验证 + DDL diff review）

- [ ] **Step 1: 写 migration SQL（5 张表 DDL，严格 copy spec §2.1-§2.5）**

文件内容按以下顺序：

```sql
-- 20260430 membership credits redesign — 新建 5 张表
-- spec: docs/superpowers/specs/2026-04-29-membership-credits-redesign-design.md §2

-- §2.1 subscription
CREATE TABLE IF NOT EXISTS `subscription` (
  `id`                       BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id`                  BIGINT UNSIGNED NOT NULL,
  `first_started_at`         DATETIME(0)     NOT NULL,
  `current_started_at`       DATETIME(0)     NOT NULL,
  `expires_at`               DATETIME(0)     NOT NULL,
  `total_months_purchased`   INT             NOT NULL,
  `source`                   ENUM('self_purchase','b2b_grant') NOT NULL DEFAULT 'b2b_grant',
  `granter_user_id`          BIGINT UNSIGNED          DEFAULT NULL,
  `created_at`               DATETIME(0)     NOT NULL,
  `updated_at`               DATETIME(0)     NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uniq_sub_user_id` (`user_id`),
  KEY `idx_sub_expires_at` (`expires_at`),
  KEY `idx_sub_granter_expires` (`granter_user_id`, `expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
  COMMENT='用户订阅主表，每个用户单行，原地更新';

-- §2.2 trial_grant
CREATE TABLE IF NOT EXISTS `trial_grant` (
  `id`                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id`             BIGINT UNSIGNED NOT NULL,
  `granted_at`          DATETIME(0)     NOT NULL,
  `expires_at`          DATETIME(0)     NOT NULL,
  `credits_remaining`   INT             NOT NULL DEFAULT 200,
  `source`              ENUM('self_purchase','b2b_grant') NOT NULL DEFAULT 'b2b_grant',
  `granter_user_id`     BIGINT UNSIGNED          DEFAULT NULL,
  `created_at`          DATETIME(0)     NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uniq_trial_user_id` (`user_id`),
  KEY `idx_trial_expires_at` (`expires_at`),
  KEY `idx_trial_granter_expires` (`granter_user_id`, `expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
  COMMENT='试用包记录，每个用户 lifetime 单行';

-- §2.3 credit_cycle
CREATE TABLE IF NOT EXISTS `credit_cycle` (
  `id`                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id`             BIGINT UNSIGNED NOT NULL,
  `subscription_id`     BIGINT UNSIGNED NOT NULL,
  `cycle_start`         DATETIME(0)     NOT NULL,
  `cycle_end`           DATETIME(0)     NOT NULL,
  `credits_granted`     INT             NOT NULL DEFAULT 0,
  `credits_remaining`   INT             NOT NULL DEFAULT 0,
  `created_at`          DATETIME(0)     NOT NULL,
  `updated_at`          DATETIME(0)     NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uniq_cycle_user_start` (`user_id`, `cycle_start`),
  KEY `idx_cycle_user_end` (`user_id`, `cycle_end`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
  COMMENT='月度积分周期，懒创建';

-- §2.4 user_booster_balance
CREATE TABLE IF NOT EXISTS `user_booster_balance` (
  `user_id`             BIGINT UNSIGNED NOT NULL,
  `credits_remaining`   BIGINT          NOT NULL DEFAULT 0,
  `updated_at`          DATETIME(0)     NOT NULL,
  PRIMARY KEY (`user_id`),
  KEY `idx_booster_updated_at` (`updated_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
  COMMENT='加量包余额，永不过期、单用户单行';

-- §2.5 membership_event
CREATE TABLE IF NOT EXISTS `membership_event` (
  `id`                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id`             BIGINT UNSIGNED NOT NULL,
  `event_type`          ENUM('trial_granted','sub_granted','sub_renewed','booster_granted') NOT NULL,
  `product_type`        ENUM('trial','monthly','booster') NOT NULL,
  `months`              TINYINT UNSIGNED         DEFAULT NULL,
  `quantity`            SMALLINT UNSIGNED        DEFAULT NULL,
  `amount_cents`        BIGINT          NOT NULL DEFAULT 0,
  `source`              ENUM('self_purchase','b2b_grant') NOT NULL,
  `granter_user_id`     BIGINT UNSIGNED          DEFAULT NULL,
  `idempotency_key`     VARCHAR(64)              DEFAULT NULL,
  `subscription_id`     BIGINT UNSIGNED          DEFAULT NULL,
  `occurred_at`         DATETIME(0)     NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uniq_event_idempotency_key` (`idempotency_key`),
  KEY `idx_event_user_occurred` (`user_id`, `occurred_at`),
  KEY `idx_event_granter_occurred` (`granter_user_id`, `occurred_at`),
  KEY `idx_event_type_occurred` (`event_type`, `occurred_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
  COMMENT='会员事件流水，append-only，B2B 账单唯一数据源';

-- ROLLBACK（仅供 ops 应急执行；本文件不主动 DROP）
-- DROP TABLE IF EXISTS `membership_event`;
-- DROP TABLE IF EXISTS `user_booster_balance`;
-- DROP TABLE IF EXISTS `credit_cycle`;
-- DROP TABLE IF EXISTS `trial_grant`;
-- DROP TABLE IF EXISTS `subscription`;
```

- [ ] **Step 2: 在 dev DB 跑 migration up，验证 SHOW TABLES 出 5 张新表**

Run:
```bash
sshpass -p "$DEV_SSH_PASS" ssh -o StrictHostKeyChecking=no "$DEV_SSH_USER@$DEV_SSH_HOST" \
  "mysql -u<user> -p<pass> numind < /tmp/20260430_membership_credits_redesign.sql"
sshpass -p "$DEV_SSH_PASS" ssh ... "mysql -e 'SHOW TABLES LIKE \"subscription\"' numind"
```
Expected: 5 张表均存在（subscription / trial_grant / credit_cycle / user_booster_balance / membership_event）

- [ ] **Step 3: 检查每张表的 SHOW INDEX，确认 UNIQUE / 复合索引就位**

Run:
```bash
mysql -e "SHOW INDEX FROM subscription" numind
mysql -e "SHOW INDEX FROM trial_grant" numind
mysql -e "SHOW INDEX FROM credit_cycle" numind
mysql -e "SHOW INDEX FROM user_booster_balance" numind
mysql -e "SHOW INDEX FROM membership_event" numind
```
Expected: 共 13 个非主键索引 + 5 个主键（参考 spec §2.6 索引一览表）

- [ ] **Step 4: 跑 rollback DDL 测试回滚 → 重新 up**

Run:
```bash
mysql -e "DROP TABLE membership_event; DROP TABLE user_booster_balance; DROP TABLE credit_cycle; DROP TABLE trial_grant; DROP TABLE subscription;" numind
mysql numind < migrations/20260430_membership_credits_redesign.sql
```
Expected: 无报错，5 张表重新创建成功

- [ ] **Step 5: Commit**

```bash
cd numind-server
git checkout -b feat/membership-credits-redesign-task-1
git add migrations/20260430_membership_credits_redesign.sql
git commit -m "feat(membership): add 5 new tables for membership credits redesign

- subscription: per-user single row, in-place update
- trial_grant: lifetime single grant per user
- credit_cycle: monthly credit cycles, lazy created
- user_booster_balance: never-expiring booster balance
- membership_event: append-only event log for B2B billing"
```

---

### Task 2：GORM Model 5 个

**Files:**
- Create: `numind-server/internal/pkg/model/membership/subscription.go`
- Create: `numind-server/internal/pkg/model/membership/trial_grant.go`
- Create: `numind-server/internal/pkg/model/membership/credit_cycle.go`
- Create: `numind-server/internal/pkg/model/membership/user_booster_balance.go`
- Create: `numind-server/internal/pkg/model/membership/membership_event.go`
- Create: `numind-server/internal/pkg/model/membership/constants.go`
- Test: `numind-server/internal/pkg/model/membership/membership_test.go`

- [ ] **Step 1: 写失败的测试**

```go
package membership_test

import (
    "testing"
    "time"

    "github.com/stretchr/testify/require"
    "gorm.io/driver/sqlite"
    "gorm.io/gorm"

    "numind-server/internal/pkg/model/membership"
)

func newTestDB(t *testing.T) *gorm.DB {
    db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
    require.NoError(t, err)
    require.NoError(t, db.AutoMigrate(
        &membership.Subscription{},
        &membership.TrialGrant{},
        &membership.CreditCycle{},
        &membership.UserBoosterBalance{},
        &membership.MembershipEvent{},
    ))
    return db
}

func TestSubscription_TableName(t *testing.T) {
    require.Equal(t, "subscription", (membership.Subscription{}).TableName())
}

func TestSubscription_CreateAndQuery(t *testing.T) {
    db := newTestDB(t)
    parentID := uint64(100)
    now := time.Now().UTC()
    sub := &membership.Subscription{
        UserID:               42,
        FirstStartedAt:       now,
        CurrentStartedAt:     now,
        ExpiresAt:            now.AddDate(0, 1, 0),
        TotalMonthsPurchased: 1,
        Source:               membership.SourceB2BGrant,
        GranterUserID:        &parentID,
        CreatedAt:            now,
        UpdatedAt:            now,
    }
    require.NoError(t, db.Create(sub).Error)
    require.NotZero(t, sub.ID)

    var got membership.Subscription
    require.NoError(t, db.Where("user_id = ?", 42).Take(&got).Error)
    require.Equal(t, uint64(1), uint64(got.TotalMonthsPurchased))
    require.Equal(t, membership.SourceB2BGrant, got.Source)
    require.NotNil(t, got.GranterUserID)
}

func TestTrialGrant_DefaultsAndQuery(t *testing.T) {
    db := newTestDB(t)
    now := time.Now().UTC()
    tg := &membership.TrialGrant{
        UserID:           7,
        GrantedAt:        now,
        ExpiresAt:        now.AddDate(0, 0, 3),
        CreditsRemaining: 200,
        Source:           membership.SourceB2BGrant,
        CreatedAt:        now,
    }
    require.NoError(t, db.Create(tg).Error)
    require.Equal(t, "trial_grant", tg.TableName())
}

func TestCreditCycle_TableNameAndCreate(t *testing.T) {
    db := newTestDB(t)
    now := time.Now().UTC()
    cc := &membership.CreditCycle{
        UserID:           1,
        SubscriptionID:   1,
        CycleStart:       now,
        CycleEnd:         now.AddDate(0, 1, 0),
        CreditsGranted:   2000,
        CreditsRemaining: 2000,
        CreatedAt:        now,
        UpdatedAt:        now,
    }
    require.NoError(t, db.Create(cc).Error)
    require.Equal(t, "credit_cycle", cc.TableName())
}

func TestUserBoosterBalance_PrimaryKeyIsUserID(t *testing.T) {
    db := newTestDB(t)
    now := time.Now().UTC()
    bb := &membership.UserBoosterBalance{
        UserID:           1,
        CreditsRemaining: 600,
        UpdatedAt:        now,
    }
    require.NoError(t, db.Create(bb).Error)

    var got membership.UserBoosterBalance
    require.NoError(t, db.Where("user_id = ?", 1).Take(&got).Error)
    require.Equal(t, int64(600), got.CreditsRemaining)
}

func TestMembershipEvent_AllEventTypes(t *testing.T) {
    db := newTestDB(t)
    now := time.Now().UTC()
    months := uint8(3)
    e := &membership.MembershipEvent{
        UserID:      9,
        EventType:   membership.EventTypeSubGranted,
        ProductType: membership.ProductTypeMonthly,
        Months:      &months,
        AmountCents: 9900,
        Source:      membership.SourceB2BGrant,
        OccurredAt:  now,
    }
    require.NoError(t, db.Create(e).Error)
    require.Equal(t, "membership_event", e.TableName())
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd numind-server && go test ./internal/pkg/model/membership/...`
Expected: FAIL with `cannot find package "numind-server/internal/pkg/model/membership"` 或 type undefined。

- [ ] **Step 3: 写最小实现**

`constants.go`：
```go
package membership

const (
    SourceSelfPurchase = "self_purchase"
    SourceB2BGrant     = "b2b_grant"

    EventTypeTrialGranted   = "trial_granted"
    EventTypeSubGranted     = "sub_granted"
    EventTypeSubRenewed     = "sub_renewed"
    EventTypeBoosterGranted = "booster_granted"

    ProductTypeTrial   = "trial"
    ProductTypeMonthly = "monthly"
    ProductTypeBooster = "booster"
)
```

`subscription.go`：
```go
package membership

import "time"

type Subscription struct {
    ID                   uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
    UserID               uint64    `gorm:"uniqueIndex:uniq_sub_user_id;not null" json:"user_id"`
    FirstStartedAt       time.Time `gorm:"type:datetime(0);not null" json:"first_started_at"`
    CurrentStartedAt     time.Time `gorm:"type:datetime(0);not null" json:"current_started_at"`
    ExpiresAt            time.Time `gorm:"type:datetime(0);not null;index:idx_sub_expires_at;index:idx_sub_granter_expires,priority:2" json:"expires_at"`
    TotalMonthsPurchased int       `gorm:"not null" json:"total_months_purchased"`
    Source               string    `gorm:"type:enum('self_purchase','b2b_grant');not null;default:b2b_grant" json:"source"`
    GranterUserID        *uint64   `gorm:"index:idx_sub_granter_expires,priority:1" json:"granter_user_id,omitempty"`
    CreatedAt            time.Time `gorm:"type:datetime(0);not null" json:"created_at"`
    UpdatedAt            time.Time `gorm:"type:datetime(0);not null" json:"updated_at"`
}

func (Subscription) TableName() string { return "subscription" }
```

`trial_grant.go`：
```go
package membership

import "time"

type TrialGrant struct {
    ID               uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
    UserID           uint64    `gorm:"uniqueIndex:uniq_trial_user_id;not null" json:"user_id"`
    GrantedAt        time.Time `gorm:"type:datetime(0);not null" json:"granted_at"`
    ExpiresAt        time.Time `gorm:"type:datetime(0);not null;index:idx_trial_expires_at" json:"expires_at"`
    CreditsRemaining int       `gorm:"not null;default:200" json:"credits_remaining"`
    Source           string    `gorm:"type:enum('self_purchase','b2b_grant');not null;default:b2b_grant" json:"source"`
    GranterUserID    *uint64   `gorm:"index:idx_trial_granter_expires" json:"granter_user_id,omitempty"`
    CreatedAt        time.Time `gorm:"type:datetime(0);not null" json:"created_at"`
}

func (TrialGrant) TableName() string { return "trial_grant" }
```

`credit_cycle.go`：
```go
package membership

import "time"

type CreditCycle struct {
    ID               uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
    UserID           uint64    `gorm:"uniqueIndex:uniq_cycle_user_start,priority:1;not null" json:"user_id"`
    SubscriptionID   uint64    `gorm:"not null" json:"subscription_id"`
    CycleStart       time.Time `gorm:"type:datetime(0);uniqueIndex:uniq_cycle_user_start,priority:2;not null" json:"cycle_start"`
    CycleEnd         time.Time `gorm:"type:datetime(0);not null;index:idx_cycle_user_end" json:"cycle_end"`
    CreditsGranted   int       `gorm:"not null;default:0" json:"credits_granted"`
    CreditsRemaining int       `gorm:"not null;default:0" json:"credits_remaining"`
    CreatedAt        time.Time `gorm:"type:datetime(0);not null" json:"created_at"`
    UpdatedAt        time.Time `gorm:"type:datetime(0);not null" json:"updated_at"`
}

func (CreditCycle) TableName() string { return "credit_cycle" }
```

`user_booster_balance.go`：
```go
package membership

import "time"

type UserBoosterBalance struct {
    UserID           uint64    `gorm:"primaryKey" json:"user_id"`
    CreditsRemaining int64     `gorm:"not null;default:0" json:"credits_remaining"`
    UpdatedAt        time.Time `gorm:"type:datetime(0);not null;index:idx_booster_updated_at" json:"updated_at"`
}

func (UserBoosterBalance) TableName() string { return "user_booster_balance" }
```

`membership_event.go`：
```go
package membership

import "time"

type MembershipEvent struct {
    ID              uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
    UserID          uint64    `gorm:"not null;index:idx_event_user_occurred,priority:1" json:"user_id"`
    EventType       string    `gorm:"type:enum('trial_granted','sub_granted','sub_renewed','booster_granted');not null;index:idx_event_type_occurred,priority:1" json:"event_type"`
    ProductType     string    `gorm:"type:enum('trial','monthly','booster');not null" json:"product_type"`
    Months          *uint8    `json:"months,omitempty"`
    Quantity        *uint16   `json:"quantity,omitempty"`
    AmountCents     int64     `gorm:"not null;default:0" json:"amount_cents"`
    Source          string    `gorm:"type:enum('self_purchase','b2b_grant');not null" json:"source"`
    GranterUserID   *uint64   `gorm:"index:idx_event_granter_occurred,priority:1" json:"granter_user_id,omitempty"`
    IdempotencyKey  *string   `gorm:"type:varchar(64);uniqueIndex:uniq_event_idempotency_key" json:"idempotency_key,omitempty"`
    SubscriptionID  *uint64   `json:"subscription_id,omitempty"`
    OccurredAt      time.Time `gorm:"type:datetime(0);not null;index:idx_event_user_occurred,priority:2;index:idx_event_granter_occurred,priority:2;index:idx_event_type_occurred,priority:2" json:"occurred_at"`
}

func (MembershipEvent) TableName() string { return "membership_event" }
```

> **注意**：未使用任何 `default:true` bool（项目踩过的 §6 GORM 坑）。所有可空状态用 `*Type` + ENUM string 表达。

- [ ] **Step 4: 跑测试确认通过**

Run: `cd numind-server && go test ./internal/pkg/model/membership/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd numind-server
git checkout -b feat/membership-credits-redesign-task-2
git add internal/pkg/model/membership/
git commit -m "feat(membership): add GORM models for 5 new membership tables

Models map 1:1 to schema in migrations/20260430_membership_credits_redesign.sql.
Constants for source/event_type/product_type ENUMs centralized in constants.go.
No default:true bool fields (project gotcha) — uses ENUM string + nullable pointer."
```

---

### Task 3：anchor_add_months 工具函数

**Files:**
- Create: `numind-server/internal/pkg/util/anchor.go`
- Create: `numind-server/internal/pkg/util/anchor_test.go`

- [ ] **Step 1: 写失败的测试**

```go
package util_test

import (
    "testing"
    "time"

    "github.com/stretchr/testify/require"

    "numind-server/internal/pkg/util"
)

func mkUTC(y int, m time.Month, d, hh, mm, ss int) time.Time {
    return time.Date(y, m, d, hh, mm, ss, 0, time.UTC)
}

func TestAnchorAddMonths_Jan31Sequence(t *testing.T) {
    a := mkUTC(2026, time.January, 31, 10, 0, 0)
    require.Equal(t, mkUTC(2026, time.February, 28, 10, 0, 0), util.AnchorAddMonths(a, 1))
    require.Equal(t, mkUTC(2026, time.March, 31, 10, 0, 0), util.AnchorAddMonths(a, 2))
    require.Equal(t, mkUTC(2026, time.April, 30, 10, 0, 0), util.AnchorAddMonths(a, 3))
    require.Equal(t, mkUTC(2026, time.May, 31, 10, 0, 0), util.AnchorAddMonths(a, 4))
    require.Equal(t, mkUTC(2027, time.January, 31, 10, 0, 0), util.AnchorAddMonths(a, 12))
    require.Equal(t, mkUTC(2027, time.February, 28, 10, 0, 0), util.AnchorAddMonths(a, 13))
}

func TestAnchorAddMonths_LeapDay(t *testing.T) {
    a := mkUTC(2024, time.February, 29, 0, 0, 0)
    require.Equal(t, mkUTC(2024, time.March, 29, 0, 0, 0), util.AnchorAddMonths(a, 1))
    require.Equal(t, mkUTC(2025, time.February, 28, 0, 0, 0), util.AnchorAddMonths(a, 12))
    require.Equal(t, mkUTC(2026, time.February, 28, 0, 0, 0), util.AnchorAddMonths(a, 24))
    require.Equal(t, mkUTC(2028, time.February, 29, 0, 0, 0), util.AnchorAddMonths(a, 48))
}

func TestAnchorAddMonths_DayCapping(t *testing.T) {
    require.Equal(t, mkUTC(2026, time.June, 30, 12, 0, 0),
        util.AnchorAddMonths(mkUTC(2026, time.May, 31, 12, 0, 0), 1))
    require.Equal(t, mkUTC(2026, time.August, 31, 12, 0, 0),
        util.AnchorAddMonths(mkUTC(2026, time.July, 31, 12, 0, 0), 1))
}

func TestAnchorAddMonths_YearWrap(t *testing.T) {
    a := mkUTC(2026, time.December, 15, 23, 59, 59)
    require.Equal(t, mkUTC(2027, time.January, 15, 23, 59, 59), util.AnchorAddMonths(a, 1))
    require.Equal(t, mkUTC(2028, time.January, 15, 23, 59, 59), util.AnchorAddMonths(a, 13))
}

func TestAnchorAddMonths_ZeroIsIdentity(t *testing.T) {
    a := mkUTC(2026, time.March, 15, 8, 30, 0)
    require.Equal(t, a, util.AnchorAddMonths(a, 0))
}

func TestAnchorAddMonths_NegativePanics(t *testing.T) {
    require.Panics(t, func() {
        util.AnchorAddMonths(time.Now(), -1)
    })
}

func TestAnchorAddMonths_PreservesClockComponents(t *testing.T) {
    // INV-3：时分秒纳秒不漂移
    a := time.Date(2026, time.January, 31, 13, 45, 7, 123456789, time.UTC)
    got := util.AnchorAddMonths(a, 1)
    require.Equal(t, 13, got.Hour())
    require.Equal(t, 45, got.Minute())
    require.Equal(t, 7, got.Second())
    require.Equal(t, 123456789, got.Nanosecond())
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd numind-server && go test ./internal/pkg/util/ -run AnchorAddMonths`
Expected: FAIL with `undefined: util.AnchorAddMonths`

- [ ] **Step 3: 写最小实现**

```go
package util

import "time"

// AnchorAddMonths 在 anchor 上加 n 个月，遵循 day 锚点规则：
// 目标 day = min(anchor.Day(), daysInMonth(targetYear, targetMonth))。
// 这避免 1/31 + 1 month 漂移到 3/3（time.AddDate 的标准漂移行为）。
//
// 不变量：
//   INV-1: AnchorAddMonths(a, 0) == a
//   INV-2: AnchorAddMonths(a, n).Day() <= a.Day()
//   INV-3: 时分秒纳秒与 anchor 完全一致
//
// n 必须 >= 0；n < 0 panic（编码 bug，调用方保证）。
func AnchorAddMonths(anchor time.Time, n int) time.Time {
    if n < 0 {
        panic("AnchorAddMonths: n must be >= 0")
    }
    if n == 0 {
        return anchor
    }

    y, m, d := anchor.Date()
    hh, mm, ss := anchor.Clock()
    nsec := anchor.Nanosecond()
    loc := anchor.Location()

    totalMonths := int(m) + n
    targetYear := y + (totalMonths-1)/12
    targetMonth := time.Month((totalMonths-1)%12 + 1)

    // time.Date(_, M+1, 0, ...) 自动归一化为 M 月最后一天
    lastDay := time.Date(targetYear, targetMonth+1, 0, 0, 0, 0, 0, loc).Day()

    targetDay := d
    if targetDay > lastDay {
        targetDay = lastDay
    }

    return time.Date(targetYear, targetMonth, targetDay, hh, mm, ss, nsec, loc)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd numind-server && go test ./internal/pkg/util/ -run AnchorAddMonths -v`
Expected: PASS（7 个测试全部通过）

- [ ] **Step 5: Commit**

```bash
cd numind-server
git checkout -b feat/membership-credits-redesign-task-3
git add internal/pkg/util/anchor.go internal/pkg/util/anchor_test.go
git commit -m "feat(util): add AnchorAddMonths for stable month-anchor arithmetic

Replaces time.AddDate's drift behavior (1/31 + 1mo = 3/3) with
day-capping semantics (1/31 + 1mo = 2/28). Covers leap-year edge cases.
Used by subscription.expires_at = AnchorAddMonths(current_started_at, total_months_purchased)."
```

---

### Task 4：Store 层接口 + 实现（5 个 store）

**Files:**
- Create: `numind-server/internal/numind/store/membership/subscription.go`
- Create: `numind-server/internal/numind/store/membership/trial_grant.go`
- Create: `numind-server/internal/numind/store/membership/credit_cycle.go`
- Create: `numind-server/internal/numind/store/membership/user_booster_balance.go`
- Create: `numind-server/internal/numind/store/membership/membership_event.go`
- Create: `numind-server/internal/numind/store/membership/store.go`（聚合 IMembershipStore + helper）
- Create: `numind-server/internal/numind/store/membership/membership_test.go`
- Create: `numind-server/internal/numind/store/membership/test_helpers.go`（仅 _test.go 可见的 newTestDB + seedUserPair，供 Phase 2 biz tests 复用）
- Modify: `numind-server/internal/numind/store/store.go`（IStore 增加 `Membership() IMembershipStore`）

- [ ] **Step 1: 写失败的测试**

```go
package membership_test

import (
    "context"
    "testing"
    "time"

    "github.com/stretchr/testify/require"
    "gorm.io/driver/sqlite"
    "gorm.io/gorm"

    model "numind-server/internal/pkg/model/membership"
    store "numind-server/internal/numind/store/membership"
)

func newTestDB(t *testing.T) *gorm.DB {
    db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
    require.NoError(t, err)
    require.NoError(t, db.AutoMigrate(
        &model.Subscription{}, &model.TrialGrant{}, &model.CreditCycle{},
        &model.UserBoosterBalance{}, &model.MembershipEvent{},
    ))
    return db
}

// --- Subscription ---

func TestSubscriptionStore_GetReturnsNilWhenNotExist(t *testing.T) {
    db := newTestDB(t)
    s := store.NewSubscriptionStore(db)
    got, err := s.Get(context.Background(), 999)
    require.NoError(t, err)
    require.Nil(t, got, "Get must return (nil, nil) when row absent")
}

func TestSubscriptionStore_CreateAndGet(t *testing.T) {
    db := newTestDB(t)
    s := store.NewSubscriptionStore(db)
    now := time.Now().UTC()
    sub := &model.Subscription{
        UserID: 1, FirstStartedAt: now, CurrentStartedAt: now,
        ExpiresAt: now.AddDate(0, 1, 0), TotalMonthsPurchased: 1,
        Source: model.SourceB2BGrant, CreatedAt: now, UpdatedAt: now,
    }
    require.NoError(t, s.Create(context.Background(), db, sub))
    got, err := s.Get(context.Background(), 1)
    require.NoError(t, err)
    require.NotNil(t, got)
    require.Equal(t, uint64(1), got.UserID)
}

func TestSubscriptionStore_HasActive(t *testing.T) {
    db := newTestDB(t)
    s := store.NewSubscriptionStore(db)
    now := time.Now().UTC()
    require.NoError(t, s.Create(context.Background(), db, &model.Subscription{
        UserID: 1, FirstStartedAt: now, CurrentStartedAt: now,
        ExpiresAt: now.AddDate(0, 1, 0), TotalMonthsPurchased: 1,
        Source: model.SourceB2BGrant, CreatedAt: now, UpdatedAt: now,
    }))
    active, err := s.HasActive(context.Background(), 1, now)
    require.NoError(t, err)
    require.True(t, active)
    expired, err := s.HasActive(context.Background(), 1, now.AddDate(0, 2, 0))
    require.NoError(t, err)
    require.False(t, expired)
}

// --- TrialGrant ---

func TestTrialGrantStore_UniqueViolation(t *testing.T) {
    db := newTestDB(t)
    s := store.NewTrialGrantStore(db)
    now := time.Now().UTC()
    g := &model.TrialGrant{
        UserID: 1, GrantedAt: now, ExpiresAt: now.AddDate(0, 0, 3),
        CreditsRemaining: 200, Source: model.SourceB2BGrant, CreatedAt: now,
    }
    require.NoError(t, s.Create(context.Background(), db, g))
    g2 := *g
    g2.ID = 0
    err := s.Create(context.Background(), db, &g2)
    require.Error(t, err, "second grant for same user must fail UNIQUE")
}

// --- CreditCycle ---

func TestCreditCycleStore_InsertOrIgnore_ConcurrencyDedup(t *testing.T) {
    db := newTestDB(t)
    s := store.NewCreditCycleStore(db)
    now := time.Now().UTC()
    cc := &model.CreditCycle{
        UserID: 1, SubscriptionID: 1, CycleStart: now,
        CycleEnd: now.AddDate(0, 1, 0),
        CreditsGranted: 2000, CreditsRemaining: 2000,
        CreatedAt: now, UpdatedAt: now,
    }
    require.NoError(t, s.InsertOrIgnore(context.Background(), db, cc))
    cc2 := *cc
    cc2.ID = 0
    require.NoError(t, s.InsertOrIgnore(context.Background(), db, &cc2),
        "duplicate (user_id, cycle_start) must be silent no-op")

    got, err := s.GetByUserAndStart(context.Background(), db, 1, now)
    require.NoError(t, err)
    require.NotNil(t, got)
}

// --- UserBoosterBalance ---

func TestUserBoosterBalanceStore_IncrementCreatesAndAdds(t *testing.T) {
    db := newTestDB(t)
    s := store.NewUserBoosterBalanceStore(db)
    require.NoError(t, s.Increment(context.Background(), db, 1, 600))
    require.NoError(t, s.Increment(context.Background(), db, 1, 1200))
    got, err := s.Get(context.Background(), 1)
    require.NoError(t, err)
    require.NotNil(t, got)
    require.Equal(t, int64(1800), got.CreditsRemaining)
}

// --- MembershipEvent ---

func TestMembershipEventStore_IdempotencyKeyUnique(t *testing.T) {
    db := newTestDB(t)
    s := store.NewMembershipEventStore(db)
    now := time.Now().UTC()
    key := "abc-123"
    months := uint8(1)
    e := &model.MembershipEvent{
        UserID: 1, EventType: model.EventTypeSubGranted, ProductType: model.ProductTypeMonthly,
        Months: &months, AmountCents: 9900, Source: model.SourceB2BGrant,
        IdempotencyKey: &key, OccurredAt: now,
    }
    require.NoError(t, s.Create(context.Background(), db, e))
    e2 := *e
    e2.ID = 0
    err := s.Create(context.Background(), db, &e2)
    require.Error(t, err, "duplicate idempotency_key must violate UNIQUE")

    got, err := s.GetByIdempotencyKey(context.Background(), key)
    require.NoError(t, err)
    require.NotNil(t, got)
    require.Equal(t, e.UserID, got.UserID)
}

func TestMembershipEventStore_GetByIdempotencyKey_NotFound(t *testing.T) {
    db := newTestDB(t)
    s := store.NewMembershipEventStore(db)
    got, err := s.GetByIdempotencyKey(context.Background(), "missing")
    require.NoError(t, err)
    require.Nil(t, got)
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd numind-server && go test ./internal/numind/store/membership/...`
Expected: FAIL with `undefined: store.NewSubscriptionStore` 等。

- [ ] **Step 3: 写最小实现**

`store.go`（聚合接口 + 工厂）：
```go
package membership

import "gorm.io/gorm"

type IMembershipStore interface {
    Subscriptions() ISubscriptionStore
    TrialGrants() ITrialGrantStore
    CreditCycles() ICreditCycleStore
    BoosterBalances() IUserBoosterBalanceStore
    Events() IMembershipEventStore
}

type membershipStore struct{ db *gorm.DB }

func NewMembershipStore(db *gorm.DB) IMembershipStore { return &membershipStore{db: db} }

func (m *membershipStore) Subscriptions() ISubscriptionStore         { return NewSubscriptionStore(m.db) }
func (m *membershipStore) TrialGrants() ITrialGrantStore             { return NewTrialGrantStore(m.db) }
func (m *membershipStore) CreditCycles() ICreditCycleStore           { return NewCreditCycleStore(m.db) }
func (m *membershipStore) BoosterBalances() IUserBoosterBalanceStore { return NewUserBoosterBalanceStore(m.db) }
func (m *membershipStore) Events() IMembershipEventStore             { return NewMembershipEventStore(m.db) }
```

`subscription.go`：
```go
package membership

import (
    "context"
    "errors"
    "fmt"
    "time"

    "gorm.io/gorm"
    "gorm.io/gorm/clause"

    model "numind-server/internal/pkg/model/membership"
)

type ISubscriptionStore interface {
    Get(ctx context.Context, userID uint64) (*model.Subscription, error)
    GetForUpdate(ctx context.Context, tx *gorm.DB, userID uint64) (*model.Subscription, error)
    Create(ctx context.Context, tx *gorm.DB, sub *model.Subscription) error
    Update(ctx context.Context, tx *gorm.DB, sub *model.Subscription) error
    HasActive(ctx context.Context, userID uint64, now time.Time) (bool, error)
}

type subscriptionStore struct{ db *gorm.DB }

func NewSubscriptionStore(db *gorm.DB) ISubscriptionStore { return &subscriptionStore{db: db} }

func (s *subscriptionStore) Get(ctx context.Context, userID uint64) (*model.Subscription, error) {
    var sub model.Subscription
    err := s.db.WithContext(ctx).Where("user_id = ?", userID).Take(&sub).Error
    if errors.Is(err, gorm.ErrRecordNotFound) {
        return nil, nil
    }
    if err != nil {
        return nil, fmt.Errorf("subscription.Get: %w", err)
    }
    return &sub, nil
}

func (s *subscriptionStore) GetForUpdate(ctx context.Context, tx *gorm.DB, userID uint64) (*model.Subscription, error) {
    var sub model.Subscription
    err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
        Where("user_id = ?", userID).Take(&sub).Error
    if errors.Is(err, gorm.ErrRecordNotFound) {
        return nil, nil
    }
    if err != nil {
        return nil, fmt.Errorf("subscription.GetForUpdate: %w", err)
    }
    return &sub, nil
}

func (s *subscriptionStore) Create(ctx context.Context, tx *gorm.DB, sub *model.Subscription) error {
    if err := tx.WithContext(ctx).Create(sub).Error; err != nil {
        return fmt.Errorf("subscription.Create: %w", err)
    }
    return nil
}

func (s *subscriptionStore) Update(ctx context.Context, tx *gorm.DB, sub *model.Subscription) error {
    if err := tx.WithContext(ctx).Save(sub).Error; err != nil {
        return fmt.Errorf("subscription.Update: %w", err)
    }
    return nil
}

func (s *subscriptionStore) HasActive(ctx context.Context, userID uint64, now time.Time) (bool, error) {
    var count int64
    err := s.db.WithContext(ctx).Model(&model.Subscription{}).
        Where("user_id = ? AND expires_at > ?", userID, now).Count(&count).Error
    if err != nil {
        return false, fmt.Errorf("subscription.HasActive: %w", err)
    }
    return count > 0, nil
}
```

`trial_grant.go`：同模式（Get / GetForUpdate / Create / Update / HasActive）。`Get` 不存在返回 `(nil, nil)`；`Create` 重复时由 UNIQUE 索引触发 1062 → 包装为 `fmt.Errorf("trial_grant.Create: %w", err)` 上抛（biz 层在 §3.3 捕获并转 `ErrTrialAlreadyGranted`）。

`credit_cycle.go`：
```go
type ICreditCycleStore interface {
    GetByUserAndStart(ctx context.Context, tx *gorm.DB, userID uint64, cycleStart time.Time) (*model.CreditCycle, error)
    GetByUserAndStartForUpdate(ctx context.Context, tx *gorm.DB, userID uint64, cycleStart time.Time) (*model.CreditCycle, error)
    InsertOrIgnore(ctx context.Context, tx *gorm.DB, cycle *model.CreditCycle) error
    Update(ctx context.Context, tx *gorm.DB, cycle *model.CreditCycle) error
    DeleteExpired(ctx context.Context, tx *gorm.DB, userID uint64, before time.Time) error
}
```

`InsertOrIgnore` 实现关键：
```go
func (s *creditCycleStore) InsertOrIgnore(ctx context.Context, tx *gorm.DB, cycle *model.CreditCycle) error {
    err := tx.WithContext(ctx).Clauses(clause.OnConflict{
        Columns:   []clause.Column{{Name: "user_id"}, {Name: "cycle_start"}},
        DoNothing: true,
    }).Create(cycle).Error
    if err != nil {
        return fmt.Errorf("credit_cycle.InsertOrIgnore: %w", err)
    }
    return nil
}
```

`user_booster_balance.go`：
```go
type IUserBoosterBalanceStore interface {
    Get(ctx context.Context, userID uint64) (*model.UserBoosterBalance, error)
    GetForUpdate(ctx context.Context, tx *gorm.DB, userID uint64) (*model.UserBoosterBalance, error)
    Increment(ctx context.Context, tx *gorm.DB, userID uint64, delta int64) error
    Decrement(ctx context.Context, tx *gorm.DB, userID uint64, delta int64) error
}
```

`Increment` 用 ON CONFLICT 实现 upsert：
```go
func (s *userBoosterBalanceStore) Increment(ctx context.Context, tx *gorm.DB, userID uint64, delta int64) error {
    now := time.Now().UTC()
    err := tx.WithContext(ctx).Clauses(clause.OnConflict{
        Columns: []clause.Column{{Name: "user_id"}},
        DoUpdates: clause.Assignments(map[string]any{
            "credits_remaining": gorm.Expr("credits_remaining + ?", delta),
            "updated_at":        now,
        }),
    }).Create(&model.UserBoosterBalance{
        UserID: userID, CreditsRemaining: delta, UpdatedAt: now,
    }).Error
    if err != nil {
        return fmt.Errorf("booster.Increment: %w", err)
    }
    return nil
}
```

`membership_event.go`：
```go
type IMembershipEventStore interface {
    Create(ctx context.Context, tx *gorm.DB, event *model.MembershipEvent) error
    GetByIdempotencyKey(ctx context.Context, key string) (*model.MembershipEvent, error)
    QueryByGranterAndMonth(ctx context.Context, granterID uint64, monthStart, monthEnd time.Time) ([]model.MembershipEvent, error)
}
```

`GetByIdempotencyKey` 不存在返回 `(nil, nil)`；`QueryByGranterAndMonth` 走半开区间 `[monthStart, monthEnd)` + 命中 `idx_event_granter_occurred`。

**修改 `internal/numind/store/store.go`**：在 `IStore` 接口加 `Membership() membership.IMembershipStore`，`datastore` 添加方法 `func (ds *datastore) Membership() membership.IMembershipStore { return membership.NewMembershipStore(ds.db) }`，并 `import "numind-server/internal/numind/store/membership"`。

**`test_helpers.go`（package membership，文件名 `_test.go` 后缀使其仅 test 可见，供本包测试 + Phase 2 biz/membership 测试通过 internal package import 复用）：**

```go
package membership

import (
    "testing"

    "github.com/stretchr/testify/require"
    "gorm.io/driver/sqlite"
    "gorm.io/gorm"

    storeUserModel "numind-server/internal/pkg/model"
    model "numind-server/internal/pkg/model/membership"
)

// NewTestDB returns an in-memory SQLite *gorm.DB with all 5 membership tables
// + minimal user table required by FK seeding. Exported (capital N) so biz tests
// in sibling packages can import it.
func NewTestDB(t *testing.T) *gorm.DB {
    t.Helper()
    db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
    require.NoError(t, err)
    require.NoError(t, db.AutoMigrate(
        &storeUserModel.User{},
        &model.Subscription{},
        &model.TrialGrant{},
        &model.CreditCycle{},
        &model.UserBoosterBalance{},
        &model.MembershipEvent{},
    ))
    return db
}

// SeedUserPair inserts (parent, child) rows in the user table, with child.parent_user_id=parentID.
// Used by Phase 2 biz tests to satisfy parent-child FK constraints implicitly modelled in spec §1.3.
func SeedUserPair(t *testing.T, db *gorm.DB, parentID, childID uint) {
    t.Helper()
    require.NoError(t, db.Create(&storeUserModel.User{ID: parentID, ParentUserID: nil}).Error)
    require.NoError(t, db.Create(&storeUserModel.User{ID: childID, ParentUserID: &parentID}).Error)
}
```

> 说明：上方 store 测试代码段示例使用了局部 `newTestDB(t)` lambda；Step 3 将其替换为本 helper（包内复用），并通过导出符号 `NewTestDB` / `SeedUserPair` 让 Phase 2 `biz/membership` 测试 import `numind-server/internal/numind/store/membership` 复用。Phase 2 biz tests 内部别名 `newTestDB := store.NewTestDB; seedUserPair := store.SeedUserPair` 保留 §1144 起测试代码可读性。如选择不导出（保持小写），把本 helper 放在共享路径 `numind-server/internal/pkg/testfixtures/membership/helpers.go` 改名 `NewMembershipTestDB`，由所有 test 文件 import。两条路径任选其一，本 task 默认采用前者（同包内导出）以减少包数。

- [ ] **Step 4: 跑测试确认通过**

Run:
```bash
cd numind-server && go test ./internal/numind/store/membership/... -v
cd numind-server && go test ./internal/numind/store/... # 验证 store.go 修改不破坏 datastore
cd numind-server && task lint
```
Expected: 全部 PASS，lint 0 警告

- [ ] **Step 5: Commit**

```bash
cd numind-server
git checkout -b feat/membership-credits-redesign-task-4
git add internal/numind/store/membership/ internal/numind/store/store.go
git commit -m "feat(store): add membership store interfaces for 5 new tables

- ISubscriptionStore: Get/GetForUpdate/Create/Update/HasActive
- ITrialGrantStore: same pattern, UNIQUE violation surfaced to biz
- ICreditCycleStore: InsertOrIgnore via ON CONFLICT for lazy concurrency
- IUserBoosterBalanceStore: Increment/Decrement via upsert
- IMembershipEventStore: append-only Create + QueryByGranterAndMonth

Aggregated under IMembershipStore, registered on IStore.Membership().
Get methods return (nil, nil) on not-found (not gorm.ErrRecordNotFound)
to keep biz callers free of gorm import."
```

---

### Task 4.5：新增 errno（必须在 Phase 2 biz task 之前）

> 集中落地 spec §5.7 + review fixes 中所有新错误码常量。Task 5/10/11/15 等下游 task 直接 import，避免重复定义和 PR 割裂。

**Files:**
- Modify: `numind-server/internal/pkg/errno/errno.go`（增加 8 个常量定义 + httpStatus 映射）
- Create / Modify: `numind-server/internal/pkg/errno/errno_test.go`（验证 code/message/HTTP 映射）

**新增错误码清单**（spec §5.7 + review fixes）：

| 常量名 | Code | HTTP | Message |
|---|---|---|---|
| `ErrTrialAlreadyGranted` | 40901 | 409 | 该账户已使用过体验包 |
| `ErrTrialNotAllowedForActivePro` | 40902 | 409 | 已是 Pro 会员，不能再开通试用包 |
| `ErrChildNotMember` | 40301 | 403 | 子账户当前不是会员 |
| `ErrNotActiveMember` | 40302 | 403 | 当前不是会员状态 |
| `ErrBoosterQuantityExceedsLimit` | 40001 | 400 | 单次最多购买 10000 份 |
| `ErrSubscriptionExpired` | 41001 | 410 | 订阅已过期 |
| `ErrIdempotencyKeyConflict` | 40903 | 409 | 幂等键冲突（同一 key 不同请求体） |
| `ErrSystemMaintenance` | 50301 | 503 | 系统维护中 |

**TDD 五段式（轻量）：**

- [ ] **Step 1**: 在 `errno_test.go` 添加 8 个 sub-test，断言 `errno.ErrXxx.Code == NNNNN`、`errno.ErrXxx.HTTP == M`、`errno.ErrXxx.Message == "..."`。同时跑 → FAIL（常量未定义）。
- [ ] **Step 2**: 在 `errno.go` 落地 8 个常量定义（沿用项目现有 errno 风格：`var ErrXxx = &Errno{Code: NNNNN, Message: "...", HTTP: M}`）。
- [ ] **Step 3**: `cd numind-server && go test ./internal/pkg/errno/... && task lint` → PASS。
- [ ] **Step 4**: 手动检查：grep 确认 8 个常量在 errno.go 都有定义，未与已有 code 冲突（建议每个新增 task 前先 `grep "Code: 40901"` 等）。
- [ ] **Step 5**: Commit

```
feat(errno): add 8 errnos for membership credits redesign

- ErrTrialAlreadyGranted (40901, 409)
- ErrTrialNotAllowedForActivePro (40902, 409)
- ErrChildNotMember (40301, 403)
- ErrNotActiveMember (40302, 403)
- ErrBoosterQuantityExceedsLimit (40001, 400)
- ErrSubscriptionExpired (41001, 410)
- ErrIdempotencyKeyConflict (40903, 409)
- ErrSystemMaintenance (50301, 503; runtime registration in Task 15)

All consumed by downstream Phase 2/3 tasks (5/10/11/15). Centralized
here to avoid PR fragmentation and code conflicts.

Spec: §5.7
```

> 注：Task 15 仍负责 `MAINTENANCE_MODE` 中间件注册和实际 503 响应行为，但常量定义在本 task 集中落地，确保 Phase 2/3 task 可以直接 `import "numind-server/internal/pkg/errno"` 引用。

---

**Phase 1 完成判定**：5 个 task（含 4.5 errno）全部 commit 后，dev DB 有 5 张新表 + 索引就绪、GORM model 可读写、anchor 函数可用、store 接口对 biz 层暴露完毕、新错误码常量集中落地。Phase 2（biz 层 grant/renew/deduct 算法）可在此基础上启动，无前置阻塞。

---

# Phase 2: 算法 biz 函数 — Tasks 5-8

> 本文件覆盖 NDF S3 计划的 Phase 2，对应 spec §3.2 / §3.3 / §3.4 / §3.5 / §3.6 / §3.7。
> 每个 task 严格按 TDD 五段式：(1) 写测试 → (2) 跑失败 → (3) 实现 → (4) 跑通过 → (5) commit。
> 所有事务体内 timestamp 取 `txNow := time.Now().UTC()`（§4.6 硬规则）。
> 所有 SELECT FOR UPDATE 锁顺序：`credit_cycle < membership_event < subscription < trial_grant < user_booster_balance`（§4.1 硬规则）。

---

### Task 5：GrantTrial biz 函数（spec §3.3）

**Files:**
- Create: `numind-server/internal/numind/biz/membership/trial.go`
- Create: `numind-server/internal/numind/biz/membership/trial_test.go`

**TDD 五段式**

- [ ] **Step 1: 写测试（先红）**

`trial_test.go` 必须覆盖以下 case，使用 in-memory SQLite + AutoMigrate 5 张表（fixtures helper 在 Task 4 已落地，本 task 直接复用 `newTestDB(t)` 与 `seedUserPair(t,db,parentID,childID)`）。

```go
func TestGrantTrial_HappyPath(t *testing.T) {
    db := newTestDB(t)
    seedUserPair(t, db, 10, 20) // parent=10, child=20
    svc := NewMembershipService(db)
    res, err := svc.GrantTrial(context.Background(), 10, 20, "uuid-1")
    require.NoError(t, err)
    require.NotNil(t, res)
    assert.Equal(t, int64(200), res.CreditsGranted)
    // INV-8: 固定 3 天
    assert.WithinDuration(t, res.GrantedAt.Add(72*time.Hour), res.ExpiresAt, time.Second)
    // 行落地 + INV-7 (UNIQUE)
    var trial model.TrialGrant
    require.NoError(t, db.Where("user_id=?", 20).Take(&trial).Error)
    assert.Equal(t, int64(200), trial.CreditsRemaining)
    // event 落地（含 idempotency_key）
    var evt model.MembershipEvent
    require.NoError(t, db.Where("idempotency_key=?", "uuid-1").Take(&evt).Error)
    assert.Equal(t, "trial_granted", evt.EventType)
}

func TestGrantTrial_AlreadyGranted(t *testing.T) {
    db := newTestDB(t)
    seedUserPair(t, db, 10, 20)
    svc := NewMembershipService(db)
    _, err := svc.GrantTrial(context.Background(), 10, 20, "uuid-1")
    require.NoError(t, err)
    _, err = svc.GrantTrial(context.Background(), 10, 20, "uuid-2") // 不同 idem，仍应拒
    assert.ErrorIs(t, err, errno.ErrTrialAlreadyGranted)
}

func TestGrantTrial_BlockedByActivePro(t *testing.T) {
    db := newTestDB(t)
    seedUserPair(t, db, 10, 20)
    // 预置子账户在期 sub
    db.Create(&model.Subscription{
        UserID: 20, FirstStartedAt: time.Now(), CurrentStartedAt: time.Now(),
        TotalMonthsPurchased: 3, ExpiresAt: time.Now().Add(60 * 24 * time.Hour),
        Source: "b2b_grant",
    })
    svc := NewMembershipService(db)
    _, err := svc.GrantTrial(context.Background(), 10, 20, "uuid-1")
    assert.ErrorIs(t, err, errno.ErrTrialNotAllowedForActivePro)
    // 校验顺序硬规则：trial_grant 表为空，sub 检查命中
    var n int64
    db.Model(&model.TrialGrant{}).Where("user_id=?", 20).Count(&n)
    assert.Equal(t, int64(0), n)
}

func TestGrantTrial_IdempotencyReplay_SameBody(t *testing.T) {
    db := newTestDB(t)
    seedUserPair(t, db, 10, 20)
    svc := NewMembershipService(db)
    r1, err := svc.GrantTrial(context.Background(), 10, 20, "uuid-1")
    require.NoError(t, err)
    r2, err := svc.GrantTrial(context.Background(), 10, 20, "uuid-1") // 重放
    require.NoError(t, err)
    assert.Equal(t, r1.EventID, r2.EventID)
    assert.Equal(t, r1.TrialGrantID, r2.TrialGrantID)
    // 仅 1 行 trial + 1 行 event
    var n int64
    db.Model(&model.MembershipEvent{}).Count(&n)
    assert.Equal(t, int64(1), n)
}

func TestGrantTrial_IdempotencyConflict_DifferentBody(t *testing.T) {
    db := newTestDB(t)
    seedUserPair(t, db, 10, 20)
    seedUserPair(t, db, 10, 21) // 第二个 child
    svc := NewMembershipService(db)
    _, err := svc.GrantTrial(context.Background(), 10, 20, "uuid-1")
    require.NoError(t, err)
    _, err = svc.GrantTrial(context.Background(), 10, 21, "uuid-1") // 同 key, 不同 child
    assert.ErrorIs(t, err, errno.ErrIdempotencyKeyConflict)
}
```

- [ ] **Step 2: 跑失败**

```bash
cd numind-server && go test ./internal/numind/biz/membership/ -run TestGrantTrial -v
```

期望全部 FAIL（trial.go 还没实现）。

- [ ] **Step 3: 实现**

`trial.go` 完整实现 spec §3.3 Go 伪代码，落地：

```go
package membership

import (
    "context"
    "errors"
    "fmt"
    "time"

    "gorm.io/gorm"
    "gorm.io/gorm/clause"

    "numind-server/internal/pkg/errno"
    "numind-server/internal/pkg/model"
)

const (
    trialCreditsAmount = int64(200)
    trialDuration      = 3 * 24 * time.Hour
)

type GrantTrialResult struct {
    EventID        uint64
    TrialGrantID   uint64
    GrantedAt      time.Time
    ExpiresAt      time.Time
    CreditsGranted int64
}

func (s *MembershipService) GrantTrial(
    ctx context.Context, parentID, childID uint64, idempotencyKey string,
) (*GrantTrialResult, error) {
    if err := validateTrialInput(s.db.WithContext(ctx), parentID, childID, idempotencyKey); err != nil {
        return nil, err
    }
    var result *GrantTrialResult
    err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
        txNow := time.Now().UTC()

        // [1] 幂等检查（请求体校验：trial 仅 child + event_type）
        var existing model.MembershipEvent
        if e := tx.Where("idempotency_key = ?", idempotencyKey).Take(&existing).Error; e == nil {
            if existing.UserID != childID || existing.EventType != "trial_granted" {
                return errno.ErrIdempotencyKeyConflict
            }
            result = decodeTrialResultFromEvent(&existing)
            return nil
        } else if !errors.Is(e, gorm.ErrRecordNotFound) {
            return fmt.Errorf("idempotency lookup: %w", e)
        }

        // [2] 字典序加锁：先 subscription 后 trial_grant
        var sub model.Subscription
        subErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
            Where("user_id = ?", childID).Take(&sub).Error
        if subErr != nil && !errors.Is(subErr, gorm.ErrRecordNotFound) {
            return fmt.Errorf("lock sub: %w", subErr)
        }

        var existingTrial model.TrialGrant
        trialErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
            Where("user_id = ?", childID).Take(&existingTrial).Error
        if trialErr != nil && !errors.Is(trialErr, gorm.ErrRecordNotFound) {
            return fmt.Errorf("lock trial: %w", trialErr)
        }

        // [3] 校验序：trial 在前
        if trialErr == nil {
            return errno.ErrTrialAlreadyGranted
        }
        // [4] sub 在期检查
        if subErr == nil && sub.ExpiresAt.After(txNow) {
            return errno.ErrTrialNotAllowedForActivePro
        }

        // [5] 创建 trial_grant
        trial := model.TrialGrant{
            UserID:         childID,
            GrantedAt:      txNow,
            ExpiresAt:      txNow.Add(trialDuration),
            CreditsGranted: trialCreditsAmount,
            CreditsRemaining: trialCreditsAmount,
            GranterUserID:  &parentID,
        }
        if err := tx.Create(&trial).Error; err != nil {
            if isUniqueViolation(err, "uniq_trial_user") {
                return errno.ErrTrialAlreadyGranted
            }
            return fmt.Errorf("create trial: %w", err)
        }

        // [6] event
        evt := model.MembershipEvent{
            IdempotencyKey: idempotencyKey,
            EventType:      "trial_granted",
            UserID:         childID,
            GranterUserID:  &parentID,
            ProductType:    "trial",
            Months:         0,
            AmountCents:    0,
            OccurredAt:     txNow,
            PayloadJSON:    encodeTrialPayload(&trial),
        }
        if err := tx.Create(&evt).Error; err != nil {
            if isUniqueViolation(err, "uk_membership_event_idem") {
                _ = tx.Where("idempotency_key = ?", idempotencyKey).Take(&existing).Error
                if existing.UserID != childID || existing.EventType != "trial_granted" {
                    return errno.ErrIdempotencyKeyConflict
                }
                result = decodeTrialResultFromEvent(&existing)
                return nil
            }
            return fmt.Errorf("insert event: %w", err)
        }

        result = &GrantTrialResult{
            EventID: evt.ID, TrialGrantID: trial.ID,
            GrantedAt: trial.GrantedAt, ExpiresAt: trial.ExpiresAt,
            CreditsGranted: trialCreditsAmount,
        }
        return nil
    })
    if err != nil {
        return nil, err
    }
    return result, nil
}
```

`isUniqueViolation(err, indexName)` helper 区分 `uniq_trial_user` 与 `uk_membership_event_idem`：MySQL 用 `mysql.MySQLError` 判 errno=1062 + 字符串 contains indexName；SQLite (test) 用 `errors.Is(err, gorm.ErrDuplicatedKey)` + Error 字符串 contains。

- [ ] **Step 4: 跑通过**

```bash
cd numind-server && go test ./internal/numind/biz/membership/ -run TestGrantTrial -v && task lint
```

5 个 case 全 PASS。

- [ ] **Step 5: commit**

```
feat(membership): GrantTrial biz function with lifetime UNIQUE + idem replay

- 实现 §3.3 GrantTrial 函数：trial 200 积分 / 3 天有效期 / lifetime 单次
- 加锁顺序按 §4.1 字典序：subscription 先 / trial_grant 后
- 校验顺序：trial_grant 已存在 → ErrTrialAlreadyGranted；sub 在期 → ErrTrialNotAllowedForActivePro
- idempotency_key UNIQUE 兜底并发；同 key 不同 body → ErrIdempotencyKeyConflict
- 5 个测试 case 全部通过

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
```

---

### Task 6：GrantOrRenewSubscription biz 函数（三场景统一，spec §3.2）

**Files:**
- Create: `numind-server/internal/numind/biz/membership/subscription.go`
- Create: `numind-server/internal/numind/biz/membership/subscription_test.go`

**TDD 五段式**

- [ ] **Step 1: 写测试（先红）**

```go
// 场景 A：新开通
func TestGrantSub_New(t *testing.T) {
    db := newTestDB(t)
    seedUserPair(t, db, 10, 20)
    svc := NewMembershipService(db)
    res, err := svc.GrantOrRenewSubscription(context.Background(), 10, 20, "monthly", 3, "k1")
    require.NoError(t, err)
    assert.Equal(t, "new", res.Scenario)
    assert.Equal(t, 3, res.TotalMonthsPurchased)
    assert.True(t, res.FirstStartedAt.Equal(res.CurrentStartedAt))
    // INV-4: expires_at = anchor + 3
    assert.WithinDuration(t, AnchorAddMonths(res.CurrentStartedAt, 3), res.ExpiresAt, time.Second)
}

// 场景 B：在期续费 + day 漂移防护（spec §3.2 测试 case 2）
func TestGrantSub_RenewAnchorPreserved(t *testing.T) {
    db := newTestDB(t)
    seedUserPair(t, db, 10, 20)
    anchor := time.Date(2026, 1, 31, 10, 0, 0, 0, time.UTC)
    db.Create(&model.Subscription{
        UserID: 20, FirstStartedAt: anchor, CurrentStartedAt: anchor,
        TotalMonthsPurchased: 3, ExpiresAt: AnchorAddMonths(anchor, 3),
        Source: "b2b_grant",
    })
    svc := NewMembershipService(db)
    res, err := svc.GrantOrRenewSubscription(context.Background(), 10, 20, "monthly", 1, "k1")
    require.NoError(t, err)
    assert.Equal(t, "renew", res.Scenario)
    assert.Equal(t, 4, res.TotalMonthsPurchased)
    expected := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)
    assert.True(t, res.ExpiresAt.Equal(expected), "anchor day=31 must be preserved")
}

// 场景 C：过期再开 + 历史 cycle 清理
func TestGrantSub_ReopenCleansStaleCycles(t *testing.T) {
    db := newTestDB(t)
    seedUserPair(t, db, 10, 20)
    pastAnchor := time.Now().Add(-180 * 24 * time.Hour)
    sub := model.Subscription{
        UserID: 20, FirstStartedAt: pastAnchor, CurrentStartedAt: pastAnchor,
        TotalMonthsPurchased: 3, ExpiresAt: pastAnchor.Add(90 * 24 * time.Hour),
        Source: "b2b_grant",
    }
    db.Create(&sub)
    db.Create(&model.CreditCycle{
        UserID: 20, SubscriptionID: sub.ID,
        CycleStart: pastAnchor, CycleEnd: pastAnchor.Add(30 * 24 * time.Hour),
        CreditsGranted: 2000, CreditsRemaining: 0,
    })
    svc := NewMembershipService(db)
    res, err := svc.GrantOrRenewSubscription(context.Background(), 10, 20, "monthly", 2, "k1")
    require.NoError(t, err)
    assert.Equal(t, "reopen", res.Scenario)
    // first_started_at 不变
    assert.True(t, res.FirstStartedAt.Equal(pastAnchor))
    // current_started_at 重置
    assert.True(t, res.CurrentStartedAt.After(pastAnchor))
    // 历史 cycle 被清空
    var n int64
    db.Model(&model.CreditCycle{}).Where("user_id=?", 20).Count(&n)
    assert.Equal(t, int64(0), n)
}

// 入参校验：自购禁止
func TestGrantSub_SelfPurchaseDisabled(t *testing.T) {
    db := newTestDB(t)
    seedUserPair(t, db, 10, 20)
    svc := NewMembershipService(db)
    _, err := svc.GrantOrRenewSubscription(context.Background(), 20, 20, "monthly", 3, "k1")
    assert.ErrorIs(t, err, errno.ErrSelfPurchaseDisabled)
}

// 入参校验：months 越界
func TestGrantSub_InvalidMonths(t *testing.T) {
    db := newTestDB(t)
    seedUserPair(t, db, 10, 20)
    svc := NewMembershipService(db)
    _, err := svc.GrantOrRenewSubscription(context.Background(), 10, 20, "monthly", 13, "k1")
    assert.ErrorIs(t, err, errno.ErrInvalidMonths)
}

// 幂等重放
func TestGrantSub_IdempotencyReplay(t *testing.T) {
    db := newTestDB(t)
    seedUserPair(t, db, 10, 20)
    svc := NewMembershipService(db)
    r1, err := svc.GrantOrRenewSubscription(context.Background(), 10, 20, "monthly", 3, "k1")
    require.NoError(t, err)
    r2, err := svc.GrantOrRenewSubscription(context.Background(), 10, 20, "monthly", 3, "k1")
    require.NoError(t, err)
    assert.Equal(t, r1.EventID, r2.EventID)
    assert.Equal(t, r1.ExpiresAt, r2.ExpiresAt)
    var n int64
    db.Model(&model.MembershipEvent{}).Count(&n)
    assert.Equal(t, int64(1), n)
}

// 幂等冲突：同 key 不同 months
func TestGrantSub_IdempotencyConflict(t *testing.T) {
    db := newTestDB(t)
    seedUserPair(t, db, 10, 20)
    svc := NewMembershipService(db)
    _, err := svc.GrantOrRenewSubscription(context.Background(), 10, 20, "monthly", 3, "k1")
    require.NoError(t, err)
    _, err = svc.GrantOrRenewSubscription(context.Background(), 10, 20, "monthly", 6, "k1")
    assert.ErrorIs(t, err, errno.ErrIdempotencyKeyConflict)
}
```

- [ ] **Step 2: 跑失败**

```bash
cd numind-server && go test ./internal/numind/biz/membership/ -run TestGrantSub -v
```

7 case 全 FAIL。

- [ ] **Step 3: 实现**

`subscription.go` 落地 spec §3.2 Go 伪代码（三场景 switch + cycle 清理 + event 写入）。关键代码块：

```go
func (s *MembershipService) GrantOrRenewSubscription(
    ctx context.Context, parentID, childID uint64,
    productType string, months int, idempotencyKey string,
) (*GrantResult, error) {
    if err := validateGrantInput(s.db.WithContext(ctx), parentID, childID, productType, months, idempotencyKey); err != nil {
        return nil, err
    }
    var result *GrantResult
    err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
        txNow := time.Now().UTC()

        // [1] 幂等
        var existing model.MembershipEvent
        if e := tx.Where("idempotency_key = ?", idempotencyKey).Take(&existing).Error; e == nil {
            if existing.UserID != childID || existing.ProductType != productType || existing.Months != months {
                return errno.ErrIdempotencyKeyConflict
            }
            result = decodeGrantResultFromEvent(&existing)
            return nil
        } else if !errors.Is(e, gorm.ErrRecordNotFound) {
            return fmt.Errorf("idempotency lookup: %w", e)
        }

        // [2] SELECT FOR UPDATE sub
        var sub model.Subscription
        subErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
            Where("user_id = ?", childID).Take(&sub).Error

        scenario := ""
        switch {
        case errors.Is(subErr, gorm.ErrRecordNotFound):
            scenario = "new"
            sub = model.Subscription{
                UserID: childID, FirstStartedAt: txNow, CurrentStartedAt: txNow,
                TotalMonthsPurchased: months,
                ExpiresAt:            AnchorAddMonths(txNow, months),
                Source:               "b2b_grant", GranterUserID: &parentID,
            }
            if err := tx.Create(&sub).Error; err != nil {
                return fmt.Errorf("create sub: %w", err)
            }
        case subErr != nil:
            return fmt.Errorf("lock sub: %w", subErr)
        case sub.ExpiresAt.After(txNow):
            scenario = "renew"
            sub.TotalMonthsPurchased += months
            sub.ExpiresAt = AnchorAddMonths(sub.CurrentStartedAt, sub.TotalMonthsPurchased)
            if err := tx.Model(&sub).Updates(map[string]any{
                "total_months_purchased": sub.TotalMonthsPurchased,
                "expires_at":             sub.ExpiresAt,
            }).Error; err != nil {
                return fmt.Errorf("update sub renew: %w", err)
            }
        default:
            scenario = "reopen"
            sub.CurrentStartedAt = txNow
            sub.TotalMonthsPurchased = months
            sub.ExpiresAt = AnchorAddMonths(txNow, months)
            sub.Source = "b2b_grant"
            sub.GranterUserID = &parentID
            if err := tx.Model(&sub).Updates(map[string]any{
                "current_started_at":     sub.CurrentStartedAt,
                "total_months_purchased": sub.TotalMonthsPurchased,
                "expires_at":             sub.ExpiresAt,
                "source":                 sub.Source,
                "granter_user_id":        sub.GranterUserID,
            }).Error; err != nil {
                return fmt.Errorf("update sub reopen: %w", err)
            }
            // 清理上一轮过期 cycle
            if err := tx.Where("user_id = ? AND cycle_end <= ?", childID, txNow).
                Delete(&model.CreditCycle{}).Error; err != nil {
                return fmt.Errorf("cleanup stale cycles: %w", err)
            }
        }

        // [3] event
        eventType := map[string]string{"new": "sub_granted", "renew": "sub_renewed", "reopen": "sub_reopened"}[scenario]
        evt := model.MembershipEvent{
            IdempotencyKey: idempotencyKey, EventType: eventType,
            UserID: childID, GranterUserID: &parentID,
            ProductType: productType, Months: months,
            AmountCents: int64(months) * monthlyPriceCents,
            OccurredAt:  txNow,
            PayloadJSON: encodeGrantPayload(&sub),
        }
        if err := tx.Create(&evt).Error; err != nil {
            if isUniqueViolation(err, "uk_membership_event_idem") {
                _ = tx.Where("idempotency_key = ?", idempotencyKey).Take(&existing).Error
                if existing.UserID != childID || existing.ProductType != productType || existing.Months != months {
                    return errno.ErrIdempotencyKeyConflict
                }
                result = decodeGrantResultFromEvent(&existing)
                return nil
            }
            return fmt.Errorf("insert event: %w", err)
        }
        result = &GrantResult{
            EventID: evt.ID, SubscriptionID: sub.ID,
            FirstStartedAt: sub.FirstStartedAt, CurrentStartedAt: sub.CurrentStartedAt,
            ExpiresAt: sub.ExpiresAt, TotalMonthsPurchased: sub.TotalMonthsPurchased,
            Scenario: scenario,
        }
        return nil
    })
    if err != nil {
        return nil, err
    }
    return result, nil
}
```

- [ ] **Step 4: 跑通过**

```bash
cd numind-server && go test ./internal/numind/biz/membership/ -run TestGrantSub -v && task lint
```

7 case 全 PASS。

- [ ] **Step 5: commit**

```
feat(membership): GrantOrRenewSubscription with new/renew/reopen scenarios

- §3.2 三场景统一 grant：new/renew/reopen
- INV-4: expires_at 恒等于 AnchorAddMonths(current_started_at, total_months_purchased)
- INV-5: first_started_at <= current_started_at（reopen 保留首开归属）
- reopen 路径清理历史 cycle（保持 GetBalance 干净）
- SELECT FOR UPDATE 防 lost update（§4.3）
- idempotency 重放 + 冲突双路径
- 7 个测试 case 全部通过
```

---

### Task 7：ensureCurrentCycle 懒创建 + DeductCredits 改写（spec §3.4 / §3.5）

**Files:**
- Create: `numind-server/internal/numind/biz/membership/cycle.go`
- Modify: `numind-server/internal/numind/biz/credit/credit.go`（DeductCredits 改写）
- Create: `numind-server/internal/numind/biz/membership/cycle_test.go`
- Create: `numind-server/internal/numind/biz/credit/deduct_test.go`

**TDD 五段式**

- [ ] **Step 1: 写测试（先红）**

`cycle_test.go`：

```go
func TestEnsureCycle_FirstCreate(t *testing.T) {
    db := newTestDB(t)
    seedUserPair(t, db, 10, 20)
    anchor := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
    sub := &model.Subscription{
        UserID: 20, FirstStartedAt: anchor, CurrentStartedAt: anchor,
        TotalMonthsPurchased: 3, ExpiresAt: AnchorAddMonths(anchor, 3),
    }
    db.Create(sub)
    svc := NewMembershipService(db)
    txNow := time.Date(2026, 1, 15, 14, 0, 0, 0, time.UTC)
    var got *model.CreditCycle
    require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
        c, err := svc.EnsureCurrentCycle(tx, 20, sub, txNow) // exported for test
        got = c
        return err
    }))
    assert.True(t, got.CycleStart.Equal(anchor))
    assert.True(t, got.CycleEnd.Equal(AnchorAddMonths(anchor, 1)))
    assert.Equal(t, int64(2000), got.CreditsRemaining)
}

func TestEnsureCycle_AnchorDayDriftSecondMonth(t *testing.T) {
    db := newTestDB(t)
    seedUserPair(t, db, 10, 20)
    anchor := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
    sub := &model.Subscription{UserID: 20, CurrentStartedAt: anchor, ExpiresAt: AnchorAddMonths(anchor, 3)}
    db.Create(sub)
    svc := NewMembershipService(db)
    txNow := time.Date(2026, 2, 20, 0, 0, 0, 0, time.UTC)
    var got *model.CreditCycle
    require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
        c, err := svc.EnsureCurrentCycle(tx, 20, sub, txNow)
        got = c
        return err
    }))
    assert.Equal(t, time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC), got.CycleStart)
    assert.Equal(t, time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC), got.CycleEnd)
}

func TestEnsureCycle_ConcurrentSingleRow(t *testing.T) {
    db := newTestDB(t)
    seedUserPair(t, db, 10, 20)
    anchor := time.Now().UTC().Truncate(time.Hour)
    sub := &model.Subscription{UserID: 20, CurrentStartedAt: anchor, ExpiresAt: AnchorAddMonths(anchor, 3)}
    db.Create(sub)
    svc := NewMembershipService(db)
    var wg sync.WaitGroup
    for i := 0; i < 5; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            _ = db.Transaction(func(tx *gorm.DB) error {
                _, err := svc.EnsureCurrentCycle(tx, 20, sub, time.Now().UTC())
                return err
            })
        }()
    }
    wg.Wait()
    var n int64
    db.Model(&model.CreditCycle{}).Where("user_id=?", 20).Count(&n)
    assert.Equal(t, int64(1), n, "INV-10: only one row per (user_id, cycle_start)")
}
```

`deduct_test.go`：

```go
// AC-6 序列：trial 200 + cycle 2000 + booster 1200
func TestDeduct_PrioritySequence(t *testing.T) {
    db, svc, userID := setupActiveUserWithAllThreePools(t) // helper: trial 200 + sub active + cycle 2000 + booster 1200
    d, err := svc.DeductCredits(context.Background(), userID, 250)
    require.NoError(t, err)
    assert.Equal(t, int64(200), d.FromTrial)
    assert.Equal(t, int64(50), d.FromCycle)
    assert.Equal(t, int64(0), d.FromBooster)

    d, err = svc.DeductCredits(context.Background(), userID, 1950)
    require.NoError(t, err)
    assert.Equal(t, int64(1950), d.FromCycle)

    d, err = svc.DeductCredits(context.Background(), userID, 500)
    require.NoError(t, err)
    assert.Equal(t, int64(500), d.FromBooster)
}

// AC-7 booster 冻结：sub 过期 + booster 1200 → 不可扣
func TestDeduct_BoosterFrozenWhenExpired(t *testing.T) {
    db, svc, userID := setupExpiredSubWithBooster(t, 1200) // sub.expires_at < now, booster 1200
    _, err := svc.DeductCredits(context.Background(), userID, 100)
    assert.ErrorIs(t, err, errno.ErrInsufficientCredits)
    var b model.UserBoosterBalance
    require.NoError(t, db.Where("user_id=?", userID).Take(&b).Error)
    assert.Equal(t, int64(1200), b.CreditsRemaining, "INV-15: 冻结时 booster 不被修改")
}

// AC-8 解冻：reopen sub 后 booster 可扣
func TestDeduct_BoosterUnfrozenAfterReopen(t *testing.T) {
    db, svc, userID := setupExpiredSubWithBooster(t, 1200)
    _, err := svc.GrantOrRenewSubscription(context.Background(), parentOf(userID), userID, "monthly", 1, "k-reopen")
    require.NoError(t, err)
    d, err := svc.DeductCredits(context.Background(), userID, 100)
    require.NoError(t, err)
    // 优先扣 cycle（reopen 后 cycle 满 2000）
    assert.Equal(t, int64(100), d.FromCycle)
    assert.Equal(t, int64(0), d.FromBooster)
    _ = db
}

// 全部用尽
func TestDeduct_Insufficient(t *testing.T) {
    _, svc, userID := setupActiveUserWithAllThreePools(t) // 200+2000+1200 = 3400
    _, err := svc.DeductCredits(context.Background(), userID, 4000)
    assert.ErrorIs(t, err, errno.ErrInsufficientCredits)
}

// trial 用尽 → cycle
func TestDeduct_TrialThenCycle(t *testing.T) {
    _, svc, userID := setupActiveUserWithAllThreePools(t)
    d, _ := svc.DeductCredits(context.Background(), userID, 200) // trial 全扣
    assert.Equal(t, int64(200), d.FromTrial)
    d, _ = svc.DeductCredits(context.Background(), userID, 100)
    assert.Equal(t, int64(0), d.FromTrial)
    assert.Equal(t, int64(100), d.FromCycle)
}

// amount 非法
func TestDeduct_InvalidAmount(t *testing.T) {
    _, svc, userID := setupActiveUserWithAllThreePools(t)
    _, err := svc.DeductCredits(context.Background(), userID, 0)
    assert.ErrorIs(t, err, errno.ErrBind)
}
```

- [ ] **Step 2: 跑失败**

```bash
cd numind-server && go test ./internal/numind/biz/membership/ -run TestEnsureCycle -v
cd numind-server && go test ./internal/numind/biz/credit/ -run TestDeduct -v
```

全 FAIL。

- [ ] **Step 3: 实现**

`cycle.go` 落地 spec §3.4 完整代码（含 `computeCycleIndex` + `ON CONFLICT DO NOTHING` + 重 SELECT FOR UPDATE）。`credit.go` 改写 `DeductCredits` 为 spec §3.5 完整伪代码（pre-snapshot sub → ensureCurrentCycle → 字典序锁 sub/trial/booster → 优先级扣减 → UpdateColumn 各表）。Reserve/Reconcile 现有签名不变（§3.8），内部 estimateCredits 改调 `MembershipService.DeductCredits`。

> **命名约定（spec compliance fix）**：spec §3.4 在伪代码层用 `ensureCurrentCycle`（小写首字母）。本 task 测试文件位于同包 `package membership`（即 `cycle_test.go` 不是 `cycle_external_test.go`），可直接调用未导出函数 `ensureCurrentCycle`，无需对外导出。Step 1 测试代码段中的 `svc.EnsureCurrentCycle(...)` 实为示意 receiver method 调用，落地实现时应保持小写 `svc.ensureCurrentCycle(...)` 与 spec 一致。命名差异已记录在 spec compliance review，无业务影响。

- [ ] **Step 4: 跑通过**

```bash
cd numind-server && go test ./internal/numind/biz/membership/ ./internal/numind/biz/credit/ -v && task lint
```

cycle 3 case + deduct 6 case 全 PASS。

- [ ] **Step 5: commit**

```
feat(membership): ensureCurrentCycle lazy create + DeductCredits 3-pool priority

- §3.4 ensureCurrentCycle：ON CONFLICT DO NOTHING + 重 SELECT FOR UPDATE
- INV-10: UNIQUE(user_id, cycle_start) 强制单行；并发证明见 §4.4
- §3.5 DeductCredits：字典序锁 cycle < sub < trial < booster
- 优先级 trial → cycle → booster；booster 冻结条件 (!subActive && !trialActive)
- INV-15: 冻结时 booster 行 credits_remaining 不被修改（事务回滚验证）
- 与现有 Reserve/Reconcile 接入点保持兼容（§3.8）
- 9 个测试 case 全部通过
```

---

### Task 8：GetMembershipState + GetBalance（spec §3.6 / §3.7）

**Files:**
- Create: `numind-server/internal/numind/biz/membership/state.go`
- Create: `numind-server/internal/numind/biz/membership/state_test.go`

**TDD 五段式**

- [ ] **Step 1: 写测试（先红）**

```go
// 三状态 free / trial / pro / trial+pro 叠加
func TestGetMembershipState_Free(t *testing.T) {
    db := newTestDB(t); seedUserPair(t, db, 10, 20)
    svc := NewMembershipService(db)
    s, err := svc.GetMembershipState(context.Background(), 20, time.Now())
    require.NoError(t, err)
    assert.Equal(t, "free", s.DisplayState)
    assert.True(t, s.BoosterFrozen) // INV-17
}

func TestGetMembershipState_TrialOnly(t *testing.T) {
    db := newTestDB(t); seedUserPair(t, db, 10, 20)
    db.Create(&model.TrialGrant{
        UserID: 20, GrantedAt: time.Now(),
        ExpiresAt: time.Now().Add(48 * time.Hour),
        CreditsGranted: 200, CreditsRemaining: 150,
    })
    svc := NewMembershipService(db)
    s, _ := svc.GetMembershipState(context.Background(), 20, time.Now())
    assert.Equal(t, "trial", s.DisplayState)
    assert.True(t, s.TrialActive)
    assert.False(t, s.BoosterFrozen)
}

func TestGetMembershipState_ProOnly(t *testing.T) {
    db := newTestDB(t); seedUserPair(t, db, 10, 20)
    db.Create(&model.Subscription{
        UserID: 20, FirstStartedAt: time.Now(), CurrentStartedAt: time.Now(),
        TotalMonthsPurchased: 3, ExpiresAt: time.Now().Add(60 * 24 * time.Hour),
    })
    svc := NewMembershipService(db)
    s, _ := svc.GetMembershipState(context.Background(), 20, time.Now())
    assert.Equal(t, "pro", s.DisplayState)
    assert.True(t, s.SubActive)
    assert.False(t, s.BoosterFrozen)
}

// US-2: trial + pro 叠加 → trial 优先显示
func TestGetMembershipState_TrialOverlapsPro(t *testing.T) {
    db := newTestDB(t); seedUserPair(t, db, 10, 20)
    now := time.Now()
    db.Create(&model.TrialGrant{
        UserID: 20, GrantedAt: now, ExpiresAt: now.Add(48 * time.Hour),
        CreditsGranted: 200, CreditsRemaining: 200,
    })
    db.Create(&model.Subscription{
        UserID: 20, FirstStartedAt: now, CurrentStartedAt: now,
        TotalMonthsPurchased: 3, ExpiresAt: now.Add(60 * 24 * time.Hour),
    })
    svc := NewMembershipService(db)
    s, _ := svc.GetMembershipState(context.Background(), 20, now)
    assert.Equal(t, "trial", s.DisplayState, "trial 优先于 pro 显示")
    assert.True(t, s.TrialActive)
    assert.True(t, s.SubActive)
    assert.False(t, s.BoosterFrozen)
}
```

`GetBalance` 测试：

```go
func TestGetBalance_NoCycleRowYet(t *testing.T) {
    db := newTestDB(t); seedUserPair(t, db, 10, 20)
    anchor := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
    db.Create(&model.Subscription{
        UserID: 20, FirstStartedAt: anchor, CurrentStartedAt: anchor,
        TotalMonthsPurchased: 3, ExpiresAt: AnchorAddMonths(anchor, 3),
    })
    svc := NewMembershipService(db)
    v, err := svc.GetBalance(context.Background(), 20)
    require.NoError(t, err)
    // INV-20: cycle 行未懒建 → 默认月度配额
    assert.Equal(t, int64(2000), v.CycleRemaining)
    assert.NotNil(t, v.CycleEnd)
    assert.Equal(t, "pro", v.MembershipState)
}

func TestGetBalance_BoosterFrozen(t *testing.T) {
    db := newTestDB(t); seedUserPair(t, db, 10, 20)
    db.Create(&model.UserBoosterBalance{
        UserID: 20, CreditsRemaining: 600,
    })
    svc := NewMembershipService(db)
    v, _ := svc.GetBalance(context.Background(), 20)
    assert.Equal(t, int64(600), v.BoosterTotal)
    assert.Equal(t, int64(0), v.BoosterUsable, "INV-19: 冻结时 BoosterUsable=0")
    assert.Equal(t, "free", v.MembershipState)
}

func TestGetBalance_TrialUserNoBooster(t *testing.T) {
    db := newTestDB(t); seedUserPair(t, db, 10, 20)
    db.Create(&model.TrialGrant{
        UserID: 20, GrantedAt: time.Now(),
        ExpiresAt: time.Now().Add(48 * time.Hour),
        CreditsGranted: 200, CreditsRemaining: 150,
    })
    svc := NewMembershipService(db)
    v, _ := svc.GetBalance(context.Background(), 20)
    assert.Equal(t, int64(150), v.TrialRemaining)
    assert.Equal(t, "trial", v.MembershipState)
    assert.Equal(t, int64(0), v.BoosterUsable)
    assert.Equal(t, int64(0), v.CycleRemaining, "无 sub → cycle_remaining=0")
}

// 完整 BalanceDTO 字段（§5.3 schema）
func TestGetBalance_DTOFieldCompleteness(t *testing.T) {
    db := newTestDB(t); seedUserPair(t, db, 10, 20)
    now := time.Now().UTC()
    db.Create(&model.Subscription{
        UserID: 20, FirstStartedAt: now, CurrentStartedAt: now,
        TotalMonthsPurchased: 1, ExpiresAt: AnchorAddMonths(now, 1),
    })
    db.Create(&model.UserBoosterBalance{UserID: 20, CreditsRemaining: 600})
    svc := NewMembershipService(db)
    v, _ := svc.GetBalance(context.Background(), 20)
    assert.NotNil(t, v.CycleEnd)
    assert.NotNil(t, v.SubExpiresAt)
    assert.Equal(t, int64(600), v.BoosterUsable, "active sub → booster 解冻")
}
```

- [ ] **Step 2: 跑失败**

```bash
cd numind-server && go test ./internal/numind/biz/membership/ -run "TestGetMembershipState|TestGetBalance" -v
```

全 FAIL。

- [ ] **Step 3: 实现**

`state.go` 落地 spec §3.6 + §3.7 Go 伪代码：

```go
type MembershipState struct {
    DisplayState      string
    TrialActive       bool
    SubActive         bool
    TrialExpiresAt    *time.Time
    SubExpiresAt      *time.Time
    SubFirstStartedAt *time.Time
    BoosterFrozen     bool
}

type BalanceView struct {
    TrialRemaining  int64
    CycleRemaining  int64
    CycleEnd        *time.Time
    BoosterTotal    int64
    BoosterUsable   int64
    MembershipState string
    SubExpiresAt    *time.Time
    TrialExpiresAt  *time.Time
}

func (s *MembershipService) GetMembershipState(
    ctx context.Context, userID uint64, now time.Time,
) (*MembershipState, error) {
    state := &MembershipState{DisplayState: "free"}

    var trial model.TrialGrant
    if err := s.db.WithContext(ctx).Where("user_id = ?", userID).Take(&trial).Error; err == nil {
        if trial.ExpiresAt.After(now) && trial.CreditsRemaining > 0 {
            state.TrialActive = true
            t := trial.ExpiresAt
            state.TrialExpiresAt = &t
        }
    } else if !errors.Is(err, gorm.ErrRecordNotFound) {
        return nil, fmt.Errorf("query trial: %w", err)
    }

    var sub model.Subscription
    if err := s.db.WithContext(ctx).Where("user_id = ?", userID).Take(&sub).Error; err == nil {
        first := sub.FirstStartedAt
        state.SubFirstStartedAt = &first
        if sub.ExpiresAt.After(now) {
            state.SubActive = true
            e := sub.ExpiresAt
            state.SubExpiresAt = &e
        }
    } else if !errors.Is(err, gorm.ErrRecordNotFound) {
        return nil, fmt.Errorf("query sub: %w", err)
    }

    switch {
    case state.TrialActive:
        state.DisplayState = "trial"
    case state.SubActive:
        state.DisplayState = "pro"
    }
    state.BoosterFrozen = !state.TrialActive && !state.SubActive
    return state, nil
}

func (s *MembershipService) GetBalance(ctx context.Context, userID uint64) (*BalanceView, error) {
    now := time.Now().UTC()
    state, err := s.GetMembershipState(ctx, userID, now)
    if err != nil {
        return nil, err
    }
    view := &BalanceView{
        MembershipState: state.DisplayState,
        SubExpiresAt:    state.SubExpiresAt,
        TrialExpiresAt:  state.TrialExpiresAt,
    }

    var trial model.TrialGrant
    if err := s.db.WithContext(ctx).Where("user_id = ?", userID).Take(&trial).Error; err == nil {
        if trial.ExpiresAt.After(now) {
            view.TrialRemaining = trial.CreditsRemain
        }
    }

    if state.SubActive && state.SubExpiresAt != nil {
        var sub model.Subscription
        if err := s.db.WithContext(ctx).Where("user_id = ?", userID).Take(&sub).Error; err == nil {
            cycleIdx := computeCycleIndex(sub.CurrentStartedAt, now)
            cycleStart := AnchorAddMonths(sub.CurrentStartedAt, cycleIdx)
            cycleEnd := AnchorAddMonths(sub.CurrentStartedAt, cycleIdx+1)
            if state.SubExpiresAt.Before(cycleEnd) {
                cycleEnd = *state.SubExpiresAt
            }
            if !cycleEnd.Before(now) {
                view.CycleEnd = &cycleEnd
                var cycle model.CreditCycle
                err := s.db.WithContext(ctx).
                    Where("user_id = ? AND cycle_start = ?", userID, cycleStart).
                    Take(&cycle).Error
                switch {
                case err == nil:
                    view.CycleRemaining = cycle.CreditsRemain
                case errors.Is(err, gorm.ErrRecordNotFound):
                    view.CycleRemaining = monthlyCreditsQuota
                default:
                    return nil, fmt.Errorf("query cycle: %w", err)
                }
            }
        }
    }

    var booster model.UserBoosterBalance
    if err := s.db.WithContext(ctx).Where("user_id = ?", userID).Take(&booster).Error; err == nil {
        view.BoosterTotal = booster.CreditsRemain
        if !state.BoosterFrozen {
            view.BoosterUsable = booster.CreditsRemain
        }
    } else if !errors.Is(err, gorm.ErrRecordNotFound) {
        return nil, fmt.Errorf("query booster: %w", err)
    }

    return view, nil
}
```

- [ ] **Step 4: 跑通过**

```bash
cd numind-server && go test ./internal/numind/biz/membership/ -run "TestGetMembershipState|TestGetBalance" -v && task lint
```

7 case 全 PASS。

- [ ] **Step 5: commit**

```
feat(membership): GetMembershipState + GetBalance read views

- §3.6 GetMembershipState：纯只读，DisplayState ∈ {free/trial/pro}
- INV-17: BoosterFrozen ⇔ !TrialActive && !SubActive
- US-2: trial+pro 叠加时 DisplayState='trial'（保留试用感知）
- §3.7 GetBalance：嵌套 BalanceView，含 cycle_end / booster_usable 派生
- INV-19: BoosterUsable ≤ BoosterTotal；冻结时 0
- INV-20: cycle 未懒建时返回月度配额（用户体感"满格"）
- 7 个测试 case 全部通过
```

---

## Phase 2 完成验收

完成 Task 5-8 后：

- 5 张表的 biz 函数（GrantTrial / GrantOrRenewSubscription / ensureCurrentCycle / DeductCredits / GetMembershipState / GetBalance）全部落地，对照 §4 小结清单逐项核对。
- 单元测试覆盖率：membership package > 85%（go test -coverprofile=cover.out）。
- 锁顺序硬规则验证：所有 SELECT FOR UPDATE 走 `lockMembershipRows` helper（Task 4 已落地）；任意裸写 = P0。
- 进入 Phase 3（controller + 路由 + middleware）。

---

# Phase 3: API Endpoints — Tasks 9-13

> Membership & Credits Redesign 实施 plan, Phase 3。
> Spec: `numind-server/docs/superpowers/specs/2026-04-29-membership-credits-redesign-design.md` §5 + §7
> Rules: `.claude/rules/api-design.md`

每个 task 严格遵循 TDD 五段式：① Red（写失败测试） → ② Green（最小实现） → ③ Refactor → ④ 手动验证（curl） → ⑤ Commit。

---

### Task 9: Idempotency-Key Middleware

### 背景

Spec §5 / §4.5 要求所有写操作端点（grant、order、payment notify）支持 HTTP header `Idempotency-Key`（客户端 UUID v4，长度 ≤ 64）。中间件负责：
- 读 header → validate 长度 → 注入 `gin.Context`
- biz 层用 `c.GetString("idempotency_key")` 读
- 写操作（POST/PUT/PATCH）缺失 key → 400
- GET / 非写路径 → 放行

### Files

- **Create** `numind-server/internal/pkg/middleware/idempotency.go`
- **Create** `numind-server/internal/pkg/middleware/idempotency_test.go`
- **Modify** `numind-server/internal/numind/router.go`：注册到需要幂等的 group（`/v1/orders`、`/v1/users/children/:child_id/grant-membership`）

### TDD 五段式

- [ ] **Step 1: Red — 写失败测试**

`idempotency_test.go` 表驱动：

```go
package middleware

import (
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/gin-gonic/gin"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestIdempotencyKeyMiddleware(t *testing.T) {
    gin.SetMode(gin.TestMode)
    cases := []struct {
        name        string
        method      string
        key         string
        wantStatus  int
        wantInjected string
    }{
        {"valid_key_post_injects_context", http.MethodPost, "7e8b3c2a-9f4d-4e21-b0c5-1a2b3c4d5e6f", http.StatusOK, "7e8b3c2a-9f4d-4e21-b0c5-1a2b3c4d5e6f"},
        {"valid_key_put_injects_context", http.MethodPut, "abc-def-123", http.StatusOK, "abc-def-123"},
        {"missing_key_post_returns_400", http.MethodPost, "", http.StatusBadRequest, ""},
        {"missing_key_patch_returns_400", http.MethodPatch, "", http.StatusBadRequest, ""},
        {"missing_key_get_passes_through", http.MethodGet, "", http.StatusOK, ""},
        {"key_too_long_returns_400", http.MethodPost, strings.Repeat("a", 65), http.StatusBadRequest, ""},
        {"key_at_64_chars_passes", http.MethodPost, strings.Repeat("a", 64), http.StatusOK, strings.Repeat("a", 64)},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            r := gin.New()
            r.Use(RequireIdempotencyKey())
            handler := func(c *gin.Context) {
                got := c.GetString("idempotency_key")
                assert.Equal(t, tc.wantInjected, got)
                c.Status(http.StatusOK)
            }
            r.POST("/test", handler)
            r.PUT("/test", handler)
            r.PATCH("/test", handler)
            r.GET("/test", handler)

            req := httptest.NewRequest(tc.method, "/test", nil)
            if tc.key != "" {
                req.Header.Set("Idempotency-Key", tc.key)
            }
            w := httptest.NewRecorder()
            r.ServeHTTP(w, req)
            require.Equal(t, tc.wantStatus, w.Code, "body=%s", w.Body.String())
        })
    }
}
```

`go test ./internal/pkg/middleware/...` → FAIL（中间件未实现）。

- [ ] **Step 2: Green — 最小实现**

`idempotency.go`：

```go
// Package middleware provides HTTP-level cross-cutting concerns.
package middleware

import (
    "net/http"

    "github.com/gin-gonic/gin"
    "github.com/skyzhouzhi/numind-server/internal/pkg/core"
    "github.com/skyzhouzhi/numind-server/internal/pkg/errno"
)

const (
    headerIdempotencyKey = "Idempotency-Key"
    maxIdempotencyKeyLen = 64
)

// RequireIdempotencyKey enforces presence of the Idempotency-Key header
// on write methods (POST/PUT/PATCH) and validates its length.
// On valid key it injects the value into gin.Context under "idempotency_key"
// for biz layer consumption via c.GetString("idempotency_key").
//
// Idempotent reads (GET/HEAD/OPTIONS/DELETE) skip enforcement; if a key is
// present on those methods it is still injected for downstream introspection.
func RequireIdempotencyKey() gin.HandlerFunc {
    return func(c *gin.Context) {
        key := c.GetHeader(headerIdempotencyKey)
        isWrite := c.Request.Method == http.MethodPost ||
            c.Request.Method == http.MethodPut ||
            c.Request.Method == http.MethodPatch

        if key == "" {
            if isWrite {
                core.WriteResponse(c, errno.ErrBind.SetMessage("Idempotency-Key 必填"), nil)
                c.Abort()
                return
            }
            c.Next()
            return
        }
        if len(key) > maxIdempotencyKeyLen {
            core.WriteResponse(c, errno.ErrBind.SetMessage("Idempotency-Key 长度超限（最多 64）"), nil)
            c.Abort()
            return
        }
        c.Set("idempotency_key", key)
        c.Next()
    }
}
```

`go test ./internal/pkg/middleware/...` → PASS。

- [ ] **Step 3: Refactor**

- 提取常量 `maxIdempotencyKeyLen = 64` 与 spec §5 表头约束对齐
- 单元测试覆盖率 100%（all branches）
- 不引入额外依赖
- 在 router.go 把中间件挂到具体路由（不全局挂载，避免影响 GET 收益不大且增加表层耦合）：

```go
// router.go 关键改动
authGroup.POST("/orders", middleware.RequireIdempotencyKey(), orderCtrl.CreateOrder)
authGroup.POST("/users/children/:child_id/grant-membership",
    middleware.RequireIdempotencyKey(), parentGrantCtrl.GrantMembership)
```

- [ ] **Step 4: 手动验证**

```bash
# 缺失 key
curl -i -X POST http://localhost:8080/v1/orders \
  -H "Authorization: Bearer $TOK" -H "Content-Type: application/json" \
  -d '{"user_id":1,"product_type":"booster","quantity":1,"pay_channel":"wechat"}'
# 期望：400 {"code":xxx,"message":"Idempotency-Key 必填"}

# 超长
curl -i -X POST http://localhost:8080/v1/orders \
  -H "Authorization: Bearer $TOK" -H "Idempotency-Key: $(python3 -c 'print("a"*65)')" \
  -H "Content-Type: application/json" \
  -d '{"user_id":1,"product_type":"booster","quantity":1,"pay_channel":"wechat"}'
# 期望：400 长度超限

# 合法
curl -i -X POST http://localhost:8080/v1/orders \
  -H "Authorization: Bearer $TOK" -H "Idempotency-Key: 7e8b3c2a-9f4d-4e21-b0c5-1a2b3c4d5e6f" \
  -H "Content-Type: application/json" \
  -d '{"user_id":1,"product_type":"booster","quantity":1,"pay_channel":"wechat"}'
# 期望：进入 biz（200 或合法业务错误）
```

- [ ] **Step 5: Commit**

```
feat(middleware): add Idempotency-Key middleware

- Inject header into gin.Context as "idempotency_key" for biz layer
- Reject write methods (POST/PUT/PATCH) without header → 400
- Cap length at 64 chars per spec §4.5
- Register on /v1/orders + grant-membership routes

Spec: 2026-04-29-membership-credits-redesign-design §4.5 / §5

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
```

---

### Task 10: POST /v1/users/children/:child_id/grant-membership

### 背景

Spec §5.1。父账户为子账户开通试用包或开通/续费 Pro 月卡。**不走支付**，写入 `membership_event` 表（source='b2b_grant', granter_user_id=父账户 ID），月底走 b2b-billing-report 对账。

API **不区分 grant 与 renew**——biz 依据子账户当前 subscription 状态自动判定（无 / 已过期 → 新开通；在期 → 续费延期）。trial 路径独立逻辑（trial_grant 表 lifetime UNIQUE）。

### Files

- **Create** `numind-server/internal/numind/controller/v1/credit/grant_membership.go`
- **Create** `numind-server/internal/numind/controller/v1/credit/grant_membership_test.go`
- **Modify** `numind-server/internal/numind/router.go`（在 `childGroup` 注册路由）

### TDD 五段式

- [ ] **Step 1: Red — 写失败测试**

`grant_membership_test.go` 6 case：

```go
func TestGrantMembership(t *testing.T) {
    cases := []struct {
        name       string
        body       string
        childID    string
        mockSetup  func(*MockCreditBiz)
        wantCode   int
        wantBizErr error
    }{
        {
            name:    "trial_granted_happy",
            childID: "1234",
            body:    `{"product_type":"trial","reason":"客户申请"}`,
            mockSetup: func(m *MockCreditBiz) {
                m.On("GrantTrial", mock.Anything, uint(100), uint(1234), "客户申请", "idem-1").
                    Return(&credit.GrantResult{EventID: 98766, EventType: "trial_granted",
                        TrialExpiresAt: &trialExpire}, nil)
            },
            wantCode: 0,
        },
        {
            name:    "monthly_granted_happy",
            childID: "1234",
            body:    `{"product_type":"monthly","months":3}`,
            mockSetup: func(m *MockCreditBiz) {
                m.On("GrantOrRenewSubscription", mock.Anything, uint(100), uint(1234), 3, "", "idem-2").
                    Return(&credit.GrantResult{EventID: 98765, EventType: "sub_granted",
                        SubscriptionExpiresAt: &subExpire}, nil)
            },
            wantCode: 0,
        },
        {
            name:    "monthly_renewed_happy",
            childID: "1234",
            body:    `{"product_type":"monthly","months":2}`,
            mockSetup: func(m *MockCreditBiz) {
                m.On("GrantOrRenewSubscription", mock.Anything, uint(100), uint(1234), 2, "", "idem-3").
                    Return(&credit.GrantResult{EventID: 99001, EventType: "sub_renewed",
                        SubscriptionExpiresAt: &renewExpire}, nil)
            },
            wantCode: 0,
        },
        {
            name:     "parent_child_relation_failure",
            childID:  "9999",
            body:     `{"product_type":"monthly","months":1}`,
            mockSetup: func(m *MockCreditBiz) {
                m.On("GrantOrRenewSubscription", mock.Anything, uint(100), uint(9999), 1, "", "idem-4").
                    Return(nil, credit.ErrParentChildRelation)
            },
            wantCode: 100403, // ErrForbidden
        },
        {
            name:    "trial_with_months_rejected_400",
            childID: "1234",
            body:    `{"product_type":"trial","months":2}`,
            mockSetup: func(m *MockCreditBiz) {},
            wantCode: 100400,
        },
        {
            name:    "monthly_invalid_months_rejected",
            childID: "1234",
            body:    `{"product_type":"monthly","months":13}`,
            mockSetup: func(m *MockCreditBiz) {},
            wantCode: 100400,
        },
        {
            name:    "trial_already_granted_409",
            childID: "1234",
            body:    `{"product_type":"trial"}`,
            mockSetup: func(m *MockCreditBiz) {
                m.On("GrantTrial", mock.Anything, uint(100), uint(1234), "", mock.Anything).
                    Return(nil, credit.ErrTrialAlreadyGranted)
            },
            wantCode: errno.ErrTrialAlreadyGranted.Code,
        },
    }
    // ... gin engine setup, c.Set("userID", uint(100)), inject idempotency_key
}
```

- [ ] **Step 2: Green — 最小实现**

`grant_membership.go`：

```go
package credit

import (
    "errors"
    "strconv"

    "github.com/gin-gonic/gin"
    "github.com/skyzhouzhi/numind-server/internal/numind/biz/credit"
    "github.com/skyzhouzhi/numind-server/internal/pkg/core"
    "github.com/skyzhouzhi/numind-server/internal/pkg/errno"
)

// GrantMembershipRequest matches spec §5.1 body schema.
type GrantMembershipRequest struct {
    ProductType string `json:"product_type" binding:"required,oneof=trial monthly"`
    Months      int    `json:"months"       binding:"omitempty,min=0,max=12"`
    Reason      string `json:"reason"       binding:"max=500"`
}

// GrantMembership handles POST /v1/users/children/:child_id/grant-membership.
// Controller: bind + extract auth + dispatch to biz; biz owns parent-child
// validation, trial uniqueness, membership_event write.
func (ctrl *Controller) GrantMembership(c *gin.Context) {
    childIDStr := c.Param("child_id")
    childID, err := strconv.ParseUint(childIDStr, 10, 64)
    if err != nil || childID == 0 {
        core.WriteResponse(c, errno.ErrBind.SetMessage("child_id 必须是正整数"), nil)
        return
    }

    var req GrantMembershipRequest
    if err := c.ShouldBindJSON(&req); err != nil {
        core.WriteResponse(c, errno.ErrBind.SetMessage(err.Error()), nil)
        return
    }
    // product_type-specific months semantics
    if req.ProductType == "trial" && req.Months > 0 {
        core.WriteResponse(c, errno.ErrInvalidMonths.SetMessage("trial 不接受 months 参数"), nil)
        return
    }
    if req.ProductType == "monthly" && (req.Months < 1 || req.Months > 12) {
        core.WriteResponse(c, errno.ErrInvalidMonths.SetMessage("月数必须在 1-12 之间"), nil)
        return
    }

    parentID := c.GetUint("userID")
    idemKey := c.GetString("idempotency_key")

    var (
        result *credit.GrantResult
        bizErr error
    )
    switch req.ProductType {
    case "trial":
        result, bizErr = ctrl.creditBiz.GrantTrial(c, parentID, uint(childID), req.Reason, idemKey)
    case "monthly":
        result, bizErr = ctrl.creditBiz.GrantOrRenewSubscription(c, parentID, uint(childID), req.Months, req.Reason, idemKey)
    }
    if bizErr != nil {
        core.WriteResponse(c, mapGrantError(bizErr), nil)
        return
    }
    core.WriteResponse(c, nil, gin.H{
        "child_user_id":     childID,
        "product_type":      req.ProductType,
        "months":            req.Months,
        "event_id":          result.EventID,
        "event_type":        result.EventType,
        "membership_state":  result.MembershipState,
    })
}

// mapGrantError translates biz sentinel errors to errno.Errno per §5.7.
func mapGrantError(err error) error {
    switch {
    case errors.Is(err, credit.ErrParentChildRelation):
        return errno.ErrForbidden.SetMessage("该子账户不属于当前账户")
    case errors.Is(err, credit.ErrChildNotFound):
        return errno.ErrUserNotFound
    case errors.Is(err, credit.ErrTrialAlreadyGranted):
        return errno.ErrTrialAlreadyGranted
    case errors.Is(err, credit.ErrTrialNotAllowedForActivePro):
        return errno.ErrTrialNotAllowedForActivePro
    case errors.Is(err, credit.ErrIdempotencyKeyConflict):
        return errno.ErrIdempotencyKeyConflict
    default:
        return errno.ErrInternal
    }
}
```

router.go：

```go
childGroup.POST("/users/children/:child_id/grant-membership",
    middleware.RequireIdempotencyKey(),
    creditCtrl.GrantMembership)
```

- [ ] **Step 3: Refactor**

- mapGrantError 集中映射，避免 controller 写 switch 散落
- 全部错误都使用 spec §5.7 定义的 errno 常量（Task 1 已新增）
- biz 接口调用按规则 6 仅传 `*gin.Context` 作 `context.Context`
- 测试覆盖率 ≥ 90%（所有 case + mapGrantError 全分支）

- [ ] **Step 4: 手动验证**

```bash
# trial happy
curl -i -X POST http://localhost:8080/v1/users/children/1234/grant-membership \
  -H "Authorization: Bearer $PARENT_TOK" \
  -H "Idempotency-Key: 7e8b3c2a-9f4d-4e21-b0c5-1a2b3c4d5e6f" \
  -H "Content-Type: application/json" \
  -d '{"product_type":"trial","reason":"测试"}'
# 期望：200, event_type=trial_granted, trial_expires_at 非空

# 续费 Pro
curl -i -X POST http://localhost:8080/v1/users/children/1234/grant-membership \
  -H "Authorization: Bearer $PARENT_TOK" \
  -H "Idempotency-Key: 7e8b3c2a-9f4d-4e21-b0c5-1a2b3c4d5e7a" \
  -H "Content-Type: application/json" \
  -d '{"product_type":"monthly","months":3}'

# 重复 idempotency-key → 200 同 event_id（首次结果）
curl -X POST ... -d '{"product_type":"monthly","months":3}'
# 同 key 返回首次结果，不重复延期

# 不属于当前父账户的子账户
curl -X POST http://localhost:8080/v1/users/children/9999/grant-membership ...
# 期望：403 该子账户不属于当前账户
```

- [ ] **Step 5: Commit**

```
feat(credit): add POST grant-membership endpoint

- Controller dispatches by product_type (trial / monthly) to biz
- Reject trial with months > 0 (400)
- Reject monthly with months ∉ [1,12] (400)
- mapGrantError covers parent-child / trial uniqueness / idempotency conflict
- Register route under childGroup with idempotency middleware

Spec: §5.1 / §5.7 / §5.9

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
```

---

### Task 11: POST /v1/orders 改写（booster quantity + payer 语义）

### 背景

Spec §5.2 / §5.10。order 接口本次只接受 `product_type=booster`（trial/monthly 全部走 grant 路径）。语义关键点：

- `payer = token 主体`，body 字段 `user_id` = 受益人；自购 vs 代付由 `body.user_id == current_user.ID` 判定
- booster `quantity` ∈ [1, 10000]；> 10000 → `ErrBoosterQuantityExceedsLimit`
- 受益人必须有 active 会员（`HasActiveSubscription || HasActiveTrial`），否则 `ErrNotActiveMember`
- fulfillOrder 重写：booster 路径写 `booster_grant` + `membership_event`，删除老的 user.UserTier / TierExpires 分支（spec §5.10 cleanup）

### Files

- **Modify** `numind-server/internal/numind/biz/payment/payment.go`（fulfillOrder 重写）
- **Modify** `numind-server/internal/numind/controller/v1/order/order.go`（接受 quantity 字段）
- **Modify** `numind-server/internal/numind/biz/payment/payment_test.go`

### TDD 五段式

- [ ] **Step 1: Red — 写失败测试**

`payment_test.go` 5 case：

```go
func TestCreateOrder_Booster(t *testing.T) {
    cases := []struct {
        name       string
        payerID    uint
        body       OrderRequest
        memberMock func(*MockCredit)
        wantErr    error
        wantCents  int
    }{
        {
            name:    "self_purchase_active_member",
            payerID: 100,
            body:    OrderRequest{UserID: 100, ProductType: "booster", Quantity: 3, PayChannel: "wechat"},
            memberMock: func(m *MockCredit) {
                m.On("HasActiveMembership", mock.Anything, uint(100)).Return(true, nil)
            },
            wantCents: 8970, // 3 * 2990
        },
        {
            name:    "parent_proxy_purchase_for_child",
            payerID: 100,
            body:    OrderRequest{UserID: 1234, ProductType: "booster", Quantity: 1, PayChannel: "alipay"},
            memberMock: func(m *MockCredit) {
                m.On("IsChild", mock.Anything, uint(100), uint(1234)).Return(true, nil)
                m.On("HasActiveMembership", mock.Anything, uint(1234)).Return(true, nil)
            },
            wantCents: 2990,
        },
        {
            name:    "quantity_exceeds_10000_rejected",
            payerID: 100,
            body:    OrderRequest{UserID: 100, ProductType: "booster", Quantity: 10001, PayChannel: "wechat"},
            memberMock: func(m *MockCredit) {},
            wantErr:    payment.ErrBoosterQuantityExceedsLimit,
        },
        {
            name:    "non_member_self_purchase_rejected",
            payerID: 100,
            body:    OrderRequest{UserID: 100, ProductType: "booster", Quantity: 1, PayChannel: "wechat"},
            memberMock: func(m *MockCredit) {
                m.On("HasActiveMembership", mock.Anything, uint(100)).Return(false, nil)
            },
            wantErr: payment.ErrNotActiveMember,
        },
        {
            name:    "trial_product_type_rejected",
            payerID: 100,
            body:    OrderRequest{UserID: 100, ProductType: "trial", PayChannel: "wechat"},
            memberMock: func(m *MockCredit) {},
            wantErr:    payment.ErrInvalidProductType,
        },
    }
}

func TestFulfillOrder_BoosterPath(t *testing.T) {
    // booster fulfillment writes user_booster_balance + membership_event,
    // does NOT touch user.UserTier / TierExpires.
    db := newTestDB(t)
    biz := payment.New(db, ...)
    order := seedOrder(db, model.Order{UserID: 1234, ProductType: "booster", Quantity: 5, AmountCents: 14950, Status: "paid"})
    require.NoError(t, biz.FulfillOrder(ctx, order.OutTradeNo))

    var bal model.UserBoosterBalance
    require.NoError(t, db.First(&bal, "user_id = ?", 1234).Error)
    assert.Equal(t, 5, bal.Quantity)

    var ev model.MembershipEvent
    require.NoError(t, db.First(&ev, "child_user_id = ? AND event_type = ?", 1234, "booster_granted").Error)
    assert.Equal(t, 5, ev.Quantity)
    assert.Equal(t, 14950, ev.AmountCents)
}

func TestFulfillOrder_NonBoosterProductType_Rejected(t *testing.T) {
    // spec §5.2 lock: order accepts booster only. If a historical/dirty row carries
    // monthly/trial in product_type, fulfillOrder must return ErrInvalidProductType
    // rather than silently dispatching to grant biz (Pro/trial flow goes through
    // /v1/users/children/:child_id/grant-membership, not /v1/orders).
    db := newTestDB(t)
    biz := payment.New(db, ...)
    order := seedOrder(db, model.Order{UserID: 1234, ProductType: "monthly", Months: 3, AmountCents: 9900, Status: "paid"})
    err := biz.FulfillOrder(ctx, order.OutTradeNo)
    assert.ErrorIs(t, err, payment.ErrInvalidProductType)
}
```

- [ ] **Step 2: Green — 最小实现**

`order.go` controller：

```go
type OrderRequest struct {
    UserID      uint   `json:"user_id"      binding:"required,gt=0"`
    ProductType string `json:"product_type" binding:"required,oneof=booster"`
    Quantity    int    `json:"quantity"     binding:"required_if=ProductType booster,omitempty,min=1,max=10000"`
    PayChannel  string `json:"pay_channel"  binding:"required,oneof=wechat alipay"`
}

func (ctrl *Controller) CreateOrder(c *gin.Context) {
    var req OrderRequest
    if err := c.ShouldBindJSON(&req); err != nil {
        core.WriteResponse(c, errno.ErrBind.SetMessage(err.Error()), nil)
        return
    }
    payerID := c.GetUint("userID")
    idemKey := c.GetString("idempotency_key")

    order, err := ctrl.paymentBiz.CreateBoosterOrder(c, payment.CreateBoosterParams{
        PayerID:        payerID,
        BeneficiaryID:  req.UserID,
        Quantity:       req.Quantity,
        PayChannel:     req.PayChannel,
        IdempotencyKey: idemKey,
    })
    if err != nil {
        core.WriteResponse(c, mapOrderError(err), nil)
        return
    }
    core.WriteResponse(c, nil, order)
}
```

`payment.go` 关键改动：

```go
const BoosterUnitPriceCents = 2990
const BoosterMaxQuantity = 10000

var (
    ErrBoosterQuantityExceedsLimit = errors.New("payment: booster quantity exceeds 10000")
    ErrNotActiveMember             = errors.New("payment: beneficiary not active member")
    ErrInvalidProductType          = errors.New("payment: invalid product_type, only booster allowed via /v1/orders")
    ErrParentChildRelation         = errors.New("payment: beneficiary is not payer's child")
)

func (b *biz) CreateBoosterOrder(ctx context.Context, p CreateBoosterParams) (*OrderDTO, error) {
    if p.Quantity < 1 || p.Quantity > BoosterMaxQuantity {
        return nil, ErrBoosterQuantityExceedsLimit
    }
    // self vs proxy
    if p.PayerID != p.BeneficiaryID {
        ok, err := b.creditBiz.IsChild(ctx, p.PayerID, p.BeneficiaryID)
        if err != nil { return nil, err }
        if !ok { return nil, ErrParentChildRelation }
    }
    active, err := b.creditBiz.HasActiveMembership(ctx, p.BeneficiaryID)
    if err != nil { return nil, err }
    if !active { return nil, ErrNotActiveMember }

    amountCents := p.Quantity * BoosterUnitPriceCents
    // ... persist order with idempotency_key, return pay_params via wechat/alipay sdk
}

// fulfillOrder: spec §5.2 锁定 order 仅 booster；trial/Pro 走 grant-membership 不走 order。
// 删除 user.UserTier/TierExpires 分支（spec §5.10 cleanup）。
func (b *biz) FulfillOrder(ctx context.Context, outTradeNo string) error {
    order, err := b.store.Orders().GetByOutTradeNo(ctx, outTradeNo)
    if err != nil { return err }
    switch order.ProductType {
    case "booster":
        return b.fulfillBooster(ctx, order)   // increment user_booster_balance + membership_event
    default:
        // spec §5.2: order 仅接受 booster；CreateBoosterOrder 已在入口拦截 trial/monthly。
        // 防御性兜底：若历史脏数据落到此处，返回 ErrInvalidProductType 不静默放行。
        return ErrInvalidProductType
    }
}

// 删除内容（spec §5.10）：原 fulfillOrder 中所有 user.UserTier = ... / user.TierExpires = ... 写入分支
```

- [ ] **Step 3: Refactor**

- 删除 `fulfillOrder` 内 `db.Model(&user).Update("user_tier", ...)` 等老分支（spec §10.3 cleanup）
- BoosterUnitPriceCents 提取为常量，与 §7.1 老口径 `LegacyAmountBoosterUnit` 数值一致但语义独立
- `CreateBoosterParams` struct 替代多参数函数签名
- 测试 case 覆盖：自购 happy + 父代购 happy + quantity 超限 + 非会员 + 错误 product_type + Pro 续费 fulfill

- [ ] **Step 4: 手动验证**

```bash
# 自购 booster 3 份
curl -i -X POST http://localhost:8080/v1/orders \
  -H "Authorization: Bearer $TOK" \
  -H "Idempotency-Key: $(uuidgen)" -H "Content-Type: application/json" \
  -d '{"user_id":100,"product_type":"booster","quantity":3,"pay_channel":"wechat"}'
# 期望：200, amount_cents=8970, status=pending, pay_params 非空

# quantity > 10000 拒绝
curl -i -X POST http://localhost:8080/v1/orders \
  -H "Authorization: Bearer $TOK" -H "Idempotency-Key: $(uuidgen)" \
  -H "Content-Type: application/json" \
  -d '{"user_id":100,"product_type":"booster","quantity":10001,"pay_channel":"wechat"}'
# 期望：400 单次最多购买 10000 份

# 非会员买 booster 拒绝
# （先确保 user 100 无 active 会员）
curl -i -X POST ... -d '{"user_id":100,"product_type":"booster","quantity":1,"pay_channel":"wechat"}'
# 期望：403 需要在期会员才能购买加量包

# trial product_type 拒绝
curl -i -X POST ... -d '{"user_id":100,"product_type":"trial","pay_channel":"wechat"}'
# 期望：400 不支持的产品类型
```

- [ ] **Step 5: Commit**

```
feat(payment): rewrite POST /v1/orders for booster quantity + new payer semantics

- Add quantity field (1..10000) for booster product_type
- Reject quantity > 10000 (ErrBoosterQuantityExceedsLimit)
- Self vs proxy purchase by token vs body.user_id relation (no payer_id field)
- Beneficiary must be active member; otherwise ErrNotActiveMember
- fulfillOrder accepts booster only (spec §5.2 lock): booster → user_booster_balance
  + membership_event; trial/monthly fall through to ErrInvalidProductType (those flows
  go through /v1/users/children/:child_id/grant-membership, never /v1/orders)
- Remove legacy user.UserTier / TierExpires write branches per spec §5.10

Spec: §5.2 / §5.7 / §5.10

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
```

---

### Task 12: GET 余额接口三变体（用户 / 父账户 / admin）+ Order 状态 + 子账户列表

### 背景

Spec §5.3 / §5.4 / §5.5 / §5.8 / §8.3.1。本 task 合并 5 个 GET 端点（统一在 controller/v1/credit + controller/v1/order 落地一次，避免 Phase 4 前端 task 19/20 出现 dangling dependency）：

| 端点 | 调用方 | booster 字段 | 父子校验 | 用途 |
|---|---|---|---|---|
| GET /v1/credits/balance | user 自己 | 含 | 不需要 | §5.3 |
| GET /v1/users/children/:child_id/balance | 父账户 | **不含**（隐私） | 必需 | §5.4 |
| GET /v1/admin/users/:user_id/balance | admin | 含 | 不需要 | §5.5 |
| GET /v1/orders/:id/status | order 受益人 / 父账户 | — | 受益人或父账户 token | §5.8（轮询 booster 支付状态，Task 19 BoosterPurchaseDialog 依赖） |
| GET /v1/users/children | 父账户 | **不含** | 隐式（仅返回当前 token 所有 child） | §8.3.1（CustomersView 列表，Task 20 依赖；ChildSummaryDTO 含 nested membership_state / has_used_trial / cycle_remaining） |

### Files

- **Create** `numind-server/internal/numind/controller/v1/credit/balance.go`（用户端 + 父账户视图）
- **Create** `numind-server/internal/numind/controller/v1/admin_credit/balance.go`（admin 视图）
- **Create** `numind-server/internal/numind/controller/v1/credit/balance_test.go`
- **Create** `numind-server/internal/numind/controller/v1/order/order_status.go`（GET /v1/orders/:id/status：返回 `{ order_id, status: 'pending'|'paid'|'failed'|'cancelled', paid_at, amount_cents, product_type, quantity }`；权限校验 = `c.GetUint("userID") == order.UserID || biz.IsChild(parent=token, child=order.UserID)`，否则 403；biz 调用 `payment.GetOrderStatus(orderID)`，复用 Task 11 已交付的 store）
- **Create** `numind-server/internal/numind/controller/v1/order/order_status_test.go`
- **Create** `numind-server/internal/numind/controller/v1/credit/children_list.go`（GET /v1/users/children：返回 `[]ChildSummaryDTO`；biz `creditBiz.ListChildren(parentID)`，每条 child 调 `GetMembershipState` + `HasUsedTrial`，map 到 ChildSummaryDTO，**不含** booster_*，与父账户视图隐私边界一致）
- **Create** `numind-server/internal/numind/controller/v1/credit/children_list_test.go`
- **Modify** `numind-server/internal/numind/router.go`（新增父账户视图 + order 状态 + children 列表 3 条路由）
- **Modify** `numind-server/internal/numind/admin_router.go`（新增 admin 视图）

> 合并理由：order 状态端点和 children 列表端点都属于轻量 GET / 复用已有 biz / 无独立 schema 变更，独立 task 反而增加 PR 数。Phase 4 task 19（BoosterPurchaseDialog 轮询）和 task 20（CustomersView 列表）的"S3 plan 必须保证"依赖声明已在本 task 落地。

### TDD 五段式

- [ ] **Step 1: Red — 写失败测试**

`balance_test.go` 4 case：

```go
func TestGetMyBalance_IncludesBooster(t *testing.T) {
    creditMock := new(MockCreditBiz)
    creditMock.On("GetBalance", mock.Anything, uint(100)).Return(&credit.BalanceDTO{
        UserID: 100, TrialRemaining: 150, CycleRemaining: 1850,
        BoosterTotal: 1800, BoosterUsable: 1800, BoosterFrozen: false,
        NextRefillAt: &nextRefill,
        MembershipState: credit.MembershipStateDTO{HasActiveSubscription: true, ...},
    }, nil)
    ctrl := credit.NewController(creditMock)
    c, w := newGinCtx("GET", "/v1/credits/balance", nil)
    c.Set("userID", uint(100))
    ctrl.GetMyBalance(c)
    var resp gin.H
    require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
    data := resp["data"].(map[string]any)
    assert.Contains(t, data, "booster_total")
    assert.Contains(t, data, "booster_usable")
    assert.Contains(t, data, "booster_frozen")
    assert.Contains(t, data, "next_refill_at")
}

func TestGetChildBalance_OmitsBooster(t *testing.T) {
    creditMock := new(MockCreditBiz)
    creditMock.On("IsChild", mock.Anything, uint(100), uint(1234)).Return(true, nil)
    creditMock.On("GetBalance", mock.Anything, uint(1234)).Return(&credit.BalanceDTO{...}, nil)
    ctrl := credit.NewController(creditMock)
    c, w := newGinCtx("GET", "/v1/users/children/1234/balance", nil)
    c.Set("userID", uint(100))
    c.Params = []gin.Param{{Key: "child_id", Value: "1234"}}
    ctrl.GetChildBalance(c)

    var resp gin.H
    json.Unmarshal(w.Body.Bytes(), &resp)
    data := resp["data"].(map[string]any)
    assert.NotContains(t, data, "booster_total")
    assert.NotContains(t, data, "booster_usable")
    assert.NotContains(t, data, "booster_frozen")
    assert.NotContains(t, data, "next_refill_at")
    assert.Contains(t, data, "trial_remaining")
    assert.Contains(t, data, "cycle_remaining")
}

func TestGetChildBalance_ParentChildMismatch_403(t *testing.T) {
    creditMock := new(MockCreditBiz)
    creditMock.On("IsChild", mock.Anything, uint(100), uint(9999)).Return(false, nil)
    // ... assert 403 + 该子账户不属于当前账户
}

func TestAdminGetUserBalance_IncludesBooster(t *testing.T) {
    creditMock := new(MockCreditBiz)
    creditMock.On("GetBalance", mock.Anything, uint(1234)).Return(&credit.BalanceDTO{
        BoosterTotal: 600, BoosterFrozen: true, ...,
    }, nil)
    // 验证响应包含 booster_total / booster_frozen 全字段
}
```

- [ ] **Step 2: Green — 最小实现**

`balance.go`（用户端）：

```go
package credit

import (
    "strconv"

    "github.com/gin-gonic/gin"
    "github.com/skyzhouzhi/numind-server/internal/pkg/core"
    "github.com/skyzhouzhi/numind-server/internal/pkg/errno"
)

// GetMyBalance handles GET /v1/credits/balance.
// Returns full balance (incl. booster) for the authenticated user.
func (ctrl *Controller) GetMyBalance(c *gin.Context) {
    userID := c.GetUint("userID")
    bal, err := ctrl.creditBiz.GetBalance(c, userID)
    if err != nil {
        core.WriteResponse(c, errno.ErrInternal, nil)
        return
    }
    core.WriteResponse(c, nil, fullBalanceView(bal))
}

// GetChildBalance handles GET /v1/users/children/:child_id/balance.
// Validates parent-child relation, returns balance WITHOUT booster fields
// (privacy boundary: parent funds membership, child owns booster purchases).
func (ctrl *Controller) GetChildBalance(c *gin.Context) {
    childID, err := strconv.ParseUint(c.Param("child_id"), 10, 64)
    if err != nil || childID == 0 {
        core.WriteResponse(c, errno.ErrBind.SetMessage("child_id 必须是正整数"), nil)
        return
    }
    parentID := c.GetUint("userID")
    isChild, err := ctrl.creditBiz.IsChild(c, parentID, uint(childID))
    if err != nil {
        core.WriteResponse(c, errno.ErrInternal, nil)
        return
    }
    if !isChild {
        core.WriteResponse(c, errno.ErrForbidden.SetMessage("该子账户不属于当前账户"), nil)
        return
    }
    bal, err := ctrl.creditBiz.GetBalance(c, uint(childID))
    if err != nil {
        core.WriteResponse(c, errno.ErrInternal, nil)
        return
    }
    core.WriteResponse(c, nil, parentScopedBalanceView(bal))
}

// fullBalanceView serializes BalanceDTO with all fields per §5.3.
func fullBalanceView(b *credit.BalanceDTO) gin.H {
    return gin.H{
        "user_id":           b.UserID,
        "membership_state":  b.MembershipState,
        "trial_remaining":   b.TrialRemaining,
        "cycle_remaining":   b.CycleRemaining,
        "cycle_start":       b.CycleStart,
        "cycle_end":         b.CycleEnd,
        "booster_total":     b.BoosterTotal,
        "booster_usable":    b.BoosterUsable,
        "booster_frozen":    b.BoosterFrozen,
        "next_refill_at":    b.NextRefillAt,
    }
}

// parentScopedBalanceView omits booster_* and next_refill_at per §5.4.
func parentScopedBalanceView(b *credit.BalanceDTO) gin.H {
    return gin.H{
        "user_id":          b.UserID,
        "membership_state": b.MembershipState,
        "trial_remaining":  b.TrialRemaining,
        "cycle_remaining":  b.CycleRemaining,
        "cycle_start":      b.CycleStart,
        "cycle_end":        b.CycleEnd,
    }
}
```

`admin_credit/balance.go`：

```go
// GetUserBalance handles GET /v1/admin/users/:user_id/balance.
// Admin view: full balance (incl. booster), no privacy boundary.
func (ctrl *Controller) GetUserBalance(c *gin.Context) {
    userID, err := strconv.ParseUint(c.Param("user_id"), 10, 64)
    if err != nil || userID == 0 {
        core.WriteResponse(c, errno.ErrBind.SetMessage("user_id 必须是正整数"), nil)
        return
    }
    bal, err := ctrl.creditBiz.GetBalance(c, uint(userID))
    if errors.Is(err, credit.ErrUserNotFound) {
        core.WriteResponse(c, errno.ErrUserNotFound, nil)
        return
    }
    if err != nil {
        core.WriteResponse(c, errno.ErrInternal, nil)
        return
    }
    // 复用用户端 fullBalanceView 序列化（同结构）
    core.WriteResponse(c, nil, credit.FullBalanceView(bal))
}
```

router.go / admin_router.go：

```go
// router.go
authGroup.GET("/credits/balance", creditCtrl.GetMyBalance)
authGroup.GET("/users/children/:child_id/balance", creditCtrl.GetChildBalance)

// admin_router.go
adminGroup.GET("/users/:user_id/balance", adminCreditCtrl.GetUserBalance)
```

- [ ] **Step 3: Refactor**

- `fullBalanceView` 导出（`FullBalanceView`），admin controller 直接复用，避免序列化代码两份
- `parentScopedBalanceView` 仅出现在用户端，作为隐私边界明确写出
- biz `IsChild` 方法在 Task 6 已实现，本 task 仅消费

- [ ] **Step 4: 手动验证**

```bash
# 用户查自己（含 booster）
curl -s -X GET http://localhost:8080/v1/credits/balance \
  -H "Authorization: Bearer $TOK" | jq
# 期望：data 含 booster_total, booster_usable, booster_frozen, next_refill_at

# 父账户查子账户（不含 booster）
curl -s -X GET http://localhost:8080/v1/users/children/1234/balance \
  -H "Authorization: Bearer $PARENT_TOK" | jq
# 期望：data 不含 booster_*, next_refill_at；含 trial_remaining, cycle_remaining

# 父子关系不通过
curl -i -X GET http://localhost:8080/v1/users/children/9999/balance \
  -H "Authorization: Bearer $PARENT_TOK"
# 期望：403 该子账户不属于当前账户

# Admin 查任意用户（含 booster）
curl -s -X GET http://localhost:8080/v1/admin/users/1234/balance \
  -H "Authorization: Bearer $ADMIN_TOK" | jq
# 期望：与第一个 endpoint 同结构
```

- [ ] **Step 5: Commit**

```
feat(credit): add 3 balance endpoints (user / parent-child / admin)

- GET /v1/credits/balance: full balance for self
- GET /v1/users/children/:child_id/balance: parent view, OMITS booster_*
  and next_refill_at per §5.4 privacy boundary
- GET /v1/admin/users/:user_id/balance: admin full view
- Single biz GetBalance method, view-layer field shaping in controller

Spec: §5.3 / §5.4 / §5.5

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
```

---

### Task 13: GET /v1/admin/b2b-billing-report cutover 三模式

### 背景

Spec §5.6 + §7。重写 `b2b_billing.go`：

- 读 `config.Billing.CutoverDate`（必填，零值启动失败 — 在 main.go 校验）
- `chooseSource(monthStart, monthEnd, cutoverDate)`：
  - `monthEnd <= cutoverDate` → `legacy_only`（扫 credit_package，§7.5）
  - `monthStart >= cutoverDate` → `new_only`（扫 membership_event，§7.6）
  - 否则 → `cutover_split`（双口径 UNION + ROW_NUMBER 去重，§7.3）
- 字段映射常量化（§7.1：`LegacyAmountTrial=990, LegacyAmountSubMonth=9900, LegacyAmountBoosterUnit=2990`）
- 复合键去重（§7.2）

### Files

- **Modify** `numind-server/internal/numind/biz/b2b_billing/b2b_billing.go`（核心重写）
- **Modify** `numind-server/internal/numind/biz/b2b_billing/b2b_billing_test.go`
- **Modify** `numind-server/internal/numind/controller/v1/admin_b2b/billing_report.go`（响应增加 source / cutover_date）
- **Modify** `numind-server/config/config.go`（新增 `Billing.CutoverDate time.Time` 必填）
- **Modify** `numind-server/cmd/numind/main.go`（启动校验 cutover_date 非零，否则 log.Fatal）

### TDD 五段式

- [ ] **Step 1: Red — 写失败测试**

```go
func TestChooseSource(t *testing.T) {
    cutover := time.Date(2026, 6, 3, 2, 0, 0, 0, time.UTC)
    // 测试用 month-boundary fixtures（test 文件顶部 var block 显式定义，避免裸字面量）
    var (
        t202604_01    = time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
        t202605_01    = time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
        t202606_01    = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
        t202607_01    = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
        t202608_01    = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
        // 切换日时刻 + 一个月后，用于验证月末/月初等于 cutover 的边界
        t202606_03_02 = cutover                            // 2026-06-03 02:00 UTC
        t202607_03_02 = cutover.AddDate(0, 1, 0)           // 2026-07-03 02:00 UTC
    )
    cases := []struct {
        name     string
        ms       time.Time
        me       time.Time
        wantSrc  string
    }{
        {"month_fully_before_cutover", t202604_01, t202605_01, "legacy_only"},
        {"month_fully_after_cutover",  t202607_01, t202608_01, "new_only"},
        {"month_straddling_cutover",   t202606_01, t202607_01, "cutover_split"},
        {"month_end_equals_cutover",   t202605_01, t202606_03_02, "legacy_only"},
        {"month_start_equals_cutover", t202606_03_02, t202607_03_02, "new_only"},
    }
    for _, tc := range cases {
        got := chooseSource(tc.ms, tc.me, cutover)
        require.Equal(t, tc.wantSrc, got, tc.name)
    }
}

func TestGetBillingReport_LegacyOnly(t *testing.T) {
    db := newTestDB(t)
    seedCreditPackage(db, model.CreditPackage{...})  // activated_at 在切换日前
    biz := New(db, B2BBillingConfig{CutoverDate: cutover})
    report, err := biz.GetBillingReport(ctx, "2026-04", 0)
    require.NoError(t, err)
    assert.Equal(t, "legacy_only", report.Source)
    assert.Equal(t, 1, report.TotalEventsCount)
}

func TestGetBillingReport_NewOnly(t *testing.T) {
    db := newTestDB(t)
    seedMembershipEvent(db, model.MembershipEvent{Source: "b2b_grant", OccurredAt: t202607_15, ...})
    biz := New(db, B2BBillingConfig{CutoverDate: cutover})
    report, err := biz.GetBillingReport(ctx, "2026-07", 0)
    require.NoError(t, err)
    assert.Equal(t, "new_only", report.Source)
}

func TestGetBillingReport_CutoverSplit(t *testing.T) {
    db := newTestDB(t)
    seedCreditPackage(db, model.CreditPackage{ActivatedAt: t202606_02, ...})  // 切换日前
    seedMembershipEvent(db, model.MembershipEvent{OccurredAt: t202606_05, ...})  // 切换日后
    biz := New(db, B2BBillingConfig{CutoverDate: cutover})
    report, err := biz.GetBillingReport(ctx, "2026-06", 0)
    require.NoError(t, err)
    assert.Equal(t, "cutover_split", report.Source)
    assert.Equal(t, 2, report.TotalEventsCount)  // legacy 1 + new 1, 不同复合键
}

func TestGetBillingReport_CutoverSplit_DedupesDuplicate(t *testing.T) {
    db := newTestDB(t)
    // 同一笔 grant：旧表 + 新表都有，复合键相同
    seedCreditPackage(db, ...)
    seedMembershipEvent(db, ...)  // 同 granter, child, occurred_at(秒级), product_type
    report, _ := biz.GetBillingReport(ctx, "2026-06", 0)
    assert.Equal(t, 1, report.TotalEventsCount)  // dedupe → 取 new
    assert.Equal(t, "new", report.ByParent[0].Details[0].SourceFlag)  // priority 1
}

func TestNew_RejectsZeroCutoverDate(t *testing.T) {
    require.Panics(t, func() {
        _ = New(db, B2BBillingConfig{CutoverDate: time.Time{}})
    })
}
```

- [ ] **Step 2: Green — 最小实现**

`b2b_billing.go` 重写：

```go
package b2b_billing

import (
    "context"
    "encoding/json"
    "fmt"
    "time"
)

// 字段映射常量（§7.1）
const (
    LegacyAmountTrial        = 990
    LegacyAmountSubMonth     = 9900
    LegacyAmountBoosterUnit  = 2990
)

type B2BBillingConfig struct {
    CutoverDate time.Time
}

type B2BBillingReport struct {
    Month              string             `json:"month"`
    CutoverDate        time.Time          `json:"cutover_date"`
    Source             string             `json:"source"`  // legacy_only / cutover_split / new_only
    ByParent           []ParentBillingRow `json:"by_parent"`
    TotalAmountCents   int64              `json:"total_amount_cents"`
    TotalEventsCount   int                `json:"total_events_count"`
    ActiveParentsCount int                `json:"active_parents_count"`
}

type b2bBillingBiz struct {
    ds  *gorm.DB
    cfg B2BBillingConfig
}

// New constructs the biz with hard validation of CutoverDate per §7.4.
func New(ds *gorm.DB, cfg B2BBillingConfig) *b2bBillingBiz {
    if cfg.CutoverDate.IsZero() {
        panic("b2b_billing: CutoverDate is required, configure billing.cutover_date in yaml")
    }
    return &b2bBillingBiz{ds: ds, cfg: cfg}
}

// chooseSource per §7.4: 严格按 monthEnd vs cutover / monthStart vs cutover 分发.
func chooseSource(monthStart, monthEnd, cutover time.Time) string {
    if !monthStart.Before(cutover) {
        return "new_only"
    }
    if !monthEnd.After(cutover) {
        return "legacy_only"
    }
    return "cutover_split"
}

func (b *b2bBillingBiz) GetBillingReport(ctx context.Context, monthStr string, granterFilter uint) (*B2BBillingReport, error) {
    monthStart, monthEnd, err := parseMonth(monthStr)
    if err != nil {
        return nil, fmt.Errorf("GetBillingReport: %w", err)
    }
    src := chooseSource(monthStart, monthEnd, b.cfg.CutoverDate)
    var rep *B2BBillingReport
    switch src {
    case "legacy_only":
        rep, err = b.getLegacyReport(ctx, monthStart, monthEnd, granterFilter)
    case "new_only":
        rep, err = b.getNewReport(ctx, monthStart, monthEnd, granterFilter)
    case "cutover_split":
        rep, err = b.getCutoverSplitReport(ctx, monthStart, monthEnd, b.cfg.CutoverDate, granterFilter)
    }
    if err != nil { return nil, err }
    rep.Month = monthStr
    rep.CutoverDate = b.cfg.CutoverDate
    rep.Source = src
    return rep, nil
}

// getLegacyReport: §7.5, 沿用现有逻辑（扫 credit_package + amountForPackage 常量价）
func (b *b2bBillingBiz) getLegacyReport(ctx context.Context, ms, me time.Time, gf uint) (*B2BBillingReport, error) {
    // 复用现有 GetBillingReport line 78-170 主体（保留 amountForPackage 语义）
    // 输出字段补齐 event_type / event_id=null / legacy_package_id=cp.id / idempotency_key=legacy_pkg_<id>
}

// getNewReport: §7.6, 单条 SQL with JSON_ARRAYAGG（避免 N+1）
func (b *b2bBillingBiz) getNewReport(ctx context.Context, ms, me time.Time, gf uint) (*B2BBillingReport, error) {
    var rows []parentRowRaw
    sql := `
        SELECT me.granter_user_id AS parent_user_id, u.username AS parent_username,
               COUNT(*) AS events_count, SUM(me.amount_cents) AS amount_cents,
               JSON_ARRAYAGG(JSON_OBJECT(
                 'event_id', me.id,
                 'child_user_id', me.child_user_id,
                 'child_username', cu.username,
                 'event_type', me.event_type,
                 'product_type', me.product_type,
                 'months', me.months,
                 'quantity', me.quantity,
                 'amount_cents', me.amount_cents,
                 'occurred_at', DATE_FORMAT(me.occurred_at, '%Y-%m-%dT%H:%i:%sZ'),
                 'source', me.source,
                 'idempotency_key', me.idempotency_key
               )) AS details_json
        FROM membership_event me
        LEFT JOIN user u  ON u.id  = me.granter_user_id
        LEFT JOIN user cu ON cu.id = me.child_user_id
        WHERE me.source='b2b_grant' AND me.granter_user_id IS NOT NULL
          AND me.occurred_at >= ? AND me.occurred_at < ?
          AND (? = 0 OR me.granter_user_id = ?)
        GROUP BY me.granter_user_id, u.username
        ORDER BY me.granter_user_id ASC`
    if err := b.ds.WithContext(ctx).Raw(sql, ms, me, gf, gf).Scan(&rows).Error; err != nil {
        return nil, fmt.Errorf("getNewReport: %w", err)
    }
    return assembleReport(rows), nil
}

// getCutoverSplitReport: §7.3 双口径 UNION + ROW_NUMBER 去重
func (b *b2bBillingBiz) getCutoverSplitReport(ctx context.Context, ms, me time.Time, cutover time.Time, gf uint) (*B2BBillingReport, error) {
    // 完整 §7.3 SQL（CTE legacy_events + new_events + unioned + deduped + 聚合）
    // gf=0 → :granter_filter IS NULL 分支
    var rows []parentRowRaw
    sql := `WITH legacy_events AS ( ... ), new_events AS ( ... ), unioned AS ( ... ),
            deduped AS ( SELECT ..., ROW_NUMBER() OVER (PARTITION BY granter_user_id, child_user_id, ...) AS rn FROM unioned )
            SELECT ... FROM deduped d WHERE d.rn = 1 GROUP BY d.granter_user_id ...`
    if err := b.ds.WithContext(ctx).Raw(sql, ms, cutover, me, gf, ...).Scan(&rows).Error; err != nil {
        return nil, fmt.Errorf("getCutoverSplitReport: %w", err)
    }
    return assembleReport(rows), nil
}

// assembleReport: 通用聚合，三个 getXxxReport 共用
func assembleReport(rows []parentRowRaw) *B2BBillingReport {
    rep := &B2BBillingReport{
        ByParent:           make([]ParentBillingRow, 0, len(rows)),
        ActiveParentsCount: len(rows),
    }
    for _, r := range rows {
        var details []GrantDetail
        _ = json.Unmarshal(r.DetailsJSON, &details)
        rep.ByParent = append(rep.ByParent, ParentBillingRow{
            ParentUserID: r.ParentUserID, ParentUsername: r.ParentUsername,
            EventsCount: r.EventsCount, AmountCents: r.AmountCents, Details: details,
        })
        rep.TotalAmountCents += r.AmountCents
        rep.TotalEventsCount += r.EventsCount
    }
    return rep
}
```

config.go：

```go
type Config struct {
    // ...
    Billing BillingConfig `mapstructure:"billing"`
}
type BillingConfig struct {
    CutoverDate time.Time `mapstructure:"cutover_date"`
}
```

main.go 启动校验：

```go
if cfg.Billing.CutoverDate.IsZero() {
    log.Fatal("billing.cutover_date is required, configure in yaml or via BILLING_CUTOVER_DATE env var")
}
```

controller billing_report.go 直接 marshal `*B2BBillingReport`（含 source / cutover_date）。

- [ ] **Step 3: Refactor**

- `assembleReport` 抽出供三 path 复用
- 常量集中（LegacyAmountTrial/SubMonth/BoosterUnit）
- 三个 SQL 全用 `gorm.Raw(...).Scan(&[]parentRowRaw)` 模式 + `json.RawMessage` 接收 `details_json`，再 `json.Unmarshal` 到 `[]GrantDetail`
- 老 `b2b_billing.go` 现有的 `amountForPackage` / `productTypeForPackage` 函数保留并迁移为内部常量映射，避免读懂老代码就能继承语义

- [ ] **Step 4: 手动验证**

```bash
# legacy_only（切换日前月份）
curl -s -X GET 'http://localhost:8080/v1/admin/b2b-billing-report?month=2026-04' \
  -H "Authorization: Bearer $ADMIN_TOK" | jq '.data | {source,total_events_count}'
# 期望：source=legacy_only

# new_only（切换日后月份）
curl -s -X GET 'http://localhost:8080/v1/admin/b2b-billing-report?month=2026-07' \
  -H "Authorization: Bearer $ADMIN_TOK" | jq '.data | {source}'
# 期望：source=new_only

# cutover_split（跨切换日的当月）
curl -s -X GET 'http://localhost:8080/v1/admin/b2b-billing-report?month=2026-06' \
  -H "Authorization: Bearer $ADMIN_TOK" | jq '.data | {source,total_events_count}'
# 期望：source=cutover_split, 总数 = 老口径 + 新口径 - 去重重叠

# 父账户过滤
curl -s -X GET 'http://localhost:8080/v1/admin/b2b-billing-report?month=2026-06&granter_user_id=100' \
  -H "Authorization: Bearer $ADMIN_TOK" | jq '.data.by_parent | length'
# 期望：≤ 1（只剩 100）

# 启动校验：删除 cutover_date 后启动
# 期望：log.Fatal "billing.cutover_date is required..."
```

- [ ] **Step 5: Commit**

```
feat(b2b_billing): add cutover-aware billing report (legacy / split / new)

- chooseSource: monthEnd<=cutover → legacy_only; monthStart>=cutover → new_only;
  else cutover_split per §7.4
- getLegacyReport: reuse existing credit_package scan with hardcoded
  amountForPackage constants (LegacyAmount{Trial,SubMonth,BoosterUnit})
- getNewReport: single SQL with JSON_ARRAYAGG over membership_event per §7.6
- getCutoverSplitReport: CTE UNION + ROW_NUMBER PARTITION BY composite key
  (granter,child,occurred_sec,product_type,COALESCE months/quantity) per §7.3
  with source_priority new=1 over legacy=2 to dedupe migration overlap
- New() panics on zero CutoverDate; main.go log.Fatal on missing config
- Response surfaces source + cutover_date for admin UI display

Spec: §5.6 / §7

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
```

---

## Phase 3 完成判定

- ✅ 5 个 task 全部 commit
- ✅ `go test ./...` 全绿
- ✅ `task lint` 全绿
- ✅ 5 个端点（grant, orders, my balance, child balance, admin balance, billing report）curl 全部通过
- ✅ `progress.completed_tasks` 9..13 全部 +1 且 `reviewed_tasks` 同步
- ✅ Spec compliance + code quality 两阶段 review 全部 PASS（per ndf-enforcement.md 规则 6）

下一阶段（Phase 4: 前端契约）依赖 Task 12 完成的 BalanceDTO + Task 13 的 source/cutover_date 字段。

---

# Plan — Tasks 14-16 (Phase 4: 迁移 & 部署 & cleanup)

> 本文件为 NDF S3 plan 的 Phase 4 部分，覆盖：
> - Task 14：4 件套迁移脚本（`scripts/2026-04-30-membership-credits-redesign-migration/`）
> - Task 15：MAINTENANCE_MODE 中间件 + 错误码
> - Task 16：cleanup（删 cron + 老 path 替换）
>
> 依赖：Task 1-13 已完成（5 张新表 schema、biz 层、controller、payment 改造均已落地）。

---

### Task 14: 迁移 4 件套脚本

**Files:**

**Create:**
- `numind-server/scripts/2026-04-30-membership-credits-redesign-migration/01-dry-run.sql`
- `numind-server/scripts/2026-04-30-membership-credits-redesign-migration/02-apply.sql`
- `numind-server/scripts/2026-04-30-membership-credits-redesign-migration/03-verify.sql`
- `numind-server/scripts/2026-04-30-membership-credits-redesign-migration/04-rollback.sql`
- `numind-server/scripts/2026-04-30-membership-credits-redesign-migration/README.md`

**TDD Plan（5 段式）**

- [ ] **Step 1: 写测试（staging dry-run 验收脚本）**

在 staging 环境用 prod 脱敏快照建立 baseline：
- 抽 10 个典型用户：1 个仅有 trial、1 个仅有 booster、3 个连续续费段、3 个有空档段、2 个混合（trial+sub+booster）
- 人工算出 expected post-migration 状态（subscription / trial_grant / user_booster_balance / membership_event 各表预期行）
- 写 expected.json 作为对账 ground truth

- [ ] **Step 2: 写脚本（4 件套 SQL + README）**

01-dry-run.sql 的关键节段（参考 spec §6.2.1，完整可执行片段）：

```sql
-- 01-dry-run.sql — 只读，输出迁前迁后预期对照
-- 运行：docker exec -i numind-mysql-prod mysql -uroot -p$DB_PASS numind-prod < 01-dry-run.sql

-- ----------------------------------------------------------------------
-- Section 1: 临时表存放段合并预期结果
-- ----------------------------------------------------------------------
DROP TEMPORARY TABLE IF EXISTS tmp_segment_merge_result;
CREATE TEMPORARY TABLE tmp_segment_merge_result (
  user_id            BIGINT UNSIGNED NOT NULL PRIMARY KEY,
  first_started_at   DATETIME(3) NOT NULL,
  current_started_at DATETIME(3) NOT NULL,
  expires_at         DATETIME(3) NOT NULL,
  total_segment_credits INT,
  total_months_purchased INT
) ENGINE=Memory;

-- ----------------------------------------------------------------------
-- Section 2: 段合并 CTE → tmp_segment_merge_result（spec §6.1.2 完整展开）
-- ----------------------------------------------------------------------
INSERT INTO tmp_segment_merge_result
WITH ordered_subs AS (
  SELECT
    user_id, id AS pkg_id, activated_at, expires_at, total_credits,
    remain_credits, grant_source, granter_user_id, order_id,
    ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY activated_at) AS rn,
    LAG(expires_at) OVER (PARTITION BY user_id ORDER BY activated_at) AS prev_expires_at
  FROM credit_package
  WHERE type = 'subscription' AND status IN ('active','expired','consumed')
),
segment_marked AS (
  SELECT *,
    CASE
      WHEN prev_expires_at IS NULL THEN 1
      WHEN activated_at > prev_expires_at THEN 1
      ELSE 0
    END AS is_new_segment
  FROM ordered_subs
),
segmented AS (
  SELECT *, SUM(is_new_segment) OVER (PARTITION BY user_id ORDER BY rn) AS segment_id
  FROM segment_marked
),
segment_summary AS (
  SELECT
    user_id, segment_id,
    MIN(activated_at) AS segment_start,
    MAX(expires_at) AS segment_end,
    SUM(total_credits) AS total_segment_credits
  FROM segmented
  GROUP BY user_id, segment_id
),
last_segment AS (
  SELECT user_id, segment_id, segment_start, segment_end, total_segment_credits,
    ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY segment_id DESC) AS seg_rn
  FROM segment_summary
)
SELECT
  user_id, segment_start, segment_start, segment_end, total_segment_credits,
  GREATEST(1, CEIL(TIMESTAMPDIFF(DAY, segment_start, segment_end) / 30.0)) AS total_months_purchased
FROM last_segment
WHERE seg_rn = 1 AND segment_end > NOW();

-- ----------------------------------------------------------------------
-- Section 3: trial 包预期映射（每用户取最早 trial 包）
-- ----------------------------------------------------------------------
DROP TEMPORARY TABLE IF EXISTS tmp_trial_grant_expected;
CREATE TEMPORARY TABLE tmp_trial_grant_expected (
  user_id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
  granted_at DATETIME(3) NOT NULL,
  expires_at DATETIME(3) NOT NULL,
  credits_total INT NOT NULL DEFAULT 200,
  credits_remaining INT NOT NULL,
  granter_user_id BIGINT UNSIGNED,
  source_package_id BIGINT UNSIGNED
) ENGINE=Memory;

INSERT INTO tmp_trial_grant_expected
SELECT
  cp.user_id, cp.activated_at, cp.expires_at, cp.total_credits,
  cp.remain_credits, cp.granter_user_id, cp.id
FROM credit_package cp
INNER JOIN (
  SELECT user_id, MIN(activated_at) AS first_trial_at
  FROM credit_package WHERE type='trial' GROUP BY user_id
) ft ON ft.user_id = cp.user_id AND ft.first_trial_at = cp.activated_at
WHERE cp.type='trial';

-- ----------------------------------------------------------------------
-- Section 4: booster 单 balance 聚合预期 + total_credits % 600 校验
-- ----------------------------------------------------------------------
DROP TEMPORARY TABLE IF EXISTS tmp_booster_balance_expected;
CREATE TEMPORARY TABLE tmp_booster_balance_expected (
  user_id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
  credits_remaining INT NOT NULL,
  total_purchased INT NOT NULL,
  last_purchase_at DATETIME(3)
) ENGINE=Memory;

INSERT INTO tmp_booster_balance_expected
SELECT
  user_id,
  COALESCE(SUM(CASE WHEN status='active' AND expires_at > NOW() THEN remain_credits ELSE 0 END), 0),
  COALESCE(SUM(total_credits), 0),
  MAX(activated_at)
FROM credit_package
WHERE type='booster'
GROUP BY user_id
HAVING credits_remaining > 0 OR total_purchased > 0;

-- BLOCKER 校验：booster total_credits 必须是 600 倍数
SELECT '=== BLOCKER: booster total_credits %% 600 != 0 (expect 0 rows) ===' AS check_name;
SELECT id, user_id, total_credits, (total_credits %% 600) AS remainder
FROM credit_package
WHERE type='booster' AND total_credits %% 600 != 0;

-- ----------------------------------------------------------------------
-- Section 5: 差额对账（pre_total vs post_total）
--   pre  = SUM(active package.remain WHERE expires_at > NOW())
--   post = trial.remain + (有 sub 时 2000 cycle 赠送) + booster.remain
--   恩泽迁移：post >= pre 即合规；post < pre 即 BLOCKER
-- ----------------------------------------------------------------------
SELECT '=== Per-user delta (BLOCKER if post < pre) ===' AS section;
SELECT
  u.id AS user_id, u.username,
  COALESCE((
    SELECT SUM(remain_credits) FROM credit_package
    WHERE user_id = u.id AND status='active' AND expires_at > NOW()
  ), 0) AS pre_total,
  COALESCE((SELECT credits_remaining FROM tmp_trial_grant_expected
            WHERE user_id = u.id AND expires_at > NOW()), 0)
  + CASE WHEN EXISTS (SELECT 1 FROM tmp_segment_merge_result WHERE user_id = u.id)
         THEN 2000 ELSE 0 END
  + COALESCE((SELECT credits_remaining FROM tmp_booster_balance_expected
              WHERE user_id = u.id), 0) AS post_total
FROM user u
WHERE EXISTS (SELECT 1 FROM credit_package WHERE user_id = u.id)
HAVING post_total < pre_total
ORDER BY (pre_total - post_total) DESC;

SELECT '=== Aggregate metrics ===' AS section;
SELECT 'users_with_packages' AS metric, COUNT(DISTINCT user_id) AS value FROM credit_package
UNION ALL SELECT 'expected_subscription_rows', COUNT(*) FROM tmp_segment_merge_result
UNION ALL SELECT 'expected_trial_grant_rows', COUNT(*) FROM tmp_trial_grant_expected
UNION ALL SELECT 'expected_booster_balance_rows', COUNT(*) FROM tmp_booster_balance_expected;

SELECT 'DRY-RUN COMPLETE — 0 BLOCKER rows required before 02-apply.sql' AS status;
```

02-apply.sql 关键结构（参考 spec §6.2.2）：

```sql
-- 02-apply.sql — 单事务全量写入；backup 在事务外（DDL implicit commit）

-- ----------------------------------------------------------------------
-- 顶部：事务外的 backup CREATE TABLE + INSERT + 行数校验
-- ----------------------------------------------------------------------
DROP TABLE IF EXISTS membership_redesign_backup_credit_package_20260430;
CREATE TABLE membership_redesign_backup_credit_package_20260430 LIKE credit_package;
INSERT INTO membership_redesign_backup_credit_package_20260430 SELECT * FROM credit_package;

SELECT
  (SELECT COUNT(*) FROM credit_package) AS source_count,
  (SELECT COUNT(*) FROM membership_redesign_backup_credit_package_20260430) AS backup_count;
-- 期望 source_count = backup_count，否则停止迁移

-- ----------------------------------------------------------------------
-- Step 1: 创建 apply_log 表（rollback 反向 JOIN 用） + 锚定 cutover_ts
-- ----------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS membership_redesign_apply_log_20260430 (
  table_name VARCHAR(64) NOT NULL,
  row_id     BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (table_name, row_id)
) ENGINE=InnoDB;

START TRANSACTION;
SET @cutover_ts = NOW(3);

-- ----------------------------------------------------------------------
-- Step 2: 段合并 → INSERT subscription
--   total_months_purchased = CEIL(DAY/30) 防 TIMESTAMPDIFF(MONTH) 整月截断
-- ----------------------------------------------------------------------
INSERT INTO subscription
  (user_id, first_started_at, current_started_at, expires_at,
   total_months_purchased, granter_user_id, created_at, updated_at)
-- spec §2.1 DDL has no status column; invariant I-1 forbids state field —
-- expires_at vs NOW() is the single source of truth for active.
WITH ordered_subs AS (
  SELECT user_id, id, activated_at, expires_at, total_credits, granter_user_id,
    ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY activated_at) AS rn,
    LAG(expires_at) OVER (PARTITION BY user_id ORDER BY activated_at) AS prev_expires_at
  FROM credit_package
  WHERE type='subscription' AND status IN ('active','expired','consumed')
),
segment_marked AS (
  SELECT *, CASE
    WHEN prev_expires_at IS NULL THEN 1
    WHEN activated_at > prev_expires_at THEN 1
    ELSE 0 END AS is_new_segment
  FROM ordered_subs
),
segmented AS (
  SELECT *, SUM(is_new_segment) OVER (PARTITION BY user_id ORDER BY rn) AS segment_id
  FROM segment_marked
),
segment_summary AS (
  SELECT user_id, segment_id, MIN(activated_at) AS segment_start,
    MAX(expires_at) AS segment_end, SUM(total_credits) AS total_segment_credits
  FROM segmented GROUP BY user_id, segment_id
),
last_segment AS (
  SELECT user_id, segment_start, segment_end, total_segment_credits,
    ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY segment_id DESC) AS seg_rn
  FROM segment_summary
)
SELECT
  ls.user_id, ls.segment_start, ls.segment_start, ls.segment_end,
  GREATEST(1, CEIL(TIMESTAMPDIFF(DAY, ls.segment_start, ls.segment_end) / 30.0)),
  (SELECT granter_user_id FROM credit_package
   WHERE user_id = ls.user_id AND type='subscription'
   ORDER BY activated_at DESC LIMIT 1),
  NOW(3), NOW(3)
FROM last_segment ls
WHERE ls.seg_rn = 1 AND ls.segment_end > NOW();

-- ----------------------------------------------------------------------
-- Step 3: trial 包 → INSERT trial_grant（每用户最早 1 行）
-- ----------------------------------------------------------------------
INSERT INTO trial_grant
  (user_id, granted_at, expires_at, credits_total, credits_remaining,
   granter_user_id, source_package_id, created_at)
SELECT
  cp.user_id, cp.activated_at, cp.expires_at, cp.total_credits, cp.remain_credits,
  cp.granter_user_id, cp.id, NOW(3)
FROM credit_package cp
INNER JOIN (
  SELECT user_id, MIN(activated_at) AS first_trial_at
  FROM credit_package WHERE type='trial' GROUP BY user_id
) ft ON ft.user_id = cp.user_id AND ft.first_trial_at = cp.activated_at
WHERE cp.type='trial';

-- ----------------------------------------------------------------------
-- Step 4: booster 聚合 → INSERT user_booster_balance
-- ----------------------------------------------------------------------
INSERT INTO user_booster_balance
  (user_id, credits_remaining, total_purchased, last_purchase_at, created_at, updated_at)
SELECT
  user_id,
  COALESCE(SUM(CASE WHEN status='active' AND expires_at > NOW()
                    THEN remain_credits ELSE 0 END), 0),
  COALESCE(SUM(total_credits), 0),
  MAX(activated_at),
  NOW(3), NOW(3)
FROM credit_package
WHERE type='booster'
GROUP BY user_id
HAVING credits_remaining > 0 OR total_purchased > 0;

-- ----------------------------------------------------------------------
-- Step 5: 反向重建 membership_event（含 sub_with_segment CTE 区分 granted/renewed）
-- ----------------------------------------------------------------------
INSERT INTO membership_event
  (user_id, granter_user_id, event_type, product_type, months, quantity,
   amount_cents, occurred_at, idempotency_key, source_package_id, created_at)
WITH numbered_subs AS (
  SELECT cp.id, cp.user_id, cp.activated_at, cp.expires_at,
    ROW_NUMBER() OVER (PARTITION BY cp.user_id ORDER BY cp.activated_at) AS rn_user,
    LAG(cp.expires_at) OVER (PARTITION BY cp.user_id ORDER BY cp.activated_at) AS prev_expires_at
  FROM credit_package cp WHERE cp.type='subscription'
),
sub_with_segment AS (
  SELECT id, user_id, activated_at,
    SUM(CASE
      WHEN prev_expires_at IS NULL THEN 1
      WHEN activated_at > prev_expires_at THEN 1
      ELSE 0 END) OVER (PARTITION BY user_id ORDER BY rn_user) AS segment_id,
    ROW_NUMBER() OVER (
      PARTITION BY user_id, SUM(CASE
        WHEN prev_expires_at IS NULL THEN 1
        WHEN activated_at > prev_expires_at THEN 1
        ELSE 0 END) OVER (PARTITION BY user_id ORDER BY rn_user)
      ORDER BY activated_at) AS rn_in_segment
  FROM numbered_subs
)
SELECT
  cp.user_id, cp.granter_user_id,
  CASE
    WHEN cp.type='trial' THEN 'trial_granted'
    WHEN cp.type='booster' THEN 'booster_purchased'
    WHEN cp.type='subscription' AND swseg.rn_in_segment = 1 THEN 'sub_granted'
    WHEN cp.type='subscription' THEN 'sub_renewed'
  END,
  cp.type,
  CASE WHEN cp.type='subscription'
       THEN GREATEST(1, CEIL(TIMESTAMPDIFF(DAY, cp.activated_at, cp.expires_at) / 30.0))
       ELSE NULL END,
  CASE WHEN cp.type='booster' THEN CEIL(cp.total_credits / 600.0) ELSE NULL END,
  COALESCE((SELECT amount_cents FROM `order` WHERE id = cp.order_id), 0),
  cp.activated_at,
  CONCAT('migration-20260430-pkg-', cp.id),
  cp.id, NOW(3)
FROM credit_package cp
LEFT JOIN sub_with_segment swseg ON swseg.id = cp.id AND cp.type='subscription'
ORDER BY cp.user_id, cp.activated_at;

-- ----------------------------------------------------------------------
-- Step 6: credit_cycle 不预创建（懒创建语义；切换后首次扣分由 biz 创建）
-- ----------------------------------------------------------------------
-- (intentionally empty)

-- ----------------------------------------------------------------------
-- Step 6.5: apply_log 写入（rollback 反向定位用）
-- ----------------------------------------------------------------------
INSERT INTO membership_redesign_apply_log_20260430 (table_name, row_id)
SELECT 'subscription', id FROM subscription WHERE created_at >= @cutover_ts
UNION ALL
SELECT 'trial_grant', id FROM trial_grant WHERE source_package_id IS NOT NULL
UNION ALL
SELECT 'user_booster_balance', id FROM user_booster_balance WHERE created_at >= @cutover_ts
UNION ALL
SELECT 'membership_event', id FROM membership_event
WHERE idempotency_key LIKE 'migration-20260430-%';

-- ----------------------------------------------------------------------
-- Step 7: legacy 字段不动（保留 user_tier / tier_expires / monthly_sop_runs 只读）
-- ----------------------------------------------------------------------
-- (intentionally empty — spec §1.4 out-of-scope)

COMMIT;

SELECT 'APPLY COMPLETE — run 03-verify.sql IMMEDIATELY' AS status;
```

03-verify.sql 关键 invariant（spec §6.2.3 全部 8 条）：

```sql
-- 03-verify.sql — 8 条 invariant 立即对账，任一 violation_count > 0 立即跑 04-rollback.sql

-- I1: 非负净增（恩泽迁移：post >= pre）
SELECT 'INVARIANT_1_NON_NEGATIVE_NET_DELTA' AS check_name, COUNT(*) AS violation_count
FROM (
  SELECT u.id AS user_id,
    COALESCE((SELECT SUM(remain_credits) FROM membership_redesign_backup_credit_package_20260430
              WHERE user_id = u.id AND status='active' AND expires_at > NOW()), 0) AS pre_total,
    COALESCE((SELECT credits_remaining FROM trial_grant
              WHERE user_id = u.id AND expires_at > NOW()), 0)
    + CASE WHEN EXISTS (SELECT 1 FROM subscription
                         WHERE user_id = u.id AND expires_at > NOW()) THEN 2000 ELSE 0 END
    + COALESCE((SELECT credits_remaining FROM user_booster_balance WHERE user_id = u.id), 0) AS post_total
  FROM user u
  WHERE EXISTS (SELECT 1 FROM membership_redesign_backup_credit_package_20260430 WHERE user_id = u.id)
) calc
WHERE post_total < pre_total;

-- I2: subscription.expires_at 匹配旧表段尾（容忍 1 秒）
SELECT 'INVARIANT_2_SUB_EXPIRES_MATCH' AS check_name, COUNT(*) AS violation_count
FROM subscription s
LEFT JOIN (
  SELECT user_id, MAX(expires_at) AS old_max_expires
  FROM membership_redesign_backup_credit_package_20260430
  WHERE type='subscription' AND status='active' AND expires_at > NOW()
  GROUP BY user_id
) old ON old.user_id = s.user_id
WHERE old.user_id IS NULL OR ABS(TIMESTAMPDIFF(SECOND, s.expires_at, old.old_max_expires)) > 1;

-- I3: trial_grant.credits_remaining 匹配
SELECT 'INVARIANT_3_TRIAL_REMAIN_MATCH' AS check_name, COUNT(*) AS violation_count
FROM trial_grant tg
LEFT JOIN (
  SELECT user_id, remain_credits AS old_remain,
    ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY activated_at DESC) AS rn
  FROM membership_redesign_backup_credit_package_20260430 WHERE type='trial'
) old ON old.user_id = tg.user_id AND old.rn = 1
WHERE old.user_id IS NULL OR tg.credits_remaining != old.old_remain;

-- I4: user_booster_balance 聚合匹配
SELECT 'INVARIANT_4_BOOSTER_REMAIN_SUM' AS check_name, COUNT(*) AS violation_count
FROM user_booster_balance ubb
LEFT JOIN (
  SELECT user_id, COALESCE(SUM(remain_credits), 0) AS old_sum
  FROM membership_redesign_backup_credit_package_20260430
  WHERE type='booster' AND status='active' AND expires_at > NOW()
  GROUP BY user_id
) old ON old.user_id = ubb.user_id
WHERE old.old_sum IS NULL OR ubb.credits_remaining != old.old_sum;

-- I5a: 迁移当天 COUNT(membership_event) >= COUNT(旧 grant 记录)
SELECT 'INVARIANT_5A_EVENT_COUNT_FLOOR_MIGRATION_DAY' AS check_name, COUNT(*) AS violation_count
FROM (
  SELECT u.id,
    (SELECT COUNT(*) FROM membership_event WHERE user_id = u.id) AS new_evt_cnt,
    (SELECT COUNT(*) FROM membership_redesign_backup_credit_package_20260430
     WHERE user_id = u.id) AS old_pkg_cnt
  FROM user u
  WHERE EXISTS (SELECT 1 FROM membership_redesign_backup_credit_package_20260430 WHERE user_id = u.id)
) calc WHERE new_evt_cnt < old_pkg_cnt;

-- I5b: 守护期 daily-verify 模板（迁移当天跳过；T+1d 起 cron 跑）
SET @window_start = DATE_SUB(NOW(), INTERVAL 1 DAY);
SET @window_end   = NOW();
SELECT 'INVARIANT_5B_EVENT_DELTA_VS_GRANT_OPS_DAILY' AS check_name,
  CASE WHEN event_delta >= grant_ops THEN 0 ELSE 1 END AS violation_count
FROM (
  SELECT
    (SELECT COUNT(*) FROM membership_event
     WHERE created_at >= @window_start AND created_at < @window_end) AS event_delta,
    (
      (SELECT COUNT(*) FROM subscription
       WHERE created_at >= @window_start AND created_at < @window_end)
      + (SELECT COUNT(*) FROM subscription
         WHERE updated_at >= @window_start AND updated_at < @window_end
           AND created_at < @window_start)
      + (SELECT COUNT(*) FROM trial_grant
         WHERE created_at >= @window_start AND created_at < @window_end)
      + (SELECT COUNT(*) FROM user_booster_balance
         WHERE updated_at >= @window_start AND updated_at < @window_end)
    ) AS grant_ops
) calc;

-- I6: trial_grant UNIQUE on user_id
SELECT 'INVARIANT_6_TRIAL_UNIQUE' AS check_name, COUNT(*) AS violation_count
FROM (SELECT user_id, COUNT(*) c FROM trial_grant GROUP BY user_id HAVING c > 1) dup;

-- I7: subscription UNIQUE on (user_id WHERE status='active')
SELECT 'INVARIANT_7_ACTIVE_SUB_UNIQUE' AS check_name, COUNT(*) AS violation_count
FROM (SELECT user_id, COUNT(*) c FROM subscription
      WHERE status='active' GROUP BY user_id HAVING c > 1) dup;

-- I8: 无 orphan 行
SELECT 'INVARIANT_8_NO_ORPHAN' AS check_name, SUM(violation_count) AS violation_count
FROM (
  SELECT COUNT(*) AS violation_count FROM subscription s LEFT JOIN user u ON u.id = s.user_id WHERE u.id IS NULL
  UNION ALL SELECT COUNT(*) FROM trial_grant tg LEFT JOIN user u ON u.id = tg.user_id WHERE u.id IS NULL
  UNION ALL SELECT COUNT(*) FROM user_booster_balance ubb LEFT JOIN user u ON u.id = ubb.user_id WHERE u.id IS NULL
  UNION ALL SELECT COUNT(*) FROM membership_event me LEFT JOIN user u ON u.id = me.user_id WHERE u.id IS NULL
) o;

SELECT 'VERIFY COMPLETE — all violation_count fields MUST be 0' AS status;
```

04-rollback.sql 关键结构（apply_log 反向 JOIN）：

```sql
-- 04-rollback.sql — 仅 T+0 ~ T+24h 内可用
-- backup 表保留（应急对账），credit_package 不动（仍可查）

START TRANSACTION;

-- 用 apply_log 反向 JOIN 删除迁移写入行（不依赖 created_at 范围判定）
DELETE me FROM membership_event me
INNER JOIN membership_redesign_apply_log_20260430 al
  ON al.table_name='membership_event' AND al.row_id = me.id;
-- 兜底（apply_log 缺失场景）
DELETE FROM membership_event WHERE idempotency_key LIKE 'migration-20260430-%';

DELETE s FROM subscription s
INNER JOIN membership_redesign_apply_log_20260430 al
  ON al.table_name='subscription' AND al.row_id = s.id;

DELETE tg FROM trial_grant tg
INNER JOIN membership_redesign_apply_log_20260430 al
  ON al.table_name='trial_grant' AND al.row_id = tg.id;
-- 兜底
DELETE FROM trial_grant WHERE source_package_id IS NOT NULL;

DELETE ubb FROM user_booster_balance ubb
INNER JOIN membership_redesign_apply_log_20260430 al
  ON al.table_name='user_booster_balance' AND al.row_id = ubb.id;

-- credit_cycle 在切换后由 biz 懒创建，rollback 一并清理
SET @cutover_ts = (SELECT MIN(created_at) FROM membership_redesign_apply_log_20260430 al
                   INNER JOIN subscription s ON s.id = al.row_id
                   WHERE al.table_name='subscription');
DELETE FROM credit_cycle WHERE created_at >= @cutover_ts;

-- backup 表 + apply_log 表保留（应急对账，30 天后人工 DROP）

COMMIT;

SELECT 'ROLLBACK COMPLETE — backup/apply_log preserved for audit' AS status;
SELECT 'NEXT: git revert deployment commit + restart service to load old code' AS next_step;
```

README.md 内容（参考 spec §6.4 maintenance window runbook）：

```markdown
# Membership Credits Redesign Migration (2026-04-30)

## 范围
将 `credit_package` 单表 + `user_tier`/`tier_expires` 字段一次性切换到 5 张新表：
- subscription / trial_grant / credit_cycle（懒创建）/ user_booster_balance / membership_event

## 时间预算（按 step 锚定，超阈值熔断）

| Step | 操作 | P50 | P95 | P99 / 熔断阈值 |
|---|---|---|---|---|
| 1 | 部署 maintenance | 30s | 60s | 90s |
| 2 | 流量稳定 503 | 30s | 45s | 60s |
| 3.1 | 跑 01-dry-run.sql | 60s | 120s | 180s |
| 3.2 | 跑 02-apply.sql | 120s | 200s | 300s（5 分钟） |
| 3.3 | 跑 03-verify.sql | 60s | 120s | 180s |
| 4 | 部署正常版本 | 90s | 150s | 180s |
| 5 | 解除 maintenance | 30s | 45s | 60s |
| 6 | smoke test | 90s | 150s | 180s |
| 整体 | T → 全恢复 | ~10min | ~12min | **15 分钟硬熔断** |

## 执行顺序（prod）
\`\`\`bash
# T-1 day staging 演练（必跑 rollback）
docker exec -i numind-mysql-staging mysql -uroot -p$DB_PASS numind-staging < 01-dry-run.sql
docker exec -i numind-mysql-staging mysql -uroot -p$DB_PASS numind-staging < 02-apply.sql
docker exec -i numind-mysql-staging mysql -uroot -p$DB_PASS numind-staging < 03-verify.sql
docker exec -i numind-mysql-staging mysql -uroot -p$DB_PASS numind-staging < 04-rollback.sql

# T 时刻（凌晨 03:00）
ssh prod 'docker stack deploy -c docker-compose.maintenance.yaml numind-prod'
# 等流量稳定 503（30s）
docker exec -i numind-mysql-prod mysql -uroot -p$DB_PASS numind-prod < 01-dry-run.sql > dry-run.log
docker exec -i numind-mysql-prod mysql -uroot -p$DB_PASS numind-prod < 02-apply.sql
docker exec -i numind-mysql-prod mysql -uroot -p$DB_PASS numind-prod < 03-verify.sql > verify.log
# verify 全 PASS → 部署正常版本
ssh prod 'docker stack deploy -c docker-compose.prod.yaml numind-prod'
# smoke test
\`\`\`

## 熔断条件
- 任一 invariant violation_count > 0 → 立即跑 04-rollback.sql
- 02-apply 单 step > 5 分钟 → KILL session + 04-rollback
- 整体 > 15 分钟 → 全部 rollback + 公告"今日切换取消"
```

- [ ] **Step 3: 写实现（已合入 Step 2 的脚本内容）**

无单独实现阶段——脚本本身即实现。

- [ ] **Step 4: 验证**

1. 在 staging 用 prod 脱敏快照跑完整 4 件套：
   - 01-dry-run.sql：Section 5 输出 0 BLOCKER 行；booster %% 600 校验返回 0 行
   - 02-apply.sql：APPLY COMPLETE，backup 行数 = 源表行数
   - 03-verify.sql：所有 8 条 invariant violation_count = 0
   - 04-rollback.sql：rollback 后 staging 回到迁移前状态（重跑 01-dry-run 仍能跑通，新表全空）
2. 抽 10 个用户对照 expected.json 验证 segment 合并正确性
3. 测时长：staging 数据量级 P50 应 < 10 分钟

- [ ] **Step 5: Commit**

```bash
git add numind-server/scripts/2026-04-30-membership-credits-redesign-migration/
git commit -m "feat(migration): add 4-script suite for membership-credits redesign

参考 scripts/2026-04-24-legacy-tier-migration/ 模式落地：
- 01-dry-run.sql: 段合并 CTE + 差额对账（恩泽迁移 post >= pre）
- 02-apply.sql: 单事务写入 5 张新表 + apply_log 反向定位
- 03-verify.sql: 8 条 invariant 立即对账（I1-I8）
- 04-rollback.sql: 基于 apply_log JOIN 删除（T+24h 内可用）
- README.md: 时间预算 P50/P95/P99 + 熔断阈值 + maintenance window runbook

staging 演练已验证 dry-run/apply/verify/rollback 全流程闭环"
```

---

### Task 15: MAINTENANCE_MODE 中间件（含支付回调豁免）

**Files:**

**Create:**
- `numind-server/internal/pkg/middleware/maintenance.go`
- `numind-server/internal/pkg/middleware/maintenance_test.go`

**Modify:**
- `numind-server/internal/numind/server.go` — 注册中间件，**JWT 之前**
- `numind-server/internal/pkg/errno/errno.go` — 新增 `ErrSystemMaintenance` (Code=50301)

**TDD Plan（5 段式）**

- [ ] **Step 1: 写测试（maintenance_test.go）**

参考 spec §10.1.2 行为定义，覆盖 4 个 case：

```go
package middleware

import (
    "net/http"
    "net/http/httptest"
    "os"
    "testing"

    "github.com/gin-gonic/gin"
    "github.com/stretchr/testify/assert"
)

func setupRouter(t *testing.T) *gin.Engine {
    gin.SetMode(gin.TestMode)
    r := gin.New()
    r.Use(MaintenanceMode())
    r.Any("/v1/web/login", func(c *gin.Context) { c.Status(http.StatusOK) })
    r.Any("/v1/sop/run", func(c *gin.Context) { c.Status(http.StatusOK) })
    r.Any("/v1/payment/wechat/notify", func(c *gin.Context) { c.Status(http.StatusOK) })
    r.Any("/v1/payment/alipay/notify", func(c *gin.Context) { c.Status(http.StatusOK) })
    return r
}

func TestMaintenanceMode_Disabled_AllPassThrough(t *testing.T) {
    os.Unsetenv("MAINTENANCE_MODE")
    r := setupRouter(t)

    for _, method := range []string{"GET", "POST", "PUT", "DELETE", "PATCH"} {
        req := httptest.NewRequest(method, "/v1/sop/run", nil)
        w := httptest.NewRecorder()
        r.ServeHTTP(w, req)
        assert.Equal(t, http.StatusOK, w.Code,
            "MAINTENANCE_MODE=false: %s should pass", method)
    }
}

func TestMaintenanceMode_Enabled_GETPassesThrough(t *testing.T) {
    os.Setenv("MAINTENANCE_MODE", "true")
    defer os.Unsetenv("MAINTENANCE_MODE")
    r := setupRouter(t)

    for _, method := range []string{"GET", "HEAD", "OPTIONS"} {
        req := httptest.NewRequest(method, "/v1/sop/run", nil)
        w := httptest.NewRecorder()
        r.ServeHTTP(w, req)
        assert.Equal(t, http.StatusOK, w.Code,
            "MAINTENANCE_MODE=true: %s should pass (read-only)", method)
    }
}

func TestMaintenanceMode_Enabled_POSTReturns503(t *testing.T) {
    os.Setenv("MAINTENANCE_MODE", "true")
    defer os.Unsetenv("MAINTENANCE_MODE")
    r := setupRouter(t)

    for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
        req := httptest.NewRequest(method, "/v1/sop/run", nil)
        w := httptest.NewRecorder()
        r.ServeHTTP(w, req)
        assert.Equal(t, http.StatusServiceUnavailable, w.Code,
            "MAINTENANCE_MODE=true: %s should 503", method)
        assert.Equal(t, "600", w.Header().Get("Retry-After"),
            "Retry-After header must be 600 seconds")
        assert.Contains(t, w.Body.String(), `"code":50301`,
            "response body must include ErrSystemMaintenance code")
    }
}

func TestMaintenanceMode_Enabled_PaymentWebhookExempt(t *testing.T) {
    os.Setenv("MAINTENANCE_MODE", "true")
    defer os.Unsetenv("MAINTENANCE_MODE")
    r := setupRouter(t)

    for _, path := range []string{"/v1/payment/wechat/notify", "/v1/payment/alipay/notify"} {
        req := httptest.NewRequest("POST", path, nil)
        w := httptest.NewRecorder()
        r.ServeHTTP(w, req)
        assert.Equal(t, http.StatusOK, w.Code,
            "MAINTENANCE_MODE=true: payment webhook %s must pass (硬豁免)", path)
    }
}
```

- [ ] **Step 2: 写错误码**

`numind-server/internal/pkg/errno/errno.go` 新增：

```go
// ErrSystemMaintenance 系统维护中（仅在 MAINTENANCE_MODE=true 时由中间件返回）
ErrSystemMaintenance = &Errno{Code: 50301, Message: "系统维护中，请 5-10 分钟后重试"}
```

- [ ] **Step 3: 写实现（maintenance.go）**

按 spec §10.1.2 完整 Go 实现：

```go
// internal/pkg/middleware/maintenance.go
//
// MaintenanceMode 中间件：在 MAINTENANCE_MODE=true 时拦截所有写请求返回 503。
// 设计要点：
// - 注册在 JWT 鉴权之前（写请求即使无 token 也能拦截）
// - GET / HEAD / OPTIONS 放行（健康检查、监控探针、CORS 预检不受影响）
// - 支付回调路径硬豁免（/v1/payment/wechat/notify, /v1/payment/alipay/notify）
// - 写请求返回 503 + Retry-After: 600，body 用统一错误码格式
//
// spec: §10.1 (2026-04-29-membership-credits-redesign-design.md)
package middleware

import (
    "net/http"
    "os"
    "strings"

    "github.com/gin-gonic/gin"

    "numind-server/internal/pkg/errno"
)

// 模块级缓存：进程启动时一次性读取，运行期不重读（环境变量改动需重启 pod 生效，
// 这与 maintenance mode 镜像通过环境变量启用的设计一致）。
var maintenanceEnabled = os.Getenv("MAINTENANCE_MODE") == "true"

// 支付回调路径白名单（spec §10.1.2 硬豁免）
var paymentWebhookPrefixes = []string{
    "/v1/payment/wechat/notify",
    "/v1/payment/alipay/notify",
}

func isPaymentWebhook(path string) bool {
    for _, p := range paymentWebhookPrefixes {
        if strings.HasPrefix(path, p) {
            return true
        }
    }
    return false
}

// MaintenanceMode 返回一个 gin 中间件函数。
func MaintenanceMode() gin.HandlerFunc {
    return func(c *gin.Context) {
        if !maintenanceEnabled {
            c.Next()
            return
        }
        // 支付回调硬豁免（即使是 POST 也直接放行）
        if isPaymentWebhook(c.Request.URL.Path) {
            c.Next()
            return
        }
        // 健康检查 / 监控 / CORS 预检不拦截
        switch c.Request.Method {
        case http.MethodGet, http.MethodHead, http.MethodOptions:
            c.Next()
            return
        }
        // 拒绝其余写请求
        c.Header("Retry-After", "600")
        c.AbortWithStatusJSON(
            http.StatusServiceUnavailable,
            gin.H{
                "code":    errno.ErrSystemMaintenance.Code,
                "message": errno.ErrSystemMaintenance.Message,
                "data":    nil,
            },
        )
    }
}
```

- [ ] **Step 4: 注册中间件**

在 `internal/numind/server.go` 的路由初始化中（参考 spec §10.1.2 注册位置）：

```go
// 在 r.Use(gin.Recovery(), gin.Logger()) 之后、所有 JWT/Auth 中间件之前注册
r.Use(middleware.MaintenanceMode())
```

User router 与 admin router 都需要注册。

- [ ] **Step 5: 验证 + Commit**

```bash
cd numind-server
go test ./internal/pkg/middleware/ -run TestMaintenanceMode -v
# 期望 4 个 case 全 PASS

task lint
# 期望 lint 通过

# 本地手动验证
MAINTENANCE_MODE=true go run ./cmd/numind &
curl -X POST http://localhost:9091/v1/sop/run -i  # 期望 503 + Retry-After
curl -X GET  http://localhost:9091/v1/credits/balance -i  # 期望 401 (放行到下一中间件)
curl -X POST http://localhost:9091/v1/payment/wechat/notify -i  # 期望 200 (回调豁免)
```

```bash
git add numind-server/internal/pkg/middleware/maintenance.go \
        numind-server/internal/pkg/middleware/maintenance_test.go \
        numind-server/internal/pkg/errno/errno.go \
        numind-server/internal/numind/server.go
git commit -m "feat(middleware): add MAINTENANCE_MODE middleware with payment webhook exemption

实现 spec §10.1 maintenance mode 中间件：
- 读取 MAINTENANCE_MODE env 启用拒写
- GET/HEAD/OPTIONS 放行（健康检查、CORS 预检）
- 支付回调路径硬豁免（spec §10.1.2 理由：支付平台不会暂停推送）
- 写请求返回 503 + Retry-After: 600 + ErrSystemMaintenance(50301)
- 注册在 JWT 之前（无 token 也能拦截）"
```

---

### Task 16: Cleanup（删 cron + 老 path 替换）

**Files:**

**Delete:**
- `numind-server/internal/numind/biz/credit/cron_billing.go`（整个文件）

**Modify:**
- `numind-server/internal/numind/server.go` — 移除 cron ticker 注册
- `numind-server/internal/numind/biz/credit/credit.go` — 移除老 `ActivatePending` / `ExpireActive` / `RecalculateBalance` 调用
- `numind-server/internal/numind/biz/sop/sop.go` — `CheckSopPermission` 改用 `HasActiveSubscription || HasActiveTrial` 替代 `user.HasActiveMembership() / CanRunSOP()` 老接口
- `numind-server/internal/numind/biz/payment/payment.go` — sweep 移除 `user.UserTier / TierExpires` 判断分支（Task 11 已部分完成，本 task 兜底）

**Keep（不删，spec §1.4 out-of-scope）：**
- `user.user_tier` / `user.tier_expires` / `user.monthly_sop_runs` 字段（保留只读）
- `credit_package` / `credit_account` 表（保留只读 7 天，T+7d 后单独 feature 评估 DROP）

**TDD Plan（5 段式）**

- [ ] **Step 1: grep 列出所有引用**

```bash
cd numind-server

# 列出所有引用老字段的代码
grep -rn 'user\.UserTier\b' internal/ cmd/ \
  | grep -v '_test.go' | grep -v '/model/' \
  > /tmp/usage-user-tier.txt

grep -rn 'user\.TierExpires\b' internal/ cmd/ \
  | grep -v '_test.go' | grep -v '/model/' \
  > /tmp/usage-tier-expires.txt

grep -rn 'monthly_sop_runs\|MonthlySopRuns' internal/ cmd/ \
  | grep -v '_test.go' | grep -v '/model/' \
  > /tmp/usage-monthly-sop-runs.txt

grep -rn 'HasActiveMembership\|CanRunSOP\|IsOldMember' internal/ cmd/ \
  | grep -v '_test.go' \
  > /tmp/usage-legacy-helpers.txt

grep -rn 'cron_billing\|ActivatePending\|ExpireActive\|RecalculateBalance\|reconcileBillingMode' internal/ cmd/ \
  > /tmp/usage-cron-billing.txt

# 期望：四个 usage-*.txt 中 schema 层（model/）外应均为空（已被前面 task 替换完）
# usage-cron-billing.txt 仅剩 cron_billing.go 自身的定义和 server.go 中的 ticker 注册
```

为这些 grep 结果写一份 cleanup checklist（task 16 验收标准）。

- [ ] **Step 2: 写测试（cleanup 后行为不退化）**

新增 / 检查现有测试覆盖 SOP 权限决策路径不依赖老接口：

```go
// internal/numind/biz/sop/sop_test.go (existing test, sweep)
func TestCheckSopPermission_NewMembershipState(t *testing.T) {
    cases := []struct {
        name           string
        hasSub         bool
        hasTrial       bool
        expectAllowed  bool
    }{
        {"active_sub_only", true, false, true},
        {"active_trial_only", false, true, true},
        {"no_membership", false, false, false},
        {"both_active", true, true, true},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            // ... mock store.HasActiveSubscription / HasActiveTrial
            // ... assert biz.CheckSopPermission 返回符合预期
        })
    }
}
```

测试目标：移除老 `HasActiveMembership()` / `CanRunSOP()` 调用后，SOP 权限决策仍按新口径正确工作。

- [ ] **Step 3: 实施 cleanup**

按以下顺序逐一替换（每步独立可编译）：

1. **删除 cron_billing.go**
   ```bash
   git rm numind-server/internal/numind/biz/credit/cron_billing.go
   ```

2. **server.go 移除 cron 注册**
   - 找到 `go cron_billing.StartCronBilling(ctx)` 或类似调用，删除
   - 找到 `time.NewTicker` 触发 `ActivatePending` / `ExpireActive` 的注册块，整段删除
   - 保留其它非 cron_billing 相关的 ticker（如 Langfuse flush）

3. **credit.go 移除老调用**
   - 在 `internal/numind/biz/credit/credit.go` 中 grep `ActivatePending|ExpireActive|RecalculateBalance`
   - 这些函数本身已在 cron_billing.go 内（Stage 1 已删），credit.go 中如有内联调用也一并删除
   - 新代码使用 lazy-create cycle + reserve/reconcile，不再依赖老激活/过期 cron

4. **sop.go 替换权限检查**
   - 找到 `CheckSopPermission` 中的 `user.HasActiveMembership()` / `user.CanRunSOP()` 调用
   - 替换为 `store.HasActiveSubscription(ctx, userID) || store.HasActiveTrial(ctx, userID)`
   - 移除对 `user.UserTier` / `user.TierExpires` / `user.MonthlySopRuns` 的读写

   示例替换：
   ```go
   // 旧
   if !user.HasActiveMembership() {
       return errno.ErrNoMembership
   }
   if !user.CanRunSOP() {
       return errno.ErrQuotaExhausted
   }

   // 新
   hasSub, err := s.subStore.HasActiveSubscription(ctx, userID)
   if err != nil { return fmt.Errorf("CheckSopPermission: %w", err) }
   hasTrial, err := s.trialStore.HasActiveTrial(ctx, userID)
   if err != nil { return fmt.Errorf("CheckSopPermission: %w", err) }
   if !hasSub && !hasTrial {
       return errno.ErrNoMembership
   }
   // 配额检查由 reserve 阶段（cycle 余额 + booster 余额）承担
   ```

5. **payment.go sweep**
   - grep `UserTier|TierExpires` 在 `biz/payment/payment.go`
   - Task 11 已重构为 `subscription` 续费分支，本 task 删除任何残留的 legacy tier 分支（if user.UserTier == "premium" 等）
   - 保留对 `billing_mode == 'credits'` 的判断（统一口径）

- [ ] **Step 4: 验证**

```bash
cd numind-server

# 4.1 编译通过
go build ./...
# 期望：0 错误

# 4.2 lint 通过
task lint
# 期望：0 issue

# 4.3 单元测试通过
go test ./...
# 期望：所有现有测试 PASS（含 sop_test.go / payment_test.go / credit_test.go）

# 4.4 grep 复验：所有老接口/字段引用已清空（schema 层除外）
grep -rn 'user\.HasActiveMembership\|user\.CanRunSOP\|user\.IsOldMember' internal/numind/biz/ internal/numind/controller/
# 期望：0 行

grep -rn 'cron_billing\|ActivatePending\|ExpireActive' internal/
# 期望：0 行（cron_billing.go 已删，所有 import / 调用都已移除）

grep -rn 'user\.UserTier\b\|user\.TierExpires\b' internal/numind/biz/ internal/numind/controller/
# 期望：0 行（biz/controller 层不再读写老字段；model/ 字段保留作 schema-level only）

# 4.5 启动服务确认无 cron 日志
go run ./cmd/numind &
sleep 5
ps -ef | grep numind | grep -v grep
# 期望：进程在跑；日志中无 "cron_billing started" / "ActivatePending tick" 类输出
```

- [ ] **Step 5: Commit**

```bash
git add -A numind-server/internal/numind/biz/credit/ \
            numind-server/internal/numind/biz/sop/ \
            numind-server/internal/numind/biz/payment/ \
            numind-server/internal/numind/server.go
git commit -m "refactor(credit): remove legacy cron_billing + replace HasActiveMembership/CanRunSOP

迁移到 5 张新表后清理：
- 删除 cron_billing.go（ActivatePending / ExpireActive / RecalculateBalance 全部下线）
- server.go 移除 cron ticker 注册（新代码懒创建 cycle，无需周期任务）
- sop.go: CheckSopPermission 改用 HasActiveSubscription || HasActiveTrial
- payment.go: sweep 移除残留的 user.UserTier / TierExpires 分支

保留（spec §1.4 out-of-scope）：
- user_tier / tier_expires / monthly_sop_runs 字段（只读）
- credit_package / credit_account 表（只读 7 天，T+7d 后再单独 feature DROP）"
```

---

## 三 task 完成验收标准

| Task | 验收 |
|---|---|
| 14 | staging 跑通 dry-run/apply/verify/rollback 全流程；rollback 后 staging 回到迁移前状态；README 时间预算 + 熔断阈值齐备 |
| 15 | 4 个测试 case 全 PASS；本地 curl 验证 503 + 600 + payment 豁免；中间件注册在 JWT 之前 |
| 16 | go build/test/lint 全过；grep 复验老接口 0 引用；启动后无 cron 日志 |

每个 task 独立可 commit、可 review、可 rollback。完成后进入 Phase 5（S5 验证 - gstack /qa 浏览器验证 + 守护期对账脚本部署）。

---

# Tasks 17-23：前端 + 验证策略（Phase 5-7）

> Phase 5：前端用户端（Task 17-20）
> Phase 6：前端管理端（Task 21）
> Phase 7：清理 + S5 验证策略（Task 22-23）

每个 task 17-22 用 TDD 五段式（写测试 → 看测试失败 → 写实现 → 验证通过 → commit）。Task 23 为 NDF Rule 10 强制末尾的 S5 验证策略文档。

---

### Task 17：utils + Pinia store + API 层（用户端 credits / parent）

### Files

- Create:
  - `numind-web-v3/src/utils/idempotency.ts`（UUID v4 生成器）
  - `numind-web-v3/src/utils/datetime.ts`（dayjs UTC+8 封装，对应 spec §8.5.2）
  - `numind-web-v3/src/utils/__tests__/idempotency.spec.ts`
  - `numind-web-v3/src/utils/__tests__/datetime.spec.ts`
  - `numind-web-v3/src/stores/__tests__/credits.spec.ts`
- Modify:
  - `numind-web-v3/src/api/credits.ts`（重写：BalanceDTO 含嵌套 membership_state；getBalance；POST /v1/orders 含 quantity；GET /v1/orders/:id/status）
  - `numind-web-v3/src/api/parent.ts`（grant-membership 支持 Idempotency-Key header）
  - `numind-web-v3/src/stores/credits.ts`（重写为 setup syntax + 派生 getter `displayState`）

- [ ] **Step 1: Test First（Red）**

`numind-web-v3/src/stores/__tests__/credits.spec.ts`：

```typescript
import { setActivePinia, createPinia } from 'pinia'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { useCreditsStore } from '../credits'

vi.mock('@/api/credits', () => ({
  getBalance: vi.fn(),
}))

describe('useCreditsStore.displayState', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('returns "free" when both trial and sub inactive', () => {
    const store = useCreditsStore()
    store.balance = {
      membership_state: { has_active_trial: false, has_active_subscription: false,
        trial_expires_at: null, subscription_expires_at: null },
      trial_remaining: 0, cycle_remaining: 0, booster_total: 0,
      booster_usable: 0, booster_frozen: false, /* ... */
    } as any
    expect(store.displayState).toBe('free')
  })

  it('returns "trial" when trial active (even if sub also active — trial masks pro)', () => {
    const store = useCreditsStore()
    store.balance = {
      membership_state: { has_active_trial: true, has_active_subscription: true,
        trial_expires_at: '2026-05-15T00:00:00+08:00',
        subscription_expires_at: '2026-08-15T00:00:00+08:00' },
    } as any
    expect(store.displayState).toBe('trial')
  })

  it('returns "pro" when only sub active', () => { /* ... */ })
  it('isBoosterFrozen reflects booster_frozen field', () => { /* ... */ })
})
```

`numind-web-v3/src/utils/__tests__/datetime.spec.ts`：

```typescript
import { formatDate, formatDateTime } from '../datetime'

describe('formatDate', () => {
  it('returns em-dash for null/undefined', () => {
    expect(formatDate(null)).toBe('—')
    expect(formatDate(undefined as any)).toBe('—')
  })
  it('renders ISO with TZ as YYYY-MM-DD in UTC+8', () => {
    expect(formatDate('2026-05-15T16:00:00Z')).toBe('2026-05-16') // UTC+8 → 次日
    expect(formatDate('2026-05-15T00:00:00+08:00')).toBe('2026-05-15')
  })
})

describe('formatDateTime', () => {
  it('renders YYYY-MM-DD HH:mm in UTC+8', () => {
    expect(formatDateTime('2026-05-15T08:30:00+08:00')).toBe('2026-05-15 08:30')
  })
})
```

`numind-web-v3/src/utils/__tests__/idempotency.spec.ts`：

```typescript
import { generateIdempotencyKey } from '../idempotency'

describe('generateIdempotencyKey', () => {
  it('returns RFC 4122 v4 UUID', () => {
    const k = generateIdempotencyKey()
    expect(k).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
  })
  it('returns unique values across calls', () => {
    const a = generateIdempotencyKey()
    const b = generateIdempotencyKey()
    expect(a).not.toBe(b)
  })
})
```

运行 `npm run test -- --run`（vitest），确认所有用例 fail（utils / store 尚未实现或为旧实现）。

- [ ] **Step 2: Implement（Green）**

`utils/idempotency.ts`：使用 `crypto.randomUUID()`（Node 19+ / 现代浏览器内置）。

`utils/datetime.ts`：dayjs + utc + timezone 插件，固定 `Asia/Shanghai`，null → `—`。

`api/credits.ts`：BalanceDTO 字段严格按 spec §8.1.1 nested membership_state；新增 `placeOrder({ user_id, product_type, quantity, pay_channel })` 带 `Idempotency-Key` header；新增 `getOrderStatus(orderId)`。

`api/parent.ts`：`grantMembership(childId, body, idempotencyKey)` 把 key 写到 header。

`stores/credits.ts`：setup syntax，state（balance/loading/error）+ getter（isMember/displayState/isBoosterFrozen/trialExpiresAt/proExpiresAt）+ actions（fetchBalance/reset），完全照 spec §8.1.2 复刻；displayState 必须实现"trial 在期一律遮蔽 pro"规则（has_active_trial 优先返回 'trial'）。

- [ ] **Step 3: Verify**

- `npm run test -- --run src/utils src/stores/__tests__/credits.spec.ts` → 全绿
- `npm run lint && npm run type-check` → 0 错误
- 手动用 vue-devtools 在 dev server 检查 fetchBalance() 后 store 状态正确

- [ ] **Step 4: Commit**

```
feat(credits): add idempotency util, datetime util, and credits store

- src/utils/idempotency.ts: RFC 4122 v4 UUID generator (crypto.randomUUID)
- src/utils/datetime.ts: dayjs UTC+8 wrapper; null → em-dash
- src/api/credits.ts: BalanceDTO with nested membership_state; placeOrder; getOrderStatus
- src/api/parent.ts: grantMembership with Idempotency-Key header
- src/stores/credits.ts: setup syntax store; displayState getter masks pro under trial

Refs: spec §8.1, §8.5, design 2026-04-29 membership-credits-redesign
```

---

### Task 18：CreditsView 余额组件 + MembershipBadge

### Files

- Modify: `numind-web-v3/src/views/CreditsView.vue`（整页重写为三卡片布局）
- Create: `numind-web-v3/src/components/MembershipBadge.vue`（free/trial/pro 三态徽章）
- Create: `numind-web-v3/src/components/__tests__/MembershipBadge.spec.ts`
- Create: `numind-web-v3/src/views/__tests__/CreditsView.spec.ts`

- [ ] **Step 1: Test First（Red）**

`MembershipBadge.spec.ts`：

```typescript
import { mount } from '@vue/test-utils'
import MembershipBadge from '../MembershipBadge.vue'

describe('MembershipBadge', () => {
  it('renders gray badge with "免费用户" for free', () => {
    const w = mount(MembershipBadge, { props: { state: 'free' } })
    expect(w.text()).toContain('免费用户')
    expect(w.classes()).toContain('badge--free')  // surface-muted token
  })
  it('renders blue badge with "试用中" for trial', () => {
    const w = mount(MembershipBadge, { props: { state: 'trial' } })
    expect(w.text()).toContain('试用中')
    expect(w.classes()).toContain('badge--trial')
  })
  it('renders gold badge with "Pro 会员" for pro', () => {
    const w = mount(MembershipBadge, { props: { state: 'pro' } })
    expect(w.text()).toContain('Pro 会员')
    expect(w.classes()).toContain('badge--pro')
  })
})
```

`CreditsView.spec.ts`：

```typescript
import { mount, flushPromises } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import CreditsView from '../CreditsView.vue'

it('renders 3-skeleton on loading state', async () => {
  const wrapper = mount(CreditsView, {
    global: { plugins: [createTestingPinia({ initialState: {
      credits: { balance: null, loading: true, error: null }
    }})] }
  })
  expect(wrapper.findAll('[data-test="skeleton-card"]')).toHaveLength(3)
})

it('renders error toast + retry CTA on error state', async () => { /* ... */ })

it('renders booster as locked when booster_frozen=true', async () => {
  const wrapper = mount(CreditsView, {
    global: { plugins: [createTestingPinia({ initialState: {
      credits: { balance: { booster_frozen: true, booster_total: 600,
        membership_state: { has_active_trial: false, has_active_subscription: false }
      } }
    }})] }
  })
  expect(wrapper.find('[data-test="booster-locked-icon"]').exists()).toBe(true)
  expect(wrapper.text()).toContain('需要开通会员后才能使用')
})

it('purchase booster button is disabled when not member', async () => { /* ... */ })
```

运行 vitest → 失败。

- [ ] **Step 2: Implement（Green）**

`MembershipBadge.vue`：props `state: 'free' | 'trial' | 'pro'`；按 spec §8.1.4 渲染颜色 token + 主文案。使用 `@DESIGN.md` 自研徽章样式（surface-muted / accent-info / accent-premium）。

`CreditsView.vue`：

- onMount 调 `creditsStore.fetchBalance()`，loading / error / success 三态
- 卡片 1：MembershipBadge + 副文案到期日（`formatDate(trialExpiresAt)` 或 `formatDate(proExpiresAt)`）
- 卡片 2：trial / cycle / booster 三栏
  - booster 栏 isBoosterFrozen 时：灰数字 + IconLock + 「需要开通会员后才能使用」+「开通会员」CTA
  - 非冻结：渲染 `booster_usable` 数字
- 卡片 3：购买加量包按钮，非会员 hover 提示「开通会员后可购买加量包」+ disabled
- error 状态：toast + 重试按钮

- [ ] **Step 3: Verify**

- `npm run test -- --run src/components/__tests__/MembershipBadge src/views/__tests__/CreditsView` 全绿
- `npm run lint && npm run type-check`
- 启动 dev server 手动验证三状态渲染

- [ ] **Step 4: Commit**

```
feat(credits): rewrite CreditsView with 3-card layout + MembershipBadge

- MembershipBadge: free/trial/pro 3-state badge, design tokens per @DESIGN.md
- CreditsView: 3-card vertical stack, 4-state handling (loading/error/success/empty)
- Booster freeze UI: locked icon + grayed value + "open membership" CTA
- Trial masks pro per spec §8.1.4

Refs: spec §8.1
```

---

### Task 19：BoosterPurchaseDialog 购买弹窗

### Files

- Create:
  - `numind-web-v3/src/components/BoosterPurchaseDialog.vue`
  - `numind-web-v3/src/components/__tests__/BoosterPurchaseDialog.spec.ts`

### Dependency 声明（S3 plan 已保证）

弹窗轮询所需的 `GET /v1/orders/:id/status` 端点**已在 Task 12 合并落地**（响应 `{ order_id, status: 'pending'|'paid'|'failed'|'cancelled', paid_at, amount_cents, product_type, quantity }`，权限 = 受益人 token 或父账户 token）。Task 19 启动前 Task 12 必须 commit，本 task 仅消费已存在端点。Schema 详见 spec §5.8 + §8.2.4。

- [ ] **Step 1: Test First（Red）**

```typescript
import { mount, flushPromises } from '@vue/test-utils'
import BoosterPurchaseDialog from '../BoosterPurchaseDialog.vue'
import { vi } from 'vitest'

vi.mock('@/api/credits', () => ({
  placeOrder: vi.fn(),
  getOrderStatus: vi.fn(),
}))

describe('BoosterPurchaseDialog quantity validation', () => {
  it('1/5/10 quick buttons sync to input + highlight', async () => {
    const w = mount(BoosterPurchaseDialog, { props: { open: true } })
    await w.find('[data-test="qty-quick-5"]').trigger('click')
    expect((w.find('input[type="number"]').element as HTMLInputElement).value).toBe('5')
    expect(w.find('[data-test="qty-quick-5"]').classes()).toContain('quick-btn--active')
  })
  it('shows error + disables submit when quantity > 10000', async () => {
    const w = mount(BoosterPurchaseDialog, { props: { open: true } })
    const input = w.find('input[type="number"]')
    await input.setValue('10001')
    await input.trigger('blur')
    expect(w.text()).toContain('单次最多购买 10000 份')
    expect(w.find('[data-test="submit-btn"]').attributes('disabled')).toBeDefined()
  })
  it('total price shows quantity × ¥29.9 with thousand-separator', async () => {
    const w = mount(BoosterPurchaseDialog, { props: { open: true } })
    await w.find('input[type="number"]').setValue('1000')
    expect(w.text()).toContain('¥29,900.00')
  })
})

describe('BoosterPurchaseDialog submit flow', () => {
  it('calls placeOrder with Idempotency-Key UUID', async () => { /* mock placeOrder, assert called with header */ })
  it('polls getOrderStatus every 2s up to 30s', async () => { /* fake timers */ })
  it('on paid: closes dialog + shows toast + calls fetchBalance', async () => { /* ... */ })
  it('on failed: shows inline error + retry CTA', async () => { /* ... */ })
  it('on 30s timeout: shows "订单处理中" with close btn', async () => { /* ... */ })
})
```

运行 vitest → 失败。

- [ ] **Step 2: Implement（Green）**

弹窗结构（自研 Modal 组件，禁用 Element Plus）：
- 横向 3 个快捷按钮（1 / 5 / 10），点击同步填入 `quantity` ref + 高亮 active
- 自定义 `<input type="number">`，默认值 1
- blur 时验证：`Number.isInteger(quantity) && quantity > 0 && quantity <= 10000`，超 10000 → 红边框 + 行内错误 + disabled submit
- 实时总价 computed：`new Intl.NumberFormat('zh-CN', { style: 'currency', currency: 'CNY' }).format(quantity * 29.9)`
- submit 流程：
  1. 生成 idempotencyKey = `generateIdempotencyKey()`（每次点击新 key）
  2. 调 `placeOrder({ user_id, product_type: 'booster', quantity, pay_channel: 'wechat' }, idempotencyKey)`
  3. 拿到 order_id 后启动轮询：`setInterval(() => getOrderStatus(orderId), 2000)`，30 秒超时（`setTimeout(clearInterval, 30000)`）
  4. paid 路径：清轮询 + emit 'success' + creditsStore.fetchBalance() + toast + 关闭弹窗
  5. failed 路径：显示行内错误 + 「重试」按钮（重试时不重新下单，关闭弹窗让用户重新点开生成新 key）
  6. 30s 未到达 paid/failed：显示「订单处理中，请稍后刷新页面」+「关闭」按钮

- [ ] **Step 3: Verify**

- `npm run test -- --run BoosterPurchaseDialog` 全绿
- `npm run lint && npm run type-check`
- 启动 dev server，手动测：1/5/10 快捷 / 输入 10001 验证 / 输入 1 提交（mock 后端可走通）

- [ ] **Step 4: Commit**

```
feat(credits): add BoosterPurchaseDialog with quantity selection + polling

- Quick-select buttons (1/5/10) + custom number input with blur validation
- Inline error + disabled submit when quantity > 10000
- Real-time total with thousand-separator formatting
- Submit: placeOrder with Idempotency-Key UUID per click
- Poll getOrderStatus every 2s, 30s timeout
- Success: refresh balance + toast; Failure: inline retry; Timeout: "处理中" notice

Refs: spec §8.2
```

---

### Task 20：CustomersView 双状态显示 + GrantMembershipModal

### Files

- Modify: `numind-web-v3/src/views/CustomersView.vue`（列表新增「会员状态」列 + 移除老的 user_tier 列）
- Create: `numind-web-v3/src/components/GrantMembershipModal.vue`
- Create: `numind-web-v3/src/components/__tests__/GrantMembershipModal.spec.ts`
- Create: `numind-web-v3/src/views/__tests__/CustomersView.spec.ts`

### Dependency 声明（S3 plan 已保证）

`GET /v1/users/children` 列表端点**已在 Task 12 合并落地**（响应字段 = `ChildSummaryDTO`，含 nested `membership_state`、`has_used_trial`、`cycle_remaining`，**不含** booster — 与父账户余额视图隐私边界一致）。Task 20 启动前 Task 12 必须 commit，本 task 仅消费已存在端点。Schema 详见 spec §8.3.1。

- [ ] **Step 1: Test First（Red）**

```typescript
describe('CustomersView 会员状态 列', () => {
  it('renders gray "免费用户" for free child (no trial, no sub)', async () => { /* ... */ })
  it('renders blue badge "试用中" for trial-only child', async () => { /* ... */ })
  it('renders dual-badge purple for trial+pro overlap (parent view, US-6)', async () => {
    // membership_state.has_active_trial=true && has_active_subscription=true
    // 验证渲染包含两个 badge: blue "试用中" + gold "Pro 已开通"
  })
  it('renders gold "Pro 会员" for sub-only child', async () => { /* ... */ })
  it('does NOT render booster balance column (privacy boundary §8.3.3)', async () => { /* ... */ })
})

describe('GrantMembershipModal', () => {
  it('disables trial tab when has_used_trial=true with hover hint', async () => { /* ... */ })
  it('Pro tab shows months 1-12 selector with total price calculation', async () => { /* ... */ })
  it('renew hint: "续费延期 N 个月，新到期日 YYYY-MM-DD" when has_active_subscription', async () => { /* ... */ })
  it('submit calls grantMembership with Idempotency-Key UUID', async () => {
    // mock api/parent grantMembership, assert called with key argument
  })
  it('on event_type=trial_granted: toast "已为 X 开通体验包，3 天有效期"', async () => { /* ... */ })
  it('on event_type=sub_granted: toast "已为 X 开通 Pro N 个月，YYYY-MM-DD 到期"', async () => { /* ... */ })
  it('on event_type=sub_renewed: toast "已为 X 续费 Pro N 个月，新到期日 YYYY-MM-DD"', async () => { /* ... */ })
})
```

运行 vitest → 失败。

- [ ] **Step 2: Implement（Green）**

`CustomersView.vue` 新增「会员状态」列（按 spec §8.3.2 4 种渲染）：
- `!mt && !ms` → 灰 "免费用户"
- `mt && !ms` → 蓝 "试用中（YYYY-MM-DD 到期）"
- `mt && ms` → 紫色双标"试用中 + Pro 已开通（试用 X 到期 / Pro Y 到期）"
- `!mt && ms` → 金 "Pro 会员（YYYY-MM-DD 到期）"

每行 action 菜单 → 「开通会员」 → 触发 GrantMembershipModal。

`GrantMembershipModal.vue`：
- 顶部 tab：体验包 / Pro 会员
- 体验包 tab：「赠送 200 积分，3 天有效期，¥0」；`has_used_trial===true` 整 tab 置灰 + hover「该账户已使用过体验包」
- Pro tab：复用 `MonthSelector` 1-12 月；显示金额 `{months} × 标准单价`；`has_active_subscription` 时附「续费延期 N 月，新到期日 YYYY-MM-DD」（用 `anchorAddMonths` 前端可选近似计算或干脆显示「确认开通后查看新到期日」）
- 提交：`generateIdempotencyKey()` → `grantMembership(childId, { product_type, months }, idempotencyKey)`
- 响应根据 `data.event_type` 显示不同 toast（spec §8.3.6 三条文案）
- 提交完成调 `customersStore.fetchChildren()` 刷新

- [ ] **Step 3: Verify**

- `npm run test -- --run CustomersView GrantMembershipModal` 全绿
- `npm run lint && npm run type-check`
- 启动 dev server 手动验证（mock 后端响应不同 event_type）

- [ ] **Step 4: Commit**

```
feat(customers): membership state column + GrantMembershipModal

- CustomersView: 4-state membership rendering (free/trial/pro/trial+pro dual)
- Privacy: no booster column for parent view (§8.3.3)
- GrantMembershipModal: trial tab grays when has_used_trial; Pro tab 1-12 months
- Submit with per-click Idempotency-Key UUID (AC-16a/AC-16b)
- Event-type-driven success toast (trial_granted / sub_granted / sub_renewed)

Refs: spec §8.3
```

---

### Task 21：B2BBillingView 月度账单页 + CSV 导出（numind-admin-web）

### Files

- Create:
  - `numind-admin-web/src/views/B2BBillingView.vue`
  - `numind-admin-web/src/api/b2bBilling.ts`
  - `numind-admin-web/src/utils/datetime.ts`（与 web-v3 同步实现，独立仓库不共享代码）
  - `numind-admin-web/src/utils/__tests__/datetime.spec.ts`
  - `numind-admin-web/src/views/__tests__/B2BBillingView.spec.ts`
- Modify:
  - `numind-admin-web/src/router/index.ts`（注册 `/b2b-billing` 路由 + admin_token 守卫）

- [ ] **Step 1: Test First（Red）**

```typescript
describe('B2BBillingView', () => {
  it('renders MonthPicker default to current month', async () => { /* ... */ })
  it('renders parent-account dropdown filter (none = all)', async () => { /* ... */ })
  it('renders DataTable grouped by parent (admin hard rule §3 #1)', async () => {
    // 验证不是卡片网格，是表格行
  })
  it('shows monthly subtotal per parent group: "本月小计 ¥X (N 笔)"', async () => { /* ... */ })
  it('expanded group shows event detail rows: 日期/子账户/事件类型/产品/月数或数量/金额', async () => { /* ... */ })
  it('translates event_type to Chinese: trial_granted→开通体验，sub_granted→开通 Pro，sub_renewed→续费 Pro，booster_purchased→购买加量包', async () => { /* ... */ })
  it('renders amount as cents → ¥X.XX with thousand-separator', async () => { /* ... */ })
  it('footer summary: 总金额 / 事件数 / 活跃父账户数（从后端 summary 字段读，不前端计算）', async () => { /* ... */ })
  it('CSV export: UTF-8 with BOM + filename b2b-billing-{month}-{granter|all}.csv', async () => {
    // mock window.URL.createObjectURL + a.click
    // assert blob 头 4 字节 = 0xEF 0xBB 0xBF（UTF-8 BOM）
  })
  it('handles 4-state: loading skeleton / empty "本月无B2B事件" / error toast / success', async () => { /* ... */ })
})
```

运行 vitest → 失败。

- [ ] **Step 2: Implement（Green）**

`api/b2bBilling.ts`：

```typescript
export interface B2BEventDTO {
  occurred_at: string  // ISO 8601
  parent_user_id: number
  parent_name: string
  child_user_id: number
  child_name: string
  event_type: 'trial_granted' | 'sub_granted' | 'sub_renewed' | 'booster_purchased'
  product_type: 'trial' | 'monthly' | 'booster'
  months?: number
  quantity?: number
  amount_cents: number
}
export interface B2BBillingReport {
  events: B2BEventDTO[]
  summary: { total_amount_cents: number; event_count: number; distinct_granter_count: number }
}
export const getB2BBillingReport = (month: string, granterId?: number) =>
  request.get<B2BBillingReport>('/v1/admin/b2b-billing-report', { params: { month, granter_user_id: granterId } })
export const downloadB2BBillingCSV = (month: string, granterId?: number) =>
  request.get('/v1/admin/b2b-billing-report.csv', { params: { month, granter_user_id: granterId }, responseType: 'blob' })
export const listParentUsers = () => request.get<Array<{ user_id: number; name: string }>>('/v1/admin/parent-users')
```

`utils/datetime.ts`：与 web-v3 一致，独立 import。

`B2BBillingView.vue`：
- 顶部：MonthPicker + 父账户下拉，watch 触发 fetch
- 主表 DataTable（自研，遵守 admin 端硬规则不用卡片）：按 parent_user_id 分组渲染折叠节点
- 展开后渲染事件明细表：列 日期 / 子账户 / 事件类型（中文映射） / 产品 / 月数或数量 / 金额（cents/100 + 千分位）
- 底部 fixed footer：summary 三项
- 「导出 CSV」按钮：调 `downloadB2BBillingCSV` → blob → 加 UTF-8 BOM 前缀（`new Blob([new Uint8Array([0xEF, 0xBB, 0xBF]), blob])`）→ `URL.createObjectURL` + `<a download>` 触发下载
- 4 状态：loading 骨架表 / empty 中央提示 / error toast + 重试 / success

`router/index.ts`：注册 `/b2b-billing` 路径，admin_token middleware。

- [ ] **Step 3: Verify**

- `npm run test -- --run B2BBillingView` 全绿
- `npm run lint && npm run type-check`
- 启动 admin dev server 手动验证（mock 后端响应）

- [ ] **Step 4: Commit**

```
feat(admin): add B2B monthly billing view with CSV export

- New /b2b-billing route, admin_token guarded
- DataTable layout (admin hard rule), grouped by parent with subtotals
- Event-type Chinese mapping; cents → ¥X.XX with thousand-separator
- Summary footer reads server `summary` field (no client-side aggregation)
- CSV export with UTF-8 BOM for Excel compatibility
- 4-state handling (loading/empty/error/success)

Refs: spec §8.4
```

---

### Task 22：旧 UI 元素移除（基于 grep 校准清单）

### Files

操作流：先 grep 跑实际命中，再逐文件修改。预估命中文件：

- `numind-web-v3/src/views/CustomersView.vue`（如未被 Task 20 全部覆盖）
- `numind-web-v3/src/views/AccountView.vue` / `CreditsView.vue`（如有遗留）
- `numind-web-v3/src/stores/user.ts`
- `numind-admin-web/src/views/UsersView.vue`
- `numind-admin-web/src/views/B2BBillingView.vue`（旧版若存在 → 已被 Task 21 整页替换）
- 任何 i18n 字典文件命中老 tier 文案

- [ ] **Step 1: Test First（Red）**

由于本 task 是删除 + 重构，test-first 体现为「regression test 覆盖关键路径无破坏」：

`numind-web-v3/src/stores/__tests__/user.spec.ts`（如已存在则修改）：

```typescript
it('user store does NOT expose userTier / tierExpires / monthlySopRuns getters', () => {
  const store = useUserStore()
  expect((store as any).userTier).toBeUndefined()
  expect((store as any).tierExpires).toBeUndefined()
  expect((store as any).monthlySopRuns).toBeUndefined()
})

it('legacy tier-checking getters (isTrialUser / isStandardUser) are removed', () => {
  const store = useUserStore()
  expect((store as any).isTrialUser).toBeUndefined()
  expect((store as any).isStandardUser).toBeUndefined()
})
```

`numind-web-v3/src/views/__tests__/CustomersView.spec.ts` 追加：

```typescript
it('does NOT render legacy tier rank or "X / 20 次" run count', async () => {
  // 渲染列表页，断言无 "rank=" / "等级:" / "/ 20 次" 文案
})
```

运行 vitest → 失败（如代码仍有引用）。

- [ ] **Step 2: Implement（Green）**

**Step 1：grep 实际命中清单**

```bash
grep -rn 'user_tier\|monthly_sop_runs\|tier_expires\|userTier\|tierExpires\|monthlySopRuns' \
  numind-web-v3/src numind-admin-web/src
```

将命中行登记为子任务列表，逐文件 review。

**Step 2：每个命中点 review 后处理**

- 业务逻辑判断（如 `if (user.userTier === 'standard')`）→ 改用 `creditsStore.displayState === 'pro'` 或 `creditsStore.isMember`
- UI 渲染（如 `{{ user.userTier }}` 徽章 / `{{ monthlySopRuns }} / 20 次`）→ 整段移除
- store 字段 → 移除字段定义 + getter
- i18n 字典 → 保留中性文案（不移除"免费用户"等通用词），仅移除依赖 user_tier 的渲染逻辑

**Step 3：旧 booster 入口清理**

grep 老 booster 组件名（如 `BuyOldBoosterButton`、`OldBoosterCard`、`LegacyBoosterModal`）：

```bash
grep -rn 'BuyOldBooster\|OldBoosterCard\|LegacyBoosterModal\|LegacyBooster' \
  numind-web-v3/src numind-admin-web/src
```

整体删除（包括组件文件 + import 引用）。

**Step 4：admin tier 升级控件**

`numind-admin-web/src/views/UsersView.vue`：移除 admin 手动设置 user_tier 的下拉控件（spec §8.7.2 不在本次范围）。但保留 admin 查看 subscription 状态的列（改读 `subscription_expires_at`）。

- [ ] **Step 3: Verify**

- 二次 grep 确认无残留：`grep -rn 'user_tier\|userTier\|monthlySopRuns' numind-web-v3/src numind-admin-web/src` → 无命中（或仅留必要的后端只读字段说明注释）
- `npm run test -- --run` 在两个仓库都全绿
- `npm run lint && npm run type-check` 在两个仓库都 0 错误
- 启动 dev server 手动跑通：登录 / 客户管理 / 余额页 / admin 用户列表

- [ ] **Step 4: Commit**

```
chore(legacy): remove user_tier/monthly_sop_runs UI references

- Grep'd numind-web-v3/src + numind-admin-web/src for legacy tier fields
- Replaced tier-based UI logic with creditsStore.displayState
- Removed legacy booster components (90-day variants)
- Removed admin tier-upgrade dropdown (out of scope per §8.7.2)
- Backend retains user_tier as read-only for historical data; UI no longer reads it

Refs: spec §8.7
```

---

### Task 23：S5 验证策略（NDF Rule 10 强制末尾 task）

> **本 task 不写代码，是 S5 阶段的验证策略文档。** 输出应同时落入 `numind-server/docs/superpowers/specs/2026-04-29-membership-credits-redesign-validation-strategy.md`，作为 S5 gate 的输入。

### 验证方式（三件套）

1. **Playwright E2E**（持久化测试代码） - 主回归保护
2. **gstack /qa**（一次性视觉与交互验证 + 截图 baseline 入 git） - 视觉 QA
3. **Go 单元测试 + 并发压测**（持久化 Go test） - 算法 + 竞态防御

### 选择理由（为什么不能省）

会员积分体系是**高风险业务**：

- **支付路径**：booster 购买涉及微信支付回调 + fulfillOrder 5 表写入。spec §1.4 已声明**不实现退款**，任何扣减/续费 bug 一旦上线极难补救。
- **会员等级权限**：trial/pro 状态影响 booster 是否冻结、子账户能否消费 cycle，状态判定错误直接影响计费。
- **B2B 关系**：父账户为子账户开通会员、月度对公结算账单，账目错误直接影响收入。

NDF Rule 10 明确：**支付/权限/会员高风险业务**必须写 Playwright E2E（持久化），不允许仅靠 gstack /qa（一次性，无回归保护）。本次重构 5 条 E2E 路径**不可缩减**。

gstack /qa 的诚实声明：
- gstack /qa 是一次性视觉验证，不产生持久化测试代码
- 截图 baseline 入 git 后，未来视觉回归通过 baseline diff 检测，但不是自动化 test code
- 选择 gstack /qa 意味着该功能未来纯视觉变更时需要手动重新 baseline，不替代 E2E 的逻辑回归保护

### 关键用户路径（6 条，spec §9.4 5 条 + 1 条新增）

#### 路径 1（spec §9.4.1）：父账户开通试用

父账户登录 → 客户管理 → 选 free 子账户 → action 菜单 → 开通会员 → 体验包 → 提交。

**验证点**：
- API：`POST /v1/users/children/:child_id/grant-membership { product_type:"trial" }` → 200
- DB：trial_grant 表新增 1 行（granter_user_id = 父账户 id）
- 前端：列表行变 "试用中（YYYY-MM-DD 到期）"，蓝标
- membership_event 新增 1 条 `trial_granted`，idempotency_key 非空

#### 路径 2（spec §9.4.2）：父账户给同一子账户开 1 月 Pro（trial+pro 叠加）

前置：路径 1 完成。

**验证点**：
- API：`POST /v1/users/children/:child_id/grant-membership { product_type:"monthly", months:1 }` → 200
- DB：subscription 表新增 1 行（first_started_at = current_started_at = now，total_months_purchased = 1，granter_user_id = 父账户）
- 父账户视角列表：紫色双标 "试用中 + Pro 已开通"
- **子账户视角**（登出父账户、切子账户登录）：`/credits` 仅显示 "试用中"（spec §8.1.4 trial 遮蔽 pro）
- membership_event 新增 1 条 `sub_granted`

#### 路径 3（spec §9.4.3）：用户端购买 1 份 booster + mock 支付回调

子账户登录 → `/credits` → 购买加量包 → 数量 1 → 提交 → mock 支付成功 → 等余额刷新。

**mock 支付钩子**：spec §9.4.3 二选一（方案 A `POST /v1/admin/test-only/fulfill-order/:order_id` + build tag gate / 方案 B 环境变量 NUMIND_E2E_BYPASS_PAY_SIG=1 + dev/qa only）。S3 plan 必须明确选项；E2E 用统一 helper `mockPayOrder(orderId)`，未来切方案不改测试代码。

**验证点**：
- API：`POST /v1/orders { product_type:"booster", quantity:1 }` → 200，total_amount_cents = 2990
- DB：booster_balance 行 balance += 600
- 前端：余额卡片 booster 数值 += 600，toast 成功
- membership_event 新增 1 条 `booster_purchased`

#### 路径 4（spec §9.4.4）：用户买 booster 量超 10000，前端拦截 + 后端兜底

**验证点**：
- 输入 10001 → 前端红框 + "单次最多购买 10000 份" + disabled 提交
- Playwright 解除 disabled 强制点击 → 后端返回 400 + `ErrBoosterQuantityExceedsLimit`
- order 表无新订单，余额无变化

#### 路径 5（spec §9.4.5）：会员到期边界 booster 自动冻结 UI

fixture：sub.expires_at = now，booster_balance.balance > 0。

**验证点**：
- `/credits` 余额卡 booster 数字保留原值 + 灰色 + 锁标 + "需要开通会员后才能使用"
- 行内 CTA 「立即开通会员」点击 → 跳转联系父账户提示页（C 端不可自购）
- 购买加量包卡片置灰
- 后端兜底：`POST /v1/orders booster` → 返回 `ErrNotActiveMember`

#### 路径 6（新增，覆盖 AC-16）：父账户两个 tab 同时续费同一子账户

**目的**：验证 idempotency 矩阵——不同 key 累加 / 相同 key 去重。

**步骤**：

子用例 6a（不同 key，AC-16a）：
- 父账户在 tab A 和 tab B 同时打开同一子账户的 GrantMembershipModal
- 两 tab 各自点击「确认开通」（前端各自 `generateIdempotencyKey()` → 两个不同 UUID）
- 验证：subscription.expires_at 累加 2 个月（1+1），total_months_purchased = 2，membership_event 2 条 sub_renewed

子用例 6b（相同 key，AC-16b）：
- 用 Playwright 拦截两次请求强制使用同一个 Idempotency-Key
- 验证：subscription.expires_at 仅累加 1 个月，membership_event 1 条 sub_renewed（UNIQUE 约束去重）

### S5 执行清单（NDF Gate）

- [ ] **后端**：
  - [ ] `task lint` 通过
  - [ ] `go test ./...` 通过（轻量）
  - [ ] `task test` 通过（含 race detection + coverage）；biz 层覆盖率 ≥ 80%，store 层 ≥ 70%
  - [ ] 4 条并发压测用例（spec §9.2，docker-compose MySQL）全过 + 60s 超时哨兵无死锁
- [ ] **前端**：
  - [ ] numind-web-v3 `npm run lint && npm run type-check` 0 错误
  - [ ] numind-admin-web `npm run lint && npm run type-check` 0 错误
  - [ ] 两个仓库 `npm run test -- --run`（vitest 单元测试）全过
- [ ] **E2E**：
  - [ ] `cd numind-web-v3 && E2E_USERNAME=$E2E_USERNAME E2E_PASSWORD=$E2E_PASSWORD npm run test:e2e -- membership-credits-redesign.spec.ts` 退出码 0
  - [ ] 6 条路径全过（spec §9.4.1-9.4.5 + 路径 6 新增）
  - [ ] 失败时 `e2e/helpers/diagnose.ts` `createDiagnostics` 抓 console + network + screenshot 上传 CI artifact
- [ ] **gstack /qa 浏览器 QA**：
  - [ ] 6 个关键页面（spec §9.5.2 清单）截图与 baseline 一致或差异已人工批准
  - [ ] 截图 baseline 提交到 `numind-web-v3/e2e/baselines/membership-redesign/`
- [ ] **迁移演练**：
  - [ ] dry-run（prod 快照灌 staging）：所有用户 diff = 0，TOTAL diff = 0
  - [ ] apply（staging 全量）：耗时 ≤ 5 分钟，verify 全过
  - [ ] rollback（staging 演练）：耗时 ≤ 2 分钟，老代码 git checkout pre-migration-tag 能启动并跑通 1 次 SOP 扣分
  - [ ] 三份报告归档至 `docs/migration-runbook/`
- [ ] **可观测性回归**：
  - [ ] 跑 1 次 SOP（fixture 用户）→ Langfuse trace 结构无退化
  - [ ] generation 数 ≥ baseline，usage 字段不缺
  - [ ] DeductCredits span 时间戳在 LLM generation 事务外（OUT-OF-tx，spec R4）
  - [ ] 异常路径（余额不足）：trace.level = ERROR + span output 含 error 字段

### 任一项不通过 → S5 阻塞

按 `.claude/rules/ndf-enforcement.md` 规则 6/7：
- P0/P1 立即修复后重新验收
- P2 能现修则现修，不允许"以后再说"
- 修复完毕回到 S4 → 重跑两阶段 review → 重新进 S5

### S5 通过后产出

- 验证报告 `docs/superpowers/specs/2026-04-29-membership-credits-redesign-s5-report.md`，含：
  - 各执行项结果（PASS / FAIL + 输出摘要）
  - 6 条 E2E 路径截图证据
  - 迁移 dry-run / apply / rollback 三份报告
  - Langfuse trace 对比 JSON dump
- 更新 `numind-server/build-manifest.yaml`：`stage: S6`，`progress.s5_passed: true`

---

> Tasks 17-23 完。Phase 5-7 总计 7 个 task，覆盖前端用户端（17-20）、前端管理端（21）、清理（22）、S5 策略（23）。
