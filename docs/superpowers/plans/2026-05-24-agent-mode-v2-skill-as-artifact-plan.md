# agent-mode-v2-skill-as-artifact — 实施计划

**关联**：[S0 需求卡](../../../requirements/agent-mode-v2-skill-as-artifact.md) · [S1 提案](../../../proposals/agent-mode-v2-skill-as-artifact-proposal.md) · [S2 spec](../specs/2026-05-24-agent-mode-v2-skill-as-artifact-design.md)

**日期**：2026-05-24 · **总 task 数**：18（含 S5 验证策略 task）· **估时**：S4 编码 10 工作日

---

## §1 Task 总览（依赖图）

```
T01 model+ddl ──┬──> T02 frontmatter ─┐
                ├──> T03 service ──┬───┼──> T07 controller+router ──> T15 e2e ──> T17 acceptance
                ├──> T04 versioning┘   │
                ├──> T05 binding ──────┤
                └──> T08 errno ────────┘
T06 migrate CLI ──> T01 + T03 后才能跑

T09 frontend api+store ──┬──> T10 SkillList ──┐
                         ├──> T11 SkillEditor ┤
                         ├──> T12 Detail+Hist ┼──> T14 router+layout ──> T15 e2e
                         └──> T13 BindingPanel┘

T16 go unit/fuzz ── 与 T02-T06 并行
T18 docs ── 收尾
```

**Wave 调度（NDF Rule 12 Tier）**：

| Wave | Tier | Tasks | 备注 |
|---|---|---|---|
| W1 | Tier 2 | T01 (model+ddl) | 串行起点，建立 schema 基础 |
| W2 | Tier 3 disjoint | T02 / T08 / T16-setup (frontmatter / errno / fuzz setup) | 3 个并发，文件归属互不重叠。**T16 跨 Wave**：W2 仅产出 `testhelper_test.go` 骨架 + fuzz target stub；完整套件 `go test ./biz/skill/artifact/...` 全 PASS 在 W4 后才能要求（依赖 T03/T04/T05 实装）。 |
| W3 | Tier 3 disjoint | T03 / T04 / T05 (service / versioning / binding) | biz/skill/artifact 内**不同文件**，需 ndf-check-disjoint 验证 |
| W4 | Tier 2 | T06 (migrate CLI) | 依赖 T01+T03+T04 |
| W5 | Tier 2 | T07 (controller+router) | 依赖 T03/T04/T05 |
| W6 | Tier 3 disjoint | T09 / T10 / T11 / T12 / T13 (frontend) | 5 个 vue 文件 + 1 api + 1 store，可并行 |
| W7 | Tier 2 | T14 (router+layout 集成) | 串行 |
| W8 | Tier 2 | T15 (e2e specs) | 5 个 spec 文件 |
| W9 | Tier 2 | T17 验证策略执行 + T18 docs | 收尾 |

> 同 Wave 内 dispatch implementer subagent **并行**前必须跑 `numind-server/scripts/ndf/ndf-check-disjoint.sh` 程序化验证文件归属无交集（NDF Rule 12）。失败立刻降级 Tier 4 串行。

---

## §2 Tasks 详细规格

### T01 — model 定义 + AutoMigrate + migration DDL

**描述**：定义 3 张表的 GORM model + AutoMigrate 注册 + migration SQL（仅建表）

**涉及文件**：
- `numind-server/internal/pkg/model/skill_artifact.go`（新建 — 3 张表的 model）
- `numind-server/internal/numind/helper.go`（修改 — 加 AutoMigrate）
- `numind-server/migrations/20260526_120000_create_skill_artifact_tables.sql`（新建 forward）
- `numind-server/migrations/20260526_120000_create_skill_artifact_tables_rollback.sql`（新建 rollback）

**验收条件**：
- `go test ./internal/pkg/model/` 通过
- `task lint` 通过
- AutoMigrate 启动后 `DESCRIBE skill / skill_history / agent_skill_binding` 三张表存在且字段完整
- 索引列名与 spec §1.1 / §1.3 一致

**注意**：
- ADR P2 — `IsActive` bool `default:true` GORM Create 陷阱：model 用 `*bool` 或在 Create 路径用 `Select("*").Create(...)` 兜底
- migration SQL `ALTER TABLE agent_definition MODIFY COLUMN ... COMMENT '...'` 不改字段类型仅改 comment

**估时**：0.5d

---

### T02 — biz/skill/artifact/frontmatter.go

**描述**：Markdown + YAML frontmatter 双向解析

**涉及文件**：
- `numind-server/internal/numind/biz/skill/artifact/frontmatter.go`（新建）
- `numind-server/internal/numind/biz/skill/artifact/frontmatter_test.go`（新建 — 100+ unit + 1 fuzz）

**验收条件**：
- 函数：`Parse(content string) (Frontmatter, body string, error)` + `Serialize(fm Frontmatter, body string) (string, error)`
- 仅识别首行 `---`（trim 后），后续 `---` 当 markdown ruler（spec §3.3）
- 100+ unit 覆盖：空 frontmatter / body 含 `---` / 特殊字符 / UTF-8 / 极长 body / 缺失可选字段
- 1 fuzz target：`FuzzRoundTrip` 验证 `Parse(Serialize(fm,body))` 等价

**估时**：0.5d

---

### T03 — biz/skill/artifact/service.go

**描述**：Skill 资产 CRUD 业务编排

**涉及文件**：
- `numind-server/internal/numind/biz/skill/artifact/service.go`（新建）
- `numind-server/internal/numind/biz/skill/artifact/store.go`（新建 — IStore interface + GORM 实现）
- `numind-server/internal/numind/biz/skill/artifact/service_test.go`（新建 — mock store）

**验收条件**：
- 函数签名严格按 spec §3.2（`Create / List / Get / Update / Delete / ListBoundAgents`，不含 history）
- 所有方法首句 `if user.ParentUserID != nil { return errno.ErrPermissionDenied }`
- List 支持分页 + 搜索（按 name 模糊）+ 排序
- Delete 软删 + 事务内级联 binding 软删
- 单测 covered: 子账户 403 / body > 200KB → ErrSkillArtifactBodyTooLarge / 跨租户 ErrSkillArtifactNotFound

**估时**：1d

---

### T04 — biz/skill/artifact/versioning.go

**描述**：版本递增 + history 快照写入 + restore

**涉及文件**：
- `numind-server/internal/numind/biz/skill/artifact/versioning.go`（新建）
- `numind-server/internal/numind/biz/skill/artifact/versioning_test.go`（新建）

**验收条件**：
- `WriteSnapshot(ctx, skill) error` — 每次 Update 在事务内调
- `Restore(ctx, parentUserID, skillID, version) (*model.Skill, error)` — 创建新版本（current+1）
- `ListHistory(ctx, parentUserID, skillID) ([]model.SkillHistory, error)` — 含 diff_summary 计算（用 sergi/go-diff，body_md 行级 diff，截断到 200 字符）
- 单测 covered: rollback 到不存在 version 返回 ErrSkillArtifactVersionNotFound / 复活 is_active=0 的 skill

**估时**：1d

---

### T05 — biz/skill/artifact/binding.go

**描述**：Agent-Skill 装载关系操作

**涉及文件**：
- `numind-server/internal/numind/biz/skill/artifact/binding.go`（新建）
- `numind-server/internal/numind/biz/skill/artifact/binding_test.go`（新建）

**验收条件**：
- 函数签名严格按 spec §3.2（`Attach / Detach / Reorder / ListByAgent`）
- Attach 时 uk 冲突 → 改 is_active=1 + 更新 sort_order（"复装"语义）
- Reorder 接受 skill_ids 数组，按数组顺序设 sort_order 0..n-1，事务内批量更新
- 所有方法首句 `if parentUserID != nil { return errno.ErrPermissionDenied }`（同 T03 B2B2C 守卫）
- 单测 covered: 同 agent 重复 attach / 卸载后复装 / 跨租户 agent_id 拒绝 / 子账户 403

**估时**：0.5d

---

### T06 — cmd/numind/migrate_skill_artifact CLI

**描述**：数据迁移 CLI 子命令（核心：避免 reviewer P0-2 的 JOIN race）

**涉及文件**：
- `numind-server/cmd/numind/migrate_skill_artifact.go`（新建）
- `numind-server/cmd/numind/main.go`（修改 — 注册 `migrate-skill-from-agent` 子命令）
- `numind-server/cmd/numind/migrate_skill_artifact_test.go`（新建 — 用 SQLite + 真 GORM）

**验收条件**：
- `numind migrate-skill-from-agent --dry-run --batch-size 50` 跑通且只 SELECT 不 INSERT
- `numind migrate-skill-from-agent` 全跑后 assert 通过（active_agents == distinct_active_bindings）
- 跑两次第二次跳过（幂等，依赖 LEFT JOIN 排除已迁移）
- `numind migrate-skill-from-agent --rollback` 删 binding 保留 skill
- 单测：注入 SQLite + AutoMigrate + seed 3 agent_definition → RunMigration → 验证 3 skill + 3 binding + 3 history v1

**估时**：1d

---

### T07 — controller + router

**描述**：11 个 endpoint 的 controller 函数 + router 注册

**涉及文件**：
- `numind-server/internal/numind/controller/v1/skill_artifact.go`（新建）
- `numind-server/internal/numind/router.go`（修改 — 注册路由）
- `numind-server/internal/numind/controller/v1/skill_artifact_test.go`（新建）

**验收条件**：
- 11 个 endpoint 全部注册 + 函数实现
- 请求字段绑定走 `c.ShouldBindJSON` / `c.ShouldBindQuery`
- 响应统一 `core.WriteResponse(c, err, data)`
- controller 不写业务逻辑（按 .claude/rules/api-design.md §6）
- controller test: 200 happy path / 400 bind error / 403 子账户 / 404 跨租户 各跑一例

**估时**：1.5d

---

### T08 — errno 新增

**描述**：新增 5 个错误码（ADR-14）

**涉及文件**：
- `numind-server/internal/pkg/errno/skill_artifact.go`（新建）

**验收条件**：
- 5 个错误码定义 + go doc 注释明确每个的语义
- Code 字符串遵循 `Category.SkillArtifact*` 命名约定
- `go test ./internal/pkg/errno/` 通过

**估时**：0.2d

---

### T09 — frontend api + store

**描述**：`src/api/skill.ts` + `src/stores/skill.ts`

**涉及文件**：
- `numind-web-v3/src/api/skill.ts`（新建）
- `numind-web-v3/src/stores/skill.ts`（新建）
- `numind-web-v3/src/types/skill.ts`（新建 — TS interface）

**验收条件**：
- 11 个 API 函数全部封装，类型严格（TS strict + 严格 request/response interface）
- Pinia store setup 风格（按 .claude/rules/frontend-state.md §1）
- `npm run type-check` 零 error
- `npm run lint` 零 error

**估时**：0.5d

---

### T10 — SkillList.vue

**描述**：Skill 列表页（管理端表格风格）

**涉及文件**：
- `numind-web-v3/src/views/config/skills/SkillList.vue`（新建）
- `numind-web-v3/src/views/config/skills/components/SkillListRow.vue`（新建）

**验收条件**：
- DataTable 布局（按 .claude/rules/ui-ux.md 硬规则#1）
- 列：图标、名称、描述、装载 Agent 数、版本、最近修改、操作
- 状态四态完整：loading skeleton / empty 引导卡 / error toast+retry / success
- 顶部：搜索框 + 排序下拉 + 新建按钮
- 分页 20/页
- 删除走 ConfirmModal

**估时**：0.8d

---

### T11 — SkillEditor.vue + CodeMirror 6 集成（含 spike）

**描述**：编辑器双向同步（最高风险 task）

**涉及文件**：
- `numind-web-v3/src/views/config/skills/SkillEditor.vue`（新建）
- `numind-web-v3/src/views/config/skills/composables/useFrontmatterSync.ts`（新建 — 双向同步逻辑封装）
- `numind-web-v3/src/views/config/skills/components/CodeMirrorEditor.vue`（新建 — CodeMirror 6 Vue 封装）
- `numind-web-v3/package.json`（修改 — 加 @codemirror/lang-markdown / @codemirror/lang-yaml / @codemirror/view / @codemirror/state / @codemirror/commands + js-yaml）

**Spike（task 起手 0.5d）**：评估 CodeMirror 6 Vue 3 reactivity 边界——若 spike 不通过回退 monaco-editor

**验收条件**：
- 编辑器变化 → 解析 frontmatter → 更新表单（debounce 300ms）
- 表单变化 → serialize → 更新编辑器原始内容
- **防死循环**：内部 `isUpdating` flag guard（在反向更新时跳过 watch trigger）
- 保存时拆分：发送给后端的是 `{name, description, ..., body_md}`（不含 frontmatter 原始 YAML）
- 离开未保存提示
- 200KB body 上限前端阻止
- 解析失败时显示警告并保留 raw

**估时**：1.5d

---

### T12 — SkillDetail.vue + SkillHistory.vue

**描述**：详情页 + 历史版本对比

**涉及文件**：
- `numind-web-v3/src/views/config/skills/SkillDetail.vue`（新建）
- `numind-web-v3/src/views/config/skills/SkillHistory.vue`（新建）
- `numind-web-v3/src/views/config/skills/components/DiffViewer.vue`（新建 — 行级 diff 展示）

**验收条件**：
- Detail：元数据卡片 + 装载 Agent 标签 + Markdown 渲染（marked + DOMPurify）
- History：时间线 + diff_summary 展示 + 选中版本与当前对比 + 一键回滚 ConfirmModal
- 操作 4 状态完整

**估时**：0.7d

---

### T13 — SkillBindingPanel.vue（嵌入 AgentEdit.vue）

**描述**：Agent 编辑页的"已装载 Skill"区块

**涉及文件**：
- `numind-web-v3/src/views/config/skills/components/SkillBindingPanel.vue`（新建）
- `numind-web-v3/src/views/config/agents/AgentEdit.vue`（**修改** — 插入 `<SkillBindingPanel :agent-id="agentId" />` 到工具开关区块上方）
- `numind-web-v3/src/views/config/skills/components/SkillSelectorModal.vue`（新建 — 添加时弹出的选择器）

**验收条件**：
- 卡片列表 + 拖拽排序（用 vue-draggable-plus 或类似）
- "添加 Skill" 按钮 → 弹 SkillSelectorModal（本租户可选 Skill 列表 + 搜索）
- "移除" 按钮 → DELETE binding
- 空态：引导文案

**估时**：0.5d

---

### T14 — router 注册 + ConfigLayout 菜单

**描述**：5 个新路由注册 + 菜单 tab

**涉及文件**：
- `numind-web-v3/src/router/index.ts`（修改 — 新增 5 个路由，meta 含 `requiresParent: true`）
- `numind-web-v3/src/views/config/ConfigLayout.vue`（修改 — 加 "我的技能" tab）

**验收条件**：
- 直接访问 `/config/skills` 子账户 redirect / 父账户进入
- ConfigLayout 显示 "我的技能" tab 高亮态

**估时**：0.2d

---

### T15 — E2E specs（5 个新文件）

**描述**：Playwright E2E 覆盖 AC-1/3/6/7/8/9/10

**涉及文件**：
- `numind-web-v3/e2e/skill-crud.spec.ts`（新建 — AC-1/3/12）
- `numind-web-v3/e2e/skill-version-history.spec.ts`（新建 — AC-8）
- `numind-web-v3/e2e/skill-delete-cascade.spec.ts`（新建 — AC-9）
- `numind-web-v3/e2e/skill-binding.spec.ts`（新建 — AC-10）
- `numind-web-v3/e2e/skill-permission.spec.ts`（新建 — AC-6 前端）
- `numind-web-v3/e2e/helpers/skill-helpers.ts`（新建 — fixtures + 共享步骤）

**验收条件**：
- 5 个 spec 用 `setupAgentMocks` 同款 fixture 模式 + 真实登录走 E2E_USERNAME/E2E_PASSWORD
- `npm run test:e2e -- e2e/skill-*.spec.ts` 全 PASS
- agent-student.spec.ts 回归套件零失败（AC-5 验证）

**估时**：1.5d

---

### T16 — Go unit + fuzz（并行 Wave 2 起步）

**描述**：所有 backend unit + fuzz 集中跑

**涉及文件**：T02/T03/T04/T05/T06/T07/T08 各自 `_test.go`（前面已列）

**验收条件**：
- `go test ./internal/numind/biz/skill/artifact/...` 100% PASS + 覆盖率 ≥ 80%
- `go test -race ./internal/numind/biz/skill/artifact/...` PASS
- `go test -fuzz=FuzzRoundTrip -fuzztime=30s ./internal/numind/biz/skill/artifact/` 30s 无 panic 无 fail

**估时**：0.5d（主要是跑测试 + 修偶发 bug）

---

### T17 — S5 验证策略执行（NDF Rule 10 强制 task）

**描述**：S3 必含的"S5 怎么验"task —— 我们已在 spec §6 拍板，本 task 是执行

**验证方式**：
- **Playwright E2E 主**：T15 的 5 个新 spec + 1 个回归 spec — 提供长期回归保护
- **Go unit + fuzz 辅**：T16 — 频繁触达路径 + 边界覆盖
- **DB EXPLAIN 手动**：在 dev 环境跑 `EXPLAIN SELECT ... FROM skill WHERE parent_user_id=? AND is_active=1 ORDER BY updated_at DESC LIMIT 20` 验证 `idx_skill_parent_active` 命中，截图存档

**理由**：UI 交互密集 + 需要长期回归 + 不引入支付/权限级业务（compliance 已在 v1 #13 处理）。选 Playwright + Go unit 组合最适合。**不选 gstack /qa**——它一次性不产持久化测试代码，本期是地基资产没有回归保护不可接受。

**关键用户路径**：
1. 创建 Skill（含 frontmatter 双向同步）
2. 编辑 Skill 触发 version+1
3. 装载到 Agent + 拖拽排序
4. 删除 Skill（含级联确认）
5. 版本回滚
6. 权限 403（子账户）
7. v1 Agent 学员对话回归

**执行时间**：与 T15 整合到一起跑

---

### T18 — 文档收尾

**描述**：更新 manifest + 写 S5 验收记录 + 必要 README

**涉及文件**：
- `numind-server/.ndf/manifest.yaml`（修改 — stage S4 → S5 → S6 + progress）
- `numind-server/.ndf/decisions/agent-mode-v2-skill-as-artifact/000N-{slug}.md`（新建 — 长决策 ADR，按 ndf rule 5）
- `numind-server/docs/superpowers/specs/2026-05-24-agent-mode-v2-skill-as-artifact-acceptance.md`（新建 — S5 验收记录，含 EXPLAIN 截图）

**估时**：0.3d

---

## §3 总估时

| Phase | Tasks | 估时 |
|---|---|---|
| W1 model | T01 | 0.5d |
| W2 frontmatter/errno/unit setup | T02/T08/T16 (并行) | 0.5d wall-clock |
| W3 biz core | T03/T04/T05 (并行) | 1d wall-clock |
| W4 migrate CLI | T06 | 1d |
| W5 controller | T07 | 1.5d |
| W6 frontend | T09/T10/T11/T12/T13 (并行) | 1.5d wall-clock（T11 是关键路径）|
| W7 frontend 集成 | T14 | 0.2d |
| W8 E2E | T15 | 1.5d |
| W9 验收+文档 | T17/T18 | 0.5d |
| **总计** | | **8.2d** |

S0+S1+S2+S3 已用 ~2d，S4+S5+S6 估 8.5d，总 ~10.5d，与 S1 提案 15d 相比有 4.5d buffer 应对风险。

---

## §4 Tier 3 并行文件归属预声明

W2、W3、W6 三个 Wave 内有并行 task，必须用 `numind-server/scripts/ndf/ndf-check-disjoint.sh` 验证文件归属互不重叠。归属表如下：

**W2 并行（T02 / T08 / T16）**：
- T02 拥有：`biz/skill/artifact/frontmatter.go` + `_test.go`
- T08 拥有：`internal/pkg/errno/skill_artifact.go`
- T16（setup）拥有：`biz/skill/artifact/testhelper_test.go`（共享 testutil）

**W3 并行（T03 / T04 / T05）**：
- T03 拥有：`service.go` + `store.go` + `service_test.go`
- T04 拥有：`versioning.go` + `versioning_test.go`
- T05 拥有：`binding.go` + `binding_test.go`

**W6 并行（T09 / T10 / T11 / T12 / T13）**：
- T09：`src/api/skill.ts` + `src/stores/skill.ts` + `src/types/skill.ts`
- T10：`src/views/config/skills/SkillList.vue` + `components/SkillListRow.vue`
- T11：`SkillEditor.vue` + `composables/useFrontmatterSync.ts` + `components/CodeMirrorEditor.vue` + `package.json`（仅加依赖）
- T12：`SkillDetail.vue` + `SkillHistory.vue` + `components/DiffViewer.vue`
- T13：`components/SkillBindingPanel.vue` + `components/SkillSelectorModal.vue` + **修改** `views/config/agents/AgentEdit.vue`（**唯一可能冲突点**：T13 单独动 AgentEdit.vue，其他 task 不触，OK）

每个 Wave dispatch 前主 session 用 `ndf-check-disjoint.sh` 跑一次确认。
