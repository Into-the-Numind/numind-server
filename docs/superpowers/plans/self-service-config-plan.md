# Self-Service Config Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build B-end self-service config tools (Chatbot + SOP configurator) with C-end consumption UI

**Architecture:** Three-layer (controller → biz → store) with new modules for chatbot and knowledge base. Backend first (numind-server), then frontend (numind-web-v3). Chatbot biz layer independent from SalesRAG, reuses store-level document/vector infrastructure.

**Tech Stack:** Go 1.24 / Gin / GORM / MySQL 8.0 (backend) + Vue 3 / TypeScript / Pinia / Vite (frontend)

**Spec:** `docs/superpowers/specs/2026-04-09-self-service-config-design.md` — all model definitions, store interfaces, biz interfaces, API contracts, and migration SQL are defined there. Each task references the relevant spec section.

---

## Backend Tasks (numind-server)

### Task 1: Migration SQL + Models + Error Codes

**Repo:** numind-server
**Files:**
- Create: `migrations/20260409_000001_self_service_config.sql`
- Create: `internal/pkg/model/chatbot.go`
- Create: `internal/pkg/model/knowledge_base.go`
- Create: `internal/pkg/errno/config.go`
- Modify: `internal/pkg/model/sop.go` — add `CreatorUserID` and `PublishStatus` fields
- Modify: `internal/pkg/model/user_feature_permission.go` — add `FeatureKeySelfServiceConfig` constant

**Acceptance criteria:**
- Migration SQL executes without error on dev database
- All 6 new model structs compile with correct GORM tags and TableName() methods
- SopTemplate has `CreatorUserID *uint` and `PublishStatus string` fields
- 7 new error codes defined in `errno/config.go`
- `FeatureKeySelfServiceConfig = "self_service_config"` constant exists
- `go build ./...` passes

**Implementation reference:** Spec §2 (data model), §7 (error codes), §8 (migration SQL)

- [ ] Create migration SQL file with all 6 new tables + SopTemplate ALTER statements (copy from Spec §8 verbatim)
- [ ] Create `internal/pkg/model/knowledge_base.go` with `KnowledgeBase` and `KnowledgeBaseDocument` structs (Spec §2.1)
- [ ] Create `internal/pkg/model/chatbot.go` with `ChatbotConfig`, `ChatbotKnowledgeBase`, `ChatbotSession`, `ChatbotMessage` structs + all status constants (Spec §2.1)
- [ ] Modify `internal/pkg/model/sop.go`: add `CreatorUserID *uint` and `PublishStatus string` fields + publish status constants (Spec §2.2)
- [ ] Modify `internal/pkg/model/user_feature_permission.go`: add `FeatureKeySelfServiceConfig` constant (Spec §2.3)
- [ ] Create `internal/pkg/errno/config.go` with 7 error codes (Spec §7)
- [ ] Run `go build ./...` to verify compilation
- [ ] Run migration on dev DB: `sshpass -p "$DEV_SSH_PASS" ssh "$DEV_SSH_USER@$DEV_SSH_HOST" "mysql -u root numind < /path/to/migration.sql"` (or via local DB)
- [ ] Commit: `feat(config): add models, migration, and error codes for self-service config`

---

### Task 2: Store Layer — KB + ChatbotConfig + ChatbotSession + IStore Registration

**Repo:** numind-server
**Files:**
- Create: `internal/numind/store/knowledge_base.go`
- Create: `internal/numind/store/chatbot_config.go`
- Create: `internal/numind/store/chatbot_session.go`
- Modify: `internal/numind/store/store.go` — add 3 new methods to IStore + datastore
- Modify: `internal/numind/store/customer.go` — add `GrantTemplateToAllSubUsers` method

**Acceptance criteria:**
- `IKnowledgeBaseStore` interface with 7 methods implemented (Spec §4.2)
- `IChatbotConfigStore` interface with 11 methods implemented (Spec §4.2)
- `IChatbotSessionStore` interface with 9 methods implemented (Spec §4.2)
- All 3 stores registered in `IStore` interface and `datastore` struct
- `GrantTemplateToAllSubUsers` added to `ICustomerStore` interface and implemented
- `go build ./...` passes
- `go test ./internal/numind/store/...` passes (no regressions)

**Implementation reference:** Spec §4.2 (store interfaces)

**Pattern to follow:** Existing `internal/numind/store/sop.go` for GORM query patterns, `store.go` for IStore registration

- [ ] Create `internal/numind/store/knowledge_base.go`: implement `IKnowledgeBaseStore` with all 7 methods per Spec §4.2. Follow GORM patterns from existing stores. Key: `ListDocumentIDsByKBs` must JOIN `knowledge_document` to filter `status = 'COMPLETED'` and `deleted_at IS NULL`.
- [ ] Create `internal/numind/store/chatbot_config.go`: implement `IChatbotConfigStore` with all 11 methods per Spec §4.2. Key: `MountKnowledgeBases` batch-inserts `ChatbotKnowledgeBase` rows; `UnmountAllByKB` hard-deletes by `knowledge_base_id`; `ListPublishedByOwner` filters `user_id = ownerUserID AND status = 'published' AND deleted_at IS NULL`.
- [ ] Create `internal/numind/store/chatbot_session.go`: implement `IChatbotSessionStore` with all 9 methods per Spec §4.2. Key: `DeleteMessagesBySession` uses hard delete (no soft delete on messages); `GetMaxSeq` returns `MAX(seq)` for the session; `IncrementMessageCount` uses `UpdateColumn("message_count", gorm.Expr("message_count + ?", 1))`.
- [ ] Modify `internal/numind/store/store.go`: add `KnowledgeBase() IKnowledgeBaseStore`, `ChatbotConfig() IChatbotConfigStore`, `ChatbotSession() IChatbotSessionStore` to IStore interface. Add implementations to datastore struct following existing pattern (`return newKnowledgeBaseStore(ds.db)`).
- [ ] Modify `internal/numind/store/customer.go`: add `GrantTemplateToAllSubUsers(ctx, parentUserID, templateID uint) error` to `ICustomerStore` interface and implement — query `user WHERE parent_user_id = parentUserID`, batch insert `user_template_permission` for each sub-user, skip duplicates.
- [ ] Run `go build ./...` and `go test ./internal/numind/store/...`
- [ ] Commit: `feat(config): add store layer for knowledge base, chatbot config, and chatbot session`

---

### Task 3: ParentUserOnly Middleware

**Repo:** numind-server
**Files:**
- Create: `internal/pkg/middleware/parent_user.go`

**Acceptance criteria:**
- `ParentUserOnly()` gin.HandlerFunc reads `current_user` from context (no DB query)
- Returns 403 if `user.ParentUserID != nil`
- `go build ./...` passes

**Implementation reference:** Spec §4.5

- [ ] Create `internal/pkg/middleware/parent_user.go` with `ParentUserOnly()` function per Spec §4.5. Read `current_user` from `c.Get("current_user")`, type-assert to `*model.User`, check `ParentUserID != nil` → abort with `errno.ErrForbidden.SetMessage("仅限主账号操作")`.
- [ ] Run `go build ./...`
- [ ] Commit: `feat(config): add ParentUserOnly middleware`

---

### Task 4: Knowledge Base Biz Layer

**Repo:** numind-server
**Files:**
- Create: `internal/numind/biz/knowledgebase/knowledge_base.go`
- Modify: `internal/numind/biz/biz.go` — add `KnowledgeBase()` to IBiz + initialization

**Acceptance criteria:**
- `IKnowledgeBaseBiz` interface with 7 methods implemented (Spec §4.3)
- All methods enforce ownership: `kb.UserID == userID`
- `Delete` uses transaction: first `UnmountAllByKB`, then soft-delete KB
- `AddDocument` validates `document.UserID == kb.UserID`
- `AddDocument` reuses existing document upload/parse/vectorize pipeline from SalesRAG
- Registered in IBiz interface
- `go build ./...` passes

**Implementation reference:** Spec §4.3 IKnowledgeBaseBiz

- [ ] Create `internal/numind/biz/knowledgebase/knowledge_base.go`: define `IKnowledgeBaseBiz` interface and `knowledgeBaseBiz` struct. Implement all 7 methods per Spec §4.3. Key patterns:
  - Every Get/Update/Delete: first `store.KnowledgeBase().Get(ctx, id)` → check `kb.UserID == userID` → proceed or return `ErrForbidden`
  - `Delete`: `db.Transaction` → `store.ChatbotConfig().UnmountAllByKB(ctx, id)` → `store.KnowledgeBase().Delete(ctx, id)`
  - `AddDocument`: validate ownership → call existing SalesRAG document upload pipeline (look at how `salesrag.Ingest` handles file upload in `biz/salesrag/`) → create `KnowledgeBaseDocument` association
  - `RemoveDocument`: validate ownership → hard delete `KnowledgeBaseDocument` row
- [ ] Modify `internal/numind/biz/biz.go`: add `KnowledgeBase() IKnowledgeBaseBiz` to `IBiz` interface. Add `knowledgeBaseBiz` field to `biz` struct. Initialize in `NewBiz()`.
- [ ] Run `go build ./...`
- [ ] Commit: `feat(config): add knowledge base biz layer`

---

### Task 5: Chatbot Biz — Config CRUD (B-end)

**Repo:** numind-server
**Files:**
- Create: `internal/numind/biz/chatbot/chatbot.go`
- Modify: `internal/numind/biz/biz.go` — add `Chatbot()` to IBiz

**Acceptance criteria:**
- `IChatbotBiz` interface defined with all B-end methods (Create, Get, List, Update, Delete, UpdateStatus)
- All B-end methods enforce ownership: `config.UserID == userID`
- `CreateChatbot` creates config + mounts KBs in one flow
- `UpdateStatus` enforces valid transitions: draft→published, published→offline, offline→published
- `DeleteChatbot` soft-deletes config
- Registered in IBiz
- `go build ./...` passes

**Implementation reference:** Spec §4.3 IChatbotBiz (B-end section only)

- [ ] Create `internal/numind/biz/chatbot/chatbot.go`: define `IChatbotBiz` interface (full interface from Spec §4.3 — both B-end and C-end methods), `chatbotBiz` struct with `store.IStore` dependency. Implement B-end methods only in this task:
  - `CreateChatbot`: create `ChatbotConfig` → if `knowledge_base_ids` provided, call `store.ChatbotConfig().MountKnowledgeBases()`
  - `GetChatbot`: get config + `ListMountedKBs` → return `ChatbotDetail` struct
  - `ListChatbots`: delegate to store `List(ctx, userID, offset, limit)`
  - `UpdateChatbot`: ownership check → update fields
  - `DeleteChatbot`: ownership check → soft delete
  - `UpdateStatus`: ownership check → validate transition → `store.ChatbotConfig().UpdateStatus()`
- [ ] Add C-end method stubs (ListVisibleChatbots, CreateSession, ListSessions, DeleteSession, ListMessages, ChatStream) that return `fmt.Errorf("not implemented")` — these will be implemented in Task 7.
- [ ] Modify `internal/numind/biz/biz.go`: add `Chatbot() IChatbotBiz` to `IBiz`, add field and initialization.
- [ ] Run `go build ./...`
- [ ] Commit: `feat(config): add chatbot biz layer — B-end config CRUD`

---

### Task 6: SOP Biz Extension (B-end SOP Config)

**Repo:** numind-server
**Files:**
- Modify: `internal/numind/biz/sop/sop.go` — add 4 new methods to ISopBiz + implementation
- Modify: `internal/numind/store/sop.go` — add `ListTemplatesByCreator` query method

**Acceptance criteria:**
- `CreateTemplateByUser` creates template with `CreatorUserID` set, `PublishStatus = draft`
- `ListTemplatesByCreator` filters by `creator_user_id`
- `PublishTemplate` sets `publish_status = published` + calls `GrantTemplateToAllSubUsers`
- `UnpublishTemplate` sets `publish_status = offline` (does NOT revoke permissions)
- `CreateRun` rejects B-end templates that aren't published
- Node creation enforces max 20 nodes per template
- `go build ./...` passes
- `go test ./internal/numind/biz/sop/...` passes (no regressions)

**Implementation reference:** Spec §4.3 ISopBiz extension

- [ ] Modify `internal/numind/store/sop.go`: add `ListTemplatesByCreator(ctx, creatorID uint, offset, limit int) ([]model.SopTemplate, int64, error)` to `ISopStore` and implement with `WHERE creator_user_id = ? AND deleted_at IS NULL`.
- [ ] Modify `internal/numind/biz/sop/sop.go`: add 4 methods to `ISopBiz` interface and implement:
  - `CreateTemplateByUser(ctx, userID, req)`: create SopTemplate with `CreatorUserID: &userID`, `PublishStatus: SopPublishStatusDraft`, `Status: "active"`
  - `ListTemplatesByCreator(ctx, creatorID, offset, limit)`: delegate to store
  - `PublishTemplate(ctx, userID, templateID)`: verify `template.CreatorUserID == &userID` → set `publish_status = published` → call `store.S.Customers().GrantTemplateToAllSubUsers(ctx, userID, templateID)`
  - `UnpublishTemplate(ctx, userID, templateID)`: verify ownership → set `publish_status = offline`
- [ ] Modify `CreateRun` in `biz/sop/sop.go`: after fetching template, add check: `if template.CreatorUserID != nil && template.PublishStatus != model.SopPublishStatusPublished { return nil, errno.ErrTemplateNotPublished }`
- [ ] Modify `CreateNode` (or the relevant node creation method): add count check — query `COUNT(*) WHERE template_id = ? AND deleted_at IS NULL`, if `>= 20` return `errno.ErrMaxNodesExceeded`
- [ ] Run `go build ./...` and `go test ./internal/numind/biz/sop/...`
- [ ] Commit: `feat(config): extend SOP biz with B-end template config methods`

---

### Task 7: Chatbot Biz — Session + ChatStream + Langfuse

**Repo:** numind-server
**Files:**
- Create: `internal/numind/biz/chatbot/stream.go`
- Modify: `internal/numind/biz/chatbot/chatbot.go` — implement C-end methods

**Acceptance criteria:**
- `ListVisibleChatbots` returns correct scope per user role (parent sees all own, sub-user sees parent's published)
- `CreateSession` validates chatbot exists and is accessible
- `DeleteSession` soft-deletes session + hard-deletes messages
- `ChatStream` implements full flow: session lookup → permission check → KB retrieval → vector search → prompt assembly → LLM call (SSE) → save messages → credit deduction
- Langfuse trace created per Spec §5: trace `chatbot-chat` → span `context-assembly` (sub-spans: `vector-retrieval`, `prompt-construction`) → generation `chatbot-llm-call`
- SSE format matches Spec §3.2: `{"type":"token","content":"..."}` and `{"type":"done",...}`
- `go build ./...` passes

**Implementation reference:** Spec §4.3 (C-end methods), §4.4 (ChatStream flow), §5 (Langfuse trace)

- [ ] Implement C-end methods in `chatbot.go` (replace stubs from Task 5):
  - `ListVisibleChatbots(ctx, user)`: if `user.ParentUserID == nil` → `store.ChatbotConfig().List(ctx, user.ID, 0, 1000)` (parent sees all); else → `store.ChatbotConfig().ListPublishedByOwner(ctx, *user.ParentUserID)`
  - `CreateSession(ctx, userID, chatbotID)`: verify chatbot exists (not soft-deleted) and is accessible (published, or draft+owner) → create `ChatbotSession`
  - `ListSessions(ctx, userID, offset, limit)`: delegate to store
  - `DeleteSession(ctx, userID, sessionID)`: verify `session.UserID == userID` → `store.ChatbotSession().DeleteMessagesBySession()` → `store.ChatbotSession().DeleteSession()`
  - `ListMessages(ctx, userID, sessionID, offset, limit)`: verify session ownership → delegate to store
- [ ] Create `internal/numind/biz/chatbot/stream.go` with `ChatStream` implementation following Spec §4.4 flow exactly:
  1. Get session → ownership check
  2. Get chatbot config → access check (published OR draft+owner)
  3. Credit check (call existing ICreditBiz or check balance)
  4. Get mounted KB IDs → `store.ChatbotConfig().ListMountedKBs()` → extract IDs
  5. Get document IDs → `store.KnowledgeBase().ListDocumentIDsByKBs(kbIDs)` (already filters COMPLETED)
  6. Vector retrieval: use existing vector search service (look at how `biz/salesrag/` does it — call embedding service for query vector, then KNN search filtered by document IDs). If no KBs mounted, skip this step.
  7. Assemble messages: system (SystemPrompt + chunks context), history (recent N messages by Seq), user message
  8. Langfuse: `CreateTrace("chatbot-chat")` → `CreateSpan("context-assembly")` → nested spans for vector-retrieval and prompt-construction → `CreateGeneration("chatbot-llm-call")`
  9. Call LLM via existing provider (Volc or Ali, through `internal/pkg/llm/` or `biz/volc/`) with SSE streaming → handler callback emitting `{"type":"token","content":"..."}` events
  10. Save user message (Seq = maxSeq+1) and assistant message (Seq = maxSeq+2) → IncrementMessageCount(+2)
  11. Deduct credits: create UsageRecord (only on stream completion, not on disconnect)
  12. EndGeneration with token counts. Use `defer` for EndGeneration to handle disconnects (record error output).
- [ ] Define `StreamHandler` type: `type StreamHandler func(event string, data interface{}) error`
- [ ] **Risk note**: Before starting `stream.go`, first read `biz/salesrag/salesrag.go` to understand existing vector search patterns, LLM streaming interfaces, and credit deduction flow. Run `go build ./...` after implementing each major sub-step (retrieval, LLM call, message save) to catch compilation errors early.
- [ ] Run `go build ./...`
- [ ] Commit: `feat(config): implement chatbot session, ChatStream with Langfuse tracing`

---

### Task 8: B-end Config Controllers

**Repo:** numind-server
**Files:**
- Create: `internal/numind/controller/v1/config/knowledge_base.go`
- Create: `internal/numind/controller/v1/config/chatbot.go`
- Create: `internal/numind/controller/v1/config/sop.go`

**Acceptance criteria:**
- KB controller: 7 handler methods matching Spec §3.1 KB endpoints
- Chatbot controller: 6 handler methods matching Spec §3.1 chatbot endpoints
- SOP controller: 10 handler methods matching Spec §3.1 SOP endpoints
- All controllers follow existing pattern: param binding → call biz → `core.WriteResponse()`
- Pagination uses `offset`/`limit`, response uses `{"list":[], "total":N}`
- Document upload uses `multipart/form-data` (`c.Request.FormFile("file")`)
- `go build ./...` passes

**Implementation reference:** Spec §3.1 (B-end API), §3.3 (route registration for controller method names)

- [ ] Create `internal/numind/controller/v1/config/knowledge_base.go`:
  - `KnowledgeBaseController` struct with `kbBiz knowledgebase.IKnowledgeBaseBiz`
  - `NewKnowledgeBaseController(kbBiz) *KnowledgeBaseController`
  - Methods: `Create`, `List`, `Get`, `Update`, `Delete`, `UploadDocument`, `RemoveDocument`
  - `UploadDocument`: use `c.Request.FormFile("file")` for multipart upload
  - Each method: extract userID from `c.GetUint("userID")`, bind params, call biz, `core.WriteResponse()`
- [ ] Create `internal/numind/controller/v1/config/chatbot.go`:
  - `ChatbotConfigController` struct with `chatbotBiz chatbot.IChatbotBiz`
  - Methods: `Create`, `List`, `Get`, `Update`, `Delete`, `UpdateStatus`
  - `Create` binds JSON body with `name` (required), `system_prompt` (required), optional fields per Spec §3.1
- [ ] Create `internal/numind/controller/v1/config/sop.go`:
  - `SopConfigController` struct with `sopBiz sop.ISopBiz`
  - Methods: `Create`, `List`, `Get`, `Update`, `Delete`, `UpdateStatus`, `CreateNode`, `UpdateNode`, `DeleteNode`, `BatchSortNodes`
  - `BatchSortNodes` binds JSON array `[{"id":N,"sort":N},...]`
- [ ] Run `go build ./...`
- [ ] Commit: `feat(config): add B-end config controllers for KB, chatbot, and SOP`

---

### Task 9: C-end Chatbot Controller + Route Registration

**Repo:** numind-server
**Files:**
- Create: `internal/numind/controller/v1/chatbot/chatbot.go`
- Modify: `internal/numind/router.go` — register all new routes

**Acceptance criteria:**
- C-end chatbot controller: 6 handler methods matching Spec §3.2
- `Chat` handler: sets SSE headers (`Content-Type: text/event-stream`, `Cache-Control: no-cache`, `Connection: keep-alive`), calls `ChatStream` with handler that writes SSE events to `c.Writer`
- All routes registered per Spec §3.3 with correct middleware chains:
  - `/v1/config/*`: AuthMiddleware → ParentUserOnly → FeaturePermission
  - `/v1/chatbot/*`: AuthMiddleware only
  - `/nodes/batch-sort` registered before `/nodes/:nodeId`
- `go build ./...` passes
- `task lint` passes

**Implementation reference:** Spec §3.2 (C-end API), §3.3 (route registration)

- [ ] Create `internal/numind/controller/v1/chatbot/chatbot.go`:
  - `ChatbotController` struct with `chatbotBiz chatbot.IChatbotBiz`
  - Methods: `List`, `CreateSession`, `ListSessions`, `DeleteSession`, `ListMessages`, `Chat`
  - `List`: get current user via `middleware.GetCurrentUser(c)`, pass full user object to `biz.ListVisibleChatbots(ctx, user)`
  - `Chat`: set SSE headers, call `biz.ChatStream(ctx, userID, sessionID, message, handler)` where handler writes `fmt.Fprintf(c.Writer, "data: %s\n\n", jsonData)` + `c.Writer.Flush()`
- [ ] Modify `internal/numind/router.go` in `installNumindRouters()`:
  - Initialize new controllers: `kbCtrl`, `configChatbotCtrl`, `configSopCtrl`, `chatbotCtrl`
  - Register `/v1/config/*` routes with `ParentUserOnly()` + `FeaturePermission(model.FeatureKeySelfServiceConfig)` middleware (Spec §3.3)
  - Register `/v1/chatbot/*` routes under `authGroup` (Spec §3.3)
  - Ensure `/nodes/batch-sort` is registered BEFORE `/nodes/:nodeId`
- [ ] Run `go build ./...` and `task lint`
- [ ] Commit: `feat(config): add C-end chatbot controller and register all routes`

---

## Frontend Tasks (numind-web-v3)

### Task 10: Frontend API Layer + TypeScript Types + Pinia Stores

**Repo:** numind-web-v3
**Files:**
- Create: `src/api/config.ts`
- Create: `src/api/chatbot.ts`
- Create: `src/types/config.ts` (or inline in api files, follow existing pattern)
- Create: `src/stores/config.ts`
- Create: `src/stores/chatbot.ts`

**Acceptance criteria:**
- All API functions defined matching Spec §3.1 and §3.2 endpoints
- TypeScript interfaces for ChatbotConfig, KnowledgeBase, SopTemplate, ChatbotSession, ChatbotMessage
- `useConfigStore` with B-end CRUD actions for chatbots, SOP templates, and KBs
- `useChatbotStore` with C-end session/message actions + SSE streaming support
- `npm run type-check` passes

**Implementation reference:** Spec §6.4 (stores), §6.6 (API layer), §3.1/§3.2 (response schemas)

- [ ] Create `src/api/config.ts`: define all B-end config API functions (KB CRUD + document upload/remove, chatbot CRUD + status, SOP template CRUD + node CRUD + batch-sort + status). Use `request` from `src/api/request.ts`. Document upload uses `FormData` with `Content-Type: multipart/form-data`.
- [ ] Create `src/api/chatbot.ts`: define C-end chatbot API functions (listVisibleChatbots, createSession, listSessions, deleteSession, listMessages). Note: chat uses EventSource, not axios.
- [ ] Define TypeScript interfaces matching Spec response schemas: `ChatbotConfig`, `ChatbotDetail`, `KnowledgeBase`, `KBDetail`, `KnowledgeDocument`, `SopTemplate`, `SopNode`, `ChatbotSession`, `ChatbotMessage`.
- [ ] Create `src/stores/config.ts`: `useConfigStore` per Spec §6.4 — state refs for lists + loading, async actions calling API functions, error handling in try/catch with toast notifications.
- [ ] Create `src/stores/chatbot.ts`: `useChatbotStore` per Spec §6.4 — sessions, currentSession, messages, streaming ref. `sendMessage` action: create `EventSource` for SSE, parse `{"type":"token","content":"..."}` events, append to messages reactively, set `streaming = false` on `{"type":"done"}`.
- [ ] Run `npm run type-check`
- [ ] Commit: `feat(config): add frontend API layer, types, and Pinia stores`

---

### Task 11: Frontend Router + Guard + Sidebar

**Repo:** numind-web-v3
**Files:**
- Modify: `src/router/index.ts` — add /config/* and /chatbot/:id routes
- Modify: `src/stores/user.ts` — add `isParentUser` computed
- Modify: `src/components/AppSidebar.vue` (or equivalent) — add conditional config center menu
- Create: `src/views/config/ConfigLayout.vue`

**Acceptance criteria:**
- `/config/*` routes registered with `requiresParent` meta
- Route guard redirects sub-users to `/` when accessing `/config/*`
- `isParentUser` computed in userStore: `user.parent_user_id == null`
- Sidebar shows "配置中心" menu with 3 sub-items only for parent users
- `/chatbot/:id` route registered
- `ConfigLayout.vue` renders `<router-view />`
- `npm run lint && npm run type-check` passes

**Implementation reference:** Spec §6.1-§6.3

- [ ] Modify `src/stores/user.ts`: add `const isParentUser = computed(() => user.value?.parent_user_id == null)`, export it.
- [ ] Modify `src/router/index.ts`: add routes per Spec §6.1. Add `requiresParent` meta. Add `beforeEach` guard per Spec §6.2.
- [ ] Modify sidebar component: add conditional "配置中心" menu item per Spec §6.3 — only render when `userStore.isParentUser` is true. Three sub-items: 智能体管理, SOP 管理, 知识库管理.
- [ ] Create `src/views/config/ConfigLayout.vue`: simple wrapper with `<router-view />`.
- [ ] Run `npm run lint && npm run type-check`
- [ ] Commit: `feat(config): add config center routes, guard, and sidebar menu`

---

### Task 12: Frontend Config Center Pages

**Repo:** numind-web-v3
**Files:**
- Create: `src/views/config/ChatbotList.vue`
- Create: `src/views/config/ChatbotEdit.vue`
- Create: `src/views/config/SopTemplateList.vue`
- Create: `src/views/config/SopTemplateEdit.vue`
- Create: `src/views/config/KnowledgeBaseList.vue`
- Create: `src/views/config/KnowledgeBaseDetail.vue`

**Acceptance criteria:**
- 6 config pages follow management table layout pattern (Spec §6.5):
  - ChatbotList: table with name/status/KB count/created/actions (edit, test, publish/offline, delete)
  - ChatbotEdit: form with name(required)/description/avatar/system_prompt/KB multi-select
  - SopTemplateList: table with name/node count/status/created/actions
  - SopTemplateEdit: left panel (draggable step list) + right panel (prompt editor)
  - KnowledgeBaseList: table with name/doc count/created/actions
  - KnowledgeBaseDetail: document list + upload area
- All pages handle 4 states: loading/empty/error/success
- `npm run lint && npm run type-check` passes

**Implementation reference:** Spec §6.5

- [ ] Create `src/views/config/ChatbotList.vue`: table layout per Spec §6.5. Columns: 名称, 状态 (badge), 知识库数, 创建时间, 操作. Actions: 编辑(→ edit page), 测试对话(→ /chatbot/:id), 发布/下线(confirm dialog), 删除(confirm dialog). Top: 新建 button (→ create page). Loading skeleton, empty state with CTA, error retry.
- [ ] Create `src/views/config/ChatbotEdit.vue`: form page per Spec §6.5. Fields: name (required, AppInput), description (optional), avatar upload, system_prompt (large textarea), knowledge_base_ids (multi-select dropdown querying KB list API). Blur validation, submit button disabled until required fields filled. Save → API call → success toast → navigate back to list.
- [ ] Create `src/views/config/SopTemplateList.vue`: table layout. Columns: 名称, 步骤数, 状态, 创建时间, 操作. Actions: edit, publish/offline, delete.
- [ ] Create `src/views/config/SopTemplateEdit.vue`: split layout per Spec §6.5. Left: step list (draggable, use vuedraggable or native drag), + add button (max 20), delete step. Right: selected step's prompt textarea. Top: template name/description + save button.
- [ ] Create `src/views/config/KnowledgeBaseList.vue`: table layout. Columns: 名称, 文档数, 创建时间, 操作. Actions: view detail, delete (confirm dialog mentioning chatbot unmount).
- [ ] Create `src/views/config/KnowledgeBaseDetail.vue`: document list (name, size, status badge with PENDING→PARSING→EMBEDDING→COMPLETED, upload time, remove action) + upload area (drag-drop or click, FormData POST).
- [ ] Run `npm run lint && npm run type-check`
- [ ] Commit: `feat(config): add config center management pages`

---

### Task 13: Frontend Chatbot Chat Page + Homepage Integration

**Repo:** numind-web-v3
**Files:**
- Create: `src/views/chatbot/ChatbotChat.vue`
- Modify: `src/views/HomeView.vue` (or equivalent homepage component) — add chatbot section

**Acceptance criteria:**
- ChatbotChat page: left sidebar (session list + create/delete) + right chat area (messages + input)
- Messages render Markdown with streaming typing effect
- SSE via EventSource, streaming state shows loading indicator
- Enter sends message, button also sends
- Credits insufficient → show InsufficientCreditsDialog
- Homepage shows chatbot card grid (avatar, name, description) from `/v1/chatbot/list`
- Homepage chatbot section has empty state handling
- `npm run lint && npm run type-check` passes

**Implementation reference:** Spec §6.5 (C-end pages)

- [ ] Create `src/views/chatbot/ChatbotChat.vue`: follow SalesRAG chat interface pattern (look at existing `src/views/SalesView.vue` or `src/components/ChatArea.vue` for reference).
  - Left sidebar: session list from `useChatbotStore.sessions`, "新对话" button to create session, click to switch, swipe/button to delete.
  - Right chat area: message list from `useChatbotStore.messages`, Markdown rendering (reuse existing Markdown component if available), streaming text with cursor indicator.
  - Input: text input + send button. Enter key sends. Disabled while `streaming` is true.
  - SSE: on send, store calls `new EventSource(url)` or `fetch()` with ReadableStream for POST SSE. Parse `data: {...}` lines, update last message content reactively.
  - Credits: before sending, check user credits. If insufficient, show `InsufficientCreditsDialog`.
- [ ] Modify homepage component: add "智能体" section with card grid. Data from `listVisibleChatbots()` API. Each card: avatar image, name, description. Click → navigate to `/chatbot/:id`. Empty state: "暂无可用智能体" message.
- [ ] Run `npm run lint && npm run type-check`
- [ ] Commit: `feat(config): add chatbot chat page and homepage chatbot section`

---

### Task 14: S5 Verification Strategy

**验证方式:** gstack `/qa` 浏览器测试 + 后端 `go test ./...`

**理由:** 功能涉及 UI 交互（配置中心、对话页面）和 API 集成，浏览器 QA 可覆盖端到端用户路径。后端单元测试覆盖 biz 层逻辑（权限校验、积分扣费、模板发布）。不写 Playwright E2E 因为功能主要是 CRUD + 对话，核心风险在后端权限逻辑（已有 biz 测试覆盖）。

**回归保护声明:** gstack `/qa` 是一次性验证，不产生持久化测试代码。未来修改此功能需手动重新运行 `/qa`。如果后续功能涉及更复杂的用户路径（如积分扣费边界情况），应考虑补充 Playwright E2E。

**关键用户路径（S5 需验证）:**

1. **B 端知识库管理**
   - [ ] 父用户登录 → 侧边栏可见"配置中心"
   - [ ] 创建知识库 → 上传文档 → 文档状态变为 COMPLETED
   - [ ] 删除知识库 → 已挂载的 chatbot 自动解挂

2. **B 端智能体配置**
   - [ ] 创建 chatbot（填写名称、提示词、挂载知识库）→ 状态为 draft
   - [ ] 测试对话：点击"测试"→ 进入对话页 → 发送消息 → 收到 AI 流式响应
   - [ ] 发布 chatbot → 状态变为 published

3. **B 端 SOP 配置**
   - [ ] 创建 SOP 模板 → 添加 3 个步骤 → 拖拽排序 → 保存
   - [ ] 发布模板 → 子用户可见

4. **C 端智能体使用**
   - [ ] 子用户登录 → 首页显示父用户发布的 chatbot 卡片
   - [ ] 点击卡片 → 创建会话 → 发送消息 → 收到流式响应 → 积分扣除
   - [ ] 验证子用户看不到"配置中心"入口

5. **C 端 SOP 使用**
   - [ ] 子用户首页看到发布的 SOP 模板
   - [ ] 点击执行 → 正常走完所有步骤

6. **权限隔离**
   - [ ] 子用户访问 `/config/*` → 重定向到首页
   - [ ] 父用户 A 的 chatbot/SOP 不出现在父用户 B 的子用户列表中

7. **Langfuse 可观测性**
   - [ ] 触发一次 chatbot 对话 → 检查 Langfuse 出现 trace `chatbot-chat`
   - [ ] trace 包含 span `context-assembly` + generation `chatbot-llm-call`
