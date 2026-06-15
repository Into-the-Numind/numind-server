# Chatbot 图片识别 — 提案

## §1 方案概述 [客户可见]

让"有数 AI"（chatbot 聊天助手）支持上传图片并被模型识别，体验对齐已有的 Agent mode。用户在聊天框上传图片后：
- 若当前选的是**能识图的模型**（如 qwen3-vl-flash、qwen-vl-plus、doubao-seed-1-8 等），图片直接给模型"看"；
- 若是**不能识图的模型**（含默认的 0 元会员模型"有数 AI"/agnes-2.0-flash），系统在后台自动把图片转成文字描述喂给模型，用户无感（透明降级）。

整套能力复用 Agent mode 已经跑通的图片处理基础设施，新增代码集中在 chatbot 一侧，对其它功能零影响。

## §2 报价与周期 [客户可见]
- 预估工作量：1.5 天（后端 ~0.8d + 前端 ~0.5d + 验证 ~0.2d）
- 报价：内部功能，N/A
- 交付时间线：当前迭代内完成至 dev 验收

## §3 技术可行性 [AI 内部]

### 现有功能复用（模式无关，已就绪）
- **上传/状态端点**：`POST /v1/agent-attachments`（multipart，字段 `file`）+ `GET /v1/agent-attachments/:id/status`，与 run 解耦，靠返回的 `id` 关联。**零改动复用**。
- **`UploadService`**（`biz/attachment/upload.go`）：MIME 嗅探 / COS 转存 / modality 检测 / 写 `agent_attachment` 表 / 自动 `Enqueue` 后台 fallback。
- **`FallbackService`** worker pool（`biz/agent/attachment/fallback_service.go`）：进程级单例，已在 `numind.go` 启动；上传时异步用 VLM(qwen3-vl-flash)+百度 OCR 生成 `text_fallback`。
- **capability 判定**（`internal/pkg/aiservice/capability`）：`GetCapabilities(modelKey).AcceptsImageInline` 读 `ai_service.capability_json`，5min 缓存，未知模型保守降级。已在 `numind.go:139` Init。
- **`agent_attachment` 表 + `IAgentAttachmentStore`**：`GetByIDAndUser`（归属校验）、`GetByID`（轮询 fallback）。
- **多模态 message 类型**：`aiservice.MessagePart` / `ImageURL` / `MessageContent.Parts`，adapter 已支持渲染 `image_url`。
- **计费/可观测**：Gateway `Billing` 中间件自动按 message 是否含 `image_url` part 计 `llm_vision`；bill-only 模式保留 reserve/reconcile；fallback worker 自建 Langfuse trace。

### 技术风险与缓解
1. **fragment 路径覆盖 Messages**（最大风险，已查清）：chatbot 的 `ChatStream` 走 `ContextFragments` → context-budget 中间件渲染成**纯文本**并覆盖 `chatReq.Messages`，会丢掉 `image_url` Parts。
   - **缓解**：识图 turn 改走 **bill-only 模式**（`aismw.WithGatewayBillingOnly`，agent 已验证），自己拼完整 Messages、`ContextFragments=nil`，绕过 fragment 渲染，同时 `synthBillOnlyResult.ChargeUser=true` 保留 reserve/reconcile。**非识图 turn 仍走 fragment 路径**（纯文本，天然兼容）。
2. **ModelOverride 与 task profile modality**：vision turn 用 `ModelOverride=modelKey` 强制用户选的识图模型，task profile `chatbot.stream` 的 `input_modalities` 仅为元数据（modality 能力在 per-model 的 `capability_json`），不阻断。附带把 profile metadata 更新为 `["text","image"]` 做文档一致性（非功能必需）。
3. **跨包复用 agent 的路由 helper**：`buildAgentInputForModel`/`presignAttachmentURL`/`waitForFallback`/`mkInlineBlock` 当前在 package `agent` 未导出，且 agent 有活跃 feature（agent-output-refine / tool-soft-error-sweep）。
   - **缓解**：抽到**新共享包** `internal/numind/biz/multimodal`（仅依赖 capability/util/store/model/aiservice，无 agent 耦合），chatbot 用它；**不改 agent**（其副本留作待迁移技术债），避免与 agent 活跃 feature 的 merge 冲突。
4. **并发编辑**：`stream.go ChatStream` 同时被活跃 feature `adaptive-session-titles`(S3) 修改 → S4 前 `git fetch` 核对，merge 冲突手工解。

### 涉及仓库
- [x] numind-server（chatbot controller + biz + 新 multimodal 共享包 + 可选 profile seed）
- [x] numind-web-v3（chatbot 上传 UI + api + store）
- [ ] numind-admin-web

### AI 可观测性
- [x] 涉及 LLM 调用：是
- **Trace 起点**：复用 `ChatStream` 已有的 `langfuse.CreateTrace("chatbot-chat", ...)`。
- **Generation 点**：(1) 识图 turn 的主 LLM 流式调用（Gateway 自动挂 generation，含 `image_url` → 计 `llm_vision`）；(2) fallback worker 的 VLM 描述生成（已自建 `attachment.fallback` trace，上传时触发，非本 trace）。
- **关键元数据**：`chatbot_id`、`session_id`、`user_id`、`attachment_ids`、命中路径（inline / fallback）。

## §4 产品需求定义 — PRD [AI 内部]

### 用户故事
- 作为 chatbot 用户，我需要上传图片并让 AI 看懂图片内容，以便就图片提问（如"这张图表说明什么"）。
- 作为用 0 元默认模型的用户，我上传图片时无需关心模型能不能识图，系统应自动降级处理，以便始终能得到基于图片的回答。
- 作为用识图模型的用户，我上传的图片应被模型真正"看到"（而非仅文字描述），以便获得更准确的视觉理解。

### 验收标准
- [ ] **AS-1**：chatbot 输入框可上传图片（`.png/.jpg/.jpeg`），上传中有 loading、可删除、可多图（≤5，复用 agent 限制）。
- [ ] **AS-2**：选**识图模型**（如 qwen3-vl-flash）上传图片提问 → 后端走 inline 路径，message 含 `image_url` part，模型回答基于真实图像内容；计费记为 `llm_vision`。
- [ ] **AS-3**：选**非识图模型**（如默认 agnes）上传图片提问 → 后端走 text_fallback 路径，模型回答基于后台生成的图片文字描述；用户无报错、无感降级。
- [ ] **AS-4**：fallback 未就绪（刚上传立刻发送）→ 等待 ≤1500ms；超时注入"描述生成中"占位，不阻断对话、不报错。
- [ ] **AS-5**：reserve/reconcile 计费在识图 turn 正常工作（bill-only：预扣 + 按实际 token 对账，多退少补）。
- [ ] **AS-6**：上传的图片在用户自己的消息气泡里可见，刷新会话后仍可见（持久化）。
- [ ] **AS-7**：纯文本对话（无图片）行为与现状完全一致（fragment 路径 + 压缩 + grounding 不受影响）。
- [ ] **AS-8**：非图片附件（已有文档上传）行为不受本次改动影响。

### 边界情况
- 空 `attachment_ids` / 不属于该用户的 id → 静默跳过该 id（`GetByIDAndUser` 失败不 abort），不报错。
- 图片 + 知识库 grounding 同时存在：识图 turn 在 bill-only 的 system 消息里保留"参考资料"+grounding 文案。
- presign 失败 → 降级到 text_fallback 路径。
- 模型 capability 查询失败 → 保守按非识图（fallback）处理。

### 权限规则
- 与现有 chatbot 一致：`HasChatbotPermission` 白名单校验（撤权即时生效）。图片上传走 `/v1/agent-attachments`（authGroup，登录态即可，归属按 userID）。
- 识图模型若为 0 元会员模型，仍受 `EnforceModelMembership` 会员门控（非会员不可达）。

### UI 行为规格
- **页面位置**：`ChatbotChat.vue` 输入区（与现有文档上传同一 `+`/回形针入口）。
- **布局要求**：附件预览条（缩略图或文件名 chip，复用 agent `attachment-strip` 样式）。
- **交互模式**：点击上传 / 拖拽 / 粘贴图片；上传后暂存 `attachment_id`，发送时随 chat 请求带 `attachment_ids`。
- **状态处理**：上传中 spinner；上传失败 toast + 可重试；发送后清空附件暂存。
