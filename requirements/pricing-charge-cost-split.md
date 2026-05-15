# pricing-charge-cost-split

## 来源
- 提出人：zhiyuchen（产品/技术 owner）
- 提出日期：2026-05-15

## 需求描述

当前积分扣费路径（Reserve / Reconcile）调用 `pricing.CalculateCost`，使用的是 **成本价字段**（`InputPricePerMTok` / `OutputPricePerMTok` / `PricePerCall` / `PricePerGB`），不是 **售价字段**（`SellInputPricePerMTok` / `SellOutputPricePerMTok` / `SellPricePerCall` / `SellPricePerGB`）。

需要改成：**不管 billing_mode 是 flat 还是 tiered_token，向用户扣减的积分都按售价计算**。

附加发现（顺带修复）：
1. **tiered 模式已经误用了售价字段**（`pricing.calculateTieredCost` 读 `tier.SellPerMTok`），与 flat 模式行为不一致，导致两条路径的字段读取语义相反。
2. **CreditMultiplier 当前被乘进 `CalculateCost` 结果**，污染了 `UsageRecord.CostCents`（成本记账列）。multiplier 是用来"加价收用户"的，不应影响我们的成本统计。
3. **`recorder.computeRevenue` 与 `pricing.CalculateCost` 是几乎重复的实现**（仅字段不同，billing_mode 处理和 tier 查询完全一致），同一逻辑散落在两个包，扩展计费模式时要同步改 2 处。

## 业务目标

1. **正确性**：让积分扣减按售价计算，与产品定价策略一致。当前因为所有 seed 数据 cost == sell 且 CreditMultiplier == 1.0，行为上没差异，但一旦运营在管理端调整售价或加价倍率，会立刻出现"按错误价格扣费"的营收损失或超扣风险。
2. **语义清晰**：UsageRecord 的 `cost_cents`（我们的成本）与 `revenue_cents`（向用户收费）真正分离，CFO 报表口径不再被 CreditMultiplier 污染。
3. **架构去重**：billing_mode / tier 处理逻辑收敛到 pricing 包单一位置，recorder 变薄壳，未来新增计费维度（缓存 token、按分钟、按文档页）只需改 1 处。
4. **零回归**：当前 seed 数据下部署后行为完全等价，可以安全上线。

## 优先级

**高**

- 直接关联营收：扣费金额是公司收入计算基础
- 是一个"latent bug"：当前因数据巧合（cost == sell）没暴露，但一旦运营在 pricing_rule 管理端调整售价，立即变成真正的财务事故
- 是上一轮 `credits-deduct-cycle-wiring` P0 的同类高敏区域，应在再次产生事故前主动修复

## Triage

- 推荐轨道：**Standard**
- 分类理由：
  1. 数据库 schema 变更：**否**（`sell_*` 字段和 `sell_per_mtok` 列在 `add_billing_dual_pricing.sql` 时就已建好，本 feature 不动 schema）
  2. 新增 API 端点：**否**（纯内部 pricing 计算逻辑）
  3. 新外部服务集成：**否**
  4. 影响文件数：**>3**（预估 6–8 文件，含 pricing 包 + pricing 包 mock + recorder + credit_service + sop biz 的 2 个调用点 + 可能的 chatbot biz 调用点 + 相关测试）
  5. 高风险业务逻辑：**是**（积分扣减核心路径，任何计算错误都直接影响每笔订单的扣费金额，与 `credits-deduct-cycle-wiring`、`membership-balance-read-path` 同级别敏感度）
- 人类决定：**确认 Standard**（2026-05-15）

## 备注

### 关键设计假设（待 S2 spec 锁定）

1. **接口形态**：在 `pricing.ICalculator` 上同时保留 `CalculateCost`（用 cost 字段、不乘 CreditMultiplier）和新增 `CalculateCharge`（用 sell 字段、乘 CreditMultiplier）。两个方法共用同一 pricing rule 解析路径和 LRU 缓存。
2. **CreditMultiplier 归属**：仅作用于 `CalculateCharge`，从 `CalculateCost` 中移除。这会修正 UsageRecord.CostCents 历史上的语义污染（当前因 multiplier 全为 1.0 而未爆出问题）。
3. **UserTypeMultiplier**：保持当前位置（在 Reconcile 阶段作用于已经算好的 charge 金额），本 feature 不动。
4. **recorder 改造**：`computeRevenue` 与 `calculateTieredRevenue` 删除，统一调用 `pricing.CalculateCharge`。recorder 不再持有 billing_mode 处理代码。
5. **调用点切换清单**（预估，S2 spec 锁定）：
   - `internal/numind/biz/credit/credit_service.go:192`（CheckAndEstimateBudget 预估）
   - `internal/numind/biz/sop/sop.go:936`（SOP 节点 Reconcile actualCost）
   - `internal/numind/biz/sop/sop.go:1693`（SOP 另一条 Reconcile 路径）
   - 可能存在的 chatbot biz / salesrag biz 的 Reserve/Reconcile（S1 调研阶段确认）
6. **向后兼容验证**：seed 数据 cost == sell、CreditMultiplier == 1.0 时，新旧行为应该字节级一致——这是 S5 的核心验证点，需要构造 cost ≠ sell、multiplier ≠ 1.0 的测试用例额外验证新路径正确。

### 关联 features

- `credits-deduct-cycle-wiring`（completed）— 上一轮修了 Reserve/Reconcile 切到新表，没动 pricing 字段层面的成本/售价语义
- `membership-balance-read-path`（completed）— 与上一项同 v2.1.17 prod tag
- `ai-service-admin-complete`（completed）— 提供了 pricing_rule 管理端 CRUD 能力，售价字段在 UI 上是可编辑的，本 feature 让这个能力真正生效

### 不在本 feature 范围

- 不动 pricing_rule schema
- 不动管理端 pricing_rule CRUD UI
- 不调整任何模型的实际售价数值（保持 seed 数据 cost == sell 现状）
- 不动 UserTypeMultiplier 计算位置
- 不动 R2 char-path 安全缓冲（safetyBufferPct）
