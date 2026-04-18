# Credits System 技术设计

> NDF S2 技术设计文档（feature: `credits-system`）
> Created: 2026-04-18
> 输入：`numind-server/requirements/credits-system.md` + `numind-server/proposals/credits-system-proposal.md`
> Status: **DRAFT — Sections 1-2 已完成并经独立 Opus reviewer 迭代 3 轮 review；Sections 3-5 进行中**

---

## §0 Spec Overview

本 feature 是"积分计费系统"的完善：从阶梯次数制（free/trial/standard/premium）迁移到"会员 + 积分 + 加量包"体系。积分基础设施 80% 已在 prod（`credit_account`/`credit_package`/`credit_transaction`/`pricing_rule` 四表及 `RechargeWithOrderTx` + `DeductCredits` FIFO 扣减 + Admin 后台 UI），本 feature 聚焦**真实缺口**：

1. `billing_mode` 字段引入双制并存（legacy_tier 老会员 + credits 新制）
2. R2 字符数估算 + Reserve/Reconcile 两阶段扣减（替代当前固定预估值）
3. SalesRAG Chat 接入扣减（prod 漏洞修复）
4. Booster 加量包购买的会员资格门槛
5. 前端积分 UI（账户卡、SOP 运行前预估条、加量包购买入口、积分不足弹窗）
6. 卡片生成死代码清理（顺手做）

### 预锁定决策（见 `build-manifest.yaml` credits-system.decisions）

- **D1-D6**：2000 积分/月额度、R2 估算路线、¥29.9/600 积分/3 月加量包、会员优先扣减、Grandfathering、加量包仅会员可购
- **P4a-e**：legacy_tier 不可购 booster、卡片是死代码顺手清理、加量包过期 lazy + daily cron、trial 纳入 Grandfathering（Option E）、legacy_tier 下 SalesRAG 免费
- **Approach B 架构**：CreditService 抽象接口 + 线性 R2 估算 + Eager Reserve + FIFO by `expires_at` 天然满足优先级 + append-only coefficient version + SalesRAG 新 operation + Booster 复用 `/v1/orders`

---

## §1 架构：`ICreditService` + `billing_mode` 双制

### 1.1 核心抽象

```go
package credit

// Singleton，wire.go 注入（与现有 ICreditBiz 同风格）
type ICreditService interface {
    // 运行前检查；调用方据 SkipDeduction 决定是否进入扣减块
    CheckAndEstimate(ctx context.Context, user *model.User, op Operation, in EstimationInput) (*PreCheckResult, error)

    // 预扣（Eager：同事务 DeductCredits FIFO + 写 credit_reservation + credit_reservation_item）
    // legacy_tier 调用此方法会 panic("unreachable: legacy_tier must be guarded by SkipDeduction")
    Reserve(ctx context.Context, user *model.User, op Operation, estimated int64, coefID uint64) (*Reservation, error)

    // 对账（幂等：终态 reservation 返回 ErrAlreadyFinalized sentinel）
    Reconcile(ctx context.Context, reservationID uint64, actualCostCents int64) error

    // 退还（幂等，同上）
    Refund(ctx context.Context, reservationID uint64, reason string) error

    // 唯一出口：成功 Reconcile / 失败 Refund / 未执行 Refund，由 defer 调用
    FinalizeReservation(ctx context.Context, rsv *Reservation, actualCostCents *int64, opErr *error) error

    // 余额查询（按 billing_mode 分发，返回统一 BalanceBreakdown）
    GetBalance(ctx context.Context, user *model.User) (*BalanceBreakdown, error)
}
```

### 1.2 单一实现 + 内部按 billing_mode 分发

```go
type creditService struct {
    store store.IStore
    biz   ICreditBiz   // 复用现有 DeductCredits / GetQuotaBreakdown
}

func NewCreditService(ds store.IStore, biz ICreditBiz) ICreditService {
    return &creditService{store: ds, biz: biz}
}

func (s *creditService) CheckAndEstimate(ctx, user, op, in) (*PreCheckResult, error) {
    if user.BillingMode == model.BillingModeLegacyTier {
        return s.legacyCheckAndEstimate(ctx, user, op)
    }
    return s.creditsCheckAndEstimate(ctx, user, op, in)
}
// Reserve / Reconcile / Refund / FinalizeReservation / GetBalance 同模式
```

### 1.3 legacy_tier 策略（Option E Grandfathering）

| 方法 | legacyTier 行为 |
|------|---------------|
| `CheckAndEstimate` | 调 `user.CanRunSOP()`，返回 `{SkipDeduction: true, EstimatedCredits: 0}` |
| `Reserve` / `Reconcile` / `Refund` | `panic("unreachable: legacy_tier")`（调用方必须用 SkipDeduction 守卫） |
| `GetBalance` | 返回 `{BillingMode: legacy_tier, RemainingRuns: user.GetRemainingSOPRuns(), MonthlyLimit: 20 或 nil(premium)}`——**不查 credit_package** |

### 1.4 credits 策略（新制）

| 方法 | credits 行为 |
|------|-------------|
| `CheckAndEstimate` | 按 R2 公式计算预估 + 查余额；不足返回 `ErrInsufficientCredits` |
| `Reserve` | 同事务：`DeductCredits` FIFO 按 `expires_at` ASC 扣减 credit_package + 写入 credit_reservation + credit_reservation_item（seq=1..N） |
| `Reconcile` | 读 reservation → 比对 actual 与 reserved → 差额补扣/退还（原路退到 item.package_id）；状态 `reserved → reconciled` |
| `Refund` | 标记 `reserved → refunded`，按 item.seq ASC 原路退还 |
| `FinalizeReservation` | 唯一出口：opErr 非 nil → Refund；actualCost 已采集 → Reconcile；否则 Refund with reason=no_actual_cost |
| `GetBalance` | 查 credit_package 返回 `{BillingMode: credits, SubscriptionTotal/Remaining, BoosterTotal/Remaining/EarliestExpires, ...}` |

### 1.5 `billing_mode` 读取规则

- **热路径决策**（CheckAndEstimate/Reserve/Reconcile）：`user` 对象必须是**本次调用前 fresh load** 的（`store.User().GetByID`），避免长 SOP run 中途用户状态变化
- **冷路径展示**（前端 balance API、预检 UI）：可用 HTTP middleware 注入的 context 值
- **cron / 后台 job**：直接 load user，无 HTTP context 依赖

### 1.6 调用方标准模板

```go
// 适用于 biz/sop/sop.go 和 biz/salesrag/salesrag.go，保留现有 helper 签名不变，
// helper 内部从调 DeductCredits 切换到以下流程：

user, err := b.store.User().GetByID(ctx, userID)
if err != nil { return err }

pre, err := b.creditSvc.CheckAndEstimate(ctx, user, credit.OpSopRun, credit.EstimationInput{
    PromptChars: len(promptText),
    Model:       modelName,
    Provider:    providerName,
})
if err != nil { return err }  // 含 ErrInsufficientCredits typed error

var rsv *credit.Reservation
if !pre.SkipDeduction {
    rsv, err = b.creditSvc.Reserve(ctx, user, credit.OpSopRun, pre.EstimatedCredits, pre.CoefficientID)
    if err != nil { return err }
}

var actualCost int64
var opErr error
defer b.creditSvc.FinalizeReservation(ctx, rsv, &actualCost, &opErr)  // 唯一出口

// LLM 调用...
actualCost, opErr = runLLM(...)
```

**现有 `deductCreditsForSop`（sop.go:1825）兼容策略**：保留 helper 签名，内部切换到上述流程；`sop.go:722, 1402` 调用点零改动。现有 `CanPerformAIOperation` 保留一个 release 内部转调 `CheckAndEstimate`，后续废弃。

### 1.7 Operation 枚举（单次 LLM 调用级）

```go
type Operation string
const (
    OpSopRun          Operation = "sop_run"
    OpSopChat         Operation = "sop_chat"
    OpSalesragChat    Operation = "salesrag_chat"   // 本 feature 新增
    OpProfileAnalysis Operation = "profile_analysis"
    OpFileParse       Operation = "file_parse"
    OpStyleAnalysis   Operation = "style_analysis"
    OpOCR             Operation = "ocr"
)
```

粒度说明：一次 SOP run 如含 N 个 node，每个 node 是一次 LLM 调用，触发 N 轮 Reserve/Reconcile。

### 1.8 关键结构体

```go
type EstimationInput struct {
    PromptChars int
    Model       string
    Provider    string
}

type PreCheckResult struct {
    SkipDeduction    bool       // legacy_tier = true
    Sufficient       bool       // credits 模式下余额是否足够
    EstimatedCredits int64
    CoefficientID    uint64     // 外键 credit_estimation_coefficient.id
    Balance          BalanceBreakdown
}

type Reservation struct {
    ID               uint64
    UserID           uint
    ReferenceType    string
    ReferenceID      string
    Operation        Operation
    ReservedCredits  int64
    CoefficientID    uint64
    Status           ReservationStatus
    ActualCostCents  *int64
    Delta            *int64
    FinalizeReason   *string
    IdempotencyKey   *string
    Items            []ReservationItem  // FIFO 扣减明细
    CreatedAt        time.Time
    ReconciledAt     *time.Time
}

type ReservationItem struct {
    PackageID        uint64
    Credits          int64
    PackageType      string
    PackageExpiresAt time.Time
    Seq              int  // FIFO 顺序号
}

type BalanceBreakdown struct {
    BillingMode string  // "credits" | "legacy_tier"
    // credits 字段
    SubscriptionRemaining    int64
    SubscriptionTotal        int64
    SubscriptionExpiresAt    *time.Time
    BoosterRemaining         int64
    BoosterTotal             int64
    BoosterEarliestExpiresAt *time.Time
    // legacy_tier 字段
    RemainingRuns *int  // nil 表示 premium unlimited
    MonthlyLimit  *int
}
```

### 1.9 错误语义（typed sentinels）

```go
var (
    ErrInsufficientCredits   = errors.New("credit: insufficient balance")
    ErrAlreadyFinalized      = errors.New("credit: reservation already finalized")
    ErrReservationNotFound   = errors.New("credit: reservation not found")
    ErrCoefficientNotFound   = errors.New("credit: estimation coefficient not found for model")
    ErrCoefficientConcurrent = errors.New("credit: coefficient update concurrent conflict, retry exhausted")
)
```

调用方用 `errors.Is(err, credit.ErrInsufficientCredits)` 区分业务错误 vs 真错误。`ErrAlreadyFinalized` 是"无操作"信号，调用方应忽略。

### 1.10 并发正确性保证

- **Reserve 事务**：复用现有 `GetActivePackagesForUpdate` 的 `SELECT ... FOR UPDATE` 行锁，保证 concurrent Reserve 串行化
- **Reconcile/Refund 事务**：`SELECT ... FOR UPDATE` on `credit_reservation.status`，原子切换状态
- **CheckAndEstimate → Reserve 间 TOCTOU race**：Reserve 失败返回 `ErrInsufficientCredits`，前端据此降级到"余额刚好不够，购买加量包"弹窗

---

## §2 数据模型 DDL + Migration 策略

### 2.1 变更一览

- **修改 1 张现有表**：`user` 新增 `billing_mode` 字段
- **新增 3 张表**：`credit_estimation_coefficient`、`credit_reservation`、`credit_reservation_item`
- **Migration 12 个文件**（6 对 DDL + rollback）
- **复用现有表**：`credit_account` / `credit_package` / `credit_transaction` / `pricing_rule` / `user_billing` / `billing_record`（皆在 prod，不动）

### 2.2 `user` 表新增 `billing_mode`

```sql
-- migrations/20260419_100000_add_billing_mode_to_user.sql
ALTER TABLE `user`
    ADD COLUMN billing_mode ENUM('legacy_tier', 'credits')
    NOT NULL DEFAULT 'credits'
    COMMENT 'legacy_tier=旧次数制老会员（Grandfathering）；credits=新积分制'
    AFTER tier_expires;

CREATE INDEX idx_user_billing_mode ON `user`(billing_mode, tier_expires)
    COMMENT '复合索引：billing_mode 分布 + cron 扫 legacy_tier 到期用户';
```

```sql
-- migrations/20260419_100000_add_billing_mode_to_user_rollback.sql
DROP INDEX idx_user_billing_mode ON `user`;
ALTER TABLE `user` DROP COLUMN billing_mode;
```

### 2.3 `credit_estimation_coefficient` 表

```sql
-- migrations/20260419_100100_create_credit_estimation_coefficient.sql
CREATE TABLE IF NOT EXISTS credit_estimation_coefficient (
    id                       BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    provider                 VARCHAR(50)   NOT NULL COMMENT 'ali/volc/dmxapi/baidu',
    model                    VARCHAR(100)  NOT NULL,
    operation                VARCHAR(50)   NOT NULL COMMENT '见 §1.7 枚举',
    char_to_token_ratio      DECIMAL(6,3)  NOT NULL COMMENT '1 汉字 ≈ N token',
    completion_prompt_ratio  DECIMAL(6,3)  NOT NULL COMMENT 'completion/prompt 历史均值',
    safety_buffer_pct        DECIMAL(5,3)  NOT NULL DEFAULT 0.200 COMMENT '安全余量，0.200=+20%',
    version                  INT UNSIGNED  NOT NULL COMMENT '(provider,model,operation) 维度递增',
    is_active                TINYINT(1)    NOT NULL DEFAULT 0 COMMENT '1=启用；同一 key 至多一行为 1',
    change_reason            VARCHAR(255)  DEFAULT NULL,
    updated_by               VARCHAR(64)   DEFAULT NULL,
    created_at               DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at               DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),

    UNIQUE KEY uk_provider_model_op_version (provider, model, operation, version),
    KEY idx_active_lookup (provider, model, operation, is_active),
    KEY idx_version_lookup (provider, model, operation, version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
  COMMENT='R2 估算系数（append-only：修改=insert 新 version，老 version 保留对账历史 reservation）';
```

```sql
-- migrations/20260419_100100_create_credit_estimation_coefficient_rollback.sql
DROP TABLE IF EXISTS credit_estimation_coefficient;
```

### 2.4 `credit_reservation` 表

```sql
-- migrations/20260419_100200_create_credit_reservation.sql
CREATE TABLE IF NOT EXISTS credit_reservation (
    id                  BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id             INT UNSIGNED  NOT NULL,
    reference_type      VARCHAR(50)   NOT NULL COMMENT 'sop_run/sop_chat/salesrag_chat',
    reference_id        VARCHAR(100)  NOT NULL,
    operation           VARCHAR(50)   NOT NULL,
    reserved_credits    BIGINT        NOT NULL,
    coefficient_id      BIGINT UNSIGNED NOT NULL COMMENT '应用层保证 FK 到 credit_estimation_coefficient.id',
    status              ENUM('reserved','reconciled','refunded','expired') NOT NULL DEFAULT 'reserved',
    actual_cost_cents   BIGINT        DEFAULT NULL,
    delta               BIGINT        DEFAULT NULL COMMENT 'actual - reserved',
    finalize_reason     ENUM('normal','op_failed','user_cancelled','provider_timeout',
                             'no_actual_cost','expired_by_cron','manual_refund')
                        DEFAULT NULL,
    idempotency_key     VARCHAR(64)   DEFAULT NULL COMMENT '防重试重复创建；允许 NULL（退化为非幂等）',
    reconciled_at       DATETIME(3)   DEFAULT NULL,
    created_at          DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at          DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),

    KEY idx_user_status (user_id, status, created_at),
    KEY idx_status_created (status, created_at) COMMENT 'cron 扫 24h 未 reconcile',
    UNIQUE KEY uk_idempotency_key (idempotency_key),
    KEY idx_coefficient (coefficient_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
  COMMENT='积分预扣记录（Reserve 写入，Reconcile/Refund/Expired 切换终态）';
```

### 2.5 `credit_reservation_item` 表

```sql
-- migrations/20260419_100300_create_credit_reservation_item.sql
CREATE TABLE IF NOT EXISTS credit_reservation_item (
    id                 BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    reservation_id     BIGINT UNSIGNED NOT NULL COMMENT 'FK credit_reservation.id（应用层保证）',
    package_id         BIGINT UNSIGNED NOT NULL COMMENT 'FK credit_package.id（应用层保证）',
    credits            BIGINT          NOT NULL COMMENT '从此 package 扣的积分',
    package_type       VARCHAR(20)     NOT NULL COMMENT 'trial/subscription/booster 扣减时快照',
    package_expires_at DATETIME(3)     NOT NULL COMMENT '扣减时 package.expires_at 快照',
    seq                INT             NOT NULL COMMENT 'FIFO 扣减顺序号（1,2,...），非 INSERT 顺序',
    created_at         DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),

    KEY idx_reservation (reservation_id),
    KEY idx_package (package_id, created_at) COMMENT '供查"某 package 被多少 reservation 冻结"',
    UNIQUE KEY uk_reservation_seq (reservation_id, seq)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
  COMMENT='预扣明细（一个 Reservation 按 FIFO 可能扣多个 Package）';
```

### 2.6 Seed 数据（占位，待 S3 数据 spike 填充真值）

```sql
-- migrations/20260419_100400_seed_credit_estimation_coefficient.sql
-- S3 plan Task 1 的数据 spike 产出后填充。Spike SQL 模板：
--   SELECT provider, model, operation,
--          AVG(completion_tokens * 1.0 / NULLIF(prompt_tokens, 0)) AS avg_ratio,
--          STDDEV_POP(completion_tokens * 1.0 / NULLIF(prompt_tokens, 0)) AS std_ratio,
--          COUNT(*) AS sample_size
--   FROM usage_record
--   WHERE created_at > DATE_SUB(NOW(), INTERVAL 90 DAY)
--     AND prompt_tokens > 0 AND completion_tokens > 0
--   GROUP BY provider, model, operation
--   HAVING COUNT(*) >= 30;
-- Sample < 30 组合用保守默认 (1.500, 0.500, 0.300)。

INSERT INTO credit_estimation_coefficient
    (provider, model, operation, char_to_token_ratio, completion_prompt_ratio, safety_buffer_pct, version, is_active, change_reason, updated_by)
VALUES
    ('ali',    'qwen-turbo',              'sop_run',       1.500, 0.500, 0.200, 1, 1, 'initial from S3 spike', 'system'),
    ('ali',    'qwen-plus',               'sop_run',       1.500, 0.450, 0.200, 1, 1, 'initial from S3 spike', 'system'),
    ('volc',   'deepseek-v3-2-251201',    'sop_run',       1.500, 0.400, 0.200, 1, 1, 'initial from S3 spike', 'system'),
    ('volc',   'glm-4-7-251222',          'sop_run',       1.500, 0.400, 0.200, 1, 1, 'initial from S3 spike', 'system'),
    ('ali',    'qwen-turbo',              'sop_chat',      1.500, 0.300, 0.200, 1, 1, 'initial from S3 spike', 'system'),
    ('ali',    'qwen-turbo',              'salesrag_chat', 1.500, 0.600, 0.250, 1, 1, 'initial from S3 spike', 'system'),
    ('dmxapi', 'qwen-turbo-latest',       'salesrag_chat', 1.500, 0.600, 0.250, 1, 1, 'initial from S3 spike', 'system')
;
```

### 2.7 一次性数据迁移（envsubst 占位符）

```sql
-- migrations/20260419_100500_init_billing_mode_values.sql
-- 部署前：export MIGRATION_CUTOFF="2026-05-08 00:00:00"
-- 执行方式：envsubst '${MIGRATION_CUTOFF}' < file.sql | mysql ...

-- 步骤 1：迁移前分布
SELECT tier,
       CASE
           WHEN tier_expires IS NULL THEN 'no_expires'
           WHEN tier_expires > '${MIGRATION_CUTOFF}' THEN 'in_period'
           ELSE 'expired'
       END AS period_status,
       COUNT(*) AS user_count
FROM `user`
GROUP BY tier, period_status;

-- 步骤 2：在期 standard/premium/trial → legacy_tier（Option E Grandfathering）
UPDATE `user`
SET billing_mode = 'legacy_tier'
WHERE tier IN ('standard', 'premium', 'trial')
  AND tier_expires IS NOT NULL
  AND tier_expires > '${MIGRATION_CUTOFF}'
  AND billing_mode = 'credits';          -- 幂等，不覆盖人工调整

-- 步骤 3：迁移后 sanity check
SELECT billing_mode, tier, COUNT(*) AS user_count
FROM `user`
GROUP BY billing_mode, tier
ORDER BY billing_mode, tier;
```

**NULL tier_expires**：按"脏数据/已过期"处理，保留 credits 默认（可由管理员手工调整）。
**@migration_cutoff**：envsubst 白名单模式（`envsubst '${MIGRATION_CUTOFF}'`）避免误伤 SQL 其他 `$` 字符。

### 2.8 Migration 文件组织（12 个）

```
migrations/
  20260419_100000_add_billing_mode_to_user.sql / *_rollback.sql
  20260419_100100_create_credit_estimation_coefficient.sql / *_rollback.sql
  20260419_100200_create_credit_reservation.sql / *_rollback.sql
  20260419_100300_create_credit_reservation_item.sql / *_rollback.sql
  20260419_100400_seed_credit_estimation_coefficient.sql / *_rollback.sql
  20260419_100500_init_billing_mode_values.sql / *_rollback.sql
```

### 2.9 GORM Model 定义

```go
// internal/pkg/model/user.go（修改现有）
type User struct {
    // ... 现有字段保持不变 ...
    BillingMode string `gorm:"column:billing_mode;type:enum('legacy_tier','credits');not null;default:'credits'" json:"billing_mode"`
}

const (
    BillingModeLegacyTier = "legacy_tier"
    BillingModeCredits    = "credits"
)

// internal/pkg/model/credit_coefficient.go（新增）
type CreditEstimationCoefficient struct {
    ID                    uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
    Provider              string    `gorm:"size:50;not null;uniqueIndex:uk_provider_model_op_version,priority:1" json:"provider"`
    Model                 string    `gorm:"size:100;not null;uniqueIndex:uk_provider_model_op_version,priority:2" json:"model"`
    Operation             string    `gorm:"size:50;not null;uniqueIndex:uk_provider_model_op_version,priority:3" json:"operation"`
    CharToTokenRatio      float64   `gorm:"type:decimal(6,3);not null" json:"char_to_token_ratio"`
    CompletionPromptRatio float64   `gorm:"type:decimal(6,3);not null" json:"completion_prompt_ratio"`
    SafetyBufferPct       float64   `gorm:"type:decimal(5,3);not null;default:0.200" json:"safety_buffer_pct"`
    Version               uint      `gorm:"not null;uniqueIndex:uk_provider_model_op_version,priority:4" json:"version"`
    IsActive              bool      `gorm:"not null;default:false" json:"is_active"`
    ChangeReason          string    `gorm:"size:255" json:"change_reason,omitempty"`
    UpdatedBy             string    `gorm:"size:64" json:"updated_by,omitempty"`
    CreatedAt             time.Time `gorm:"autoCreateTime:milli" json:"created_at"`
    UpdatedAt             time.Time `gorm:"autoUpdateTime:milli" json:"updated_at"`
}
func (CreditEstimationCoefficient) TableName() string { return "credit_estimation_coefficient" }

// internal/pkg/model/credit_reservation.go（新增）
type CreditReservation struct {
    ID                uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
    UserID            uint       `gorm:"not null;index:idx_user_status,priority:1" json:"user_id"`
    ReferenceType     string     `gorm:"size:50;not null" json:"reference_type"`
    ReferenceID       string     `gorm:"size:100;not null" json:"reference_id"`
    Operation         string     `gorm:"size:50;not null" json:"operation"`
    ReservedCredits   int64      `gorm:"not null" json:"reserved_credits"`
    CoefficientID     uint64     `gorm:"not null;index:idx_coefficient" json:"coefficient_id"`
    Status            string     `gorm:"type:enum('reserved','reconciled','refunded','expired');not null;default:'reserved';index:idx_user_status,priority:2;index:idx_status_created,priority:1" json:"status"`
    ActualCostCents   *int64     `json:"actual_cost_cents,omitempty"`
    Delta             *int64     `json:"delta,omitempty"`
    FinalizeReason    *string    `gorm:"type:enum('normal','op_failed','user_cancelled','provider_timeout','no_actual_cost','expired_by_cron','manual_refund')" json:"finalize_reason,omitempty"`
    IdempotencyKey    *string    `gorm:"size:64;uniqueIndex:uk_idempotency_key" json:"idempotency_key,omitempty"`
    ReconciledAt      *time.Time `json:"reconciled_at,omitempty"`
    CreatedAt         time.Time  `gorm:"autoCreateTime:milli;index:idx_user_status,priority:3;index:idx_status_created,priority:2" json:"created_at"`
    UpdatedAt         time.Time  `gorm:"autoUpdateTime:milli" json:"updated_at"`

    Items []CreditReservationItem `gorm:"foreignKey:ReservationID" json:"items,omitempty"`
}
func (CreditReservation) TableName() string { return "credit_reservation" }

type CreditReservationItem struct {
    ID               uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
    ReservationID    uint64    `gorm:"not null;index:idx_reservation" json:"reservation_id"`
    PackageID        uint64    `gorm:"not null;index:idx_package,priority:1" json:"package_id"`
    Credits          int64     `gorm:"not null" json:"credits"`
    PackageType      string    `gorm:"size:20;not null" json:"package_type"`
    PackageExpiresAt time.Time `gorm:"not null" json:"package_expires_at"`
    Seq              int       `gorm:"not null;uniqueIndex:uk_reservation_seq,priority:2" json:"seq"`
    CreatedAt        time.Time `gorm:"autoCreateTime:milli;index:idx_package,priority:2" json:"created_at"`
}
func (CreditReservationItem) TableName() string { return "credit_reservation_item" }
```

### 2.10 运维注意

**legacy_tier 用户管理端展示：** 在 Admin `CreditUsersView.vue` 用户详情顶部加 banner："此用户 billing_mode=legacy_tier（Grandfathering 老会员）。其 credit_package 自然过期不扣减，到期升级后进入积分制。"

**Cron 行为对 legacy_tier 用户（已知，可接受）：** 现有 `RunCronTasks` 仍激活其 pending→active credit_package。legacy_tier 用户的这些 active pkg 永不被扣（策略在 biz/credit 层隔离），积分"冻结"到 tier 到期自然清零——内部 bookkeeping 浪费但用户无感。后续可延后优化（cron 加 `if user.BillingMode==legacy_tier { continue }`）。

### 2.11 并发与兼容性边界

#### 2.11.1 `GetBalance` / `QuotaBreakdown` 按 billing_mode 分发

**扩展现有 `QuotaBreakdown` 结构**（非破坏性，新增字段，老调用方无感）：

```go
type QuotaBreakdown struct {
    Balance               int64 `json:"balance"`
    SubscriptionTotal     int64 `json:"subscription_total"`
    SubscriptionRemaining int64 `json:"subscription_remaining"`
    BoosterTotal          int64 `json:"booster_total"`
    BoosterRemaining      int64 `json:"booster_remaining"`
    // v3 新增字段
    BillingMode    string `json:"billing_mode"`              // "credits" | "legacy_tier"
    RemainingRuns  *int   `json:"remaining_runs,omitempty"`  // 仅 legacy_tier；nil=无限
    MonthlyLimit   *int   `json:"monthly_limit,omitempty"`   // 仅 legacy_tier
}
```

**老接口保持不变**：`ICreditBiz.GetBalance(ctx, userID) (int64, error)` 不改，继续返回 credit_account.balance。

**新 API `ICreditService.GetBalance(user) *BalanceBreakdown`**（见 §1.8 BalanceBreakdown 结构）：
- `creditsImpl`：查 credit_package → 填 subscription/booster 字段
- `legacyTierImpl`：调 `user.GetRemainingSOPRuns()`（既有方法，返回 int，-1=premium 无限）→ 填 RemainingRuns/MonthlyLimit，**不查 credit_package**

**controller 层**：改造 `GetQuotaBreakdown` endpoint 调 `ICreditService.GetBalance` 并映射到扩展后的 `QuotaBreakdown` 结构返回前端。

#### 2.11.2 FK 约束策略

新增 3 张表**不使用 MySQL FOREIGN KEY**。遵循项目惯例（`credit_package.user_id` / `credit_transaction.user_id` / `credit_package.order_id` 皆无 FK，见 `add_credits_system.sql`）。引用完整性由 `biz/credit` 应用层保证。理由：降低 DDL 变更成本 + 避免并发锁争用。

#### 2.11.3 `idempotency_key` 生成约定

| Operation | key 格式 | 示例 |
|-----------|---------|------|
| sop_run | `sop_run:{run_id}:{node_id}` | `sop_run:12345:node_2` |
| sop_chat | `sop_chat:{run_id}:{msg_id}` | `sop_chat:12345:msg_99` |
| salesrag_chat | `salesrag_chat:{session_id}:{request_uuid}` | `salesrag_chat:sess_7:req_abc123` |
| 其他 | `{op}:{resource_id}:{YYYYMMDD}` | `file_parse:doc_33:20260419` |

**salesrag_chat 的 `request_uuid`** 由 controller 层 middleware 在请求入口生成（或前端传入 `X-Request-ID` header），在 Reserve 调用前确定。**不用 msg_id**（msg_id 在回复写库后才生成，Reserve 前不可用）。

无法构造稳定 key 时传 nil；InnoDB UNIQUE 允许多 NULL 共存，退化为非幂等。

#### 2.11.4 `credit_reservation_item.seq` 语义

`seq` 是 **FIFO 扣减顺序号**（1, 2, ...），非 INSERT 顺序：
- Reserve 时按 `ORDER BY credit_package.expires_at ASC` 分配 seq
- Refund 时按 `ORDER BY seq ASC` 遍历 item，原路退还
- `UNIQUE (reservation_id, seq)` 保证顺序唯一

#### 2.11.5 `operation` VARCHAR + CHECK 约束

`operation` 用 VARCHAR(50) 理由：将来新增 operation 不需 DDL 修改。如 prod MySQL ≥ 8.0.16（部署前确认），附加 CHECK 约束做弱枚举校验：

```sql
ALTER TABLE credit_estimation_coefficient
    ADD CONSTRAINT chk_coef_operation
    CHECK (operation IN ('sop_run','sop_chat','salesrag_chat','profile_analysis','file_parse','style_analysis','ocr'));
ALTER TABLE credit_reservation
    ADD CONSTRAINT chk_rsv_operation
    CHECK (operation IN ('sop_run','sop_chat','salesrag_chat','profile_analysis','file_parse','style_analysis','ocr'));
```

**如 prod MySQL < 8.0.16**：跳过 CHECK 约束，改为代码层 `credit.ValidateOperation(op)` 在 Reserve/CheckAndEstimate 入口验证。新增 operation 时同步更新代码常量。

#### 2.11.6 Append-only coefficient 修改的事务 + 锁 + retry

```go
package credit

const (
    mysqlErrDuplicateKey  = 1062
    coefficientMaxRetries = 3
)

// UpdateCoefficient 原子更新系数
// 并发安全：SELECT...FOR UPDATE 锁最大 version 行 + duplicate key retry 覆盖"首次插入"竞态
func (b *creditBiz) UpdateCoefficient(ctx context.Context, req UpdateCoefficientReq) error {
    var lastErr error
    for attempt := 0; attempt < coefficientMaxRetries; attempt++ {
        err := b.store.DB().Transaction(func(tx *gorm.DB) error {
            var maxRow model.CreditEstimationCoefficient
            result := tx.WithContext(ctx).
                Clauses(clause.Locking{Strength: "UPDATE"}).
                Where("provider = ? AND model = ? AND operation = ?",
                    req.Provider, req.Model, req.Operation).
                Order("version DESC").Limit(1).
                First(&maxRow)

            nextVersion := uint(1)
            if result.Error == nil {
                nextVersion = maxRow.Version + 1
                if err := tx.Model(&model.CreditEstimationCoefficient{}).
                    Where("provider = ? AND model = ? AND operation = ? AND is_active = 1",
                        req.Provider, req.Model, req.Operation).
                    Update("is_active", false).Error; err != nil {
                    return err
                }
            } else if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
                return result.Error
            }
            // 首次插入场景（ErrRecordNotFound）：SELECT FOR UPDATE 无法预锁 phantom，
            // 依赖 uk_provider_model_op_version 触发 duplicate key，外层 retry

            return tx.Create(&model.CreditEstimationCoefficient{
                Provider: req.Provider, Model: req.Model, Operation: req.Operation,
                CharToTokenRatio:      req.CharToTokenRatio,
                CompletionPromptRatio: req.CompletionPromptRatio,
                SafetyBufferPct:       req.SafetyBufferPct,
                Version:               nextVersion, IsActive: true,
                ChangeReason: req.ChangeReason, UpdatedBy: req.UpdatedBy,
            }).Error
        })

        if err == nil { return nil }
        lastErr = err

        var mysqlErr *mysql.MySQLError
        if errors.As(err, &mysqlErr) && mysqlErr.Number == mysqlErrDuplicateKey && attempt < coefficientMaxRetries-1 {
            time.Sleep(time.Millisecond * time.Duration(50<<attempt)) // 50ms, 100ms, 200ms
            continue
        }
        return err
    }
    return fmt.Errorf("%w: after %d attempts: %v", ErrCoefficientConcurrent, coefficientMaxRetries, lastErr)
}
```

**失败 UX：** 3 次 retry 失败返回 `ErrCoefficientConcurrent`；admin controller 映射为 HTTP 503；前端显示 "系数更新繁忙，请稍后重试" toast。

**依赖 schema**：§2.3 的 `UNIQUE KEY uk_provider_model_op_version (provider, model, operation, version)` 必须存在，retry 逻辑依赖此约束兜底首次插入竞态。

#### 2.11.7 `@migration_cutoff` 部署机制

```bash
# deploy/deploy-credits-system.sh
export MIGRATION_CUTOFF=$(date -u '+%Y-%m-%d %H:%M:%S')
envsubst '${MIGRATION_CUTOFF}' < migrations/20260419_100500_init_billing_mode_values.sql | mysql -u... -p... numind
```

白名单模式（`'${MIGRATION_CUTOFF}'`）限定只替换该变量，避免误伤 SQL 里其他 `$` 字符（若有）。

#### 2.11.8 架构依赖 cross-reference

| 文档位置 | 依赖项 | 用途 |
|---------|-------|------|
| §2.11.6 UpdateCoefficient retry 逻辑 | §2.3 `uk_provider_model_op_version` UNIQUE 约束 | retry 依赖此约束触发 duplicate key 检测 |
| §2.11.1 GetBalance 分发 | §1.8 `BalanceBreakdown` 结构 | creditsImpl vs legacyTierImpl 返回值类型 |
| §2.11.3 idempotency_key | §2.4 `uk_idempotency_key` UNIQUE 索引 | 幂等性由 DB 约束保证 |
| §2.11.4 seq | §2.5 `uk_reservation_seq` UNIQUE 约束 | FIFO 顺序唯一性 |

---

## §3 核心 biz API + 时序图（v2 FINAL，Direction 3）

> Section 3 经 Opus round 1 FAIL（5 P0）后按 **Direction 3 同步 pricing 计算**方向重写。核心决策：cost 计算从 recorder 解耦为独立 `pricing` 模块，调用方 LLM 返回后**同步**计算 actualCost，保留"用多少付多少"UX 承诺。工作量从 12-15 天修正为 **14-18 天**。

### 3.0 actualCost 采集契约（v2 新增，解决 P0-1）

#### 3.0.1 问题陈述

现有 `UsageRecorder` 是 channel 异步批量 flusher（`recorder.go:220` buildRecord + flushBatch）。LLM 调用返回时 cost 未算出、未落库。defer FinalizeReservation 拿不到 actualCost。

#### 3.0.2 决策：cost 计算解耦为同步 pure 函数

新建 `internal/pkg/pricing/` 包，提取 `recorder.calculateCostAndRevenue` 的核心逻辑为公共函数。Recorder 保持异步批量持久化职责，不再做 cost 计算。

```go
// internal/pkg/pricing/pricing.go (新建)
package pricing

type ICalculator interface {
    CalculateCost(ctx context.Context, serviceType, provider, model string,
                  promptTokens, completionTokens int) (costCents int64, err error)
}

type calculator struct {
    store store.IPricingStore
    cache *lru.Cache[string, *model.PricingRule]  // key = serviceType+provider+model, 5min TTL
}

func NewCalculator(ds store.IStore) ICalculator {
    return &calculator{store: ds.Pricing(), cache: lru.New(500)}
}

func (c *calculator) CalculateCost(ctx, serviceType, provider, model string, promptTokens, completionTokens int) (int64, error) {
    rule, err := c.resolvePricingRuleCached(ctx, serviceType, provider, model)
    if err != nil { return 0, err }
    yuan := float64(promptTokens)/1_000_000*rule.InputPricePerMTok +
            float64(completionTokens)/1_000_000*rule.OutputPricePerMTok
    return int64(math.Round(yuan * 100)), nil
}
```

#### 3.0.3 Recorder 改造（最小）

`recorder.buildRecord` 改调 `pricing.CalculateCost`（单一数据源），recorder.go 内部不再维护 cost 计算。两处调用路径（recorder 异步入库 + biz/sop 同步拿值）共享同一公式。

#### 3.0.4 Pricing 缓存策略

`pricing_rule` 表是运营低频改动。用 in-process LRU 缓存（500 条容量，5 分钟 TTL）。LLM 调用前先查缓存命中率预期 99%+。缓存失效策略：被动 TTL 过期；管理端修改规则时通过 `pubsub.Publish("pricing_rule_changed")` 主动失效（现有 Redis pubsub 基础设施可用）。

### 3.1 控制流反转影响范围（承认 P0-2）

**诚实声明：** 这是调用链重写，不是签名调整。

**重写的函数：**
| 函数 | 改动性质 |
|------|---------|
| `biz/sop/sop.go runNode()` | 重写主循环：Reserve 前置 + LLM 调用 + 同步 pricing.CalculateCost + defer Reconcile |
| `biz/sop/sop.go runChat()` | 同上（operation=sop_chat） |
| `biz/salesrag/salesrag.go Chat()` | 新建完整 Reserve/Reconcile 包装（当前零扣减） |
| `biz/salesrag/salesrag.go ChatStream()` | 新建完整包装 + stream drain 后 defer 触发 |
| `biz/sop/sop.go deductCreditsForSop()` | 废弃（旧 fire-and-forget 风格语义变化，保留同名但内部重写） |
| `biz/credit/credit.go CanPerformAIOperation()` | 1 release 过渡，内部转调 CheckAndEstimate，之后删 |

### 3.2 场景 A：credits 正常 SOP 流程（v2）

```
┌────────────┐    ┌────────────┐    ┌──────────┐    ┌──────────────┐    ┌─────────────┐
│  前端      │    │ controller │    │ biz/sop  │    │ biz/credit   │    │ pricing     │
└─────┬──────┘    └─────┬──────┘    └────┬─────┘    └──────┬───────┘    └──────┬──────┘
      │ POST /v1/credits/estimate (operation, reference_id)│                    │
      ├────────────────────►│                    │                  │                    │
      │                     │ 后端自己渲染 prompt 估算字符数               │                    │
      │                     ├───────────────►│                  │                    │
      │                     │                │ CheckAndEstimate │                    │
      │                     │                ├─────────────────►│                    │
      │                     │                │                  │ 查 coefficient + balance
      │                     │                │                  │ 返回 PreCheckResult │
      │                     │◄───────────────┤◄─────────────────┤                    │
      │ {estimated: 180, sufficient, balance, coefficient_id, reason?} │             │
      │◄────────────────────┤                │                  │                    │
      │ 用户确认"开始运行"   │                │                  │                    │
      │ POST /v1/sop/runs   │                │                  │                    │
      ├────────────────────►│                │                  │                    │
      │                     │ runNode loop   │                  │                    │
      │                     ├───────────────►│                  │                    │
      │                     │                │ 1. CheckAndEstimate (per-node)│       │
      │                     │                │ 2. Reserve (idempKey=sop_run:{run_id}:{node_id})│
      │                     │                │    TX: DeductCreditsTx + INSERT rsv + items
      │                     │                │ 3. defer FinalizeReservation │        │
      │                     │                │ 4. LLM 调用 (existing Langfuse trace) │
      │                     │                │ 5. pricing.CalculateCost(serviceType, provider, model, pt, ct)│
      │                     │                ├──────────────────────────────────────►│
      │                     │                │                                       │ 查缓存/pricing_rule
      │                     │                │◄──────────────────────────────────────┤
      │                     │                │    actualCost = 150 cents             │
      │                     │                │ 6. defer 触发 Reconcile(rsv, 150) │    │
      │                     │                │    TX: 读 rsv+items，delta=150-180=-30 → 按 seq ASC 退还│
      │                     │                │    UPDATE rsv SET status='reconciled'│
      │                     │◄───────────────┤                                       │
      │◄────────────────────┤ run result + 实际消耗 150 积分             │           │
```

#### 代码骨架（新 runNode 核心段）

```go
// biz/sop/sop.go
func (b *sopBiz) runNode(ctx context.Context, run *model.SopRun, node *model.SopNode, user *model.User) (err error) {
    // 1. 渲染 prompt（后端）+ 预估
    promptText := b.renderNodePrompt(ctx, run, node)
    pre, err := b.creditSvc.CheckAndEstimate(ctx, user, credit.OpSopRun, credit.EstimationInput{
        PromptChars: utf8.RuneCountInString(promptText),
        Model:       node.Model,
        Provider:    node.Provider,
    })
    if err != nil {
        // ErrInsufficientCredits 或 legacy_tier 次数用尽 → wrap with pre.Reason（P0-4）
        return b.wrapCreditError(err, pre)
    }

    // 2. Reserve（legacy_tier 跳过）
    var rsv *credit.Reservation
    if !pre.SkipDeduction {
        idempKey := fmt.Sprintf("sop_run:%d:%d", run.ID, node.ID)  // P0-3: caller 传稳定 key
        rsv, err = b.creditSvc.Reserve(ctx, user, credit.OpSopRun,
            pre.EstimatedCredits, pre.CoefficientID, &idempKey)
        if err != nil { return err }
    }

    // 3. defer Finalize（nil-safe for legacy_tier）
    var actualCost int64
    var opErr error
    defer func() {
        if rsv != nil {
            _ = b.creditSvc.FinalizeReservation(ctx, rsv, &actualCost, &opErr)
        }
    }()

    // 4. LLM 调用（现有代码，带 Langfuse trace）
    resp, llmErr := b.llmAdapter.Chat(ctx, node.Model, promptText, ...)
    if llmErr != nil {
        opErr = llmErr  // defer 触发 Refund
        return llmErr
    }

    // 5. 同步计算 cost（Direction 3）
    if rsv != nil {
        cost, err := b.pricing.CalculateCost(ctx, "llm_chat", resp.Provider, resp.Model,
            resp.PromptTokens, resp.CompletionTokens)
        if err != nil {
            // pricing 失败不阻塞业务，走 Refund（no_actual_cost）
            opErr = fmt.Errorf("pricing calc: %w", err)
        } else {
            actualCost = cost  // defer 触发 Reconcile
        }
    }

    // 6. legacy_tier 仍需 MonthlySopRuns++
    if pre.SkipDeduction {
        _ = b.store.User().IncrementMonthlySopRuns(ctx, user.ID)
    }

    // ... 节点后续处理 ...
    return nil
}
```

### 3.3 场景 B：SOP 失败/取消 → Refund

`opErr != nil` → defer FinalizeReservation 走 Refund：
- LLM 调用失败（provider_timeout 等）
- 用户 context.Cancelled（前端断开）
- pricing 计算失败

`FinalizeReservation` 内部逻辑：
```go
func (s *creditService) FinalizeReservation(ctx, rsv *Reservation, actualCost *int64, opErr *error) error {
    if rsv == nil { return nil }
    if *opErr != nil {
        return s.Refund(ctx, rsv.ID, classifyReason(*opErr))
    }
    if actualCost == nil || *actualCost == 0 {
        return s.Refund(ctx, rsv.ID, "no_actual_cost")  // 兜底防 pricing 失败误扣
    }
    return s.Reconcile(ctx, rsv.ID, *actualCost)
}

func classifyReason(err error) string {
    if errors.Is(err, context.Canceled) { return "user_cancelled" }
    if errors.Is(err, context.DeadlineExceeded) { return "provider_timeout" }
    return "op_failed"
}
```

Refund 按 `item.seq ASC` 遍历，原路退还到 `item.package_id`。如该 package 已过期：视为无操作（§2 边界表已说明）。

### 3.4 场景 C：SalesRAG Chat 接入（prod 漏洞修复）

```go
// biz/salesrag/salesrag.go
func (b *salesragBiz) Chat(ctx context.Context, req ChatReq) (resp *ChatResp, err error) {
    user, err := b.store.User().GetByID(ctx, req.UserID)
    if err != nil { return nil, err }

    // RAG 召回先做（估算 prompt 含召回上下文）
    ragCtx, err := b.retriever.Retrieve(ctx, req.SessionID, req.Message)
    if err != nil { return nil, err }

    promptChars := utf8.RuneCountInString(req.Message) + utf8.RuneCountInString(ragCtx)
    pre, err := b.creditSvc.CheckAndEstimate(ctx, user, credit.OpSalesragChat, credit.EstimationInput{
        PromptChars: promptChars,
        Model:       b.defaultModel,
        Provider:    b.defaultProvider,
    })
    if err != nil { return nil, b.wrapCreditError(err, pre) }

    var rsv *credit.Reservation
    if !pre.SkipDeduction {
        requestUUID := getRequestUUIDFromCtx(ctx)  // middleware 注入的 X-Request-ID
        idempKey := fmt.Sprintf("salesrag_chat:%s:%s", req.SessionID, requestUUID)
        rsv, err = b.creditSvc.Reserve(ctx, user, credit.OpSalesragChat,
            pre.EstimatedCredits, pre.CoefficientID, &idempKey)
        if err != nil { return nil, err }
    }

    var actualCost int64
    var opErr error
    defer func() {
        if rsv != nil {
            _ = b.creditSvc.FinalizeReservation(ctx, rsv, &actualCost, &opErr)
        }
    }()

    resp, err = b.callLLM(ctx, req, ragCtx)
    if err != nil { opErr = err; return nil, err }

    if rsv != nil {
        actualCost, _ = b.pricing.CalculateCost(ctx, "llm_chat",
            resp.Provider, resp.Model, resp.PromptTokens, resp.CompletionTokens)
    }
    return resp, nil
}
```

### 3.5 场景 D：ChatStream 流式特殊处理（P1-3 修正）

```go
func (b *salesragBiz) ChatStream(ctx context.Context, req ChatReq, ch chan<- Event) (err error) {
    user, err := b.store.User().GetByID(ctx, req.UserID)
    if err != nil { return err }

    ragCtx, err := b.retriever.Retrieve(ctx, req.SessionID, req.Message)
    if err != nil { return err }

    pre, err := b.creditSvc.CheckAndEstimate(ctx, user, credit.OpSalesragChat, credit.EstimationInput{
        PromptChars: utf8.RuneCountInString(req.Message) + utf8.RuneCountInString(ragCtx),
        Model:       b.defaultModel,
        Provider:    b.defaultProvider,
    })
    if err != nil { return b.wrapCreditError(err, pre) }

    var rsv *credit.Reservation
    if !pre.SkipDeduction {
        idempKey := fmt.Sprintf("salesrag_chat:%s:%s", req.SessionID, getRequestUUIDFromCtx(ctx))
        rsv, err = b.creditSvc.Reserve(ctx, user, credit.OpSalesragChat,
            pre.EstimatedCredits, pre.CoefficientID, &idempKey)
        if err != nil { return err }
    }

    var actualCost int64
    var opErr error
    defer func() {
        if rsv != nil {
            _ = b.creditSvc.FinalizeReservation(ctx, rsv, &actualCost, &opErr)
        }
    }()

    // Stream drain with token accumulator
    var promptTokens, completionTokens int
    streamErr := b.callLLMStream(ctx, req, ragCtx, func(chunk *StreamChunk) {
        // chunk.PromptTokens 通常在首 chunk 给出，completionTokens 累加
        if chunk.PromptTokens > 0 { promptTokens = chunk.PromptTokens }
        completionTokens += chunk.CompletionTokens
        ch <- Event{Type: "chunk", Data: chunk.Content}
    })

    // context cancelled (client disconnect) / 或 stream 错误 → Refund
    if streamErr != nil {
        opErr = streamErr  // 含 context.Canceled 分类
        return streamErr
    }

    // 正常 drain 完成，同步计算 cost
    if rsv != nil && promptTokens > 0 {
        actualCost, _ = b.pricing.CalculateCost(ctx, "llm_chat",
            b.defaultProvider, b.defaultModel, promptTokens, completionTokens)
    }
    return nil
}
```

**关键：** defer 在 handler 函数返回时触发——无论 stream 是 drain 完成还是 client 中途断开（context.Canceled），都能走到 defer。drain 未完成时 `actualCost=0 → no_actual_cost` Refund（保守退还）。

### 3.6 场景 E：legacy_tier SOP + reason 传递（P0-4 修正）

```go
// internal/numind/biz/credit/credit_service.go
type PreCheckResult struct {
    SkipDeduction    bool
    Sufficient       bool
    EstimatedCredits int64
    CoefficientID    uint64
    Balance          BalanceBreakdown
    Reason           string  // P0-4: legacy_tier 次数不足时填入 CanRunSOP() 中文原因，前端直接展示
}

func (s *creditService) legacyCheckAndEstimate(ctx, user *model.User, op Operation) (*PreCheckResult, error) {
    canRun, reason := user.CanRunSOP()  // 现有方法 user.go:89
    if !canRun {
        return &PreCheckResult{
            SkipDeduction: true,
            Sufficient:    false,
            Reason:        reason,   // 如 "体验会员运行次数已达上限"
            Balance:       s.buildLegacyBalance(user),
        }, fmt.Errorf("%w: %s", ErrInsufficientCredits, reason)
    }
    return &PreCheckResult{
        SkipDeduction: true,
        Sufficient:    true,
        Balance:       s.buildLegacyBalance(user),
    }, nil
}

// biz 层统一 wrap helper
func (b *sopBiz) wrapCreditError(err error, pre *credit.PreCheckResult) error {
    if pre != nil && pre.Reason != "" && errors.Is(err, credit.ErrInsufficientCredits) {
        return errno.ErrInsufficientCredits.SetMessage(pre.Reason)  // 前端看到中文原因
    }
    return err
}
```

### 3.7 场景 F：Booster 购买会员门槛（P0-5 修正：复用 HasActiveSubscription）

```go
// biz/payment/payment.go CreateOrder 新增 Booster case（现有 switch 无此 case）
case model.ProductTypeBooster:
    // P0-5: 复用现有 creditStore.HasActiveSubscription（不新建 HasActiveSubscriptionPackage）
    hasActive, err := b.creditStore.HasActiveSubscription(ctx, userID)
    if err != nil { return nil, err }
    if !hasActive {
        return nil, errno.ErrMembershipRequired
    }

    user, err := b.store.User().GetByID(ctx, userID)
    if err != nil { return nil, err }
    if user.BillingMode == model.BillingModeLegacyTier {
        return nil, errno.ErrBoosterNotAvailableForLegacy
    }

    // ... 现有订单创建逻辑（生成订单号、发起支付）不变 ...
```

### 3.8 billing_mode 切换时机（P1-4 修正：独立短事务）

不在 `RechargeWithOrderTx` 的订单事务内切换（避免 User 锁争用导致订单回滚）。由 `biz/order.OnPaymentSuccess` webhook 处理流程分两步：

```go
// biz/order/order.go
func (b *orderBiz) OnPaymentSuccess(ctx context.Context, orderID uint64) error {
    // 1. 现有：RechargeWithOrderTx 发放 credit_package
    if err := b.creditBiz.RechargeWithOrderTx(ctx, orderID, ...); err != nil {
        return err  // 订单处理失败，整个回滚
    }

    // 2. 独立短事务切换 billing_mode（幂等；失败不影响订单结果）
    if err := b.switchBillingModeIfLegacy(ctx, userID); err != nil {
        logger.Warn("switch billing_mode failed; cron fallback will retry",
            "user_id", userID, "order_id", orderID, "err", err)
    }
    return nil
}

func (b *orderBiz) switchBillingModeIfLegacy(ctx context.Context, userID uint) error {
    return b.store.DB().WithContext(ctx).
        Model(&model.User{}).
        Where("id = ? AND billing_mode = ?", userID, model.BillingModeLegacyTier).
        Update("billing_mode", model.BillingModeCredits).Error
}
```

**兜底 cron**（避免切换失败导致用户永远留在 legacy_tier）：在 `biz/credit/RunCronTasks` 内新增 daily job，扫描 `billing_mode='legacy_tier' AND 存在 active subscription credit_package` 的用户，批量切换。

### 3.9 防提前续费（P1-5 展开）

```go
// biz/payment/payment.go CreateOrder 内在所有 tier 相关分支前置校验
case model.ProductTypeMonthly, model.ProductTypeYearly:
    user, _ := b.store.User().GetByID(ctx, userID)
    if user.Tier != model.TierFree && user.TierExpires != nil && user.TierExpires.After(time.Now()) {
        targetRank := tierRank(req.ProductType)  // monthly → standard=2 / yearly → standard=2 / premium_yearly → premium=3
        currentRank := tierRank(user.Tier)
        if targetRank <= currentRank {
            return nil, errno.ErrTierInPeriod  // 在期不允许同类或降级购买
        }
        // 允许升级（如 standard → premium）
    }

case model.ProductTypeTrial:
    if b.creditStore.HasTrialPackage(ctx, userID) {  // 现有方法
        return nil, errno.ErrTrialAlreadyPurchased
    }
    user, _ := b.store.User().GetByID(ctx, userID)
    if user.Tier != model.TierFree && user.TierExpires != nil && user.TierExpires.After(time.Now()) {
        return nil, errno.ErrTrialNotAvailableInPeriod  // 在期会员不能"降级购买 trial"
    }
```

`tierRank`/`isEqualOrLowerTier` helper 放 `model/user.go`（和现有 tier 定义同位置）。

### 3.10 Reserve 事务嵌套契约（P1-6 修正）

**不嵌套事务。** 改造现有 `DeductCredits` 为支持外部 tx 传入：

```go
// biz/credit/credit.go 现有：
func (b *creditBiz) DeductCredits(ctx, userID, credits int64, reason string) error {
    return b.ds.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
        return b.deductCreditsTx(ctx, tx, userID, credits, reason)
    })
}

// 新增 public 方法供 Reserve 调用
func (b *creditBiz) DeductCreditsTx(ctx context.Context, tx *gorm.DB, userID uint, credits int64, reason string) (items []credit.PackageDeduction, err error) {
    // 原 Transaction 内部逻辑提取到这里
    // 额外返回 items 列表供 Reserve 写入 credit_reservation_item
}
```

Reserve 实装：

```go
func (s *creditService) creditsReserve(ctx, user, op, estimated, coefID, idempKey) (*Reservation, error) {
    return s.store.DB().Transaction(func(tx *gorm.DB) error {
        // 1. DeductCreditsTx（返回 FIFO 扣减的 items）
        items, err := s.biz.DeductCreditsTx(ctx, tx, user.ID, estimated, "reserve:"+string(op))
        if err != nil { return nil, err }  // ErrInsufficientCredits

        // 2. INSERT credit_reservation
        rsv := &model.CreditReservation{...}
        if err := tx.Create(rsv).Error; err != nil { return nil, err }

        // 3. INSERT credit_reservation_item × len(items)
        for i, item := range items {
            _ = tx.Create(&model.CreditReservationItem{
                ReservationID: rsv.ID,
                PackageID:     item.PackageID,
                Credits:       item.Credits,
                PackageType:   item.PackageType,
                PackageExpiresAt: item.ExpiresAt,
                Seq:           i + 1,  // FIFO 顺序号
            }).Error
        }

        return rsv, nil
    })
}
```

### 3.11 HTTP Controller 契约（P1-2 修正：后端渲染 prompt）

#### 新增：`POST /v1/credits/estimate`

```go
type EstimateReq struct {
    Operation   string `json:"operation" binding:"required"`
    ReferenceID string `json:"reference_id" binding:"required"`  // sop_template_id / session_id / node_id
    // 不收 prompt_chars：由后端根据 operation+reference_id 自己渲染
}

type EstimateResp struct {
    EstimatedCredits int64                   `json:"estimated_credits"`
    Sufficient       bool                    `json:"sufficient"`
    SkipDeduction    bool                    `json:"skip_deduction"`
    Reason           string                  `json:"reason,omitempty"`  // legacy_tier 次数不足原因
    Balance          credit.BalanceBreakdown `json:"balance"`
    CoefficientID    uint64                  `json:"coefficient_id"`
}

func (ctl *CreditController) Estimate(c *gin.Context) {
    var req EstimateReq
    if err := c.ShouldBindJSON(&req); err != nil {
        core.WriteResponse(c, errno.ErrBind.SetMessage(err.Error()), nil); return
    }
    userID := c.GetUint("userID")
    user, _ := ctl.userStore.GetByID(c, userID)

    // 后端渲染 prompt 字符数
    promptChars, model, provider, err := ctl.promptEstimator.Estimate(c, req.Operation, req.ReferenceID)
    if err != nil {
        core.WriteResponse(c, err, nil); return
    }

    pre, err := ctl.creditSvc.CheckAndEstimate(c, user, credit.Operation(req.Operation), credit.EstimationInput{
        PromptChars: promptChars, Model: model, Provider: provider,
    })
    if err != nil && !errors.Is(err, credit.ErrInsufficientCredits) {
        core.WriteResponse(c, err, nil); return
    }
    // ErrInsufficientCredits 场景：仍返回 200 + sufficient=false + reason
    core.WriteResponse(c, nil, EstimateResp{
        EstimatedCredits: pre.EstimatedCredits, Sufficient: pre.Sufficient,
        SkipDeduction: pre.SkipDeduction, Reason: pre.Reason,
        Balance: pre.Balance, CoefficientID: pre.CoefficientID,
    })
}
```

**router.go：** `apiV1.POST("/credits/estimate", userTokenMiddleware, creditController.Estimate)`

**`promptEstimator` 接口**（新增）：

```go
type IPromptEstimator interface {
    Estimate(ctx context.Context, operation, referenceID string) (chars int, model, provider string, err error)
}
// 实现按 operation 分发：
// - sop_run: 查 sop_template + 默认变量渲染估算字符数
// - sop_chat: 取 sop_run.last_context 估算
// - salesrag_chat: 取 session 近 N 轮上下文估算
// 不精确但比前端传 0 强；真实 cost 由 Reconcile 兜底
```

#### 扩展：`GET /v1/credits/balance`（复用现有）

返回扩展后的 `QuotaBreakdown`（含 `billing_mode` / `remaining_runs` / `monthly_limit` 非破坏字段，见 §2.11.1）。

#### 管理端：`POST/PUT/GET/DELETE /v1/admin/estimation-coefficients`

CRUD，内部调 `UpdateCoefficient`（含 §2.11.6 retry）。3 次失败返 HTTP 503 + `Coefficient.Concurrent` code。

### 3.12 errno 条目（P1-1 修正：项目风格字符串 code）

```go
// internal/pkg/errno/code.go 新增
var (
    ErrInsufficientCredits          = &Errno{HTTP: 402, Code: "Credits.Insufficient", Message: "积分不足"}
    ErrMembershipRequired           = &Errno{HTTP: 403, Code: "Membership.Required", Message: "需要会员资格才能购买加量包"}
    ErrBoosterNotAvailableForLegacy = &Errno{HTTP: 403, Code: "Booster.LegacyTierNotAllowed", Message: "老会员制暂不支持加量包，到期升级后可购"}
    ErrCoefficientConcurrent        = &Errno{HTTP: 503, Code: "Coefficient.Concurrent", Message: "系数更新繁忙，请稍后重试"}
    ErrTierInPeriod                 = &Errno{HTTP: 400, Code: "Tier.InPeriod",           Message: "当前会员在期，不可购买同类或更低类型"}
    ErrTrialAlreadyPurchased        = &Errno{HTTP: 400, Code: "Trial.AlreadyPurchased",  Message: "您已购买过体验卡"}
    ErrTrialNotAvailableInPeriod    = &Errno{HTTP: 400, Code: "Trial.NotAvailableInPeriod", Message: "在期会员不支持购买体验卡"}
)
```

### 3.13 helper 改造契约总览（v2 FINAL）

| 文件 | 原实现 | v2 改造 |
|------|-------|--------|
| `biz/sop/sop.go runNode` | fire-and-forget deductCreditsForSop after LLM | **重写控制流**：CheckAndEstimate → Reserve → LLM → pricing.CalculateCost → defer Reconcile |
| `biz/sop/sop.go runChat` | 同上 | 同上（operation=sop_chat） |
| `biz/sop/sop.go deductCreditsForSop` | 直接调 DeductCredits | **废弃**：新路径内联在 runNode |
| `biz/salesrag/salesrag.go Chat` | **零扣减（prod 漏洞）** | **新建完整包装** |
| `biz/salesrag/salesrag.go ChatStream` | **零扣减（prod 漏洞）** | **新建完整包装** + drain 处理 |
| `biz/credit/credit.go CanPerformAIOperation` | HasActiveMembership 分支 | 内部转调 CheckAndEstimate，1 release 过渡后删 |
| `biz/credit/credit.go DeductCredits` | 自己开事务 | 保留签名不动，内部转调 DeductCreditsTx |
| `biz/credit/credit.go DeductCreditsTx` | 不存在 | **新增**：接受外部 tx，返回 items 供 Reserve 用 |
| `biz/credit/credit_service.go creditService` | 不存在 | **新建**：实现 ICreditService，含 Reserve/Reconcile/Refund/FinalizeReservation |
| `biz/credit/credit.go RechargeWithOrderTx` | 发 credit_package | 不动 |
| `biz/order/order.go OnPaymentSuccess` | 调 RechargeWithOrderTx | **新增 billing_mode 独立切换** + cron fallback |
| `biz/payment/payment.go CreateOrder` | 无 booster case、无防提前续费 | **新增** booster 会员校验 + 防提前续费（所有 tier 产品） |
| `internal/pkg/pricing/pricing.go` | 不存在 | **新建** `pricing.CalculateCost` + LRU 缓存 |
| `internal/pkg/billing/recorder.go buildRecord` | 内置 cost 计算 | 改调 `pricing.CalculateCost`（单一数据源） |
| `biz/credit/estimation.go UpdateCoefficient` | 不存在 | **新建**（含 retry，见 §2.11.6） |
| `controller/v1/credit/credit.go Estimate` | 不存在 | **新建** HTTP endpoint |
| `controller/v1/admin_credit/coefficients.go` | 不存在 | **新建** CRUD endpoints |
| `internal/pkg/errno/code.go` | — | **新增** 7 个错误码 |

### 3.14 不在本 feature scope 的改动（防范围蔓延）

- 现有前端多处消费 `QuotaBreakdown`：只扩展字段（非破坏），不 breaking change
- 现有 `credit_account.balance_cents` 字段：保持和 credit_package.remain_credits 的既有同步逻辑，不重写
- 现有 `credit_transaction` 表：所有新扣减路径（Reserve/Reconcile/Refund）必须写入（审计轨迹），但表结构不改

## §4 跨仓库集成（HTTP API + Admin UI + 前端）（TBD）

_待 Section 4 brainstorming 完成后填充_

## §5 可观测性 + 边界 + E2E 测试策略（TBD）

_待 Section 5 brainstorming 完成后填充_

---

## 附录 A：独立 Opus Reviewer Review 历史

- **Section 1 round 1**（架构接口）：FAIL → 修 P0-1 (Factory 依赖方向)、P0-2 (legacy_tier 静默)、P0-3 (defer 竞态)
- **Section 1 round 2**（通过）：PASS
- **Section 2 round 1**（数据模型 v1）：FAIL → 3 个 P0 (P0-1 表名 sub_user、P0-2 并发锁缺失、P0-3 Trial 切换语义断层)
- **Section 2 round 2**（v2）：PASS_WITH_CONCERNS → v2 新 P0 (GetBalance 分发未定)、P0-2 retry 未完全、P1-5 @migration_cutoff 手工替换
- **Section 2 round 3**（v3）：FAIL → 3 个新 P0 (CalcMonthlyRemainingRuns 不存在、sub_user 残留在 proposal、GetBalance 接口兼容性未答)
- **Section 2 v4**（本文档 current）：folded 所有 P0 修复，未再独立 review（user 选择 A：fold + 进 Section 3）
