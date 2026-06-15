# Chatbot 图片识别 — 实施计划（S3）

> feature `chatbot-image-recognition` · 2026-06-16 · 蓝本 spec.md
> 后端 task 在前、前端在后。每 task 独立可构建 + 验证。

## 依赖图
```
T1(multimodal包) ─┐
T2(DB schema)    ─┼─► T3(ChatStream改造) ─► T4(controller)
                  │
T6(FE api/store) ─────► T7(FE UI)
T1..T7 ─► T8(S5 验证策略 = 文档 task, 实测在 S5)
```
- T1、T2 互不依赖，可并行（同仓库 disjoint file → Tier 3 需 check-disjoint）。
- T6 前端独立（按 §2 API 契约 mock），与后端 Tier 2 跨仓库并行。
- T3 ← T1+T2；T4 ← T3；T7 ← T6。

---

## T1 — 共享包 `internal/numind/biz/multimodal`（后端）
**目标**：把 agent 的 capability 路由抽成模式无关共享件，chatbot 复用，**不碰 agent**。

**涉及文件（新增）**：
- `internal/numind/biz/multimodal/multimodal.go`
- `internal/numind/biz/multimodal/multimodal_test.go`

**内容**：
- `BuildUserParts(ctx, userMessage string, atts []*model.AgentAttachment, modelKey string, attStore store.IAgentAttachmentStore) (parts []aiservice.MessagePart, hasInlineImage bool, err error)` — 复制 `agent/multimodal.go:buildAgentInputForModel` 的路由逻辑，**改为返回 `(parts, hasInlineImage, err)`**（hasInlineImage = 至少有一个 `MessagePartTypeImageURL`）。
- `LoadAttachmentsByIDs(ctx, attStore, ids []uint64, userID uint) []*model.AgentAttachment` — 复制 `agent/student_run_lifecycle.go:loadAttachmentsByIDs`（逐 id `GetByIDAndUser`，失败静默跳过）。
- `FlattenTextParts(parts []aiservice.MessagePart) string` — 全 text parts join（`\n` 分隔）。
- 复制内部 helper：`presignAttachmentURL`、`extractCOSObjectKey`、`waitForFallback`、`mkInlineBlock`、`pendingFallbackTextFor`、`textFallbackOf`，常量 `fallbackMaxWait=1500ms`/`fallbackPollInterval=100ms`/`presignExpiry=15min`。每个 copy 顶部注释 `TODO(dedup): 与 agent/multimodal.go 重复，agent 应迁移到本包`。

**单测**：
- 识图模型（mock caps.AcceptsImageInline=true）+ image att → parts 含 `MessagePartTypeImageURL`，hasInlineImage=true。
- 非识图模型（caps=false）+ image att（TextFallback 已就绪）→ parts 全 text，hasInlineImage=false。
- fallback 未就绪 + 超时 → pending 占位文本。
- `LoadAttachmentsByIDs`：他人 id 被 `GetByIDAndUser` 过滤跳过。
- capability 包用 `capability.Init`（in-mem）或注入 mock；attStore 用 fake。

**验收**：`go test ./internal/numind/biz/multimodal/...` 绿；`task lint` 0。T1 完成后包可独立编译，不依赖 chatbot。

---

## T2 — DB schema + model + store（后端）
**目标**：chatbot_message 持久化 attachments。
**⚠ S3 review P1 终解：不放宽 chatbot.stream task profile**（`matchLLM` 子集过滤会排除 agnes 默认模型，识图靠 ModelOverride 绕过过滤——见 spec §3.5）。**T2 不含任何 task profile migration。**

**涉及文件**：
- 新增 `migrations/20260616_HHMMSS_chatbot_message_attachments.sql`
- 改 `internal/pkg/model/chatbot_message.go`：加 `Attachments []MessageAttachment gorm:"column:attachments;serializer:json"` + `MessageAttachment{ID,Filename,MimeType}` struct
- 改 `internal/numind/store/chatbot_session_store.go`：`CreateMessage` 写入 + `ListMessages` 读出 attachments（serializer 自动，确认 Select 不漏列）

**migration 要点**：`ALTER TABLE chatbot_message ADD COLUMN attachments JSON NULL;` 幂等（先查 information_schema 或用 `ADD COLUMN IF NOT EXISTS` 视 MySQL 版本；参照仓库既有 migration 写法）。dev/prod CI **不自动跑 migration**（见 memory），S6 部署前手工 SSH 执行——plan 末提醒。

**单测**：store 层 CreateMessage→ListMessages round-trip，attachments 非空时正确序列化/反序列化为 `[]MessageAttachment`（in-mem SQLite，注意 SQLite 对 JSON 列的兼容——若 serializer:json 在 SQLite 退化为 TEXT 仍可 round-trip）。

**验收**：`go test ./internal/numind/store/... ./internal/pkg/model/...` 绿；migration SQL 语法自查。

---

## T3 — `ChatStream` 双路径改造（后端）
**目标**：ChatStream 接 attachment_ids，按 capability 分流 inline/fallback。

**涉及文件**：
- 改 `internal/numind/biz/chatbot/stream.go`：
  - `ChatStream` 签名加 `attachmentIDs []uint64`（放 message 后、modelKey 前，与 controller 对齐）。
  - 在 step 5/6 之间：`atts := multimodal.LoadAttachmentsByIDs(...)`；若非空 → `BuildUserParts`。
  - `hasInlineImage` → bill-only 分支：`buildVisionMessages`（新 helper，复用 buildChatMessages 的 system/history，user 换 Parts；KB 在场把 grounding+参考资料拼 system）；`ctx = aismw.WithGatewayBillingOnly(ctx)`；`gatewayReq.Messages = aiMessages`；`gatewayReq.ContextFragments = nil`。
  - 非 inline → `message = multimodal.FlattenTextParts(parts)`，用新 message 重建 `messages`+`ctxFragments`（复用已取 historyMsgs/retrievedChunks，不重查）。
  - 持久化 user 消息时写 `Attachments`（从 atts 映射 `{ID,Filename,MimeType}`）。
  - trace input 加性补 `attachment_count`/`vision_path`。**（S3 review P2）trace 元数据测试 trade-off**：Langfuse 是全局优雅降级，难做确定性单测 → 这两个字段靠 **S5 人工 Langfuse 核验**（observability 非功能），不强加单测断言；功能正确性由"is bill-only + Parts"断言覆盖。
- 改 `internal/numind/biz/chatbot/biz.go`（或 chatbotBiz 构造处）：struct 加 `attStore store.IAgentAttachmentStore`，构造注入 `ds.AgentAttachments()`。
- **同步更新 `IChatbotBiz` 接口的 `ChatStream` 签名**（接口定义在 chatbot controller 包或 biz 包；`var _ IChatbotBiz = (*chatbotBiz)(nil)` 编译断言会强制对齐——不漏）。

**依赖**：T1（multimodal）、T2（model.Attachments 字段）。

**单测**（`stream_test.go` 或新增）：
- 识图 turn → `WithGatewayBillingOnly` 被注入 + gatewayReq.Messages 末条含 image_url Part + ContextFragments=nil。
- 非识图 turn → message 含 fallback 文本，走 fragment 路径（ContextFragments 非空）。
- 无 attachment_ids → 行为不变（回归）。
- 用 fake gateway/aiservice 捕获 ctx flag + req（或抽一个可注入的 chatFn）。

**验收**：`go test ./internal/numind/biz/chatbot/...` 绿；`task lint` 0。

---

## T4 — chatbot controller 接 attachment_ids（后端）
**涉及文件**：改 `internal/numind/controller/v1/chatbot/chatbot.go`：`chatReq` 加 `AttachmentIDs []uint64 json:"attachment_ids,omitempty"`；`Chat` 把 `req.AttachmentIDs` 传给 `ChatStream`。

**依赖**：T3（签名）。
**验收**：编译通过；`task lint` 0。controller 仅参数透传（无业务逻辑，符合 controller 职责）。

---

## T6 — 前端 api + types + store（numind-web-v3）
**涉及文件**：
- 改 `src/types/agent.ts`：共享 `UploadResponse` 加 `id: number`（**不新建 chatbot-local type**）。
- 改 `src/types/config.ts`：`ChatbotMessage` 加 `attachments?: { id:number; filename:string; mime_type:string }[]`。
- 改 `src/api/chatbot.ts`：`sendChatbotMessageStream(...)` 加 `attachmentIds?: number[]` → `body.attachment_ids`。
- 改 `src/stores/chatbot.ts`：state 加 `imageAttachments: ref<{id,filename,previewUrl}[]>`；`uploadImage(file)`（调 `uploadAttachment` 捕获 id + 本地 previewUrl）；`removeImage`；`sendMessage` 透传 `attachment_ids` + 发送后清空。

**单测（S3 review P2 — 图片/文档分流）**：vitest 验 `sendMessage` 带 1 图 + 1 文档时，`imageAttachments`（id 暂存）与 useDocUpload 文档（文本注入）**分开处理**，`attachment_ids` 只含图片 id；发送后 `imageAttachments` 清空。

**验收**：`npm run type-check` 0；`npm run lint`（scope 改动文件）0；vitest（store + 分流单测）绿。

---

## T7 — ChatbotChat.vue 上传 UI（numind-web-v3）
**涉及文件**：改 `src/views/chatbot/ChatbotChat.vue`：
- 文件 `accept` 增 `.png,.jpg,.jpeg`；图片分流到 `store.uploadImage`（文档仍走 useDocUpload）。
- 输入区预览条加图片缩略图（本地 blob）+ 删除；复用 agent `attachment-strip` 样式 + `useImagePreview` 放大。
- 用户气泡渲染 `message.attachments` 文件名 chip（reload 持久化展示，对齐 agent `📎 filename`）。

**依赖**：T6。
**验收**：`npm run type-check` 0；`npm run lint` 0；浏览器手测在 S5。

---

## T8 — S5 验证策略（rule 10，文档 task）
**验证方式**：后端 Go 单测（持久回归）+ 前端 vitest + 本地 gstack `/qa`（浏览器）。
**理由**：核心识别/计费逻辑（multimodal 路由 + ChatStream 分流 + bill-only 计费）有 Go 单测做持久回归保护；端到端视觉/UX/上传交互一次性 /qa 确认。非 bug-from-customer（新功能），无强制复现测试。涉及计费（vision）属中风险 → 关键逻辑必须有 Go 单测覆盖（已在 T1/T3）。
**S5 关键用户路径**：
1. 登录 chatbot → 选识图模型（qwen3-vl-flash）→ 上传图片 → 提问 → 验回答基于真实图像 + done/usage 的 ModelName == 识图模型 key（验 ModelOverride 未静默 fallback）。
2. 切默认/非识图模型（agnes）→ 上传同图 → 提问 → 验透明降级（基于文字描述回答，无报错）。
3. 刚上传立即发送（fallback 未就绪）→ 验 ≤1.5s 等待 / 占位不阻断。
4. reload 会话 → 验用户气泡图片 chip 持久化。
5. 纯文本对话回归 → 行为不变。
6. 计费 DB 核验：识图 turn 在 usage_record / credit 链路记 `llm_vision` + reserve/reconcile 落账。

---

## 跨仓库 / 并发提醒
- **并发**：`stream.go`（T3）同时被活跃 feature `adaptive-session-titles`(S3) 修改。**S4 实现 T3 前 `git fetch origin develop` + 核对**；ndf-done merge 前再 rebase/核对，冲突人工解。
- **migration**：dev/prod 不自动跑（memory `project_dev_deploy_migration_gap`）。S6 `/deploy-dev` 前手工 SSH 执行 T2 的 migration。
- **Tier**：T1+T2 同仓库 disjoint → 主 session 串行实现（量不大，不值得 sub-worktree）；前端 T6/T7 与后端跨仓库 Tier 2。主 session 顺序实现 + 每 task 完成并行双 reviewer（rule 6）。

## S3 原子性审查吸收（Sonnet CONDITIONAL_PASS，0 P0）
- **P1（task profile 必要性）终解**：代码核实 `matchLLM`(capability.go:125-131) 子集过滤——放宽 `chatbot.stream` 到 `["text","image"]` 会排除纯文本 agnes 默认模型。**故删除 task profile migration，保持 `["text"]`**，识图靠 ModelOverride 绕过过滤（比 reviewer 字面建议更正确）。已改 T2 + spec §3.5/§7。
- **P2（trace 元数据无测试）**：trace `attachment_count`/`vision_path` 靠 S5 人工 Langfuse 核验（observability 非功能），已在 T3 标注 trade-off。
- **P2（前端分流无单测）**：T6 加图片/文档分流 vitest，已补。
- 原子性/依赖/spec 覆盖/S5 策略：reviewer 判 PASS。code task = T1–T7（7 个），T8 为验证策略文档 task。
