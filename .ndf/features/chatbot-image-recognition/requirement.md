# Chatbot（AI 助手）图片识别

## 来源
- 提出人：用户（产品负责人）
- 提出日期：2026-06-16

## 需求描述
> "agent mode 里应该对识别图片这一块有完整的解决方案，你现在需要做的就是把 agent mode 对图片识别的处理方案，想办法移植到 AI 助手那个模式中（也就是 chatbot），要注意对能识图和不能识图的模型的处理方式的区分，以及如何能够合理的移植到 chatbot 模式中。"

结构化：当前 chatbot（"有数 AI" 聊天助手）完全不支持图片识别——前端文件选择器只接受文档类型（`.txt/.md/.pdf/.doc/.docx`），后端 chatbot chat 请求结构（`chatReq`）只有 `message` 文本字段，任务画像 `chatbot.stream` 的 `input_modalities` 只有 `["text"]`。把图片喂进去无任何识别能力。

而 Agent mode 已有一套**模式无关的完整图片识别方案**（双路径），需将其移植/复用到 chatbot。

## 业务目标
让 chatbot 用户能上传图片并被模型"看懂"，与 Agent mode 体验对齐。核心是**按所选模型是否支持识图分流**：
- **能识图的模型**（如 `qwen3-vl-flash`、`qwen-vl-plus`、`doubao-seed-1-8`）→ 把图片以 OpenAI 兼容 `image_url` content part 直接喂模型（inline）。
- **不能识图的模型**（含 prod 默认 0 元会员模型 `agnes-2.0-flash`）→ **透明降级**（用户已拍板）：后台 VLM（`qwen3-vl-flash`）+ 百度 OCR 预生成文字描述（`text_fallback`），以纯文本拼进消息喂给文本模型。用户无感。

## 优先级
中（功能增强，非线上故障）

## Triage
- 推荐轨道：**Standard**
- 分类理由（5 条标准评估，任一不满足即升 Standard）：
  1. 数据库 schema 变更：**很可能是**——`agent_attachment` 表可复用，但 chatbot 上传可能需要 `source` 字段区分来源，或 chatbot 上传端点配套；待 S2 定。
  2. 新增 API 端点：**是**——chatbot 需要图片上传入口（复用 `POST /v1/agent-attachments` 或新建 chatbot 上传端点）+ `ChatStream` 请求契约新增 `attachment_ids`。
  3. 新外部服务集成：否（VLM/OCR/COS 均已接入）。
  4. 影响文件数：**>3**——跨 `numind-server` + `numind-web-v3` 两仓库，含 controller、biz `stream.go`、请求结构、前端 `ChatbotChat.vue`、`api/chatbot.ts`、types。
  5. 高风险业务逻辑（支付/权限）：**部分涉及**——识图调用计费为 `llm_vision`（比文本贵），后台 VLM 描述生成有系统侧成本；非扣减核心但需在设计中明确计费口径。
- 人类决定：**确认 Standard，启动 S0**（2026-06-16，经 AskUserQuestion）

## 备注

### Agent mode 现有方案（移植蓝本，已调研）
- **入口/存储**：`POST /v1/agent-attachments`（multipart）→ 转存腾讯 COS + 写 `agent_attachment` 表（含 `modality`/`mime_type`/`width`/`height`/`ocr_text`/`vision_description`/`text_fallback`/`fallback_ready` 等字段）。
- **上传时（异步预生成）**：`FallbackService` worker pool（10 workers，per-user 并发上限 3，重试 3 次，启动时全局拉起）用 VLM + 百度 OCR 生成 `text_fallback`，存回 DB。
- **请求时（实时路由）**：`buildAgentInputForModel`（`biz/agent/multimodal.go`）查 `capability.GetCapabilities(modelKey)` 读 `ai_service.capability_json.accepts_image_inline`（5 min 缓存，未知模型保守降级 false）：
  - `true` → presign COS URL（15 min）→ `mkInlineBlock` 组装 `image_url` part。
  - `false` → `waitForFallback`（最多等 1500ms）取 `text_fallback` 纯文本 part；未 ready 注入"描述生成中"占位。
- **计费**：aiservice Gateway 的 `Billing` middleware 自动按 message 是否含 `image_url` part 分类 `llm_vision` vs `llm_chat`，**零改动自动生效**。
- **可观测**：fallback worker 每 job 建独立 Langfuse trace `attachment.fallback`；inline 调用由 Gateway Tracing middleware 自动挂 generation。

### 可直接复用（模式无关）
`agent_attachment` 表 + model + store、`UploadService`、`FallbackService`（整个 worker pool）、`capability` 判断、VLM/OCR prompt+模板、task profiles + capability seed、billing/langfuse middleware、aiservice 多模态类型（`MessagePart`/`ImageURL`/`MessageContent`）。

### Agent 专属耦合需解耦/重写
- `buildAgentInputForModel` 路由逻辑可复制，但其输出被 agent 专属的 `MessagesToInputString` **压平回字符串**塞进 ReAct `RunRequest.Input`（runner 过渡债）。chatbot 是流式 `ChatStream`，应让 `image_url` part **直接进 `ChatRequest.Messages`，不压平**——反而比 Agent 现状更干净。
- 入口请求契约（`CreateRunRequest` vs chatbot `ChatStream(userID, sessionID, message, modelKey, thinking, handler)`）需新增 `attachment_ids` 参数 + controller 绑定。
- `waitForFallback` 1500ms 等待对流式首字节有影响，需决定等待 vs 占位策略。
- legacy `attachment_urls` + `file_read` 工具提示路径是 agent 专属（chatbot 无工具），**不移植**。

### 关键产品决策（已拍板）
- 非识图模型上传图片 → **透明降级**（后台 VLM 转文字描述），与 Agent 一致。代价：即使在 0 元默认模型上传图也会触发一次后台 VLM 系统成本（当前不计入用户扣费）。

### 待 S1/S2 进一步明确
- chatbot 上传是否复用 `POST /v1/agent-attachments` 还是新建 chatbot 端点 / 共享 `UploadService`。
- 是否需要 `agent_attachment.source` 字段区分 chatbot/agent。
- 前端上传 affordance：图片是否与现有文档上传共用同一 "+" 入口、`accept` 放开图片格式。
- 流式场景下 fallback 未 ready 的等待/占位策略。
- 计费口径：后台 VLM 描述生成成本归属（系统成本 vs 用户扣减）。
