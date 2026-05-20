# NDF S0 Requirement Card · `agent-mode-configurator-ux`

**Track**：Standard
**Feature ID**：`agent-mode-configurator-ux`（14-feature 分解 #10/14）
**起草日期**：2026-05-21
**起草人**：AI（autopilot）
**状态**：S0 草案
**仓库**：`numind-admin-web`（前端 admin 仓库）
**依赖**：#3 `agent-mode-tool-registry`（merged `e0ae5da9`）+ #5 `agent-mode-skill-system`（merged `e05498b6`，9 个 user API 端点 + AgentDefinition/AgentDefinitionHistory/SkillTemplate 三表已落地）
**阻塞**：#11 `agent-mode-student-ux`（部分 — Builder UI 不阻塞，但需要 #10 至少能创建一个 agent 才能让学员看到）+ #14 e2e rollout

---

## 1. 起因（Why now）

Agent 模式底座 14-feature 分解的 **#10/14** —— Configurator UX 是 Skill 系统（#5）落地后，配置者（父账户）唯一能用得上的入口。

**当前问题**：

- #5 已落地 9 个 `/v1/agent/skills/*` 端点 + Skill Builder（问卷 → SKILL.md 自动组装），但**没有 UI**——配置者无法创建/编辑/管理 Agent
- 没有 UI = 业务上 Agent 模式无法真正起来——学员端（#11）即使做完也没东西可学（"AI 助手"图标点进去是空列表）
- 蓝本 §5 把"配置者 UX"作为 Agent 模式三大用户接触面之一（与学员 UX、监控/合规并列）

**核心矛盾**：配置者是完全非技术的机构父账户，但 LLM Agent 配置本质有 12 个参数 + 工具开关 + 成本控制 + 版本管理——信息密度天然不低。

**解决方案**（蓝本 §5）：

- 95% 配置者走问卷路径（**12 题单页可滚动表单**）：填问卷 → 保存 → 试聊 → 微调，全程无 prompt 术语暴露
- 5% 头部机构有 prompt 资产库 → 切高级模式直接编辑 Markdown（不可逆，但历史版本作救援网）
- 三种创建路径（模板派生 / 从零 / 从已有派生）覆盖不同起点
- 历史版本回滚 = 安全网
- 监控后台 = 让父账户看到学员实际怎么用

**为什么 #10 在 admin-web 不在 user-v3**：管理后台是父账户的"工作台"，与销售 CRM、SOP 配置、学员管理共存。学员端只用学员视角的对话窗（#11）。

---

## 2. 业务范围

> **关键术语翻译**：蓝本 §5.1 说"管理后台顶部导航"——Numind 实际 admin-web 用左侧边栏（`AdminSidebar.vue`），位置上等价"顶部导航"概念，落到侧边栏新加 "AI 助手" 菜单项。
>
> **API 鉴权口径**：#5 的 9 个端点全部在 `/v1/agent/skills/*` —— **user_token 鉴权**（非 admin_token），父账户专属（biz 层校验 `parent_user_id == nil` 否则 403）。admin-web 已有 user_token 的复用机制（销售 CRM / 客户管理已用），可以直接接入。
>
> **设计 token / 字体 / 间距以 `@DESIGN.md` 为准**；`@.impeccable.md` 提供品牌叙事方向。

### In scope（落到 admin-web 一个仓库）

1. **侧边栏 + 路由**
   - `AdminSidebar.vue` 加 "AI 助手" 菜单项（图标 + 文案；位置在销售 CRM 下方，监控后台上方）
   - 新增路由：
     - `/admin/agents` — Agent 列表（DataTable）
     - `/admin/agents/new` — 创建（路径选择 + 问卷）
     - `/admin/agents/:id` — 详情（3 tab：配置 / 历史 / 数据）
     - `/admin/agents/:id/edit` — 编辑（复用问卷 component）
     - `/admin/agents/:id/advanced` — 高级模式编辑
     - `/admin/agent-monitoring` — 监控后台（实时会话列表）
   - 路由全部走 `AdminLayout` + `requireAuth` 中间件

2. **Pinia store + API 客户端**
   - `src/stores/agent.ts` — setup syntax Pinia store，含 List/CRUD/Restore/AdvancedToggle/ListTemplates 状态 + actions
   - `src/api/agent.ts` — axios wrappers for 9 endpoints
     - `POST /v1/agent/skills` Create
     - `GET /v1/agent/skills?page=&page_size=&include_inactive=` List
     - `GET /v1/agent/skills/:id` Get
     - `PATCH /v1/agent/skills/:id` Patch
     - `DELETE /v1/agent/skills/:id` SoftDelete
     - `GET /v1/agent/skills/:id/history` ListHistory
     - `POST /v1/agent/skills/:id/restore/:version` Restore
     - `POST /v1/agent/skills/:id/advanced-toggle` AdvancedToggle
     - `GET /v1/agent/skill-templates` ListTemplates
   - 所有响应 unwrap 走 `src/api/request.ts`（已有）的拦截器

3. **Agent 列表页**（`views/agent/AgentList.vue`）
   - **管理端必须用 `DataTable` 表格布局**（CLAUDE.md ui-ux.md 硬规则#1）
   - 列：名称 / 描述 / 版本 / 模式（问卷 📋 / 高级 🔧）/ 状态（运行中 / 已下架）/ 操作
   - 操作列：编辑 / 历史 / 派生 / 下架 / 删除
   - 顶栏：搜索框（**仅过滤当前已加载页内 list，纯客户端 filter**；P0-1 修复 — 后端 `GET /v1/agent/skills` 无 `?name=` 参数，v1 不引入服务端搜索；当 page_size=50 + 单父账户 Agent 数通常 ≤20 时体验可接受。如未来 Agent 数 >50 → 由后续 micro 加 backend `?name=`）+ [+ 创建 Agent] 主按钮 + [从模板库选] 次按钮
   - 4 状态全部处理（loading skeleton / empty + CTA "去创建第一个 Agent" / error + retry / success）
   - 分页：page + page_size（默认 page_size=20）

4. **创建路径选择页**（`views/agent/AgentCreateChoose.vue`）
   - 3 张大卡片：从模板创建（推荐）/ 从零创建 / 从已有派生
   - 从模板：跳到 `TemplateGallery.vue`
   - 从零：跳到 `AgentBuilder.vue`（空白问卷）
   - 从已有：弹 Modal 选择源 agent → 调 GET /skills/:id → 复制 questionnaire_answers + 改名（自动加" - 副本"）→ 跳到 `AgentBuilder.vue`

5. **模板画廊**（`views/agent/TemplateGallery.vue`）
   - 调 `GET /v1/agent/skill-templates` 拿后端返回的所有模板（P2-3 修复 — 不假设固定 10 个；v1 #5 seed 提供约 10 个，未来 admin 可加减）
   - 网格布局：每张卡片 = 图标 + 名称 + 一句话描述 + 适用场景标签 + [用这个模板] 按钮
   - 点 [用这个模板] → 跳到 `AgentBuilder.vue?from=template:<id>`（预填问卷）
   - 模板预填包含：name / description / icon_url / welcome_message / **starters** / questionnaire_answers / tool_flags / credit_cap_per_session / daily_credit_cap（P1-1 修复 — Q5 starters 也必须预填，不仅是 questionnaire_answers）

6. **问卷编辑器**（`views/agent/AgentBuilder.vue`）—— **核心**
   - 单页可滚动表单（**禁止多页 Wizard**，蓝本 §5.3 设计原则）
   - 12 题严格按蓝本 §5.3 canonical 规格实装（控件 / 占位符 / 验证 / 默认值见下表）
   - 右上角常驻 [保存草稿] + [保存并发布]
   - 必填题前有 `*`，验证失败红边框 + 错误文案（**blur 触发**，不是每次 keystroke；ui-ux.md 硬规则#3）
   - 右下角小字链接 "需要更精细的控制？切换到高级模式 →"
   - 保存成功后弹出 `AfterSaveModal.vue`（试聊一下 / 暂时跳过）

7. **12 题问卷 schema**（与 #5 后端 `QuestionnaireAnswers` Go struct 完全对齐）

   | Q | 控件 | Props | 落到 |
   |---|------|-------|------|
   | Q1 名字 | `<TextInput>` | 2-20 字，不允许纯数字 | `AgentDefinition.name` |
   | Q2 头像 | `<AvatarPicker>` | 上传 ≤2MB jpg/png + 12 默认图标 | `AgentDefinition.icon_url` |
   | Q3 描述 | `<TextInput>` | 10-100 字 | `AgentDefinition.description` |
   | Q4 欢迎语 | `<Textarea rows=4>` | 20-500 字 | `AgentDefinition.welcome_message` |
   | Q5 starters | `<ChipInput max=4>` | 每条 5-50 字，最多 4 | `AgentDefinition.starters` (JSON array) |
   | Q6 任务类型 | `<CheckboxGroup>` | 5 选项 + 其他（自填）；至少选 1 | `questionnaire_answers.q6` ([]string，**code 值**：`analyze_data`/`generate_content`/`answer_questions`/`make_plan`/`grade_assignment`) |
   | Q7 材料类型 | `<CheckboxGroup>` | 4 选项；至少选 1 | `questionnaire_answers.q7` ([]string，**code 值**：`text`/`csv`/`image`/`none`) |
   | Q8 积分上限 | `<Slider min=200 max=2000 step=100>` + 数字显示 | 默认 800 | `questionnaire_answers.q8` (int) |
   | Q9 网络搜索 | `<RadioGroup>` | 必选 | `questionnaire_answers.q9` (`no_web_search` \| `allow_search`) |
   | Q10 注意话题 | `<Textarea rows=3>` | ≤500 字 | `questionnaire_answers.q10` (string) |
   | Q11 越界话术 | `<Textarea rows=3>` | 5-200 字；**默认预填**`"这个问题有点超出我的能力范围，你可以去问老师或者换个方式描述一下～"`（P1-2 修复 — 蓝本 §5.3 line 3334 默认值）| `questionnaire_answers.q11` (string) |
   | Q12 说话风格 | `<RadioGroup>` | 3 选项 | `questionnaire_answers.q12` (`friendly` \| `professional` \| `encouraging`) |

8. **Agent 详情页**（`views/agent/AgentDetail.vue`）
   - 3 个 Tab：基本配置 / 历史版本 / 使用数据
   - **配置 Tab**：只读展示 Q1-Q12 + tool_flags + credit cap；底部 [编辑] 按钮跳 `/admin/agents/:id/edit`
   - **历史 Tab**（`AgentHistoryTab.vue`）：
     - 调 `GET /v1/agent/skills/:id/history` 拿列表
     - 表格：版本号 / 创建时间 / 操作（[查看] [恢复]）
     - 当前版本标 "当前版本" 不显示恢复按钮
     - [查看] → 只读 Modal 展示该版本 questionnaire_answers
     - [恢复] → 弹 `ConfirmModal`（销毁性操作，硬规则#4）→ 确认后调 `POST /v1/agent/skills/:id/restore/:version`
   - **数据 Tab**（`AgentStatsTab.vue`）：v1 仅占位（"使用数据将于下次迭代上线"），不实装 — 真实数据由 #14 接入 Langfuse 后落地

9. **高级模式编辑**（`views/agent/AgentAdvancedEdit.vue`）
   - 入口：问卷右下角小字链接 → 弹 `AdvancedToggleConfirmModal.vue`（销毁性 + 不可逆警示）→ 确认后调 `POST /v1/agent/skills/:id/advanced-toggle` → 跳 `/admin/agents/:id/advanced`
   - UI：单栏 Markdown editor（**自研，不引外部库**——硬规则#5）
     - 字符数显示，>8000 字符变红警示
     - 工具开关（3 个 toggle）对应 `tool_flags.code_sandbox` / `tool_flags.media` / `tool_flags.dangerous`
     - 高危工具开启时弹二次确认 Modal
   - **不展示平台固定段**（P1-4 修复 — 经核查 `numind-server/internal/numind/biz/skill/skill_builder.go` 的 `Build()` 函数：generated_skill_body 仅含用户内容段，PlatformBasePrompt + PlatformSafetyFooter 是 runtime 拼接的独立 const，**不在 body 内**。UI 不需要做 marker 解析或只读块渲染）。如果后续需要让配置者看到完整 system prompt 预览，由独立 micro feature 落地（"system prompt 预览"）
   - 保存调 PATCH（advanced_mode=1 不允许切回 0，UI 不暴露切换按钮）

10. **试聊弹窗**（`views/agent/AfterSaveModal.vue`）
    - 保存成功后自动弹出
    - 文案：「✅ 助手已发布！」+「要先体验一下，看看效果吗？」
    - 两个按钮：[试聊一下] / [暂时跳过]
    - [试聊一下] → v1 不实装试聊功能（占位 toast "试聊功能即将上线"），蓝本 §4.3.8 的 `admin_test` 配额由 #12 真正落地
    - [暂时跳过] → 关闭 Modal 回详情页

11. **监控后台**（`views/agent/AgentMonitoring.vue`）（P0-2 修复 — v1 zero-backend-dep）
    - 路由 `/admin/agent-monitoring`
    - **v1 行为：UI 骨架完整，但不调任何后端 API**（避免 404 噪音）—— 直接渲染 empty-state："监控功能即将上线 · v1 不联机"
    - 30 秒自动刷新逻辑代码就位，但 fetcher 函数 v1 直接返回空数组（注释 TODO(#14) 标记 wire 真实 API 端点）
    - DataTable 列定义就位（学员 / Agent / 开始时间 / 已用时 / 已用积分 / 状态 / 操作）—— 仅作 v2 骨架，v1 永远空数组所以不会渲染真实行
    - 操作列定义保留（[查看 Trace] / [强制取消]）但 v1 不渲染（因无数据行），避免无效 ConfirmModal 设计成本
    - 紧急下架 Agent：列表页 → [下架] → ConfirmModal → DELETE /v1/agent/skills/:id（这部分是真功能，不属于监控后台）

12. **派生 + 删除等操作**
    - 派生：操作列 [派生] → 调 GET /:id → 提取 questionnaire_answers → 跳 `/admin/agents/new` 并预填
    - 删除（下架）：操作列 [下架] → ConfirmModal "下架后已在进行的会话将立即终止" → DELETE /v1/agent/skills/:id

### Out of scope（明确划线）

- **学员端 Agent 对话窗** — #11；本 feature 不实装学员端对话 UI
- **试聊真实流程**（要消耗管理员独立 5000 积分配额）— #12（admin_test source_type 落地后） / #14 接入真实 LLM 后
- **Langfuse trace 跳转**——v1 [查看 Trace] 只是 toast 占位，真实跳转 URL 由 #14 wire
- **强制取消正在运行的 agent 会话** —— v1 仅 UI 操作占位；后端 API（停止 agent_run）由 #14 落地
- **监控后台真实数据** —— v1 mock 空数组；真实数据来自 #11/#14
- **数据统计 Tab** —— v1 占位，真实统计源（agent_run 表）由 #14 提供后接入
- **per-tenant narration 模板编辑器**（蓝本 §4.7 narration 段）—— Out of scope（独立 micro 或 feature 后续）
- **后端 API 改动**——本 feature 不动 numind-server；如发现 #5 端点缺关键字段，记录到 follow-up issue（不阻塞本 feature merge）
- **prod 部署** —— develop merge 后停（不打 git tag、不动 prod）
- **响应式 mobile 布局** —— admin-web 仅桌面端；mobile 由 #11 学员端解决
- **国际化 i18n** —— v1 中文硬编码（admin-web 当前现状如此，#10 不引入新依赖）

---

## 3. 验收条件（Definition of Done）

S6 ndf-done 准入门槛：

### 工件 + 测试

- [ ] `src/api/agent.ts` — 9 个 axios wrappers（含 TypeScript Request/Response interface）
- [ ] `src/stores/agent.ts` — Pinia setup syntax store（list/current/templates/history 状态 + actions）
- [ ] `src/views/agent/AgentList.vue` — DataTable 列表 + 4 状态全部处理
- [ ] `src/views/agent/AgentCreateChoose.vue` — 3 路径选择
- [ ] `src/views/agent/TemplateGallery.vue` — 10 模板卡片
- [ ] `src/views/agent/AgentBuilder.vue` — 12 题问卷（严格按 §5.3 canonical）
- [ ] `src/views/agent/AgentDetail.vue` — 3 Tab 容器
- [ ] `src/views/agent/AgentHistoryTab.vue` — 历史版本 + 恢复
- [ ] `src/views/agent/AgentStatsTab.vue` — 占位
- [ ] `src/views/agent/AgentAdvancedEdit.vue` — Markdown editor + 工具开关
- [ ] `src/views/agent/AgentMonitoring.vue` — 监控后台
- [ ] `src/views/agent/AfterSaveModal.vue` + `AdvancedToggleConfirmModal.vue` 等子组件
- [ ] 路由注册到 `src/router/index.ts`
- [ ] `AdminSidebar.vue` 加 "AI 助手" 菜单项
- [ ] 类型定义文件 `src/types/agent.ts`（含 QuestionnaireAnswers 等 12 题 schema）
- [ ] **单元测试覆盖**（vitest + Vue Test Utils）
  - [ ] `agent.ts` store actions（mock axios）
  - [ ] `AgentBuilder.vue` 12 题验证（blur 触发 + 红边框 + 错误文案）
  - [ ] `AgentList.vue` 4 状态切换
  - [ ] `AgentHistoryTab.vue` 恢复确认流程
  - [ ] `AgentAdvancedEdit.vue` 8000 字符红色警示边界
- [ ] **E2E 测试覆盖**（Playwright，admin-web/e2e/）—— 仅 critical path
  - [ ] 模板派生 → 改名 → 保存 → 列表出现 → 详情查看 → 历史版本 1 条
  - [ ] 从零创建 → 12 题填写 → 验证错误展示 → 修正 → 保存 → 弹试聊 modal
  - [ ] 高级模式切换 → 警示 confirm → 确认 → DB 状态变化 → UI 反映
  - [ ] 派生：选择源 agent → 复制 → 改名保存 → 不影响源
- [ ] `npm run lint` 0 warn 0 error（admin-web 现状如有 baseline 警告：保持不变）
- [ ] `npm run type-check` PASS
- [ ] **0 外部 UI 框架引入**（不引入 Element Plus / Ant Design / Vant 等；硬规则#5）
  - 验证命令：`grep -r "element-plus\|ant-design-vue\|vant\|naive-ui" numind-admin-web/src/ numind-admin-web/package.json | grep -v node_modules` → 0 hits（#10 不新增；现有依赖如有，由本 feature 不引入新条目）
- [ ] **0 inline LLM 调用 + 0 硬编码 API key**（P1-3 修复 — 验证命令具体化）
  - 验证命令 1：`grep -r "openai\|anthropic\|dashscope\|sk-\|API_KEY" numind-admin-web/src/views/agent/ numind-admin-web/src/api/agent.ts numind-admin-web/src/stores/agent.ts` → 0 hits（不含 mock keyword 注释）
  - 验证命令 2：所有 HTTP 调用通过 `src/api/request.ts` axios 实例：`grep -r "import axios" numind-admin-web/src/views/agent/ numind-admin-web/src/api/agent.ts numind-admin-web/src/stores/agent.ts` → 仅 `src/api/request.ts` 命中（已存在），新文件 0 直接 import axios

### 安全 + 合规

- [ ] 所有 axios 调用走 `src/api/request.ts`（不直接 import axios）
- [ ] 401 → 跳登录（由 request.ts 拦截器自动处理）
- [ ] 销毁性操作（Delete / Restore / AdvancedToggle）必须经 `ConfirmModal`
- [ ] 表单验证 **blur** 触发（不是 input）
- [ ] 异步视图 4 状态（loading skeleton / empty + CTA / error + retry / success）全覆盖
- [ ] 子账户访问列表页 → 后端返回 403 → UI 友好提示"仅父账户可配置 AI Agent，请联系机构主"（不裸出 403）

### UI / 设计一致

- [ ] 管理端列表用 **DataTable**（不用卡片网格）—— 硬规则#1
- [ ] 颜色 / 字体 / 间距走 `@DESIGN.md` 设计 token
- [ ] 组件复用项目自研 `DataTable` / `Modal` / `FormField` / `Button`（不引入外部）
- [ ] Markdown editor 用自研轻量 component（或浏览器原生 textarea + 语法高亮 CSS；不引 monaco/codemirror，bundle 太大）

### 0 prod 影响

- [ ] 不动后端代码（numind-server zero diff）
- [ ] 不打 git tag
- [ ] 不调 `/deploy-prod`
- [ ] feature 分支不推 GitHub（pre-push hook 拦）
- [ ] 不动 `numind-admin-web/config_prod.yaml`（如有）/ 不改 nginx prod 配置

---

## 4. 风险

1. **#5 API 字段口径与 §5.3 题目编号不一致** — 风险：UI 把 Q6 当任务类型，但 #5 后端的 questionnaire_answers.q6 schema 已固定（已 merged develop）；如果不对齐会 wire 不上
   - 缓解：（已确认）#5 `internal/numind/biz/skill/questionnaire.go` 的 `QuestionnaireAnswers` struct 已完全对齐 §5.3 编号（Q6 任务类型多选 / Q7 材料类型 / Q8 积分滑块 / Q9 网络搜索 / Q10 话题 / Q11 越界 / Q12 风格）。我的 type 定义直接 mirror 后端 Go struct，不另外发明编号。

2. **questionnaire_answers JSON shape 未来演进** — 风险：后端 v2 新增题，admin-web 旧版不识别
   - 缓解：（与 #5 同策略）TypeScript interface 用 `?` optional + 不开 `strict` extra-properties；旧字段保留；新字段按需展示

3. **DataTable 自研 component 现状能力** — 风险：admin-web 现有 DataTable 不支持某些必需特性（如行内 action 按钮 / 自定义 cell render / 分页）
   - 缓解：S1 先验收 admin-web 现有 DataTable 的 props/slots；如不够 → S2 评估扩展（小补丁）或回退到普通 `<table>` + 自定义 CSS。**禁止引入外部 table 库**

4. **Markdown editor 自研难度** — 风险：高级模式需要一个能编辑 Markdown 的 component，monaco/codemirror 是常规方案但 bundle 太大
   - 缓解：v1 用最简方案——`<textarea>` + 字符数显示 + 语法高亮纯 CSS（如 `.md-editor pre { color: ... }`）；行号 / 自动缩进 / 完整 markdown 渲染**全部 out of scope**。如果体验差，由后续 micro 接入轻量 markdown 库（如 toast-ui editor）解决

5. **高级模式不可逆性 UI** — 风险：配置者误切高级后回不去问卷，体验差
   - 缓解：(a) 切换前 Modal 警示明确（"无法切回问卷模式，但可以从历史版本回滚"）；(b) 历史版本 Tab 用 📋 标问卷模式版本 / 🔧 标高级模式版本；(c) [恢复] 按钮在历史 Tab 持续可见

6. **监控后台数据源缺失** — 风险：v1 没有"正在运行的 agent 会话"API
   - 缓解：v1 仅 UI 骨架 + 空状态 + 30s 刷新逻辑；数据源 API（agent_run 表查询）由 #14 落地；v1 mock 调一个返回 `{list:[], total:0}` 的 endpoint（如 `/v1/agent/sessions/active` 后端如果未实现就返回 404 → 前端 catch 后展示空状态）

7. **试聊真实流程缺位** — 风险：保存成功弹 "试聊" Modal 但点击没反应，配置者体验割裂
   - 缓解：v1 点击 [试聊一下] → toast "试聊功能即将上线"（明示 v1 限制，避免无声响）；真实试聊由 #12 / #14 落地后接入

8. **B2B2C 父账户鉴权 UI 提示** — 风险：admin-web 同时支持父账户登录和销售管理员登录（实际只有父账户登录，admin 端没有完整子账户登录），但后端会对子账户调用返回 403
   - 缓解：（已确认）admin-web 当前仅父账户可登录（销售 CRM、客户管理等都用 user_token = 父账户 token）；不需要做特殊子账户引导。如果 API 返回 403，request.ts 拦截器处理，UI 显示"仅父账户可访问"

9. **DataTable 4 状态 skeleton 现状** — 风险：admin-web 现有 DataTable 可能没标准 skeleton/empty/error slot
   - 缓解：S2 检查现有 component；若 slot 缺失，**包一层** `<AsyncView v-if/v-else-if/v-else>` 在外层处理 4 状态（不改 DataTable 本身）

10. **Q6 "其他（填写）" 自由文本处理**（P2-1 follow-up） — 风险：蓝本 §5.3 line 3277 Q6 有"其他（填写）"作为第 6 选项；后端 `taskTypeDisplay()` 只 switch 5 已知 code，自由文本走 `default: return t`
    - 缓解：S2 spec 明确：UI 把 "其他" 渲染为 chip 内联 input；提交时该自由文本作为 array 第 6 项（如 `q6: ["analyze_data", "<自由文本>"]`）；后端透传到 SKILL.md 时按原样渲染。不引入 Q6 OTHER enum，保持 schema 简单

11. **lint baseline** — 风险：admin-web 现状 `npm run lint` 可能含 N 条 baseline 警告，#10 增量代码混进基线后无法验收
    - 缓解：S2 阶段记录当前 `npm run lint` 的精确警告/错误 count 作 baseline，S5 验收要求"`npm run lint` warning ≤ baseline，error == 0"

---

## 5. 简单时间线（参考）

S0（本卡） → S1 proposal/PRD → S2 spec → S3 plan → S4 编码（M1-M~12）→ S5 验收 → S6 ndf-done

每阶段独立 Sonnet reviewer，遵循 `feedback_review_each_stage`。

---

## 6. 相关文档

- 蓝本 §5 配置者 UX（**canonical UI source of truth**）：`docs/agent-mode/architecture-v1.md`
- 蓝本 §5.3 12 题问卷规格：同上
- 蓝本 §4.3 Skill 系统：同上
- #5 落地 acceptance：`numind-server/docs/superpowers/qa/2026-05-22-agent-mode-skill-system-s5-acceptance.md`
- #5 spec：`numind-server/docs/superpowers/specs/2026-05-22-agent-mode-skill-system-design.md`
- #5 API 实装：`numind-server/internal/numind/controller/v1/agent/skill.go`
- #5 questionnaire schema：`numind-server/internal/numind/biz/skill/questionnaire.go`
- `numind-admin-web/CLAUDE.md` — Vue 3 + Pinia + axios
- `.claude/rules/ui-ux.md` — 5 条硬规则
- `.claude/rules/frontend-state.md` — Pinia setup syntax + axios pattern
- `@DESIGN.md` — 设计 token
- `@.impeccable.md` — 品牌叙事

---

**S0 完结。S1 写 proposal + PRD（含路由树 / 组件树 / API 对接细节 / 数据流图）。**
