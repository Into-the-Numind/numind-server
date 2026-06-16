# multimodal-billing-fix — 实施计划（S3）

> feature `multimodal-billing-fix` · 2026-06-17 · 蓝本 spec.md · 仅 numind-server
> code task = T1–T3，T4 = S5 验证策略。主 session 顺序实现 + 每 task 双 reviewer。
> **Bug-from-Customer（Rule 11）：每个 task repro-test-first（红 commit）→ fix（绿 commit）。**

## 依赖图
```
T1(定价 agnostic 回退) ── 独立(pricing 包)
T2(bill-only 预扣 8192) ──┐ 同文件 context_budget.go, 串行
T3(对账缺价退款+观测性) ──┘
T1..T3 ──► T4(S5 验证策略)
```
T1 在 pricing 包，与 T2/T3 完全 disjoint。T2/T3 同改 `context_budget.go` 不同函数 → **主 session 串行实现**（不并行 dispatch implementer，避免同文件 race）。

---

## T1 — Fix ① 定价按 (provider, model) 解析（chain 1，管"钱"）

> **（S3 review P1 + S4 scope 收敛）** 代码有**三处**重复定价解析。S4 实现时核实：**钱全走 chain 1**——
> - **chain 1** `pricing.resolvePricingRule`（pricing.go:241，自带 LRU cache）→ pricing.ICalculator → reconcile 扣费 **且** `usage_record.cost_cents`（recorder.go:372 `r.calc.CalculateCostWithCache`）。**修这一处，钱（对账+cost_cents）全对。**
> - **chain 2** `middleware/billing.go:311`（dbUsageStore）+ **chain 3** `billing.ResolvePricingRule`（recorder.go:57）→ 仅写 `pricing_*_snapshot` **审计列**（miss 时 nil，非致命，不影响扣费）。
>
> **scope 决策**：加 `GetPricingRuleByModel` 到 3 个接口需触及 3 接口 + 2 DB 实现(billingStore/dbUsageStore) + 6 测试桩 ≈ 11 处，为**审计列**铺这么多跨包管线、且固化"3 个重复 resolver"的债 → 不划算。**T1 只修 chain 1（钱）**；chain 2/3 snapshot + **resolver 合并** 降为 follow-up（reviewer 允许"记录 known gap"）。snapshot 在 vision→claude 下仍可能 NULL（已知、非钱、cost_cents 正确）。

**涉及文件**：`internal/pkg/pricing/pricing.go`（resolvePricingRule）+ `internal/pkg/pricing/`（PricingStore 接口加 `GetPricingRuleByModel`）+ `internal/numind/store/billing.go`（billingStore 实现）+ `pricing_test.go`（stub + 测试）

**repro test（红，已提交 df03d5ad）**：`TestCalculateCost_VisionFallsBackToChat`——seed `(llm_chat, dmxapi, claude-opus-4-6)` 价但无 `(llm_vision, …)`；`CalculateCost("llm_vision", …)` 当前 ErrRecordNotFound（FAIL）→ fix 后解析到 chat 价、cost=12 分。

**实现**：
- `PricingStore` 接口 + `stubPricingStore` + `billingStore` 加 `GetPricingRuleByModel(ctx, provider, modelKey) (*model.PricingRule, error)`：GORM `Where("provider=? AND model=? AND is_active=?", …, true).Order("id DESC").Find`；>1 行打 warn 取最新。
- `resolvePricingRule` 末尾（directKey miss + providerModelID miss 之后）加 agnostic 回退：`GetPricingRuleByModel(provider, modelKey)` → 仍 miss 再 `(provider, providerModelID)`；命中用 agnostic cache key `"agnostic|"+provider+"|"+model` 缓存（不与带 service_type 的 key 碰撞）。
- 注释说明 agnostic 回退只在精确 miss 时兜底；标注 chain 2/3 snapshot + resolver 合并 = follow-up。

**验收**：repro test 转绿（cost=12）；`go test ./internal/pkg/pricing/... ./internal/numind/store/...` 全绿（专用视觉模型精确命中不回归——加 `TestCalculateCost_VisionModelExactStillWins`）；`task lint` 0；`go build ./...` 0（接口新增方法需 billingStore 同步实现，编译即验证）。

**follow-up（记入 manifest）**：chain 2/3 snapshot 审计列对 vision→统一模型仍 NULL（非钱）；3 个重复 resolver 应合并为单一入口。

---

## T2 — Fix ② bill-only 预扣冷启动 8192（middleware 包）

**涉及文件**：`internal/pkg/aiservice/middleware/context_budget.go`（`synthBillOnlyResult`）+ `context_budget_test.go`

**repro test（红）**：`TestSynthBillOnlyReserve_NotHalfWindow`——构造 MaxOutputTokens=128000 的 route，调 `synthBillOnlyResult`，断言 `Policy.ReservedOutputTokens <= 8192`（当前 = 64000，FAIL）。

**实现**：
- `synthBillOnlyResult` 内：`reserved := MaxOutputTokens/2`；`if reserved<=0 || reserved>defaultBillOnlyReservedOutputTokens { reserved = defaultBillOnlyReservedOutputTokens }`；`if MaxOutputTokens>0 && reserved>MaxOutputTokens { reserved=MaxOutputTokens }`。
- 新增包级常量 `const defaultBillOnlyReservedOutputTokens = 8192`，注释说明：冷启动上界，估算器有历史时返回贴近真实值，此值只封冷启动 + 长文模型上界。

**验收**：repro test 转绿；既有 bill-only 相关测试不回归（小窗口模型 MaxOutputTokens<8192 时 reserved=MaxOutputTokens，加用例）；`go test ./internal/pkg/aiservice/middleware/...` 绿；`task lint` 0。

---

## T3 — Fix ③ 对账缺价退款 + 消单位错误 + 保观测性（middleware 包）

**涉及文件**：`internal/pkg/aiservice/middleware/context_budget.go`（`doReserveBudget` / `buildBaseFinalizeInput` / `finalizeReservationIfNeeded`）+ `context_budget_test.go`；验收点核对 `internal/numind/biz/contextbudget/biz.go:504`

**repro test（红）**：`TestReconcile_PricingMiss_RefundsNotWorstCase`——构造 reservationID>0、holder 未 Set（模拟缺价）、fi 有 token 用量；当前 `finalizeReservationIfNeeded` 会 `FinalizeReservation(actualCredits=EstimatedCredits=ReservedOutputTokens=大数)`（FAIL：断言应 Refund 且不出现大额 FinalizeReservation）。用 fake CreditService 记录 Refund/FinalizeReservation 调用。

**实现**（三管，spec §2.3）：
1. `doReserveBudget` 签名 `(uint64, error)` → `(reservationID uint64, reservedCredits int64, err error)`；返回 `precheck.EstimatedCredits`（SkipDeduction/无 reservation 时 reservedCredits=0）。更新唯一调用点（line 511）。
2. `buildBaseFinalizeInput(result, reservationID, reservedCredits)` 的 `EstimatedCredits = reservedCredits`（真实预扣积分额，非 token 数）。**（S3 review P2）`buildBaseFinalizeInput` 只有一处调用点（line 607），`baseFI` 随后被流式(619)/非流式(624 `fi:=baseFI`)结构体赋值继承——改 607 签名即两路径都正确，无需找"第二处"。**
3. `finalizeReservationIfNeeded` holder 未 Set 分支：区分 `CalibrationSkipped||!hasUsage`（warn 退款）vs 有用量缺价（ERROR 告警退款），都 `Refund`；holder 已 Set（含 0）走原 `FinalizeReservation`。

**验收**：
- repro test 转绿；新增 `TestReconcile_PricingResolved_ChargesActual`（holder Set 30 → FinalizeReservation(30) 不回归）+ `TestReconcile_CalibrationSkipped_RefundsWarnNotError`（无用量 → warn 退款）。
- **观测性验收点**：确认 `biz.go:504 if EstimatedCredits>0` 在 EstimatedCredits=真实预扣额后仍写 ReserveAmount/ReconcileDelta（EstimatedCredits>0 成立）；若发现 reservedCredits 可能为 0 的合法预扣场景导致漏写，则把 gate 改 `ReservationID>0`（plan 执行时核对，记入 review）。
- `go test ./internal/pkg/aiservice/middleware/... ./internal/numind/biz/contextbudget/...` 绿；`task lint` 0。

---

## T4 — S5 验证策略（rule 10）

**验证方式**：Go 单测（持久回归核心，3 个复现测试 + 不回归用例）+ **dev 端到端**（计费高风险，必须实跑）。
**理由**：bug-from-customer 碰计费，三处逻辑必须 Go 单测永久锁死；定价/预扣/对账的端到端正确性必须 dev 真实发图验证（单测 mock 不了完整网关链路）。非纯前端，无需 Playwright；后端 TDD + dev 实跑。
**S5 关键路径**：
1. chatbot 选 claude-opus-4-6 发图 → 查 credit_reservation：actual_cost_cents ~10 量级（不再 64000/796）、reserved_credits 与之同量级、status=reconciled、delta 小。
2. 专用视觉模型（qwen3-vl-flash 若 dev 可选）发图 → 定价精确命中、不回归。
3. 构造缺价（可选：临时停用某模型定价行）→ 对账退款 + 日志 ERROR 告警，绝不大额扣。
4. **（S3 review P1 澄清）** `usage_record.cost_cents`（经 calc/chain1，T1 修）与 reservation actual_cost_cents 一致且为真实值；`pricing_input_snapshot/output_snapshot`（chain2/3，T1 修后）对 llm_vision→claude 也写入真实价（不再 NULL）。
5. 退积分前后对照（前面已退 4758，验证新逻辑下不再发生大额错扣）。

**回归保护诚实声明**：本 feature 用 Go 单测（持久）+ dev 手动 /qa 实跑。计费核心逻辑（定价解析/对账兜底/预扣）有 Go 单测永久覆盖；dev 端到端是一次性验证，未来改动靠 Go 单测回归。

---

## 边界 / 并发
- 活跃 feature `agent-mode-ux-repair`(numind-web-v3) 与本 feature 不同仓库，零冲突。
- T1(pricing 包) 与 T2/T3(middleware 包 + 同文件) disjoint；T2/T3 同文件主 session 串行。
- follow-up（不在本 feature）：prod provider 名 `ali` vs `ali-dashscope` 归一化。
