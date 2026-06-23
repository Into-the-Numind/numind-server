# S3 Plan — xhs-collector

> Task 切分。每个 task 是可独立构建+验证的原子单元（NDF Rule 9）。S0–S2 见 design.md。

## Tasks

### T1 — 数据模型 + migration（DONE）
- `internal/pkg/model/xhs_topic.go`：`XhsTopicNote` GORM model + `XhsEnrich*` / `XhsNoteType*` 常量 + `TableName()`。照 `MonitorNote` 约定。
- `migrations/20260624_015740_create_xhs_topic_note.sql`：`CREATE TABLE IF NOT EXISTS xhs_topic_note`，字段/索引与 model 严格一致。
- `internal/pkg/model/xhs_topic_test.go`：in-memory SQLite AutoMigrate + 往返 + 唯一索引 + nullable 单测。
- **nullable 契约**：`crawled_at` 非指针 `time.Time` → SQL `NOT NULL DEFAULT CURRENT_TIMESTAMP`（与 GORM 渲染一致，无 schema drift）。指针字段 → NULL。
- **验收**：`go build ./...` + `go test ./internal/pkg/model/...` 全绿；gofmt 干净。

### T2 — store 层（TODO）
- `internal/numind/store/xhs_topic.go`：`IXhsTopicStore`（UpsertByUserNote / List(offset,limit) (xs,total,err) / Get / ListPendingEnrich / UpdateEnrichResult）。
- 照 `database.md` store 约定：context 首参、分页返回 `([]T,int64,error)`、`gorm.ErrRecordNotFound` 转 domain error。

### T3 — 采集摄入 API（TODO）
- 用户端 `POST /v1/xhs/notes`：插件 payload 绑定校验 → 算 content_hash → upsert by (user_id, xhs_note_id) → 置 `enrich_status='pending'`。
- controller 只做绑定/auth/调 biz/格式化；业务在 biz。注册 router.go。

### T4 — LLM 富化 biz（TODO）
- biz：扫 pending → `aiservice.Chat` 生成 6 分析字段；视频 `aiservice.ASR` 转写。
- 计费 Reserve/Reconcile；积分不足置 `insufficient_credits` 保留原始数据。Langfuse trace。

### T5 — 列表/详情 API（TODO）
- 用户端 `GET /v1/xhs/notes`（分页）/ `GET /v1/xhs/notes/:id` / 重试富化端点。注册 router.go。

### T6 — 前端（numind-web-v3，TODO）
- 选题库列表 + 详情；4 状态处理；销毁性操作确认 dialog。

### S5 验证策略（task）
- 见 design.md「S5 验证策略」。T1 后端 TDD 单测；涉及计费的 T4/T6 需 Playwright E2E 留回归保护。
