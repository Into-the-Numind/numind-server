# agent-mode-v2-skill-as-artifact — 提案 + PRD

**关联**：[S0 需求卡](../requirements/agent-mode-v2-skill-as-artifact.md) · NDF Standard track · v2 三件套 #1

---

## §1 方案概述 [客户可见]

把"技能 (Skill)"从 Agent 配置里的一段 prompt 文字升级成**独立可管理的技能文件**——就像 Word 文档之于文件夹：

- **以前**：你创建 Agent 时填问卷生成的"AI 行为说明书"是这个 Agent 的私产，换个 Agent 想用类似能力只能 copy-paste
- **以后**：技能文件独立存在于"我的技能"页面，每个 Agent 可以装载多个技能；同一个技能可以装载到多个 Agent；技能可以单独编辑、版本回滚、未来发布到市场

本期（v2 #1）只做**地基**：建独立技能库 + 数据搬家 + 装载关系管理。运行时 Agent 真"调用"技能的能力在 v2 #2 落地，跨机构市场在 v2 #3。

**用户感知**：本期上线后侧边栏多一个"我的技能"菜单，可以创建/编辑/查看历史版本；Agent 编辑页多一个"已装载技能"区块。但 Agent 对话行为暂时不变（runtime 没动）——是为 v2 #2 准备的安全过渡。

---

## §2 报价与周期 [客户可见]

- 预估工作量：**15 工作日**（S0 已完成 0.5 天 · S1 0.5 天 · S2 1.5 天 · S3 1 天 · S4 编码 8 天 · S5 验收 1.5 天 · S6 部署 0.5 天 · 留 1.5 天 buffer）
- 报价：内部研发，不计费
- 交付时间线：S6 dev 部署目标 2026-06-13（含周末）；prod 部署本期不规划，按父账户拍板

---

## §3 技术可行性 [AI 内部]

### 现有功能复用

| 模块 | 用途 | 文件位置 |
|---|---|---|
| `biz/agent/skill_builder.go` | 问卷→markdown 组装逻辑 | `numind-server/internal/numind/biz/agent/skill_builder.go`（v1 #5）|
| `agent_definition_history` 表模式 | 历史版本表模板 | `numind-server/migrations/*agent_definition_history*.sql`（v1 #5）|
| `SkillTemplate` 表 | 10 个内置模板 seed | `numind-server/migrations/*skill_template*.sql`（v1 #5）|
| `parent_user_id` 父账户隔离模型 | 租户字段命名 + JWT 鉴权 | v1 全 14-feature 通用 |
| `yaml.v3` | YAML frontmatter parse | go.mod 已含 |
| `goldmark` | Markdown body 渲染（前端可选）| v1 已用 |
| `ConfigLayout` 菜单组件 | 加 /config/skills tab | `numind-web-v3/src/views/config/ConfigLayout.vue`（v1 #10 后 relocate）|

### 技术风险

| # | 风险 | 缓解方案 |
|---|---|---|
| R1 | **数据迁移破坏 v1 Agent** — 迁移脚本写错导致 binding 表数据不全或 skill 副本错位 | (1) migration 双文件（forward + rollback）（2）forward 末尾 SELECT COUNT 验证 binding 数 == 老 agent_definition 数 + 1 skill per agent，不匹配则 RAISE（3）dev 全量演练一次（4）S5 跑 agent-student.spec.ts 全套回归 |
| R2 | **frontmatter 解析歧义** — 用户的 markdown body 出现 `---` 三横线分隔符（合法 markdown），被误识为 frontmatter 终止 | 使用 `gopkg.in/yaml.v3` + `gohugoio/hugo/parser/pageparser` 风格的 BOM 双横线检测：仅识别**首行 `---`** 的 frontmatter；后续 `---` 一律当 markdown ruler |
| R3 | **markdown 编辑器选型** — 性能差或不支持 frontmatter 高亮 | S2 阶段独立技术选型，候选 monaco-editor / CodeMirror 6；S2 写 spike 评估 |
| R4 | **v2 #2 接管 runtime 时发现 v1 字段不能直接弃用** | 本期 deprecated 字段不删；v2 #2 实现 dual-read fallback；本期 e2e 测试覆盖 v1 老字段读路径 + 新 binding 写路径 |
| R5 | **配置者混淆 Skill 与 Agent** | 前端文案明确分层；首次进入 /config/skills 显示引导卡（"Skill 是独立技能，Agent 是装载多个 Skill 的角色"）；S5 跑 gstack `/qa` 检查 UX 清晰度 |
| R6 | **Skill 命名冲突** — 同租户允许重名导致用户选错 | DB 层不强制唯一（B 端机构可能多人配同名），但前端在装载页提示"已存在同名 Skill"；S2 拍板 |
| R7 | **迁移脚本不可重入** — 多次 AutoMigrate 派生重复 Skill | migration 内 INSERT 用 `NOT EXISTS` 子查询（按 agent_id 检查 binding 不存在才派生）+ skill 行用 `ON DUPLICATE KEY (parent_user_id, name) UPDATE id=id`（no-op）|

### 涉及仓库

- [x] **numind-server** — DB + biz/skill + controller + router
- [x] **numind-web-v3** — /config/skills 5 view + Agent 编辑器扩展 + api/store
- [ ] numind-admin-web — 不动（SkillTemplate 平台模板的 admin 管理不在本期）

### AI 可观测性

- [x] 涉及 LLM 调用：**否**
- 理由：本 feature 纯 CRUD + 数据迁移，无 LLM 调用。v2 #2 接管 runtime 后 use_skill tool 触发 Skill body 注入 LLM context，那时才需要 Langfuse trace 改造（v2 #2 范围）。
- N/A — 无 trace 起点 / generation 点 / 元数据需求

---

## §4 产品需求定义 — PRD [AI 内部]

### 用户故事

1. **创建 Skill**：作为父账户，我需要**独立创建一个 Skill 文件**（不依附于某个 Agent），用 Markdown 编辑器+frontmatter 表单写技能内容，以便沉淀可复用的能力资产
2. **管理 Skill 库**：作为父账户，我需要**集中查看/编辑/删除我所有的 Skill**（无论它是否绑定到 Agent），以便统一治理我的技能资产
3. **版本回滚**：作为父账户，我需要**看到 Skill 的历史版本并一键回滚**，以便误改后能撤销，不丢失原有的精调内容
4. **Agent 装载 Skill**：作为父账户，我需要**在 Agent 编辑页装载多个我已有的 Skill**（一对多关系），以便组合复用能力（虽然本期 runtime 还未真调用，但配置先到位为 v2 #2 准备）
5. **v1 Agent 零回归**：作为父账户，我现有的 Agent 在本期更新后**对话行为不变**（runtime 没动），以便升级期间业务连续
6. **权限隔离**：作为子账户（C 端学员），我**看不到/config/skills 菜单**，调用 /v1/skills/* API 返回 403，以避免学员误操作或泄露其他机构 Skill 内容

### 验收标准

| ID | 标准 | 验证方式 |
|---|---|---|
| AC-1 | 父账户在 `/config/skills` 点"新建"，进入 SkillEditor，填写 name+description+when_to_use+allowed_tools+body Markdown，点保存后跳转列表页且新 Skill 出现 | Playwright E2E `e2e/skill-crud.spec.ts` |
| AC-2 | Skill 创建后 `skill` 表写入 1 行 + `skill_history` 表写入 1 行 `version=1` 快照 | Go integration test `internal/numind/biz/skill/service_test.go` + DB 断言 |
| AC-3 | 编辑 Skill 保存后 `skill.version+1`，`skill_history` append 新快照（旧版本不删）| 同 AC-2 |
| AC-4 | 数据迁移完成后，`SELECT COUNT(*) FROM agent_skill_binding GROUP BY agent_id` 每个 agent_id 至少 1 binding；`SELECT COUNT(*) FROM skill WHERE source_type IN ('generated','custom')` 等于 `SELECT COUNT(*) FROM agent_definition WHERE is_active=1` | Migration SQL 末尾内置 assert + Go integration test |
| AC-5 | 迁移完成后，`numind-web-v3/e2e/agent-student.spec.ts` 全部 8 个 case PASS（v1 Agent 学员对话行为零回归）| Playwright E2E 回归套件 |
| AC-6 | 子账户（`parent_user_id != null` 的 user）持子账户 JWT 调用 `GET /v1/skills` / `POST /v1/skills` 返回 HTTP 403 + `errno.ErrPermissionDenied` | Go controller test + Playwright E2E |
| AC-7 | Skill body markdown + frontmatter 双向解析：`parse(serialize(skill)) == skill` 在 100+ fuzz case 下 PASS（含 body 含 `---`、空 frontmatter、特殊字符）| Go fuzz test `internal/numind/biz/skill/frontmatter_test.go` |
| AC-8 | 父账户在版本史页面点"回滚到 v2"，Skill 内容变为 v2 快照，且 `skill.version=4`（创建新版本 v4，不删 v1-v3）。**注**：S0 AC-8 原文"Agent 行为符合回滚版本的 Skill 内容"中"Agent 行为"维度推迟至 v2 #2（runtime 接管后），本期 AC-8 仅验证 skill 表内容回滚正确（DB 状态层），不验证 Agent 对话行为 | Playwright E2E（仅验 DB + 列表展示）|
| AC-9 | 父账户 DELETE 一个被 3 个 Agent 装载的 Skill，前端弹确认对话框列出 3 个 Agent；确认后 `skill.is_active=0` 且所有 binding `is_active=0`（级联软删）| Playwright E2E + Go integration test |
| AC-10 | 父账户在 `/config/agents/:id/edit` 装载一个 Skill 到 Agent，binding 表写入 1 行；调整顺序后 `sort_order` 字段更新；卸载后 binding `is_active=0` | Playwright E2E |
| AC-11 | 同租户 Skill 列表分页 `page_size=20`，N>20 时分页正常；list 端点查询走 `idx_skill_parent_active` 复合索引（`EXPLAIN` 显示 index hit）| Go integration test + 手动 EXPLAIN |
| AC-12 | `task lint`（Go）和 `npm run lint && npm run type-check`（Vue）双仓库零 error 零新增 warning | CI / S5 验收前手动跑 |

### 边界情况

1. **空 binding 的 Skill**：可创建可保存可删除，列表正常显示，标识"未装载"
2. **空 Skill 的 Agent**：v1 老 Agent 迁移后必有 1 binding，但用户主动卸载所有 binding 后允许（Agent 仍能用老字段跑，本期 runtime 不变）；前端编辑器提示"该 Agent 未装载任何 Skill"
3. **超长 Skill body**：body_md 列 MEDIUMTEXT 上限 16MB；前端编辑器软限 50KB 警告，硬限 200KB 阻止保存
4. **frontmatter 字段缺失**：仅 name 必填，其他可空；description / when_to_use / allowed_tools 缺失时 fallback 空字符串 / 空数组
5. **并发编辑**：同租户内同一 Skill 被两个浏览器 tab 同时编辑——后保存者覆盖（不引入乐观锁，B 端单人为主）；version 字段自然 +1 不会冲突
6. **rollback 边界**：rollback 到不存在的 version → 400 错误；rollback 到 is_active=0 的 Skill → 同时把 is_active=1 复活
7. **migration 中断**：长事务被 kill → rollback SQL 文件可手动重跑，数据状态可恢复

### 权限规则

- **路由级**：所有 `/v1/skills/*` 和 `/v1/agents/:id/skills/*` 都挂 user_token middleware
- **业务级**：每个端点函数顶部 `if user.ParentUserID != nil { return 403 }`（仅父账户）
- **资源所有权**：GET/PUT/DELETE 操作隐含 `WHERE parent_user_id = jwt.userID`；跨租户访问返回 404（不区分"无权限"和"不存在"避免枚举）
- **前端**：`/config/skills/*` 路由守卫检查 `userStore.isParentUser`，不是父账户 redirect 到 `/`

### UI 行为规格

#### `/config/skills` — SkillList.vue
- **页面位置**：左侧导航栏 ConfigLayout 内，"AI 助手" tab 同级新增 "我的技能" tab
- **布局**：DataTable 表格（按 [.claude/rules/ui-ux.md](.claude/rules/ui-ux.md) 硬规则#1 管理端必用表格）— 列：图标、Skill 名称、描述、装载 Agent 数、版本、最近修改、操作
- **交互**：行点击 → 详情页；操作列含 编辑 / 历史 / 删除
- **状态处理**：
  - loading：表格 skeleton
  - empty（首次进入）：居中引导卡 "**Skill 是独立的技能资产**，可以装载到不同 Agent 复用。点击新建开始" + CTA 按钮
  - error：toast + 重试按钮
- **顶部操作**：搜索框（按 name + description 模糊）+ "新建 Skill" 按钮（主色）+ 排序下拉

#### `/config/skills/new` 和 `/config/skills/:id/edit` — SkillEditor.vue
- **布局**：左右双栏 — 左 70% Markdown 编辑器（含 frontmatter），右 30% 表单（frontmatter 字段单独输入）
- **双向同步**：左右编辑任意一边，另一边实时反映（debounce 300ms 解析 frontmatter）
- **frontmatter 表单字段**：name (text, 必填) / description (text 1-300char) / when_to_use (textarea) / allowed_tools (multi-select 复用 v1 Agent 编辑器的工具开关组件)
- **底部操作**：取消（离开未保存提示弹窗）/ 保存（+version 写 history）。**不做草稿态**：本期所有保存都即时落库 + 版本 +1（MVP 范围收紧；草稿态需要新增 `draft_body` 字段引入双状态管理，复杂化 v2 #2 dual-read 逻辑——若后续真有需求走独立 micro feature 加 `draft_body TEXT` 字段 + `is_draft TINYINT` 区分）
- **状态处理**：保存中 loading mask；保存失败 toast；离开未保存提示

#### `/config/skills/:id` — SkillDetail.vue
- **布局**：上方元数据卡片（含装载到的 Agent 列表小标签）+ 下方 Markdown 渲染预览
- **顶部操作**：编辑 / 查看历史 / 删除（按 [.claude/rules/ui-ux.md](.claude/rules/ui-ux.md) 硬规则#4 删除必弹 ConfirmModal）

#### `/config/skills/:id/history` — SkillHistory.vue
- **布局**：左侧版本列表（时间线样式，最新在上）+ 右侧 diff 预览（选中版本与当前对比）
- **操作**：每行"恢复"按钮 → 弹 ConfirmModal "回滚后将创建新版本，旧版本保留" → 确认后 redirect 到 SkillDetail

#### `/config/agents/:id/edit` 扩展 — 新增 "已装载 Skill" 区块
- **位置**：紧挨现有 "工具开关" 区块上方
- **布局**：可拖拽排序的卡片列表 + "添加 Skill" 按钮 → 弹 modal 选本租户其它 Skill
- **交互**：拖拽 → 实时 PUT reorder；卡片右上角"移除"按钮 → DELETE binding
- **空态**：引导文案 "该 Agent 还没装载技能，点击添加从我的技能库选择"

---

## §5 估时拆解 [AI 内部]

| 阶段 | 工作 | 估时 |
|---|---|---|
| S0 | 需求卡 | 0.5d ✓ |
| S1 | 本提案 + reviewer | 0.5d ✓（当前）|
| S2 | spec（含 6 个 S0 留下的决策项 + spike markdown 编辑器选型）| 1.5d |
| S3 | task plan（~14-18 task，含 S5 验证策略 task）| 1d |
| S4 | 编码（后端 5d：model/migration/biz/skill/controller/router + 数据迁移测试；前端 3d：5 view + api/store + Agent 编辑器扩展）| 8d |
| S5 | 验收（Playwright E2E `e2e/skill-crud.spec.ts` + Agent 编辑器扩展 spec + 回归 agent-student.spec.ts + Go unit test 跑全套）| 1.5d |
| S6 | ndf-done + /deploy-dev server + /deploy-dev web-v3 + dev 烟测 | 0.5d |
| Buffer | 风险处置 / review 修正 / 文档刷新 | 1.5d |
| **总计** | | **15d** |
