# 免费模型仅限会员、零扣费 — 实施计划（S3）

> 输入：spec.md（C1–C7 + 错误码 + §8 修正）。单仓库 numind-server。后端 only。
> 全程 TDD（RED→GREEN→REFACTOR）。每个 task 完成后并行双 Sonnet reviewer（spec-compliance + code-quality），P0/P1 必修，再进下一 task。

## 依赖图（无环）
```
T1(errno) ─┐
T2(pricing.IsFreeModel) ─┤
T3(membership.IsActiveMember) ─┤
                               └→ T4(credit 服务层门+接口) ─┬→ T5(中间件 C7)
                                                            └→ T6(SOP CreateRun)
T7(S5 验证策略) 独立
```
T1/T2/T3 互相独立（可并行 Tier 3，但本 session 串行实现+逐 task review，避免同 worktree git race）。T4 依赖 T1+T2+T3。T5/T6 依赖 T4。

---

## T1 — 新增错误码 `ErrModelMembershipOnly`
- **文件**：`internal/pkg/errno/credits.go`
- **改动**：新增 `ErrModelMembershipOnly = &Errno{HTTP: 403, Code: "Model.MembershipOnly", Message: "该模型仅限会员使用，请先开通会员"}`
- **TDD**：errno 是数据声明，加一个最小测试断言其 HTTP=403/Code 正确（或并入 T4 的错误穿透测试）。
- **验收**：编译通过；`errno.Decode(fmt.Errorf("%w", errno.ErrModelMembershipOnly))` 返回 (403, "Model.MembershipOnly", msg)。
- **依赖**：无。

## T2 — `pricing.IsFreeModel` + ICalculator 接口 + mock
- **文件**：`internal/pkg/pricing/pricing.go`（impl + 接口 `ICalculator`）；`internal/pkg/pricing/*_test.go`；**所有 ICalculator 实现/mock**（S4 先 `grep -rn "ICalculator" --include=*.go` + 找实现 `CalculateCost` 的 mock 类型，全部加 `IsFreeModel`）。
- **改动**：
  - 接口 `ICalculator` 加 `IsFreeModel(ctx, serviceType, provider, model string) (bool, error)`。
  - `*calculator` 实现：`resolvePricingRule` → 命中且 `InputPricePerMTok==0 && OutputPricePerMTok==0 && PricePerCall==0` 且非 tiered → true；`ErrRecordNotFound` → (false,nil)；tiered(`BillingMode=="tiered_token"`) → false；其它 err → (false,err)。
- **TDD（RED 先写）**：表驱动——全0 rule→true / 部分非0→false / not-found→false / tiered→false / DB error→(false,err)。
- **验收**：`go test ./internal/pkg/pricing/...` 绿；所有 ICalculator mock 编译通过。
- **依赖**：无。

## T3 — `membership.IsActiveMember`（按有效期）
- **文件**：`internal/numind/biz/membership/state.go`；`internal/numind/biz/membership/*_test.go`
- **改动**：新增 `IsActiveMember(ctx, userID uint64, now time.Time) (bool, error)`：sub 未过期 OR trial 未过期（**忽略 CreditsRemaining**），**store 错误必须传播**（spec §8 P0-1）。
- **TDD**：sub 在期→true / trial 在期且 CreditsRemaining=0→true（关键 AC2）/ 全过期→false / 无任何记录→false / store 报错→(false,err)。
- **验收**：`go test ./internal/numind/biz/membership/...` 绿。
- **依赖**：无。

## T4 — credit 服务层免费门 + ICreditService 接口扩张（核心）
- **文件**：`internal/numind/biz/credit/credit_service.go`、`internal/numind/biz/credit/contracts.go`、`internal/numind/biz/credit/*_test.go`；**所有 ICreditService 实现/mock**（S4 grep 找全）。
- **改动**：
  1. `ICreditService` 加：`IsActiveMember(ctx, userID uint64) (bool, error)`、`EnforceModelMembership(ctx, userID uint64, provider, model string) error`。
  2. `creditService` 实现二者：`IsActiveMember`→`s.membershipSvc.IsActiveMember(ctx,userID,now)`；`EnforceModelMembership`→ `IsFreeModel("llm_chat",provider,model)` 非 free 返 nil；free 时 `IsActiveMember` 非会员返 `ErrModelMembershipOnly`、会员返 nil；内部 err → fail-open nil（spec §8 P0-3）。
  3. 抽 helper `applyFreeModelGate(ctx, user, provider, model, balProvider) (*PreCheckResult, handled bool, err error)`：free+非会员→(nil,true,ErrModelMembershipOnly)；free+会员→(SkipDeduction PreCheckResult, true, nil)；非 free 或判定 err→(nil,false,nil) 让调用方走正常逻辑。nil-safe deref balance。
  4. 在 `CheckAndEstimateBudget`（C3，PoolAdminTest 分支**之后**）调 helper：handled 则直接返回。
  5. 在 `creditsImpl.CheckAndEstimate`（C4，GetBalance 后、EstimateCredits 前）调同款 helper。
- **TDD**：表驱动覆盖 spec §3 真值表全行 ×（gateway + 默认）两入口：会员0额+0价→SkipDeduction/Sufficient/Estimated=0；非会员+0价→ErrModelMembershipOnly；任意+收费足额→正常；任意+收费不足→ErrInsufficientCredits；查不到价→走正常；membership err→降级正常。`EnforceModelMembership` 单测（free非会员→err / free会员→nil / 收费→nil / err→nil）。
- **验收**：`go test ./internal/numind/biz/credit/...` 绿；全部 ICreditService mock 编译通过；`task lint` 绿。
- **依赖**：T1、T2、T3。

## T5 — 中间件 C7（ChargeUser 无关的免费用户拦截）
- **文件**：`internal/pkg/aiservice/middleware/context_budget.go`；`internal/pkg/aiservice/middleware/*_test.go`
- **改动**：在 `if result.Policy.ChargeUser ...`（L483）**之前**，对 chat 请求 + userID!=0 调 `deps.CreditService.EnforceModelMembership(ctx, userID, route.Provider.Name, route.ServiceKey)`，非 nil 则 `return nil, fmt.Errorf("ContextBudgetCredits: %w", err)`。
- **TDD（关键回归）**：`ChargeUser=false` + 0价模型 + 非会员 → 仍返回 ErrModelMembershipOnly（锁 AC3 airtight）；`ChargeUser=false` + 0价 + 会员 → 放行不扣；收费模型 → EnforceModelMembership no-op 不影响。用 fake CreditService。
- **验收**：`go test ./internal/pkg/aiservice/middleware/...` 绿。
- **依赖**：T4（EnforceModelMembership on ICreditService）。

## T6 — SOP `CreateRun` 会员感知放宽
- **文件**：`internal/numind/biz/sop/sop.go`（L385-403）；`internal/numind/biz/sop/*_test.go`
- **改动**：粗检改为 `member, err := b.creditSvc.IsActiveMember(...)`；非会员（或判定 err 保守按非会员）才做 `totalRemain<=0` 拦截；会员跳过粗检。
- **TDD**：会员 + 0 余额 → CreateRun 成功（不被粗检拦）；非会员 + 0 余额 → ErrInsufficientCredits；会员判定 err → 保守按非会员（0 余额被拦）。mock creditSvc。
- **验收**：`go test ./internal/numind/biz/sop/...` 绿。
- **依赖**：T4（IsActiveMember on ICreditService）。

## T7 — S5 验证策略（Rule 10，无代码）
- **验证方式**：**Go 单元测试为主（持久化回归）** + `task test`（完整版含 race+coverage）+ 本地 `task build` 编译冒烟。**不做 Playwright/gstack E2E。**
- **理由**：本 feature 是后端计费/权限**纯决策逻辑**，无前端改动；决策矩阵是 biz 层可 mock 的纯逻辑，表驱动单测能完整覆盖 AC1–AC7 且**永久回归保护**（符合 testing.md：biz 权限/计费→mock store 单测）。gstack /qa 需在运行环境播种"0 价模型+会员/免费用户"，setup 成本高、一次性、无回归价值——对高风险计费逻辑反而是 Rule 10 点名要避免的"无持久回归保护"。
- **关键验证路径（由 T2–T6 单测覆盖）**：
  1. 会员0额+0价模型 不扣分可用（AC1）；trial 0 积分会员可用（AC2）。
  2. 免费用户+0价模型 被 403 拦（AC3），含 ChargeUser=false 回归（T5）。
  3. 任意用户+收费模型 行为不变（AC4 回归）。
  4. 查不到价 ≠ 免费（AC5）。
  5. 接口扩张后全部 mock 编译通过（无遗漏实现）。
- **诚实声明**：选单测=该功能未来修改有自动回归保护；无浏览器端到端，但因无 UI 改动，端到端价值低。若 S4 发现错误码在某前端入口被吞/跳登录（spec §1.4 已确认 403≠401 不跳登录、errtranslate 正常透传），再追加最小验证。
- **依赖**：无（计划项）。

## S4 执行顺序
T1 → T2 → T3 →（三者各自 TDD+双 reviewer 后）→ T4 → T5 ‖ T6（可 Tier 3 并行，但本 session 串行）→ 全绿后 S5。
每 task：RED→GREEN→REFACTOR → `go test ./改动包/...` + `task lint` → 并行双 Sonnet reviewer → 修 P0/P1 → 更新 manifest progress.reviewed_tasks。

---

## §S3 Gate Review 修正（Sonnet 原子性审查 PASS_WITH_CONCERNS，2026-06-11）
审查 verdict=PASS_WITH_CONCERNS；覆盖矩阵 C1–C7 / AC1–AC7 全覆盖、依赖无环、T7 验证策略合理。但发现接口/ mock 枚举不全会破坏"每 task 编译绿"。**以下为 S4 必须遵守的精确清单**：

### P0-A（修）：中间件 `deps.CreditService` 是 **`ContextBudgetCreditService`**（窄接口，context_budget.go），不是 `ICreditService`
T5 调 `EnforceModelMembership` 前，必须：
- **T5** 在 `ContextBudgetCreditService` 接口（`internal/pkg/aiservice/middleware/context_budget.go`）加 `EnforceModelMembership(ctx, userID uint64, provider, model string) error`。
- **T5** 在 `creditServiceFacade`（`internal/numind/numind.go`，即实际传入中间件的实现）加 delegation 方法。
- **T5** 更新中间件测试 mock：`mockCreditService`（context_budget_test.go）、`poolCapturingCreditService` + `ctxCheckingCreditService`（bill_only_integration_test.go）加该方法。

### P0-B（修）：`ICalculator` 加 `IsFreeModel` → **6 个实现/mock 全改**（否则 5 个包编译失败）
T2 文件清单显式锁定：
1. `*calculator` — `internal/pkg/pricing/pricing.go`（真实现）
2. `fakePricing` — `internal/numind/biz/agent/budgetgate/gate_units_test.go`
3. `spyPricingCalc` — `internal/numind/biz/salesrag/salesrag_credits_integration_test.go`
4. `mockPricer` — `internal/numind/biz/budget/r2_estimator_test.go`
5. `mockPricingCalc` — `internal/pkg/aiservice/middleware/billing_test.go`
6. `spyCalculator` — `internal/pkg/billing/recorder_test.go`
（S4 仍跑 `grep -rn "ICalculator\|func.*CalculateCost" --include=*.go` 复核，防漏。）

### P0-C（修）：`ICreditService` 加 2 方法 → **显式 stub 全改**
T4 文件清单显式锁定（这两个是"显式实现全部方法"的 stub，不加新方法即编译失败）：
1. `creditService` — `internal/numind/biz/credit/credit_service.go`（真实现：`IsActiveMember`+`EnforceModelMembership`）
2. `creditServiceFacade` — `internal/numind/numind.go`（若它被当 `ICreditService` 用则需两法；中间件用到的 `EnforceModelMembership` 见 T5；为安全两法都补 delegation）
3. `stubCreditSvc` — `internal/numind/controller/v1/credit/credit_test.go`
4. `stubCreditService` — `internal/numind/biz/agent/tool_image_gen_billing_test.go`
- `zeroBalanceCreditSvc`（sop_test.go）用 **embedding `struct{ creditbiz.ICreditService }`** → 编译安全（新方法走嵌入接口，未实现则运行时 panic；T6 测试若需调 IsActiveMember 须在该 stub 显式实现，避免 panic）。
（S4 仍跑 `grep -rn "ICreditService" --include=*.go` 复核。）

### 修正后任务-接口归属表（S4 据此，保证逐 task 编译绿）
- **T2**：`ICalculator.IsFreeModel` + 上述 6 实现。
- **T4**：`ICreditService.{IsActiveMember,EnforceModelMembership}` + `creditService` 实现 + `creditServiceFacade` 两法 delegation + `stubCreditSvc` + `stubCreditService`。
- **T5**：`ContextBudgetCreditService.EnforceModelMembership` + 3 中间件 mock + 调用点（ChargeUser 之前）。
- **T6**：`zeroBalanceCreditSvc` 若被 IsActiveMember 调用则显式实现该法。

P1/P2（测试断言显式化、`time.Now().UTC()` 注入）已并入各 task TDD 要求。**修正后 verdict 视为 PASS**，进 S4。
