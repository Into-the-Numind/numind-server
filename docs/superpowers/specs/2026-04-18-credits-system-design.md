# Credits System 技术设计

> NDF S2 技术设计文档（feature: `credits-system`）
> Created: 2026-04-18
> 输入：`numind-server/requirements/credits-system.md` + `numind-server/proposals/credits-system-proposal.md`
> Status: **DRAFT Complete — 5 Sections 全部 brainstorm 完成并经独立 Opus reviewer 多轮 review + back-prop；等待 S2 Gate 人类确认设计方向**

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
| `GetBalance` | 查 credit_package 返回 `{BillingMode: credits, SubTotal/SubRemain, BoosterTotal/BoosterRemain/BoosterEarliestExpiresAt, ...}` |

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
    Reason           string     // legacy_tier 次数不足时填入 user.CanRunSOP() 返回的中文原因；调用方可直接用于前端 message（见 §3.6）
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
    BillingMode string  `json:"billing_mode"` // "credits" | "legacy_tier"
    // credits 字段（JSON 短名与现有前端 credits.ts 对齐）
    SubRemain                int64      `json:"sub_remain"`
    SubTotal                 int64      `json:"sub_total"`
    SubExpiresAt             *time.Time `json:"sub_expires_at,omitempty"`
    BoosterRemain            int64      `json:"booster_remain"`
    BoosterTotal             int64      `json:"booster_total"`
    BoosterEarliestExpiresAt *time.Time `json:"booster_earliest_expires_at,omitempty"`
    // legacy_tier 字段
    RemainingRuns *int `json:"remaining_runs,omitempty"` // nil 表示 premium unlimited
    MonthlyLimit  *int `json:"monthly_limit,omitempty"`
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

### 1.11 外部依赖声明

`ICreditService.Reconcile(rsv, actualCostCents)` 的 `actualCostCents` **必须由调用方同步采集后传入**。本 spec §3.0 定义了采集机制：新建 `internal/pkg/pricing` 模块，`pricing.CalculateCost(serviceType, provider, model, promptTokens, completionTokens) → costCents` 作为纯函数同步返回。`biz/sop` 和 `biz/salesrag` 在 LLM 调用完成后立即调此函数，传给 `FinalizeReservation`。现有 `UsageRecorder` 保持异步职责，不参与 cost 采集热路径。

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
// 对齐现有 numind-web-v3/src/api/credits.ts 的 JSON 短字段名（非破坏扩展）
type QuotaBreakdown struct {
    Balance       int64 `json:"balance"`
    SubTotal      int64 `json:"sub_total"`
    SubRemain     int64 `json:"sub_remain"`
    BoosterTotal  int64 `json:"booster_total"`
    BoosterRemain int64 `json:"booster_remain"`
    // v3 新增字段（可选，老前端不读即可）
    BillingMode              string     `json:"billing_mode"`                           // "credits" | "legacy_tier"
    RemainingRuns            *int       `json:"remaining_runs,omitempty"`               // 仅 legacy_tier；nil=无限
    MonthlyLimit             *int       `json:"monthly_limit,omitempty"`                // 仅 legacy_tier
    SubExpiresAt             *time.Time `json:"sub_expires_at,omitempty"`               // credits 月底过期展示
    BoosterEarliestExpiresAt *time.Time `json:"booster_earliest_expires_at,omitempty"`  // 最早过期 booster
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

**`IPromptEstimator` 接口**（新增，**位于 `biz/credit/prompt_estimator.go`**，biz 层而非 controller）：

```go
// biz/credit/prompt_estimator.go (新建)
type IPromptEstimator interface {
    Estimate(ctx context.Context, operation, referenceID string) (chars int, model, provider string, err error)
}

// 实装 promptEstimator 按 operation 分发调用各业务 store：
// - sop_run: 查 sop_template + 默认变量渲染 → 估算字符数
// - sop_chat: 取 sop_run.last_context → 估算
// - salesrag_chat: 取 session 近 N 轮上下文 → 估算
// 不精确但比前端传 0 强；真实 cost 由 Reconcile 兜底
```

**位置原则**：遵循 `.claude/rules/business-logic.md`（"业务逻辑统一放 biz 层"）——controller 只负责参数绑定和响应格式化，估算逻辑涉及 SOP 模板渲染、session 上下文读取，属于业务层职责。

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

## §4 跨仓库集成（HTTP API / Admin UI / 前端）（v2 FINAL）

> Section 4 经 Opus round 1 FAIL（4 P0 + 5 P1）后按现实代码契约修订：前端字段名对齐、402 拦截器设计、estimate 聚合口径、三态判断、MigrationsView 状态机、middleware 命名修正。工作量上调到 18-23 天。

### 4.0 v2 相对 v1 的主要变更

| # | 变更 | 理由 |
|---|------|------|
| 1 | 前端字段名保持现有短名（`sub_total`/`sub_remain`/`booster_total`/`booster_remain`），新增字段用项目惯例 snake_case（`billing_mode`/`remaining_runs`/`monthly_limit`）| P1-D：`credits.ts` 现有前端在 prod，breaking rename 风险高 |
| 2 | 402 拦截器新增 `case 402` + `code === 'Credits.Insufficient'` 识别 + 派发 `insufficient-credits` 事件 | P0-A：现有 request.ts 不识别 402 |
| 3 | `estimate` 聚合口径：SOP 整单（遍历所有 node）返回 `total_estimated_credits` + `node_count` + `first_node_estimate` | P0-B：预估条数字必须对用户有意义 |
| 4 | `CreditBalanceCard` 三态判断改为**跨 store 读 `user.tier === 'free'`**（不新增后端字段） | P0-C：billing_mode enum 2 值不够，跨 store 判断最简单 |
| 5 | `MigrationsView` 定义完整 UI 状态机：PENDING/EXECUTING/EXECUTED | P0-D：一次性操作需明确 UX |
| 6 | middleware 示例改为 `AuthMiddleware()` / `AdminAuthMiddleware()`；路由 group 改为 `authGroup` | P2：与现有代码一致 |
| 7 | 组件位置 `SopEstimateBar` 改 `src/views/sop/components/`（非 `src/components/sop/`） | P2：项目无 `src/components/sop/` 目录 |
| 8 | 复用现有 `InsufficientCreditsDialog`（teleport + `show(msg)`），不新建 `InsufficientCreditsModal` | P2：已存在组件 |
| 9 | `SopEstimateBar` 挂载条件：仅详情页、debounce 300ms、billing_mode guard | P1-B：避免列表页爆炸 |
| 10 | 新增 `GET /v1/admin/estimation-coefficients/history?provider=X&model=Y&operation=Z` endpoint | P1-C：历史版本查询需独立 |
| 11 | `GET /v1/credits/packages` 完整契约（分页 + 筛选 + 排序） | P1-A：§3 未定义 |
| 12 | 卡片清理退出本 feature scope（独立 commit）；`card_config.go` 审计确认已不存在（无需删除），仅清理 `CLAUDE.md` §1 描述 | P1-E 违反 §3.14；审计纠错 |
| 13 | 工作量 14-18 → **18-23 天**；交付时间线 2026-05-11 → 2026-05-16 | 吸收 P0 修订 + reviewer 建议的前端改造工作量 |

### 4.1 HTTP API 统一视图（v2）

| Method | Path | 仓库责任 | Middleware | Status | 说明 |
|--------|------|---------|-----------|--------|------|
| POST | `/v1/credits/estimate` | numind-server 新增 | `AuthMiddleware()` | §3.11 定义 | 整单估算（§4.3） |
| GET | `/v1/credits/balance` | numind-server **改造** | `AuthMiddleware()` | §2.11.1 + §4.2.1 | QuotaBreakdown 扩展 3 新字段 |
| GET | `/v1/credits/packages` | numind-server 新增 | `AuthMiddleware()` | §4.1.1 | 分页+筛选+排序 |
| POST | `/v1/orders`（productType=booster） | numind-server **改造** | `AuthMiddleware()` | §3.7 定义 | 加门槛校验 |
| POST | `/v1/orders`（其他 productType） | numind-server **改造** | `AuthMiddleware()` | §3.9 定义 | 防提前续费 |
| GET | `/v1/admin/estimation-coefficients` | numind-server 新增 | `AdminAuthMiddleware()` | §4.1.2 | 分页 + query：is_active（默认 1），支持 all |
| GET | `/v1/admin/estimation-coefficients/history` | numind-server 新增 | `AdminAuthMiddleware()` | §4.1.2 | 按 (provider, model, operation) 分组查所有 version |
| POST | `/v1/admin/estimation-coefficients` | numind-server 新增 | `AdminAuthMiddleware()` | §3.11 + §2.11.6 | 新增（触发 UpdateCoefficient retry）|
| PUT | `/v1/admin/estimation-coefficients/:id` | numind-server 新增 | `AdminAuthMiddleware()` | §2.11.6 | 编辑（append-only version bump）|
| DELETE | `/v1/admin/estimation-coefficients/:id` | numind-server 新增 | `AdminAuthMiddleware()` | §4.1.2 | 软删（is_active=0） |
| POST | `/v1/admin/migrations/billing-mode-init` | numind-server 新增 | `AdminAuthMiddleware()` | §4.4.3 | 含幂等状态返回 |
| GET | `/v1/admin/migrations/billing-mode-init/status` | numind-server 新增 | `AdminAuthMiddleware()` | §4.4.3 | 查询当前迁移分布（按钮状态依据） |

#### 4.1.1 `GET /v1/credits/packages` 契约

```go
// Query params
type ListPackagesReq struct {
    Page     int    `form:"page,default=1"`
    PageSize int    `form:"page_size,default=20"`       // max 100
    Status   string `form:"status,omitempty"`           // active/expired/revoked，空为全部
    Type     string `form:"type,omitempty"`             // trial/subscription/booster，空为全部
    Sort     string `form:"sort,default=expires_at:asc"` // expires_at:asc/desc, created_at:asc/desc
}

// Response
type ListPackagesResp struct {
    List  []CreditPackageItem `json:"list"`
    Total int64               `json:"total"`
}

type CreditPackageItem struct {
    ID            uint64     `json:"id"`
    Type          string     `json:"type"`
    TotalCredits  int64      `json:"total_credits"`
    RemainCredits int64      `json:"remain_credits"`
    ActivatedAt   time.Time  `json:"activated_at"`
    ExpiresAt     time.Time  `json:"expires_at"`
    Status        string     `json:"status"`
    OrderID       *uint64    `json:"order_id,omitempty"`
    CreatedAt     time.Time  `json:"created_at"`
}
```

**安全：** 必须 `WHERE user_id = :current_user_id`，不得跨用户。

#### 4.1.2 系数管理 endpoint

```go
// GET /v1/admin/estimation-coefficients?is_active=1&provider=X&model=Y&operation=Z&page=1
// 默认 is_active=1（仅列当前启用）；is_active=all 列所有 version（供管理端普通列表查询）

// GET /v1/admin/estimation-coefficients/history?provider=X&model=Y&operation=Z
// 返回该 (provider, model, operation) 的所有 version（含 is_active=0 的历史），按 version DESC 排序
// 专供 admin UI 历史 drawer
type CoefficientHistoryResp struct {
    List []CoefficientVersion `json:"list"`
}
```

### 4.2 numind-web-v3 前端改造

#### 4.2.1 TypeScript 类型（保持现有字段名 + 新增字段）

```typescript
// src/api/credits.ts — 扩展（非破坏）
export interface QuotaBreakdown {
  // 现有字段（保留，不动）
  balance: number
  sub_total: number
  sub_remain: number
  booster_total: number
  booster_remain: number
  // v3 新增（可选；老代码不读即可）
  billing_mode?: 'credits' | 'legacy_tier'
  remaining_runs?: number | null      // null = premium unlimited
  monthly_limit?: number | null
  sub_expires_at?: string             // 会员积分月底过期（展示用）
  booster_earliest_expires_at?: string // 最早过期的 booster
}

// 新增 EstimateResp（v2 改：含整单聚合字段）
export interface EstimateResp {
  total_estimated_credits: number       // SOP 整单估算（N 个 node 之和）
  first_node_estimate?: number           // 首 node 估算（用户提前预览）
  node_count?: number                    // N（仅 sop_run 有效）
  sufficient: boolean
  skip_deduction: boolean                // legacy_tier=true
  reason?: string                        // legacy_tier 次数不足原因
  balance: QuotaBreakdown
  coefficient_id: number
}

export function estimateCredits(operation: string, reference_id: string): Promise<EstimateResp> {
  return request.post('/v1/credits/estimate', { operation, reference_id })
}
```

#### 4.2.2 402 拦截器改造（P0-A 修复）

```typescript
// src/api/request.ts — 响应拦截器新增 case
// ... 现有 case 401/403/500 保留 ...
switch (response.status) {
  case 402:
    if (response.data?.code === 'Credits.Insufficient') {
      eventBus.emit('insufficient-credits', {
        message: response.data.message || '积分不足',
        reason: response.data.reason,
      })
      return Promise.reject(response.data)
    }
    // fallthrough 到 default
  case 403:
    // 现有"额度不足"子串匹配保留一个 release 作为 fallback
    if (typeof response.data?.message === 'string' && response.data.message.includes('额度不足')) {
      eventBus.emit('insufficient-credits', { message: response.data.message })
      return Promise.reject(response.data)
    }
    // ... 现有 403 处理 ...
  // ... 其他 ...
}
```

**App.vue** 保留现有 `insufficient-credits` 事件监听 → 触发 `InsufficientCreditsDialog.show(msg)`。

#### 4.2.3 组件清单与位置（修正 P2）

| 组件 | 位置 | 类型 |
|------|------|------|
| `CreditBalanceCard.vue` | `src/components/credit/` 新建目录 | 新组件 |
| `SopEstimateBar.vue` | `src/views/sop/components/` （遵循项目现状） | 新组件 |
| `BoosterPurchaseCard.vue` | `src/components/credit/` | 新组件 |
| `InsufficientCreditsDialog.vue` | `src/components/common/` 已存在 | **复用不新建** |
| `SettingsView.vue` | `src/views/` 已存在 | 改造（嵌入 BalanceCard + BoosterCard） |
| SOP 运行页 | `src/views/sop/` 相关 | 改造（嵌入 EstimateBar） |

#### 4.2.4 `CreditBalanceCard` 三态判断（P0-C 修复，跨 store）

```vue
<script setup lang="ts">
import { computed } from 'vue'
import { useUserStore } from '@/stores/user'
import { useCreditsStore } from '@/stores/credits'

const user = useUserStore()
const credits = useCreditsStore()

// 三态判断（按优先级）
const cardState = computed(() => {
  if (user.tier === 'free') return 'free'                              // 未购买过任何付费
  if (credits.balance?.billing_mode === 'legacy_tier') return 'legacy' // Grandfathering 老会员
  return 'credits'                                                     // 新制会员 / trial
})
</script>

<template>
  <div class="credit-balance-card">
    <!-- credits 模式 -->
    <template v-if="cardState === 'credits'">
      <div class="subscription">
        <span class="label">会员积分</span>
        <span class="value">{{ credits.balance.sub_remain }} / {{ credits.balance.sub_total }}</span>
        <span class="sublabel">{{ formatMonthEnd(credits.balance.sub_expires_at) }} 过期</span>
      </div>
      <div class="booster" v-if="credits.balance.booster_total > 0">
        <span class="label">加量包</span>
        <span class="value">{{ credits.balance.booster_remain }} / {{ credits.balance.booster_total }}</span>
        <span class="sublabel">最早 {{ formatDate(credits.balance.booster_earliest_expires_at) }} 过期</span>
      </div>
    </template>
    <!-- legacy_tier -->
    <template v-else-if="cardState === 'legacy'">
      <span v-if="credits.balance.monthly_limit === null">本月运行次数：无限</span>
      <span v-else>本月已用 {{ (credits.balance.monthly_limit ?? 0) - (credits.balance.remaining_runs ?? 0) }} / {{ credits.balance.monthly_limit }}</span>
    </template>
    <!-- free -->
    <template v-else>
      <p>成为会员解锁 AI 能力</p>
      <AppButton @click="goToMembership">升级会员</AppButton>
    </template>
  </div>
</template>
```

#### 4.2.5 `SopEstimateBar` 挂载条件 + debounce（P1-B 修复）

```vue
<script setup lang="ts">
import { ref, watch, onMounted } from 'vue'
import { useDebounceFn } from '@vueuse/core'
import { useCreditsStore } from '@/stores/credits'
import { useUserStore } from '@/stores/user'

const props = defineProps<{ sopTemplateId: string }>()
const credits = useCreditsStore()
const user = useUserStore()
const estimate = ref<EstimateResp | null>(null)

const shouldEstimate = computed(() =>
  user.tier !== 'free' &&
  credits.balance?.billing_mode !== 'legacy_tier'
)

const fetchEstimate = useDebounceFn(async () => {
  if (!shouldEstimate.value) return
  estimate.value = await estimateCredits('sop_run', props.sopTemplateId)
}, 300)

onMounted(() => {
  // 仅在 SopRunView 详情页挂载；禁止在列表/首页使用
  if (shouldEstimate.value) fetchEstimate()
})
watch(() => props.sopTemplateId, fetchEstimate)
</script>

<template>
  <!-- legacy_tier 或 free 时不渲染 -->
  <div v-if="estimate && !estimate.skip_deduction" class="estimate-bar">
    <span>预估消耗 {{ estimate.total_estimated_credits }} 积分（{{ estimate.node_count }} 步）</span>
    <span>当前余额 {{ (credits.balance?.sub_remain ?? 0) + (credits.balance?.booster_remain ?? 0) }}</span>
    <AppButton :disabled="!estimate.sufficient" @click="$emit('start')">
      {{ estimate.sufficient ? '开始运行' : '积分不足，购买加量包' }}
    </AppButton>
  </div>
</template>
```

**挂载约束（code review 检查项）：**
- 禁止 SopEstimateBar 出现在 `HomeView.vue` / SOP 列表页 / 任何循环渲染的容器内
- 仅允许在 SopRunView（单模板详情页）

#### 4.2.6 `BoosterPurchaseCard` 灰态交互（P2 补）

- `credits` 模式会员：卡片可点，点击走现有订单流程
- `free` / `trial`：灰态 + tooltip "升级为正式会员（standard/premium）后可购买加量包"，点击跳转会员购买
- `legacy_tier`：灰态 + tooltip "老会员制暂不支持加量包，到期升级后可购买"，点击**不跳转**（无操作）

### 4.3 `POST /v1/credits/estimate` 聚合口径（P0-B 修复）

**reference_id 语义按 operation 分发：**

| Operation | reference_id 语义 | 后端行为 |
|-----------|------------------|---------|
| `sop_run` | `sop_template_id` | 遍历该模板所有 node，每个 node 调 IPromptEstimator 渲染 → 求和 total_estimated_credits + 返回 first_node_estimate + node_count |
| `sop_chat` | `sop_run_id` | 取 sop_run.last_context 渲染 → 单次估算 |
| `salesrag_chat` | `session_id` | 取 session 近 N 轮上下文 → 单次估算 |
| 其他（profile_analysis/file_parse/...）| 各自 resource_id | 单次估算 |

**Response：**
- SOP 场景：`total_estimated_credits` 是用户看到的数字（N 步总和），`first_node_estimate` 是"跑第一步扣多少"供快速预览
- 非 SOP 场景：`total_estimated_credits = first_node_estimate`，`node_count = 1`

**UI 展示（SOP）：** "预估消耗 **XX** 积分（**N** 步）"。如 `sufficient=false` → 按钮禁用提示 "积分不足"。

### 4.4 numind-admin-web 管理端

#### 4.4.1 `EstimationCoefficientView` 菜单归属

放在现有 "AIServices" 组下（路由 `/ai-services/coefficients`，菜单项 "估算系数"），与 `ai-services/*` / `ai-providers/*` / `ai-tasks/*` / `ai-audit-logs` 同级。

**Sidebar 改造：** 在 `AdminSidebar.vue` 的 "AIServices" 子菜单中追加一项（单行 diff）。

#### 4.4.2 历史版本 drawer endpoint

当管理员点击某系数行 "历史版本" 按钮：
- 前端调 `GET /v1/admin/estimation-coefficients/history?provider=X&model=Y&operation=Z`
- 返回所有 version（含 is_active=0）倒序，展示在 side drawer
- 列：Version / IsActive / Values / ChangeReason / UpdatedBy / UpdatedAt

#### 4.4.3 `MigrationsView` 状态机（P0-D 修复）

**路由：** `/system-tools/migrations`（系统工具组，如无则新建菜单项）

**状态：**

| State | 条件 | 按钮表现 | UI 显示 |
|-------|------|---------|---------|
| `PENDING` | 尚未执行：查 `GET .../status` 返回 `pre_migration_stats` | 启用 | 展示"待迁移用户 N 人（分布：standard X / premium Y / trial Z）" + "执行" 按钮 |
| `EXECUTING` | 点击按钮后 | 禁用 + spinner | "正在迁移..." |
| `EXECUTED` | 执行完成，status 返回 `already_executed: true` | 永久禁用 | "已迁移 N 人 / 时间：YYYY-MM-DD HH:MM:SS / 执行人：admin_username" |

**后端判定 `already_executed`：** `COUNT(*)` from `user` where `billing_mode='legacy_tier'` > 0（迁移成功后至少有 N 人是 legacy_tier，未执行时为 0）

```go
// 状态查询 API
type MigrationStatusResp struct {
    AlreadyExecuted     bool       `json:"already_executed"`
    ExecutedAt          *time.Time `json:"executed_at,omitempty"`
    ExecutedBy          *string    `json:"executed_by,omitempty"`
    PreMigrationStats   *MigrationStatsPerTier `json:"pre_migration_stats,omitempty"`
    MigratedCount       int64      `json:"migrated_count"`
}
```

`executed_at` / `executed_by` 写入 `admin_audit_log`（现有表）。

#### 4.4.4 `CreditUsersView` 增强

在现有用户详情页顶部加 banner：
```vue
<div v-if="user.billing_mode === 'legacy_tier'" class="banner legacy-tier">
  此用户为 legacy_tier 老会员制（Grandfathering）。credit_package 自然过期不扣减，到期后下次购买进入积分制。
</div>
```

新增 Tab "活跃 Reservation"：列出 `status='reserved'` 的 credit_reservation（便于排障）。

### 4.5 路由注册（numind-server，修正 middleware/group）

```go
// internal/numind/router.go 的 authGroup 下追加
authGroup.POST("/credits/estimate", creditController.Estimate)
authGroup.GET("/credits/packages", creditController.ListPackages)
// /credits/balance 已在 authGroup 下，controller 内部改造支持 extended fields

// internal/numind/admin_router.go 的 adminAuthGroup 下追加
adminAuthGroup.GET("/estimation-coefficients", adminCreditController.ListCoefficients)
adminAuthGroup.GET("/estimation-coefficients/history", adminCreditController.ListCoefficientHistory)
adminAuthGroup.POST("/estimation-coefficients", adminCreditController.CreateCoefficient)
adminAuthGroup.PUT("/estimation-coefficients/:id", adminCreditController.UpdateCoefficient)
adminAuthGroup.DELETE("/estimation-coefficients/:id", adminCreditController.DeleteCoefficient)
adminAuthGroup.POST("/migrations/billing-mode-init", adminMigrationController.InitBillingMode)
adminAuthGroup.GET("/migrations/billing-mode-init/status", adminMigrationController.GetInitStatus)
```

**middleware 命名注意：** Section 4 v1 用的 `userTokenMiddleware` / `adminTokenMiddleware` 是伪命名——实际是项目内 `importMw.AuthMiddleware()` / `importMw.AdminAuthMiddleware()`，已在各 group 上绑定，新 endpoint 只需追加到对应 group。

### 4.6 卡片残留清理（退出本 feature scope）

**审计纠错：** 第二轮 reviewer 发现 `internal/pkg/model/card_config.go` **实际不存在**（第一轮审计误报）。验证：`ls internal/pkg/model/card*.go` 无匹配。

**本 feature 范围内剩余动作（仅 CLAUDE.md 修正）：**
- 修 `CLAUDE.md` §1 "核心功能"列表，移除"卡片生成（Markdown → 图片）"一项

**拆出本 feature 的动作：** 独立 `chore(cleanup): remove card generation references from CLAUDE.md` commit，与 credits-system feature 解耦。遵循 §3.14 scope 声明 + CLAUDE.md "不混 feature 和 bugfix" 硬规则。

### 4.7 工作分工（v2 再估算：18-23 天）

| 仓库 | 主要工作 | 预估天数 |
|------|---------|---------|
| **numind-server** | pricing 模块 + biz/credit 扩展 + ICreditService 实装 + SalesRAG 接入 + controller + 12 migration + R2 spike + promptEstimator + admin endpoints + retry 封装 | **11-14 天** |
| **numind-web-v3** | 3 新组件（BalanceCard/EstimateBar/BoosterCard）+ 2 改造（SettingsView/SopRunView）+ TS 类型扩展 + 402 拦截器改造 + 复用 InsufficientCreditsDialog + Playwright E2E | **5-6 天** |
| **numind-admin-web** | CoefficientView（DataTable + 历史 drawer + 编辑 modal）+ MigrationsView 状态机 + CreditUsersView banner + Sidebar 菜单 | **2.5-3 天** |
| **独立 commit** | CLAUDE.md 卡片清理 | 0.1 天（当天 |

**合计 18-23 天**（与 Opus reviewer 建议一致），超 §2 预估 14-18 天约 **4-5 天**。承认 S1 工作量估算**进一步低估**——根因：Section 4 发现前端 402 拦截器、estimate 聚合、字段命名对齐、MigrationsView 状态机都需要独立设计。交付时间 2026-05-11 → **2026-05-16**。

### 4.8 前端 breaking 兼容策略（P1-D 跟进）

现有 `sub_total` / `sub_remain` / `booster_total` / `booster_remain` 短名**保留不改**，新增字段 `billing_mode` / `remaining_runs` / `monthly_limit` / `sub_expires_at` / `booster_earliest_expires_at` 作为可选字段。老代码零影响，新组件消费新字段。

**spec §2.11.1 内部一致性**：v2 back-prop 已完成——`SubscriptionTotal/SubscriptionRemaining` → `SubTotal/SubRemain`（§1.8 `BalanceBreakdown` + §2.11.1 `QuotaBreakdown` + §1.4 方法行为描述都已对齐），JSON 字段与现有 `credits.ts` 一致，无 breaking rename。

## §5 可观测性 + 边界 + E2E 测试策略

### 5.1 Langfuse 可观测性（延伸 §3 trace topology）

#### 5.1.1 `span:credit-estimate`（LLM 调用前）

```go
if tc := langfuse.FromContext(ctx); tc != nil {
    spanID := langfuse.SpanID()
    langfuse.CreateSpan(tc.TraceID, spanID,
        langfuse.WithSpanParent(tc.ParentObservationID),
        langfuse.WithSpanName("credit-estimate"),
        langfuse.WithSpanInput(map[string]any{
            "operation":    string(op),
            "prompt_chars": in.PromptChars,
            "model":        in.Model,
            "provider":     in.Provider,
            "billing_mode": user.BillingMode,
        }),
        langfuse.WithSpanOutput(map[string]any{
            "estimated_credits":       pre.EstimatedCredits,
            "sufficient":              pre.Sufficient,
            "skip_deduction":          pre.SkipDeduction,
            "coefficient_id":          pre.CoefficientID,
            "char_to_token_ratio":     coef.CharToTokenRatio,
            "completion_prompt_ratio": coef.CompletionPromptRatio,
            "safety_buffer_pct":       coef.SafetyBufferPct,
            "sub_remain_before":       pre.Balance.SubRemain,
            "booster_remain_before":   pre.Balance.BoosterRemain,
        }),
    )
    langfuse.EndSpan(spanID)
}
```

#### 5.1.2 `span:credit-reserve`（扣减写入后）

记录：`reservation_id`, `reserved_credits`, `idempotency_key`, `reserved_from_packages`（FIFO 扣减明细数组）, `sub_remain_after`, `booster_remain_after`

#### 5.1.3 `span:credit-reconcile`（LLM 调用后对账）

Input: `reservation_id`, `reserved_credits`, `actual_cost_cents`, `actual_prompt_tokens`, `actual_completion_tokens`
Output: `delta` (actual - reserved), `reconcile_direction` (refund/topup/noop), `refunded_to_packages`（若 delta<0 按 seq 退还的 pkg 列表）, `final_status=reconciled`

#### 5.1.4 `span:credit-refund`（失败/取消路径）

Input: `reservation_id`, `reason`（ENUM：op_failed/user_cancelled/provider_timeout/no_actual_cost/expired_by_cron/manual_refund）
Output: `refunded_credits`, `refunded_items`, `final_status=refunded`

#### 5.1.5 Trace-level metadata（全局）

所有 SOP/SalesRAG trace root 的 metadata 追加：
- `billing_mode`: legacy_tier / credits
- `deducted_from`: subscription / booster / mixed / none(legacy)
- `credit_balance_at_start`: 快照运行开始时余额

### 5.2 迁移数据观察指标

上线后运营需要监控：

| 指标 | 查询 | 用途 |
|------|------|------|
| 每日新制用户数 | `COUNT(user WHERE billing_mode='credits' AND created_at > today)` | 迁移速度 |
| 老会员剩余数 | `COUNT(user WHERE billing_mode='legacy_tier' AND tier_expires > NOW())` | 自然衰减监控 |
| 每日 Reserve 总积分 | `SUM(reserved_credits) FROM credit_reservation WHERE created_at > today` | 消费总量 |
| 每日 Reconcile delta 分布 | `avg/p50/p95/p99(delta)` | R2 估算精度（应逐步收敛到 ±10%） |
| 加量包转化率 | `COUNT(booster orders) / COUNT(active subscription users)` | 产品 PMF |
| 24h 未 reconcile reservation | `COUNT(WHERE status='reserved' AND created_at < NOW-24h)` | 异常告警 |
| 按模型的估算偏差 | `GROUP BY (provider, model, operation), avg(delta)` | calibration 依据 |

可用 Grafana dashboard（推荐）或 Langfuse trace analytics（已有基础设施）。

### 5.3 边界情况矩阵（扩展 §2.4）

| 场景 | 预期行为 | 回归测试 |
|------|---------|---------|
| 未登录调 `/v1/credits/estimate` | 401 Unauthorized（现有 middleware） | 现有 E2E 覆盖 |
| free 用户调 estimate | 返回 `{sufficient:false, reason:'需要会员资格'}` + balance 0/0 | 新增 E2E |
| legacy_tier 用户调 estimate | 返回 `{skip_deduction:true, sufficient: per CanRunSOP}` | 新增 E2E |
| Reserve 时余额刚好耗尽（race） | DeductCredits `FOR UPDATE` 行锁保证原子；返回 `ErrInsufficientCredits` | 压测（手工，S5 外） |
| Reconcile 时 reservation 已终态 | 返回 `ErrAlreadyFinalized`，defer 忽略 | 单元测试 |
| delta 极大（actual >> reserved 10x） | 按 actual 补扣；超 balance 留负债 credit_transaction type='reconcile_debt' | 单元测试 |
| 同 SOP run 重试（双击） | `uk_idempotency_key` 命中，返回已存在 reservation，不重复扣 | E2E |
| Admin 改系数后新 Reserve 用新 version，in-flight Reservation 用老 version Reconcile | `coefficient_id` 冻结快照保证 | 单元测试 |
| Cron 扫 24h 未 reconcile | 自动 Refund `reason=expired_by_cron` | 单元 + 定时任务集成测试 |
| SOP 跨月（Reserve 4/30 23:59，Reconcile 5/1 00:01） | 按 `reserved_from_packages` 精确退原 pkg；原 pkg 已过期则退还 no-op | E2E（若可构造） |
| 会员到期瞬间发起 SOP | CheckAndEstimate 读 fresh user（§1.5）；到期则 BillingMode 已切换 | 单元测试 |
| MySQL 连接池耗尽 Reserve 失败 | 返回 500 + 不产生 reservation；前端弹"系统繁忙" | 单元测试（mock） |
| Admin GDPR 批量删除积分 | 先清零 reserved（防 dangling），再物理删 credit_package | 运维手册（非 E2E） |

### 5.4 E2E 测试策略（6 条 Playwright 路径）

**位置：** `numind-web-v3/e2e/credits-system.spec.ts`（单文件 6 组 test）
**依赖：** 现有 `e2e/auth.setup.ts` + `$E2E_USERNAME` / `$E2E_PASSWORD` 环境变量（`.claude/rules/testing.md §2`）

#### Path 1：credits 会员新购 + SOP 正常扣减

- 前置 force tier=free → 购买 monthly 会员（mock 支付成功） → 验证余额 2000/2000
- 进 SOP 详情页 → `SopEstimateBar` 展示预估 → 启动 → 运行完成
- 回账户中心验证余额减少；查 `credit_reservation` status=reconciled

#### Path 2：跨池扣减（会员 + booster FIFO 顺序）

- 前置：sub_remain=50, booster_remain=600
- 跑 SOP 扣 150 → 验证 sub_remain=0, booster_remain=500
- 验证 `credit_reservation_item` 2 行：seq=1 from sub_pkg credits=50, seq=2 from booster_pkg credits=100

#### Path 3：非会员购买 booster 被拒

- 前置：free 用户
- 访问加量包入口 → 灰态 + 提示 + 跳转会员购买
- 直接 `POST /v1/orders?productType=booster` → 403 `Membership.Required`

#### Path 4：legacy_tier 老会员 SOP（零扣减）

- 前置手工 SET billing_mode='legacy_tier', tier='standard', tier_expires=future
- SOP 详情页 → SopEstimateBar **不渲染**（skip_deduction=true）
- 跑 SOP → 成功 + monthly_sop_runs +1；账户中心展示"本月已用 X/20"

#### Path 5：SalesRAG Chat 新扣减（prod 漏洞修复验证）

- 前置：credits 用户 sub_remain=100
- SalesRAG 对话发一条消息 → LLM 响应 → 验证 sub_remain 下降
- Langfuse 验证 credit-estimate / credit-reserve / credit-reconcile 三 span

#### Path 6：trial 完整生命周期

- free 用户购买 trial（¥9.9）→ 验证 billing_mode=credits, sub_remain=200, expires=now+3d
- 跑 SOP 扣减 → 手工快进时间 force tier_expires=past → trial 到期
- 再跑 SOP → 提示积分不足 → 购买 standard → 余额变 2000

### 5.5 数据 spike 产出验证清单（S3 plan Task 1）

| 验证项 | 标准 |
|-------|------|
| 样本时间范围 | 最近 90 天 `usage_record` |
| 样本量下限 | 每个 `(provider, model, operation)` ≥ 30 条；< 30 用保守默认 (1.5, 0.5, 0.3) |
| `completion_prompt_ratio` 范围 | [0.05, 3.0]；超出需人工 review |
| `safety_buffer_pct` 初值 | 2σ 覆盖（≈ 20-30%） |
| 覆盖度 | 必须包含 `seed_pricing_rules.sql` 所有活跃 (provider, model) 组合 |
| Provenance | migration 注释或伴随 md 记录统计 SQL、样本数、时间范围、执行时间 |

上线后 2-4 周 beta，对比 reservation delta 分布 → 首次 calibration（管理员触发 append-only 新 version）。

### 5.6 回归测试策略

**单元测试（Go，`*_test.go`）：**
- `biz/credit/credit_service_test.go`：ICreditService 全方法 happy/unhappy path（表驱动）
- `biz/credit/estimation_test.go`：R2 估算 + UpdateCoefficient 并发 retry
- `internal/pkg/pricing/pricing_test.go`：CalculateCost 公式 + 缓存失效
- `biz/payment/payment_test.go`：Booster 会员门槛 + 防提前续费

**集成测试：**
- 12 个 migration 顺序执行 + rollback（MySQL testcontainer）

**E2E 测试：** 上述 6 条 Playwright 路径（持久回归）

**性能测试（手工，S5 外）：** 验证 Reserve/Reconcile 延迟 < 100ms

### 5.7 S5 Gate 验证计划

对照 `.claude/skills/ndf-workflow.md` §3 S5 清单：

- [ ] `task lint` + `task test`（完整版 race detection + coverage）退出码 0
- [ ] `npm run lint` + `npm run type-check`（web-v3 + admin-web 两仓库）退出码 0
- [ ] `npm run test:e2e`（6 条关键路径）退出码 0
- [ ] gstack `/qa` 浏览器截图 QA（本地 localhost:5173）无 P0 视觉/功能回归
- [ ] Langfuse local stack（docker compose -f docker-compose.langfuse.yml up -d）验证 credit-estimate / credit-reserve / credit-reconcile / credit-refund span 出现
- [ ] 可观测性检查：所有 span 字段齐全（参照 §5.1 schema）
- [ ] 数据 spike 产出验证（§5.5 清单 6 项全过）
- [ ] migration rollback 演练（本地 MySQL forward + rollback 一轮不报错）

**E2E 必须 Playwright 持久回归**（符合 `.claude/rules/ndf-enforcement.md` 规则 10，涉及支付 + 权限 + 会员高风险业务逻辑）。

---

## 附录 A：独立 Opus Reviewer Review 历史

- **Section 1 round 1**（架构接口）：FAIL → 修 P0-1 (Factory 依赖方向)、P0-2 (legacy_tier 静默)、P0-3 (defer 竞态)
- **Section 1 round 2**（通过）：PASS
- **Section 2 round 1**（数据模型 v1）：FAIL → 3 个 P0 (P0-1 表名 sub_user、P0-2 并发锁缺失、P0-3 Trial 切换语义断层)
- **Section 2 round 2**（v2）：PASS_WITH_CONCERNS → v2 新 P0 (GetBalance 分发未定)、P0-2 retry 未完全、P1-5 @migration_cutoff 手工替换
- **Section 2 round 3**（v3）：FAIL → 3 个新 P0 (CalcMonthlyRemainingRuns 不存在、sub_user 残留在 proposal、GetBalance 接口兼容性未答)
- **Section 2 v4**（current）：folded 所有 P0 修复，未再独立 review（user 选择 A：fold + 进 Section 3）
- **Section 3 round 1**（核心 biz API + 时序）：FAIL → 5 个 P0 (P0-1 getBillingCostFromRecorder 虚构 / P0-2 控制流反转被当签名调整 / P0-3 idempotency key 与 §2.11.3 矛盾 / P0-4 PreCheckResult 缺 Reason 字段 / P0-5 重复发明 HasActiveSubscription)
- **Section 3 v2**（current）：选 Direction 3 同步 pricing，folded 所有 P0+P1 修复；back-prop §1.8（PreCheckResult.Reason）+ §1.11（pricing 依赖声明）+ §3.11（IPromptEstimator 位置）
- **Section 4 round 1**（跨仓库集成）：FAIL → 4 个 P0 (P0-A 402 拦截器缺失 / P0-B estimate 聚合口径未定 / P0-C free 与余额 0 不可区分 / P0-D MigrationsView UI 未定义) + 5 P1
- **Section 4 v2**（current）：folded 所有 P0+P1 修复；back-prop §1.8 + §2.11.1（BalanceBreakdown/QuotaBreakdown 字段名 Subscription* → Sub* 对齐现有 credits.ts）
- **Section 5**（可观测性 + 边界 + E2E）：按用户要求不再 review，直接落盘进入 S2 Gate
