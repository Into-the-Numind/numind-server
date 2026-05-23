# Task 1.2: Attachment 双模态固化（异步生成 vision_description + ocr_text + text_fallback）

## 概要

用户上传图片/PDF/音频时，后端**异步**用 VLM + OCR + ASR 生成文字描述/识别文本，落库到 `agent_attachment` 表新增字段。后续若用户切换到单模态模型，buildAgentInput（task 1.3）直接读 `text_fallback`，无需现场调用 VLM。生成失败不阻塞用户，最终失败由调用方降级处理。

## 依赖

- 前置依赖：无（与 task 1.1 并行）
- 被依赖：task 1.3（buildAgentInput 读取 fallback 字段）、task 1.5（runtime 兜底也会读 fallback）

## 输入 / 输出契约

### DB Migration (`numind-server/migrations/20260523_120000_agent_attachment_fallback.sql`)

```sql
ALTER TABLE agent_attachment
  ADD COLUMN ocr_text TEXT NULL,
  ADD COLUMN vision_description TEXT NULL,
  ADD COLUMN text_fallback TEXT NULL,
  ADD COLUMN fallback_ready TINYINT(1) NOT NULL DEFAULT 0,
  ADD COLUMN fallback_error TEXT NULL,
  ADD COLUMN fallback_started_at DATETIME NULL,
  ADD COLUMN fallback_completed_at DATETIME NULL,
  ADD COLUMN modality VARCHAR(32) NOT NULL DEFAULT 'unknown',
  ADD COLUMN width INT NULL,
  ADD COLUMN height INT NULL,
  ADD COLUMN retry_count TINYINT NOT NULL DEFAULT 0,
  ADD INDEX idx_aa_ready (fallback_ready, user_id),
  ADD INDEX idx_aa_modality (modality);
```

### GORM Model (`internal/pkg/model/agent_attachment.go`)

```go
type AgentAttachment struct {
    ID                  uint64     `gorm:"primaryKey"`
    UserID              uint       `gorm:"not null;index"`
    URL                 string     `gorm:"type:text;not null"`
    Filename            string     `gorm:"size:255"`
    MimeType            string     `gorm:"size:128"`
    Size                int64
    Modality            string     `gorm:"size:32;default:'unknown'"` // image | pdf | audio | unknown
    Width               *int
    Height              *int
    OCRText             *string    `gorm:"type:text"`
    VisionDescription   *string    `gorm:"type:text"`
    TextFallback        *string    `gorm:"type:text"`
    FallbackReady       bool       `gorm:"default:false"`
    FallbackError       *string    `gorm:"type:text"`
    FallbackStartedAt   *time.Time
    FallbackCompletedAt *time.Time
    RetryCount          uint8      `gorm:"default:0"`
    CreatedAt           time.Time
}
func (AgentAttachment) TableName() string { return "agent_attachment" }
```

### Service API (`internal/numind/biz/agent/attachment/fallback_service.go`)

```go
type FallbackService interface {
    // Enqueue：上传 controller 调，立刻返回，不阻塞 HTTP
    Enqueue(ctx context.Context, attID uint64) error
    // WaitReady：调用方（task 1.3）若需要 fallback 但 ready=false，最多阻塞 timeout
    WaitReady(ctx context.Context, attID uint64, timeout time.Duration) (*model.AgentAttachment, error)
    // GenerateNow：测试/管理端手动触发（绕过队列）
    GenerateNow(ctx context.Context, att *model.AgentAttachment) error
}
```

### 上传 controller 改动（`internal/numind/controller/agent/upload.go`）

```go
// 保存 attachment 记录后追加：
att.Modality = detectModality(att.MimeType) // image|pdf|audio|unknown
store.Update(ctx, att)
_ = svc.Enqueue(ctx, att.ID) // fire-and-forget，永不阻塞 HTTP
return core.WriteResponse(c, nil, gin.H{
    "id": att.ID, "url": att.URL, "modality": att.Modality,
    "fallback_ready": false,
})
```

### GET 接口（task 1.3/前端轮询用）

`GET /v1/agent/attachments/:id/status` → `{id, fallback_ready, fallback_error, modality}`（user_token 中间件，仅返回自己的）

## 设计要点

### 1. 异步队列（进程内 worker pool）

不引入 Kafka/Redis Stream，用进程内 buffered channel + worker pool：

```go
type Pool struct {
    jobs        chan uint64                 // attachment ID
    perUserSem  map[uint]*semaphore.Weighted // 每用户 max 3 并发
    workers     int                         // 全局 worker 数：10
}
```

- 入队失败（channel 满）→ 降级为同步执行（reserve 上限 1000）
- 进程重启 → 启动时扫 `fallback_ready=false AND fallback_started_at IS NULL OR fallback_started_at < NOW()-INTERVAL 5 MIN`，重新入队（resume）

### 2. 模态分支

```go
switch att.Modality {
case "image":
    ocrText, _ := aiservice.OCR(ctx, "ocr.baidu", ocrReq)      // 软失败
    visDesc, err := callVLM(ctx, att)                          // 硬失败
    text := composeImageFallback(att, ocrText, visDesc)
case "pdf":
    text, err := callPDFExtract(ctx, att) // qwen-long file API
case "audio":
    text, err := aiservice.ASR(ctx, "monitor.transcribe", asrReq)
}
```

### 3. text_fallback 模板

**Image:**

```
[图片：{filename}（{width}x{height}，{filesize_kb}KB）
当前模型不支持直接看图，以下是该图的文字描述：

画面描述：
{vision_description}

OCR 提取的文字：
{ocr_text}
]
```

OCR 为空时整段省略；vision_description 为空但 OCR 有内容时仅保留 OCR 段。

**PDF:**

```
[PDF：{filename}（{page_count} 页，{filesize_kb}KB）
全文文本提取：
{extracted_text}
]
```

**Audio:**

```
[音频：{filename}（{duration_sec}s）
语音转文字：
{transcript}
]
```

### 4. VLM Prompt（针对销售场景）

```
你是销售助理的视觉分析员。请对这张图片做结构化中文描述。

按以下场景之一识别后描述（200-400 字）：
- 客户聊天截图：完整还原对话顺序，标注发言者和关键诉求
- 产品/UI 截图：列出界面元素、品牌标识、关键功能点
- 数据图表：图表类型、坐标轴、关键数值、趋势
- 自然实物图：物体、品牌、型号、可见参数
- 设计稿/合同/单据：版式、关键字段、金额/日期/落款

输出仅描述事实，禁止编造、禁止评价、禁止建议。
```

VLM profile：**新建 `attachment.vision_describe` task profile**（不复用 `salesrag.chatstyle`，理由：1) 语义边界清晰，便于将来切模型；2) 复用 chatstyle 会污染销售对话的 Langfuse trace；3) 加 task profile 需更新 `constants.go::allTaskIDsList` — 项目允许，符合"动态加 task profile 在 DB Registry 里做"原则）。路由到 qwen3-vl-flash（D2 推荐）。

### 5. Retry & Backoff

- 最多重试 **3 次**，间隔 1s → 4s → 16s（exponential）
- 超时单次调用 60s（VLM）/ 90s（PDF/ASR）
- 最终失败：`fallback_ready=true`（标记完成，避免 WaitReady 死等）+ `fallback_error` 填错误 + `text_fallback = "[图片：{filename}，描述生成失败：{error}]"`（兜底文案，task 1.3 直接用）

### 6. 并发控制

- 全局 worker：10
- 每用户：semaphore.Weighted, max 3 并发
- 入队前检查用户 sem.TryAcquire；获取不到 → 排队等

### 7. WaitReady 行为（task 1.3 调用）

```go
// 轮询 + 短超时（D4 决策：阻塞 1-2s 等待）
deadline := time.Now().Add(timeout) // 默认 2s
for time.Now().Before(deadline) {
    att, _ := store.GetByID(ctx, attID)
    if att.FallbackReady { return att, nil }
    time.Sleep(100 * time.Millisecond)
}
return att, ErrFallbackTimeout
```

### 8. 边界 case

- 上传后立即用单模态模型 → WaitReady 2s 超时 → buildAgentInput 注入 `[图片：{filename}，描述正在生成中，请稍后重试或切换到多模态模型]`
- 同一 attachment 被多次引用（用户重发） → fallback_ready 已 true 直接读，不重跑
- 图片超过 20MB → 跳过 VLM（成本太高），只跑 OCR + 拼装"图过大，仅文字识别"模板
- mime detect 失败（modality='unknown'） → 不入队，fallback_ready 保持 false，task 1.3 用文件名兜底

## 实施步骤

1. **`numind-server`**: 写 migration `20260523_120000_agent_attachment_fallback.sql`，开发机 SSH 跑一次
2. **`numind-server`**: 改 `internal/pkg/model/agent_attachment.go` 加字段
3. **`numind-server`**: 新增 `internal/numind/store/agent_attachment_store.go` 加 `UpdateFallback / ListPendingFallback / GetByID` 方法
4. **`numind-server`**: 改 `internal/pkg/aiservice/constants.go::allTaskIDsList` 加 `attachment.vision_describe`
5. **`numind-server`**: DB Registry 加 ai_service 行 `attachment.vision_describe` → 路由到现有 qwen3-vl-flash provider（开发机 SSH 跑 INSERT）
6. **`numind-server`**: 新增 `internal/numind/biz/agent/attachment/fallback_service.go`（接口 + 实现 + worker pool + 模态分支）
7. **`numind-server`**: 新增 `internal/numind/biz/agent/attachment/templates.go`（text_fallback 拼装）
8. **`numind-server`**: 新增 `internal/numind/biz/agent/attachment/prompts.go`（VLM prompt 常量）
9. **`numind-server`**: 改 `internal/numind/controller/agent/upload.go` 加 modality detection + Enqueue 调用
10. **`numind-server`**: 改 `internal/numind/router.go` 注册 `GET /v1/agent/attachments/:id/status`
11. **`numind-server`**: 改 `cmd/numind/main.go` 或 `internal/numind/biz/init.go`：进程启动时 `pool.Start()` + `pool.RecoverPending(ctx)`
12. **`numind-server`**: 测试 — unit tests + integration test (mock aiservice)
13. **`numind-server`**: 跑 `task lint` + `go test ./internal/numind/biz/agent/attachment/...`

## 验证策略（S5）

**验证方式选择**：Go unit test + 后端 integration test（**主**）+ gstack /qa 端到端（**辅**）。理由：fallback service 纯后端逻辑、无 UI，单测覆盖 90% case；端到端只需验证上传后字段写入正确。本任务无支付/权限风险，不强制 Playwright E2E。

**回归保护诚实声明**：gstack /qa 一次性验证，无持久回归保护。但 Go unit test + integration test 留库覆盖核心路径，已构成回归保护。

### 单元测试（`fallback_service_test.go`）

- `TestEnqueue_QueueFull_DegradesToSync` — channel 满时降级同步
- `TestGenerate_Image_OCRFailVLMOK` — OCR 软失败但 VLM 成功，fallback 仍 ready
- `TestGenerate_Image_VLMFail_RetriesThenFinalError` — 3 次失败后 ready=true + error 填入
- `TestGenerate_PerUserConcurrencyLimit` — 同一用户同时入队 5 个，只 3 个并发跑
- `TestWaitReady_TimeoutReturnsLatestState` — 2s 内未 ready 返回最新 att
- `TestComposeImageFallback_AllVariants` — OCR 有/无、VLM 有/无、size 极大的所有组合
- `TestRecoverPending_OnStartup` — mock 一行 `fallback_ready=false AND started_at<5min ago` 重启后重新入队
- `TestModalityDetection` — image/png, application/pdf, audio/mpeg, application/octet-stream 全覆盖

### 集成测试

mock `aiservice.OCR/Chat/ASR` 返回固定 string → 端到端跑 Enqueue → 等 2s → 校验 DB `text_fallback` 内容符合模板

### 手动 dev 验证步骤

1. dev 部署后用 curl POST `/v1/agent/upload` 一张测试图
2. `GET /v1/agent/attachments/:id/status` 轮询 → 看到 `fallback_ready=true`
3. SSH 到 dev MySQL: `SELECT vision_description, ocr_text, text_fallback FROM agent_attachment WHERE id=?` 校验内容合理
4. 用 PDF / 音频重复
5. 查 Langfuse trace：`attachment.vision_describe` generation 应有 prompt/output/token usage

### gstack /qa 场景

仅做 sanity check — 登录 → 上传图 → 等 3s → API 看 `fallback_ready=true`。不做 UI 验证（UI 在 task 1.3/前端 task）。

## 工期估算

- **总工期：1.5-2 工作日**
- DB migration + GORM model：0.5h
- worker pool + fallback service 核心：6h
- VLM/OCR/PDF 三模态分支 + 模板：3h
- controller + router 集成：1h
- 单元测试 + integration：4h
- dev 部署 + 手动验证 + 调优：2h

## 风险 / 待决策项

- ⚠️ **VLM 副模型成本**：qwen3-vl-flash ¥0.0008/k token，按平均 1 张图 800 token 算约 ¥0.0006/张。需在 admin 端加监控；建议 V1.5 先不上限额，看 prod 数据再加每用户配额
- ⚠️ **PDF 大文件**：qwen-long 限 10M token，超大 PDF 需先 pymupdf4llm 本地提取再喂给 qwen-long 摘要——本 task **MVP 只做 ≤10MB PDF 直传**，超过则 fallback_error="PDF too large"
- ⚠️ **新增 task profile**：`attachment.vision_describe` 增加 task profile 数到 22。需用户确认是否接受（context.md §3 写"21 个 task profile 是稳定的，不要轻易加"，但同条也说"如果要加必须更新 constants.go::allTaskIDsList"——按规则增加是允许的）
- ⚠️ **进程内队列重启丢失**：worker pool 是进程内的，`RecoverPending` 扫表恢复——但若进程崩溃后未及时拉起，pending job 等到下次启动才跑。可接受（fallback 是优化路径，不是关键路径）
- ⚠️ **图片宽高读取**：上传 controller 当前可能没解析 width/height。新增依赖 `image.DecodeConfig`（标准库），无外部包
- ⚠️ **OCR 软失败定义**：百度 OCR 偶发超时/返回空——把"空字符串" 也算成功（图就是没文字），仅 HTTP 错误算失败
