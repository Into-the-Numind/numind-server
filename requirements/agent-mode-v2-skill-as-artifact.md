# NDF S0 Requirement Card · `agent-mode-v2-skill-as-artifact`

**Track**：Standard
**Feature ID**：`agent-mode-v2-skill-as-artifact`（v2 第 1 个 feature，**v2 三件套之首**）
**起草日期**：2026-05-24
**起草人**：AI（与父账户对话拍板）
**状态**：S0 草案
**v1 依赖**：14-feature 全部 merge（含 #5 agent-mode-skill-system `e05498b6`、#10 configurator-ux `fdebd7b`、#14 e2e-rollout `e87b090e`/`78128f3`/`a15f134`）+ v1.5 三 feature 全 merge（multimodal `591d0f32`、context-v2、memory-layer-a `b0fa0cf2`）
**阻塞**：`agent-mode-v2-skill-invocation`（v2 #2）、`agent-mode-v2-skill-marketplace`（v2 #3）

---

## 1. 起因（Why now）

### v1 现状

v1 #5 `agent-mode-skill-system`（已 merge）把 Skill 实装为 `agent_definition` 表的**嵌入字段**：

| 字段 | 类型 | 角色 |
|---|---|---|
| `generated_skill_body` | TEXT | 问卷自动生成的 SKILL.md |
| `custom_skill_body` | TEXT | 高级模式用户编辑的 SKILL.md |
| `tool_flags` | JSON | 该 Agent 的工具白名单 |
| `questionnaire_answers` | JSON | 问卷原始答案 |

**v1 设计决策（蓝本 §决策#11）**：单 Skill / 1:1 绑定 Agent。Skill 不是独立概念，是 Command 的受约束子集。这是 **v1 工程权宜**——为了让非技术配置者心智简单（"一个 Agent 做一件事"），不引入路由层。

### 矛盾

v1 设计在 12 周节奏内是对的，但**长期阻塞三个能力**：

1. **能力复用断**：父账户 A 配置了一个"销售训练"角色，里面调研竞品的能力很强；父账户 B 想做"市场分析"角色，调研能力一模一样——但只能 copy-paste 整个 Skill body，无法引用
2. **Skill 演化断**：Anthropic 出新的 prompt 技巧、Numind 平台层加新工具能力——所有 v1 Agent 必须重新走问卷，无法增量升级
3. **Marketplace 死锁**：跨机构发布 Skill 必须先解耦 Skill 与 Agent；不然 marketplace 卖的是"完整 Agent 配置"，订阅方还得重新填问卷拼装

### 父账户终态描述（2026-05-24 对话拍板）

> "agent 是 agent，agent 有自己的提示词约束和其它 harness 约束，而 skills 是可以被单独管理、编写的技能型文件，可以被 agent 调用"

**=Claude Code 风格的 Skill 模型**：
- Skill = markdown + frontmatter 的独立资产（`name` / `description` / `when_to_use` / `allowed_tools` / body）
- Agent = harness（system prompt + 已装载 Skill 列表 + budget + memory + 工具白名单）
- Agent 通过 LLM tool-call `use_skill(name)` **按需调用** Skill；不是 1:1 绑定，不是路由层预决策

### v2 三件套总览

| # | Feature | 范围 | 本 feature 关系 |
|---|---|---|---|
| 1 | **agent-mode-v2-skill-as-artifact** | DB 解耦 + 独立 CRUD + UI 菜单 + 数据迁移 | **本 feature** |
| 2 | `agent-mode-v2-skill-invocation` | 运行时 use_skill tool + system prompt 注入 + 子工具白名单扩展 + narration | 依赖本 feature 完成 |
| 3 | `agent-mode-v2-skill-marketplace` | 跨租户脱敏发布订阅 + 运营推荐 | 依赖 #1 + #2 |

**本 feature 唯一职责**：把 Skill 从 `agent_definition` 字段升级为独立表 + CRUD + UI，**不动运行时**（v1 Agent 仍按原方式跑），保留 backward compat 路径直到 v2 #2 接管运行时。

---

## 2. 业务范围

### 关键术语统一

| 术语 | v1 含义 | v2 含义（本 feature 起）|
|---|---|---|
| **Skill** | `agent_definition.generated_skill_body` 字段值 | **独立表 `skill` 的一行**，含 frontmatter + markdown body |
| **Agent** | 装载 Skill 的运行实体 | **harness 实体**（保留 `agent_definition` 表，但 skill 字段标 deprecated）|
| **装载关系** | 1:1 嵌入字段 | 多对多 `agent_skill_binding` 表 |
| **租户隔离** | `parent_user_id` 指 Agent 所属父账户 | 同上，Skill 也按 `parent_user_id` 隔离 |

### In scope（本 feature 必交付）

#### 2.1 DB 层

新增 3 张表 + 1 张表 deprecated（不删字段，标注废弃）：

| 表 | 用途 | 关键字段 |
|---|---|---|
| `skill` | Skill 资产主表 | id / parent_user_id / name / description / when_to_use / allowed_tools(JSON) / body_md / source_template_id / version / is_active / source_type(generated\|custom\|imported) / created_by / created_at / updated_at |
| `skill_history` | append-only 版本快照 | id / skill_id / version / snapshot(JSON) / created_at |
| `agent_skill_binding` | Agent 装载 Skill 多对多 | id / agent_id / skill_id / sort_order / bound_at |
| `agent_definition` | v1 表 deprecated 字段标记 | generated_skill_body / custom_skill_body / tool_flags 加注释"deprecated since v2, use agent_skill_binding"，不删 |

迁移策略：双文件 migration（forward + rollback）+ AutoMigrate 注册到 `internal/numind/helper.go`。

#### 2.2 数据迁移（backward compat 核心）

每个现有 `agent_definition` 行 → 派生 **1 个 Skill 行 + 1 个 binding 行**：

- Skill body：取 `custom_skill_body` 优先，否则 `generated_skill_body`
- Skill name：`agent_definition.name + " 的默认技能"`
- Skill description：`agent_definition.description`
- Skill allowed_tools：`agent_definition.tool_flags`
- Skill source_type：`generated`（除非有 `custom_skill_body`，则 `custom`）
- Binding：`agent_id = agent_definition.id`, `skill_id = 新 skill.id`, `sort_order=0`
- Migration SQL 双向：forward 派生 + binding 写入；rollback 删 binding + 删 skill（不动 agent_definition 字段）

**关键约束**：迁移完成后，v1 Agent 仍能用 `generated_skill_body` 字段跑（runtime 还没改，是 v2 #2 的事）。binding 表只是新增信息，**不影响 v1 运行时行为**。

#### 2.3 biz/skill 子包（**新建**，不复用 v1 biz/agent/skill_builder）

- `skill/service.go`：业务编排（CRUD / 版本管理 / 列表过滤）
- `skill/binding.go`：装载/卸载 Skill 到 Agent
- `skill/migration.go`：把 v1 AgentDefinition 派生为 Skill 的迁移函数（一次性脚本，写入 migration SQL）
- `skill/frontmatter.go`：parse + serialize markdown + YAML frontmatter
- `skill/versioning.go`：每次保存触发 +1 写 history

v1 的 `biz/agent/skill_builder.go` **保留不动**（问卷→ markdown 组装逻辑还要给前端用），但其输出现在写入 `skill` 表而不是 `agent_definition.generated_skill_body`。

#### 2.4 API 端点

**用户端 `/v1/skills/*`（新增 8 端点）**：

| Method | Path | 说明 |
|---|---|---|
| POST | `/v1/skills` | 创建（直接 markdown，或 source_template_id 派生）|
| GET | `/v1/skills` | 列表（仅父账户；过滤 `parent_user_id=jwt.userID`）|
| GET | `/v1/skills/:id` | 详情 |
| PUT | `/v1/skills/:id` | 更新（version +1，写 history）|
| DELETE | `/v1/skills/:id` | 软删除（is_active=0，binding 自动失效）|
| GET | `/v1/skills/:id/history` | 历史版本列表 |
| POST | `/v1/skills/:id/restore/:version` | 回滚到指定版本（创建新版本，不删旧）|
| GET | `/v1/skills/:id/agents` | 列出装载了该 Skill 的所有 Agent |

**用户端 `/v1/agents/:id/skills/*`（新增 3 端点）**：

| Method | Path | 说明 |
|---|---|---|
| POST | `/v1/agents/:id/skills` | 装载 Skill 到 Agent（body: `{skill_id, sort_order?}`）|
| DELETE | `/v1/agents/:id/skills/:skill_id` | 卸载 |
| PUT | `/v1/agents/:id/skills/reorder` | 调整顺序（body: `{skill_ids[]}`）|

**所有端点**：父账户 JWT 鉴权，子账户调用返回 403。

#### 2.5 前端（numind-web-v3）

新增独立菜单 **`/config/skills/*`**：

| 路由 | 页面 | 说明 |
|---|---|---|
| `/config/skills` | `SkillList.vue` | 我的 Skill 列表（卡片或表格，按 hardrule §3 用表格 DataTable）|
| `/config/skills/new` | `SkillEditor.vue`（创建模式）| Markdown 编辑器 + frontmatter 表单（双向同步）|
| `/config/skills/:id` | `SkillDetail.vue` | 详情查看 + 版本列表 |
| `/config/skills/:id/edit` | `SkillEditor.vue`（编辑模式）| 同上 |
| `/config/skills/:id/history` | `SkillHistory.vue` | 历史版本对比 + 回滚 |

`/config/agents/:id/edit` 页面**新增 "已装载 Skill" 区块**：
- 显示当前 binding 列表（拖拽排序）
- "添加 Skill" 按钮 → 弹出本租户 Skill 选择器
- "移除" 按钮 → 卸载 binding

API 层 `src/api/skill.ts` 新建，遵循 [frontend-state.md](.claude/rules/frontend-state.md)。
Pinia store `src/stores/skill.ts` 新建。

### Out of scope（**明确不做**）

1. ❌ **运行时改造**：不动 `biz/agent/runner.go`/`adapter.go`，Agent 仍按 v1 方式从 `agent_definition.generated_skill_body`/`custom_skill_body` 拼装 system prompt。Skill 装载关系 binding 表写入但**runtime 暂不读**——v2 #2 接手
2. ❌ **`use_skill` tool**：v2 #2 的事
3. ❌ **跨租户共享 / Marketplace**：v2 #3 的事
4. ❌ **Skill 内嵌 scripts/代码**：Claude Code skill 支持 `scripts/` 子目录，v2 #1 只做 body markdown，scripts 留 v2.5 评估
5. ❌ **Skill 子工具白名单临时扩展**：Skill 的 allowed_tools 字段先存住，但 runtime 调用时合并到 Agent 工具白名单是 v2 #2 的事
6. ❌ **意图分类器 / 路由层**：终态不需要（LLM 自主 tool-call 调 Skill），v1 蓝本里的"Multi-Skill 路由"被本方案吸收消失
7. ❌ **prod 部署**：本 feature 收尾在 dev，prod 等用户拍板（按 agent-mode autopilot 规则）
8. ❌ **管理端 admin-web 改动**：v1 SkillTemplate 表平台预置模板，admin 已有管理界面（如有），本 feature 不动

---

## 3. 业务目标 / 验收标准

### 业务目标

让父账户能像管理"文件"一样管理 Skill：单独创建、编辑、版本回滚、装载到 Agent；为 v2 #2（运行时调用）和 v2 #3（marketplace）解锁前置。

### 关键验收标准

| # | 标准 | 验证方式 |
|---|---|---|
| AC-1 | 父账户能在 `/config/skills` 创建一个新 Skill（markdown 编辑器 + frontmatter 表单）并保存 | Playwright E2E |
| AC-2 | 创建的 Skill 自动写入 v1 history 表（version=1），后续编辑 +1 | Go unit test + DB 验证 |
| AC-3 | 父账户能装载一个 Skill 到现有 Agent，binding 表写入 | Playwright E2E |
| AC-4 | 数据迁移完成后，**所有 v1 AgentDefinition 都有 1 个对应 Skill + 1 个 binding**，数量校验 SQL pass | migration SQL 内置 assert |
| AC-5 | 数据迁移后，**v1 Agent 仍能正常对话**（runtime 不变）——回归测试 agent-student.spec.ts 全通过 | Playwright E2E |
| AC-6 | 子账户调用 `/v1/skills/*` 任意端点返回 403 | Go unit test |
| AC-7 | Skill body markdown + frontmatter 双向解析无损（任何合法 markdown 都能解析回结构化字段，反之亦然）| Go unit test 含 fuzz |
| AC-8 | 版本回滚后 Agent 行为符合回滚版本的 Skill 内容 | Playwright E2E |
| AC-9 | DELETE Skill 后所有 binding 失效（is_active 级联）；DELETE 一个被装载的 Skill 时给出二次确认 | Playwright E2E + Go unit test |

### 非功能性

- 迁移脚本要可重入（idempotent）：跑两次不重复派生
- frontmatter 解析失败时不阻塞 Skill 保存，但前端显示警告并保留 raw body
- 现有 e2e 测试零回归（runtime 没动，应当全 pass）

---

## 4. Triage

- **推荐轨道**：**Standard**
- **分类理由**：
  1. 数据库 schema 变更：**是**（3 张新表 + 双向 migration + 数据迁移）
  2. 新增 API 端点：**是**（11 个新端点：/v1/skills/* 8 + /v1/agents/:id/skills/* 3）
  3. 新外部服务集成：**否**（纯内部 DB + 已有 markdown 库即可）
  4. 影响文件数：**>3**（biz/skill/ 5 文件 + controller + router + model + 2 migration + 前端 5 view + store + api + 编辑器组件 + 单测 + e2e）
  5. 高风险业务逻辑：**是**（数据迁移影响所有现有 Agent，回滚错误 = 全平台 Agent 失效；版本管理错误 = 用户数据丢失）

条件 1+2+4+5 触发 Standard 强制。

- **人类决定**：待父账户确认本卡片（**S0 硬门禁**）

---

## 5. 风险

| # | 风险 | 概率 | 影响 | 缓解 |
|---|------|------|------|------|
| 1 | **数据迁移破坏 v1 Agent** | 中 | 极高 | rollback SQL 双文件 + 迁移前 dev 全量演练 + 迁移完跑 agent-student.spec.ts 回归套件 + migration 内置数量校验 assert |
| 2 | **frontmatter 解析歧义**（用户写的 markdown 与 YAML 头冲突）| 中 | 中 | 使用成熟库（goldmark + yaml.v3）+ fuzz test 覆盖 100+ corner case + 前端预览 |
| 3 | **v2 #2 接管运行时时发现 v1 字段不能直接弃用**（兼容窗口估错）| 中 | 中 | 本 feature deprecated 字段而非删除；v2 #2 加 dual-read 兜底 |
| 4 | **markdown 编辑器选型不当**（性能差 / 协作冲突）| 低 | 中 | 选 monaco-editor 或 CodeMirror 6；S2 阶段技术选型 |
| 5 | **Skill 数量爆炸导致列表分页 / 查询性能问题** | 低 | 低 | parent_user_id + is_active 加复合索引；前端默认分页 20/页 |
| 6 | **配置者把 Skill 概念跟 Agent 弄混** | 高 | 中 | 前端 UI 文案强调"Skill 是独立技能、Agent 是装载多个 Skill 的角色"；Skill 列表页加引导卡 |
| 7 | **后续 v2 #2 不做或延期 → Skill 创建了但用不上** | 低 | 高 | 与父账户约定 v2 #1 + #2 必须串行交付，#1 上线后立即启 #2 |

---

## 6. 仓库与估时

- **仓库**：`numind-server` + `numind-web-v3`（admin-web 不动）
- **估时**：3 周（含 S0-S7 全流程；S4 编码主体 ~10 工作日）
- **worktree**：
  - numind-server: `/private/tmp/wt-agent-mode-v2-skill-as-artifact-numind-server`
  - numind-web-v3: `/private/tmp/wt-agent-mode-v2-skill-as-artifact-numind-web-v3`

---

## 7. S0 待解决项（留给 S1/S2）

1. **数据迁移触发时机**：AutoMigrate 启动时自动跑（侵入性大），还是独立 CLI（运维步骤多但安全）？倾向独立 CLI，S2 确认
2. **Skill body 长度上限**：建议 50KB（约 5 万字符），S2 拍板
3. **markdown 编辑器**：monaco-editor / CodeMirror 6 / 自研，S2 选型
4. **Skill 与 SkillTemplate 关系**：v1 已有 SkillTemplate 表（平台预置 10 模板）。本 feature 是否把 SkillTemplate 也升级到新 skill 表的 source_type='platform_template'，还是保留分表？S2 拍板
5. **frontmatter 字段是否要单独列存**（便于查询）：是 / 否 / 部分（name + description + when_to_use 单列，allowed_tools JSON）？S2 拍板
6. **Skill 命名唯一性**：同一父账户下 Skill name 是否唯一？或允许重名靠 id 区分？S2 拍板

---

## 8. 备注

- 本 feature 是 v2 第一步，**为 #2 #3 准备地基**，工程节奏要稳，不追求一次到位
- 父账户已明确不要 Multi-Agent 编排（v2 #3 第 3 条线），本系列 3 feature 收完 v2 闭环
- 本 feature 的 deprecated 字段在 v2 #2 完成后再删（独立 micro feature 处理）
