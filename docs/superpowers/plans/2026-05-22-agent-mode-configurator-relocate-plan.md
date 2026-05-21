# Plan: agent-mode-configurator-relocate (S3)

> Track: **standard** · Stage: **S3** · Repos: **numind-admin-web + numind-web-v3** · 2026-05-22

---

## §0 Plan 概述

把 S2 spec 拆成可独立审计 + 可并行实施的 atomic task。共 **14 个 M task** + 1 个 S5 验证策略 task。每个 task 完成后并行 dispatch 2 个 Sonnet reviewer（spec compliance + code quality）。Tier 3 并行用 ndf-check-disjoint 验证文件归属无交集。

**Worktree 与 cwd 约定：**
- admin-web 工作 cwd：`/private/tmp/wt-agent-mode-configurator-relocate-numind-admin-web`
- web-v3 工作 cwd：`/private/tmp/wt-agent-mode-configurator-relocate-numind-web-v3`
- 文档（manifest / requirements / proposals / specs / plans）已在 develop 上，无需在 worktree 内编辑

---

## §1 Task 清单

### M0 — 捕获 lint baseline（S4 开始前，PRE-IMPLEMENTATION）

**仓库：** 两个 worktree
**Wave：** 0（其他所有 task 之前）
**文件归属：** 无文件改动（只产出 /tmp 临时文件）

**操作：**
```bash
# Step 1 — admin-web baseline + spec backup
cd /private/tmp/wt-agent-mode-configurator-relocate-numind-admin-web
npm install 2>&1 | tail -3
npm run lint 2>&1 > /tmp/admin-web-lint-pre.log
admin_warnings=$(grep -cE "warning" /tmp/admin-web-lint-pre.log || echo 0)
admin_errors=$(grep -cE "error" /tmp/admin-web-lint-pre.log || echo 0)
echo "admin-web baseline: warnings=$admin_warnings errors=$admin_errors" > /tmp/relocate-baseline.txt

# Step 1b — backup 9 spec to /tmp 给 M11 用（M1 会 rm 这些目录）
mkdir -p /tmp/relocate-spec-backup/top
mkdir -p /tmp/relocate-spec-backup/components
cp src/views/agent/__tests__/*.spec.ts /tmp/relocate-spec-backup/top/
cp src/views/agent/components/__tests__/*.spec.ts /tmp/relocate-spec-backup/components/
ls /tmp/relocate-spec-backup/top/        # 应有 4 spec
ls /tmp/relocate-spec-backup/components/ # 应有 5 spec

# Step 2 — web-v3 baseline
cd /private/tmp/wt-agent-mode-configurator-relocate-numind-web-v3
npm install 2>&1 | tail -3
npm run lint 2>&1 > /tmp/web-v3-lint-pre.log
web_warnings=$(grep -cE "warning" /tmp/web-v3-lint-pre.log || echo 0)
web_errors=$(grep -cE "error" /tmp/web-v3-lint-pre.log || echo 0)
echo "web-v3 baseline: warnings=$web_warnings errors=$web_errors" >> /tmp/relocate-baseline.txt

cat /tmp/relocate-baseline.txt
```

**验证：** baseline 文件有 2 行。如果 baseline error 数 > 0，先 fix（不属于本 feature 引入，但既然先 capture 就要保证 baseline 干净）。

**不 commit 文件** — baseline 保存在 `/tmp/relocate-baseline.txt`，S5 引用。

### M1 — admin-web 删除：7 view + 11 components + 9 spec + store（**先于 M2 删 view/store**）

**仓库：** numind-admin-web
**Wave：** 1a (M2 之前串行 — reviewer P0-2 修正)
**文件归属：**
- `src/views/agent/AgentList.vue`
- `src/views/agent/AgentCreateChoose.vue`
- `src/views/agent/TemplateGallery.vue`
- `src/views/agent/AgentBuilder.vue`
- `src/views/agent/AgentDetail.vue`
- `src/views/agent/AgentEdit.vue`
- `src/views/agent/AgentAdvancedEdit.vue`
- `src/views/agent/components/` 整目录（11 文件含 components/__tests__/ 5 spec）
- `src/views/agent/__tests__/` 整目录（4 spec）
- `src/stores/agent.ts`

**M1 顺序在 M2 前的原因：** 如果先 M2（删 api/types 中 9 个函数），则 stores/agent.ts 仍 import 这 9 个函数会 type-check FAIL。先 M1（删 views + stores）后，agent.ts 的 9 个函数变成 dead code（无 caller），但 type-check 仍 PASS。然后 M2 安全删 dead code。

**9 个 spec 已 backup（M0 完成）：** 4 top-level + 5 component-level 由 M0 backup 到 `/tmp/relocate-spec-backup/`，M11 从该备份恢复到 web-v3。**rm 不丢失。**

**操作：**
```bash
cd /private/tmp/wt-agent-mode-configurator-relocate-numind-admin-web
rm src/views/agent/AgentList.vue
rm src/views/agent/AgentCreateChoose.vue
rm src/views/agent/TemplateGallery.vue
rm src/views/agent/AgentBuilder.vue
rm src/views/agent/AgentDetail.vue
rm src/views/agent/AgentEdit.vue
rm src/views/agent/AgentAdvancedEdit.vue
rm -rf src/views/agent/components/
rm -rf src/views/agent/__tests__/
rm src/stores/agent.ts
```

**验证：**
```bash
ls src/views/agent/  # 仅 AgentMonitoring.vue
npm run type-check   # 应 PASS（agent.ts 9 funcs dead code 但 self-consistent；AgentMonitoring 引用 AgentRunDTO/ListResponse 仍在）
npm run lint         # warning 可能涨（9 dead funcs），M2 后清除
```

**Commit message：** `refactor(admin-web): remove agent builder views/components/9 specs/store`

### M2 — admin-web 删除：sidebar 项 + 6 路由 + 9 个 api funcs + types builder 部分

**仓库：** numind-admin-web
**Wave：** 1b (M1 commit 后串行)
**文件归属：**
- `src/components/layout/AdminSidebar.vue`（修改：删 "AI 助手" navItem + Bot import）
- `src/router/index.ts`（修改：删 /agents/* 6 条路由）
- `src/api/agent.ts`（修改：见 S2 §1.2 final state — 保留 2 admin endpoints）
- `src/types/agent.ts`（修改：见 S2 §1.3 final state — 保留 AgentRunDTO + ListResponse）

**验证：**
```bash
cd /private/tmp/wt-agent-mode-configurator-relocate-numind-admin-web
npm run type-check  # 应 PASS — AgentMonitoring 仍可 import 保留的 AgentRunDTO/ListResponse + 2 admin funcs
npm run lint        # warning <= M0 baseline + 0（dead code 已清）
grep -rn "from '@/types/agent'" src/ | grep -v AgentMonitoring  # 应无命中
grep -n "Bot," src/components/layout/AdminSidebar.vue  # 应无命中
grep -n "agents/builder\|agents/new" src/router/index.ts  # 应无命中
```

**Commit message：** `refactor(admin-web): clean sidebar/routes/API/types after agent builder relocate`

### M3 — web-v3 port DataTable.vue + NoticeBanner.vue + CheckboxGroup.vue 3 个 common component

**仓库：** numind-web-v3
**Wave：** 1（与 admin-web M1/M2 并列，不同仓库 Tier 2 disjoint）
**文件归属：**
- `src/components/common/DataTable.vue`（新建）
- `src/components/common/NoticeBanner.vue`（新建）
- `src/components/common/CheckboxGroup.vue`（新建）

**操作：**
```bash
cd /private/tmp/wt-agent-mode-configurator-relocate-numind-web-v3
cp /private/tmp/wt-agent-mode-configurator-relocate-numind-admin-web/src/components/common/DataTable.vue src/components/common/DataTable.vue
cp /private/tmp/wt-agent-mode-configurator-relocate-numind-admin-web/src/components/common/NoticeBanner.vue src/components/common/NoticeBanner.vue
cp /private/tmp/wt-agent-mode-configurator-relocate-numind-admin-web/src/components/common/CheckboxGroup.vue src/components/common/CheckboxGroup.vue

# 按 S2 §2.2 token map 改写 CSS variable（3 个文件统一处理）
for f in src/components/common/DataTable.vue src/components/common/NoticeBanner.vue src/components/common/CheckboxGroup.vue; do
  sed -i.bak \
    -e 's/var(--surface-lowest)/var(--surface)/g' \
    -e 's/var(--surface-low)/var(--surface-tint)/g' \
    -e 's/var(--surface-high)/var(--surface-hover)/g' \
    -e 's/var(--on-surface-variant)/var(--text-muted)/g' \
    -e 's/var(--on-surface)/var(--text)/g' \
    -e 's/var(--text-sm)/0.875rem/g' \
    -e 's/var(--text-xs)/0.75rem/g' \
    "$f"
  rm "$f.bak"
done

# font-family declarations 清理
grep -n "font-family.*var(--font-" src/components/common/{DataTable,NoticeBanner,CheckboxGroup}.vue
# 如有命中，手工删除整行（让 inherit）
```

**验证：**
```bash
npm run type-check  # 这三个组件本身无依赖，应 PASS
grep "var(--surface-lowest\|--surface-low\|--surface-high\|--on-surface\|--text-sm\|--text-xs" src/components/common/{DataTable,NoticeBanner,CheckboxGroup}.vue
# 应无命中（所有 token 已替换）
```

**Commit message：** `feat(web-v3): port DataTable, NoticeBanner, CheckboxGroup from admin-web`

### M4 — web-v3 新建 types/agentBuilder.ts

**仓库：** numind-web-v3
**Wave：** 2（M3 后，所有后续 view/store/api 依赖此文件）
**文件归属：**
- `src/types/agentBuilder.ts`（新建）

**操作：** 见 S2 §2.3，cp `numind-admin-web/src/types/agent.ts` 到 destination，删 AgentRunDTO + 改文件头 comment。

**验证：**
```bash
npm run type-check  # 应 PASS
grep -E "(AgentRunDTO|user_id.*number.*agent_definition_id)" src/types/agentBuilder.ts  # 应无命中
```

**Commit message：** `feat(web-v3): add agentBuilder types (relocated from admin-web)`

### M5 — web-v3 新建 api/agentBuilder.ts

**仓库：** numind-web-v3
**Wave：** 2（M4 后；不与 M4 并行因为 import M4 的 types）
**文件归属：**
- `src/api/agentBuilder.ts`（新建）

**操作：** 见 S2 §2.4，按 spec 骨架写，9 个函数命名去 `Api` 后缀，import from `@/types/agentBuilder`。

**验证：**
```bash
npm run type-check
grep "data\.data" src/api/agentBuilder.ts  # 应无命中（reviewer P0-1 修正后是 return data）
grep "request.post\|request.get\|request.patch\|request.delete" src/api/agentBuilder.ts | wc -l  # 9 (函数数)
```

**Commit message：** `feat(web-v3): add agentBuilder API wrapper (9 endpoints)`

### M6 — web-v3 新建 stores/agentBuilder.ts

**仓库：** numind-web-v3
**Wave：** 3（M4 + M5 后）
**文件归属：**
- `src/stores/agentBuilder.ts`（新建）

**操作：** 见 S2 §2.5，cp `numind-admin-web/src/stores/agent.ts` 到 destination，5 处改：
1. `defineStore("agent", ...)` → `defineStore("agentBuilder", ...)`
2. import from `@/api/agent` → `@/api/agentBuilder`
3. import from `@/types/agent` → `@/types/agentBuilder`
4. 函数调用去 `Api` 后缀（9 处）
5. 文件头 comment

**验证：**
```bash
npm run type-check
grep "Api" src/stores/agentBuilder.ts | grep -v "Api wrappers" # 应无 listAgentsApi/createAgentApi 等命中
```

**Commit message：** `feat(web-v3): add agentBuilder Pinia store (relocated from admin-web)`

### M7 — web-v3 搬迁 11 components + validation.ts

**仓库：** numind-web-v3
**Wave：** 3（M4 + M5 后；与 M6 并行 Tier 3 disjoint）

components 不直接调用 store（store 由 view 注入 props），所以**M7 不依赖 M6**，可与 M6 并行（Tier 3 disjoint）。M7 只依赖 M4（types）+ M3（CheckboxGroup 等 common component）。

**文件归属：**
- `src/views/config/agents/components/` 下 11 个文件（全部）

**操作：**
```bash
mkdir -p src/views/config/agents/components
ADMIN_COMP=/private/tmp/wt-agent-mode-configurator-relocate-numind-admin-web/src/views/agent/components
cp "$ADMIN_COMP"/AdvancedToggleConfirmModal.vue src/views/config/agents/components/
cp "$ADMIN_COMP"/AfterSaveModal.vue src/views/config/agents/components/
cp "$ADMIN_COMP"/AgentConfigTab.vue src/views/config/agents/components/
cp "$ADMIN_COMP"/AgentHistoryTab.vue src/views/config/agents/components/
cp "$ADMIN_COMP"/AgentStatsTab.vue src/views/config/agents/components/
cp "$ADMIN_COMP"/AvatarPicker.vue src/views/config/agents/components/
cp "$ADMIN_COMP"/ChipInput.vue src/views/config/agents/components/
cp "$ADMIN_COMP"/CreditSlider.vue src/views/config/agents/components/
cp "$ADMIN_COMP"/HistoryViewModal.vue src/views/config/agents/components/
cp "$ADMIN_COMP"/QuestionnaireForm.vue src/views/config/agents/components/
cp "$ADMIN_COMP"/validation.ts src/views/config/agents/components/

# 改 import：@/types/agent → @/types/agentBuilder
# 改 import：@/composables/useToast → @/stores/notifications
for f in src/views/config/agents/components/*.vue src/views/config/agents/components/*.ts; do
  sed -i.bak \
    -e "s|from '@/types/agent'|from '@/types/agentBuilder'|g" \
    -e "s|from \"@/types/agent\"|from \"@/types/agentBuilder\"|g" \
    "$f"
  rm "$f.bak"
done

# useToast → useNotificationsStore: 手工搜索逐个改
grep -l "useToast" src/views/config/agents/components/*.vue
# 对每个命中文件：
#   - import { useToast } from "@/composables/useToast" → import { useNotificationsStore } from "@/stores/notifications"
#   - const toast = useToast() → const notifications = useNotificationsStore()
#   - toast.success(x) → notifications.success(x) 等
```

**验证：**
```bash
npm run type-check  # 此时仍 FAIL（view 文件还没建）
grep -rn "from '@/composables/useToast'" src/views/config/agents/components/  # 应无命中
grep -rn "from '@/types/agent'" src/views/config/agents/components/  # 应无命中
```

**Commit message：** `feat(web-v3): port 11 agent builder components (questionnaire/modals/inputs)`

### M8 — web-v3 搬迁 7 view 文件

**仓库：** numind-web-v3
**Wave：** 4（M6 + M7 后；view 依赖 store + components 都就位）
**文件归属：**
- `src/views/config/agents/AgentList.vue`
- `src/views/config/agents/AgentCreateChoose.vue`
- `src/views/config/agents/TemplateGallery.vue`
- `src/views/config/agents/AgentBuilder.vue`
- `src/views/config/agents/AgentDetail.vue`
- `src/views/config/agents/AgentEdit.vue`
- `src/views/config/agents/AgentAdvancedEdit.vue`

**操作：**
```bash
ADMIN_VIEW=/private/tmp/wt-agent-mode-configurator-relocate-numind-admin-web/src/views/agent
for v in AgentList AgentCreateChoose TemplateGallery AgentBuilder AgentDetail AgentEdit AgentAdvancedEdit; do
  cp "$ADMIN_VIEW/$v.vue" src/views/config/agents/$v.vue
done

# 通用 sed 改写
for f in src/views/config/agents/*.vue; do
  sed -i.bak \
    -e "s|from '@/types/agent'|from '@/types/agentBuilder'|g" \
    -e "s|from \"@/types/agent\"|from \"@/types/agentBuilder\"|g" \
    -e "s|from '@/api/agent'|from '@/api/agentBuilder'|g" \
    -e "s|from \"@/api/agent\"|from \"@/api/agentBuilder\"|g" \
    -e "s|from '@/stores/agent'|from '@/stores/agentBuilder'|g" \
    -e "s|from \"@/stores/agent\"|from \"@/stores/agentBuilder\"|g" \
    -e "s|useAgentStore|useAgentBuilderStore|g" \
    -e "s|'/agents/|'/config/agents/|g" \
    -e "s|\"/agents/|\"/config/agents/|g" \
    -e "s|router.push('/agents')|router.push('/config/agents')|g" \
    -e "s|router.push(\"/agents\")|router.push(\"/config/agents\")|g" \
    "$f"
  rm "$f.bak"
done

# 手工 useToast → useNotificationsStore（参考 M7 注释）
grep -l "useToast" src/views/config/agents/*.vue
# 对每个命中文件按 M7 模式改

# 手工 *Api 函数调用去后缀（如有 view 直接调 API；正常应通过 store，但 view 可能有直调）
grep -E "(createAgentApi|listAgentsApi|getAgentApi|patchAgentApi|deleteAgentApi|listAgentHistoryApi|restoreAgentApi|toggleAgentAdvancedApi|listSkillTemplatesApi)" src/views/config/agents/*.vue
# 命中文件逐个改去 Api 后缀
```

**验证：**
```bash
npm run type-check  # 应 PASS（M8 后所有文件就位）
npm run lint
grep -rn "from '@/api/agent'\|from '@/stores/agent'\|from '@/types/agent'" src/views/config/agents/
# 应无 admin-web 风格 import 残留
grep "/agents/" src/views/config/agents/*.vue | grep -v "/config/agents"
# 应无 admin-web 风格路由残留（除非是 SOP-related 偶然命中）
```

**Commit message：** `feat(web-v3): port 7 agent builder views (list/create/builder/detail/edit/advanced/template)`

### M9 — web-v3 router 新增 7 条 /config/agents/* 路由 + guard 改 async

**仓库：** numind-web-v3
**Wave：** 5（M8 后；M8 的 view 文件作为路由 import 目标）
**文件归属：**
- `src/router/index.ts`（修改）

**操作 1 — 加路由：** 在 `/config` children 数组末尾追加 6 条 `/config/agents/*` 路由（见 S2 §2.9 完整路由对象）。

**操作 2 — guard 改 async（精确 diff，reviewer P1-1 修正）：**

实测 web-v3 `src/router/index.ts` 第 175-211 行现有 guard 用 `(to, from, next) =>` + `requiresAuth` + `to.path === '/login'` + `next(...)` 风格。改造为 async + return-style：

**Before：**
```typescript
router.beforeEach((to, from, next) => {
  const title = to.meta.title as string | undefined
  if (title) document.title = `${title} - 莫小派工作站`
  else document.title = '莫小派工作站'

  const userStore = useUserStore()
  const isLoggedIn = userStore.isLoggedIn
  const requiresAuth = to.meta.requiresAuth

  if (requiresAuth && !isLoggedIn) {
    next({ name: 'login', query: { redirect: to.fullPath } })
    return
  }
  if (isLoggedIn && to.path === '/login') {
    next('/')
    return
  }
  if ((to.meta.parentOnly || to.meta.requiresParent) && !userStore.isParentUser) {
    next('/')
    return
  }
  next()
})
```

**After（仅 4 处改：函数签名 async / next→return / parentOnly 块新增 await fetchUserInfo + toast / 末尾 next() 删除）：**
```typescript
router.beforeEach(async (to) => {
  const title = to.meta.title as string | undefined
  if (title) document.title = `${title} - 莫小派工作站`
  else document.title = '莫小派工作站'

  const userStore = useUserStore()
  const isLoggedIn = userStore.isLoggedIn
  const requiresAuth = to.meta.requiresAuth

  if (requiresAuth && !isLoggedIn) {
    return { name: 'login', query: { redirect: to.fullPath } }
  }
  if (isLoggedIn && to.path === '/login') {
    return { path: '/' }
  }
  if (to.meta.parentOnly || to.meta.requiresParent) {
    if (!userStore.userInfo && userStore.isLoggedIn) {
      await userStore.fetchUserInfo()
    }
    if (!userStore.isParentUser) {
      useNotificationsStore().info('AI 助手配置仅父账户可访问')
      return { path: '/' }
    }
  }
  // 默认 pass（Vue Router 4 返回 undefined = pass）
})
```

**新增顶部 import：**
```typescript
import { useNotificationsStore } from '@/stores/notifications'
```

**验证：**
```bash
npm run type-check
npm run dev  # 启动后访问 http://localhost:5173/config/agents 应能进入（父账户登录态下）
```

**Commit message：** `feat(web-v3): add /config/agents/* routes + async router guard with userInfo wait`

### M10 — web-v3 ConfigLayout 改 computed tabs

**仓库：** numind-web-v3
**Wave：** 5（与 M9 同 Wave，但文件不同，Tier 3 disjoint）
**文件归属：**
- `src/views/config/ConfigLayout.vue`（修改）

**操作：** 见 S2 §2.10。

**验证：**
```bash
npm run type-check
npm run dev  # 浏览器手测：父账户看到 4 tab，子账户看到 3 tab
```

**Commit message：** `feat(web-v3): ConfigLayout tabs filter by isParentUser (computed)`

### M11 — web-v3 搬迁 9 spec 单测 + mock 适配（4 view spec + 5 component spec）

**仓库：** numind-web-v3
**Wave：** 6（M9 + M10 后；测试需要完整 view + router + layout 就位）
**文件归属：**
- `src/views/config/agents/__tests__/AgentList.spec.ts`
- `src/views/config/agents/__tests__/AgentBuilder.spec.ts`
- `src/views/config/agents/__tests__/AgentAdvancedEdit.spec.ts`
- `src/views/config/agents/__tests__/AgentHistoryTab.spec.ts`
- `src/views/config/agents/components/__tests__/AvatarPicker.spec.ts`（**reviewer P0-3 修正：含 5 个 component spec**）
- `src/views/config/agents/components/__tests__/ChipInput.spec.ts`
- `src/views/config/agents/components/__tests__/CreditSlider.spec.ts`
- `src/views/config/agents/components/__tests__/QuestionnaireForm.spec.ts`
- `src/views/config/agents/components/__tests__/validation.spec.ts`

**操作：**
```bash
# 注意：M1 已删除原文件，所以 cp 必须在 M1 commit 之前从原版获取
# 实际上 cp 应该从 admin-web develop 分支的 git show 获取，避免依赖未删除状态。
# 简化方案：M1 执行前先 cp 整个目录到 web-v3，M1 commit 后原版才消失
# 顺序约束：M11 cp 必须在 M1 之前**完成 copy 步骤**（M1 commit 是 worktree 操作，rm 后文件物理消失）
# 但 M11 是 Wave 6（M1 是 Wave 1）— 矛盾。
#
# 解决方案：M11 用 git show admin-web 分支历史的文件内容获取，不依赖 worktree 当前状态。
# 例如：git -C /private/tmp/wt-agent-mode-configurator-relocate-numind-admin-web show develop:src/views/agent/__tests__/AgentList.spec.ts > target.spec.ts
# 或者：spec 文件在 M1 删除前**额外保留一份在 /tmp/preserved-specs/**，M11 从 /tmp 读取

# 实施 simpler：M1 在删之前先 stash 一份 spec
# 但 M1 Wave 1, M11 Wave 6 — 时间间隔大，最佳做法是 M0 做一次 backup：
# bash -c "cp -r /private/tmp/wt-agent-mode-configurator-relocate-numind-admin-web/src/views/agent/__tests__ /tmp/relocate-spec-backup/top/ && cp -r /private/tmp/wt-agent-mode-configurator-relocate-numind-admin-web/src/views/agent/components/__tests__ /tmp/relocate-spec-backup/components/"
# M11 从 /tmp/relocate-spec-backup/ 读取（**M0 修订：加这一步**）

# 设 M0 已 backup 到 /tmp/relocate-spec-backup/{top,components}/，M11 操作：
mkdir -p src/views/config/agents/__tests__
mkdir -p src/views/config/agents/components/__tests__
cp /tmp/relocate-spec-backup/top/*.spec.ts src/views/config/agents/__tests__/
cp /tmp/relocate-spec-backup/components/*.spec.ts src/views/config/agents/components/__tests__/

# 通用 sed 改写
for f in src/views/config/agents/__tests__/*.spec.ts src/views/config/agents/components/__tests__/*.spec.ts; do
  sed -i.bak \
    -e "s|from '@/views/agent/|from '@/views/config/agents/|g" \
    -e "s|from \"@/views/agent/|from \"@/views/config/agents/|g" \
    -e "s|from '@/api/agent'|from '@/api/agentBuilder'|g" \
    -e "s|from \"@/api/agent\"|from \"@/api/agentBuilder\"|g" \
    -e "s|from '@/stores/agent'|from '@/stores/agentBuilder'|g" \
    -e "s|from \"@/stores/agent\"|from \"@/stores/agentBuilder\"|g" \
    -e "s|from '@/types/agent'|from '@/types/agentBuilder'|g" \
    -e "s|from \"@/types/agent\"|from \"@/types/agentBuilder\"|g" \
    -e "s|vi.mock('@/api/agent'|vi.mock('@/api/agentBuilder'|g" \
    -e "s|vi.mock(\"@/api/agent\"|vi.mock(\"@/api/agentBuilder\"|g" \
    -e "s|vi.mock('@/stores/agent'|vi.mock('@/stores/agentBuilder'|g" \
    -e "s|vi.mock(\"@/stores/agent\"|vi.mock(\"@/stores/agentBuilder\"|g" \
    -e "s|useAgentStore|useAgentBuilderStore|g" \
    -e "s|listAgentsApi|listAgents|g" \
    -e "s|getAgentApi|getAgent|g" \
    -e "s|createAgentApi|createAgent|g" \
    -e "s|patchAgentApi|patchAgent|g" \
    -e "s|deleteAgentApi|deleteAgent|g" \
    -e "s|listAgentHistoryApi|listAgentHistory|g" \
    -e "s|restoreAgentApi|restoreAgent|g" \
    -e "s|toggleAgentAdvancedApi|toggleAgentAdvanced|g" \
    -e "s|listSkillTemplatesApi|listSkillTemplates|g" \
    "$f"
  rm "$f.bak"
done

# 手工：useToast mock → useNotificationsStore mock（每个 spec 都有）
# AgentHistoryTab.spec.ts：删 attachTo + 加 stubs.Teleport=true（S2 §2.8）
```

**验证：**
```bash
npm run test src/views/config/agents/__tests__/  # 4 spec 全部 PASS
```

**Commit message：** `test(web-v3): port 4 agent builder unit tests with mock adaptation`

### M12 — web-v3 新增 ConfigLayout.spec.ts（tabs 过滤测试）

**仓库：** numind-web-v3
**Wave：** 7（M10 + M11 后）
**文件归属：**
- `src/views/config/__tests__/ConfigLayout.spec.ts`（新建）

**操作：** 见 S2 §3.2，按 mock template 写 3 个 test case。

**验证：**
```bash
npm run test src/views/config/__tests__/ConfigLayout.spec.ts  # 3 test PASS
```

**Commit message：** `test(web-v3): add ConfigLayout tabs filter unit tests`

### M13 — web-v3 新增 router guard.spec.ts（如 router/__tests__ 不存在则新建）

**仓库：** numind-web-v3
**Wave：** 7（与 M12 并行，Tier 3 disjoint）
**文件归属：**
- `src/router/__tests__/guard.spec.ts`（新建，可能需要先 mkdir __tests__）

**操作：** 5 个测试用例（reviewer P1-3 新增第 5 个）：

```typescript
// 1. requiresParent + userInfo loaded + isParentUser=true → pass (return undefined)
// 2. requiresParent + userInfo loaded + isParentUser=false → return { path: '/' } + 调用 notifications.info
// 3. requiresParent + userInfo=null + isLoggedIn=true → 调用 fetchUserInfo() → 再判定
// 4. 非 requiresParent route (e.g., '/sop') → pass without check
// 5. **旧 requiresParent route (e.g., '/config/chatbots') + parent user → pass** — 验证 async guard 改造后不破坏现有 13 个 /config/* 路由行为
```

**验证：**
```bash
npm run test src/router/__tests__/guard.spec.ts  # 5 test PASS
```

**Commit message：** `test(web-v3): add router guard async + requiresParent unit tests`

### M14 — S5 验证策略文档 + lint baseline 捕获

**仓库：** numind-server（doc 类工件，commit 到 develop）
**Wave：** 8（最后）
**文件归属：**
- `numind-server/docs/superpowers/qa/2026-05-22-agent-mode-configurator-relocate-s5-acceptance.md`（新建）

**操作：**
1. Lint baseline 捕获（在每个 worktree 跑）：
   ```bash
   cd /private/tmp/wt-agent-mode-configurator-relocate-numind-admin-web
   npm run lint 2>&1 > /tmp/admin-web-lint-baseline.log
   cd /private/tmp/wt-agent-mode-configurator-relocate-numind-web-v3
   npm run lint 2>&1 > /tmp/web-v3-lint-baseline.log
   ```
2. 写 S5 acceptance md，含：
   - 验证 step list (per S2 §3.1)
   - lint baseline 数字（warning 数 / error 数）
   - 接受 vs 不接受标准

**注意：M14 是 plan task 但不实际跑验证（那是 S5 阶段的事）。M14 只产出**文档**让 S5 reviewer / 实施者照做。**

**Commit message：** `docs(ndf-s5): agent-mode-configurator-relocate S5 acceptance criteria + lint baseline`

---

## §2 Wave 与并行规则

### Wave 概览（reviewer P0-2 修正：Wave 1 改串行 M1 → M2）

| Wave | Tasks | 仓库 | Tier | 备注 |
|------|-------|------|------|------|
| 0 | M0 | both worktrees | — | baseline 捕获 + 9 spec backup 到 /tmp（不 commit） |
| 1a | M1 | admin-web | — | 删 view + components + 9 spec + store（先删）|
| 1b | M2 | admin-web | — | M1 commit 后串行：删 api/types/router/sidebar |
| 1c | M3 | web-v3 | Tier 2（跨仓库） | 与 admin-web M1/M2 并行（不同仓库）— 仅在 Wave 1a/1b 期间也允许 |
| 2 | M4, M5 | web-v3 | 串行 | M5 import M4 |
| 3 | M6, M7 | web-v3 | Tier 3 disjoint | M6 = stores/agentBuilder.ts，M7 = views/config/agents/components/* |
| 4 | M8 | web-v3 | 串行 | M8 view 依赖 M6 store + M7 components 都已就位 |
| 5 | M9, M10 | web-v3 | Tier 3 disjoint | M9 = router/index.ts，M10 = views/config/ConfigLayout.vue |
| 6 | M11 | web-v3 | 串行 | spec 依赖完整 view/router/layout 已就位（含 9 个 spec port） |
| 7 | M12, M13 | web-v3 | Tier 3 disjoint | 不同文件不同目录 |
| 8 | M14 | numind-server (develop) | 单 | S5 acceptance doc，引用 M0 捕获的 baseline 数字 |

### Tier 3 文件归属验证

每个 Tier 3 Wave 在 dispatch 前用 `ndf-check-disjoint` 验证：

**Wave 1（M1 → M2 串行）：** 不需要 ndf-check-disjoint（串行依赖）。M3 在 web-v3 Tier 2 跨仓库可与 M1+M2 并行（不需 disjoint）。

**Wave 3（M6 + M7 同仓库 web-v3，Tier 3 disjoint）：**
```bash
bash numind-server/scripts/ndf/ndf-check-disjoint.sh \
  "src/stores/agentBuilder.ts" \
  "src/views/config/agents/components/AdvancedToggleConfirmModal.vue,src/views/config/agents/components/AfterSaveModal.vue,src/views/config/agents/components/AgentConfigTab.vue,src/views/config/agents/components/AgentHistoryTab.vue,src/views/config/agents/components/AgentStatsTab.vue,src/views/config/agents/components/AvatarPicker.vue,src/views/config/agents/components/ChipInput.vue,src/views/config/agents/components/CreditSlider.vue,src/views/config/agents/components/HistoryViewModal.vue,src/views/config/agents/components/QuestionnaireForm.vue,src/views/config/agents/components/validation.ts"
```

**Wave 5（M9 + M10 同仓库 web-v3，Tier 3 disjoint）：**
```bash
bash numind-server/scripts/ndf/ndf-check-disjoint.sh \
  "src/router/index.ts" \
  "src/views/config/ConfigLayout.vue"
```

**Wave 7（M12 + M13 同仓库 web-v3，Tier 3 disjoint）：**
```bash
bash numind-server/scripts/ndf/ndf-check-disjoint.sh \
  "src/views/config/__tests__/ConfigLayout.spec.ts" \
  "src/router/__tests__/guard.spec.ts"
```

每个 ndf-check-disjoint exit 0 才能并行 dispatch subagent；exit 1 时降级 Tier 4 串行。

---

## §3 S5 验证策略（M14 产出，但本节先 outline）

### admin-web 端

```bash
cd /private/tmp/wt-agent-mode-configurator-relocate-numind-admin-web
npm run lint && npm run type-check && npm run build
# 应全 exit 0；lint warning 数 ≤ baseline，error 0
```

手测：
1. dev 部署 → 登录 → 侧边栏不展示 "AI 助手"
2. URL 输 /agents → NotFoundView
3. "Agent 监控" + "合规规则" 可正常进入（功能保留）

### web-v3 端

```bash
cd /private/tmp/wt-agent-mode-configurator-relocate-numind-web-v3
npm run lint && npm run type-check && npm run build
npm run test  # 含 4 个搬迁 spec + ConfigLayout.spec + guard.spec = 11+ test
```

父账户单账户 e2e：
```bash
E2E_USERNAME=$E2E_USERNAME E2E_PASSWORD=$E2E_PASSWORD \
  npm run test:e2e -- e2e/agent-builder.spec.ts
```

子账户 Vitest 单测：包含在 ConfigLayout.spec + guard.spec 中（mock parent_user_id=123 模拟子账户）。

学员视角 e2e（沿用 #11 已有）应 PASS（不被本 feature 影响）。

### Prod 影响

- `git diff develop -- config_prod.yaml migrations/` 都应空
- 后端 `numind-server/internal/numind/` 无 diff
- web-v3 学员视角 view + api + store 0 改动

---

## §4 进度追踪

S4 实施期间，每完成一个 M task：
1. dispatch 2 个 reviewer subagent（并行）：spec compliance + code quality
2. P0/P1 修
3. 更新 manifest progress.reviewed_tasks += 1
4. 进下一 task

---

## §5 失败回退

任一 M task 失败：
- M2 失败（删错文件）→ git checkout HEAD 撤销
- M3-M11 失败（文件 sed 错或 type-check FAIL）→ git diff 看错在哪，手工修
- M9 router guard 改错破坏现有路由 → checkout 原版 + 重写
- 总体失败 → ndf-done 不会跑，worktree 保留待 debug

---

## §6 与其他活跃 feature 的关系

- **sop-stepnav-bookmark-star** (hotfix, H1)：完全独立的功能（SOP 步骤导航 ⭐ icon），不与本 feature 共享文件。可并发跑，无冲突。
- **agent-mode-e2e-rollout** (#14, S4 in progress)：已停止（standard track session 1 完成 Phase A Wave 1 3/12，handoff 给 session 2 但 session 2 尚未启动）。本 feature 不与之共享文件。

---

*Created 2026-05-22 15:30 +0800 · ai-s3*
