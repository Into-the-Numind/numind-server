# Plan: B2B2C 子账户使用父账户配置的 Agent

> Feature id: `b2b2c-student-agent-access`. Spec: `docs/superpowers/specs/2026-06-03-b2b2c-student-agent-access-design.md`.
> Worktree: `/private/tmp/wt-b2b2c-student-agent-access-numind-server` (branch `feature/b2b2c-student-agent-access`).
> 全部在 numind-server 单仓库；2 个 task，串行（共享 helper，Tier 4 — 同包耦合文件，不并行）。

---

## Task 1 — 共享访问 helper `agentTenantAccess` (TDD + Rule 11 repro)

**目标**：在 `biz/agent` 加一个包级纯函数实现 spec §2 访问规则，含 P1 nil 守卫与 R9。

**涉及文件**：
- `internal/numind/biz/agent/tenant_access.go`（新增）
- `internal/numind/biz/agent/tenant_access_test.go`（新增）

**步骤（TDD / Rule 11）**：
1. **RED（首 commit，`test(repro):` 前缀）**：先写 `tenant_access_test.go` 覆盖矩阵用例 1–7b + 9，并写一个**临时 stub** helper（旧语义：仅 `ad.ParentUserID==callerID` 放行，其余拒）。
   - **真正 RED（stub 下 FAIL）的是非 nil-store 的子账户路径**：用例 3（子+active→应允许，stub 拒）= 核心 bug 复现；用例 4（子+inactive）、5（子+别租户）在 stub 下"恰好"返回拒但理由不对（stub 无 R9/跨租户语义）——按断言可能 PASS，**不能**当作 RED 依据。
   - **stub 下本就 GREEN（勿误标 RED）**：1/2（父快路径）、7/7b（nil-store 一律拒）、9（父跑别父，stub 也拒、不 panic）。
   - commit：`test(repro): agentTenantAccess denies active child-of-owner (case 3) [b2b2c blocker]`。
2. **GREEN**：把 stub 替换为真实实现（fast-path → nil 守卫慢路径 → R9 → 跨租户拒）。全用例（含 3/4/5 的正确语义）GREEN。commit：`feat(agent): add agentTenantAccess tenant access helper`。

**函数签名**：`func agentTenantAccess(ctx context.Context, userStore store.UserStore, callerID uint, ad *model.AgentDefinition) error`

**验收条件**：
- `go test ./internal/numind/biz/agent/ -run TestAgentTenantAccess` 全 PASS。
- 矩阵：父+active✓ / 父+inactive✓ / 子+active✓ / 子+inactive✗ErrSkillNotFound / 子+别租户✗ / 独立用户✗ / nil-store+父✓ / nil-store+子✗(7b) / **父跑别父 agent(慢路径 nil 守卫)✗ 不 panic(9)**。
- `task lint` exit 0。
- 含 `gorm.ErrRecordNotFound`→ErrSkillNotFound、其他 err 包装。

**原子性**：仅新增 2 文件，编译独立（依赖现有 store.UserStore / model / errno）。reviewer 可独立验证纯逻辑。

---

## Task 2 — 接线 userStore + 三个校验点改用 helper (集成 + 测试)

**目标**：把 `userStore` 注入 `agentRunner` 与 `StudentRunService`，三处 gate 改用 helper，加集成测试（含 Answer 续跑用例 10）。

**涉及文件**：
- `internal/numind/biz/agent/runner.go`（`agentRunner` 加 `userStore store.UserStore` 字段 + `WithUserStore` RunnerOption；line 470 gate 改用 helper）
- `internal/numind/biz/agent/runner_runstream.go`（line 134 gate 改用 helper）
- `internal/numind/biz/agent/student_run_lifecycle.go`（`StudentRunService` 加 `userStore` 字段 + `WithUserStore` 链式方法；`resolveDefinition` line 683 gate 改用 helper）
- `internal/numind/biz/biz.go`（`NewAgentRunner` 追加 `agent.WithUserStore(ds.Users())`；`NewStudentRunService(...)` 后链 `.WithUserStore(ds.Users())`）
- `internal/numind/biz/agent/student_run_lifecycle_test.go`（gate #1：加子账户 access 测试，fake userStore，子+active→允许 / 子+inactive→拒 / 子+别租户→拒，经 Estimate/Create→resolveDefinition）
- `internal/numind/biz/agent/answer_test.go`（gate #2，**用例 10**：子账户经 Answer→runner.Run 续跑，active 父 agent 放行 / inactive 经 R9 拒；fake userStore）
- `internal/numind/biz/agent/runner_runstream_test.go`（gate #3，**用例 10b（P2 必加）**：子账户**直接经 RunStream**——生产流式路径——active 父 agent 放行 / inactive R9 拒 / 别租户拒；fake userStore + skillStore。不可只靠 dev e2e 兜底 gate #3 回归）

**步骤**：
1. 加 `WithUserStore` 到两结构体 + biz.go 接线。
2. 三处 gate `if ad.ParentUserID != X { return ErrSkillNotFound }` → `if err := agentTenantAccess(ctx, <store>, X, ad); err != nil { return ..., err }`。
3. 写/扩集成测试。
4. commit：`feat(agent): allow B2B2C child accounts to run parent-configured agents`。

**验收条件**：
- `go test ./internal/numind/biz/agent/... ./internal/numind/biz/...` 全 PASS（含既有测试不回归 — nil-store 降级保旧行为）。
- 子账户经 Estimate/Create(resolveDefinition) + RunStream/Run(runner) + Answer(gate #2) 三路径均放行 active 父 agent、拒 inactive(R9)、拒别租户。
- 隔离回归：`verifyRunOwnership`/`verifySessionOwnership`/`GetRun`/`WriteFeedback` 未改动（reviewer grep 确认）。
- `task lint` exit 0。

**原子性**：完成后系统可编译可运行，三处 gate 一致使用 helper。依赖 Task 1 的 helper（串行）。

---

## S5 验证策略（NDF Rule 10）

见 spec §6。Go 单测矩阵（Task 1+2 永久回归）+ dev 子账户 e2e 冒烟（`E2E_CHILD_USERNAME/PASSWORD`）：子账户登录→首页见父 agent→`/agent/chat` 跑通一次（无 ErrSkillNotFound）。
**夹具前置**：dev 需先 seed/造一个「父账户拥有的 active agent」+ 挂该父的子账户（admin id=30 名下当前无 agent；"从零创建"有 422 bug）。

## 依赖图
Task 1 → Task 2（Task 2 用 Task 1 的 helper）。无环。两 task 串行。

## 文件冲突注记（跨 feature）
并行 feature `agent-mode-billing` 同改 `runner.go`/`runner_runstream.go`/`biz.go`/`student_run_lifecycle.go`。两 feature 各自 worktree 独立；先 merge 到 develop 者无冲突，后者在 `ndf-done` merge 时解冲突（gate 逻辑 vs billing 接线在不同代码块，预期低冲突）。
