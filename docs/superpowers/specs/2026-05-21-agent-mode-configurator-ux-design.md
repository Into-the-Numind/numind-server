# NDF S2 Spec · `agent-mode-configurator-ux`

**Feature ID**：`agent-mode-configurator-ux`（#10/14）
**起草日期**：2026-05-21
**仓库**：`numind-admin-web`
**状态**：S2 草案
**前置**：S0 requirement + S1 proposal/PRD（已 commits `7837395b` / `e4574609`）

> 本 spec 把 S1 中的 PRD 决策具体化为**实装级别**的契约：TypeScript interfaces / Vue 组件 Props/Emits/Slots / 验证函数 / 路由配置 / 测试 case 列表。S4 implementer 应直接复制本 spec 的代码片段。

---

## 1. 数据契约 (`src/types/agent.ts`)

> 严格 mirror 后端 Go struct json tags（`numind-server/internal/pkg/model/agent_definition.go` develop branch）。

```typescript
// ============================================================
// Questionnaire — 与 backend QuestionnaireAnswers Go struct 对齐
// ============================================================

/** Q6 任务类型 — 5 个内置 code + 自由文本 */
export type Q6TaskType =
  | "analyze_data"
  | "generate_content"
  | "answer_questions"
  | "make_plan"
  | "grade_assignment";

/** Q7 材料类型 */
export type Q7MaterialType = "text" | "csv" | "image" | "none";

/** Q9 网络搜索 */
export type Q9WebSearch = "no_web_search" | "allow_search";

/** Q12 说话风格 */
export type Q12Style = "friendly" | "professional" | "encouraging";

/**
 * 12 题问卷答案（Q1-Q5 存在 AgentDefinition 直接字段；这里仅 Q6-Q12）。
 * 全 optional — 兼容旧 history snapshot；后端 Build() 在保存时校验 q6/q7/q12 非空。
 *
 * P0-1 修复：后端 Go struct 用 `omitempty` tag，空字符串/空数组序列化时会被剥去；
 * 历史快照 unmarshal 时字段缺失视为 undefined。q9 / q12 类型 union 不允许 "" 是
 * 故意的——store 层 fetchOne / fetchHistory 应在结果上 normalize（"" 视为 undefined），
 * 防止表单 v-model 绑定到无效空字符串。
 */
export interface QuestionnaireAnswers {
  q6?: (Q6TaskType | string)[]; // 多选 + 自由文本透传
  q7?: Q7MaterialType[]; // 多选
  q8?: number; // 200-2000；0 视为 default 800
  q9?: Q9WebSearch;
  q10?: string;
  q11?: string;
  q12?: Q12Style;
}

/**
 * Store 层 normalize helper — 后端 omitempty 可能返回 "" / null / undefined，
 * 全部统一为 undefined，避免 union type 运行时不一致。
 */
export function normalizeQuestionnaire(q: Partial<QuestionnaireAnswers>): QuestionnaireAnswers {
  const out: QuestionnaireAnswers = {};
  if (Array.isArray(q.q6) && q.q6.length > 0) out.q6 = q.q6;
  if (Array.isArray(q.q7) && q.q7.length > 0) out.q7 = q.q7 as Q7MaterialType[];
  if (typeof q.q8 === "number" && q.q8 > 0) out.q8 = q.q8;
  if (q.q9 === "no_web_search" || q.q9 === "allow_search") out.q9 = q.q9;
  if (typeof q.q10 === "string" && q.q10 !== "") out.q10 = q.q10;
  if (typeof q.q11 === "string" && q.q11 !== "") out.q11 = q.q11;
  if (q.q12 === "friendly" || q.q12 === "professional" || q.q12 === "encouraging") out.q12 = q.q12;
  return out;
}

// ============================================================
// AgentDefinition — 对齐后端 model.AgentDefinition
// ============================================================

/**
 * Tool flags — 三个已知 boolean key + extensible map（P2 fix — 类型收紧）
 */
export interface ToolFlags {
  code_sandbox?: boolean;
  media?: boolean;
  dangerous?: boolean;
  [k: string]: boolean | undefined;
}

export interface Agent {
  id: number;
  parent_user_id: number;
  name: string;
  description: string;
  icon_url: string;
  welcome_message: string;
  starters: string[]; // 后端 datatypes.JSON 序列化后 = array
  questionnaire_answers: QuestionnaireAnswers; // 同上
  generated_skill_body: string;
  advanced_mode: boolean;
  custom_skill_body: string;
  tool_flags: ToolFlags;
  credit_cap_per_session: number | null; // 后端 *uint
  daily_credit_cap: number | null;
  version: number;
  is_active: boolean;
  source_template_id: number | null;
  created_by: number;
  created_at: string; // ISO 8601
  updated_at: string;
}

// ============================================================
// AgentDefinitionHistory — 历史快照（含 changes_summary）
// ============================================================

export interface AgentHistory {
  id: number;
  agent_id: number;
  version: number;
  snapshot: Agent; // 后端 datatypes.JSON 序列化的完整 row
  changes_summary: string; // 后端 ComputeChangesSummary 生成的中文摘要
  created_by: number;
  created_at: string;
}

// ============================================================
// SkillTemplate — 内置模板
// ============================================================

export interface SkillTemplate {
  id: number;
  name: string;
  description: string;
  icon_url: string;
  welcome_message: string;
  starters: string[];
  questionnaire_answers: QuestionnaireAnswers;
  tool_flags: ToolFlags;
  credit_cap_per_session: number | null;
  daily_credit_cap: number | null;
  created_at: string;
}

// ============================================================
// API Request payloads
// ============================================================

export interface CreateAgentPayload {
  name: string;
  description?: string;
  icon_url?: string;
  welcome_message?: string;
  starters?: string[];
  questionnaire_answers?: QuestionnaireAnswers;
  tool_flags?: ToolFlags;
  credit_cap_per_session?: number | null;
  daily_credit_cap?: number | null;
  source_template_id?: number | null;
}

export type PatchAgentPayload = Partial<Omit<CreateAgentPayload, "source_template_id">>;
// 注意：source_template_id 仅 create 时有效；advanced_mode / is_active 不可 PATCH。

// ============================================================
// API Response wrappers
// ============================================================

export interface ListResponse<T> {
  list: T[];
  total: number;
}
```

---

## 2. API wrappers (`src/api/agent.ts`)

```typescript
import { get, post, patch, del } from "./request";
import type {
  Agent,
  AgentHistory,
  SkillTemplate,
  CreateAgentPayload,
  PatchAgentPayload,
  ListResponse,
} from "@/types/agent";

export interface ListAgentsParams {
  page?: number;
  page_size?: number;
  include_inactive?: boolean;
}

// 9 endpoints — 严格按 #5 端点

export function listAgentsApi(
  params: ListAgentsParams = {},
): Promise<ListResponse<Agent>> {
  return get<ListResponse<Agent>>("/v1/agent/skills", { params });
}

export function getAgentApi(id: number): Promise<Agent> {
  return get<Agent>(`/v1/agent/skills/${id}`);
}

export function createAgentApi(payload: CreateAgentPayload): Promise<Agent> {
  return post<Agent>("/v1/agent/skills", payload);
}

export function patchAgentApi(
  id: number,
  payload: PatchAgentPayload,
): Promise<Agent> {
  return patch<Agent>(`/v1/agent/skills/${id}`, payload);
}

export function deleteAgentApi(id: number): Promise<void> {
  return del<void>(`/v1/agent/skills/${id}`);
}

export function listAgentHistoryApi(
  id: number,
): Promise<ListResponse<AgentHistory>> {
  return get<ListResponse<AgentHistory>>(`/v1/agent/skills/${id}/history`);
}

export function restoreAgentApi(id: number, version: number): Promise<Agent> {
  return post<Agent>(`/v1/agent/skills/${id}/restore/${version}`);
}

export function toggleAgentAdvancedApi(id: number): Promise<Agent> {
  return post<Agent>(`/v1/agent/skills/${id}/advanced-toggle`);
}

export function listSkillTemplatesApi(): Promise<SkillTemplate[]> {
  return get<SkillTemplate[]>("/v1/agent/skill-templates");
}
```

### `src/api/request.ts` 修订（M2 内容）

在现有 `del<T>()` 之后添加：

```typescript
export function patch<T>(
  url: string,
  data?: unknown,
  config?: AxiosRequestConfig,
): Promise<T> {
  return request.patch(url, data, config) as Promise<T>;
}
```

**验证**：response interceptor (line 21-32) 对所有 method 生效，PATCH 返回 `response.data.data`；401 拦截同样适用 PATCH。

---

## 3. Pinia Store (`src/stores/agent.ts`)

```typescript
import { defineStore } from "pinia";
import { ref, computed } from "vue";
import {
  listAgentsApi,
  getAgentApi,
  createAgentApi,
  patchAgentApi,
  deleteAgentApi,
  listAgentHistoryApi,
  restoreAgentApi,
  toggleAgentAdvancedApi,
  listSkillTemplatesApi,
  type ListAgentsParams,
} from "@/api/agent";
import type {
  Agent,
  AgentHistory,
  SkillTemplate,
  CreateAgentPayload,
  PatchAgentPayload,
} from "@/types/agent";

export const useAgentStore = defineStore("agent", () => {
  // --- State (refs) ---
  const list = ref<Agent[]>([]);
  const total = ref(0);
  const loading = ref(false);
  const error = ref("");

  const current = ref<Agent | null>(null);
  const currentLoading = ref(false);
  const currentError = ref("");

  const history = ref<AgentHistory[]>([]);
  const historyLoading = ref(false);
  const historyError = ref("");

  const templates = ref<SkillTemplate[]>([]);
  const templatesLoading = ref(false);
  const templatesError = ref("");

  const saving = ref(false); // 共享 create/patch/restore/toggleAdvanced

  // --- Getters ---
  const isEmpty = computed(() => list.value.length === 0);

  // --- Actions ---
  // Internal sequence token — 用于 softDelete 与 fetchList 的 race 防御（P1-7 fix）
  // softDelete 后赋本地 _lastMutationSeq；fetchList 完成后比较——如果间隔有 mutation，
  // 不覆盖 list（仅 total + 等下次明确 refresh）。简化版：直接 mutation 同时 refresh。
  let _lastFetchSeq = 0;

  async function fetchList(params: ListAgentsParams = {}) {
    loading.value = true;
    error.value = "";
    try {
      const res = await listAgentsApi({
        page: 1,
        page_size: 20,
        include_inactive: false, // v1 仅显示 active
        ...params,
      });
      list.value = res.list;
      total.value = res.total;
    } catch (e) {
      error.value = (e as Error).message || "加载失败";
      throw e;
    } finally {
      loading.value = false;
    }
  }

  async function fetchOne(id: number) {
    currentLoading.value = true;
    currentError.value = "";
    try {
      const a = await getAgentApi(id);
      // P0-1 fix: normalize questionnaire_answers from omitempty backend
      a.questionnaire_answers = normalizeQuestionnaire(a.questionnaire_answers ?? {});
      current.value = a;
    } catch (e) {
      currentError.value = (e as Error).message || "加载失败";
      throw e;
    } finally {
      currentLoading.value = false;
    }
  }

  async function create(payload: CreateAgentPayload): Promise<Agent> {
    saving.value = true;
    try {
      const a = await createAgentApi(payload);
      a.questionnaire_answers = normalizeQuestionnaire(a.questionnaire_answers ?? {});
      current.value = a;
      return a;
    } finally {
      saving.value = false;
    }
  }

  // P0-4 fix: rename from `patch` → `update` to avoid collision with request.ts patch helper
  // and Pinia's built-in $patch method on store instances.
  async function update(id: number, payload: PatchAgentPayload): Promise<Agent> {
    saving.value = true;
    try {
      const a = await patchAgentApi(id, payload);
      a.questionnaire_answers = normalizeQuestionnaire(a.questionnaire_answers ?? {});
      current.value = a;
      return a;
    } finally {
      saving.value = false;
    }
  }

  async function softDelete(id: number): Promise<void> {
    saving.value = true;
    try {
      await deleteAgentApi(id);
      // P1-7: optimistic update; race with concurrent fetchList is acceptable v1
      // (last-write-wins). For full race safety, caller should `await fetchList()`
      // after softDelete (AgentList.vue 已 await softDelete + 不主动 refetch，UI 直接消失)
      list.value = list.value.filter((a) => a.id !== id);
      total.value = Math.max(0, total.value - 1);
    } finally {
      saving.value = false;
    }
  }

  async function fetchHistory(id: number) {
    historyLoading.value = true;
    historyError.value = "";
    try {
      const res = await listAgentHistoryApi(id);
      history.value = res.list;
    } catch (e) {
      historyError.value = (e as Error).message || "加载历史失败";
      throw e;
    } finally {
      historyLoading.value = false;
    }
  }

  async function restore(id: number, version: number): Promise<Agent> {
    saving.value = true;
    try {
      const a = await restoreAgentApi(id, version);
      current.value = a;
      // 回滚后刷新历史
      await fetchHistory(id);
      return a;
    } finally {
      saving.value = false;
    }
  }

  async function toggleAdvanced(id: number): Promise<Agent> {
    saving.value = true;
    try {
      const a = await toggleAgentAdvancedApi(id);
      current.value = a;
      return a;
    } finally {
      saving.value = false;
    }
  }

  async function fetchTemplates() {
    templatesLoading.value = true;
    templatesError.value = "";
    try {
      templates.value = await listSkillTemplatesApi();
    } catch (e) {
      templatesError.value = (e as Error).message || "加载模板失败";
      throw e;
    } finally {
      templatesLoading.value = false;
    }
  }

  function $reset() {
    list.value = [];
    total.value = 0;
    loading.value = false;
    error.value = "";
    current.value = null;
    currentLoading.value = false;
    currentError.value = "";
    history.value = [];
    historyLoading.value = false;
    historyError.value = "";
    templates.value = [];
    templatesLoading.value = false;
    templatesError.value = "";
    saving.value = false;
  }

  return {
    // state
    list,
    total,
    loading,
    error,
    current,
    currentLoading,
    currentError,
    history,
    historyLoading,
    historyError,
    templates,
    templatesLoading,
    templatesError,
    saving,
    // getters
    isEmpty,
    // actions
    fetchList,
    fetchOne,
    create,
    update,           // P0-4 fix: 重命名（原 patch）
    softDelete,
    fetchHistory,
    restore,
    toggleAdvanced,
    fetchTemplates,
    $reset,           // Pinia setup-store 必须显式 export reset
  };
});
```

> **注意**：store 用 named export，setup syntax；action 名 `patch` / `restore` 不带 `Api` 后缀（与 store action 风格一致）。

---

## 4. 路由配置（M5b 内容）

`src/router/index.ts` `routes[1].children` 数组加 6 个新条目（位置在现有 `runs` 之后）：

```typescript
// AI Agent 助手（feature #10）
{
  path: "agents",
  name: "agents",
  component: () => import("@/views/agent/AgentList.vue"),
  meta: { title: "AI 助手" },
},
{
  path: "agents/new",
  name: "agents-new",
  component: () => import("@/views/agent/AgentCreateChoose.vue"),
  meta: { title: "创建助手" },
},
{
  path: "agents/new/from-template",
  name: "agents-from-template",
  component: () => import("@/views/agent/TemplateGallery.vue"),
  meta: { title: "选择模板" },
},
{
  path: "agents/:id",
  name: "agents-detail",
  component: () => import("@/views/agent/AgentDetail.vue"),
  props: true,
  meta: { title: "助手详情" },
},
{
  path: "agents/:id/edit",
  name: "agents-edit",
  component: () => import("@/views/agent/AgentEdit.vue"),
  props: true,
  meta: { title: "编辑助手" },
},
{
  path: "agent-monitoring",
  name: "agent-monitoring",
  component: () => import("@/views/agent/AgentMonitoring.vue"),
  meta: { title: "Agent 监控" },
},
```

**M5b stub view files**（让 lazy import 不破环境）— 每个文件最小内容：

```vue
<!-- src/views/agent/AgentList.vue (stub) -->
<script setup lang="ts"></script>
<template>
  <div>AgentList (M7 stub)</div>
</template>
```

同样 stub：AgentCreateChoose / TemplateGallery / AgentDetail / AgentEdit / AgentMonitoring。M7-M12 implementer 在 Wave 5/6/7 替换 stub 为完整实装。

---

## 5. Sidebar 菜单（M5a 内容）

`src/components/layout/AdminSidebar.vue` `navItems` 数组在 `runs` 后插入两条：

```typescript
import { Bot, Activity } from "lucide-vue-next"; // 加 import

// 在 navItems 中加：
{ name: "agents", label: "AI 助手", icon: Bot, path: "/agents" },
{
  name: "agent-monitoring",
  label: "Agent 监控",
  icon: Activity,
  path: "/agent-monitoring",
},
```

---

## 6. 子组件契约（M6 内容）

### 6.1 `CheckboxGroup` （新增到 `src/components/common/CheckboxGroup.vue`）

> S2 决策：抽到 common（Q6/Q7 都用 + 未来其他视图可复用）

**Props**:
```typescript
interface Option {
  value: string;
  label: string;
}

interface Props {
  modelValue: string[]; // v-model 双向绑定
  options: Option[];
  allowOther?: boolean; // true 时显示"其他（填写）" + 自由文本框
  otherLabel?: string;  // default "其他（填写）"
  layout?: "vertical" | "horizontal"; // default "vertical"
}
```

**Emits**: `{ "update:modelValue": [value: string[]] }`

**行为**：
- 选项 checkbox `<input type="checkbox" :value="opt.value" v-model="proxy">` 模式
- allowOther 时多一个 row：checkbox + inline `<input type="text">`；勾选时启用 input；用户输入的字符串作为 modelValue 数组的额外元素
- 内部用 `computed get/set` 管理 v-model

### 6.2 `ChipInput` （新增到 `src/views/agent/components/ChipInput.vue`）

> S2 决策：放 `views/agent/components/`（仅 Q5 用）

**Props**:
```typescript
interface Props {
  modelValue: string[];
  max?: number; // default 4
  minLen?: number; // default 5
  maxLen?: number; // default 50
  placeholder?: string;
}
```

**Emits**: `{ "update:modelValue": [value: string[]] }`

**行为**：
- 渲染已有 chip + [x] 删除按钮
- 当 modelValue.length < max 时显示 `<input>` 接受输入
- **Enter 或 blur 都提交 chip**（P2-2 fix — 用户便利）
- 输入长度 < minLen 或 > maxLen → 提交时拒绝 + toast "每条 5-50 字"
- 已有 chip 数 == max 时隐藏 input

### 6.3 `CreditSlider` （新增到 `src/views/agent/components/CreditSlider.vue`）

**Props**:
```typescript
interface Props {
  modelValue: number;
  min?: number; // default 200
  max?: number; // default 2000
  step?: number; // default 100
}
```

**Emits**: `{ "update:modelValue": [value: number] }`

**行为**：
- `<input type="range">` + 配对 `<input type="number">`（P2-3 fix — 双向键入支持）
- 数字 input min/max/step 同 range
- 同步：range change → number 更新；number blur → 校验 + range 更新
- 帮助文本动态：
  - 200-500: "适合简单问答"
  - 500-1500: "适合数据分析"
  - 1500-2000: "适合复杂多步骤任务"

### 6.4 `AvatarPicker` （新增到 `src/views/agent/components/AvatarPicker.vue`）

> S2 决策：用 lucide-vue-next 现成图标（无 SVG 维护成本）

**Props**:
```typescript
interface Props {
  modelValue: string; // icon_url：'lucide:Bot' / 'lucide:User' / 'data:image/png;base64,...'
}
```

**Emits**: `{ "update:modelValue": [value: string] }`

**12 个内置 lucide 图标**（按业务场景挑选）：
```
Bot, User, Briefcase, BookOpen, MessageCircle,
GraduationCap, BarChart3, Lightbulb, Sparkles,
Heart, Star, Coffee
```

存储格式：`'lucide:Bot'`（前缀 + 组件名）。详情展示时解析 prefix → 渲染对应 `<Bot :size="48">`。

**上传**：
- `<input type="file" accept="image/jpeg,image/png">`
- 验证：size <= 2MB
- 用 `FileReader.readAsDataURL()` 转 base64 data URL
- 存储为 `'data:image/png;base64,...'` 形式
- 详情展示时 `startsWith('data:')` → 渲染 `<img :src="modelValue" :width="48">`

**default**：`'lucide:Bot'`

### 6.5 `QuestionnaireForm` （新增到 `src/views/agent/components/QuestionnaireForm.vue`）

> 12 题表单容器，被 AgentBuilder（创建+编辑通用）和模板预览 Modal（只读模式）使用

**Props**:
```typescript
interface Props {
  modelValue: AgentFormState; // 见下方 shape
  readonly?: boolean; // default false
  errors?: Record<string, string>; // 父组件传入校验结果
}
```

**`readonly` 传播契约**（P1-2 fix）：所有子控件必须接受并 wire readonly：

| 子控件 | readonly 处理 |
|--------|---------|
| `<AppInput>` (Q1, Q3) | `:disabled="readonly"` |
| `<textarea>` (Q4, Q10, Q11) | `:disabled="readonly"` |
| `<AvatarPicker>` (Q2) | 新增 `readonly` prop，readonly=true 时图标网格不可点击 + 上传按钮隐藏 |
| `<ChipInput>` (Q5) | 新增 `readonly` prop，readonly=true 时隐藏添加 input + chip 不显示 [x] |
| `<CheckboxGroup>` (Q6, Q7) | 新增 `readonly` prop，readonly=true 时 checkbox `:disabled="readonly"` |
| `<CreditSlider>` (Q8) | 新增 `readonly` prop，readonly=true 时 range + number `:disabled="readonly"` |
| `<input type=radio>` (Q9, Q12) | `:disabled="readonly"` |

**HistoryViewModal 用例**：所有 12 题都展示为 readonly；CSS class `.questionnaire-form--readonly` 整体淡灰底，让用户明确这是只读快照。

```typescript
export interface AgentFormState {
  // Q1-Q5 顶层字段
  name: string;
  icon_url: string;
  description: string;
  welcome_message: string;
  starters: string[];
  // Q6-Q12 嵌套
  questionnaire_answers: QuestionnaireAnswers;
  // tool_flags + cap 隐藏字段（v1 通过模板预设或保持默认）
  tool_flags: ToolFlags;
  credit_cap_per_session: number | null;
  daily_credit_cap: number | null;
}

/**
 * P0-2 fix: `initialFormState` 仅在 NEW-create（无模板、无 copy）时调用。
 * Edit / template / copy 模式不调此函数（数据来自后端 / 模板，不能被默认值覆盖）。
 *
 * Q11 默认提示语 — 仅作为新建时 UI 友好预填，让 textarea 不空白；
 * 用户可改可清空。EditMode 直接用 backend value（不强制注入此字符串）。
 */
export function initialFormState(): AgentFormState {
  return {
    name: "",
    icon_url: "lucide:Bot",
    description: "",
    welcome_message: "",
    starters: [],
    questionnaire_answers: {
      q6: [],
      q7: [],
      q8: 800,
      q9: "no_web_search",
      q10: "",
      q11: "这个问题有点超出我的能力范围，你可以去问老师或者换个方式描述一下～",
      q12: "friendly",
    },
    tool_flags: {},
    credit_cap_per_session: null,
    daily_credit_cap: null,
  };
}
```

**Emits**: `{ "update:modelValue": [value: AgentFormState] }`

**布局**：单页可滚动，12 题按 §5.3 顺序，必填星号在 label 前。

---

## 7. 验证函数（M9 内容 — Builder 用）

> 集中在 `src/views/agent/components/validation.ts`，每题独立函数，纯逻辑（无 DOM 依赖），便于单测。

```typescript
import type { AgentFormState } from "./QuestionnaireForm.vue";

/** 单个题目验证返回值：'' 表示无错误，非空字符串是错误文案 */
export type ValidationResult = string;

export function validateQ1(name: string): ValidationResult {
  if (!name) return "请输入助手名字";
  if (name.length < 2 || name.length > 20) return "名字应为 2-20 字";
  if (/^\d+$/.test(name)) return "名字不能全是数字";
  return "";
}

export function validateQ3(description: string): ValidationResult {
  if (!description) return "请输入描述";
  if (description.length < 10 || description.length > 100) return "描述应为 10-100 字";
  return "";
}

export function validateQ4(welcome: string): ValidationResult {
  if (!welcome) return "请输入欢迎语";
  if (welcome.length < 20 || welcome.length > 500) return "欢迎语应为 20-500 字";
  return "";
}

export function validateQ5(starters: string[]): ValidationResult {
  if (starters.length > 4) return "最多 4 个";
  for (const s of starters) {
    if (s.length < 5 || s.length > 50) return "每条 5-50 字";
  }
  return "";
}

export function validateQ6(q6: string[]): ValidationResult {
  if (!q6 || q6.length === 0) return "请至少选择一种任务类型";
  return "";
}

export function validateQ7(q7: string[]): ValidationResult {
  if (!q7 || q7.length === 0) return "请至少选择一种材料类型";
  return "";
}

export function validateQ8(q8: number): ValidationResult {
  // P1-6 fix: handle NaN / Infinity (typed input might produce these)
  if (!Number.isFinite(q8) || q8 < 200 || q8 > 2000) return "积分上限应在 200-2000";
  return "";
}

export function validateQ9(q9: string): ValidationResult {
  if (!q9) return "请选择";
  if (q9 !== "no_web_search" && q9 !== "allow_search") return "无效选项";
  return "";
}

export function validateQ10(q10: string): ValidationResult {
  if (q10.length > 500) return "最多 500 字";
  return "";
}

export function validateQ11(q11: string): ValidationResult {
  if (q11 && (q11.length < 5 || q11.length > 200)) return "5-200 字";
  return "";
}

export function validateQ12(q12: string): ValidationResult {
  if (!q12) return "请选择";
  if (!["friendly", "professional", "encouraging"].includes(q12)) return "无效选项";
  return "";
}

/** 全表单校验 — 返回 { 字段名: 错误文案 } map，空对象表示全通过 */
export function validateForm(form: AgentFormState): Record<string, string> {
  const errors: Record<string, string> = {};
  const q = form.questionnaire_answers;
  const e1 = validateQ1(form.name);
  if (e1) errors.name = e1;
  const e3 = validateQ3(form.description);
  if (e3) errors.description = e3;
  const e4 = validateQ4(form.welcome_message);
  if (e4) errors.welcome_message = e4;
  const e5 = validateQ5(form.starters);
  if (e5) errors.starters = e5;
  const e6 = validateQ6(q.q6 ?? []);
  if (e6) errors.q6 = e6;
  const e7 = validateQ7(q.q7 ?? []);
  if (e7) errors.q7 = e7;
  const e8 = validateQ8(q.q8 ?? 800);
  if (e8) errors.q8 = e8;
  const e9 = validateQ9(q.q9 ?? "");
  if (e9) errors.q9 = e9;
  const e10 = validateQ10(q.q10 ?? "");
  if (e10) errors.q10 = e10;
  const e11 = validateQ11(q.q11 ?? "");
  if (e11) errors.q11 = e11;
  const e12 = validateQ12(q.q12 ?? "");
  if (e12) errors.q12 = e12;
  return errors;
}
```

---

## 8. AgentBuilder 行为契约（M9）

文件：`src/views/agent/AgentBuilder.vue`

**Props**:
```typescript
interface Props {
  mode: "create" | "edit";
  agentId?: number;       // mode=edit 时必传
  fromTemplateId?: number; // create 时由 query ?from=template:N 传入
  fromCopyId?: number;     // create 时由 query ?from=copy:N 传入
}
```

**生命周期**:
- onMounted:
  - mode=edit：调 `store.fetchOne(agentId)` → reactive form = `Object.assign(form, current)`
  - mode=create + fromTemplateId：调 `store.fetchTemplates()` 找对应模板 → `Object.assign(form, template)`
  - mode=create + fromCopyId：调 `store.fetchOne(fromCopyId)` → `Object.assign(form, current, { name: current.name + ' - 副本' })`
  - 否则：`form = initialFormState()`
- onBeforeRouteLeave：
  ```typescript
  onBeforeRouteLeave((to, from) => {
    if (!isDirty.value) return true;
    return new Promise<boolean>((resolve) => {
      pendingResolve.value = resolve;
      unsavedConfirmVisible.value = true;
    });
  });
  ```
- onMounted + onBeforeUnmount: 注册/取消 `beforeunload` listener

**保存行为**：
```typescript
async function handleSave() {
  errors.value = validateForm(form);
  if (Object.keys(errors.value).length > 0) {
    scrollToFirstError();
    return;
  }
  try {
    const payload = formToPayload(form);
    const saved = props.mode === "create"
      ? await store.create(payload)
      : await store.patch(props.agentId!, payload);
    afterSaveModalVisible.value = true;
    afterSavedAgentId.value = saved.id;
    initialFormSnapshot.value = JSON.stringify(form); // reset dirty
  } catch (e) {
    toast.error((e as Error).message || "保存失败");
    // 后端 builder 失败时（如 q6 required）也走这里
  }
}

function scrollToFirstError() {
  const firstKey = Object.keys(errors.value)[0];
  const el = document.querySelector(`[data-question="${firstKey}"]`);
  el?.scrollIntoView({ behavior: "smooth", block: "center" });
}
```

`formToPayload` helper：复制 form 字段到 payload，把 starters 等空数组保持空数组（不剥），credit_cap_* 保持 null。

---

## 9. AgentList 行为契约（M7）

文件：`src/views/agent/AgentList.vue`

**布局**：
```
[搜索框] [+ 创建 Agent (主)] [从模板库选 (次)]

<DataTable :columns :data="filtered" :loading :total :page @update:page="...">
  <template #cell-name="{ row }">{{ row.name }} <Badge if advanced_mode>🔧</Badge> else 📋</template>
  <template #cell-actions="{ row }">
    <AppButton size="sm" @click="goEdit(row.id)">编辑</AppButton>
    <AppButton size="sm" @click="goDetail(row.id)">详情</AppButton>
    <AppButton size="sm" @click="derive(row)">派生</AppButton>
    <AppButton size="sm" danger @click="confirmDelete(row)">下架</AppButton>
  </template>
</DataTable>
```

**columns**:
```typescript
const columns: Column[] = [
  { key: "name", title: "名字", width: "200px", align: "left" },
  { key: "description", title: "描述", align: "left" },
  { key: "version", title: "版本", width: "70px" },
  { key: "updated_at", title: "更新时间", width: "160px" },
  { key: "actions", title: "操作", width: "260px" },
];
```

**状态机**：
- onMounted: `store.fetchList({ page: 1, page_size: 20 })`
- 4 状态：
  - loading=true → DataTable 内置 skeleton 行渲染
  - loading=false + list=[] → DataTable 内置 empty + 自定义 emptyText "暂无助手，点击 + 创建第一个"
  - loading=false + error≠'' → 自定义 error banner + [重试] 按钮
  - loading=false + list>0 → DataTable 渲染行

**搜索过滤**（P0-1/S0 决议）：
```typescript
const searchTerm = ref("");
const filtered = computed(() =>
  store.list.filter((a) =>
    a.name.toLowerCase().includes(searchTerm.value.toLowerCase()) ||
    a.description.toLowerCase().includes(searchTerm.value.toLowerCase())
  )
);
```

**下架确认 Modal**：
```typescript
function confirmDelete(agent: Agent) {
  pendingAgent.value = agent;
  confirmTitle.value = `确认下架「${agent.name}」？`;
  confirmMessage.value = "下架后：\n- 学员将无法启动新会话\n- 已下架后无法恢复（需重新创建）";
  confirmDanger.value = true;
  confirmVisible.value = true;
}

async function executeDelete() {
  if (!pendingAgent.value) return;
  try {
    await store.softDelete(pendingAgent.value.id);
    toast.success("已下架");
  } catch (e) {
    toast.error((e as Error).message);
  } finally {
    confirmVisible.value = false;
    pendingAgent.value = null;
  }
}
```

**子账户 403 提示**（P0-3 修复 — 用 HTTP status 而非 code 字符串）：

经核查 `internal/pkg/core/core.go` `WriteResponse`：响应 body `code` 永远是 0（成功）或 1（错误），**errno 的字符串 Code（如 `"AuthFailure.ChildAccountForbidden"`）不暴露到 body**——只通过 HTTP status 区分（403 / 404 / 422 等）。

因此前端检测必须用 `error.response?.status`：

```typescript
import type { AxiosError } from "axios";

catch (e) {
  const ae = e as AxiosError<{ code: number; message: string }>;
  const status = ae.response?.status;
  if (status === 403) {
    error.value = "仅父账户可配置 AI 助手，请联系机构主";
  } else if (status === 404) {
    error.value = "Agent 不存在或已下架";
  } else {
    error.value = ae.response?.data?.message || (e as Error).message || "加载失败";
  }
}
```

**注意**：`request.ts` interceptor 只 401 跳登录，4xx/5xx 透传 axios native error。caller 必须从 `error.response.status` 读 HTTP status。

`request.ts` response interceptor 已 unwrap 200 + code=0 success 路径返回 `response.data.data`；200 + code != 0 路径包成 `Error` with `.code` (always 1)；非 200 axios 直接 throw `AxiosError`。

**S4 实装常量**（替代 S2 spec 之前提到的 `src/constants/agentErrno.ts`）：
```typescript
// src/constants/agentErrno.ts
export const HTTP_CHILD_ACCOUNT_FORBIDDEN = 403;
export const HTTP_SKILL_NOT_FOUND = 404;
export const HTTP_SKILL_BUILDER_FAILED = 422;
// （未来如 backend 添加 X-Error-Code response header，可改为 string code 匹配）
```

---

## 10. AgentDetail / Tab 行为（M10）

文件：`src/views/agent/AgentDetail.vue` (容器)

**Tabs**：
```vue
<div class="agent-detail-tabs">
  <button :class="{ active: tab==='config' }" @click="tab='config'">基本配置</button>
  <button :class="{ active: tab==='history' }" @click="tab='history'">历史版本</button>
  <button :class="{ active: tab==='stats' }" @click="tab='stats'">使用数据</button>
</div>

<AgentConfigTab v-if="tab==='config'" :agent="store.current" />
<AgentHistoryTab v-else-if="tab==='history'" :agentId="id" />
<AgentStatsTab v-else />
```

**onMounted**：`store.fetchOne(id)` → 显示 loading skeleton；404 → 显示 "Agent 不存在" + [返回列表]

### AgentConfigTab（只读展示问卷）
- 用 `<QuestionnaireForm readonly />` 展示
- 底部：[编辑] [派生此 Agent]（S2 决策：加 — 详情页是第二个自然触发点）

> **Nit fix — HistoryViewModal 渲染过期 Q6 codes**：QuestionnaireForm 渲染时如遇 q6 数组含未知 code（如 backend 后续 add/remove code），CheckboxGroup 应：
> - 已知 code → 渲染对应 label
> - 未知 code → 渲染原 string（如 `"<已废弃: xxx>"` 或直接 `<chip>原 string</chip>`）
> - 不丢、不报错；与后端 `taskTypeDisplay` default 一致

### AgentHistoryTab
- onMounted：`store.fetchHistory(agentId)`
- 4 状态全覆盖
- 表格列：
  - version (e.g., "v3")
  - created_at (formatDate util)
  - changes_summary (后端 ComputeChangesSummary 生成的中文，如"首次发布" / "从 v2 恢复" / "修改了 Q1（名字）, Q3（描述）")
  - actions: 当前版本（version == current.version）→ 标 "当前版本"；非当前版本 → [查看] + [恢复]
- [查看]：弹 `HistoryViewModal`（只读 `<QuestionnaireForm readonly :modelValue="agentFromSnapshot">`）
- [恢复]：弹 ConfirmModal danger："恢复将创建 v{max+1}，当前 v{current} 仍保留在历史中。确认恢复到 v{version}？"

### AgentStatsTab（v1 占位）
```vue
<template>
  <div class="empty-state">
    <BarChart3 :size="48" />
    <p>使用数据将于下次迭代上线</p>
  </div>
</template>
```

---

## 11. AfterSaveModal（M10 子组件）

文件：`src/views/agent/components/AfterSaveModal.vue`

**Props**: `{ visible: boolean, agentName: string }`

**Emits**: `{ "trial-chat": [], "skip": [] }`

**模板**:
```vue
<ConfirmModal
  :visible="visible"
  :title="`✅ 助手已发布！`"
  confirmText="试聊一下"
  cancelText="暂时跳过"
  @confirm="$emit('trial-chat')"
  @cancel="$emit('skip')"
>
  <p>「{{ agentName }}」已经可以让学员使用了。</p>
  <p>要先体验一下，看看效果吗？</p>
</ConfirmModal>
```

**父组件 handler**:
```typescript
function onTrialChat() {
  toast.info("试聊功能即将上线");
  afterSaveModalVisible.value = false;
  router.push(`/agents/${afterSavedAgentId.value}`);
}

function onSkip() {
  afterSaveModalVisible.value = false;
  router.push(`/agents/${afterSavedAgentId.value}`);
}
```

---

## 12. AdvancedToggleConfirmModal（M11 子组件）

```vue
<ConfirmModal
  :visible="visible"
  title="⚠️ 切换到高级模式"
  confirmText="切换到高级模式"
  cancelText="取消"
  :danger="true"
  @confirm="$emit('confirm')"
  @cancel="$emit('cancel')"
>
  <p>高级模式允许你直接编写系统提示词（Prompt），适合有 AI 使用经验的配置者。</p>
  <p><strong>注意</strong>：</p>
  <ul>
    <li>切换后，当前问卷内容会转换为 Prompt 文本</li>
    <li><strong>无法切回问卷模式</strong>（问卷结构会丢失）</li>
    <li>写错了可以从历史版本回滚</li>
  </ul>
</ConfirmModal>
```

---

## 13. AgentAdvancedEdit（M11）

文件：`src/views/agent/AgentAdvancedEdit.vue`

**布局**：
```
<div class="advanced-editor">
  <header>
    <h2>{{ agent.name }} - 高级模式</h2>
    <span :class="{ warn: charCount > 8000 }">{{ charCount }} 字符</span>
  </header>

  <textarea
    v-model="body"
    class="advanced-textarea"
    rows="30"
    placeholder="# 助手指令"
    @input="markDirty"
  />

  <section class="tool-flags">
    <h3>工具开关</h3>
    <label>
      <input type="checkbox" v-model="toolFlags.code_sandbox">
      沙箱代码执行
    </label>
    <label>
      <input type="checkbox" v-model="toolFlags.media">
      多媒体处理
    </label>
    <label>
      <input type="checkbox" v-model="toolFlags.dangerous" @change="onDangerousChange">
      高危工具（谨慎开启）
    </label>
  </section>

  <footer>
    <AppButton @click="handleSave" :loading="store.saving">保存</AppButton>
  </footer>

  <ConfirmModal :visible="dangerousConfirmVisible" title="开启高危工具" :danger="true"
    @confirm="confirmDangerous" @cancel="cancelDangerous">
    高危工具可能造成不可逆操作（如发送邮件、修改学员数据），仅在你充分理解后果时启用。
  </ConfirmModal>
</div>
```

**字符计数**:
```typescript
const charCount = computed(() => body.value.length);
const isOverLimit = computed(() => charCount.value > 8000);
```

> 红色样式：`:class="{ warn: charCount > 8000 }"` + CSS `.warn { color: var(--color-danger); }`

**保存**：
```typescript
async function handleSave() {
  await store.patch(props.agentId, {
    // PATCH 后端不接受 custom_skill_body 单独字段；
    // 但 advanced_mode=1 状态下后端如何接受？S4 需查 service.go Patch 方法
    // S2 假设：advanced_mode=1 时 patch 用 questionnaire_answers 字段不影响，
    //         真正的 body 通过……
    //
    // !!! IMPORTANT !!! S4 待办 — 高级模式保存路径需查 service.go：
    // 后端 PatchRequest 没有 custom_skill_body 字段（见 controller/v1/agent/skill.go line 50-62），
    // 所以 v1 高级模式编辑 = advanced-toggle 切到 1 之后，无法 PATCH 更新 custom_skill_body。
    //
    // 这是 #5 backend 限制。v1 高级模式仅能"查看 + 切换"，不能编辑 body。
    // → S3 plan 必须把这个 limitation 移到 Out of Scope 或 file backend follow-up
    tool_flags: toolFlags.value,
  });
  toast.success("已保存");
}
```

> **S2 P0 — 高级模式编辑能力实际不存在**：经核查 `controller/v1/agent/skill.go` `PatchRequest` 结构（line 50-62），**没有 `custom_skill_body` 字段**，意味着切到高级模式后无法通过 PATCH 更新自定义 body。
>
> **S2 决议**：
> - v1 高级模式 = 仅切换（advanced_mode 0→1）+ 工具开关编辑（tool_flags 在 PATCH 中支持）
> - **custom_skill_body 编辑 v1 不支持**（后端 limitation，#5 未实装）
> - UI 必须明示："切换后系统会保留你的问卷答案；如需进一步编辑 SKILL.md 全文，需在 backend 后续 feature 开放编辑端点"
> - Out of Scope 加：**custom_skill_body 编辑（依赖后端补 PatchRequest 字段）**
>
> 这是 S2 spec 发现的重大边界——必须显式记录到 S3 plan 和 manifest decisions。

---

## 14. AgentMonitoring（M12）

文件：`src/views/agent/AgentMonitoring.vue`

**布局**：
```vue
<div class="agent-monitoring">
  <NoticeBanner type="info">
    ℹ️ 实时监控功能即将上线（v1 不联机）。当前页面是 UI 预览。
  </NoticeBanner>

  <h1>Agent 监控</h1>
  <p>实时查看学员与 AI 助手的会话</p>

  <DataTable :columns :data="sessions" :loading="false" :total="0" :emptyText="emptyText" />
</div>
```

`NoticeBanner` 是新组件吗？检查现有 — 如果没有，**新建** `src/components/common/NoticeBanner.vue`（M6 内补）：

```vue
<script setup lang="ts">
import { Info, AlertTriangle, AlertCircle } from "lucide-vue-next";
interface Props {
  type?: "info" | "warn" | "error";
}
const props = withDefaults(defineProps<Props>(), { type: "info" });
const Icon = props.type === "warn" ? AlertTriangle : props.type === "error" ? AlertCircle : Info;
</script>

<template>
  <div class="notice-banner" :class="`notice-banner--${type}`">
    <component :is="Icon" :size="18" />
    <slot />
  </div>
</template>
```

**fetcher stub**（P2-5 fix — 删除无意义 setInterval；直接占位 TODO）：
```typescript
const sessions = ref([]);
const emptyText = ref("v1 暂不联机，等待 #14 接入");

// onMounted: 不调任何 API
// TODO(#14): mount 30s polling here when GET /v1/agent/sessions/active 落地
// 当前 v1 不启动 interval — 避免无意义的 setInterval 计时器
```

---

## 15. 测试契约（M13 + 各 task 单测）

### 单测列表（vitest + Vue Test Utils）

| 文件 | 覆盖范围 |
|------|---------|
| `src/stores/__tests__/agent.spec.ts` | store 9 actions（mock @/api/agent） + $reset + error 路径 |
| `src/views/agent/components/__tests__/validation.spec.ts` | 12 个 validate 函数 happy + boundary + error case（每函数 4-5 case） |
| `src/views/agent/components/__tests__/ChipInput.spec.ts` | Enter 添加 / blur 添加 / 删除 / max 限制 / 长度错误 |
| `src/views/agent/components/__tests__/CreditSlider.spec.ts` | range / number 同步 / 边界 |
| `src/views/agent/components/__tests__/AvatarPicker.spec.ts` | 切换 icon / 上传 base64 / 2MB 限制 |
| `src/views/agent/components/__tests__/CheckboxGroup.spec.ts` | 单选 / 多选 / allowOther |
| `src/views/agent/__tests__/AgentBuilder.spec.ts` | 验证错误 → 滚动 / 保存成功 → afterSave Modal / dirty 守卫 / 模板预填 / copy 预填 |
| `src/views/agent/__tests__/AgentList.spec.ts` | 4 状态切换 / 搜索过滤 / 下架 ConfirmModal |
| `src/views/agent/__tests__/AgentHistoryTab.spec.ts` | 历史列表渲染 / 当前版本不显恢复 / 恢复 ConfirmModal |
| `src/views/agent/__tests__/AgentAdvancedEdit.spec.ts` | 字符计数 / 8000 边界变红 / 高危工具二次确认 |

### E2E 列表（Playwright）

| 文件 | 场景 |
|------|------|
| `e2e/agent-template-derive.spec.ts` | 列表 → [从模板库] → 选第一个 → 改名 → 保存 → afterSave Modal → 跳详情 → 历史 v1 一条 |
| `e2e/agent-scratch-create.spec.ts` | 列表 → [+ 创建] → 从零 → 填 12 题（含必填校验红边框）→ 保存 → 列表出现新 agent |
| `e2e/agent-advanced-toggle.spec.ts` | 详情 → 编辑 → 右下角 [高级模式] → 警示 Confirm → 确认 → URL 跳 /edit + 内部分支为只读 Markdown view + 工具开关 |
| `e2e/agent-history-restore.spec.ts` | 详情 → 历史 Tab → 多版本（至少 2 条）→ [恢复 v1] → ConfirmModal → 确认 → 历史 v3 出现 changes_summary "从 v1 恢复" |

> **P1-5 子账户 403 不加 E2E**（reviewer 建议加了又移到 manual）— 理由：dev 账号是父账户，无可用子账户测号；mock 子账户需要后端 fixture，复杂度高；改为 **S5 manual check item**（用 curl 模拟子账户调 GET /agents 拿 403 → UI 显示"仅父账户"提示）。

E2E 前置：每个 spec 第一步 `await page.goto('/agents')` （已通过 auth.setup.ts 登录态）。

**`request.ts` patch helper 单测**（Nit add）：`src/api/__tests__/request.spec.ts` — mock axios.create → 调 patch() → expect axios.patch called with correct args。

### lint baseline（S0/S1 待办）

**已运行 `npm run lint`** → **0 errors, 2 warnings**（均在 `e2e/context-budget.spec.ts` 和 `e2e/wave2-smoke.spec.ts`，无关本 feature）。

**S5 验收门**：`npm run lint` 后总 warning ≤ 2（baseline）+ 0 error。

### `npm run type-check` 门

**S5 验收门**：`npm run type-check` 必须 exit 0。

---

## 16. 文件归属表（Tier 3 disjoint check 用）

| Task | 文件归属（new = 新文件；mod = 修改既有）|
|------|---------|
| M1 types | new: `src/types/agent.ts` |
| M2 request.ts patch | mod: `src/api/request.ts` (+5 行 patch helper) |
| M3 api/agent.ts | new: `src/api/agent.ts` |
| M4 store | new: `src/stores/agent.ts` |
| M5a sidebar | mod: `src/components/layout/AdminSidebar.vue` (+2 navItem + 2 import) |
| M5b router + stubs | mod: `src/router/index.ts` (+6 routes) ; new: `src/views/agent/{AgentList,AgentCreateChoose,TemplateGallery,AgentDetail,AgentEdit,AgentMonitoring}.vue` (6 stubs) |
| M6 common 组件 | new: `src/components/common/CheckboxGroup.vue` + `src/components/common/NoticeBanner.vue` ; new: `src/views/agent/components/{ChipInput,CreditSlider,AvatarPicker}.vue` |
| M7 AgentList full | replace stub: `src/views/agent/AgentList.vue` |
| M8 Create + Template | replace stub: `src/views/agent/{AgentCreateChoose,TemplateGallery}.vue` |
| M9 Builder + Form + validation | new: `src/views/agent/AgentBuilder.vue` ; new: `src/views/agent/components/{QuestionnaireForm,validation}.{vue,ts}` ; replace stub: `src/views/agent/AgentEdit.vue` (wraps Builder + AgentAdvancedEdit by advanced_mode) |
| M10 Detail + Tabs + Modals | replace stub: `src/views/agent/AgentDetail.vue` ; new: `src/views/agent/components/{AgentConfigTab,AgentHistoryTab,AgentStatsTab,HistoryViewModal,AfterSaveModal,AdvancedToggleConfirmModal}.vue` |
| M11 AdvancedEdit | new: `src/views/agent/AgentAdvancedEdit.vue` (AgentEdit 内嵌使用) |
| M12 Monitoring full | replace stub: `src/views/agent/AgentMonitoring.vue` |
| M13 tests | new: `src/views/agent/__tests__/*.spec.ts` ; new: `src/views/agent/components/__tests__/*.spec.ts` ; new: `src/stores/__tests__/agent.spec.ts` ; new: `e2e/agent-*.spec.ts` |

**Tier 3 verifications**（S3 用）：
- Wave 4：M5a / M5b / M6 — disjoint（不同目录的不同文件）✓
- Wave 5：M7 / M8 / M11 / M12 — 各自独立 view + 子组件，无交集 ✓
- 总 unique 文件数 ~40，无重叠

---

## 17. 边界 / 错误 handling

| 场景 | 处理 |
|------|-----|
| 网络 timeout | request.ts 默认 15s timeout；toast 显示 "网络超时" |
| 401 token 过期 | request.ts 拦截器自动跳 /login |
| 403 子账户 | List/Builder/Detail catch via `(e as AxiosError).response?.status === 403` → 显示友好文案 "仅父账户可配置 AI 助手" |
| 404 agent 不存在 | Detail/Edit catch via status === 404 → 显示 "Agent 不存在或已下架" + [返回] |
| 422 后端 builder 失败 | Builder save catch → toast 显示后端 message + 滚到第一个空必填题（如果 message 含 "q6 required" 则手动加 errors.q6 = 后端 message） |
| 422 字段过长 | 前端验证已拦；如绕过 → toast 后端 message |
| 网络 5xx | toast "服务器错误，请稍后重试" |

**P1-4 docs**: 任何 PATCH（含仅 tool_flags 修改）都会让后端 `version++` 写一条 history。AgentList "更新时间" 与历史版本号同步变化；UI 不优化该行为（与 #5 后端契约一致）。

**P1-7 docs**: `softDelete` 是 optimistic — list 立即过滤，total 立即减 1；与并发 fetchList 的 race 接受 last-write-wins。AgentList 不在 softDelete 后主动 refetch（保持简单）。如果 race 实际造成显示问题，未来加 `lastMutationSeq` token。

---

## 18. 字符计数 / Markdown editor 实装细节（M11）

```html
<textarea
  v-model="body"
  class="md-editor"
  rows="30"
  spellcheck="false"
/>
```

```css
.md-editor {
  font-family: 'JetBrains Mono', 'Courier New', monospace;
  font-size: 14px;
  line-height: 1.6;
  width: 100%;
  padding: var(--space-3);
  border: 1px solid var(--surface-border);
  border-radius: var(--radius-sm);
  resize: vertical;
}
.md-editor:focus {
  outline: 2px solid var(--color-accent);
}
```

字符计数显示在 editor 上方，超 8000 字符变红：

```vue
<span :class="{ 'char-count': true, 'char-count--warn': charCount > 8000 }">
  {{ charCount }} / 建议 ≤ 8000
</span>
```

**undo / cmd+Z**：浏览器原生支持（`<textarea>` 自带 undo stack）。S2 决策不接入第三方 editor。

---

## 19. 已 close 的 S2 待办（来自 S1 §5）

- ✓ AgentDefinitionHistory.snapshot：嵌套对象（`datatypes.JSON` marshal 为 `Agent` 完整对象，UI 直接 `history.snapshot.name` 访问）
- ✓ lint baseline：0 errors, 2 warnings (e2e 文件中，无关本 feature)
- ✓ E2E_USERNAME/PASSWORD：是 admin（is_admin=true）凭据；dev 该账号同时是父账户（与 v3 C 端共用）
- ✓ CheckboxGroup：抽到 `src/components/common/`
- ✓ AvatarPicker 内置图标：用 lucide-vue-next（12 个 from `Bot/User/Briefcase/BookOpen/MessageCircle/GraduationCap/BarChart3/Lightbulb/Sparkles/Heart/Star/Coffee`）
- ✓ 详情页 [派生此 Agent] CTA：加（详情页是第二个自然触发点）

---

## 20. **S2 新发现的边界**（重要）

**custom_skill_body 编辑 v1 不支持** —— 见 §13。后端 `PatchRequest` 无 `custom_skill_body` 字段（已 verified `controller/v1/agent/skill.go` line 50-62 + `biz/skill/service.go` Patch 函数只 apply 9 字段）；切高级模式后 UI 仅能查看 + 切工具开关，不能编辑 SKILL.md 全文。

**影响**：
- S0 / S1 中的"高级模式 = 编辑 Markdown"实质 v1 无法实现保存
- S3 plan 必须把 AgentAdvancedEdit 简化为：展示当前 generated_skill_body / custom_skill_body（只读 Markdown rendered）+ 工具开关 PATCH（用 `store.update()`）
- AgentAdvancedEdit 顶部加 NoticeBanner："✏️ 自定义 Prompt 编辑功能即将上线。当前可查看 + 修改工具开关。"
- 实际"编辑高级模式 body"需后端补 PATCH field；S3 plan 必须 file follow-up issue：
  - **Backend follow-up**：`agent-mode-skill-system-advanced-mode-edit` micro：`controller/v1/agent/skill.go` PatchRequest 加 `custom_skill_body *string`；service.go Patch 处理该字段
  - **Frontend follow-up**：本 feature merged 后做独立 micro 把 AgentAdvancedEdit 升级为可编辑（要求 backend 先 merged）
- **此 follow-up 在 S3 plan 写入 manifest decisions + 单独 follow-up issue 文件**（推迟到 S3 创建）

---

## 21. 0 prod 影响 reaffirm

- 不动 numind-server 任何代码
- 不打 git tag
- 不调 /deploy-prod
- feature 分支不推 GitHub
- 不引入新 npm 依赖
- 不动 prod 配置文件

---

**S2 完结。S3 plan 把这些契约切成 ~13 atomic task，含 Wave 分组 + 文件归属 + ndf-check-disjoint 命令 + 验证策略。**
