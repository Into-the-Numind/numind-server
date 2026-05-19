# Agent 模式 Runtime Skeleton

## 来源
- 提出人：产品负责人 / 创始人
- 提出日期：2026-05-20
- 上下文：agent-mode 14-feature 分解 #2/14。Phase 0 完成（feature #1 `agent-mode-phase0-verification`），验证了 A2=YES（Eino+aiservice adapter 可行）+ A1=NO 触发 V5 选 Docker pool。本 feature 为 Phase 1 起点：把 Phase 0 V2 demo 中的 adapter 模式工程化为 Agent Runtime 主路径代码。

## 需求描述

### 问题

architecture-v1.md §4.1 设计了完整的 Agent Runtime（Query Loop + 12 Terminal reasons + 7 Continue reasons + AbortController 三层 + Withhold recovery），但**主 server 代码里完全没有 Runtime 骨架**。后续 11 个 feature（#3 tool-registry / #4 sandbox / #5 skill / #6 permission / #7 memory / #8 narration / #9 compact / #10 configurator UX / #11 student UX / #12 billing / #13 compliance / #14 e2e）全部依赖一个可工作的 Runtime 骨架作为载体。

不做 #2 直接跳到 #3 = 后续 feature 的 implementer 不知道把 Tool Registry / Memory / Permission 这些组件挂到哪个 Runtime 接口上，每个都要重新设计入口契约，会反复返工。

### 范围（Runtime Skeleton — 不含具体工具/沙箱/记忆/权限实现）

| # | 模块 | 产出物 |
|---|------|--------|
| M1 | **DB 表**：`agent_run` + `agent_message`（runtime 状态持久化） | `migrations/YYYYMMDD_*_agent_runtime_schema.sql` 双文件（forward + rollback）；`internal/pkg/model/agent_run.go` + `agent_message.go` GORM model |
| M2 | **Store 层**：`IAgentRunStore` + 实现 | `internal/numind/store/agent_run.go` + `_test.go`；含 Create / Get / UpdateState / AppendMessage / ListBySession 方法 |
| M3 | **Runtime Core**：`AgentRunner` biz（包装 Eino ReAct） | `internal/numind/biz/agent/runner.go`；接受 `RunRequest`，调度 Eino，写 agent_run；与 Phase 0 V2 adapter 模式一致 |
| M4 | **状态机**：12 Terminal reasons + 7 Continue reasons | `internal/numind/biz/agent/state.go`（枚举常量 + state transitions 函数） |
| M5 | **AbortController 三层** | `internal/numind/biz/agent/abort.go`（queryCtx / batchCtx / toolCtx 派生 + cancel 传播） |
| M6 | **Withhold recovery**（PTL chain + max_output_tokens chain） | `internal/numind/biz/agent/withhold.go`（两条独立 recovery chain） |
| M7 | **Mock Tool 接口**（**仅 interface，无具体工具**） | `internal/numind/biz/agent/tool.go`（最小 Tool interface — 字段是 Name/Description/Run；38 字段完整版在 #3） |
| M8 | **Unit tests**（mock Eino + mock tool） | `runner_test.go` / `state_test.go` / `abort_test.go` / `withhold_test.go`；mock 工具 5 步 ReAct 循环正常终止；各 Terminal/Continue case 全覆盖 |

### 不在范围（Out of Scope）

- **Tool Registry 38 字段完整版 + ToolFactory 插件**：feature #3 `agent-mode-tool-registry` 处理。#2 仅定义最小可用 Tool interface（够 mock test 用）
- **6 个 platform 工具**：#3 处理
- **沙箱集成**：feature #4 处理（Docker pool wrapper，来自 V5 ADR）
- **权限 pipeline**：#6 处理
- **Memory 系统**：#7 处理
- **Narration 层 / Compact / 前后端 UX / 计费 / 合规**：后续 feature
- **API 端点**：#2 不暴露 HTTP（Runtime 是 biz 层组件，由后续 feature 的 controller 调用）
- **prod 部署**：止步 develop merge + dev container 部署

### 技术约束

- **Eino 版本沿用 Phase 0 pin 的 v0.8.13**（已在主 go.mod）
- **Adapter 模式沿用 Phase 0 V2 demo 设计**（`AiserviceAdapter` 包装 `aiservice.Chat(ctx, taskID, req)` 3 参数）
- **升级到 ToolCallingChatModel**：Phase 0 V2 用了 deprecated `ChatModel`；本 feature 升级到 `ToolCallingChatModel.WithTools()` 以支持并发场景（V2 reviewer DONE_WITH_CONCERNS 已建议）
- **Langfuse trace 三件套保留**：CreateTrace / Generation / Span 按 ai-service.md §1 + §3 规范
- **biz 层规则**（CLAUDE.md §3）：业务逻辑全部在 `internal/numind/biz/agent/`，controller 不做业务
- **GORM `default:true` bool**：参考 `database.md §6` 避坑

## 业务目标

1. **建立后续 11 feature 的物理载体**：每个 feature 知道自己挂到 Runtime 的哪个 hook（pre-call / tool-call / post-call / state-change / abort / withhold）
2. **state machine 形式化**：Terminal/Continue 共 19 种 reason 在代码里有枚举常量，避免每个 feature 自己拍脑袋
3. **AbortController 三层接入点固化**：用户取消 → queryCtx 取消 → batchCtx / toolCtx 级联 cancel，所有后续 feature 复用同一套
4. **Withhold recovery 在 Runtime 层就处理**：PTL & max_output_tokens 两条 chain 独立运作，#3/#4 等 feature 不必各自实现

## 优先级

**高** — 阻塞剩余 11 个 agent-mode-* feature（除 #1 Phase 0 已闭环）。

## Triage

- 推荐轨道：**Standard**
- 分类理由：
  1. 数据库 schema 变更：**是**（agent_run + agent_message 两张新表）
  2. 新增 API 端点：**否**（#2 只做 biz 层，无 controller）
  3. 新外部服务集成：**否**（Eino 已在 go.mod，aiservice 已存在）
  4. 影响文件数：**>3**（migration + 2 model + 1 store + 5 biz + N test = 10+ 文件）
  5. 高风险业务逻辑：**否**（不动支付/权限）

   **5 条中 1 条命中（DB schema） → Standard**。同时 #2 需要严肃方案设计（19 种 reason 枚举 / AbortController 派生关系 / Withhold chain 编排）+ 跨 feature 接口契约稳定性极高，Hotfix 三阶段无法承载。

- 人类决定：**确认 Standard**（按 [[feedback_agent_mode_autopilot]] 自主推进协议，沿用 #1 Triage 决策模式）

## S5 验证策略（NDF Rule 10）

**验证方式：Go 单元测试 + race detection + 集成测试（mock Eino + mock 工具）**

- M1-M2 DB schema + store：unit test 调用 store 方法，校验 DB 行
- M3 AgentRunner：mock Eino adapter + mock tool，跑 5 步 ReAct loop，断言 Terminal 状态正确
- M4 状态机：每个 Terminal/Continue reason 独立 test case
- M5 AbortController：派生 ctx + cancel 传播测试
- M6 Withhold：构造两条 chain 的触发条件，断言 recovery 行为
- M7 Mock Tool interface：用一个真实最小工具（如 echo）验证 interface 完整

**理由**：Phase 0 已完成 Eino+aiservice 可行性验证；feature #2 是工程化主路径代码，单元测试覆盖率 > 集成。没有用户路径需要 Playwright（#2 不暴露 UI），没有需要 gstack `/qa`（无浏览器界面）。

**回归保护诚实声明**：M1-M8 单元测试**进 CI 主套件**（`task test`），永久回归保护。这与 Phase 0（demo 单测仅作参考）不同——本 feature 产出的是 production 代码骨架，必须有持续 CI 守护。

## 备注

- **架构蓝本同步**：本 feature 实施可能发现蓝本 §4.1 中 Runtime 设计的小漏洞（如某个 Terminal reason 边界 case），允许通过 follow-up note 记录到 manifest 的 decisions，但**不修改蓝本**（蓝本统一在 #14 e2e-rollout 之前更新一次）
- **接口稳定性**：本 feature 定义的 `AgentRunner`、`Tool` interface（最小版）、AbortController API 必须**成为后续 feature 不能轻易破坏的契约**。任何 #3-#14 feature 修改这些接口需在该 feature 的 ADR 中明示理由
- **autopilot 协议**：本 feature 完成至 dev 部署后停止，等待用户决定何时 prod；不阻塞继续启动 #3
