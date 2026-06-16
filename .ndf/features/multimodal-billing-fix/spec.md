# multimodal-billing-fix — S1 提案 + S2 技术设计

> feature `multimodal-billing-fix` · Standard · 2026-06-17 · 仅 numind-server
> Bug-from-Customer（Rule 11）：第一个 commit = 失败的复现测试。

## §1 提案（为什么 / S1）

**问题**：一次 chatbot 上传图片（claude-opus-4-6），真实用量 2178 输入 + 504 输出 token（真实成本 ≈ 0.12 元 ≈ 12 积分），却预扣 796、对账扣到 **64000 积分**。

**三处 bug 叠加**（已 forensic 确认，见 requirement.md §2）：
1. 定价把 `service_type` 当主键 → 带图请求被归类 `llm_vision`，dmxapi/claude 无 vision 价 → CalculateCost 报错。
2. 对账缺价兜底把 `ReservedOutputTokens`（=64000 个 token）**直接当 64000 积分**扣（单位错误 + 取最坏值）。
3. bill-only 预扣冷启动上限 = `MaxOutputTokens/2` = 64000，比实际高 66 倍。

**用户拍板的方向**（2026-06-17）：
- ①「llm_vision 统一定价逻辑是错的——每个模型本来就有自己的定价，无论模态」→ 定价按 `(provider, model)` 解析，service_type 不再是定价主键。
- ②「预估 796 实际 12，预估就大错特错，要调到和实际非常接近」→ bill-only 预扣用历史均值估算器 + 合理冷启动默认。
- ③ 对账兜底绝不放大（安全网）。
- 三个改纳入**同一 feature**。

**技术可行性**：三处都在既有 billing/pricing 代码内小范围改，无新表、无新 API、无新外部服务。已验证**没有任何模型配双模态价** → 定价 service_type-agnostic 回退零歧义。

## §2 技术设计（S2）

### 2.1 Fix ① — 定价按 (provider, model) 解析（`internal/pkg/pricing/pricing.go`）

**现状**：`resolvePricingRule(serviceType, provider, modelKey)` 把 service_type 编进 cache key 和 `store.GetPricingRule` 查询。两级查找（直接 key + provider-model-ID 回退）都带 service_type，缺则 `ErrRecordNotFound` 硬返回。

**改动**：在现有两级查找全部 miss（`ErrRecordNotFound`）后，**新增最后一级 service_type-agnostic 回退**：按 `(provider, modelKey)`（及 provider-model-ID 形式）查"该模型唯一的一条 active 定价"，无视 service_type。
- 新增 store 方法 `GetPricingRuleByModel(ctx, provider, modelKey) (*PricingRule, error)`：`WHERE provider=? AND model=? AND is_active=1 ORDER BY id DESC LIMIT 1`（GORM query builder，不写 raw SQL）。**（S2 review P2-2）`ORDER BY id DESC`**（取最新行）而非 ASC——多 active 行时偏向最新配置；并在命中后若检测到 >1 行（`Find` 取 count）打 warn 日志，提示运营该模型有多条 active 定价需澄清。
- `resolvePricingRule` 末尾：直接 key miss → provider-model-ID miss → **`GetPricingRuleByModel(provider, modelKey)` + `GetPricingRuleByModel(provider, providerModelID)`**；命中则 cache。
- **（S2 review P1-1）agnostic cache key 格式**：用固定专用前缀 **`"agnostic|"+provider+"|"+model`**（不与任何真实 service_type 碰撞，禁止用 `cacheKey("", provider, model)` 生成 `"|provider|model"` 这种语义不清的 key）。代码注释说明此前缀专用于 service_type-agnostic 回退结果。
- **顺序保证**：精确 `(serviceType,provider,model)` 命中优先（现有逻辑不变），agnostic 回退**只在精确 miss 时**兜底 → 专用视觉模型（qwen-vl/doubao-vision，本就有自己的 llm_vision 行）仍精确命中、零回归。

**效果**：`(llm_vision, dmxapi, claude-opus-4-6)` 精确 miss → 回退 `(dmxapi, claude-opus-4-6)` → 命中 llm_chat 行（24.82/124.1）→ 对账算出真实 ~12 积分。

**不在本 fix 范围**：prod `ali` vs 定价规则 `ali-dashscope` 的 **provider 名不匹配**（agnostic 回退仍按 provider 匹配，名字不同仍 miss）→ 记 follow-up（provider 名归一化，独立问题，prod 下少收非致命）。

### 2.2 Fix ② — bill-only 预扣冷启动合理化（`.../middleware/context_budget.go` `synthBillOnlyResult`）

**现状**：`reserved := route.Capability.MaxOutputTokens / 2`（claude 128K → 64000）。`effectiveCompletionTokens` 用历史均值但**上限被这个 64000 卡住**，冷启动（无历史）直接返回 64000。

**改动**：把冷启动上限从 `MaxOutputTokens/2` 降到合理默认：
```go
const defaultBillOnlyReservedOutputTokens = 8192 // 覆盖绝大多数 completion；reconcile 退到实际
reserved := route.Capability.MaxOutputTokens / 2
if reserved <= 0 || reserved > defaultBillOnlyReservedOutputTokens {
    reserved = defaultBillOnlyReservedOutputTokens
}
if reserved > route.Capability.MaxOutputTokens && route.Capability.MaxOutputTokens > 0 {
    reserved = route.Capability.MaxOutputTokens
}
```
- 估算器（`effectiveCompletionTokens`）仍在上层运行：**有历史 → 返回 min(历史均值, 8192) ≈ 真实**（预扣贴近实际，满足用户"非常接近"诉求）；**冷启动 → 8192**（安全上界，远小于 64000）。
- 8192 选取理由：覆盖绝大多数对话/agent completion；claude 价下 8192 输出冷启动预扣 ≈ 1 元/100 积分（vs 今天 7.96 元/796），且 reconcile 立即退到实际。预扣只是临时 hold，真实账单由 reconcile（fix ①后正确）决定。
- **（S2 review P2-1）长文输出模型说明**：8192 是**冷启动上界**，不影响有历史数据的场景——`effectiveCompletionTokens` 历史均值路径照常给出贴近真实的预扣（用户"非常接近"诉求靠估算器满足）。仅历史均值 >8192 的长文模型会被封到 8192（仍远小于 64000，且 reconcile 修正）。
- **欠费风险评估**：预扣是 pre-auth，reconcile 多退少补。预扣调小不会导致欠费——reconcile 时余额不足由既有扣减逻辑处理（最多扣到 0），且单次 chat completion 极少超 8192；真超了 reconcile 据实补扣（用户确有余额才补）。
- **同时受益 agent 模式**（也走 bill-only，今天同样 64000 冷启动过度冻结）。

### 2.3 Fix ③ — 对账缺价兜底安全化（`.../middleware/context_budget.go`）

**现状**：`buildBaseFinalizeInput` 设 `EstimatedCredits = int64(result.Policy.ReservedOutputTokens)`（token 数当积分）；`finalizeReservationIfNeeded` 在 holder 未 Set（缺价）时 `actualCredits = fi.EstimatedCredits` → 扣 64000。

**改动**（三管）：

1. **消除单位错误 + 保住观测性（S2 review P2-3）**：`buildBaseFinalizeInput` 的 `EstimatedCredits` **不再** = `int64(ReservedOutputTokens)`（token 数当积分，危险）。改为 = **真实预扣积分额**（`precheck.EstimatedCredits` / `rsv.ReservedCredits`，是积分值不是 token 数）。
   - 需让 `doReserveBudget` 返回预扣积分额：签名从 `(uint64, error)` 改为 `(reservationID uint64, reservedCredits int64, err error)`；`buildBaseFinalizeInput(result, reservationID, reservedCredits)` 用它填 `EstimatedCredits`。
   - **为什么这样更对**：`context_budget_event` 的 `ReserveAmount`（`biz.go:504 if EstimatedCredits>0`）和 `ReconcileDelta = PricingCostCents − EstimatedCredits` 都依赖 `EstimatedCredits`。改 0 会丢观测性；改成真实预扣额则 ReserveAmount=真实预扣、ReconcileDelta=实际成本−预扣（两边都是积分/分单位，delta 才有意义）。
   - **且 `EstimatedCredits` 在 fix ③后不再被当扣费值**（缺价走退款，见下），所以它纯观测、永不导致乱扣。

2. **缺价即退款 + 告警**：`finalizeReservationIfNeeded` 区分 holder 已 Set vs 未 Set：
   ```go
   costResolved := false
   actualCredits := int64(0)
   if holder := finalCostHolderFromCtx(ctx); holder != nil {
       if c, ok := holder.Get(); ok { actualCredits = c; costResolved = true }
   }
   if !costResolved {
       // holder 未 Set：绝不编造最坏值扣费 → 退款。区分两种成因的日志级别（P1-2）。
       hasUsage := fi.ActualPromptTokens > 0 || fi.ActualCompletionTokens > 0
       if fi.CalibrationSkipped || !hasUsage {
           // 流中断/无 usage 数据：良性，无法计费，静默退款（warn 级，非定价问题）。
           deps.warnw("reconcile: no usage data — refunding (benign)", "reservation_id", fi.ReservationID, ...)
       } else {
           // 有 token 用量却查不到价 → 真定价配置缺失，ERROR 告警让运营补价（fix ①后极少触发）。
           deps.errorw("reconcile: pricing unavailable despite usage — REFUNDING, config error", "reservation_id", fi.ReservationID, "operation", ..., "prompt_tokens", fi.ActualPromptTokens, "completion_tokens", fi.ActualCompletionTokens)
       }
       deps.CreditService.Refund(ctx, fi.ReservationID, "pricing_unavailable_or_no_usage")
       return
   }
   deps.CreditService.FinalizeReservation(ctx, fi.ReservationID, actualCredits, "context_budget_reconcile")
   ```
   - **（S2 review P1-2）区分两种 holder-未-Set 成因**：① `CalibrationSkipped`/无 token 用量（流中断）= 良性 → warn 级退款，不误报"定价问题"；② 有 token 用量但无价 = 真定价配置错误 → ERROR 告警。两者都退款（用户绝不被乱扣），只是日志语义不同，避免流中断刷假定价告警。

- 原则：**对账缺价时，宁可少收（退款）也绝不乱扣**；真定价缺失必须 ERROR 告警让运营补价（fix ①让真实模型几乎不会缺价）。
- 与现有 refund 路径（用户取消/provider_err，`fi.Refund=true`）不冲突——那些在上游 return，不进此分支；此分支只命中"对账成功但 holder 未 Set"。
- **F-7 兼容**：holder.Set(0)（0/0 定价规则的合法 0 成本）走 `costResolved=true` → 扣 0，不退款（保持既有语义）。

### 2.4 不改 / 边界

- `classifyServiceType`（带图→llm_vision）**保留**——它对 usage 分析有价值（统计"处理了图片"），只是不再当定价主键。
- 不动 reserve/reconcile 的两阶段框架、不动三池扣减优先级。
- 无 DB schema 变更、无新 API、无新外部服务。
- prod provider 名不匹配 → follow-up。

## §3 验证策略（S5，rule 10）

**验证方式**：Go 单测（持久回归核心）+ dev 端到端（计费是高风险，必须实跑）。
**理由**：bug-from-customer 碰计费，三处逻辑都必须有 Go 单测永久锁死；dev 真实发图验证端到端账单正确。

**复现测试（Rule 11，第一个 commit，当前 FAIL）**：
- `TestReconcile_PricingMiss_DoesNotChargeWorstCase`：holder 未 Set（缺价）时，对账**不应**扣 `ReservedOutputTokens`（64000）——当前会扣（FAIL），fix ③后退款（PASS）。
- `TestResolvePricingRule_VisionFallsBackToChat`：`(llm_vision, dmxapi, claude-opus-4-6)` 无 vision 价但有 llm_chat 价时，应解析到 llm_chat 价——当前 ErrRecordNotFound（FAIL），fix ①后命中（PASS）。
- `TestSynthBillOnlyReserve_NotHalfWindow`：128K 模型 bill-only 预扣不应是 64000——当前是（FAIL），fix ②后 ≤8192（PASS）。

**S5 dev 关键路径**：
1. chatbot 选 claude-opus-4-6 发图 → 账单 ~10 量级积分（不再 64000/796）；查 credit_reservation actual_cost_cents 接近真实。
2. 专用视觉模型（qwen-vl/doubao）发图 → 定价仍正确、不回归。
3. 预扣值（reserved_credits）与对账值（actual_cost_cents）同量级。
4. agent 模式发图（如适用）→ 同样正确。

## §4 涉及文件

- 改：`internal/pkg/pricing/pricing.go`（resolvePricingRule 加 agnostic 回退 + agnostic cache key）+ pricing store 接口/实现（新增 `GetPricingRuleByModel`）
- 改：`internal/pkg/aiservice/middleware/context_budget.go`（synthBillOnlyResult 预扣默认 8192；`doReserveBudget` 返回 reservedCredits；buildBaseFinalizeInput EstimatedCredits=真实预扣额；finalizeReservationIfNeeded 缺价退款+区分告警）
- 验证：`internal/numind/biz/contextbudget/biz.go:504` 的 ReserveAmount/ReconcileDelta 在 EstimatedCredits=真实预扣额后正确写入（观测性保持）——无需改代码，但 plan 要有验收点
- 新增测试：pricing 包 + middleware 包（含 3 个复现测试：PricingMiss 不扣最坏值 / Vision 回退 chat 价 / bill-only 预扣不是半窗口）
- 不改：classifyServiceType、reserve/reconcile 框架、DB schema、三池扣减优先级
- follow-up（不在本 feature）：prod provider 名 `ali` vs 定价 `ali-dashscope` 归一化
