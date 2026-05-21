# NDF S0 Requirement Card · `agent-mode-compliance-3layer`

**Track**：Standard
**Feature ID**：`agent-mode-compliance-3layer`（14-feature 分解 #13/14）
**起草日期**：2026-05-21
**起草人**：AI（autopilot）
**状态**：S0 草案
**依赖**：
- #2 `agent-mode-runtime-skeleton`（merged `45770bb5`）— LoopState / RunHooks 接口 / TerminalReason 枚举
- #4 `agent-mode-sandbox-integration`（merged `8c883533`）— SandboxHook + ctx 注入运行时
- #5 `agent-mode-skill-system`（merged `e05498b6`）— `agent_definition.questionnaire_answers` Q10/Q11 字段已落地（本 feature L2 源数据；不改 schema）+ `skill.PlatformBasePrompt` / `skill.PlatformSafetyFooter` 常量已就绪
- #6 `agent-mode-permission-pipeline`（merged `65e9d144`）— PermissionPipeline + 7 个 validator + WrapHooks 模式 + DecisionReason 11 种枚举；本 feature 在 hook chain 外层追加 compliance.WrapHooks
- #7 `agent-mode-memory-system`（merged `49c8ab67`）— memory.SystemBlock 段位 step [4]；本 feature 落地 step [2] `tenantHardRulesPlaceholder`（runner.go:275 已留 placeholder）
- #8 `agent-mode-narration-layer`（merged `124e62b4`）— narration provider 透传规约；本 feature compliance deny 走 narration error 通道
- #12 `agent-mode-billing-integration`（merged `bd988fd5`）— BudgetTracker / WrapHooks 透传顺序，本 feature 在最外层追加 compliance

**阻塞**：#14 `agent-mode-e2e-rollout`（端到端验收 + 真实 qwen-turbo L3 输出过滤接入 + 管理端 compliance_rule CRUD UI）

---

## 1. 起因（Why now）

Agent 模式 14-feature 分解的 **#13/14** —— Compliance 3-Layer 是 Agent 模式的"内容与隐私防线"。前 12 个 feature 完成失控保护（#12 BudgetTracker）、权限管控（#6 PermissionPipeline）、能力隔离（#4 Sandbox），但**没有内容级合规框架**：

1. **平台合规底线（L0）** — 政治 / 医疗 / 投资承诺 / PII 等绝对禁线，所有 Agent 共享，运营不可关。蓝本 §7.1 第 1 层。
2. **租户运营规则（L1）** — 父账户机构级规则（如金融机构禁讨论竞品、教育机构禁讨论政治话题）。蓝本 §7.1 第 2 层语义对应；蓝本通过 Q10/Q11 配置（个 skill 粒度），本 feature 升级为 **per-parent_user_id 可配置 DB 表**（运营粒度，比 skill 粒度更高）。
3. **Skill 软规则（L2）** — 配置者通过问卷 Q10/Q11 设置的"注意话题 / 越界话术"。蓝本 §7.1 第 3 层；#5 已在 `agent_definition.questionnaire_answers` 落地，本 feature **复用读取**，不重复存储。
4. **Prompt Injection 防护（蓝本 §7.3）** — 外部数据（用户输入 / RAG / 工具返回）注入对话上下文时，用 fence tag 隔离 + 启发式检测 + （v1 mock）LLM 分类器。
5. **Data 隔离强制（蓝本 §7.4）** — agent-mode 表所有查询必须含 `WHERE parent_user_id = ?` filter；runtime GORM Before-Query hook 拦截违规查询（v1 opt-in 到 agent-mode 表）。
6. **审计日志（蓝本 §7.5）** — 每次合规判定写 `compliance_audit_log`（async goroutine 不阻塞主流程）。
7. **成本控制（蓝本 §7.6）** — 规则 LRU 缓存 + per-parent rate limit；输出长度阈值（<200 字跳过 / 200-2000 字阻塞过滤 / >2000 字阻塞过滤）— v1 接口预留，实际 qwen-turbo 调用 mock。

**核心矛盾**：Agent 模式自主性比 SOP / Chatbot 强一个量级 — LLM 自主决定下一步 → 单次 run 30 步 → 没有 L0 兜底就是"合规事故 / 一次诉讼"；没有 L1 就是"每个机构必须自己审查所有 skill" → B2B 卖不动；没有 scope 拦截就是"A 学员能读到 B 学员数据 = GDPR 级红线"。

---

## 2. 业务范围

> **关键术语对齐**：
> - 用户 prompt 三层 L0/L1/L2 ↔ 蓝本 §7.1 三层（Platform Hard / Tenant Soft / Output Filter）的映射不是 1:1：用户 prompt 把蓝本 L2 升级为 DB 配置（L1），蓝本 L3 输出过滤拆分到 `CheckLLMOutput` 接口（v1 mock，#14 接 qwen-turbo）
> - 蓝本 `tenant_id` ↔ Numind `parent_user_id`（B2B2C 父账户）
> - `agent_definition.questionnaire_answers.q10` ↔ "注意话题"（caution topics），`q11` ↔ "越界话术"（out-of-scope response template）

### In scope

#### 2.1 DB 层（2 张新表 + 双 migration 文件）

**`compliance_rule` 表（L1 父账户运营规则）**

| 字段 | 类型 | 说明 |
|---|---|---|
| id | BIGINT UNSIGNED PK AI | 主键 |
| parent_user_id | INT UNSIGNED NOT NULL INDEX | 父账户 user.id |
| rule_type | VARCHAR(32) NOT NULL | 枚举：`forbid_topic` / `forbid_brand` / `forbid_phrase` / `custom` |
| rule_text | TEXT NOT NULL | 规则正文（注入 prompt 用；运营手写，v1 不做模板化）|
| priority | INT NOT NULL DEFAULT 100 | 排序权重（小在前；同 priority 按 created_at 倒序）|
| is_active | TINYINT(1) NOT NULL DEFAULT 1 | 开关（软删用，因 GORM default:true bool 坑见 `.claude/rules/database.md §6`，store 层用 UpdateColumn fixup）|
| created_at | DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP | |
| updated_at | DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP | |

**索引**：
- `idx_parent_active_priority (parent_user_id, is_active, priority)` — 覆盖索引，缓存 miss 时 query 走索引

**注释**：`COMMENT='Layer-1 父账户级合规规则（运营可配；通过管理端 CRUD，#14 落地 UI）'`

**`compliance_audit_log` 表（蓝本 §7.5 审计）**

| 字段 | 类型 | 说明 |
|---|---|---|
| id | BIGINT UNSIGNED PK AI | 主键 |
| agent_run_id | BIGINT UNSIGNED NULL INDEX | 关联 agent_run.id；session-prompt 装配时无 run_id 可空 |
| parent_user_id | INT UNSIGNED NOT NULL INDEX | 父账户 ID（聚合审计） |
| agent_definition_id | BIGINT UNSIGNED NULL | 关联 skill ID |
| rule_layer | VARCHAR(8) NOT NULL | 枚举：`L0` / `L1` / `L2` / `injection` / `fence` / `scope` |
| rule_id | BIGINT UNSIGNED NULL | L1 命中时引用 compliance_rule.id；L0/L2/injection/fence/scope 为空 |
| decision | VARCHAR(16) NOT NULL | 枚举：`allow` / `deny` / `sanitize` / `passthrough` |
| triggered_text | TEXT NULL | 触发判定的源文本片段（≤500 字符截断；不存全文，避免审计表膨胀）|
| reason | VARCHAR(255) NULL | 人类可读理由 |
| created_at | DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP | |

**索引**：
- `idx_parent_created (parent_user_id, created_at)` — 按机构分时段查
- `idx_run (agent_run_id)` — 按 run trace 查
- `idx_layer_decision (rule_layer, decision)` — 按判定类型聚合（运营看 deny 率）

**注释**：`COMMENT='Layer-0/1/2 + injection/fence/scope 合规判定异步审计日志'`

> **rule_id 不加 FK 决策**：`compliance_audit_log.rule_id` 引用 `compliance_rule.id` 但**不**加 FK constraint。理由：审计行必须在 compliance_rule 软删（is_active=0）或硬删（运营手工 DELETE）后**继续可读**，否则违反审计"事件历史不可篡改"原则。S2 spec 中 schema 注释明确写"intentional no-FK; audit row survives source rule deletion"。

> **保留期决策**：蓝本 §7.5 说 90 天热数据 + 1 年冷归档。本 feature **仅落地表 schema**，不实施归档/清理。归档由 #14 cron daemon 独立 task。

> **Migration 文件名格式**（per `.claude/rules/database.md §1`）：`YYYYMMDD_HHMMSS_*.sql` + `_rollback.sql` 双文件。

#### 2.2 biz/compliance 子包（新建）

```
internal/numind/biz/compliance/
├── types.go                # ComplianceRequest / ComplianceResult / RuleLayer / Decision 枚举
├── errno.go                # 域错误（ErrComplianceDeny 等）
├── platform_rules.go       # L0 常量 + 系统 prompt fence tag 包装
├── tenant_rules.go         # L1 — 读 compliance_rule 表 + 缓存包装
├── skill_soft_rules.go     # L2 — 从 agent_definition.questionnaire_answers Q10/Q11 提取
├── system_prompt_block.go  # 装配 L0+L1+L2 段落填 runner.go:275 tenantHardRulesPlaceholder
├── injection_detector.go   # input fence 包装 + 关键词启发式 + mock LLM classifier
├── fence_validator.go      # output fence tag 检测（防 LLM 输出 `<system>` 等）
├── scope_validator.go      # GORM Before-Query hook，agent-mode 表强制 parent_user_id filter
├── audit_logger.go         # async goroutine 写 compliance_audit_log
├── cache.go                # 规则 LRU + TTL 5min（per parent_user_id）
├── gate.go                 # ComplianceGate interface + 三层组合实现
└── *_test.go               # 单测含 race detector
```

**`biz/agent/compliancegate/` 子包**（沿用 #12 budgetgate 解耦模式 — S2 spec 锁定）：

```
internal/numind/biz/agent/compliancegate/
├── wrap_hooks.go           # PreToolCall hook 装饰器（compliance → permission → sandbox）
└── wrap_hooks_test.go      # 单测
```

> **解耦理由**（沿用 #12 `biz/agent/budgetgate/` 模式）：`compliance.WrapHooks` 需要 import `biz/agent`（拿 `agent.RunHooks` 类型 + ctx helper 如 `agent.RunIDFromContext`），而 `biz/agent.NewAgentRunner` 又要 import `compliance` 拿 `ComplianceGate` interface → **import cycle**。解决：`compliance.ComplianceGate` interface + 实现留在 `biz/compliance/`；**装饰器**单独抽到 `biz/agent/compliancegate/` 子包（导入两边但不被两边导入）。biz.go wire 时调 `compliancegate.WrapHooks(base, gate)`。

#### 2.3 model + store 扩展（既有改动）

- `internal/pkg/model/`：新增 `compliance_rule.go` + `compliance_audit_log.go` GORM model
- `internal/numind/store/`：新增 `IComplianceStore` interface（ListRulesByParent / GetRule / CreateRule / UpdateRule / SoftDeleteRule / WriteAuditLog）+ Tx 变体
- `internal/numind/store/store.go`：扩展 `IStore` 加 `Compliance() IComplianceStore`
- `internal/numind/helper.go` AutoMigrate：新增 `&model.ComplianceRule{}` + `&model.ComplianceAuditLog{}`

#### 2.4 biz/agent 集成（既有包改动）

- **`runner.go:275-299`** step [2] tenantHardRulesPlaceholder 赋值区：
  ```go
  if r.complianceGate != nil && ad != nil {
      block, err := r.complianceGate.SystemPromptBlock(ctx, ad)
      if err != nil { log.Warnw(...); /* fail-open */ }
      else { tenantHardRulesPlaceholder = block }
  }
  ```
  其他 5 段位不动；merge conflict 风险与 #7/#8/#12 已隔离（line 275 单独行）。
  **附带 line 275 注释更新**：现有注释 `// PLACEHOLDER: tenant.hard_rules (#6 will fill)` 是 #6 起草时的过期注释；S4 落地时改为 `// step [2] tenant_hard_rules (filled by #13 agent-mode-compliance-3layer compliance.SystemPromptBlock)`。
- **`runner.go` PreToolCall hook 链**：`biz.go` wire 阶段在 permission.WrapHooks 外再包 compliance.WrapHooks，运行时顺序变为 `compliance → permission → sandbox`
- **`biz.go`** wire：新增 `WithComplianceGate(g ComplianceGate) RunnerOption`；NewAgentRunner 接入；biz.Init wire link compliance.WrapHooks 在 permission.WrapHooks 之外
- **`agent_def_ctx.go`** 已有 `WithAgentDefCtx(ctx, agentDefID, parentUserID)`（#6 落地），compliance hooks 直接消费

#### 2.5 errno 扩展

`internal/pkg/errno/compliance.go` 新文件：
- `ErrComplianceL0Violation`（HTTP 422，code 4040001）— L0 平台规则命中（拒绝 + 标准化回应）
- `ErrComplianceL1Violation`（HTTP 422，code 4040002）— L1 租户规则命中
- `ErrComplianceInjectionDetected`（HTTP 422，code 4040003）— 输入侧 prompt injection 命中
- `ErrComplianceFenceViolation`（HTTP 422，code 4040004）— LLM 输出含禁用 fence tag
- `ErrComplianceScopeViolation`（HTTP 500，code 5000005）— scope_validator runtime 拦截到跨 parent 查询（系统级 bug）
- `ErrComplianceRuleNotFound`（HTTP 404，code 4040006）— compliance_rule CRUD 用

#### 2.6 ComplianceGate interface（最终签名以 S2 spec 为准）

```go
type ComplianceGate interface {
    SystemPromptBlock(ctx context.Context, ad *model.AgentDefinition) (string, error)
    CheckUserInput(ctx context.Context, parentUserID uint, input string) (ComplianceResult, error)
    CheckLLMOutput(ctx context.Context, parentUserID uint, output string) (ComplianceResult, error)
    CheckToolCall(ctx context.Context, req ComplianceRequest) (ComplianceResult, error)
}
```

四个方法对应：
- `SystemPromptBlock`：装配 L0+L1+L2 三段文本，runner.Run 进 LLM loop 前调一次
- `CheckUserInput`：fence 用户输入 + 启发式检测 + mock LLM 分类器；命中 → Deny
- `CheckLLMOutput`：fence tag 检测 + （v1 mock）qwen-turbo 输出过滤；#14 集成真实调用
- `CheckToolCall`：PreToolCall hook 调用；v1 主要是 L1 forbid_brand / forbid_phrase 在工具参数中检测

#### 2.7 Scope Validator 范围（蓝本 §7.4 落地策略）

**v1 opt-in 到 agent-mode 表**（白名单）：
- `agent_run` / `agent_session` / `agent_memory*` / `agent_definition` / `compliance_rule` / `compliance_audit_log`

**实现**：GORM `db.Callback().Query().Before("gorm:query")` hook 注入，检查 query SQL 是否含 `WHERE parent_user_id` 或 `WHERE user_id`（user_id 等价于自己访问自己数据）；缺失则：
- v1：log warn + 不阻断（让团队先观察 false positive；防误伤系统级 cron / migration job）
- v2（#14）：升级为阻断 + 触发 `ErrComplianceScopeViolation`

**已知合法跨 parent 查询白名单（S2 spec 落地为 SkipScopeCtxKey）**：

| 查询路径 | 表 | 说明 | 跳过策略 |
|---|---|---|---|
| AutoMigrate（启动） | 所有 | helper.go 启动时 schema migrate | ctx 携带 `compliance.WithSkipScope(ctx, "automigrate")` |
| Admin API `GET /v1/admin/skills` | agent_definition | 管理端跨父账户列表（admin_router） | ctx `WithSkipScope(ctx, "admin_list")` |
| Admin API `GET /v1/admin/agent-runs` | agent_run | 管理端运维查询（#14 落地，本 feature 出协议） | 同上 |
| Cron `archive_compliance_audit_log` | compliance_audit_log | 90 天归档（#14 daemon） | ctx `WithSkipScope(ctx, "archive_cron")` |
| compliance.tenant_rules ListRulesByParent 自身 | compliance_rule | scope_validator 不能审查自己（递归 LOOP） | ctx `WithSkipScope(ctx, "compliance_self")` |
| compliance.audit_logger Write 自身 | compliance_audit_log | 同上 | 同上 |

**实现机制**：`compliance.WithSkipScope(ctx, reason string)` 把 reason 注入 ctx；scope_validator GORM hook 在 query 前检查 `compliance.SkipScopeFromCtx(ctx)`，非空则跳过 + 写一条 `compliance_audit_log.decision='passthrough'` 行（reason 记 SkipScope reason 便于审计）。

> **理由**：runtime GORM hook 适用面广，可能误伤合法系统查询。v1 走"日志先行 + 白名单 opt-in"路径，避免硬阻断引发回归。预先枚举已知合法跨 parent 查询能让 v1 log 信噪比可用（否则启动后 log 被 AutoMigrate / admin list 淹没，监控失效）。

### Out of scope

- **prod 部署**（不部 prod，#14 / 用户决定）
- **真实 qwen-turbo L3 输出过滤**（v1 mock，#14 接 aiservice.Chat 同步分类）
- **管理端 compliance_rule CRUD UI**（#14 或独立 micro 落地；本 feature 仅出表 schema + store + biz + errno）
- **管理端审计日志查询接口**（#14；本 feature 仅写入侧）
- **学员侧合规提示 UI**（#11 已部分覆盖；#14 完善）
- **SQL-AST 静态分析**（v2）— v1 走 GORM Before-Query runtime hook + 白名单
- **跨机构审计聚合报表**（v2）
- **真实 LLM injection 分类器**（v1 启发式 + mock；#14 集成）
- **审计日志 90 天热 / 1 年冷归档 cron**（#14 独立 daemon task）
- **23 个 Bash validator 扩展**（与 #6 一致，留 backlog）

---

## 3. Triage（Standard 5 条）

1. **DB schema 变更**：是（2 张新表 `compliance_rule` + `compliance_audit_log`）✓
2. **新 API 端点**：否（本 feature 只出 biz 层 + GORM Before-Query hook；管理端 CRUD 接口 #14 落地）
3. **新外部服务集成**：否（v1 LLM 分类器 mock；#14 接 aiservice.Chat）
4. **影响文件数**：>3（migration ×2 + 2 model + 1 store interface + store extension + 13 biz/compliance 文件 + 等量 *_test + errno 新文件 + runner.go + biz.go + helper.go = ≥35）✓
5. **高风险业务逻辑**：是（GORM Before-Query hook 全局生效；规则注入影响所有 Agent 输出；audit log 高写入吞吐）✓

**触发条件**：1 + 4 + 5 多条 → **Standard track**。

---

## 4. 业务目标 / 成功标准

1. **L0 兜底**：6 条平台硬规则（政治 / 医疗 / 投资 / PII / 真实人物伪装 / 引导回归）通过 fence tag 进 system prompt step [2]，所有 Agent 共享，运营不可关
2. **L1 可配置**：父账户在 compliance_rule 表配置规则 → 5 分钟内（缓存 TTL）生效，注入对应 Agent 的 system prompt
3. **L2 复用**：从 #5 已有 `agent_definition.questionnaire_answers.q10 / q11` 读取（不引入新存储字段），合规层视作软规则源数据
4. **System prompt 6 段顺序不破**：填 step [2] tenantHardRulesPlaceholder；其他 5 段（PlatformBase / body / disclaimer / memory / tools / SafetyFooter）单字符不动
5. **PreToolCall 链外层接入**：compliance.WrapHooks 在 permission.WrapHooks 之外；compliance deny → 短路返回 + Registry record + narration error
6. **Prompt injection 防线**：输入 fence + 关键词启发式（v1 list 10 项以上：`ignore previous` / `pretend you are` / `system:` 等）；mock LLM classifier 接口预留
7. **输出 fence 检测**：LLM 输出含 `<system>` / `<memory>` / `<compliance>` 等禁用 fence tag → Deny
8. **Scope 隔离 v1**：agent-mode 6 张表的 query 缺 parent_user_id / user_id filter → log warn（不阻断）
9. **审计完整**：每次 L0/L1/L2/injection/fence/scope 判定都写 compliance_audit_log，async 不阻塞
10. **0 prod 影响**：config_prod.yaml zero diff / 不打 git tag / 不 `/deploy-prod` / pre-push 拦 / migration 不在 CI 自动跑 / 不动 prod 环境变量与 SSH
11. **覆盖率**：biz/compliance ≥ 80%；biz/agent / biz/permission 不下降
12. **race detector**：`go test -race ./...` 全 PASS（async audit logger + LRU cache 是 race 重点）

---

## 5. 优先级与节奏

**优先级**：P0（蓝本 §7 安全与合规是 Agent 模式上线的硬底线；先有框架才能在 #14 接真实 LLM 分类器）

**节奏（Standard track，估时 W13）**：
- S0 requirement card：本文件
- S1 proposal + PRD：技术方案 + 与 #6 permission 的协同 + B2B 父账户 / 平台运营双视角 PRD
- S2 spec：详细 DB DDL + ComplianceGate 接口签名 + L0 6 条硬规则常量定稿 + injection 关键词清单 + scope hook 实现策略
- S3 plan：原子 task 拆分（M1-Mn）+ Wave 调度 + S5 验证策略 + Tier 3 disjoint 文件归属
- S4 编码：实施 + per-task 双 reviewer + P0/P1 全修
- S5 acceptance：覆盖率 / race detector / 0 prod 检查 + S2 验证用例（attack vectors per 蓝本 §7 测试用例）
- S6 ndf-done：merge develop + 清 worktree + deploy-checklist + manifest stage=completed

---

## 6. 备注

**与 #6 permission 的协同方案（最重要决策）**：

#6 已落地 7 个 validator + DecisionReason 11 种 + WrapHooks 装饰器模式。**本 feature 不重复发明权限语义**，而是在 hook chain 外层追加一层独立 `compliance.WrapHooks`：

```
runner.Run hook chain（biz.go wire 阶段构造）：
  sandbox.AsRunHooks()
    → permission.WrapHooks(base, permGate)        # 既有 #6
    → compliance.WrapHooks(base, complianceGate)   # 本 feature 加在最外层
```

运行时 PreToolCall 顺序：
```
compliance.Check (deny 短路) → permission.Check (deny 短路) → base hook (sandbox 启动容器 / 工具实际调用)
```

理由：
- compliance 在最外 = 内容级合规优先于权限判断；如果用户输入触发 L0 政治禁线，连 permission 都不该走（避免暴露内部权限决策）
- permission 在中 = 工具权限判断不受 compliance 内部状态影响
- base 在最内 = sandbox 启动容器 + 工具实际调用

**复用 #6 已有 narration 透传补丁**（permission.WrapHooks line 96-110）：compliance.WrapHooks 同样从 base.NarrationProvider / NarrationRunID 透传到 wrapper，避免 narration emit 链断裂。

**Q10/Q11 双轨问题（L2 落地决策）**：

#5 skill_builder.go 已把 Q10/Q11 注入 `body`（skill 自然语言形式）。本 feature L2 处理：
- **v1**：compliance 层 **读** Q10/Q11 作为 `CheckLLMOutput` 的源数据（让 LLM 输出过滤知道"这个 skill 禁讨论的话题"），**不**在 system prompt 段位重复注入硬规则形式
- **理由**：避免双轨 — skill body 已有软语言表达，compliance 段位再加硬规则形式会冗余 + token 浪费 + 可能让 LLM 困惑
- **v2（#14）**：如发现 LLM 仍违反 Q10/Q11，再考虑硬规则形式（届时 strip skill body 中 Q10/Q11 自然语言段，仅保留 compliance 硬规则段）

**Scope Validator v1 fail-open 决策**：

GORM Before-Query hook 全局生效，可能误伤合法系统查询（admin SDK / cron job / migration）。v1 走 **log warn + 不阻断**：
- 先观察 30 天，运营看 log 监控 false positive
- v2（#14）升级为硬阻断 + ErrComplianceScopeViolation

**Audit Logger 异步性**：

`audit_logger.go` 用 goroutine + buffered channel（cap=1000）写 compliance_audit_log：
- 非阻塞 send（channel 满则丢日志 + log warn）
- 数据安全 > 性能：channel 满意味着流量异常，warn + 监控告警 > 阻塞 LLM 调用
- compliance 路径主流程**永不阻塞**于 audit 写入

**0 prod 红线**（6 条 — 与 #12 卡片一致）：
- config_prod.yaml 不动
- 不 `/deploy-prod`
- 不 `git tag v*`
- feature/* 分支 pre-push hook 拦
- migration SQL 文件不在 dev/prod CI 自动跑（per `project_dev_deploy_migration_gap`），上线前用户手 SSH 跑
- 不动 prod 环境变量与服务器 SSH（PROD_SSH_* 凭据不调用）

---

**完成本 Card 后**：标记 S0 done，进 S1。
