# SOP 运行页视觉 + IA 重设计 — 技术规格 (S2 Spec)

## 0. 元信息

| 字段 | 值 |
|---|---|
| feature_id | `sop-runtime-visual-redesign` |
| ndf_version | 1.0 |
| status | S2 Draft |
| created_at | 2026-04-11 |
| author | Claude + 产品负责人 |
| repos | `numind-web-v3` (重), `numind-server` (轻) |
| 前置依赖 | `sop-runtime-vue-rewrite` (S5 已完成) |
| 视觉契约 | `numind-server/proposals/sop-runtime-visual-redesign-mockups/01-active-and-history.html`、`02-additional-states.html` |
| 上游工件 | `requirements/sop-runtime-visual-redesign.md`, `proposals/sop-runtime-visual-redesign-proposal.md`, `proposals/sop-runtime-visual-redesign-backend-audit.md` |

本 spec 是 **改造规格** 而非新建规格。它描述如何把当前已经工程化但视觉停在 legacy 的 `SOPRunView` 及其子组件，改造为与 mockup 像素级对齐的 γ Focused 布局，并顺带补齐后端 2 个小字段以支撑 mockup 的 footer 元信息。

---

## 1. Scope

### 1.1 In Scope

**numind-web-v3（重）**
- `src/views/sop/SOPRunView.vue` 整体结构重写（从"垂直滚动列表"改为"topbar + 左 nav + 主区"三栏）
- `src/views/sop/components/` 下全部子组件：保留可复用逻辑，改写 template + style 以对齐 mockup
- 新增若干子组件（StepNav / StepCanvas / InputCard / OutputCard / MetaFooter / TrailingChat 等，详见 §3.1）
- `src/stores/sopRun.ts` 扩展 viewing state 字段（区分"当前任务"和"正在查看的历史步骤"）
- `src/api/sop.ts` 补 `saveBookmark` / `removeBookmark` 封装
- `src/views/sop/types.ts`（含 `SopNodeRun.model_name` 字段等）
- 设计 token 与 mockup 对齐（页面内 scope，不全局污染）

**numind-server（轻）**
- 2 个 migration 加列：`sop_node_run.model_name`、`sop_chat_message.{model_name, duration_ms}`（P0-1 修复：chat 表实际缺少 model_name 字段，必须同时加）
- `internal/pkg/model/sop.go` 两个 model 新增字段
- `internal/numind/biz/sop/sop.go` 节点执行完成 / chat 消息保存时同步落字段
- 确认 DTO / controller 回传这两个字段给前端（不做 json:"-"）

### 1.2 Out of Scope

显式不做：
- 后端业务逻辑（执行流程 / SSE 协议 / 幂等 / 心跳）
- 权限 / 配额 / 积分扣减逻辑
- 模板 / 节点编辑（admin 侧 self-service-config）
- 历史 run 列表页 `SOPHistoryView`（保留旧 UI，仅通过顶部"历史"icon 弹 modal）
- Mobile / 平板响应式适配（桌面最小宽度约定 1280px）
- 国际化 / a11y 专项
- LLM 模型切换 UI（是另一个独立 feature `llm-model-switch`）
- 服务端流中断（后端 abort），停止生成仅前端 `EventSource.close()`
- `fetchExecutedRuns` / `deleteRun` / `batchDeleteRuns` 等历史记录 API 不改

---

## 2. 现状分析（important）

> 改造前必须理解目前跑着的代码结构，避免 S4 时出现"为了贴 mockup 把可复用逻辑扔掉再重写"。

### 2.1 当前 SOPRunView 组件树

| 文件 | 职责 | 大致规模 | 是否保留 |
|---|---|---|---|
| `views/sop/SOPRunView.vue` | 主容器：路由参数 → store init → 渲染 stepper + main | ~740 行 | **大改** template+style，保留 script 主干 |
| `components/StepperPanel.vue` | 横向步骤指示器（从左到右 N 个圆圈） | 中 | **废弃** 横向结构，核心计算逻辑迁入新 `StepNav` |
| `components/StepInput.vue` | textarea + 文件上传 + 字数统计 | 中 | **保留 script 主干**，template 改为 `InputCard` 变体 |
| `components/StepOutput.vue` | markdown 渲染 + 思维链 + 滚动跟随 | 中 | **保留 script 主干**，template 改为 `OutputCard` 变体 |
| `components/ToolbarActions.vue` | 底部三按钮（prev / copy / regenerate / next） | 小 | **拆分**：action row（主 CTA）+ OutputCard head 右侧 tiny button |
| `components/TrailingChatPanel.vue` | 末尾聊天面板 | 中 | **改造** 为 headless 全铺模式，去 step header |
| `components/ChatBubble.vue` | 聊天气泡 | 小 | **保留**，加 meta 行（模型 · 耗时 · token） |
| `components/HistoryModal.vue` | 历史记录弹窗 | 中 | **保留**，topbar 历史 icon 触发 |
| `components/EmptyStateCard.vue` | loading/error/empty | 小 | **保留** |
| `components/ScrollFollowButton.vue` | 跳回底部按钮 | 小 | **保留** |

**关键观察**：当前 script 侧结构已经很干净（store / composables 抽得好），视觉重写的主要工作是 template + style，不动 biz/store 的核心 action。但**信息架构**要改：原来"所有步骤下滑堆叠"的垂直结构要改成"单步聚焦 + 左 nav 切换"。

### 2.2 当前 store 状态结构（`src/stores/sopRun.ts`）

**state（保留）：**
- `template: SopTemplatePublic | null`
- `nodes: SopNodePublic[]`
- `currentRun: SopRun | null`
- `nodeRuns: Record<number, SopNodeRun>` — 节点执行结果缓存
- `completedNodeIds: Set<number>`
- `nextNodeId: number | null`
- `nodeAccessibility: Record<number, boolean>`
- `streamingNodeId / streamingThinking / streamingContent`
- `currentStep: number`（1-based）
- `loading / lastError`

**getters（保留）：** `isDraftRun` / `trailingChatEnabled` / `totalSteps` / `isOnTrailingChatStep` / `currentNode`

**actions（保留）：** `loadTemplate / loadRun / enterDraftMode / setCurrentRun / markNodeComplete / markNodeIncomplete / setNextNodeId / setNodeRun / setStreamingState / appendStreamingThinking / appendStreamingContent / clearStreamingState / setActiveStep / reset`

### 2.3 当前 API 调用（`src/api/sop.ts`）

| 函数 | Endpoint | 状态 |
|---|---|---|
| `fetchTemplateNodes(id)` | `GET /v1/sop/templates/:id/nodes` | 保留 |
| `fetchRun(runId)` | `GET /v1/sop/runs/:id` | 保留 |
| `fetchRunStatusDetail(runId)` | `GET /v1/sop/runs/:id/status` | 保留 |
| `createRun(body)` | `POST /v1/sop/runs` | **改**：默认传 `auto_apply_bookmarks=true` |
| `listBookmarksByTemplate` | `GET /v1/sop/templates/:id/bookmarks` | 保留 |
| `applyBookmark` | `POST /v1/sop/runs/:id/nodes/:node_id/apply-bookmark` | 保留 |
| `listRunChatMessages` | `GET /v1/sop/runs/:id/chat-messages` | 保留 |
| `uploadImageForOCR` / `uploadFileForText` | 上传相关 | 保留 |
| `fetchExecutedRuns` / `deleteRun` / `batchDeleteRuns` | 历史记录 | 保留 |
| **新增** `saveBookmark` | `POST /v1/sop/bookmarks` | **新增**（后端 endpoint 已存在） |
| **新增** `removeBookmark` | `DELETE /v1/sop/bookmarks/:id` | **新增**（后端 endpoint 已存在） |

> 注意：SSE execute 和 chat stream 的 URL 是在 SOPRunView 内通过 `useSSEStream` 直接构造的 fetch 请求，不经 `src/api/sop.ts`。本 feature 不改动这条路径。

### 2.4 当前数据流（draft → execute → done → next）

```
用户进入页面 (无 runId)
  → SOPRunView onMounted
  → store.loadTemplate(templateId) [GET /templates/:id/nodes]
  → bookmarks.loadBookmarks(templateId) [GET /templates/:id/bookmarks]
  → store.enterDraftMode(templateId)  // currentRun = null, nextNodeId = nodes[0].id
  → navigation.restoreFromSession()  // 从 sessionStorage 恢复上次停留步骤
  → persistence.loadInput(...)  // 从 localStorage 恢复节点输入

用户填 input / 上传文件
  → StepInput v-model currentInputText
  → persistence.saveInput(...)  // 实时写 localStorage
  → 第一次需要 runId 时（上传 / 执行）
     → ensureRun()
     → draft.lazyCreateRun(templateId, composedText) [POST /runs, auto_apply_bookmarks]
     → store.setCurrentRun(...)
     → router.replace({ query: { runId } })

用户点执行
  → executeCurrentNode()
  → store.setStreamingState(nodeId, '', '')
  → sseStream.streamPost(POST /runs/:id/nodes/:nid/execute, FormData{text, files})
  → onThinking / onMessage → store.appendStreamingThinking/Content
  → onDone → store.setNodeRun + markNodeComplete + clearStreamingState + setNextNodeId

用户点"下一步"
  → navigation.setActiveStep(currentStep + 1)
```

**关键约束（当前已成立）**：
- 所有状态都通过 `store.nodeRuns[nodeId]` 或 SSE streaming state 读
- 历史回看当前是靠 `currentStep` 一个标量切换步骤
- **没有"当前任务 vs 正在查看的步骤"的区分** —— 这是本 feature 要补的核心 state

---

## 3. 目标架构

### 3.1 新组件树

| 路径 | 类型 | 职责 | 对应 mockup |
|---|---|---|---|
| `views/sop/SOPRunView.vue` | **改** | 顶层容器：topbar + body(nav + main) 双栏布局；持 store，协调 composables | 所有状态的 `.app` 骨架 |
| `views/sop/components/TopBar.vue` | **新** | slim header：返回 / 模板名 / 历史 icon | `.header`（56px） |
| `views/sop/components/StepNav.vue` | **新** | 左侧 vertical step nav，承载 `主流程` + `追问` 两组 steps，支持 active / done / viewing / pending-return / disabled 五态 | `.nav` + `.step--*` |
| `views/sop/components/StepNavItem.vue` | **新** | 单条 step，接收 `state: 'active' \| 'done' \| 'viewing' \| 'pending-return' \| 'disabled'` | `.step`（见 §3.2） |
| `views/sop/components/StepCanvas.vue` | **新** | 主区容器 / 路由器：根据当前 viewing step 的类型和状态，决定渲染 `SopStepView` 还是 `TrailingChat` | `.main + .canvas` |
| `views/sop/components/SopStepView.vue` | **新** | SOP 节点主区：step header（title + description）+ 根据状态渲染 InputCard / OutputCard / MetaFooter / ActionRow | 状态 A/B/C/D/E |
| `views/sop/components/InputCard.vue` | **新** | 白底 card，封装 `StepInput` 的输入 + 上传 + 字数；底部 toolbar（左上传 + 字数；右执行按钮或停止按钮） | 状态 A/C `.card` |
| `views/sop/components/OutputCard.vue` | **新** | 白底 card：head（sparkle + "AI 输出" + live-dot + tiny buttons: ⭐/复制）+ body（markdown 渲染）+ foot（meta 行） | 状态 B/D/E `.output` |
| `views/sop/components/OutputEmpty.vue` | **新** | 虚线边框占位，状态 A 使用 | `.output-empty` |
| `views/sop/components/MetaFooter.vue` | **新** | 底部 mono 小字 `耗时 · 模型 · tokens · 完成时间`（集成进 OutputCard 内，也可独立给 chat 消息用） | `.output__foot` / `.msg__meta` |
| `views/sop/components/ActionRow.vue` | **新** | 主区底部按钮行：主 CTA（执行 / 下一步 / 返回步骤 N）+ 次 CTA（重新生成） | 状态 B/E `.action-row` / `.return-row` |
| `views/sop/components/TrailingChat.vue` | **新** | Headless 全铺聊天；内部使用 `ChatBubble` + `MetaFooter` | 状态 F `.chat` |
| `views/sop/components/ChatBubble.vue` | **改** | 用户气泡右对齐 + AI 气泡左对齐；AI 气泡下方贴 MetaFooter | `.msg` |
| `views/sop/components/ChatComposer.vue` | **新** | sticky 底部 textarea + 发送/停止按钮 | `.chat__composer` |
| `views/sop/components/HistoryViewStrip.vue` | **新** | 当 viewing 一个已完成步骤时，主区顶部出现一条 info strip："正在查看历史步骤，输入不可修改"，含"返回当前步骤"CTA | 无 mockup 直接对应（D5 硬约束要求） |
| `views/sop/components/StepInput.vue` | **改** | 去掉 card wrapper；仅导出 `compose()` + textarea + upload logic，由 InputCard 包裹 | — |
| `views/sop/components/StepOutput.vue` | **改** | 去掉 card wrapper；仅导出 markdown + thinking 渲染，由 OutputCard 包裹 | — |
| `views/sop/components/StepperPanel.vue` | **删** | 被 StepNav 替代 | — |
| `views/sop/components/ToolbarActions.vue` | **删** | 职责分到 ActionRow + OutputCard head | — |
| `views/sop/components/EmptyStateCard.vue` | **保留** | loading / error | — |
| `views/sop/components/ScrollFollowButton.vue` | **保留** | 滚动跟随按钮，挂在 OutputCard body 上 | — |
| `views/sop/components/HistoryModal.vue` | **保留** | 历史 modal | — |

### 3.2 状态机（step type × 状态 × UI）

| step type | 内部状态 | UI 状态名 | StepNav 标记 | 主区组件 | InputCard 可见 | OutputCard 状态 | MetaFooter | 主 CTA | 次 CTA | HistoryViewStrip |
|---|---|---|---|---|---|---|---|---|---|---|
| sop-node | draft / first-entry | **C** | `active` | SopStepView | yes editable | hidden（empty 占位） | — | 执行这一步 | — | — |
| sop-node | active未执行（非首次） | **A** | `active` | SopStepView | yes editable | OutputEmpty 或上一步结果摘要（取消） | — | 执行这一步 | — | — |
| sop-node | executing（SSE 流中） | **D** | `active` + live dot | SopStepView | **hidden** | streaming（live-dot + 追加 markdown + caret） | — | 停止生成 | — | — |
| sop-node | done（viewing 当前刚完成步） | **E** | `done` (但 active 指针已前进到 next) | SopStepView | hidden | read-only markdown | yes | 下一步 / 返回步骤 N | 重新生成 + ⭐ | — |
| sop-node | done（viewing 历史步骤）| **B** | `done` + `viewing` | SopStepView | hidden | read-only markdown | yes | 返回步骤 N（主 CTA）| ⭐ + 复制 | yes |
| sop-node | disabled | — | `disabled` | — | 不能进入 | — | — | — | — | — |
| trailing-chat | active（有消息） | **F** | `active`（chat 图标）| TrailingChat（无 step header） | composer | chat history | 每条 AI 消息贴 meta | 发送 | 停止生成（流中） | — |
| trailing-chat | active（首次）| F-empty | `active` | TrailingChat + welcome empty state | composer | empty + hint | — | 发送 | — | — |
| trailing-chat | disabled（主流程未完成）| — | `disabled` | — | — | — | — | — | — | — |

**⭐（收藏）button 隐藏条件统一**（修订 P1-3）：⭐ button 在 State A / C 下隐藏，统一以 **"本节点 nodeRun 不存在或 nodeRun.output 为空"** 作为隐藏条件（通过 `hasOutput: boolean` prop 在 OutputCard 上用 `v-if="hasOutput"` 控制）。State B / D-done / E 下该节点必有 output → 显示。

**特殊：State B 的 "pending-return"**
进入 B 状态时，StepNav 上原本 active 的步骤（即"下一个要做的任务"）被标记为 `pending-return`（虚线环），以告诉用户"回到这里继续"。这是 mockup 01 的 `.step--pending-return` 直接对应的状态。

### 3.3 Store 改造（`src/stores/sopRun.ts`）

#### 新增 state

```typescript
// 当前"正在查看"的步骤（1-based）。
// 区别于 currentStep：
//   - currentStep 是"当前任务"，推进主流程、决定 nextNodeId 基准
//   - viewingStep 是"用户此刻看的步骤"，可能等于 currentStep（默认看当前）
//     也可能是历史步骤（用户点 StepNav 过去）
// 规则：viewingStep 可 <= currentStep，不允许 > currentStep
const viewingStep = ref<number>(1)
```

#### 新增 getters

```typescript
const viewingNode = computed<SopNodePublic | null>(() => {
  if (isViewingTrailingChat.value) return null
  return nodes.value[viewingStep.value - 1] ?? null
})

const isViewingTrailingChat = computed(() =>
  trailingChatEnabled.value && viewingStep.value === nodes.value.length + 1
)

/**
 * 正在看的 step 的业务状态
 * - 'draft-first':  没有 run，看的是第一步（等价状态 C）
 * - 'active':       看的正是当前任务步骤且未执行（状态 A）
 * - 'executing':    viewing 正在流式执行（状态 D）
 * - 'done-current': 当前任务刚执行完、正在看它（状态 E）
 * - 'done-history': viewing 是过去已完成步骤（状态 B）
 * - 'trailing':     viewing 在 trailing chat
 */
const viewingStepStatus = computed<
  'draft-first' | 'active' | 'executing' | 'done-current' | 'done-history' | 'trailing'
>(() => { /* ... 根据 currentStep / viewingStep / streamingNodeId / completedNodeIds 判断 */ })

const isViewingHistory = computed(() => viewingStepStatus.value === 'done-history')
```

#### 新增 actions

```typescript
function setViewingStep(step: number): void     // 用户点 StepNav 切换"看哪一步"
function returnToCurrentTask(): void            // 从 history 返回 → viewingStep = currentStep
function advanceCurrentStep(): void             // 执行成功后：currentStep++, viewingStep = currentStep
async function refreshNodeRun(nodeId: number): Promise<void>  // onDone 回调触发：调 fetchRunStatusDetail(currentRun.id) → 从 completed_nodes 中找到该 nodeId 的完整 info（含 model_name / latency_ms / total_tokens）→ 更新 nodeRuns[nodeId]
```

#### 保留不变

`currentStep`、`setActiveStep`、已有 node run / streaming / bookmark accessibility 的所有 action 全部保留。`currentStep` 的语义**收窄**为"当前任务指针"，不再兼做"正在看的步骤"。

#### 不改 action

SSE 流（`useSSEStream`）、lazyCreateRun（`useDraftLifecycle`）、input persistence、file upload 全部不动。

### 3.4 API 层改造（`src/api/sop.ts`）

#### 新增

```typescript
// POST /v1/sop/bookmarks
export interface SaveBookmarkRequest {
  run_id: number
  node_id: number
  bookmark_name?: string
  description?: string
}
export interface SaveBookmarkResponse {
  id: number
  node_id: number
  node_sort: number
  node_name?: string
  bookmark_name: string
  total_tokens: number
  created_at: string
}
export const saveBookmark = async (body: SaveBookmarkRequest): Promise<SaveBookmarkResponse> => {
  const res = await request.post('/v1/sop/bookmarks', body)
  return (res as unknown as { data: SaveBookmarkResponse }).data
}

// DELETE /v1/sop/bookmarks/:id
export const removeBookmark = async (bookmarkId: number): Promise<void> => {
  await request.delete(`/v1/sop/bookmarks/${bookmarkId}`)
}
```

#### 改动

```typescript
// createRun 的默认行为：auto_apply_bookmarks=true
export const createRun = async (body: CreateRunRequest): Promise<CreateRunResponse> => {
  const payload = { auto_apply_bookmarks: true, ...body } // body 后置允许 caller 覆盖
  const res = await request.post('/v1/sop/runs', payload)
  return (res as unknown as { data: CreateRunResponse }).data
}
```

注：实际 caller 是 `composables/useDraftLifecycle.ts` 的 `lazyCreateRun`，需要同步更新为不显式传 flag（使用默认即可）。

### 3.5 类型改造（`src/views/sop/types.ts`）

```typescript
export interface SopNodeRun {
  id: number
  run_id: number
  node_id: number
  status: 'pending' | 'running' | 'succeeded' | 'failed'
  input: string
  output: string
  thinking: string
  latency_ms: number
  /** 新增：节点使用的模型名（来自后端 Gap 1 修复） */
  model_name: string
  /** 新增：通过 DTO 透出的 token 总量（model 层字段 json:"-"，由 B5 task 在 DTO 层映射） */
  total_tokens?: number
  is_accessible?: boolean
  started_at: string | null
  finished_at: string | null
}

// 新增
export interface SopChatMessageMeta {
  model_name: string
  duration_ms: number
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  created_at: string
}
```

`api/sop.ts` 的 `RunChatMessageItem` 也需要补 `model_name`、`duration_ms` 字段（后端补字段后才能读到真实值）。

---

## 4. 后端改动（numind-server）

### 4.1 Migration — sop_node_run 加 model_name

**路径**：`numind-server/migrations/20260411_120000_add_sop_node_run_model_name.sql`

```sql
-- 加 model_name 列到 sop_node_run，用于前端 mockup 元信息 footer 展示
-- 当前前端需要从 sop_node.model_id 反查，性能差且模板更换模型会污染历史记录。
-- 冗余落在 node_run 上即可。
ALTER TABLE sop_node_run
  ADD COLUMN model_name VARCHAR(64) NOT NULL DEFAULT '' AFTER latency_ms;

-- 回滚: ALTER TABLE sop_node_run DROP COLUMN model_name;
```

### 4.2 Migration — sop_chat_message 加 model_name + duration_ms

**路径**：`numind-server/migrations/20260411_120100_add_sop_chat_message_model_name_and_duration_ms.sql`

```sql
-- 加 model_name + duration_ms 到 sop_chat_message，用于 trailing chat 每条 AI 消息
-- 的模型 + 耗时元信息显示（mockup 状态 F `.msg__meta`）。
-- P0-1 修复：代码中 SopChatMsg struct 不含 model_name 字段（backend audit §5 描述错误），
-- 必须在同一 migration 补齐，避免 B3/B5 task 遗漏。
ALTER TABLE sop_chat_message
  ADD COLUMN model_name VARCHAR(100) NOT NULL DEFAULT '' AFTER thinking,
  ADD COLUMN duration_ms BIGINT NOT NULL DEFAULT 0 AFTER model_name;

-- 回滚:
-- ALTER TABLE sop_chat_message DROP COLUMN duration_ms;
-- ALTER TABLE sop_chat_message DROP COLUMN model_name;
```

### 4.3 GORM Model 改动（`internal/pkg/model/sop.go`）

```go
// SopNodeRun — 在 LatencyMs 字段之后增加
type SopNodeRun struct {
    gorm.Model
    // ... existing fields ...
    LatencyMs  int64  `gorm:"default:0" json:"latency_ms"`
    ModelName  string `gorm:"size:64;default:''" json:"model_name"`  // 新增
    // ...
}

// SopChatMsg — 在 Thinking 字段之后增加两个字段
type SopChatMsg struct {
    gorm.Model
    // ...
    Thinking   string `gorm:"type:longtext" json:"thinking,omitempty"`
    ModelName  string `gorm:"size:100;default:''" json:"model_name"` // 新增：代码当前无 model_name 字段（backend audit 错误描述）
    DurationMs int64  `gorm:"default:0" json:"duration_ms"`          // 新增
    Seq        int    `gorm:"default:0;index:idx_run_seq" json:"seq"`
    // ...
}

> **P0-1 修复说明**：之前 backend audit §5 称"消息表字段含 model_name"与代码不符。`SopChatMsg`（sop.go:156-170）实际无 `model_name` 字段，需 B3 task 新增。注意这会要求 B2 migration 对应补加 `model_name` 列（或新建一条 migration），详见 plan B2/B3 task。
```

注意：不要加 `json:"-"`，前端需要读这两个字段。

### 4.4 biz 层改动（`internal/numind/biz/sop/sop.go`）

#### 节点执行成功路径（~line 676-700）

```go
updateData := map[string]interface{}{
    "status":      model.SopStatusSucceeded,
    "output":      output,
    "thinking":    thinking,
    "latency_ms":  latency,
    "finished_at": nodeEndTime,
    "model_name":  node.ModelName, // 新增：从 sop_node 配置拷贝落库
}
```

`node` 是上下文里已经加载的 `*model.SopNode`（本函数的执行目标节点，含 ModelName 字段）。失败路径同样 copy 一份 `"model_name": node.ModelName` 到 updateData，保证失败记录也有模型信息。

#### Chat 消息保存路径（~line 1303-1330）

```go
// 在 ExecuteNodeStreamWithThinking 调用前记录开始时间
chatStart := time.Now()

// ... 现有流式调用 ...

assistantMsg := &model.SopChatMsg{
    RunID:          runID,
    ConversationID: conversationID,
    UserID:         userID,
    Role:           "assistant",
    Content:        answerBuf.String(),
    Thinking:       thinking,
    DurationMs:     time.Since(chatStart).Milliseconds(), // 新增
    Seq:            maxSeq + 2,
}
```

### 4.5 DTO / Controller 回传确认

- `SopNodeRun` 已经直接 json 序列化（非 DTO 中转），加 `model_name` 字段后前端自动可读。但 `PromptTokens/CompletionTokens/TotalTokens` 全部标注 `json:"-"`（sop.go:93-97），前端读不到 token 数据。
- **`CompletedNodeInfo`（`pkg/api/numind/v1/sop.go:191-203`，即 `/runs/:id/status` 响应中的 `completed_nodes[]` 元素）当前字段包含：NodeRunID / NodeID / NodeName / Sort / Input / Output / Thinking / FromBookmark / BookmarkID / IsAccessible，缺失 `LatencyMs / ModelName / TotalTokens` 三个字段。B5 task 需新增：**
  ```go
  type CompletedNodeInfo struct {
      // ... 现有字段 ...
      LatencyMs   int64  `json:"latency_ms"`   // 新增
      ModelName   string `json:"model_name"`   // 新增（B3 加到 model 层后 B5 透出）
      TotalTokens int    `json:"total_tokens"` // 新增（绕过 model 层 json:"-"，由 controller 显式 mapping）
  }
  ```
- `RunChatMessageItem`（`pkg/api/numind/v1/sop.go:319-330`）当前字段：ID / Role / Content / Thinking / CreatedAt / PromptTokens / CompletionTokens / TotalTokens / ReasoningTokens / EstimatedPromptTokens。**实际缺失 `ModelName` 和 `DurationMs` 两个字段**，B5 需新增：
  ```go
  type RunChatMessageItem struct {
      // ... 现有字段 ...
      ModelName  string `json:"model_name"`  // 新增
      DurationMs int64  `json:"duration_ms"` // 新增
  }
  ```
- Controller 层（`controller/v1/sop/sop.go` 的 chat messages list 构造函数 + status detail 构造函数）必须在 DTO mapping 时显式赋值 `TotalTokens` / `ModelName` / `LatencyMs` / `DurationMs`（token 字段因 model 层 json:"-" 不能靠自动序列化）。不在 controller 写业务逻辑，只做字段映射。

### 4.6 不动的东西

- `router.go` / `admin_router.go`：所有需要的 endpoint (`saveBookmark` / `removeBookmark` / `listBookmarks` / `applyBookmark` / `createRun`) 都已注册
- `store` 层接口：migration 加字段后 GORM 自动 pick up，不用改 store
- 业务逻辑：权限 / 配额 / 积分扣减 / SSE 事件协议 / draft 机制全部不动
- `config_*.yaml`：无新增配置项

---

## 5. 前端实施细节

### 5.1 设计 token

**方针**：因为本页面的视觉基调与其他页面明显不同（全白 / 固定布局 / 品牌绿），使用 **scoped CSS 变量** 放在 `SOPRunView.vue` 的 `<style scoped>` 根 `.sop-run-view-v2` 上，不覆盖全局 `:root`。

从 mockup 01/02 提取清单：

| Token | 值 | 用途 |
|---|---|---|
| `--bg` | `#FFFFFF` | 页面背景 |
| `--surface` | `#FFFFFF` | 所有 card / header / nav |
| `--surface-hover` | `#F4F5F8` | hover 态 |
| `--text` | `#1A1D26` | 主文字 |
| `--text-secondary` | `#5F6577` | 描述 |
| `--text-muted` | `#8B90A0` | meta 小字 |
| `--accent` | `hsl(160, 75%, 44%)` | 品牌绿 |
| `--accent-hover` | `hsl(160, 75%, 38%)` | — |
| `--accent-soft` | `hsl(160, 60%, 93%)` | — |
| `--accent-light` | `hsl(160, 70%, 68%)` | pending-return 虚线 |
| `--accent-ultra-soft` | `hsl(160, 60%, 95%)` | active 背景 |
| `--primary` | `hsl(160, 72%, 40%)` | 按钮主色 |
| `--primary-hover` | `hsl(160, 72%, 34%)` | — |
| `--primary-foreground` | `#FFFFFF` | 按钮文字 |
| `--border` | `#E2E4EA` | 普通 border |
| `--border-light` | `#EEEFF3` | card shell border |
| `--divider` | `#F0F1F5` | 细分隔 |
| `--space-xs..4xl` | `4/8/12/16/24/32/40/48` | 间距阶梯 |
| `--radius-sm/md/lg/xl/pill` | `6/12/16/20/999` | 圆角 |
| `--shadow-sm/md/lg/focus` | 见 mockup | 阴影 |
| `--font-sans` | 系统栈 | 所有文字（含 markdown body） |
| `--font-mono` | JetBrains Mono / SF Mono / Menlo | meta / step status |
| `--transition-fast/base` | `150ms/250ms` | — |

**全局 DESIGN.md 对齐**：S4 实施时需要确认这些值在根目录 `DESIGN.md` 中是否有等价 token；若有则在注释中引用（`/* sop-scoped: --primary ≈ DESIGN --color-accent-green-500 */`），若无则 scope 内硬编码并在 S3 plan 中排一个"DESIGN.md 同步"task 推荐但不强制。

**硬规则**（S4 reviewer 必须检查）：CSS 中**不出现** hex 字面量或 `hsl(...)` 字面量（除在 `:root`/scope 根上）；间距值必须从 `--space-*` 挑，不允许 `padding: 14px` 这种非阶梯值。

### 5.2 组件改造清单

> 每个条目 ≤ 30 行。props / emits 是 final spec，S4 不能随意变。

#### TopBar.vue（新建）
- **路径**：`src/views/sop/components/TopBar.vue`
- **职责**：slim 56px 顶栏 `[← 返回首页] | [模板名] | [历史 icon]`
- **props**：`{ templateName: string }`
- **emits**：`back`（点返回按钮） / `open-history`（点历史 icon）
- **CSS**：对齐 mockup `.header` / `.header__back` / `.header__divider` / `.header__title` / `.icon-btn`
- **mockup 引用**：01-active-and-history.html 行 717-730

#### StepNav.vue（新建）
- **路径**：`src/views/sop/components/StepNav.vue`
- **职责**：左 264px vertical nav，两个分组（主流程 / 追问），渲染 StepNavItem 列表
- **props**：`{ nodes: SopNodePublic[], currentStep: number, viewingStep: number, completedIds: Set<number>, accessibility: Record<number, boolean>, trailingChatEnabled: boolean, streamingNodeId: number | null }`
- **emits**：`navigate(step: number)` — 用户点击某可访问步骤
- **内部逻辑**：根据 props 计算每个 item 的 `state`，传给 StepNavItem
- **mockup 引用**：01 行 733-785, 02 行 930-980

#### StepNavItem.vue（新建）
- **路径**：`src/views/sop/components/StepNavItem.vue`
- **职责**：单条 step 展示
- **props**：`{ index: number, node: SopNodePublic | null /* trailing-chat 时 null */, state: 'active'|'done'|'viewing'|'pending-return'|'disabled', isTrailingChat?: boolean, statusLine: string /* "已完成 · 7.4s" 或 "等待输入" 等 */, isLive?: boolean }`
- **emits**：`click`
- **CSS**：`.step` + `.step--{state}` + `.step__dot` + `.step__body` + `.step__status`
- **mockup 引用**：01 行 737-784

#### StepCanvas.vue（新建）
- **路径**：`src/views/sop/components/StepCanvas.vue`
- **职责**：主区路由器。根据 store.viewingStepStatus 渲染 SopStepView / TrailingChat
- **props**：无（直接用 store）
- **CSS**：`.main + .canvas`
- **mockup 引用**：01 行 787-821

#### SopStepView.vue（新建）
- **路径**：`src/views/sop/components/SopStepView.vue`
- **职责**：单个 SOP 节点视图组合：HistoryViewStrip（可选）+ step header + 根据状态组合 InputCard/OutputCard/OutputEmpty/ActionRow
- **props**：`{ node: SopNodePublic, status: ViewingStepStatus }`
- **内部**：从 store 读取 nodeRun + streaming state，传给子组件
- **mockup 引用**：01 A/B, 02 C/D/E

#### InputCard.vue（新建）
- **路径**：`src/views/sop/components/InputCard.vue`
- **职责**：封装 `StepInput`（保留其 script），加 mockup `.card` 样式 + toolbar
- **props**：`{ node: SopNodePublic, runId: number | null, ensureRun: () => Promise<number | null>, label: string, hint: string, showExecute: boolean, isExecuting: boolean }`
- **emits**：`execute` / `stop` / `error(msg)`
- **v-model**：input text
- **CSS**：`.card`、`textarea.input`、`.toolbar`、`.upload`、`.btn--primary`
- **mockup 引用**：02 行 989-1030

#### OutputCard.vue（新建）
- **路径**：`src/views/sop/components/OutputCard.vue`
- **职责**：封装 markdown + meta footer 的 AI 输出卡；支持 3 态（streaming / read-only / empty-skip）
- **props**：`{ content: string, thinking: string, streaming: boolean, nodeRun: SopNodeRun | null, hasBookmark: boolean, hasOutput: boolean, streamingTokens?: number }` —— ⭐ button 使用 `v-if="hasOutput"` 控制显隐（与 spec §3.2 state A 隐藏条件对齐）
- **emits**：`toggle-bookmark` / `copy` / `regenerate` / `stop`
- **内部**：streaming 时 head 显示 live-dot + 停止按钮；read-only 时显示 ⭐ + 复制 + regenerate
- **CSS**：`.output` / `.output__head` / `.output__body` / `.output__foot` / `.tiny-btn`
- **mockup 引用**：01 行 914-968（B 态），02 行相应 D/E 态

#### OutputEmpty.vue（新建）
- **路径**：`src/views/sop/components/OutputEmpty.vue`
- **职责**：虚线边框占位（纯视觉）
- **props**：`{ hint?: string }`
- **mockup 引用**：`.output-empty`

#### MetaFooter.vue（新建）
- **路径**：`src/views/sop/components/MetaFooter.vue`
- **职责**：mono 小字 meta 行，顺序 `[clock 耗时] · [cpu 模型] · [coin tokens] · [完成时间]`
- **props**：`{ latencyMs?: number, modelName?: string, totalTokens?: number, finishedAt?: string }` —— `totalTokens` 来自 nodeRun DTO（B5 task 透出），缺失或为 0 时 token 段不渲染
- **CSS**：`.output__foot` 或 `.msg__meta` 变体
- **防御**：缺字段时整行不渲染；后端 model_name 为 `""` 时显示"未知模型"

#### ActionRow.vue（新建）
- **路径**：`src/views/sop/components/ActionRow.vue`
- **职责**：主区底部的主要操作按钮行
- **props**：`{ primary: { label: string, icon?: string, disabled?: boolean }, secondary?: { label: string, icon?: string } | null }`
- **emits**：`primary` / `secondary`
- **mockup 引用**：01 行 971-976, 02 E 态

#### HistoryViewStrip.vue（新建）
- **路径**：`src/views/sop/components/HistoryViewStrip.vue`
- **职责**：顶部 info strip："正在查看历史步骤 · 输入不可修改"；右侧 CTA "返回当前步骤"
- **props**：`{ targetStep: number, targetName: string }`
- **emits**：`return`
- **CSS**：scope 内新建，border-bottom 分隔；不在 mockup 01 有直接对应（但 proposal D5 要求）

#### TrailingChat.vue（新建）
- **路径**：`src/views/sop/components/TrailingChat.vue`
- **职责**：整个主区的聊天区（headless，无 step header）；包括 history + composer
- **props**：`{ runId: number, conversationId: string }`
- **emits**：`error(msg)`
- **内部**：沿用现有 `useSSEStream` 调 chat/stream endpoint；消息列表直接从 `store.chatMessages`（新加的 state）或 `api.listRunChatMessages` 读
- **CSS**：`.chat` / `.chat__history` / `.chat__composer`
- **mockup 引用**：02 F 态 `.chat`

#### ChatBubble.vue（改）
- **职责**：用户气泡 / AI 气泡
- **props**：加 `{ meta?: SopChatMessageMeta }` — 非空时在 AI 气泡下方贴 MetaFooter
- **CSS**：`.msg--user .bubble` / `.msg--ai .bubble`

#### ChatComposer.vue（新建）
- **路径**：`src/views/sop/components/ChatComposer.vue`
- **职责**：sticky 底部 textarea + 发送按钮 / 停止按钮
- **props**：`{ disabled: boolean, streaming: boolean }`
- **emits**：`send(text)` / `stop`

#### StepInput.vue（改）
- 保留当前的 `compose()` 方法、文件上传 composable、OCR 上传逻辑
- 去掉 label + card wrapper，只剩 `<textarea>` + 隐式 `<input type="file">`
- 由 InputCard 提供外层 card 样式

#### StepOutput.vue（改）
- 保留 markdown 渲染 + 思维链渲染 + scroll follow
- 去掉 card wrapper，只暴露 `<div class="md">...</div>`
- 由 OutputCard 提供外层 chrome + meta + tiny buttons

### 5.3 SSE 流式集成

InputCard 的"执行"按钮 click → `emit('execute')` → SopStepView 调用 `store.setStreamingState(...)` → `useSSEStream.streamPost(...)`（保留现有路径）→ `onMessage` 累积到 store → OutputCard 响应式读 `store.streamingContent` 渲染。

OutputCard 的"停止生成"按钮 click → `emit('stop')` → SopStepView 调用 `useSSEStream.abort()` → store 保留已收到的 partial content → 不标 markNodeComplete → UI 回到 state A（但 OutputEmpty 替换为 "已停止 · 保留片段" card，可选）。

> **S3 open question Q6**：停止后"保留片段 + 不入库"还是"提示用户丢弃"。本 spec 倾向**保留片段只在内存，不入库**（后端 sse handler 原本就不保证 partial flush 一致性）。

### 5.4 Bookmark UI 集成

- **位置**：OutputCard head 右侧的 tiny-btn `.tiny-btn--star`
- **态**：未收藏（线框星 + 文字"收藏"）/ 已收藏（填充星 + 文字"已收藏"+ accent 色）
- **交互**：
  - 未收藏点击 → `saveBookmark({ run_id, node_id })` → 成功 toast → hasBookmark=true
  - 已收藏点击 → **弹 ConfirmModal**（ui-ux.md 硬规则 4：销毁性操作必须确认）→ "将移除此节点的书签"→ 确认后 `removeBookmark(id)` → hasBookmark=false
- **bookmark id 本地映射**：`useBookmarks.getBookmarksForNode(nodeId)[0]?.id` 就能拿
- **⭐ button 隐藏条件（与 §3.2 对齐）**：本节点 `nodeRun` 不存在或 `nodeRun.output` 为空时隐藏（通过 OutputCard props `hasOutput: boolean` 控制，`v-if="hasOutput"`）。State A / C 下该条件为真 → 隐藏；State B / D-done / E 下必有 output → 显示

### 5.5 重新生成 UI 集成

- **位置**：ActionRow 或 OutputCard head 右侧（次 CTA，ghost 样式）
- **文案**：mockup 01 无直接显示（E 态才有），语义"用同样输入重跑"
- **交互**：
  - **必弹 ConfirmModal**（覆盖旧 output = 销毁性）：文案 `"重新生成会抹除当前 AI 输出，是否继续？"`
  - 如果 `isInputDirty && hasBookmark`：文案 `"您修改了输入内容，重新生成会删除此节点的书签，是否继续？"`（沿用现有 `showRegenConfirm` 逻辑）
  - 确认后 → `store.markNodeIncomplete(nodeId)` → `executeCurrentNode()`

> 注：proposal §3.2 D6 说"不弹确认对话框"。但 ui-ux.md 硬规则 4 明确销毁性操作必须弹。此处 spec **遵循硬规则**，S3 gate reviewer 若认为不需要可调整，但默认弹。

### 5.6 停止生成 UI 集成

- **位置**：streaming 状态下，OutputCard head 右侧替换 tiny buttons 为 `停止生成` button（ghost + i-stop icon）
- **交互**：点击 → `useSSEStream.abort()`（内部 `EventSource.close()` 或 `AbortController.abort()`）
- **后端**：继续跑完但前端忽略（D11）
- **保留的部分输出**：`store.streamingContent` 的内容保留显示在 body 内，但不入 `nodeRuns`

### 5.7 createRun auto_apply_bookmarks

在 `src/api/sop.ts` 的 `createRun` 默认 payload 加 `auto_apply_bookmarks: true`。`useDraftLifecycle.lazyCreateRun` 的调用点**不传** bookmarks 参数即可享受默认行为。

前端无 UI 暴露此 flag。用户感知："打开 SOP 时已完成步骤神奇地填好了"。

`createRun` 响应的 `auto_applied_count` 若 > 0，在页面 onMounted 完成后 **toast 成功提示** `"已自动应用 N 个书签"`（通过 notifications store）。

---

## 6. 测试策略

### 6.1 单元测试（Vitest）

保留：
- `components/__tests__/StepperPanel.spec.ts` — **删除**（StepperPanel 废弃）
- `components/__tests__/StepInput.spec.ts` — **保留**，但更新 snapshot（template 变了）
- `components/__tests__/StepOutput.spec.ts` — **保留**
- `composables/__tests__/*` — 全部保留不动
- `stores/__tests__/sopRun.spec.ts`（若有）— **加新用例** 覆盖 `viewingStep` / `viewingStepStatus` / `returnToCurrentTask`

新增：
- `components/__tests__/StepNav.spec.ts` — 6 种 state 快照测试，点击触发 navigate emit
- `components/__tests__/OutputCard.spec.ts` — 3 态（streaming / done / empty）渲染 + bookmark toggle 事件
- `components/__tests__/MetaFooter.spec.ts` — 缺字段时不渲染 / 全字段时按顺序渲染

### 6.2 E2E 测试（Playwright）

现有 `e2e/sop-*.spec.ts`（若有 11 条路径，由前置 feature 产出）需要**更新 selector**，因为 DOM 结构变了：
- 原 `.sop-stepper .step-item` → `.sop-nav-v2 .step`
- 原 `.sop-step-header` → 保留（SopStepView 内部结构相似）
- 原 `ToolbarActions` 按钮 → ActionRow + OutputCard head

**新增关键路径**：
1. **bookmark save / remove E2E**：执行 step 1 → 收藏 → 刷新页面 → 新 run → 自动应用 bookmark → 验证 output 一致
2. **view history E2E**：执行 step 1 → step 2 active → 点 step 1 nav → HistoryViewStrip 出现 → 点返回 → viewingStep 回到 2
3. **stop generation E2E**：点执行 → 看到 streaming → 点停止 → button 变回"执行" → 已生成内容保留在 output body

### 6.3 视觉回归

gstack `/qa` 验证 6 个状态截图，逐个与 mockup `.html` 做视觉比对：
- State A → navigate to step 2 (已完成 step 1)
- State B → click step 1 nav from State A
- State C → first entry no run
- State D → click execute，在 streaming 中截图
- State E → executing 完成后
- State F → trailing chat 有至少一条消息

每个状态生成截图并归档到 `proposals/sop-runtime-visual-redesign-screenshots/`（S5 阶段）。

---

## 7. 风险与缓解

| # | 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|---|
| R1 | 长内容（5000+ 字 markdown）溢出 OutputCard body | 中 | 中 | `.output__body { max-height: 620px; overflow-y: auto }` + S5 用真实长样本 Playwright 截图测 |
| R2 | 现有 E2E 因 DOM 结构变更全 fail | 100% | 中 | S3 plan 单列一个 "E2E selector 迁移" task；若工作量大则重写 |
| R3 | `viewingStep` vs `currentStep` 双指针语义用户/开发者混淆 | 高 | 中 | store 侧单元测试覆盖所有转换；spec §3.3 明确 rule（viewingStep ≤ currentStep） |
| R4 | 老节点 `description` 为 NULL 导致 step header 空白 | 100% | 低 | SopStepView 内 `v-if="node.description"` graceful fallback |
| R5 | ConfirmModal 对"重新生成"过度打扰 | 中 | 低 | 非 dirty+bookmark 场景可改为 inline toast undo；S3 open question |
| R6 | 停止生成后 partial content 与下一次执行交互 | 中 | 中 | 明确规则：partial 只展示不入 nodeRuns；下次执行直接清空 streaming state 覆盖 |
| R7 | 后端 model_name 迁移后老数据全为空字符串 | 100% | 低 | MetaFooter 收到 empty string 时整段不渲染（不显示"未知模型"误导） |
| R8 | CSS scoped 变量污染全局（:root 覆盖） | 低 | 高 | `<style scoped>` 内变量定义在 `.sop-run-view-v2` 根 class 上，不用 `:root` |
| R9 | 左 nav 节点 > 10 时溢出 | 中 | 低 | `.nav { overflow-y: auto }` + 最小高度 flex-1；S3 open question Q4 |
| R10 | trailing chat 消息数量 > 100 渲染性能 | 低 | 中 | 不做虚拟滚动；monitor + follow-up |

---

## 8. Open Questions（待 S3 plan 阶段决策）

- **Q1**：TrailingChat 是新建还是复用 sales 模块 `ChatArea`？
  - 复用成本：需把 ChatArea 拆 headless 变体，附件能力去掉
  - 新建成本：~200 行新代码 + 样式
  - **倾向新建**（sales ChatArea 耦合 salesRag 太重）
- **Q2**：老节点 `description` NULL fallback 文案
  - (a) 不渲染描述行（默认）
  - (b) 渲染灰色占位 "暂无描述"
  - **倾向 (a)**
- **Q3（CLOSED · 2026-04-11）**：ConfirmModal 是否弹"重新生成"？
  - proposal D6 说不弹，ui-ux.md 硬规则 4 说销毁性必弹
  - **决策：弹 ConfirmModal**（遵循 ui-ux.md 硬规则 4，销毁性操作必须确认）
- **Q4**：重新生成文案确认词
  - "重新生成会抹除当前结果，是否继续？"（通用）
  - "重新生成会用相同输入重跑，抹除当前输出"（更明确）
  - S3 确定
- **Q5**：trailing chat 每条 AI 消息 meta 行放在气泡下方还是气泡内？
  - mockup 行 767-774 是气泡外下方 `.msg__meta`
  - **采用 mockup 方案**
- **Q6**：停止生成保留片段的语义
  - 内存保留 / 下次执行清空（spec §5.3 倾向）
  - 或 "已停止 · 部分结果" card 提示用户
  - S3 决定
- **Q7**：StepNav 主流程 + 追问两组间距 / 分组 label 样式（mockup 已给，但需要确认是否 DESIGN.md 有等价 token）
- **Q8**：主区 max-width 是否继续按 mockup 的 980px 硬限制？或依 viewport 自适应？
  - 超宽屏用户体验可能堆积
  - **倾向 980 固定**，与 mockup 一致
- **Q9**：E2E 策略 — 修 selector vs 重写？（见 R2 + Rule 10 回归保护诚实声明）
  - 高风险业务（bookmark 保存 / createRun auto-apply）必写 Playwright
  - 纯视觉态可用 gstack /qa 一次性截图验证
- **Q10**：前端 `auto_applied_count > 0` 的 toast 位置 / 文案

---

## 9. 实施依赖顺序

S3 plan 的 task 应按此依赖图排列：

```
[backend]                                    [frontend]
  │                                              │
  ├─ B1 migration sop_node_run                   │
  ├─ B2 migration sop_chat_message               │
  ├─ B3 model 加字段                             │
  ├─ B4 biz 写入 model_name/duration_ms          │
  ├─ B5 DTO / response 透出                      │
  │                                              │
  └─ (deploy dev)  ◄─────────── depends ───────  F 全体 task
                                                 │
                                   ┌──  F0 scoped token + style 基础
                                   ├──  F1 store: viewingStep + getters
                                   ├──  F2 api: saveBookmark / removeBookmark + createRun 默认参数
                                   ├──  F3 TopBar + StepNav + StepNavItem（导航骨架）
                                   ├──  F4 StepCanvas + SopStepView 路由器
                                   ├──  F5 InputCard（封装 StepInput）
                                   ├──  F6 OutputCard + OutputEmpty + MetaFooter（封装 StepOutput）
                                   ├──  F7 ActionRow + HistoryViewStrip
                                   ├──  F8 bookmark UI 集成（⭐ toggle + confirm）
                                   ├──  F9 stop generation UI
                                   ├──  F10 TrailingChat + ChatBubble + ChatComposer 改造
                                   ├──  F11 SOPRunView 主容器重写 + initialize 改造
                                   ├──  F12 单元测试更新 + 新增
                                   ├──  F13 E2E selector 迁移 + 新增 3 条关键路径
                                   └──  F14 视觉回归 gstack /qa 6 态截图
```

**关键 deploy gate**：B1-B5 必须先 merge 到 develop 并部署到 dev 环境，F 系列才能在 dev API 上验证 `model_name` / `duration_ms` 字段。否则前端会一直读到空字符串，MetaFooter 永远不渲染，无法验收。

**跨仓库原子性**：B 系列和 F 系列独立 commit，但 manifest 必须同时记录；S5 gstack /qa 验证依赖后端先上线。

---

## 10. Spec 验证清单（S3 gate reviewer 用）

- [ ] 9 个 sections 完整
- [ ] 6 个 mockup 状态全部在 §3.2 状态机表格覆盖
- [ ] §3.1 每个新增组件明确了对应 mockup 的行号或 state 块
- [ ] §4 每个后端改动都给了具体 SQL / Go 代码片段
- [ ] §5.1 的 token 清单与 mockup 一一对齐
- [ ] §6 测试策略覆盖单元 + E2E + 视觉回归
- [ ] §8 open questions 至少 10 条，给 S3 留决策空间
- [ ] §9 实施依赖图明确 backend → frontend 顺序
- [ ] 没有引入 mockup 没有的 UI 元素
- [ ] 没有使用后端没有的字段（model_name / duration_ms 都在本 feature 内补齐）
- [ ] 所有销毁性操作走 ConfirmModal（ui-ux.md 硬规则 4）
- [ ] 不使用外部 UI 框架（ui-ux.md 硬规则 5）
- [ ] 前端异步视图有 loading / empty / error / success 4 态（ui-ux.md 硬规则 2，由 EmptyStateCard 覆盖）

---

## 附录 A — 组件交互矩阵（SOPRunView 主容器协调）

> S4 实施 SOPRunView 主容器重写时（task F11），参照此矩阵。

| 用户操作 | 触发点 | 调用链 | store 变化 | UI 响应 |
|---|---|---|---|---|
| 打开页面（URL 带 runId） | onMounted | loadTemplate → loadRun → bookmarks.load → navigation.restore | template/nodes/nodeRuns/completedIds 填充；currentStep = next pending；viewingStep = currentStep | 渲染对应态（A/B/E） |
| 打开页面（无 runId）| onMounted | loadTemplate → enterDraftMode → bookmarks.load | currentRun=null; nextNodeId=nodes[0].id; currentStep=1; viewingStep=1 | 状态 C |
| 点 StepNav 已完成步骤 | StepNav @navigate | navigation.canAccessStep → store.setViewingStep | viewingStep = 目标 step | 状态 B + HistoryViewStrip |
| 点 StepNav 当前任务 | StepNav @navigate | store.setViewingStep(currentStep) | viewingStep = currentStep | 状态 A |
| 点 HistoryViewStrip 返回 | strip @return | store.returnToCurrentTask | viewingStep = currentStep | 状态 A |
| 填 input + 点执行 | InputCard @execute | ensureRun → executeCurrentNode → useSSEStream.streamPost | setStreamingState + 流式 append | 状态 D |
| 点停止生成 | OutputCard @stop | useSSEStream.abort | 保留 streamingContent（不入 nodeRuns） | 回到状态 A（保留片段展示） |
| 执行成功 onDone | SSE onDone | setNodeRun + markNodeComplete + advanceCurrentStep | currentStep++; viewingStep 保持在完成的那步 | 状态 E |
| 点下一步 | ActionRow @primary | navigation.setActiveStep(currentStep+1) | currentStep 变 + viewingStep 同步 | 状态 A 或 F |
| 点 ⭐ 未收藏 | OutputCard @toggle-bookmark | saveBookmark → bookmarks.loadBookmarks | bookmarks 列表刷新 | ⭐ 变填充态 |
| 点 ⭐ 已收藏 | OutputCard @toggle-bookmark | ConfirmModal → removeBookmark → reload | — | 确认后 ⭐ 变线框 |
| 点复制 | OutputCard @copy | navigator.clipboard.writeText | — | toast "已复制" |
| 点重新生成 | OutputCard @regenerate 或 ActionRow @secondary | ConfirmModal → markNodeIncomplete → executeCurrentNode | — | 状态 D 重来 |
| trailing chat 发消息 | ChatComposer @send | sseStream 调 /chat/stream | chatMessages 追加 | 状态 F 流式 |
| 点 topbar 历史 icon | TopBar @open-history | showHistory = true | — | HistoryModal 弹出 |

## 附录 B — StepNav 状态判定伪代码

```typescript
function computeStepState(
  index: number,            // 1-based
  isTrailingChat: boolean,
  currentStep: number,
  viewingStep: number,
  completedIds: Set<number>,
  accessibility: Record<number, boolean>,
  streamingNodeId: number | null,
  nodes: SopNodePublic[]
): StepState {
  const node = isTrailingChat ? null : nodes[index - 1]

  // disabled 优先级最高
  if (node && accessibility[node.id] === false) return 'disabled'
  if (!isTrailingChat && index > currentStep) return 'disabled'
  if (isTrailingChat && currentStep < nodes.length + 1) return 'disabled'

  // streaming = active + live（由 isLive prop 承载，state 仍是 active）
  if (node && streamingNodeId === node.id) return 'active'

  // viewing 历史
  if (viewingStep !== currentStep && index === viewingStep) {
    return node && completedIds.has(node.id) ? 'viewing' : 'active'
  }

  // pending-return: viewingStep != currentStep 时，currentStep 上的节点显示虚线
  if (viewingStep !== currentStep && index === currentStep) return 'pending-return'

  // done
  if (node && completedIds.has(node.id)) return 'done'

  // active 默认
  if (index === currentStep) return 'active'

  return 'disabled'
}
```

此函数由 StepNav 内部计算，不进 store（纯派生逻辑）。S4 实施时需写单元测试覆盖 10+ 种输入组合。

## 附录 C — 后端加字段后的前端类型同步 checklist

- [ ] `src/views/sop/types.ts` `SopNodeRun.model_name: string` 新增
- [ ] `src/views/sop/types.ts` `SopNodeRun.total_tokens?: number` 新增（B5 task 在 DTO 层透出）
- [ ] `src/api/sop.ts` `RunChatMessageItem.model_name: string` / `duration_ms: number` 新增（两字段 DTO 当前均不存在，B5 task 补）
- [ ] `src/api/sop.ts` `CompletedNodeInfo`（`/runs/:id/status` 响应中 `completed_nodes[]` 元素）补 `latency_ms: number` / `model_name: string` / `total_tokens: number` 三字段 —— **已关闭（P2-1）**：B5 task 透出 3 字段，详见 spec §4.5
- [ ] `MetaFooter.vue` props 与这些字段类型一致
- [ ] `store.setNodeRun` 调用点传入完整对象（streaming done 时从 SSE 的 done 事件 payload 读，或 fall back 为空字符串）

### onDone 后 model_name / latency_ms / total_tokens 拉取策略（P0-2 决策）

**已关闭**：采用 **方案 A — 前端 onDone 后重新拉取 `/runs/:id/status`**，不改后端 SSE done payload。

**具体实现**：
- SSE `done` 事件当前 payload 是 `{"status":"completed","message_id":...}`，不含 meta 字段（model_name / latency_ms / total_tokens）
- 前端 `onDone` 回调触发后 → 调用 `store.refreshNodeRun(nodeId)`（见 §3.3）
- `refreshNodeRun` 内部调用 `fetchRunStatusDetail(currentRun.id)` → 从响应的 `completed_nodes[]` 中找到 `node_id === nodeId` 的元素 → 取其 `model_name` / `latency_ms` / `total_tokens` → 合并更新 `store.nodeRuns[nodeId]`
- 这要求 B5 task 在 `CompletedNodeInfo` DTO 上透出这 3 个字段（见 §4.5）
- State B（从 URL 直接进入历史 run）路径同样走这个 API，类型一致

**为什么不改 SSE done payload**：后端 SSE 协议冻结（§1.2 out of scope），且改 done payload 需要改 executor + sseStream 多处，风险高于前端多拉一次 HTTP 的成本。

## 附录 D — S4 reviewer 硬性检查项

Code Quality reviewer（S4 每个 task 的第二道 review）必须检查：

1. **token 纯度**：`src/views/sop/**/*.vue` 的 `<style scoped>` 内不出现 hex 字面量 / `rgb()` / `hsl()` 字面量（除 scope 根的 `--*` 定义）
2. **间距阶梯**：padding / margin / gap 全部使用 `var(--space-*)`，不出现 `14px` / `22px` 等非阶梯值（mockup 内的非阶梯值要在落地时对齐到最近的阶梯）
3. **图标统一**：所有 icon 使用 `lucide-vue-next`；不使用 unicode `←` / emoji / inline SVG 字面量
4. **Props 类型**：每个新建组件有 `defineProps<T>()` 类型定义，不允许 `any`
5. **Emits 声明**：每个新建组件有 `defineEmits<T>()`
6. **composable 复用**：`useSSEStream` / `useFileUpload` / `useInputPersistence` / `useStepNavigation` / `useBookmarks` 复用，不重造轮子
7. **ConfirmModal 使用**：销毁性操作（删除书签 / 重新生成）必须弹 ConfirmModal
8. **4 态处理**：loading / empty / error / success 在 SOPRunView 主容器通过 EmptyStateCard 覆盖
9. **后端字段防御**：`MetaFooter` 在缺失字段时整段不渲染，不显示"undefined"
10. **CSS scope**：不污染 `:root`，变量定义在组件根 class 上
11. **viewingStep ≤ currentStep** 不变量在所有 store action 中保持
12. **E2E selector**：新增测试 ID (`data-testid="sop-nav-item"`, `data-testid="output-card"` 等) 方便 Playwright 定位

Spec Compliance reviewer（第一道 review）必须检查：

1. 组件树与 §3.1 一致（无未经协商的新增组件）
2. 状态机与 §3.2 表格一致（每个态渲染的子组件正确）
3. props / emits 与 §5.2 组件清单一致
4. 后端字段改动与 §4 一致，不扩大范围
5. mockup 像素级对齐：每个组件交付时附一张截图与 mockup 对应 state 块比对

---

*本 spec 是 `sop-runtime-visual-redesign` feature 的 S2 产出，基于 S0 requirement + S1 proposal + backend audit + 2 份 mockup 写成。S3 plan 应直接引用本 spec 的 §3.1 组件清单和 §9 依赖图作为 task 切分基准。*
