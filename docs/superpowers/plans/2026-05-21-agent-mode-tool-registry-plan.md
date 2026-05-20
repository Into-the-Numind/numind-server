# Agent 模式 Tool Registry — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task.

**Goal:** 实施 8 模块（M1-M8）：tool_definition + tool_factory_registry DB + Store + 36-method FullTool interface + ToolFactory plugin + AgentToolRegistry + 6 platform tools + AgentRunner 集成 + 测试。

**Architecture:** numind-server 单仓库；biz/agent 包扩展（沿用 #2）。无前端改动。

**Tech Stack:** Go 1.24 + Gin + GORM v2 + MySQL 8.0 + cloudwego/eino v0.8.13 + gorm.io/datatypes（已含）

**Spec 引用**：[2026-05-21-agent-mode-tool-registry-design.md](../specs/2026-05-21-agent-mode-tool-registry-design.md)（S2 gate 通过，1 P0 + 2 P1 + 4 P2 已修）

---

## 文件清单

### 新建

| 路径 | 职责 |
|---|---|
| `migrations/20260521_120000_create_tool_definition_and_factory_registry.sql` | Forward migration |
| `migrations/20260521_120000_create_tool_definition_and_factory_registry_rollback.sql` | Rollback |
| `internal/pkg/model/tool_definition.go` | ToolDefinition GORM model |
| `internal/pkg/model/tool_factory_registry.go` | ToolFactoryRegistryRow GORM model |
| `internal/numind/store/tool_definition.go` | IToolDefinitionStore + impl |
| `internal/numind/store/tool_definition_test.go` | store unit tests |
| `internal/numind/store/tool_factory_registry.go` | IToolFactoryRegistryStore + impl |
| `internal/numind/store/tool_factory_registry_test.go` | store unit tests |
| `internal/numind/biz/agent/tool_full.go` | FullTool interface 36 方法 + ToolConfig / ToolInput / ToolResult / PermissionMatcher / MCPToolInfo / CLIToolInfo / ContentBlock / NarrationMessage 占位 |
| `internal/numind/biz/agent/tool_full_test.go` | FullTool 编译期断言 + BaseTool 默认值测试 |
| `internal/numind/biz/agent/base_tool.go` | BaseTool struct + 28 个默认方法实现 |
| `internal/numind/biz/agent/minimal_to_full.go` | MinimalToFullAdapter (#2 兼容) |
| `internal/numind/biz/agent/factory.go` | ToolFactory interface + ToolMetadata + ToolDiff |
| `internal/numind/biz/agent/factory_test.go` | ToolFactory interface 编译期断言 + mock factory test |
| `internal/numind/biz/agent/factory_platform.go` | PlatformToolFactory impl（**Task 7 写入**，不在 Phase 1 Tier 3 范围） |
| `internal/numind/biz/agent/factory_platform_test.go` | Factory LoadTools 测试（**Task 7 写入**） |
| `internal/numind/biz/agent/registry.go` | AgentToolRegistry interface + agentToolRegistry impl |
| `internal/numind/biz/agent/registry_test.go` | Registry race detector + LoadAll integration |
| `internal/numind/biz/agent/tool_kb_search.go` | kb_search FullTool |
| `internal/numind/biz/agent/tool_kb_search_test.go` | mock SalesRAGBiz, Execute test |
| `internal/numind/biz/agent/tool_learner_data_query.go` | learner_data_query FullTool |
| `internal/numind/biz/agent/tool_learner_data_query_test.go` | mock IStore.Users() |
| `internal/numind/biz/agent/tool_document_generate.go` | document_generate FullTool (qwen-long) |
| `internal/numind/biz/agent/tool_document_generate_test.go` | mock aiservice.Chat |
| `internal/numind/biz/agent/tool_image_gen.go` | image_gen stub |
| `internal/numind/biz/agent/tool_image_gen_test.go` | stub error + IsEnabled test |
| `internal/numind/biz/agent/tool_bash_exec.go` | bash_exec stub |
| `internal/numind/biz/agent/tool_bash_exec_test.go` | stub error + IsEnabled + IsDestructive test |
| `internal/numind/biz/agent/tool_get_current_date.go` | get_current_date FullTool |
| `internal/numind/biz/agent/tool_get_current_date_test.go` | ISO 8601 format |
| `internal/numind/biz/agent/adapter_full_to_eino.go` | adaptFullToEinoTool helper |
| `internal/numind/biz/agent/adapter_full_to_eino_test.go` | adapter Info / InvokableRun |

### 修改

| 路径 | 改动 |
|---|---|
| `internal/numind/helper.go` | AutoMigrate 列表加 `&model.ToolDefinition{}` + `&model.ToolFactoryRegistryRow{}` |
| `internal/numind/store/store.go` | IStore interface 加 `ToolDefinitions()` + `ToolFactoryRegistries()` + 工厂注册 |
| `internal/numind/biz/biz.go` | IBiz 加 `AgentTools()`；NewBiz 末尾构造 registry + RegisterFactory + LoadAll；**修改 line 103 现有 NewAgentRunner 单参数 → 2 参数** |
| `internal/numind/biz/agent/runner.go` | `RunRequest.Tools` → `RunRequest.ToolNames`；`NewAgentRunner` 加 registry 参数；Run() 内部装配 + ctx userID 注入 + adaptFullToEinoTool |
| `internal/numind/biz/agent/runner_test.go` | mock IAgentRunStore + mock AgentToolRegistry；ToolNames 测试 |
| `internal/numind/biz/agent/tool.go` | 重命名 Tool interface → MinimalTool（保留 #2 兼容） |
| `internal/numind/controller/v1/salesrag/sales_rag_test.go` | realBizOnlyCustomers mock 加 `AgentTools()` 方法（IBiz interface 扩展） |

> **零变更**：controller / router / API / 前端 / config / 其他业务包。

---

## TOC（按 Phase 拆分）

### Phase 1：基础设施（Tier 3 并行，4 路）
- **Task 1**: M1 DB schema + GORM model + AutoMigrate
- **Task 2**: M3 FullTool interface + BaseTool + 占位类型 + MinimalToFullAdapter
- **Task 3**: M4 ToolFactory interface + PlatformToolFactory 框架（不含 6 tools 实现）
- **Task 4**: M6 6 工具实现（Tier 3 内并行子组：kb_search+learner_data_query / document_generate+get_current_date / image_gen+bash_exec stubs）

### Phase 2：Store + Registry（依赖 Phase 1 部分）
- **Task 5**: M2 Stores (toolDefinition + toolFactoryRegistry) + 单测（依赖 Task 1 model）
- **Task 6**: M5 AgentToolRegistry impl + 单测（依赖 Task 2/3/4 全部）

### Phase 3：Runner 集成（依赖 Phase 1+2 全部）
- **Task 7**: M7 runner.go 改造（Tools → ToolNames）+ biz.go 接入 + salesrag_test mock 修复

### Phase 4：集成测试
- **Task 8**: M8 集成测试 + race detector + 修 runner_test 已知 break

---

## 并行 Tier 评估

### Phase 1 Tier 3 disjoint（4 路并行）

| Agent | 文件归属（每组逗号分隔） |
|-------|---------|
| Agent A (Task 1) | `migrations/20260521_120000_*.sql,migrations/20260521_120000_*_rollback.sql,internal/pkg/model/tool_definition.go,internal/pkg/model/tool_factory_registry.go,internal/numind/helper.go` |
| Agent B (Task 2) | `internal/numind/biz/agent/tool_full.go,internal/numind/biz/agent/tool_full_test.go,internal/numind/biz/agent/base_tool.go,internal/numind/biz/agent/minimal_to_full.go,internal/numind/biz/agent/tool.go` (rename) |
| Agent C (Task 3) | `internal/numind/biz/agent/factory.go,internal/numind/biz/agent/factory_test.go` （**仅 interface + mock test**；`factory_platform.go/_test.go` 由 Task 7 独占写入） |
| Agent D (Task 4) | `internal/numind/biz/agent/tool_kb_search.go,*_test.go,internal/numind/biz/agent/tool_learner_data_query.go,*_test.go,internal/numind/biz/agent/tool_document_generate.go,*_test.go,internal/numind/biz/agent/tool_image_gen.go,*_test.go,internal/numind/biz/agent/tool_bash_exec.go,*_test.go,internal/numind/biz/agent/tool_get_current_date.go,*_test.go` |

**ndf-check-disjoint 命令**（逗号分隔，每组用引号包裹）：

```bash
bash /Users/zhiyuchen/Documents/10_跃迁有数/有数AI工作台/莫小派/Codes/numind-server/scripts/ndf/ndf-check-disjoint.sh \
  "migrations/20260521_120000_create_tool_definition_and_factory_registry.sql,migrations/20260521_120000_create_tool_definition_and_factory_registry_rollback.sql,internal/pkg/model/tool_definition.go,internal/pkg/model/tool_factory_registry.go,internal/numind/helper.go" \
  "internal/numind/biz/agent/tool_full.go,internal/numind/biz/agent/tool_full_test.go,internal/numind/biz/agent/base_tool.go,internal/numind/biz/agent/minimal_to_full.go,internal/numind/biz/agent/tool.go" \
  "internal/numind/biz/agent/factory.go,internal/numind/biz/agent/factory_test.go" \
  "internal/numind/biz/agent/tool_kb_search.go,internal/numind/biz/agent/tool_kb_search_test.go,internal/numind/biz/agent/tool_learner_data_query.go,internal/numind/biz/agent/tool_learner_data_query_test.go,internal/numind/biz/agent/tool_document_generate.go,internal/numind/biz/agent/tool_document_generate_test.go,internal/numind/biz/agent/tool_image_gen.go,internal/numind/biz/agent/tool_image_gen_test.go,internal/numind/biz/agent/tool_bash_exec.go,internal/numind/biz/agent/tool_bash_exec_test.go,internal/numind/biz/agent/tool_get_current_date.go,internal/numind/biz/agent/tool_get_current_date_test.go"
```

预期 exit 0。

> **Agent B / D 编译顺序依赖**：Agent D 的 6 工具 import `agent.FullTool` / `agent.BaseTool`（Agent B 产物）。**文件物理 disjoint 通过 ndf-check-disjoint 验证（exit 0）**，但 Agent D 的 `go test` 要等 Agent B commit 才能编译。
>
> **主 session 责任**（**不要让 Agent D 自己判断**）：
> 1. 4 路 implementer 并行 dispatch 后，等所有 implementer 都 commit
> 2. 主 session 显式跑 `git -C worktree log --oneline -10` 确认 B 的 commit 存在
> 3. 主 session 显式跑 `go test ./internal/numind/biz/agent/...`（**只跑一次，验所有 task 整合后是否编译 + 测试通过**）
> 4. 若编译失败，按 commit 顺序定位（先 cherry-pick 验证 B 单独是否过）
> 5. 各 task reviewer 在此之后 dispatch

### Phase 2 串行

- Task 5 (M2 Stores) 依赖 Task 1 model commit
- Task 6 (M5 Registry) 依赖 Task 2/3/5（Stores + FullTool + Factory）

Task 5 + Task 6 文件 disjoint（store 包 vs biz/agent 包），但 Task 6 编译要 Task 5 IToolDefinitionStore 接口存在，所以 6 等 5 完。

### Phase 3 单 task 串行

Task 7 依赖全部前置；改 #2 现有 runner.go + biz.go + salesrag_test。

### Phase 4 单 task 串行

Task 8 最后。

---

## Task 详情

### Task 1：M1 DB + GORM Model + AutoMigrate

按 spec §2.1-§2.4 实施。验收：`go build ./...` + `go vet`。Commit message: `feat(agent-tool-registry): M1 tool_definition + tool_factory_registry tables`

### Task 2：M3 FullTool + BaseTool + MinimalToFull

按 spec §4.1-§4.4 实施。完整 36 方法 interface + 28 个默认值 BaseTool。**Rename 现有 #2 tool.go 中的 `Tool` → `MinimalTool`，更新所有 reference**（grep 验证）。Commit: `feat(agent-tool-registry): M3 FullTool interface (36 methods) + BaseTool + MinimalTool rename`

### Task 3：M4 ToolFactory + PlatformToolFactory

按 spec §5 实施。PlatformToolFactory.LoadTools 返回 6 工具实例 + metadata，**但 6 工具的实现由 Task 4 提供**。Task 3 阶段 PlatformToolFactory 内部用占位 stub 工具（如 `&placeholderTool{name: "kb_search"}`）让 LoadTools 至少能编译；Task 4 提供真实实现后 Task 3 改 import 即可。

或者：Task 3 只实现 `ToolFactory` interface + `factory.go`；`factory_platform.go` 留给 Task 7（依赖 Task 4 全部工具）。**采用此策略**：Task 3 只写 `factory.go`，`factory_platform.go` 由 Task 7 合并实现。文件归属相应调整。

修订后 Task 3 文件归属：
- `internal/numind/biz/agent/factory.go`（interface + types）
- `internal/numind/biz/agent/factory_test.go`（interface 编译期断言 + mock factory 测试）

### Task 4：M6 6 个工具实现

按 spec §7.1-§7.6 实施。注意：
- kb_search 调 SalesRAGBiz.Retrieve；ctx userID 由 runner 注入（Task 7 处理）；本 task 内单测 mock SalesRAGBiz
- learner_data_query 调 IStore.Users().GetByID
- document_generate Execute 主体可以是占位（返回 "[#3 placeholder for document_generate]"），完整 qwen-long 调用留 follow-up；spec §7.3 注释说"S4 implementer 按 ai-service.md 规范实施"——此处选**最小可工作**：调用 `aiservice.Chat(ctx, "agent-tool-document-generate", req)` with model="qwen-long" system prompt 文档生成模板，但不强求完美 prompt（足够编译 + 单测）
- image_gen / bash_exec stub：Execute 返回 error；IsEnabled gated by cfg

### Task 5：M2 Stores

按 spec §3 实施。GORM OnConflict 用法在 numind-server 现有代码 grep 一下作参考。**race detector 测试**：Upsert 并发同 tool_name → last-write-wins。

### Task 6：M5 AgentToolRegistry + Eino adapter

按 spec §6 实施。包含 P2-2 长度 assert + P2-3 单次 Upsert。race detector 测试：50 goroutine 并发 GetTool + 1 goroutine LoadAll。

**Task 6 文件归属**（Phase 2 串行，不与其他 task 并行）：
- `internal/numind/biz/agent/registry.go` + `registry_test.go`
- `internal/numind/biz/agent/adapter_full_to_eino.go` + `_test.go`（FullTool → Eino tool.InvokableTool 适配，runner.go 装配工具时使用）

Task 6 依赖 Task 2 (FullTool interface) + Task 3 (ToolFactory interface) + Task 5 (Stores) 均 commit 后才能编译。

### Task 7：M7 Runner + biz.go 接入 + factory_platform.go 完整版

修改：
- `runner.go`：spec §8 改造（RunRequest / NewAgentRunner / Run 内部装配）
- `runner_test.go`：mock 加 AgentToolRegistry；ToolNames 用法测试
- `biz.go`：spec §8.3 修改 + 接入
- `factory_platform.go`：Task 3 推迟到本 task 完整实施（含 6 个真实工具实例化，需要 Task 2/4 commit 后）
- `salesrag_test.go realBizOnlyCustomers`：加 `AgentTools()` 方法

**严格 commit 顺序**：
1. T7.1：完成 factory_platform.go 实现（先把工具实例化 wire 起来）
2. T7.2：改 runner.go + runner_test.go
3. T7.3：改 biz.go（含修改 #2 现有 NewAgentRunner 行）
4. T7.4：改 salesrag_test.go mock
5. `go build ./...` 通过 + `go vet ./...` 干净
6. `go test -race ./internal/numind/biz/agent/...` 全 PASS
7. commit

### Task 8：M8 集成测试 + race detector

- registry_integration_test.go：full Registry 启动流程（mock 6 工具 + mock 2 stores） → LoadAll → GetTool → seed tool_definition 验证
- runner_integration_test.go 改造：用真实 Registry 替代 mock Tools 字段
- 跑 `task test`（含 -race -cover）整体 PASS
- biz/agent 覆盖率目标 ≥ 78%（沿用 #2 标准，因 Eino bridge / 真实 LLM 调用仍无法触发）

---

## S5 验证策略

**纯后端：Go unit + integration + race detection**（与 #2 #3 sandbox 等价）

### 验证方式

- M1/M2/M5 store + registry：unit test in-memory SQLite + AutoMigrate
- M3/M4：编译期断言 + BaseTool 默认值测试
- M6 6 工具：mock 依赖（SalesRAGBiz / Users store / aiservice）
- M7 runner：mock IAgentRunStore + mock AgentToolRegistry
- M8 集成：registry + runner + 6 工具串联

### 理由

- 无 UI / 无 HTTP API
- registry 并发安全是核心，必须 -race
- 6 工具单测主要验证适配真实 API 签名正确（reviewer 已 grep 验证）
- 真实 LLM 调用（document_generate qwen-long）留给 dev 部署后手工触发

### 回归保护

所有测试进 CI 主套件。

### 必停场景

- aiservice.Chat 真实签名变化
- SalesRAGBiz / Users store 接口变化
- Eino v0.8.13 兼容性

---

## ndf-done 前置门槛

- [ ] manifest `completed_tasks == 8 && reviewed_tasks == 8 && stage == S6`
- [ ] 全部文件 commit
- [ ] `task test` 含 -race -cover PASS
- [ ] `task lint` 干净
- [ ] biz/agent 覆盖率 ≥ 78%
- [ ] 无 P0/P1 残留
- [ ] **未部署 qa/prod**
- [ ] ndf-done 原子化 merge → develop

---

**Plan 完成，待 reviewer 审。**
