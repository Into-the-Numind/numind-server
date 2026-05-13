# Chatbot 会话改名与置顶 — S2 Spec

**Feature ID:** `chatbot-session-rename-pin`
**Track:** Standard (轻量 MVP)
**Repos:** numind-server + numind-web-v3
**Date:** 2026-05-13
**Approach:** A (极简 MVP, 1-1.5 d)

---

## §0 背景与边界

S1 proposal `numind-server/proposals/chatbot-session-rename-pin-proposal.md` 已锁定 Approach A：

- **范围**：用户端 chatbot 对话页 (`numind-web-v3/src/views/chatbot/ChatbotChat.vue`) 左侧会话列表，每条 session 支持改名 + 置顶
- **DB 改动**：仅新增 `chatbot_session.pinned_at TIMESTAMP NULL`，复用现有 `title` 字段做改名
- **新 API 端点**：2 个（rename / pin），1 个改造（ListSessions 加可选 `chatbot_id` 参数）
- **不在范围**：SalesView 销售对话 / SOP 运行历史 / 管理端 / search / 跨 chatbot 全局视图 / 置顶硬上限 / 错误码集中 / 菜单组件抽离（详 S1 §4.6）

**S1 锁定的 3 个决策**：
- D1 作用域 = **per-chatbot**（后端 ListSessions 加可选 chatbot_id 参数；向后兼容：缺参数保持现状）
- D2 改名/置顶/取消置顶 **不更新 `updated_at`**（用 GORM `UpdateColumn` 显式列更新）
- D3 v1 **不设置顶硬上限**

---

## §1 数据模型变更

### 1.1 Migration（forward + rollback 双脚本）

#### Forward：`migrations/20260513_120000_add_chatbot_session_pinned_at.sql`

```sql
-- 添加 pinned_at 字段：NULL = 未置顶；非 NULL = 置顶时间（也用作置顶组内排序）
ALTER TABLE chatbot_session
    ADD COLUMN pinned_at TIMESTAMP NULL DEFAULT NULL COMMENT '置顶时间，NULL=未置顶' AFTER message_count;

-- 复合索引：覆盖列表查询的 WHERE (user_id, chatbot_id) + ORDER BY (pinned_at IS NULL, pinned_at, updated_at)
-- 注：pinned_at 列单独建索引收益边际，因为 WHERE 已经按 user_id+chatbot_id 大幅过滤；
-- 当前 idx_cs_user_chatbot 已覆盖过滤，排序在过滤后的小结果集上扫描成本极低。本次不新增 index。
```

**为什么不加 index on pinned_at**：单 user × 单 chatbot 下的 session 数量在量级上通常 < 200（极端情况 < 1000）。`idx_cs_user_chatbot` 已经把 query 缩小到该量级，ORDER BY 在 in-memory 排序无瓶颈。如未来观测到 perf 问题再单独优化。

#### Rollback：`migrations/20260513_120000_add_chatbot_session_pinned_at_rollback.sql`

```sql
ALTER TABLE chatbot_session DROP COLUMN pinned_at;
```

### 1.2 GORM Model 改动

`internal/pkg/model/chatbot.go` 第 47-58 行的 `ChatbotSession` struct 增加一字段：

```go
type ChatbotSession struct {
    ID           uint           `gorm:"primaryKey" json:"id"`
    CreatedAt    time.Time      `json:"created_at"`
    UpdatedAt    time.Time      `json:"updated_at"`
    DeletedAt    gorm.DeletedAt `gorm:"index" json:"deleted_at"`
    UserID       uint           `gorm:"not null;index:idx_cs_user_chatbot" json:"user_id"`
    ChatbotID    uint           `gorm:"not null;index:idx_cs_user_chatbot" json:"chatbot_id"`
    Title        string         `gorm:"size:200" json:"title"`
    Status       string         `gorm:"size:20;not null;default:'active'" json:"status"`
    MessageCount int            `gorm:"default:0" json:"message_count"`
    PinnedAt     *time.Time     `gorm:"default:null" json:"pinned_at,omitempty"` // 新增
}
```

**类型选择 `*time.Time`**：
- 区分"未置顶"(`nil`/JSON `null`) vs "置顶且时间为 t"(`*t`)
- GORM 自动处理 nullable timestamp
- JSON 序列化用 `omitempty` 避免 unmarshal 时把 `null` 当 zero time
- **不**用 `default:true` 类陷阱（database.md §6 记录的 bool 陷阱不适用 nullable timestamp）

---

## §2 后端 API 契约

### 2.1 PUT `/v1/chatbot/sessions/:id/rename` — 重命名

**Request**:
```http
PUT /v1/chatbot/sessions/123/rename
Authorization: Bearer <user_token>
Content-Type: application/json

{
  "title": "客户 A 试用咨询"
}
```

**Validation**:
- `title` trim 后长度 ∈ [1, 200]
- trim 后为空字符串 → `ErrBind` "标题不能为空"
- 长度 > 200 字节 → `ErrBind` "标题最长 200 字符"

**Response 200**:
```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "id": 123,
    "title": "客户 A 试用咨询"
  }
}
```

**Error responses**:
- 400 `ErrBind`：参数绑定失败 / title 空 / title 超长
- 401 `ErrTokenInvalid`：未登录
- 403 `ErrForbidden`：session 存在但非本人所有
- 404 `ErrSessionNotFound`：session 不存在或已软删

### 2.2 PUT `/v1/chatbot/sessions/:id/pin` — 置顶 / 取消置顶

**Request**:
```http
PUT /v1/chatbot/sessions/123/pin
Authorization: Bearer <user_token>
Content-Type: application/json

{
  "pinned": true
}
```

**Semantics**:
- `pinned: true` → 写入 `pinned_at = NOW()`（即便已置顶也刷新 `pinned_at` 到当前时间 — 实现"最近一次置顶操作"语义，新置顶的 session 会移到置顶组顶部）
- `pinned: false` → 写入 `pinned_at = NULL`

**Response 200**:
```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "id": 123,
    "pinned_at": "2026-05-13T10:00:00+08:00"
  }
}
```

`pinned_at` 字段格式：**RFC3339**（Go `time.Time` 的默认 JSON 序列化格式 = `time.RFC3339Nano`）。取消置顶时 `pinned_at` 字段为 `null`。

**Error responses**:
- 400 `ErrBind`：参数绑定失败（pinned 字段缺失）
- 401 `ErrTokenInvalid`
- 403 `ErrForbidden`
- 404 `ErrSessionNotFound`

### 2.3 GET `/v1/chatbot/sessions` — 改造（加可选 `chatbot_id` 参数）

**Request**:
```http
GET /v1/chatbot/sessions?chatbot_id=42&offset=0&limit=20
Authorization: Bearer <user_token>
```

**分页参数**：现有接口实际使用 `offset` / `limit` query 参数（见 `controller.go:281-288` `parsePagination` 函数）。本 spec 全文统一使用 `offset/limit`。

**新增 query 参数**: `chatbot_id` (可选, uint)
- 提供 → 仅返回该 chatbot 下的 session
- 不提供 / 空 → **保持现有行为**：返回该用户所有 chatbot 的 session 混合列表（向后兼容）

**Sort order**:
- 置顶组在前，组内按 `pinned_at DESC`
- 非置顶组在后，组内按 `updated_at DESC`
- SQL: `ORDER BY pinned_at IS NULL ASC, pinned_at DESC, updated_at DESC`

**Response 200** (字段不变，新增 `pinned_at`):
```json
{
  "code": 0,
  "data": {
    "list": [
      {
        "id": 5,
        "user_id": 100,
        "chatbot_id": 42,
        "title": "客户 A 咨询",
        "status": "active",
        "message_count": 12,
        "pinned_at": "2026-05-13T10:00:00+08:00",
        "created_at": "...",
        "updated_at": "..."
      }
    ],
    "total": 1
  }
}
```

---

## §3 后端实现设计

### 3.1 Store 层 — `internal/numind/store/chatbot_session.go`

新增 3 个方法到 `IChatbotSessionStore` 接口：

```go
type IChatbotSessionStore interface {
    // ... 现有 9 个方法保留 ...

    // 新增
    UpdateTitle(ctx context.Context, sessionID uint, title string) error
    SetPinnedAt(ctx context.Context, sessionID uint, pinnedAt *time.Time) error
    ListSessionsByChatbot(ctx context.Context, userID, chatbotID uint, offset, limit int) ([]model.ChatbotSession, int64, error)
}
```

**`UpdateTitle` 实现**：
```go
func (s *chatbotSessionStore) UpdateTitle(ctx context.Context, sessionID uint, title string) error {
    // 用 UpdateColumn 显式列更新，绕开 GORM updated_at 自动刷新（D2 决策）
    result := s.db.WithContext(ctx).
        Model(&model.ChatbotSession{}).
        Where("id = ?", sessionID).
        UpdateColumn("title", title)
    if result.Error != nil {
        return result.Error
    }
    if result.RowsAffected == 0 {
        return gorm.ErrRecordNotFound  // 由 biz 层翻译为 ErrSessionNotFound
    }
    return nil
}
```

**`SetPinnedAt` 实现**：
```go
func (s *chatbotSessionStore) SetPinnedAt(ctx context.Context, sessionID uint, pinnedAt *time.Time) error {
    // 同样用 UpdateColumn，pinnedAt = nil 即写入 NULL
    result := s.db.WithContext(ctx).
        Model(&model.ChatbotSession{}).
        Where("id = ?", sessionID).
        UpdateColumn("pinned_at", pinnedAt)
    if result.Error != nil {
        return result.Error
    }
    if result.RowsAffected == 0 {
        return gorm.ErrRecordNotFound
    }
    return nil
}
```

**`ListSessionsByChatbot` 实现**（修复 cross-chatbot perf bug 顺带价值）：
```go
func (s *chatbotSessionStore) ListSessionsByChatbot(ctx context.Context, userID, chatbotID uint, offset, limit int) ([]model.ChatbotSession, int64, error) {
    var sessions []model.ChatbotSession
    var total int64

    query := s.db.WithContext(ctx).Model(&model.ChatbotSession{}).
        Where("user_id = ? AND chatbot_id = ?", userID, chatbotID)

    if err := query.Count(&total).Error; err != nil {
        return nil, 0, err
    }

    if err := query.Offset(offset).Limit(limit).
        Order("pinned_at IS NULL ASC, pinned_at DESC, updated_at DESC").
        Find(&sessions).Error; err != nil {
        return nil, 0, err
    }

    return sessions, total, nil
}
```

**注意**：现有 `ListSessions(userID, offset, limit)` 方法 **保留不动**，向后兼容 cross-chatbot 调用方（若无）。

### 3.2 Biz 层 — `internal/numind/biz/chatbot/chatbot.go`

新增 3 个方法到 `IChatbotBiz` 接口：

```go
type IChatbotBiz interface {
    // ... 现有方法保留 ...

    // 新增
    RenameSession(ctx context.Context, userID, sessionID uint, title string) error
    PinSession(ctx context.Context, userID, sessionID uint, pinned bool) (*time.Time, error)
    ListSessionsByChatbot(ctx context.Context, userID, chatbotID uint, offset, limit int) ([]model.ChatbotSession, int64, error)
}
```

**`RenameSession` 实现要点**：

```go
func (b *chatbotBiz) RenameSession(ctx context.Context, userID, sessionID uint, title string) error {
    // 1. trim + length check
    title = strings.TrimSpace(title)
    if len(title) == 0 {
        return errno.ErrBind.SetMessage("标题不能为空")
    }
    if len(title) > 200 {
        return errno.ErrBind.SetMessage("标题最长 200 字符")
    }

    // 2. ownership check（防止用户改他人 session）
    session, err := b.ds.ChatbotSession().GetSession(ctx, sessionID)
    if err != nil {
        if errors.Is(err, gorm.ErrRecordNotFound) {
            return errno.ErrSessionNotFound
        }
        return fmt.Errorf("RenameSession: %w", err)
    }
    if session.UserID != userID {
        return errno.ErrForbidden
    }

    // 3. update title (UpdateColumn 不刷新 updated_at)
    if err := b.ds.ChatbotSession().UpdateTitle(ctx, sessionID, title); err != nil {
        if errors.Is(err, gorm.ErrRecordNotFound) {
            return errno.ErrSessionNotFound  // 并发软删的兜底
        }
        return fmt.Errorf("RenameSession: %w", err)
    }
    return nil
}
```

**`PinSession` 实现要点**：

```go
func (b *chatbotBiz) PinSession(ctx context.Context, userID, sessionID uint, pinned bool) (*time.Time, error) {
    // 1. ownership check
    session, err := b.ds.ChatbotSession().GetSession(ctx, sessionID)
    if err != nil {
        if errors.Is(err, gorm.ErrRecordNotFound) {
            return nil, errno.ErrSessionNotFound
        }
        return nil, fmt.Errorf("PinSession: %w", err)
    }
    if session.UserID != userID {
        return nil, errno.ErrForbidden
    }

    // 2. compute new pinned_at
    var newPinnedAt *time.Time
    if pinned {
        now := time.Now()
        newPinnedAt = &now
    }
    // else newPinnedAt stays nil → store writes NULL

    // 3. write (UpdateColumn, no updated_at refresh)
    if err := b.ds.ChatbotSession().SetPinnedAt(ctx, sessionID, newPinnedAt); err != nil {
        if errors.Is(err, gorm.ErrRecordNotFound) {
            return nil, errno.ErrSessionNotFound
        }
        return nil, fmt.Errorf("PinSession: %w", err)
    }

    return newPinnedAt, nil
}
```

**`ListSessionsByChatbot` 实现**（仅薄包装 store 方法，无业务逻辑）：

```go
func (b *chatbotBiz) ListSessionsByChatbot(ctx context.Context, userID, chatbotID uint, offset, limit int) ([]model.ChatbotSession, int64, error) {
    sessions, total, err := b.ds.ChatbotSession().ListSessionsByChatbot(ctx, userID, chatbotID, offset, limit)
    if err != nil {
        return nil, 0, fmt.Errorf("ListSessionsByChatbot: %w", err)
    }
    return sessions, total, nil
}
```

### 3.3 Controller 层 — `internal/numind/controller/v1/chatbot/chatbot.go`

新增 2 个 handler + 修改 1 个 handler：

**`RenameSession` handler**:
```go
type renameSessionRequest struct {
    Title string `json:"title" binding:"required"`
}

// RenameSession 重命名对话会话
func (ctrl *ChatbotController) RenameSession(c *gin.Context) {
    user := middleware.GetCurrentUser(c)
    if user == nil {
        core.WriteResponse(c, errno.ErrTokenInvalid, nil)
        return
    }

    id, ok := parseUintParam(c, "id")
    if !ok {
        return
    }

    var req renameSessionRequest
    if err := c.ShouldBindJSON(&req); err != nil {
        core.WriteResponse(c, errno.ErrBind.SetMessage(err.Error()), nil)
        return
    }

    if err := ctrl.chatbotBiz.RenameSession(c, user.ID, id, req.Title); err != nil {
        core.WriteResponse(c, err, nil)
        return
    }

    core.WriteResponse(c, nil, gin.H{"id": id, "title": strings.TrimSpace(req.Title)})
}
```

**`PinSession` handler**:
```go
type pinSessionRequest struct {
    Pinned *bool `json:"pinned" binding:"required"`  // 指针避免 binding 跳过 false
}

// PinSession 置顶 / 取消置顶对话会话
func (ctrl *ChatbotController) PinSession(c *gin.Context) {
    user := middleware.GetCurrentUser(c)
    if user == nil {
        core.WriteResponse(c, errno.ErrTokenInvalid, nil)
        return
    }

    id, ok := parseUintParam(c, "id")
    if !ok {
        return
    }

    var req pinSessionRequest
    if err := c.ShouldBindJSON(&req); err != nil {
        core.WriteResponse(c, errno.ErrBind.SetMessage(err.Error()), nil)
        return
    }

    pinnedAt, err := ctrl.chatbotBiz.PinSession(c, user.ID, id, *req.Pinned)
    if err != nil {
        core.WriteResponse(c, err, nil)
        return
    }

    core.WriteResponse(c, nil, gin.H{"id": id, "pinned_at": pinnedAt})
}
```

**关键设计**：`Pinned *bool` 用指针类型 + `binding:"required"`。如果用 `Pinned bool`，则 `{"pinned": false}` 会被 Gin 当作 zero value 跳过校验（与 `{}` 不可区分）。

**`ListSessions` handler 改造**（最小侵入）:
```go
func (ctrl *ChatbotController) ListSessions(c *gin.Context) {
    user := middleware.GetCurrentUser(c)
    if user == nil {
        core.WriteResponse(c, errno.ErrTokenInvalid, nil)
        return
    }

    offset, limit := parsePagination(c)

    // 新增：可选 chatbot_id 参数
    var (
        list  []model.ChatbotSession
        total int64
        err   error
    )

    chatbotIDStr := c.Query("chatbot_id")
    if chatbotIDStr != "" {
        chatbotID, parseErr := strconv.ParseUint(chatbotIDStr, 10, 64)
        if parseErr != nil {
            core.WriteResponse(c, errno.ErrBind.SetMessage("chatbot_id 必须为正整数"), nil)
            return
        }
        list, total, err = ctrl.chatbotBiz.ListSessionsByChatbot(c, user.ID, uint(chatbotID), offset, limit)
    } else {
        // 向后兼容路径：保持现有 cross-chatbot 行为
        list, total, err = ctrl.chatbotBiz.ListSessions(c, user.ID, offset, limit)
    }

    if err != nil {
        core.WriteResponse(c, err, nil)
        return
    }

    core.WriteResponse(c, nil, gin.H{"list": list, "total": total})
}
```

### 3.4 Router 注册 — `internal/numind/router.go`

在 `chatbotGroup` (line ~302-310) 增加 2 行：

```go
chatbotGroup := authGroup.Group("/chatbot")
{
    // ... 现有路由保留 ...
    chatbotGroup.POST("/sessions", chatbotCtrl.CreateSession)
    chatbotGroup.GET("/sessions", chatbotCtrl.ListSessions)
    chatbotGroup.DELETE("/sessions/:id", chatbotCtrl.DeleteSession)
    chatbotGroup.GET("/sessions/:id/messages", chatbotCtrl.ListMessages)
    chatbotGroup.POST("/sessions/:id/chat", chatbotCtrl.Chat)

    // 新增 2 行
    chatbotGroup.PUT("/sessions/:id/rename", chatbotCtrl.RenameSession)
    chatbotGroup.PUT("/sessions/:id/pin", chatbotCtrl.PinSession)
}
```

---

## §4 排序 SQL 兼容性验证

### 4.1 MySQL 8 `ORDER BY pinned_at IS NULL ASC, pinned_at DESC, updated_at DESC`

**语义验证**:
- `pinned_at IS NULL ASC` → false (0) 在前，true (1) 在后 → **非 NULL 行在前**（即置顶组在前）
- 同一组内（IS NULL 结果相同）按 `pinned_at DESC` → 置顶时间晚的在前
- 非置顶组（pinned_at IS NULL）的 `pinned_at DESC` 全部 NULL，比较稳定按 `updated_at DESC` → 活跃时间晚的在前

**MySQL 8 NULL 排序行为**：
- MySQL 中 NULL 在 ASC 排序中**默认排在最前**
- 但 `IS NULL` 表达式返回布尔（0/1），不是 NULL 本身 — 这是 deterministic 的，不依赖 NULLS FIRST/LAST dialect 差异
- GORM 默认连接 `?charset=utf8mb4&parseTime=True&loc=Local`，MySQL 服务端排序行为一致

**验证 query**:
```sql
SELECT id, title, pinned_at, updated_at FROM chatbot_session
WHERE user_id = 1 AND chatbot_id = 42 AND deleted_at IS NULL
ORDER BY pinned_at IS NULL ASC, pinned_at DESC, updated_at DESC
LIMIT 20 OFFSET 0;
```

**S5 验证 case** (3 行测试数据):
- A: pinned_at = '2026-05-13 10:00:00', updated_at = '2026-05-13 08:00:00'
- B: pinned_at = '2026-05-13 09:00:00', updated_at = '2026-05-13 12:00:00'
- C: pinned_at = NULL, updated_at = '2026-05-13 13:00:00'

**期望顺序**: A, B, C（A 置顶时间最新 → B 置顶时间次之 → C 非置顶但最近活跃）

### 4.2 PostgreSQL 兼容性

本项目用 MySQL 8.0（CLAUDE.md §1）。不需要考虑 PG。

---

## §5 前端设计

### 5.1 API 层 — `numind-web-v3/src/api/chatbot.ts`

新增 2 个 API 方法 + 改造 1 个：

```typescript
// 改造：listChatbotSessions 加可选 chatbotId 参数（offset/limit 与现有接口契约一致）
export const listChatbotSessions = (offset = 0, limit = 20, chatbotId?: number) => {
  const params: Record<string, number> = { offset, limit }
  if (chatbotId) params.chatbot_id = chatbotId
  return request.get<Resp>('/v1/chatbot/sessions', { params })
}

// 新增
export const renameChatbotSession = (id: number, title: string) =>
  request.put<Resp>(`/v1/chatbot/sessions/${id}/rename`, { title })

export const pinChatbotSession = (id: number, pinned: boolean) =>
  request.put<Resp>(`/v1/chatbot/sessions/${id}/pin`, { pinned })
```

### 5.2 TypeScript 类型 — `numind-web-v3/src/types/config.ts`

`ChatbotSession` interface 加 `pinned_at` 字段：

```typescript
export interface ChatbotSession {
  id: number
  user_id: number
  chatbot_id: number
  title: string
  status: string
  message_count: number
  pinned_at?: string | null  // 新增：ISO8601 字符串 or null
  created_at: string
  updated_at: string
}
```

### 5.3 Pinia Store — `numind-web-v3/src/stores/chatbot.ts`

#### 5.3.1 改造 `fetchSessions`：增加 `chatbotId` 必传参数

**签名变更**：从 `fetchSessions(offset, limit)` → `fetchSessions(chatbotId, offset, limit)`。

```typescript
async function fetchSessions(chatbotId: number, offset = 0, limit = 20) {
  try {
    const res = await listChatbotSessions(offset, limit, chatbotId)
    const data = (res as any)?.data as { list: ChatbotSession[]; total: number } | undefined
    sessions.value = data?.list ?? []
    sessionsTotal.value = data?.total ?? 0
  } catch (e) {
    console.error('[chatbot] fetchSessions failed:', e)
  }
}
```

#### 5.3.1.1 chatbotId 来源策略 — store 加 `currentChatbotId` 状态

为了让 store 内部的 `createSession` / `deleteSession` / `sendMessage` 在自己内部能调用 `fetchSessions(chatbotId)`，store 需要持有"当前 chatbot"上下文：

```typescript
const currentChatbotId = ref<number | null>(null)

// ChatbotChat.vue onMounted 时设置：store.currentChatbotId = chatbotId.value
// 切换 chatbot 路由参数变化时更新
```

#### 5.3.1.2 所有调用方迁移清单（完整）

**`ChatbotChat.vue` 内调用**：
- `onMounted` 初始化：先 `store.currentChatbotId = chatbotId.value` 再 `await store.fetchSessions(chatbotId.value)`
- 切换 chatbot 路由参数变化时：**S4 实施需新增** 一个 watcher（当前文件中不存在此 watcher），监听 `chatbotId` (即 `route.params.id`) 变化时同步更新：

  ```typescript
  watch(chatbotId, async (newId) => {
    store.currentChatbotId = newId
    await store.fetchSessions(newId)
  })
  ```

**`stores/chatbot.ts` 内部调用（3 处需迁移）**：
- `createSession(chatbotId)`（现有 line ~63）：内部 `await fetchSessions()` → 改为 `await fetchSessions(chatbotId)`（chatbotId 是该函数已有的入参）
- `deleteSession(id)`（现有 line ~82）：内部 `await fetchSessions()` → 改为 `await fetchSessions(currentChatbotId.value ?? 0)`；若 `currentChatbotId` 为 null（不应发生）则降级跳过刷新
- `sendMessage(text)`（现有 line ~209 finally 块刷新）：同上 `await fetchSessions(currentChatbotId.value ?? 0)`

**S4 实施时 reviewer 必查**：grep `store.fetchSessions(`、`fetchSessions(`、`await fetchSessions(` 三个 pattern 验证全部已迁移。

#### 5.3.2 新增 2 个 actions（pessimistic UI — 先 API 成功再更新本地）

```typescript
async function renameSession(id: number, title: string): Promise<boolean> {
  try {
    await renameChatbotSession(id, title)
    // pessimistic UI: API 成功后才更新本地 title（失败时本地状态不变）
    const s = sessions.value.find((x) => x.id === id)
    if (s) s.title = title
    return true
  } catch (e) {
    console.error('[chatbot] renameSession failed:', e)
    return false
  }
}

async function togglePin(id: number, currentPinnedAt: string | null | undefined): Promise<boolean> {
  const newPinned = !currentPinnedAt
  try {
    const res = await pinChatbotSession(id, newPinned)
    const newPinnedAt = (res as any)?.data?.pinned_at as string | null
    // pessimistic UI: API 成功后才更新本地 pinned_at 并重排（失败时本地状态不变）
    const s = sessions.value.find((x) => x.id === id)
    if (s) s.pinned_at = newPinnedAt
    sortSessionsLocally()
    return true
  } catch (e) {
    console.error('[chatbot] togglePin failed:', e)
    return false
  }
}

// 本地排序：与后端 SQL 排序逻辑一致
function sortSessionsLocally() {
  sessions.value.sort((a, b) => {
    const aPinned = !!a.pinned_at
    const bPinned = !!b.pinned_at
    if (aPinned !== bPinned) return aPinned ? -1 : 1
    if (aPinned) {
      return new Date(b.pinned_at!).getTime() - new Date(a.pinned_at!).getTime()
    }
    return new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime()
  })
}
```

return 对象增加 `renameSession`, `togglePin`。

### 5.4 ChatbotChat.vue 改动

#### 5.4.1 保留 client-side filter（防御性 dedupe）

后端在 `chatbot_id` 参数下已经做过滤，但 `chatbotSessions` 计算属性的 `.filter` **保留**作为防御性写法 — 应对其他代码路径意外触发 cross-chatbot fetchSessions 的边缘情况。

```typescript
// line ~50-52, 保持原样不变
const chatbotSessions = computed(() =>
  store.sessions.filter((s) => s.chatbot_id === chatbotId.value)
)
```

#### 5.4.2 fetchSessions 调用方更新

`ChatbotChat.vue` 内对 `store.fetchSessions()` 的所有调用需补传 `chatbotId.value`：
- `onMounted` 初始化
- 路由 `chatbotId` 变化时的 watcher
- （store 内部 `createSession` / `deleteSession` / `sendMessage` 的调用迁移详 §5.3.1.2，不在 ChatbotChat.vue 内修改）

切换 chatbot 时同步更新 `store.currentChatbotId = chatbotId.value`。

#### 5.4.3 hover 显示「⋯」按钮 + dropdown 菜单

每条 session item 模板：

```vue
<div
  class="session-item"
  :class="{ 'session-item--pinned': session.pinned_at }"
  @click="store.switchSession(session)"
>
  <MessageSquare class="session-icon" :size="16" />
  <span class="session-title">{{ session.title || '新对话' }}</span>
  <span v-if="session.pinned_at" class="session-pinned-indicator" title="已置顶">📌</span>
  <button
    class="session-more-btn"
    @click.stop="openMenu(session.id)"
    aria-label="更多操作"
  >⋯</button>

  <div
    v-if="openMenuSessionId === session.id"
    v-click-outside="closeMenu"
    class="session-dropdown"
  >
    <button class="dropdown-item" @click.stop="onRenameClick(session)">重命名</button>
    <button class="dropdown-item" @click.stop="onTogglePinClick(session)">
      {{ session.pinned_at ? '取消置顶' : '置顶' }}
    </button>
    <button class="dropdown-item dropdown-item--danger" @click.stop="onDeleteClick(session.id)">删除</button>
  </div>
</div>
```

**关键 script setup additions（菜单状态）**:
```typescript
const openMenuSessionId = ref<number | null>(null)

function openMenu(id: number) {
  openMenuSessionId.value = openMenuSessionId.value === id ? null : id
}
function closeMenu() {
  openMenuSessionId.value = null
}

async function onTogglePinClick(session: ChatbotSession) {
  closeMenu()
  const ok = await store.togglePin(session.id, session.pinned_at)
  if (!ok) toast.error('操作失败，请重试')
}

// onRenameClick / confirmRename / renameModalOpen 等改名相关的 ref 和函数详见 §5.4.4
```

#### 5.4.4 改名弹窗（ChatbotChat.vue 内联实现，参考 sales/RenameSessionModal.vue 模式）

**S2 设计决策**：经查证，现有 `ConfirmModal.vue`（line 40-69）模板**无 `<slot>` 定义**，Props 必需 `message: string`，不支持自定义 body 内容。同时 `sales/RenameSessionModal.vue` 已存在但属于 sales 模块（销售知识库）专用。

**决策**：在 `ChatbotChat.vue` 内联实现一个简化的 inline RenameModal — **仅参考 `sales/RenameSessionModal.vue` 的模板结构和 CSS 类用法，不 import 该组件**（避免跨模块依赖 + 满足 MVP 不抽离原则）。视觉一致性通过 `@/assets/styles/sales-modal.css` 共享 CSS 类来保证（与 sales 模块视觉统一，不改造 ConfirmModal）。

**模板（直接在 ChatbotChat.vue template 中插入，紧邻已有的删除 ConfirmModal）**：

```vue
<!-- 改名弹窗（inline，复用 sales-modal.css 视觉） -->
<Teleport to="body">
  <div class="modal-overlay" :class="{ open: renameModalOpen }">
    <div
      class="modal-card modal-card-simple"
      role="dialog"
      aria-modal="true"
      @keydown.escape="closeRenameModal"
    >
      <div class="modal-header">
        <span class="modal-title">重命名对话</span>
      </div>
      <div class="modal-body-simple">
        <input
          ref="renameInputRef"
          v-model="renameInputValue"
          type="text"
          maxlength="200"
          class="form-input"
          placeholder="对话名称"
          @keydown.enter="confirmRename"
        />
      </div>
      <div class="modal-footer">
        <button class="btn-secondary" @click="closeRenameModal">取消</button>
        <button class="btn-primary" @click="confirmRename"><span>保存</span></button>
      </div>
    </div>
  </div>
</Teleport>
```

**script setup 配套**（同 §5.4.3 的 setup 一起声明）：

```typescript
import { nextTick } from 'vue'

const renameModalOpen = ref(false)
const renameInputRef = ref<HTMLInputElement | null>(null)
const renameInputValue = ref('')
const renameTargetSession = ref<ChatbotSession | null>(null)

function closeRenameModal() {
  renameModalOpen.value = false
  renameTargetSession.value = null
  renameInputValue.value = ''
}

async function onRenameClick(session: ChatbotSession) {
  closeMenu()
  renameTargetSession.value = session
  renameInputValue.value = session.title || ''
  renameModalOpen.value = true
  await nextTick()
  renameInputRef.value?.focus()
  renameInputRef.value?.select()
}

async function confirmRename() {
  if (!renameTargetSession.value) return
  const newTitle = renameInputValue.value.trim()
  if (!newTitle) {
    toast.error('标题不能为空')
    return
  }
  // 200 字 maxlength 已经在 input 层限制；后端会再次 ErrBind 校验作为兜底
  const ok = await store.renameSession(renameTargetSession.value.id, newTitle)
  if (ok) {
    closeRenameModal()
  } else {
    toast.error('重命名失败，请重试')
  }
}
```

**style 添加** (script + scoped style 之外，全局 style import)：
```vue
<style scoped>
@import '@/assets/styles/sales-modal.css';
</style>
```

若 ChatbotChat.vue 已有 `<style scoped>`，把 `@import` 放在该块顶部。

#### 5.4.5 删除按钮迁移

现有 hover trash icon → 移除；改为通过下拉菜单中"删除"项触发现有 `deleteConfirmId` 流程：
```typescript
function onDeleteClick(id: number) {
  closeMenu()
  deleteConfirmId.value = id  // 触发现有的 ConfirmModal
}
```

#### 5.4.6 CSS 改动要点

```scss
.session-item {
  position: relative;  // 让 .session-more-btn 绝对定位

  &:hover .session-more-btn {
    opacity: 1;
  }

  // 置顶 session 左侧加 2px primary 色边框，作为微弱视觉强调（不喧宾夺主）
  &--pinned {
    border-left: 2px solid var(--color-primary);
    padding-left: 6px;  // 补偿边框宽度，保持内容对齐
  }
}

.session-more-btn {
  position: absolute;
  right: 8px;
  top: 50%;
  transform: translateY(-50%);
  width: 22px;
  height: 22px;
  border: none;
  background: transparent;
  cursor: pointer;
  opacity: 0;
  transition: opacity 150ms;
  color: var(--text-secondary);

  &:hover {
    color: var(--text-primary);
    background: var(--bg-hover);
    border-radius: 4px;
  }
}

.session-dropdown {
  position: absolute;
  right: 8px;
  top: calc(100% + 4px);
  z-index: 100;
  background: var(--bg-elevated);
  border: 1px solid var(--border);
  border-radius: 6px;
  box-shadow: var(--shadow-medium);
  min-width: 120px;
  padding: 4px 0;
}

.dropdown-item {
  display: block;
  width: 100%;
  text-align: left;
  padding: 8px 12px;
  border: none;
  background: transparent;
  cursor: pointer;
  font-size: 14px;
  color: var(--text-primary);

  &:hover {
    background: var(--bg-hover);
  }

  &--danger {
    color: var(--color-danger);
  }
}

.session-pinned-indicator {
  font-size: 12px;
  margin-left: 4px;
}

.rename-input {
  width: 100%;
  padding: 8px 12px;
  border: 1px solid var(--border);
  border-radius: 4px;
  font-size: 14px;

  &:focus {
    border-color: var(--color-primary);
    outline: none;
  }
}
```

**置顶组与非置顶组的视觉分隔**：v1 不加分隔线（D3 决策下的 MVP 选择 — 若后续观察到拥挤再 v2 加）。

### 5.5 点击外部关闭菜单（document click listener）

不引入 `v-click-outside` 自定义指令（项目目前无此指令，引入是过度设计）。在 `ChatbotChat.vue` setup 中直接挂载 document-level click listener：

```typescript
import { onMounted, onBeforeUnmount } from 'vue'

function handleDocClick(e: MouseEvent) {
  // 如果点击不在任何 dropdown 或 more-btn 上，关闭菜单
  const target = e.target as HTMLElement
  if (!target.closest('.session-dropdown') && !target.closest('.session-more-btn')) {
    closeMenu()
  }
}

onMounted(() => document.addEventListener('click', handleDocClick))
onBeforeUnmount(() => document.removeEventListener('click', handleDocClick))
```

---

## §6 边界 case 全清单

| # | 场景 | 期望行为 | 实现位置 |
|---|------|---------|---------|
| EC-1 | 改名传入超长 title (> 200 bytes) | 400 `ErrBind` "标题最长 200 字符" | biz.RenameSession |
| EC-2 | 改名传入纯空白（trim 后为空）| 400 `ErrBind` "标题不能为空" | biz.RenameSession |
| EC-3 | 改名传入合法纯 emoji 或纯标点 | 通过，不限制字符种类 | （无特殊处理）|
| EC-4 | 改名/置顶/取消置顶请求时 session 已被软删 | 404 `ErrSessionNotFound`（GetSession 返回 ErrRecordNotFound）| biz.RenameSession / biz.PinSession |
| EC-5 | 同一 session 短时间内两次置顶（双击防抖前）| 两次 UPDATE 都执行，最后一次 `pinned_at` 胜出；语义安全 | store.SetPinnedAt |
| EC-6 | 用户 A 改/置顶用户 B 的 session | 403 `ErrForbidden`（ownership 检查）| biz 两个方法 |
| EC-7 | 改名/置顶/取消置顶成功后 `updated_at` | **保持不变**；用 GORM `UpdateColumn` 显式列更新绕开自动刷新 | store.UpdateTitle / SetPinnedAt |
| EC-8 | 跨设备同步（A 设备置顶 → B 设备）| 最终一致，无 websocket。B 设备下次 `fetchSessions()` 时看到 | （接受，不实现）|
| EC-9 | ListSessions 不传 `chatbot_id` | 保持现有 cross-chatbot 行为（向后兼容）| controller.ListSessions |
| EC-10 | ListSessions 传非数字 `chatbot_id` | 400 `ErrBind` "chatbot_id 必须为正整数" | controller.ListSessions |
| EC-11 | 父子账户跨账户访问 | session `user_id` 维度天然隔离，ownership check 阻断 | biz |
| EC-12 | 并发改名（两 tab）| last-write-wins；无 ETag | （接受）|
| EC-13 | 已被置顶的 session 被软删 | 软删自然不显示；`pinned_at` 字段在 DB 保留（未来恢复时仍有效）| 现有 gorm.DeletedAt |
| EC-14 | 重复置顶（已置顶再点击置顶）| `pinned_at` 刷新为 NOW()，session 移到置顶组顶部 | biz.PinSession |
| EC-15 | `pinned: null` 或缺失 pinned 字段 | 400 `ErrBind`（Gin binding required 校验 + 指针类型）| controller.PinSession |
| EC-16 | 删除按钮（迁入菜单后）| 现有 ConfirmModal 删除流程 + 现有 `deleteSession` API | ChatbotChat.vue |
| EC-17 | 软删 session 的 `pinned_at` 是否清空 | **保留**（不主动清空）；与 DeletedAt 自然过滤一致 | （无特殊处理）|

---

## §7 测试策略

### 7.1 后端单元测试（Go）

新增 `internal/numind/store/chatbot_session_test.go`：
- `TestUpdateTitle_Success`
- `TestUpdateTitle_NotFound`
- `TestUpdateTitle_DoesNotRefreshUpdatedAt`（关键 — D2 验证）
- `TestSetPinnedAt_SetNow_Then_Clear`
- `TestSetPinnedAt_DoesNotRefreshUpdatedAt`
- `TestListSessionsByChatbot_OrderPinnedFirst`
- `TestListSessionsByChatbot_PinnedAtDescWithinGroup`
- `TestListSessionsByChatbot_UpdatedAtDescAmongUnpinned`

新增 `internal/numind/biz/chatbot/chatbot_test.go`（或扩展现有）：
- `TestRenameSession_TrimEmpty_ReturnsBindError`
- `TestRenameSession_OverLimit_ReturnsBindError`
- `TestRenameSession_NotOwner_ReturnsForbidden`
- `TestRenameSession_SoftDeleted_ReturnsNotFound`
- `TestPinSession_NotOwner_ReturnsForbidden`
- `TestPinSession_PinThenUnpin_AffectsField`

### 7.2 前端单元测试（Vitest）

`stores/chatbot.spec.ts`：
- `togglePin` API 成功后本地排序结果符合期望（pessimistic UI 验证）
- `renameSession` API 失败时本地 title **不更新**（pessimistic UI — 失败状态不写入 store）
- `sortSessionsLocally` 多组合测试（全置顶 / 全非置顶 / 混合）

### 7.3 E2E（Playwright，S5 阶段**必做**）

鉴于前端交互复杂（hover + dropdown + inline modal + pessimistic UI 排序）+ 后端 ListSessions API 契约变化，S5 至少跑一条 Playwright E2E 关键路径（**非可选**）：

`numind-web-v3/e2e/chatbot-session-rename-pin.spec.ts`：
- 登录 → 进入某个 chatbot 对话页 → hover session → 点「⋯」→ 「重命名」→ 输入新名 → 保存 → 验证列表显示新名
- 选另一条 session → 「置顶」→ 验证移到列表顶部
- 重复置顶 → 验证 `pinned_at` 刷新（新置顶移到置顶组最顶）
- 取消置顶 → 验证回到非置顶组按 `updated_at` 位置

---

## §8 不在本 feature 范围内（重申 S1 §4.6）

❌ SalesView 销售对话页的 rename/pin
❌ SOP 运行历史的 rename/pin
❌ 全文搜索 session 内容
❌ 跨 chatbot 全局 session 索引页
❌ 置顶硬上限 + 折叠组（v1 不做；v2 视拥挤情况再加）
❌ SessionContextMenu 抽离为可复用组件（v1 内联）
❌ 新增 ErrPinLimitExceeded errno（无上限就不需要）
❌ 改名审计日志
❌ Websocket 实时同步
❌ 置顶组与非置顶组的 UI 视觉分隔线（v1 无分隔）

---

## §9 S3 plan 阶段待答开放问题

1. **task 拆分粒度**：建议 4-6 个 task：T1 migration + model；T2 store 3 方法 + 单元测试；T3 biz 3 方法 + 单元测试；T4 controller + router；T5 前端 api + store + types；T6 前端 ChatbotChat.vue 改造（含 inline RenameModal）+ 测试 — 由 S3 final lock

## §9.1 已在 S2 解决的设计决策（reviewer 提的开放问题前置答案）

| 原"待答"问题 | S2 解决方案 |
|--------------|------------|
| ConfirmModal 是否支持 `#body` slot？ | **不支持**（验证：模板 line 40-69 无 `<slot>`，Props `message` 必需）→ **§5.4.4 改为 ChatbotChat.vue 内联 inline RenameModal**（参考 `sales/RenameSessionModal.vue` 模式 + 复用 `sales-modal.css`），不抽组件、不污染 sales 模块、不改造 ConfirmModal |
| S5 验证策略 | §7.3 锁定 Playwright E2E **必做**（非可选），覆盖 rename + pin + unpin + 重排 4 个关键路径 |

---

## §9.2 S2 review 记录

| 轮次 | Reviewer | 结果 | 修复 |
|------|---------|------|------|
| Round 1 | 独立 Sonnet subagent | PASS_WITH_CONCERNS — 2 P0 + 4 P1 + 3 P2 | **2 P0 + 4 P1 + 4 P2 全修**：(P0-1) ConfirmModal 不支持 slot → 改为 inline RenameModal 参考 sales/RenameSessionModal.vue 模式（§5.4.4）；(P0-2) v-model:visible 错误 → inline 方案自然解决；(P1-1) §5.4.1 标题误导 → 改为"保留 client-side filter（防御性 dedupe）"；(P1-2) fetchSessions 调用方迁移不完整 → §5.3.1.1 加 currentChatbotId 状态 + §5.3.1.2 列全 3 处 store 内部调用；(P1-3) optimistic→pessimistic 注释纠正；(P1-4) page/page_size → offset/limit 统一；(P2-1) `.session-item--pinned` 加左 2px primary 边框；(P2-2) §5.5 标题改为"document click listener"；(P2-3) `pinned_at` 明确 RFC3339；(P2-4) §7.3 Playwright E2E 改为 S5 必做 |
| Round 2 | 独立 Sonnet subagent | PASS_WITH_CONCERNS — 0 P0 + 1 P1-NEW + 2 P2-NEW | **1 P1-NEW + 2 P2-NEW 全修**：(P1-NEW) §7.2 Vitest 测试描述用词"optimistic UI"与设计 pessimistic 矛盾 → 改为明确 pessimistic UI 表达；(P2-NEW-1) §5.3.1.2 切换 chatbot watcher 不存在需 S4 新增 → 加完整 watch 代码示例；(P2-NEW-2) §5.4.4 `sales/RenameSessionModal.vue` 仅参考视觉模式不 import → 明确写"仅参考模板结构和 CSS 类用法，不 import 该组件" |

---

## §10 与现有 feature 的关系

| Feature | 状态 | 与本 feature 的关系 |
|---------|------|---------------------|
| `sop-chatbot-visibility-scope` | S4 进行中 (10/23 tasks) | 不耦合：visibility 作用在 chatbot list（决定用户能不能看到 chatbot），本 feature 作用在 session list（一个具体 chatbot 内的会话）。两层逻辑正交。共用 develop manifest 时通过 worktree 隔离 |
| `child-run-permission` | 已上线 | 不耦合：本 feature 是 session-level metadata 操作，不触发运行权限检查 |
| `membership-credits-redesign` | S5 进行中 | 不耦合：本 feature 不触及积分/会员状态 |
