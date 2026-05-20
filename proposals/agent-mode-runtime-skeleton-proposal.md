# Agent 模式 Runtime Skeleton — 提案

## §1 方案概述 [内部]

> 本 feature 无终端用户可见变更，"客户可见"语义 N/A。

agent-mode 14-feature 分解 #2/14。把 Phase 0 V2 demo 中的 adapter 模式工程化为 Agent Runtime 主路径代码。Phase 0 验证完成（A1=NO 选 Docker pool / A2=YES Eino 可用 / A3=YES Bash validators / A4=YES Eino 心智模型），现在把 V2 demo 中临时跑通的 adapter 升级为 production-grade Runtime 骨架。

**核心交付**：

| ID | 模块 | 一句话 |
|----|------|--------|
| M1 | DB schema | **仅 agent_run 单表**（消息存 JSON 列在 agent_run.messages 内，turn 级整体覆写；蓝本 §8 行 4154 明确"不另建 message 表"为准——S0 提及的 agent_message 表为 reviewer P2 修订后撤销）。`id BIGINT AUTO_INCREMENT` PK（与现有表约定一致），含 `reservation_id BIGINT NULL`（FK 到 credit_reservation；#2 不集成 credits，**字段创建但置 NULL**，#12 billing 时填充）。migration 双文件（forward + rollback）+ GORM model |
| M2 | Store | IAgentRunStore + WriteTurn / Create / Get / UpdateState / ListBySession 方法 |
| M3 | AgentRunner biz | 包装 Eino ReAct，**`AiserviceAdapter` 实现 `model.ToolCallingChatModel` 接口三方法**（`Generate` / `Stream` / `WithTools(tools) (ToolCallingChatModel, error)`，**WithTools 返回克隆体**，线程安全）；`react.AgentConfig.ToolCallingModel` 填 adapter（不是 `Model` 字段——该字段已 deprecated）；暴露 **`RunHooks` 接口含 `HookAction` enum 返回值**（`Continue` / `Stop` / `BlockingStop`），分别映射 continue / `hook_stopped` / `stop_hook_prevented` terminal reasons（#4 sandbox 注入点） |
| M4 | 状态机 | 12 Terminal + 7 Continue reasons 枚举（DB CHECK constraint 校验）+ state transitions |
| M5 | AbortController | 三层 ctx 派生：queryCtx → batchCtx → toolCtx，cancel 级联 |
| M6 | Withhold recovery | 两条独立 chain：PromptTooLong（PTL）+ max_output_tokens recovery |
| M7 | Tool interface（最小版） | 字段 Name/Description/Run 三字段（完整 38 字段在 #3） |
| M8 | Unit + Integration tests | 19 个 reason 独立 test + mock Eino + 5 步 ReAct 集成 + race detection |

## §2 报价与周期 [内部]

- 预估工作量：**10 工作日**（W2-W3，两周）
- 报价：N/A（内部 R&D）
- 交付时间线：2026-06-03（W3 末）

时间分配：
- W2 day 1-3：M1 + M2（DB + Store，可与 M4 状态机并行）
- W2 day 4-5：M3 AgentRunner biz（核心）+ M7 Tool interface
- W3 day 1-2：M5 AbortController + M6 Withhold（并行）
- W3 day 3-4：M8 测试 + M4 状态机最终化
- W3 day 5：集成测试 + 部署到 dev container 验证

## §3 技术可行性 [AI 内部]

### 现有功能复用

| 模块 | 来源 | 复用方式 |
|------|------|---------|
| Eino v0.8.13 | 已在 numind-server/go.mod（Phase 0 pin） | 直接 `import "github.com/cloudwego/eino/..."` |
| `aiservice.Chat(ctx, taskID, req)` 3-arg | `internal/pkg/aiservice/ai.go` | AgentRunner 内部 adapter 沿用 Phase 0 V2 pattern |
| `langfuse.CreateTrace/Generation/Span` | `internal/pkg/langfuse/helpers.go` | Trace 注入 / 工具 Span / LLM Generation 全套 |
| Phase 0 V2 demo 代码 | `cmd/agent-phase0-eino-demo/` | 作为"工程化模板"参考，不直接 copy（demo 是 deprecated ChatModel，#2 升级到 ToolCallingChatModel） |
| 现有 user / customer 表 | `internal/pkg/model/` | agent_run.user_id FK 到 user.id |

### 技术风险

| # | 风险 | 概率 | 影响 | 缓解 |
|---|------|-----|------|------|
| R1 | Eino `ToolCallingChatModel` 接口含 `WithTools(tools) (ToolCallingChatModel, error)` 方法（已经核对 v0.8.13 真实 API），与 Phase 0 V2 用的 `ChatModel.BindTools()` 不同 | 中 | M3 adapter 实现路径需精确 | M3 实现 `model.ToolCallingChatModel` 三方法（`Generate` / `Stream` / `WithTools`）；`WithTools` 返回**克隆体**（线程安全，不变更原实例）；`react.AgentConfig.ToolCallingModel` 字段（非 deprecated `Model` 字段）；adapter 内部 toolInfos 作为不可变字段 |
| R2 | agent_run.messages JSON 列在 turn 级覆盖时遇到 GORM 序列化坑（如 nil slice vs empty slice） | 中 | M1/M2 数据丢失或 unmarshal 失败 | M2 store 用 `datatypes.JSON` 类型 + custom Marshaler；测试覆盖 nil/empty/full 三种 case |
| R3 | AbortController 三层 ctx 在 goroutine 间传播失败（context.Background derived chain 错误） | 中 | 用户取消请求时下游不响应 | M5 用 derived context（`context.WithCancel`）严格父子链；race detector 测试用例覆盖 cancel propagation |
| R4 | Withhold recovery 两条 chain 互相干扰（如 PTL 触发时 max_output 也累计） | 中 | recovery 死循环或 silent fail | M6 两条 chain 用独立 state field + transitions table 显式标注互斥 |
| R5 | 12 Terminal reason 中部分（如 hook_stopped / stop_hook_prevented）依赖 Hook System，但 Hook System 在 #2 不实现 | 高 | 这两个 reason 在 #2 测试用 mock，转 follow-up | M4 写到 enum 里但实现 noop；follow-up feature（可能 #5 skill 或独立 hooks feature）补真实 |
| R6 | 19 个 reason 的 DB CHECK constraint 在 migration 写复杂、跨数据库版本（MySQL 5.7 vs 8.0）兼容性问题 | 低 | migration 失败 | M1 用 ENUM 类型（MySQL 8.0+ 支持）或 VARCHAR + CHECK；fallback 应用层 validation |
| R7 | 主 go.mod 已含 Eino v0.8.13 但其他 transitive deps 与现有 server deps 冲突（如 some old golang.org/x/net） | 低 | go mod tidy 时出问题 | Phase 0 已验证 build clean，沿用即可；如有冲突走 replace directive |

### 涉及仓库

- [x] numind-server（M1-M8 全部）
- [ ] numind-web-v3（#2 不出 UI）
- [ ] numind-admin-web

### AI 可观测性（Langfuse）

- [x] 涉及 LLM 调用：**是**（AgentRunner 包装 Eino，最终调 aiservice.Chat）
- Trace 起点：`internal/numind/biz/agent/runner.go::AgentRunner.Run()` 创建 trace，写 `user_id` / `agent_run_id` / `task` 元数据
- Generation 点：
  - `gen-react-step-N`：每个 ReAct loop 的 LLM 调用（aiservice 内部已记录）
  - error path：失败时含 `{"error": err.Error()}` 输出
- Span 点：
  - `span-runner-loop`：包裹整个 ReAct 循环（M3 顶层 span）
  - `span-tool-exec-<name>`：每次 Tool.Run 包装（M3 通过 hook 注入）
  - `span-state-transition-<from>-<to>`：每次状态迁移（M4 用，便于调试）
- 关键元数据：
  - trace tag：`agent-runtime-skeleton`
  - `user_id`（真实用户）
  - `agent_run_id`（DB 行 PK）
  - `terminal_reason` / `continue_reason` 写到 trace metadata

## §4 产品需求定义 — PRD [AI 内部]

### 用户故事

> Phase 1 起点，"用户" = 后续 11 feature 的实施者 + Agent Runtime 内部维护者。

- 作为 **#3-#14 feature 的实施者**，我需要 **一个稳定的 AgentRunner 接口**（含 RunHooks），以便 **挂载自己的工具/沙箱/权限/记忆/合规/计费组件而不破坏 Runtime 内部**
- 作为 **Runtime 维护者**，我需要 **19-reason 状态机有形式化定义和 DB CHECK 校验**，以便 **未来 6 个月内不被人随手加新 reason 破坏跨 feature 契约**
- 作为 **测试工程师**，我需要 **mock Eino 单测 + race detection 集成测试**，以便 **CI 主套件能在 30 秒内回归 19 个 reason + AbortController + Withhold**
- 作为 **运维**，我需要 **Langfuse 后台能看到每个 ReAct 循环的完整 trace 树**，以便 **生产问题定位**

### 验收标准

M1（DB schema）：
- [ ] migration 双文件存在（forward + rollback），SQL 在本地 + dev 数据库都跑通
- [ ] GORM models 含必要字段 + index（user_id / status / created_at 三索引；状态字段 ENUM 或 VARCHAR + CHECK constraint）
- [ ] AutoMigrate 通过（dev 数据库 schema 一致）

M2（Store）：
- [ ] IAgentRunStore interface 定义完整（Create / Get / UpdateState / WriteTurn / ListBySession）
- [ ] 实现单测覆盖 ≥ 90%
- [ ] WriteTurn 严格 turn 级覆写（覆盖之前 messages JSON 全量），SQL 用 UPDATE
- [ ] race detector 测试两个 goroutine 并发 WriteTurn 不数据丢失（GORM transaction）

M3（AgentRunner）：
- [ ] `Run(ctx, RunRequest) (*RunResult, error)` 接口签名稳定
- [ ] `RunHooks` struct 暴露 `PreToolCall(ctx, tool, input) (HookAction, error)` / `PostToolCall(ctx, tool, output, err) (HookAction, error)` 两个 func 字段
- [ ] `HookAction` enum 定义：`HookActionContinue` / `HookActionStop` / `HookActionBlockingStop`；映射到 continue / `hook_stopped` / `stop_hook_prevented` reason
- [ ] AiserviceAdapter 实现 `model.ToolCallingChatModel` 接口 3 方法（Generate / Stream / WithTools）；WithTools 返回克隆体
- [ ] `react.AgentConfig.ToolCallingModel` 字段填 adapter（**不是 deprecated `Model` 字段**——升级 Phase 0 V2）
- [ ] adapter 透传 `aiservice.Chat(ctx, taskID="agent-runner-<runID>", req)`
- [ ] Langfuse trace 写入完整（trace + ≥1 generation + ≥1 span）
- [ ] 5 步 mock ReAct loop 正常终止（terminal reason `completed`）

M4（状态机）：
- [ ] **12 Terminal reasons**（来自蓝本 §4.1.5）作 typed string constants：
  - `completed` / `blocking_limit` / `image_error` / `model_error` / `aborted_streaming` / `prompt_too_long` / `stop_hook_prevented` / `aborted_tools` / `hook_stopped` / `max_turns` / `error_max_budget` / `error_max_retries`
- [ ] **7 Continue reasons**（来自蓝本 §4.1.9）作 typed string constants：
  - `next_turn` / `collapse_drain_retry` / `reactive_compact_retry` / `max_output_escalate` / `max_output_recovery` / `stop_hook_blocking` / `token_budget_continue`
- [ ] state transitions function：给定 current state + event → next state（含合法性 check）
- [ ] DB CHECK constraint 校验 reason 字符串值（VARCHAR(50) + CHECK，兼容 MySQL 5.7+）
- [ ] 19 个 unit test，每个 reason 独立触发路径覆盖
- [ ] **hook_stopped / stop_hook_prevented**：单测用 mock RunHooks 返回 `HookActionStop` / `HookActionBlockingStop` 触发；**Hook system 真实落地**由 #5 skill-system（hooks 是 skill 的扩展点之一）或独立 follow-up feature 处理 — 不在 #2 范围

M5（AbortController）：
- [ ] queryCtx / batchCtx / toolCtx 三层派生关系正确（每层 `context.WithCancel(parent)`）
- [ ] 父 ctx cancel → 所有子 ctx 立即 Done
- [ ] race detector 测试覆盖：goroutine A 调用，goroutine B cancel，A 立即收到 ctx.Err()

M6（Withhold recovery）：
- [ ] PTL chain：context window 超限 → 触发 reactive compact → 重新 attempt（最多 N 次，超过 → terminal `prompt_too_long`）
- [ ] max_output_tokens chain：output 超限 → 升级 model context window → 重新 attempt（terminal `error_max_budget` 兜底）
- [ ] 两条 chain state field 独立（结构体里两个字段），不互相影响
- [ ] mock 触发条件单测覆盖

M7（Tool interface）：
- [ ] `Tool` interface 含 `Name() string` / `Description() string` / `Run(ctx, input json.RawMessage) (json.RawMessage, error)` 三方法
- [ ] 一个最小 mock 工具实现（用于 M8 集成测试）
- [ ] interface 实现 `model.ToolCallingChatModel.WithTools()` 适配

M8（Tests）：
- [ ] `task test`（含 `-race -cover`）PASS
- [ ] Unit test 覆盖率 ≥ 85% (biz/agent 包)
- [ ] 集成测试：mock Eino + mock tool，跑 5 步 ReAct loop，断言 `terminal_reason == completed` + Langfuse trace 写入
- [ ] 19 个 reason 各有独立 test case（部分用 mock condition 触发）
- [ ] race detector 触发 0 次

整体：
- [ ] **未部署到 prod**（止步 dev container 部署后停下）
- [ ] dev container 部署：rsync 代码到构建机 + build + push TCR + dev 部署机 docker pull + 健康检查 PASS
- [ ] **健康检查语义**：`GET /healthz` 返回 200（已有端点）+ `docker logs numind-server` 无 panic / error，server 启动正常即可。AgentRunner 在 #2 是 idle 代码（无 API 端点触发），代码正确性由 M8 测试覆盖；部署只验证 build 没破坏现有服务
- [ ] manifest.progress.completed_tasks == 8 && reviewed_tasks == 8 && stage == S6

### 边界情况

| 场景 | 处理 |
|------|------|
| Eino ToolCallingChatModel 不存在或签名变化 | M3 第一步发现 → 降级到 deprecated ChatModel + 标记 follow-up（与 Phase 0 一致） |
| migration 在 dev 数据库 idempotent（已有同名表） | 用 `CREATE TABLE IF NOT EXISTS` + 显式列检查 |
| GORM AutoMigrate 与手工 migration SQL 顺序冲突 | 跑 migration 在 server 启动**之前**（docker entrypoint 前置），AutoMigrate 仅做 sanity check |
| 19 reason DB CHECK 在 MySQL 5.7 不支持 ENUM | 用 VARCHAR(50) + Go 应用层 validation（兼容性优先） |
| AbortController 中间层 cancel 但子 goroutine 还在跑 | 子 goroutine 必须 `select { case <-ctx.Done() }` 显式监听；M5 设计 review 强制 |
| Withhold 两条 chain 都触发 | 优先 PTL chain（先 compact 才能 retry max_output）；M6 设计明确优先级 |
| 主 server build 因 Eino transitive deps 失败 | follow-up debugging，可能 replace directive |

### 权限规则

> #2 仅 biz 层，无 API 端点 → 权限规则未启用

- AgentRunner 调用方（即将来的 controller）需自己做 auth；#2 不做
- DB 表 `agent_run.user_id` FK 到 user.id，删除 user 时**不级联删 agent_run**（保留审计）

### UI 行为规格

> #2 不出 UI，N/A

唯一"输出"是 Langfuse trace 树和 dev 服务器日志。
