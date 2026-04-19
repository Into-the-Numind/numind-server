# SOP 运行页 Vue 3 完整重写 — 技术设计

## 概述

将 C 端 SOP 运行页从 7518 行 legacy vanilla JavaScript（`numind-web-v3/public/legacy/sop-legacy.js`）+ 1019 行 Vue hydration wrapper（`numind-web-v3/src/views/SOPView.vue`）完整重写为 Vue 3 Composition API + TypeScript 组件体系。

**核心原则**：数据库 = 唯一真相源，前端零硬编码。

**涉及仓库**：numind-server（DTO 改造 + 字段守卫）、numind-web-v3（页面完整重写）

**明确排除**：
- self-service-config 配置器本身（已完成功能，零修改）
- 视觉重设计（保持现有 + DESIGN.md token）
- 新 SOP 运行功能（仅做"等价重写 + 删硬编码 + 修安全漏洞"）
- numind-admin-web（不涉及）
- 多 tab 竞态防护（用户决策不处理，未来由 single-session-enforcement 功能消解）

**输入工件**：
- `numind-server/proposals/sop-runtime-vue-rewrite-proposal.md`
- `numind-server/requirements/sop-runtime-vue-rewrite.md`
- 实测 dev DB 数据（templateId=1, 2 含历史 LLM 凭证；templateId=3+ 走全局 fallback）

---

## §1 数据契约

### 1.1 设计原则

**两个 DTO，按使用场景区分：**

| DTO | 用于 | 隐藏字段 | 暴露字段 |
|---|---|---|---|
| `SopNodePublicDTO` | C 端运行时（GetTemplateNodes） | api_key, base_url, model_name, timeout_seconds, **prompt** | id, template_id, name, description, sort, status, created_at, updated_at |
| `SopNodeEditDTO` | B 端配置器（GET /v1/config/sop-templates/:id） | api_key, base_url, model_name, timeout_seconds | id, template_id, name, description, sort, status, **prompt**, created_at, updated_at |

**为什么 prompt 在 C 端隐藏，B 端可见：**
- C 端用户只需看到 step 的 name + description，不应看到驱动 LLM 行为的 prompt 模板（这是 B 端核心 IP）
- B 端配置器需要让创建者编辑 prompt（`SopTemplateEdit.vue:167` 当前就在 v-model 这个字段）
- 实测验证：legacy `sop-legacy.js` 全文 grep `api_key|base_url|model_name|node.prompt` 0 命中，C 端隐藏 prompt 零破坏

**显式排除字段**：`parent_id`、`is_root` 在当前用户运行路径 0 引用（实测 grep `biz/sop/sop.go` + `controller/v1/sop/sop.go`），不进 DTO 以减少前端噪音

### 1.2 SopNodePublicDTO Go 定义

```go
// internal/pkg/model/dto/sop.go（新建）
package dto

import "time"

type SopNodePublicDTO struct {
    ID          uint      `json:"id"`
    TemplateID  uint      `json:"template_id"`
    Name        string    `json:"name"`
    Description string    `json:"description"`  // 可能为空，前端必须优雅退化
    Sort        int       `json:"sort"`
    Status      string    `json:"status"`
    CreatedAt   time.Time `json:"created_at"`
    UpdatedAt   time.Time `json:"updated_at"`
}

func ToSopNodePublicDTO(node *model.SopNode) SopNodePublicDTO {
    return SopNodePublicDTO{
        ID:          node.ID,
        TemplateID:  node.TemplateID,
        Name:        node.Name,
        Description: node.Description,
        Sort:        node.Sort,
        Status:      node.Status,
        CreatedAt:   node.CreatedAt,
        UpdatedAt:   node.UpdatedAt,
    }
}
```

### 1.3 SopTemplatePublicDTO Go 定义

`GetTemplateNodes` 当前返回的 template 元信息不完整（只有 id/name/trailing_chat_enabled）。补完为：

```go
type SopTemplatePublicDTO struct {
    ID                  uint      `json:"id"`
    Name                string    `json:"name"`
    Description         string    `json:"description"`
    Status              string    `json:"status"`
    PublishStatus       string    `json:"publish_status"`
    TrailingChatEnabled bool      `json:"trailing_chat_enabled"`
    CreatedAt           time.Time `json:"created_at"`
    UpdatedAt           time.Time `json:"updated_at"`
}
```

**隐藏字段**：`prompt`（template 级别的预处理 prompt，只在后端使用）、`creator_user_id`（不暴露 B 端身份）

### 1.4 GetTemplateNodes 新返回结构

```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "template": {
      "id": 1,
      "name": "流量选题口播稿",
      "description": "借热点输观点，吸引精准成交粉",
      "status": "active",
      "publish_status": "published",
      "trailing_chat_enabled": true,
      "created_at": "2026-03-15T10:00:00+08:00",
      "updated_at": "2026-04-11T15:30:00+08:00"
    },
    "nodes": [
      {
        "id": 1,
        "template_id": 1,
        "name": "AI拆解产品",
        "description": null,
        "sort": 0,
        "status": "active",
        "created_at": "...",
        "updated_at": "..."
      }
    ],
    "total": 4
  }
}
```

**字段差异 vs 当前**：
- 顶层 `template_id`/`template_name`/`trailing_chat_enabled` → 移到 `template` 对象内，并扩展为完整 SopTemplatePublicDTO
- `nodes` 数组中的对象不再包含 `api_key`/`base_url`/`model_name`/`timeout_seconds`/`prompt`/`parent_id`/`is_root`

**这是 breaking change**。但实测 legacy 前端 0 消费这些字段，所以无运行时风险。

---

## §2 后端改动

### 2.1 改动文件清单

| 文件 | 改动 | 类型 |
|---|---|---|
| `internal/pkg/model/dto/sop.go` | 新建，定义 SopNodePublicDTO + SopTemplatePublicDTO + 转换函数 | 新建 |
| `internal/numind/controller/v1/sop/sop.go` | (a) `GetTemplateNodes` 改用 DTO 返回结构；(b) 清理 `~/Desktop/...` 调试日志（5+ 处，行 262/275/311/323/337） | 修改 |
| `internal/numind/controller/v1/config/sop.go` | `CreateNode` 添加字段白名单 binding，reject api_key/base_url/model_name/timeout_seconds（**UpdateNode 已有 updateNodeReq 白名单，无需改动，仅核验**） | 修改 |

### 2.2 字段守卫（CreateNode / UpdateNode）

```go
// controller/v1/config/sop.go CreateNode
func (c *SopConfigController) CreateNode(ctx *gin.Context) {
    var req struct {
        TemplateID  uint   `json:"template_id" binding:"required"`
        Name        string `json:"name" binding:"required"`
        Description string `json:"description"`
        Prompt      string `json:"prompt" binding:"required"`
        Sort        int    `json:"sort"`
    }
    if err := ctx.ShouldBindJSON(&req); err != nil {
        core.WriteResponse(ctx, errno.ErrBind.SetMessage(err.Error()), nil)
        return
    }
    // 注意：不绑定 base_url / model_name / api_key / timeout_seconds
    // 即使前端发送了，也被丢弃
    node := &model.SopNode{
        TemplateID:  req.TemplateID,
        Name:        req.Name,
        Description: req.Description,
        Prompt:      req.Prompt,
        Sort:        req.Sort,
        Status:      "active",
    }
    // ...
}
```

**UpdateNode 已有守卫**：实测 `controller/v1/config/sop.go:192-237` 已使用 `updateNodeReq` 白名单 `{Name, Description, Prompt, Sort}` 配合 `map[string]interface{}` 增量更新。无需改动，但 S4 task 必须**核验**此结构未被回退。

**历史数据不清理**：templateId=1, 2 的 sop_node 已有 base_url/model_name/api_key（admin 早期硬编码，配置器之前），保留不动 —— 后端 executor 会优先使用这些字段，fallback 才读 viper 配置。这是它们能正常运行的方式，删了反而会破坏。

### 2.3 GetTemplateNodes 实现

```go
// controller/v1/sop/sop.go
func (c *SopController) GetTemplateNodes(ctx *gin.Context) {
    templateIDStr := ctx.Param("id")
    templateID, err := strconv.ParseUint(templateIDStr, 10, 64)
    if err != nil {
        core.WriteResponse(ctx, errno.ErrBind.SetMessage("invalid template id"), nil)
        return
    }

    template, err := c.sopBiz.GetTemplate(ctx, uint(templateID))
    if err != nil {
        core.WriteResponse(ctx, errno.ErrTemplateNotFound, nil)
        return
    }

    nodes, err := c.sopBiz.ListNodesByTemplate(ctx, uint(templateID))
    if err != nil {
        core.WriteResponse(ctx, err, nil)
        return
    }

    // 转 DTO
    nodeDTOs := make([]dto.SopNodePublicDTO, 0, len(nodes))
    for _, n := range nodes {
        nodeDTOs = append(nodeDTOs, dto.ToSopNodePublicDTO(&n))
    }

    core.WriteResponse(ctx, nil, gin.H{
        "template": dto.ToSopTemplatePublicDTO(template),
        "nodes":    nodeDTOs,
        "total":    len(nodeDTOs),
    })
}
```

### 2.4 后端测试

- 单元测试：`internal/pkg/model/dto/sop_test.go` —— 验证转换函数不泄露任何敏感字段
- curl 实测：`curl -H "Authorization: Bearer $TOKEN" http://49.233.219.254:9091/v1/sop/templates/1/nodes | jq '.data.nodes[0]'` 必须 0 命中 `api_key|base_url|model_name|prompt`

---

## §3 前端架构

### 3.1 文件组织

```
numind-web-v3/src/views/sop/                    # 新建目录
├── SOPRunView.vue                              # 顶层路由组件（替换 SOPView.vue）
├── components/
│   ├── StepperPanel.vue                        # 步骤指示器
│   ├── StepContent.vue                         # 单个步骤内容容器
│   ├── StepInput.vue                           # 输入区（textarea + 文件上传）
│   ├── StepOutput.vue                          # 输出区（流式 Markdown + 思维链）
│   ├── ToolbarActions.vue                      # 工具栏（复制 / 重新生成 / 下一步）
│   ├── TrailingChatPanel.vue                   # 第 N+1 步聊天面板
│   ├── HistoryModal.vue                        # 历史记录弹窗
│   ├── ScrollFollowButton.vue                  # 跳回底部按钮
│   └── EmptyStateCard.vue                      # 空状态 / 错误状态
├── composables/
│   ├── useSOPRun.ts                            # 节点执行编排
│   ├── useSSEStream.ts                         # SSE 流解析
│   ├── useScrollFollow.ts                      # 自动滚动状态机
│   ├── useInputPersistence.ts                  # localStorage 持久化
│   ├── useDraftLifecycle.ts                    # Draft 创建/升级/Beacon 清理
│   ├── useFileUpload.ts                        # 文件上传 + OCR/PDF 处理
│   ├── useBookmarks.ts                         # 书签系统
│   └── useStepNavigation.ts                    # 步骤切换 + 权限检查
└── types.ts                                    # SopTemplate / SopNode / SopRun TypeScript 接口
```

**Pinia store**：`src/stores/sopRun.ts`（新建）—— 持有当前 run 的全局状态

> **现有 `src/stores/sop.ts` 处置策略（S2 已实测确认）**：该文件 273 行**全部是 legacy hydration 胶水代码** —— 注入 sop-legacy.css/js、设置 window.* 全局、调用 `__sopLegacyInit/Cleanup`。**0 行业务状态**。
>
> **决策：删除 sop.ts，新建 sopRun.ts**。删除 legacy 后该文件失去全部存在理由。S3 plan 中作为 task 22（删除 legacy 文件）的一部分。

**API 层**：`src/api/sop.ts`（已存在）—— 实测确认存在，本次重写在此扩展 SOP 运行时相关函数

**现有可复用组件清单（实测 src/components/common/）**：
- ✅ `InsufficientCreditsDialog.vue` —— 复用
- ✅ `AppButton.vue` / `AppInput.vue` —— 复用基础控件
- ✅ `ModelSelector.vue` —— 复用模型选择器
- ❌ `ConfirmModal` —— **不存在**，本次需新建（用于"重新生成将删除书签"等确认）
- ❌ `AppNotification` / 全局 toast —— **不存在**，本次需新建（或复用现有 store / message API）

> **§14 task 估算修订**：因为 ConfirmModal 和 AppNotification 不存在，必须 +2 个 task 新建这两个组件，或者用 Vue 原生 `window.confirm()` 和简单的内联 toast 代替。**S3 plan 阶段决定采用哪种方式**，spec 默认推荐"新建简洁版组件"以保证 UI 一致性

### 3.2 Pinia store 设计

```typescript
// src/stores/sopRun.ts
export const useSopRunStore = defineStore('sopRun', () => {
  // ===== 核心 state =====
  const template = ref<SopTemplatePublic | null>(null)
  const nodes = ref<SopNodePublic[]>([])
  const currentRun = ref<SopRun | null>(null)
  const conversationId = ref<string | null>(null)

  // ===== 节点执行状态 =====
  const nodeRuns = ref<Record<number, SopNodeRun>>({}) // nodeId → 最新执行记录
  const completedNodeIds = ref<Set<number>>(new Set())
  const nextNodeId = ref<number | null>(null)
  const nodeAccessibility = ref<Record<number, boolean>>({}) // nodeId → is_accessible

  // ===== UI state =====
  const currentStep = ref<number>(1) // 1-based
  const isDraftRun = computed(() => currentRun.value?.status === 'draft')
  // 注意：draft 是后端独立状态（model.SopStatusDraft），不是 pending+counted=false 的组合
  const trailingChatEnabled = computed(() => template.value?.trailing_chat_enabled ?? false)
  const totalSteps = computed(() => nodes.value.length + (trailingChatEnabled.value ? 1 : 0))
  const isOnTrailingChatStep = computed(() => currentStep.value === nodes.value.length + 1)

  // ===== Actions =====
  async function loadTemplate(templateId: number) { /* ... */ }
  async function loadOrCreateRun(templateId: number, runId?: number) { /* ... */ }
  async function executeNode(nodeId: number, input: string, files: File[]) { /* ... */ }
  async function setActiveStep(step: number) { /* ... */ }
  function reset() { /* 切换 run / 离开页面时清理 */ }

  return {
    template, nodes, currentRun, conversationId, nodeRuns, completedNodeIds,
    nextNodeId, nodeAccessibility, currentStep, isDraftRun, trailingChatEnabled,
    totalSteps, isOnTrailingChatStep,
    loadTemplate, loadOrCreateRun, executeNode, setActiveStep, reset,
  }
})
```

### 3.3 TypeScript 接口（types.ts）

```typescript
export interface SopTemplatePublic {
  id: number
  name: string
  description: string
  status: 'active' | 'inactive'
  publish_status: 'draft' | 'published' | 'offline'
  trailing_chat_enabled: boolean
  created_at: string
  updated_at: string
}

export interface SopNodePublic {
  id: number
  template_id: number
  name: string
  description: string  // 可能为空字符串
  sort: number
  status: 'active' | 'inactive'
  created_at: string
  updated_at: string
}

export type SopRunStatus = 'draft' | 'pending' | 'running' | 'succeeded' | 'failed'

export interface SopRun {
  id: number
  template_id: number
  user_id: number
  status: SopRunStatus
  conversation_id: string
  counted: boolean
  started_at: string | null
  finished_at: string | null
  created_at: string
}

export interface SopNodeRun {
  id: number
  run_id: number
  node_id: number
  status: 'pending' | 'running' | 'succeeded' | 'failed'
  input: string | null   // longtext，可能为 null
  output: string | null  // longtext，可能为 null
  thinking: string | null // longtext，可能为 null
  latency_ms: number
  is_accessible: boolean
  started_at: string | null
  finished_at: string | null
}

export interface ExecutedTemplate { /* GET /v1/sop/templates/executed */ }
export interface BookmarkItem { /* GET /v1/sop/templates/:id/bookmarks */ }
```

### 3.4 组件树

```
SOPRunView.vue
├── <TopBar>
│   ├── <BackHomeButton>
│   ├── <TemplateTitle>{{ store.template?.name }}</TemplateTitle>
│   └── <HistoryButton @click="showHistory = true">
├── <StepperPanel
│     :steps="store.nodes"
│     :trailing-chat-enabled="store.trailingChatEnabled"
│     :current-step="store.currentStep"
│     :completed-ids="store.completedNodeIds"
│     :accessibility="store.nodeAccessibility"
│     @navigate="store.setActiveStep" />
├── <main>
│   <StepContent v-if="!store.isOnTrailingChatStep" :node="currentNode">
│     <StepInput v-model:text="..." v-model:files="..." />
│     <StepOutput :stream-content="..." :thinking="..." />
│     <ToolbarActions :step="currentStep" />
│   </StepContent>
│   <TrailingChatPanel v-else />
│ </main>
├── <HistoryModal v-model="showHistory" />
└── <ScrollFollowButton v-if="scrollFollow.isInterrupted" @click="scrollFollow.resume" />
```

---

## §4 SSE 流契约（基于实测代码）

### 4.1 协议格式（实测自 controller/v1/sop/sop.go:908-956 + 2438-2480）

后端 `POST /v1/sop/runs/:id/nodes/:node_id/execute` 和 `POST /v1/sop/chat/stream` 都返回 `Content-Type: text/event-stream`，**实测的真实事件格式**：

#### 4.1.1 thinking 事件

```
event: thinking
data: "<JSON-encoded string>"\n\n
```

- 有 `event:` 行
- `data` 是 **JSON-encoded 字符串**（即 `json.Marshal(chunk)` 的结果），**不是 JSON 对象**
- 例如思考片段是 `"思考中..."`，则 data 字段是字面量 `"思考中..."`（含外层双引号）

#### 4.1.2 message 事件

```
data: "<JSON-encoded string>"\n\n
```

- **没有 `event:` 行**（这是关键易错点）—— 默认事件类型是 `message`
- data 同样是 JSON-encoded 字符串

#### 4.1.3 done 事件（被发送 1 次或 2 次）

```
event: done
data: {"status":"completed"}\n\n
```

或带 uploaded_file_ids 的版本（execute 端点的第二次 done）：

```
event: done
data: {"status":"completed","uploaded_file_ids":[1,2,3]}\n\n
```

- ExecuteNodeStream **会发送两次 done**：第一次在 biz 流回调里（`{"status":"completed"}`），第二次在控制器最后（带 uploaded_file_ids）—— 见 controller/v1/sop/sop.go:915-916 + 950-955
- ChatAfterRunStream 通常只发一次 done（除非 chunk 已携带数据）
- **前端 parser 必须幂等处理重复 done**：第二次 done 仅用于补 file IDs，不能重复触发"完成"动作（如清空 streamingMessage、新消息入队）
- **前端必须能解析 done 的 data 是 JSON 对象**

#### 4.1.4 error 事件

```
event: error
data: "<JSON-encoded error message string>"\n\n
```

- 是 JSON-encoded 字符串（如 `"余额不足"`），**不是** `{"code":xxx,"message":"..."}`
- 前端拿到后直接当作错误文案显示

#### 4.1.5 心跳行

```
:\n\n
```

每 15 秒一次，由 controller heartbeat goroutine 发送（行 854-878）。**前端 parser 必须忽略以 `:` 开头的行**（这是 SSE 标准注释行）。

### 4.2 useSSEStream composable（基于真实协议）

```typescript
// composables/useSSEStream.ts
export interface SSEStreamHandlers {
  onThinking?: (chunk: string) => void
  onMessage?: (chunk: string) => void
  onDone?: (meta: { status: string; uploaded_file_ids?: number[] }) => void
  onError?: (errorMessage: string) => void
}

interface SSEEvent {
  event: string  // 默认 "message"
  data: string
}

export function useSSEStream() {
  const abortController = ref<AbortController | null>(null)
  const doneFiredRef = ref(false)  // 幂等保护：done 只触发一次

  async function streamPost(url: string, init: RequestInit, handlers: SSEStreamHandlers) {
    abortController.value = new AbortController()
    doneFiredRef.value = false

    const response = await fetch(url, {
      ...init,
      signal: abortController.value.signal,
      headers: {
        Authorization: `Bearer ${getToken()}`,
        ...init.headers,
      },
    })
    if (!response.ok) {
      handlers.onError?.(`HTTP ${response.status}: ${response.statusText}`)
      return
    }

    const reader = response.body!.getReader()
    const decoder = new TextDecoder()
    let buffer = ''

    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })
      const events = buffer.split('\n\n')
      buffer = events.pop() ?? ''
      for (const block of events) {
        const evt = parseEventBlock(block)
        if (evt) dispatchEvent(evt, handlers)
      }
    }
    // flush 剩余 buffer
    if (buffer.trim()) {
      const evt = parseEventBlock(buffer)
      if (evt) dispatchEvent(evt, handlers)
    }
  }

  function parseEventBlock(block: string): SSEEvent | null {
    const trimmed = block.trim()
    // 心跳行：以 ":" 开头的注释行，整体忽略
    if (!trimmed || trimmed.startsWith(':')) return null

    let event = 'message'  // SSE 默认事件类型
    let data = ''
    for (const line of trimmed.split('\n')) {
      if (line.startsWith('event:')) {
        event = line.slice(6).trim()
      } else if (line.startsWith('data:')) {
        data = line.slice(5).trim()
      }
      // 其他行（id:, retry:）当前后端不发送，忽略
    }
    return { event, data }
  }

  function dispatchEvent(evt: SSEEvent, handlers: SSEStreamHandlers) {
    try {
      switch (evt.event) {
        case 'thinking': {
          // data 是 JSON-encoded 字符串，需 JSON.parse 还原
          const chunk = JSON.parse(evt.data) as string
          handlers.onThinking?.(chunk)
          break
        }
        case 'message': {
          const chunk = JSON.parse(evt.data) as string
          handlers.onMessage?.(chunk)
          break
        }
        case 'done': {
          // 幂等保护：done 可能被发送两次
          if (doneFiredRef.value) {
            // 第二次 done 仅用于补 uploaded_file_ids，不再触发完成动作
            // 但仍然 parse 一下，万一前端需要拿 file IDs（可选扩展点）
            return
          }
          doneFiredRef.value = true
          const meta = JSON.parse(evt.data) as { status: string; uploaded_file_ids?: number[] }
          handlers.onDone?.(meta)
          break
        }
        case 'error': {
          const errMsg = JSON.parse(evt.data) as string
          handlers.onError?.(errMsg)
          break
        }
        default:
          // 未知事件类型，忽略
      }
    } catch (e) {
      console.warn('SSE parse error', e, evt)
    }
  }

  function abort() { abortController.value?.abort() }

  return { streamPost, abort }
}
```

**关键点：**
- 心跳行 `:\n\n` 通过 `parseEventBlock` 的 `startsWith(':')` 过滤
- thinking/message/error 的 data 是 JSON-encoded **字符串**，需要 `JSON.parse` 还原（结果还是字符串）
- done 的 data 是 JSON **对象**，`JSON.parse` 后是 `{status, uploaded_file_ids?}`
- `doneFiredRef` 保证完成动作只触发一次，避免 ExecuteNodeStream 的双 done 问题

### 4.3 ExecuteNodeStream 调用方式（实测自 controller/v1/sop/sop.go:760-900）

```typescript
async function executeNode(runId: number, nodeId: number, text: string, files: File[]) {
  const formData = new FormData()
  formData.append('text', text)
  for (const f of files) {
    formData.append('files', f)
  }

  // model_key + thinking 是 query 参数（不是 body）
  const params = new URLSearchParams()
  if (modelStore.selectedModelKey) params.set('model_key', modelStore.selectedModelKey)
  if (modelStore.deepThinking) params.set('thinking', '1')
  const url = `/v1/sop/runs/${runId}/nodes/${nodeId}/execute?${params}`

  await sseStream.streamPost(url, { method: 'POST', body: formData }, {
    onThinking: (chunk) => store.appendThinking(nodeId, chunk),
    onMessage: (chunk) => store.appendOutput(nodeId, chunk),
    onDone: (meta) => store.markNodeComplete(nodeId, meta.uploaded_file_ids),
    onError: (msg) => store.handleError(nodeId, msg),
  })
}
```

**注意**：text 字段也支持 JSON body 调用方式（行 822-829），但 FormData 兼容文件上传场景，统一用 FormData。

### 4.4 ChatAfterRunStream 调用方式（实测自 controller/v1/sop/sop.go:2362-2380）

```typescript
async function sendChatMessage(runId: number, question: string, conversationId: string | null, regenerateMsgId?: number) {
  const body = {
    run_id: runId,
    conversation_id: conversationId ?? '',
    question,
    deep_thinking: modelStore.deepThinking,
    regenerate_msg_id: regenerateMsgId ?? 0,
  }
  await sseStream.streamPost('/v1/sop/chat/stream', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  }, { /* handlers */ })
}
```

**与 ExecuteNodeStream 的关键差异**：chat 是 **JSON body**（不是 FormData），无文件上传，无 query params。`deep_thinking` 在 body 里。

### 4.5 思维链渲染（保持不变）

`StepOutput.vue` 和 `ChatBubble.vue` 共用思维链显示逻辑：

```vue
<template>
  <div class="output-area">
    <div v-if="thinking" class="thinking-container" :class="{ collapsed: thinkingCollapsed }">
      <div class="thinking-header" @click="thinkingCollapsed = !thinkingCollapsed">
        🧠 思考过程 {{ thinkingCollapsed ? '▶' : '▼' }}
      </div>
      <div v-if="!thinkingCollapsed" class="thinking-content prose" v-html="thinkingHtml" />
    </div>
    <div class="message-content prose" v-html="messageHtml" ref="contentEl" />
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { renderMarkdown } from '@/utils/markdown'  // 封装 marked + DOMPurify

const props = defineProps<{ thinking: string; content: string }>()
const thinkingCollapsed = ref(false)
const contentEl = ref<HTMLElement>()

const thinkingHtml = computed(() => renderMarkdown(props.thinking))
const messageHtml = computed(() => renderMarkdown(props.content))

// 流式增量更新时触发滚动跟随
watch(() => props.content, () => {
  scrollFollow.checkAndScroll(contentEl.value)
})
</script>
```

**marked / highlight.js / DOMPurify 迁移到 npm**：从 CDN 改为 `npm install marked highlight.js dompurify`，封装在 `src/utils/markdown.ts`。

### 4.2 useSSEStream composable

```typescript
// composables/useSSEStream.ts
export interface SSEStreamHandlers {
  onThinking?: (chunk: string) => void
  onMessage?: (chunk: string) => void
  onDone?: (meta: { node_run_id: number; latency_ms: number }) => void
  onError?: (err: { code: number; message: string }) => void
}

export function useSSEStream() {
  const abortController = ref<AbortController | null>(null)

  async function streamPost(url: string, body: FormData, handlers: SSEStreamHandlers) {
    abortController.value = new AbortController()
    const response = await fetch(url, {
      method: 'POST',
      headers: { Authorization: `Bearer ${getToken()}` },
      body,
      signal: abortController.value.signal,
    })
    if (!response.ok) {
      handlers.onError?.({ code: response.status, message: await response.text() })
      return
    }
    const reader = response.body!.getReader()
    const decoder = new TextDecoder()
    let buffer = ''
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })
      const events = buffer.split('\n\n')
      buffer = events.pop() ?? ''
      for (const evt of events) {
        parseAndDispatch(evt, handlers)
      }
    }
    // flush 剩余 buffer
    if (buffer.trim()) parseAndDispatch(buffer, handlers)
  }

  function abort() { abortController.value?.abort() }

  return { streamPost, abort }
}
```

**等价复刻 legacy 的 `handleStreamingResponse`**（sop-legacy.js 行 3062 起）。关键点：
- buffer 分隔符 `\n\n`
- pop 出最后一个不完整 chunk 留到下次循环
- 流结束时 flush buffer 残留
- AbortController 支持组件卸载时取消

---

## §5 关键状态机

### 5.1 Draft 生命周期（基于实测后端状态机）

**关键事实**：后端有独立的 `SopStatusDraft = "draft"` 常量（model/sop.go:206），与 pending/running/succeeded/failed 并列。不是"pending + counted=false"的组合。

```
[未进入页面]
    │
    │ 用户点击 SOP 模板进入 /sop?templateId=X
    ↓
[前端纯本地 draft]
    │ - currentRun.value = null
    │ - 没有任何后端 run 记录
    │ - localStorage key: sop_input_draft_<templateId>_<inputId>
    │ - Pinia: isDraftRun = computed → false（因为 currentRun 为 null）
    │
    │ 首次点击"执行"按钮 / 加载书签时
    ↓
[Lazy Create Run（后端 status=draft）]
    │ - POST /v1/sop/runs { template_id, text }
    │ - 后端 CreateRun 创建一个 run，status = "draft"，counted = false
    │ - 成功 → 拿到 runId
    │ - URL 更新为 /sop?templateId=X&runId=Y
    │ - localStorage 迁移：sop_input_draft_<templateId>_<inputId> → sop_input_<runId>_<inputId>
    │ - Pinia: currentRun.status = "draft" → isDraftRun = true
    │
    │ 节点开始执行（POST /execute）
    ↓
[Running 状态]
    │ - 后端 run.status: draft → pending → running
    │ - 节点首次成功后：run.counted = true（biz/sop/sop.go:768-779）
    │ - Pinia: isDraftRun = false
    │
    │ 用户关闭浏览器 / 切换路由
    ↓
[Beacon 清理 — 仅当 status=draft 时生效]
    │ - 前端通过 navigator.sendBeacon('POST /v1/sop/runs/:id/draft?token=<jwt>')
    │ - 后端 DeleteDraftRun（biz/sop/sop.go:1758-1790）：
    │     - 检查 run.UserID == requestUserID（权限）
    │     - 检查 run.Status == "draft"（**这是唯一的保护条件**）
    │     - 不是 draft 直接报错返回，不删
    │ - draft 被删 → 关联 sop_node_run 也被删
    │
    └─→ [清理完成 / 已转 running 的 run 不动]
```

**关键差异 vs 之前版本：**
- "draft" 是后端独立状态字符串，不是组合判断
- `isDraftRun` 直接读 `currentRun.status === 'draft'`
- DeleteDraftRun 只看 status，不看 counted —— counted 是配额扣减用的标志，与 draft 清理无关
- 只要 run 的 status 已经从 draft 转走（pending/running/succeeded/failed），DeleteDraftRun 就不会动它

`useDraftLifecycle.ts` 职责：
- `enterDraftMode(templateId)`：纯前端 draft 状态，不创建后端记录
- `lazyCreateRun(text)`：调 `POST /v1/sop/runs`，后端创建 status=draft 的记录
- `migrateLocalStorageKeys(oldRunId, newRunId)`：把 input 持久化键从 draft_<tid> 改为 <runId>
- `cleanupDraft()`：在 onBeforeUnmount 调用，构造 `?token=<jwt>` query，用 `navigator.sendBeacon('POST /v1/sop/runs/:id/draft?token=...')` 异步触发清理

**Beacon token 传递方式**：因为 `sendBeacon` 不支持自定义 header，token 必须通过 query 参数传递。后端 `controller/v1/sop/bookmark.go` 的 DeleteDraftRun controller 已经支持 `?token=<jwt>` query 提取（实测确认）。

### 5.2 scrollFollowManager 状态机

```
                ┌──────────────┐
                │  Following   │ ← 默认状态：流式输出时自动滚到底部
                └──────┬───────┘
                       │
                       │ 用户向上滚动 / 移动端向下滑
                       ↓
                ┌──────────────┐
                │  Interrupted │ → 显示"跳回底部"按钮
                └──────┬───────┘
                       │
        ┌──────────────┼──────────────┐
        │              │              │
   用户点击按钮      用户向下     新一次执行节点
        │            滚到底部           │
        ↓              ↓              ↓
   resume()      自动 resume      auto resume
        │              │              │
        └──────────────┴──────────────┘
                       ↓
                  Following
```

**实现**：`useScrollFollow()` composable 注册一个流式输出元素的引用，监听 wheel / touchmove 事件判断方向，更新 `isInterrupted` ref。`StepOutput.vue` 在 watch content 变化时调用 `checkAndScroll()`，仅在 Following 状态下生效。

### 5.3 步骤可访问性

```typescript
function canAccessStep(stepIndex: number): boolean {
  // stepIndex 是 1-based
  const isTrailingChat = stepIndex === store.nodes.length + 1
  if (isTrailingChat) {
    return store.trailingChatEnabled && store.completedNodeIds.size === store.nodes.length
  }
  const node = store.nodes[stepIndex - 1]
  if (!node) return false
  if (store.completedNodeIds.has(node.id)) {
    return store.nodeAccessibility[node.id] !== false  // 默认 true，明确 false 才禁
  }
  return node.id === store.nextNodeId
}
```

---

## §6 文件上传设计

### 6.1 支持的文件类型

```typescript
const ACCEPT = '.pdf,.txt,.md,.docx,.doc,.rtf,.jpg,.jpeg,.png,.gif,.webp,.bmp,.svg'
const MAX_SIZE_MB = 20
```

### 6.2 处理流程（useFileUpload.ts）

```typescript
export function useFileUpload(targetTextareaText: Ref<string>) {
  const baseText = ref('')         // 用户手动输入部分
  const uploadResults = ref<Map<string, string>>(new Map()) // filename → 识别结果
  const uploading = ref(false)

  async function handleFiles(files: File[]) {
    uploading.value = true
    try {
      for (const file of files) {
        if (file.size > MAX_SIZE_MB * 1024 * 1024) {
          showToast(`${file.name} 超过 ${MAX_SIZE_MB}MB 限制`)
          continue
        }
        if (isImage(file)) {
          const text = await callVisionAPI(file)  // /v1/ali/vision/analyze
          uploadResults.value.set(file.name, text)
        } else if (isPDF(file)) {
          const text = await callPdfAPI(file)     // /v1/pdf/convert-to-text
          uploadResults.value.set(file.name, text)
        } else {
          // 文本类直接 read
          const text = await file.text()
          uploadResults.value.set(file.name, text)
        }
      }
      // 累积到 textarea：baseText + 所有上传结果
      targetTextareaText.value = composeText()
    } finally {
      uploading.value = false
    }
  }

  function composeText() {
    const parts = [baseText.value, ...uploadResults.value.values()].filter(Boolean)
    return parts.join('\n\n')
  }

  return { uploading, handleFiles, baseText, uploadResults }
}
```

**关键决策**：分离 `baseText`（用户手输）和 `uploadResults`（OCR/PDF 结果），避免后续上传覆盖手输内容。等价复刻 legacy 的 `textareaBaseText` / `textareaImageResults` Map（行 2956-2959）。

### 6.3 拖拽支持

`StepInput.vue` 监听 `dragenter/dragover/dragleave/drop` 事件，drop 时触发 `useFileUpload.handleFiles`。

---

## §7 历史记录与书签

### 7.1 历史记录弹窗

```typescript
// HistoryModal.vue
const runs = ref<ExecutedTemplate[]>([])
const loading = ref(false)

async function loadHistory() {
  loading.value = true
  try {
    const { data } = await getExecutedTemplates() // GET /v1/sop/templates/executed
    runs.value = data.list.filter(r => r.status !== 'pending' && r.status !== 'failed')
                         .sort((a, b) => +new Date(b.created_at) - +new Date(a.created_at))
  } finally {
    loading.value = false
  }
}

async function switchToRun(runId: number, templateId: number) {
  router.push({ path: '/sop', query: { templateId, runId } })
  emit('close')
}

async function deleteRun(runId: number) {
  if (!await confirm('确定删除此运行记录？')) return
  await deleteRunAPI(runId) // DELETE /v1/sop/runs/:id
  await loadHistory()
}
```

### 7.2 书签系统

- 加载：`GET /v1/sop/templates/:id/bookmarks` 在页面初始化时调用
- 应用：`POST /v1/sop/runs/:id/nodes/:node_id/apply-bookmark`
- 自动恢复：CreateRun 时后端会自动应用所有匹配书签，返回 `auto_applied_count`，前端 toast 提示
- 输入修改检测：复刻 legacy `originalInputValues` 机制 —— 用户编辑过的步骤再点"重新生成"会提示"将删除该书签，确认？"

```typescript
// useInputDirtyDetection.ts
const originalValues = ref<Record<number, string>>({}) // nodeId → 原始值

function snapshot(nodeId: number, value: string) {
  if (!(nodeId in originalValues.value)) {
    originalValues.value[nodeId] = value.trim()
  }
}

function isDirty(nodeId: number, currentValue: string): boolean {
  const original = originalValues.value[nodeId]
  return original !== undefined && original !== currentValue.trim()
}
```

---

## §8 trailing chat 设计

### 8.1 UI 位置决策（解决 §8 开放问题 1）

**作为第 N+1 步显示在 stepper 上**（与 legacy 一致），不是 footer 下挂。原因：
- 用户已经习惯 legacy 的位置
- stepper 上的"聊天"步骤更显眼，用户更可能使用
- 与"完成所有步骤"形成自然连接

### 8.2 TrailingChatPanel.vue

```vue
<template>
  <div class="chat-panel">
    <div class="chat-messages" ref="messagesEl">
      <ChatBubble v-for="msg in messages" :key="msg.id" :message="msg" 
                  @regenerate="handleRegenerate" />
      <ChatBubble v-if="streamingMessage" :message="streamingMessage" :streaming="true" />
    </div>
    <div class="chat-input-area">
      <textarea v-model="inputText" @keydown.enter.exact.prevent="send" 
                ref="inputEl" :disabled="streaming" />
      <button @click="send" :disabled="!inputText.trim() || streaming">发送</button>
    </div>
  </div>
</template>

<script setup lang="ts">
const messages = ref<ChatMessage[]>([])
const streamingMessage = ref<ChatMessage | null>(null)
const inputText = ref('')
const streaming = ref(false)
const conversationId = ref<string | null>(null)

onMounted(async () => {
  // 加载历史消息
  const { data } = await getRunChatMessages(store.currentRun!.id)
  messages.value = data.list
  if (data.list.length > 0) {
    conversationId.value = data.list[0].conversation_id
  }
})

async function send() {
  const question = inputText.value.trim()
  if (!question || streaming.value) return
  
  // 用户消息立即上屏
  messages.value.push({ id: tempId(), role: 'user', content: question })
  inputText.value = ''
  streaming.value = true
  streamingMessage.value = { id: tempId(), role: 'assistant', content: '', thinking: '' }
  
  // 实测自 controller/v1/sop/sop.go:2362-2380，是 JSON body 不是 FormData
  const body = {
    run_id: store.currentRun!.id,
    conversation_id: conversationId.value ?? '',
    question,
    deep_thinking: modelStore.deepThinking,
    regenerate_msg_id: 0,
  }
  
  await sseStream.streamPost('/v1/sop/chat/stream', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  }, {
    onThinking: (chunk) => streamingMessage.value!.thinking += chunk,
    onMessage: (chunk) => streamingMessage.value!.content += chunk,
    onDone: (meta) => {
      // chat 的 done meta 不含 conversation_id —— conversation_id 由后端在第一次响应时
      // 通过 metadata 或独立机制返回。需要实测确认（S4 task 1 复测 chat 流的实际事件）
      messages.value.push(streamingMessage.value!)
      streamingMessage.value = null
      streaming.value = false
    },
    onError: handleError,
  })
}

async function handleRegenerate(msgId: string) {
  // 删除当前 AI 消息和用户消息，重新发送
  // 等价复刻 legacy `handleRegenerateChat()` 行 7175
}
</script>
```

---

## §9 路由与初始化

### 9.1 路由配置

保持现有路径不变（外链兼容性）：

```typescript
// router/index.ts
{
  path: '/sop',
  name: 'SOPRun',
  component: () => import('@/views/sop/SOPRunView.vue'),
  meta: { requiresAuth: true },
}
```

**注意**：原 `SOPView.vue` 文件被 `SOPRunView.vue` 替代。原文件直接删除，不保留兼容路径。

### 9.2 SOPRunView.vue 初始化流程

```typescript
// SOPRunView.vue setup
const route = useRoute()
const store = useSopRunStore()
const draft = useDraftLifecycle()

const templateId = computed(() => Number(route.query.templateId))
const runId = computed(() => route.query.runId ? Number(route.query.runId) : null)

onMounted(async () => {
  // 1. 加载 template + nodes
  await store.loadTemplate(templateId.value)
  
  // 2. 加载或创建 run
  if (runId.value) {
    await store.loadRun(runId.value)
  } else {
    // Draft 模式：不立即创建 run
    draft.enterDraftMode(templateId.value)
  }
  
  // 3. 恢复 sessionStorage 步骤
  const savedStep = sessionStorage.getItem(`sop_step_${runId.value || 'draft_' + templateId.value}`)
  if (savedStep) {
    store.setActiveStep(Number(savedStep))
  }
})

onBeforeUnmount(() => {
  // Beacon 清理 draft
  draft.cleanup()
  // 重置 store
  store.reset()
})
```

---

## §10 错误处理与边界情况

### 10.1 错误分类与处理（基于 numind 实际响应约定）

**关键事实**：numind 后端遵循 `.claude/rules/api-design.md` —— **所有业务响应都是 HTTP 200 + business code**，不使用 HTTP 402/403/422 等业务状态码。

| HTTP 状态 | response.data.code | 前端处理 |
|---|---|---|
| 200 | 0 | 成功，正常处理 `data` 字段 |
| 200 | 非 0（业务错误） | 进入业务错误路由（见下表） |
| 401 | - | axios 拦截器自动跳登录页 |
| 5xx | - | 全局 toast "服务器异常，请稍后重试" + retry 按钮 |
| Network error / fetch failed | - | 全局 toast "网络异常" |

#### 业务错误码到 UI 的映射（前端按 code 路由）

`internal/pkg/errno/` 定义的业务错误码（实测）：

| code 类别 | 触发场景 | 前端 UI 处理 |
|---|---|---|
| `ErrUnauthorized` | token 失效 / 未登录 | 跳登录页 |
| `ErrForbidden`（含 message "积分不足" 或 "余额不足"） | 配额耗尽 / 积分不足 | 触发 InsufficientCreditsDialog |
| `ErrForbidden`（其他 message） | 权限不足 / 模板未发布 | toast 显示 message + 升级会员引导（如适用） |
| `ErrTemplateNotFound` | 模板不存在 | EmptyStateCard "SOP 不存在或已被删除" |
| `ErrBind` | 参数错误 | toast 显示 message（开发期可能出现） |
| 其他业务错误 | 通用 | toast 显示 `response.data.message` |

**注意**：numind 不区分"配额耗尽"和"积分不足"为单独的 errno code，两者都走 `ErrForbidden` + 不同 message 字符串。前端用 message 关键字（"积分"、"余额"、"配额"、"次数"）做 fuzzy match 决定是否触发 InsufficientCreditsDialog。这是 SSE 流之前的预检（`controller/v1/sop/sop.go:2357` 的 `CanPerformAIOperation`），在执行节点和聊天前都会检查。

#### SSE 流中的错误（`event: error`）

SSE 流执行过程中的错误通过 `event: error` 推送（见 §4.1.4），不走 axios 拦截器：

```typescript
onError: (errMsg: string) => {
  // errMsg 是 JSON.parse 后的纯字符串，例如 "上下文超长" / "网络中断"
  if (errMsg.includes('积分') || errMsg.includes('余额') || errMsg.includes('次数')) {
    showInsufficientCreditsDialog()
  } else {
    showErrorToast(errMsg)
  }
  // 保留 streamingMessage 内容（已接收的部分）
  store.markStreamFailed(currentNodeId, errMsg)
}
```

#### InsufficientCreditsDialog 的接入机制

**当前现状（实测 src/App.vue:4-13）**：`InsufficientCreditsDialog` 是 App 级 ref，通过 `<InsufficientCreditsDialog ref="creditsDialogRef" />` 暴露。Vue 组件树中其他组件无法直接访问。

**新方案**：把 dialog 的触发改为 Pinia store 事件总线：

```typescript
// stores/uiDialogs.ts（新建或扩展现有 user store）
export const useUiDialogsStore = defineStore('uiDialogs', () => {
  const showCredits = ref(false)
  const creditsMessage = ref('')
  function openCreditsDialog(msg: string) {
    creditsMessage.value = msg
    showCredits.value = true
  }
  function closeCreditsDialog() { showCredits.value = false }
  return { showCredits, creditsMessage, openCreditsDialog, closeCreditsDialog }
})
```

App.vue 中监听 store state 显示 dialog。SOP 重写代码统一通过 `useUiDialogsStore().openCreditsDialog(msg)` 触发。这样做也消除了"组件 hierarchy 通过 ref 传递" 的耦合。

### 10.2 边界情况清单

| # | 场景 | 处理 |
|---|---|---|
| 1 | template.nodes 为空 | EmptyStateCard "该 SOP 暂未配置步骤"，禁所有交互 |
| 2 | node.description = NULL/空 | 不渲染描述行（不显示空 div / "undefined" / "null"） |
| 3 | node.name = NULL（防御） | fallback "步骤 N"（N = sort + 1） |
| 4 | trailing_chat_enabled=false 且全部 nodes 完成 | 显示完成态卡片，stepper 不显示第 N+1 步 |
| 5 | nodes.length > 10 | StepperPanel 横向滚动 + 桌面端 collapsed 视图 |
| 6 | GetTemplateNodes 失败 | 全屏 error 卡片 + retry 按钮 |
| 7 | 节点执行 SSE 中断 | 保留 streamingMessage，显示"网络中断"+"继续生成" |
| 8 | 同节点连续点"下一步" | 按钮 disabled until streaming 结束 |
| 9 | 流式输出中切换步骤 | 后台继续接收，UI 切到新步骤；返回原步骤时显示完整结果 |
| 10 | 浏览器关闭中途 | sendBeacon 触发 draft 清理；持久 run 不动 |
| 11 | B 端中途修改 SOP 配置 | C 端 run 按创建时快照执行，不响应实时变化（后端行为） |
| 12 | B 端删除正在运行的 SOP template | **不可能发生**：实测 `sop_run.template_id` FK 是 RESTRICT（默认）+ NOT NULL，DB 拒绝删除被引用的 template。B 端配置器删除会失败。无前端处理 |
| 13 | 文件上传过程中切步骤 | 上传继续进行，结果累积到原步骤 textarea，回原步骤可见 |
| 14 | 重复点击"重新生成" | 第二次点击禁用，清理上次结果再开始 |
| 15 | 输入框被书签填充后用户编辑 | 重新生成时提示"将删除该书签，确认？"（dirty 检测） |

### 10.3 待 S2 task 1 实测确认的事项

**S2 task 1 = "事实核对 task"**，必须先做：

1. **SSE 事件协议格式**：从 `executor.go` 提取实际发送的事件类型 + data 结构，与 §4.1 spec 对照，不一致则更新 spec
2. **`sop_run.template_id` ON DELETE 行为**：`SHOW CREATE TABLE sop_run` 确认外键约束
3. **DraftRun 后端清理逻辑**：读 `controller/v1/sop/bookmark.go` `DeleteDraftRun` 函数，确认它**只删 pending 且 counted=false** 的 draft，不会误删 running/succeeded 的 run
4. **trailing chat 的 stepper 显示样式**：实测当前 legacy 的渲染（截图或 DOM 检查），重写时保持视觉一致

---

## §11 全量切换部署 Runbook

### 11.1 部署顺序

1. **后端先行**（约 5 分钟）
   - merge `feature/sop-runtime-vue-rewrite` → `develop`（仅 numind-server）
   - push develop，触发 dev 自动部署
   - curl 验证：`GET /v1/sop/templates/1/nodes` 返回结构包含 `template` 对象，`nodes[*]` 不含敏感字段
   
2. **前端紧随**（约 5 分钟）
   - merge `feature/sop-runtime-vue-rewrite` → `develop`（numind-web-v3）
   - push develop，触发 dev 自动部署
   
3. **冒烟测试**（约 10 分钟）
   - 用 trial 账号进入 templateId=1（流量选题口播稿）走完 4 步 + trailing chat
   - 用 standard 账号进入 templateId=3+（self-service-config 创建的）走完所有步
   - 验证步骤名称 / 描述 = DB 真实数据
   - 验证浏览器 DevTools 不再有 api_key / prompt
   - 验证侧边栏无绿色卡片
   
4. **进入 prod**：dev 验证通过后，cherry-pick 到 release 分支 → tag → prod

### 11.2 紧急回退

如果 dev 冒烟测试发现 P0：

```bash
# 前端回退（最快，~3 分钟）
cd numind-web-v3
git revert <vue-rewrite-merge-commit>
git push develop

# 后端回退（如需）
cd numind-server
git revert <backend-merge-commit>
git push develop
```

**前置条件**：S4 实现时，numind-server 和 numind-web-v3 各自的"重写完成"必须**单一 merge commit**（不要 squash 进多 commit）。S3 plan 的最后一个 task 是"合并到 develop"，明确要求保留 merge commit 以便单条 git revert。

---

## §12 验证策略（满足 NDF Rule 10）

### 12.1 选定方式：Playwright E2E

**理由**：本次重写规模大、回归风险高、涉及配额/计费链路，必须有持久化的回归保护。gstack `/qa` 是一次性验证不留代码，不符合本场景需求。

### 12.2 关键路径清单

E2E test 文件：`numind-web-v3/e2e/sop-runtime.spec.ts`

| # | 路径 | 断言 |
|---|---|---|
| 1 | trial 账号进入 templateId=1，走完 4 步 | 配额从 X 减为 X-1（仅 1 次），所有步骤名称匹配 DB |
| 2 | trial 账号 templateId=3（无 description）| 描述行不渲染（不显示空 div） |
| 3 | trial 账号配额耗尽时点执行 | 弹 InsufficientCreditsDialog |
| 4 | standard 账号进入 templateId=2，走完 + trailing chat 多轮对话 | 第 5 步聊天正常，conversation_id 持续，重新生成有效 |
| 5 | 上传 PDF 触发 OCR | 输入框显示识别结果，与 baseText 不冲突 |
| 6 | 节点 SSE 流式输出 | StepOutput 增量渲染，思维链正确显示和折叠 |
| 7 | 流式输出过程中刷新页面 | 重新进入后从相同步骤恢复（sessionStorage） |
| 8 | 历史记录弹窗 | 列表正确，删除有确认，切换 run 跳转正确 |
| 9 | Draft 模式下关闭浏览器 | 后端 draft 被清理（SQL 验证 sop_run 表无 pending+counted=false 残留） |
| 10 | API 安全验证 | curl `GET /v1/sop/templates/1/nodes` 后 jq 断言无敏感字段 |

### 12.3 单元测试

- 后端：`internal/pkg/model/dto/sop_test.go` —— DTO 转换不泄露敏感字段
- 前端：composables 单测（`useSSEStream`、`useScrollFollow`、`useDraftLifecycle`、`useFileUpload`）—— mock fetch / scroll event / file，验证状态机正确

### 12.4 Lint / TypeCheck

- `task lint`（后端）
- `npm run lint && npm run type-check`（前端）
- **0 warnings 0 errors** 才能进入 review

---

## §13 已知数据问题与处理

### 13.1 templateId=1, 2 的 sop_node.description 为 NULL

**实测**（dev DB 2026-04-11）：8 个 nodes 全部 NULL。

**处理**：UI 优雅退化，不渲染描述行。不做 SQL backfill。

**长期方案**：creator (user 25) 通过 self-service-config 编辑器自助补齐。

### 13.2 templateId=1, 2 的 sop_node 含历史 LLM 凭证

**实测**：base_url=`https://ark.cn-beijing.volces.com/api/v3`，model_name=`deepseek-v3-2-251201`，api_key 长度 36 字符（真实 token）。

**处理**：**保留不动**。后端 executor 优先使用节点字段，fallback 才读 viper 配置 —— 这是这两个模板能正常运行的方式。删除会破坏它们。

**新隐藏**：这些字段不再通过 API 返回给前端（DTO 隐藏 5 字段），但 DB 中仍然存在。

**未来清理**：不在本次范围。如果未来要彻底统一走全局 LLM 路由，需要单独 task 把这些字段清空 + 验证 fallback 路径。

### 13.3 templateId=3+ 的 sop_node 字段为空，走 viper fallback

**实测**：base_url/model_name/api_key 全空，prompt 有值。

**处理**：spec 不需特殊处理。后端 executor 已经支持 fallback。

---

## §14 task 估算（粗粒度，详细 plan 在 S3 产出）

| # | task | 文件数 | 约工作量 |
|---|---|---|---|
| 1 | 事实核对 task：(a) 实测 chat 流的 conversation_id 返回机制；(b) 核验 UpdateNode 白名单未被回退；(c) 检查现有 src/stores/sop.ts 处置策略；(d) Beacon `?token=` query 参数后端实测；(e) trailing chat 视觉对照 legacy | 0（仅调研） | 0.5 |
| 2 | 后端 DTO 定义 + ToDTO 函数 + 单测 | 2 | 0.5 |
| 3 | 后端 GetTemplateNodes 改造 + curl 验证 | 1 | 0.3 |
| 4 | 后端 CreateNode 字段白名单守卫 + 测试 + 调试日志清理 | 2 | 0.5 |
| 5 | 前端 types.ts + Pinia store 骨架（含 SopRunStatus 含 'draft'） | 2 | 0.5 |
| 6 | 前端 useSSEStream（含心跳过滤 + done 幂等 + JSON.parse data 字符串）+ 单测 mock | 2 | 1 |
| 7 | 前端 useScrollFollow composable + 单测 | 1 | 0.7 |
| 8 | 前端 useDraftLifecycle + Beacon 清理（`?token=` query）+ 单测 | 1 | 0.7 |
| 9 | 前端 useInputPersistence composable | 1 | 0.3 |
| 10 | 前端 useFileUpload + useBookmarks composables | 2 | 0.7 |
| 11 | 前端 useStepNavigation composable（canAccessStep 等价复刻） | 1 | 0.4 |
| 12 | 前端 markdown.ts util（marked + highlight.js + DOMPurify 迁移 npm） | 1 | 0.3 |
| 13 | 前端 ConfirmModal.vue + AppNotification.vue 新建（如 S3 决定新建）| 2 | 0.5 |
| 14 | 前端 stores/uiDialogs.ts（InsufficientCreditsDialog 触发总线） + App.vue 接入改造 | 2 | 0.4 |
| 15 | 前端 StepperPanel.vue 组件 + DESIGN.md token 对齐 | 1 | 0.7 |
| 16 | 前端 StepInput.vue（含拖拽 + 文件上传 UI） | 1 | 0.7 |
| 17 | 前端 StepOutput.vue（流式 Markdown + 思维链折叠 + 滚动跟随接入） | 1 | 0.8 |
| 18 | 前端 ToolbarActions.vue + EmptyStateCard.vue + ScrollFollowButton.vue | 3 | 0.5 |
| 19 | 前端 HistoryModal.vue 组件 | 1 | 0.7 |
| 20 | 前端 TrailingChatPanel.vue + ChatBubble 子组件（含模型选择 + 深度思考 wire） | 2 | 1 |
| 21 | 前端 SOPRunView.vue 主组件集成 + 路由 | 1 | 0.7 |
| 22 | 前端 删除 sop-legacy.js + sop-legacy.css + 旧 SOPView.vue + 旧 store（如适用） | -3 | 0.2 |
| 23 | Playwright E2E test 编写（11 关键路径，含模型切换） | 1 | 1.5 |
| 24 | 全链路冒烟 + lint / typecheck / test pass | 0 | 0.5 |
| 25 | 部署 dev + curl 安全验证 + 多账号冒烟 | 0 | 0.3 |
| **合计** | **~25 个 task** | **~30 文件改动** | **~14 工作日** |

**比 self-service-config (14 task) 多 11 task**，符合 reviewer "20-25 task" 的预估区间上限。task 数增长来自：reviewer 发现的 ConfirmModal/AppNotification 不存在（+1）、useStepNavigation 单列（+1）、调试日志清理（合并入 task 4）、stores/uiDialogs 接入（+1）、SSE 复杂度拆分（+1）。

---

## §15 决策汇总（spec 阶段新增）

| 决策 | 选择 | 理由 |
|---|---|---|
| DTO 数量 | 2 个（C 端 SopNodePublicDTO 隐藏 5 字段；B 端 SopNodeEditDTO 隐藏 4 字段保留 prompt） | C 端无需 prompt，B 端配置器需要 prompt 编辑 |
| 历史 LLM 凭证清理 | 不清理 DB 字段，仅隐藏 DTO | 删除会破坏 templateId=1, 2 的执行路径 |
| trailing chat UI 位置 | stepper 第 N+1 步（与 legacy 一致） | 视觉连续性 + 用户习惯 |
| 第三方库 | marked / highlight.js / DOMPurify 从 CDN 迁移到 npm | 版本管理 + bundle 优化 |
| Pinia store 数量 | 单一 `useSopRunStore` | 状态紧密耦合，拆多个 store 反而难管理 |
| Composable 数量 | 8 个（useSOPRun / useSSEStream / useScrollFollow / useInputPersistence / useDraftLifecycle / useFileUpload / useBookmarks / useStepNavigation） | 单一职责，可独立单测 |
| 路由路径 | 保持 `/sop?templateId=X&runId=Y` 不变 | 外链兼容性 |
| Beacon 清理时机 | onBeforeUnmount 触发，仅 draft 被清理 | 避免活跃 run 误删 |
| trailing chat conversation_id 生成时机 | 后端首次响应返回，前端缓存 | 与 legacy 一致 |
| dirty 检测策略 | 用 trim 后字符串比较 originalValues | 等价复刻 legacy 行为 |
| 重新生成确认 | 仅当 dirty=false 且来自 bookmark 时确认 | 等价复刻 legacy 行为 |
| 验证策略 | Playwright E2E（非 gstack /qa） | 持久化回归保护，符合 NDF Rule 10 |
| **SSE 协议实测后修订（2026-04-11）** | thinking/error data 是 JSON-encoded 字符串；message 无 event 行；done 被发送两次需幂等；心跳行 `:\n\n` 必须忽略；error data 是字符串不是 `{code,message}` | reviewer 实测核对，初版 spec 描述错误 |
| **Draft 状态机修订** | 后端有独立 `SopStatusDraft = "draft"` 常量；DeleteDraftRun 仅看 `status == "draft"`，与 counted 无关；TS SopRunStatus 必须包含 `'draft'` | reviewer 实测，初版 spec 错误声称是 pending+counted 组合 |
| **错误处理修订** | numind 全部 HTTP 200 + business code，不使用 HTTP 402/403。`ErrInsufficientCredits` errno 不存在，"积分不足"通过 `ErrForbidden` + message 字符串区分。前端用 message 关键字 fuzzy match 触发 InsufficientCreditsDialog | reviewer 实测，初版 spec 基于 HTTP 语义错误假设 |
| **InsufficientCreditsDialog 接入** | 改为 Pinia store 事件总线 `useUiDialogsStore`，App.vue 监听 state 显示。消除 ref 跨组件传递耦合 | reviewer 指出现有触发机制不可跨组件复用 |
| **现有组件库实测** | ConfirmModal 和 AppNotification 不存在；只有 InsufficientCreditsDialog/AppButton/AppInput/ModelSelector 可复用。task 13 新建两个 | reviewer 实测 src/components/common/ |
| **现有 sop store 共存** | src/stores/sop.ts 已存在；新建 sopRun.ts 并存，task 1 评估是否合并 | reviewer 实测 |
| **ChatStream 是 JSON body 不是 FormData** | `/v1/sop/chat/stream` 是 application/json body：`{run_id, conversation_id, question, deep_thinking, regenerate_msg_id}`；ExecuteNodeStream 才是 FormData | reviewer 实测 controller |
| **模型选择 wire 格式** | ExecuteNodeStream：`?model_key=X&thinking=1` query 参数；ChatAfterRunStream：JSON body `deep_thinking` 字段 | reviewer 实测 |
| **sop_run.template_id FK 行为** | 实际是 RESTRICT (默认) + NOT NULL，不是 SET NULL。"B 端删除被引用的 template" 不可能发生 | reviewer 实测 SHOW CREATE TABLE |
| **UpdateNode 已有白名单** | 实测 controller/v1/config/sop.go:192-237 已用 updateNodeReq 白名单。无需修改，仅核验 | reviewer 实测，移除多余 task |
| **调试日志位置** | 5+ 处 `~/Desktop/...` 硬编码在 controller/v1/sop/sop.go (262/275/311/323/337)，不在 biz/sop/sop.go | reviewer 实测，修正改动文件位置 |

---

## §16 待 S3 plan 阶段细化

1. 19 个 task 的精确依赖关系（哪些可并行 / 哪些必须顺序）
2. 每个 task 的"完成定义"（DoD）
3. 每个 task 的 commit message 草稿
4. S5 验证策略 task 的明确归属（task 17 = E2E）
5. S4 编码阶段的 review 节奏（Rule 6: 每 task 必须双 review）
