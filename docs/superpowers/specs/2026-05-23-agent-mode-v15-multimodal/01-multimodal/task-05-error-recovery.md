# Task 1.5: Runtime 错误剥离重试（最后兜底）

## 概要

在 `aiservice.Chat` 外层包一层 wrapper，识别 provider 返回的 "multimodal not supported" 类错误（多种格式），自动剥离 image content + 重试 1 次。是板块 1 的第 4 道防线，理论上前 3 层（capability matrix / 双模态固化 / tool gating）正常时永不触发。每次触发都告警，指示 capability 数据漏 seed 或新 provider 未覆盖。

## 依赖

- 前置依赖：**task 1.3**（buildAgentInput capability-aware 路由 — 需复用其 strip 工具）
- 被依赖：无（板块内最后一个 task）

## 输入 / 输出契约

### 新文件

```go
// numind-server/internal/numind/biz/aiservice/errors/multimodal.go
package errors

import "regexp"

// 已知 provider "不支持图片" 错误的正则集合
var multimodalNotSupportedPatterns = []*regexp.Regexp{
    regexp.MustCompile(`(?i)Invalid value:\s*'image_url'`),
    regexp.MustCompile(`(?i)model does not support image`),
    regexp.MustCompile(`(?i)unsupported.*modality.*image`),
    regexp.MustCompile(`(?i)multimodal.*not.*support`),
    regexp.MustCompile(`(?i)does not support.*vision`),
    regexp.MustCompile(`(?i)image.*input.*not.*support`),
    regexp.MustCompile(`(?i)image_url.*not.*allowed`),
    regexp.MustCompile(`(?i)vision.*not.*enabled`),
}

// IsMultimodalNotSupportedError 判断错误是否属于"模型不支持图片"
// 检查：1) error.Error() 文本 2) HTTP 4xx + body 含特定关键词
func IsMultimodalNotSupportedError(err error) bool

// MultimodalStripRetryMetric 暴露给上层调用方监控
type MultimodalStripRetryMetric struct {
    ModelKey      string
    ProviderID    int64
    StrippedCount int    // 剥掉几张图
    OrigPromptKB  int
    RetrySucceeded bool
}
```

### 调用方改动（runner.go）

```go
// numind-server/internal/numind/biz/agent/runner.go
// 在 callAIService wrapper 内（task 1.3 已经定义此包装函数）
resp, err := aiservice.Chat(ctx, profile, req)
if err != nil && errpkg.IsMultimodalNotSupportedError(err) {
    metric := &errpkg.MultimodalStripRetryMetric{...}
    stripped, n := stripImagesFromMessages(req.Messages) // task 1.3 共用 helper
    metric.StrippedCount = n
    req.Messages = stripped
    log.Warnw("multimodal not supported, stripping and retrying", ...)
    resp, err = aiservice.Chat(ctx, profile, req)
    metric.RetrySucceeded = (err == nil)
    metrics.IncCounter("agent.runtime.strip_image_retry", ...)
    langfuse.AddEventToTrace(ctx, "multimodal_strip_retry", metric)
}
```

### Terminal Reason

- 重试仍失败 → 已有 `terminal_reason=model_error`（沿用现有 19 个，不新增）
- 错误日志含 `"strip_retry_exhausted"` 关键词便于查询

## 设计要点

### 1. 错误识别多层

- **优先 1**：err.Error() 文本正则匹配 8 个 pattern（覆盖 OpenAI / Ali / Volc / DMXAPI / 通用）
- **优先 2**：若 err 实现 `interface{ StatusCode() int }` 且 status ∈ {400,422} + body 含关键词
- **优先 3**：errors.Is 链查找 provider-specific sentinel（未来扩展）

### 2. 剥离策略

- 复用 task 1.3 的 `stripImagesFromMessages(msgs) (msgs, n)` helper（不重复实现）
- 替换占位文本：`[图片已自动剥离：当前模型不支持图片输入。请切换到支持视觉的模型重新分析。]`
- 保留原始消息顺序 + 其他字段（role / tool_call_id 等）

### 3. 重试上限

- **最多 1 次** — 二次仍失败立即返回 model_error，不再剥（避免无限循环）
- 重试前必须确认 messages 实际被剥（n>0），否则说明误报错误格式，直接返回原 err

### 4. 告警与监控

每次走到这条路径必须：
- `log.Warnw` 含 model_key / provider_id / stripped_count / orig_prompt_kb / retry_succeeded
- `metrics.IncCounter("agent.runtime.strip_image_retry", labels{model, provider, succeeded})`
- langfuse trace event `multimodal_strip_retry`（含完整 metric）
- 监控告警阈值：单模型 24h 触发 >5 次 → 自动 issue capability 数据修正提醒

### 5. 边界 case

- err 为 nil 或非 multimodal 类 → 透传，不进重试
- messages 中无 image block（n=0）→ 跳过重试直接返回原 err（说明误判）
- 第二次也是 multimodal 错误 → 返回原始 err（不递归剥）
- ctx canceled → 立即返回，不重试

### 6. 与 task 1.3 的关系

task 1.3 在 `buildAgentInput` 已做主动路由（capability=false 不送 image）。本 task 1.5 仅作 **defense-in-depth**：当 capability matrix 数据漏 seed / 新接 provider / model 突然降级时兜底。每次触发都说明前 3 层数据有 gap，是质量信号。

## 实施步骤

### Step 1: numind-server — 新建 errors package

`numind-server/internal/numind/biz/aiservice/errors/multimodal.go`
- 定义 8 个正则 patterns
- 实现 `IsMultimodalNotSupportedError(err error) bool`
- 实现 HTTP status code 检查辅助（如 err 是 `*aiservice.ProviderError` 类型）

### Step 2: numind-server — 单元测试

`numind-server/internal/numind/biz/aiservice/errors/multimodal_test.go`
- 8 个 provider 错误格式 table-driven test（每个 pattern 一个 case + 一个 negative case）
- HTTP 4xx + body 检查 case
- nil err / 非相关 err 透传 case

### Step 3: numind-server — runner.go 集成

`numind-server/internal/numind/biz/agent/runner.go`
- 在 task 1.3 已经包装的 `callAIService(ctx, profile, req)` 内加错误检查 + 重试逻辑
- 引入 metric 上报 + langfuse event
- 注意：若 task 1.3 没建 wrapper，则本 task 一并建（与 task 1.3 owner 协商，估计本 task 兜底建）

### Step 4: numind-server — metrics 注册

`numind-server/internal/pkg/metrics/agent.go`
- 新增 counter `agent.runtime.strip_image_retry` 含 labels: model_key / provider_id / retry_succeeded
- 若 metrics package 不存在则用现有 zap log + 后续 Phase 2 接 prometheus

### Step 5: numind-server — 集成测试

`numind-server/internal/numind/biz/agent/runner_strip_retry_test.go`
- mock aiservice.Chat：第一次返回 "Invalid value: 'image_url'" → 看是否剥图重试 → 第二次成功
- mock 第二次仍 multimodal 错误 → 看 terminal_reason=model_error
- mock 第一次返回普通 500 错误 → 不走剥图路径
- mock messages 无图 + multimodal err → 跳过重试直接返回

### Step 6: numind-server — 文档

`numind-server/docs/agent-mode/multimodal-fallback.md` 追加章节 "Layer 4: Runtime Strip Retry"，记录正则 patterns + 触发条件 + 监控位置。

## 验证策略（S5）

### 单元测试（必须）

| Case | 输入 err | 期望 |
|---|---|---|
| OpenAI 格式 | `"Invalid value: 'image_url'..."` | IsMultimodal=true |
| Ali DashScope | `"model does not support image input"` | IsMultimodal=true |
| Volc Ark | `"unsupported modality: image"` | IsMultimodal=true |
| DMXAPI | `"multimodal feature not supported"` | IsMultimodal=true |
| 通用 1 | `"does not support vision"` | IsMultimodal=true |
| 通用 2 | `"image input is not supported"` | IsMultimodal=true |
| HTTP 422 + body | `ProviderError{Status:422, Body:"image_url not allowed"}` | IsMultimodal=true |
| 普通 500 | `"internal server error"` | IsMultimodal=false |
| Timeout | `context.DeadlineExceeded` | IsMultimodal=false |
| Rate limit | `"rate limit exceeded"` | IsMultimodal=false |

### 集成测试（runner 层）

`runner_strip_retry_test.go` 4 个核心场景：
1. **happy retry**：第一次 multimodal err + 剥图后第二次成功 → 返回正常 resp，stripped_count=2
2. **retry exhausted**：两次都失败 → terminal_reason=model_error
3. **non-multimodal err**：不触发剥图 → 透传原 err
4. **no images**：messages 无图但收到 multimodal err → 跳过重试，原 err 返回

### 手动 dev 验证

1. 临时把 capability matrix 中 qwen-turbo 标 `accepts_image=true`（模拟漏 seed）
2. 在 agent mode 上传图片 → 切到 qwen-turbo 发问
3. 后端 log 应见 `"multimodal not supported, stripping and retrying"`
4. 前端应收到正常文字回复（基于剥后内容）
5. langfuse 应见 `multimodal_strip_retry` event
6. 还原 capability 数据

### 监控验证（dev 部署后）

- grep dev log `strip_image_retry` 应为 0（前 3 层正常时）
- 若 >0 → 立即排查哪个 model 漏 seed → 修 ai_service.capability_json

### gstack /qa 不需要

本 task 是后端 defensive 逻辑，前端无 UI 改动；单元 + 集成测试覆盖足够。

## 工期估算

| 分项 | 工时 |
|---|---|
| errors package + 单元测试 | 2-3h |
| runner.go 集成 + metric 上报 | 1-2h |
| 集成测试 4 case | 2h |
| 文档 + dev 手动验证 | 1h |
| **总计** | **0.5-1 天** |

## 风险 / 待决策项

### R1: 与 task 1.3 wrapper 边界

task 1.3 也会建 `callAIService` wrapper（capability 路由）。两者共用还是叠加？**默认方案**：task 1.3 建 wrapper，本 task 在 wrapper 内最后一层加错误检查；若 task 1.3 owner 未建则本 task 一并建（无需用户拍板）。

### R2: 正则覆盖是否够

8 个 pattern 来自已知 5 个 provider + 通用关键词。**新接入 provider 时**：每次接入流程必须加该 provider 的 multimodal 错误格式到正则列表 + 单元测试。这是接入 checklist 项。

### R3: 重试导致 token 双计

重试时 token usage 是否双扣？**默认**：第一次失败不计费（reserve 阶段已扣，reconcile 时按实际成功的那次结算）。但要确认 aiservice 现有 reserve/reconcile 是否处理重试失败的 release。**建议**：spec 实施时由 owner 跑一个端到端测试确认计费正确，必要时与计费 owner 协商；不阻塞本 task 上线（重试场景预期 <0.1% 调用，对账偏差可忽略）。

### R4: 是否需要 P2-fallback

剥图重试失败后是否还能再剥别的（如 PDF）+ 再试？**不做** — 复杂度爆炸；当前设计是兜底，前 3 层覆盖率应 ≥99.9%。
