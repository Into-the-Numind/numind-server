# Proposal: agent-mode-configurator-relocate (S1)

> Track: **standard** · Stage: **S1** · Repos: **numind-admin-web + numind-web-v3** · 2026-05-22

---

## 1. 一句话总结

把 #10 (`fdebd7b`) 落在 numind-admin-web 的 7 个 Agent builder view（不含 AgentMonitoring，第 8 个 view 保留 admin-web）+ 11 个 components + 4 个单测 + 9 个 user_token endpoint wrapper + Pinia store **搬迁**到 numind-web-v3 的 `src/views/config/agents/` 下，让父账户（B 端机构主）真的能配置自己的 Agent；admin-web 保留 AgentMonitoring + 合规规则 + 2 个 admin_token endpoint（Numind 员工用）。**0 后端改动 / 0 prod 部署。**

---

## 2. PRD（用户视角）

### 2.1 用户故事

**Story 1 — 父账户创建第一个 Agent**

> 作为一名 B 端机构主，我登录 https://youshu.asia 后，点击侧边栏「配置管理」→ 顶部 tab「AI 助手」→「+ 创建 Agent」→ 选模板「学习陪伴者」→ 改助手名字为「我的小派」→ 点击「保存并发布」→ 看到「✅ 助手已发布」弹窗 → 点「试聊一下」→ 进入对话窗体验。

- **当前**：❌ 父账户在 web-v3 找不到任何 Agent 配置入口
- **修复后**：✅ /config/agents/* 是父账户视角

**Story 2 — 父账户编辑已有 Agent**

> 我看到列表里有「爆款分析师 v3」，想改欢迎语 → 点「编辑」→ 进入 12 题问卷（已预填）→ 改 Q4 → 保存。

- **当前**：❌ 不可达
- **修复后**：✅ /config/agents/:id/edit

**Story 3 — 父账户回滚版本**

> 我把欢迎语改坏了，想回退 → 进入「助手详情」→「历史版本」Tab → 点 v3 旁的「恢复」→ 确认 Modal → 完成。

- **当前**：❌ 不可达
- **修复后**：✅ /config/agents/:id（详情页 3 Tab：基本配置 / 历史版本 / 使用数据）

**Story 4 — 子账户被拦截**

> 我以学员（子账户）身份登录，路径栏输入 /config/agents → 自动 redirect 到 / 首页（看不到 "AI 助手" tab）。

- **当前**：❌ 子账户能在前端看到学员 view 但不会看到 admin-web 的"配 Agent"入口（因为根本进不去 admin-web）
- **修复后**：✅ 子账户在 web-v3 ConfigLayout tabs 看不到「AI 助手」，路由层 redirect 兜底，后端 biz 层 403 兜底

**Story 5 — Numind 员工监控全平台**

> 我以 Numind 员工身份登录 admin-web，点击 sidebar「Agent 监控」 → 看到所有 tenant 的 running agent_run 列表，能 「查看 Trace」（跳 Langfuse）和「强制取消」运行。

- **当前**：✅ 已 wire 真实数据（#14）
- **修复后**：✅ 不变（本 feature 不动这部分）

### 2.2 非用户故事的隐性变化

- 父账户登录 web-v3 后**右上角无新增 banner**（不打扰已有 UI）
- 学员视角 `/agent`、`/agent/history`、`/agent/chat/:sessionId` 三个路由**完全不动**（#11 落地）
- admin-web 主导航上**"AI 助手"图标消失**，但「Agent 监控」+「合规规则」保留

---

## 3. 范围（What this feature delivers）

### 3.1 admin-web 端

#### 删除（17 个文件 + 9 个 endpoint 函数）

| 类型 | 路径 | 备注 |
|------|------|------|
| view | `src/views/agent/AgentList.vue` | 整文件删 |
| view | `src/views/agent/AgentCreateChoose.vue` | 整文件删 |
| view | `src/views/agent/TemplateGallery.vue` | 整文件删 |
| view | `src/views/agent/AgentBuilder.vue` | 整文件删 |
| view | `src/views/agent/AgentDetail.vue` | 整文件删 |
| view | `src/views/agent/AgentEdit.vue` | 整文件删 |
| view | `src/views/agent/AgentAdvancedEdit.vue` | 整文件删 |
| components | `src/views/agent/components/*.vue` (10 个) | 全删 |
| utility | `src/views/agent/components/validation.ts` | 整文件删 |
| spec | `src/views/agent/__tests__/AgentList.spec.ts` | 整文件删 |
| spec | `src/views/agent/__tests__/AgentBuilder.spec.ts` | 整文件删 |
| spec | `src/views/agent/__tests__/AgentAdvancedEdit.spec.ts` | 整文件删 |
| spec | `src/views/agent/__tests__/AgentHistoryTab.spec.ts` | 整文件删 |
| store | `src/stores/agent.ts` | 整文件删 |
| api | `src/api/agent.ts` | 删 9 个函数 (createAgentApi/listAgentsApi/getAgentApi/patchAgentApi/deleteAgentApi/listAgentHistoryApi/restoreAgentApi/toggleAgentAdvancedApi/listSkillTemplatesApi) 和相应 import；保留 2 个 admin 函数 (listAgentRunsApi/cancelAgentRunApi) |
| sidebar | `src/components/layout/AdminSidebar.vue` | 删 navItems 中 `{ name: "agents", label: "AI 助手", icon: Bot, path: "/agents" }` 一项；保留 agent-monitoring 项 |
| router | `src/router/index.ts` | 删 6 条 /agents/* 路由 (agents-builder/agents/agents-new/agents-from-template/agents-detail/agents-edit)；保留 agent-monitoring + compliance-rules |
| types | `src/types/agent.ts` | 删与 builder 相关类型，保留 `AgentRunDTO` 等 monitoring 用类型 — 具体行级删除在 S2 spec 标注 |

#### 保留（不动）

- `src/views/agent/AgentMonitoring.vue` — Numind 员工监控全平台 agent_run
- `src/api/agent.ts` 中 `listAgentRunsApi` + `cancelAgentRunApi` 函数 + 必要的 import (`AgentRunDTO`, `ListResponse`)
- `src/views/compliance/*` 全部 — Numind 员工管理 L1 合规规则
- AdminSidebar 中 "Agent 监控"（Activity icon）和 "合规规则"（ShieldCheck icon）两个 navItems
- router 中 /agent-monitoring + /compliance-rules/* 全部路由

### 3.2 web-v3 端

#### 新建（24+ 文件）

| 类型 | 新路径 | 来源 | 适配 |
|------|--------|------|------|
| view | `src/views/config/agents/AgentList.vue` | admin-web AgentList.vue | DataTable → 自定义 HTML 表格（仿 ChatbotList.vue 风格）；useToast → useNotificationsStore |
| view | `src/views/config/agents/AgentCreateChoose.vue` | admin-web 同名 | router path 改 /config/agents/* |
| view | `src/views/config/agents/TemplateGallery.vue` | admin-web 同名 | router path |
| view | `src/views/config/agents/AgentBuilder.vue` | admin-web 同名 | router path + useToast |
| view | `src/views/config/agents/AgentDetail.vue` | admin-web 同名 | router path + useToast |
| view | `src/views/config/agents/AgentEdit.vue` | admin-web 同名 | router path |
| view | `src/views/config/agents/AgentAdvancedEdit.vue` | admin-web 同名 | router path + useToast |
| component | `src/views/config/agents/components/AdvancedToggleConfirmModal.vue` | admin-web 同名 | 检查 ConfirmModal import 一致 |
| component | `src/views/config/agents/components/AfterSaveModal.vue` | 同 | router path 试聊跳学员视图 (`/agent/chat/...` 由父账户身份触发；v1 用占位 toast，参见 #10 决策 S0-D... — 详 S2) |
| component | `src/views/config/agents/components/AgentConfigTab.vue` | 同 | — |
| component | `src/views/config/agents/components/AgentHistoryTab.vue` | 同 | — |
| component | `src/views/config/agents/components/AgentStatsTab.vue` | 同 | v1 空占位（监控数据 v2 引入） |
| component | `src/views/config/agents/components/AvatarPicker.vue` | 同 | — |
| component | `src/views/config/agents/components/ChipInput.vue` | 同 | — |
| component | `src/views/config/agents/components/CreditSlider.vue` | 同 | — |
| component | `src/views/config/agents/components/HistoryViewModal.vue` | 同 | — |
| component | `src/views/config/agents/components/QuestionnaireForm.vue` | 同 | — |
| utility | `src/views/config/agents/components/validation.ts` | 同 | — |
| spec | `src/views/config/agents/__tests__/AgentList.spec.ts` | 同 | import 路径 + useToast→notifications mock + happy-dom→jsdom 适配（如需） |
| spec | `src/views/config/agents/__tests__/AgentBuilder.spec.ts` | 同 | 同 |
| spec | `src/views/config/agents/__tests__/AgentAdvancedEdit.spec.ts` | 同 | 同 |
| spec | `src/views/config/agents/__tests__/AgentHistoryTab.spec.ts` | 同 | 同 |
| api | `src/api/agentBuilder.ts` | admin-web `src/api/agent.ts` 中 9 个 user endpoint 函数 | request 实例从 `./request` 单例 / 函数命名去 `Api` 后缀 / 函数风格沿用 web-v3 的 named export pattern |
| store | `src/stores/agentBuilder.ts` | admin-web `src/stores/agent.ts`（已 setup syntax） | store id `'agent'` → `'agentBuilder'`；import 改 `@/api/agentBuilder` + `@/types/agentBuilder` |
| types | `src/types/agentBuilder.ts` | admin-web `src/types/agent.ts` 中 builder 相关 type | 保留 Agent / AgentHistory / SkillTemplate / CreateAgentPayload / PatchAgentPayload / QuestionnaireAnswers / ToolFlags / ListResponse / normalizeQuestionnaire 等；**剥离 AgentRunDTO** 等 monitoring 类型（留 admin-web） |
| router | `src/router/index.ts` | 新增 | 顶层加 7 条 `/config/agents/*` 路由 (list/create/from-template/builder/detail/edit/advanced-edit)；meta.parentOnly = true；放在 `/config/...` ConfigLayout 父路由 children 中（与 chatbots / sop-templates / knowledge-bases 并列） |
| layout | `src/views/config/ConfigLayout.vue` | 现有 | `tabs` 改 computed，按 isParentUser 过滤；新增 `{ label: 'AI 助手', path: '/config/agents', parentOnly: true }` |

#### 不动（学员视角 #11 落地）

- `src/views/agent/AgentChatView.vue` / AgentHistoryView.vue / AgentSelectView.vue — 3 view 保留
- `src/api/agent.ts` — 学员端 13 个 endpoint 保留
- `src/stores/agentChat.ts` — 学员 chat 状态保留
- 顶层路由 /agent、/agent/history、/agent/chat/:sessionId — 保留

### 3.3 numind-server 端

**0 改动。** 9 个 `/v1/agent/skills/*` user_token endpoint + 2 个 `/v1/admin/agent-runs*` admin_token endpoint 全部保持现状。

---

## 4. 关键决策（在 S0 上细化）

> 决策编号策略：D1-D10 继承/细化 S0 决策；D11-D14 为 S1 新增（针对 S0 reviewer 提出的实现细节问题）。

### D1（locked）目录命名 = `src/views/config/agents/`

- 与现有 chatbots/sop-templates/knowledge-bases 并列，IA 最自然
- 学员 `/views/agent/` 不冲突（顶层目录 vs `/views/config/agents/` 子目录）

### D2（locked）菜单入口 = ConfigLayout tab，computed 过滤

- `tabs` 从 `const` 改 `computed`
- `userInfo === null` 时**默认隐藏** parentOnly tab，避免 flash
- `userInfo` 加载完后再按 `isParentUser` 决定显隐

### D3（locked）API 文件 = `src/api/agentBuilder.ts`

- 与现有 `src/api/agent.ts`（学员端）分离
- 9 个函数命名去 `Api` 后缀（如 `createAgent` 而非 `createAgentApi`），对齐 web-v3 命名习惯（学员端 `listAvailableAgents` 而非 `listAvailableAgentsApi`）
- 文件头注释明确"父账户配置者 API，与学员端 agent.ts 区分"

### D4（locked）Store 文件 = `src/stores/agentBuilder.ts`

- store id 为 `'agentBuilder'`
- setup syntax，从 admin-web 直接搬（admin-web 也是 setup syntax，仅 import 路径改）

### D5（refined per S0 P2-1）Types 文件 = `src/types/agentBuilder.ts`

- 新建文件，与 D3/D4 命名一致
- 不合并到 `src/types/agent.ts`（学员端）— 学员端有自己的 `Agent` type（含 `AgentSkillListResponse` 等学员视角字段），与父账户的 `Agent` type 字段集不同
- **S2 必须明确**：admin-web `src/types/agent.ts` 哪些 type 是 monitoring 用（留 admin-web），哪些是 builder 用（搬走）

### D6（refined per S1 P0-2 + P0-3 + P1-3）路由守卫三道防线（实施细节）

**meta key 用 `requiresParent: true`（与现有 /config/* 家族一致），不引入新的 `parentOnly`。** 实测 router/index.ts:226 现有守卫已经兼容 `(parentOnly || requiresParent)`，但为避免新代码与现有约定漂移，本 feature 统一用 `requiresParent`。

**router beforeEach 改为 async**（实测当前是 sync `(to, from, next) =>`，需改造）：

```typescript
router.beforeEach(async (to) => {
  const userStore = useUserStore()
  if (!to.meta.guest && !userStore.isLoggedIn) return { name: 'login', query: { redirect: to.fullPath } }
  if (to.meta.guest && userStore.isLoggedIn && to.name === 'login') return { name: 'home' }
  // ✱ 新增：requiresParent 判定（含 userInfo 未就绪时的 await）
  if (to.meta.requiresParent) {
    if (!userStore.userInfo) await userStore.fetchUserInfo()
    if (!userStore.isParentUser) {
      useNotificationsStore().info('AI 助手配置仅父账户可访问')
      return { name: 'home' }
    }
  }
  document.title = ...
})
```

注意：guard 由 `(to, from, next) =>` 改成 `async (to) =>`，**不再用 `next()` 参数**（Vue Router 4 推荐写法）。返回值 = 重定向目标 或 `undefined`/`true` = pass。S2 spec 必须给出此完整改写代码，S4 implementer 严格按写。

**三道防线：**

1. **ConfigLayout tabs computed** — userInfo 未就绪时默认隐藏 parentOnly tab
2. **router beforeEach async guard** — `requiresParent` meta 触发 await + isParentUser 判定 + redirect with toast
3. **后端 biz 层 403** — 兜底，子账户调用 9 个 endpoint 时返回 403

### D7（locked）AgentMonitoring 留 admin-web

- 已 wire `listAgentRunsApi` + `cancelAgentRunApi`（admin_token），是 Numind 员工真实功能
- 父账户视角监控 v2 引入（需新后端 endpoint `/v1/agent/skills/:id/runs`）

### D8（locked）合规规则 CRUD 留 admin-web

- L1 规则 v1 由 Numind 员工代为配置（服务化），简化产品
- v2 可考虑给父账户开放 L1 子集

### D9（refined per S1 P1-2）单测搬迁 + 子账户验证策略 + mock 完整清单

- 4 个 admin-web spec 全部搬到 `src/views/config/agents/__tests__/`
- import 路径适配（`@/api/agent` → `@/api/agentBuilder`，`@/stores/agent` → `@/stores/agentBuilder`，`@/types/agent` → `@/types/agentBuilder`）

**`vi.mock` 完整等价物清单**（覆盖 4 个 spec 中出现的所有）：

| admin-web mock | web-v3 等价 | 出现 spec |
|---------------|-------------|----------|
| `vi.mock('@/api/agent', ...)` | `vi.mock('@/api/agentBuilder', ...)` | AgentList.spec.ts / AgentBuilder.spec.ts / AgentHistoryTab.spec.ts |
| `vi.mock('@/composables/useToast', ...)` | `vi.mock('@/stores/notifications', () => ({ useNotificationsStore: () => ({ success: vi.fn(), error: vi.fn(), info: vi.fn() }) }))` | 4 spec 都用 |
| `vi.mock('@/stores/agent', ...)` | `vi.mock('@/stores/agentBuilder', ...)` | AgentAdvancedEdit.spec.ts |
| `vi.mock('vue-router', ...)` | 同（vue-router 包不变） | AgentBuilder.spec.ts / AgentAdvancedEdit.spec.ts |
| import `@/components/common/ConfirmModal` | 同（web-v3 也有 ConfirmModal）| 多 spec |

**子账户守卫验证走 Vitest 单测：**
- mock user store 设置 `userInfo.value = { parent_user_id: 123 }`
- 断言 ConfigLayout `tabs.value` 不含 "AI 助手"
- 断言 router beforeEach 在 `to.meta.requiresParent` + `isParentUser=false` 时返回 `{ name: 'home' }`

**父账户主流程走 Playwright e2e 单账户**（用 `$E2E_USERNAME`/`$E2E_PASSWORD`）：登录 → 进 /config/agents → 列表 → 创建 → 保存 → 详情 → 删除
- 注意：S5 验证策略由 S3 plan 最后一个 task 锁定（NDF 规则 10）

### D10（locked）Lint baseline 沿用

- S4 开始前各 worktree 跑 `npm run lint` 记录 baseline
- S5 验收 = "warning ≤ baseline + error == 0"

### D11（refined per S1 P0-1）admin-web `DataTable.vue` 搬到 web-v3 `src/components/common/`

**Background：** admin-web `AgentList.vue` 用 `<DataTable :columns :data :loading :total :page :pageSize @page-change>` 组件 + `<template #cell-name>` 等 named slot。该组件 384 行（含 thead/tbody/pagination/empty-state/loading skeleton/click-row emit），实测 AgentList.vue 262 行。

**Trade-off 重测（reviewer P0-1 修正）：**

| 选项 | AgentList.vue 行数 | 新增 web-v3 文件 | 实际 net 行数变化 |
|------|-------------------|-----------------|-----------------|
| A. Port DataTable | ~262（基本不动） | DataTable.vue 384 行 | +646 |
| B. ChatbotList custom table 风格 | ~475+（含 search/badge/derive 按钮） | 0 | +475+ |
| C. 极简 table（无 pagination/skeleton） | ~350 | 0 | +350 |

ChatbotList.vue 实测 475 行（不含 AgentList 必需的 search/badge/derive 按钮）。reviewer 实测纠正 S1 P0-1：用 custom table 是**净增 213+ 行**而不是减少。

**决定（refined）：** **port DataTable.vue 到 web-v3 `src/components/common/DataTable.vue`**。原因：

1. DataTable 是独立可复用组件，未来 web-v3 SOP/Chatbot/KB list 也可统一为 DataTable 风格（**本 feature 不重构 ChatbotList，留 out-of-scope**）
2. AgentList.vue 搬过去基本零改动（仅改 router path + import 路径）
3. 单文件新增 vs AgentList 块状嵌套膨胀，可读性 + 可审 review 性更好
4. DataTable 的依赖是 `lucide-vue-next`（web-v3 已用）+ 标准 Vue 3 API，无新外部依赖

**S2 spec 必须给出：**
- DataTable.vue 搬迁后是否需要 CSS variable 改动（admin-web `--text-secondary` vs web-v3 实际有的 CSS variable，如不一致需 inline 改一遍）
- AgentList.vue 的 5 列 column 定义：name+badge / description / version / updated_at / actions
- search 输入框单独写在 AgentList.vue 顶部（不动 DataTable），过滤 list 数组（与 admin-web 一致 — S0 §2.4 决策 s0-1 client-side filter）

### D12（新增）useToast → useNotificationsStore 适配模式

- admin-web 用 `import { useToast } from '@/composables/useToast'`，调用 `toast.success(msg)`
- web-v3 用 `import { useNotificationsStore } from '@/stores/notifications'`，调用 `notifications.success(msg)`（参考 `ChatbotList.vue:127`）
- 适配方法：所有搬过来的 view/component 中替换 `useToast()` → `useNotificationsStore()`，方法调用保持 `success/error/info`（API 一致）
- 单测中 mock 也要相应替换

### D13（新增）AfterSaveModal 试聊跳转

- admin-web v1 是 toast 占位（"试聊功能即将上线"）
- web-v3 父账户视角下「试聊」语义是用父账户身份跳学员视图 `/agent/chat/...`（用父账户身份扮演学员）
- v1 暂保持占位，**不接通跳转**（接通涉及 admin_test source_type 的 #12 真实流程，未落地）
- v2 接通后改

### D14（refined per S1 P1-1）vitest 配置差异 + Teleport 处理

- admin-web `environment: 'happy-dom'`
- web-v3 `environment: 'jsdom'`
- 4 个 spec 搬过来后用 jsdom 跑，**风险**：Teleport / focus 等 DOM 行为差异

**Teleport 处理策略统一化**（reviewer 实测）：
- AgentHistoryTab.spec.ts 用 `attachTo: document.body`（mount option）
- AgentAdvancedEdit.spec.ts 用 `Teleport: true` stub（global stubs）

**搬到 web-v3 后统一改为 `Teleport: true` stub**（去掉 `attachTo: document.body`）：
- 原因：jsdom 下 stub 是更稳定的方案，避免 attachTo 依赖 document.body 在测试 cleanup 时未清理引起 mem leak
- 修改点：
  - AgentHistoryTab.spec.ts 第 97, 136 行删 `attachTo: document.body`，mount option 加 `global.stubs.Teleport = true`
  - AgentAdvancedEdit.spec.ts 已经用 stub，保留

---

## 5. 接受标准（S5）

### admin-web

- `npm run lint` exit 0
- `npm run type-check` exit 0
- `npm run build` exit 0
- 删除文件后无残余 import 引用（`grep -r "from '@/views/agent/Agent[BCDE]" src/` 应只命中 AgentMonitoring 相关）
- `/agents` 路由访问返回 NotFoundView
- AdminSidebar 不展示 "AI 助手" 菜单项
- "Agent 监控" + "合规规则" 菜单项可正常进入

### web-v3

- `npm run lint` exit 0
- `npm run type-check` exit 0
- `npm run build` exit 0
- 父账户 e2e PASS：登录 → /config/agents → 创建模板派生 Agent → 保存 → 详情 → 历史 → 删除
- 子账户 Vitest PASS：tabs 过滤 + router guard redirect
- userInfo === null Vitest PASS：tabs 不闪烁 "AI 助手"
- 学员视角 e2e PASS（沿用 #11 已有）

### Prod

- 0 行 config_prod.yaml 改动
- 0 行 migrations/*.sql 改动
- 0 行后端代码改动
- 0 prod 部署

---

## 6. 不在范围（明确划线）

- 后端 API 改 / 新 endpoint
- AgentMonitoring 搬到 web-v3（v2 引入父账户视角监控）
- 合规规则 CRUD 搬到 web-v3（v2 可考虑）
- 12 题问卷设计变化（沿用蓝本 §5.3 canonical）
- 试聊真实跳转接通（v2）
- 真实双账户 Playwright e2e（用 Vitest 单测验证子账户守卫）
- prod 部署 + git tag

---

## 7. 风险（细化）

### R1. admin-web `src/api/agent.ts` 拆分边界混乱

**缓解**：S2 spec 必须给出该文件**最终状态** snippet（保留哪 2 个函数 + 保留哪些 import + 删除哪些）。S4 reviewer 用 file diff 验证。

### R2. `src/types/agent.ts` 拆分丢失类型导致 type-check 失败

**缓解**：S2 spec 列出 admin-web `src/types/agent.ts` **每个**导出 type 的去向（保留 admin-web / 搬 web-v3 / 双方都需要）。

### R3. AgentList 表格风格转换工作量被低估

**缓解**：S3 plan 把 AgentList 单独作为 1 个 task（不和其他 view 混），独立 review。

### R4. useNotificationsStore vs useToast API 不完全一致

**缓解**：S2 spec 必须 grep `useToast` 在 admin-web 7 个 view + 11 个 component 中的所有调用点，逐一映射。

### R5. ConfigLayout tab + router guard 实现 bug 导致父账户看不到 tab

**缓解**：S4 task 完成后 reviewer 必须用真实 user store mock 跑 unit test 验证 tabs 和 guard 行为。

### R6. happy-dom → jsdom 差异让搬过来的 spec 失败

**缓解**：S4 中 spec 适配 task 独立于 view 适配，并跑 `npm run test` 直接确认 PASS。失败的 spec 列入 P1 修复。

### R7. 子账户登录时通过浏览器 history 直接访问 /config/agents

**缓解**：router beforeEach guard 兜底 + 后端 biz 层 403 兜底。即使 tab 显示 bug，最终页面也会因 API 调用失败显示 error state（不会泄露父账户数据）。

### R8. 并行 NDF feature 引起 manifest 冲突

**当前**：sop-stepnav-bookmark-star hotfix 同时跑（独立仓库 + 独立 worktree，互不影响）
**缓解**：本 feature 不与 hotfix 共享文件；manifest.yaml 在 numind-server develop 上由各 session 顺序 commit（已观察到 [feedback_parallel_ai_sessions.md](feedback_parallel_ai_sessions.md) 的并发场景）。

---

## 8. 估算

| 阶段 | 估算 | 关键产出 |
|------|------|---------|
| S0 | 完成 | requirements + manifest |
| S1 | 进行中 | 本文档 |
| S2 | 1.5h | 文件级映射 spec + admin-web/web-v3 类型边界 |
| S3 | 1.5h | 14 个 task + Wave 拆分 + ndf-check-disjoint + S5 策略 |
| S4 | 6-8h | 实施 14 task，每个 task ≤ 30 min + 2 并行 review |
| S5 | 1h | lint + type-check + unit test + smoke |
| S6 | 30min | merge 2 仓库 + cleanup |

Total ≈ 10-12 hours single-AI session pipeline。

---

*Created 2026-05-22 14:30 +0800 · ai-s1*
