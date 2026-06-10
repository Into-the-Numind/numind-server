# 免费模型仅限会员、零扣费 — 技术设计 Spec

> 日期：2026-06-11 ｜ Feature: free-model-member-only ｜ Track: Standard (S2)
> 输入：proposal.md §4 PRD（AC1–AC7）。本 spec 必须覆盖全部 AC。

## §0 一句话设计
在**信用预扣的两个入口**（gateway 路径 `creditService.CheckAndEstimateBudget` + 默认 R2 路径 `creditsImpl.CheckAndEstimate`）加一道"免费模型门"：解析到的模型若是 0 价，则**会员跳过预扣（零扣费），非会员拒绝（`ErrModelMembershipOnly` 403）**；非 0 价模型走原逻辑不变。配套放宽 SOP `CreateRun` 的粗粒度余额预检，使 0 余额会员能创建运行。

## §1 取证结论（S2 待查项已查实）
1. **拦截点 = 唯一的 chat-reserve**：`ContextBudgetCredits` 中间件（`internal/pkg/aiservice/middleware/context_budget.go`）只对 **带 ContextFragments 的 ChatRequest** 调 `doReserveBudget`（L405-406 的 `!billOnly && len(ContextFragments)==0` 直接 passthrough；L484 `ChargeUser` 才进）。
   - `Embed` / `Rerank` 经 `asChatReq` 守卫被 passthrough（L390-394），**从不扣积分、从不拦截**，仅 Billing 中间件记 UsageRecord（可观测）。
   - salesrag 的 query-rewrite（intent 分析）是 Chat 但**无 ContextFragments** → 同样 passthrough，**不扣不拦**。
   - **结论**：一次 salesrag query 里，唯一会因积分被拦的子调用是**主回复 chat**（带 fragments）。embed/rerank/rewrite 对所有用户都是 uncharged 现状。
2. **per-call 语义落点**：因此本 feature 的"per-call 决策"作用在**会被预扣的 chat 调用**上（gateway with-fragments + 默认路径）。中途 embed/rerank/rewrite 不在积分链上 → 既不需要免费豁免，也本就不会"拦积分不足"。**此为现状，不在本 feature 扩张**（给 embed/rerank 加预扣是独立的大改，且它们刻意 uncharged）。AC6 据此收敛为"chat 类被预扣的子调用"。
3. **依赖已就位**：`creditService{pricing, membershipSvc, credits *creditsImpl}`（credit_service.go:47-59），`creditsImpl{pricing, membershipSvc, estimation}`（:522-528）。两个入口都能直接拿到 pricing + membership。
4. **错误传播无损**：`errno.*Errno` 经 `%w` 链 → 非流式 `core.WriteResponse`→`errno.Decode`（`errors.As`）→ 原 HTTP code+message；流式 `errtranslate.FriendlyForSSE`→`ToErrno`（`errors.As`）→ `.Message`。无"统一改写成积分不足"的拦截。
5. **会员判定坑**：`membership.GetMembershipState` 的 `TrialActive = trial!=nil && ExpiresAt.After(now) && CreditsRemaining>0`——**含余额条件**。PRD（AC2）要 trial 积分用光也算会员 → 本 feature **不复用 TrialActive**，改用**有效期判定**。

## §2 改动清单（4 处代码 + 1 错误码 + 测试）

### C1. 新增 `pricing.IsFreeModel`
**文件**：`internal/pkg/pricing/pricing.go`（+ 接口 `ICalculator`）。
**签名**：`IsFreeModel(ctx context.Context, serviceType, provider, model string) (bool, error)`
**语义**：
- 走 `resolvePricingRule(ctx, serviceType, provider, model)`（已有，带 5min LRU 缓存）。
- rule 命中且**价格分量全 0** → `(true, nil)`：
  - 普通模式：`InputPricePerMTok==0 && OutputPricePerMTok==0 && (PricePerCall 为 0 或 nil)`。
  - tiered 模式（`BillingMode=="tiered_token"`）：所有 tier 的单价为 0（保守起见，tiered 一律按"非免费"处理，除非能廉价确认全 0；S4 决定：tiered → 返回 false，避免误判，0 价模型不会用 tiered 计费）。
- rule 未命中（`gorm.ErrRecordNotFound`）→ `(false, nil)`：**查不到 ≠ 免费**（AC5）。
- 其它 DB 错误 → `(false, err)`：调用方据此 fall through 到正常计费（不免费、不拒绝）。
**理由**：单点、可缓存、无 token 入参。serviceType 固定传 `"llm_chat"`（这些是 LLM chat 模型）。

### C2. 新增 `membership.MembershipService.IsActiveMember`（按有效期）
**文件**：`internal/numind/biz/membership/state.go`。
**签名**：`IsActiveMember(ctx context.Context, userID uint64, now time.Time) (bool, error)`
**语义**：`sub 未过期 || trial 未过期`，**忽略剩余积分**：
- `sub, _ := store.Subscriptions().Get(...)`；`subOK = sub!=nil && sub.ExpiresAt.After(now)`（与 `SubActive` 同义）。
- `trial, _ := store.TrialGrants().Get(...)`；`trialOK = trial!=nil && trial.ExpiresAt.After(now)`（**去掉** `CreditsRemaining>0`）。
- `return subOK || trialOK, nil`。
**理由**：AC2。不改 `GetMembershipState`（其语义被余额展示等多处依赖），新增独立方法避免回归。2 次 DB 读，可接受。

### C3. gateway 路径门：`creditService.CheckAndEstimateBudget`
**文件**：`internal/numind/biz/credit/credit_service.go`（L427）。
**插入位置**：`budgetOperationMap` 归一化之后、`PoolAdminTest` 分支**之后仅对默认池**、estimate/balance 之前：
```go
op, found := budgetOperationMap[input.Operation]
if !found { return nil, ...ErrUnknownBudgetOperation }
if input.Pool == PoolAdminTest { return s.precheckAdminTestPath(...) }  // 保持现状，免费门不作用于 admin_test 池

// NEW: free-model member gate (默认三池 + 真实模型)
if s.pricing != nil && s.membershipSvc != nil && input.Model != "" {
    if isFree, ferr := s.pricing.IsFreeModel(ctx, "llm_chat", input.Provider, input.Model); ferr == nil && isFree {
        member, merr := s.membershipSvc.IsActiveMember(ctx, uint64(user.ID), time.Now().UTC())
        if merr != nil {
            log.Warnw("free-model gate: membership check failed, fall through to normal billing", ...)
            // fall through → 正常 estimate/balance（0 价模型仍需余额；安全降级）
        } else if !member {
            return nil, fmt.Errorf("%w", errno.ErrModelMembershipOnly)  // 免费用户拒绝（AC3）
        } else {
            bal, _ := s.credits.GetBalance(ctx, user)  // 仅快照，失败容忍
            // 会员 + 0 价 → 跳过预扣、零扣费（AC1）
            emitFreeModelSpan(ctx, user, input)  // is_free_model=true, skip_reason=free_model_member
            return &PreCheckResult{SkipDeduction: true, Sufficient: true, EstimatedCredits: 0, Balance: deref(bal)}, nil
        }
    }
}
// ……既有 estimate + balance 检查不变……
```
- `SkipDeduction=true` → `ReserveBudget`（L481）`if pre.SkipDeduction { return nil,nil }`，`doReserveBudget`（L807）`if precheck.SkipDeduction { return 0,nil }` → **不建 reservation、不扣分**（AC1）。无 reservation → 无 reconcile（FinalizeReservation(nil) no-op）。
- merr 容错：fall through 到正常计费（0 价模型经 estimateBudgetCredits→CalculateCost 返 0→fallback flat estimate→balance 检查）= 旧行为，安全降级。

### C4. 默认 R2 路径门：`creditsImpl.CheckAndEstimate`
**文件**：同上（L544）。
**插入位置**：`GetBalance` 后、`estimation.EstimateCredits` 前，加同款免费门（用 `c.pricing` / `c.membershipSvc` / `in.Model` / `in.Provider`，serviceType `"llm_chat"`）。
**理由**：覆盖"SOP 节点把 0 价模型设为默认模型、modelKey 为空"的场景（AC 完整性）。逻辑与 C3 同构，抽 helper `applyFreeModelGate(ctx, user, provider, model, bal) (*PreCheckResult, handled bool, err error)` 复用，避免两份漂移。

### C5. SOP `CreateRun` 粗检放宽（会员感知）
**文件**：`internal/numind/biz/sop/sop.go`（L385-403）。
**改**：
```go
if b.creditSvc != nil {
    member, merr := b.creditSvc.IsActiveMember(ctx, uint64(userID))   // 新增 facade 方法
    if merr != nil { /* log warn, 保守按非会员走余额检查 */ }
    if !member {
        bal, balErr := b.creditSvc.GetBalance(ctx, user)
        if balErr == nil {
            totalRemain := bal.SubRemain + bal.BoosterRemain + bal.TrialRemain
            if totalRemain <= 0 { return nil, errno.ErrInsufficientCredits.SetMessage("积分不足，请充值积分") }
        }
    }
    // 会员：跳过粗检（per-node reserve 负责精确计费；0 价节点零扣，收费节点不足则在该 node 失败）
}
```
**新增**：`ICreditService.IsActiveMember(ctx, userID uint64) (bool, error)` + facade `creditServiceFacade.IsActiveMember`（delegate `membershipSvc.IsActiveMember(ctx,userID,now)`）。
**已知 trade-off**：0 余额会员跑"含收费节点"的 SOP → 创建 run 后该节点 reserve 失败 → run failed（orphan pending run）。END 结果仍是"积分不足"被拦，与 AC4 一致；仅多一条 failed run 记录。可接受（文档化）。

### C6. 新增错误码 `errno.ErrModelMembershipOnly`
**文件**：`internal/pkg/errno/credits.go`。
```go
ErrModelMembershipOnly = &Errno{HTTP: 403, Code: "Model.MembershipOnly", Message: "该模型仅限会员使用，请先开通会员"}
```
**理由**：语义独立于"购买加量包需会员"（`ErrMembershipRequired`），避免 SetMessage 改全局 sentinel 的风险。`errtranslate.ToErrno`/`errno.Decode` 用 `errors.As` 自动识别，无需额外注册。

## §3 决策矩阵（落到代码的真值表）
判定单位 = 一次会被预扣的 chat 调用，模型 = 该调用解析模型。

| 模型 0 价? | 会员(有效期)? | 余额 | 结果 |
|---|---|---|---|
| 是 | 是 | 任意（含 0） | SkipDeduction，放行，**不扣**（AC1/AC2）|
| 是 | 否 | 任意 | `ErrModelMembershipOnly` 403（AC3）|
| 否 | 任意 | 足 | 正常预扣+对账（AC4）|
| 否 | 任意 | 不足 | `ErrInsufficientCredits`（AC4）|
| 查不到价 | 任意 | — | 当作非 0 价走现状（AC5）|

## §4 范围边界（显式声明）
- **作用面**：所有走 `CheckAndEstimateBudget`(带 fragments 的 gateway chat) 或 `CheckAndEstimate`(默认路径) 的被预扣调用——即 SOP 主调用 / chatbot 主回复 / salesrag 主回复 / agent ReAct chat。
- **不作用面**：embed / rerank / 无 fragments 的 rewrite——它们本就 uncharged（对所有用户），保持现状（§1.2）。AC6 = "chat 类被预扣子调用"。
- **admin_test 池**（`PoolAdminTest`）：不套免费门，走既有 `precheckAdminTest`（独立池）。
- **booster-only 用户**（sub/trial 均过期、仅剩 booster）：按用户定义=非会员 → 0 价模型拒绝。

## §5 可观测性（ai-service.md 合规）
- 不新增 trace/generation。
- 在 credits 的 `credit-estimate` span 补 metadata：`is_free_model`(bool)、`skip_reason`("free_model_member")、`membership`("member"/"non_member")。会员免费放行时记一条 span，便于线上回答"为什么这次没扣分"。
- 免费用户被拒：不调 LLM，记 span/trace metadata（`blocked_reason="model_membership_only"`），不强制 generation error（无 LLM 调用）。

## §6 测试策略（喂给 S3）
- **单测为主**（biz 层，mock store/pricing/membership），覆盖 §3 真值表全部行 + 边界：
  - `credit`：`CheckAndEstimateBudget` / `CheckAndEstimate` 免费门 5 行真值表 + merr 降级 + IsFreeModel not-found。
  - `pricing`：`IsFreeModel`（全 0 rule→true / 部分非 0→false / not-found→false / tiered→false）。
  - `membership`：`IsActiveMember`（sub 在期/trial 在期含 0 积分/全过期/booster-only）。
  - `sop`：`CreateRun` 会员 0 余额放行 + 非会员 0 余额拒绝。
- 高风险（billing/权限）→ **持久化 Go 测试**做回归（非 gstack 一次性）。详细路径在 S3 plan 的"S5 验证策略"task 锁定。

## §7 S4 需现场确认的小项（设计已定，实现时核对）
1. `errno.Errno.SetMessage` 是否返回副本（C5 复用 ErrInsufficientCredits.SetMessage——既有代码已这么用，沿用即可）。
2. `GetMembershipState` 是否在 trial 余额=0 时仍填 `TrialExpiresAt`（C2 不依赖它，独立读 trial，规避）。
3. `pricing.PricingRule` 的 `PricePerCall` 字段名/可空性（C1 全 0 判定）。
4. `resolvePricingRule` 是否导出/同包可达（C1 在 pricing 包内，可达）。
5. `ContextBudgetPolicy.ChargeUser` 对 sop/chatbot/salesrag 主调用为 true（免费门在 doReserveBudget 内，依赖 ChargeUser 进入；确认主调用 ChargeUser=true）。

## §8 S2 Gate Review 修正（Sonnet 设计审查 FAIL→已解，2026-06-11）
独立 Sonnet 审查报 FAIL（3×P0 + 多 P1），逐条解决，设计据此更新：

### P0-1 → C2 错误传播（已改）
`IsActiveMember` **必须传播 store 错误**，不可吞：
```go
func (s *MembershipService) IsActiveMember(ctx, userID uint64, now time.Time) (bool, error) {
    sub, err := s.store.Subscriptions().Get(ctx, userID)
    if err != nil { return false, fmt.Errorf("IsActiveMember: sub: %w", err) }
    if sub != nil && sub.ExpiresAt.After(now) { return true, nil }
    trial, err := s.store.TrialGrants().Get(ctx, userID)
    if err != nil { return false, fmt.Errorf("IsActiveMember: trial: %w", err) }
    if trial != nil && trial.ExpiresAt.After(now) { return true, nil }   // 忽略 CreditsRemaining（AC2）
    return false, nil
}
```
调用方（C3/C4/C5）`err != nil` 时**降级按"非会员走正常计费/余额检查"**（不是"放行免费"，也不是"误拒会员"——交给下游 reserve/余额逻辑，0 价模型在余额检查下对有额会员仍通过，对 0 额会员报积分不足，安全）。

### P0-2 → ICreditService 接口扩张（已纳入计划）
新增到 `ICreditService`（contracts.go）的方法：
- `IsActiveMember(ctx, userID uint64) (bool, error)`（delegate `membershipSvc.IsActiveMember(ctx,userID,now)`）—— C5 用。
- `EnforceModelMembership(ctx, userID uint64, provider, model string) error`（见 P0-3）—— C7 用。
**这是 breaking interface change**：所有 `ICreditService` 实现 + 测试 mock 必须同步加这两个方法（编译期暴露）。S3 plan 必须有一个 task 专门"扩 ICreditService + 更新全部实现/mock"，并在 S4 grep `ICreditService` 找全实现体。同理 `pricing.ICalculator` 加 `IsFreeModel` 后，其全部实现/mock 同步更新。

### P0-3 → AC3 airtight：免费用户拦截独立于 ChargeUser（新增 C7）
**问题**：gateway 预扣只在 `result.Policy.ChargeUser==true` 时进 doReserveBudget；若某 0 价模型操作被配成 `ChargeUser=false`，免费用户绕过 AC3。
**解**：把"免费用户拦截"提到 **ChargeUser 守卫之前**、对 chat 请求恒执行。

**C7（新增）— 中间件层 ChargeUser 无关的会员门**：`internal/pkg/aiservice/middleware/context_budget.go`，在 L483 `if result.Policy.ChargeUser ...` **之前**插入：
```go
// AC3 airtight: free model is member-only regardless of ChargeUser.
if deps.CreditService != nil && userID != 0 {
    if err := deps.CreditService.EnforceModelMembership(ctx, userID, route.Provider.Name, route.ServiceKey); err != nil {
        return nil, fmt.Errorf("ContextBudgetCredits: %w", err)   // ErrModelMembershipOnly 或 nil
    }
}
if result.Policy.ChargeUser && deps.CreditService != nil && userID != 0 {
    reservationID, err = doReserveBudget(...)   // 会员零扣费(SkipDeduction)仍在此
}
```
`EnforceModelMembership(ctx, userID, provider, model)`：
- `IsFreeModel("llm_chat", provider, model)` 非 free → return nil（no-op，收费模型不受影响）。
- free + 非会员 → `ErrModelMembershipOnly`。free + 会员 → nil。
- 内部错误（pricing/membership DB err）→ **fail-open return nil**（不误拒；ChargeUser=true 时下游 reserve 门会再拦 0 额，ChargeUser=false+DB错 的极小概率漏判可接受，文档化）。

**分工厘清（避免重复/遗漏）**：
- gateway 路径（modelKey≠""，走中间件）：**C7** 负责免费用户拦截（ChargeUser 无关）；**C3**（CheckAndEstimateBudget，仅 ChargeUser=true 进）负责会员零扣费 SkipDeduction。C3 内的免费用户分支对 gateway 是冗余防御（C7 已拦），保留无害。
- 默认/R2 路径（modelKey=""，SOP 节点直扣，**不走中间件**，sop.go L856 不依赖 ChargeUser）：**C4**（CheckAndEstimate）负责完整门（免费用户拦截 AC3 + 会员零扣 AC1）。此路径 AC3 本就 airtight。

### P1 修正集
- **C1**：`PricingRule.PricePerCall` 是非空 `float64`，删去"或 nil"，判定即 `InputPricePerMTok==0 && OutputPricePerMTok==0 && PricePerCall==0`。`CreditMultiplier` 不参与判定（全 0 价 × 任意 multiplier 仍 0）。tiered 模式（`BillingMode=="tiered_token"`）一律返回 false（0 价模型不会用 tiered）。
- **C6 错误穿透**：`ErrModelMembershipOnly` 是 `*errno.Errno`，经 `errno.Decode`/`errtranslate.ToErrno` 的 `errors.As` 自动识别（先于 `errors.Is` 分支）；`sop.go:wrapCreditError` 只认 `credit.ErrInsufficientCredits`，对本错误**原样穿透**（已确认正确）。S4 不得在 wrapCreditError 加"未知错误→积分不足"兜底。
- **deref 安全**：会员零扣返回的 `PreCheckResult.Balance` 用 nil-safe 取值（`GetBalance` 失败时填 `BalanceBreakdown{}`，不 panic）。
- **proposal AC3 勘误**：proposal.md §4 AC3 写的 `ErrMembershipRequired` 以本 spec 的**新错误码 `ErrModelMembershipOnly`** 为准（语义独立、避免改全局 sentinel）。
- **serviceType 范围**：`IsFreeModel` 固定传 `"llm_chat"`；本 feature 只覆盖 LLM chat 类 0 价模型，未来 0 价 image_gen 等不在范围（文档化）。
- **orphan pending run UX**：0 余额会员跑"含收费节点"SOP → CreateRun 放行 → 该收费节点 reserve 失败 → run 置 `failed` 并带"积分不足"消息（与旧 CreateRun 直接拒同义的最终结果，仅多一条 failed run 记录）。可接受，文档化。

### 复核结论
3×P0 全部在设计层解决（C2 传错误 / C5+C7 接口扩张入计划 / C7 中间件独立拦截）；P1 全部吸收进 C1/C6/C7 与本节。改动清单更新为 **C1–C7 + 错误码 + 测试**。S3 plan 必须含"扩 ICreditService/ICalculator 接口 + 更新 mock"专项 task，并对 C7 加 ChargeUser=false 回归测试锁 AC3。

