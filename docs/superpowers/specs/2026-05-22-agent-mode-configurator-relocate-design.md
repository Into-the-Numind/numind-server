# Spec: agent-mode-configurator-relocate (S2 Design)

> Track: **standard** · Stage: **S2** · Repos: **numind-admin-web + numind-web-v3** · 2026-05-22

---

## §0 Spec 适用范围

本 spec 给出 S4 implementer 可直接执行的**精确级别**指令：

- 每个删除文件 / 删除函数 / 删除路由 + 行号
- 每个新建文件的内容骨架（类型、import、CSS token 映射）
- 每个改动现有文件的 unified diff snippet

S4 implementer **不需要再做架构决定**，照 spec 执行即可。所有架构决定在 S1 proposal §4 D1-D14 锁定。

---

## §1 admin-web 端：删除 + 局部修改

### §1.1 整文件删除（17 文件）

```
src/views/agent/AgentList.vue
src/views/agent/AgentCreateChoose.vue
src/views/agent/TemplateGallery.vue
src/views/agent/AgentBuilder.vue
src/views/agent/AgentDetail.vue
src/views/agent/AgentEdit.vue
src/views/agent/AgentAdvancedEdit.vue
src/views/agent/components/AdvancedToggleConfirmModal.vue
src/views/agent/components/AfterSaveModal.vue
src/views/agent/components/AgentConfigTab.vue
src/views/agent/components/AgentHistoryTab.vue
src/views/agent/components/AgentStatsTab.vue
src/views/agent/components/AvatarPicker.vue
src/views/agent/components/ChipInput.vue
src/views/agent/components/CreditSlider.vue
src/views/agent/components/HistoryViewModal.vue
src/views/agent/components/QuestionnaireForm.vue
src/views/agent/components/validation.ts
src/views/agent/__tests__/AgentList.spec.ts
src/views/agent/__tests__/AgentBuilder.spec.ts
src/views/agent/__tests__/AgentAdvancedEdit.spec.ts
src/views/agent/__tests__/AgentHistoryTab.spec.ts
src/stores/agent.ts
```

**注意：`src/views/agent/AgentMonitoring.vue` 保留**（Numind 员工监控功能，#14 已 wire）。`src/views/agent/` 目录在搬完后只剩这 1 个文件 + 空的 `components/` 目录（components 应一起删，但 AgentMonitoring 不依赖任何 components 文件）。

**实施提示：** S4 implementer 用 `rm` 列出文件逐个删，**禁止**用 `rm -rf src/views/agent/` (会误删 AgentMonitoring.vue)。

### §1.2 修改 `src/api/agent.ts`

**最终状态（保留 27 行）：**

```typescript
// API wrappers for admin-only agent run monitoring endpoints.
// User-facing agent CRUD wrappers moved to numind-web-v3/src/api/agentBuilder.ts
// per feature agent-mode-configurator-relocate (2026-05-22).
//
// Admin monitoring endpoints (admin_token middleware):
//   GET  /v1/admin/agent-runs        — list runs (M-C4a backend)
//   POST /v1/admin/agent-runs/:id/cancel — force cancel (M-C3b backend)

import { get, post } from "./request";
import type { AgentRunDTO, ListResponse } from "@/types/agent";

export interface ListRunsParams {
  status?: "running" | "terminated" | "cancelled";
  page?: number;
  page_size?: number;
  parent_user_id?: number;
}

// GET /v1/admin/agent-runs — list agent runs with optional filters (M-C4a)
export function listAgentRunsApi(
  params: ListRunsParams = {},
): Promise<ListResponse<AgentRunDTO>> {
  return get<ListResponse<AgentRunDTO>>("/v1/admin/agent-runs", { params });
}

// POST /v1/admin/agent-runs/:id/cancel — force-cancel a running agent run (M-C3b)
export function cancelAgentRunApi(id: number): Promise<void> {
  return post<void>(`/v1/admin/agent-runs/${id}/cancel`);
}
```

**Diff vs 原版：**
- 删除 line 1-25（注释头中关于 9 个 user endpoint 的描述）
- 删除 line 9-18 `import` 中的 builder type（仅留 AgentRunDTO + ListResponse）
- 删除 line 20-24 `ListAgentsParams` interface
- 删除 line 26-76 9 个 user endpoint 函数（createAgentApi / listAgentsApi / getAgentApi / patchAgentApi / deleteAgentApi / listAgentHistoryApi / restoreAgentApi / toggleAgentAdvancedApi / listSkillTemplatesApi）
- 删除 `patch`, `del` 从 `./request` 的 import（保留 `get`, `post`）
- 保留 `ListRunsParams` + `listAgentRunsApi` + `cancelAgentRunApi`（原 line 78-99）

### §1.3 修改 `src/types/agent.ts`

**最终状态（保留 ~30 行）：**

```typescript
// Admin monitoring types — user-facing agent types moved to
// numind-web-v3/src/types/agentBuilder.ts per agent-mode-configurator-relocate (2026-05-22).
//
// Backend source: numind-server/internal/pkg/model/agent_run.go

// ============================================================
// API Response wrapper
// ============================================================

export interface ListResponse<T> {
  list: T[];
  total: number;
}

// ============================================================
// AgentRunDTO — admin monitoring (GET /v1/admin/agent-runs)
// Mirrors backend model.AgentRun (json tags)
// ============================================================

export interface AgentRunDTO {
  id: number;
  user_id: number;
  agent_definition_id: number;
  agent_name?: string;
  status: "running" | "terminated" | "cancelling" | "cancelled";
  terminal_reason?: string;
  trace_id?: string;
  cancellation_requested_at?: string | null;
  created_at: string;
  ended_at?: string | null;
  duration_ms?: number;
}
```

**Diff vs 原版（235 行 → 35 行）：**
- 删除 line 1-7（顶部 comment + Questionnaire 注释）
- 删除 line 9-66（Q6-Q12 union types + QuestionnaireAnswers interface + normalizeQuestionnaire 函数）
- 删除 line 68-77（ToolFlags interface）
- 删除 line 79-104（Agent interface）
- 删除 line 106-119（AgentHistory interface）
- 删除 line 121-137（SkillTemplate interface）
- 删除 line 139-159（CreateAgentPayload + PatchAgentPayload）
- 删除 line 161-207（AgentFormState + initialFormState）
- 保留 line 209-216（ListResponse — admin 也用）
- 保留 line 218-235（AgentRunDTO — admin 监控用）

**验证：** S4 implementer 完成后 `grep -r "from '@/types/agent'" src/` 应只命中 `AgentMonitoring.vue` 一处。

### §1.4 修改 `src/router/index.ts`

**Diff（删除 line 169-234 即 /agents/* 6 条路由 + agent-monitoring 上方的 comment "// AI Agent 助手 (feature #10 agent-mode-configurator-ux)"）：**

删除以下 6 个 children 路由对象（行号约 169-209）：
1. `path: "agents/builder"` (line 173)
2. `path: "agents"` (line 179)
3. `path: "agents/new"` (line 185)
4. `path: "agents/new/from-template"` (line 191)
5. `path: "agents/:id"` (line 197)
6. `path: "agents/:id/edit"` (line 204)

**保留** `path: "agent-monitoring"` (line 210-215) + compliance-rules 三条路由 (line 216-234)。

**验证：** S4 implementer 完成后 router build 仍 PASS，`/agents` 访问应返回 NotFoundView（默认 catch-all line 237-242 兜底）。

### §1.5 修改 `src/components/layout/AdminSidebar.vue`

**Diff（删除 `navItems` 数组中 line 134-135 一项 "AI 助手"）：**

```typescript
// 删除这两行（line 134-135）：
// Feature #10: agent-mode-configurator-ux
{ name: "agents", label: "AI 助手", icon: Bot, path: "/agents" },
```

**保留** "Agent 监控"（line 136-141）+ "合规规则"（line 143-148）。

**同时**删除顶部 import line 26 `Bot,`（lucide icon 不再用）：
- Before: `Bot, Activity, ShieldCheck,`
- After: `Activity, ShieldCheck,`

**验证：** S4 implementer 完成后 sidebar 仍展示 "Agent 监控" + "合规规则"，"AI 助手" 消失。

---

## §2 web-v3 端：新建

### §2.1 新文件目录树

```
src/components/common/DataTable.vue              ← port from admin-web（CSS token 改写）
src/components/common/NoticeBanner.vue           ← port from admin-web（AgentAdvancedEdit 依赖）
src/components/common/CheckboxGroup.vue          ← port from admin-web（QuestionnaireForm 依赖）
src/types/agentBuilder.ts                        ← 从 admin-web src/types/agent.ts 剥离 builder 类型
src/api/agentBuilder.ts                          ← 9 个 user endpoint wrapper（命名调整）
src/stores/agentBuilder.ts                       ← 从 admin-web src/stores/agent.ts 搬迁
src/views/config/agents/                         ← 整个目录新建
├── AgentList.vue                                ← 用 port 后的 DataTable
├── AgentCreateChoose.vue
├── TemplateGallery.vue
├── AgentBuilder.vue
├── AgentDetail.vue
├── AgentEdit.vue
├── AgentAdvancedEdit.vue
├── components/
│   ├── AdvancedToggleConfirmModal.vue
│   ├── AfterSaveModal.vue
│   ├── AgentConfigTab.vue
│   ├── AgentHistoryTab.vue
│   ├── AgentStatsTab.vue
│   ├── AvatarPicker.vue
│   ├── ChipInput.vue
│   ├── CreditSlider.vue
│   ├── HistoryViewModal.vue
│   ├── QuestionnaireForm.vue
│   └── validation.ts
└── __tests__/
    ├── AgentList.spec.ts
    ├── AgentBuilder.spec.ts
    ├── AgentAdvancedEdit.spec.ts
    └── AgentHistoryTab.spec.ts
```

### §2.2 DataTable.vue 搬迁 + CSS token 改写

**Source：** `numind-admin-web/src/components/common/DataTable.vue`（384 行）

**Destination：** `numind-web-v3/src/components/common/DataTable.vue`

**搬迁过程：**
1. cp 整个文件到 destination
2. 仅改 `<style scoped>` 内的 CSS 变量（按下表）
3. 模板和脚本部分**零改动**

**CSS token mapping（admin-web → web-v3）：**

| admin-web token | web-v3 token | 行号位置（参考） |
|----------------|-------------|-----------------|
| `var(--surface-lowest)` | `var(--surface)` | 多处 |
| `var(--surface-low)` | `var(--surface-tint)` | 多处 |
| `var(--surface-high)` | `var(--surface-hover)` | 多处 |
| `var(--surface-tint)` | `var(--surface-tint)` | unchanged |
| `var(--on-surface)` | `var(--text)` | 多处 |
| `var(--on-surface-variant)` | `var(--text-muted)` | 多处 |
| `var(--primary)` | `var(--primary)` | unchanged |
| `var(--font-body)` | `inherit` 或 删除 declaration | font-family 行 |
| `var(--font-label)` | `inherit` 或 删除 declaration | font-family 行 |
| `var(--text-sm)` | `0.875rem` (inline) | font-size declarations |
| `var(--text-xs)` | `0.75rem` (inline) | font-size declarations |
| `var(--shadow-sm)` | `var(--shadow-sm)` | unchanged |
| `var(--radius-sm)` | `var(--radius-sm)` | unchanged |
| `var(--transition-fast)` | `var(--transition-fast)` | unchanged |

**精确替换（sed 风格）：**
```bash
sed -i.bak \
  -e 's/var(--surface-lowest)/var(--surface)/g' \
  -e 's/var(--surface-low)/var(--surface-tint)/g' \
  -e 's/var(--surface-high)/var(--surface-hover)/g' \
  -e 's/var(--on-surface-variant)/var(--text-muted)/g' \
  -e 's/var(--on-surface)/var(--text)/g' \
  -e 's/var(--text-sm)/0.875rem/g' \
  -e 's/var(--text-xs)/0.75rem/g' \
  src/components/common/DataTable.vue
```

替换顺序重要：`--on-surface-variant` 必须在 `--on-surface` 前替换（避免前缀误匹配）。

**font-family declarations：** admin-web DataTable 可能用 `font-family: var(--font-body)` 等。改完后用 `grep "font-family.*var(--font-" src/components/common/DataTable.vue`，如有命中则删除整行（让 inherit）。

**Space tokens 注意（reviewer P2-1）：** DataTable 还用 `--space-1` / `--space-2` / `--space-3` / `--space-6` / `--space-12` 等 token。这些**在 web-v3 也存在同名**（参见 `src/shared/styles/variables.css`），**0 改动**。

**验证：** S4 implementer 跑 `npm run dev`，导航到任何用 DataTable 的页面（暂无 — AgentList 还没建），先在浏览器自测组件不报 CSS variable 未定义。

### §2.2b NoticeBanner.vue 搬迁

**Source：** `numind-admin-web/src/components/common/NoticeBanner.vue`（~82 行）

**Destination：** `numind-web-v3/src/components/common/NoticeBanner.vue`

**搬迁过程：**
1. cp 整个文件到 destination
2. 同 §2.2 的 CSS token map 改写（admin-web token → web-v3 token）

NoticeBanner 是静态 banner 组件，与 web-v3 现有 AppNotification（toast/queue）语义不同，**不可用 AppNotification 替代**。AgentAdvancedEdit.vue 依赖此组件展示"高级模式说明"banner。

S2 reviewer 已验证 web-v3 没有等价组件，必须独立 port。

### §2.2c CheckboxGroup.vue 搬迁

**Source：** `numind-admin-web/src/components/common/CheckboxGroup.vue`

**Destination：** `numind-web-v3/src/components/common/CheckboxGroup.vue`

**搬迁过程：** cp + CSS token map 改写（同 §2.2）。

CheckboxGroup 被 QuestionnaireForm.vue (Q6 + Q7 多选 checkbox) 使用，web-v3 无等价组件。

### §2.3 src/types/agentBuilder.ts 新建

**Source：** `numind-admin-web/src/types/agent.ts` line 1-216（去除 line 218-235 AgentRunDTO）

**Destination：** `numind-web-v3/src/types/agentBuilder.ts`

**搬迁过程：**
1. cp 文件到 destination 然后改文件名
2. 删除 line 218-235（AgentRunDTO interface）— 这部分留 admin-web
3. 修改文件头 comment：

```typescript
// Agent (Skill) types — parent-account configurator UI.
// Originally lived in numind-admin-web/src/types/agent.ts but relocated here
// in feature agent-mode-configurator-relocate (2026-05-22).
//
// Backend source: numind-server/internal/pkg/model/agent_definition.go
//
// Student-facing types are in src/types/agent.ts (do NOT confuse — that file
// is for the consumer view, this file is for the configurator view).
```

**最终类型清单：**
- Q6TaskType, Q7MaterialType, Q9WebSearch, Q12Style（4 个 union type）
- QuestionnaireAnswers
- normalizeQuestionnaire（函数）
- ToolFlags
- Agent
- AgentHistory
- SkillTemplate
- CreateAgentPayload
- PatchAgentPayload
- AgentFormState
- initialFormState（函数）
- ListResponse（也保留，与 admin-web 重复 3 行可接受）

### §2.4 src/api/agentBuilder.ts 新建

**Source：** `numind-admin-web/src/api/agent.ts` line 1-76（9 个 user endpoint 函数）

**Destination：** `numind-web-v3/src/api/agentBuilder.ts`

**搬迁过程：**

1. cp 9 个函数 + ListAgentsParams interface 到 destination
2. **重命名函数（去 `Api` 后缀）** —— web-v3 命名习惯
3. **改 import** —— web-v3 axios wrapper 不同（用 `request` default export，非 `{ get, post, patch, del }` named exports）
4. **改返回类型** —— web-v3 axios wrapper 返回 `AxiosResponse<{ data: T }>`，需要 `.data.data` 解构（参考 `src/api/agent.ts` 学员端）

**Axios wrapper pattern（reviewer P0-1 修正）：**

web-v3 `src/api/request.ts` 的 response 拦截器返回 `res as any`（line 176），其中 `res = response.data` 实际是 `ApiResponse<T> = { code, message, data: T }`。所以 `await request.get<U>(url)` resolve 后是 `ApiResponse` 对象。

正确解构 pattern（**复制学员端 `src/api/agent.ts` 的 idiom**）：
```typescript
const { data } = await request.get<{ data: T }>(url)
return data  // ✓ data 是 T，不是嵌套
```

错误 pattern（先前 spec 写错的）：
```typescript
const { data } = await request.get<{ data: T }>(url)
return data.data  // ✗ runtime undefined
```

**最终文件骨架（修正后）：**

```typescript
// API wrappers for /v1/agent/skills/* (9 endpoints, user_token middleware).
// Backend: numind-server feature #5 agent-mode-skill-system (merged e05498b6).
// Parent-account only — child accounts receive HTTP 403 from backend biz layer.
//
// Student-facing agent endpoints live in src/api/agent.ts (do NOT confuse).

import request from './request'
import type {
  Agent,
  AgentHistory,
  SkillTemplate,
  CreateAgentPayload,
  PatchAgentPayload,
  ListResponse,
} from '@/types/agentBuilder'

export interface ListAgentsParams {
  page?: number
  page_size?: number
  include_inactive?: boolean
}

// 1. POST /v1/agent/skills — Create
export const createAgent = async (payload: CreateAgentPayload): Promise<Agent> => {
  const { data } = await request.post<{ data: Agent }>('/v1/agent/skills', payload)
  return data
}

// 2. GET /v1/agent/skills — List (parent's own agents)
export const listAgents = async (
  params: ListAgentsParams = {},
): Promise<ListResponse<Agent>> => {
  const { data } = await request.get<{ data: ListResponse<Agent> }>(
    '/v1/agent/skills',
    { params },
  )
  return data
}

// 3. GET /v1/agent/skills/:id — Get one
export const getAgent = async (id: number): Promise<Agent> => {
  const { data } = await request.get<{ data: Agent }>(`/v1/agent/skills/${id}`)
  return data
}

// 4. PATCH /v1/agent/skills/:id — Partial update
export const patchAgent = async (
  id: number,
  payload: PatchAgentPayload,
): Promise<Agent> => {
  const { data } = await request.patch<{ data: Agent }>(`/v1/agent/skills/${id}`, payload)
  return data
}

// 5. DELETE /v1/agent/skills/:id — Soft delete (is_active=false)
export const deleteAgent = async (id: number): Promise<void> => {
  await request.delete(`/v1/agent/skills/${id}`)
}

// 6. GET /v1/agent/skills/:id/history — Version history
export const listAgentHistory = async (
  id: number,
): Promise<ListResponse<AgentHistory>> => {
  const { data } = await request.get<{ data: ListResponse<AgentHistory> }>(
    `/v1/agent/skills/${id}/history`,
  )
  return data
}

// 7. POST /v1/agent/skills/:id/restore/:version — Restore (creates new version)
export const restoreAgent = async (id: number, version: number): Promise<Agent> => {
  const { data } = await request.post<{ data: Agent }>(
    `/v1/agent/skills/${id}/restore/${version}`,
  )
  return data
}

// 8. POST /v1/agent/skills/:id/advanced-toggle — Switch to advanced (irreversible)
export const toggleAgentAdvanced = async (id: number): Promise<Agent> => {
  const { data } = await request.post<{ data: Agent }>(
    `/v1/agent/skills/${id}/advanced-toggle`,
  )
  return data
}

// 9. GET /v1/agent/skill-templates — Built-in templates (no pagination)
export const listSkillTemplates = async (): Promise<SkillTemplate[]> => {
  const { data } = await request.get<{ data: SkillTemplate[] }>(
    '/v1/agent/skill-templates',
  )
  return data
}
```

**调用方更新：** admin-web store 调 `createAgentApi(...)`；web-v3 store 调 `createAgent(...)`。所有调用点在 §2.5 store 更新一并改。

### §2.5 src/stores/agentBuilder.ts 新建

**Source：** `numind-admin-web/src/stores/agent.ts`（setup syntax，240 行）

**Destination：** `numind-web-v3/src/stores/agentBuilder.ts`

**搬迁过程：**

1. cp 整个 store 到 destination
2. 改 `defineStore("agent", ...)` → `defineStore("agentBuilder", ...)`
3. 改 imports：
   - `from "@/api/agent"` → `from "@/api/agentBuilder"`
   - `from "@/types/agent"` → `from "@/types/agentBuilder"`
4. 改函数调用（去 `Api` 后缀）：
   - `listAgentsApi` → `listAgents`
   - `getAgentApi` → `getAgent`
   - `createAgentApi` → `createAgent`
   - `patchAgentApi` → `patchAgent`
   - `deleteAgentApi` → `deleteAgent`
   - `listAgentHistoryApi` → `listAgentHistory`
   - `restoreAgentApi` → `restoreAgent`
   - `toggleAgentAdvancedApi` → `toggleAgentAdvanced`
   - `listSkillTemplatesApi` → `listSkillTemplates`
5. 文件头 comment 改：

```typescript
// Pinia store for parent-account agent (Skill) CRUD, history, restore, advanced toggle.
// Setup syntax (per numind-web-v3 CLAUDE.md §2).
// Relocated from numind-admin-web/src/stores/agent.ts in agent-mode-configurator-relocate (2026-05-22).
//
// Student-facing agent chat store is src/stores/agentChat.ts (do NOT confuse).
```

**其他代码逻辑** 0 改动（normalizeAgent / fetchList / fetchOne / create / update / softDelete / fetchHistory / restore / toggleAdvanced / fetchTemplates / $reset）。

### §2.6 src/views/config/agents/*.vue 7 view 搬迁

**通用搬迁规则（应用到所有 7 view）：**

| admin-web 写法 | web-v3 写法 |
|---------------|-------------|
| `import { useToast } from '@/composables/useToast'` | `import { useNotificationsStore } from '@/stores/notifications'` |
| `const toast = useToast()` | `const notifications = useNotificationsStore()` |
| `toast.success(msg)` | `notifications.success(msg)` |
| `toast.error(msg)` | `notifications.error(msg)` |
| `toast.info(msg)` | `notifications.info(msg)` |
| `import { useAgentStore } from '@/stores/agent'` | `import { useAgentBuilderStore } from '@/stores/agentBuilder'` |
| `const store = useAgentStore()` | `const store = useAgentBuilderStore()` |
| `router.push('/agents/new')` | `router.push('/config/agents/new')` |
| `router.push('/agents/${id}')` | `router.push('/config/agents/${id}')` |
| `router.push('/agents/${id}/edit')` | `router.push('/config/agents/${id}/edit')` |
| `router.push('/agents/new/from-template')` | `router.push('/config/agents/new/from-template')` |
| `router.push('/agents/builder?from=...')` | `router.push('/config/agents/builder?from=...')` |
| `router.push('/agents')` | `router.push('/config/agents')` |
| `import type { Agent } from '@/types/agent'` | `import type { Agent } from '@/types/agentBuilder'` |
| `import DataTable from '@/components/common/DataTable.vue'` | unchanged（同名同位置，新 port） |
| `import ConfirmModal from '@/components/common/ConfirmModal.vue'` | unchanged（web-v3 也有同名组件） |
| `import AppButton from '@/components/common/AppButton.vue'` | unchanged（web-v3 也有同名组件） |

**Per-file 特殊适配：**

| 文件 | 特殊改动 |
|------|---------|
| AgentList.vue | DataTable import 路径不变；search filter 客户端实现保留；page-header / 操作列保留；4 处 `router.push('/agents/...')` 全改 `/config/agents/...` |
| AgentBuilder.vue | 12 题表单不动；**3 处 router.push 全改**（line 252 `/agents/${id}` → `/config/agents/${id}`；line 259 `/agents/${id}` → `/config/agents/${id}`；line 275 `/agents/${id}/edit` → `/config/agents/${id}/edit`）；onBeforeRouteLeave 内引用的路径如有也改 |
| AgentDetail.vue | 3 Tab 不动；router.push 路径改；history Tab 子组件路径 `from '../components/'` 不变（相对路径） |
| AgentAdvancedEdit.vue | Markdown editor 不动；NoticeBanner import 路径不变（已 port）；toggle 后 `router.replace('/agents/${id}')` → `'/config/agents/${id}'` |
| AgentEdit.vue | wrapper view，router.params 读取不变；其内部 router.push 如有则改 |
| AgentCreateChoose.vue | 3 个 router-link `:to="/agents/..."` 在 template 中改 `:to="/config/agents/..."` |
| TemplateGallery.vue | 模板卡片 + 预览 Modal 不动；select 后 `router.push('/agents/builder?from=template:${id}')` → `'/config/agents/builder?from=template:${id}'` |

**实施方法：** S4 implementer 用 `grep -n "/agents" src/views/config/agents/*.vue src/views/config/agents/components/*.vue` 列出所有命中，逐行手工改（避免误改）。所有 `'/agents/...'` 字符串字面量 + `:to="/agents/..."` template 字面量都要换 `/config/agents/...` 前缀。

### §2.7 src/views/config/agents/components/ 11 文件搬迁

**通用规则：** 全部 cp 文件到 destination，**仅改 import 路径**：
- `from '../../components/'` 之类相对路径若涉及 component 间引用，重计算（实际全是平铺 components 平级，相对引用应是 `./XxxComponent.vue`）
- `from '@/types/agent'` → `from '@/types/agentBuilder'`
- `from '@/composables/useToast'` → `from '@/stores/notifications'`（如有）

**validation.ts** 是纯函数，0 改动（除 import 类型路径）。

### §2.8 src/views/config/agents/__tests__/*.spec.ts 4 spec 搬迁

每个 spec **mock 替换清单**：

| spec | admin-web mock | web-v3 等价物 |
|------|---------------|--------------|
| AgentList.spec.ts | `vi.mock('@/api/agent', { listAgentsApi, deleteAgentApi, ... })` | `vi.mock('@/api/agentBuilder', { listAgents, deleteAgent, ... })` (函数去 Api 后缀) |
| AgentList.spec.ts | `vi.mock('@/composables/useToast', () => ({ useToast: () => toastSpy }))` | `vi.mock('@/stores/notifications', () => ({ useNotificationsStore: () => notifySpy }))` |
| AgentList.spec.ts | `import AgentList from '@/views/agent/AgentList.vue'` | `import AgentList from '@/views/config/agents/AgentList.vue'` |
| AgentList.spec.ts | `import * as agentApi from '@/api/agent'` | `import * as agentApi from '@/api/agentBuilder'` |
| AgentBuilder.spec.ts | 同 List + `vi.mock('vue-router', ...)` | 路径改 + mock 包名不变 |
| AgentAdvancedEdit.spec.ts | + `vi.mock('@/stores/agent', ...)` | `vi.mock('@/stores/agentBuilder', ...)` |
| AgentHistoryTab.spec.ts | 用 `attachTo: document.body` mount option（line 97 + 136）| 改用 `global.stubs.Teleport = true`（删 attachTo） — reviewer P1-1 验证：测试用 `wrapper.findComponent(ConfirmModal)` 与 `wrapper.findAll('button')` 风格，stub 模式下 modal 内联渲染于 wrapper vnode tree，断言仍工作；同时去掉 `beforeEach` 中的 `document.body.innerHTML = ""` cleanup（变 no-op，无副作用） |
| 全部 | `import type { Agent } from '@/types/agent'` | `from '@/types/agentBuilder'` |

**spec 内部测试逻辑** 0 改动（断言、test case 数量）。

### §2.9 src/router/index.ts 改动

**新增 7 条路由**（在 `/config/...` ConfigLayout children 内部，与 chatbots/sop-templates/knowledge-bases 并列）。

参考现有 router/index.ts 第 99-140 行 `/config/...` 配置：

```typescript
// 现有 /config 路由结构（line 99-140）：
{
  path: '/config',
  component: () => import('@/views/config/ConfigLayout.vue'),
  meta: { requiresAuth: true, requiresParent: true },
  children: [
    { path: '', redirect: '/config/chatbots' },
    { path: 'chatbots', ... },
    { path: 'chatbots/:id/edit', ... },
    { path: 'sop-templates', ... },
    { path: 'sop-templates/:id/edit', ... },
    { path: 'knowledge-bases', ... },
    { path: 'knowledge-bases/:id', ... },
    // ✱ 新增 7 条 /config/agents/* 路由（与上述并列）
  ]
}
```

**新增 children 路由对象（7 条）：**

```typescript
{
  path: 'agents',
  name: 'config-agents',
  component: () => import('@/views/config/agents/AgentList.vue'),
  meta: { title: 'AI 助手', requiresAuth: true, requiresParent: true },
},
{
  path: 'agents/new',
  name: 'config-agents-new',
  component: () => import('@/views/config/agents/AgentCreateChoose.vue'),
  meta: { title: '创建助手', requiresAuth: true, requiresParent: true },
},
{
  path: 'agents/new/from-template',
  name: 'config-agents-from-template',
  component: () => import('@/views/config/agents/TemplateGallery.vue'),
  meta: { title: '选择模板', requiresAuth: true, requiresParent: true },
},
{
  path: 'agents/builder',
  name: 'config-agents-builder',
  component: () => import('@/views/config/agents/AgentBuilder.vue'),
  meta: { title: '创建助手', requiresAuth: true, requiresParent: true },
},
{
  path: 'agents/:id',
  name: 'config-agents-detail',
  component: () => import('@/views/config/agents/AgentDetail.vue'),
  props: true,
  meta: { title: '助手详情', requiresAuth: true, requiresParent: true },
},
{
  path: 'agents/:id/edit',
  name: 'config-agents-edit',
  component: () => import('@/views/config/agents/AgentEdit.vue'),
  props: true,
  meta: { title: '编辑助手', requiresAuth: true, requiresParent: true },
},
```

**注意：** 不直接为 AgentAdvancedEdit 配独立路由——AgentEdit wrapper view 根据 `agent.advanced_mode` 渲染子组件（AgentBuilder 或 AgentAdvancedEdit），URL 稳定（S1 决策 D5 沿用）。

**guard 改造**（line 199-216 router.beforeEach）：

**Before：**
```typescript
router.beforeEach((to, from, next) => {
  const userStore = useUserStore()

  if (!to.meta.guest && !userStore.isLoggedIn) {
    next({ name: 'login', query: { redirect: to.fullPath } })
    return
  }
  if (to.meta.guest && userStore.isLoggedIn && to.name === 'login') {
    next({ name: 'home' })
    return
  }
  if ((to.meta.parentOnly || to.meta.requiresParent) && !userStore.isParentUser) {
    next({ name: 'home' })
    return
  }

  const title = to.meta.title as string | undefined
  document.title = title ? `${title} - 莫小派` : '莫小派'
  next()
})
```

**After：**
```typescript
router.beforeEach(async (to) => {
  const userStore = useUserStore()

  if (!to.meta.guest && !userStore.isLoggedIn) {
    return { name: 'login', query: { redirect: to.fullPath } }
  }
  if (to.meta.guest && userStore.isLoggedIn && to.name === 'login') {
    return { name: 'home' }
  }
  // requiresParent guard — wait for userInfo if not loaded, then check
  if ((to.meta.parentOnly || to.meta.requiresParent)) {
    if (!userStore.userInfo) {
      await userStore.fetchUserInfo()
    }
    if (!userStore.isParentUser) {
      useNotificationsStore().info('AI 助手配置仅父账户可访问')
      return { name: 'home' }
    }
  }

  const title = to.meta.title as string | undefined
  document.title = title ? `${title} - 莫小派` : '莫小派'
})
```

**新增 imports（顶部）：**
```typescript
import { useNotificationsStore } from '@/stores/notifications'
```

### §2.10 src/views/config/ConfigLayout.vue 改动

**Diff（line 26-40）：**

**Before：**
```typescript
const route = useRoute()

const tabs = [
  { label: '智能体管理', path: '/config/chatbots' },
  { label: 'SOP 管理', path: '/config/sop-templates' },
  { label: '知识库管理', path: '/config/knowledge-bases' }
]

function isActive(path: string) {
  return route.path.startsWith(path)
}
```

**After：**
```typescript
import { computed } from 'vue'
import { useUserStore } from '@/stores/user'

const route = useRoute()
const userStore = useUserStore()

interface Tab {
  label: string
  path: string
  parentOnly?: boolean
}

const allTabs: Tab[] = [
  { label: '智能体管理', path: '/config/chatbots' },
  { label: 'SOP 管理', path: '/config/sop-templates' },
  { label: '知识库管理', path: '/config/knowledge-bases' },
  { label: 'AI 助手', path: '/config/agents', parentOnly: true },
]

const tabs = computed(() => {
  // userInfo === null → 默认隐藏 parentOnly tab（避免 flash）
  if (!userStore.userInfo) return allTabs.filter(t => !t.parentOnly)
  return allTabs.filter(t => !t.parentOnly || userStore.isParentUser)
})

function isActive(path: string) {
  return route.path.startsWith(path)
}
```

**template 部分** 0 改动（`v-for="tab in tabs"` 现在引用 computed ref，自动 reactivity）。

---

## §3 测试与验证策略

### §3.1 S5 验证完整策略

由 S3 plan 最后一个 task 锁定（NDF 规则 10）。本 spec 提议：

| 验证项 | 工具 | 何时运行 |
|--------|------|---------|
| admin-web lint | `npm run lint` in worktree | S5 第 1 步 |
| admin-web type-check | `npm run type-check` in worktree | S5 第 2 步 |
| admin-web build | `npm run build` in worktree | S5 第 3 步 |
| admin-web AgentMonitoring smoke | dev 部署后浏览器手动 | S5 第 4 步（无 baseline 退化即可） |
| web-v3 lint | `npm run lint` in worktree | S5 第 5 步 |
| web-v3 type-check | `npm run type-check` in worktree | S5 第 6 步 |
| web-v3 build | `npm run build` in worktree | S5 第 7 步 |
| web-v3 4 个搬迁 spec | `npm run test -- src/views/config/agents/__tests__` | S5 第 8 步 |
| web-v3 ConfigLayout tabs filter test | `npm run test -- src/views/config/__tests__/ConfigLayout.spec.ts` | S5 第 9 步（新增 spec） |
| web-v3 父账户 e2e | `E2E_USERNAME=... npm run test:e2e -- e2e/agent-builder.spec.ts` | S5 第 10 步（新增 e2e） |

### §3.2 新增测试文件

| 文件 | 覆盖 | 测试数 估算 |
|------|------|------------|
| `src/views/config/__tests__/ConfigLayout.spec.ts` | tabs 过滤逻辑（父账户/子账户/未登录） | 3 |
| `src/router/__tests__/guard.spec.ts`（如不存在） | requiresParent guard async 行为 | 4 (with/without userInfo + parent/child) |
| `e2e/agent-builder.spec.ts` | 父账户主流程：登录→列表→创建→保存→详情→删除 | 1 main flow |

**ConfigLayout.spec.ts mock template（reviewer P2-3）：**

```typescript
import { describe, it, expect, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import { createRouter, createWebHistory } from 'vue-router'
import ConfigLayout from '@/views/config/ConfigLayout.vue'
import { useUserStore } from '@/stores/user'

function mountLayout(userInfoState: 'null' | 'parent' | 'child') {
  const pinia = createTestingPinia({
    createSpy: vi.fn,
    initialState: {
      user: {
        userInfo: userInfoState === 'null' ? null
          : userInfoState === 'parent' ? { id: 1, parent_user_id: null }
          : { id: 2, parent_user_id: 1 },
      },
    },
  })
  const router = createRouter({
    history: createWebHistory(),
    routes: [{ path: '/', component: { template: '<div/>' } }],
  })
  return mount(ConfigLayout, {
    global: { plugins: [pinia, router], stubs: { MainLayout: true } },
  })
}

describe('ConfigLayout tabs', () => {
  it('父账户：4 tab', () => {
    const wrapper = mountLayout('parent')
    const links = wrapper.findAll('.config-tab')
    expect(links).toHaveLength(4)
    expect(links.map(l => l.text())).toContain('AI 助手')
  })
  it('子账户：3 tab（无 AI 助手）', () => {
    const wrapper = mountLayout('child')
    const links = wrapper.findAll('.config-tab')
    expect(links).toHaveLength(3)
    expect(links.map(l => l.text())).not.toContain('AI 助手')
  })
  it('未 fetch 完 userInfo=null：3 tab（默认隐藏 parentOnly，避免 flash）', () => {
    const wrapper = mountLayout('null')
    const links = wrapper.findAll('.config-tab')
    expect(links).toHaveLength(3)
    expect(links.map(l => l.text())).not.toContain('AI 助手')
  })
})
```

### §3.3 baseline 捕获

S4 启动前在每个 worktree 跑一次：

```bash
cd /private/tmp/wt-agent-mode-configurator-relocate-numind-admin-web
npm install
npm run lint 2>&1 | grep -E "warning|error" | wc -l > /tmp/admin-web-baseline.txt
cd /private/tmp/wt-agent-mode-configurator-relocate-numind-web-v3
npm install
npm run lint 2>&1 | grep -E "warning|error" | wc -l > /tmp/web-v3-baseline.txt
```

S5 验收：post-feature 数字 ≤ baseline，error 必须 0。

---

## §4 Out of scope 重申

- 后端 API 改动 / migrations
- AgentMonitoring 搬到 web-v3（v2 需新后端 endpoint）
- 合规规则搬到 web-v3
- ChatbotList / SopTemplateList / KnowledgeBaseList refactor to DataTable（独立 follow-up micro）
- prod 部署 + git tag
- 12 题问卷重新设计

---

## §5 实施验证 checkpoint（给 reviewer 用）

S2 reviewer 应核查：

- [ ] §1.2 admin-web src/api/agent.ts 最终状态可编译（保留 2 函数依赖的 import 完整）
- [ ] §1.3 admin-web src/types/agent.ts 最终状态可编译（AgentRunDTO 不依赖被删类型）
- [ ] §2.2 DataTable CSS token 映射完整覆盖 admin-web 原文件用到的所有变量
- [ ] §2.4 web-v3 agentBuilder.ts 函数命名 + 返回类型与 web-v3 axios wrapper 兼容
- [ ] §2.5 store import 替换完整
- [ ] §2.6 view 搬迁的路径改写覆盖所有 router.push 调用
- [ ] §2.8 4 spec mock 替换完整
- [ ] §2.9 router guard 改写无破坏性（现有非 requiresParent 路由仍正常工作）
- [ ] §2.10 ConfigLayout 改 computed 后 userInfo === null 状态测试可证明无 flash

---

*Created 2026-05-22 15:00 +0800 · ai-s2*
