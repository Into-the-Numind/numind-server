# Task 1.4: Vision 工具实现（放大镜 — analyze_image + annotate_image）

## 概要

在 agent 的工具集里实现两个"放大镜"工具 — `analyze_image` 和 `annotate_image`。当 agent 需要对图片做精细分析时主动调用，内部调 vision 模型（`qwen3-vl-plus` 或 `qwen-vl-plus`），返回结构化文字。**不论主模型是单模态还是多模态都注册这两个工具**（多模态主模型也可能用 — 当它觉得自己看不清细节时）。任何场景（数据分析、SOP 执行、PPT 制作、监控值班、销售助理等）下，只要 agent 想"放大看图"都能调。

> 这是 D2 决策的实现：放大镜是 V1.5 必交付能力，不是 V2 占位。Task 1.2 的 attachment 双模态固化（异步预生成 vision_description + ocr_text）作为缓存基座，本 task 在 agent 运行时按需主动调用 vision 模型做"看不清就放大"。

## 依赖

- **前置依赖**：
  - task 1.1（capability matrix — 提供 `capability.GetCapabilities(modelKey)` 用于工具 metadata 注册）
  - task 1.2（attachment 双模态固化 — 提供 `attachment.vision_description` / `ocr_text` 字段作为缓存源）
  - 板块外：`profile.attachment.vision_describe` profile（D1 已加入，在 context.md §12 列表 V1.5 新增 4 个 profile 中）
- **被依赖**：task 1.3 buildAgentInput 在调用 `react.NewAgent` 时把这两个 tool 注册进 ToolsConfig
- **可并行**：在 task 1.1 + 1.2 完成后，与 task 1.3 / 1.5 并行（Tier 2 disjoint）

## 输入 / 输出契约

### 1. `analyze_image` 工具签名

```go
// internal/numind/biz/agent/tools/analyze_image.go
package tools

type AnalyzeImageInput struct {
    AttachmentURL string `json:"attachment_url" jsonschema:"description=The COS URL of the image attachment to analyze (must be from agent_attachment.url)"`
    Question      string `json:"question" jsonschema:"description=What to focus on in the image (e.g. 'extract all numerical data', 'describe the layout', 'identify text', 'count the items')"`
}

type AnalyzeImageOutput struct {
    Description string  `json:"description"`   // 自然语言描述（针对 Question 的回答）
    OCRText     string  `json:"ocr_text"`      // 从图中提取的所有可见文字（结构化优先）
    Confidence  float32 `json:"confidence"`    // [0, 1]，模型对结果的置信度（若 provider 不返回则恒为 0.85）
    ModelUsed   string  `json:"model_used"`    // 实际命中的 model_key，便于 trace
    FromCache   bool    `json:"from_cache"`    // true = 命中 attachment 表预生成缓存，没重新调 LLM
}
```

工具 description（暴露给 LLM 看的）：

> Analyze an image attachment in detail using a vision specialist model. Use this tool when you need to extract text, identify visual elements, count items, or describe layouts in an image — even if you can see the image directly, this tool gives more precise structured results. Returns a description, OCR text, and confidence. Costs ~1 vision call per invocation; do not call more than necessary.

### 2. `annotate_image` 工具签名

```go
// internal/numind/biz/agent/tools/annotate_image.go
package tools

type AnnotateImageRegion struct {
    X      int    `json:"x" jsonschema:"description=Top-left X pixel coordinate"`
    Y      int    `json:"y" jsonschema:"description=Top-left Y pixel coordinate"`
    Width  int    `json:"width" jsonschema:"description=Region width in pixels"`
    Height int    `json:"height" jsonschema:"description=Region height in pixels"`
    Label  string `json:"label" jsonschema:"description=Short label for this region (e.g. 'top chart', 'button A', 'error message')"`
}

type AnnotateImageInput struct {
    AttachmentURL string                `json:"attachment_url" jsonschema:"description=The COS URL of the image attachment"`
    Regions       []AnnotateImageRegion `json:"regions" jsonschema:"description=Regions of interest to focus the analysis on (max 10 regions per call)"`
}

type AnnotateImageAnnotation struct {
    Region      AnnotateImageRegion `json:"region"`       // 回显输入
    Description string              `json:"description"`  // 该区域内容的描述（基于 vision model 看裁剪后的子图）
    OCRText     string              `json:"ocr_text"`     // 该区域提取的文字
}

type AnnotateImageOutput struct {
    Annotations []AnnotateImageAnnotation `json:"annotations"`
    ModelUsed   string                    `json:"model_used"`
}
```

工具 description：

> Analyze specific regions within an image. Provide a list of bounding boxes (x, y, width, height) and labels; returns a per-region description and OCR text using a vision specialist model. Use when you need fine-grained spatial analysis (e.g. comparing two areas of a dashboard, reading a specific table cell). Max 10 regions per call.

### 3. profile 接入

两个工具内部调用 aiservice 时使用 D1 决策中新加的 `attachment.vision_describe` profile：

```go
// 工具实现内部
resp, err := aiservice.Chat(ctx, "attachment.vision_describe", &aiservice.ChatRequest{
    Messages: []aiservice.Message{
        {Role: "user", Content: visionMessageWithImageURL(url, question)},
    },
    Temperature: 0.2,  // 描述类任务低温度
    MaxTokens:   1024,
})
```

profile 路由配置（DB Registry 已由 D1 决策落实）：主路由 → `qwen3-vl-plus`；备路由 → `qwen-vl-plus`。

### 4. attachment 表读写契约（缓存复用）

```go
// 工具内部先查缓存
type cachedDescription struct {
    VisionDescription string  // attachment.vision_description
    OCRText           string  // attachment.ocr_text
    GeneratedAt       time.Time
    FallbackReady     bool    // attachment.fallback_ready
}

// 命中条件：
// - fallback_ready=true
// - 且 Question 与默认 prompt 关键词重合度足够（见"设计要点 4"）
```

## 设计要点

### 1. profile 与模型选择

- 使用 D1 新加的 `profile.attachment.vision_describe`（DB Registry 配置：主 `qwen3-vl-plus`，备 `qwen-vl-plus`）
- 不写死 model_key，全部通过 aiservice 入口 + profile 路由
- 主备路由由 aiservice 自身的 failover 机制处理，本工具层不重复实现

### 2. 超时

- 单次 vision 调用 timeout 30s（vision 模型 token 生成比纯文本 chat 慢 2-3 倍，常见单次响应 5-15s，30s 给重型图够用）
- 用 `context.WithTimeout(ctx, 30*time.Second)`，超时返回 graceful error（见"错误处理"）

### 3. 复用 task 1.2 的缓存

当 task 1.2 已经为该 attachment 异步预生成了 `vision_description` 和 `ocr_text`：

- `analyze_image` 优先返回缓存（`FromCache=true`），不重跑 LLM
- 命中条件：
  - `attachment.fallback_ready=true`
  - `question` 跟 task 1.2 默认描述 prompt 语义匹配（见下一点）

### 4. 缓存 invalidation（强制重跑判定）

判定 `question` 是否能用缓存的逻辑（写在工具内部辅助函数 `shouldBypassCache`）：

```go
// 关键词匹配：question 含以下其中之一就强制重跑 vision 模型
var bypassKeywords = []string{
    "count", "数一下", "几个", "多少个",
    "compare", "对比", "比较",
    "extract", "提取", "抽取",
    "specific", "具体",
    "region", "区域", "位置",
    "color", "颜色",
    "size", "大小", "尺寸",
}
```

- 命中任一关键词 → bypass 缓存，重新调 vision（`FromCache=false`）
- 默认通用 question（如 `"describe this image"`）→ 用缓存
- `annotate_image` **始终不走缓存**（spatial 分析需要新鲜调用）

### 5. 错误处理（graceful — 永远不返 error）

vision 模型挂了 / 超时 / attachment URL 404 / 非图片 MIME → 返回**结构化的失败描述**，工具本身不返 error：

```go
return AnalyzeImageOutput{
    Description: fmt.Sprintf("Vision analysis failed: %s", reason),
    OCRText:     "",
    Confidence:  0,
    ModelUsed:   "(failed)",
    FromCache:   false,
}, nil  // 注意：nil error
```

原因：tool error 在 Eino v0.8.13 是 fatal NodeRunError（context.md §4），会让整个 agent run 挂掉。Graceful degrade 让 agent 自己决定怎么应对（道歉、改用其他方法、问用户）。

### 6. 每 run 配额（防 cost 失控）

在 `agent_run` 级别维护两个 counter（写到 `agent_run` 的某个 JSON 字段或新增 `vision_tool_usage` 字段，待 task 1.3 定型）：

- `analyze_image` 每 agent_run 最多 **10 次**
- `annotate_image` 每 agent_run 最多 **5 次**

超额时工具返回：

```go
return AnalyzeImageOutput{
    Description: "Vision tool quota exceeded for this conversation (max 10 analyze_image / 5 annotate_image per run). Use cached descriptions from attachment metadata instead.",
    ...
}, nil
```

配额参数化为常量，便于后期 dev 数据驱动调整。

### 7. Tool metadata 注册

仍走 task 1.1 / 旧 task 1.4 设计的 `toolregistry` capability 中心化 metadata 表，但**两个工具都标 `RequiresVision: false`**（关键变化 — 因为内部走 vision 专家模型，对主模型无 vision 要求）：

```go
// metadata_table.go
"analyze_image":  {Name: "analyze_image",  RequiresVision: false, OutputModality: "text"},
"annotate_image": {Name: "annotate_image", RequiresVision: false, OutputModality: "text"},
```

含义：单模态主模型也注册这两个工具（这是 D2 决策的核心）。tool gating 层（旧 task-04 的 filter）保留，但这两个新工具不会被过滤掉 — 任何主模型都能调。

> 题外：未来若有 `screenshot_compare` 类**需要主模型本身看图**的工具（即主模型必须能 inline 接收图片块作为工具输出），那种工具会标 `RequiresVision: true` 并被 filter 过滤。本 task 的两个放大镜工具不是那种 — 它们的输出都是纯文字。

### 8. Langfuse trace

工具内部调 `aiservice.Chat` 已经自动走 Langfuse generation（aiservice 入口规则保证）。本工具层额外创建一个 span 包裹 vision 调用，便于在 trace 里看到"agent 调了 analyze_image 这个工具，内部又调了 vision 模型"两层结构：

```go
if tc := langfuse.FromContext(ctx); tc != nil {
    spanID := langfuse.SpanID()
    langfuse.CreateSpan(tc.TraceID, spanID,
        langfuse.WithSpanParent(tc.ParentObservationID),
        langfuse.WithSpanName("tool.analyze_image"),
        langfuse.WithSpanInput(map[string]any{"url": input.AttachmentURL, "question": input.Question}),
    )
    defer langfuse.EndSpan(spanID)
}
```

## 实施步骤

> 全部在 `numind-server` 仓库，无前端/admin 改动。

1. **`numind-server`** 新建 `internal/numind/biz/agent/tools/analyze_image.go`（150-200 行）
   - struct 定义 + 工具 description + 内部 `runAnalyze(ctx, input) (output, error)`
   - 缓存查询逻辑（query `agent_attachment` by URL → 拿 vision_description / ocr_text）
   - `shouldBypassCache(question)` 关键词判定
   - 调 `aiservice.Chat("attachment.vision_describe", ...)`
   - graceful error 路径

2. **`numind-server`** 新建 `internal/numind/biz/agent/tools/annotate_image.go`（100-150 行）
   - struct 定义 + 描述
   - 不走缓存，每次都裁剪 region 后调 vision（裁剪可通过让 LLM 在 vision prompt 里说明"focus on region (x,y,w,h)"，无需服务端真正裁图，省去图像处理依赖）
   - 多 region 时**串行**调（防爆 provider QPS），每个 region 一次 vision call

3. **`numind-server`** 新建 `internal/numind/biz/agent/tools/quota.go`（公共配额逻辑，约 50 行）
   - `CheckAndIncVisionQuota(ctx, agentRunID, toolName) error`
   - 内部读写 `agent_run.vision_tool_usage` JSON 字段（task 1.3 定 schema 时同步加）

4. **`numind-server`** `internal/numind/biz/agent/toolregistry/metadata_table.go` 改 entry：
   - `analyze_image.RequiresVision` → `false`（关键变更，与原 task-04 spec 不同）
   - `annotate_image.RequiresVision` → `false`
   - 添加 `screenshot_compare.RequiresVision = true` 作为 V2 占位例子

5. **`numind-server`** `internal/numind/biz/agent/runner.go::buildEinoTools`（或等价位置，task 1.3 收口处）
   - 把 `analyze_image` + `annotate_image` 加入 base tool list
   - 确认 capability filter 不会过滤掉它们

6. **`numind-server`** 新建 `internal/numind/biz/agent/tools/analyze_image_test.go`（单元测试）
   - mock `aiservice.Chat` 返 fixed response
   - mock `agent_attachment` 表查询（cache hit / miss 两条路径）
   - mock 配额超额场景
   - 至少 6 个测试 case（见 S5 章节）

7. **`numind-server`** 新建 `internal/numind/biz/agent/tools/annotate_image_test.go`
   - mock vision 模型多次调用
   - 多 region 串行验证
   - 至少 4 个测试 case

8. **`numind-server`** 运行 `task lint` 确保通过

9. **`numind-server`** 集成测试（与 task 1.3 联调时）
   - 起一个 agent run（不论用什么主模型），在对话中上传图，prompt LLM 用 `analyze_image`
   - 验证：日志显示工具被调用、返回结构化文字、Langfuse trace 显示两层 span

## 验证策略（S5）

### 单元测试（必跑）

| Case | 输入 | 预期 |
|---|---|---|
| C1 | `analyze_image`，question="describe this image"，缓存命中 | `FromCache=true`，不调 aiservice |
| C2 | `analyze_image`，question="count the buttons"（含 bypass 关键词） | `FromCache=false`，调用 aiservice 一次 |
| C3 | `analyze_image`，attachment URL 不存在 | 返回 graceful error 描述，nil error |
| C4 | `analyze_image`，aiservice 模拟 timeout | 返回 graceful error 描述，nil error |
| C5 | `analyze_image`，已经调用 10 次 | 第 11 次返回配额超限描述 |
| C6 | `analyze_image`，Confidence 字段在 provider 不返回时默认 0.85 | 断言 Confidence=0.85 |
| C7 | `annotate_image`，3 个 region | 串行调 aiservice 3 次，按顺序返回 3 个 annotation |
| C8 | `annotate_image`，0 个 region | 返回空 Annotations，不调 aiservice |
| C9 | `annotate_image`，超过 5 次/run | 配额超限 |
| C10 | metadata 表中 `analyze_image.RequiresVision=false` 断言 | 单元测试直接断 map value |

### 集成测试（dev 部署后）

部署 dev → 多场景测试（关键：**不只销售场景**）：

**场景 A：数据分析**
- 上传销售趋势折线图（带 6 个数据点 + 中文标签）
- prompt LLM："分析这张图里的销售趋势，重点提取每个月的数字"
- 预期 LLM 主动调 `analyze_image` 提取 OCR 数字 → 总结趋势

**场景 B：SOP 制造业**
- 上传机器操作面板的照片（含 4 个按钮 + 状态灯）
- prompt LLM："这台机器现在的状态是什么？哪些按钮是按下的？"
- 预期 LLM 调 `analyze_image` 拿状态描述，可能再调 `annotate_image` 看具体按钮区域

**场景 C：PPT 制作**
- 上传一张 slide 截图（含标题 + 3 列内容 + 一个图表）
- prompt LLM："帮我把这页 PPT 的内容转成大纲"
- 预期 LLM 调 `analyze_image` 拿布局 + 文字 → 输出大纲

**场景 D：多模态主模型主动放大**
- 用 qwen3-vl-flash（多模态主模型）做对话，上传一张细节复杂的图
- prompt："这张图里左下角那个小标签写的是什么？"
- 预期：LLM 即便能看到全图，仍主动调 `analyze_image` 或 `annotate_image` 做精细识别（验证"专家工具"在多模态主模型下也被用到）

**场景 E：单模态主模型 fallback**
- 用 GLM-5.1（单模态文本主模型）做对话，上传一张图
- prompt："这张图里写了什么？"
- 预期：LLM 调 `analyze_image`（因为它没有别的方式看图），返回 OCR + 描述

### 手动验证步骤

1. SSH dev，跑 `tail -f` 监控 agent 服务日志
2. 在前端 agent mode 起一个 session
3. 切到对应主模型（按场景）
4. 上传 attachment，等 task 1.2 异步生成完成（看日志 `attachment fallback_ready=true`）
5. 发 prompt
6. 验证：日志显示 `tool.analyze_image invoked`，Langfuse trace 出现两层 span
7. 验证：前端 chat 流回正确文字回复

### 不需要 gstack /qa

本 task 主要是后端工具实现，前端 UI 不变。集成测试通过日志 + Langfuse trace + 手动 chat 验证即可。回归保护由单元测试 + 集成测试覆盖。

## 工期估算

- 总工期：**1.5 工作日**
- 分项：
  - `analyze_image` 实现 + 缓存逻辑：**0.5 天**
  - `annotate_image` 实现 + 多 region 串行：**0.4 天**
  - quota + metadata table 接入 + runner 注册：**0.2 天**
  - 单元测试 + 集成测试 + lint：**0.4 天**

## 风险 / 待决策项

- **R1（实测）**：vision 模型 30s timeout 是否够？`qwen3-vl-plus` 实测平均响应 5-15s，30s 应该够，但**复杂图（多文字 + 多元素）可能逼近上限**。dev 部署后跟踪 P95 / P99 调用时长，若 30s 频繁打中，调到 45s
- **R2（运营调参）**：每 run 配额 10 / 5 是否合理？取决于 dev 实际用法分布。先按此配额上线，1-2 周后看数据调整。配额必须是常量，方便改
- **R3（LLM 实际使用率）**：`annotate_image` 的 region 参数对 LLM 来说是"精细"接口 — 现实中 LLM 可能不主动算坐标，更倾向于直接调 `analyze_image` 全图。dev 部署后跟踪 `annotate_image` 调用占比，若 < 5%，考虑 V2 移除并合并到 `analyze_image`
- **R4（缓存判定准确度）**：关键词 bypass 列表是初版，可能漏判（用户用同义词）或过判（误把全图描述当作精细查询）。dev 部署后跟踪 `from_cache=false` 比例，调整关键词
- **R5（Eino tool 注册兼容）**：Eino v0.8.13 在 ToolsConfig 接收 BaseTool 列表，工具实现需符合 `tool.BaseTool` 接口。开始 task 时第一件事是 verify 接口签名（`Info(ctx) *ToolInfo`，`Run(ctx, args) (string, error)` 等），若不符要写 wrapper
- **D1（已定 — D2 决策）**：单/多模态主模型都注册这两个工具，metadata 中 `RequiresVision=false`
- **D2（已定 — D1 决策）**：用 `profile.attachment.vision_describe`，不写死 model_key
- **D3（已定）**：graceful error，不返 error 给 Eino（防 NodeRunError）
- **D4（已定）**：annotate_image 不走缓存
- **D5（待 dev 数据驱动）**：配额数字、关键词列表、超时值 — 初版用约定值，根据 dev 数据调
