# Agent 模式 Runtime Skeleton — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task.

**Goal:** 把 Phase 0 V2 demo 工程化为 `internal/numind/biz/agent/` 下的 production Runtime 骨架（含 agent_run 表 + Store + AgentRunner + RunHooks + ToolCallingChatModel adapter + 19-reason 状态机 + AbortController 三层 + Withhold 两 chain + 最小 Tool interface + Unit + Integration + race tests）。

**Architecture:** numind-server 单仓库。新增包：`internal/numind/biz/agent/`；新增 model：`agent_run.go`；新增 store：`agent_run.go`；新增 migration：`20260520_120000_create_agent_run_table.sql`（+ rollback）。无前端改动。

**Tech Stack:** Go 1.24 + Gin + GORM v2 + MySQL 8.0 + cloudwego/eino v0.8.13（已 pin） + gorm.io/datatypes（已含）

**Spec 引用**：[2026-05-20-agent-mode-runtime-skeleton-design.md](../specs/2026-05-20-agent-mode-runtime-skeleton-design.md)（S2 gate 通过，2 P1 + 4 P2 已吸收）

---

## 文件清单

### 新建

| 路径 | 职责 |
|---|---|
| `migrations/20260520_120000_create_agent_run_table.sql` | Forward migration（DDL，含 CHECK constraint） |
| `migrations/20260520_120000_create_agent_run_table_rollback.sql` | Rollback (DROP TABLE) |
| `internal/pkg/model/agent_run.go` | AgentRun GORM model + TableName |
| `internal/numind/store/agent_run.go` | IAgentRunStore interface + impl |
| `internal/numind/store/agent_run_test.go` | Store 单测（in-memory SQLite + AutoMigrate）。**注**：SQLite < 3.25 不强制 CHECK constraint，state_reason 非法值校验在 dev MySQL 部署时才完整生效；本单测覆盖 happy path，**CHECK 约束生效**靠 MySQL dev 验证 |
| `internal/numind/biz/agent/state.go` | 19 reason constants + LoopState + Transition() |
| `internal/numind/biz/agent/state_test.go` | 19 reason 各自独立 test case |
| `internal/numind/biz/agent/hooks.go` | RunHooks struct + HookAction enum |
| `internal/numind/biz/agent/hooks_test.go` | Hook 三种 action 返回路径测试 |
| `internal/numind/biz/agent/abort.go` | AbortController helper（queryCtx/batchCtx/toolCtx 派生 + Cancel） |
| `internal/numind/biz/agent/abort_test.go` | Cancel 级联传播测试 + race detector |
| `internal/numind/biz/agent/withhold.go` | Withhold 两 chain handler |
| `internal/numind/biz/agent/withhold_test.go` | PTL / max_output chain 优先级 + 步数限制测试 |
| `internal/numind/biz/agent/tool.go` | 最小 Tool interface + einoToolAdapter |
| `internal/numind/biz/agent/tool_test.go` | Tool adapter + mock 工具测试 |
| `internal/numind/biz/agent/adapter.go` | AiserviceAdapter（实现 ToolCallingChatModel 三方法） |
| `internal/numind/biz/agent/adapter_test.go` | adapter Generate/Stream/WithTools 测试 |
| `internal/numind/biz/agent/runner.go` | AgentRunner（Run + Cancel）+ in-mem cancel registry |
| `internal/numind/biz/agent/runner_test.go` | Runner 单测（mock adapter + mock tool） |
| `internal/numind/biz/agent/runner_integration_test.go` | 5 步 mock ReAct loop 集成测试 |

### 修改

| 路径 | 改动 |
|---|---|
| `internal/numind/store/store.go` | IStore interface 加 `AgentRuns() IAgentRunStore` + 工厂注册 |
| `internal/numind/helper.go` | AutoMigrate 列表加 `&model.AgentRun{}`（**正确路径已实测**：`internal/numind/helper.go`，package `numind`，不是 `internal/pkg/model/helper.go`） |
| `internal/numind/biz/biz.go` | 包级 `var B IBiz` 暴露 `Agents() AgentRunner`（若现有模式如此）；或 wire 中接入 |

> **零变更**：controller / router / API / 前端 / migrations 之外的 SQL / config_*.yaml。

---

## TOC（按依赖分阶段）

### Phase 1：基础设施（无依赖，可强并行 Tier 3）
- **Task 1**: M1 DB migration + model + AutoMigrate
- **Task 2**: M4 状态机 19 reason + transitions
- **Task 3**: M7 最小 Tool interface + Eino adapter
- **Task 4**: M5 AbortController 三层 helper

### Phase 2：Store + Withhold（依赖 Phase 1 部分）
- **Task 5**: M2 Store impl + 单测（依赖 Task 1 完成）
- **Task 6**: M6 Withhold recovery 两 chain（依赖 Task 2 状态机常量）

### Phase 3：核心 Runtime（依赖 Phase 1+2 全部）— **拆为 Task 7a + 7b 降低 review 风险**
- **Task 7a**: M3 hooks + AiserviceAdapter（hooks.go / hooks_test.go / adapter.go / adapter_test.go）
- **Task 7b**: M3 AgentRunner Run/Cancel + in-mem cancel registry + biz 接入（runner.go / runner_test.go / biz.go 接入）

### Phase 4：集成测试
- **Task 8**: M8 集成测试 + race detector（5 步 mock ReAct loop，跨 Runner+state+abort+withhold+tool 全套）

---

## 并行 Tier 评估（NDF Rule 12）

### Phase 1 强并行（Tier 3 — disjoint）

| Agent | 文件归属 |
|-------|---------|
| Agent A (Task 1) | `migrations/20260520_120000_create_agent_run_table.sql` + `_rollback.sql` / `internal/pkg/model/agent_run.go` / `internal/numind/helper.go`（局部修改 AutoMigrate 列表）|
| Agent B (Task 2) | `internal/numind/biz/agent/state.go` + `state_test.go` |
| Agent C (Task 3) | `internal/numind/biz/agent/tool.go` + `tool_test.go` |
| Agent D (Task 4) | `internal/numind/biz/agent/abort.go` + `abort_test.go` |

**ndf-check-disjoint 命令**（每组用**逗号分隔**，符合脚本 Usage 规范）：

```bash
bash /Users/zhiyuchen/Documents/10_跃迁有数/有数AI工作台/莫小派/Codes/numind-server/scripts/ndf/ndf-check-disjoint.sh \
  "migrations/20260520_120000_create_agent_run_table.sql,migrations/20260520_120000_create_agent_run_table_rollback.sql,internal/pkg/model/agent_run.go,internal/numind/helper.go" \
  "internal/numind/biz/agent/state.go,internal/numind/biz/agent/state_test.go" \
  "internal/numind/biz/agent/tool.go,internal/numind/biz/agent/tool_test.go" \
  "internal/numind/biz/agent/abort.go,internal/numind/biz/agent/abort_test.go"
```

预期 exit 0（完全 disjoint）。

> **`helper.go` 风险**：Agent A 改 `internal/pkg/model/helper.go` 的 AutoMigrate 列表。该文件可能被其他 dev 同时改（rare，单人项目可控）。`ndf-check-disjoint` 验证 Phase 1 四组互相 disjoint 即足够。

### Phase 2 串行依赖

- Task 5 (M2 Store) 依赖 Task 1 完成的 model 文件 → **必须 Task 1 commit 后启动**
- Task 6 (M6 Withhold) 依赖 Task 2 完成的 19 reason constants → **必须 Task 2 commit 后启动**
- Task 5 + Task 6 写的文件不重叠（`store/agent_run.go` vs `biz/agent/withhold.go`），可**互相并行**

### Phase 3 单 task 顺序

- Task 7 (M3 AgentRunner + Adapter) **依赖全部前置**（M1+M2+M4+M5+M6+M7）→ 串行最后

### Phase 4 单 task 顺序

- Task 8 (M8 集成测试) 最后

---

## Task 1：M1 DB migration + model + AutoMigrate

### 实现清单

- [ ] T1.1：写 `migrations/20260520_120000_create_agent_run_table.sql`（spec §2.1 DDL）+ rollback
- [ ] T1.2：写 `internal/pkg/model/agent_run.go`（spec §2.3 GORM model；含 `datatypes.JSON` + nullable `*uint64 ReservationID` + `*time.Time EndedAt`）
- [ ] T1.3：在 `internal/pkg/model/helper.go` AutoMigrate 列表加 `&AgentRun{}`
- [ ] T1.4：本地跑 `go build ./...` 通过；`go vet ./...` 干净
- [ ] T1.5：手工跑 migration on local MySQL/SQLite 验证 DDL 合法（CHECK constraint 在 SQLite 可能失败，可降级到 app 层校验，但 MySQL 8.0 必须支持）
- [ ] T1.6：commit `feat(agent-runtime): M1 agent_run table migration + GORM model`

### 验收

```bash
go build ./...
go vet ./internal/pkg/model/...
# manual: run migration SQL against local MySQL, check schema
mysql -e "DESCRIBE agent_run;"
```

### Reviewer dispatch

- spec-compliance：核对 DDL 与 spec §2.1 字段一致 / CHECK constraint 完整
- code-quality：GORM tag 正确 / nullable 指针使用 / 无 dead field

---

## Task 2：M4 状态机 19 reason + transitions

### 实现清单

- [ ] T2.1：写 `internal/numind/biz/agent/state.go`：
  - 12 `TerminalReason` typed string constants
  - 7 `ContinueReason` typed string constants
  - 编译期 array 长度断言（spec §5.1）
  - `LoopState` struct（StepCount / TerminalReason / ContinueReason / PTLRetries / MaxOutputRetries）
  - `LoopEvent` enum（每个可能的 event）
  - `Transition(event)` function（返回 next state + isTerminal）
- [ ] T2.2：写 `state_test.go`：
  - **19 个 reason 各自独立 test case**（每个 reason 都能从某个 event 触发到，且不能被 unreachable）
  - state machine transitions table 完整性测试
- [ ] T2.3：`go test ./internal/numind/biz/agent/... -run TestState` 全 PASS
- [ ] T2.4：commit `feat(agent-runtime): M4 state machine (19 reasons)`

### Reviewer dispatch

- spec-compliance：核对 19 reason 字符串值与 spec §5.1 一致（避免 typo）
- code-quality：枚举常量风格 / transition 逻辑无 fallthrough / 测试覆盖完备

---

## Task 3：M7 最小 Tool interface + Eino adapter

### 实现清单

- [ ] T3.1：写 `internal/numind/biz/agent/tool.go`（spec §8）：
  - 内部 `Tool` interface（Name / Description / Run）
  - `einoToolAdapter` 实现 `tool.InvokableTool`（Info / InvokableRun）
- [ ] T3.2：写 `tool_test.go`：
  - mock 工具（`echoTool`：input → input）
  - 测试 einoToolAdapter Info + InvokableRun 调用正确
- [ ] T3.3：commit `feat(agent-runtime): M7 minimal Tool interface + Eino adapter`

---

## Task 4：M5 AbortController helper

### 实现清单

- [ ] T4.1：写 `internal/numind/biz/agent/abort.go`：
  - `DeriveQueryCtx(parent) (ctx, cancel)`
  - `DeriveBatchCtx(query) (ctx, cancel)`
  - `DeriveToolCtx(batch) (ctx, cancel)`
  - 严格父子链
- [ ] T4.2：写 `abort_test.go`：
  - `TestAbortController_CancelPropagation`（spec §6.3）— 含 defer cancel 防 lint
  - race detector 启动两个 goroutine 测试 cancel 信号到达
- [ ] T4.3：`go test ./internal/numind/biz/agent/... -race -run TestAbort` 全 PASS
- [ ] T4.4：commit `feat(agent-runtime): M5 AbortController three-layer ctx derivation`

---

## Task 5：M2 Store impl + 单测（依赖 Task 1）

### 实现清单

- [ ] T5.1：写 `internal/numind/store/agent_run.go`：
  - `IAgentRunStore` interface（spec §3.1，含 time import）
  - `agentRunStore` struct 实现（含 GORM db）
  - Create / Get / UpdateState / WriteTurn / ListBySession
- [ ] T5.2：在 `store/store.go` IStore interface 加 `AgentRuns() IAgentRunStore` + 工厂
- [ ] T5.3：写 `agent_run_test.go`：
  - in-memory SQLite + AutoMigrate
  - 覆盖每个方法 happy + edge case
  - **race detector 测试**：两个 goroutine 并发 WriteTurn 同一 id；**断言**：最终 messages 等于两次写入之一（last-write-wins 是 GORM/MySQL 行锁的预期行为），**无 panic / 无 data corruption / 无 race detector 报警**
- [ ] T5.4：`go test ./internal/numind/store/... -race -cover -run TestAgentRun` 覆盖率 ≥ 90%
- [ ] T5.5：commit `feat(agent-runtime): M2 IAgentRunStore + impl`

### Reviewer dispatch

- spec-compliance：方法签名与 spec §3.1 一致 / WriteTurn 真整体覆写
- code-quality：err wrapping / context 传播 / 索引使用

---

## Task 6：M6 Withhold recovery 两 chain（依赖 Task 2）

### 实现清单

- [ ] T6.1：写 `internal/numind/biz/agent/withhold.go`（spec §7）：
  - `MaxPTLRetries = 2` / `MaxOutputRetriesLimit = 2`（取蓝本 §4.1.6 canonical 值）
  - `handleError(err, state) (LoopEvent, error)` 函数实现 PTL > max_output 优先级
  - `isPTL(err) bool` / `isMaxOutput(err) bool` helper
- [ ] T6.2：写 `withhold_test.go`：
  - PTL chain：触发 1 次 → ContinueReactiveCompactRetry；触发 3 次 → TerminalPromptTooLong
  - max_output chain 同样
  - PTL > max_output 优先级测试（同时触发时 PTL 先处理）
- [ ] T6.3：commit `feat(agent-runtime): M6 Withhold recovery (PTL + max_output chains)`

---

## Task 7a：M3 Hooks + AiserviceAdapter（依赖 Task 1/2/3/4/5/6）

### 实现清单

- [ ] T7a.1：写 `internal/numind/biz/agent/hooks.go`：
  - `HookAction` enum 三值（HookActionContinue / HookActionStop / HookActionBlockingStop）
  - `RunHooks` struct（PreToolCall / PostToolCall func 字段，返回 (HookAction, error)）
  - HookAction → reason 映射 helper
- [ ] T7a.2：写 `hooks_test.go`：覆盖三种 action 返回 + 映射 reason 正确
- [ ] T7a.3：写 `internal/numind/biz/agent/adapter.go`（spec §4.3）：
  - `aiserviceAdapter` struct（modelName / taskID / tools immutable）
  - 实现 `model.ToolCallingChatModel`：Generate / Stream / WithTools（克隆体）
  - `convertReq` / `convertResp` / `wrapStreamReader` helpers
- [ ] T7a.4：写 `adapter_test.go`：
  - mock aiservice.Chat 测试 Generate 调用 3 参数正确
  - WithTools 测试克隆体独立（receiver tools 不变）
- [ ] T7a.5：`go build ./internal/numind/biz/agent/...` 通过 + `go test ./internal/numind/biz/agent/... -run "Hooks|Adapter"` PASS
- [ ] T7a.6：commit `feat(agent-runtime): M3a Hooks + AiserviceAdapter`

## Task 7b：M3 AgentRunner + biz 接入（依赖 Task 7a）

### 实现清单（**严格 commit 顺序**：T7b.1-T7b.4 编译通过 → T7b.5 biz.go 接入 → 整包 `go build ./...` 通过 → 整体 commit）

- [ ] T7b.1：写 `internal/numind/biz/agent/runner.go`（spec §4.4）：
  - `AgentRunner` interface（Run + Cancel(runID uint64) bool）
  - `agentRunner` struct（含 in-mem cancelRegistry map[uint64]context.CancelFunc + sync.Mutex）
  - Run() 主流程：Create → langfuse trace → 三层 ctx → Eino agent.Generate loop → state.Transition → WriteTurn → UpdateState → cancel registry add/remove
  - Cancel(runID) 在 cancelRegistry 找该 runID 调用 cancel func；不存在返回 false
- [ ] T7b.2：写 `runner_test.go`（mock adapter + mock tool + mock state + mock store via interface）
- [ ] T7b.3：`go build ./internal/numind/biz/agent/...` 通过（**此时仅 biz/agent 包编译，main server 还未引用**）
- [ ] T7b.4：`go test ./internal/numind/biz/agent/... -race -run TestRunner` PASS
- [ ] T7b.5：在 `internal/numind/biz/biz.go` 加 `Agents() agent.AgentRunner` 到 IBiz interface + factory 接入（**此时 main server 才引用 biz/agent 包**）
- [ ] T7b.6：`go build ./...`（**整包编译**）通过 — 防止 biz/agent 包被 main server 引用后编译断链
- [ ] T7b.7：commit `feat(agent-runtime): M3b AgentRunner + biz integration`

---

## Task 8：M8 集成测试 + race detector

### 实现清单

- [ ] T8.1：写 `internal/numind/biz/agent/runner_integration_test.go`：
  - mock Eino LLM 返回（5 步 ReAct）：step1 调用 mockTool → step2 调用 → ... step5 final answer
  - 集成测试断言：AgentRunner 跑通 → DB agent_run 行存在 → status=terminated → state_reason=completed
  - 测试 cancel 路径：另一 goroutine 调用 runner.Cancel(runID) → 主 goroutine ctx.Done 收到 → state_reason=aborted_streaming
  - 测试 hook 路径：注入 PreToolCall returning HookActionStop → state_reason=hook_stopped
- [ ] T8.2：`task test`（含 -race -cover）整体 PASS；覆盖率 ≥ 85% (biz/agent 包)
- [ ] T8.3：commit `feat(agent-runtime): M8 integration tests + race detector`

---

## S5 验证策略（NDF Rule 10）

### 验证方式

**纯后端：Go unit + integration tests + race detection**。无 Playwright，无 gstack `/qa`。

### 理由

- #2 不出 UI，无浏览器路径
- #2 不暴露 API，无 HTTP 集成测试需要
- AbortController + WriteTurn 涉及并发，必须 `-race` flag
- 19-reason 状态机 / Withhold 优先级是 logic 正确性问题，unit test 最直接

### 关键用户路径（开发者自检）

1. `go test ./internal/numind/biz/agent/... -race -cover` → 覆盖率 ≥ 85% + 0 race
2. `go test ./internal/numind/store/... -race -run TestAgentRun` → all PASS
3. `task test` 完整套件 PASS（不破坏现有测试）
4. `task lint` clean
5. dev container 部署前：**手工 SSH dev MySQL 跑 migration**（`sshpass -p "$DEV_SSH_PASS" ssh $DEV_SSH_USER@$DEV_SSH_HOST 'mysql numind < /tmp/20260520_120000_create_agent_run_table.sql'`，遵循 [[project_dev_deploy_migration_gap]] memory：dev/prod CI 不跑 migration），然后 rsync + build + push TCR + dev docker pull + `GET /healthz` 200 + `docker logs` 无 panic

### 回归保护诚实声明

| 产物 | 回归 |
|------|------|
| migration | 一次性，dev/qa/prod 各跑一次（**#2 仅 dev**） |
| GORM model + Store unit tests | 进 `task test` 主套件，永久回归保护 |
| biz/agent unit tests | 同上 |
| biz/agent integration tests | 同上（mock Eino，无外部依赖） |

**关键**：本 feature 是 production 代码，**所有测试必须进 CI 主套件**（与 Phase 0 reference-only 测试不同）。

### 必停场景（真阻塞）

1. dev 数据库 migration 失败 → 必停，让用户检查 dev MySQL 状态
2. `aiservice.Chat` 真实签名 / 真实 ChatRequest struct 与 spec 不匹配 → 必停，调研真实 API 后修正 adapter
3. Eino v0.8.13 `model.ToolCallingChatModel` interface 在真实代码与 spec §4.3 不一致 → 必停，read 源码核对，按真实 API 修正 adapter

### NOT 必停（AI 自主修）

- reviewer P0/P1 → AI 自己修
- task 内 unit test 失败 → AI 修代码 + 重测
- race detector 触发 → AI 修 goroutine / mutex / channel

---

## 主 session dispatch 与 commit 验证（NDF Rule 8 + 12）

每个 Task 完成顺序：

1. 主 session dispatch implementer Agent（Phase 1 四路并行，跑 `ndf-check-disjoint`；Phase 2/3/4 串行）
2. **Phase 1 batch dispatch（4 路同 turn）**：
   - 单条消息含 4 个 Agent tool call
   - 每个 Agent 拿到自己的文件归属 + spec / plan 引用
3. Agent 返回后主 session 验证：
   - `git -C /private/tmp/wt-agent-mode-runtime-skeleton-numind-server log --oneline -10`
   - `git -C ... status` 确认无遗留
4. **并行** dispatch spec-compliance + code-quality reviewer（同 turn 2 个 Agent）
5. P0/P1 修 → 重 dispatch reviewer
6. `manifest.progress.reviewed_tasks +=1`
7. 进 Phase 2 / Phase 3 / Phase 4

---

## ndf-done 前置门槛（S6 进入标准）

- [ ] manifest `progress.completed_tasks == 9`（M1-M6 + M7a + M7b + M8）
- [ ] manifest `progress.reviewed_tasks == 9`
- [ ] manifest `stage == S6`
- [ ] 全部 19 文件存在并 commit
- [ ] `task test` 全 PASS（含 -race -cover）
- [ ] `task lint` 干净
- [ ] biz/agent 包覆盖率 ≥ 85%
- [ ] 无 P0/P1 残留
- [ ] dev container 部署成功 + `/healthz` 200 + `docker logs` 无 panic
- [ ] **未部署到 qa/prod 任一环境**
- [ ] `ndf-done` 原子化 merge → develop

---

**Plan 完成。等待独立 reviewer 审 task 原子性 + S5 验证策略。**
