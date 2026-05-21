# Feature: agent-mode-configurator-relocate (S0 Requirements)

> Track: **standard** · Stage: **S0** · Repos: **numind-admin-web + numind-web-v3** · Created: 2026-05-22

---

## 1. 问题陈述（Problem Statement）

**#10 agent-mode-configurator-ux (merged commit `fdebd7b` in numind-admin-web, 2026-05-21) 把 Agent 配置者 UX 落地在了错误的仓库。**

### 1.1 三方关系（蓝本 §3）

| 角色 | 登录的 webapp | JWT 类型 | 用途 |
|------|--------------|---------|------|
| Numind 公司员工（运营/客服/产品/开发）| numind-admin-web (port 5174/9100) | admin_token | 内部管理后台：客户管理、SOP 模板审核、全平台数据看板 |
| 父账户（B 端机构主，例如培训机构）| numind-web-v3 (port 5173/9200) | user_token，`parent_user_id=null` | 为自己机构配业务（SOP/Chatbot/知识库/Agent），帮子账户开通会员 |
| 子账户（C 端学员）| numind-web-v3 | user_token，`parent_user_id=<父 ID>` | 消费父账户配好的业务 |

### 1.2 错位的本质

蓝本 §5 标题"配置者 UX"中的"配置者"指**父账户**（B 端机构主），不是 Numind 公司员工。但 #10 落地时（之前 spawn 的 session）把"配置者"误解成"管理端 admin UI"，全部 8 个 view + 11 个 component + 4 个单测 + Pinia store + axios API wrapper 都放在了 `numind-admin-web/`。

**导致的产品后果：**

1. **父账户根本无法登录 admin-web** — 两个 webapp 的账号体系**完全独立隔离**：`/v1/web/login` 颁发 user_token，存到 web-v3 的 localStorage；`/v1/admin/login` 颁发 admin_token，存到 admin-web 的 localStorage。父账户的注册流程没有创建 admin 账号，因此没有进入 admin-web 的物理路径——既不存在"父账户在 admin-web 输入密码被拒"，也不存在"父账户 token 类型不对"，而是父账户压根从未走进过 admin-web 这个 webapp。
2. **后端 9 个 `/v1/agent/skills/*` endpoint 是 user_token middleware** — 即使 admin-web 内部用了 user_token，admin 这个用户在 biz 层 `GetParentSkillsByOwner(user_id)` 也查不到任何 agent（admin 不是任何机构的 parent_user）。
3. **当前 #10 → #14 的 Agent 模式 14-feature 项目产品链路是断的** — 父账户没有任何 UI 入口去创建/编辑/管理自己的 Agent；子账户在 web-v3 学员视角（#11 落地的 3 view）能聊 agent，但**没人有办法配 agent 出来**。

### 1.3 为什么 admin-web 的 #10 实现是 dead UI

- admin-web 的 API wrapper `src/api/agent.ts` 调的 URL 是 `/v1/agent/skills/*`（蓝本里就是这么写的），不是 `/v1/admin/agents/*`。
- 但 admin-web 的 `src/api/request.ts` axios 实例附加的是 admin_token（不是 user_token）。
- 后端 router.go 把 `/v1/agent/skills/*` 注册在 user_token middleware 下，admin_token 通不过身份校验，发请求会被 400/401（具体码取决于 middleware 实现）。
- 即使后端兜底放宽（不会，安全风险），biz 层 `GetParentSkillsByOwner(parent_user_id)` 拿 admin 的 user ID 也查不到任何东西。

**所以：#10 在 admin-web 的实现对父账户物理不可达，对 Numind 员工没业务意义，是死代码（dead UI）。**

---

## 2. 修复范围（Scope）

### 2.1 admin-web 端：删除错位的 #10 代码

**删除：**

- `src/views/agent/AgentList.vue`
- `src/views/agent/AgentCreateChoose.vue`
- `src/views/agent/TemplateGallery.vue`
- `src/views/agent/AgentBuilder.vue`
- `src/views/agent/AgentDetail.vue`
- `src/views/agent/AgentEdit.vue`
- `src/views/agent/AgentAdvancedEdit.vue`
- `src/views/agent/components/` 下全部 11 个文件
  - AdvancedToggleConfirmModal.vue / AfterSaveModal.vue / AgentConfigTab.vue / AgentHistoryTab.vue / AgentStatsTab.vue / AvatarPicker.vue / ChipInput.vue / CreditSlider.vue / HistoryViewModal.vue / QuestionnaireForm.vue / validation.ts
- `src/views/agent/__tests__/` 下全部 4 个 spec 文件
  - AgentAdvancedEdit.spec.ts / AgentBuilder.spec.ts / AgentHistoryTab.spec.ts / AgentList.spec.ts
- `src/api/agent.ts` 中的 9 个 parent endpoint 函数（保留 2 个 admin endpoint）
- `src/stores/agent.ts` 整个文件
- `src/components/layout/AdminSidebar.vue` 中的 "AI 助手"（Bot icon, /agents）菜单项
- `src/router/index.ts` 中 /agents/* 全部 6 条路由（agents-builder / agents / agents-new / agents-from-template / agents-detail / agents-edit）
- `src/types/agent.ts` 中仅供 builder 用的类型（保留供 AgentMonitoring 用的 `AgentRunDTO` 等）

**保留：**

- `src/views/agent/AgentMonitoring.vue`（Numind 员工监控全平台 agent 运行实况，#14 已 wire 真实数据）
- `src/api/agent.ts` 中 `listAgentRunsApi` + `cancelAgentRunApi`（admin_token 中间件下的真 admin endpoint）
- `src/views/compliance/*`（合规规则 CRUD，Numind 员工管理 L1 规则）
- AdminSidebar "Agent 监控"（Activity icon, /agent-monitoring）菜单项
- AdminSidebar "合规规则"（ShieldCheck icon, /compliance-rules）菜单项
- router /agent-monitoring + /compliance-rules/* 路由

### 2.2 web-v3 端：新建父账户视角配 Agent UI

**新增：**

- `src/views/config/agents/AgentList.vue`
- `src/views/config/agents/AgentCreateChoose.vue`
- `src/views/config/agents/TemplateGallery.vue`
- `src/views/config/agents/AgentBuilder.vue`
- `src/views/config/agents/AgentDetail.vue`
- `src/views/config/agents/AgentEdit.vue`
- `src/views/config/agents/AgentAdvancedEdit.vue`
- `src/views/config/agents/components/` 下 11 个 component（从 admin-web 搬，必要时改 import）
- `src/views/config/agents/__tests__/` 下 4 个 spec（从 admin-web 搬，必要时改 mock）
- `src/api/agentBuilder.ts`（新文件，9 个 `/v1/agent/skills/*` endpoint 包装；命名加 `Builder` 后缀避免与现有 `src/api/agent.ts` 学员 API 冲突）
- `src/stores/agentBuilder.ts`（新 Pinia store，setup syntax）
- `src/types/agentBuilder.ts`（新类型文件；或合并到现有 `src/types/agent.ts` 内的命名空间）
- `src/router/index.ts` 加 `/config/agents/*` 路由群（在 `/config/...` ConfigLayout 下，与 chatbots / sop-templates / knowledge-bases 并列），路由 `meta.parentOnly: true`
- `src/views/config/ConfigLayout.vue` 把 `tabs` 从静态 `const` 改为 `computed`，按 `userStore.userInfo` 和 `userStore.isParentUser` 过滤：
  - 学员/匿名访客只看到 3 个 tab（智能体管理 / SOP 管理 / 知识库管理）
  - 父账户看到 4 个 tab（追加"AI 助手"）
  - `userInfo === null`（未 fetch 完）时**默认隐藏** parentOnly tab，避免 flash
- 路由守卫：`/config/agents/*` 必须 `userInfo` 已加载 且 `isParentUser === true`，否则 redirect 到 `/`（用 `await userStore.fetchUserInfo()` 在 guard 内显式等就绪状态）

**不变：**

- `src/views/agent/AgentChatView.vue`、`AgentHistoryView.vue`、`AgentSelectView.vue`（学员视角，#11 已 merge）
- `src/api/agent.ts`（学员端 13 个 endpoint，已用 mock 占位）
- `src/stores/agentChat.ts`（学员聊天 store）

### 2.3 后端 numind-server

**0 改动。** #5 9 个 endpoint (`/v1/agent/skills/*`) 本来就是 user_token middleware + 父账户 biz 层 403 校验，搬到 web-v3 后**自动**生效，无须改任何 controller / biz / store / router 代码。

---

## 3. Out of Scope（明确划线）

- ❌ 后端 API 改动（包括 9 个 user endpoint + 2 个 admin endpoint，全部不动）
- ❌ Prod 部署（dev 部署 OK；prod 部署文档可写但不执行）
- ❌ 新功能（不引入 v2 / v3 特性，仅搬迁 + 适配）
- ❌ 重命名 admin-web 旧 URL 做 302 redirect（父账户本来就进不去 admin-web，无 redirect 必要）
- ❌ AgentMonitoring 搬到 web-v3（Numind 员工功能，留 admin-web）
- ❌ 合规规则 CRUD 搬到 web-v3（Numind 员工管理 L1 平台规则，留 admin-web）
- ❌ 12 题问卷重新设计（沿用蓝本 §5.3 canonical，admin-web 已实现版本搬过来）
- ❌ 真实双账户 Playwright e2e 跨账户流程（不要求注册两个测试账户跑 e2e；子账户守卫验证走 Vitest 单测 mock `isParentUser=false` + 手动 smoke，父账户主流程用 `$E2E_USERNAME`/`$E2E_PASSWORD` 单账户 e2e）
- ❌ 父账户视角监控 v1（蓝本 §5.6 监控是父账户视角，但后端无对应 endpoint；本 feature 不引入；v2 再做）

---

## 4. 关键决策提案（待 S0 reviewer 审查）

### D1. web-v3 目录命名 = `src/views/config/agents/`

**理由：** web-v3 已有 `src/views/config/` 子目录承载父账户的业务配置（chatbots、sop-templates、knowledge-bases），与之并列最自然。

**备选：**
- `src/views/agent-builder/` — 顶级路由，区隔感更强但不与 config 并列，破坏现有 IA
- `src/views/agent/builder/` — 与学员视角 `/views/agent/` 共目录但子目录隔离，路由会冲突难处理
- `src/views/studio/` — 抽象命名，与 config 风格不一致

**决定：** `src/views/config/agents/`，路由前缀 `/config/agents`。

### D2. 菜单入口 = ConfigLayout tab "AI 助手"

**理由：** web-v3 父账户配置入口全在 `/config/*` 下，加 tab 而不是单独 sidebar 菜单。学员视角的 `/agent` 路由不冲突（在 ConfigLayout 之外）。

**菜单文案：** "AI 助手"（与 #10 admin-web 一致；蓝本 §5 也用此词）。

**决定：** 在 `src/views/config/ConfigLayout.vue` `tabs` 数组末尾加 `{ label: 'AI 助手', path: '/config/agents' }`。

### D3. API 文件命名 = `src/api/agentBuilder.ts`

**理由：** `src/api/agent.ts` 已存在（学员 13 个 endpoint），强行合并会让单文件超 250 行 + 概念混在一起（学员的 chat/run/feedback vs 父账户的 skill CRUD/history/restore）。分文件清晰。

**决定：** 新建 `src/api/agentBuilder.ts`，导出 9 个函数：`createAgent / listAgents / getAgent / patchAgent / deleteAgent / listAgentHistory / restoreAgent / toggleAgentAdvanced / listSkillTemplates`（去掉 admin-web 版本的 `Api` 后缀，对齐 web-v3 命名习惯）。

### D4. Store 文件命名 = `src/stores/agentBuilder.ts`

**理由：** 同 D3。`src/stores/agentChat.ts` 已存在（学员），新建 `agentBuilder.ts`。

**决定：** 新建 `src/stores/agentBuilder.ts`，setup syntax（Composition API）；从 admin-web `src/stores/agent.ts` 的 240 行 options syntax 转译为 web-v3 风格。

### D5. Types = 合并到 `src/types/agent.ts` 命名空间

**理由：** web-v3 已有 `src/types/agent.ts`（学员端类型）。父账户 builder 的类型加进去同文件，用 namespace/comment 分区，避免双 agent.ts 文件混淆。

**备选：** 新建 `src/types/agentBuilder.ts`，与 D3/D4 命名对齐。

**决定（暂定）：** 新建 `src/types/agentBuilder.ts`，与 D3/D4 命名对齐，避免改动现有类型文件。S1 reviewer 可挑战此选择。

### D6. 路由守卫 = 父账户判定 + 子账户 redirect（含 userInfo 未就绪处理）

**理由：** 后端 biz 层会 403 子账户调用，但前端用户体验更好是直接路由层拦截，不让子账户看到"AI 助手" tab/页面。需要明确处理 `userInfo` 异步未就绪的场景，否则首次渲染会"flash"。

**`isParentUser` 当前定义（user store line 34）：**
```typescript
const isParentUser = computed(() => userInfo.value?.parent_user_id == null)
```
当 `userInfo.value === null`（fetch 未完成）时，`null?.parent_user_id` → `undefined`，`undefined == null` → **`true`**——所有未登录/未 fetch 完用户都被误判为父账户。本 feature 必须解决这个 flash。

**实现：**

1. **ConfigLayout tabs 改 computed**（不是 const）：
   ```typescript
   const tabs = computed(() => {
     const all = [
       { label: '智能体管理', path: '/config/chatbots' },
       { label: 'SOP 管理', path: '/config/sop-templates' },
       { label: '知识库管理', path: '/config/knowledge-bases' },
       { label: 'AI 助手', path: '/config/agents', parentOnly: true },
     ]
     // userInfo 未就绪时，parentOnly tab 默认隐藏（避免 flash）
     if (!userStore.userInfo) return all.filter(t => !t.parentOnly)
     return all.filter(t => !t.parentOnly || userStore.isParentUser)
   })
   ```

2. **router meta**：`/config/agents`、`/config/agents/*` 子路由全部加 `meta: { parentOnly: true }`。

3. **router `beforeEach` 守卫**（在现有 auth 守卫之后）：
   ```typescript
   if (to.meta.parentOnly) {
     // 确保 userInfo 已就绪（如未加载则先 await）
     if (!userStore.userInfo) await userStore.fetchUserInfo()
     if (!userStore.isParentUser) return { name: 'home' }
   }
   ```

4. **后端 403** 是第三道防线，即使前两道失效，子账户调用 9 个 endpoint 仍会被 biz 层拒。

**决定：** 三道防线 + 显式处理 `userInfo === null` 的 flash 场景。S2 spec 阶段会进一步明确 `userStore` 是否有 `fetchUserInfo()` 方法或需要新增。

### D7. AgentMonitoring 不搬

**理由：** 蓝本 §5.6 监控是父账户视角，但后端目前只有 `/v1/admin/agent-runs` (admin_token) 这一个 endpoint，无父账户视角的同款 endpoint。强搬过去会变 404。

**Trade-off：** 父账户暂时无法看到自己 Agent 的运行实况。v1 接受，v2 可加 `/v1/agent-runs/parent` 或 `/v1/agent/skills/:id/runs` 父账户视角 endpoint，再搬监控 UI。

**决定：** AgentMonitoring.vue 留 admin-web（Numind 员工看全平台 agent_run）。本 feature 不在 web-v3 加监控页。

### D8. 合规规则 CRUD 不搬

**理由：** 后端 `/v1/admin/compliance-rules` CRUD 端点是 admin_token。蓝本架构里 L1 规则确实是 tenant-specific (per parent_user_id)，但 v1 实现里 Numind 员工代为配置（服务化），简化产品。

**决定：** 合规规则 view 留 admin-web。本 feature 不动。

### D9. 单测搬迁策略

**理由：** admin-web `__tests__` 4 个 spec 用 Vitest，引用 `@/stores/agent` + `@/api/agent` + admin-web 特定的 `useToast` composable。web-v3 也用 Vitest。

**适配点：**
- import 路径换：`@/stores/agent` → `@/stores/agentBuilder`，`@/api/agent` → `@/api/agentBuilder`
- 类型 import 换：`@/types/agent` → `@/types/agentBuilder`
- user store mock 改：admin-web 的 `useAuthStore` → web-v3 的 `useUserStore`，并补 `parent_user_id: null` 让组件认为是父账户
- 任何 ConfirmModal / AppButton 等公共组件 mock：web-v3 路径不同

**决定：** 全部 4 spec 搬过来，做 import + mock 适配。lint 通过后认定通过。

### D10. Lint baseline 沿用

**理由：** admin-web #10 的 S5 验收 baseline 是 "warning ≤ baseline, error == 0"。web-v3 有独立 baseline。

**决定：** S5 验证以 web-v3 当前 baseline（feature 启动时的 npm run lint 输出）为基准，要求搬迁后 baseline 不退化。

---

## 5. 验收标准（S5 Acceptance）

### admin-web

- `npm run lint` exit 0，error 0
- `npm run type-check` exit 0
- `npm run build` exit 0
- 删除的 view/component/api 函数无残余 import 引用
- `/agents` 路由访问返回 404（NotFoundView）
- AdminSidebar 不再展示 "AI 助手" 菜单项
- AgentMonitoring + 合规规则功能保留可用（手动 smoke：点 sidebar 进入页面无报错）

### web-v3

- `npm run lint` exit 0，error 0
- `npm run type-check` exit 0
- `npm run build` exit 0
- 父账户登录后 `/config/agents` 可访问，能列表 / 创建 / 编辑 / 派生 / 历史 / 删除（手动 smoke + 单账户 Playwright e2e）
- **子账户 redirect 验证走 Vitest 单测**：mock user store 设置 `userInfo.value.parent_user_id = 123n`（非 null），断言 ConfigLayout tabs 不含 "AI 助手"，断言 router guard 把 `/config/agents` redirect 到 `/`
- ConfigLayout tabs 父账户看到 4 个 tab（含 "AI 助手"），子账户只看到 3 个（单测验证）
- **首次渲染无 flash**：单测 `userInfo === null` 状态下 tabs 不含 "AI 助手"
- 学员视角 `/agent/*` 3 view 不受影响
- 12 题问卷 UI 渲染与蓝本 §5.3 一致（题目顺序 + 默认值 + 验证规则）

### Prod 影响

- **0 行 config_prod.yaml 改动**
- **0 行 migrations/*.sql 改动**
- **0 行后端代码改动**

---

## 6. 风险（Risks）

### R1. admin-web `src/api/agent.ts` 拆分错误

**风险：** 误删 listAgentRunsApi / cancelAgentRunApi 会让 AgentMonitoring.vue 编译失败。

**缓解：** S4 implementer 必须保留这两个函数，S4 reviewer 验证 file diff 不动这两行。

### R2. web-v3 学员视角的 `src/api/agent.ts` 与新 `agentBuilder.ts` 命名混淆

**风险：** 后续 onboarding 的 AI 助手看到两个 agent-* 文件可能不知道用哪个。

**缓解：** 两个文件顶部加注释明确"学员端" vs "父账户配置者"，并写清 endpoint URL 前缀差异 (`/v1/agent-skills/available` 学员 vs `/v1/agent/skills` 父账户)。

### R3. ConfigLayout 风格与 admin-web DataTable 风格不一致

**风险：** admin-web AgentList 用 DataTable（admin 必须表格），web-v3 ConfigLayout 下其他 view（ChatbotList / SopTemplateList / KnowledgeBaseList）用什么风格？需要先看。

**缓解：** S2 spec 阶段先扫 web-v3 现有 config 子目录 view 的 UI 风格（卡片 / 表格 / 混合），AgentList 风格与之对齐。如果差异大，引入 web-v3 风格表格组件或用现有 web-v3 list 组件。

### R4. 路由守卫漏检导致子账户看到"AI 助手" tab

**风险：** computed filter 在某些 reactivity 路径下不重计算（例如 user info 异步 fetch 后），tabs 显示错。

**缓解：** S4 加 vitest 测试 ConfigLayout tabs 在 isParentUser = false 时只渲染 3 个 tab。

### R5. 单测搬迁 mock 适配不全

**风险：** admin-web 的 useToast / AppButton mock 在 web-v3 找不到对应。

**缓解：** S2 spec 列出每个 spec 文件依赖的 mock 项，逐一在 web-v3 找替代或写新 mock。

### R6. Vitest 配置差异

**风险：** admin-web 和 web-v3 的 `vitest.config.ts` 可能 alias / setup 不同。

**缓解：** S2 阶段读取两个仓库的 vitest 配置，差异点列出。

---

## 7. 相关 feature（Related）

- **agent-mode-configurator-ux** (#10/14, merged `fdebd7b` 2026-05-21) — 本 feature 是它的位置修复，14 个 S4 task 的代码全部要搬。
- **agent-mode-skill-system** (#5/14, merged `e05498b6`) — 后端 9 个 endpoint 来源，本 feature 不动。
- **agent-mode-student-ux** (#11/14, merged `a02a442`) — web-v3 已有学员视角 3 view，不动。
- **agent-mode-e2e-rollout** (#14/14, merged `e87b090e` server / `78128f3` admin-web / `a15f134` web-v3) — admin-web AgentMonitoring + 合规规则 wire 真实数据源由 #14 完成，本 feature 不影响。

---

## 8. 估算（rough）

- S0-S3 文档：~6 hours
- S4 实施：~10 hours（14 个 task 量级，但都是搬迁不写新逻辑）
- S5 验收：~2 hours
- S6 merge：~30 min

**Total ≈ 18-20 hours** 单 AI session 流水线。

---

*Created: 2026-05-22 14:00 +0800 · ai-s0*
