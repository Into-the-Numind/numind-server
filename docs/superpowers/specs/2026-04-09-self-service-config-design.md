# B端自助配置工具 — 技术设计 Spec

> Feature ID: `self-service-config`
> PRD: `proposals/self-service-config-proposal.md`
> Date: 2026-04-09
> Status: DRAFT
> Repos: numind-server, numind-web-v3

---

## 1. 概述

为父用户（B 端客户）在用户端新增"配置中心"，提供两项自助配置能力：

1. **Chatbot 智能体配置器** — 创建 chatbot（system prompt + 知识库挂载），发布后 C 端子用户可对话
2. **SOP 工作流配置器** — 创建多步骤 SOP（每步自定义提示词），发布后 C 端子用户可执行

架构决策：Chatbot biz 层独立实现（Approach C），复用 store 层文档/向量检索基础设施，不依赖 SalesRAG biz 层。

---

## 2. 数据模型

### 2.1 新增表

#### KnowledgeBase — 知识库（文档分组抽象）

```go
type KnowledgeBase struct {
    gorm.Model                                                  // uint PK, soft delete
    UserID      uint   `gorm:"not null;index:idx_kb_user_id" json:"user_id"`
    Name        string `gorm:"size:100;not null" json:"name"`
    Description string `gorm:"size:1024" json:"description"`
    Status      string `gorm:"size:20;not null;default:'active'" json:"status"` // active, archived
}

func (KnowledgeBase) TableName() string { return "knowledge_base" }
```

Status 常量：
```go
const (
    KBStatusActive   = "active"
    KBStatusArchived = "archived"
)
```

#### KnowledgeBaseDocument — 知识库-文档关联（硬删除）

```go
type KnowledgeBaseDocument struct {
    ID              uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
    KnowledgeBaseID uint      `gorm:"not null;uniqueIndex:idx_kbd_kb_doc" json:"knowledge_base_id"`
    DocumentID      uint      `gorm:"not null;uniqueIndex:idx_kbd_kb_doc" json:"document_id"` // FK → knowledge_document.id
    CreatedAt       time.Time `json:"created_at"`
}

func (KnowledgeBaseDocument) TableName() string { return "knowledge_base_document" }
```

硬删除决策理由：解挂后可重新关联，soft delete 的 unique index 会阻止重新关联同一文档。

#### ChatbotConfig — 智能体配置

```go
type ChatbotConfig struct {
    gorm.Model
    UserID       uint   `gorm:"not null;index:idx_cc_user_status" json:"user_id"`
    Name         string `gorm:"size:100;not null" json:"name"`
    Description  string `gorm:"size:1024" json:"description"`
    Avatar       string `gorm:"size:500" json:"avatar"`
    SystemPrompt string `gorm:"type:longtext;not null" json:"system_prompt"`
    Status       string `gorm:"size:20;not null;default:'draft';index:idx_cc_user_status" json:"status"`
}

func (ChatbotConfig) TableName() string { return "chatbot_config" }
```

Status 常量：
```go
const (
    ChatbotStatusDraft     = "draft"
    ChatbotStatusPublished = "published"
    ChatbotStatusOffline   = "offline"
)
```

`draft` 状态下，仅创建者（父用户）可对话测试。C 端子用户不可见 draft 状态的 chatbot。

#### ChatbotKnowledgeBase — 智能体-知识库挂载（硬删除）

```go
type ChatbotKnowledgeBase struct {
    ID              uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
    ChatbotID       uint      `gorm:"not null;uniqueIndex:idx_ckb_chatbot_kb" json:"chatbot_id"`
    KnowledgeBaseID uint      `gorm:"not null;uniqueIndex:idx_ckb_chatbot_kb" json:"knowledge_base_id"`
    CreatedAt       time.Time `json:"created_at"`
}

func (ChatbotKnowledgeBase) TableName() string { return "chatbot_knowledge_base" }
```

#### ChatbotSession — 对话会话

```go
type ChatbotSession struct {
    gorm.Model
    UserID       uint   `gorm:"not null;index:idx_cs_user_chatbot" json:"user_id"`
    ChatbotID    uint   `gorm:"not null;index:idx_cs_user_chatbot" json:"chatbot_id"`
    Title        string `gorm:"size:200" json:"title"`
    Status       string `gorm:"size:20;not null;default:'active'" json:"status"` // active, closed
    MessageCount int    `gorm:"default:0" json:"message_count"`
}

func (ChatbotSession) TableName() string { return "chatbot_session" }
```

#### ChatbotMessage — 对话消息（追加型，无软删除）

```go
type ChatbotMessage struct {
    ID               uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
    SessionID        uint      `gorm:"not null;index:idx_cm_session_seq" json:"session_id"`
    UserID           uint      `gorm:"not null;index:idx_cm_user_id" json:"user_id"` // session 所属用户，assistant 消息也填此值
    Role             string    `gorm:"size:20;not null" json:"role"` // user, assistant, system
    Content          string    `gorm:"type:longtext" json:"content"`
    Thinking         string    `gorm:"type:longtext" json:"thinking"`
    TraceID          string    `gorm:"size:100" json:"trace_id"`
    Seq              int       `gorm:"default:0;index:idx_cm_session_seq" json:"seq"`
    PromptTokens     int       `gorm:"default:0" json:"prompt_tokens"`
    CompletionTokens int       `gorm:"default:0" json:"completion_tokens"`
    CreatedAt        time.Time `json:"created_at"`
}

func (ChatbotMessage) TableName() string { return "chatbot_message" }
```

`UserID` 说明：assistant 消息的 `user_id` 设为 session 所属用户 ID，便于按用户查询账单和 token 消耗。

### 2.2 现有表改动

#### SopTemplate — 新增字段

```go
// 新增到 SopTemplate struct
CreatorUserID *uint  `gorm:"index:idx_st_creator" json:"creator_user_id"` // nil=admin创建
PublishStatus string `gorm:"size:20" json:"publish_status"`               // draft/published/offline，仅 B 端创建时使用
```

- `creator_user_id = nil`：admin 创建的模板，走现有 `status`（active/inactive）逻辑，`publish_status` 为空
- `creator_user_id != nil`：B 端创建的模板，使用 `publish_status` 控制可见性
- 现有 admin 路由和 C 端 SOP 执行代码零改动

PublishStatus 常量：
```go
const (
    SopPublishStatusDraft     = "draft"
    SopPublishStatusPublished = "published"
    SopPublishStatusOffline   = "offline"
)
```

### 2.3 新增 Feature Permission 常量

```go
// internal/pkg/model/user_feature_permission.go
const FeatureKeySelfServiceConfig = "self_service_config"
```

需在 migration 中 seed 到 `feature_permission` 表。

---

## 3. API 契约

### 3.1 B 端配置 API

路由组：`/v1/config/`
中间件链：`AuthMiddleware()` → `ParentUserOnly()` → `FeaturePermission(model.FeatureKeySelfServiceConfig)`

#### 知识库

| Method | Path | Body | Response |
|--------|------|------|----------|
| POST | `/v1/config/knowledge-bases` | `{"name":"...", "description":"..."}` | `{"id":1, "name":"...", ...}` |
| GET | `/v1/config/knowledge-bases?offset=0&limit=20` | — | `{"list":[...], "total":N}` |
| GET | `/v1/config/knowledge-bases/:id` | — | KB detail + `{"documents":[...]}` |
| PUT | `/v1/config/knowledge-bases/:id` | `{"name":"...", "description":"..."}` | ok |
| DELETE | `/v1/config/knowledge-bases/:id` | — | ok（事务：先删 chatbot_knowledge_base 关联，再软删 KB） |
| POST | `/v1/config/knowledge-bases/:id/documents` | `multipart/form-data: file` | `{"document_id":1, "status":"PENDING"}` |
| DELETE | `/v1/config/knowledge-bases/:id/documents/:docId` | — | ok（硬删关联行，文档本体保留） |

#### 智能体

| Method | Path | Body | Response |
|--------|------|------|----------|
| POST | `/v1/config/chatbots` | `{"name":"...", "description":"...", "avatar":"...", "system_prompt":"...", "knowledge_base_ids":[1,3]}` | chatbot detail |
| GET | `/v1/config/chatbots?offset=0&limit=20` | — | `{"list":[...], "total":N}` |
| GET | `/v1/config/chatbots/:id` | — | chatbot detail + `{"knowledge_bases":[...]}` |
| PUT | `/v1/config/chatbots/:id` | partial update fields | ok |
| DELETE | `/v1/config/chatbots/:id` | — | ok（软删除） |
| PUT | `/v1/config/chatbots/:id/status` | `{"status":"published"}` 或 `{"status":"offline"}` | ok |

status 转换规则：`draft→published`（首次发布）、`published→offline`（下线）、`offline→published`（重新上线）。

**Create Chatbot Request Body:**
| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| name | string | required | 名称，最长 100 字符 |
| description | string | optional | 描述，最长 1024 字符 |
| avatar | string | optional | 头像 URL |
| system_prompt | string | required | 系统提示词 |
| knowledge_base_ids | []uint | optional | 挂载的知识库 ID 列表 |

**Chatbot Detail Response Schema:**
```json
{
  "id": 1,
  "user_id": 100,
  "name": "产品咨询助手",
  "description": "解答产品相关问题",
  "avatar": "https://...",
  "system_prompt": "你是一个专业的产品咨询顾问...",
  "status": "draft",
  "knowledge_bases": [
    {"id": 1, "name": "产品文档库", "description": "...", "status": "active"}
  ],
  "created_at": "2026-04-09T12:00:00Z",
  "updated_at": "2026-04-09T12:00:00Z"
}
```

**Chatbot List Item Response Schema（不含 knowledge_bases）:**
```json
{
  "id": 1,
  "user_id": 100,
  "name": "产品咨询助手",
  "description": "解答产品相关问题",
  "avatar": "https://...",
  "status": "draft",
  "created_at": "2026-04-09T12:00:00Z",
  "updated_at": "2026-04-09T12:00:00Z"
}
```

#### SOP 模板

| Method | Path | Body | Response |
|--------|------|------|----------|
| POST | `/v1/config/sop-templates` | `{"name":"...", "description":"..."}` | template detail |
| GET | `/v1/config/sop-templates?offset=0&limit=20` | — | `{"list":[...], "total":N}`（按 creator_user_id 过滤） |
| GET | `/v1/config/sop-templates/:id` | — | template detail + `{"nodes":[...]}` |
| PUT | `/v1/config/sop-templates/:id` | partial update | ok |
| DELETE | `/v1/config/sop-templates/:id` | — | ok（软删除） |
| PUT | `/v1/config/sop-templates/:id/status` | `{"status":"published"}` 或 `{"status":"offline"}` | ok |
| POST | `/v1/config/sop-templates/:id/nodes` | `{"prompt":"...", "sort":0}` | node detail |
| PUT | `/v1/config/sop-templates/:id/nodes/:nodeId` | partial update | ok |
| DELETE | `/v1/config/sop-templates/:id/nodes/:nodeId` | — | ok |
| PUT | `/v1/config/sop-templates/:id/nodes/batch-sort` | `[{"id":1,"sort":0},{"id":2,"sort":1}]` | ok |

**Create SOP Template Request Body:**
| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| name | string | required | 名称，最长 100 字符 |
| description | string | optional | 描述 |

**Create Node Request Body:**
| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| prompt | string | required | 步骤提示词 |
| sort | int | optional | 排序序号，默认追加末尾 |

**SOP Template Detail Response Schema:**
```json
{
  "id": 1,
  "name": "客户跟进流程",
  "description": "...",
  "creator_user_id": 100,
  "publish_status": "draft",
  "status": "active",
  "nodes": [
    {"id": 1, "prompt": "分析客户背景...", "sort": 0},
    {"id": 2, "prompt": "生成跟进方案...", "sort": 1}
  ],
  "created_at": "2026-04-09T12:00:00Z",
  "updated_at": "2026-04-09T12:00:00Z"
}
```

**Create KB Request Body:**
| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| name | string | required | 知识库名称，最长 100 字符 |
| description | string | optional | 描述 |

**KB Detail Response Schema:**
```json
{
  "id": 1,
  "user_id": 100,
  "name": "产品文档库",
  "description": "...",
  "status": "active",
  "documents": [
    {"id": 1, "name": "产品手册.pdf", "status": "COMPLETED", "file_size": 1024000, "chunk_count": 45, "created_at": "2026-04-09T12:00:00Z"}
  ],
  "created_at": "2026-04-09T12:00:00Z",
  "updated_at": "2026-04-09T12:00:00Z"
}
```

SOP 步骤上限：biz 层 `CreateNode` 校验当前模板节点数 `>= 20` 时返回 `errno.ErrMaxNodesExceeded`。

发布行为：`PublishTemplate` → 设 `publish_status = published` → 调用 `store.Customer().GrantTemplateToAllSubUsers(parentUserID, templateID)` 自动为所有子用户创建 `UserTemplatePermission`。

下线行为：`UnpublishTemplate` → 设 `publish_status = offline` → 不撤销已有权限（已在执行的 run 可完成）。`CreateRun` 增加检查：`creator_user_id != nil && publish_status != 'published'` → 拒绝新 run。

### 3.2 C 端使用 API

路由组：`/v1/chatbot/`
中间件：`AuthMiddleware()`

| Method | Path | Body | Response |
|--------|------|------|----------|
| GET | `/v1/chatbot/list` | — | chatbot list（见下方作用域规则） |
| POST | `/v1/chatbot/sessions` | `{"chatbot_id":N}` | session detail |
| GET | `/v1/chatbot/sessions?offset=0&limit=20` | — | `{"list":[...], "total":N}` |
| DELETE | `/v1/chatbot/sessions/:id` | — | ok（软删 session + 硬删 messages） |
| GET | `/v1/chatbot/sessions/:id/messages?offset=0&limit=50` | — | `{"list":[...], "total":N}` |
| POST | `/v1/chatbot/sessions/:id/chat` | `{"message":"..."}` | SSE stream |

**Chatbot list 作用域规则：**
- 父用户（`parent_user_id IS NULL`）→ `WHERE user_id = self.ID`（返回全部状态，含 draft，用于自测）
- 子用户（`parent_user_id IS NOT NULL`）→ `WHERE user_id = parent_user_id AND status = 'published'`

**SSE 响应格式：**
```
data: {"type":"token","content":"你"}
data: {"type":"token","content":"好"}
data: {"type":"done","prompt_tokens":120,"completion_tokens":15}
```

**C 端 SOP：** 无新 API。现有 `/v1/sop/templates` 通过 `UserTemplatePermission` 过滤已自动包含 B 端发布的模板。

### 3.3 路由注册

```go
// router.go 新增

// B 端配置
configGroup := v1.Group("/config")
configGroup.Use(middleware.ParentUserOnly(), middleware.FeaturePermission(model.FeatureKeySelfServiceConfig))
{
    // KB
    configGroup.POST("/knowledge-bases", kbCtrl.Create)
    configGroup.GET("/knowledge-bases", kbCtrl.List)
    configGroup.GET("/knowledge-bases/:id", kbCtrl.Get)
    configGroup.PUT("/knowledge-bases/:id", kbCtrl.Update)
    configGroup.DELETE("/knowledge-bases/:id", kbCtrl.Delete)
    configGroup.POST("/knowledge-bases/:id/documents", kbCtrl.UploadDocument)
    configGroup.DELETE("/knowledge-bases/:id/documents/:docId", kbCtrl.RemoveDocument)

    // Chatbot
    configGroup.POST("/chatbots", configChatbotCtrl.Create)
    configGroup.GET("/chatbots", configChatbotCtrl.List)
    configGroup.GET("/chatbots/:id", configChatbotCtrl.Get)
    configGroup.PUT("/chatbots/:id", configChatbotCtrl.Update)
    configGroup.DELETE("/chatbots/:id", configChatbotCtrl.Delete)
    configGroup.PUT("/chatbots/:id/status", configChatbotCtrl.UpdateStatus)

    // SOP Template
    configGroup.POST("/sop-templates", configSopCtrl.Create)
    configGroup.GET("/sop-templates", configSopCtrl.List)
    configGroup.GET("/sop-templates/:id", configSopCtrl.Get)
    configGroup.PUT("/sop-templates/:id", configSopCtrl.Update)
    configGroup.DELETE("/sop-templates/:id", configSopCtrl.Delete)
    configGroup.PUT("/sop-templates/:id/status", configSopCtrl.UpdateStatus)
    configGroup.POST("/sop-templates/:id/nodes", configSopCtrl.CreateNode)
    configGroup.PUT("/sop-templates/:id/nodes/batch-sort", configSopCtrl.BatchSortNodes) // 静态路由先于动态
    configGroup.PUT("/sop-templates/:id/nodes/:nodeId", configSopCtrl.UpdateNode)
    configGroup.DELETE("/sop-templates/:id/nodes/:nodeId", configSopCtrl.DeleteNode)
}

// C 端 Chatbot
chatbotGroup := v1.Group("/chatbot")
{
    chatbotGroup.GET("/list", chatbotCtrl.List)
    chatbotGroup.POST("/sessions", chatbotCtrl.CreateSession)
    chatbotGroup.GET("/sessions", chatbotCtrl.ListSessions)
    chatbotGroup.DELETE("/sessions/:id", chatbotCtrl.DeleteSession)
    chatbotGroup.GET("/sessions/:id/messages", chatbotCtrl.ListMessages)
    chatbotGroup.POST("/sessions/:id/chat", chatbotCtrl.Chat) // SSE
}
```

注意：`/nodes/batch-sort` 必须注册在 `/nodes/:nodeId` 之前，避免 Gin 路由碰撞。

---

## 4. 后端架构

### 4.1 目录结构（新增部分）

```
internal/numind/
├── biz/
│   ├── chatbot/                    # 新增
│   │   ├── chatbot.go              # IChatbotBiz 接口 + B端配置 + C端使用
│   │   └── stream.go               # ChatStream SSE 流式对话
│   └── knowledgebase/              # 新增
│       └── knowledge_base.go       # IKnowledgeBaseBiz 接口
├── controller/v1/
│   ├── chatbot/                    # 新增：C 端 chatbot controller
│   │   └── chatbot.go
│   └── config/                     # 新增：B 端配置 controller
│       ├── chatbot.go
│       ├── sop.go
│       └── knowledge_base.go
├── store/
│   ├── chatbot_config.go           # 新增：IChatbotConfigStore
│   ├── chatbot_session.go          # 新增：IChatbotSessionStore
│   └── knowledge_base.go           # 新增：IKnowledgeBaseStore
internal/pkg/
├── middleware/
│   └── parent_user.go              # 新增：ParentUserOnly 中间件
├── model/
│   ├── chatbot.go                  # 新增：ChatbotConfig, ChatbotKnowledgeBase, ChatbotSession, ChatbotMessage
│   └── knowledge_base.go           # 新增：KnowledgeBase, KnowledgeBaseDocument
├── errno/
│   └── config.go                   # 新增：配置相关错误码
```

### 4.2 Store 接口

新增 store 注册到 `IStore` 接口和 `datastore` 实现：

```go
// store/store.go — IStore 新增方法
ChatbotConfig() IChatbotConfigStore
ChatbotSession() IChatbotSessionStore
KnowledgeBase() IKnowledgeBaseStore
```

#### IChatbotConfigStore

```go
type IChatbotConfigStore interface {
    Create(ctx context.Context, config *model.ChatbotConfig) error
    Get(ctx context.Context, id uint) (*model.ChatbotConfig, error)
    List(ctx context.Context, userID uint, offset, limit int) ([]model.ChatbotConfig, int64, error)
    Update(ctx context.Context, config *model.ChatbotConfig) error
    Delete(ctx context.Context, id uint) error
    UpdateStatus(ctx context.Context, id uint, status string) error

    // KB 挂载
    MountKnowledgeBases(ctx context.Context, chatbotID uint, kbIDs []uint) error
    UnmountKnowledgeBase(ctx context.Context, chatbotID uint, kbID uint) error
    ListMountedKBs(ctx context.Context, chatbotID uint) ([]model.KnowledgeBase, error)
    UnmountAllByKB(ctx context.Context, kbID uint) error  // KB 删除时级联解挂

    // C 端查询
    // ListPublishedByOwner 查询指定 owner（父用户）已发布的 chatbot
    // C 端子用户调用时，biz 层负责传入 user.ParentUserID（而非子用户自身 ID）
    ListPublishedByOwner(ctx context.Context, ownerUserID uint) ([]model.ChatbotConfig, error)
}
```

#### IChatbotSessionStore

```go
type IChatbotSessionStore interface {
    CreateSession(ctx context.Context, session *model.ChatbotSession) error
    GetSession(ctx context.Context, id uint) (*model.ChatbotSession, error)
    ListSessions(ctx context.Context, userID uint, offset, limit int) ([]model.ChatbotSession, int64, error)
    DeleteSession(ctx context.Context, id uint) error
    IncrementMessageCount(ctx context.Context, sessionID uint) error

    CreateMessage(ctx context.Context, msg *model.ChatbotMessage) error
    ListMessages(ctx context.Context, sessionID uint, offset, limit int) ([]model.ChatbotMessage, int64, error)
    DeleteMessagesBySession(ctx context.Context, sessionID uint) error  // 硬删除
    GetMaxSeq(ctx context.Context, sessionID uint) (int, error)
}
```

#### IKnowledgeBaseStore

```go
type IKnowledgeBaseStore interface {
    Create(ctx context.Context, kb *model.KnowledgeBase) error
    Get(ctx context.Context, id uint) (*model.KnowledgeBase, error)
    List(ctx context.Context, userID uint, offset, limit int) ([]model.KnowledgeBase, int64, error)
    Update(ctx context.Context, kb *model.KnowledgeBase) error
    Delete(ctx context.Context, id uint) error

    // 文档关联
    AddDocument(ctx context.Context, kbID uint, docID uint) error
    RemoveDocument(ctx context.Context, kbID uint, docID uint) error
    ListDocuments(ctx context.Context, kbID uint) ([]model.KnowledgeDocument, error)
    ListDocumentIDsByKBs(ctx context.Context, kbIDs []uint) ([]uint, error)  // ChatStream 用：批量查所有挂载 KB 的 document IDs
}
```

### 4.3 Biz 层接口

#### IChatbotBiz

```go
type IChatbotBiz interface {
    // ===== B 端配置 =====
    CreateChatbot(ctx context.Context, userID uint, req *CreateChatbotReq) (*model.ChatbotConfig, error)
    GetChatbot(ctx context.Context, userID uint, id uint) (*ChatbotDetail, error)
    ListChatbots(ctx context.Context, userID uint, offset, limit int) ([]model.ChatbotConfig, int64, error)
    UpdateChatbot(ctx context.Context, userID uint, id uint, req *UpdateChatbotReq) error
    DeleteChatbot(ctx context.Context, userID uint, id uint) error
    UpdateStatus(ctx context.Context, userID uint, id uint, status string) error

    // ===== C 端使用 =====
    ListVisibleChatbots(ctx context.Context, user *model.User) ([]model.ChatbotConfig, error)
    CreateSession(ctx context.Context, userID uint, chatbotID uint) (*model.ChatbotSession, error)
    ListSessions(ctx context.Context, userID uint, offset, limit int) ([]model.ChatbotSession, int64, error)
    DeleteSession(ctx context.Context, userID uint, sessionID uint) error
    ListMessages(ctx context.Context, userID uint, sessionID uint, offset, limit int) ([]model.ChatbotMessage, int64, error)
    ChatStream(ctx context.Context, userID uint, sessionID uint, message string, handler StreamHandler) error
}
```

所有权校验硬规则：所有接受 `userID` + `id` 参数的方法，第一步查记录校验 `record.UserID == userID`（B 端）或 `session.UserID == userID`（C 端），不匹配返回 `errno.ErrForbidden`。

#### IKnowledgeBaseBiz

```go
type IKnowledgeBaseBiz interface {
    Create(ctx context.Context, userID uint, req *CreateKBReq) (*model.KnowledgeBase, error)
    Get(ctx context.Context, userID uint, id uint) (*KBDetail, error)
    List(ctx context.Context, userID uint, offset, limit int) ([]KBWithDocCount, int64, error)
    Update(ctx context.Context, userID uint, id uint, req *UpdateKBReq) error
    Delete(ctx context.Context, userID uint, id uint) error  // 事务：先 UnmountAllByKB，再软删 KB
    AddDocument(ctx context.Context, userID uint, kbID uint, file multipart.File, header *multipart.FileHeader) (*model.KnowledgeDocument, error)
    RemoveDocument(ctx context.Context, userID uint, kbID uint, docID uint) error
}
```

`AddDocument` 内部流程：
1. 校验 KB 所有权：`kb.UserID == userID`
2. 调用现有文档上传链路（复用 SalesRAG Ingest 的底层文件上传+解析+向量化）
3. 创建 `KnowledgeBaseDocument` 关联

`RemoveDocument`：校验所有权 → 硬删 `KnowledgeBaseDocument` 关联行 → 文档本体保留（可能被其他 KB 引用）

跨用户安全校验：`AddDocument` 时检查 `document.UserID == kb.UserID`，防止关联他人文档。

#### ISopBiz 扩展

在现有 `biz/sop/sop.go` 中新增方法（不修改现有方法签名）：

```go
// 新增到 ISopBiz
CreateTemplateByUser(ctx context.Context, userID uint, req *CreateTemplateByUserReq) (*model.SopTemplate, error)
ListTemplatesByCreator(ctx context.Context, creatorID uint, offset, limit int) ([]model.SopTemplate, int64, error)
PublishTemplate(ctx context.Context, userID uint, templateID uint) error
UnpublishTemplate(ctx context.Context, userID uint, templateID uint) error
```

`PublishTemplate` 流程：
1. 校验 `template.CreatorUserID == userID`
2. 设 `publish_status = published`
3. 调用 `store.Customer().GrantTemplateToAllSubUsers(userID, templateID)` 自动授权

`UnpublishTemplate` 流程：
1. 校验所有权
2. 设 `publish_status = offline`
3. 不撤销已有权限（已在执行的 run 可完成）

新增 store 方法：
```go
// store/customer.go 新增
GrantTemplateToAllSubUsers(ctx context.Context, parentUserID uint, templateID uint) error
// 实现：查 user WHERE parent_user_id = parentUserID → 批量插入 user_template_permission
```

现有 `CreateRun` 增加校验：
```go
// biz/sop/sop.go CreateRun 新增检查
if template.CreatorUserID != nil && template.PublishStatus != SopPublishStatusPublished {
    return nil, errno.ErrTemplateNotPublished
}
```

### 4.4 ChatStream 核心流程

```
ChatStream(ctx, userID, sessionID, message, handler)
│
├─ 1. 查 session → 校验 session.UserID == userID
├─ 2. 查 chatbot_config → 校验 status == published（或 draft 且 user 是创建者）
├─ 3. 积分校验：用户余额足够（调用 ICreditBiz）
├─ 4. 查挂载的 KB：chatbot_knowledge_base → knowledge_base_ids
├─ 5. 查 KB 关联的 document IDs：
│     store.KnowledgeBase().ListDocumentIDsByKBs(kbIDs)
│     → 过滤：仅 knowledge_document.status = 'COMPLETED' 的文档
├─ 6. 向量检索（两层策略）：
│     ├─ 优先：SQLiteVec KNN + document_id IN (...) 过滤（如支持）
│     └─ 降级：全局 top-K*3 → 应用层按 document_id 过滤 → 取 top-K
│     → 返回 top-K chunks
├─ 7. 组装 messages：
│     ├─ system: chatbot.SystemPrompt + "\n\n参考资料：\n" + chunks context
│     ├─ history: 最近 N 条 chatbot_message（按 Seq 排序）
│     └─ user: 当前消息
├─ 8. Langfuse trace（见 §5）
├─ 9. 调用 LLM（通过现有封装层 internal/pkg/llm/ 或 biz/volc/）
│     → SSE stream tokens → handler 回调
├─ 10. 保存消息：
│      ├─ user message（Seq = maxSeq + 1）
│      └─ assistant message（Seq = maxSeq + 2）
│      └─ IncrementMessageCount（+2）
├─ 11. 扣积分：创建 UsageRecord（stream 完成后扣费，中断不扣）
└─ 12. Langfuse: EndGeneration（token counts）
```

无 KB 挂载时跳过步骤 4-6，仅用 system_prompt + history + user message 直接调用 LLM。

### 4.5 ParentUserOnly 中间件

```go
// internal/pkg/middleware/parent_user.go

func ParentUserOnly() gin.HandlerFunc {
    return func(c *gin.Context) {
        // 从 context 读取已有的 user 对象（AuthMiddleware 已加载），不额外查 DB
        user, exists := c.Get("current_user")
        if !exists {
            core.WriteResponse(c, errno.ErrTokenInvalid, nil)
            c.Abort()
            return
        }
        u := user.(*model.User)
        if u.ParentUserID != nil {
            core.WriteResponse(c, errno.ErrForbidden.SetMessage("仅限主账号操作"), nil)
            c.Abort()
            return
        }
        c.Next()
    }
}
```

---

## 5. Langfuse Trace 拓扑

### Chatbot 对话 Trace

```
Trace: chatbot-chat
├── TraceID: langfuse.TraceID()
├── Name: "chatbot-chat"
├── UserID: userID (string)
├── Tags: ["chatbot", chatbot.Name]
├── Input: {"message": userMessage, "chatbot_id": chatbotID, "session_id": sessionID}
├── Metadata: {"chatbot_name": chatbot.Name, "knowledge_base_ids": kbIDs}
│
├── Span: context-assembly
│   ├── Span: vector-retrieval
│   │   └── Input/Output: query → retrieved chunk IDs + scores
│   └── Span: prompt-construction
│       └── Output: assembled messages array
│
└── Generation: chatbot-llm-call
    ├── Model: {配置中的模型名}
    ├── Input: assembled messages
    ├── Output: assistant response
    └── Usage: {prompt_tokens, completion_tokens}
```

错误时记录 generation（output 为 error 信息），遵循 `.claude/rules/ai-service.md`。

RAG 检索为 Span（非 Generation） — 不涉及 LLM API 调用。

---

## 6. 前端设计（numind-web-v3）

### 6.1 路由

```typescript
// router/index.ts 新增

// B 端配置中心（ParentUser 守卫）
{
  path: '/config',
  component: () => import('@/views/config/ConfigLayout.vue'),
  meta: { requiresAuth: true, requiresParent: true },
  children: [
    { path: 'chatbots', component: () => import('@/views/config/ChatbotList.vue') },
    { path: 'chatbots/:id/edit', component: () => import('@/views/config/ChatbotEdit.vue') },
    { path: 'sop-templates', component: () => import('@/views/config/SopTemplateList.vue') },
    { path: 'sop-templates/:id/edit', component: () => import('@/views/config/SopTemplateEdit.vue') },
    { path: 'knowledge-bases', component: () => import('@/views/config/KnowledgeBaseList.vue') },
    { path: 'knowledge-bases/:id', component: () => import('@/views/config/KnowledgeBaseDetail.vue') },
  ]
}

// C 端 Chatbot 对话
{
  path: '/chatbot/:id',
  component: () => import('@/views/chatbot/ChatbotChat.vue'),
  meta: { requiresAuth: true }
}
```

### 6.2 路由守卫

```typescript
router.beforeEach((to) => {
  if (to.meta.requiresParent) {
    const userStore = useUserStore()
    if (!userStore.isParentUser) return { path: '/' }  // 子用户重定向首页
  }
})

// userStore
const isParentUser = computed(() =>
  user.value?.parent_user_id == null
)
```

### 6.3 侧边栏

AppSidebar 中根据 `isParentUser` 条件渲染"配置中心"菜单项：

```typescript
// 菜单配置
const menuItems = computed(() => {
  const items = [...baseItems]
  if (userStore.isParentUser) {
    items.push({
      label: '配置中心',
      icon: 'Settings',
      path: '/config/chatbots',
      children: [
        { label: '智能体管理', path: '/config/chatbots' },
        { label: 'SOP 管理', path: '/config/sop-templates' },
        { label: '知识库管理', path: '/config/knowledge-bases' },
      ]
    })
  }
  return items
})
```

### 6.4 新增 Pinia Store

```typescript
// stores/config.ts — B 端配置状态
export const useConfigStore = defineStore('config', () => {
  const chatbots = ref<ChatbotConfig[]>([])
  const sopTemplates = ref<SopTemplate[]>([])
  const knowledgeBases = ref<KnowledgeBase[]>([])
  const loading = ref(false)

  async function fetchChatbots(offset: number, limit: number) { ... }
  async function createChatbot(req: CreateChatbotReq) { ... }
  async function updateChatbot(id: number, req: UpdateChatbotReq) { ... }
  async function deleteChatbot(id: number) { ... }
  async function updateChatbotStatus(id: number, status: string) { ... }
  // ... SOP 和 KB 同理

  return { chatbots, sopTemplates, knowledgeBases, loading, ... }
})

// stores/chatbot.ts — C 端对话状态
export const useChatbotStore = defineStore('chatbot', () => {
  const sessions = ref<ChatbotSession[]>([])
  const currentSession = ref<ChatbotSession | null>(null)
  const messages = ref<ChatbotMessage[]>([])
  const streaming = ref(false)

  async function fetchSessions(offset: number, limit: number) { ... }
  async function createSession(chatbotId: number) { ... }
  async function deleteSession(id: number) { ... }
  async function fetchMessages(sessionId: number, offset: number, limit: number) { ... }
  async function sendMessage(sessionId: number, message: string) { ... } // SSE

  return { sessions, currentSession, messages, streaming, ... }
})
```

### 6.5 页面规格

#### 管理页面（B 端）— 表格布局

**智能体管理（ChatbotList.vue）：**
- 表格列：名称、状态（draft/published/offline badge）、知识库数、创建时间、操作
- 操作：编辑、测试对话（跳转 `/chatbot/:id`，仅 draft/published 状态可用）、发布/下线、删除（确认对话框）
- 顶部：新建按钮
- 4 状态处理：loading 骨架屏 / empty 引导+创建按钮 / error 重试 / success 正常渲染

**智能体编辑（ChatbotEdit.vue）：**
- 表单：名称（必填）、描述、头像上传、系统提示词（textarea，大文本框）、知识库选择（多选下拉）
- 验证：blur 时校验，提交按钮 disabled 直到必填项填完
- 提交：loading 态 → 成功 toast → 返回列表

**SOP 管理（SopTemplateList.vue）：**
- 表格列：名称、步骤数、状态、创建时间、操作
- 操作：编辑（跳转编辑页）、发布/下线、删除

**SOP 编辑（SopTemplateEdit.vue）：**
- 左侧：步骤列表（可拖拽排序，+ 添加步骤按钮，最多 20 步）
- 右侧：当前选中步骤的提示词编辑区（textarea）
- 顶部：模板名称/描述编辑 + 保存按钮

**知识库管理（KnowledgeBaseList.vue）：**
- 表格列：名称、文档数、创建时间、操作
- 操作：查看详情、删除（确认对话框，提示将解除所有 chatbot 挂载）

**知识库详情（KnowledgeBaseDetail.vue）：**
- 文档列表（名称、大小、状态 badge、上传时间、操作：移除）
- 上传区（拖拽或点击上传，复用现有上传组件）
- 显示文档解析进度（PENDING → PARSING → EMBEDDING → COMPLETED）

#### C 端页面

**首页展示：**
- 智能体区：卡片网格（头像、名称、描述），数据源 `GET /v1/chatbot/list`
- SOP 区：卡片网格（名称、描述、步骤数），数据源现有 `/v1/sop/templates`
- 各区域空状态处理

**Chatbot 对话页（ChatbotChat.vue）：**
- 布局：类 SalesRAG 对话界面
  - 左侧：会话列表（创建新会话、切换会话、删除会话）
  - 右侧：对话区（消息列表 + 输入框）
- 消息渲染：Markdown + 流式打字效果
- 输入：文本输入框 + 发送按钮（Enter 发送）
- 流式响应：SSE EventSource，streaming 状态显示 loading 指示器
- 积分不足：拦截提示（复用现有 InsufficientCreditsDialog）

### 6.6 API 层

```typescript
// api/config.ts
export const createChatbot = (data: CreateChatbotReq) => request.post('/v1/config/chatbots', data)
export const listChatbots = (offset: number, limit: number) => request.get('/v1/config/chatbots', { params: { offset, limit } })
// ... 其他 CRUD

// api/chatbot.ts
export const listVisibleChatbots = () => request.get('/v1/chatbot/list')
export const createSession = (chatbotId: number) => request.post('/v1/chatbot/sessions', { chatbot_id: chatbotId })
export const listSessions = (offset: number, limit: number) => request.get('/v1/chatbot/sessions', { params: { offset, limit } })
export const listMessages = (sessionId: number, offset: number, limit: number) =>
  request.get(`/v1/chatbot/sessions/${sessionId}/messages`, { params: { offset, limit } })
// chat 使用 EventSource，不走 axios
```

---

## 7. 错误码（新增）

```go
// internal/pkg/errno/config.go

var (
    ErrChatbotNotFound       = &Errno{Code: 110001, Message: "智能体不存在"}
    ErrChatbotNotPublished   = &Errno{Code: 110002, Message: "智能体未发布"}
    ErrKnowledgeBaseNotFound = &Errno{Code: 110003, Message: "知识库不存在"}
    ErrMaxNodesExceeded      = &Errno{Code: 110004, Message: "SOP步骤数已达上限(20)"}
    ErrTemplateNotPublished  = &Errno{Code: 110005, Message: "SOP模板未发布"}
    ErrSessionNotFound       = &Errno{Code: 110006, Message: "对话会话不存在"}
    ErrNotParentUser         = &Errno{Code: 110007, Message: "仅限主账号操作"}
)
```

---

## 8. Migration SQL

```sql
-- migrations/20260409_000001_self_service_config.sql

-- 知识库
CREATE TABLE IF NOT EXISTS knowledge_base (
    id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id INT UNSIGNED NOT NULL,
    name VARCHAR(100) NOT NULL,
    description VARCHAR(1024) DEFAULT '',
    status VARCHAR(20) NOT NULL DEFAULT 'active',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    deleted_at DATETIME DEFAULT NULL,
    INDEX idx_kb_user_id (user_id),
    INDEX idx_kb_deleted_at (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS knowledge_base_document (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    knowledge_base_id INT UNSIGNED NOT NULL,
    document_id INT UNSIGNED NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_kbd_kb_doc (knowledge_base_id, document_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 智能体
CREATE TABLE IF NOT EXISTS chatbot_config (
    id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id INT UNSIGNED NOT NULL,
    name VARCHAR(100) NOT NULL,
    description VARCHAR(1024) DEFAULT '',
    avatar VARCHAR(500) DEFAULT '',
    system_prompt LONGTEXT NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'draft',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    deleted_at DATETIME DEFAULT NULL,
    INDEX idx_cc_user_status (user_id, status),
    INDEX idx_cc_deleted_at (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS chatbot_knowledge_base (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    chatbot_id INT UNSIGNED NOT NULL,
    knowledge_base_id INT UNSIGNED NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_ckb_chatbot_kb (chatbot_id, knowledge_base_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 对话
CREATE TABLE IF NOT EXISTS chatbot_session (
    id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id INT UNSIGNED NOT NULL,
    chatbot_id INT UNSIGNED NOT NULL,
    title VARCHAR(200) DEFAULT '',
    status VARCHAR(20) NOT NULL DEFAULT 'active',
    message_count INT NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    deleted_at DATETIME DEFAULT NULL,
    INDEX idx_cs_user_chatbot (user_id, chatbot_id),
    INDEX idx_cs_deleted_at (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS chatbot_message (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    session_id INT UNSIGNED NOT NULL,
    user_id INT UNSIGNED NOT NULL,
    role VARCHAR(20) NOT NULL,
    content LONGTEXT,
    thinking LONGTEXT,
    trace_id VARCHAR(100) DEFAULT '',
    seq INT NOT NULL DEFAULT 0,
    prompt_tokens INT NOT NULL DEFAULT 0,
    completion_tokens INT NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_cm_session_seq (session_id, seq),
    INDEX idx_cm_user_id (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- SOP 模板扩展
ALTER TABLE sop_template ADD COLUMN creator_user_id INT UNSIGNED DEFAULT NULL;
ALTER TABLE sop_template ADD COLUMN publish_status VARCHAR(20) DEFAULT '';
ALTER TABLE sop_template ADD INDEX idx_st_creator (creator_user_id);

-- Feature permission seed
INSERT INTO user_feature_permission (parent_user_id, sub_user_id, feature_key, created_at, updated_at)
SELECT id, 0, 'self_service_config', NOW(), NOW()
FROM user WHERE parent_user_id IS NULL
ON DUPLICATE KEY UPDATE updated_at = NOW();
-- 注意：上面是示例 seed，实际需要通过 admin 手动授权给指定 B 端客户
```

---

## 9. 边界情况处理汇总

| 场景 | 处理方式 |
|------|---------|
| B 端删除知识库，已挂载到 chatbot | biz 层事务：先硬删 `chatbot_knowledge_base` 关联行，再软删 KB |
| B 端下线 SOP，C 端正在执行 | 正在执行的 run 允许完成；`CreateRun` 检查 `publish_status != published` 时拒绝新 run |
| C 端积分不足 | ChatStream 步骤 3 拦截，复用现有积分不足逻辑 |
| B 端未创建任何配置 | C 端首页对应区域显示空状态（引导文案） |
| 知识库文档解析未完成就挂载到 chatbot | 允许挂载；ChatStream 步骤 5 仅查 `status = COMPLETED` 的文档 |
| SOP 步骤超过 20 个 | `CreateNode` biz 层拒绝，返回 `ErrMaxNodesExceeded` |
| SSE 连接中断 | 不扣积分（stream 完成后才扣费）。Langfuse generation 在 defer 中调用 `EndGeneration`：正常完成记 token counts，中断时记 `output: {"error": "client disconnected"}` |
| 删除对话会话 | 软删 session + 硬删 messages（追加型表，无需保留） |
| 父用户测试 draft chatbot | 允许（`ListVisibleChatbots` 对父用户返回全部状态） |
| 子用户尝试访问 /config/* | `ParentUserOnly` 中间件拦截，返回 403 |
| 父用户删除 chatbot 后已有 sessions | sessions 保留（chatbot 软删除），ListSessions 正常展示历史会话，但无法发起新对话（CreateSession 检查 chatbot 存在性） |
| SalesRAG 删除底层文档 | chatbot 检索时查 `knowledge_document.status = COMPLETED`，已删除文档（deleted_at 非空）被 GORM 自动过滤，不影响 chatbot |
