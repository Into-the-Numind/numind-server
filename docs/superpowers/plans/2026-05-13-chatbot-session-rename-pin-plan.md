# Chatbot 会话改名与置顶 — 实施 Plan (S3)

- **Feature ID**: `chatbot-session-rename-pin`
- **NDF Stage**: S3 (Implementation Plan)
- **Created**: 2026-05-13
- **Approach**: A (极简 MVP, 1-1.5 d)
- **Source Spec**: `numind-server/docs/superpowers/specs/2026-05-13-chatbot-session-rename-pin-design.md`
- **Source Proposal-PRD**: `numind-server/proposals/chatbot-session-rename-pin-proposal.md`

---

## §0 前置同步

```bash
# 后端
cd numind-server && git checkout develop && git pull
git checkout -b feature/chatbot-session-rename-pin

# 前端
cd ../numind-web-v3 && git checkout develop && git pull
git checkout -b feature/chatbot-session-rename-pin
```

> **NDF §3 S4 protocol 提醒**：按 `subagent-driven-development` 顺序执行 8 个 task；**每个 task 完成后**按 NDF Rule 6 两阶段 review (spec compliance + code quality)；P0/P1 必修；**0 must-fix 才能进下一 task**。多仓库功能后端 task 排前端 task 之前（NDF §3 S4 第 5 条）。

> **Holistic reviewer P1 指引（来自 S2 完成时的第三只眼审查）**：
> - **P1-1**：`stores/chatbot.ts` 的 `fetchSessions` 签名变更 `(offset, limit)` → `(chatbotId, offset, limit)` 是**破坏性变更**（不是后向兼容的 optional 参数）。Task 6 必须同步迁移**全部 3 处** store 内部调用 + ChatbotChat.vue 内调用。S4 reviewer 必查：`grep -rn "fetchSessions(" numind-web-v3/src/` 应该 0 处单参数旧调用。
> - **P1-2**：TypeScript `ChatbotSession` interface 新增 `pinned_at?: string | null` 字段是后续 task 的**前置依赖** — 必须在前端首个 task (Task 5) 里完成，否则 Task 6 / Task 7 会 type-check 失败。

---

## §1 Task 分解（共 7 实施 task + 1 S5 策略 task）

每个 task 独立可构建、可测试、可 commit；两阶段 review (spec compliance + code quality) 后才能启动下一个。

### Task 1 — DB migration + Go model（numind-server）

**目标**：新增 `chatbot_session.pinned_at TIMESTAMP NULL` 字段；GORM model 加 `PinnedAt *time.Time`。

**文件改动**：
- `numind-server/migrations/20260513_120000_add_chatbot_session_pinned_at.sql`（新建 forward）
- `numind-server/migrations/20260513_120000_add_chatbot_session_pinned_at_rollback.sql`（新建 rollback）
- `numind-server/internal/pkg/model/chatbot.go`（修改：ChatbotSession struct +1 字段）

**SQL（forward，spec §1.1）**：
```sql
ALTER TABLE chatbot_session
    ADD COLUMN pinned_at TIMESTAMP NULL DEFAULT NULL COMMENT '置顶时间，NULL=未置顶' AFTER message_count;
```

**SQL（rollback）**：
```sql
ALTER TABLE chatbot_session DROP COLUMN pinned_at;
```

**Model 改动（spec §1.2）**：
```go
type ChatbotSession struct {
    // ... 现有 9 字段保留 ...
    MessageCount int        `gorm:"default:0" json:"message_count"`
    PinnedAt     *time.Time `gorm:"default:null" json:"pinned_at,omitempty"` // 新增
}
```

**验收**：
- SQL forward 在 dev DB 运行成功；rollback 验证 DROP COLUMN 不报错（**二次 apply 验证幂等**：`ALTER ... ADD COLUMN` 不幂等，rerun 会报"Duplicate column"是预期；不用 IF NOT EXISTS）
- `go build ./...` PASS
- 现有所有行 `pinned_at` 自动为 NULL（DEFAULT NULL 行为）
- Migration 文件头部注释说明执行顺序（forward 在 model 改动之前 apply 到 DB）

**独立性**：纯 DDL/model，不依赖任何其他 task。是其他后端 task 的前置依赖。

---

### Task 2 — Store 层 3 方法 + 单元测试（numind-server）

**目标**：扩展 `IChatbotSessionStore` 加入 `UpdateTitle` / `SetPinnedAt` / `ListSessionsByChatbot` 三方法；不动现有 `ListSessions` 方法（向后兼容）。

**文件改动**：
- `numind-server/internal/numind/store/chatbot_session.go`（修改：interface + 实现 + 3 新方法）
- `numind-server/internal/numind/store/chatbot_session_test.go`（新建）

**新增方法签名**（spec §3.1）：
```go
type IChatbotSessionStore interface {
    // ... 现有 9 方法保留 ...
    UpdateTitle(ctx context.Context, sessionID uint, title string) error
    SetPinnedAt(ctx context.Context, sessionID uint, pinnedAt *time.Time) error
    ListSessionsByChatbot(ctx context.Context, userID, chatbotID uint, offset, limit int) ([]model.ChatbotSession, int64, error)
}
```

**关键实现要点**：
- `UpdateTitle` / `SetPinnedAt` **必须用 `UpdateColumn`**（spec §3.1 + spec D2），绕开 GORM 自动刷新 `updated_at`
- `UpdateColumn` 返回 `RowsAffected == 0` 时返回 `gorm.ErrRecordNotFound`（由 biz 层翻译为 `ErrSessionNotFound`）
- `ListSessionsByChatbot` SQL：`ORDER BY pinned_at IS NULL ASC, pinned_at DESC, updated_at DESC`

**测试用例（必须覆盖）**：
- `TestUpdateTitle_Success` — 正常修改
- `TestUpdateTitle_NotFound_ReturnsErrRecordNotFound` — RowsAffected == 0
- `TestUpdateTitle_DoesNotRefreshUpdatedAt` — **关键 D2 验证**（query updated_at 前后相等）
- `TestSetPinnedAt_SetThenClear` — pin → unpin 链路
- `TestSetPinnedAt_DoesNotRefreshUpdatedAt`
- `TestSetPinnedAt_NotFound`
- `TestListSessionsByChatbot_PinnedFirstThenUnpinned` — 3 行测试数据（spec §4.1）：A pinned 10:00 updated 08:00 / B pinned 09:00 updated 12:00 / C unpinned updated 13:00；期望顺序 A, B, C
- `TestListSessionsByChatbot_OnlyPinned_OrderByPinnedAtDesc`
- `TestListSessionsByChatbot_OnlyUnpinned_OrderByUpdatedAtDesc`
- `TestListSessionsByChatbot_FilteredByChatbotID` — 验证 chatbot_id WHERE 子句不返回其他 chatbot 的 session

**验收**：
- `go test ./internal/numind/store/...` PASS
- `task lint` 退出码 0
- 测试用 in-memory SQLite (`newTestDB(t)` 现有 helper)

**独立性**：依赖 Task 1 model 的 `PinnedAt` 字段。是 Task 3 的前置。

---

### Task 3 — Biz 层 3 方法 + 单元测试（numind-server）

**目标**：扩展 `IChatbotBiz` 加 `RenameSession` / `PinSession` / `ListSessionsByChatbot` 三方法；含校验 + ownership check + 错误码翻译。

**文件改动**：
- `numind-server/internal/numind/biz/chatbot/chatbot.go`（修改：interface + impl 加 3 方法）
- `numind-server/internal/numind/biz/chatbot/chatbot_test.go`（新建或扩展，加 6 测试用例）

**新增方法签名（spec §3.2）**：
```go
type IChatbotBiz interface {
    // ... 现有方法保留 ...
    RenameSession(ctx context.Context, userID, sessionID uint, title string) error
    PinSession(ctx context.Context, userID, sessionID uint, pinned bool) (*time.Time, error)
    ListSessionsByChatbot(ctx context.Context, userID, chatbotID uint, offset, limit int) ([]model.ChatbotSession, int64, error)
}
```

**关键实现要点**：
- `RenameSession`：trim → 长度 [1, 200] 校验 → ownership check (GetSession + session.UserID != userID → ErrForbidden) → UpdateTitle
- `PinSession`：ownership check → 计算 `*time.Time`（pinned=true 用 `time.Now()` 指针，false 用 nil） → SetPinnedAt → 返回 `*time.Time`
- `ListSessionsByChatbot`：薄包装 store 方法
- 错误码：复用现有 `errno.ErrBind` / `errno.ErrForbidden` / `errno.ErrSessionNotFound`（**无需新增 errno**）

**测试用例（必须覆盖）**：
- `TestRenameSession_TrimEmpty_ReturnsBindError` — 全空白
- `TestRenameSession_OverLimit_ReturnsBindError` — 201 字节
- `TestRenameSession_NotOwner_ReturnsForbidden`
- `TestRenameSession_SoftDeleted_ReturnsSessionNotFound` — Get 时 ErrRecordNotFound
- `TestPinSession_PinFirstTime` — 返回非 nil `*time.Time`
- `TestPinSession_Unpin` — pinned=false 返回 nil
- `TestPinSession_RepinRefreshesPinnedAt` — 重置 pinned_at 到 NOW()（spec EC-14）
- `TestPinSession_NotOwner_ReturnsForbidden`

**验收**：
- `go test ./internal/numind/biz/chatbot/...` PASS
- `task lint` 退出码 0

**独立性**：依赖 Task 2 的 store 方法。是 Task 4 的前置。

---

### Task 4 — Controller + Router + 端到端集成验证（numind-server）

**目标**：新增 2 个 controller handler (Rename + Pin) + 改造 1 个 (ListSessions 加 chatbot_id query 参数)；路由注册。

**文件改动**：
- `numind-server/internal/numind/controller/v1/chatbot/chatbot.go`（修改：+2 handler + 改 ListSessions）
- `numind-server/internal/numind/router.go`（修改：chatbotGroup 内 +2 行路由注册）

**Router 注册位置**：通过 `grep -n "chatbotGroup" numind-server/internal/numind/router.go` 定位（**不依赖 spec §3.4 写的 line 302-310**，因为 visibility-scope feature 的并行开发可能已经把行号推后）。

**新增端点**：
```go
chatbotGroup.PUT("/sessions/:id/rename", chatbotCtrl.RenameSession)
chatbotGroup.PUT("/sessions/:id/pin", chatbotCtrl.PinSession)
```

**改造的 ListSessions handler**（spec §3.3）：
- 加 `chatbot_id` 可选 query 参数解析
- 提供时调用 `ctrl.chatbotBiz.ListSessionsByChatbot(...)`；缺失保持现有 `ListSessions(...)` 行为

**关键实现要点**：
- `pinSessionRequest.Pinned` **必须用 `*bool` 指针 + `binding:"required"`**（spec §3.3）——防止 Gin 把 `false` 当 zero value 跳过 required 校验
- Controller 只做参数绑定 + biz 调用 + 响应；**禁止业务逻辑**（CLAUDE.md §2 controller 职责边界）
- Response 格式严格遵循 spec §2.1 / §2.2：`{"id": N, "title": "..."}` 和 `{"id": N, "pinned_at": "..." | null}`

**集成验证（手动 curl 或 Postman，dev DB）**：
```bash
# Rename
curl -X PUT "http://49.233.219.254:9091/v1/chatbot/sessions/1/rename" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"title": "客户A咨询"}'

# Pin
curl -X PUT "http://49.233.219.254:9091/v1/chatbot/sessions/1/pin" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"pinned": true}'

# Unpin
curl -X PUT "http://49.233.219.254:9091/v1/chatbot/sessions/1/pin" \
  -d '{"pinned": false}'

# List with chatbot_id
curl "http://49.233.219.254:9091/v1/chatbot/sessions?chatbot_id=42&offset=0&limit=20" \
  -H "Authorization: Bearer $TOKEN"
```

**验收**：
- `go build ./...` PASS
- `task lint` 退出码 0
- `go test ./...` 已通过 (无需 controller 单元测试，集成测试由 S5 Playwright 覆盖)
- 手动 curl 三个端点均返回 200 + 符合 spec response schema 的 JSON
- 错误 case 手动测试：传超长 title 返回 400 / 用别人的 session_id 返回 403 / 不存在 id 返回 404

**独立性**：依赖 Task 3 的 biz 方法 + Task 1 的 migration 已在 dev DB apply。是后端最后一个 task，**Task 4 commit 后后端全链路可用，前端 task 才能依赖 dev 后端**。

---

### Task 5 — TypeScript types + API 层（numind-web-v3）— **前端第一个 task，前端其他 task 的前置依赖**

**目标**：补 `ChatbotSession` interface 的 `pinned_at` 字段；api 层加 2 个新方法 + 改 1 个签名。

**文件改动**：
- `numind-web-v3/src/types/config.ts`（修改：ChatbotSession interface +pinned_at）
- `numind-web-v3/src/api/chatbot.ts`（修改：+ renameChatbotSession + pinChatbotSession；改 listChatbotSessions 签名加 chatbotId 参数）

**Types 改动（spec §5.2）**：
```typescript
export interface ChatbotSession {
  // ... 现有字段保留 ...
  message_count: number
  pinned_at?: string | null  // 新增：ISO8601/RFC3339 字符串 or null
  created_at: string
  updated_at: string
}
```

**API 层改动（spec §5.1）**：
```typescript
// 改造（offset/limit 与现有接口一致；chatbotId 为新增可选）
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

**验收**：
- `npm run type-check` 退出码 0（前端首个 task，types 改动不会破坏其他模块——因 `pinned_at` 是 optional 字段）
- `npm run lint` 退出码 0
- listChatbotSessions 现有调用方（如 stores/chatbot.ts:47 当前 `listChatbotSessions(offset, limit)`）**仍能编译通过**（chatbotId 是 optional，旧调用不传 = 后端走 cross-chatbot 兼容路径）

**独立性**：纯类型 + API 函数声明，不依赖其他 task；是 Task 6 / Task 7 的**关键前置**（无 `pinned_at` 字段则后续 task type-check 必失败）。

---

### Task 6 — Pinia Store 改造 + Vitest（numind-web-v3）

**目标**：(1) `fetchSessions` 签名破坏性变更；(2) 加 `currentChatbotId` 状态；(3) 加 `renameSession` / `togglePin` / `sortSessionsLocally` 三个新功能；(4) 迁移**全部 3 处** store 内部 `fetchSessions()` 调用。

**文件改动**：
- `numind-web-v3/src/stores/chatbot.ts`（修改：含 4 处改动）
- `numind-web-v3/src/stores/__tests__/chatbot.spec.ts`（新建或扩展）

**关键改动**（spec §5.3）：

1. **新状态**：`const currentChatbotId = ref<number | null>(null)` + 在 return 对象暴露
2. **fetchSessions 签名破坏性变更**：
   ```typescript
   async function fetchSessions(chatbotId: number, offset = 0, limit = 20) {
     // 旧签名 (offset, limit) 已不存在
   }
   ```
3. **3 处 store 内部调用迁移**：
   - `createSession(chatbotId)`（current line 63）：`await fetchSessions()` → `await fetchSessions(chatbotId)`
   - `deleteSession(id)`（current line 82）：`await fetchSessions()` → `await fetchSessions(currentChatbotId.value ?? 0)`
   - `sendMessage(text)`（current line 209 finally 块）：同 deleteSession
4. **新 actions**（pessimistic UI — 先 API 成功再更新本地）：
   - `renameSession(id, title)`
   - `togglePin(id, currentPinnedAt)`
   - `sortSessionsLocally()` 内部 helper

**【P0 review 修正点 — S2 holistic reviewer P1-1】**：
- `fetchSessions` 签名变更是**破坏性变更**（不是后向兼容）。S4 implementer 不要误以为是"加可选 chatbotId 参数"
- S4 reviewer 必查：在 numind-web-v3/src/ 全仓 grep：`fetchSessions(`、`store.fetchSessions(`、`await fetchSessions(`，**零处**单参数旧调用残留

**【P0 task 原子性边界 — S3 reviewer P1-1】fetchSessions 调用迁移在 T6 必须完整闭环**：

为保证 T6 commit 后 `npm run type-check` PASS（NDF Rule 9 task 原子性硬要求），**T6 必须同时迁移 ChatbotChat.vue 内现有的所有 `store.fetchSessions()` 旧签名调用**（不含 T7 的新 UI 改动）。具体：
- T6 在改 store `fetchSessions` 签名的同时，**一并修改** `ChatbotChat.vue` 中所有现有的旧签名调用（如 `onMounted` 中的 `store.fetchSessions()` → `store.fetchSessions(chatbotId.value)`；其他 deleteSession 等回调）
- ChatbotChat.vue 中**新增** chatbotId watcher（含 store.currentChatbotId 同步）以及 hover「⋯」menu / inline RenameModal 等**新 UI** 改动留给 T7
- T6 验收条件 `grep -rn "fetchSessions(" numind-web-v3/src/` 输出 0 处旧签名残留，**此时 T7 尚未开始也必须 PASS**

**T6 任务边界清单**：
- ✅ 在 T6 内：store.ts 加 currentChatbotId / 改 fetchSessions 签名 / 3 处 store 内部调用迁移 / 加 renameSession/togglePin/sortSessionsLocally / Vitest + **ChatbotChat.vue 内现有 fetchSessions 调用迁移**
- ❌ 不在 T6（留给 T7）：新增 chatbotId watcher / hover「⋯」按钮 + dropdown / inline RenameModal / 删除按钮菜单迁移 / 视觉 CSS

**测试用例（必须覆盖，stores/__tests__/chatbot.spec.ts）**：
- `renameSession API 成功后本地 title 更新（pessimistic 验证）`
- `renameSession API 失败时本地 title 不更新（pessimistic）`
- `togglePin pin 后本地 pinned_at 写入 + 排序后置顶组在前`
- `togglePin unpin 后本地 pinned_at 清空 + 排序后回到 updated_at 序列`
- `sortSessionsLocally 全置顶按 pinned_at DESC`
- `sortSessionsLocally 全非置顶按 updated_at DESC`
- `sortSessionsLocally 混合 — 置顶组在前`
- `fetchSessions(chatbotId) 携带 chatbot_id 参数调用 API`（mock API）

**验收**：
- `npm run lint` 退出码 0
- `npm run type-check` 退出码 0
- `npx vitest run src/stores/__tests__/chatbot.spec.ts` PASS
- `grep -rn "fetchSessions(" numind-web-v3/src/` 输出**仅**含新签名调用（chatbotId 在第一参数位置）

**独立性**：依赖 Task 5 的 types + api 函数。是 Task 7 的前置。

---

### Task 7 — ChatbotChat.vue 改造（numind-web-v3）

**目标**：(1) hover 显示「⋯」按钮 + dropdown 菜单；(2) 内联 inline RenameModal；(3) 加 chatbotId watcher；(4) 删除按钮迁入新菜单；(5) 调用方迁移到新 store action 签名。

**文件改动**：
- `numind-web-v3/src/views/chatbot/ChatbotChat.vue`（修改：template + script + style）

**改动清单**（spec §5.4 全部）：

**Template 改动**：
- 5.4.3 session item hover「⋯」按钮 + dropdown 菜单（重命名 / 置顶/取消置顶 / 删除）
- 5.4.4 新增 inline RenameModal（参考 sales/RenameSessionModal.vue 模板模式 + 复用 sales-modal.css；**不 import sales/RenameSessionModal.vue**）
- 5.4.5 现有 hover trash icon 移除（删除入口移入「⋯」菜单）

**Script setup 改动**：
- 5.4.3 加 `openMenuSessionId` ref + `openMenu / closeMenu / onTogglePinClick` 函数
- 5.4.4 加 `renameModalOpen / renameInputRef / renameInputValue / renameTargetSession` ref 状态 + `closeRenameModal / onRenameClick / confirmRename` 函数
- 5.4.5 新增 `onDeleteClick(id)` 函数复用现有 `deleteConfirmId` 流程
- 5.5 加 document click listener (`handleDocClick`) onMounted / onBeforeUnmount 生命周期

**关键新增 - chatbotId watcher (S2 holistic reviewer P2-NEW-1)**：
```typescript
// onMounted 时已设置 currentChatbotId 但路由变化需要 watcher
watch(chatbotId, async (newId) => {
  store.currentChatbotId = newId
  await store.fetchSessions(newId)
})
```

**fetchSessions 调用迁移（注意：基础迁移已在 T6 完成，T7 仅处理新增 watcher 相关）**：
- `onMounted` 初始化：T6 已迁移到 `store.fetchSessions(chatbotId.value)`；T7 在此前加 `store.currentChatbotId = chatbotId.value`
- **新增 watcher**（T7 唯一新增 fetchSessions 调用点）：

```typescript
watch(chatbotId, async (newId) => {
  store.currentChatbotId = newId
  await store.fetchSessions(newId)
})
```

**Client-side filter 保留语义说明（S3 reviewer P2-1 澄清）**：
- `chatbotSessions` computed 中现有的 `.filter(s => s.chatbot_id === chatbotId.value)` **保留**作为防御性 dedupe 写法（按 spec §5.4.1 锁定）
- 这与 PRD AC-3.4 "移除 client-side filter，保留可选 dedupe 防御"语义一致：后端 chatbot_id 参数下推到 DB 后该 filter 不再承担"过滤角色"，但保留作为应对其他代码路径意外触发 cross-chatbot fetch 的防御
- S4 reviewer 不要把这条 `.filter` 当成"AC-3.4 未落实"

**Style 改动**：
- 5.4.6 加 .session-more-btn / .session-dropdown / .dropdown-item / .session-pinned-indicator / .session-item--pinned CSS
- 顶部 import：`@import '@/assets/styles/sales-modal.css';`（如该 `<style scoped>` 块没有 import）

**【NDF §3 硬规则提醒】**：前端 UI bug **必须用 Playwright 诊断**，禁止直接静态推理（CLAUDE.md numind-web-v3/§6 + .claude/rules/testing.md §2.5）。完成实现后用 gstack `/qa` 或 Playwright 截图验证。

**验收**：
- `npm run lint` 退出码 0
- `npm run type-check` 退出码 0
- 现有 Vitest tests 不退化 (`npm run test`)
- **本地浏览器验证**（手动）：
  - hover session 出现「⋯」按钮 (150ms fade-in)
  - 点击「⋯」弹出菜单
  - 点击外部菜单关闭
  - 点重命名弹出输入框（自动 focus + select）
  - 改名成功后列表显示新名 + updated_at 不变化（DevTools Network 看 PUT response + 再 fetch sessions 验证）
  - 点置顶后立即移到列表顶部，左侧出现 2px primary 边框
  - 点取消置顶后回到原排序位置
  - 删除按钮在新菜单内，行为与旧版一致

**独立性**：依赖 Task 5 (types) + Task 6 (store actions)。是前端最后一个实施 task。

---

## §2 Task 依赖图

```
T1 (migration + model)
  ↓
T2 (store + tests)
  ↓
T3 (biz + tests)
  ↓
T4 (controller + router + 集成验证) ── 后端完成，dev 后端可用
  ↓
T5 (types + api 层) ── 前端前置依赖
  ↓
T6 (store + Vitest)
  ↓
T7 (ChatbotChat.vue) ── 前端完成
  ↓
T8 (S5 验证策略文档)
```

**关键约束**：
- T4 之前不要开始任何前端 task（前端依赖后端可用的 API）
- T5 之前不要开始 T6 / T7（types 是前置依赖）
- 全部为顺序执行（NDF §3 S4 不可并行）

---

## §3 Task 8 — S5 验证策略 (NDF Rule 10 强制末尾 task)

**目标**：写出 S5 阶段验证方案文档，明确选择 Playwright E2E vs gstack `/qa` vs 仅后端 TDD，并列出关键用户路径。

**T8 实施说明（S3 reviewer P2-2 澄清）**：S5 验证策略的**全部内容已在本 plan §3 后续条目（"验证方式"/"理由"/"关键用户路径"等）完整写出**。T8 的实际工作是：**将本节内容拷贝到新建的 markdown 文件并 commit**。不需要在 S4 阶段重新设计验证策略；S3 阶段已经锁定。S4 implementer 只是把内容物质化为独立文件以便 S5 阶段直接执行。

**文件改动**：
- `numind-server/docs/superpowers/specs/2026-05-13-chatbot-session-rename-pin-validation-strategy.md`（新建）— 内容即下方"验证方式 + 理由 + 关键用户路径 + E2E test file location + Backend/Frontend test files + 重复置顶验证方式"全部小节

**验证方式（锁定）**：**Playwright E2E + 后端 Go test + 前端 Vitest 三件套**（不仅 gstack `/qa`，理由：本 feature 前端交互复杂 + 后端 API 契约变化 + S5 必做明确写在 spec §7.3，gstack /qa 一次性验证不产生持久回归保护）

**理由**（NDF Rule 10 必须）：
1. 前端交互复杂（hover + dropdown + inline modal + pessimistic UI 重排）→ 需 E2E 自动化覆盖
2. 后端 API 契约变化（新加 2 端点 + 改 1 端点）→ 需 store/biz 单元测试 + Playwright 串联验证
3. `updated_at` 不刷新（D2）是核心不变量 → E2E 需读取 API response 验证（详 §3 验证用户路径 #6）
4. SQL 排序逻辑首次引入 `pinned_at IS NULL ASC` → 需 store unit test 3-行 case 验证 + E2E 视觉位置验证
5. 不选仅 gstack `/qa` 的理由：feature 未来修改时需要回归保护，特别是改名 / 置顶逻辑（涉及前后端契约）

**关键用户路径**（S5 必须验证的具体操作步骤）：

1. **改名 happy path**：登录 → 进入某 chatbot 对话页 → hover session → 点「⋯」→ 点重命名 → 输入新名 → 保存 → 列表显示新名 → API response 200 + body `{id, title}`
2. **改名空白校验**：菜单 → 重命名 → 输入纯空白 → 保存 → toast 错误 "标题不能为空"（前端 trim 校验，不发请求）
3. **置顶 happy path**：菜单 → 点置顶 → session 移到列表顶部 + 左侧 2px primary 边框 → DevTools Network 看 PUT response `pinned_at` 非 null
4. **重复置顶**：先置顶 session A 再置顶 session B → B 移到列表顶部（pinned_at 比 A 更新）
5. **取消置顶**：菜单 → 点取消置顶 → session 离开置顶组回到 updated_at 排序位置
6. **`updated_at` 不变量验证**（核心 D2）：改名 / 置顶 / 取消置顶前后用 GET sessions 拿 `updated_at`；操作前后值应**完全相等**（精度到秒）
7. **删除菜单迁移验证**：菜单内点删除 → 现有 ConfirmModal 出现 → 确认 → session 从列表消失（行为与旧版完全一致）
8. **跨 chatbot 隔离**：在 chatbot A 改名 session X → 切换到 chatbot B → B 的 session 列表不含 X 的改名（chatbot_id 查询参数生效）
9. **未登录 401**：直接调 PUT rename 不带 token → 401
10. **非本人 403**：用 user A token 改 user B 的 session → 403（手动 SQL 制造 + curl 验证）

**E2E test file location**：`numind-web-v3/e2e/chatbot-session-rename-pin.spec.ts`

**Backend test files**：
- `numind-server/internal/numind/store/chatbot_session_test.go`（Task 2）
- `numind-server/internal/numind/biz/chatbot/chatbot_test.go`（Task 3）

**Frontend Vitest file**：
- `numind-web-v3/src/stores/__tests__/chatbot.spec.ts`（Task 6）

**E2E 中 "重复置顶 → pinned_at 刷新" 验证方式（S2 holistic reviewer P2-NEW-2 锁定）**：用 API response 的 `pinned_at` 时间戳比较（先记录 A pinned_at；置顶 B；再 GET sessions 看 B 的 pinned_at > A 的）+ UI 位置变化（B 在最顶）双重验证。

**独立性**：所有实施 task (T1-T7) 完成后写 validation-strategy 文档；不影响实施 task 的开发，但 S5 阶段必须按此文档执行。

---

## §4 验收总结（S4 gate 要求）

完成 T1-T7 + T8 后：

- [ ] `task lint`（numind-server）退出码 0
- [ ] `go test ./...`（numind-server）退出码 0
- [ ] `npm run lint`（numind-web-v3）退出码 0
- [ ] `npm run type-check`（numind-web-v3）退出码 0
- [ ] `npm run test`（numind-web-v3 Vitest）退出码 0
- [ ] 两阶段 review (spec compliance + code quality) 均 PASS 或 PASS_WITH_CONCERNS（无 P0）
- [ ] manifest progress: completed_tasks == 8（含 T8 验证策略 task），reviewed_tasks == 8
- [ ] T8 validation-strategy 文档已 commit

S4 gate 通过后进入 S5（按 T8 文档执行 E2E + 完整 lint/test 套餐）。

---

## §5 与现有 feature 协调

- **`sop-chatbot-visibility-scope`** 当前 S4 进行中 (16/23 tasks done)，预计与本 feature 实施时间有重叠。**两 feature 不耦合**（reviewer 验证过）但需注意：
  - 两 feature 都改 `numind-server/internal/numind/store/store.go` (IStore 注册)— **本 feature 不动 IStore 接口**（chatbotSessionStore 已存在），无冲突
  - 两 feature 都改 `numind-server/internal/numind/router.go` — 本 feature 加 2 行到 chatbotGroup 内，visibility-scope 加到不同 group，无冲突
  - manifest 同步用 worktree 隔离（S0/S1/S2 已验证可行）

- **`membership-credits-redesign`** S5 in flight — 完全不耦合，无依赖

---

## §6 风险与缓解

| 风险 | 概率 | 严重性 | 缓解 |
|------|------|--------|------|
| SQL `ORDER BY pinned_at IS NULL ASC, ...` 在 MySQL 8 上行为与预期不符 | Low | High | T2 store 测试 3-行 case 完整覆盖（spec §4.1）；in-memory SQLite 与 MySQL 8 行为可能差异 — 若 SQLite test 不能完全验证 MySQL NULL 排序，T4 dev DB curl 验证补足 |
| `UpdateColumn` 实际仍刷新 updated_at（GORM 版本差异）| Low | High | T2 写专项测试 `TestUpdateTitle_DoesNotRefreshUpdatedAt` 验证；database.md §6b 已验证 GORM v1.30.0 的 Save/UpdateColumn 行为，本 feature 同版本 |
| `fetchSessions` 签名破坏性变更漏迁移 | Medium | Medium | Plan §0 P1-1 明示 + Task 6 验收要求 grep 0 处旧签名残留 |
| ConfirmModal 改造引入跨 feature 回归 | Low | Medium | spec P0-1 已锁不改 ConfirmModal，纯 inline RenameModal — 零跨 feature 影响 |
| 并行 session 干扰 develop manifest 同步 | High | Low | worktree 隔离策略已在 S0/S1/S2 验证可行；S4 各 task commit 也用同样策略 |
| 现有 chatbot_session 行 `pinned_at` 默认 NULL 与 GORM `*time.Time` 解析 | Low | Low | T1 model `*time.Time` 标准 nullable timestamp 模式；spec §1.2 已分析无 GORM default:true bool 陷阱 |
| dev DB migration apply 失败 | Low | High | T1 验收要求 dev DB 实跑成功；rollback SQL 已准备；并行 visibility-scope 也在 apply migration，二者无表冲突（不同表） |

---

## §7 工作量估算

| Task | 估算 (CC+gstack) | 估算 (人类) |
|------|-----------------|------------|
| T1 migration + model | 15 min | 1 h |
| T2 store + tests | 30 min | 3 h |
| T3 biz + tests | 30 min | 3 h |
| T4 controller + router + 集成验证 | 30 min | 3 h |
| T5 types + api | 15 min | 1 h |
| T6 store + Vitest | 30 min | 3 h |
| T7 ChatbotChat.vue | 45 min | 4 h |
| T8 validation strategy doc | 15 min | 1 h |
| **总计（仅实施）** | **3-3.5 h** | **~2 d (16 h)** |
| **+ S4 review 开销 (8 task × 两阶段)** | **+2-3 h** | **+1 d** |
| **+ S5 验证 + S6/S7** | **+1-2 h** | **+0.5 d** |
| **跨 S4→S7 总 wall time** | **6-9 h** | **~3.5 d** |

S4 纯 CC+gstack 实施时间在 **1 工作日内可完成**（含 review）。

---

## §8 S3 plan 写作说明 + Review 记录

**Superpowers `writing-plans` skill 处理方式**：
- 本 plan 由主控 AI 直接基于 S2 spec §9 task 拆分建议 + holistic reviewer P1 指引整合写出
- 未独立 invoke `writing-plans` skill（理由：spec §9 已给出 task 拆分建议；S0 reviewer + S2 spec Round 1+2 + S2 holistic reviewer 已收集全部需要的设计决策；plan 是 mechanical translation）
- Plan 完成后由独立 Sonnet reviewer subagent 做 plan atomicity + completeness 审查（NDF §3 S3 强制要求）

### Review 记录

| 轮次 | Reviewer | 结果 | 修复 |
|------|---------|------|------|
| Round 1 | 独立 Sonnet subagent (NDF §3 S3 强制) | PASS_WITH_CONCERNS — 0 P0 + 1 P1 + 2 P2。NDF S3 Gate 检查清单全 PASS；AC → Task 映射 **11/11 全覆盖** | **1 P1 + 2 P2 全修**：(P1-1) T6 → T7 间 fetchSessions 破坏性签名变更原子性 → Task 6 描述加"task 边界清单"，明确 T6 必须同时迁移 ChatbotChat.vue 内现有 fetchSessions 旧签名调用，T7 仅处理新增 UI（watcher / hover menu / inline modal / 删除按钮菜单迁移）；(P2-1) PRD AC-3.4 措辞 vs spec/plan client-side filter 保留语义 → Task 7 加"Client-side filter 保留语义说明"段落明确两者一致；(P2-2) T8 是文档 task 但描述未说明实际工作 → T8 描述加"实施说明"段，明确 S5 验证策略全部内容已在 plan §3 写出，T8 工作即拷贝物质化为独立 .md 文件 |
