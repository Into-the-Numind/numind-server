# pricing-charge-cost-split — 提案

## §1 方案概述 [客户可见]

> **本 feature 是内部基础设施改进，无客户可见变更。**

简单地说：让积分扣减按"售价"计算，而不是按"成本价"。当前因数据巧合（售价等于成本价），用户看不到任何差异；但这是一个潜伏的 bug——一旦运营在管理端调整某个模型的售价或加价倍率，扣费金额会按错误的字段计算，立即变成营收事故。本次修复让代码与管理端的售价配置真正对齐，同时澄清"我们花了多少成本"和"向用户收多少钱"两个独立的账目维度。

部署后用户层面无任何感知，运营在管理端的定价 UI 行为也不变；这是一项纯粹的"代码与配置语义对齐"工作。

## §2 报价与周期 [客户可见]

> **N/A — 内部基础设施改进，不向客户报价。**

内部工作量预估见 §3。

## §3 技术可行性 [AI 内部]

### 现有功能复用

- **`pricing` 包**：现有 `ICalculator` 接口 + `calculator` 实现 + LRU 缓存 + `resolvePricingRule` 路径全部复用。本 feature 在接口上新增方法，不重写解析层。
- **`PricingStore`**：现有 store 接口的 3 个方法（`GetPricingRule` / `GetPricingRuleTiers` / `GetProviderModelID`）足够支撑新方法，无需扩展。
- **pricing_rule 表结构**：sell_* 字段、`pricing_rule_tier.sell_per_mtok`、`CreditMultiplier` 列在 `add_billing_dual_pricing.sql` 和 `add_credit_multiplier_to_pricing_rule.sql` 已建好。
- **管理端 pricing CRUD**：已存在（`ai-service-admin-complete` 完成），售价字段在 UI 上可编辑，本 feature 让该 UI 真正驱动扣费。

### 完整调用点清单（S1 调研产出）

按数据流向分组 — 这是 S2 spec 锁定的精确改动依据：

#### 路径 A：估算路径 → **切换到 `CalculateCharge`**

向用户收费的预估，决定 Reserve 阶段预扣多少积分。

| # | 文件 | 行 | 函数 | 当前流向 |
|---|------|----|------|---------|
| 1 | `internal/numind/biz/credit/credit_service.go` | 197 | `CheckAndEstimateBudget` | → 决定 Reserve 是否充足 |
| 2 | `internal/numind/biz/credit/estimation.go` | 96 | `EstimateCredits` | → SOP/SalesRAG pre-flight |

#### 路径 B：实际计费路径 → **切换到 `CalculateCharge`**

LLM 调用返回后的真实金额，决定 Reconcile 阶段最终扣减/退还多少。

| # | 文件 | 行 | 函数 | 当前流向 |
|---|------|----|------|---------|
| 3 | `internal/numind/biz/sop/sop.go` | 936 | SOP 节点执行 | → `FinalizeReservation` → `Reconcile` |
| 4 | `internal/numind/biz/sop/sop.go` | 1693 | SOP chat completion stream | → `FinalizeReservation` → `Reconcile` |
| 5 | `internal/numind/biz/salesrag/salesrag.go` | 418 | salesrag `setActualCost` | → `FinalizeReservation` → `Reconcile` |
| 6 | `internal/pkg/aiservice/middleware/billing.go` | 546 | `publishCostToHolder`（chatbot + gateway） | → `finalCostHolder` → `FinalizeReservation` |

#### 路径 C：成本统计路径 → **保留 `CalculateCost`**

UsageRecord.CostCents（我们花了多少），用于财务报表口径。

| # | 文件 | 行 | 函数 | 当前流向 |
|---|------|----|------|---------|
| 7 | `internal/pkg/billing/recorder.go` | 365 | `computeCost` | → `UsageRecord.CostCents` |

#### 路径 D：收入统计路径 → **删除，改调 `pricing.CalculateCharge`**

UsageRecord.RevenueCents（向用户收了多少），目前是 recorder 内的重复实现。

| # | 文件 | 行 | 函数 | 重构后 |
|---|------|----|------|-------|
| 8 | `internal/pkg/billing/recorder.go` | 400-423 | `computeRevenue` + `calculateTieredRevenue` | 删除，调 `pricing.CalculateCharge` |

#### 配套：Mock & 测试

- `internal/pkg/billing/recorder_test.go`（`spyCalculator`）：新增 `CalculateCharge` 实现
- `internal/pkg/aiservice/middleware/billing_test.go`（`mockPricingCalc`）：新增 `CalculateCharge` 实现
- `internal/pkg/pricing/pricing_test.go`：新增 `CalculateCharge` 单元测试，覆盖 cost ≠ sell、multiplier ≠ 1.0、tiered/flat 两模式
- `internal/numind/biz/credit/credit_service_test.go` 和相关 fixtures：mock 期望从 `CalculateCost` 调用改为 `CalculateCharge`

### 接口设计（S2 spec 会再 review，这里是预定方向）

```go
type ICalculator interface {
    // CalculateCost: 我们的成本。用 cost 字段（InputPricePerMTok / OutputPricePerMTok 或 tier.CostPerMTok）。
    // 不乘 CreditMultiplier。仅 recorder.computeCost 调用，写入 UsageRecord.CostCents。
    CalculateCost(ctx context.Context, serviceType, provider, model string,
        promptTokens, completionTokens int) (costCents int64, err error)

    // CalculateCharge: 向用户收费的金额。用 sell 字段（SellInputPricePerMTok / SellOutputPricePerMTok 或 tier.SellPerMTok）。
    // 乘 CreditMultiplier。Reserve/Reconcile 路径和 recorder.computeRevenue 调用。
    CalculateCharge(ctx context.Context, serviceType, provider, model string,
        promptTokens, completionTokens int) (chargeCents int64, err error)
}
```

两方法共用同一个 `resolvePricingRule` 和 LRU 缓存。billing_mode 分支（flat / tiered_token）抽到一个内部 helper，按 priceField 参数决定读哪组字段。

### 技术风险

| 风险 | 等级 | 缓解 |
|------|------|------|
| **R1 误改 recorder.computeCost 调用** 导致 UsageRecord.CostCents 被污染为售价 | 中 | spec 明确"CostCents 链路保留 CalculateCost"；S4 review 把 grep 验证作为 reviewer checklist |
| **R2 漏掉某个调用点** 导致一处仍按成本扣费，与其他路径不一致 | 中 | S1 已穷举 6 个 charge 路径 + 1 个 cost 路径 + 1 个 revenue 路径；S4 编码前 `git grep CalculateCost` 二次确认零遗漏 |
| **R3 Mock 文件签名不同步** 导致测试假绿 | 低 | S4 第一个 task 改 ICalculator 接口本身，编译器会强制 mock 补齐 |
| **R4 部署后行为变化** | 高 | S2 spec 明确"seed cost == sell + multiplier == 1.0 → 字节级一致"为不变量；S5 验证策略包括：①跑全套现有 credit/sop/salesrag 单测确认零回归 ②构造 cost ≠ sell 用例验证新路径取值正确 ③dev 环境 curl `/v1/credits/estimate` 对比新旧值 |
| **R5 CreditMultiplier 语义变化** 影响历史 UsageRecord.CostCents 解读 | 低 | 当前 prod 所有 multiplier == 1.0，CostCents 数据无污染；新代码上线后语义统一为"纯成本"。无需数据回填 |
| **R6 并发场景下 cache 失效** 不一致 | 低 | 两方法共用同一 cache key，pubsub `pricing_rule_changed` 已覆盖；无新增缓存层 |
| **R7 改动跨 4 个 biz 包** 引入意外回归 | 中 | 通过 `task lint` + `go test ./...` + 现有 credit/sop/salesrag 测试套件；S5 必跑 Playwright E2E 至少 1 条 SOP 流程 |

### 涉及仓库

- [x] **numind-server**（唯一受影响仓库）
- [ ] numind-web-v3
- [ ] numind-admin-web

### AI 可观测性

- [x] 涉及 LLM 调用：**间接是**（pricing 计算服务于 LLM 调用的扣费，但不发起新的 LLM 调用）
- Trace 起点：N/A（pricing 计算本身不创建 trace，所有 trace 由调用方 SOP/Chatbot/SalesRAG 创建）
- Generation 点：N/A
- 关键元数据：N/A
- **影响现有 trace 的元数据：** 否。pricing 计算结果当前不写入 Langfuse generation usage（usage 用真实 token 数）。本 feature 不动 trace 上下文传播。
- **回归保护点：** `.claude/rules/ai-service.md` 要求 LLM 调用必须有 Langfuse trace；本 feature 不动 LLM 调用本身，但 S4 review checklist 应确认 sop.go:936/1693 和 salesrag.go:418 这几处改动不破坏附近的 `langfuse.FromContext` / `EndGeneration` 调用。

### 工作量估算

| 阶段 | 内容 | 预估 |
|------|------|------|
| S2 | 写 spec（接口契约 + billing_mode 分支处理 + 测试矩阵 + 不变量声明） | 1.5h |
| S3 | 写 plan（5-7 task，含验证策略 task） + 独立 reviewer 审 plan 原子性 | 1h |
| S4 | 编码（按 task 顺序：①接口扩展+mock 补齐 ②`CalculateCharge` 实现 ③6 处调用点切换 ④recorder.computeRevenue 收编 ⑤测试新增） + per-task 两阶段 Sonnet review | 3-4h |
| S5 | 本地验证：`task test` + 构造 cost ≠ sell 用例 + dev 环境 curl 验证 | 1-1.5h |
| S6 | merge develop + push + dev 验收 | 0.5h |
| S7 | release → qa → tag → prod | 0.5h |
| **总计** | | **8-9h（约 1 个工作日）** |

## §4 产品需求定义 — PRD [AI 内部]

### 用户故事

> 注：本 feature 没有终端用户故事。用户故事按"内部角色"维度撰写。

- 作为**运营**，我需要在管理端给某个 LLM 模型设置一个高于成本价的售价（例如 cost ¥1/MTok, sell ¥1.5/MTok），以便用户使用该模型时按 ¥1.5 扣减积分而不是 ¥1，让公司从该模型上有 50% 的毛利。
- 作为**运营**，我需要在管理端给昂贵模型（如 Claude Sonnet 4.6）设置 `credit_multiplier = 1.5`，以便用户调用时按 1.5 倍积分计费，反映该模型的高单位价值。
- 作为 **CFO**，我需要在月度财报中清晰区分"AI 服务成本"（usage_record.cost_cents 加总）和"AI 服务收入"（usage_record.revenue_cents 加总），以便准确计算业务毛利率。当前因为 cost_cents 被 `credit_multiplier` 乘过，毛利率统计不准（虽然现状 multiplier=1.0 不暴露）。
- 作为**后端工程师**，我需要 pricing 计算逻辑收敛到单一位置，以便未来新增计费维度（例如缓存 token、按分钟计费）时只改一处。

### 验收标准

- [ ] **AC1**：`pricing.ICalculator` 接口暴露两个方法：`CalculateCost` 和 `CalculateCharge`。`CalculateCost` 不乘 `CreditMultiplier`，`CalculateCharge` 读 sell 字段并乘 `CreditMultiplier`。
- [ ] **AC2**：所有积分扣减路径（路径 A + B 共 6 个调用点）改为调 `CalculateCharge`。`grep -rn "CalculateCost" internal/numind/biz/credit internal/numind/biz/sop internal/numind/biz/salesrag internal/pkg/aiservice/middleware/billing.go` 在生产代码中只剩注释/文档引用，无实际调用。
- [ ] **AC3**：`recorder.computeCost` 调用 `CalculateCost`，结果写入 `UsageRecord.CostCents`。`recorder.computeRevenue` 调用 `CalculateCharge`，结果写入 `UsageRecord.RevenueCents`。`recorder.calculateTieredRevenue` 被删除。
- [ ] **AC4（不变量）**：当 `input_price_per_m_tok == sell_input_price_per_m_tok`、`output_price_per_m_tok == sell_output_price_per_m_tok`、`credit_multiplier == 1.0` 时，`CalculateCharge` 与重构前的 `CalculateCost` 返回完全相同的值（覆盖 flat / tiered_token 两种 billing_mode）。
- [ ] **AC5（新行为）**：当 `sell_input_price_per_m_tok = 1.5, input_price_per_m_tok = 1.0, credit_multiplier = 1.0` 时，`CalculateCharge` 返回基于 1.5 的金额；`CalculateCost` 返回基于 1.0 的金额。Tiered 模式同理用 `tier.SellPerMTok` 而非 `tier.CostPerMTok`。
- [ ] **AC6（multiplier 行为）**：当 `credit_multiplier = 1.5`、sell == cost 时，`CalculateCharge` 返回值 = `CalculateCost` × 1.5。
- [ ] **AC7**：单元测试覆盖 AC4/AC5/AC6 三种场景 × flat/tiered 两种 billing_mode = 6 个基础用例 + 边界（multiplier=0 当 1.0 处理、sell=0 fallback、零 token 等）。
- [ ] **AC8**：`task lint` + `task test` 全绿。所有现有 credit / sop / salesrag / chatbot 测试无回归。
- [ ] **AC9（dev 验证）**：dev 环境部署后，相同用户相同模型相同 token 数下，credit_reservation 表的 `reserved_credits` 字段值与重构前一致（因为 cost == sell + multiplier == 1.0）。
- [ ] **AC10（构造场景验证）**：S5 在 dev 环境手动修改一条 pricing_rule 使 sell ≠ cost，触发一次 SOP 调用，验证扣减按 sell 值；验证完恢复数据。

### 边界情况

- **B1 sell 字段为 0**：当 `sell_input_price_per_m_tok = 0` 时如何处理？
  - **决定**：`CalculateCharge` 严格使用 sell 字段，即返回 0。这是运营有意配置（例如"免费体验"模型）。不 fallback 到 cost——避免与运营意图冲突。
  - **影响的 recorder.go:347**：当前 `if revenueCents == 0 && costCents > 0 { revenueCents = costCents }` 的 fallback 行为需要在 spec 中重新评估。建议保留 fallback 但加注释说明这是为了处理"运营漏配 sell 字段"的安全网。

- **B2 CreditMultiplier 为 0 或负数**：
  - **决定**：保持 `pricing.go:134-136` 当前逻辑——`<= 0` 视为 `1.0`。

- **B3 BillingMode = "tiered_token" 但无 tier 数据**：
  - **决定**：保持当前 `pricing.go:149-151` 行为——返回错误，不静默扣 0。

- **B4 LRU cache 击穿**：
  - **决定**：两方法共用同一 cache（同一份 PricingRule 实例同时供 cost 和 charge 计算），无需独立 cache。

- **B5 历史 UsageRecord.CostCents 数据语义不一致**：
  - 改动前：CostCents = cost × multiplier（语义"已加倍率的成本"）
  - 改动后：CostCents = cost（纯成本）
  - **决定**：因 prod 所有 multiplier == 1.0，历史数据与新数据数值一致。不做数据迁移。在 S2 spec 的"数据兼容性"节做出明确说明。

- **B6 Reconcile 已经在 credit_service.go:651 应用 UserTypeMultiplier**：
  - **关系**：UserTypeMultiplier 作用于 `actualCostCents`（已经是 CalculateCharge 的返回值）。CreditMultiplier 在 CalculateCharge 内部，UserTypeMultiplier 在 CalculateCharge 之外。两个乘法因子语义独立，不重复。
  - **决定**：本 feature 不动 UserTypeMultiplier 位置。在 spec 中用 ASCII 图表清晰画出乘法因子的链路。

### 权限规则

不涉及用户权限层面变更——所有路径仍走现有的 `billing_mode` 分支（`credits` vs `legacy_tier`），`SkipDeduction` 逻辑不变。

### UI 行为规格

无 UI 改动。

### 不在本 feature 范围

- ❌ 不修改 `pricing_rule` / `pricing_rule_tier` 的 schema
- ❌ 不修改任何模型的实际 sell 数值（保持 seed cost == sell 现状）
- ❌ 不修改管理端 pricing CRUD 的 UI 行为
- ❌ 不动 R2 char-path 安全缓冲 / safetyBufferPct
- ❌ 不动 UserTypeMultiplier 的位置和计算
- ❌ 不做历史 UsageRecord 数据迁移
- ❌ 不调整 Langfuse trace topology

---

## 附录：office-hours 式假设挑战（S1 自审）

> NDF S1 要求"挑战假设、重定义问题"。无外部客户参与，自审如下。

### 这个问题真的存在吗？

**是。** 当前 prod 所有 pricing_rule 行 `sell == cost`、`credit_multiplier == 1.0` 是因为：
1. 管理端 CRUD 上线前所有 seed 数据是手写硬编码 `sell == cost`
2. 管理员还没真正在管理端调整过任何模型的售价

但 `ai-service-admin-complete` 已经把售价字段做成可编辑。**第一次有人在管理端把某个模型的 sell 改成 > cost 的那一刻，就会出现错误扣费**（按 cost 扣，公司损失差价）。这是个"等着踩"的 latent bug。

### 有没有更简单的方案？

考察过 3 个：

1. **方案 X：直接把 `CalculateCost` 改成读 sell 字段。** — 否决。会污染 `UsageRecord.CostCents`（财务成本统计），破坏 CFO 报表口径。
2. **方案 Y：在管理端禁止 sell ≠ cost。** — 否决。彻底失去定价灵活性，违背 `ai-service-admin-complete` 的本意。
3. **方案 Z：本 feature 方案，分两个方法。** — 采纳。语义清晰、可测试、为未来扩展计费维度铺路。

### 这个时机对吗？

**是。** 当前 prod 数据 cost == sell，部署是"字节级一致"的零风险窗口。等到运营第一次调价后再改，需要数据回填和兼容性处理，风险大得多。

### 是否应该升级为更大的重构？

不应该。本 feature 只做 cost/charge 语义分离这一件事。诱惑：顺便重构 `UserTypeMultiplier` 位置、把 `safetyBufferPct` 也收编进 pricing 包、统一 cache 层…… 都否决，每一项都增加 review 面积和回归风险。**单一职责 feature 是 NDF 主干轨道的硬规则**。
