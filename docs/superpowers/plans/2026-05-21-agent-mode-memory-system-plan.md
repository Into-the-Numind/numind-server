# Agent 模式 Memory 系统 — S3 Task Plan

> NDF v2 S3 工件 | Feature: agent-mode-memory-system | #7/14
> 前置：S0 (commit `7f9b5d5b`) / S1 (commit `a1004caf`) / S2 (commit `f192a5b6`)

---

## §1 总览

11 个 M task（M1-M10 实现 + M11 S5 验证策略）。

**预估总工时**：4-6 小时（按 NDF 标准 task 复杂度）

**依赖图**：
```
M1 (migration SQL) ──┐
                     ├──> M2 (GORM model) ──> M3 (store impl) ──┐
                     │                                          │
M4 (biz types + fence + errno) ─────┬─> M5 (Notepad)            │
                                    │                           ├──> M7 (Provider) ──┐
                                    └─> M6 (Retrieval) ─────────┘                    │
                                                                                     │
                                                                                     ├──> M8 (tools) ──> M9 (factory_platform + IStore wire)
                                                                                     │
                                                                                     └──> M10 (runner + ctx_keys + biz.go wire + AutoMigrate)

M11 = S5 验证策略（贯穿；最终 M10 后执行）
```

**串行 critical path**：M1 → M2 → M3 → M7 → M10（5 个 task 串行依赖；其他可 disjoint 并行）

---

## §2 Task 详情

### M1 — Migration SQL

**目标**：写 2 张表 DDL + 2 个 rollback。

**文件归属**（独占；Tier 3 disjoint）：
- `migrations/20260521_120000_create_agent_session_memory.sql` (新建)
- `migrations/20260521_120000_create_agent_session_memory_rollback.sql` (新建)
- `migrations/20260521_120100_create_user_global_memory.sql` (新建)
- `migrations/20260521_120100_create_user_global_memory_rollback.sql` (新建)

**实现**：直接按 S2 §2.1 / §2.2 DDL 写。`_rollback.sql` 用 `DROP TABLE IF EXISTS`。

**验收**：
- migration SQL 可单独跑通（手工测试 dev 环境前的形式验证）
- 字段类型 / NOT NULL / INDEX 与 S2 §2.1 / §2.2 完全一致

---

### M2 — GORM Model + Create 边界单测

**目标**：定义 2 个 model；含 `Score=0` / `Confidence=0` Create 边界测试。

**文件归属**（独占；Tier 3 disjoint）：
- `internal/pkg/model/agent_session_memory.go` (新建)
- `internal/pkg/model/user_global_memory.go` (新建)
- `internal/pkg/model/agent_session_memory_test.go` (新建)
- `internal/pkg/model/user_global_memory_test.go` (新建)

**依赖**：M1 完成（migration SQL 字段定义是 model 的依据）

**实现**：按 S2 §2.4 GORM model 模板写，含 `TableName()` 方法。

**Test 覆盖**：
- `TestAgentSessionMemory_Create_ScoreZero`: 显式 `Score: 0.0` Create → DB 行存 0.0（非 default 1.0）
- `TestUserGlobalMemory_Create_ConfidenceZero`: 显式 `Confidence: 0.0` Create → DB 行存 0.0

**关键决策**：
- model_test.go 用 in-memory SQLite（`gorm.io/driver/sqlite` `:memory:`）AutoMigrate
- 测试用 `db.Select("*").Create(&m)` 验证 P2-1 / S0 P2-1 fix 实际有效

**验收**：
- `go test ./internal/pkg/model/...` PASS
- 覆盖率不强制要求（model 是 struct + tag，逻辑少）

---

### M3 — Store 实现 + IStore 接口注册

**目标**：实装 `agentSessionMemoryStore` + `userGlobalMemoryStore`；注册到 `IStore`。

**文件归属**（独占；Tier 3 disjoint）：
- `internal/numind/store/agent_session_memory.go` (新建)
- `internal/numind/store/user_global_memory.go` (新建)
- `internal/numind/store/store.go` (MOD — 加 2 行 getter)
- `internal/numind/store/agent_session_memory_test.go` (新建)
- `internal/numind/store/user_global_memory_test.go` (新建)

**依赖**：M2 完成

**实现**：
1. 写 `IAgentSessionMemoryStore` interface + `agentSessionMemoryStore` struct + 5 个方法（按 S2 §3.1）
2. 写 `IUserGlobalMemoryStore` interface + `userGlobalMemoryStore` struct + 5 个方法（按 S2 §3.2）
3. 改 `store.go` 加 2 行 IStore method + 2 行 `datastore.AgentSessionMemories()` / `.UserGlobalMemories()` 实现
4. **新加 NewTestStore 注册**（agent_definition_test 是已建的 helper，复用 `NewTestStore(db)` 模式）

**Test 覆盖**：
- Store CRUD happy path（Create / Get / List / Update / Delete）
- Upsert 并发：100 goroutine 同 key → 最终行数 1（用 `errgroup.WithContext` + `sync.WaitGroup`）
- 跨 user 隔离：user_A 写入 + user_B Read/List → 0 行
- TTL 过滤：expires_at < now() 行不在 `AliveOnly=true` 结果中
- ListByUserKind kind 过滤 + limit
- Upsert ON CONFLICT 行为：(user_A, "key1") write 1 → write 2（不同 value） → 行数 1，值已更新

**覆盖率目标**：≥75%

**关键决策**：
- Tx 变体不单独建（沿用 #5）— `agentSessionMemoryStore.db` 直接 `WithContext(ctx)`
- **P1-3 修复**：单测用包内独立 `newTestDB(t, models...)` helper — **必须**用文件 DB + `?_busy_timeout=5000&_journal_mode=WAL`（与 `agent_definition_test.go` 现有 helper 一致；`:memory:` 不支持 100 goroutine 并发 upsert，会 "database is locked"）
- store.go 新增 `AgentSessionMemories()` / `UserGlobalMemories()` 后，`NewTestStore` 函数自动生效（看实际签名 — 当前 NewTestStore 只接 *gorm.DB，datastore impl 上的方法自动注册）

---

### M4 — biz/memory types + fence + errno

**目标**：定义所有共享类型 + fence 渲染逻辑 + 错误码。

**文件归属**（独占；Tier 3 disjoint）：
- `internal/numind/biz/memory/types.go` (新建)
- `internal/numind/biz/memory/fence.go` (新建)
- `internal/numind/biz/memory/errno.go` (新建)
- `internal/numind/biz/memory/types_test.go` (新建)
- `internal/numind/biz/memory/fence_test.go` (新建)

**依赖**：无（独立子包）

**实现**：按 S2 §4.2 + §4.8 + §4.10 直接写。

**Test 覆盖**：
- `TestMemoryKind_Valid`: L1 接受所有 6 kind；L2 接受 5 kind（不接受 summary）
- `TestFence_RenderEmpty`: 空 l1+l2 → ""
- `TestFence_RenderOnlyL1`: 仅 [本 agent 历史] 段
- `TestFence_RenderOnlyL2`: 仅 [全局画像] 段
- `TestFence_RenderBoth`: 全局画像 + 本 agent 历史，结构完整
- `TestEscapeForStorage_HTMLEntities`: `<script>` / `</memory-context>` / `&amp;` 转义正确
- `TestUnescapeForToolResponse`: 反转义正确

**覆盖率目标**：≥90%（纯函数，覆盖率应高）

---

### M5 — biz/memory Notepad

**目标**：实装 `Notepad` interface + `notepadImpl`。

**文件归属**（独占；Tier 3 disjoint）：
- `internal/numind/biz/memory/notepad.go` (新建)
- `internal/numind/biz/memory/notepad_test.go` (新建)

**依赖**：M3 (store) + M4 (types + fence + errno)

**实现**：按 S2 §4.9 直接写。

**Test 覆盖**：
- `TestNotepad_Write_HappyPath`: value 已转义后存储
- `TestNotepad_Write_KindInvalid`: kind 不合法 → ErrMemoryKindInvalid
- `TestNotepad_Write_KeyTooLong`: > 100 → ErrMemoryKeyTooLong
- `TestNotepad_Write_ValueTooLong`: > 1024 → ErrMemoryValueTooLong
- `TestNotepad_Write_UserRequired`: userID=0 → ErrMemoryUserRequired
- `TestNotepad_Write_Upsert`: 同 key 重复写 → 行数 1，值更新
- `TestNotepad_Read_NotFound`: 不存在 → (nil, nil)
- `TestNotepad_ListByKind`: 按 kind + limit
- `TestNotepad_Delete`: 按 key 删
- `TestNotepad_ConfidenceZero`: 显式传 `Confidence=ptr(0.0)` → DB 存 0.0
- `TestNotepad_CrossUserIsolation`: u1 写 + u2 read → 0

**覆盖率目标**：≥85%

---

### M6 — biz/memory retrieval + embedder mock

**目标**：实装 `Retriever` interface + `retrieverImpl` + `mockEmbedder`。

**文件归属**（独占；Tier 3 disjoint）：
- `internal/numind/biz/memory/retrieval.go` (新建)
- `internal/numind/biz/memory/embedder.go` (新建)
- `internal/numind/biz/memory/retrieval_test.go` (新建)

**依赖**：M3 (store) + M4 (types)

**实现**：按 S2 §4.6 / §4.7 / §4.11 直接写。
- BM25Searcher / VectorStore / Embedder interface 在 retrieval.go 内
- mockEmbedder 在 embedder.go
- v1 BM25 实现：在 `RetrieveL1` 中 `strings.Contains` 实现（不引 bleve）

**Test 覆盖**：
- `TestRetrieveL1_RecencyBoost`: 30 天前的项 score ≈ 1.0 * exp(-1) ≈ 0.368
- `TestRetrieveL1_BM25Boost`: query 命中 content → score *1.5
- `TestRetrieveL1_BothBoosts`: 同时命中 → 顺序正确（先 BM25 后 decay）
- `TestRetrieveL1_AliveFilter`: expires_at < now → 不在结果
- `TestRetrieveL1_TopK`: 50 行输入 + topK=5 → 返回 5
- `TestRetrieveL2_FactAndPreference`: 仅返回 fact + preference 两类 kind
- `TestRetrieveL2_TopKPerKind`: 5 行 fact + 5 行 preference + topKPerKind=3 → 总 6 条
- `TestMockEmbedder_ZeroVector`: 返回 1024 维零向量

**覆盖率目标**：≥80%

**关键决策**：
- **P2-6 修复**：BM25Searcher / VectorStore 接口按 S2 §4.5 保留 interface 定义（v2 扩展点）；v1 `NewRetriever()` 内部用 inline struct 作 BM25/Vector impl，但接口存在不删（与 spec 一致）
- mockEmbedder 不需要测试 — return 固定零向量

---

### M7 — biz/memory Provider (composite)

**目标**：实装 `MemoryProvider` interface + `compositeProvider`。

**文件归属**（独占；Tier 3 disjoint）：
- `internal/numind/biz/memory/provider.go` (新建)
- `internal/numind/biz/memory/provider_test.go` (新建)
- **P2-2 修复**：不新建 `short_term.go` / `long_term.go`（v1 全部逻辑在 provider.go + retrieval.go 内联，避免空 stub 死代码；未来 #11/#14 扩展时再拆分）

**依赖**：M3 (store) + M4 (types + fence) + M6 (retrieval)

**实现**：按 S2 §4.3 / §4.4 直接写。

**Test 覆盖**：
- `TestProvider_SystemPromptBlock_Empty`: 无 L1+L2 → ""
- `TestProvider_SystemPromptBlock_L1Only`: 仅 L1 → 含 [本 agent 历史]
- `TestProvider_SystemPromptBlock_L2Only`: 仅 L2 → 含 [全局画像]
- `TestProvider_SystemPromptBlock_Both`: 含两个 section
- `TestProvider_SystemPromptBlock_UserZero`: userID=0 → "" (early return)
- `TestProvider_SystemPromptBlock_L1Error_DegradeL2`: L1 store error → 降级仅 L2
- `TestProvider_SystemPromptBlock_L2Error_DegradeL1`: L2 store error → 降级仅 L1
- `TestProvider_SystemPromptBlock_BothError`: 两者 error → "" + warn log
- `TestProvider_Clear_BothLayers`: Clear → L1 + L2 都 DeleteByUser
- `TestProvider_OnPreCompress_NoOp`: return nil
- `TestProvider_SyncTurn_NoOp`: return nil
- `TestProvider_Prefetch_Stub`: 调用 Retrieve 并返回结果（v1 等价于 SystemPromptBlock 内部）
- **`TestProvider_SystemPromptBlock_AfterWrite`（P2-3 / PI-2 集成测试）**：用 newTestDB 起 SQLite → 用 Notepad 写一条 L2 → 调 SystemPromptBlock(userID=1, agentDefID=100) → 返回字符串含写入的 value（已转义形式）；这是 PI-2 验证入口

**覆盖率目标**：≥80%

---

### M8 — Memory Tools (memory_write + memory_read)

**目标**：实装 2 个 FullTool。

**文件归属**（独占；Tier 3 disjoint）：
- `internal/numind/biz/agent/tool_memory_write.go` (新建)
- `internal/numind/biz/agent/tool_memory_read.go` (新建)
- `internal/numind/biz/agent/tool_memory_write_test.go` (新建)
- `internal/numind/biz/agent/tool_memory_read_test.go` (新建)

**依赖**：M5 (Notepad)

**实现**：按 S2 §5.1 / §5.2 直接写（**嵌入 BaseTool + 重写 5 个方法**）。

**Test 覆盖**：
- `TestMemoryWriteTool_Execute_HappyPath`: notepad.Write 被调用
- `TestMemoryWriteTool_Execute_JSONInvalid`: bad JSON → error
- `TestMemoryWriteTool_Execute_UserMissing`: no userID in ctx → ErrMemoryUserRequired
- `TestMemoryWriteTool_Execute_AgentDefIDZero`: ctx 无 agentDefID → source_agent_definition_id=nil
- `TestMemoryWriteTool_Execute_AgentDefIDPresent`: ctx 有 agentDefID → SourceAgentDefinitionID 注入
- `TestMemoryWriteTool_BaseTool_Methods`: IsReadOnly=false / IsDestructive=false / AlwaysLoad=true
- `TestMemoryReadTool_Execute_ByKey_Found`: 单条 JSON
- `TestMemoryReadTool_Execute_ByKey_NotFound`: 空数组
- `TestMemoryReadTool_Execute_ByKind`: 多条 JSON
- `TestMemoryReadTool_Execute_UnescapeValue`: DB 存 `&lt;script&gt;` → 返回 `<script>` (反转义)
- `TestMemoryReadTool_Execute_LimitClamp`: limit=100 → clamp 到 10 default; limit=0 → 10
- `TestMemoryReadTool_Execute_CrossUserIsolation`: u1 写 + u2 read → 0
- `TestMemoryReadTool_BaseTool_Methods`: IsReadOnly=true / IsSearchOrReadCommand=true

**覆盖率目标**：≥85%

---

### M9 — factory_platform.go 注册 + 集成测试

**目标**：把 2 个 memory 工具注册到 platform tool factory；端到端集成测试。

**文件归属**（独占；Tier 3 disjoint）：
- `internal/numind/biz/agent/factory_platform.go` (MOD — 加 2 个 tool + 2 条 metadata)
- `internal/numind/biz/agent/factory_platform_test.go` (MOD — 测 8 工具列表)

**依赖**：M8 (tools)

**实现**：按 S2 §5.3 改 `LoadTools`：
- `tools = append(tools, NewMemoryWriteTool(np), NewMemoryReadTool(np))`
- `metadata = append(metadata, {memory_write metadata}, {memory_read metadata})`

**Test 覆盖（P0-2 修复 — 保留现有 nil-ds 测试）**：
- **保留** 现有 `TestPlatformToolFactory_LoadTools`（nil ds → 6 工具，不破坏 #3 既有测试）
- **新增** `TestPlatformToolFactory_LoadTools_WithDS_8Tools`：传入非 nil test store（mock IUserGlobalMemoryStore，可直接 inline `&fakeUserGlobalMemoryStore{}` struct）→ 现有 6 + memory 2 = 8 个 FullTool
- **新增** `TestPlatformToolFactory_LoadTools_WithDS_Metadata8`：传入非 nil test store → 8 条 ToolMetadata，含 memory_write + memory_read 且 RiskLevel/Category 字段对

**覆盖率目标**：维持现有 #3 / #5 覆盖率不下降

---

### M10 — Runner.go 集成 + context_keys + biz.go wire + AutoMigrate

**目标**：runner.go 装配 memory；新增 ctx key；biz.go wire；AutoMigrate。

**文件归属**（独占；Tier 3 disjoint，**注意 #6/#8/#9 也改 runner.go 但区域不重叠**）：
- `internal/pkg/middleware/context_keys.go` (MOD — 加 CtxKeyAgentDefinitionID + 2 函数)
- `internal/pkg/middleware/context_keys_test.go` (新建 — 文件目前不存在；含 `TestNewContextWithAgentDefinitionID_GetSet`：set 100 → get 100, true / set 0 → get 0, true / 未 set → get 0, false；P2-5 修复)
- `internal/numind/biz/agent/runner.go` (MOD — RunRequest 新字段 + WithMemoryProvider option + Step 4 改造)
- `internal/numind/biz/agent/runner_memory_test.go` (新建 — 测 SystemPrompt 注入路径)
- `internal/numind/biz/biz.go` (MOD — wire memory.NewProvider + agent.WithMemoryProvider；与 #5 同行追加一个 option)
- `internal/numind/helper.go` (MOD — AutoMigrate 加 2 张新表；与 #5 注册块紧邻)

**依赖**：M3 (store) + M7 (provider)

**实现**：
1. **context_keys.go**：按 S2 §7 直接写（加 CtxKeyAgentDefinitionID + NewContextWithAgentDefinitionID + AgentDefinitionIDFromCtx 3 项）
2. **runner.go（P0-1 修复 — 必读）**：按 S2 §6.1 / §6.2 / §6.3 改造。**关键删除点**：
   - **删除现有 runner.go 第 169 行** `req.SystemPrompt = skill.PlatformBasePrompt + body + skill.PlatformSafetyFooter`
   - 在 skill if-block 内只赋值 `body = ad.GeneratedSkillBody`（或 CustomSkillBody）
   - 把 `ctx = middleware.NewContextWithAgentDefinitionID(ctx, req.AgentDefinitionID)` 加在 skill if-block 内 skill lookup 之后（**仅注入 agentDefID 不注入 sessionID**，P2-3 决议）
   - 在 skill if-block 之后新增 5 个 placeholder var（tenantHardRulesPlaceholder / memoryDisclaimerBlock / memorySystemBlock / toolsSectionPlaceholder + 复用 body）
   - memory if-block 调 `r.memoryProvider.SystemPromptBlock`
   - **6 段统一拼接**到 `req.SystemPrompt` 单一表达式 — 取代原第 169 行
3. **biz.go（P1-2 修复 — 注意复数命名）**：在 NewAgentRunner 调用处加：
   ```go
   memoryProvider := memory.NewProvider(store.AgentSessionMemories(), store.UserGlobalMemories())  // 复数
   ```
   并在 `agent.NewAgentRunner(...)` 的 opts 列表追加 `agent.WithMemoryProvider(memoryProvider)`
4. **helper.go**：AutoMigrate 加 2 个 model；紧邻 #5 `&model.AgentDefinition{}` 注册块

**Test 覆盖**（`runner_memory_test.go`）：
- `TestRunner_EnableMemoryTrue_WithProvider_HasMemoryBlock`: SystemPrompt 含 `<memory-context>` + disclaimer
- `TestRunner_EnableMemoryFalse_NoBlock`: SystemPrompt 不含 memory
- `TestRunner_EnableMemoryTrue_NilProvider_NoBlock`: SystemPrompt 不含 memory（降级）
- `TestRunner_EnableMemoryTrue_ProviderError_NoBlock_NoBlock`: SystemPrompt 不含 memory + warn log
- `TestRunner_EnableMemoryTrue_EmptyMemory_NoBlock`: provider 返回 "" → SystemPrompt 不含
- `TestRunner_AgentDefIDInCtx`: Run 后 ctx 包含 CtxKeyAgentDefinitionID（用 hook test）

**覆盖率目标**：runner.go 覆盖率不下降（#5 已达 80%+）

**关键决策**：
- runner.go 改造保持 #5 / #4 现有测试不破坏（沿用 #5 placeholder 协调模式）
- biz.go wire 与 #5 / #6 / #8 / #9 并行 — 全部用 Functional Option 模式叠加（自然 merge-friendly）

---

### M11 — S5 验证策略

**目标**：定义 S5 验收方式 + 关键用户路径列表。

**文件归属**：本 plan 文档（§3）

**S5 验证方式（规则 #10 — 必须 reviewer 审查）**：

- **验证方式**：**纯后端 TDD + go test -race**（不走 Playwright E2E / gstack /qa）
- **理由**：
  1. 本 feature 是纯后端 biz/memory + tool + runner 改造，**无用户端 UI 改动**（学员侧 memory 可视化在 #11，管理端配置在 #10）
  2. 关键不变量都可以通过单测 + 集成测试覆盖（fence 防注入、隔离边界、并发 upsert）
  3. **规则 10 高风险类别适用性分析（P2-1）**：本 feature **不涉及**规则 10 列出的"支付、权限、会员等级等高风险业务逻辑"。Memory 系统涉及学员隐私（跨 user 隔离）+ 防注入（fence tag），这两类风险通过 PI-3 跨 user 隔离测试（store 层 + tool 层双重覆盖）+ PI-4 fence 防注入测试（fence + tool + provider 三层覆盖）严密保护，且每个测试都用 race detector 验证并发安全。学员隐私 ≠ 会员等级权限；防注入 ≠ 支付授权 — 故规则 10 不强制 E2E。
  4. Playwright E2E / gstack /qa 都需要 UI 触发；本 feature 无 UI → 这些工具无法运行
- **回归保护**：单元测试 + race detector 持久化在代码库；P1-3 PI-1~PI-5 五条 v1 代理指标全部转化为可执行的 Go test
- **风险**：选 TDD 不写 E2E → 未来 UI 接入（#11）时可能在 UI ↔ biz 边界发现问题；接受此风险，由 #11 自己写学员端 E2E
- **诚实声明**：本 feature 选纯后端 TDD = **没有持久化端到端回归保护**；未来修改 memory 相关代码时需手工跑相关单测 + 跑 #11 学员端 E2E（如已落地）

**关键用户路径列表（S5 实际验证）**：

| 路径 | 验证 | Test |
|------|------|------|
| **PI-1** EnableMemory + provider → system prompt 含 `<memory-context>` | TestRunner_EnableMemoryTrue_WithProvider_HasMemoryBlock | runner_memory_test.go |
| **PI-2** memory_write 写入后 SystemPromptBlock 含此条 | TestProvider_SystemPromptBlock_AfterWrite | provider_test.go（集成测） |
| **PI-3** 跨 user 隔离 | TestNotepad_CrossUserIsolation + TestMemoryReadTool_Execute_CrossUserIsolation | notepad_test.go + tool_memory_read_test.go |
| **PI-4** fence 防注入 | TestEscapeForStorage_HTMLEntities + TestFence_RenderBoth + TestMemoryReadTool_Execute_UnescapeValue | fence_test.go + tool_memory_read_test.go |
| **PI-5** Notepad 并发 upsert | TestStore_UserGlobalMemory_Upsert_Concurrent100 | store/user_global_memory_test.go |

**S5 准入门槛**：
- 上述 5 个 PI test 全 PASS
- `go test -race ./internal/numind/biz/memory/... ./internal/numind/biz/agent/... ./internal/numind/store/...` 全 PASS
- biz/memory 包覆盖率 ≥80%
- biz/agent 包覆盖率不下降
- `go vet ./...` exit 0
- `task lint` PASS
- 0 prod 影响声明：`config_prod.yaml` zero diff + 无 git tag + 无 /deploy-prod

---

## §3 Tier 3 并行批次（dispatch order）

**Wave 1**（M1 + M4 — 完全 disjoint，可并行）：
- M1: migrations 文件夹
- M4: biz/memory types + fence + errno

**Wave 2**（M2 — 依赖 M1）：
- M2: GORM model + 边界测试

**Wave 3a**（M3 串行）：
- M3: store impl（依赖 M2）+ IAgentSessionMemoryStore / IUserGlobalMemoryStore 接口定义 + IStore 注册

**Wave 3b**（M5 + M6 并行 — 必须 M3 完成后）：
- M5: biz/memory notepad（依赖 M3 完成）
- M6: biz/memory retrieval（依赖 M3 完成）

> **P1-1 修复**：M5/M6 编译时需要 `IUserGlobalMemoryStore` / `IAgentSessionMemoryStore` interface 已注册到 store 包，**必须**在 M3 完成 commit + push 后才能 dispatch；不接受 "stub first" 模式（之前误判 — 接口未就绪时无法编译）。Wave 3a 必须先于 Wave 3b 完成。

**Wave 4**（M7 — 依赖 M3 + M5 + M6）：
- M7: biz/memory provider

**Wave 5**（M8 — 依赖 M5）：
- M8: 2 个 memory 工具

**Wave 6**（M9 + M10 — 主要 disjoint，但 M10 改 runner.go 与 #6/#8/#9 可能冲突；本 feature 内 disjoint）：
- M9: factory_platform 注册
- M10: runner.go + ctx_keys + biz.go wire + AutoMigrate

**串行 critical path**：M1 → M2 → M3 → M7 → M10（5 个 task）

---

## §4 每 task 完成后流程（沿用 #5 模式）

1. Implementer subagent 完成代码 + commit
2. 主 session 验证 commit 状态（`git log --oneline -1` + `git status`）
3. **完成时**：主 session 更新 `.ndf/manifest.yaml` `progress.completed_tasks += 1`（P2-4 修复）
4. **并行** dispatch 2 个 Sonnet reviewer：
   - **Spec Compliance Review**：核对 S2 spec 字段名 / interface 签名 / 段位约定 / 类型对齐 / Out of scope 边界
   - **Code Quality Review**：审 Go idiom / GORM tag / race condition / 错误包装 / 命名规范 / nil-safety
5. 修 P0/P1（顺手 P2）
6. **review 通过后**：更新 `.ndf/manifest.yaml` `progress.reviewed_tasks += 1`
7. 下一 task

---

## §5 与 #6 / #8 / #9 协调

| 文件 | 本 feature 改动 | #6/#8/#9 改动 | conflict 风险 |
|------|---------|---------|---------|
| `runner.go` Step 4 | 加 4 个 placeholder var + memory if-block + 6 段拼接表达式 | 各自加各自的 placeholder 赋值 | 高 — S6 手工合并 |
| `biz.go` NewAgentRunner | 加 `WithMemoryProvider` option 一行 | 各自加自己的 option 一行 | 低 — Functional Option 自然 merge |
| `helper.go` AutoMigrate | 加 2 行 `&model.X{}` | 各自加 model | 低 — append 行 |
| `context_keys.go` | 加 CtxKeyAgentDefinitionID + 2 函数 | #6 / #8 / #9 不动 ctx | 无 |
| `factory_platform.go` LoadTools | 在 tools slice append + metadata append | #6 不动 / #8 不动 / #9 不动 | 无 |

**S6 ndf-done 准备**：合并前 `git fetch && git diff develop..HEAD -- internal/numind/biz/agent/runner.go internal/numind/biz/biz.go internal/numind/helper.go` 看冲突预览。

---

## §6 完成判定（S5 准入）

每 task 完成后 `.ndf/manifest.yaml.progress.completed_tasks += 1`。

**进 S5 acceptance 准入**：
- 11 task 全 completed
- reviewed_tasks == completed_tasks == 11
- all single-feature tests PASS
- race detector PASS
- 覆盖率达标
- 0 prod 影响

---

**S3 完结。S4 编码开始。**
