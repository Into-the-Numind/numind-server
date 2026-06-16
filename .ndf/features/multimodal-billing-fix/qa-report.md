# QA Report — multimodal-billing-fix

## 验证环境
- 后端：本地（Go 单测）+ dev（端到端，部署后）。仅 numind-server。

## 自动化检查结果
| 检查项 | 命令 | 结果 | 备注 |
|--------|------|------|------|
| Go build 全模块 | `go build ./...` | PASS | 三处签名/接口改动跨包编译通过 |
| go test 改动包 | pricing / billing / aiservice/middleware / store / biz/contextbudget | PASS | 全绿（含 -race） |
| go vet 改动包 | `go vet` | PASS | |
| golangci-lint 改动包 | exit 0 | PASS | |

### 复现测试（Rule 11，红→绿，永久回归）
| Task | 复现测试 | 红(bug 在) | 绿(fix 后) |
|------|---------|-----------|-----------|
| T1 | `TestCalculateCost_VisionFallsBackToChat` | record not found | cost=12 分（真实 claude token 价）|
| T2 | `TestSynthBillOnlyReserve_NotHalfWindow` | 64000 | ≤8192 |
| T3 | `TestReconcile_PricingMiss_RefundsNotWorstCase` | finalize 64000 | refund，不扣 fallback |

### 守护/正向测试（不回归）
- T1：`TestCalculateCost_VisionModelExactStillWins`（专用视觉模型精确命中、agnostic 不触发）、`TestCalculateCost_AgnosticDBErrorPropagates`（缺价 DB 错误传播非静默 0）
- T2：`TestSynthBillOnlyReserve_SmallWindowKept`（小窗口不回归）、`TestSynthBillOnlyReserve_UnknownCapability`（MaxOutputTokens=0→8192）
- T3：`TestReconcile_PricingResolved_ChargesActual`（holder Set 据实扣 30 非预扣 796）、`TestReconcile_HolderUnset_DistinguishesWarnVsError`（流中断 warn vs 缺价 ERROR）、`TestFinalize_UsesZeroCostFromHolder`（F-7 holder.Set(0)→扣 0 不回归）

> **预存失败（非本 feature 回归，Rule 8 已核实）**：`biz/salesrag`、`controller/v1/credit`、`controller/v1/agent` 三包失败均为 `table user has no column named company_name`——test-DB AutoMigrate 缺列的 schema 漂移，agent 的 `TestStudentQueryCtrl_*` 是 agent-mode-backend-repair manifest 已记录的同一预存 flake。本 feature diff 未碰 salesrag / user model / migration（`git diff --name-only develop...HEAD` 确认），改动 5 包全绿。

## S4 双 review（T1-T3，每 task 并行双 Sonnet reviewer）
- **T1**：spec PASS_WITH_CONCERNS + quality PASS_WITH_CONCERNS，0 P0。P1=resolveAgnostic 改传播非-NotFound DB 错误（守 ICalculator 契约）；P2=InvalidateCache 清 agnostic key / DB 错误测试 / recorder stub IsActive。全修。
- **T2**：双 PASS_WITH_CONCERNS，0 P0/P1。P2=min() 重构 / 补 MaxOutputTokens=0 测试 / 常量决策注释。全修。
- **T3**（最高风险）：spec PASS + quality PASS_WITH_CONCERNS，0 P0/P1。P2 全为过时注释（fix 改了行为注释没跟上）已刷新；PricingCostCents 未填致 ReconcileDelta 失真=预先存在 gap（非 T3 回归）记 follow-up。

## 端到端 QA —— dev（S6 部署后）
1. **治本验收**：chatbot 选 claude-opus-4-6 上传图片 → 查 credit_reservation：actual_cost_cents ~10 量级（不再 64000/796）、reserved_credits 与之同量级、status=reconciled、delta 小。
2. 专用视觉模型（qwen3-vl-flash）发图 → 定价精确命中、不回归。
3. usage_record.cost_cents 与 reservation actual_cost_cents 一致且真实（snapshot 审计列对 vision→claude 仍可能 NULL=已知 follow-up，非钱）。
4. 预扣值（reserved_credits）与对账值（actual_cost_cents）同量级（不再 66 倍虚高）。
5. （可选）构造缺价场景 → 对账退款 + 日志 ERROR 告警，绝不大额扣。

## 结论
**ALL_PASS（自动化层）** — 三处 fix 各有复现测试（红→绿）+ 守护测试永久覆盖；改动 5 包全绿 + lint 0；预存 company_name 失败已核实非本 feature 回归。端到端 + 治本验收在 S6 dev（重点：重新上传图片验证 actual_cost_cents ~10 量级而非 64000）。

## Follow-up（不在本 feature）
- chain 2/3 snapshot 审计列对 vision→统一模型仍 NULL（非钱）；3 个重复定价 resolver 应合并为单一入口。
- `PricingCostCents` 未填 → `context_budget_event.ReconcileDelta` 失真（预先存在）。
- prod provider 名 `ali` vs 定价 `ali-dashscope` 归一化（prod 识图少收）。
