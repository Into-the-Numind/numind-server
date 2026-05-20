# Agent 模式 Tool Registry

## 来源
- 提出人：产品负责人 / 创始人
- 提出日期：2026-05-20
- 上下文：agent-mode 14-feature 分解 #3/14。#1 Phase 0 完成（A4 Eino+Coze 心智模型 PASS，决策"部分借鉴 Coze"）。#2 runtime-skeleton 完成，已暴露最小 Tool interface（3 字段）+ RunHooks 接口。本 feature 扩展 Tool 到完整 38 字段（来自蓝本 §4.2，参考 Claude Code v2.1 源码），并实现 ToolFactory 插件模式 + 6 个 platform 内置工具。

## 需求描述

### 问题

#2 给了最小 `Tool` interface（Name / Description / Run），够 mock 测试，但**生产级 Agent Runtime 需要完整 Tool 元信息**：
- **权限/安全**：IsDestructive / IsReadOnly / InterruptBehavior（决定该工具是否需要二次确认 / 是否能被中断）
- **可观测性**：BackfillObservableInput（在 Langfuse trace 中如何显示工具输入，脱敏 / 截断）
- **Prompt 控制**：Prompt() 独立于 Description（Description 给 LLM 决策时看，Prompt 给学员看自然语言描述）
- **路由**：Source（platform / mcp / cli / webhook）决定工具来自哪个 ToolFactory，便于运营动态接入
- **元数据**：38 字段完整集合（详见蓝本 §4.2 Tool 接口表）

不做 #3 直接进 #4 sandbox = sandbox 不知道哪些工具该跑沙箱（IsDestructive=true？），permission pipeline (#6) 不知道哪些工具该二次确认，narration (#8) 不知道工具输入如何显示给学员。

### 范围（Tool Registry 完整版）

| # | 模块 | 产出物 |
|---|------|--------|
| M1 | **DB 表**：`agent_tool` + `agent_tool_factory`（tool registry 持久化）| migration 双文件 + GORM model |
| M2 | **Store**：`IAgentToolStore` + `IAgentToolFactoryStore` | store impl + 单测 |
| M3 | **完整 Tool interface（38 字段）** | 替换 #2 最小 interface，含 IsDestructive / InterruptBehavior / Prompt / BackfillObservableInput 等（蓝本 §4.2）；向后兼容（#2 旧 Tool 用 adapter wrap） |
| M4 | **ToolFactory 插件模式** | `ToolFactory` interface（Source / LoadTools / Watch）；实现 `PlatformToolFactory`（内置工具，从代码加载） |
| M5 | **Tool Registry biz**：`AgentToolRegistry` | 启动时调 LoadTools 注册 + 运行时按 toolName 查找；线程安全 |
| M6 | **6 个 platform 内置工具实现** | web_search / kb_search / file_read / bash_exec（stub，#4 sandbox 接） / image_gen（stub）/ get_current_date（沿用 #1 V2 demo）|
| M7 | **AgentRunner 集成**：Run 接入 ToolRegistry | runner.go 用 registry.GetTool(name) 替换 直接传 tools；hook 点接入 sandbox（hook injection 在 #4） |
| M8 | **Unit + Integration tests** | 38 字段完整测试 + 6 工具单测 + registry 注册/查找测试 |

### 不在范围（Out of Scope）

- **MCP / CLI / Webhook ToolFactory 实现**：本 feature 只做 platform；蓝本 §4.2.10 CLI 在 dev 服务器装 ffmpeg/pandoc 等 → 留到后续 feature
- **沙箱集成**：#4 处理（本 feature 留 hook 点）
- **权限 pipeline 集成**：#6 处理
- **Memory 系统集成**：#7 处理
- **Tool 配置 UI**（管理端用 ToolFactory 接入新 tool）：#10 configurator-ux 处理
- **API 端点**：保持 biz 层 only

### 技术约束

- **#2 现有最小 Tool interface 保留**：作为内部 alias 或 adapter，避免破坏 #2 单测
- **ToolFactory.LoadTools 启动时调用**：在 `biz.NewBiz()` 时通过 `agentToolRegistry := agent.NewRegistry(); agentToolRegistry.RegisterFactory(NewPlatformToolFactory())`
- **agent_tool 表 vs 代码硬编码 tool**：完整版用 DB 持久化（运营可在 #10 管理端 CRUD），但 platform tool 仍由代码定义；DB 记录工具的运行时配置（是否启用 / 限流等）
- **bash_exec / image_gen 在 #3 是 stub**：返回 "需要 #4 沙箱才能执行" 错误，避免在没沙箱前 prod 出事

## 业务目标

1. **#4 sandbox-integration 启动条件**：需要 IsDestructive / SandboxRequired 等 Tool 元信息
2. **#6 permission-pipeline 启动条件**：需要 IsReadOnly / InterruptBehavior
3. **#8 narration-layer 启动条件**：需要 Prompt / BackfillObservableInput
4. **#10 configurator-ux 启动条件**：需要 ToolFactory 抽象（管理端 CRUD tool 元信息）

## 优先级

**高** — 阻塞 #4 / #6 / #8 / #10 全部启动。

## Triage

- 推荐轨道：**Standard**
- 分类理由：
  1. DB schema 变更：**是**（agent_tool + agent_tool_factory 两表）
  2. 新增 API 端点：**否**（biz 层）
  3. 新外部服务集成：**否**
  4. 影响文件数：**>3**（migration + 2 model + 2 store + 8 biz + 6 tool impl + N test）
  5. 高风险业务逻辑：**否**

   **1+4 触发 Standard**。

- 人类决定：**确认 Standard**（autopilot 协议）

## S5 验证策略

**纯后端：Go unit + race + integration tests**（与 #2 相同）

- M1/M2 DB + Store：unit test + in-memory SQLite
- M3 38 字段 Tool interface：编译期断言 + 单测
- M4 ToolFactory：mock factory + 启动注册测试
- M5 Registry：并发查找 race detector
- M6 6 工具：每个工具独立单测（stub 工具验证返回 stub error）
- M7 runner 集成：modified runner_integration_test.go 跑 5 步 ReAct 用 registry

**回归保护**：所有测试进 CI 主套件。

## 备注

- **架构蓝本同步**：本 feature 实施可能发现蓝本 §4.2 中某个 Tool 字段命名/语义需要调整，记 manifest decisions
- **跨 feature 接口稳定性**：本 feature 定义的 Tool interface 是 #4/#6/#8/#10 共享契约，**不可轻易破坏**
- **autopilot**：dev 部署后停，等用户决定 prod
