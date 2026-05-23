# Task 1.3: buildAgentInput capability-aware 路由

## 概要

重构 `internal/numind/biz/agent/runner.go` 的 `buildAgentInput()` 函数，返回 `[]aiservice.ChatMessage` 而非 string，根据当前 model 的 capability（来自 task 1.1）决定 attachment 走多模态直送（路径 A）还是文本 fallback（路径 B，来自 task 1.2）。是板块 1 的"编排核心"，把 task 1.1（能力查询）+ task 1.2（fallback 数据）串成一条可用的 message 链。

## 依赖

- 前置依赖：
  - **Task 1.1**: `capability.GetCapabilities(modelKey) → Capabilities{AcceptsImageInline, AcceptsPDFInline, AcceptsAudioInline}` 必须可用
  - **Task 1.2**: `agent_attachment` 表新增字段 `modality / text_fallback / fallback_ready / fallback_error` 必须存在
- 被依赖：
  - **Task 1.4**: Tool gating 需复用本 task 的 `capability.GetCapabilities()` 调用结果
  - **Task 1.5**: Runtime 错误剥离重试需复用本 task 产出的 message 结构来"剥图"

## 输入 / 输出契约

### 新函数签名

```go
// internal/numind/biz/agent/runner.go
func buildAgentInputForModel(
    ctx context.Context,
    userMessage string,
    attachments []*model.AgentAttachment,
    modelKey string,
) ([]aiservice.ChatMessage, error)
```

### 旧函数移除

```go
// 删除 buildAgentInput(message string, attachmentURLs []string) string
// 所有 caller 改成 buildAgentInputForModel
```

### Caller 改动点

- `runner.go::AgentRunner.runOnce()` — 调用点改成 `buildAgentInputForModel(ctx, userMsg, atts, currentModelKey)`
- `agent_attachment` 改为传完整 `*model.AgentAttachment` 列表（不再传 URL 字符串数组）

### 返回的 ChatMessage 结构

```go
// 用户消息：根据每个 attachment 决定走 ImageBlock / TextBlock
type ChatMessage struct {
    Role    string         // "system" / "user" / "assistant" / "tool"
    Content []ContentBlock // 多模态走 block 数组
}
type ContentBlock struct {
    Type     string // "text" / "image_url" / "input_audio"
    Text     string
    ImageURL *ImageURLBlock // {URL, Detail}
    Audio    *AudioBlock
}
```

返回的 messages 列表恰好是一个 `user` role message（包含 text + 0..N 个多模态 block），不构造 system prompt（system prompt 拼装由上游 `buildSystemPrompt()` 负责）。

## 设计要点

### 1. 核心路由逻辑

```go
for _, att := range attachments {
    caps := capability.GetCapabilities(modelKey)
    inline := false
    switch att.Modality {
    case "image": inline = caps.AcceptsImageInline
    case "pdf":   inline = caps.AcceptsPDFInline
    case "audio": inline = caps.AcceptsAudioInline
    }

    if inline {
        // 路径 A：原生多模态 block
        url := presignAttachmentURL(ctx, att)  // COS 私有桶每次 sign
        blocks = append(blocks, mkInlineBlock(att.Modality, url))
    } else {
        // 路径 B：文本 fallback
        text, err := waitForFallback(ctx, att, 1500*time.Millisecond)
        if err != nil {
            text = fmt.Sprintf("[%s：%s，描述生成中，请稍后重试]",
                att.Modality, att.Filename)
        }
        blocks = append(blocks, ContentBlock{Type: "text", Text: text})
    }
}
```

### 2. Fallback 等待（waitForFallback）

```go
func waitForFallback(ctx context.Context, att *AgentAttachment, timeout time.Duration) (string, error) {
    if att.FallbackReady { return att.TextFallback, nil }

    ticker := time.NewTicker(100 * time.Millisecond)
    defer ticker.Stop()
    deadline := time.NewTimer(timeout)
    defer deadline.Stop()

    for {
        select {
        case <-ctx.Done(): return "", ctx.Err()
        case <-deadline.C: return "", ErrFallbackTimeout
        case <-ticker.C:
            // 重新读 DB，看 fallback_ready 是否变 true
            fresh, err := attachmentStore.Get(ctx, att.ID)
            if err != nil { return "", err }
            if fresh.FallbackReady { return fresh.TextFallback, nil }
            if fresh.FallbackError != "" {
                return fmt.Sprintf("[%s：%s，描述生成失败：%s]",
                    att.Modality, att.Filename, fresh.FallbackError), nil
            }
        }
    }
}
```

- 轮询 **100ms** × **15 次** = max **1500ms**
- 超时 → 注入占位文字（不阻塞 agent run，宁愿信息有损也不挂掉）
- ctx 取消 → 立刻返回错误（上游决定是否终止）

### 3. COS Presign 策略

- 私有桶下，inline base64 嵌入 ImageBlock 用 URL 时**每次都要重新 sign**（presign expires=15min 即可，单次 agent turn 用完）
- helper 函数：`presignAttachmentURL(ctx, att) (string, error)`
- 失败降级：若 presign 失败 → log 警告 + 自动转走路径 B（fallback）

### 4. System prompt 注入（在 caller 那一层做，但本 task 标注约束）

本 task 不构造 system prompt，但要把"是否注入附件说明"标志返回给 caller：

```go
// 包内新增 helper：
func HasFallbackAttachments(blocks []ContentBlock) bool {
    // 若 blocks 中有 text 类型且匹配 "[图片：..." 前缀 → true
}
```

Caller（`buildSystemPrompt()`）在第 5 段 "System reminders" 拼一段：

```
【附件说明】用户上传的图片/PDF 已转为文字描述。请基于描述内容回答用户问题。
```

**System prompt 6 段顺序不变**（context.md I3 约束）。

### 5. 边界 case

| Case | 处理 |
|---|---|
| `attachments` 为空 | 直接返回单纯文本 user message |
| `modelKey` 为空或未识别 | `capability.GetCapabilities` 返回保守默认（全 false）→ 全走路径 B |
| `att.Modality` 是未知值（如 "video"）| 走路径 B + log 警告 |
| Fallback 超时 + 也没有 error | 注入"描述生成中"占位 |
| Presign 失败 | log + 走路径 B |
| ctx 取消（用户中途断开） | 返回 ctx.Err()，上游处理 |

## 实施步骤

> 仓库：`numind-server`，分支：`feature/agent-multimodal-task-1-3`（task 1.1 / 1.2 完成后通过 `ndf-start` 启动）

### Step 1 — 重写函数本体

**文件**：`internal/numind/biz/agent/runner.go`

- 删除 `buildAgentInput()`
- 新增 `buildAgentInputForModel(ctx, userMessage, attachments, modelKey) ([]ChatMessage, error)`
- 内部调用 `capability.GetCapabilities()`、`waitForFallback()`、`presignAttachmentURL()`

### Step 2 — 新增 helper 文件

**文件**：`internal/numind/biz/agent/multimodal.go`（新建）

- `waitForFallback(ctx, att, timeout)` — 轮询逻辑
- `presignAttachmentURL(ctx, att)` — COS presign 调用
- `HasFallbackAttachments(blocks)` — 给 system prompt 判断是否注入附件说明
- `mkInlineBlock(modality, url)` — 构造多模态 block
- 常量：`fallbackPollInterval = 100ms`、`fallbackMaxWait = 1500ms`、`presignExpiry = 15 * time.Minute`

### Step 3 — 改 caller 调用点

**文件**：`internal/numind/biz/agent/runner.go::AgentRunner.runOnce()`

- 把传 `attachmentURLs []string` 改为传 `attachments []*model.AgentAttachment`
- 上游 controller 已经从 DB 查出 attachment 实体，直接传

### Step 4 — System prompt 注入

**文件**：`internal/numind/biz/agent/system_prompt.go`（如已存在则改，否则在 runner.go 内）

- 在第 5 段 "System reminders" 拼接前调用 `HasFallbackAttachments()` 判断
- 若 true → append 附件说明文字
- 不动 6 段固定顺序

### Step 5 — 测试

**文件**：`internal/numind/biz/agent/multimodal_test.go`（新建）

详见 S5。

## 验证策略（S5）

### 单元测试（table-driven，覆盖 6 model × 3 modality 矩阵）

```go
func TestBuildAgentInputForModel(t *testing.T) {
    cases := []struct{
        name      string
        modelKey  string
        modality  string
        ready     bool
        wantPath  string // "inline" or "fallback"
    }{
        // 多模态 model × 3 modality
        {"qwen-vl-flash + image", "qwen-vl-flash", "image", true, "inline"},
        {"qwen-vl-flash + pdf",   "qwen-vl-flash", "pdf",   true, "fallback"}, // VL flash 不收 PDF
        {"mimo-v2.5 + image",     "mimo-v2.5-pro", "image", true, "inline"},
        {"kimi-k2.6 + image",     "kimi-k2.6",     "image", true, "inline"},
        // 单模态 model × 3 modality（应全走 fallback）
        {"glm-5.1 + image",       "glm-5.1",       "image", true, "fallback"},
        {"glm-5.1 + pdf",         "glm-5.1",       "pdf",   true, "fallback"},
        {"minimax-m2.7 + image",  "minimax-m2.7",  "image", true, "fallback"},
        {"qwen-3.7-max + image",  "qwen-3.7-max",  "image", true, "fallback"},
        // Edge case
        {"unknown model",         "fake-model",    "image", true, "fallback"},
        {"fallback not ready",    "glm-5.1",       "image", false, "fallback-timeout"},
        {"empty attachments",     "glm-5.1",       "",      true, "no-attachment"},
    }
    // ...
}
```

### 集成测试（与 task 1.1 / 1.2 一起）

- 启动 dev MySQL + Redis
- 用 `agent_attachment` 表造 3 条假数据：image (ready=true) / pdf (ready=true) / audio (ready=false)
- 调用 `buildAgentInputForModel` 用 glm-5.1 model
- 断言：3 条均走路径 B，2 条用 text_fallback，1 条注入"描述生成中"占位

### Fallback 阻塞测试

- 造一条 attachment 初始 `fallback_ready=false`
- 起 goroutine 800ms 后写 `fallback_ready=true + text_fallback="测试描述"`
- 调用 `buildAgentInputForModel`
- 断言：返回内容包含"测试描述"，总耗时在 800-1000ms 之间

### 手动 dev 验证

1. dev 部署后，agent mode 开会话
2. 选 `qwen-vl-flash` 上传图 → 期望走路径 A，日志看到 "inline image"
3. 切到 `glm-5.1` 再发一句话 → 期望历史 image 转 fallback，日志看到 "fallback path"
4. 切回 `qwen-vl-flash` → 期望恢复 inline
5. Mock fallback_ready=false 卡 2s → 期望注入"描述生成中"占位

### gstack /qa 浏览器场景（与板块整体一起）

由板块级 S5 覆盖（README 中的"会话切换混合"场景）。

## 工期估算

- 总工期：**1 工作日**

| 子项 | 时间 |
|---|---|
| 函数重写 + helper 文件 | 0.3d |
| Caller 改动 + system prompt 注入 | 0.2d |
| 单元测试（覆盖矩阵） | 0.3d |
| 集成测试 + dev 手验 | 0.2d |

## 风险 / 待决策项

- ⚠️ **轮询 100ms × 15 次 = 1500ms 总等待是否合理**：若 VLM 副模型 P99 > 1.5s，大量请求会注入占位文字。需要看 task 1.2 上线后实测 P99 再调（可能要降到 100ms × 30 次 = 3s）。**待 task 1.2 上线后调参**。
- ⚠️ **Presign 每次 sign 的成本**：单次 agent turn 可能有 5+ attachment，每个都要 presign = 5 次 COS API 调用。考虑做 ctx-scoped 缓存（同一 turn 内复用）。
- ⚠️ **`Modality == "audio"` 当前还没在板块范围内**（README S0 明确排除），但函数签名要预留。Audio 上传走单独分支，若无可用 modality 处理，统一走路径 B + 占位。
- 🟡 **System prompt 注入位置由 caller 控制还是本函数控制**：当前设计是本函数返回 `HasFallbackAttachments()` 给 caller 决策。若 caller 漏调用，附件说明会丢。可能改成本函数直接返回 system message + user message 两条更稳。**倾向当前设计（保持单一职责）**，但若 caller 改动量大可考虑切换。
- 🟡 **buildAgentInputForModel 的 model.AgentAttachment 是 GORM model**：本函数依赖 store 层结构，违反 biz 层不依赖 store 实体的原则。若严格遵守，需要新建 `agent.AttachmentDTO`。**倾向先用 GORM model 简化（与项目现有 biz 模式一致）**。
