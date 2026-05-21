# agent-mode-p0-tools — Implementation Plan (S3)

> Stage: S3 · Track: standard · Repos: numind-server + numind-web-v3 · Date: 2026-05-22
> Predecessor: spec `docs/superpowers/specs/2026-05-22-agent-mode-p0-tools-design.md` (ca9d9200)

---

## §0 Plan 总览

9 个原子 task，按依赖与文件归属编排。

```
┌──────────────────────────────────────────────────────────────┐
│  Wave 1 (Tier 3 同仓库 disjoint files 并行 4 task)            │
│  ┌─────┬─────┬─────┬─────┐                                   │
│  │ T1  │ T2  │ T3  │ T5  │  (numind-server)                  │
│  └─────┴─────┴─────┴─────┘                                   │
└──────────────────────────────────────────────────────────────┘
                          │
                          ▼
┌──────────────────────────────────────────────────────────────┐
│  Wave 2 (serial, depends on T3 yield protocol)               │
│  ┌─────┐                                                      │
│  │ T4  │  ask_user_question tool + biz + endpoint            │
│  └─────┘                                                      │
└──────────────────────────────────────────────────────────────┘
                          │
                          ▼
┌──────────────────────────────────────────────────────────────┐
│  Wave 3 (Tier 2 跨仓库，depends on T4 API contract)            │
│  ┌─────┐                                                      │
│  │ T6  │  Frontend QuestionPrompt (numind-web-v3)            │
│  └─────┘                                                      │
└──────────────────────────────────────────────────────────────┘
                          │
                          ▼
┌──────────────────────────────────────────────────────────────┐
│  Wave 4 (serial, depends on T1+T2+T4+T5)                     │
│  ┌─────┐                                                      │
│  │ T7  │  Registry + biz.go wiring + migrations              │
│  └─────┘                                                      │
└──────────────────────────────────────────────────────────────┘
                          │
                          ▼
┌──────────────────────────────────────────────────────────────┐
│  Wave 5 (serial, final docs + validation strategy)            │
│  ┌─────┬─────┐                                                │
│  │ T8  │ T9  │  Docs update + S5 strategy                    │
│  └─────┴─────┘                                                │
└──────────────────────────────────────────────────────────────┘
```

总 task 数：**9**。预期 reviewed_tasks：**9**（每个 task 完成后并行双 reviewer，0 P0/P1 才进下个）。

---

## §1 Wave 1 — 4 个并行 task (Tier 3 同仓库 disjoint files)

### T1: web_search backend + aiservice wrapper + Tavily + cache

**Owner**: 1 agent in numind-server worktree (sub-worktree `/private/tmp/wt-agent-mode-p0-tools-numind-server-T1`)
**Goal**: 实现 `web_search` 工具的完整后端 + Tavily provider 集成 + in-memory cache + Langfuse Span
**Files (created/modified)**:
- `internal/numind/biz/agent/tool_web_search.go` (新)
- `internal/numind/biz/agent/tool_web_search_test.go` (新)
- `internal/pkg/aiservice/web_search.go` (新)
- `internal/pkg/aiservice/web_search_test.go` (新)
- `config_local.yaml` 加 `web_search:` 段
- `config_dev.yaml` 加 `web_search:` 段
- `config_qa.yaml` 加 `web_search:` 段
- **NOT touched**: `config_prod.yaml`（硬规则）

**实现内容**：
1. `webSearchTool` struct embed BaseTool + 5 必需方法 + cache + config 引用
2. `aiservice.WebSearch(ctx, req)`：
   - cache check (key = `md5(query + max_results + sorted_domains)`)
   - 路由到 `tavilyProvider`（v1 只接 Tavily）
   - Langfuse Span `tool.web_search.execute` with metadata
   - 5s timeout
3. `tavilyProvider`：HTTP POST https://api.tavily.com/search，JSON body `{api_key, query, max_results, include_domains}`，response parse 为 `[]WebSearchResult`
4. in-memory cache: map[string]searchCacheEntry，TTL 5 分钟，crude 1k 上限

**测试** (`tool_web_search_test.go`)：
- happy path: 模拟 Tavily response → 验证 ToolResult 含 results / cache_hit=false
- cache hit: 同 query 第二次调 → cache_hit=true，未触发 HTTP
- input 校验: query="" → ErrInvalidInput；max_results=11 → 拒绝
- provider 429: 模拟 Tavily 限流 → ErrExternalAPI
- timeout: provider 模拟挂 6s → ErrTimeout

**验收条件**:
- `go test ./internal/numind/biz/agent/... -run TestWebSearch` PASS
- `go test ./internal/pkg/aiservice/... -run TestWebSearch` PASS
- `task lint` PASS

**预期 LOC**：~300 prod + ~250 test。

---

### T2: web_fetch backend + SSRF + html-to-markdown

**Owner**: 1 agent in numind-server worktree (sub-worktree `T2`)
**Goal**: 实现 `web_fetch` 工具完整后端 + SSRF 防护 + HTML→Markdown
**Files**:
- `internal/numind/biz/agent/tool_web_fetch.go` (新)
- `internal/numind/biz/agent/tool_web_fetch_test.go` (新)
- 可能：`internal/pkg/httpclient/safe_dial.go`（如 SSRF defense 独立成 helper）
- `go.mod` 增加 `github.com/JohannesKaufmann/html-to-markdown` 或类似（S4 阶段选定）

**实现内容**:
1. `webFetchTool` struct + 5 方法
2. `validateFetchURL()` 实现 spec §8.1 算法（scheme check + .local + DNS resolve + IP class + cloud metadata blocklist）
3. `safeHTTPClient()` 构造一个 `http.Client` with custom Dial 重新校验 IP（防 DNS rebinding）
4. fetch → 100KB limit → HTML→Markdown convert → 返回
5. Langfuse Span `tool.web_fetch.execute`

**测试**:
- happy path: 模拟 HTTP fixture（fixture server in test）→ 返回 markdown
- SSRF: `http://localhost` / `http://127.0.0.1` / `http://192.168.1.1` / `http://169.254.169.254/latest/meta-data/` / `http://foo.local` 全拒绝
- DNS rebinding: 用 `httptest.Server` + custom resolver 模拟 → Dial 阶段重 check 拦截
- 大 body: > 100KB → truncated=true
- timeout: 模拟 31s 响应 → ErrTimeout
- 非 http(s): `ftp://...` 拒
- 4xx / 5xx: 返回 ErrExternalAPI

**验收**: `go test ./internal/numind/biz/agent/... -run TestWebFetch` PASS + lint PASS

**预期 LOC**: ~400 prod + ~300 test。

---

### T3: State machine yield 协议 + runner.go yield handler

**Owner**: 1 agent in numind-server worktree (sub-worktree `T3`)
**Goal**: 改造 state.go + runner.go 引入 yield-turn 能力
**Files**:
- `internal/numind/biz/agent/state.go` (改：加 `TerminalWaitingForUserChoice` + `LoopEventAskUserPaused` + Transition() case + 编译期 invariant 长度更新)
- `internal/numind/biz/agent/state_test.go` (改：加 yield case 测试)
- `internal/numind/biz/agent/runner.go` (改：加 yieldError 识别 + 6 step yield handler)
- `internal/numind/biz/agent/runner_yield_test.go` (新：测试 yield handler 完整流程)
- `internal/numind/biz/agent/yield_error.go` (新：sentinel error + payload struct，独立 file 便于 mock)

**实现内容**：
1. state.go：
   - 加常量 `TerminalWaitingForUserChoice TerminalReason = "waiting_for_user_choice"`
   - 加事件 `LoopEventAskUserPaused LoopEvent = ...`（next available）
   - 更新 `[13]TerminalReason{...}` → `[14]TerminalReason{..., TerminalWaitingForUserChoice}`
   - 更新 `[19]LoopEvent{...}` → `[20]LoopEvent{..., LoopEventAskUserPaused}`
   - `Transition()` 加 `case LoopEventAskUserPaused: s.TerminalReason = TerminalWaitingForUserChoice; return TerminalWaitingForUserChoice, "", true`
2. yield_error.go：
   - 定义 `ErrYieldForUserQuestion` sentinel
   - 定义 `YieldPayload` struct（Question/Options/Header/MultiSelect）
   - 定义 `yieldError` struct（携带 Payload，实现 `Is(target)` 返 ErrYieldForUserQuestion match）
3. runner.go：
   - 在 tool exec 块后加 `errors.As(err, &yieldErr)` 检查
   - yield handler：序列化 payload → SetPendingQuestion(runID) → Langfuse Span → BudgetTracker.Pause() → Narration.EmitYield() → state.Transition(LoopEventAskUserPaused)
   - 不修改其他错误路径

**测试** (`state_test.go`)：
- `TestLoopState_Transition_AskUserPaused`: state.Transition(LoopEventAskUserPaused) → (TerminalWaitingForUserChoice, "", true) + s.IsTerminal()=true + s.StepCount 不变

**测试** (`runner_yield_test.go`)：
- 完整流程：mock tool 返回 yieldError → 验证 store.SetPendingQuestion 被调 + Span 写 + Pause 被调 + RunResult.TerminalReason=TerminalWaitingForUserChoice

**验收**：state_test + runner_yield_test PASS + lint PASS

**预期 LOC**: ~150 prod + ~250 test。

---

### T5: file_read backend + parsers + user_id check

**Owner**: 1 agent in numind-server worktree (sub-worktree `T5`)
**Goal**: 实现 `file_read` 工具的完整后端含 PDF/image/text 派发
**Files**:
- `internal/numind/biz/agent/tool_file_read.go` (新)
- `internal/numind/biz/agent/tool_file_read_test.go` (新)
- `internal/numind/biz/agent/file_read_parsers.go` (新，分离 PDF/image/text parser 函数)
- `internal/numind/biz/agent/file_read_parsers_test.go` (新)
- 可能：`internal/pkg/aiservice/file_parse.go` 或扩展现有 chat 接口

**实现内容**：
1. `fileReadTool` struct + 5 方法 + 依赖 `aiSvc` (qwen-long) + `aliOCR` client
2. `isAttachmentOfUser(fileURL, ctxUserID)`：parse URL path `agent-attachments/<userID>/...` → 比对
3. dispatch：
   - HEAD request 拿 mime + size（用 httpclient）
   - `application/pdf` → `parsePDF(ctx, url, prompt)` 走 qwen-long
   - `image/*` → `parseImage(ctx, url)` 走 ali OCR
   - `text/plain` / `text/markdown` → `parseText(ctx, url)` 直读
   - 其他 → ErrUnsupportedFileType
4. Langfuse Generation（PDF）/ Span（image, text）

**测试**：
- happy path PDF: mock qwen-long → 返回 content + page_count
- happy path image: mock ali OCR → 返回 OCR text
- happy path text: mock HTTP → 返回 string
- user_id mismatch: URL 含 `agent-attachments/999/...`, ctx user=123 → ErrPermissionDenied
- 不支持 mime: `application/zip` → ErrUnsupportedFileType
- 文件超 200KB: text content 被截断 + truncated=true
- HEAD 失败: ErrExternalAPI

**验收**: `go test ./internal/numind/biz/agent/... -run TestFileRead` PASS + lint PASS

**预期 LOC**: ~400 prod + ~250 test。

---

### Wave 1 并行执行规则（Tier 3）

主 session dispatch 前必须：

```bash
# 文件归属表
T1 拥有: 
  internal/numind/biz/agent/tool_web_search.go
  internal/numind/biz/agent/tool_web_search_test.go
  internal/pkg/aiservice/web_search.go
  internal/pkg/aiservice/web_search_test.go
  config_local.yaml
  config_dev.yaml
  config_qa.yaml

T2 拥有:
  internal/numind/biz/agent/tool_web_fetch.go
  internal/numind/biz/agent/tool_web_fetch_test.go
  internal/pkg/httpclient/safe_dial.go

T3 拥有:
  internal/numind/biz/agent/state.go
  internal/numind/biz/agent/state_test.go
  internal/numind/biz/agent/runner.go
  internal/numind/biz/agent/runner_yield_test.go
  internal/numind/biz/agent/yield_error.go

T5 拥有:
  internal/numind/biz/agent/tool_file_read.go
  internal/numind/biz/agent/tool_file_read_test.go
  internal/numind/biz/agent/file_read_parsers.go
  internal/numind/biz/agent/file_read_parsers_test.go

# 验证程序化无交集
ndf-check-disjoint "<T1 files>" "<T2 files>" "<T3 files>" "<T5 files>"
```

**Risk**: T3 改 runner.go，T5/T1/T2 不改但调用 runner 的相关行为。**T3 的 runner.go 改动只在 yield 路径，其他路径不变 → 其他 task 测试不受影响**。但 dispatch 后跑 merge 时如 runner.go 也被其他 task 改了 → 升 Tier 4 串行。

**Wave 1 结束 gate**:
- 4 个 sub-worktree 各自独立 commit + reviewer PASS
- 主 session 串行 merge 4 sub-worktree 回主 feature worktree
- 合并后 `go build ./...` 通过（编译，但功能未 wire 进 factory_platform）

---

## §2 Wave 2 — T4 ask_user_question backend + answer endpoint

**Depends on**: T3 done (yield protocol available)
**Owner**: 1 agent in feature worktree (主)
**Goal**: ask_user_question 工具 + answer endpoint + biz 层 AnswerPendingQuestion + store 操作
**Files**:
- `internal/numind/biz/agent/tool_ask_user_question.go` (新)
- `internal/numind/biz/agent/tool_ask_user_question_test.go` (新)
- `internal/numind/biz/agent/answer.go` (新，含 AnswerRequest / AnswerResponse + AnswerPendingQuestion biz)
- `internal/numind/biz/agent/answer_test.go` (新)
- `internal/numind/controller/v1/agent/student_run.go` (改：加 Answer handler 和路由注册)
- `internal/numind/controller/v1/agent/student_run_test.go` (改：加 Answer test)
- `internal/numind/store/agent_run.go` (改：加 SetPendingQuestion / ClearPendingQuestion / UpdateStateReason 方法)
- `internal/numind/store/agent_run_test.go` (改：加上述方法测试)
- 可能 `internal/pkg/model/agent.go`（如 PendingQuestionJSON 字段未 add）

**实现内容**：
1. `askUserQuestionTool`：input 校验 + return yieldError
2. `AnswerRequest{Selected []string; FreeText string}` + `AnswerResponse{RunID uint64; Status string}`
3. `AgentBiz.AnswerPendingQuestion(ctx, userID, runID, req)`：
   - Load agent_run，校验 user_id + state_reason
   - 构造 user message: `"[user answered]\nQuestion: ...\nSelected: [...]\nFree text: ..."`
   - store.AppendMessage(runID, "user", content)
   - store.ClearPendingQuestion(runID) + UpdateStateReason("running")
   - Langfuse Span `tool.ask_user_question.resume`
   - BudgetTracker.Resume(runID)
   - Goroutine 重启 runner.Run(ctx, RunRequest{UserID: userID, ExistingRunID: runID, ...还原其他字段...})
4. Controller: `POST /agent-runs/:id/answer` → `c.Answer` handler

**测试**：
- tool_ask_user_question_test: input 校验（options 数量 / question 空 / multiSelect 等）→ 返回 yieldError
- answer_test: 
  - happy path: 创建 mock waiting run → 调 AnswerPendingQuestion → 验证 message append + state_reason cleared + run restart goroutine 被调
  - 跨用户：用户 999 调 user 123 的 run → ErrPermissionDenied
  - 状态不对：state_reason="running" → Err（run not waiting）
  - run 不存在：404
- controller test: HTTP POST 测试 happy + 4xx + 5xx

**验收**: `go test ./internal/numind/biz/agent/...` + `go test ./internal/numind/controller/v1/agent/...` + `go test ./internal/numind/store/...` 全 PASS + lint PASS + manual smoke (curl) 通过

**预期 LOC**: ~600 prod + ~400 test。

---

## §3 Wave 3 — T6 frontend QuestionPrompt + AgentChatView 集成

**Depends on**: T4 (answer endpoint API exists in OpenAPI / curl-able)
**Owner**: 1 agent in numind-web-v3 worktree
**Goal**: 前端组件 + SSE handler + api client + e2e
**Files (numind-web-v3)**:
- `src/components/agent/QuestionPrompt.vue` (新，完整 SFC 见 spec §13.1)
- `src/components/agent/__tests__/QuestionPrompt.spec.ts` (新 vitest 单测)
- `src/views/agent/AgentChatView.vue` (改：监听 SSE tool_call_yield + run_resumed + 渲染 QuestionPrompt + disable 输入框 in waiting 态)
- `src/api/agent.ts` (改：加 postAgentAnswer 函数)
- `e2e/agent-ask-user-question.spec.ts` (新 Playwright e2e)

**实现内容**：
1. QuestionPrompt.vue：见 spec §13.1（含 single/multi select + free_text + submit）
2. AgentChatView.vue 改造：
   - 在 useAgentChat / useNarrationSSE composable（如已存在）增加 SSE event handler for `tool_call_yield` → 推到 messages 列表，类型为 'question_prompt'
   - chat message rendering：含 v-if message.type === 'question_prompt' 渲染 QuestionPrompt
   - SSE `run_resumed` → 隐藏对应 QuestionPrompt + enable input
   - input 框: `disabled` 当 `currentRun.state_reason === 'waiting_for_user_choice'`
3. api/agent.ts: `postAgentAnswer(runId, payload)` 包装

**测试**：
- vitest QuestionPrompt.spec.ts：
  - 渲染：传 question + 2 options → 渲染 2 个按钮
  - single-select: 点击立即 submit
  - multi-select + free_text: 选 2 个 + 输入文字 + 点提交
  - submitting state: 提交中按钮 disabled
  - error: api 返 4xx → 显示 toast，按钮恢复
- Playwright e2e:
  - 登录 → 进 agent chat → 发送特定消息触发 ask_user_question
  - 等 QuestionPrompt 渲染（最长 30s wait）
  - 截图 1
  - 点击 option button
  - 等 narration stream 继续（agent 回复出现）
  - 截图 2
  - 校验 chat history 含"[user answered]"

**验收**: 
- `npm run test:unit -- QuestionPrompt` PASS
- `npm run lint` + `npm run type-check` PASS
- e2e local 跑通（S5 阶段验证）

**预期 LOC**: ~250 prod + ~400 test/e2e。

---

## §4 Wave 4 — T7 Registry + biz.go wiring + migrations

**Depends on**: T1, T2, T4, T5 done
**Owner**: 1 agent in feature worktree
**Goal**: 4 工具注册到 factory_platform + biz.go 装配 + DB migration
**Files (numind-server)**:
- `internal/numind/biz/agent/factory_platform.go` (改：tools / metadata 列表追加 4 个；加 config / aiSvc / aliOCR 依赖)
- `internal/numind/biz/biz.go` (改：在 Init 装配 platformToolFactory 时传入新依赖)
- `internal/numind/biz/biz_test.go` (改：mock 新依赖)
- `internal/numind/biz/agent/factory_platform_test.go` (改：验证 LoadTools 返回 10 (or 12 with memory) 个 tool + 4 个新 metadata 在列)
- `migrations/20260522_153000_add_agent_run_pending_question.sql` (新)
- `migrations/20260522_154500_seed_p0_tool_definitions.sql` (新，作 idempotent fallback)
- `internal/pkg/model/agent.go` (改：AgentRun struct 加 2 字段；T4 可能已加，T7 fallback)
- `internal/numind/store/agent_run.go`（如未在 T4 加）

**实现内容**：
1. factory_platform.go：
   - `platformToolFactory` struct 加 fields: `webSearchConfig`, `aiSvc`, `aliOCR`
   - `NewPlatformToolFactory` 加参数
   - `LoadTools` 追加 4 个 FullTool + ToolMetadata（spec §11 已写过完整代码）
2. biz.go：
   - `Init` 读 `viper.Get("web_search.*")` 装配 `webSearchConfig`
   - 调 `NewPlatformToolFactory(rag, ds, webSearchConfig, aiSvc, aliOCR)`
3. migration SQL（spec §4.1 + §4.3 已写过）
4. AutoMigrate 集成（如 AgentRun model 字段 add，GORM AutoMigrate 自动加列；migration SQL 作 prod fallback）

**测试**：
- factory_platform_test：LoadTools 验证 10/12 tool 数量 + 4 新 ToolMetadata 在列
- biz_test：验证 NewPlatformToolFactory 装配新依赖
- migration：跑一次 AutoMigrate 看 agent_run 表加了 2 列（用 sqlite in-mem）

**验收**: 
- `go test ./...` 全 PASS
- `go build ./...` PASS
- lint PASS
- 本地 server start：日志看 LoadTools 输出 10 tool（or 12 with memory）

**预期 LOC**: ~200 prod + ~150 test + ~30 SQL。

---

## §5 Wave 5 — T8 architecture-v1.md update + T9 S5 validation strategy

### T8: architecture-v1.md 工具清单更新 + final docs

**Depends on**: T1-T7 done
**Owner**: 1 agent in feature worktree
**Goal**: 更新 architecture-v1.md 中工具清单 8→12，更新 README / agent runbook
**Files**:
- `docs/agent-mode/architecture-v1.md` (改：工具清单从 8 增加到 12；注意此文件**当前未入 git，工作目录 untracked**，T8 阶段决定是否 git add — 建议**不入 git**保留本地草稿状态)
- `docs/agent-mode/runbook.md` (改：加新 4 工具的运维说明：Tavily quota 监控 / web_fetch SSRF 异常 / ask_user_question stuck run 处理)
- `numind-server/CLAUDE.md` (可能改：如有 agent 相关章节需更新)

**实现内容**：
1. 找 architecture-v1.md 中 "工具清单" / "tools" / "platform tools" 等 section，把 8 工具列表扩到 12，加新增 4 工具简介
2. runbook.md 增加 4 个 troubleshooting section
3. 如 CLAUDE.md 中有 "现有 N 个内置工具" 类描述，更新数字

**验收**:
- 文档变更 review PASS（手动）
- 不破坏现有 markdown 渲染（如有 fence 跳跃）

**预期 LOC**: ~50 markdown。

---

### T9: S5 验证策略（独立 task per NDF rule 10）

**Depends on**: T1-T7 done
**Owner**: 主 session（不 dispatch subagent，是 plan-level 工件）
**Goal**: 锁定 S5 阶段使用哪种验证方式 + 列具体路径
**File**:
- `docs/agent-mode/agent-mode-p0-tools-validation-strategy.md` (新)

**内容**:

```markdown
# agent-mode-p0-tools — S5 验证策略

## 验证方式分层

### 1. 后端 TDD（强制 + 永久回归保护）

- T1-T7 每个 task 完成后 reviewer PASS 含其单测
- 单测覆盖：4 工具 happy path + error path + 边界 + 权限
- state machine + runner yield handler 单测
- biz.AnswerPendingQuestion + answer controller 单测

**理由**：单测最快、可重复、零依赖；保护后端核心逻辑长期不被破坏。

### 2. Playwright E2E（强制 1 个）

- `numind-web-v3/e2e/agent-ask-user-question.spec.ts`
- 覆盖：登录 → agent chat → 触发 ask_user_question → QuestionPrompt 渲染 → 选项点击 → narration 继续

**理由**：ask_user_question 是最复杂工具（yield turn + 跨 SSE event + 跨前后端 + 跨用户态）；e2e 覆盖能验证整链路。

**回归保护诚实声明**：本 e2e 用 fixture agent 触发 ask_user_question，未来如 fixture 变更需同步维护测试。

### 3. 后端集成测试（覆盖 yield-resume 完整流程）

- `internal/numind/biz/agent/runner_yield_test.go`（T3 落地）
- 用 in-mem SQLite + mock LLM 跑完整 Run → yield → Answer → resume → 第二段 Run → completion

**理由**：跨 process boundary 的集成测；单测无法覆盖 goroutine restart 行为。

### 4. 本地 manual smoke（S5 阶段执行）

由 AI 在 S5 阶段执行：
1. 本地 server start (`cd numind-server && task dev`)
2. 本地 web-v3 start (`cd numind-web-v3 && npm run dev`)
3. 浏览器登录 `$E2E_USERNAME`
4. 跑测试用例：
   - "今天最新的高考新闻" → 看 web_search 触发，narration 含来源
   - "看这篇 https://www.moe.gov.cn/..." → 看 web_fetch 触发
   - "我表姐..." → 看 QuestionPrompt 弹出，点击后继续
   - 上传 PDF + 问 "这份文件讲什么" → 看 file_read 触发
5. 看本地 Langfuse UI（`docker compose -f docker-compose.langfuse.yml up -d`）确认 4 工具的 Span/Generation 出现

### 5. 不在 S5 范围（明确）

- ❌ Production load test（v1）
- ❌ 跨账户 file_read 安全压测（单测已覆盖正例 + 1 个反例）
- ❌ Tavily quota 耗尽场景压测（人工触发不现实）

## 关键用户路径列表（S5 阶段 manual smoke 必跑）

| # | 路径 | 期望 |
|---|------|------|
| P1 | login → agent chat → "今天高考新闻" → 等回复 | agent 调 web_search → narration 含 ≥1 URL |
| P2 | login → agent chat → 含 URL 的消息 → 等回复 | agent 调 web_fetch → narration 总结网页 |
| P3 | login → agent chat → 模糊问题 → 等 QuestionPrompt → 点 | agent 调 ask_user_question → 前端按钮 → 答 → agent 继续 |
| P4 | login → agent chat → 附件按钮上传 PDF → "读这个" → 等 | agent 调 file_read → narration 含 PDF 内容 |
| P5 | login → agent chat → 同时触发 P1 + P3（一句话含搜索意图 + 模糊）→ 等 | agent 先调 web_search 再调 ask_user_question（或反），最终回答合理 |
```

**验收**: 文件存在 + 与 T8 一并 review PASS

**预期 LOC**: ~150 markdown。

---

## §6 Task 依赖图（DAG）

```
T1 ──┐
T2 ──┤
T3 ──┤───── Wave 1 完成 ─────► T4 ─────► T6 ──┐
T5 ──┘                                          ├──── Wave 4 ─────► T7 ─────► T8 ──┐
                                                                                    ├──► S5 验收 ──► S6 ndf-done
                                                                                T9 ─┘
```

**关键检查**:
- T4 启动前：T3 已 merge 回主 worktree
- T6 启动前：T4 已 merge 回主 worktree（answer endpoint API ready）
- T7 启动前：T1 + T2 + T4 + T5 全 merged
- T8/T9 启动前：T7 已 merged

---

## §7 Reviewer Dispatch（每 task 完成后）

per CLAUDE.md `.claude/rules/ndf-enforcement.md` 规则 6 + NDF §4 S4 gate：

**每个 task 完成后**主 session **并行** dispatch 2 个 sonnet reviewer subagent：

1. **Spec Compliance Review** (`subagent_type: general-purpose`, `model: "sonnet"`)
   - 模板：`templates/ndf/review-spec-compliance.md`
   - 输入：spec § 该 task 对应 sections + 实际 commit diff
   - 输出：`<severity>: <file>:<line> — <rule-id> — <problem> — fix: <suggestion>`

2. **Code Quality Review** (`subagent_type: general-purpose`, `model: "sonnet"`)
   - 模板：`templates/ndf/review-code-quality.md`
   - 输入：commit diff
   - 输出：同结构

**P0/P1 必修**：主 session inline 修复，更新 commit；**P2 能现修则现修**，无依赖时不推迟（per `.claude/rules/ndf-enforcement.md` 规则 7）。

**Bug-from-Customer Rule 11**：本 feature **不是 bug-fix**（是新功能），不强制复现测试 commit；但每个 task 有新增正向测试。

---

## §8 Task 原子性自检 (per rule 9)

| Task | 完成后系统能编译？ | reviewer 能独立理解？ | 内部多模块？ |
|------|---------------|------------------|------------|
| T1 | ✅ go build 通过（未注册到 factory，未被调）| ✅ web_search 独立工具，clear scope | 否 — 单一工具 + 配套 aiservice wrapper |
| T2 | ✅ 同上 | ✅ web_fetch 独立工具 | 否 |
| T3 | ✅ go build 通过（state 加新 enum + runner 加 yield handler，仍向后兼容）| ✅ 状态机改动局部 | 否 — 单一职责 yield 协议 |
| T5 | ✅ 同上 | ✅ file_read 独立工具 | 否 |
| T4 | ✅ go build 通过（依赖 T3 已 merge）| ✅ ask_user_question + answer endpoint 内聚 | 否 — 一个 LLM-yield 工具 + 配套 biz/controller/store 改动是一组原子改动 |
| T6 | ✅ npm build 通过 | ✅ 前端组件 + e2e 内聚 | 否 |
| T7 | ✅ 完整 go build + LoadTools 输出 12 tool | ✅ registry wiring 内聚 | 否 — 注册 + 装配 + migration 是一组原子 wiring 改动 |
| T8 | N/A 文档 | ✅ 文档独立 | 否 |
| T9 | N/A 文档 | ✅ 验证策略独立 | 否 |

**结论**: 9 个 task 均原子，符合 rule 9。

---

## §9 Task DoD (Definition of Done)

每个 task 通过下列条件才算 done：

1. ✅ 实现代码 + 单元测试 commit on feature 分支
2. ✅ `go test ./changed-package/...` PASS（或前端 `npm run test:unit`）
3. ✅ `task lint` (numind-server) / `npm run lint && npm run type-check` (web-v3) PASS
4. ✅ Spec Compliance Reviewer subagent 输出 0 P0/P1
5. ✅ Code Quality Reviewer subagent 输出 0 P0/P1
6. ✅ manifest progress.completed_tasks += 1 + reviewed_tasks += 1
7. ✅ `git log --oneline -1` 消息符合 Conventional Commits + 描述准确

不满足任一条件 → task 仍 in_progress；reviewer P0/P1 → 主 session inline 修复后重 review。

---

## §10 验证策略锚定（per NDF rule 10）

T9 是独立的"S5 验证策略" task（见 §5），spec compliance reviewer **必须**在 S3 gate 审议其合理性。本 plan 已 lock：

- **Playwright E2E** for ask_user_question 主流程（最复杂 yield）
- **Go unit + integration** for 后端核心逻辑（4 工具 + state machine + biz）
- **Local manual smoke** for end-to-end 综合体验（5 路径 P1-P5）
- **回归保护诚实声明**：Playwright e2e fixture 依赖 agent prompt，未来 agent 配置变更需同步维护

---

## §11 Tier 标签 + 文件归属（per rule 12）

**Wave 1 主 session dispatch 前必输出**：

```
Agent T1 拥有: internal/numind/biz/agent/tool_web_search.go, internal/numind/biz/agent/tool_web_search_test.go, internal/pkg/aiservice/web_search.go, internal/pkg/aiservice/web_search_test.go, config_local.yaml, config_dev.yaml, config_qa.yaml
Agent T2 拥有: internal/numind/biz/agent/tool_web_fetch.go, internal/numind/biz/agent/tool_web_fetch_test.go, internal/pkg/httpclient/safe_dial.go
Agent T3 拥有: internal/numind/biz/agent/state.go, internal/numind/biz/agent/state_test.go, internal/numind/biz/agent/runner.go, internal/numind/biz/agent/runner_yield_test.go, internal/numind/biz/agent/yield_error.go
Agent T5 拥有: internal/numind/biz/agent/tool_file_read.go, internal/numind/biz/agent/tool_file_read_test.go, internal/numind/biz/agent/file_read_parsers.go, internal/numind/biz/agent/file_read_parsers_test.go
```

执行 ndf-check-disjoint 等价校验（手动）：上述 4 组 path set 无交集 ✅。

T1/T2/T5 完全独立。T3 改 runner.go，但 yield 处理是新代码块（追加，不修改 existing logic）—**编译期独立**。运行期通过 T7 wiring 串起来。

---

*S3 完成。任务清单 9 个 ready；进入 S4 编码 Wave 1。*
