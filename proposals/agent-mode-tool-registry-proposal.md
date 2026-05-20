# Agent 模式 Tool Registry — 提案

## §1 方案概述

agent-mode 14-feature #3/14。扩展 #2 最小 Tool interface（3 方法）→ 完整版（蓝本 §4.2.3 ~36 方法）。引入 `ToolFactory` 插件模式 + `AgentToolRegistry` 启动注册/运行时查找 + 6 个 platform 工具实现。

阻塞：#4 sandbox-integration / #6 permission-pipeline / #8 narration-layer / #10 configurator-ux。

## §2 周期

- 预估 10 工作日（W3-W4）
- 内部 R&D
- dev 部署后停 prod

## §3 技术可行性

### 复用

| 模块 | 来源 | 用途 |
|------|------|------|
| #2 `agent.Tool` / `einoToolAdapter` | `numind-server/internal/numind/biz/agent/` | rename to MinimalTool 作 v1 内嵌；einoToolAdapter wrap FullTool |
| `salesrag.SalesRAGBiz.Retrieve(ctx, query, docIDs)` | `numind-server/internal/numind/biz/salesrag/` | kb_search 工具调（**真实方法名 Retrieve，不是 Search**；S1 reviewer 已 grep 核对） |
| `b.ds.Users()` / `model.User` | `numind-server/internal/numind/store/` | learner_data_query 工具直接读 user 表（**不用 customerbiz.GetSubUserDetail** — 那是父→子视角，工具上下文只有当前 userID） |
| `aiservice.Chat`（qwen-long） | `internal/pkg/aiservice/` | document_generate 调 qwen-long |
| `aiservice` 图像生成（wanx2.1） | `internal/pkg/aiservice/` | image_gen 工具实现（aiservice 已有 wanx 调用） |
| GORM datatypes.JSON | 已用于 #2 agent_run | agent_tool.config_json 列 |

### 风险

| ID | 风险 | 缓解 |
|----|------|------|
| R1 | FullTool 36 方法 makes mock impossible | 提供 `BaseTool` 嵌入结构（default values for 30 字段），mock 只需覆盖 5-6 关键字段 |
| R2 | 启动时 PlatformToolFactory.LoadTools 失败（aiservice 未 ready）| RegisterFactory 不立即 LoadTools；用 lazy load + 启动后调 `Registry.LoadAll()` 在 wire 末尾 |
| R3 | agent_tool DB seed 与代码 PlatformToolFactory 行漂移 | INSERT IGNORE + 启动时对比 (code defs vs DB rows) log warning 而不报错 |
| R4 | bash_exec / image_gen stub 误激活 | **`IsEnabled(cfg ToolConfig)` 而非蓝本 §4.2.3 的 AgentConfig**（`AgentConfig` 是 #10 配置者表的 model，#3 范围内还不存在）。引入轻量 `agent.ToolConfig struct{ EnableSandbox bool; EnableImageGen bool }`，由调用方（runner / future configurator-ux）传入；默认全 false。`IsEnabled` 在 stub 工具中返回 cfg.EnableSandbox |
| R5 | 蓝本 §4.2.3 36 方法在 v1 是否过度工程 | 我们 v1 默认实现 18 个核心方法，其余 18 个用 BaseTool 默认值 + 注释"#10/#11 时按需扩展" |

### AI 可观测性

- 涉及 LLM 调用：是（document_generate / image_gen / kb_search 内部 SalesRAG → aiservice）
- 各工具自行 CreateGeneration / CreateSpan
- registry 启动时 log 注册的工具数（便于 production 验证）

## §4 PRD

### 用户故事

- 作为 **#4 sandbox-integration 实施者**，我需要 IsDestructive + IsEnabled(cfg) 元信息
- 作为 **#6 permission-pipeline 实施者**，我需要 IsReadOnly / InterruptBehavior
- 作为 **#8 narration-layer 实施者**，我需要 Prompt / BackfillObservableInput / NarrationVerb / NarrationDetail
- 作为 **#10 configurator-ux 实施者**，我需要 ToolFactory 抽象 + agent_tool 表 schema

### 验收标准

M1 DB：
- [ ] agent_tool + agent_tool_factory 两表 migration 跑通
- [ ] AutoMigrate 通过
- [ ] CHECK constraint 校验 source ∈ {platform, mcp, cli, webhook}（**webhook 是预留值，蓝本 §4.2.2 注明"未来"**；v1 实际只会出现 platform；表注释写明）

M2 Store：
- [ ] IAgentToolStore: Create/Get/ListByEnabled/UpdateConfig
- [ ] IAgentToolFactoryStore: List (read-only #3)

M3 Tool interface：
- [ ] `agent.FullTool` 含蓝本 §4.2.3 ~36 方法
- [ ] `agent.BaseTool` 嵌入结构 + 默认值
- [ ] `MinimalTool` alias 给 #2 单测使用
- [ ] 编译期断言 + adapter 包装

M4 ToolFactory：
- [ ] `ToolFactory` interface (Source/LoadTools/Watch)
- [ ] `PlatformToolFactory` 实现：扫描代码注册的工具
- [ ] Watch 在 v1 是 noop（不监听变化），但保留接口给 #10 用

M5 AgentToolRegistry：
- [ ] RegisterFactory + LoadAll + GetTool(name) + ListEnabled
- [ ] sync.RWMutex 保护
- [ ] race detector 测试

M6 6 platform 工具：
- [ ] kb_search：FullTool 实现，调 `SalesRAGBiz.Retrieve(ctx, query, docIDs)` — docIDs 作为工具可选参数（空 = 全库）
- [ ] learner_data_query：FullTool 实现，直接 `b.ds.Users().GetByID(ctx, userID)`（read-only，从 ReactRun.UserID 取 userID；不走 customerbiz）
- [ ] document_generate：FullTool，调 aiservice.Chat (qwen-long)
- [ ] image_gen：**stub**（与 bash_exec 同等处理），Execute 返回 `errors.New("requires #4 sandbox / future aiservice.ImageGenerate")`；**不新增 aiservice 图像入口**（范围控制，避免 #3 膨胀）
- [ ] bash_exec stub：FullTool，Execute 返回 `errors.New("requires #4 sandbox")`
- [ ] get_current_date：FullTool，沿用 #1 Phase 0 V2 demo impl

M7 AgentRunner 集成：
- [ ] `RunRequest.Tools []tool.BaseTool` → `RunRequest.ToolNames []string`（#2 现有 runner_test 均未传 Tools 字段，rename 不破坏测试 — S1 reviewer 已 grep 核对）
- [ ] runner.go 内部装配：`for _, name := range req.ToolNames { ft, ok := registry.GetTool(name); if !ok { continue /* or err */ }; tools = append(tools, einoToolAdapter{ft}) }`
- [ ] 装配后传给 `react.AgentConfig.ToolsConfig.Tools`
- [ ] 单测覆盖 ToolNames 中含未注册工具的处理（log warn + skip）

M8 测试：
- [ ] 38 字段全覆盖测试（FullTool interface satisfaction）
- [ ] 6 工具各自 unit test
- [ ] Registry race detector 测试
- [ ] runner_integration_test.go 改造支持 ToolNames

### 边界

| 场景 | 处理 |
|------|------|
| SalesRAG/KnowledgeBase 包暂未初始化（biz wire 顺序问题）| Registry.LoadAll 在 NewBiz 末尾调用（在 SalesRAG / KB 初始化后） |
| agent_tool 表已有不同版本工具（旧 deploy 残留）| INSERT IGNORE + log warning，不报错 |
| Eino tool.InvokableTool 与 FullTool 接口的转换 | adapter wrap：FullTool.Execute → InvokableRun |
| #2 现有 runner.go 用 `[]tool.BaseTool` | M7 改 RunRequest，#2 单测继续传 BaseTool，FullTool 提供 BaseTool() 方法 |
| 启动时 LoadAll 失败 | log error + 工具列表为空，AgentRunner.Run 调用时报"no tool registered" |

### 权限规则

- 配置者（B 端父账户）可在 #10 管理端启用/禁用每个 tool（agent_tool.is_enabled）
- 学员（C 端）不能直接控制；只能通过会话感知工具被调用（#8 narration）
- #3 不实现 UI；is_enabled 默认全 true（除 bash_exec / image_gen 默认 false）

### UI 行为规格

> 无 UI；biz 层 only。
