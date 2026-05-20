# NDF S3 Task Plan · `agent-mode-permission-pipeline`

**Track**：Standard
**Feature ID**：`agent-mode-permission-pipeline`（#6/14）
**起草日期**：2026-05-21
**状态**：S3 草案
**前置 stage**：S2 通过（commit `b3e99d4b`）

---

## §1 任务分解（M1-M13，共 13 个）

### M1 — Migration SQL（双文件）

**输入**：S2 §2.1-§2.3
**输出**：
- `numind-server/migrations/20260521_120000_agent_permission_pipeline.sql` — CREATE TABLE 双表
- `numind-server/migrations/20260521_120000_agent_permission_pipeline_rollback.sql` — DROP TABLE 双表

**验收**：
- 双文件存在
- SQL 含 InnoDB / utf8mb4 / 完整字段含 DEFAULT CURRENT_TIMESTAMP
- 复合索引正确（idx_apc_parent_active / idx_apdl_run_tool / idx_apdl_parent_created）

**LOC 估算**：50 行

---

### M2 — GORM Models + AutoMigrate

**输入**：S2 §2.4-§2.5
**输出**：
- `numind-server/internal/pkg/model/agent_permission.go`（NEW）— AgentPermissionConfig + AgentPermissionDecisionLog 含 TableName() 方法 + autoCreateTime/autoUpdateTime tags
- `numind-server/internal/numind/helper.go`（**修改**）— AutoMigrate 加 2 张表

**测试**：
- `numind-server/internal/pkg/model/agent_permission_test.go` — TableName 测试 + GORM `default:true` Create test (config table)

**验收**：
- 字段类型与 user.parent_user_id 对齐 (INT UNSIGNED → uint)
- ID 字段 uint64 + autoIncrement
- 单测覆盖 `is_active=false` Create 正确持久化（UpdateColumn fixup 模式；database.md §6）
- `go test ./internal/pkg/model/...` PASS

**LOC 估算**：80 行 + 100 行测试

---

### M3 — Store 层

**输入**：S2 §3
**输出**：
- `numind-server/internal/numind/store/agent_permission.go`（NEW）— IAgentPermissionStore 接口 + agentPermissionStore 实现 + CreateRule 含 UpdateColumn fixup
- `numind-server/internal/numind/store/store.go`（**修改**）— IStore 加 AgentPermissions() 方法 + datastore.AgentPermissions() impl

**测试**：
- `numind-server/internal/numind/store/agent_permission_test.go`（NEW）— in-memory SQLite tests
  - ListActiveByParent: empty / has rules / 跨父账户隔离
  - CreateRule: 含 is_active=false UpdateColumn fixup 验证
  - CreateDecisionLog: 写一行 + 查 INDEX 工作

**验收**：
- 3 个 store 方法 + ≥ 5 个测试 case
- `go test ./internal/numind/store/...` PASS
- 覆盖率 ≥ 85%

**LOC 估算**：100 行 + 150 行测试

---

### M4 — biz/permission 基础类型

**输入**：S2 §4.1-§4.3 + §4.7
**输出**：
- `numind-server/internal/numind/biz/permission/result.go`（NEW）— DecisionReasonType 11 个常量 + PermissionResult struct + PendingClassifierCheck stub + PermissionUpdate stub + Behavior 常量
- `numind-server/internal/numind/biz/permission/request.go`（NEW）— PermissionRequest struct
- `numind-server/internal/numind/biz/permission/validator.go`（NEW）— Validator interface + Passthrough/Allow/Deny/Ask helper builders
- `numind-server/internal/numind/biz/permission/digest.go`（NEW）— Digest(s) SHA-256 64 hex

**测试**：
- `numind-server/internal/numind/biz/permission/digest_test.go` — SHA-256 输出验证 + edge case (empty / unicode)
- `numind-server/internal/numind/biz/permission/result_test.go` — builder helpers round trip + Behavior 常量
- `numind-server/internal/numind/biz/permission/validator_test.go` — Validator interface compile-time check via stub

**验收**：
- 编译通过；包结构干净（permission → biz/agent 单向依赖；request.go 的 `Tool agent.FullTool` 字段为合法正向依赖）
- biz/permission **绝不** import biz/permission/validators（validators 反过来 import biz/permission）
- 11 个 DecisionReason 常量定义
- digest 测试 100% 覆盖
- `go test ./internal/numind/biz/permission/...` PASS

**LOC 估算**：80 行 + 100 行测试

---

### M5 — PermissionPipeline.Check

**输入**：S2 §4.4
**输出**：
- `numind-server/internal/numind/biz/permission/pipeline.go`（NEW）— PermissionPipeline struct + NewPipeline 工厂 + Check 主入口

**测试**：
- `numind-server/internal/numind/biz/permission/pipeline_test.go`（NEW）—
  - 全 passthrough → default allow + DecisionReasonOther + ValidatorID=DefaultAllow
  - 第 N 个返回 deny → 取该 result（短路）
  - 第 N 个返回 ask → 取该 result（短路；ask 也算 terminal）
  - 第 N 个返回 allow → 取该 result（短路）
  - 0 validators → default allow

**验收**：
- 5 个测试 case 全 PASS
- 覆盖率 100%
- `go test -race ./internal/numind/biz/permission/...` PASS

**LOC 估算**：30 行 + 100 行测试

---

### M6 — PermissionGate + Audit

**输入**：S2 §4.5-§4.6
**输出**：
- `numind-server/internal/numind/biz/permission/gate.go`（NEW）— PermissionGate struct + Options（WithStore/WithSkillStore/WithValidators/WithAuditChannelSize/WithAuditLogger）+ NewPermissionGate + Check + drainAudit goroutine + Close()
- `numind-server/internal/numind/biz/permission/audit.go`（NEW）— AuditLogger interface + dbAuditLogger 默认实现

**测试**：
- `numind-server/internal/numind/biz/permission/gate_test.go`（NEW）—
  - Check + audit 同步写入（小 chan + sync logger 验证）
  - audit channel full → warn 不阻塞（chan size=1，连发 10 个）
  - Close drain + 5s 超时（小 sleeper logger 验证 drain 行为）
  - Close 后 Check 走 warn 不进 channel
  - `go test -race` PASS（并发 send 验证）
- `numind-server/internal/numind/biz/permission/audit_test.go`（NEW）—
  - dbAuditLogger.Log 写一行（in-mem SQLite）
  - store 错误 → warn 不 panic

**验收**：
- 覆盖率 ≥ 85%
- race-safe（atomic + sync.Mutex + WaitGroup 正确使用）
- 5 + 2 = 7 个测试 case
- **drainAudit goroutine 退出验证**（S3 P2 reviewer fix）：5s timeout 路径下 goroutine 必须退出（无泄漏）；测试用 `runtime.NumGoroutine()` 前后对比 或 `goleak.VerifyNone(t)`（轻量内置 check）

**LOC 估算**：180 行 + 200 行测试

---

### M7 — 7 个 Validators 实现

**输入**：S2 §4.10.1-§4.10.7
**输出**：
- `numind-server/internal/numind/biz/permission/validators/platform_hard_rule.go`（NEW）+ test
- `numind-server/internal/numind/biz/permission/validators/sandbox_override.go`（NEW）+ test
- `numind-server/internal/numind/biz/permission/validators/tenant_admin_rule.go`（NEW）+ test
- `numind-server/internal/numind/biz/permission/validators/working_dir.go`（NEW）+ test
- `numind-server/internal/numind/biz/permission/validators/tool_flag.go`（NEW）+ test
- `numind-server/internal/numind/biz/permission/validators/user_session_rule.go`（NEW）+ test
- `numind-server/internal/numind/biz/permission/validators/classifier_placeholder.go`（NEW）+ test

**测试**：每个 validator ≥ 3 case（见 S2 §7 表）

**验收**：
- 7 个 validator + 7 个 test 文件
- 总覆盖率 ≥ 85%
- `go test ./internal/numind/biz/permission/validators/...` PASS

**LOC 估算**：300 行 + 400 行测试

---

### M8 — biz/agent NEW ctx 助手 + PermissionDenialDetail

**输入**：S2 §4.1（permission_denial.go）+ §5.3（permission_sink.go）+ §5.4（full_tool_ctx.go）+ §5.6（agent_def_ctx.go）

> **§5.6 注**：spec 中此节标号变化过，实际指 `agent_def_ctx.go`（见 spec §5.6 修订）。

**输出**（4 个新文件，全在 biz/agent 包）：
- `numind-server/internal/numind/biz/agent/permission_denial.go`（NEW）— PermissionDenialDetail struct + String()
- `numind-server/internal/numind/biz/agent/permission_sink.go`（NEW）— sink ctx key + WithPermissionSink + PermissionSinkFromCtx
- `numind-server/internal/numind/biz/agent/full_tool_ctx.go`（NEW）— FullToolMap ctx key + WithFullToolMap + FullToolFromCtx
- `numind-server/internal/numind/biz/agent/agent_def_ctx.go`（NEW）— agentDefCtx struct + WithAgentDefCtx + AgentDefAndParentFromCtx

**测试**（4 个新 test 文件）：
- `permission_denial_test.go` — String() output + nil safety
- `permission_sink_test.go` — round trip + nil ctx
- `full_tool_ctx_test.go` — round trip + missing tool
- `agent_def_ctx_test.go` — round trip + nil ctx returns (0, 0)

**验收**：
- 4 个新文件 + 4 个测试文件
- 包内仅 import "context"（permission_denial.go 加 "encoding/json"）
- 不 import 任何 biz/permission（单向依赖）
- 100% 覆盖率
- `go test ./internal/numind/biz/agent/...` 不下降

**LOC 估算**：120 行 + 150 行测试

---

### M9 — biz/agent 现有文件改造（HookAction = 3 + state.go 新值）

**输入**：S2 §5.1 + §5.2

**输出**（**修改** 现有文件）：
- `numind-server/internal/numind/biz/agent/hooks.go`（**修改**）— 加 HookActionPermissionDeny = 3 + HookActionToLoopEvent 加 case
- `numind-server/internal/numind/biz/agent/state.go`（**修改**）— 加 TerminalPermissionDenied + LoopEventPermissionDenied + 验证集合 [12]→[13] + Transition switch 加 case

**测试**（**修改** + 新增）：
- `numind-server/internal/numind/biz/agent/hooks_test.go`（**修改/扩展**）—
  - Record(HookActionPermissionDeny) → LastAction == HookActionPermissionDeny（atomic.Int32 涵盖 3）
  - HookActionToLoopEvent(HookActionPermissionDeny) == LoopEventPermissionDenied
- `numind-server/internal/numind/biz/agent/state_test.go`（**修改/扩展**）—
  - Transition(LoopEventPermissionDenied) → (TerminalPermissionDenied, "", true)
  - 验证集合编译期 [13] 数组 PASS

**验收**：
- 现有所有 hooks_test / state_test 测试 PASS（不破坏）
- 新增 4 个测试 case
- `go test ./internal/numind/biz/agent/...` PASS

**LOC 估算**：10 行 + 50 行测试

---

### M10 — wrap_hooks.go

**输入**：S2 §4.9 + §5.5

**输出**：
- `numind-server/internal/numind/biz/permission/wrap_hooks.go`（NEW）— WrapHooks 函数 + buildRequest + registryFromBase

**测试**：
- `numind-server/internal/numind/biz/permission/wrap_hooks_test.go`（NEW）—
  - (a) gate 返回 deny → wrapper 返回 HookActionPermissionDeny + sink 收到 detail + Registry.Record 调用
  - (b) gate 返回 allow → wrapper 透传 base.PreToolCall
  - (c) UpdatedInput nil → input 原样透传
  - (d) UpdatedInput 非 nil → marshal + 透传修改后 input（stub validator 返回固定 UpdatedInput）
  - (e) base 为 nil → wrapper 退化（permission only）
  - (f) PostToolCall 透传 base.PostToolCall
  - (g) buildRequest 失败 → fail-open 透传 base
  - (h) gate 返回未知 behavior → fail-open warn

**验收**：
- 覆盖率 ≥ 85%
- 8 个测试 case PASS
- `go test -race ./internal/numind/biz/permission/...` PASS

**LOC 估算**：120 行 + 250 行测试

---

### M11 — runner.go 集成

**输入**：S2 §5.3 + §6.1

**输出**（**修改** 现有 runner.go）：
- `numind-server/internal/numind/biz/agent/runner.go`（**修改**）—
  - RunResult 加 `PermissionDenial *PermissionDenialDetail` 字段
  - runner.Run Step 4.1: 创建 sinkCh + ctx = WithPermissionSink + 如 skillVer>0 加 WithAgentDefCtx
  - runner.Run Step 4.2: 构造 toolMap + ctx = WithFullToolMap
  - runner.Run Step 末尾: select sinkCh → 填 permDetail → RunResult.PermissionDenial = permDetail
  - HookActionRegistry LastAction == HookActionPermissionDeny → Transition(LoopEventPermissionDenied)

**测试**：
- `numind-server/internal/numind/biz/agent/runner_integration_test.go`（**扩展**）—
  - mock einoAgent + WrapHooks + stub deny validator → terminal_reason == "permission_denied" + RunResult.PermissionDenial 非 nil + 字段正确
  - mock einoAgent + WrapHooks + stub allow validator → terminal_reason == "completed"（沿用 #2 mock 路径）
  - 不传 sink（无 wrapper）→ RunResult.PermissionDenial == nil（向后兼容）

**验收**：
- 现有所有 runner_test / runner_integration_test 测试 PASS（不破坏）
- 3 个新集成测试 case
- biz/agent 覆盖率不下降（保持 80%+）

**LOC 估算**：40 行 + 200 行测试

---

### M12 — biz.go wire + errno + 端到端集成

**输入**：S2 §6 + §8

**输出**：
- `numind-server/internal/pkg/errno/permission.go`（NEW）— ErrPermissionGateUnavailable + ErrPermissionDenied 占位
- `numind-server/internal/numind/biz/biz.go`（**修改**）—
  - 构造 permGate
  - WrapHooks 包 sandbox + permission
  - runner 用 wrapped hooks
  - biz.go 暴露 `ClosePermissionGate()` 包级函数（S3 P2 reviewer fix — 不假设 main.go 有现成 shutdown hook 框架）
- `numind-server/cmd/numind/main.go`（**修改 — 仅当现有 shutdown sequence 可改时**）—
  - 在现有 server.Shutdown 后调 biz.ClosePermissionGate()
  - 如 main.go 无 shutdown hook 框架 → 留 biz.ClosePermissionGate() 暴露未用，仅保证测试可调；S4 实现者标注 follow-up

**测试**：
- `numind-server/internal/numind/biz/biz_permission_wire_test.go`（NEW）— wire-only smoke：构造 biz 实例 + 验证 permGate 非 nil + runner default hooks 非 nil

**验收**：
- biz.go 编译通过 + lint clean
- 端到端 smoke test PASS
- `go test ./...` + `task lint` 全 PASS
- `go test -race ./...` 全包 PASS

**LOC 估算**：50 行 + 80 行测试

---

### M13 — S5 验证策略 task

**输入**：S2 §10
**输出**：
- `numind-server/docs/superpowers/qa/2026-05-21-agent-mode-permission-pipeline-s5-acceptance.md`（NEW，S5 阶段填）

**S5 验证方式**：**纯后端 TDD**
- 不需要 Playwright/gstack 浏览器测试（本 feature 无 HTTP/UI）
- 验证手段：`go test -race ./...` PASS + 覆盖率达标 + 关键路径 8 个 case 逐项确认

**关键路径列表（S5 acceptance 必须确认）**：
1. PermissionPipeline.Check 全 passthrough → default allow + ValidatorID="DefaultAllow"
2. PlatformHardRule deny bash control char → terminal_reason == "permission_denied"
3. ToolFlag deny → terminal_reason == "permission_denied" + PermissionDenial.ValidatorID == "ToolFlag:<name>"
4. TenantAdminRule deny via DB rule → audit log 写入 + terminal_reason == "permission_denied"
5. UserSessionRule deny IsDestructive → terminal_reason == "permission_denied"
6. WrapHooks PreToolCall deny → 不调 base.PreToolCall（验证 sandbox borrow 不发生）
7. WrapHooks PreToolCall allow → 透传 base.PreToolCall（sandbox 启动验证）
8. PermissionGate.Close 后 Check 走同步 warn 不阻塞

**回归保护诚实声明**：
- Go 单元 + 集成测试 = 持久化回归保护（每次 CI 跑）
- 无 Playwright/gstack 一次性测试

**LOC 估算**：0 代码 + 400 行 acceptance 文档

---

## §2 文件归属表（Tier 3 disjoint check）

**Wave 1 并行**（M1 + M4 + M8 + M9，4 个 task 文件归属互不交集）：

```
M1 owns:
  numind-server/migrations/20260521_120000_agent_permission_pipeline.sql
  numind-server/migrations/20260521_120000_agent_permission_pipeline_rollback.sql

M4 owns:
  numind-server/internal/numind/biz/permission/result.go
  numind-server/internal/numind/biz/permission/request.go
  numind-server/internal/numind/biz/permission/validator.go
  numind-server/internal/numind/biz/permission/digest.go
  numind-server/internal/numind/biz/permission/result_test.go
  numind-server/internal/numind/biz/permission/validator_test.go
  numind-server/internal/numind/biz/permission/digest_test.go

M8 owns:
  numind-server/internal/numind/biz/agent/permission_denial.go
  numind-server/internal/numind/biz/agent/permission_sink.go
  numind-server/internal/numind/biz/agent/full_tool_ctx.go
  numind-server/internal/numind/biz/agent/agent_def_ctx.go
  numind-server/internal/numind/biz/agent/permission_denial_test.go
  numind-server/internal/numind/biz/agent/permission_sink_test.go
  numind-server/internal/numind/biz/agent/full_tool_ctx_test.go
  numind-server/internal/numind/biz/agent/agent_def_ctx_test.go

  ⚠️ M8 与 M9 同包并行编译约束（S3 P0 reviewer fix）：
  M8 的 4 个新文件**不得 import / 引用 HookAction / TerminalReason / LoopEvent 任何枚举常量**
  （M9 在同 Wave 添加这些常量但同包并行时 Go 编译器以包为单位编译；若 M8 引用
  HookActionPermissionDeny 等 M9 尚未提交的常量，M8 单独编译会失败）。
  具体：
    permission_denial.go    — Behavior 字段类型为 string（不是 HookAction），其他字段全 string
    permission_sink.go      — 仅 import "context"
    full_tool_ctx.go        — 仅 import "context"
    agent_def_ctx.go        — 仅 import "context"
  如 S4 实现时发现需要引用 M9 的常量 → 立即降级为 M9 → M8 串行。

M9 owns:
  numind-server/internal/numind/biz/agent/hooks.go
  numind-server/internal/numind/biz/agent/state.go
  numind-server/internal/numind/biz/agent/hooks_test.go
  numind-server/internal/numind/biz/agent/state_test.go
```

跑 `ndf-check-disjoint`（逗号分隔）：

```bash
bash numind-server/scripts/ndf/ndf-check-disjoint.sh \
  "numind-server/migrations/20260521_120000_agent_permission_pipeline.sql,numind-server/migrations/20260521_120000_agent_permission_pipeline_rollback.sql" \
  "numind-server/internal/numind/biz/permission/result.go,numind-server/internal/numind/biz/permission/request.go,numind-server/internal/numind/biz/permission/validator.go,numind-server/internal/numind/biz/permission/digest.go,numind-server/internal/numind/biz/permission/result_test.go,numind-server/internal/numind/biz/permission/validator_test.go,numind-server/internal/numind/biz/permission/digest_test.go" \
  "numind-server/internal/numind/biz/agent/permission_denial.go,numind-server/internal/numind/biz/agent/permission_sink.go,numind-server/internal/numind/biz/agent/full_tool_ctx.go,numind-server/internal/numind/biz/agent/agent_def_ctx.go,numind-server/internal/numind/biz/agent/permission_denial_test.go,numind-server/internal/numind/biz/agent/permission_sink_test.go,numind-server/internal/numind/biz/agent/full_tool_ctx_test.go,numind-server/internal/numind/biz/agent/agent_def_ctx_test.go" \
  "numind-server/internal/numind/biz/agent/hooks.go,numind-server/internal/numind/biz/agent/state.go,numind-server/internal/numind/biz/agent/hooks_test.go,numind-server/internal/numind/biz/agent/state_test.go"
```

**Wave 2 串行**（M2 → M3）— store 改 store.go 与 M11 改 runner.go 不并行（runner.go 在 M11 single-task wave）

**Wave 3 并行**（M5 + M6 + M7，3 个 task）：

```
M5 owns:
  numind-server/internal/numind/biz/permission/pipeline.go
  numind-server/internal/numind/biz/permission/pipeline_test.go

M6 owns:
  numind-server/internal/numind/biz/permission/gate.go
  numind-server/internal/numind/biz/permission/audit.go
  numind-server/internal/numind/biz/permission/gate_test.go
  numind-server/internal/numind/biz/permission/audit_test.go

M7 owns:
  numind-server/internal/numind/biz/permission/validators/*.go (all 14 files: 7 prod + 7 test)
```

Wave 3 disjoint check：

```bash
bash numind-server/scripts/ndf/ndf-check-disjoint.sh \
  "numind-server/internal/numind/biz/permission/pipeline.go,numind-server/internal/numind/biz/permission/pipeline_test.go" \
  "numind-server/internal/numind/biz/permission/gate.go,numind-server/internal/numind/biz/permission/audit.go,numind-server/internal/numind/biz/permission/gate_test.go,numind-server/internal/numind/biz/permission/audit_test.go" \
  "numind-server/internal/numind/biz/permission/validators/platform_hard_rule.go,numind-server/internal/numind/biz/permission/validators/sandbox_override.go,numind-server/internal/numind/biz/permission/validators/tenant_admin_rule.go,numind-server/internal/numind/biz/permission/validators/working_dir.go,numind-server/internal/numind/biz/permission/validators/tool_flag.go,numind-server/internal/numind/biz/permission/validators/user_session_rule.go,numind-server/internal/numind/biz/permission/validators/classifier_placeholder.go,numind-server/internal/numind/biz/permission/validators/platform_hard_rule_test.go,numind-server/internal/numind/biz/permission/validators/sandbox_override_test.go,numind-server/internal/numind/biz/permission/validators/tenant_admin_rule_test.go,numind-server/internal/numind/biz/permission/validators/working_dir_test.go,numind-server/internal/numind/biz/permission/validators/tool_flag_test.go,numind-server/internal/numind/biz/permission/validators/user_session_rule_test.go,numind-server/internal/numind/biz/permission/validators/classifier_placeholder_test.go"
```

**Wave 4 单 task**：M10 wrap_hooks.go（依赖 M6 + M8 + M4）

**Wave 5 单 task**：M11 runner.go integration（依赖 M10 + M9 + M8）

**Wave 6 单 task**：M12 biz.go wire + errno + smoke（依赖 ALL prior）

**Wave 7 文档 task**：M13 S5 acceptance（S5 阶段写）

---

## §3 Wave Schedule（Wave-level）

```
Wave 1 (parallel, 4 tasks):  M1 + M4 + M8 + M9
                               (M8 + M9 同包但文件 disjoint；M8 不引用 M9 常量)
Wave 2 (sequential):          M2 (depends M1)
Wave 3 (sequential):          M3 (depends M2)
                               注: Wave 2/3 准备 M10/M11/M12 用的 store，
                                   不阻塞 Wave 4。Wave 4 可与 Wave 2/3 并行启动。
Wave 4 (parallel, 3 tasks):  M5 + M6 + M7
                               前置: Wave 1 [M4 types] 完成
                               M5 仅需 M4；M6 需 M4 + M3 (store)；M7 需 M4 + M3 + skill
                               实务: 等 M3 完成后再启 Wave 4 简化依赖
Wave 5 (single):              M10
                               依赖: M4 + M6 + M8 (前 3 Wave 完成后启动)
Wave 6 (single):              M11
                               依赖: M9 (已在 Wave 1 完成) + M8 + M10
                               注: M9 已在 Wave 1 提供 LoopEventPermissionDenied + HookAction*Deny
Wave 7 (single):              M12
                               依赖: 所有 M1-M11 完成
Wave 8 (S5 doc):              M13 (S5 阶段写)
```

**总计：12 个实现 task + 1 个 acceptance 文档 task = 13 tasks**

---

## §4 每 task 完成准则（Subagent 提交规范）

每个 task implementer subagent 完成后：
1. **commit**：Conventional Commits `feat(agent-perm): M<N> <description>` + `Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>` trailer
2. 工作树干净：`git status -s` 空输出
3. **本 task 内** `go test -race -count=1 ./<changed packages>/...` PASS
4. **report**：列出 commit hash + 新增/修改文件名 + 覆盖率（如适用）

每 task 完成后，主 session 跑：
- `git -C numind-server log --oneline -1`
- `git -C numind-server status -s`
- 然后 dispatch **2 个并行 reviewer**（spec-compliance + code-quality）

---

## §5 Reviewer 协议（双 reviewer 并行 dispatch）

每 task 完成后：

```
Wave N+1 reviewer dispatch (并行 2 个 Sonnet)：
  Reviewer A: spec-compliance — 验证 task 实现是否符合 S2 spec 的描述
  Reviewer B: code-quality — 验证代码风格 / lint / 测试质量
```

每个 reviewer 输出结构化 issue list（P0/P1/P2 + Strengths）。主 session 修 P0/P1（顺手 P2），然后更新 `progress.reviewed_tasks += 1` 在 manifest。

---

## §6 测试覆盖率目标

- `biz/permission` 包：≥ 80%
- `biz/permission/validators` 包：≥ 80%（每个 validator 单测 ≥ 3 case）
- `biz/agent` 包：不下降（保持 80%+）
- `biz/agent/bashvalidator` 包：不下降（保持 100%）
- `store/agent_permission*`：≥ 85%
- 端到端集成测试：3 个关键路径（permission deny / allow 透传 / 无 wrapper 兼容）

---

## §7 关键风险检查清单（S4 实现时确认）

1. **HookActionRegistry 兼容**：新值 3 落 int32 合法区间，旧 0/1/2 行为 0 改动 — 单测验证
2. **TerminalReason 验证集合 [13]**：state.go 编译期数组更新；如已写 [12] 引用其他地方需 grep 全部更新
3. **biz/permission → biz/agent 单向依赖**：S4 中如有 biz/agent → biz/permission import 立即 review FAIL
4. **wrapper deny 不调 base.PreToolCall**：单测验证（mock base 计数器）
5. **audit goroutine race-safe**：`go test -race` 多次跑（≥ 3 次）确认稳定
6. **HookActionPermissionDeny 经 Registry 传递回 runner**：runner_integration_test 端到端覆盖

---

**S3 完结。S4 编码（13 tasks 跨 7+1 Wave）。**
