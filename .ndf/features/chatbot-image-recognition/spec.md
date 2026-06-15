# Chatbot 图片识别 — 技术设计 Spec

> 状态：S2 · 2026-06-16 · feature `chatbot-image-recognition`
> 蓝本：移植 Agent mode 图片识别双路径，落到 chatbot `ChatStream`。

## 1. 架构总览

```
前端 ChatbotChat.vue
  ├─(1) 选图 → POST /v1/agent-attachments (multipart,复用) → {id,url,...}
  │         后端 UploadService: COS + agent_attachment 行 + Enqueue 异步 VLM/OCR → text_fallback
  └─(2) 发送 → POST /v1/chatbot/sessions/:id/chat  body 增 attachment_ids:[]
            后端 ChatStream:
              loadAttachmentsByIDs(归属校验)
              caps = GetCapabilities(modelKey).AcceptsImageInline
              ├─ 能识图 → bill-only 路径: 自拼完整 Messages(含 image_url Parts) + WithGatewayBillingOnly + ContextFragments=nil
              └─ 不能识图 → 透明降级: waitForFallback 取 text_fallback 拼进 user 文本 → 现有 fragment 路径(不变)
```

**核心不变量**：
- 无图片 turn / 纯文本 → **现有 fragment 路径完全不动**。
- 非识图模型 + 图片 → 图片转文字后仍是纯文本 → **走 fragment 路径**（享受压缩/grounding/历史管理）。
- 识图模型 + 图片 → **唯一需要 bill-only 的分支**（image_url 必须以 Parts 到达 adapter）。

## 2. API 契约

### 2.1 上传（复用，零改动）
- `POST /v1/agent-attachments`（multipart，字段 `file`）→ `{id, url, filename, mime_type, size, modality, fallback_ready}`
- `GET /v1/agent-attachments/:id/status` → `{id, fallback_ready, modality, fallback_error?}`

### 2.2 发消息（改 — 增字段）
- `POST /v1/chatbot/sessions/:id/chat`
  - **Request**（新增 `attachment_ids`）：
    ```json
    { "message": "这张图说明什么", "model_key": "qwen3-vl-flash-2026-01-22",
      "thinking": false, "attachment_ids": [123, 124] }
    ```
  - `attachment_ids` 可选、`omitempty`；空或缺省 = 纯文本，行为同现状。
  - **Response**：SSE 事件类型不变（`token`/`thinking`/`done`/`error`）。

### 2.3 历史消息（改 — 加性）
- `GET /v1/chatbot/sessions/:id/messages`：每条 message 加性返回 `attachments?: [{id, filename, mime_type}]`（仅 user 消息有；assistant 消息无）。

## 3. 后端设计（numind-server）

### 3.1 新共享包 `internal/numind/biz/multimodal`（解耦 agent，不碰 agent）

把 capability 路由从 package `agent` 抽出为模式无关共享件（仅依赖 capability/util/store/model/aiservice）。**agent 的副本保持不动**（活跃 feature 冲突规避；agent 迁移到本包记为后续技术债）。

导出 API：
```go
package multimodal

// BuildUserParts 按模型 capability 路由 attachments，返回组装好的 user 消息 parts。
//   - 识图模型: 图片 → MessagePartTypeImageURL(presigned)；hasInlineImage=true
//   - 非识图模型: 图片 → waitForFallback 取 text_fallback 文本 part；hasInlineImage 不因图片置 true
// userMessage 作为首个 text part。parts 全为 text 时调用方可扁平化回字符串走 fragment 路径。
func BuildUserParts(
    ctx context.Context,
    userMessage string,
    atts []*model.AgentAttachment,
    modelKey string,
    attStore store.IAgentAttachmentStore,
) (parts []aiservice.MessagePart, hasInlineImage bool, err error)

// LoadAttachmentsByIDs 逐 id GetByIDAndUser 归属校验，失败行静默跳过（不 abort）。
func LoadAttachmentsByIDs(ctx, attStore, ids []uint64, userID uint) []*model.AgentAttachment

// FlattenTextParts 把全 text 的 parts 拼回单字符串（非识图路径用）。
func FlattenTextParts(parts []aiservice.MessagePart) string
```
内部迁入（从 agent/multimodal.go **复制**，非移动）：`presignAttachmentURL`、`waitForFallback`、`mkInlineBlock`、`pendingFallbackTextFor`、`textFallbackOf`、常量 `fallbackMaxWait=1500ms`/`fallbackPollInterval=100ms`/`presignExpiry=15min`。

> 决策：复制而非重构 agent。理由：agent 有 2 个活跃 feature 在改其文件，重构 agent 会引入 merge 冲突且超出本 feature 边界。实际复制体量 **~200 行**（S2 review 修正，非 90 行：`buildAgentInputForModel`+`waitForFallback`+`presignAttachmentURL`+`extractCOSObjectKey`+`mkInlineBlock`+`pendingFallbackTextFor`+`textFallbackOf` 含注释）。每个函数顶部注释标注 `TODO(dedup): 与 agent/multimodal.go 重复，agent 应迁移到本包`，便于 reviewer audit。

### 3.2 `chatbotBiz` 注入 attachment store
- `chatbotBiz` struct 加字段 `attStore store.IAgentAttachmentStore`。
- 构造处（`biz.go` 的 `Chatbot()` accessor / chatbotBiz 构造）注入 `ds.AgentAttachments()`。

### 3.3 `ChatStream` 改造（`biz/chatbot/stream.go`）

签名：`ChatStream(ctx, userID, sessionID uint, message string, attachmentIDs []uint64, modelKey string, thinking bool, handler StreamHandler) error`（新增 `attachmentIDs`）。

流程改动（在现有 step 6 构建 messages 之后、step 7 调 gateway 之前插入分支）：
```
atts := multimodal.LoadAttachmentsByIDs(ctx, b.attStore, attachmentIDs, userID)   // 空 → nil
if len(atts) == 0 {
    // 现状不变：fragment 路径
} else {
    parts, hasInlineImage, _ := multimodal.BuildUserParts(ctx, message, atts, modelKey, b.attStore)
    if hasInlineImage {
        // —— bill-only 识图路径 ——
        // 自拼完整 Messages（绕过 fragment 渲染）:
        aiMessages = buildVisionMessages(config, historyMsgs, retrievedChunkContents, parts)
        //   [0] system: SystemPrompt (+grounding+参考资料 if KB)
        //   [1..N] history (text)
        //   [last] user: Content.Parts = parts(text + image_url)
        ctx = aismw.WithGatewayBillingOnly(ctx)
        gatewayReq.Messages = aiMessages
        gatewayReq.ContextFragments = nil      // bill-only 忽略 fragments
    } else {
        // —— 非识图/透明降级：拼回纯文本，走 fragment 路径 ——
        message = multimodal.FlattenTextParts(parts)   // message + fallback 文本
        // 重新构建 messages / ctxFragments（用增强后的 message）
    }
}
```
- `buildVisionMessages`：新 chatbot-local helper，复用 `buildChatMessages` 的 system/history 拼装逻辑，仅把 user 消息换成 `Content.Parts`；KB 存在时把 grounding(`chatbotGroundingPrompt`)+参考资料拼进 system 文本。
- 非识图路径：把 `message` 替换为 flatten 后文本，然后用新 message **重新构建** `messages` + `ctxFragments`（调 `buildChatMessages` / `BuildChatContextFragments`）。**复用第一次已取的 `historyMsgs` / `retrievedChunks` / `retrievedChunkContents`，不重复查 DB / 不重复检索**（仅这两个组装函数用新 message 跑一遍）。Langfuse `prompt-construction` span 已 End，重建后 fragment count 不再补记（可接受 P2）。
- **实现顺序**：分支判定（load atts → BuildUserParts → hasInlineImage）应放在 step 6 组装 messages **之前**，避免组装两次；纯文本/非识图先确定最终 message 文本再组装一次。
- **计费**：bill-only `synthBillOnlyResult.ChargeUser=true` → reserve/reconcile 正常；fragment 路径计费亦不变。
- **Trace**：不变，主 LLM generation 由 Gateway 自动挂（含 image_url → `llm_vision`）。

### 3.4 持久化（AS-6）
- migration `YYYYMMDD_HHMMSS_chatbot_message_attachments.sql`：`ALTER TABLE chatbot_message ADD COLUMN attachments JSON NULL;`（加性、可空、幂等 `IF NOT EXISTS` 视 MySQL 版本，用兼容写法）。
- `model.ChatbotMessage` 加字段 `Attachments []MessageAttachment gorm:"column:attachments;serializer:json" json:"attachments,omitempty"`。`MessageAttachment` struct = `{ID uint64; Filename string; MimeType string}`。
  - **选型（S2 review P2）**：用 `gorm:"serializer:json"`（GORM 自带 JSON serializer）承载 Go slice，**不用 `datatypes.JSON`**（后者是 `[]byte` 需二次 `json.Unmarshal`，易产生 `null`/`[]`/字节串格式不一致 bug）。`ListMessages` response 直接返回该 Go slice，JSON 序列化天然 `[]`，`omitempty` 时 nil → 字段省略。
- `ChatStream` 持久化 user 消息时写入 `Attachments`（来自 loaded atts）。
- `ListMessages` 读出 `attachments` 原样返回（无需 presign：仅展示文件名 chip，**不渲染图片**——与 agent `📎 filename` 一致，规避 COS presign-on-read 复杂度；reload 看缩略图列为后续增强）。

### 3.5 task profile — **不放宽**（S3 review 修正）
- **决策（代码核实）**：`profile/capability.go:125-131` `matchLLM` 是**子集过滤**——task `requirements.input_modalities` 要求的模态必须全在模型支持列表里。若把 `chatbot.stream` 放宽到 `["text","image"]`，纯文本的 **agnes（0元会员默认模型）会被排除出默认路由**，破坏省钱默认 + free-model-member 设计。**故保持 `chatbot.stream` = `["text"]`，不改 task profile。**
- 识图模型经 **ModelOverride** 到达：`gateway.go` 在 `req.ModelOverride!=""` 时走 `registry.ResolveByModelKey`，**绕过 `matchLLM` requirements 过滤**（双 reviewer + 代码确认），所以用户在 ModelSelector 选 `qwen3-vl-flash` 时即便 task profile 限 `["text"]` 也能正常解析到识图模型路由。
- S5 验证 `done`/usage 的 ModelName == 用户所选识图模型 key，确认 ModelOverride 未静默 fallback。

## 4. 前端设计（numind-web-v3）

### 4.1 上传 + 暂存
- `ChatbotChat.vue`：文件 `accept` 增 `.png,.jpg,.jpeg`（与文档共存）。图片走 `uploadAttachment(file)`（`api/agent.ts` 复用）→ **捕获返回的 `id`**。
  - **`UploadResponse.id`（S2 review P1）**：当前 `types/agent.ts` 的共享 `UploadResponse` 漏了 `id`（后端 `/v1/agent-attachments` 实际返回了 `id`，前端类型没接）。改动是**在共享 `types/agent.ts` 的 `UploadResponse` 上加 `id: number`**（加性、安全），**禁止**为 chatbot 新建局部类型——保持单一类型来源，agent 侧不受影响。
- chatbot store 加 `imageAttachments: ref<{id, filename, previewUrl}[]>`；上传中 spinner，可删除，≤5。
- 发送时把 `imageAttachments.map(a=>a.id)` 作为 `attachment_ids` 传入；发送后清空。

### 4.2 API
- `api/chatbot.ts` `sendChatbotMessageStream(...)` 加参数 `attachmentIds?: number[]` → body.attachment_ids。
- store `sendMessage` 透传 attachment ids。

### 4.3 渲染
- 输入区预览条：图片显示**缩略图**（本地 `URL.createObjectURL` blob）+ 删除按钮；复用 agent `attachment-strip` CSS + `useImagePreview` 点击放大。
- 消息气泡：`ChatbotMessage` 类型加 `attachments?: [{id, filename, mime_type}]`；user 气泡渲染**文件名 chip**（reload 时，复用 agent `AgentMessageItem` `📎 filename` 模式）。

## 5. Trace 拓扑
- 复用 `ChatStream` 已有 `langfuse.CreateTrace("chatbot-chat", ...)`。
- 主 LLM 流式 generation：Gateway Tracing 中间件自动挂（识图 turn 含 image_url，自动归 `llm_vision`）。
- VLM 描述生成：上传时 fallback worker 自建 `attachment.fallback` trace（独立于本 trace，已就绪）。
- 元数据：trace input 已含 message/chatbot_id/session_id/user_id；可加性补 `attachment_count`、`vision_path`(inline/fallback)。

## 6. 验证策略（S5，详见 plan 末 task）
- **后端 Go 单测（持久回归）**：
  - `multimodal.BuildUserParts`：识图模型→image_url part + hasInlineImage=true；非识图→text_fallback part；fallback 未就绪超时→pending 占位；归属过滤跳过他人 id。
  - chatbot stream：识图 turn→走 bill-only(WithGatewayBillingOnly 注入)+Parts；非识图 turn→message 含 fallback 文本走 fragment；无图 turn→行为不变。
- **前端 vitest**：上传捕获 id、发送带 attachment_ids、清空暂存。
- **本地浏览器 QA（gstack /qa）**：登录 → chatbot 选识图模型上传图片提问（验 inline 识别）→ 切非识图模型上传图片提问（验透明降级）→ reload 看图片 chip 持久化。
- **ModelOverride 路由解析校验（S2 review P1）**：识图 turn 完成后断言 `gatewayUsage.ModelName`（或 done 事件回填的模型）== 用户选的识图模型 key——验证 `ModelOverride` 没有因 task profile requirements 静默 fallback 到默认 agnes（否则会出现"算 agnes 的账、却没真识图"的隐性降级）。这条在 Go stream 单测 + 本地 /qa 双重覆盖。
- **理由**：核心识别逻辑 + 计费有 Go 单测持久回归；端到端视觉/UX 一次性 /qa 确认。非 bug-from-customer（是新功能），**无强制复现测试**（rule 11 不适用）。

## 7. 不做 / 边界
- 不改 agent（其 multimodal 副本留存）；不改共享 contextbudget 包；不改 SOP。
- **AS-6 v1 明确口径（S2 review P2，消歧义）**：reload 后用户气泡显示的是**文件名 chip**（对齐 agent `📎 filename`），**不是图片缩略图**。发送前的输入区预览可显示本地 blob 缩略图。reload 显示真图缩略图（需 messages 读路径 presign COS）列为**后续增强**，不在 v1。
- **task profile 不放宽（S2 P1 + S3 P1 终解）**：见 §3.5——`matchLLM` 子集过滤使放宽会排除 agnes 默认模型，故 `chatbot.stream` 保持 `["text"]`，识图靠 ModelOverride 绕过过滤。S5 用 ModelName 断言验证 ModelOverride 路由正确。
- PDF/音频 inline 不在本 feature（仅 image）。

## 8. 涉及文件预览（S3 细化）
- 新增：`biz/multimodal/*.go`、migration、`chatbot/vision_messages.go`(或并入 stream.go)
- 改：`controller/v1/chatbot/chatbot.go`、`biz/chatbot/stream.go`、`biz.go`(wiring)、`model/chatbot_message.go`、`store/chatbot_session_store.go`(读写 attachments)
- 前端改：`ChatbotChat.vue`、`api/chatbot.ts`、`stores/chatbot.ts`、`types/config.ts`、`types/agent.ts`(UploadResponse +id)
