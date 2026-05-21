# NDF S3 Task Plan · `agent-mode-compliance-3layer`

**Track**：Standard
**Feature ID**：`agent-mode-compliance-3layer`（14-feature 分解 #13/14）
**起草日期**：2026-05-21
**前置 stage**：S2 通过（commit `21adda29`）

---

## §1 Task 总览

16 个原子 task（M1-M16），分为 5 个 Wave。

| Task | Wave | 范围 | LOC 估 | 测试要求 | 依赖 |
|---|---|---|---|---|---|
| **M1** | W1 | Migration SQL 双文件（compliance_rule + compliance_audit_log）| ~120 | pre/post-check SQL | 无 |
| **M2** | W1 | model.ComplianceRule + model.ComplianceAuditLog + 单测（含 default:true bool 回归）| ~180 | go test in-memory SQLite | 无（参考 S2 §1 schema）|
| **M3** | W2 | errno/compliance.go 新文件（6 errno）| ~30 | go test | 无 |
| **M4** | W2 | biz/compliance/types.go（ToolInfo / ComplianceResult / ComplianceRequest / ComplianceGate interface / truncate helper / DefaultOutOfScopeNarration）+ test | ~100 | go test | M2（model 常量）+ M3（errno re-export）|
| **M5** | W2 | biz/compliance/platform_rules.go（L0 6 条硬规则常量）+ test | ~40 | go test | 无 |
| **M6** | W3a | store/compliance.go interface + impl + test + store.go IStore 扩展 + WithSkipScope("compliance_self") | ~400 | go test -race + SQLite | M2, M3 |
| **M7** | W3b | biz/compliance/cache.go（TTLCache）+ test（含 race）| ~250 | go test -race | M2（rule model）|
| **M8** | W3c | biz/compliance/skill_soft_rules.go + test | ~120 | go test | M2（AgentDefinition model）|
| **M9** | W3d | biz/compliance/injection_detector.go（18 关键词 + mock classifier + WrapInputFence）+ test | ~250 | go test | M4（types）|
| **M10** | W3e | biz/compliance/fence_validator.go（11 fence tag 检测）+ test | ~150 | go test | 无 |
| **M11** | W4a | biz/compliance/audit_logger.go（async + Start/Stop + drop count）+ test（含 race + flush-on-shutdown）| ~280 | go test -race | M6（store）|
| **M12** | W4b | biz/compliance/tenant_rules.go（TenantRuleProvider + cache 整合 + RenderFenced + MatchOutput）+ test | ~280 | go test | M6（store）+ M7（cache）|
| **M13** | W4c | biz/compliance/scope_validator.go（GORM Before-Query hook + SkipScope ctx + 7 表白名单 + fail-open）+ test | ~280 | go test -race | M6（compliance_scope 微包）+ M11（audit）|
| **M14** | W5 | biz/compliance/system_prompt_block.go（SystemPromptAssembler）+ test + biz/compliance/gate.go（complianceGate 4 方法 含 *AuditLogger 依赖）+ test | ~450 | go test -race | M5/M8/M9/M10/M11/M12 |
| **M15** | W5 | biz/agent/compliancegate/gate.go（WrapHooks 装饰器，沿用 #12 budgetgate 模式）+ test | ~200 | go test -race | M14（gate interface）|
| **M16** | W5 | runner.go step [2] 注入 + WithComplianceGate option + helper.go AutoMigrate + biz.go wire（含 hook chain compliance→permission→budget→sandbox + audit_logger.Start + biz.Shutdown 接 Stop）| ~120 | 全包 go test -race | M14 + M15 |

**估总 LOC**：~3,250 行（含 spec 中所有方法实现 + 80+ 单测）

---

## §2 Wave 调度（并行 Tier 协议）

### W1 — Migration + Model（2 task 并行，Tier 3 disjoint）

| Task | 文件归属 |
|---|---|
| **M1** | `numind-server/migrations/20260521_120000_agent_mode_compliance_3layer.sql`,`numind-server/migrations/20260521_120000_agent_mode_compliance_3layer_rollback.sql` |
| **M2** | `numind-server/internal/pkg/model/compliance_rule.go`,`numind-server/internal/pkg/model/compliance_rule_test.go`,`numind-server/internal/pkg/model/compliance_audit_log.go`,`numind-server/internal/pkg/model/compliance_audit_log_test.go` |

**ndf-check-disjoint 验证**（逗号分隔）：
```
M1: numind-server/migrations/20260521_120000_agent_mode_compliance_3layer.sql,numind-server/migrations/20260521_120000_agent_mode_compliance_3layer_rollback.sql
M2: numind-server/internal/pkg/model/compliance_rule.go,numind-server/internal/pkg/model/compliance_rule_test.go,numind-server/internal/pkg/model/compliance_audit_log.go,numind-server/internal/pkg/model/compliance_audit_log_test.go
```

零交集 → Tier 3 安全。

### W2 — errno + types + platform 串行 baseline（3 task 并行，但下游 W3 依赖）

| Task | 文件归属 |
|---|---|
| **M3** | `numind-server/internal/pkg/errno/compliance.go` |
| **M4** | `numind-server/internal/numind/biz/compliance/types.go`,`numind-server/internal/numind/biz/compliance/types_test.go`,`numind-server/internal/numind/biz/compliance/errno.go` |
| **M5** | `numind-server/internal/numind/biz/compliance/platform_rules.go`,`numind-server/internal/numind/biz/compliance/platform_rules_test.go` |

**注**：M4 包含 biz/compliance/errno.go（re-export），依赖 M3 (errno/compliance.go) 已 commit。M4/M5 与 M3 严格串行：先 M3 commit 后再启 M4+M5 并行。

**ndf-check-disjoint**：
```
M3: numind-server/internal/pkg/errno/compliance.go
M4: numind-server/internal/numind/biz/compliance/types.go,numind-server/internal/numind/biz/compliance/types_test.go,numind-server/internal/numind/biz/compliance/errno.go
M5: numind-server/internal/numind/biz/compliance/platform_rules.go,numind-server/internal/numind/biz/compliance/platform_rules_test.go
```

零交集（M4 errno.go 与 M3 errno/compliance.go 不同路径）→ Tier 3 安全。

### W3 — 核心组件并行 5 task（Tier 3 disjoint）

| Task | 文件归属 |
|---|---|
| **M6** | `numind-server/internal/numind/store/compliance.go`,`numind-server/internal/numind/store/compliance_test.go`,`numind-server/internal/numind/store/store.go` (Edit only: add Compliance() to IStore + datastore) |
| **M7** | `numind-server/internal/numind/biz/compliance/cache.go`,`numind-server/internal/numind/biz/compliance/cache_test.go` |
| **M8** | `numind-server/internal/numind/biz/compliance/skill_soft_rules.go`,`numind-server/internal/numind/biz/compliance/skill_soft_rules_test.go` |
| **M9** | `numind-server/internal/numind/biz/compliance/injection_detector.go`,`numind-server/internal/numind/biz/compliance/injection_detector_test.go` |
| **M10** | `numind-server/internal/numind/biz/compliance/fence_validator.go`,`numind-server/internal/numind/biz/compliance/fence_validator_test.go` |

**ndf-check-disjoint**：5 task 共 11 文件，store/ 与 biz/compliance/ 完全 disjoint → Tier 3 安全。M6 改 store.go 是 Edit（add Compliance() entry），不会与 M7-M10 冲突。

### W4 — Audit + Tenant + Scope（3 task 并行 Tier 3）

| Task | 文件归属 |
|---|---|
| **M11** | `numind-server/internal/numind/biz/compliance/audit_logger.go`,`numind-server/internal/numind/biz/compliance/audit_logger_test.go` |
| **M12** | `numind-server/internal/numind/biz/compliance/tenant_rules.go`,`numind-server/internal/numind/biz/compliance/tenant_rules_test.go` |
| **M13** | `numind-server/internal/numind/biz/compliance/scope_validator.go`,`numind-server/internal/numind/biz/compliance/scope_validator_test.go` |

**ndf-check-disjoint**：3 task 共 6 文件，全在 biz/compliance/ 不同文件 → Tier 3 安全。

### W5 — 装配 + 集成（3 task 串行）

> **必须串行**：M14 依赖 W4 全部完成；M15 依赖 M14 gate interface；M16 集成 wire 依赖 M14 + M15 都已编译。

| Task | 文件归属 |
|---|---|
| **M14** | `numind-server/internal/numind/biz/compliance/system_prompt_block.go`,`numind-server/internal/numind/biz/compliance/system_prompt_block_test.go`,`numind-server/internal/numind/biz/compliance/gate.go`,`numind-server/internal/numind/biz/compliance/gate_test.go` |
| **M15** | `numind-server/internal/numind/biz/agent/compliancegate/gate.go`,`numind-server/internal/numind/biz/agent/compliancegate/gate_test.go` |
| **M16** | `numind-server/internal/numind/biz/agent/runner.go` (Edit),`numind-server/internal/numind/biz/agent/runner_compliance_test.go` (新建),`numind-server/internal/numind/biz/biz.go` (Edit),`numind-server/internal/numind/helper.go` (Edit) |

**M15 / M16 不冲突**：M15 在新子包 compliancegate/，M16 改既有 runner.go / biz.go / helper.go。

---

## §3 每个 Task 详细 spec（implementer agent 直接读）

> S4 implementer 取 task 时**只读** S2 spec 对应 §章节 + 本 plan 对应 Mi。spec 是单一真实源；本 plan 是调度 + 验收。

### M1 — Migration SQL 双文件

**spec 引用**：S2 spec §1.1-§1.4

**文件清单（2 个）**：
1. `migrations/20260521_120000_agent_mode_compliance_3layer.sql` (UP)
2. `migrations/20260521_120000_agent_mode_compliance_3layer_rollback.sql` (DOWN)

**UP 含**（按 S2 §1.2 + §1.3 完整 SQL）：
- `CREATE TABLE IF NOT EXISTS compliance_rule (...)` 含 idx_parent_active_priority 复合索引
- `CREATE TABLE IF NOT EXISTS compliance_audit_log (...)` 含 idx_parent_created / idx_run / idx_layer_decision 三索引
- 文件头注释（feature + 日期）
- 末尾验证 SQL（`SHOW INDEX FROM compliance_rule` 等）

**DOWN 含**：
- `DROP TABLE IF EXISTS compliance_audit_log;`
- `DROP TABLE IF EXISTS compliance_rule;`

**验收**：
- 双文件 syntax check（MySQL 8 / SQLite 兼容）
- `git diff` 仅新增 2 SQL 文件，不动既有 migration

### M2 — GORM model + 单测

**spec 引用**：S2 spec §2.1-§2.3

**文件清单（4 个）**：
1. `internal/pkg/model/compliance_rule.go`
2. `internal/pkg/model/compliance_rule_test.go`
3. `internal/pkg/model/compliance_audit_log.go`
4. `internal/pkg/model/compliance_audit_log_test.go`

**model 内容**：S2 §2.1 / §2.2 完整 Go struct + TableName() + 常量定义

**测试要求**（每 model ≥ 3 测试）：
- TestComplianceRule_TableName / TestComplianceAuditLog_TableName
- TestComplianceRule_AutoMigrate / TestComplianceAuditLog_AutoMigrate（in-memory SQLite）
- **TestComplianceRule_CreateWithIsActiveFalse**（回归：default:true bool 坑；先 INSERT with IsActive=false → 验证 DB 行 is_active=0；不通过此测说明 store 层 fixup 必须）

**验收**：
- `go test ./internal/pkg/model/...` PASS
- 覆盖率 ≥ 80%（model 文件本身简单）

### M3 — errno 新文件

**spec 引用**：S2 spec §6

**文件清单（1 个）**：
1. `internal/pkg/errno/compliance.go`

**内容**：S2 §6 完整 6 个 errno 定义

**验收**：
- `go build ./internal/pkg/errno/...` PASS
- 与既有 errno.Errno pattern 一致（HTTP / Code / Message）

### M4 — biz/compliance/types.go + errno.go

**spec 引用**：S2 spec §4.1 + §4.2

**文件清单（3 个）**：
1. `internal/numind/biz/compliance/types.go`
2. `internal/numind/biz/compliance/types_test.go`
3. `internal/numind/biz/compliance/errno.go`

**types.go 内容**：
- `ToolInfo` struct
- `ComplianceResult` struct
- `ComplianceRequest` struct（Tool: ToolInfo，非 agent.FullTool）
- `ComplianceGate` interface
- `truncate(s string, max int) string` helper
- `DefaultOutOfScopeNarration` const

**errno.go 内容**：re-export 6 个 errno 变量（依赖 M3）

**验收**：
- `go build ./internal/numind/biz/compliance/...` PASS（仅 types + errno + platform 文件存在；其他文件未写）
- types_test.go 测试 enum 常量字符串值

### M5 — biz/compliance/platform_rules.go

**spec 引用**：S2 spec §4.3

**文件清单（2 个）**：
1. `internal/numind/biz/compliance/platform_rules.go`
2. `internal/numind/biz/compliance/platform_rules_test.go`

**内容**：S2 §4.3 完整 `PlatformHardRulesFenced` 常量（含 fence tag + 6 条规则）

**测试**：const 非空 + 含关键字段（"<platform_hard_rules>" / "1." / "6." / "</platform_hard_rules>"）

### M6 — store + IStore 扩展

**spec 引用**：S2 spec §3.1-§3.3

**文件清单（3 个）**：
1. `internal/numind/store/compliance.go`（interface + impl）
2. `internal/numind/store/compliance_test.go`
3. `internal/numind/store/store.go`（Edit: 加 `Compliance() IComplianceStore` 到 IStore + impl）

**关键细节**：
- `CreateRule` 包含 default:true bool 坑 UpdateColumn fixup（S2 §3.2 完整片段）
- `UpdateRule` 用 map form（避开 GORM struct zero-value 坑）
- `SoftDeleteRule` 用 UpdateColumn
- `ListRulesByParent` / `WriteAuditLog` 内部 ctx 包 `compliance.WithSkipScope(ctx, "compliance_self")` 跳过 scope hook（S2 §9.1 决策）— 这里**不**import biz/compliance 以避反向 import；改用 raw `context.WithValue(ctx, scopeCtxKey{}, "compliance_self")`（store 内复制 ctx key 定义，与 scope_validator.go 共享 key type）

> **import 解耦决策更新（S3 reviewer P1-3）**：S2 spec §9.1 原文写"S2 锁定方案 B（store 内复制 ctx key 定义）"，但实施评估后**S3 plan 升级到方案 A**——单独建 `internal/pkg/compliance_scope` 微包共享 key + WithSkipScope / SkipScopeFromCtx。
> **理由**：方案 B（复制 ctx key 定义）会让 store 和 biz/compliance 各持一份 struct{} key——Go 的 `context.WithValue` 用 `interface{}` 比对 key 时**要求同一 type 才能取出值**，复制 key 定义会形成两套 key，互不识别 → SkipScope 失效。方案 A 共享微包是唯一正确解。
> **M6 实施者动作**：
> 1. 先建微包 `internal/pkg/compliance_scope/scope.go`（约 30 LOC）：
>    ```go
>    package compliance_scope
>    type skipScopeCtxKey struct{}
>    func WithSkipScope(ctx context.Context, reason string) context.Context { ... }
>    func SkipScopeFromCtx(ctx context.Context) (string, bool) { ... }
>    ```
> 2. store/compliance.go 的 ListRulesByParent / WriteAuditLog 入口 import 此微包并包 ctx
> 3. M13 scope_validator.go 也从此微包 import（不在自己文件内重复定义）
> 4. **S2 spec §4.9 + §9.1 prose 在 S5 acceptance 文档更新声明**（不改 spec 文件本身，避免溯源混乱；plan 决策即权威）

**测试要求**（≥ 10 测试）：S2 §3.4 完整列表

**验收**：
- `go test -race ./internal/numind/store/...` PASS
- 覆盖率 ≥ 80%（compliance.go 本身）

### M7 — biz/compliance/cache.go

**spec 引用**：S2 spec §4.11

**文件清单（2 个）**：
1. `internal/numind/biz/compliance/cache.go`
2. `internal/numind/biz/compliance/cache_test.go`

**内容**：S2 §4.11 完整代码（含 evictionCnt atomic.Uint64 + EvictionCount() + 全程 sync.Mutex Lock）

**测试要求**（≥ 8 测试）：
- Get miss / Set / TTL 过期触发 evictionCnt += 1
- Invalidate / 重复 Invalidate（幂等）
- cap 满触发 evictLRU + evictionCnt += 1
- **100 并发 Get + 10 Invalidate race（race detector PASS）**
- Size() / EvictionCount() 准确

**验收**：
- `go test -race ./internal/numind/biz/compliance/...` PASS
- cache.go 覆盖率 ≥ 85%

### M8 — biz/compliance/skill_soft_rules.go

**spec 引用**：S2 spec §4.5

**文件清单（2 个）**：
1. `internal/numind/biz/compliance/skill_soft_rules.go`
2. `internal/numind/biz/compliance/skill_soft_rules_test.go`

**内容**：S2 §4.5 完整代码

**测试要求**（≥ 5 测试）：
- ExtractFromAgentDef nil ad → 零值
- 空 QuestionnaireAnswers → 零值
- 含 Q10/Q11 → 正常提取
- JSON 解析失败 → 零值（fail-soft）
- NarrationOrDefault Q11 空 → DefaultOutOfScopeNarration / 非空 → Q11

### M9 — biz/compliance/injection_detector.go

**spec 引用**：S2 spec §4.7

**文件清单（2 个）**：
1. `internal/numind/biz/compliance/injection_detector.go`
2. `internal/numind/biz/compliance/injection_detector_test.go`

**内容**：S2 §4.7 完整代码（含 18 关键词 + WrapInputFence + mockClassifier）

**测试要求**（≥ 10 测试）：
- 每个关键词命中各 1 测试（≥ 14 测试覆盖 14+ 关键词）
- 大小写不敏感（"IGNORE PREVIOUS" 命中 "ignore previous"）
- 中英混合命中
- mock classifier 永远 false
- classifier 报错 → fail-open (false, "", err)
- WrapInputFence 包装格式

### M10 — biz/compliance/fence_validator.go

**spec 引用**：S2 spec §4.8

**文件清单（2 个）**：
1. `internal/numind/biz/compliance/fence_validator.go`
2. `internal/numind/biz/compliance/fence_validator_test.go`

**内容**：S2 §4.8 完整代码（含 11 fence tag）

**测试要求**（≥ 12 测试）：
- 每个 fence tag 各 1 测试（≥ 11）
- 大小写不敏感
- 无命中 → 返回 (false, "")

### M11 — biz/compliance/audit_logger.go

**spec 引用**：S2 spec §4.10

**文件清单（2 个）**：
1. `internal/numind/biz/compliance/audit_logger.go`
2. `internal/numind/biz/compliance/audit_logger_test.go`

**内容**：S2 §4.10 完整代码（含 Start / Stop / Write / DropCount / consumer goroutine + flush-on-shutdown）

**测试要求**（≥ 6 测试）：
- New + Start → consumer 启动
- Write 入队 → consumer 消化
- 1000 并发 Write 不丢日志（或 drop count 准确）— **race detector PASS**
- 队满（cap=1000 全占）触发 drop count
- Stop(ctx) 正常 → drain 剩余 + close doneCh
- Stop(ctx) 超时（mock store 阻塞）→ 返回 timeout error
- DropCount 单调

### M12 — biz/compliance/tenant_rules.go

**spec 引用**：S2 spec §4.4

**文件清单（2 个）**：
1. `internal/numind/biz/compliance/tenant_rules.go`
2. `internal/numind/biz/compliance/tenant_rules_test.go`

**内容**：S2 §4.4 完整代码（TenantRuleProvider + GetActiveRules + RenderFenced + MatchOutput）

**测试要求**（≥ 8 测试）：
- GetActiveRules cache hit 不调 store
- GetActiveRules cache miss 调 store + Set cache
- store 错误 → 返回 error
- 排序：priority ASC 优先，同 priority created_at DESC
- RenderFenced 4 rule_type 各 1 测试
- RenderFenced 空 rules → ""
- MatchOutput 命中 forbid_brand / 命中 forbid_phrase
- MatchOutput 不命中 → nil

### M13 — biz/compliance/scope_validator.go

**spec 引用**：S2 spec §4.9

**文件清单（2 个）**：
1. `internal/numind/biz/compliance/scope_validator.go`
2. `internal/numind/biz/compliance/scope_validator_test.go`

**内容**：S2 §4.9 完整代码（含 **7 表**白名单 + WithSkipScope / SkipScopeFromCtx + Install + beforeQuery + hasScopeFilter）

> **白名单数量更正（S3 reviewer P2-1）**：S2 §4.9 代码中 `scopeWhitelistTables` map 实际有 **7 项**：`agent_run / agent_session / agent_session_memory / user_global_memory / agent_definition / compliance_rule / compliance_audit_log`。S2 散文之前误写"6 表"，以代码为准 = 7 表。

> **依赖 M6 已建立 internal/pkg/compliance_scope 微包** — scope_validator.go 从该微包 import key（而不是定义在 scope_validator.go 内）。M6 实施者负责建微包；M13 实施者读 M6 输出后引用。

**测试要求**（≥ 8 测试）：
- 表名不在白名单 → 不检查
- 表名在白名单 + ctx 含 SkipScope → 跳过 + 写 passthrough audit
- 表名在白名单 + SQL 含 parent_user_id → 不 warn
- 4 种 quoting variant（裸 / backtick / double-quote）各 1 测试
- 不含 filter → log warn + 写 deny audit + 不返回 error（v1 fail-open）
- 含 user_id filter → 视作 scope OK

### M14 — system_prompt_block.go + gate.go（**M14 不拆 2 个 task**，因为 gate.go 严重依赖 system_prompt_block.go）

**spec 引用**：S2 spec §4.6 + §4.12

**文件清单（4 个）**：
1. `internal/numind/biz/compliance/system_prompt_block.go`
2. `internal/numind/biz/compliance/system_prompt_block_test.go`
3. `internal/numind/biz/compliance/gate.go`
4. `internal/numind/biz/compliance/gate_test.go`

**system_prompt_block.go**：S2 §4.6 完整 `SystemPromptAssembler.Assemble`

**gate.go**：S2 §4.12 完整 4 方法实现（SystemPromptBlock / CheckUserInput / CheckLLMOutput / CheckToolCall）

**测试要求**：
- system_prompt_block_test.go ≥ 4 测试（Assemble L0+L1 / L1 fetch 失败 fail-open / ad 为 nil 仅 L0 / 多 rule 排序）
- gate_test.go ≥ 12 测试（每方法命中/未命中 + audit 写入路径）

### M15 — compliancegate 装饰器

**spec 引用**：S2 spec §5.1

**文件清单（2 个）**：
1. `internal/numind/biz/agent/compliancegate/gate.go`
2. `internal/numind/biz/agent/compliancegate/gate_test.go`

**内容**：S2 §5.1 完整代码（WrapHooks + buildRequest 含 ToolInfo 转换）

**S2 spec 笔误修正（S3 reviewer P2-3）**：S2 §5.1 代码片段中 PreToolCall handler 内：
- `ToolName: req.Tool.Name(),` ← 错（带括号当方法）
- `ToolName: req.Tool.Name,` ← 对（ToolInfo.Name 是 struct field）

同样 `req.Tool.Name()` 在另一行 sink full warn log 也是 field access。S4 实施者直接 copy spec 代码时**改括号为 field access**。

**测试要求**（≥ 6 测试）：
- WrapHooks nil gate → base 透传
- PreToolCall allow → 透传 base.PreToolCall
- PreToolCall deny → HookActionPermissionDeny + Registry.Record
- PreToolCall sink 满 → log warn 但正常返回
- PostToolCall 透传 base
- Registry / NarrationProvider / NarrationRunID 字段全透传

### M16 — runner.go + biz.go + helper.go 集成

**spec 引用**：S2 spec §7 + §8 + §9

**文件清单（4 个 Edit + 1 新建）**：
1. `internal/numind/biz/agent/runner.go` (Edit: 加 complianceGate 字段 + step [2] 注入 + WithComplianceGate option)
2. `internal/numind/biz/agent/runner_compliance_test.go` (新建: 测试 step [2] 注入与 fail-open)
3. `internal/numind/biz/biz.go` (Edit: 构造 TTLCache / TenantRuleProvider / SystemPromptAssembler / AuditLogger.Start / InjectionDetector / ComplianceGate；wrappedHooks chain 加 compliancegate.WrapHooks outermost；NewAgentRunner 加 WithComplianceGate；biz struct 加 complianceAuditLogger 字段；Shutdown 接 Stop)
4. `internal/numind/helper.go` (Edit: 在 #4 sandbox / #5 skill 注册块附近加 `db.AutoMigrate(&model.ComplianceRule{}, &model.ComplianceAuditLog{})`)

**集成验收**：
- 全包编译 `go build ./...` PASS
- `go test -race ./...` PASS
- runner.go step [2] 注释字符串改为 `// step [2] tenant_hard_rules (filled by #13 agent-mode-compliance-3layer compliance.SystemPromptBlock)`
- 其他 5 段位（PlatformBase / body / disclaimer + memory / tools / SafetyFooter）单字符不变

**定位指引（S3 reviewer P2-2）**：
- runner.go step [2] 修改入口 grep：`grep -n "PLACEHOLDER: tenant.hard_rules" internal/numind/biz/agent/runner.go`
- biz.go wire 修改入口 grep：`grep -n "budgetWrappedHooks := budgetGate.WrapHooks" internal/numind/biz/biz.go`（在下一行加 compliancegate.WrapHooks）
- helper.go AutoMigrate 入口 grep：`grep -n "AgentSandboxSession" internal/numind/helper.go`（紧邻后加 ComplianceRule + ComplianceAuditLog）

---

## §4 双 reviewer 协议（每 task 后并行 dispatch）

每 M1-M16 完成后**必须**并行 dispatch 2 sonnet reviewer：
1. **Spec Compliance**：核对 S2 spec 对应章节 1:1 落地
2. **Code Quality**：Go idiom / 测试覆盖 / race / error wrap

reviewer 输出结构化 `<severity>: <file>:<line> — <rule-id> — <problem> — fix: <suggestion>`

P0/P1 修复 → re-review 直到 PASS → 然后再 dispatch 下一 task implementer。

**reviewed_tasks 字段**：每 task 完整 review 后 manifest.progress.reviewed_tasks += 1。

---

## §5 S5 验证策略（NDF Rule 10 强制）

**验证方式**：Go TDD + race detector + in-memory SQLite + 攻击向量集成测试

**理由**：
- 本 feature 不涉及前端 UI（管理端 CRUD UI 在 #14）
- biz/compliance 是纯后端逻辑，单测 + race 已能覆盖 95% 风险面
- 集成测试覆盖 13 攻击向量（S2 §10）
- 不上 Playwright E2E（无 UI 可测）
- 不上 gstack /qa（同上）

**回归保护诚实声明**：
- 本 feature 的回归保护**完全依赖 Go 单测**（永久留在 codebase）
- 任何未来改动 biz/compliance 都会被 go test ./... 拦截

**关键用户路径验证清单**（S5 acceptance 必跑）：
1. AutoMigrate 启动 0 warn（dev 环境真实跑 helper.go autoMigrate）
2. 13 攻击向量集成测试（S2 §10 表格）全 PASS
3. 6 个 ComplianceGate 方法路径 trace（每条路径输出 ComplianceResult + audit row 验证）
4. Hook chain 实测：mock RunHooks 透传链 compliance → permission → budget → sandbox（chain 顺序断言）
5. Race detector：`go test -race ./internal/numind/biz/compliance/... ./internal/numind/biz/agent/compliancegate/...` PASS
6. 覆盖率：biz/compliance ≥ 80%

---

## §6 Task 原子性核对

每个 task 完成后必须：
- 独立 commit（Conventional Commits）
- 独立编译（`go build ./internal/numind/biz/compliance/...` 至当前进度可编译，未完成接口 stub）
- 独立运行测试（`go test ./internal/numind/biz/compliance/<file>_test.go` PASS）
- 不依赖同批次其他 task 未提交代码

**例外**：W5 集成 task M16 必须等 M14 + M15 commit 后才可启动；这是设计上的串行依赖，不违反原子性。

---

## §7 Bug-from-Customer 规则适用性（NDF Rule 11）

**本 feature 不是 bug 修复**：是 14-feature 分解的第 13 个 feature，新建合规框架。不适用 Rule 11（无客户上报 bug 复现测试）。

---

## §8 0 prod 红线（呼应 S0 / S1 / S2）

S4 实施期间**严格遵守**：
- ✗ 不动 config_prod.yaml
- ✗ 不 `/deploy-prod`
- ✗ 不 `git tag v*`
- ✗ feature/* 分支 pre-push hook 拦
- ✗ migration SQL 不在 dev/prod CI 自动跑（手工 SSH 上线）
- ✗ 不动 PROD_SSH_* / 不动 prod 服务器

---

**完成本 Plan 后**：标记 S3 done，进 S4 编码（按 Wave 1 → Wave 5 顺序）。
