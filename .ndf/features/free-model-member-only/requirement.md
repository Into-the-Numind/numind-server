# 免费模型仅限会员、零扣费（free-model-member-only）

## 来源
- 提出人：R1（产品负责人）
- 提出日期：2026-06-11

## 需求描述
> 客户原话整理：
> 我在 prod 新增了一个模型（agnes / 有数 AI），它的计费为 0。我想要：
> 1. 免费用户**绝对不能**使用这个模型来运行。
> 2. 只要是会员用户（不管是 sub 订阅还是 trial 体验包），**有没有积分都可以**用这个模型来运行，随便怎么用都不扣积分，因为定价为 0。
>
> 补充（关键，per-call 语义）：规则适用于"所有会调用 AI 模型服务、且调用的是定价为 0 的"地方。但比如用 SalesRAG 时，最后主回复的 AI 是定价为 0 的，但中途会调用一些其它定价不为 0 的 AI，**这种时候该收费的部分仍要按积分扣、积分不足要提醒"积分不足"**。

## 业务目标
- 让 0 价模型成为**会员专属增值**（拉动会员转化 / 留存），而不是被免费用户白嫖的后门。
- 会员侧体验顺滑：会员用 0 价模型不受"积分余额"门槛限制（哪怕余额为 0 也能用）。
- 计费正确性不被破坏：同一次操作里夹杂的**收费**子调用仍正常计费、余额不足正常拦截。

## 优先级
高

## Triage
- 推荐轨道：**Standard**
- 分类理由（5 条标准）：
  1. 数据库 schema 变更：**否**（规则键 `pricing_rule` 既有表的价格列，无新表/新列）
  2. 新增 API 端点：**否**（改的是已有计费/权限决策路径，无新路由）
  3. 新外部服务集成：**否**
  4. 影响文件数：**>3**（pricing helper + credit service 预检/预扣两处 + SOP CreateRun + 错误码/传播，预计 4-6 文件）
  5. 高风险业务逻辑（支付/权限）：**是**（会员判定 + 跳过扣费 + 余额门槛放宽，直接动计费与权限）
- 人类决定：**确认 Standard**（用户已授权"全流程自主推进到 dev 部署"，S0/S1/S2 设计硬门禁由 AI 按本项目最优方式自主通过并在 manifest/ADR 留痕；S7 生产部署不在本次授权内）

## 核心语义（写进设计的判定单位）
**判定单位 = 单次被计费拦截的 AI 调用**，看"这一次调用解析到的模型，其 `pricing_rule` 价格是否为 0"。

| 调用情形 | 会员（sub 或 trial 在期） | 免费用户（无 sub 且无 trial） |
|---|---|---|
| 该调用模型 **0 价** | 放行，**跳过预扣、不扣分** | **拒绝**（`ErrMembershipRequired`，提示需开通会员）|
| 该调用模型 **收费** | 正常预扣/对账，余额不足 → "积分不足" | 余额不足 → "积分不足"（现状不变）|

- "免费用户" 定义（已与用户确认）：`!(SubActive || TrialActive)`，即既无在期订阅、也无在期体验包。trial 在期即算会员。
- "0 价" 定义：`pricing_rule` 命中且价格分量全为 0（input/output/per-call/tiered 全 0）。**查不到 pricing_rule ≠ 0 价**——查不到要走现状（flat-estimate 兜底 + 正常预扣），绝不当成免费放行（防止"漏配价格"变后门）。
- per-call 推论：SalesRAG 这类一次操作含多次 AI 调用时，0 价的那几次会员免费、收费的那几次照常计费/拦截。

## 待查项（S2 必须查实，不在 S0 拍死）
- **embed / rerank 子调用的计费路径**：取证显示只有 Chat 调用走 `ContextBudgetCredits` 预扣并能拦"积分不足"；`Embed`/`Rerank` 不走同一预扣点。S2 必须查实：embed/rerank 是否真的从三池积分扣减、是否会因余额不足拦截。
  - 若 embed/rerank **不从积分扣减**（只记 UsageRecord）→ 用户"中途收费要拦"的诉求对 **chat 类**子调用（如 query 改写）已自动满足（改写是 chat 调用、走预扣），embed/rerank 不在积分拦截链上，0 价豁免也无需覆盖它们。S2 据此把范围收敛到"chat 调用级"，并在 spec 显式声明。
  - 若 embed/rerank **会从积分扣减/拦截** → 0 价豁免逻辑必须同步覆盖其计费点。

## 备注
- 取证已完成（见会话）：拦截点 `internal/pkg/aiservice/middleware/context_budget.go` 的 `ContextBudgetCredits`；会员判定 `membership.GetMembershipState`（`SubActive`/`TrialActive`）；0 价判定查 `pricing_rule`；现成开关 `PreCheckResult.SkipDeduction`（T1 后恒 false，可复用表达"本次免费跳过预扣"）；错误码 `errno.ErrMembershipRequired`（已定义、未使用）。
- 非 bug-from-customer（是新功能/产品规则），NDF §12 复现测试不强制；但 billing/权限高风险，S5 须有持久化 Go 单测做回归保护（见 S3 验证策略 task）。
