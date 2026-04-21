# QA Report — sales-agent-child-permission

**Feature**: 销售智能体父子账号权限管控
**Date**: 2026-04-21
**Stage**: S5
**Plan**: `numind-server/docs/superpowers/plans/2026-04-21-sales-agent-child-permission-plan.md`
**Spec**: `numind-server/docs/superpowers/specs/2026-04-21-sales-agent-child-permission-design.md`

## 验证环境

- **策略**：**S5 Option B（静态检查 + Playwright --list 加载）**，沿用 parent-self-grant-membership 2026-04-20 先例
- **理由**：本地启动后端会连接 dev MySQL（`config_local` 回退到 `config_dev`），有污染生产数据风险。实际运行时 E2E 推迟到 S6 dev CI/人工验收
- **后端 worktree**：`numind-server/.worktrees/sales-agent-child-permission` (`feature/sales-agent-child-permission`)
- **前端分支**：`numind-web-v3` on `feature/sales-agent-permission-e2e`
- 编译器：Go 1.24；Node/Vite 环境

## 自动化检查结果

| 检查项 | 命令 | 结果 | 备注 |
|--------|------|------|------|
| Go build | `go build ./...` | PASS | exit 0，仅 cgo deprecation warnings（pre-existing，非本 feature） |
| Go vet | `go vet ./internal/numind/...` | PASS | exit 0 |
| gofmt 干净（feature 文件） | `gofmt -l <5 个 feature 文件>` | PASS | 0 行输出 |
| Go test (feature tests) | `go test ... -run 'TestCheckSalesPermission\|TestGate_'` | PASS | **10/10 PASS**（4 C3 + 6 C5） |
| Go test race mode | 同上 `-race` | PASS | **10/10 PASS，0 data race** |
| Go test 全量（backend） | `go test ./internal/numind/... ./internal/pkg/...` | PASS_WITH_PRE_EXISTING_FAILURES | 除 6 个 pre-existing failure（见下），其余全 PASS |
| Vue lint | `npm run lint` | PASS | 0 errors；1 pre-existing warning `KnowledgeBaseDetail.vue:295` formatDate 未用（非本 feature） |
| Vue type-check | `npm run type-check` | PASS | exit 0 |
| Playwright 加载（E2E 可被识别） | `npx playwright test e2e/sales-agent-permission.spec.ts --list` | PASS | 4 tests 加载成功 |
| Admin lint/type-check | N/A | N/A | 本功能不涉及 admin 前端 |

### Pre-existing failures（非本 feature 引入）

在 develop base commit `64a690a`（未 merge 本功能）上重跑同样的测试集，以下 6 个测试 **全部一样失败**，证实非本 feature 导致：

| 测试 | 位置 | 错误 |
|------|------|------|
| `TestReserve_ExactlyExhaustedThenRetry_ReturnsInsufficientSentinel` | `biz/credit/credit_service_boundary_test.go:100` | panic: `unreachable: legacy_tier must be guarded by SkipDeduction`（已记录于 manifest 2026-04-20 S4 notes） |
| `TestAcquireSalesragCredits_CreditsHappyPath` | `biz/salesrag/` | 同一 panic 根因 |
| `TestAcquireSalesragCredits_InsufficientBalance` | `biz/salesrag/` | 同上 |
| `TestFinalize_StreamErrorTriggersRefund` | `biz/salesrag/` | 同上 |
| `TestAcquireSalesragCredits_IdempotentReplay` | `biz/salesrag/` | 同上 |
| `TestSopCredits_CreditsMode_ReserveThenReconcile` | `biz/sop/sop_credits_integration_test.go:271` | 同一 panic 根因 |

**所有 6 个测试的根因都是同一个** `unreachable: legacy_tier must be guarded by SkipDeduction`，是 credit_service.go:164 的 panic，指向 credit 模块内部 legacy_tier 分支守护逻辑问题。独立 hotfix，不阻塞本 feature。

## 浏览器 QA

**本阶段不执行 gstack `/qa`。** 理由：S5 Option B 不启动本地服务。S6 merge 到 develop 后 CI 部署 dev 环境，人工验收时用 Playwright 真跑 E2E 才是实测。

## 可观测性验证

**N/A** — 本 feature 是权限 gate，不触发任何 LLM 调用。Gate 发生在 credit 预检之前，拒绝的请求根本不会进入 LLM 路径。现有 SalesRAG trace 不受本改动影响。

## PRD 验收标准核对

| # | AS | S5 验证证据 | 结果 |
|---|----|------------|------|
| AS-1 | 父账号开关 ON → 子账号可进 | E2E E1 `mockFeaturesApi.post > 0` 断言；C3 T2 SubGranted → true | PASS（待 S6 dev 实测） |
| AS-2 | 父账号关 OFF → 子账号下次请求 403 | E2E E3 `mockFeaturesApi.del > 0` 断言；C5 H1-H3 子账号 gate 403；E5 契约 code=100207 | PASS（待 S6 dev 实测） |
| AS-3 | `/check-permission` 返回 false（子账号未授权），且端点不被 gate | C3 T3 (SubDenied_FalseNot403 — HTTP 200 非 403) + C5 H4 (CheckPermissionNotGated) 双向验证 | **PASS** |
| AS-4 | 所有运行端点 403 | C5 H1 documents GET / H2 chat POST / H3 ocr POST → 全 403 | **PASS** |
| AS-5 | 父账号无需 grant 即可访问 | C3 T1 (Parent_True) + C5 H5 (Parent_PassesThrough) | **PASS** |
| AS-6 | 发布后无 backfill | Task 5 SSH baseline 证实：dev grants=3 / prod grants=20，没有任何 migration 插入 | **PASS** |
| AS-7 | check-permission 与 gate 判断一致 | C3 T1/T2/T3 与 C5 H4/H5/H6 使用同一 `user_feature_permission` 表 + 同一 `HasFeaturePermission` 逻辑 | **PASS** |
| AS-8 | 对 credit/会员/billing_mode 无影响 | Middleware 顺序保证 gate 在 credit 预检之前（C5 测试不 mock credit 也能通过，说明未触发） | **PASS** |

### 边界 (E1-E4)

| # | 边界 | 状态 |
|---|------|------|
| E1 | 并发撤权 SSE → 当前流不中断（下次请求 403） | Accepted Exposure 2（spec §5 宽松模式决策 A），不强制验证 |
| E2 | 子账号身份迁移（parent_user_id 被 NULL）→ 自动 true | `HasFeaturePermission` 代码层面保证，既有 store 测试覆盖 |
| E3 | 未知 featureKey | 非本 spec 范围（grant API 层面） |
| E4 | `/check-permission` 位置 | C5 H4 验证端点不在 salesGroup 下 |

## S6 Go/No-Go 基线（Task 5 SSH 查询结果）

| 环境 | `sales_agent` grants | total sub-users | 上线即 403 占比 |
|------|---------------------:|----------------:|----------------:|
| dev (`49.233.219.254:13306`) | 3 | 20 | **85%** (17/20) |
| prod (`129.28.125.51:13306`) | 20 | 94 | **79%** (74/94) |

- ✅ 不是 reviewer 预测的 100% worst-case（说明前端 toggle 历史上确实有父账号用过）
- ⚠️ 仍是 **多数冲击**（74 prod 子账号上线瞬间 403）
- ✅ dev/prod 比例一致（差 6pp），无 NDF §5 Pause-and-Ask 触发条件
- ✅ D1 deny-all 策略维持，**不 backfill**
- **产品侧责任**：上线前向所有 74 位 prod 子账号的 2 个父账号发通知（或给所有使用 sales-rag 的子账号）

## S6 人工验收补齐清单（待在 dev 实测）

S5 Option B 不跑真 E2E，S6 merge 后必须在 dev 环境手动走以下流程：

1. **启动 Playwright E2E**（真打 dev 后端）：
   ```bash
   cd numind-web-v3
   E2E_USERNAME=$E2E_USERNAME E2E_PASSWORD=$E2E_PASSWORD npm run test:e2e -- sales-agent-permission.spec.ts
   ```
   期望 4 tests PASS
2. **真人浏览器操作验证**（至少走 1 条路径）：
   - 登 dev 父账号 → 客户管理 → 找一个未 grant sales_agent 的子账号 → 开关 ON → save
   - 登该子账号 → 访问 `/sales` → 能进
   - 父账号 → 开关 OFF → save
   - 子账号刷新 `/sales` → 被 redirect 到 `/`，看到"未开通销售智能体权限"提示
   - 子账号打开 devtools → `fetch('/v1/sales-rag/sessions/1/chat', {method:'POST', ...})` → 返回 code=100207
3. **日志抽查**：dev 日志里出现 `FeaturePermission` middleware 的 403 日志，确认 gate 工作
4. **数据库状态**：dev DB 上增加的 grant 记录落在 `user_feature_permission` 表，feature_key='sales_agent'，granter=正确父账号

## 结论

**ALL_PASS（S5 静态检查阶段）**

所有**可在 S5 静态阶段验证**的项目全绿。唯一阻塞 S6 的是人工在 dev 跑 E2E + 手动验证 UI 路径（见上清单）。

## 失败项修复要求

无 S4 回退项。

## 技术债声明（不阻塞本 feature）

1. **6 个 pre-existing credit/legacy_tier panic 测试**：`credit_service.go:164` 的 `unreachable` panic 需独立 hotfix，建议单开 manifest 条目跟进。
2. **content_monitor 路由级集成测试缺失**：本 feature 的 C5 是项目首个路由-middleware 集成测试。建议未来补 content_monitor 等效测试（不在本 feature 范围）。
