# S5 验收记录 — agent-mode-tool-registry

## 验收日期
2026-05-21

## 验收人
AI 主控（autopilot 协议）

## 测试环境
本地 macOS + GORM in-memory SQLite

**未部署到任何环境**（dev/qa/prod）；S5 后进 S6 ndf-done。Dev 部署作单独跟进。

## 结果
**ACCEPTED**

---

## 测试路径

| # | 路径 | 命令 | 结果 |
|---|------|------|------|
| 1 | biz/agent 包单测 + race | `go test -race ./internal/numind/biz/agent/...` | ✅ ALL PASS |
| 2 | biz/agent 覆盖率 | `go test -cover ...` | **76.4%**（比 #2 78.3% 略低，因为 #3 引入大量占位类型/默认值未直接覆盖）|
| 3 | store 单测 + race | `go test -race ./internal/numind/store/...` | ✅ PASS（19% 包级 cov；Tool stores 内部高覆盖）|
| 4 | `go build ./...` | — | ✅ clean |
| 5 | `go vet ./...` | — | ✅ exit 0 |
| 6 | 跨模块集成验证 | registry+factory+runner+adapter chain | ✅ PlatformToolFactory.LoadTools → registry → GetTool → adaptFullToEinoTool 全链路无 panic |

---

## M1-M8 分模块验收

| 模块 | commit | reviewer | 验收 |
|------|--------|---------|------|
| M1 DB schema | `4157dfaa` | PASS_NO_FIXES | ✅ |
| M2 IToolDefinition + IToolFactoryRegistry Stores | `0ef34537` | PASS_NO_FIXES | ✅（13/13 tests） |
| M3 FullTool 36-method + BaseTool + MinimalTool rename | `9ed352f1` | PASS（1 P2 修） | ✅ |
| M4 ToolFactory interface | `2b06a0c3` | PASS_NO_FIXES | ✅ |
| M5 AgentToolRegistry + Eino adapter | `70bc095f` | PASS（1 P2 修） | ✅ |
| M6 6 platform tools | `08d3b289` + `ebde5b87` | PASS（1 P1 + 2 P2 修；document_generate 降级为 stub） | ✅ |
| M7 Runner integration + biz wire + Salesrag mock fix | `8cac8ce0` | PASS（1 P2 修） | ✅ |
| M8 Integration tests | 分散在 M5/M6/M7 测试中 | — | ✅ |

---

## 4 个核心假设验证

| 假设 | 结果 |
|------|------|
| FullTool 36 方法可被运营/开发理解 + BaseTool 默认值实用 | ✅ BaseTool 31 default + 5 必须重写，工具 impl 平均 7-12 行 |
| ToolFactory plugin 扩展性 | ✅ PlatformToolFactory v1；MCP/CLI/Webhook 留接口 |
| Registry concurrent 安全 | ✅ sync.RWMutex + 100-goroutine race test 干净 |
| #2 Runner 集成无破坏 | ✅ ToolNames replace Tools；#2 现有测试用 nil registry 全过；biz.go 整包编译 clean |

---

## 不变量验证（spec §10）

| # | 不变量 | 验证 |
|---|--------|------|
| 1 | #2 兼容（MinimalTool 保留 + WrapMinimal） | ✅ tool.go Tool→MinimalTool rename；#2 tool_test 仍 PASS |
| 2 | FullTool 36 方法 + BaseTool 默认 31 | ✅ tool_full_test 编译期断言 |
| 3 | bash_exec/image_gen/document_generate 默认禁用 | ✅ IsEnabled(ToolConfig{}) 全 false |
| 4 | ToolFactory.Watch v1 noop | ✅ |
| 5 | INSERT IGNORE 不破坏运营 is_enabled | ✅ OnConflict.DoUpdates 不含 is_enabled / is_beta |
| 6 | Registry 并发安全 | ✅ race detector 干净 |
| 7 | aiservice 唯一入口 | ✅ document_generate stub 不裸 HTTP；真实落地由 #12 |
| 8 | tool_factory_registry #3 read-only | ✅ List 仅启动 seed 一次 |
| 9 | prod 零影响 | ✅ |

---

## 关键 follow-ups

1. **#4 sandbox-integration**：把 bash_exec stub 替换为真实实现，注入 RunHooks
2. **#6 permission-pipeline**：使用 IsDestructive / IsReadOnly / InterruptBehavior 实现权限 gate
3. **#8 narration-layer**：使用 Prompt / BackfillObservableInput / NarrationVerb / NarrationDetail
4. **#10 configurator-ux**：tool_definition + tool_factory_registry 表 CRUD 管理端
5. **#12 billing-integration**：注册 `agent.document_generate` taskID + 添加 qwen-long 计费规则
6. **runner.go ReAct loop 真实集成**（#2 skeleton 留的 `_ = einoAgent`）：可能在 #4 或单独 follow-up
7. **InputSchema 自动推导**：当前 adaptFullToEinoTool 用空 params；#8 接入 utils.InferTool

---

## ndf-done 前置门槛

- [x] manifest `completed_tasks == 8 && reviewed_tasks == 8 && stage == S6`
- [x] 全部文件 commit
- [x] `task test`（含 race）PASS
- [x] `go vet` clean
- [x] biz/agent 覆盖率 76.4%（接近 plan 目标 78%；考虑到 #3 引入大量 stub + 默认值后下降合理，reviewer 接受）
- [x] 无 P0/P1 残留
- [x] **未部署 qa/prod**
- [ ] `ndf-done` 原子化 merge → develop（**S6 步骤**）

---

进入 S6 ndf-done。
