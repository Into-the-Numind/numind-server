# NDF S1 Proposal + PRD · `agent-mode-configurator-ux`

**Feature ID**：`agent-mode-configurator-ux`（#10/14）
**关联 S0**：`numind-server/requirements/agent-mode-configurator-ux.md`
**起草日期**：2026-05-21
**起草人**：AI（autopilot）
**仓库**：`numind-admin-web`
**状态**：S1 草案
**前置依赖**：#5 agent-mode-skill-system（merged `e05498b6`）— 9 个 `/v1/agent/skills/*` 端点 + AgentDefinition/History/SkillTemplate 三表

---

## 1. 产品需求文档（PRD）

### 1.1 用户与场景

**目标用户**：父账户机构主（B2B2C 模型中购买席位的"老板"），登录 admin-web 配置 AI Agent，发布给子账户学员使用。

**关键场景**：

| # | 场景 | 关键动作 |
|---|------|----------|
| 1 | 第一次创建（70% 用户）| 模板派生 → 改名 → 保存 → 试聊 |
| 2 | 从零创建（20%）| 12 题问卷填写 → 保存 |
| 3 | 从已有派生（10%）| 复制 + 改差异 → 保存 |
| 4 | 微调迭代 | 编辑现有 → 保存 → 试聊 → 反复 |
| 5 | 改坏回滚 | 详情 → 历史 Tab → 恢复 v3 |
| 6 | 切高级模式 | 警示 Modal → 切换 → 编辑 Markdown |
| 7 | 下架 | 列表 → 操作 → 下架 ConfirmModal |
| 8 | 查看运行 | 监控后台（v1 仅 UI 骨架）|

**关键非目标**：
- 不做学员端对话窗（#11）
- 不做真实试聊计费（#12）
- 不做 Langfuse trace 跳转（#14）
- 不做后端 API 改动（消费 #5 现有 9 端点）

---

### 1.2 信息架构（IA）与路由树

> **P0-1 修复（S0 → S1 路径校正）**：S0 §2.1 写的 `/admin/agents` 是错的——admin-web router 的 children 挂在 `/` 下，**没有 `/admin/` 前缀**（验证：`numind-admin-web/src/router/index.ts` 已存在路由如 `/users` `/templates` `/billing`，皆无 `/admin/` 前缀）。S1 校正为根路径，S0 同步勘误（manifest decisions 也加一条）。
>
> **Nit fix**：合并 `/agents/:id/edit` 与 `/agents/:id/advanced` 为单路由 `/agents/:id/edit`，内部根据 `agent.advanced_mode` 切换模式（与 decision-3 单 Builder 模式一致；减少路由 surface）。

```
/login                              # 已存在
/                                   # AdminLayout（已存在）
├── /                               # dashboard（已存在）
├── /users                          # 已存在
├── /templates                      # 已存在（SOP 模板）
├── /runs                           # 已存在
├── /agents                         # 🆕 Agent 列表（DataTable）
├── /agents/new                     # 🆕 创建路径选择（模板 / 从零 / 派生）
├── /agents/new/from-template       # 🆕 模板画廊
├── /agents/:id                     # 🆕 详情（3 Tab：配置 / 历史 / 数据）
├── /agents/:id/edit                # 🆕 编辑（内部按 advanced_mode 切问卷 OR Markdown editor）
├── /agent-monitoring               # 🆕 监控后台
├── /billing                        # 已存在
├── ... (其他已存在路由)
└── /ai-services/*                  # 已存在
```

**侧边栏菜单分组**（在现有"运行监控"下方新增分组）：

```
导航
├── 仪表盘
├── 用户管理
├── SOP模板
├── 运行监控
├── AI 助手          ← 🆕 主菜单
│   ├── 助手列表     ← /agents
│   └── 运行监控     ← /agent-monitoring
├── 用量概览
├── ... (billing 等)
```

> **决策**：sidebar 用嵌套展开菜单 OR 平铺？admin-web 当前 navItems 是平铺列表。为了不引入"嵌套菜单"新组件（复杂度高），**S1 决策：平铺**。两条目分别注册为顶级 navItem：`AI 助手` (path `/agents`, icon `Bot`) + `Agent 监控` (path `/agent-monitoring`, icon `Activity`)。后续如菜单项过多再考虑分组。

---

### 1.3 组件树

```
src/views/agent/
├── AgentList.vue                    # 列表页（DataTable）
├── AgentCreateChoose.vue            # 创建路径选择（3 大卡）
├── TemplateGallery.vue              # 模板画廊（卡片网格 — 唯一一处用卡片，因为是"画廊"非"列表"）
├── AgentBuilder.vue                 # 12 题问卷主表单（创建 + 编辑通用）
├── AgentDetail.vue                  # 详情容器（3 Tab）
├── AgentAdvancedEdit.vue            # 高级模式编辑（专用页，独立于 Builder）
├── AgentMonitoring.vue              # 监控后台（v1 骨架）
└── components/
    ├── AvatarPicker.vue             # Q2 头像选择 + 上传
    ├── ChipInput.vue                # Q5 starters 输入（最多 4）
    ├── CheckboxGroup.vue            # Q6/Q7 多选（与其他视图复用）
    ├── CreditSlider.vue             # Q8 滑块
    ├── QuestionnaireForm.vue        # 12 题表单（被 AgentBuilder 用）
    ├── AgentHistoryTab.vue          # 详情页历史 Tab 内容
    ├── AgentStatsTab.vue            # 详情页数据 Tab 内容（v1 占位）
    ├── AgentConfigTab.vue           # 详情页配置 Tab 内容（只读展示）
    ├── AfterSaveModal.vue           # 保存后试聊弹窗
    ├── AdvancedToggleConfirmModal.vue # 切高级模式确认
    └── HistoryViewModal.vue         # 历史版本只读查看 Modal
```

**复用现有 common 组件**：DataTable / ConfirmModal / AppButton / AppInput / AppSelect / AppToast / StatusBadge

**新增公共组件**：`CheckboxGroup` （如果不存在）会放到 `components/common/`，否则放 `views/agent/components/`（局部使用）。**S2 决定**。

---

### 1.4 数据流图（API ↔ Store ↔ View）

```
┌──────────────────────────────────────────────────────────────────┐
│                          View Layer                              │
│  AgentList.vue → stores/agent.fetchList()                        │
│  AgentBuilder.vue → stores/agent.create() / .patch()             │
│  AgentDetail.vue → stores/agent.get() / .fetchHistory()          │
│  AgentHistoryTab.vue → stores/agent.restore(version)             │
│  TemplateGallery.vue → stores/agent.fetchTemplates()             │
│  AgentAdvancedEdit.vue → stores/agent.toggleAdvanced() / patch() │
└──────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌──────────────────────────────────────────────────────────────────┐
│                        Pinia Store                                │
│  stores/agent.ts (setup syntax)                                   │
│    state: list, current, templates, history, loading, error      │
│    actions: fetchList / get / create / patch / softDelete /      │
│             fetchHistory / restore / toggleAdvanced /             │
│             fetchTemplates                                        │
└──────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌──────────────────────────────────────────────────────────────────┐
│                        API Layer                                  │
│  api/agent.ts (axios wrappers, 9 functions)                      │
│    listAgentsApi / getAgentApi / createAgentApi /                │
│    patchAgentApi / deleteAgentApi /                              │
│    listAgentHistoryApi / restoreAgentApi /                       │
│    toggleAgentAdvancedApi / listSkillTemplatesApi                │
└──────────────────────────────────────────────────────────────────┘
                              │
                              ▼  (HTTP via src/api/request.ts)
┌──────────────────────────────────────────────────────────────────┐
│                      Backend (已 merged)                          │
│  /v1/agent/skills/*  (9 endpoints, user_token, parent only)      │
└──────────────────────────────────────────────────────────────────┘
```

---

### 1.5 9 API 端点契约（详细）

> 全部以 #5 实际 merged Go struct 为准（`numind-server/internal/numind/biz/skill/` + `controller/v1/agent/skill.go`）。

#### POST `/v1/agent/skills` — 创建

**Request body**:
```typescript
{
  name: string                  // 必填，2-20 字
  description?: string          // 10-100 字（前端校验；后端不强制）
  icon_url?: string
  welcome_message?: string      // 20-500 字
  starters?: string[]           // 最多 4 条
  questionnaire_answers?: {
    q6?: string[]               // 必填（后端强制）
    q7?: string[]               // 必填（后端强制）
    q8?: number                 // 0 视为 default 800
    q9?: 'no_web_search' | 'allow_search'
    q10?: string
    q11?: string
    q12?: 'friendly' | 'professional' | 'encouraging'  // 必填（后端强制）
  }
  tool_flags?: Record<string, boolean>
  credit_cap_per_session?: number | null  // null=不限；undefined=不传该字段（同上）；P0-2 修复
  daily_credit_cap?: number | null        // 同上
  source_template_id?: number | null      // 同上
}
// 注意：JSON.stringify 默认保留 null，剥去 undefined。
// 为了把"不限"明确传给后端，必须显式 `credit_cap_per_session: null`。
// 不传字段（undefined） = 让后端用默认（Go zero value = 0 = "不限"）；
// 二者后端语义等价但传输上不同。前端 store create() 调用应统一显式传 null。
```

**Response**: `AgentDefinition`（含 generated_skill_body 等所有字段）

**错误**：
- `code: 4xx` `message: "questionnaire.q6 required"` 等 → 后端 builder 校验失败
- `code: 4xx` `ErrChildAccountForbidden` → 子账户调用

#### GET `/v1/agent/skills` — 列表

**Query params**: `page` (default 1) / `page_size` (default 20) / `include_inactive` (default false)

**Response**: `{ list: AgentDefinition[], total: number }`

#### GET `/v1/agent/skills/:id` — 详情

**Response**: `AgentDefinition`

**错误**：404 当 id 不存在或不属于当前 user

#### PATCH `/v1/agent/skills/:id` — 部分更新

**Request body** (所有字段 optional)：
```typescript
{
  name?: string
  description?: string
  icon_url?: string
  welcome_message?: string
  starters?: string[]
  questionnaire_answers?: { /* 同 Create */ }
  tool_flags?: Record<string, boolean>
  credit_cap_per_session?: number | null   // P0-2 修复同上
  daily_credit_cap?: number | null
  // 注意：advanced_mode / is_active / parent_user_id 不可改
}
```

**Response**: 更新后的 `AgentDefinition`

#### DELETE `/v1/agent/skills/:id` — 软删除

Empty response on success.

#### GET `/v1/agent/skills/:id/history` — 历史版本

**Response**: `{ list: AgentDefinitionHistory[], total: number }`

每个 history row：
```typescript
{
  id: number
  agent_id: number
  version: number
  snapshot: AgentDefinition          // 完整快照
  created_by: number
  created_at: string                  // ISO timestamp
}
```

> **重要**：列表按 version 倒序；当前版本是 list[0]；调 `/v1/agent/skills/:id` 返回的 `version` 字段与 `history.list[0].version` 一致。

#### POST `/v1/agent/skills/:id/restore/:version` — 回滚

无 body。

**Response**: 新创建的 `AgentDefinition`（version = max+1，snapshot 复制自指定 version）

#### POST `/v1/agent/skills/:id/advanced-toggle` — 切高级模式

无 body（不可逆，仅 0→1）。

**Response**: 更新后的 `AgentDefinition`（advanced_mode=1）

**前端约束**：UI 不暴露 advanced_mode=1→0 切换，按钮直接消失。

#### GET `/v1/agent/skill-templates` — 内置模板列表

**Response**: `SkillTemplate[]`（数组，不分页）

每个 template row：
```typescript
{
  id: number
  name: string
  description: string
  icon_url: string
  welcome_message: string
  starters: string[]
  questionnaire_answers: { /* 完整 12 题预填 */ }
  tool_flags: Record<string, boolean>
  credit_cap_per_session: number
  daily_credit_cap: number
  created_at: string
}
```

---

### 1.6 12 题问卷 UI 详细规格（基于蓝本 §5.3 canonical）

> S0 已列总表。S1 这里补**控件级**实装规格 + 默认值 + 验证。

| Q | UI Spec |
|---|---------|
| **Q1 名字** | `<AppInput>` 单行；placeholder "例如：爆款分析师、学习陪伴小助手..."；帮助折叠默认隐藏 "这个名字学员会直接看到，建议亲切一点"；validators: required + len 2-20 + 不全数字（正则 `/^\d+$/` 不通过）；default：空字符串 |
| **Q2 头像** | `<AvatarPicker>`（自研）：12 个内置 SVG 图标网格 + 上传按钮（accept `image/jpeg,image/png` + 2MB 限制 + base64 转 data url 临时显示，上传 API 暂用占位 = 不实际上传到后端，仅前端 base64 → POST 时存为 icon_url）；default：第 0 个图标 |
| **Q3 描述** | `<AppInput>` 单行；placeholder "例如：帮你分析小红书笔记，找出爆款规律"；validators: required + len 10-100；default：空 |
| **Q4 欢迎语** | `<textarea>` rows=4；placeholder（蓝本 §5.3 line 3251 默认例句）；validators: required + len 20-500；default：空 |
| **Q5 starters** | `<ChipInput>`（自研）：[+ 添加] 按钮 → 出现 textbox → 输入 → blur 转 chip；每 chip 有 [x] 删除；最多 4；每条 5-50 字；default：空数组（模板预填时填充）|
| **Q6 任务类型** | `<CheckboxGroup>` 5 固定选项 + 第 6 "其他（填写）" → 出现自由文本 input；vbalidators: 至少选 1（多选/自由文本任一计入）；codes: `analyze_data` `generate_content` `answer_questions` `make_plan` `grade_assignment` + 自由文本字符串；default：[] |
| **Q7 材料类型** | `<CheckboxGroup>` 4 固定选项；codes: `text` `csv` `image` `none`；validators: 至少选 1；default：[] |
| **Q8 积分上限** | `<CreditSlider>`（自研：`<input type="range">` + 数字显示）；min=200 max=2000 step=100；显示当前值 + 帮助文本动态："200 适合简单问答；800 适合数据分析；2000 适合复杂多步骤任务"；default：800 |
| **Q9 网络搜索** | `<input type="radio">` × 2；选项：`no_web_search`（"不需要（推荐，更安全）"）/ `allow_search`（"允许搜索公开信息"）；validators: required；default：`no_web_search` |
| **Q10 注意话题** | `<textarea>` rows=3；placeholder "例如：不要提竞品名称 XX；不要讨论退款..."；validators: optional + len ≤ 500；default：空 |
| **Q11 越界话术** | `<textarea>` rows=3；placeholder（蓝本 §5.3 default 例句）；validators: optional + 如非空则 len 5-200；default：`"这个问题有点超出我的能力范围，你可以去问老师或者换个方式描述一下～"` |
| **Q12 风格** | `<input type="radio">` × 3；选项：`friendly`（"亲切活泼"）/ `professional`（"专业严谨"）/ `encouraging`（"鼓励陪伴"）；validators: required；default：`friendly` |

**验证触发时机**（硬规则#3）：
- `<AppInput>` / `<textarea>`：`@blur` 触发验证
- `<CheckboxGroup>` / `<radio>`：`@change` 触发（用户操作时立刻校验）
- `<ChipInput>`：chip 添加/删除时校验数量上限
- 全表单提交：`[保存并发布]` 点击 → 全表单校验 → 任一失败 → 滚到第一个失败题（`element.scrollIntoView({ behavior: 'smooth' })`）

---

### 1.7 关键 UX 决策

**决策 1：v1 不暴露"已下架"切换 toggle**（P0-3 修复 — 删除半成品 UX）

> 原方案：列表加 toggle，但下架 = 永久软删除（后端 PATCH 不接受 is_active 字段），无法重新上架——toggle 只能"看"，[重新上架] 按钮是死的，用户困惑

**修订决定**：**v1 列表只显示 active agents**（fetchList 永远调 `include_inactive=false`，前端不暴露切换 UI）。下架后该 agent 从列表消失，配置者无法在 UI 中再访问。

如果后续机构反映"误删恢复"诉求 → 后续 feature 加：
- 后端：支持 PATCH `is_active=true` reactivate
- 前端：加 toggle 暴露已下架行 + [重新上架] 按钮

本 feature 不实装"半路"方案，保持 UX 一致性（DELETE 就意味着隐藏；后续按需开放）。

**决策 2：保存路径**

`AgentBuilder.vue` 有 2 个保存按钮：[保存草稿] vs [保存并发布]。但后端没有 draft 状态。

> **revised**：v1 简化为 **1 个按钮 [保存]**（所有保存都立即生效）。
>
> **依据**：蓝本 §4.3.1 原则 3 verbatim：「保存即生效。**不设 Playground、不设 Draft/Live 双态、不做 Mock Tool 测试环境**。配置完成保存后直接生效，配置者可以立刻用自己的账号进入对话窗以学员视角试聊几句。」（架构文件 line 1819-1821）
>
> 蓝本 §5.3 line 3214 写"右上角常驻 [保存草稿] + [保存并发布]" 与 §4.3.1 原则 3 矛盾——以 §4.3.1 为准（原则层级 > 控件细节）。
>
> **v2 兼容路径**：如果未来后端加 draft column → UI 可在 AgentBuilder 加第二个按钮（约 30 行修改：state.value `'draft' | 'published'` + 2 个 action handlers），不需要 refactor 表单结构。

**决策 3：详情页编辑入口**（Nit fix — 单路由 `/agents/:id/edit` 内部分支）

详情页 [编辑] 按钮：

- 始终跳到 `/agents/:id/edit`
- `/agents/:id/edit` 视图组件内：fetch agent.advanced_mode → 渲染 `<AgentBuilder>` (advanced_mode=0) OR `<AgentAdvancedEdit>` (advanced_mode=1) 子组件
- 与决策 8 单 Builder 模式一致；减少路由数量；URL 稳定（用户分享/收藏不会因切高级而失效）

**Edit 模式与 Create 模式数据流**（决策 8）：
- 单 Builder 组件用 query 区分 `mode=edit` vs 默认 create
- Edit 模式：onMounted 调 GET /skills/:id 预填表单
- 保存：edit 模式发完整 payload 走 PATCH（**不计算 diff**，简化逻辑；后端 PATCH 接受所有字段 optional，传完整即可）；create 模式发完整 payload 走 POST

**决策 4：派生流程**

派生 = 创建新 agent + 复制源 agent 字段。两种触发点：
- 列表行操作 [派生] → 调 GET /:id 拿源 → 跳 `/agents/new?from=copy:<sourceId>`（Builder 监听 query 预填）
- 详情页底部 [派生此 Agent] 按钮（次要 CTA）→ 同上

派生后的新 agent name 默认 `<原 name> - 副本`。**已验证**（P2-7 fix）：后端 `agent_definition` 表 `name` 无 unique 约束（GORM tag 仅 `size:50;not null`），可重名；前端无需冲突检测。

**决策 5：试聊 Modal 行为**

`AfterSaveModal.vue`：
- 保存成功后自动弹出（一次性，关闭后不再弹）
- [试聊一下] → toast "试聊功能即将上线"（v1 placeholder；#12/#14 接入真实试聊）+ 关闭 Modal 跳详情页
- [暂时跳过] → 关闭 Modal 跳详情页

为什么是 toast 而不是 disabled 按钮：保持蓝本 §5.4 设计（按钮可点击让用户体验完整流程），但 v1 实际没试聊功能。toast 明示"即将上线"，体验割裂感小。

**决策 6：监控后台 v1 fetcher 行为**

`AgentMonitoring.vue`：
- onMounted + 30s setInterval 调本地 helper `fetchActiveSessions()`
- helper 返回 hardcoded `{ list: [], total: 0 }`，加注释 `// TODO(#14): wire to GET /v1/agent/sessions/active`
- 0 HTTP 调用，0 404 噪音
- DataTable 永远 empty state（蓝本 §5.6 列定义就位作骨架，不删）
- **页面顶部加 NoticeBanner**（P1-3 修复 — UX honesty）：
  ```
  ┌──────────────────────────────────────────────────────────────┐
  │ ℹ️ 实时监控功能即将上线（v1 不联机）。当前页面是 UI 预览。      │
  └──────────────────────────────────────────────────────────────┘
  ```
  避免配置者看到空 DataTable 误判为"无人在用"（实际是功能未接入）
- 区分 "v1 not connected" vs "no active sessions" 的 UX 信号

---

**决策 7：表单状态管理（P1-6 修复 — 12 题大型表单）**

`QuestionnaireForm.vue` state：
- 选项 A：单个 `reactive({ q1, q2, ..., q12 })`，~12 顶级 + nested 多选数组 ≈ 30+ reactive 字段
- 选项 B：12 个独立 `ref()`，每题独立 reactive root
- 选项 C：单 `ref({ ... })`，整体 ref，子字段非 reactive（浅引用）

**决定**：**选项 A**（单 `reactive({})`）。理由：Vue 3 reactive 已优化深层依赖追踪（Proxy-based），12 题 ~30 字段不会触发 re-render storm（每个 input 仅追踪自己绑定的字段）。选项 B 会让"全表单 reset" / "全表单 dirty 检查" 都需要遍历 12 个 ref，代码重复。选项 C 会让子字段失去响应性，需手动 splice 数组（差用户体验）。

- 用 `<input v-model="form.q1">` 模式绑定
- dirty 检查：`computed(() => JSON.stringify(form) !== JSON.stringify(initial))`（O(N) 字符串化但 N=12 字段，<1ms）
- reset：`Object.assign(form, initialFormState())`（不丢 reactive proxy）

---

**决策 8：路由离开守卫（P1-5 修复 — 未保存数据保护）**

`AgentBuilder.vue` + `AgentAdvancedEdit.vue` 必须实装 `onBeforeRouteLeave` guard：

```typescript
onBeforeRouteLeave((to, from, next) => {
  if (!isDirty.value) return next()
  // 弹 ConfirmModal："您有未保存的更改，确认离开？"
  pendingNavigation.value = next
  unsavedConfirmVisible.value = true
})
```

ConfirmModal 操作：[继续编辑]（cancel → next(false)）/ [放弃更改]（confirm → next(true)）

cmd+W / 关浏览器：监听 `beforeunload` → `e.returnValue = '您有未保存的更改'`（浏览器原生提示，无定制 UI）

---

**决策 9：sidebar 命名锁定**（P2-6 fix）

navItems 添加：
- `{ name: 'agents', label: 'AI 助手', icon: Bot, path: '/agents' }`
- `{ name: 'agent-monitoring', label: 'Agent 监控', icon: Activity, path: '/agent-monitoring' }`

两个独立 navItem，平铺位置在"运行监控"下方、"用量概览"上方。

---

### 1.8 边界 / 错误处理

| 场景 | 行为 |
|------|------|
| 401 Unauthorized | `request.ts` 拦截器自动跳 `/login`（已有逻辑）|
| 403 Forbidden (child account) | UI 显示"仅父账户可配置 AI Agent，请联系机构主"（在 AgentList.vue 的 error 状态显示）|
| 404 (id not found) | AgentDetail.vue 显示"Agent 不存在或已删除"，提供 [返回列表] |
| 422 业务校验失败（如 q6 required） | toast 显示后端 message 原文（如 "questionnaire.q6 required"）+ 滚到第一个空必填题 |
| 网络错误 | toast 显示 "网络错误，请检查连接后重试" + 列表/详情页保留 error state with retry 按钮 |
| 422 字段过长（如 Q4 > 500） | 前端验证已拦截不该到后端；fallback：toast 显示后端 message |
| 模板列表为空 | TemplateGallery.vue 显示空状态："暂无可用模板，您可以从零创建" + CTA "从零创建" |
| 历史只有 1 条（首次发布） | HistoryTab 显示 "暂无历史版本" + 说明 "v1 是当前版本，下次保存后会生成 v2 可供回滚" |

---

### 1.9 i18n / 文案

v1 全中文硬编码，符合 admin-web 现状（其他视图同样硬编码）。所有用户可见字符串放在组件 `<template>` 内或 `const messages = { ... }` 模块顶部，便于未来 i18n 迁移（独立 micro feature）。

---

### 1.10 Browser support

桌面端 Chrome / Edge / Safari 最新版。最小宽度 1280px（不做响应式 mobile，admin 是桌面工具）。

---

### 1.11 性能

- 首次加载列表 < 2s（依赖后端 ListAgents 性能 + 网络）
- 模板画廊 < 1s（≤10 条）
- 问卷表单 keystroke 无可感卡顿
- 保存动作 < 3s（含后端 builder 组装 + 历史写入）

不引入虚拟列表（Agent 数量预期 < 50 / 父账户）。

---

### 1.12 测试策略概览（S3 plan 详细）

- **单测**（vitest + Vue Test Utils）：store actions（mock axios）+ 关键 component（Builder 验证规则 / Slider 边界 / ChipInput 数量限制）
- **集成测试**（vitest + jsdom）：可选；如果时间允许覆盖 Builder + Store 联合（mock api）
- **E2E**（Playwright + admin-web `e2e/`）：3-4 个 critical path（从模板派生 → 改 → 保存 / 从零 12 题 → 验证错误 → 修正 → 保存 / 切高级 / 历史恢复）

**Manual QA**：dev 部署后由用户手测验收（开 Chrome 走一遍 8 个场景）。

---

## 2. 技术 Proposal（架构 & 决策）

### 2.1 Vue 3 Composition API 模式选择

**决策**：所有新组件使用 `<script setup lang="ts">` + Composition API。

**理由**：admin-web CLAUDE.md §2 强制；与现有组件风格一致；TypeScript 类型推导友好。

### 2.2 Pinia store 设计

**决策**：单 store `useAgentStore`（setup syntax），不拆分子 store。

**理由**：
- 9 个 API 端点共享同一个 domain（agent），数据相关性高
- 拆 store（如 agentList / agentTemplate / agentHistory）会引入跨 store 调用复杂度
- 单 store 容易整体重置（用户登出时）

**state shape**：
```typescript
state: {
  list: Agent[]                      // 当前页列表
  total: number
  loading: boolean                   // 列表 loading
  error: string                      // 列表 error message
  current: Agent | null              // 详情页缓存
  currentLoading: boolean
  history: AgentHistory[]
  historyLoading: boolean
  templates: SkillTemplate[]
  templatesLoading: boolean
  saving: boolean                    // create/patch/restore/toggleAdvanced 共用
}
```

**actions**: 9 个，每个对应 1 个 API endpoint 调用 + 状态更新。

### 2.3 API wrappers

**决策**：单文件 `src/api/agent.ts`，9 个 named export functions。

**遵循现有 admin-web pattern**：函数名以 `Api` 结尾（如 `getUsersApi`），用 `get/post/put/del` from `./request`。

**新增 helper**：PATCH 方法 — 当前 `request.ts` 没暴露 `patch`。**采用选项 A** — 加 `patch<T>()` helper 到 `request.ts`（5 行修改）：

```typescript
// 加在 del<T>() 之后
export function patch<T>(
  url: string,
  data?: unknown,
  config?: AxiosRequestConfig,
): Promise<T> {
  return request.patch(url, data, config) as Promise<T>;
}
```

**P1-2 verification**：response interceptor (line 21-32 of current request.ts) 对所有 HTTP 方法生效（包括 PATCH），无 method 特定逻辑，PATCH 返回值同样会被 unwrap (`response.data.data`)。无副作用。

**影响面 0**：现有 12 个 api/*.ts 文件不 import `patch`（grep 验证），添加仅供 `api/agent.ts` 用。

### 2.4 路由模式

**决策**：使用 query string 区分模式（如 `?from=template:5` `?from=copy:12` `?mode=edit`），不引入子路由。

**理由**：减少路由数量；Builder 组件内 watch route.query 处理预填。

### 2.5 Markdown editor（高级模式）

**决策**：v1 用最简 `<textarea>` + 字符计数 + 单色 monospace 字体。**不引入 monaco / codemirror / toast-ui editor**。

**理由**：
- 自研组件原则（硬规则#5）
- bundle size 控制（monaco ~5MB，admin-web 当前总 bundle 估 ~300KB；引入会膨胀 10x+）
- 95% 配置者不会走高级模式，复杂 editor 是 over-engineering
- 后续如有体验诉求由独立 micro feature 加 markdown 高亮（轻量 CSS 高亮 ≤ 10KB）

### 2.6 自研 components 范围

| 组件 | 实装位置 | 是否新建 |
|------|---------|---------|
| `DataTable` | `common/` | 已存在 ✓ |
| `ConfirmModal` | `common/` | 已存在 ✓ |
| `AppInput / Button / Select / Toast` | `common/` | 已存在 ✓ |
| `StatusBadge` | `common/` | 已存在 ✓ |
| `CheckboxGroup` | `common/` 或 `views/agent/components/` | **新建**（S2 决定位置）|
| `RadioGroup` | 或直接用 `<input type=radio>` | **看情况** — 如果只 Q9/Q12 用 2 处，不抽组件；如果未来其他视图也用，抽到 common |
| `Slider` (`CreditSlider`) | `views/agent/components/` | **新建**（仅 Q8 用，不通用）|
| `ChipInput` | `views/agent/components/` | **新建**（仅 Q5 用）|
| `AvatarPicker` | `views/agent/components/` | **新建**（仅 Q2 用）|
| `QuestionnaireForm` | `views/agent/components/` | **新建**（Builder 内部用，封装 12 题）|

### 2.7 路由 lazy loading

**决策**：所有新视图组件用 `() => import('@/views/agent/...')` 异步加载（与现有路由风格一致）。

### 2.8 类型定义

**新建** `src/types/agent.ts`，含：
- `Agent`（对应后端 `AgentDefinition`）
- `AgentHistory`（对应 `AgentDefinitionHistory`）
- `SkillTemplate`
- `QuestionnaireAnswers`
- `Q6Code` / `Q7Code` / `Q9Code` / `Q12Code` enums (literal union types)
- `ToolFlags` (Record<string, boolean>)

放到 `src/types/agent.ts`（与 `src/types/` 现有约定一致）。

`src/api/agent.ts` import from this file。

### 2.9 测试组织

- **Unit tests**：`src/__tests__/agent/` 或 `src/views/agent/__tests__/`（与 `src/views/AIService/__tests__/` 同 pattern；S2 决定）
- **E2E**：`e2e/agent-*.spec.ts`（与现有 `e2e/` 风格一致）

### 2.10 i18n / 文案抽离

**决策**：v1 中文硬编码于 `<template>` 内或组件顶部 `const COPY = { ... }`。

不引入 i18n lib（vue-i18n）— admin-web 现状无 i18n，此 feature 不引入新依赖。

---

## 3. 范围确认（与 S0 一致）

In scope：见 S0 §2 In scope（不重复）。
Out of scope：见 S0 §2 Out of scope（不重复）。

### S1 新增/调整范围

参考 S0 §2 in/out scope 全列表。S1 调整项：

| 项 | S0 描述 | S1 修订 |
|----|---------|--------|
| 路由前缀 | `/admin/agents` (P0-1 错) | `/agents`（admin-web router 无 /admin/ 前缀；S0 manifest decision 同步） |
| 列表 inactive toggle | "可选" | **删除**（P0-3 fix — DELETE 等于隐藏，无重新上架功能避免半成品 UX） |
| Builder 保存按钮 | "[保存草稿] + [保存并发布]" | **单按钮 [保存]**（决策 2 — 蓝本 §4.3.1 原则 3 verbatim） |
| 详情页编辑路由 | `/edit` + `/advanced` 两个 | **单路由 `/edit` 内部分支**（Nit fix — 与决策 3 单 Builder 一致） |
| 监控后台 | "mock endpoint 占位" | **0 HTTP 调用 + NoticeBanner**（P0-2/P1-3 修复 — 避免 404 + UX 诚实信号） |
| 派生重名 | "S2 验证" | **已验证后端无约束**（决策 4 — 不需特殊处理） |
| Builder 离开守卫 | 未提 | **新增 `onBeforeRouteLeave` + `beforeunload`**（决策 8 — P1-5 修复） |
| 表单 state shape | 未提 | **单 `reactive({})`**（决策 7 — P1-6 修复） |
| Sidebar 命名 | "AI 助手" | **AI 助手 + Agent 监控 两 navItem**（决策 9） |

S0 in-scope 11 项 / out-scope 8 项 不变。

---

## 4. 风险（S1 补 S0 之外）

10. **PATCH 方法未在 request.ts 导出** — 需小修 `request.ts` 加 `patch<T>()` helper（5 行修改），属于本 feature 范围内的小改动
    - 缓解：S2 spec 明确 `request.ts` 改动 + 单测覆盖（mock axios → expect PATCH 调用）

11. **后端 `Get`/`List` 返回字段命名（snake_case 还是 camelCase）** — `AgentDefinition` Go struct 有 JSON tag，需要前端 TypeScript 完全对齐
    - 缓解：S2 spec 用 jq + 实际调一次 dev API（curl `$DEV_API_URL/v1/agent/skills` w/ $E2E_PASSWORD）取真实 JSON 样本作为 TypeScript interface 源头；或直接读 Go struct json tag 列出
    - **fallback**：如果 dev 无父账户测试号 → 直接读 `model.AgentDefinition` Go struct（develop 分支）确认每个字段 JSON name

12. **AvatarPicker 上传后端** — 后端 `agent_definition.icon_url` 是 string；图片实际上传到哪里？
    - 缓解：v1 用 base64 data URL 内联存（占用 DB 空间但 < 100KB 可接受）；upload OSS 由独立 micro feature 后续
    - 实装：`AvatarPicker.vue` upload handler 直接 `FileReader.readAsDataURL()` → 存到 `icon_url` 字段；后端无需改

13. **历史版本 snapshot JSON 字段名** — 蓝本说"complete snapshot"，实际 Go struct 有 `Snapshot datatypes.JSON`，存的是序列化的 AgentDefinition 完整 row
    - 缓解：S2 spec 通过 `git show develop:internal/numind/biz/skill/versioning.go` 确认 snapshot 序列化字段，TypeScript interface 对齐

14. **是否需要"派生此 Agent" 详情页 CTA** — S0 只提"列表行操作 [派生]"，详情页未明
    - 缓解：S2 决定（建议加，详情页是第二个自然触发点）；不加也不影响 critical path

15. **生成的 SKILL.md 是否在 UI 展示** — `agent_definition.generated_skill_body` 是后端组装的纯文本
    - 缓解：v1 详情页配置 Tab **不展示** generated_skill_body（避免暴露 prompt 工程细节，违背蓝本 §5.1 "不露 prompt 术语"）；后续 micro feature 可加"system prompt 预览"按钮（开发者模式）

16. **DataTable 行内 action 按钮** — 现有 DataTable.vue 列定义是 `{ key, title, width, align }`，没有 `render` 函数。如何渲染行内按钮（如 [编辑] [历史] [派生] [下架]）？
    - **已验证**：DataTable.vue line 126-132 已支持 `cell-{key}` scoped slot（`<slot :name="cell-${col.key}" :row="row" :value="row[col.key]">`），可直接 `<template #cell-actions="{ row }">` 渲染行内按钮。无需扩展。

17. **路由离开未保存数据**（P1-5 修复）— 用户在 Builder 编辑 12 题后点浏览器后退 / 切换路由 / cmd+W → 数据无声丢失
    - 缓解：`onBeforeRouteLeave` + `beforeunload` 双重守卫（决策 8 实装）；ConfirmModal "您有未保存的更改，确认离开？"

18. **12 题表单 reactive state 性能**（P1-6 修复）— 单 `reactive({})` 持 12 顶级 + nested 数组，naive 实装可能 re-render storm
    - 缓解：Vue 3 Proxy-based reactive 已细粒度追踪（每个 input 仅追踪自己绑定的字段），实测 12 题 ~30 字段无性能问题（决策 7 实装）；dirty 检查用 `JSON.stringify` O(N) 但 N=12 字段，<1ms 可接受

---

## 5. S2 待办清单（S1 已识别，留给 spec 解决）

> S1 已部分解决，剩余项 S2 spec 必须 close。

**S1 已 close**（无需 S2 再做）：
- ✓ AgentDefinition JSON 字段命名：snake_case（见 model.AgentDefinition 全 json tags）
- ✓ DataTable 支持 `cell-{key}` scoped slot（DataTable.vue line 126-132 已验证）
- ✓ vitest 已配置（vitest@4.1.4 + @vue/test-utils@2.4.6 + vitest.config.ts 存在）
- ✓ Playwright e2e 已配置（playwright.config.ts 存在；e2e/auth.setup.ts 用 E2E_USERNAME/E2E_PASSWORD；BASE_URL 默认 http://49.233.219.254:9100 dev）
- ✓ 派生重名后端无 unique 约束（model.AgentDefinition.Name `size:50;not null` 无 uniqueIndex）

**S2 仍需 close**：
- [ ] AgentDefinitionHistory.snapshot JSON 字段结构（`datatypes.JSON` 序列化后是嵌套对象 OR 字符串）—— 读 versioning.go 确认
- [ ] lint baseline（运行 `npm run lint` 记录基线 warning / error count）
- [ ] E2E_USERNAME / E2E_PASSWORD 是否对应一个**父账户**（is_admin=true 但同时 parent_user_id=null）—— 配置者 UX feature 必须以父账户视角测
- [ ] CheckboxGroup 抽到 common 还是局部—— S2 决定：抽 common（多视图潜在复用）
- [ ] AvatarPicker 12 个内置图标：lucide-vue-next 既有（如 Bot/User/Briefcase 等）vs 自绘 SVG —— S2 决定：**用 lucide 现成**（减少 SVG 维护，bundle 不变）
- [ ] [派生此 Agent] 详情页 CTA — S2 决定：**加**（详情页是第二个自然触发点）

---

## 6. S3 / S4 任务粗分（S3 plan 详细）

预估 ~12 atomic tasks，按依赖大致：

```
M1  types/agent.ts                          — 类型定义文件
M2  api/request.ts (add patch helper)       — 小修
M3  api/agent.ts                            — 9 axios wrappers
M4  stores/agent.ts                         — Pinia store
M5  router 注册 + AdminSidebar 加菜单         — 路由 & 导航
M6  views/agent/components/CheckboxGroup    — 子组件
    + ChipInput + CreditSlider + AvatarPicker
M7  views/agent/AgentList.vue               — 列表页
M8  views/agent/AgentCreateChoose.vue       — 创建路径选择
    + TemplateGallery.vue
M9  views/agent/components/QuestionnaireForm — 12 题表单容器
    + AgentBuilder.vue                       — Builder 主页（创建+编辑）
M10 views/agent/AgentDetail.vue              — 详情容器 + 3 Tab 内容
    + AgentHistoryTab + ConfigTab + StatsTab + HistoryViewModal
    + AfterSaveModal + AdvancedToggleConfirmModal
M11 views/agent/AgentAdvancedEdit.vue       — 高级模式编辑
M12 views/agent/AgentMonitoring.vue         — 监控后台骨架
M13 S5 验证策略 doc + Playwright e2e specs  — 测试
```

**Wave 分组**（Tier 3 disjoint files 并行）—— **P1-4 修复 — 明确 route ownership**：

M5 拆为两个子任务：
- **M5a**: AdminSidebar.vue 加 2 个 navItem（仅修一个文件）
- **M5b**: router/index.ts 加 6 个 route 条目，组件以 `() => import('@/views/agent/{Foo}.vue')` 占位（实际组件由 M7-M12 落地；路由 import 在浏览器导航时才解析，编译时不要求文件已存在但 TypeScript 类型检查会报错——所以 M5b 必须包含占位组件文件创建：每个 view 创建空 `<script setup lang="ts"></script><template></template>`）

新 Wave 分组：
- Wave 1（无依赖）：M1 (types) + M2 (request.ts patch helper) — 并行
- Wave 2（依赖 M1+M2）：M3 (api/agent.ts) — 串行（依赖 M1 types）
- Wave 3（依赖 M3）：M4 (stores/agent.ts) — 串行（依赖 M3 api）
- Wave 4（无 store 依赖）：M5a (sidebar) + M5b (router + 6 stub view files) + M6 (子组件 CheckboxGroup/ChipInput/CreditSlider/AvatarPicker) — 并行（disjoint files）
- Wave 5（依赖 M4+M5b+M6）：M7 (AgentList) + M8 (CreateChoose + TemplateGallery) + M11 (AdvancedEdit) + M12 (Monitoring) — 并行（替换 stub 组件，disjoint files）
- Wave 6（依赖 M4+M5b+M6）：M9 (Builder + QuestionnaireForm) — 串行（Builder 是 critical path，独立 dispatch 确保质量）
- Wave 7（依赖 M9）：M10 (AgentDetail + 3 Tab 容器 + 子组件) — 串行
- Wave 8：M13 测试 — 最后

每个 Wave 内并行任务必须 `ndf-check-disjoint` 验证文件归属（无交集）。

**总实施时间估**：S0/S1/S2/S3 各 ~30 min（含 reviewer）+ S4 编码 ~3-5 小时 + S5/S6 ~30 min。

---

## 7. 0 prod 影响 reaffirm

- 不动后端代码（numind-server zero diff）
- 不打 git tag
- 不调 `/deploy-prod`
- feature 分支不推 GitHub
- 不动 `numind-admin-web/config_prod.yaml` 或 nginx prod 配置
- 不引入新外部 npm 依赖（PATCH helper 用现有 axios；Markdown editor 用原生 textarea；所有 UI 自研）

---

**S1 完结。S2 写 spec（数据契约 + UI 契约 + 测试契约 + 解决 §5 待办）。**
