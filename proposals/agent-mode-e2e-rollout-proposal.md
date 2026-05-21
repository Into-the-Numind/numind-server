# NDF S1 Proposal · `agent-mode-e2e-rollout`

**Track**：Standard
**Feature ID**：`agent-mode-e2e-rollout` (#14/14)
**Author**: AI (autopilot)
**Date**: 2026-05-21
**前置**：S0 `c8aeeded` + reviewer fixes `dd3203cd`，0 P0/P1 残留

---

## §1 总览

13 个 feature 留下 9 个 mock 切换点（A1-A9）+ 8 个 e2e 端到端缺口（B1-B8）+ 5 个 admin-web 补全（C1-C5）+ dev 部署链 + prod 准备文档。本 S1 给出每个交付的**技术方案 + 接口契约 + 文件 wiring 草案 + 风险**。

## §2 Phase A 技术方案

### A1 Adapter Generate — Eino ReAct loop 接通 aiservice

> **S1 reviewer P1-1 修正（critical）**：原描述错把 A1 主要工作放到 `adapter_full_to_eino.go`。实际情况：
> - `adapter.go:56` 已有 `Generate(ctx, msgs)` 实装，已经走 `aiservice.Chat(ctx, a.taskID, req)`
> - `adapter.go:67` 已有 `Stream(ctx, msgs)` 实装
> - `adapter_full_to_eino.go` 是 `fullToolEinoAdapter`（工具包装器，实现 `einotool.InvokableTool`），与 `aiserviceAdapter` 是两个独立角色 — **不要混淆**
>
> **A1 真实工作位置**：`runner.go:389` 的 `_ = einoAgent` 短路 + 后续状态机简化

**现状**（`runner.go:375-389`）：
```go
einoAgent, err := react.NewAgent(queryCtx, &react.AgentConfig{
    ToolCallingModel: einoAdapter,
    ToolsConfig:      compose.ToolsNodeConfig{Tools: einoTools},
    MaxStep:          30,
})
if err != nil { ... UpdateState terminated ... }
_ = einoAgent // #2 不在 runner 内执行完整 loop，留给 Task 8 集成测试
// 接下来是简化状态机 — 写终止 messages + UpdateState
```

**A1 目标**（runner.go 改造）：

1. **删除 `_ = einoAgent` 短路**（line 389）
2. **接入真实 ReAct loop**：
   ```go
   // queryCtx 已含 traceID + abort ctx
   einoMessages := buildEinoMessages(req)  // (user 消息 + 历史 messages 转 schema.Message)
   output, err := einoAgent.Generate(queryCtx, einoMessages)
   if err != nil {
       // 触发 PTL chain / MaxOutput chain（来自 #9 compact）
       if shouldRetryCompact, retryMsgs := r.tryPreLLMCompact(ctx, run.ID, ...); shouldRetryCompact {
           output, err = einoAgent.Generate(queryCtx, retryMsgs)
       }
       // ... 错误派发到 TerminalReason
   }
   // 写 turn 到 agent_run.messages
   ```
3. **状态机派发**：根据 einoAgent.Generate 错误 / FinishReason / hook actions 派发到 19 个 `LoopEvent` + `TerminalReason`（已 #2/#6/#9 锁定）
4. **集成 #6/#9 helpers**：`r.handlePTLError` / `r.handleMaxOutputError` / `r.tryPreLLMCompact`（#9 已就绪，#14 接通）
5. **taskID 当前已是** `fmt.Sprintf("agent-runner-%d", run.ID)`（line 364）→ 这个字符串**未在 task_profile 表注册** → **见 §11 task_profile 注册**

**adapter.go 改造（轻量）**：
- 验证 `convertToAiserviceRequest` 把 tool schemas 正确转 `aiservice.Tool`（之前可能漏）
- **Usage 透出**：Generate 返回前把 `resp.Usage` 通过新 context helper 注入（A8 数据流用）—— 见 §2.A8 详细

**B1 → adapter 不动**：`fullToolEinoAdapter` 已实装 PreToolCall/PostToolCall hooks，本 feature 不改

**Langfuse trace**：aiservice gateway middleware 已自动 `CreateGeneration`，adapter 不再手写

### A2 Memory Embedder

**现状**：`memory.NewMockEmbedder()` 返回 1024 维零向量

**目标**：

```go
type aiserviceEmbedder struct{ runID uint64 }
func (e *aiserviceEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
    resp, err := aiservice.Embed(ctx, fmt.Sprintf("agent_run_%d_embed", e.runID), aiservice.EmbedRequest{
        Texts:     texts,
        Dimension: 1024,  // 对齐 doubao-embedding-vision / text-embedding-v4 dim
    })
    if err != nil { return nil, fmt.Errorf("aiservice.Embed: %w", err) }
    return resp.Embeddings, nil
}
```

**新增 constructor**：`memory.NewAIServiceEmbedder() Embedder`（包级别函数；runID 可空 0）
**修改 NewRetriever**：加 `RetrieverOption WithEmbedder(Embedder)`；保留 mockEmbedder default 用于 单测

**Wire 位置**：`biz.go` `NewBiz` 注入 `memory.NewRetriever(memory.WithEmbedder(memory.NewAIServiceEmbedder()))`

### A3 Memory SyncTurn

**现状**：`compositeProvider.SyncTurn(ctx, ...) error { return nil }` stub

**目标**：每个 turn 结束后 LLM 提取 "新增 fact/preference" 异步写 L1。

**技术决策**：

1. **新增 prompt** in `biz/memory/sync_prompt.go`：
   ```
   你是一个对话观察员。读以下用户/助手对话，提取 0-3 条**只对未来对话有用**的"事实"或"偏好"。
   - 事实：用户主动声明的稳定信息（如"我在做销售"/"我的客户是 B2B SaaS"）
   - 偏好：用户表达的风格喜好（如"我喜欢看图表不喜欢看长文字"）
   - 不提取：临时问题、一次性请求、闲聊
   - 输出 JSON: {"items": [{"kind": "fact|preference", "content": "<≤80字>", "confidence": 0.0-1.0}]}
   ```
2. **流程**：
   - runner.go Run() 末尾（成功 final answer 后）调 `provider.SyncTurn(ctx, userID, agentDefID, sessionID, userMsg, assistantMsg)`
   - SyncTurn 内异步 `go func()` 调 aiservice.Chat with ResponseFormat=JSONObject
   - 解析 JSON → 写 store.AgentSessionMemoryStore（带 fence 转义 — 来自 #7）
   - 失败 / 解析失败 / 0 items → silently skip（不污染 memory）
3. **runner.go ctx**：S0-D9 决议 — runner.go step [4a] 后注入：
   ```go
   ctx = context.WithValue(ctx, middleware.CtxKeySessionID, sessionID)
   ```
   `CtxKeySessionID` 已在 #7 留位（未真正定义），#14 加 `internal/pkg/middleware/agent_session_ctx.go` 定义并 export

### A4 Compact Provider

**现状**：`biz.go: agent.WithCompactProvider(&compact.MockCompactProvider{PlaceholderSummary: "..."})`

**目标**：

```go
type aiserviceCompactProvider struct{ cfg compact.Config }
func NewAIServiceCompactProvider(cfg compact.Config) compact.CompactProvider {
    return &aiserviceCompactProvider{cfg: cfg}
}
func (p *aiserviceCompactProvider) Compact(ctx context.Context, req *compact.CompactRequest) (*compact.CompactResult, error) {
    systemPrompt := compact.BuildCompactSystemPrompt(req.Mode)  // 9-section BASE_COMPACT_PROMPT (蓝本 §4.8.2)
    msgs := []aiservice.ChatMessage{
        {Role: "system", Content: systemPrompt},
        {Role: "user", Content: compact.SerializeMessagesForCompact(req.Messages)},
    }
    resp, err := aiservice.Chat(ctx, fmt.Sprintf("compact_%d", req.RunID), aiservice.ChatRequest{
        Messages:      msgs,
        ModelOverride: p.cfg.CompactModel,  // default "qwen-plus" (cfg.Compact.Model)
        MaxTokens:     p.cfg.MaxSummaryTokens,
        Temperature:   0.0,  // deterministic compact
    })
    if err != nil { return nil, fmt.Errorf("compact.aiservice.Chat: %w", err) }
    return &compact.CompactResult{
        Summary:      resp.Content,
        InputTokens:  resp.Usage.PromptTokens,
        OutputTokens: resp.Usage.CompletionTokens,
    }, nil
}
```

**Wire**：`biz.go` 替换 `&compact.MockCompactProvider{...}` 为 `compact.NewAIServiceCompactProvider(compact.DefaultConfig())`

**保留 MockCompactProvider**：仍用于 ptl_chain 单测

### A5 Narration LLM Fallback

> **S1 reviewer P1-2 修正（critical）**：原描述用了 `Generate(ctx, ev *narration.Event)` 签名，但实际 interface 在 `translator.go:12-14` 是：
> ```go
> type LLMFallback interface {
>     Render(ctx context.Context, toolName string, state State, payload EmitPayload) (verb, detail string)
> }
> ```
> 返回 `(verb, detail string)` 不返回 error（S1-D12 早期决议 —— LLMFallback 内部吞 error 返回 safe default）。

**现状**：`biz/narration/translator.go` 有 `stubLLMFallback` 返回硬编码中文文案（"正在执行" / "完成" / "执行出错" 等）

**目标**：动态生成中文 narration（YAML miss + dynamic_fallback 配置时）

**实现**：

```go
type aiserviceLLMFallback struct {
    cache *sync.Map  // (toolName + ":" + state) → string（thread-safe，多 Run 并发安全）
}

func NewAIServiceLLMFallback() narration.LLMFallback {
    return &aiserviceLLMFallback{}
}

// Render 符合 narration.LLMFallback 接口签名
func (f *aiserviceLLMFallback) Render(ctx context.Context, toolName string, state narration.State, payload narration.EmitPayload) (verb, detail string) {
    cacheKey := toolName + ":" + string(state)
    if v, ok := f.cache.Load(cacheKey); ok {
        cached := v.([2]string)
        return cached[0], cached[1]
    }
    // 200ms 兜底超时 — 内部吞 error 返回 stub fallback
    timeoutCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
    defer cancel()
    resp, err := aiservice.Chat(timeoutCtx, profile.AgentNarrationFallback, aiservice.ChatRequest{
        Messages: []aiservice.ChatMessage{
            {Role: aiservice.MessageRoleSystem, Content: aiservice.MessageContent{Text: NarrationFallbackSystemPrompt}},
            {Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: fmt.Sprintf("工具：%s，状态：%s，细节：%s", toolName, state, payload.Detail)}},
        },
        ModelOverride: "qwen-turbo",
        MaxTokens:     50,
        Temperature:   0.3,
    })
    if err != nil {
        return stubFallbackFor(toolName, state)  // **timeout fail-allow** — UX 优先（与 A6 注入检测 fail-deny 刻意不同 — S0-D12）
    }
    // 解析返回 "动词|细节" 格式（如 "查询|知识库"）
    verb, detail = parseNarrationContent(resp.Content, toolName)
    f.cache.Store(cacheKey, [2]string{verb, detail})
    return verb, detail
}
```

**关键约束**：
- 使用 `sync.Map`（reviewer P2-6 修复）解决多 Run 并发竞争 — 不依赖 narration.LRUCache 内部 mutex
- 返回 `(string, string)` 不返回 error（接口要求）
- 内部超时 + fallback 由 stub 实现兜底

**Wire**：`biz.go` 注入到 `narration.NewTranslator(renderer, NewAIServiceLLMFallback())`

### A6 Injection Classifier — fail-deny 方向

**现状**：`biz/compliance/injection_detector.go` keyword 22 个 + mock LLM classifier 永远 false

**目标**：keyword miss 时调 qwen-turbo classifier（**fail-deny on timeout**）

**实现**：

```go
type aiserviceInjectionClassifier struct{ /* nothing */ }
func NewAIServiceInjectionClassifier() compliance.InjectionClassifier { ... }
func (c *aiserviceInjectionClassifier) Classify(ctx context.Context, input string) (compliance.InjectionDecision, error) {
    timeoutCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
    defer cancel()
    resp, err := aiservice.Chat(timeoutCtx, "compliance_injection", aiservice.ChatRequest{
        Messages: []aiservice.ChatMessage{
            {Role: "system", Content: InjectionClassifyPrompt},  // 输出 yes/no
            {Role: "user", Content: input},
        },
        ModelOverride: "qwen-turbo",
        MaxTokens: 5,
        Temperature: 0,
    })
    if err != nil || timeoutCtx.Err() != nil {
        // **fail-deny** — 注入检测安全优先
        log.Warnw("injection classifier timeout — fail-deny", "input_prefix", truncate(input, 50))
        return compliance.InjectionDecision{IsInjection: true, Reason: "classifier_unavailable"}, nil
    }
    return compliance.InjectionDecision{
        IsInjection: strings.HasPrefix(strings.TrimSpace(resp.Content), "yes"),
        Reason:      "llm_classified",
    }, nil
}
```

**Wire**：`biz.go` 注入到 `compliance.NewInjectionDetector(NewAIServiceInjectionClassifier())`

### A7 Permission L3 Auto-Mode Classifier — fail-allow 方向

> **方向刻意与 A6 相反**（来自 S0-D12 决议）

**现状**：mock returns "auto" permission classifier；L3 auto-mode validator stub

**目标**：用 qwen-turbo 判断工具调用 args 是否需要明确学员确认

**实现**：

```go
type aiservicePermissionClassifier struct{ /* nothing */ }
func (c *aiservicePermissionClassifier) Classify(ctx context.Context, toolName, args string) (permission.AutoModeDecision, error) {
    timeoutCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
    defer cancel()
    resp, err := aiservice.Chat(timeoutCtx, "permission_automode", ...)
    if err != nil || timeoutCtx.Err() != nil {
        // **fail-allow + warn** — UX 优先
        log.Warnw("permission classifier timeout — fail-allow", "tool", toolName)
        return permission.AutoModeDecision{NeedsConfirm: false, Reason: "classifier_unavailable"}, nil
    }
    return permission.AutoModeDecision{
        NeedsConfirm: strings.HasPrefix(strings.TrimSpace(resp.Content), "confirm"),
        Reason:       "llm_classified",
    }, nil
}
```

### A8 PostToolCall Tokens ctx 数据流

**现状**：BudgetTracker.RecordUsage 期待从 ctx 拿 Usage，但 aiservice adapter 没注入

**目标**：A1 adapter Generate() 调 aiservice.Chat 后 → 把 Usage 注入 ctx → PostToolCall hook 读出来调 BudgetTracker.RecordUsage

**实现**（在 A1 内已合并 — 见 §2.A1 step 3）：

```go
// 在 adapter.Generate 后：
ctx = budget.WithUsage(ctx, budget.Usage{
    Prompt: resp.Usage.PromptTokens,
    Completion: resp.Usage.CompletionTokens,
})
// 在 BudgetGate.WrapHooks PostToolCall 内：
if usage, ok := budget.UsageFromCtx(ctx); ok {
    tracker.RecordUsage(ctx, usage.Prompt, usage.Completion)
}
```

**需新增**：`internal/numind/biz/budget/usage_ctx.go`（WithUsage / UsageFromCtx）

### A10 (NEW) Task Profile 注册（S1 reviewer P2-1 升级到 A 级）

> **S1 reviewer P2-1 + P2-3 处置**：现有 `runner.go:364` 用 `fmt.Sprintf("agent-runner-%d", run.ID)` 作 taskID，**未注册到 `profile/constants.go`**。`aiservice.ResolveTask` 走 task_profile DB lookup，未注册 → "route not found" → A1-A9 所有真实 LLM 调用全 fail。

**新增 6 个 task profile constants**（`internal/pkg/aiservice/profile/constants.go`）：

| Constant | 字符串值 | 用途 | LLM model（dev default） |
|----------|---------|------|------------------------|
| `AgentRun` | `agent.run` | A1 主 ReAct LLM 调用 | qwen-turbo |
| `AgentEmbed` | `agent.embed` | A2 Memory embedding | text-embedding-v4（dim=1024）|
| `AgentSyncTurn` | `agent.sync_turn` | A3 Memory turn 摘要提取 | qwen-turbo |
| `AgentCompact` | `agent.compact` | A4 Context 压缩 | qwen-plus |
| `AgentNarrationFallback` | `agent.narration_fallback` | A5 narration 动态生成 | qwen-turbo |
| `AgentInjectionCheck` | `agent.injection_check` | A6 注入检测 | qwen-turbo |
| `AgentPermissionCheck` | `agent.permission_check` | A7 L3 auto-mode 分类 | qwen-turbo |

**修改**：
1. `profile/constants.go` 新增 7 个常量（**注**：A2 是 Embed 不是 Chat，但 task profile 概念统一）
2. `allTaskIDsList` 加 7 个 entry
3. `AllTaskIDs()` 数量从 14 → 21
4. **Migration**：新增 `migrations/20260521_XX0000_agent_task_profiles_seed.sql` —— INSERT 7 行到 `task_profile` 表（+ rollback）

**`runner.go:364` 修改**：
```go
taskID: profile.AgentRun,  // 替代 fmt.Sprintf("agent-runner-%d", run.ID)
// 注：原 taskID 在 trace metadata 中带 run.ID 是有意义的，但 task_profile 是路由 key 不是 trace label
// trace 关联通过 langfuse.WithTrace(ctx, traceID) 注入，taskID 仅用于 aiservice 路由表查询
```

**Wire**：A1/A2/A4/A5/A6/A7 各自 call site 使用对应 constant

### A9 Log-based Observability

**目标**（来自 S0-D6 决议）：log-based 替代 Prometheus 接入

| 埋点 | 文件 | 字段 |
|------|------|------|
| AuditLogger drop count 阈值告警 | `biz/compliance/audit_logger.go` | `log.Warnw("audit drop count exceeded", "drop_count", N, "threshold", T)` |
| LLM 拒答率 | runner.go final answer 后 | `log.Infow("agent_run_completed", "run_id", id, "refusal", isRefusal, "terminal_reason", reason)` |
| Compliance hit | `biz/compliance/gate.go CheckLLMOutput` deny | `log.Infow("compliance_hit", "rule_type", t, "rule_id", id)` |

**Filebeat / Loki 抓取**：Phase E E3 runbook 写入

---

## §3 Phase B 技术方案 — 8 个 Playwright e2e

### B1 → B2 状态共享（S0-D8 决议）

**决策**：用 D1 阶段**SQL seed 固定 test agent**（`name="E2E Test Assistant"`, `parent_user_id=$E2E_PARENT_USER_ID`），ID 在 dev DB 固定。

- **B1 spec**（admin-web）创建一个**新临时 Agent**（验证创建路径），命名带时间戳避免冲突，e2e 结尾 SoftDelete
- **B2-B8 spec**（web-v3）用 seed Agent ID（从 `e2e/fixtures/test-agent-id.json` 读 hardcoded ID）

**新增 D1 步骤 D1.1**：跑 `migrations/seed_e2e_test_agent.sql`（仅 dev 环境，prod 不跑）

### B1-B8 spec 文件

| Spec | 仓库 | 涉及 |
|------|------|------|
| B1 `admin-create-agent.spec.ts` | admin-web | TemplateGallery → AgentBuilder → 保存 → 试聊 modal 跳过 |
| B2 `student-dialog-happy.spec.ts` | web-v3 | 选 agent → 发消息 → narration → tool call → final answer |
| B3 `student-permission-deny.spec.ts` | web-v3 | 触发 bash rm → terminal_reason=permission_denied → narration 拒绝 |
| B4 `student-budget-exceed.spec.ts` | web-v3 | 累计 token > MaxCredits → terminal_reason=error_max_budget |
| B5 `student-compliance-block.spec.ts` | web-v3 | 输入"竞品X" → 友好拒绝（Q11 话术）|
| B6 `student-compact-trigger.spec.ts` | web-v3 | 长对话 → PTL chain → CompactSummary 写入 |
| B7 `student-session-resume.spec.ts` | web-v3 | sessionStorage clear + reload → 读 compact_summary → 续聊 |
| B8 `admin-history-rollback.spec.ts` | admin-web | v3 → 回滚 v1 → 创建 v4 |

### 网络层 mock 策略

| 类型 | 策略 |
|------|------|
| LLM 调用 | **走真实 dev qwen-turbo**（aiservice routing dev key）— **不 mock**（Phase A 才是 e2e 真实切换的目的）|
| Auth | 真实登录（$E2E_USERNAME / $E2E_PASSWORD），cookie 复用 |
| Backend | 真实 dev backend（部署完 D2 后）|

### Playwright 配置

- admin-web `playwright.config.ts`: `baseURL=http://49.233.219.254:9100`（dev）或 `http://localhost:5174`（local）
- web-v3 `playwright.config.ts`: `baseURL=http://49.233.219.254:9200` 或 `http://localhost:5173`
- E2E_BASE_URL env var 切换 local vs dev

---

## §4 Phase C 技术方案 — Admin-web UI 补全

### C1 compliance_rule CRUD UI

**新增路由**：`/admin/compliance-rules`（在 AdminSidebar "AI 助手" 下加菜单项 "合规规则"）

**前端**：
- `views/compliance/ComplianceRuleList.vue` —— DataTable (id / parent_user_id / rule_type / pattern / 状态 / 操作)
- `views/compliance/ComplianceRuleForm.vue` —— 创建/编辑表单（rule_type 4 选项 RadioGroup / pattern Textarea / is_active Switch）
- Pinia store `src/stores/complianceRule.ts`
- API wrapper `src/api/complianceRule.ts`

**后端新增 5 endpoints**（admin_router.go 注册；biz 层复用 #13 store + cache 双 invalidate）：
- `GET /v1/admin/compliance-rules?page=&page_size=&parent_user_id=&rule_type=`
- `POST /v1/admin/compliance-rules`
- `GET /v1/admin/compliance-rules/:id`
- `PATCH /v1/admin/compliance-rules/:id`
- `DELETE /v1/admin/compliance-rules/:id`

**Controller**：`controller/v1/admin/compliance_rule.go`（**零业务逻辑**，仅 BindJSON + 调 biz）

**关键**：写入后 **cache invalidate** — biz/compliance/cache.go `Invalidate(parentUserID)`

### C2 Langfuse Trace 跳转

**前端**：AgentMonitoring 表格 / agent-run 详情页加 [查看 Trace] 链接：
```vue
<a :href="`${langfuseBaseURL}/trace/${run.trace_id}`" target="_blank">查看 Trace</a>
```
`langfuseBaseURL` 从 `import.meta.env.VITE_LANGFUSE_URL` 读，配置在 `.env.development` / `.env.production`

**注意**：`agent_run.trace_id` 字段 #2 已有；如果 #2 没写值（mock 阶段）则前端 fallback 显示 "Trace 未生成"

### C3 Agent_run 强制取消

**后端新增 endpoint**：`POST /v1/admin/agent-runs/:id/cancel`
- biz 层：`biz/agent/admin_cancel.go: CancelByAdmin(ctx, runID) error`
- 调 `AbortController.CancelAll(runID, "admin_cancel")` + `store.UpdateRunStatus(runID, status=cancelled, terminal_reason=admin_cancel)`
- **新 terminal_reason `admin_cancel`?** —— **不引入**（违反 I2）；用现有 `cancelled` + terminal_metadata 字段记录 admin 操作

**前端**：AgentMonitoring 列表 action 加 [强制取消] 按钮 + ConfirmModal "确认强制取消该会话？"

**Controller**：`controller/v1/admin/agent_run.go`

### C4 监控真实数据源

**后端新增 endpoint**：`GET /v1/admin/agent-runs?status=running&page=&page_size=`
- biz 层：`biz/agent/admin_query.go: ListByStatus(ctx, parentUserID, status, offset, limit) ([]AgentRunDTO, int64, error)`
- store 层 `agent_run_store.go` 加 `ListByParentUserIDAndStatus` method（**注意**：需 join `agent_definition.parent_user_id` 过滤；scope_validator hook 已自动加 WHERE，但显式 join 更稳）

**前端**：替换 `AgentMonitoring.vue` 的 hardcoded 空数组 fetcher 为真实 GET 调用 + 30s 轮询

### C5 NoticeBanner 移除

`AgentMonitoring.vue` 删除 NoticeBanner 组件实例 + import + 测试快照更新

---

## §5 Phase D — Dev 部署

### D0 Same-timestamp migration 排序（S1 reviewer P2-5）

> 验证 `ls migrations/` 后发现至少 3 个文件同 timestamp `20260521_120000`（permission_pipeline / compliance_3layer / create_agent_session_memory），D1 SSH 跑时**必须明示文件名顺序**：

**Phase D 跑 migration 时按字母序**（mysql `<` 不保证 OS readdir 顺序）：
```bash
# Same-timestamp 同 batch — 显式列出按字母序：
20260521_120000_agent_mode_compliance_3layer.sql  # → 字母 'co' 排在 'pe' 前
20260521_120000_agent_permission_pipeline.sql
20260521_120000_create_agent_session_memory.sql
```

由于这 3 个 migration **DDL 互独立**（不同表名），字母序 ≡ 任何顺序都 OK。S2 spec D1 表格显式列出。

### D1 Migration 顺序 + 验证

**13 features migration SQL 顺序**（按 timestamp）：

| # | Feature | Migration 文件 |
|---|---------|--------------|
| 1 | phase0 | — (no schema) |
| 2 | runtime-skeleton | `20260520_*_agent_mode_runtime_skeleton.sql` |
| 3 | tool-registry | `20260520_*_agent_mode_tool_registry.sql` |
| 4 | sandbox-integration | `20260521_*_agent_sandbox_session.sql` |
| 5 | skill-system | `20260521_*_agent_mode_skill_system.sql` (3 tables) |
| 6 | permission-pipeline | `20260521_120000_agent_permission_pipeline.sql` (2 tables) |
| 7 | memory-system | `20260521_*_agent_memory_system.sql` (2 tables) |
| 8 | narration | — (no schema) |
| 9 | compact | `20260521_*_agent_mode_compact.sql` (ALTER agent_run) |
| 10 | configurator-ux | — (no schema) |
| 11 | student-ux | — (no schema) |
| 12 | billing-integration | `20260521_140000`, `_140100`, `_140200` (3 SQL) |
| 13 | compliance-3layer | `20260521_120000_agent_mode_compliance_3layer.sql` (2 tables) |
| 14 | e2e-rollout (本) | `2026052X_*_agent_e2e_rollout.sql` (新增的 schema 来自 A3/C1/C3-C4) |

**逐个 migration 跑前验证**：`SELECT 1 FROM information_schema.tables WHERE table_name='<expected>'` 决定 skip / apply

**SSH 命令模板**：
```bash
sshpass -p "$DEV_SSH_PASS" ssh -o StrictHostKeyChecking=no "$DEV_SSH_USER@$DEV_SSH_HOST" \
  "mysql -u <db_user> -p<db_pass> <db_name> < /tmp/migration.sql"
```

### D2 后端部署

```bash
cd /private/tmp/wt-agent-mode-e2e-rollout-numind-server
/deploy-dev server   # 9091
/deploy-dev admin    # 9099
```

### D3 前端部署

```bash
cd /private/tmp/wt-agent-mode-e2e-rollout-numind-admin-web
/deploy-dev          # 9100

cd /private/tmp/wt-agent-mode-e2e-rollout-numind-web-v3
/deploy-dev          # 9200
```

### D4 Smoke test

跑 8 e2e 在 dev：
```bash
cd /private/tmp/wt-agent-mode-e2e-rollout-numind-admin-web
BASE_URL=http://49.233.219.254:9100 \
  E2E_USERNAME=$E2E_USERNAME E2E_PASSWORD=$E2E_PASSWORD \
  npm run test:e2e -- e2e/admin-create-agent.spec.ts e2e/admin-history-rollback.spec.ts

cd /private/tmp/wt-agent-mode-e2e-rollout-numind-web-v3
BASE_URL=http://49.233.219.254:9200 \
  E2E_STUDENT_USERNAME=$E2E_STUDENT_USERNAME E2E_STUDENT_PASSWORD=$E2E_STUDENT_PASSWORD \
  npm run test:e2e -- e2e/student-*.spec.ts
```

---

## §6 Phase E — Prod 准备文档

### E1 deploy-checklist-feature-14.md 结构

```markdown
# Deploy Checklist · Feature #14 + 14-feature combined

## Pre-deploy
1. backup prod DB
2. drain in-flight agent_run（如 prod 已有学员对话）
3. tag: git tag v_X.Y.Z

## Migration 顺序（按 timestamp）
| # | SQL | Rollback | 验证 SQL |
| 1 | ... | ... | SELECT COUNT(*) FROM ... |

## Deploy
4. /deploy-prod server (需 tag v*)
5. /deploy-prod admin

## Post-deploy verify
6. /healthz
7. Langfuse trace check
8. ...

## Rollback
- 按 migration 倒序 rollback SQL
```

### E2 config-prod-diff.md

列出 config_prod.yaml 应增加的字段（不真改）：
- `langfuse.prod_secret_key`
- `aiservice.routes` 新增 agent_run task profile
- ...

### E3 runbook.md

oncall 手册：强制取消 / 查 audit / 升降阈值 / Sandbox iptables 配置（来自 S0-D2）/ L1 TTL cron 配置（来自 S0-D5）

### E4 architecture-v1.md — 加 §16 v1.0 Landing Record

不覆盖 §11（来自 S0-D13）。

### E5-E8 见 S0 §"Phase E"

---

## §7 API 合约清单

### 新增（Phase C）

| Method | Path | Auth | 说明 |
|--------|------|------|------|
| GET | `/v1/admin/compliance-rules` | admin_token | List + filter |
| POST | `/v1/admin/compliance-rules` | admin_token | Create |
| GET | `/v1/admin/compliance-rules/:id` | admin_token | Get one |
| PATCH | `/v1/admin/compliance-rules/:id` | admin_token | Update |
| DELETE | `/v1/admin/compliance-rules/:id` | admin_token | Delete |
| POST | `/v1/admin/agent-runs/:id/cancel` | admin_token | 强制取消 |
| GET | `/v1/admin/agent-runs` | admin_token | List by status |

### 修改（Phase A）

- `aiservice.Chat` / `Embed` 调用增加（无新接口，使用既有）

### 数据库 schema 变更

| 表 | 变更 | 用途 |
|----|------|-----|
| 无新增表 | — | — |
| `agent_run` 加 `terminal_metadata` 字段 | 已 #12 落地 | 不需 #14 改 |
| `agent_run` 加 `cancellation_requested_at` 字段 | **#14 新增 ALTER**（C3 admin cancel 用）| — |
| 无 `agent_session_memory` schema 变更 | A3 SyncTurn 用现有 schema | — |
| 无 `compliance_rule` schema 变更 | C1 复用 #13 schema | — |

---

## §8 测试策略

| 层 | 工具 | 范围 |
|----|------|------|
| Phase A 单测 | Go testing + in-memory SQLite + mock aiservice | A1-A9 各 case |
| Phase A 集成测 | Go testing + dev aiservice (real qwen-turbo) | runner_e2e_test.go 跑 ReAct 5 step |
| Phase B e2e | Playwright + real dev backend | 8 spec |
| Phase C admin endpoints | Go controller test | 7 endpoints CRUD + auth |
| Phase D smoke | Manual + Playwright in dev | 8 e2e + healthz |
| Phase E doc | Reviewer subagent | reviewer PASS |

---

## §9 风险与对策

| 风险 | S1 加固 |
|------|--------|
| dev qwen-turbo quota 用尽 | Phase B 8 e2e 单线程顺序跑（不并发）；总 LLM 调用 < 50 次 |
| Langfuse trace 树不完整 | adapter.Generate 内不再手写 generation — 信任 aiservice middleware；写一个集成测验证 trace 树 |
| compact 真实摘要质量差导致 B7 失败 | A4 实施时单测验证 qwen-plus 摘要保留关键信息；B7 spec 设计为"最近一个用户提问能否被正确响应" 而非"全细节复现" |
| dev migration 跑挂 | D1 跑前 backup dev DB；逐个 migration `SELECT ... FROM information_schema` 验证 |
| admin endpoints scope 漏 | 复用 #13 scope_validator GORM Before-Query hook（已 fail-open）；admin 端 ctx WithSkipScope("admin") 显式标识 |
| 3 仓库 merge 冲突 | S6 顺序：server (API) → admin-web → web-v3；develop 各仓库 pull rebase 前 |

---

## §10 假设清单（S1 → S2 转换前 verify）

- [ ] aiservice 已配置 dev qwen-turbo 路由（默认 task profile 可调）
- [ ] aiservice 已配置 dev embedding 路由（doubao-embedding 或 text-embedding-v4）
- [ ] dev MySQL 有完整 13 features schema（如无，D1 跑齐）
- [ ] Langfuse dev 凭据已配
- [ ] 父账户 + 子账户 e2e 凭据已配（**Phase B 前 BLOCKED_NEEDS_USER if missing**）

---

## §11 S1 reviewer 反馈处置

| Decision | 来源 | 处置 |
|---------|------|------|
| S1-D1 | reviewer P1-1 | **A1 文件归属修正**：runner.go line 389 是真实工作位置；adapter.go Generate 已实装；adapter_full_to_eino.go 不动 |
| S1-D2 | reviewer P1-2 | **A5 LLMFallback 接口修正**：`Render(ctx, toolName, state, payload) (verb, detail string)`，不返回 error |
| S1-D3 | reviewer P2-1 + P2-3 | **A10 task profile 注册**：7 个新 constants + seed migration |
| S1-D4 | reviewer P2-2 | **A8 budget.WithUsage 包路径选定**：放在 `internal/numind/biz/agent/budgetctx/usage_ctx.go`（新 budgetctx 子包，无 import cycle —— biz/budget 与 biz/agent 已通过 budgetgate 单向依赖，新子包在 agent 下不破坏）|
| S1-D5 | reviewer P2-4 | **A6 InjectionClassifier 接口决策**：v1 用新 interface `compliance.LLMClassifier` (已存在 — 返回 `(bool, error)`)；不引入新 `InjectionDecision` struct；S2 spec 修正 |
| S1-D6 | reviewer P2-5 | **D0 same-timestamp migration 排序**：3 个同 timestamp 文件字母序跑 |
| S1-D7 | reviewer P2-6 | **A5 LRU cache thread-safety**：使用 `sync.Map` 替代 `narration.LRUCache`（不依赖内部 mutex 实装） |

---

## §12 状态

**S1 完结。0 P0 + 0 P1 残留。7 项 S1-Dx 决策入 §11。Ready for S2 spec。**
