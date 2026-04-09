# LLM 模型切换与多供应商智能路由 — 技术设计

## 概述

为 SOP 执行和智能体对话提供多模型选择能力（Claude Sonnet 4.6 / Gemini 3.1 Pro / DeepSeek V3.2 / GPT 5.4），支持"深度思考"模式切换。后端通过 LLMRouter 服务层实现多供应商智能路由，自动选择最优供应商并支持故障 fallback。管理端提供供应商/模型/路由的完整管理能力。

**涉及仓库**：numind-server、numind-web-v3、numind-admin-web

**明确排除**：SalesRAG 的 LLM 调用不受用户模型选择影响，保持现有 volcBiz + DMXAPIClient 调用链路不变。原因：SalesRAG 有独立的模型策略（意图分析用 qwen-turbo、回复用 deepseek-v3），不应被用户偏好覆盖。

---

## §1 数据模型

### 1.1 llm_provider（供应商表）

每个 API key 分组对应一条记录。同一聚合平台的不同 key 分组拆成多条（如 `linkapi-group1`、`linkapi-group2`）。

```sql
CREATE TABLE llm_provider (
    id           BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    name         VARCHAR(50) NOT NULL UNIQUE,
    display_name VARCHAR(100) NOT NULL,
    base_url     VARCHAR(255) NOT NULL,
    api_key      VARCHAR(255) NOT NULL,
    is_active    TINYINT(1) DEFAULT 1,
    created_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
);
```

API key 明文存储，管理端 API 返回时脱敏（只显示后 4 位）。

### 1.2 llm_model（逻辑模型表）

用户看到的模型列表。8 条记录：4 个基础模型 + 4 个 thinking 变体。

```sql
CREATE TABLE llm_model (
    id                BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    model_key         VARCHAR(100) NOT NULL UNIQUE,
    display_name      VARCHAR(100) NOT NULL,
    is_thinking       TINYINT(1) DEFAULT 0,
    base_model_id     BIGINT UNSIGNED,
    supports_thinking TINYINT(1) DEFAULT 0,
    icon              VARCHAR(50),
    sort_order        INT DEFAULT 0,
    is_active         TINYINT(1) DEFAULT 1,
    created_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at        DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_base_model (base_model_id),
    CONSTRAINT fk_base_model FOREIGN KEY (base_model_id) REFERENCES llm_model(id)
);
```

**字段说明：**
- `is_thinking`：标识该记录是否为 thinking 变体（0=基础模型，1=thinking 变体）
- `base_model_id`：thinking 变体指向的基础模型 ID（基础模型为 NULL）
- `supports_thinking`：基础模型是否有对应的 thinking 变体。前端据此决定"深度思考"按钮是否可用。`supports_thinking=0` 用于将来新增不支持 thinking 的模型（如纯补全类模型），初始 seed 中的 4 个模型全部支持

**用户端 API 只返回 `is_thinking=0 AND is_active=1` 的基础模型。**

### 1.3 llm_model_provider（路由映射表）

模型×供应商路由，每条记录是一个可用的调用路径。

```sql
CREATE TABLE llm_model_provider (
    id                    BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    model_id              BIGINT UNSIGNED NOT NULL,
    provider_id           BIGINT UNSIGNED NOT NULL,
    provider_model_id     VARCHAR(100) NOT NULL,
    priority              INT DEFAULT 0,
    input_price_per_mtok  DECIMAL(10,4) DEFAULT 0,
    output_price_per_mtok DECIMAL(10,4) DEFAULT 0,
    is_active             TINYINT(1) DEFAULT 1,
    created_at            DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at            DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_model_provider (model_id, provider_id),
    INDEX idx_mp_model_active (model_id, is_active, priority),
    CONSTRAINT fk_mp_model FOREIGN KEY (model_id) REFERENCES llm_model(id),
    CONSTRAINT fk_mp_provider FOREIGN KEY (provider_id) REFERENCES llm_provider(id)
);
```

**字段说明：**
- `provider_model_id`：供应商侧实际模型标识（可能与 model_key 不同）
- `priority`：数字越小越优先（通常按价格排）
- `input/output_price_per_mtok`：仅供管理员参考和路由排序，实际计费仍走 `pricing_rule` 表

### 1.4 user_model_preference（用户偏好表）

```sql
CREATE TABLE user_model_preference (
    id         BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id    BIGINT UNSIGNED NOT NULL,
    feature    VARCHAR(20) NOT NULL,
    model_key  VARCHAR(100) NOT NULL,
    thinking   TINYINT(1) DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_user_feature (user_id, feature)
);
```

按功能独立记忆：`feature='chatbot'` 和 `feature='sop'` 各一条。

### 1.5 初始数据（Seed）

| 显示名称 | model_key | is_thinking | base_model_id | supports_thinking |
|----------|-----------|-------------|---------------|-------------------|
| Claude Sonnet 4.6 | claude-sonnet-4-6 | 0 | NULL | 1 |
| Claude Sonnet 4.6 Thinking | claude-sonnet-4-6-thinking | 1 | → claude-sonnet-4-6 | 0 |
| Gemini 3.1 Pro | gemini-3.1-pro-preview | 0 | NULL | 1 |
| Gemini 3.1 Pro Thinking | gemini-3.1-pro-preview-thinking | 1 | → gemini-3.1-pro-preview | 0 |
| DeepSeek V3.2 | deepseek-v3.2 | 0 | NULL | 1 |
| DeepSeek V3.2 Thinking | deepseek-v3.2-thinking | 1 | → deepseek-v3.2 | 0 |
| GPT 5.4 | gpt-5.4 | 0 | NULL | 1 |
| GPT 5.4 Thinking | gpt-5.4-thinking | 1 | → gpt-5.4 | 0 |

---

## §2 后端架构

### 2.1 LLMRouter 服务层

**位置**：`internal/numind/biz/llmrouter/`

```
llmrouter/
├── router.go      // LLMRouter 核心：Resolve + StreamChat
├── cache.go       // 内存缓存，5 分钟 TTL，管理端写操作主动失效
└── types.go       // ResolvedRoute, RouteList 类型定义
```

**核心接口：**

```go
type LLMRouter struct {
    ds    store.IStore
    cache *routerCache
}

type ResolvedRoute struct {
    BaseURL         string
    APIKey          string
    ProviderModelID string
    ProviderName    string
    EnableThinking  bool
}

// Resolve 解析逻辑模型到供应商路由列表（按 priority 排序）
func (r *LLMRouter) Resolve(ctx context.Context, modelKey string, thinking bool) ([]ResolvedRoute, error)

// StreamChat 统一流式调用入口
func (r *LLMRouter) StreamChat(ctx context.Context, modelKey string, thinking bool,
    messages []llm.ChatMessage, temperature float64, maxTokens int,
    onEvent func(eventType, content string) error) (string, *billing.TokenUsage, error)

// InvalidateCache 管理端写操作后调用
func (r *LLMRouter) InvalidateCache()
```

### 2.2 Resolve 流程

```
输入: modelKey="deepseek-v3.2", thinking=true

1. 查缓存/DB: llm_model WHERE model_key="deepseek-v3.2" AND is_active=1
2. thinking=true:
   a. 查 llm_model WHERE base_model_id=<基础模型ID> AND is_thinking=1 AND is_active=1
   b. 找到 → 使用 thinking 变体的 ID 查路由
   c. 找不到 → 降级到基础模型，enableThinking=false（日志告警）
3. 查 llm_model_provider WHERE model_id=? AND is_active=1 ORDER BY priority ASC
4. JOIN llm_provider WHERE is_active=1 获取 base_url + api_key
5. 返回路由列表（多条，用于 fallback）
```

### 2.3 StreamChat 流程（含 fallback）

```
1. Resolve → 获取路由列表
2. 遍历路由列表:
   a. 构造 DMXAPIClient(route.BaseURL, route.APIKey)
   b. 调用 StreamChatCompletion(route.ProviderModelID, messages, ..., route.EnableThinking)
   c. 成功 → 记录 Langfuse generation + 返回
   d. 失败:
      - 连接失败/非200响应 → 日志 + 尝试下一个路由
      - 流式输出已开始后中途断连 → 返回错误，不 fallback
3. 全部失败 → 返回最后一个错误
```

**fallback 边界规则：**
- **可 fallback**：连接拒绝、超时、401/403/429/500 等 HTTP 错误（第一个 token 之前）
- **不可 fallback**：流式输出已经开始（已收到至少一个 content chunk），中途断连只能返回错误
- 理由：中途 fallback 到新供应商会产生不连贯的输出，前端已渲染部分内容

### 2.4 DMXAPIClient 泛化

DMXAPIClient 已存在于 `internal/pkg/llm/dmxapi_client.go`（从 `biz/salesrag/adapter/` 提取的共享版本）。在该文件新增动态构造函数：

```go
// NewDMXAPIClientWithConfig 支持动态传入 baseURL 和 apiKey（LLMRouter 使用）
func NewDMXAPIClientWithConfig(baseURL, apiKey string) *DMXAPIClient

// 原 NewDMXAPIClient() 保留不动，SalesRAG 等现有调用方继续使用
```

注意：不修改原有 `NewDMXAPIClient()`，SalesRAG 的调用链路完全不变。

### 2.5 Langfuse/计费分层

**职责划分：**
- **调用方（chatbot/SOP）**：创建 trace + span，通过 context 传入 traceID 和 parentObservationID
- **LLMRouter.StreamChat**：在调用方提供的 trace 下创建 generation 记录（含 model、provider、input/output、usage）
- **DMXAPIClient**：不再独立创建 generation（由 LLMRouter 层统一处理）

**改造要点：**
- chatbot `stream.go` 第 150-153 行的 `CreateGeneration` 代码删除，改由 LLMRouter 内部创建
- SOP executor 的 Langfuse 逻辑同理，由 LLMRouter 统一处理
- DMXAPIClient 内部已有的 Langfuse generation 代码：当通过 LLMRouter 调用时跳过（检查 context 中是否已有 LLMRouter 标记）

### 2.6 集成点改造

#### Chatbot（biz/chatbot/stream.go）

```go
// 之前：
const chatStreamDefaultModel = "deepseek-v3-2-251201"
result, usage, err := b.volcBiz.StreamChatWithModel(ctx, messages, chatStreamDefaultModel, 0, 0.7, "minimal", onEvent)

// 之后：
// modelKey 和 thinking 从 API 请求参数获取（controller 传入）
result, usage, err := b.llmRouter.StreamChat(ctx, modelKey, thinking, messages, 0.7, 0, onEvent)
```

`ChatStream` 方法签名扩展：新增 `modelKey string, thinking bool` 参数。

#### SOP Executor（biz/sop/executor.go）

```go
// 之前：
applyDefaultLLMConfig(node)
// ... 构造 LLMRequest，直接 HTTP POST 到 node.BaseURL

// 之后：
// 如果用户选择了模型 → 通过 LLMRouter 调用（全局覆盖 node 配置）
// 如果用户未选择模型 → 保留现有 applyDefaultLLMConfig 逻辑作为 fallback
if modelKey != "" {
    result, usage, err = e.llmRouter.StreamChat(ctx, modelKey, thinking, messages, 0.7, maxTokens, handler)
} else {
    // 保留现有逻辑，兼容无模型选择的场景
    applyDefaultLLMConfig(node)
    // ... 原有 HTTP 调用
}
```

**消息格式适配：**
- SOP executor 使用 `[]sop.LLMMessage`，需转换为 `[]llm.ChatMessage`
- chatbot 使用 `[]map[string]interface{}`，需转换为 `[]llm.ChatMessage`
- 转换函数在各自的 biz 层实现

### 2.7 缓存策略

- **存储**：内存缓存（sync.RWMutex + map），5 分钟 TTL
- **缓存内容**：模型列表、模型→路由映射
- **主动失效**：管理端供应商/模型/路由的写操作完成后调用 `LLMRouter.InvalidateCache()`
- **限制**：单实例有效。多实例部署时需改为 Redis 缓存或事件通知机制（当前单实例，不需要）

---

## §3 API 设计

### 3.1 用户端 API

#### GET /v1/llm/models

返回可用基础模型列表（is_thinking=0, is_active=1）。

**响应：**
```json
{
  "code": 0,
  "data": {
    "list": [
      {
        "model_key": "claude-sonnet-4-6",
        "display_name": "Claude Sonnet 4.6",
        "supports_thinking": true,
        "icon": "claude",
        "sort_order": 1
      }
    ],
    "default_model_key": "deepseek-v3.2"
  }
}
```

#### GET /v1/llm/preference

返回当前用户的模型偏好。

**响应：**
```json
{
  "code": 0,
  "data": {
    "chatbot": { "model_key": "deepseek-v3.2", "thinking": false },
    "sop": { "model_key": "claude-sonnet-4-6", "thinking": true }
  }
}
```

用户无偏好记录时返回系统默认值：`model_key` 为 llm_model 表中 `is_active=1 AND is_thinking=0` 且 `sort_order` 最小的记录，`thinking=false`。

#### PUT /v1/llm/preference

保存用户模型偏好。

**请求：**
```json
{
  "feature": "chatbot",
  "model_key": "claude-sonnet-4-6",
  "thinking": true
}
```

**校验：**
- `feature` 必须是 `chatbot` 或 `sop`
- `model_key` 必须存在于 llm_model 且 is_active=1 且 is_thinking=0
- 如果 thinking=true，检查 supports_thinking=1

#### Chatbot 消息发送 — 扩展参数

```
GET /v1/chatbot/sessions/:id/chat?model_key=xxx&thinking=1
```

现有 SSE 接口新增 query 参数。

#### SOP 节点执行 — 扩展参数

```
POST /v1/sop/runs/:id/nodes/:node_id/execute?model_key=xxx&thinking=1
```

现有接口新增 query 参数。

#### 三级回退逻辑（后端统一处理，Chatbot 和 SOP 共用）

后端 controller 层按以下优先级确定实际使用的模型：
1. **query 参数**：`model_key` + `thinking`（最高优先级）
2. **用户偏好**：query 为空时，从 `user_model_preference` 表实时查询该用户该 feature 的偏好
3. **系统默认**：偏好表也无记录时，使用 llm_model 表中 `is_active=1 AND is_thinking=0` 且 `sort_order` 最小的模型，`thinking=false`

### 3.2 管理端 API

#### 供应商管理

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /v1/admin/llm/providers | 列表（api_key 脱敏） |
| POST | /v1/admin/llm/providers | 创建 |
| PUT | /v1/admin/llm/providers/:id | 更新 |
| DELETE | /v1/admin/llm/providers/:id | 删除（软删除或检查无关联路由） |

#### 模型管理

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /v1/admin/llm/models | 列表（含 thinking 变体） |
| POST | /v1/admin/llm/models | 创建 |
| PUT | /v1/admin/llm/models/:id | 更新 |
| DELETE | /v1/admin/llm/models/:id | 删除 |

#### 路由管理

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /v1/admin/llm/models/:modelId/routes | 该模型的路由列表 |
| POST | /v1/admin/llm/models/:modelId/routes | 创建路由 |
| PUT | /v1/admin/llm/models/:modelId/routes/:routeId | 更新路由 |
| DELETE | /v1/admin/llm/models/:modelId/routes/:routeId | 删除路由 |

所有管理端写操作完成后调用 `LLMRouter.InvalidateCache()`。

---

## §4 前端设计

### 4.1 用户端（numind-web-v3）

#### 新增文件

| 文件 | 说明 |
|------|------|
| `src/components/common/ModelSelector.vue` | 通用模型选择器（pill 下拉 + 深度思考 toggle） |
| `src/stores/llmModel.ts` | Pinia store：模型列表 + 用户偏好 |
| `src/api/llm.ts` | API 封装 |

#### ModelSelector.vue

- **Props**：`feature: 'chatbot' | 'sop'`
- **布局**：紧凑型 pill 下拉按钮 + "深度思考" toggle 按钮，并排放置
- **数据源**：`useLLMModelStore()`
- **交互**：
  - 点击 pill → 展开模型列表（4 个基础模型）
  - 选择模型 → 收起，调用 `PUT /v1/llm/preference` 保存
  - 点击"深度思考" toggle → 切换状态，调用 `PUT /v1/llm/preference` 保存
  - 当前模型 `supports_thinking=false` 时，"深度思考"按钮禁用（灰色）
- **状态处理**：
  - loading：skeleton
  - empty / error：隐藏选择器，使用默认模型
  - success：显示列表，标记选中项

#### useLLMModelStore

```typescript
export const useLLMModelStore = defineStore('llmModel', () => {
  const models = ref<LLMModel[]>([])
  const preferences = ref<Record<string, { model_key: string; thinking: boolean }>>({})

  async function fetchModels()       // GET /v1/llm/models
  async function fetchPreferences()  // GET /v1/llm/preference
  async function savePreference(feature: string, modelKey: string, thinking: boolean)

  function getSelectedModel(feature: string): LLMModel | undefined
  function isThinkingEnabled(feature: string): boolean

  return { models, preferences, fetchModels, fetchPreferences, savePreference, getSelectedModel, isThinkingEnabled }
})
```

#### Chatbot 集成（ChatbotChat.vue）

- 在输入框工具栏（send 按钮左侧）插入 `<ModelSelector feature="chatbot" />`
- `sendMessage` 时从 store 读取 modelKey + thinking，作为 query 参数附加到 SSE 请求

#### SOP 集成（SOPView.vue + sop-legacy.js）

- `SOPView.vue` 在 SOP 容器上方插入 `<ModelSelector feature="sop" />`
- 通过 `window.__selectedModel = { modelKey, thinking }` 传递给 legacy JS（`sop-legacy.js` 是非 Vue 模块，无法直接访问 Pinia store，故通过 window 全局变量桥接）
- `sop-legacy.js` 在执行 API 调用时读取 `window.__selectedModel`，附加到请求 URL query 参数

### 4.2 管理端（numind-admin-web）

新增两个页面：

#### 供应商管理页面
- 表格展示供应商列表（名称、API 地址、API Key 脱敏、状态）
- 支持增删改查
- 创建/编辑时用表单弹窗

#### 模型管理页面
- 表格展示所有逻辑模型（名称、标识、thinking 状态、排序、是否启用）
- 每行可展开查看路由映射子表格
- 路由子表格：供应商、供应商模型名、优先级、价格、状态
- 支持增删改查

---

## §5 Langfuse Trace Topology

### Chatbot 对话 trace

```
trace: "chatbot-chat" (user_id, session_id, chatbot_id, model_key)
├── span: "context-assembly"
│   └── span: "vector-retrieval"
└── generation: "llm-chat" (model=provider_model_id, provider=provider_name)
```

### SOP 执行 trace

```
trace: "sop-execute" (user_id, run_id, node_id, model_key)
└── generation: "llm-chat" (model=provider_model_id, provider=provider_name)
```

generation 由 LLMRouter 内部创建，包含：
- `model`：实际调用的 provider_model_id
- `metadata.provider`：供应商名称
- `metadata.logical_model`：用户选择的逻辑模型 key
- `metadata.thinking`：是否启用深度思考
- `input`：messages
- `output`：LLM 回复内容
- `usage`：token 用量

---

## §6 边界情况处理

| 场景 | 处理策略 |
|------|----------|
| 用户未选择模型（首次使用） | 使用系统默认模型（seed 数据中 sort_order 最小的） |
| 用户偏好中的模型已下线 | 返回可用模型列表时不含该模型，前端自动重置为默认 |
| thinking 变体不存在 | 降级到基础模型（不启用 thinking），日志告警 |
| 所有供应商对某模型都失败 | 返回错误信息，前端提示用户重试或切换模型 |
| 流式输出中途断连 | 返回错误，不 fallback，前端提示用户重试 |
| 供应商 API key 无效 | 跳过该供应商，尝试下一个 |
| 管理员禁用供应商 | 主动 invalidate 缓存，该供应商所有路由立即失效 |
| 模型列表为空（配置错误） | 使用系统硬编码默认模型，前端隐藏选择器 |
| SOP 执行时用户未选择模型 | 保留现有 applyDefaultLLMConfig 逻辑（node 配置 → volc config → 硬编码默认） |

---

## §7 设计决策记录

| 决策 | 选择 | 理由 |
|------|------|------|
| 多供应商 vs 单供应商 | 多供应商路由 | 不同平台价格差异大，需灵活切换 |
| 路由实现方案 | 泛化 DMXAPIClient + LLMRouter | 所有聚合平台均为 OpenAI 兼容格式，不需要独立 client |
| API key 存储位置 | llm_provider 表 | 拆多 provider 后每个 provider 一个 key，无需路由层存 key |
| API key 加密 | 明文存储 + 管理端脱敏 | 用户选择，后续可补加密 |
| thinking 控制方式 | 独立 toggle 按钮 | 比在模型列表里展示 8 个选项更清晰 |
| SOP 模型覆盖策略 | 全局覆盖 node 配置 | 用户选择 |
| SalesRAG 是否受影响 | 不影响 | SalesRAG 有独立模型策略，不应被用户偏好覆盖 |
| 缓存策略 | 内存缓存 + 管理端写操作主动失效 | 单实例部署，简单高效 |
| Fallback 边界 | 第一个 token 之前可 fallback | 中途 fallback 会产生不连贯输出 |
| 配置管理方式 | 数据库 + 管理端 UI | 增减模型不需要改代码/重启服务 |
