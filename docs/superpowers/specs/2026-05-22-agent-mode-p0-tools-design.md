# agent-mode-p0-tools — Technical Spec (S2)

> Stage: S2 · Track: standard · Repos: numind-server + numind-web-v3 · Date: 2026-05-22
> Predecessor artifacts:
> - S0 requirement: `numind-server/requirements/agent-mode-p0-tools.md` (e02cdf95)
> - S1 proposal: `numind-server/proposals/agent-mode-p0-tools-proposal.md` (5a1ac02b)

---

## §0 Spec 范围

本 spec 锁定 4 个新工具（`web_search` / `web_fetch` / `ask_user_question` / `file_read`）的：

1. 工具 input/output JSON Schema 与 Execute() 签名
2. State machine yield-turn 协议（state.go + runner.go 改造）
3. API 契约：`POST /v1/agent/sessions/:run_id/answer`
4. DB schema 变更：`agent_run` 加 2 列
5. SSE 协议：`tool_call_yield` + `run_resumed`
6. Langfuse trace topology
7. Web_search provider 集成（Tavily）+ 配置 keys
8. SSRF 防护算法（web_fetch）
9. file_read 的 mime 派发表
10. compliance gate + BudgetTracker 协同
11. Tool registry seed + migration

S3 plan 会把这些切成 9 个原子 task。

---

## §1 工具 input/output Schema 全集

### 1.1 web_search

**Tool 元数据**：
```yaml
ToolName: web_search
DisplayName: 网络搜索
Description: "Search the web for real-time information. Input: { query: string, max_results?: number, allowed_domains?: string[] }. Returns: { results: [{title, url, snippet, published_at?}], cache_hit: bool }."
UserFacingName: 网络搜索
NarrationVerb: 搜索网络
IsReadOnly: true
IsSearchOrReadCommand: true
AlwaysLoad: true
RiskLevel: safe
Category: 网络
```

**InputSchema**（JSON Schema）：
```json
{
  "type": "object",
  "properties": {
    "query": {"type": "string", "minLength": 1, "maxLength": 500, "description": "搜索关键词"},
    "max_results": {"type": "integer", "minimum": 1, "maximum": 10, "default": 5},
    "allowed_domains": {"type": "array", "items": {"type": "string"}, "maxItems": 10, "description": "可选——限定结果域名（如 ['edu.cn', 'gov.cn']）"}
  },
  "required": ["query"]
}
```

**Output**：
```json
{
  "results": [
    {"title": "string", "url": "string", "snippet": "string", "published_at": "string?"}
  ],
  "cache_hit": "bool",
  "provider": "tavily"
}
```

**Execute() 签名**：
```go
func (t *webSearchTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error)
```

错误：
- `errno.ErrBind` — input JSON 解析失败
- `errno.ErrInvalidInput` — query 空 / max_results 越界
- `errno.ErrExternalAPI` — Tavily 网络错误 / 429 限流
- `errno.ErrTimeout` — 5s 整体超时

### 1.2 web_fetch

**Tool 元数据**：
```yaml
ToolName: web_fetch
DisplayName: 网页读取
Description: "Fetch a URL and return its contents as Markdown. Input: { url: string, prompt?: string }. Returns: { title, content_md, byte_size, truncated, fetched_at }."
UserFacingName: 网页读取
NarrationVerb: 读取网页
IsReadOnly: true
IsSearchOrReadCommand: true
AlwaysLoad: true
RiskLevel: moderate (因为 outbound HTTP)
Category: 网络
```

**InputSchema**：
```json
{
  "type": "object",
  "properties": {
    "url": {"type": "string", "format": "uri", "description": "要读取的 URL；自动补 https://"},
    "prompt": {"type": "string", "maxLength": 200, "description": "可选——LLM 二次摘要的 hint，如 '提取活动时间和报名链接'"}
  },
  "required": ["url"]
}
```

**Output**：
```json
{
  "title": "string",
  "content_md": "string",
  "byte_size": "integer",
  "truncated": "bool",
  "fetched_at": "string (RFC3339)"
}
```

错误：
- `errno.ErrInvalidInput` — URL 解析失败 / 不是 http(s) / SSRF 命中
- `errno.ErrExternalAPI` — HTTP fetch 失败 / 4xx / 5xx
- `errno.ErrTimeout` — 30s 超时

**最大返回**：100KB（content_md 截断到 100KB 上限 + 设 `truncated=true`）。

### 1.3 ask_user_question

**Tool 元数据**：
```yaml
ToolName: ask_user_question
DisplayName: 反问学员
Description: "Ask the user a clarifying question with structured options. Input: { question, options: [{key, label, description?}], header?, multiSelect? }. **This tool yields the agent run** — it pauses until the user answers via POST /v1/agent/sessions/:run_id/answer."
UserFacingName: 反问
NarrationVerb: 反问
IsReadOnly: true
IsSearchOrReadCommand: false
AlwaysLoad: true
RiskLevel: safe
Category: 交互
```

**InputSchema**：
```json
{
  "type": "object",
  "properties": {
    "question": {"type": "string", "minLength": 1, "maxLength": 500},
    "options": {
      "type": "array",
      "minItems": 2,
      "maxItems": 4,
      "items": {
        "type": "object",
        "properties": {
          "key": {"type": "string", "minLength": 1, "maxLength": 50, "description": "稳定 key（如 plan_a / plan_b）"},
          "label": {"type": "string", "minLength": 1, "maxLength": 100, "description": "按钮显示文本"},
          "description": {"type": "string", "maxLength": 200, "description": "可选——按钮下方解释"}
        },
        "required": ["key", "label"]
      }
    },
    "header": {"type": "string", "maxLength": 12, "description": "可选——选项群上方小标签"},
    "multiSelect": {"type": "boolean", "default": false}
  },
  "required": ["question", "options"]
}
```

**Output**：本工具的 Execute 不返回"普通结果"——返回 `ErrYieldForUserQuestion` sentinel error，runner 捕获后：
1. 把 question payload 序列化存入 `agent_run.pending_question_json`
2. 设 `agent_run.state_reason = "waiting_for_user_choice"`
3. 通过 narration / SSE 推 `tool_call_yield` 事件给前端
4. 通过 state machine 触发 `LoopEventAskUserPaused` → 退出 ReAct loop

**Sentinel error**：
```go
var ErrYieldForUserQuestion = errors.New("agent: yield for user question")

type YieldPayload struct {
    Question     string   `json:"question"`
    Options      []Option `json:"options"`
    Header       string   `json:"header,omitempty"`
    MultiSelect  bool     `json:"multi_select"`
}

type Option struct {
    Key         string `json:"key"`
    Label       string `json:"label"`
    Description string `json:"description,omitempty"`
}

// yieldError 携带 payload；runner 通过 errors.As 取出
type yieldError struct {
    Payload YieldPayload
}

func (e *yieldError) Error() string { return "agent: yield for user question" }
func (e *yieldError) Is(target error) bool { return target == ErrYieldForUserQuestion }
```

**Execute 实现摘要**：
```go
func (t *askUserQuestionTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
    var in askUserQuestionInput
    if err := json.Unmarshal(input, &in); err != nil {
        return nil, errno.ErrBind.SetMessage("ask_user_question input: %s", err.Error())
    }
    // input 校验（options 个数、长度等）
    if err := validateAskUserQuestion(in); err != nil {
        return nil, err
    }
    return nil, &yieldError{Payload: YieldPayload{
        Question:    in.Question,
        Options:     in.Options,
        Header:      in.Header,
        MultiSelect: in.MultiSelect,
    }}
}
```

### 1.4 file_read

**Tool 元数据**：
```yaml
ToolName: file_read
DisplayName: 读取文件
Description: "Read the contents of an uploaded file by URL. Input: { file_url: string, prompt?: string }. Returns: { file_name, mime_type, content, page_count?, byte_size, truncated }."
UserFacingName: 读取文件
NarrationVerb: 读取文件
IsReadOnly: true
IsSearchOrReadCommand: true
AlwaysLoad: true
RiskLevel: moderate
Category: 文件
```

**InputSchema**：
```json
{
  "type": "object",
  "properties": {
    "file_url": {"type": "string", "format": "uri", "description": "由 POST /v1/agent-attachments 返回的 URL"},
    "prompt": {"type": "string", "maxLength": 200, "description": "可选——PDF parser hint（让 qwen-long 关注哪些方面）"}
  },
  "required": ["file_url"]
}
```

**Output**：
```json
{
  "file_name": "string",
  "mime_type": "string",
  "content": "string (markdown 或 plain text)",
  "page_count": "integer (PDF 专用)?",
  "byte_size": "integer",
  "truncated": "boolean (content > 200KB 时 true)"
}
```

**Mime 派发表**：

| mime_type | parser | 实现 |
|-----------|--------|------|
| application/pdf | qwen-long via aiservice | DashScope file upload → 文件 ID → qwen-long extract |
| image/png, image/jpeg, image/webp, image/gif | 阿里 OCR via biz/ali | 已有 `biz/ali/ocr.go` 调 DashScope OCR |
| text/plain, text/markdown | 直接读 | http.Get URL → string |
| 其他（docx / xlsx / 等）| 拒绝 | 返回 `errno.ErrUnsupportedFileType` |

**user_id 校验**：
- attachment URL 格式：`https://<cos-bucket>.cos.<region>.myqcloud.com/agent-attachments/<user_id>/<timestamp>-<filename>`
- 工具解析 URL path 段 `agent-attachments/<user_id>/...` → 与 `ctx.user_id` 比对 → 不匹配 → `errno.ErrPermissionDenied`
- 跨账户读取：父账户读子账户文件**也拒绝**（v1 严格策略）

**最大返回**：content 截断到 200KB；PDF 总页数限 60 页（5s/页 × 60 = 5 分钟硬上限）。

---

## §2 State machine yield-turn 协议

### 2.1 state.go 改动

**新增枚举：**
```go
// state.go: 第 14 个 TerminalReason
TerminalWaitingForUserChoice TerminalReason = "waiting_for_user_choice"

// state.go: 第 20 个 LoopEvent
LoopEventAskUserPaused LoopEvent = iota + 19  // 在 LoopEventErrorMaxBudget 之后
```

**Transition() 新 case：**
```go
case LoopEventAskUserPaused:
    s.TerminalReason = TerminalWaitingForUserChoice
    return TerminalWaitingForUserChoice, "", true
```

**编译期不变量长度变 14 + 20**（更新 state.go 第 33-43 行的 `[13]TerminalReason{...}` 和 `[19]LoopEvent{...}` 长度）。

**重要语义**：`TerminalWaitingForUserChoice` 在状态机层是 terminal（loop 退出），但在 agent_run model 层是 semi-terminal（is_resumable=true，等待 user answer 后可重启）。

### 2.2 runner.go 改动

**新增 yield 处理（在 ReAct loop 内 tool 执行后）：**

```go
// Pseudo-code: 在 ReAct loop 的 tool exec 块内
toolResult, err := tool.Execute(toolCtx, toolInput)
if err != nil {
    // 新增：捕获 yield sentinel
    var yieldErr *yieldError
    if errors.As(err, &yieldErr) {
        // 1. 序列化 question payload
        payloadJSON, _ := json.Marshal(yieldErr.Payload)
        
        // 2. 更新 agent_run row
        if err := r.runStore.SetPendingQuestion(ctx, runID, payloadJSON); err != nil {
            return nil, fmt.Errorf("runner: set pending question: %w", err)
        }
        
        // 3. Langfuse span
        if tc := langfuse.FromContext(ctx); tc != nil {
            spanID := langfuse.SpanID()
            langfuse.CreateSpan(tc.TraceID, spanID,
                langfuse.WithSpanParent(tc.ParentObservationID),
                langfuse.WithSpanName("tool.ask_user_question.yield"),
                langfuse.WithSpanInput(yieldErr.Payload),
            )
            langfuse.EndSpan(spanID)
        }
        
        // 4. BudgetTracker pause（如已 wire）
        if r.budgetTracker != nil {
            r.budgetTracker.Pause(runID)
        }
        
        // 5. Narration: 推 tool_call_yield 事件
        if r.narrationProvider != nil {
            r.narrationProvider.EmitYield(runID, yieldErr.Payload)
        }
        
        // 6. State machine transition
        terminal, _, isTerminal := state.Transition(LoopEventAskUserPaused)
        if isTerminal {
            // Set run state_reason
            r.runStore.UpdateStateReason(ctx, runID, string(terminal))
            // Exit loop
            return &RunResult{
                AgentRunID:     runID,
                TerminalReason: terminal,
                FinalOutput:    "",  // 无 final output，run 还没结束
                StepCount:      state.StepCount,
                Duration:       time.Since(start),
            }, nil
        }
    }
    // 现有错误处理逻辑保留（非 yield 错误）
    return nil, err
}
```

### 2.3 Resume 协议（answer endpoint 触发）

```
1. POST /v1/agent/sessions/:run_id/answer 收到 user answer
2. biz.AgentBiz.AnswerPendingQuestion(ctx, userID, runID, answer):
   a. Load agent_run by ID, 校验 user_id == ctx.user_id
   b. 校验 state_reason == "waiting_for_user_choice"
   c. 校验 pending_question_json IS NOT NULL
   d. 把 answer 打包为 user message: 
      content = "[user answered]\nQuestion: <q>\nSelected: <opts>\nFree text: <ft>"
      runStore.AppendMessage(ctx, runID, role="user", content)
   e. Clear: pending_question_json = NULL, state_reason = "running"
   f. BudgetTracker.Resume(runID) 如已 wire
   g. Langfuse span: tool.ask_user_question.resume (含 user answer)
   h. Goroutine 重启 runner.Run(ctx, RunRequest{
        UserID:        userID,
        ExistingRunID: runID,
        AgentDefinitionID: <从 row 取>,
        ... // 其他字段从 row 还原
      })
   i. Narration: 推 run_resumed 事件
3. Endpoint return 200 { run_id, status: "resumed" }
4. 前端继续 SSE 监听 narration stream（同 run_id）
```

### 2.4 Sequence Diagram (ascii)

```
学员                前端                后端              ReAct loop         LLM
  │                   │                   │                  │                │
  │ "我表姐想报国防"   │                   │                  │                │
  ├──────────────────►│                   │                  │                │
  │                   │ POST /agent-runs  │                  │                │
  │                   ├──────────────────►│                  │                │
  │                   │                   │ Run(start)       │                │
  │                   │                   ├─────────────────►│                │
  │                   │                   │                  │ LLM call       │
  │                   │                   │                  ├───────────────►│
  │                   │                   │                  │ tool_call:     │
  │                   │                   │                  │  ask_user_q    │
  │                   │                   │                  │◄───────────────┤
  │                   │                   │                  │ Tool.Execute() │
  │                   │                   │                  │  → yieldError  │
  │                   │                   │ SetPendingQ      │                │
  │                   │                   │ Pause(budget)    │                │
  │                   │ SSE:tool_call_yield (question)       │                │
  │                   │◄─────────────────────────────────────┤                │
  │ <按钮 UI>          │                   │                  │                │
  │◄──────────────────┤                   │                  │                │
  │ 点击 "录取分数线"  │                   │                  │                │
  ├──────────────────►│                   │                  │                │
  │                   │ POST /sessions/   │                  │                │
  │                   │   :run_id/answer  │                  │                │
  │                   ├──────────────────►│                  │                │
  │                   │                   │ Inject msg       │                │
  │                   │                   │ ClearPendingQ    │                │
  │                   │                   │ Resume(budget)   │                │
  │                   │ SSE:run_resumed   │                  │                │
  │                   │◄──────────────────┤                  │                │
  │                   │                   │ Run(resume)      │                │
  │                   │                   ├─────────────────►│                │
  │                   │                   │                  │ LLM call cont. │
  │                   │                   │                  ├───────────────►│
  │                   │                   │                  │ (normal flow)  │
  │                   │ SSE:narration     │                  │                │
  │                   │◄─────────────────────────────────────┤                │
  │ "看录取分数线..."   │                   │                  │                │
  │◄──────────────────┤                   │                  │                │
```

---

## §3 API 契约

### 3.1 POST /v1/agent/sessions/:run_id/answer

**Auth**: user_token middleware
**Path param**: `run_id` uint64
**Body**:
```json
{
  "selected": ["plan_a"],
  "free_text": "可选自由文本"
}
```

**Response 200**:
```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "run_id": 123,
    "status": "resumed"
  }
}
```

**Response 400**（参数错误）：
```json
{"code": 100403, "message": "selected[] 为空 / 数量 > 4"}
```

**Response 404**（run 不存在）：
```json
{"code": 100404, "message": "run not found"}
```

**Response 403**（user 不匹配 / 状态不对）：
```json
{"code": 100403, "message": "run is not waiting for user input"}
```

**Controller signature**（`internal/numind/controller/v1/agent/student_run.go`）：
```go
authGroup.POST("/agent-runs/:id/answer", c.Answer)

func (h *StudentRunController) Answer(c *gin.Context) {
    user := middleware.GetCurrentUser(c)
    if user == nil { core.WriteResponse(c, errno.ErrTokenInvalid, nil); return }
    
    runID, err := strconv.ParseUint(c.Param("id"), 10, 64)
    if err != nil { core.WriteResponse(c, errno.ErrBind.SetMessage("invalid id"), nil); return }
    
    var req agent.AnswerRequest
    if err := c.ShouldBindJSON(&req); err != nil { core.WriteResponse(c, errno.ErrBind.SetMessage("%s", err.Error()), nil); return }
    
    resp, err := h.runSvc.Answer(c.Request.Context(), user.ID, runID, req)
    core.WriteResponse(c, err, resp)
}
```

> 注意路径：`/v1/agent-runs/:id/answer`（与现有 `/v1/agent-runs/:id/cancel` 等并列），不是 brief 中的 `/v1/agent/sessions/:id/answer`。理由：保持与现有 student_run endpoints 命名一致。

### 3.2 复用现有：POST /v1/agent-attachments

无需改动。已有 `attachment.UploadService.Upload(ctx, userID, file, hdr)` 返回 `UploadResult{URL, Size, MimeType, Filename, CreatedAt}`。

---

## §4 DB schema 变更

### 4.1 agent_run 表新增 2 列

**Migration file**: `migrations/20260522_153000_add_agent_run_pending_question.sql`

```sql
ALTER TABLE `agent_run`
  ADD COLUMN `pending_question_json` JSON NULL COMMENT '若 state_reason=waiting_for_user_choice 则存 ask_user_question payload',
  ADD COLUMN `pending_question_at` TIMESTAMP NULL COMMENT '该 question 入库时间，便于 admin 检索"卡了多久"';

-- Index for finding stuck runs in admin dashboards
CREATE INDEX `idx_ar_state_pending` ON `agent_run`(`state_reason`, `pending_question_at`);
```

**Rollback** (reference)：
```sql
DROP INDEX `idx_ar_state_pending` ON `agent_run`;
ALTER TABLE `agent_run` DROP COLUMN `pending_question_at`, DROP COLUMN `pending_question_json`;
```

### 4.2 GORM model 增字段

`internal/pkg/model/agent.go` 的 `AgentRun` struct 加：

```go
type AgentRun struct {
    // ... existing fields ...
    PendingQuestionJSON datatypes.JSON `gorm:"type:json;column:pending_question_json" json:"pending_question_json,omitempty"`
    PendingQuestionAt   *time.Time     `gorm:"type:timestamp null;column:pending_question_at" json:"pending_question_at,omitempty"`
}
```

### 4.3 tool_definition seed migration

**Migration file**: `migrations/20260522_154500_seed_p0_tool_definitions.sql`

```sql
INSERT INTO `tool_definition` (`tool_name`, `display_name`, `description`, `source`, `risk_level`, `requires_sandbox`, `requires_tenant_whitelist`, `input_schema`, `category`, `is_active`, `created_at`, `updated_at`)
VALUES
  ('web_search', '网络搜索', 'Search the web for real-time information.', 'platform', 'safe', FALSE, FALSE, '{...input schema json...}', '网络', TRUE, NOW(), NOW()),
  ('web_fetch', '网页读取', 'Fetch a URL and return its contents as Markdown.', 'platform', 'moderate', FALSE, FALSE, '{...}', '网络', TRUE, NOW(), NOW()),
  ('ask_user_question', '反问学员', 'Ask the user a clarifying question with structured options. Yields the run.', 'platform', 'safe', FALSE, FALSE, '{...}', '交互', TRUE, NOW(), NOW()),
  ('file_read', '读取文件', 'Read the contents of an uploaded file by URL.', 'platform', 'moderate', FALSE, FALSE, '{...}', '文件', TRUE, NOW(), NOW())
ON DUPLICATE KEY UPDATE
  display_name = VALUES(display_name),
  description = VALUES(description),
  is_active = TRUE,
  updated_at = NOW();
```

**Note**: `tool_definition` 已有的 AutoMigrate 体系（`agent-mode-e2e-rollout` #14 落地）会**自动**在 server 启动时 seed —— 实际上不需要手动 SQL，只需在 `factory_platform.go::LoadTools` 列表追加 4 个 ToolMetadata。但**显式 migration SQL**作为 idempotent fallback，给 prod 环境一个 explicit checkpoint。

---

## §5 SSE 协议

### 5.1 新增 event types

**`tool_call_yield`** — agent 调 ask_user_question 时推
```
event: tool_call_yield
data: {"run_id": 123, "tool": "ask_user_question", "payload": {"question": "...", "options": [...], "multiSelect": false, "header": "..."}}
```

**`run_resumed`** — user answer 接收后推
```
event: run_resumed
data: {"run_id": 123}
```

### 5.2 既有 narration event 流不变

`tool_call_yield` 是独立的事件类型，不混入 narration 文本流。前端区分：narration 渲染为消息泡泡，`tool_call_yield` 渲染为 QuestionPrompt 组件。

### 5.3 narration.Provider 改动

`internal/numind/biz/narration/provider.go` 加方法：
```go
func (p *Provider) EmitYield(runID uint64, payload YieldPayload) {
    // 走与现有 narration message 相同的 channel/buffer 体系
    // 但 event type = "tool_call_yield"
}

func (p *Provider) EmitResumed(runID uint64) {
    // event type = "run_resumed"
}
```

---

## §6 Langfuse trace topology

### 6.1 既有

- `runner.go::Run` 已 CreateTrace（trace_id 存入 agent_run.langfuse_trace_id）
- 每次 LLM 调用走 CreateGeneration

### 6.2 4 工具新增

| 工具 | 类型 | Span / Generation | 关键 metadata |
|------|------|-------------------|--------------|
| web_search | Span | `tool.web_search.execute` | provider=tavily, query, results_count, cache_hit, latency_ms |
| web_fetch | Span | `tool.web_fetch.execute` | url, status_code, content_length, mime_type, latency_ms, truncated |
| ask_user_question (yield) | Span | `tool.ask_user_question.yield` | question, options_count, multi_select |
| ask_user_question (resume) | Span | `tool.ask_user_question.resume` | user_answer.selected[], user_answer.free_text, wait_duration_ms |
| file_read (PDF) | Generation | `tool.file_read.qwen-long.parse` | model=qwen-long, byte_size, page_count, prompt_tokens, completion_tokens |
| file_read (image) | Span | `tool.file_read.ocr` | provider=ali, byte_size, mime_type |
| file_read (text) | Span | `tool.file_read.direct` | byte_size, mime_type, truncated |

### 6.3 ask_user_question 跨 yield-resume 的 trace 连贯

- 同 trace_id（agent_run.langfuse_trace_id 在 run 第一次 start 时 set，整个 run 复用）
- yield span 在 run 第一段（pre-yield）结束时 EndSpan
- resume span 在 run 第二段（post-resume）开始时 CreateSpan，parent = trace root
- 两个 span 在 Langfuse UI 同一个 trace 树下，时间轴有 gap（user wait time），但同 trace

---

## §7 web_search provider 集成

### 7.1 Tavily 选型理由（S1-D1 已建议）

| 维度 | Tavily | Serper | Bing | DuckDuckGo |
|------|--------|--------|------|-----------|
| LLM-friendly | ✅ snippet 摘要、include_answer | ⚠️ raw Google results | ⚠️ Bing format | ❌ scrape HTML |
| 国际访问 | ✅ Vercel/Cloudflare hosted | ✅ Google CDN | ⚠️ 国内慢 | ❌ 国内被墙 |
| 价格 | $5 / 1000 query 透明 | $50/月 (~5000 query) | $1k+ 起 | 免费 (但易 ban) |
| Quota / 限速 | 1k/月免费 → 付费 100QPS | 50QPS | 3QPS | 无限但 fragile |
| API 复杂度 | 简单 POST + JSON | 简单 | 较复杂 (auth) | reverse engineering |
| 教育领域质量 | 良好 (含 arxiv / .edu) | 良好 (Google base) | 一般 | 一般 |

**结论：选 Tavily v1**。理由：(1) LLM-friendly 摘要省 token；(2) 国际访问稳定，避开国内被墙问题；(3) 价格透明可控；(4) API 简洁，集成成本低；(5) 教育内容质量好。

**Fallback 策略**：v1 不接备份 provider。Tavily 故障 → web_search 返回 error，agent narration 提示"网络搜索暂不可用"，agent 用其他工具或 LLM 已有知识答。

### 7.2 配置 keys

**`config_local.yaml` / `config_dev.yaml`**:
```yaml
web_search:
  provider: tavily
  tavily:
    api_key: ""  # 通过 env var TAVILY_API_KEY 注入或在 local 配
    base_url: "https://api.tavily.com"
    timeout_seconds: 5
    cache_ttl_seconds: 300
```

**`config_prod.yaml`**：**本 feature 不改 prod 配置**（CLAUDE.md 硬规则）。Prod 上线时由用户手工 sync。

**`internal/numind/biz/agent/tool_web_search.go`** 读 config：
```go
type webSearchConfig struct {
    Provider        string `mapstructure:"provider"`
    TavilyAPIKey    string `mapstructure:"tavily.api_key"`
    TavilyBaseURL   string `mapstructure:"tavily.base_url"`
    TimeoutSeconds  int    `mapstructure:"timeout_seconds"`
    CacheTTLSeconds int    `mapstructure:"cache_ttl_seconds"`
}
```

### 7.3 aiservice wrapper

虽然 Tavily 不是 LLM，**仍包一层 aiservice wrapper** 保证 Langfuse Span + 后续路由能力：

```go
// internal/pkg/aiservice/web_search.go (新文件)
package aiservice

type WebSearchRequest struct {
    Query          string
    MaxResults     int
    AllowedDomains []string
}

type WebSearchResult struct {
    Title       string
    URL         string
    Snippet     string
    PublishedAt string  // RFC3339 or empty
}

func WebSearch(ctx context.Context, req WebSearchRequest) ([]WebSearchResult, bool /* cache_hit */, error) {
    // 1. cache check（in-memory TTL）
    // 2. provider 路由（v1 只 Tavily）
    // 3. Langfuse Span
    // 4. 返回
}
```

### 7.4 in-memory cache

```go
// internal/numind/biz/agent/tool_web_search.go
type searchCacheEntry struct {
    Results []aiservice.WebSearchResult
    Expiry  time.Time
}

type webSearchTool struct {
    BaseTool
    config  webSearchConfig
    cache   map[string]searchCacheEntry  // key = query|max|domains_hash
    cacheMu sync.RWMutex
}

func (t *webSearchTool) cacheGet(key string) ([]aiservice.WebSearchResult, bool) {
    t.cacheMu.RLock(); defer t.cacheMu.RUnlock()
    e, ok := t.cache[key]
    if !ok || time.Now().After(e.Expiry) { return nil, false }
    return e.Results, true
}

func (t *webSearchTool) cachePut(key string, results []aiservice.WebSearchResult) {
    t.cacheMu.Lock(); defer t.cacheMu.Unlock()
    if len(t.cache) > 1000 {  // crude cap; LRU eviction TODO
        t.cache = make(map[string]searchCacheEntry, 100)
    }
    t.cache[key] = searchCacheEntry{
        Results: results,
        Expiry:  time.Now().Add(time.Duration(t.config.CacheTTLSeconds) * time.Second),
    }
}
```

---

## §8 SSRF 防护（web_fetch）

### 8.1 算法

```go
func validateFetchURL(rawURL string) (string, error) {
    // 1. 自动补 https:// 前缀
    if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
        rawURL = "https://" + rawURL
    }
    
    u, err := url.Parse(rawURL)
    if err != nil {
        return "", errno.ErrInvalidInput.SetMessage("invalid URL: %s", err.Error())
    }
    
    // 2. 拒绝非 http(s)
    if u.Scheme != "http" && u.Scheme != "https" {
        return "", errno.ErrInvalidInput.SetMessage("unsupported scheme: %s", u.Scheme)
    }
    
    // 3. host 非空
    host := u.Hostname()
    if host == "" {
        return "", errno.ErrInvalidInput.SetMessage("URL missing host")
    }
    
    // 4. 拒绝 .local TLD
    if strings.HasSuffix(strings.ToLower(host), ".local") {
        return "", errno.ErrInvalidInput.SetMessage("internal hostname not allowed")
    }
    
    // 5. DNS resolve → 拿所有 IP（v4 + v6）
    ips, err := net.LookupIP(host)
    if err != nil {
        return "", errno.ErrInvalidInput.SetMessage("DNS resolve failed: %s", err.Error())
    }
    
    // 6. check 每个 IP 是否 loopback / private / link-local / multicast
    for _, ip := range ips {
        if ip.IsLoopback() {
            return "", errno.ErrInvalidInput.SetMessage("loopback address not allowed")
        }
        if ip.IsPrivate() {
            return "", errno.ErrInvalidInput.SetMessage("private address not allowed")
        }
        if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
            return "", errno.ErrInvalidInput.SetMessage("link-local not allowed")
        }
        // Cloud metadata endpoints
        if ip.Equal(net.IPv4(169,254,169,254)) ||  // AWS / GCP
           ip.Equal(net.ParseIP("fd00:ec2::254")) { // AWS IPv6
            return "", errno.ErrInvalidInput.SetMessage("cloud metadata endpoint blocked")
        }
    }
    
    return u.String(), nil
}
```

### 8.2 TOCTOU 注意

DNS resolve 跟实际 HTTP fetch 中间有时间差，攻击者可能用 "DNS rebinding" 绕过。缓解：
- 用 `http.Transport.DialContext` 自定义，**在 Dial 时再 check IP**（重复验证）
- 整体 timeout 30s，rebinding 时间窗小

实现见 `internal/numind/biz/agent/tool_web_fetch.go` 的 `safeHTTPClient()` helper。

---

## §9 file_read mime 派发表

### 9.1 派发逻辑

```go
func (t *fileReadTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
    var in fileReadInput
    if err := json.Unmarshal(input, &in); err != nil { ... }
    
    // 1. user_id check (parse URL path)
    if !isAttachmentOfUser(in.FileURL, ctx) {
        return nil, errno.ErrPermissionDenied.SetMessage("file not owned by current user")
    }
    
    // 2. HEAD request to get mime_type + size
    head, err := httpclient.Head(ctx, in.FileURL)
    if err != nil { ... }
    
    mimeType := head.Header.Get("Content-Type")
    size := head.ContentLength
    
    // 3. dispatch
    var (
        content    string
        pageCount  int
        truncated  bool
    )
    switch {
    case mimeType == "application/pdf":
        content, pageCount, truncated, err = t.parsePDF(ctx, in.FileURL, in.Prompt)
    case strings.HasPrefix(mimeType, "image/"):
        content, err = t.parseImage(ctx, in.FileURL)
    case mimeType == "text/plain" || mimeType == "text/markdown":
        content, truncated, err = t.parseText(ctx, in.FileURL)
    default:
        return nil, errno.ErrUnsupportedFileType.SetMessage("unsupported mime: %s", mimeType)
    }
    
    if err != nil { return nil, err }
    
    return ToolResult(json.Marshal(fileReadOutput{
        FileName:   path.Base(in.FileURL),
        MimeType:   mimeType,
        Content:    content,
        PageCount:  pageCount,
        ByteSize:   size,
        Truncated:  truncated,
    }))
}
```

### 9.2 PDF parser（qwen-long via aiservice）

DashScope qwen-long 已在 `internal/pkg/aiservice` 体系内。需要：
1. Upload file via `internal/service/bailian_http.go` 或 DashScope compatible-mode file upload（实现细节 S4 查现有 code）
2. Get file_id（DashScope 内部 ID）
3. Call qwen-long with `system: "extract content as markdown"` + file_id reference
4. Parse response, count pages from response metadata

### 9.3 Image OCR（阿里 OCR via biz/ali）

```go
func (t *fileReadTool) parseImage(ctx context.Context, url string) (string, error) {
    // biz/ali 已有 OCR client; reuse via aiservice wrapper if exists, else call directly
    result, err := t.aliOCR.Extract(ctx, url)
    if err != nil { return "", err }
    return result.Text, nil
}
```

### 9.4 Text 直读

```go
func (t *fileReadTool) parseText(ctx context.Context, url string) (string, bool, error) {
    resp, err := httpclient.Get(ctx, url)
    if err != nil { return "", false, err }
    body, err := io.ReadAll(io.LimitReader(resp.Body, 200*1024+1))
    if err != nil { return "", false, err }
    truncated := len(body) > 200*1024
    if truncated { body = body[:200*1024] }
    return string(body), truncated, nil
}
```

---

## §10 Compliance gate + BudgetTracker 协同

### 10.1 Compliance gate

新工具默认走 compliance gate 的 hook chain（`compliancegate.WrapHooks`）。

`compliance.ToolInfo` 已有 `IsDestructive bool`——4 新工具填：
- web_search: false
- web_fetch: false
- ask_user_question: false
- file_read: false

全部 read-only / non-destructive，compliance gate 默认 ALLOW（除非 admin 在 platform_rules 或 tenant_rules 显式 deny）。

### 10.2 BudgetTracker

`ask_user_question` yield 时：
- `r.budgetTracker.Pause(runID)` — 暂停 wall-clock tracking
- Resume 时调 `r.budgetTracker.Resume(runID)` — 恢复

其他 3 工具（web_search / web_fetch / file_read）—— BudgetTracker 不需特殊处理：
- web_search / web_fetch: 自身延迟 < 5s，正常计入 wall-clock budget
- file_read PDF: 调 qwen-long 时 token 经 aiservice 进 budget tracker 的 token bucket

---

## §11 Tool Registry 注册

`internal/numind/biz/agent/factory_platform.go` 的 `LoadTools()` 改造：

```go
tools := []FullTool{
    &kbSearchTool{rag: f.rag},
    &learnerDataQueryTool{users: usersGetter},
    &documentGenerateTool{},
    &imageGenTool{},
    &bashExecTool{},
    &getCurrentDateTool{},
    // ─── 本 feature 新增 ───
    &webSearchTool{config: f.webSearchConfig},
    &webFetchTool{},
    &askUserQuestionTool{},
    &fileReadTool{aiSvc: f.aiSvc, aliOCR: f.aliOCR},  // 依赖见 §12
}
metadata := []ToolMetadata{
    // ... existing 6 ...
    {ToolName: "web_search", DisplayName: "网络搜索", Description: "...", Source: "platform", RiskLevel: "safe", Category: "网络", InputSchema: webSearchSchema},
    {ToolName: "web_fetch", DisplayName: "网页读取", Description: "...", Source: "platform", RiskLevel: "moderate", Category: "网络", InputSchema: webFetchSchema},
    {ToolName: "ask_user_question", DisplayName: "反问学员", Description: "...", Source: "platform", RiskLevel: "safe", Category: "交互", InputSchema: askUserQuestionSchema},
    {ToolName: "file_read", DisplayName: "读取文件", Description: "...", Source: "platform", RiskLevel: "moderate", Category: "文件", InputSchema: fileReadSchema},
}
// 既有 memory tool 追加保持不变
if f.ds != nil {
    np := memory.NewNotepad(f.ds.UserGlobalMemories())
    tools = append(tools, NewMemoryWriteTool(np), NewMemoryReadTool(np))
    metadata = append(metadata, /* memory_write metadata */, /* memory_read metadata */)
}
```

---

## §12 biz.go 装配链改动

`internal/numind/biz/biz.go` 的 `IBiz` 体系：

1. `platformToolFactory` 增 fields：`webSearchConfig` + `aiSvc` + `aliOCR`
2. `NewPlatformToolFactory` 加参数：传入 config + aiservice ref + ali OCR client
3. `biz.Init` 装配时：从 viper 读 `web_search.*` config，传给 factory

---

## §13 前端协议

### 13.1 QuestionPrompt.vue（新组件）

`numind-web-v3/src/components/agent/QuestionPrompt.vue`

```vue
<template>
  <div class="question-prompt" role="region" aria-label="agent 反问">
    <div v-if="header" class="prompt-header">{{ header }}</div>
    <div class="prompt-question">{{ question }}</div>
    <div class="prompt-options">
      <button
        v-for="opt in options"
        :key="opt.key"
        :class="{ selected: isSelected(opt.key) }"
        :disabled="submitting"
        @click="toggleOption(opt.key)"
      >
        <div class="opt-label">{{ opt.label }}</div>
        <div v-if="opt.description" class="opt-desc">{{ opt.description }}</div>
      </button>
    </div>
    <textarea
      v-if="multiSelect"
      v-model="freeText"
      placeholder="补充说明（可选）"
      :disabled="submitting"
      maxlength="500"
    />
    <button
      v-if="multiSelect"
      class="submit-btn"
      :disabled="selected.length === 0 || submitting"
      @click="submit"
    >
      {{ submitting ? '提交中...' : '提交' }}
    </button>
  </div>
</template>

<script setup lang="ts">
import { ref, computed } from 'vue'
import { postAgentAnswer } from '@/api/agent'

const props = defineProps<{
  runId: number
  question: string
  options: { key: string; label: string; description?: string }[]
  header?: string
  multiSelect: boolean
}>()

const emit = defineEmits<{
  (e: 'answer-submitted', runId: number): void
}>()

const selected = ref<string[]>([])
const freeText = ref('')
const submitting = ref(false)

const isSelected = (key: string) => selected.value.includes(key)

function toggleOption(key: string) {
  if (props.multiSelect) {
    if (isSelected(key)) {
      selected.value = selected.value.filter(k => k !== key)
    } else {
      selected.value.push(key)
    }
  } else {
    selected.value = [key]
    // 单选模式立刻 submit
    submit()
  }
}

async function submit() {
  if (selected.value.length === 0) return
  submitting.value = true
  try {
    await postAgentAnswer(props.runId, {
      selected: selected.value,
      free_text: freeText.value || undefined,
    })
    emit('answer-submitted', props.runId)
  } catch (err) {
    // toast error; reset submitting
    submitting.value = false
  }
}
</script>
```

### 13.2 AgentChatView.vue 改造

监听新 SSE events：
- `tool_call_yield`: 渲染 QuestionPrompt 作为消息流的特殊 bubble
- `run_resumed`: 隐藏 QuestionPrompt，输入框 enable，等待 narration stream 继续

### 13.3 API client

`numind-web-v3/src/api/agent.ts`：
```typescript
export async function postAgentAnswer(
  runId: number,
  payload: { selected: string[]; free_text?: string }
): Promise<{ run_id: number; status: 'resumed' }> {
  return request.post(`/v1/agent-runs/${runId}/answer`, payload)
}
```

---

## §14 测试策略

### 14.1 单元测试（per-tool）

每个工具有专属 `tool_<name>_test.go`，覆盖：
- ✅ happy path：典型输入 → 期望输出
- ✅ error path：input 校验失败 / provider error / timeout
- ✅ 边界：空字符串 / max length / max options / mime type 不支持
- ✅ 权限：file_read 跨账户拒绝

### 14.2 State machine 单元测试

`internal/numind/biz/agent/state_test.go` 增 case：
- `TestLoopState_Transition_AskUserPaused` — 验证 LoopEventAskUserPaused → TerminalWaitingForUserChoice, isTerminal=true

### 14.3 Runner 集成测试

`internal/numind/biz/agent/runner_integration_test.go` 增 case：
- `TestRunner_Run_AskUserQuestionYield` — 模拟 LLM 返回 ask_user_question tool_call → 验证 pending_question_json 入库 + state_reason 改 + budget paused
- `TestRunner_Run_ResumeAfterAnswer` — 接上文，调 AnswerPendingQuestion → 验证 state_reason cleared + budget resumed + 新 user message inject

### 14.4 Playwright E2E

`numind-web-v3/e2e/agent-ask-user-question.spec.ts`：
- 登录 → 进 agent chat → 发送会触发 ask_user_question 的消息（可用 fixture agent 配特定 prompt 强制 LLM 选这个 tool）
- 等 QuestionPrompt 渲染
- 截图
- 点击 option → 等 narration 继续
- 截图 + 校验 chat 历史含答案

### 14.5 验证策略（S5 阶段执行）

per CLAUDE.md §4.10 规则 10，S3 plan 末尾必须有独立 task "S5 验证策略" 包含：
- 验证方式：Playwright E2E（ask_user_question 主流程）+ 后端 go test（其他 3 工具 + state machine）
- 关键路径：登录 → 上传文件 → 触发 4 工具 → 看 trace + narration
- 回归保护：4 工具的 Playwright e2e + state machine unit test 永久留库

---

## §15 风险与回滚

### 15.1 Rollback 路径

- Migration 增的 2 列：可 DROP COLUMN 回滚（无外键依赖）
- tool_definition seed：可 SET is_active=FALSE 软关闭（不删除行，保留审计）
- factory_platform 列表：git revert
- runner.go yield 处理：git revert（无外部状态变更，幂等）
- 前端组件：直接删除文件 + revert AgentChatView 改动

### 15.2 数据一致性

- pending_question_json 与 state_reason 一致性：用 GORM 事务保证两字段同 INSERT/UPDATE
- runner restart 后 ExistingRunID 的 attachment_urls 等需正确恢复（既有 ExistingRunID 路径已处理）

### 15.3 已知限制

- web_fetch 不支持 JS 渲染（SPA 类页面读不到）
- file_read 不支持 docx（v1 决策）
- ask_user_question 无 timeout（永久 pending 可能性，需 admin 手工 cancel）
- web_search Tavily quota 1k/月免费，dev 测试可能耗光（监控）

---

## §16 决策 ADR 索引

S2 spec 涉及的实施决策：

- **S2-D1**: state machine yield 用 sentinel error + Transition() 14th case（选项 A），不引入额外 LoopState flag（选项 B），不创建 "WaitingForUser" 非终态（选项 C）。理由：A 复用现有 Transition() 接口；保持 isTerminal 语义不变；agent_run model 通过 pending_question_json 区分 semi-terminal。
- **S2-D2**: answer endpoint 路径 `/v1/agent-runs/:id/answer`（与 cancel/extend-budget 并列），不是 brief 中的 `/v1/agent/sessions/:id/answer`。理由：现有 student_run 用 `/agent-runs/` prefix，保持一致。
- **S2-D3**: file_read 用 file_url 而非新建 file_id 抽象。理由：attachment.UploadService 返回的是 URL，不引入新 PK；user_id 校验通过 URL path 解析。
- **S2-D4**: web_search 即使非 LLM 仍包 aiservice wrapper（`internal/pkg/aiservice/web_search.go`）。理由：CLAUDE.md `.claude/rules/ai-service.md §0` 硬规则，统一入口 + Langfuse Span。
- **S2-D5**: SSRF 防护用 `net.IP.IsLoopback/IsPrivate/IsLinkLocalUnicast` 标准库 + 显式 cloud metadata IP block，不依赖第三方 lib。理由：依赖最小化 + 标准库覆盖足够。
- **S2-D6**: tool_definition seed 走 AutoMigrate 而非显式 migration SQL。理由：agent-mode-e2e-rollout #14 已建立 AutoMigrate 机制；显式 SQL 重复且易漂移。可附 SQL 文件作 idempotent fallback 供 prod 手工跑。
- **S2-D7**: PDF parser 用 qwen-long via aiservice（不用 pdfcpu / go-fitz）。理由：qwen-long 已在 aiservice 体系；解析质量优于 raw text extract（保留图表、表格语义）；走 aiservice 自动有 Langfuse Generation。

---

*S2 完成。进入 S3：拆 9 个原子 task plan + S5 验证策略 task 独立。*
