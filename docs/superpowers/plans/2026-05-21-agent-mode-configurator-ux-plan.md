# NDF S3 Task Plan · `agent-mode-configurator-ux`

**Feature ID**：`agent-mode-configurator-ux`（#10/14）
**起草日期**：2026-05-21
**前置**：S0 commit `7837395b` / S1 commit `e4574609` / S2 commit `a41cded5`
**仓库**：`numind-admin-web` (feature/agent-mode-configurator-ux worktree)
**worktree**：`/private/tmp/wt-agent-mode-configurator-ux-numind-admin-web/`

---

## 1. 任务总览（M1-M13 = 14 atomic tasks 含 M9 拆分 / M13 跨仓库拆分）

| # | Task | 类型 | Wave | 文件归属 | 验收 |
|---|------|------|------|---------|------|
| M1 | types/agent.ts | new | W1 | 1 file | tsc 通过 |
| M2 | request.ts +patch helper | mod | W1 | 2 files | 单测 patch wrapper |
| M3 | api/agent.ts (9 wrappers) | new | W2 | 1 file | tsc 通过 + import 自 M1 |
| M4 | stores/agent.ts (Pinia) | new | W3 | 2 files | store 单测 9 actions |
| M5a | AdminSidebar 加 2 navItem | mod | W4 | 1 file | 视觉验证 |
| M5b | router + 6 stub views | mod+new | W4 | 7 files | tsc 通过 + 浏览器导航不 404 |
| M6 | 6 个子组件 + NoticeBanner + 单测 | new | W4 | 12 files | 单测各 component |
| M7 | AgentList full | replace | W5 | 3 files | 4 状态切换 e2e + 单测 |
| M8 | CreateChoose + TemplateGallery | replace | W5 | 2 files | e2e 模板派生 |
| M9a | validation.ts + 单测 | new | W6 | 2 files | 12 验证函数单测 |
| M9b | Builder + Edit wrapper + AfterSaveModal + 单测 | new+replace | W6 | 4 files | e2e 从零创建 + Builder 单测 |
| M10 | Detail + 3 Tab + 3 Modal | replace+new | W7 | 7 files | e2e 历史回滚 + Tab 单测 |
| M11 | AgentAdvancedEdit + 单测 | new | W5 | 2 files | e2e 高级切换 + 单测字符计数 |
| M12 | AgentMonitoring full | replace | W5 | 1 file | 视觉 NoticeBanner + DataTable empty |
| M13a | E2E specs (4) | new | W8 | 4 files (admin-web) | playwright list 列出 4 specs |
| M13b | follow-up issues + S5 acceptance placeholder | new | W8 | 3 files (numind-server) | 文件存在 + 内容描述清楚 |

**总文件数**：~48（new ~42 + mod ~6；跨 2 仓库）

---

## 2. 详细 Task 规格（M1-M13）

### M1: `src/types/agent.ts`

**新文件**：完整拷贝 S2 spec §1 的 TypeScript 内容。包含：
- 3 个 enum union: Q6TaskType / Q7MaterialType / Q9WebSearch / Q12Style
- `QuestionnaireAnswers` interface
- `normalizeQuestionnaire()` helper export
- `ToolFlags` interface (3 known keys + extensible)
- `Agent` interface
- `AgentHistory` interface
- `SkillTemplate` interface
- `CreateAgentPayload` interface
- `PatchAgentPayload` = Partial<Omit<CreateAgentPayload, 'source_template_id'>>
- `ListResponse<T>` interface

**验收**：
- `cd numind-admin-web && npm run type-check` 在新文件上不报错
- 文件不 import 任何运行时模块（纯类型 + 1 个 normalize 函数）

**依赖**：无

---

### M2: `src/api/request.ts` 添加 `patch<T>()` helper

**修改**：在 `del<T>()` 之后添加（S2 spec §2 末段代码）：

```typescript
export function patch<T>(
  url: string,
  data?: unknown,
  config?: AxiosRequestConfig,
): Promise<T> {
  return request.patch(url, data, config) as Promise<T>;
}
```

**验收**：
- `npm run type-check` 通过
- 新增单测 `src/api/__tests__/request.spec.ts`：
  - mock axios.create → 调 patch('/foo', {x:1}) → expect request.patch called with /foo, {x:1}

**依赖**：无

---

### M3: `src/api/agent.ts`

**新文件**：S2 spec §2 完整代码。9 个 named export functions，import from `./request` + `@/types/agent`。

**验收**：
- `npm run type-check` 通过
- 每个函数 1 行实装，类型完整

**依赖**：M1 (types) + M2 (patch helper)

---

### M4: `src/stores/agent.ts`

**新文件**：S2 spec §3 完整 Pinia setup syntax store。9 actions + $reset。

**关键点**：
- `update` action（**不是 patch**，P0-4 fix）
- `fetchOne` 调 `normalizeQuestionnaire()`
- `softDelete` optimistic（filter 本地 list + total--）
- `restore` 后自动调 `fetchHistory()` 刷新

**验收**：
- `npm run type-check` 通过
- 新增单测 `src/stores/__tests__/agent.spec.ts`：
  - 9 个 action mock api 路径 happy + error
  - $reset 清空所有 state
  - softDelete optimistic 路径（pre + post mutations）
  - normalize 后表单读取 q9="" 被去掉

**依赖**：M3 (api)

---

### M5a: `src/components/layout/AdminSidebar.vue`

**修改**：
1. 顶部 import 加 `import { Bot, Activity } from "lucide-vue-next";`
2. `navItems` 数组在现有 `runs` 项之后插入两条：
   ```typescript
   { name: "agents", label: "AI 助手", icon: Bot, path: "/agents" },
   { name: "agent-monitoring", label: "Agent 监控", icon: Activity, path: "/agent-monitoring" },
   ```

**验收**：
- `npm run dev` → 浏览器看 sidebar 两条新菜单
- `npm run lint` 通过

**依赖**：无（独立修改）

---

### M5b: 路由 + 6 个 stub view files

**修改**：`src/router/index.ts` 在 `routes[1].children` 数组中插入 6 条新路由（S2 spec §4 完整 routes 代码）。

**新建** 6 个 stub view files（让 lazy import 不破环境）：

```vue
<!-- src/views/agent/AgentList.vue -->
<script setup lang="ts"></script>
<template><div>AgentList stub (M7 will replace)</div></template>
```

同样：AgentCreateChoose / TemplateGallery / AgentDetail / AgentEdit / AgentMonitoring。

**验收**：
- `npm run type-check` 通过
- `npm run dev` → 浏览器手动导航 /agents /agents/new /agent-monitoring 不报 404
- `npm run lint` 通过

**依赖**：无（router 修改和 stub 文件创建并行也可，但同 task 内串行执行）

---

### M6: 6 个子组件 + NoticeBanner

**新建**：

1. `src/components/common/CheckboxGroup.vue` — S2 spec §6.1（含 allowOther + readonly prop）
2. `src/components/common/NoticeBanner.vue` — S2 spec §14（type info/warn/error + slot + lucide icon）
3. `src/views/agent/components/ChipInput.vue` — S2 spec §6.2（Enter/blur 双触发 + readonly）
4. `src/views/agent/components/CreditSlider.vue` — S2 spec §6.3（range + number 双向 + 动态帮助文本 + readonly）
5. `src/views/agent/components/AvatarPicker.vue` — S2 spec §6.4（12 lucide icons + base64 上传 + readonly）
6. `src/views/agent/components/QuestionnaireForm.vue` — S2 spec §6.5（12 题表单容器；初始化 `initialFormState()`）

**验收**：
- `npm run type-check` 通过
- 每个 component 1 个单测：
  - CheckboxGroup: 单选/多选/allowOther
  - NoticeBanner: 3 type 渲染对应 icon
  - ChipInput: Enter+blur 双添加 / max 限制 / 删除 / readonly
  - CreditSlider: range+number 同步 / NaN 处理 / readonly
  - AvatarPicker: 切 icon / 上传 base64 / 2MB 限制 / readonly
  - QuestionnaireForm: 12 题 v-model 双向 / readonly 传播

**依赖**：M1 (types) — QuestionnaireForm 需要 AgentFormState 类型

---

### M7: `src/views/agent/AgentList.vue` (replace stub)

**完整实装** S2 spec §9：
- 顶栏搜索框（客户端 filter） + [+ 创建] + [从模板库] 按钮
- DataTable with `cell-{key}` slots for name (📋/🔧 badge) + actions ([编辑] [详情] [派生] [下架])
- 4 状态切换（loading skeleton / empty + CTA / error + retry / success）
- 下架 ConfirmModal danger
- 子账户 403 catch via AxiosError.response?.status === 403 → friendly message
- 创建 src/constants/agentErrno.ts 文件（HTTP code 常量）

**验收**：
- `npm run type-check` + lint 通过
- 单测 `src/views/agent/__tests__/AgentList.spec.ts`：
  - mock store → 4 状态 render
  - 搜索 input → filter list
  - 下架 ConfirmModal 流程
  - 403 catch → friendly message

**依赖**：M4 (store) + M5b (route + stub) + M6 (DataTable 已用，但是公共 component)

---

### M8: `src/views/agent/{AgentCreateChoose,TemplateGallery}.vue` (replace stubs)

**AgentCreateChoose**：
- 3 张大卡片：从模板 / 从零 / 派生
- 派生卡片：点击弹 Modal 列出已有 agents（调 store.fetchList）→ 选一个 → 跳 `/agents/new?from=copy:<id>`
- 模板：跳 `/agents/new/from-template`
- 从零：跳 `/agents/new?from=scratch`（Builder 监听 query 用 initialFormState）

**TemplateGallery**：
- onMounted: `store.fetchTemplates()`
- 网格布局（这是唯一允许的卡片布局——是"画廊"非"管理列表"）
- 每张卡片：lucide icon + name + description + [用这个模板] 按钮
- 点 [用这个模板] → 跳 `/agents/new?from=template:<id>`
- 4 状态全覆盖

**验收**：
- type-check + lint 通过
- e2e 测试 M13 中覆盖：列表 → [从模板库] → 选第一个 → 跳 Builder 预填

**依赖**：M4 (store) + M5b (stub + route)

---

### M9a: validation.ts + 单测 (W6 串行第 1 步)

**新建**：
1. `src/views/agent/components/validation.ts` — S2 spec §7 完整 12 个验证函数 + validateForm + Number.isFinite 修正
2. `src/views/agent/components/__tests__/validation.spec.ts` — 12 函数每函数 4-5 case (happy/boundary/error/NaN)

**验收**：
- `npm run type-check` + lint 通过
- `npm run test:unit -- validation.spec` PASS

**依赖**：M1 (types — AgentFormState)

---

### M9b: Builder + AgentEdit wrapper + AfterSaveModal + 单测 (W6 串行第 2 步)

**新建**：
1. `src/views/agent/AgentBuilder.vue` — S2 spec §8 完整：Props (mode/agentId/fromTemplateId/fromCopyId) + lifecycle (mode 分支) + handleSave (validation + scroll + store.create/update) + onBeforeRouteLeave 守卫 + beforeunload listener + ConfirmModal 未保存
2. `src/views/agent/components/AfterSaveModal.vue` — S2 spec §11 (P1-3 fix moved from M10)
3. `src/views/agent/__tests__/AgentBuilder.spec.ts` — validation error → scroll / save success → afterSave Modal / dirty 守卫 / 模板预填 / copy 预填

**Replace stub**：
4. `src/views/agent/AgentEdit.vue` — wrapper 视图：
   ```vue
   <script setup lang="ts">
   import { onMounted, computed } from "vue";
   import { useRoute } from "vue-router";
   import { useAgentStore } from "@/stores/agent";
   import AgentBuilder from "./AgentBuilder.vue";
   import AgentAdvancedEdit from "./AgentAdvancedEdit.vue";

   const route = useRoute();
   const store = useAgentStore();
   const agentId = computed(() => Number(route.params.id));

   onMounted(() => store.fetchOne(agentId.value));
   </script>
   <template>
     <div v-if="store.currentLoading">加载中...</div>
     <div v-else-if="store.currentError">{{ store.currentError }}</div>
     <AgentAdvancedEdit v-else-if="store.current?.advanced_mode" :agent-id="agentId" />
     <AgentBuilder v-else mode="edit" :agent-id="agentId" />
   </template>
   ```

**注意**：M9b 内 AgentBuilder 用 `formToPayload` helper 转换 form → payload；同文件实装。

**验收**：
- type-check + lint 通过
- 单测 AgentBuilder.spec.ts PASS
- e2e 测试 M13a 中覆盖

**依赖**：M9a (validation) + M4 (store) + M5b (stub + route) + M6 (子组件 QuestionnaireForm/Modal) + M11 (AgentAdvancedEdit — Edit wrapper imports)

---

### M10: AgentDetail + 3 Tab + 2 Modal（AfterSaveModal 已搬到 M9b）

**Replace stub**：
1. `src/views/agent/AgentDetail.vue` — S2 spec §10 容器（3 tab 切换 + 404 处理 + loading）

**新建** 6 文件：
2. `src/views/agent/components/AgentConfigTab.vue` — 只读 QuestionnaireForm + [编辑] [派生此 Agent] 按钮
3. `src/views/agent/components/AgentHistoryTab.vue` — 历史列表 DataTable + 查看 + 恢复
4. `src/views/agent/components/AgentStatsTab.vue` — v1 占位（BarChart3 icon + "使用数据将于下次迭代上线"）
5. `src/views/agent/components/HistoryViewModal.vue` — 只读 QuestionnaireForm Modal
6. `src/views/agent/components/AdvancedToggleConfirmModal.vue` — S2 spec §12 完整代码
7. `src/views/agent/__tests__/AgentHistoryTab.spec.ts`

**验收**：
- type-check + lint 通过
- 单测：
  - AgentHistoryTab.spec.ts: 列表渲染 / 当前版本不显恢复 / 恢复 ConfirmModal
  - AfterSaveModal 单测：confirm/cancel emit
- e2e M13：历史回滚

**依赖**：M4 (store) + M5b (stub + route) + M6 (QuestionnaireForm)

---

### M11: `src/views/agent/AgentAdvancedEdit.vue`

**新建**：v1 简化版（S2 §13 + §20 决定）：
- 顶部 NoticeBanner: "✏️ 自定义 Prompt 编辑功能即将上线。当前可查看 + 修改工具开关。"
- 只读展示 `current.custom_skill_body || current.generated_skill_body` 渲染为 monospace textarea (disabled)
- 字符计数（>8000 字符变红警示作用——v1 没有 save body 所以 warning 仅 UI 提示）
- 工具开关 3 个 toggle (code_sandbox / media / dangerous)
- dangerous 切 true 时弹二次 ConfirmModal
- [保存工具开关] 按钮 → 调 store.update({ tool_flags })
- onBeforeRouteLeave 守卫（同 AgentBuilder）

**验收**：
- type-check + lint 通过
- 单测 AgentAdvancedEdit.spec.ts: 字符计数 / 8000 边界 / dangerous 二次确认 / 保存 tool_flags
- e2e M13：详情 → 编辑 → 高级切换 → 看到只读 body + NoticeBanner

**依赖**：M4 (store) + M5b (stub)

---

### M12: `src/views/agent/AgentMonitoring.vue` (replace stub)

**完整实装** S2 spec §14：
- 顶部 NoticeBanner: "ℹ️ 实时监控功能即将上线（v1 不联机）"
- DataTable with empty list + emptyText "v1 暂不联机，等待 #14 接入"
- 列定义就位作骨架（学员 / Agent / 开始时间 / 已用时 / 已用积分 / 状态 / 操作）
- 不启动 setInterval（P2-5 fix — 删除无意义计时器）
- 注释 TODO(#14) 标记 wire 真实 API

**验收**：
- type-check + lint 通过
- 视觉：dev 浏览 /agent-monitoring 看到 NoticeBanner + 空 DataTable

**依赖**：M5b (stub + route) + M6 (NoticeBanner)

---

### M13a: E2E specs（admin-web 仓库）

**新建** 4 e2e specs，每个 spec 用 admin token (auth.setup.ts 提供) 走 critical path：
1. `e2e/agent-template-derive.spec.ts` — 模板派生流程：访 /agents → 点 [从模板库] → 选第一个 → 改名 → 保存 → afterSave Modal 出现 → 跳详情 → 历史 v1 显示
2. `e2e/agent-scratch-create.spec.ts` — 从零 12 题：访 /agents/new → 选 [从零] → 不填必填提交 → 看到红色错误 → 修正 → 保存 → 列表出现新 agent
3. `e2e/agent-advanced-toggle.spec.ts` — 高级切换：详情 → 编辑 → 右下角 [高级模式] → 警示 Confirm → 确认 → URL 切到只读 body view + NoticeBanner
4. `e2e/agent-history-restore.spec.ts` — 历史回滚：详情多版本 → [恢复 v1] → ConfirmModal → 确认 → 历史 v3 "从 v1 恢复"

**验收**：
- 4 个文件存在
- `cd numind-admin-web && npx playwright test --list e2e/agent-*.spec.ts` 列出 4 spec
- M13a 实际跑 e2e（设 BASE_URL=http://localhost:5174 + dev server 起着）由 S5 执行；M13a 仅创建文件 + verify list

**依赖**：所有 M1-M12 完成（admin-web 编码完整才能 e2e）

---

### M13b: Follow-up issues + S5 acceptance placeholder（numind-server 仓库 — cux-server-docs worktree）

> P0-2 fix: 这是跨仓库 task。主 session 不 dispatch subagent，直接在 `/private/tmp/wt-cux-server-docs/` 写文件并 commit 到 develop（与 NDF 文档同流程）。

**新建** 在 `/private/tmp/wt-cux-server-docs/`：
1. `follow-ups/agent-mode-skill-system-advanced-mode-edit.md`（新目录 + 文件；S3 §6 完整内容；加 origin 注脚指向本 S2 commit a41cded5 §13/§20）
2. `follow-ups/agent-mode-skill-system-monitoring-api.md`（同上）
3. `docs/superpowers/qa/2026-05-21-agent-mode-configurator-ux-s5-acceptance.md`（v1 placeholder，S5 阶段填实际结果）

**验收**：
- 文件存在 + 内容描述清楚（problem / proposed scope / acceptance / origin commit ref）
- `git -C /private/tmp/wt-cux-server-docs log -1` 显示 M13b commit

**依赖**：无（独立于 admin-web feature 编码进度；可在 S4 任何时候完成；建议放最后与 acceptance 同步）

---

## 3. Wave 分组 + ndf-check-disjoint 命令

ndf-check-disjoint 脚本位置：`numind-server/scripts/ndf/ndf-check-disjoint.sh`（接受多组 **逗号分隔** 的 file list 字符串，exit 0 = 安全可并行，exit 1 = 有交集）。

> **P0-1 修复**：脚本只识别 **comma-separated**（`tr ',' '\n'` 内部），不识别空格。空格分隔会被当成"一个文件名"导致 false PASS。所有 ndf-check-disjoint 调用必须用逗号。

### Wave 1（并行 — M1 + M2）

```
M1 files: src/types/agent.ts
M2 files: src/api/request.ts, src/api/__tests__/request.spec.ts
```

```bash
bash numind-server/scripts/ndf/ndf-check-disjoint.sh \
  "src/types/agent.ts" \
  "src/api/request.ts,src/api/__tests__/request.spec.ts"
# Expected: exit 0
```

### Wave 2（M3 串行 — 依赖 M1+M2）

只 1 task；无 disjoint check。

```
M3 files: src/api/agent.ts
```

### Wave 3（M4 串行 — 依赖 M3）

```
M4 files: src/stores/agent.ts
```

### Wave 4（并行 — M5a + M5b + M6）

```
M5a files: src/components/layout/AdminSidebar.vue
M5b files: src/router/index.ts
           src/views/agent/AgentList.vue (stub)
           src/views/agent/AgentCreateChoose.vue (stub)
           src/views/agent/TemplateGallery.vue (stub)
           src/views/agent/AgentDetail.vue (stub)
           src/views/agent/AgentEdit.vue (stub)
           src/views/agent/AgentMonitoring.vue (stub)
M6 files: src/components/common/CheckboxGroup.vue
          src/components/common/NoticeBanner.vue
          src/views/agent/components/ChipInput.vue
          src/views/agent/components/CreditSlider.vue
          src/views/agent/components/AvatarPicker.vue
          src/views/agent/components/QuestionnaireForm.vue
```

```bash
bash numind-server/scripts/ndf/ndf-check-disjoint.sh \
  "src/components/layout/AdminSidebar.vue" \
  "src/router/index.ts,src/views/agent/AgentList.vue,src/views/agent/AgentCreateChoose.vue,src/views/agent/TemplateGallery.vue,src/views/agent/AgentDetail.vue,src/views/agent/AgentEdit.vue,src/views/agent/AgentMonitoring.vue" \
  "src/components/common/CheckboxGroup.vue,src/components/common/NoticeBanner.vue,src/views/agent/components/ChipInput.vue,src/views/agent/components/CreditSlider.vue,src/views/agent/components/AvatarPicker.vue,src/views/agent/components/QuestionnaireForm.vue,src/components/common/__tests__/CheckboxGroup.spec.ts,src/components/common/__tests__/NoticeBanner.spec.ts,src/views/agent/components/__tests__/ChipInput.spec.ts,src/views/agent/components/__tests__/CreditSlider.spec.ts,src/views/agent/components/__tests__/AvatarPicker.spec.ts,src/views/agent/components/__tests__/QuestionnaireForm.spec.ts"
# Expected: exit 0
```

### Wave 5（并行 — M7 + M8 + M11 + M12）

```
M7 files: src/views/agent/AgentList.vue (replace stub)
          src/constants/agentErrno.ts (new)
          src/views/agent/__tests__/AgentList.spec.ts (new)
M8 files: src/views/agent/AgentCreateChoose.vue (replace stub)
          src/views/agent/TemplateGallery.vue (replace stub)
M11 files: src/views/agent/AgentAdvancedEdit.vue (new)
          src/views/agent/__tests__/AgentAdvancedEdit.spec.ts (new)
M12 files: src/views/agent/AgentMonitoring.vue (replace stub)
```

```bash
bash numind-server/scripts/ndf/ndf-check-disjoint.sh \
  "src/views/agent/AgentList.vue,src/constants/agentErrno.ts,src/views/agent/__tests__/AgentList.spec.ts" \
  "src/views/agent/AgentCreateChoose.vue,src/views/agent/TemplateGallery.vue" \
  "src/views/agent/AgentAdvancedEdit.vue,src/views/agent/__tests__/AgentAdvancedEdit.spec.ts" \
  "src/views/agent/AgentMonitoring.vue"
# Expected: exit 0
```

### Wave 6（M9a + M9b 串行 — P1-2 split）

> P1-2 fix: M9 拆 M9a (纯 validation 逻辑) + M9b (Builder + Edit wrapper)；M9a 可独立测；M9b 依赖 M9a。

```
M9a files: src/views/agent/components/validation.ts (new)
           src/views/agent/components/__tests__/validation.spec.ts (new)
M9b files: src/views/agent/AgentBuilder.vue (new)
           src/views/agent/AgentEdit.vue (replace stub)
           src/views/agent/__tests__/AgentBuilder.spec.ts (new)
           src/views/agent/components/AfterSaveModal.vue (new — P1-3 fix moved from M10)
```

M9a → M9b 串行（Builder 用 validation）。Wave 6 内顺序 dispatch。

> **P1-3 fix**: `AfterSaveModal.vue` 从 M10 移到 M9b — M9 AgentBuilder 直接 import/render，避免 cross-Wave import 失败。

### Wave 7（M10 串行 — 依赖 M9b 完成；AfterSaveModal 已搬到 M9b）

```
M10 files: src/views/agent/AgentDetail.vue (replace stub)
           src/views/agent/components/AgentConfigTab.vue (new)
           src/views/agent/components/AgentHistoryTab.vue (new)
           src/views/agent/components/AgentStatsTab.vue (new)
           src/views/agent/components/HistoryViewModal.vue (new)
           src/views/agent/components/AdvancedToggleConfirmModal.vue (new)
           src/views/agent/__tests__/AgentHistoryTab.spec.ts (new)
```

### Wave 8（M13a + M13b 并行 — P0-2 split for cross-worktree）

> P0-2 fix: M13 拆为 M13a (admin-web e2e specs) + M13b (numind-server follow-up + S5 acceptance placeholder)。M13a 在 admin-web worktree commit；M13b 在 numind-server cux-server-docs worktree（temp worktree）commit 到 develop。

```
M13a files: e2e/agent-template-derive.spec.ts (new)
            e2e/agent-scratch-create.spec.ts (new)
            e2e/agent-advanced-toggle.spec.ts (new)
            e2e/agent-history-restore.spec.ts (new)
M13b files: follow-ups/agent-mode-skill-system-advanced-mode-edit.md (new)
            follow-ups/agent-mode-skill-system-monitoring-api.md (new)
            docs/superpowers/qa/2026-05-21-agent-mode-configurator-ux-s5-acceptance.md (placeholder)
```

M13a → admin-web worktree commit；M13b → cux-server-docs worktree commit。**两个分布在不同 git repo，不能同 dispatch**——主 session 分两次操作（M13a dispatch subagent；M13b 主 session 自己写文件不需 subagent）。

---

## 4. 文件归属总表（全文件 disjoint）

> 所有 `__tests__` 目录由 implementer subagent 创建（若不存在）。vitest config include `src/**/__tests__/**/*.spec.ts` 自动发现。

```
[admin-web 仓库 — /private/tmp/wt-agent-mode-configurator-ux-numind-admin-web/]

M1:  src/types/agent.ts                                                    (NEW)
M2:  src/api/request.ts                                                    (MOD +5 lines patch helper)
     src/api/__tests__/request.spec.ts                                     (NEW dir + spec)
M3:  src/api/agent.ts                                                      (NEW)
M4:  src/stores/agent.ts                                                   (NEW)
     src/stores/__tests__/agent.spec.ts                                    (NEW dir + spec)
M5a: src/components/layout/AdminSidebar.vue                                (MOD)
M5b: src/router/index.ts                                                   (MOD; stubs first, router edit second)
     src/views/agent/AgentList.vue                                         (NEW stub)
     src/views/agent/AgentCreateChoose.vue                                 (NEW stub)
     src/views/agent/TemplateGallery.vue                                   (NEW stub)
     src/views/agent/AgentDetail.vue                                       (NEW stub)
     src/views/agent/AgentEdit.vue                                         (NEW stub)
     src/views/agent/AgentMonitoring.vue                                   (NEW stub)
M6:  src/components/common/CheckboxGroup.vue                               (NEW)
     src/components/common/NoticeBanner.vue                                (NEW)
     src/views/agent/components/ChipInput.vue                              (NEW)
     src/views/agent/components/CreditSlider.vue                           (NEW)
     src/views/agent/components/AvatarPicker.vue                           (NEW)
     src/views/agent/components/QuestionnaireForm.vue                      (NEW)
     src/components/common/__tests__/CheckboxGroup.spec.ts                 (NEW dir + spec)
     src/components/common/__tests__/NoticeBanner.spec.ts                  (NEW)
     src/views/agent/components/__tests__/ChipInput.spec.ts                (NEW dir + spec)
     src/views/agent/components/__tests__/CreditSlider.spec.ts             (NEW)
     src/views/agent/components/__tests__/AvatarPicker.spec.ts             (NEW)
     src/views/agent/components/__tests__/QuestionnaireForm.spec.ts       (NEW)
M7:  src/views/agent/AgentList.vue                                         (REPLACE stub)
     src/constants/agentErrno.ts                                           (NEW)
     src/views/agent/__tests__/AgentList.spec.ts                           (NEW dir + spec)
M8:  src/views/agent/AgentCreateChoose.vue                                 (REPLACE stub)
     src/views/agent/TemplateGallery.vue                                   (REPLACE stub)
M9a: src/views/agent/components/validation.ts                              (NEW)
     src/views/agent/components/__tests__/validation.spec.ts               (NEW)
M9b: src/views/agent/AgentBuilder.vue                                      (NEW)
     src/views/agent/AgentEdit.vue                                         (REPLACE stub)
     src/views/agent/components/AfterSaveModal.vue                         (NEW — moved from M10 per P1-3)
     src/views/agent/__tests__/AgentBuilder.spec.ts                        (NEW)
M10: src/views/agent/AgentDetail.vue                                       (REPLACE stub)
     src/views/agent/components/AgentConfigTab.vue                         (NEW)
     src/views/agent/components/AgentHistoryTab.vue                        (NEW)
     src/views/agent/components/AgentStatsTab.vue                          (NEW)
     src/views/agent/components/HistoryViewModal.vue                       (NEW)
     src/views/agent/components/AdvancedToggleConfirmModal.vue             (NEW)
     src/views/agent/__tests__/AgentHistoryTab.spec.ts                     (NEW)
M11: src/views/agent/AgentAdvancedEdit.vue                                 (NEW)
     src/views/agent/__tests__/AgentAdvancedEdit.spec.ts                   (NEW)
M12: src/views/agent/AgentMonitoring.vue                                   (REPLACE stub)
M13a: e2e/agent-template-derive.spec.ts                                    (NEW)
      e2e/agent-scratch-create.spec.ts                                     (NEW)
      e2e/agent-advanced-toggle.spec.ts                                    (NEW)
      e2e/agent-history-restore.spec.ts                                    (NEW)

[numind-server 仓库 — /private/tmp/wt-cux-server-docs/]
M13b: follow-ups/agent-mode-skill-system-advanced-mode-edit.md             (NEW + dir)
      follow-ups/agent-mode-skill-system-monitoring-api.md                 (NEW)
      docs/superpowers/qa/2026-05-21-agent-mode-configurator-ux-s5-acceptance.md (NEW placeholder)
```

**总文件数验证**：~48 unique files。MX vs MY 同 Wave 内交集？逐对 grep 后无重叠（每 task 拥有独立路径前缀）。

**跨仓库注意**：M13b 不在 admin-web feature worktree（在 numind-server cux-server-docs worktree on develop branch）。主 session 在 cux-server-docs 直接写 + commit 到 develop。

---

## 5. S5 验证策略（规则 10 强制）

**验证方式**：**Playwright E2E + vitest 单元 + `npm run lint && npm run type-check` + manual visual QA**

**为什么不仅 gstack /qa**：
- /qa 是一次性快照，不留持久回归保护
- 12 题问卷验证规则、销毁性 Modal 流程、表单 dirty 守卫——这些是**关键交互逻辑**，将来改动 Builder 时需要回归测试
- 模板派生 / 历史回滚是核心用户路径，长期需要 e2e 保护
- 表单验证函数（12 个）+ store actions（9 个）+ 子组件（5 个）—— 单测覆盖率应 ≥ 60%

**S5 验证清单**：

### 5.1 静态校验
- [ ] `cd numind-admin-web && npm run lint` → 0 errors，warning ≤ baseline (2)
- [ ] `npm run type-check` → exit 0
- [ ] `grep -rE "import axios" numind-admin-web/src/views/agent/ numind-admin-web/src/api/agent.ts numind-admin-web/src/stores/agent.ts` → 0 hits（验证不直接 import axios）
- [ ] `grep -rE "openai|anthropic|dashscope|sk-|API_KEY" numind-admin-web/src/views/agent/ numind-admin-web/src/api/agent.ts numind-admin-web/src/stores/agent.ts` → 0 hits
- [ ] `grep -E "element-plus|ant-design-vue|vant|naive-ui" numind-admin-web/package.json` → 0 new entries

### 5.2 单测
- [ ] `npm run test:unit` PASS
- [ ] vitest coverage（agent 模块）≥ 60%（store actions / validation 12 函数 / 5 子组件 / Builder 核心流程）

### 5.3 E2E
- [ ] 启 dev server：`cd numind-admin-web && npm run dev`（监听 5174）
- [ ] BASE_URL=http://localhost:5174 + E2E_USERNAME=$E2E_USERNAME E2E_PASSWORD=$E2E_PASSWORD 跑 `npm run test:e2e -- e2e/agent-*.spec.ts`
- [ ] 4 specs 全 PASS

### 5.4 Manual visual QA（dev 部署后）
- [ ] /deploy-dev → 访 http://49.233.219.254:9100/agents
- [ ] 用 dev 父账户登录 → sidebar 看到 "AI 助手" + "Agent 监控"
- [ ] 创建第一个 Agent（从模板派生），看到弹试聊 Modal，点 [暂时跳过] 回详情
- [ ] 详情 3 Tab 切换正常；配置 Tab 只读 OK
- [ ] 历史 Tab 显示 v1 "首次发布"
- [ ] 编辑改个字段 → 保存 → 历史 v2 出现 changes_summary "修改了 Q1（名字）"
- [ ] 历史 [恢复 v1] → ConfirmModal → 确认 → v3 出现 "从 v1 恢复"
- [ ] 高级切换：编辑 → 右下角链接 → 警示 Modal → 确认 → URL /edit 切到只读 body view + NoticeBanner
- [ ] 监控页：访 /agent-monitoring → 看到 NoticeBanner + DataTable 空状态
- [ ] 下架某 agent：列表 → [下架] → ConfirmModal → 确认 → agent 从列表消失
- [ ] **manual: 子账户 403 模拟**（用 curl 调一次 `/v1/agent/skills` 用子账户 token → 应得 HTTP 403；UI 后续可自测父账户 vs 子账户对比；如 dev 无子账户测号 → 此项 record-defer）

### 5.5 0 prod 影响验证
- [ ] `git -C numind-server diff develop` 无任何代码改动（只有 docs commits）
- [ ] `git -C numind-admin-web log --oneline -10` 全是 feature 分支的 commits
- [ ] 无 git tag 新建
- [ ] 无 /deploy-prod 调用

**理由**：本 feature 是用户高频路径（配置 Agent 是 #11 学员端的前置），未来改动频次大；e2e + 单测 + 严格 type-check 是合理投入。

---

## 6. Follow-up issues（S2 §20 决定，S3 创建文件）

### `numind-server/follow-ups/agent-mode-skill-system-advanced-mode-edit.md`

```markdown
# Follow-up: agent-mode-skill-system advanced-mode body edit

**触发**: #10 agent-mode-configurator-ux S2 spec 发现
**优先级**: P1（影响高级模式 5% 用户体验）
**类型**: backend + frontend 联动 micro

## Problem
#5 backend `controller/v1/agent/skill.go` `PatchRequest` 缺 `custom_skill_body *string` 字段。
切高级模式后无法保存自定义 SKILL.md 全文（仅能切工具开关）。

## Proposed
1. backend (micro)：PatchRequest 加 `custom_skill_body *string` + service.go Patch 处理
2. frontend (micro，依赖 backend 先 merged)：升级 `views/agent/AgentAdvancedEdit.vue` 为可编辑

## Acceptance
- 切高级 → 编辑 textarea → 保存 → DB custom_skill_body 更新 + 历史新版本
- 平台固定段 PLATFORM_BASE_PROMPT 不被用户修改（保存时后端拒绝）
```

### `numind-server/follow-ups/agent-mode-skill-system-monitoring-api.md`

```markdown
# Follow-up: agent-mode-skill-system monitoring API

**触发**: #10 agent-mode-configurator-ux S2 spec 发现
**优先级**: P2（监控功能直到 #14 e2e rollout 才真正需要）
**类型**: backend

## Problem
admin-web `/agent-monitoring` UI 已 ready，但后端缺：
- GET /v1/agent/sessions/active：返回正在运行的 agent_run 列表
- POST /v1/agent/sessions/:id/cancel：强制取消

## Proposed
依赖 #14 e2e rollout（agent_run 表 + ReAct loop 全 wire 真实 LLM）后落地。

## Acceptance
- 监控页可看到运行中会话 + [强制取消] 按钮可用
- 30s 自动刷新
```

---

## 7. Subagent dispatch 模板（S4 实施用）

每个 task dispatch 1 个 Sonnet implementer subagent，prompt 模板：

```
You are a Sonnet implementer for NDF v2 stage S4 — feature #10 agent-mode-configurator-ux.

**Task**: M{N} - {task name}
**File ownership**: <files from §4>
**Spec location**: /private/tmp/wt-cux-server-docs/docs/superpowers/specs/2026-05-21-agent-mode-configurator-ux-design.md (§N relevant section)
**Worktree**: /private/tmp/wt-agent-mode-configurator-ux-numind-admin-web/
**Branch**: feature/agent-mode-configurator-ux

Implementation:
1. Read spec section (cd worktree; read spec from /private/tmp/wt-cux-server-docs/...)
2. **Create __tests__ dir if absent** (mkdir -p as needed)
3. Implement files exactly per spec
4. **For M5b: create 6 stub view files FIRST, then edit router/index.ts** (避免 lazy import 引用不存在文件导致 type-check 失败)
5. Run `npm run type-check` in worktree
6. Run relevant unit tests if any (`npm run test:unit -- <file>`)
7. Commit with message "feat(agent-configurator): M{N} <description>" + Co-Authored-By trailer
8. Report DONE with commit hash + tests passing summary

Constraints:
- Use Composition API <script setup lang="ts">
- Pinia setup syntax
- No external UI framework (硬规则#5)
- 4 状态全处理 (硬规则#2)
- Form validation on blur (硬规则#3)
- Destructive ops via ConfirmModal (硬规则#4)
- Management lists use DataTable (硬规则#1)
- Imports via axios go through src/api/request.ts (not direct import)
- Types only from @/types/agent.ts (no circular import)

Validation gate before commit:
- npm run lint passes (warning ≤ baseline = 2, error == 0)
- npm run type-check passes
```

---

## 8. 风险与降级路径

| 风险 | 影响 | 降级 |
|------|------|------|
| backend 9 端点其中一个 字段命名 与 model.AgentDefinition json tag 不一致 | TypeScript runtime mismatch | M4 store 单测发现 → S4 修 store normalize 层 |
| `npm run test:e2e` 在 dev BASE_URL 不通过（dev server 状态不稳） | e2e 卡 | E2E 改 BASE_URL=http://localhost:5174（M9b 后开 dev server 跑） |
| Builder 12 题 reactive 性能差 | 输入卡顿 | onMounted 加 console.time/timeEnd 测 mount 时间；超 200ms 改 reactive 拆 ref 策略 |
| AdvancedEdit `custom_skill_body` 显示但 PATCH 不能改 | 用户期望落空 | NoticeBanner 明示已实装；follow-up issue 已存档 |
| Playwright auth.setup.ts 用 `admin_token` 而本 feature 同 token 路径 | 兼容 | 无修改，复用现有 setup |
| 循环 import store ↔ component（P2-2 fix） | 编译失败或运行时 undefined | types 只放 @/types/agent.ts；store import types；components import types；components 不 import store 的类型 — 仅 import 函数 useAgentStore |
| happy-dom（vitest env）FileReader / keydown 不完整（P2-3 fix） | AvatarPicker / ChipInput 单测失败 | 单 spec 文件顶部 `// @vitest-environment jsdom` 切换 jsdom |
| dev 父账户测号 vs E2E_USERNAME 不匹配（dev admin 不是父账户）| e2e 创建 agent 调 POST → 403 | S4 前 manual 跑 `curl -H "Authorization: Bearer $TOKEN" ${DEV_API_URL}/v1/agent/skills` 验证 200 / 403；若 403 → blocker (需用户配置父账户 dev 凭据) |
| Wave 5 同时 4 subagent dispatched | TPM / 上下文不够 | 实际 S4 按需降级为 2-2 串行（M7+M8 / M11+M12 两 wave） |

---

## 9. 0 prod 影响 reaffirm

- 不动 numind-server（除了 NDF docs / follow-up issue markdown）
- 不打 git tag
- 不调 /deploy-prod
- feature 分支不推 GitHub
- 不引入新 npm 依赖

---

**S3 完结。S4 编码开始。**
