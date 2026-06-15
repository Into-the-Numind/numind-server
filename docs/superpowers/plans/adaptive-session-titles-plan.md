# 实施计划 — adaptive-session-titles (S3)

- Spec: `docs/superpowers/specs/2026-06-16-adaptive-session-titles-design.md`
- 仓库: numind-server (后端) + numind-web-v3 (前端)
- Worktrees: `/private/tmp/wt-adaptive-session-titles-{server,web-v3}`

## 任务依赖图
```
T1(后端基座) ──┬──> T2(chatbot 接入)
               └──> T3(agent 接入 + history 去窗)
T4(agent 前端) ── 独立(依赖既有 listAllHistorySessions API，可与后端并行)
T5(chatbot 前端) ── 独立(done.session_title 为可选消费，向后兼容)
T6(S5 验证策略) ── 文档，最后
```
- 唯一硬代码依赖：T2、T3 依赖 T1 的 `sessiontitle.Generate` + `profile.SessionTitle` + `aismw.WithoutGatewayBillingOnly`。
- T4/T5 与后端不同仓库（Tier 2 可并行）；T4 与 T5 同仓库不同文件（Tier 3，需 disjoint 校验）。
- 执行策略：主 session 在两个 feature worktree 内**顺序实现**每个 task（T1→T2→T3 后端；T4→T5 前端），每个 task 完成后**并行** dispatch 双 reviewer（spec-compliance + code-quality, Sonnet）。

---

## T1 — 后端基座：billing helper + profile + sessiontitle 包
**仓库**: numind-server
**文件归属**:
- `internal/pkg/aiservice/middleware/billing_pool.go`（新增 `WithoutGatewayBillingOnly`）
- `internal/pkg/aiservice/profile/constants.go`（新增 `SessionTitle="session.title"` + 加入 `allTaskIDsList`）
- `internal/numind/biz/sessiontitle/sessiontitle.go`（新建）
- `internal/numind/biz/sessiontitle/sessiontitle_test.go`（新建）

**内容**:
- `WithoutGatewayBillingOnly(ctx) ctx`：`context.WithValue(ctx, ctxKeyGatewayBillingOnly{}, false)`。
- `sessiontitle.Generate(ctx, userMsg, assistantMsg string) (string, error)`：截断输入→剥离 billing ctx（`billing.WithBilling(ctx,0,"")` + `aismw.WithUserID(ctx,0)` + `aismw.WithoutGatewayBillingOnly(ctx)` + `aiservice.WithSkipLegacyBilling(ctx)`）+ 3s 超时→`aiservice.Chat(ctx, profile.SessionTitle, req{无 ContextFragments, ModelOverride:"qwen-turbo", MaxTokens:32})`→`sanitizeTitle`→Langfuse generation（FromContext 守卫）。
- `sanitizeTitle(s) string`：去首尾空白/引号(各类)/句号/换行；rune 截 ≤20；空→返回触发"不改标题"。

**验收**:
- `go build ./...` 通过。
- 单测：sanitize 纯函数多 case（引号/超长/空/含换行）；mock aiservice chatFn 断言传入 req `ContextFragments==nil` 且 ctx 经 `GatewayBillingOnlyFromCtx==false`、`billing.FromContext().UserID==0`、`ShouldSkipLegacyBilling==true`。
- `task lint` 0。

**原子性**: 自包含，编译通过，可独立验证。无前置依赖。

---

## T2 — chatbot ChatStream 接入
**仓库**: numind-server（依赖 T1）
**文件归属**:
- `internal/numind/biz/chatbot/stream.go`
- `internal/numind/biz/chatbot/stream_title_test.go`（新建，避免动既有大测试文件）

**内容**: 在持久化消息（已取 `maxSeq`）后、`done` 前，`if maxSeq==0` → `sessiontitle.Generate(ctx, message, fullContent.String())` → `UpdateTitle` → `doneData["session_title"]=title`。失败仅 log。

**验收**:
- 单测：首轮(maxSeq==0)触发并 UpdateTitle + done 带 session_title；非首轮(maxSeq>0)不触发；Generate 失败时不改标题且 done 不带 session_title、流程不报错。
- `go test ./internal/numind/biz/chatbot/...` + `task lint` 0。

**原子性**: 依赖 T1 已合入 worktree；完成后 chatbot 首轮自动标题可独立验证。

---

## T3 — agent finalizeRun 接入 + history 去 30 天窗
**仓库**: numind-server（依赖 T1）
**文件归属**:
- `internal/numind/biz/agent/runner.go`（finalizeRun 挂载）
- `internal/numind/biz/agent/student_query.go`（`ListAllHistorySessions` 去 `since` 窗 + 校验排序）
- `internal/numind/biz/agent/*_test.go`（新建针对性测试文件，如 `runner_title_test.go` / `student_query_history_test.go`）

**内容**:
- finalizeRun WriteTurn 后：`firstRun := total==1 && len(runs)==1 && runs[0].SessionName==""` → `sessiontitle.Generate(finalizeCtx, req.InputText, finalText)` → `UpdateSessionName`。失败仅 log。
- `ListAllHistorySessions`：移除 `since=now-30d` 过滤，保留宽松上限(≤500)；确认/补 `is_pinned DESC, started_at DESC` 排序。

**验收**:
- 单测：首轮触发标题 + UpdateSessionName；已有 session_name 不触发；多 run 不重复触发；history 不再按 30 天截断、排序最新在上。
- `go test ./internal/numind/biz/agent/...` + `task lint` 0。

**原子性**: 依赖 T1；完成后 agent 首轮标题 + 全量历史可独立验证。注意 runner.go 字段名以实际为准。

---

## T4 — agent 侧边栏全量历史 + 滚动
**仓库**: numind-web-v3（与后端 Tier 2 可并行；与 T5 Tier 3 disjoint）
**文件归属**:
- `src/stores/agentChat.ts`（`fetchRecentSessions` 改调 `listAllHistorySessions`）
- `src/views/agent/AgentChatView.vue`（`.sessions-list` 加 `max-height + overflow-y:auto`；`filteredSessions` 按 last_active_at desc 兜底排序）
- `src/stores/__tests__/agentChat.*.spec.ts`（新建/追加：不再限 5 回归）

**验收**: vitest 通过；`npm run type-check` + `npm run lint`(scope 改动文件) 0；fetch 全量、排序最新在上。
**原子性**: 独立可验证（API 已存在）。⚠️ 与 agent-output-refine 可能同改 AgentChatView.vue/agentChat.ts — 改动局部化到 sessions-list 段 + fetch action；merge 前 fetch develop 看是否已被该 feature 改动。

---

## T5 — chatbot 加载更多 + 滚动 + done 实时标题
**仓库**: numind-web-v3（与 T4 Tier 3 disjoint）
**文件归属**:
- `src/stores/chatbot.ts`（`sessionsOffset` + `loadMoreSessions`；`done` case 处理 `session_title`）
- `src/views/chatbot/ChatbotChat.vue`（滚动容器 + 加载更多按钮）
- `src/stores/__tests__/chatbot.*.spec.ts`（新建/追加）

**验收**: vitest 通过；type-check + lint 0；加载更多 append、可滚动、done 带标题时即时更新当前会话与列表项。
**原子性**: 独立可验证。

---

## T6 — S5 验证策略（Rule 10，独立 task，S3 reviewer 审）
- **验证方式**: 后端 Go 单测（TDD，每 task）+ 前端 vitest + **dev 浏览器 QA（gstack /qa 或 browse，访问 dev）**。
- **理由**: 涉及前端交互（标题刷新、列表滚动、加载更多）必须真实浏览器验证；标题生成是后端逻辑用单测保证；计费豁免必须 dev 实跑后查 DB 无扣费行（高风险业务，不能只信单测）。本 feature 非纯后端，故不只 TDD。
- **回归保护诚实声明**: 后端单测 + 前端 vitest 持久留库提供回归保护；dev 浏览器 QA 为一次性验证不产持久测试。agent 5-limit 修复用前端 vitest 锁住"不再限 5"做回归。计费豁免有后端 mock 单测 + 一次性 dev DB 核验（无持久回归测试断言真实扣费，因需真实 LLM 调用，记为可接受边界）。
- **关键用户路径**（S5 须验证）:
  1. chatbot 新建会话→发首条→assistant 回复结束→标题从智能体名变为内容摘要（当前会话头部 + 列表项同步）。
  2. chatbot 第二条消息→标题不再变；手动 rename 后发消息→标题保持手动值。
  3. agent 新建 session→发首条指令→run 完成→侧边栏该会话标题从"新对话"变为内容摘要。
  4. agent 侧边栏显示 >5 个历史会话、最新在最上、可滚动。
  5. chatbot 会话 >20 时"加载更多"可翻出全部、可滚动。
  6. 计费核验：上述 chatbot/agent 首轮后，查 `credit_reservation`/`credit_transaction` 无标题操作产生的扣费行。

---

## S3 原子性 review 结论（Sonnet 独立审查 2026-06-16）
CONDITIONAL_PASS，0 P0，2 P1 + 3 P2，已折入下列执行约束：
- **P1-1（T1）**：T1 验收**追加** Langfuse generation **失败路径**单测——mock aiservice 返回 err 时验证记 generation error（output 含 error）或无 trace 时优雅跳过（ai-service.md §3 合规）。
- **P1-2（T5←T2）**：T5 消费 T2 的 `done.session_title`；T5 可先实现（字段缺失时 no-op 向后兼容），但 **T2 合并后才能做完整集成验证**。
- **P2-1（T3）**：T3 用**两个独立 commit**（runner 标题接入 / student_query 去时间窗），各自验收分述。
- **P2-2（T4）**：T4 开工前**必跑** `ndf-check-disjoint` 对比本 feature 与 agent-output-refine 的前端文件集，exit 0 才动手。
- **P2-3（T6）**：计费核验法具体化——首轮完成后 `SELECT COUNT(*) FROM credit_reservation WHERE user_id=<uid> AND created_at>=<首轮起>`，差值应等于主对话 reserve 行数（标题不应新增 reserve 行）；并可对 session-title 的 Langfuse generation 确认无 reserve/transaction。

## 路由依赖（S6/S7 操作项）
`profile.SessionTitle="session.title"` 需在 DB registry 注册一条 → qwen-turbo 路由（dev S6 / prod S7 各一次，30s 缓存免重启）。漏配时 `Generate` 优雅 no-op（best-effort，不报错不扣费）。附 seed SQL 文档化（部署不自动跑 migration，需手动 SSH 执行——见 `project_dev_deploy_migration_gap`）。

## 门禁汇总
- 每 task: `go test`(后端) / `vitest`(前端) + lint + **并行双 reviewer(Sonnet) PASS(无 P0)**。
- S4 出口: `task lint` 0 + `go test ./...` 0 + 前端 `npm run lint`+`type-check` 0 + `reviewed_tasks==completed_tasks`。
- bug-from-customer: 本 feature 非客户 bug（用户主动体验改进 + 内部发现的 limit bug），无强制复现测试链要求，按 Standard 常规测试。
