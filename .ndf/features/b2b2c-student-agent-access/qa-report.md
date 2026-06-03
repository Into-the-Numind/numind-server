# QA Report — B2B2C 子账户使用父账户配置的 Agent

> Feature id: `b2b2c-student-agent-access` (Standard, 精简). Branch `feature/b2b2c-student-agent-access`.
> S5 验证策略（spec §6 / Rule 10）：后端访问控制改动 → Go 集成测试矩阵（永久回归）为主 + 真实二进制编译 + dev 子账户 live smoke（S6）。

## 验证环境
- 后端：worktree feature 分支本地（`go test` + `go build ./cmd/numind`）
- 前端：无改动（纯后端 access 逻辑）
- DB/Redis：单测用包内 mock store（确定性，无外部依赖）

## 自动化检查结果

| 检查项 | 命令 | 结果 | 备注 |
|--------|------|------|------|
| Go vet | `go vet ./internal/numind/biz/...` | PASS | exit 0，无 finding |
| Go lint | `task lint`（go vet + golangci-lint run ./...） | PASS | exit 0，无 finding |
| Go test（feature 包） | `go test ./internal/numind/biz/agent/ ./internal/numind/biz/` | PASS | 全绿 |
| Go test（全仓库） | `go test ./...` | PASS（feature 范围） | 76 包 ok；2 包失败（`cmd/numind` migration、`biz/memory` digest）**经 clean develop 复测为预存失败，与本改动无关** |
| 真实二进制 | `go build ./cmd/numind` | PASS | 78MB 二进制；生产 wiring `WithUserStore(ds.Users())` 编译进真实 binary |
| E2E（dev 子账户 live smoke） | 手动 | **待 S6**（需 merge→dev 部署 + parent-agent/child 夹具，见下） | — |

## 访问矩阵单测（永久回归 — Rule 10 高风险持久保护）

helper `agentTenantAccess`（`tenant_access_test.go`）+ 三 gate 集成（`runner_tenant_access_test.go` / `student_run_lifecycle_test.go`）全部 PASS：

| 场景 | helper (单元) | gate#1 resolveDefinition (Estimate) | gate#2 Run | gate#2 Run/Answer-resume | gate#3 RunStream |
|------|:---:|:---:|:---:|:---:|:---:|
| 父跑自己 active | ✅ | — | ✅ | — | ✅ |
| 父跑自己 inactive（试聊） | ✅ | — | — | — | — |
| **子跑父 active（核心修复）** | ✅ | ✅ | ✅ | ✅ | ✅ |
| 子跑父 inactive（R9 拒） | ✅ | ✅ | ✅ | ✅(de-list 陷阱) | ✅ |
| 子跨租户（拒） | ✅ | ✅ | ✅ | — | ✅ |
| 独立用户跑别人（拒） | ✅ | — | — | — | — |
| nil-store + 父（降级允许） | ✅ | — | — | — | — |
| nil-store + 子（降级拒，不泄漏） | ✅ | ✅ | ✅ | — | ✅ |
| 父跑别父（慢路径 nil 守卫，不 panic） | ✅ | — | — | — | — |
| store error 不被掩成 NotFound | ✅ | — | — | — | — |

> "nil-store + 子拒" 对照 "子跑父 active 允许" 证明每个 gate 真实查询了 userStore（无 store 子账户永不被识别）。

## 隔离回归（reviewer 确认未改动）
`verifyRunOwnership` / `verifySessionOwnership` / `GetSessionSnapshot` / `GetRun` / `WriteFeedback` / `Pin/Rename/DeleteSession` 全 per-userID，未触碰。run 仍以 caller(子账户) userID 建（`lifecycle.go` / `runner.go`）。下游 `ad.ParentUserID`（skillBinding/WithAgentDefCtx/compliance）按拥有者加载，正确。

## 可观测性验证
N/A —— 本改动不新增 LLM 调用（仅访问 gate 改动，复用既有 run 路径的 Langfuse trace，未触碰）。

## 验收标准核对（spec §5）
| 验收标准 | 结果 |
|---|---|
| 父账户跑自己 agent（active/inactive）允许 | PASS（单测） |
| 子账户跑父账户 active agent 允许（核心修复） | PASS（gate#1/#2/#3 单测） |
| 子账户跑父账户 inactive agent 拒（R9） | PASS |
| 子账户跨租户拒（ErrSkillNotFound） | PASS |
| 子账户 run/session 隔离（只见自己） | PASS（隔离函数未改 + reviewer 确认） |
| 子账户 Answer 续跑（active 允许 / inactive R9 拒） | PASS（ExistingRunID 单测） |

## 结论
**ALL_PASS（后端单元 + 二进制层面）**。dev 子账户 live smoke 列为 S6（需 merge→dev 部署 + parent-agent/child 夹具；spec §6 已记夹具坑：admin id=30 名下无 agent、"从零创建"有 422 bug → 需 seed/API 造 parent agent + child）。

## 失败项修复要求
无。（`cmd/numind` + `biz/memory` 的预存失败属其他范围/技术债，不在本 feature。）
