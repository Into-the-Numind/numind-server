# 免费模型仅限会员、零扣费 — S5 验收报告

> 日期：2026-06-11 ｜ Feature: free-model-member-only ｜ 验证方式：持久化 Go 单测（biz/中间件层）+ race + 全仓库 `go test ./...`

## 验证策略（S3 T7 锁定）
后端纯决策逻辑、无前端改动 → **Go 单元测试为主**（持久化回归保护），不做 Playwright/gstack E2E。
- 决策矩阵（AC1–7）由 biz/credit + membership + pricing + middleware + sop 的表驱动/集成单测覆盖。
- race 检测覆盖核心门包。

## AC 覆盖矩阵（每条 AC → 测试）

| AC | 验证点 | 测试 |
|----|--------|------|
| AC1 | 会员 0 余额 + 0 价模型 → SkipDeduction、零扣、无 reservation | `credit.TestCheckAndEstimateBudget_FreeModel_Member_SkipsDeduction` / `TestCheckAndEstimate_FreeModel_Member_SkipsDeduction` / `TestReserveBudget_FreeModel_Member_NoReservation` |
| AC2 | trial 在期但积分=0 仍算会员、可用 0 价模型 | `membership.TestIsActiveMember_TrialZeroCreditsStillMember` + `credit.TestCheckAndEstimateBudget_FreeModel_TrialMemberZeroCredits_SkipsDeduction` |
| AC3 | 免费用户 + 0 价模型 → 403 ErrModelMembershipOnly；中间件层 ChargeUser/fragment 无关 | `credit.TestCheckAndEstimateBudget_FreeModel_NonMember_Blocks` / `TestCheckAndEstimate_FreeModel_NonMember_Blocks` / `TestEnforceModelMembership` + `middleware.TestContextBudgetCredits_FreeModelNonMember_BlockedRegardlessOfFragmentsOrCharge` |
| AC4 | 收费模型行为不变（余额足→跑；不足→积分不足）；回归 | `credit.TestCheckAndEstimateBudget_PaidModel_ZeroBalance_Insufficient` / `TestCheckAndEstimateBudget_PaidModel_MemberWithBalance_OK` / `TestCheckAndEstimate_PaidModel_ZeroBalance_Insufficient` + `middleware.TestContextBudgetCredits_NoBlock_NoFragmentPassthroughUnchanged` |
| AC5 | 查不到 pricing_rule ≠ 免费 | `pricing.TestIsFreeModel`(not-found→false) + `credit.TestEnforceModelMembership`(no-such-model→放行) |
| AC6 | per-call 语义（收费子调用照常拦）；scope=被预扣的 chat 调用 | spec §1/§4 已显式声明范围；embed/rerank/无fragment rewrite 现状 uncharged，不在积分拦截链（取证确认）。chat 类被预扣子调用走 AC4 路径 |
| AC7 | 决策矩阵有持久化 Go 单测 | 上述全部 + `credit.TestDecideFreeModel`（纯真值表）+ `sop.TestCreateRun_*`（CreateRun C5） |
| C5 | 会员 0 余额可创建 SOP run；非会员被拦；membership-err 保守 | `sop.TestCreateRun_MemberZeroBalance_NotBlockedByBalance` / `TestCreateRun_FreeUserReturnsTypedError` / `TestCreateRun_MemberCheckError_ConservativelyBlockedOnZeroBalance` |
| C7 | SkipDeduction → ReserveBudget(nil,nil)；SOP Reserve 守卫 | `TestReserveBudget_FreeModel_Member_NoReservation` + sop.go ExecuteNodeStream/ChatAfterRunStream `if !pre.SkipDeduction` |

## 测试结果
- **改动包单测全绿**：pricing / errno / membership / credit / aiservice/middleware / sop / agent(budgetgate) / budget / billing / controller/v1/credit。
- **race 检测全绿**（核心门包）：credit / middleware / membership / pricing。
- **lint 全绿**（golangci-lint，改动包）。
- **`go build ./...` 全绿**（含 ICreditService / ICalculator / ContextBudgetCreditService 三接口扩张 + 8 个实现/mock 同步）。

## 诚实声明 / 已知项
- **全仓库 `go test ./...` 有 1 处 FAIL：`controller/v1/agent.TestAnswerHandler_MissingBody`（student_run_test.go:299）。** 经隔离根因（`git show 7b6a63ed..HEAD` 我方 9 个 commit 均未触及 `controller/v1/agent`），确认为**并行 feature `agent-multi-question`（S4 在研）的中间态测试失败，非本 feature 引入**。不阻塞本 feature 的编译/部署（仅 unit test，server build 正常）。已告知用户。
- **无前端改动**：免费用户 403 ErrModelMembershipOnly 复用既有业务错误展示（errtranslate→SSE / errno.Decode→HTTP，取证确认 403≠401 不跳登录、不被改写成"积分不足"）。若 dev 验收发现某前端入口吞消息，再追加最小前端验证。
- **per-call 范围收敛**：取证证明唯一被积分拦截的 chat 调用 = 带 ContextFragments 的主回复；embed/rerank/无 fragment 的 rewrite 对所有用户 uncharged（现状），不在本 feature 扩张。

## 结论
S5 gate 通过（本 feature 范围内全绿 + race 干净；唯一全仓库 FAIL 属并行 feature，非己引入已隔离）。可进 S6（ndf-done + dev 部署）。
