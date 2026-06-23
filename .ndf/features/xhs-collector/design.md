# S0–S3 — xhs-collector（小红书选题采集 + 富化累积选题库）

> 跨仓库 standard feature（numind-server 后端 + numind-web-v3 用户端）。后端先行 T1 数据模型。
> 本文件为 S0 需求 / S1 提案 / S2 规格 / S3 计划的合并工件（artifact-before-code，T1 编码归 S4）。

## S0 需求 / 依据

有数当前 GTM = 深耕小红书 IP 孵化陪跑。自媒体作者做选题时需要持续观察对标账号的爆款笔记，人工逐条看效率低、洞察难沉淀。

需求：客户用浏览器插件在小红书页面采集笔记（标题/正文/标签/封面/数据/热评/作者），插件把结构化 payload POST 到有数后端，后端落库进**用户私有的累积选题库**，并用 LLM 对每条笔记做选题级分析（选题角度、爆款原因、可借鉴点、目标人群、标题公式、一句话总结），供作者复盘与二次创作参考。视频笔记额外做口播稿转写（video → ASR）。

- **采集主体**：浏览器插件（利用客户真实登录态，绕反爬）。后端只收结构化 payload，不自己爬。
- **多租户**：每条笔记归属 `user_id`，库是 per-user 累积的。
- **去重 + 防重复扣分**：同一用户同一 `xhs_note_id` 唯一（`uk_xtn_user_note`）；`content_hash = SHA256(title+content+video_url)` 用于判定内容是否变化、避免重复富化重复扣积分。
- **计费**：LLM 富化走积分制（Reserve/Reconcile）。积分不足时 `enrich_status='insufficient_credits'`，笔记保留原始采集数据不丢。

## S1 提案（范围 / 切口）

最小可用切口（按 task 拆分，T1 为本次提交）：

1. **T1 数据模型 + migration**（本次）：`xhs_topic_note` 表 + GORM model `XhsTopicNote` + 单测。
2. T2 store 层：`IXhsTopicStore`（Create/Upsert by (user_id,note_id)、List 分页、Get、按 enrich_status 查询、更新富化字段），照 `internal/numind/store` 既有约定。
3. T3 采集摄入 API（用户端）：`POST /v1/xhs/notes`（插件上送 payload，去重 upsert，置 `enrich_status='pending'`）；注册 router.go。
4. T4 LLM 富化 biz：异步消费 pending 笔记，走 `aiservice.Chat` 生成 6 分析字段；视频走 ASR 转写填 `video_transcript`。计费 Reserve/Reconcile。
5. T5 列表 / 详情 API（用户端）：分页列表 + 详情 + 触发/重试富化。
6. T6 前端（numind-web-v3）：选题库列表 / 详情视图，4 状态（loading/empty/error/success）。
7. S5 验证策略：见下。

## S2 规格（T1 数据模型）

表 `xhs_topic_note`（model `internal/pkg/model/xhs_topic.go`，照 `MonitorNote` 约定：显式字段、无软删除、显式 `TableName()`）。

关键字段与约束：

| 字段 | 类型 | 约束 / 说明 |
|------|------|------|
| `id` | `uint64` | PK autoIncrement |
| `user_id` | `uint` | NOT NULL；`idx_xtn_user_crawled`(priority1) + `uk_xtn_user_note`(priority1) |
| `xhs_note_id` | `string(100)` | NOT NULL；`uk_xtn_user_note`(priority2) |
| `content_hash` | `string(64)` | `idx_xtn_hash`；SHA256(title+content+video_url) 防重复富化/扣分；`json:"-"` |
| `note_type` | `string(20)` | default `normal`；`normal`/`video` |
| 采集内容 | title/content/tags(JSON)/cover_url/note_url/published_at | 原始笔记数据 |
| `video_url` / `video_transcript` | string / `*string` | transcript NULL=未转写或直链失效（区分两态用 enrich_status） |
| 互动数据 | like/collect/comment/share_count | default 0 |
| `comments` | JSON | 热门 ≤10 条，每条 text ≤200 字 |
| 作者 | author_name/link/followers | followers 取不到=0（已知限制） |
| 6 LLM 字段 | ai_topic_angle / ai_viral_reason / ai_borrowable / ai_target_audience / ai_title_formula / ai_one_line | type text（one_line size 500） |
| `enrich_status` | `string(24)` | default `pending`；`idx_xtn_enrich`；枚举 pending/enriching/done/partial/failed/insufficient_credits |
| `collected_at` | `*time.Time` | 客户端采集时刻（payload 传入，可空） |
| `crawled_at` | `time.Time` | 服务端入库时刻，**非指针 = NOT NULL**；`idx_xtn_user_crawled`(priority2) |
| created_at / updated_at | `time.Time` | GORM 自动维护 |

**枚举常量**：`XhsEnrich*`（6 个状态）、`XhsNoteType*`（normal/video）。

**Nullable 契约（model 与 migration 必须一致）**：
- 指针字段（`*string` / `*time.Time`）→ DB 列 NULL（`video_transcript`、`collected_at`、`published_at`）。
- 非指针 `time.Time`（`crawled_at`）→ DB 列 **NOT NULL DEFAULT CURRENT_TIMESTAMP**（与 GORM AutoMigrate 渲染一致，避免 schema drift；review P1 修正点）。

**索引**：`uk_xtn_user_note`(user_id,xhs_note_id) 唯一去重 / `idx_xtn_user_crawled`(user_id,crawled_at) 列表分页 / `idx_xtn_enrich`(enrich_status) 富化队列扫描 / `idx_xtn_hash`(content_hash) 内容变化判定 / `idx_xtn_published`(published_at)。

migration `migrations/20260624_015740_create_xhs_topic_note.sql`：`CREATE TABLE IF NOT EXISTS` 幂等；字段/索引与 model 严格一致；dev 需手工 SSH 执行（CI 不跑 migration）。

## S3 计划（task 切分 + S5 验证策略）

- **T1**（本次）：model + migration + 单测。验收：`go build ./...` 过、`go test ./internal/pkg/model/...` 过（AutoMigrate 校验所有 GORM tag、唯一索引、nullable 往返）。
- T2–T6：见 S1 提案。

### S5 验证策略
- **后端 TDD**：T1 已用 in-memory SQLite AutoMigrate 单测覆盖 GORM tag 合法性 / 唯一索引拒重 / nullable 指针往返。后续 store/biz 层用 mocked store 单测，覆盖去重 upsert、富化状态机、计费 Reserve/Reconcile。
- **前端关键路径（T6 后）**：用 Playwright/gstack `/qa` 验采集库列表→详情→重试富化。涉及积分扣减（高风险业务），应写 Playwright E2E 留回归保护，不止 gstack 一次性验证。
- **理由**：T1 纯数据层无 UI，TDD 单测足够；后续涉及计费的 task 需 E2E。

## 风险
- 浏览器插件采集合规 / 反爬封号 → 用客户真实登录态、插件侧采集，后端不主动爬（参考 memory 中"自托管硬爬被禁言"教训）。
- LLM 富化成本 → 按用户积分扣；content_hash 防重复扣分；积分不足优雅降级保留原始数据。
- 视频直链时效 → transcript NULL + `enrich_status='partial'` 区分"直链失效"与"未转写"。
