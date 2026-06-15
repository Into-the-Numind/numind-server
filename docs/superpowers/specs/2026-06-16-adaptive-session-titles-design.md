# 会话自适应标题 + Agent 历史列表完整化 — 设计 Spec (S1+S2 合并)

- Feature: `adaptive-session-titles`
- 日期: 2026-06-16
- 仓库: numind-server, numind-web-v3
- Track: Standard
- 关联需求卡: `requirements/adaptive-session-titles.md`

---

## §1 问题与目标 (S1 PRD)

### 1.1 现状痛点
1. **Chatbot**：同一智能体下所有会话标题都写死成智能体名（`config.Name`）→ 列表里一列重复标题，无法区分。
2. **Agent**：`session_name` 默认空字符串 → 侧边栏全显示"新对话"。
3. **Agent 历史 bug**：侧边栏只显示最近 5 个会话（四层硬编码 limit=5），用户找不回更早的会话。
4. **Chatbot 历史**：默认只拉首页 20 条，无"加载更多"，会话多了看不全。

### 1.2 用户故事 + 验收标准

| US | 用户故事 | 验收标准 |
|----|---------|---------|
| US1 | 作为用户，chatbot 首轮对话后，会话标题自动变成与内容相关的简短标题 | 首轮 assistant 回复结束后，该会话标题从"智能体名"变为 6-12 字内容摘要；列表与当前会话头部同步显示 |
| US2 | 作为用户，agent 首轮指令完成后，会话标题自动变成内容相关标题 | 首轮 run 终止后，`session_name` 从空变为 6-12 字摘要；侧边栏该会话标题同步更新 |
| US3 | 作为用户，我手动改过的标题不会被自动覆盖 | 已 rename（chatbot title≠默认名 / agent session_name≠空）的会话，后续对话不触发自动标题 |
| US4 | 作为用户，agent 侧边栏能看到我全部历史会话，最新在最上，可滚动 | 侧边栏列出全部会话（不再限 5），按最近活跃倒序，超出高度可滚动 |
| US5 | 作为用户，chatbot 也能查看全部历史会话 | chatbot 会话列表支持"加载更多"翻页到全部，可滚动 |
| US6 | 作为用户，自动生成标题不消耗我的积分 | 标题生成的 LLM 调用不产生 `credit_reservation` / `credit_transaction` 行 |

### 1.3 非目标
- 不做标题的"重新生成"按钮（手动 rename 已存在，够用）。
- 不改 agent 全部历史页 `AgentHistoryView` 的分组逻辑（复用）。
- 不引入新的会员/权限判定。

---

## §2 技术设计 (S2)

### 2.1 架构总览

```
首轮对话结束
   ├─ chatbot: biz/chatbot/stream.go ChatStream — 消息持久化后、done 前
   └─ agent:   biz/agent/runner.go finalizeRun — WriteTurn 后
        │
        ▼
   biz/sessiontitle.Generate(ctx, firstUserMsg, firstAssistantMsg) → title  ← 新增共享 helper
        │  (aiservice.Chat, qwen-turbo, 无 ContextFragments, WithSkipLegacyBilling, 不计费)
        ▼
   ├─ chatbot: store.ChatbotSession().UpdateTitle(sessionID, title) + done 事件带 session_title
   └─ agent:   store.AgentRun().UpdateSessionName(sessionID, title) + 前端 terminal 后 refetch
```

### 2.2 共享标题生成 helper（新增）

新建包 `internal/numind/biz/sessiontitle/sessiontitle.go`：

```go
package sessiontitle

// Generate 用便宜小模型从首轮对话生成 6-12 字内容标题。
// 不向用户计费（无 ContextFragments → 预算中间件 pass-through；WithSkipLegacyBilling → 跳过 UsageRecord）。
// 失败时返回 ("", err)，调用方应降级为不改标题（best-effort）。
func Generate(ctx context.Context, userMsg, assistantMsg string) (string, error)
```

实现要点：
1. 截断输入（userMsg 取前 ~500 字，assistantMsg 取前 ~800 字）控制 prompt 体积。
2. Prompt（系统）：`"你是会话标题生成器。根据下面的对话，用 6 到 12 个汉字概括主题作为标题。只输出标题本身，不要任何标点、引号、前后缀、解释。"`，user 消息拼首轮对话。
3. 调用（**计费豁免 — 已按 S2 review P0 修正**）：
   ```go
   // 关键：剥离一切 billing 上下文，确保两条调用路径(chatbot / agent finalizeCtx)都走预算中间件
   // pass-through，不依赖 DB policy。原因见下方 P0 说明。
   ctx, cancel := context.WithTimeout(ctx, 3*time.Second)   // P2: 短超时，慢则降级不改标题
   defer cancel()
   ctx = billing.WithBilling(ctx, 0, "")          // 覆盖继承的 userID/operation → 0
   ctx = aismw.WithUserID(ctx, 0)                 // 清 tracing userID fallback（free-model gate 在 pass-through 前跑）
   ctx = aismw.WithoutGatewayBillingOnly(ctx)     // 清 bill-only flag → 使 Step1 pass-through 生效（新增 helper）
   ctx = aiservice.WithSkipLegacyBilling(ctx)     // 跳过 legacy UsageRecord
   req := aiservice.ChatRequest{
       Messages:      msgs,           // 不设 ContextFragments → 预算中间件 Step1 pass-through
       ModelOverride: "qwen-turbo",   // 非 0 价廉价模型（绕开 free-model 会员门）
       MaxTokens:     32,
       Temperature:   0.3,
   }
   resp, err := aiservice.Chat(ctx, profile.SessionTitle, req)
   ```
4. 后处理 `sanitizeTitle`：去首尾空白/引号/句号/换行；按 rune 截到最多 20 字；若清洗后为空则返回 err（让调用方不改标题）。
5. Langfuse：title 调用在所属 trace 下记一个 generation（name="session-title", model, usage）；trace 不存在时优雅跳过（`if tc := langfuse.FromContext(ctx); tc != nil`）。失败按 `.claude/rules/ai-service.md §3` 记 generation error。

**新增 helper** `internal/pkg/aiservice/middleware/billing_pool.go`：
```go
// WithoutGatewayBillingOnly 清除 bill-only 标记（用于系统内部、不计费的 LLM 调用）。
func WithoutGatewayBillingOnly(ctx context.Context) context.Context {
    return context.WithValue(ctx, ctxKeyGatewayBillingOnly{}, false)
}
```

**新增 profile 常量** `internal/pkg/aiservice/profile/constants.go`：
```go
SessionTitle = "session.title"
```
并加入该文件底部的 **`allTaskIDsList` 切片**（review P2：不是只加常量声明，必须进 `allTaskIDsList`，否则 `AllTaskIDs()` / DB seed/validation 不包含此 task）。

> **P0（S2 review 关键修正）**：agent 路径 `finalizeRun` 的 `finalizeCtx = context.WithoutCancel(ctx)` **继承了 `billOnly=true`（`WithGatewayBillingOnly`）和 userID**。预算中间件 pass-through（`context_budget.go:431`）要求 `!billOnly`，否则走 bill-only → reserve 仅由 DB `context_budget_policy.charge_user=false` 拦截。即先前"无 ContextFragments 即 pass-through"的断言**仅对 chatbot 路径成立，对 agent 路径不成立**。修复：`Generate` 内部用上面 4 行剥离全部 billing ctx（含 `WithoutGatewayBillingOnly`），使两条路径都 `billOnly=false` 且 `userID=0` → Step1 pass-through 不 reserve，free-model gate 因 userID=0 跳过。**"不计费"由此不依赖任何 DB 配置。**
>
> **路由（仅影响"用哪个模型"，不影响"是否计费"）**：`session.title` task 在 DB registry 注册指向 qwen-turbo 的路由（S6 dev / S7 prod 各配一次，30s 缓存免重启，见 `project_add_ai_model_registry_runbook`）。漏配时 `ModelOverride` 命中失败 → gateway 回落该 task primary 路由，标题仍生成（只是模型可能偏贵），**不会因此扣用户积分**（计费已由剥离 ctx 兜死）。

### 2.3 Chatbot 接入（`biz/chatbot/stream.go`）

挂载点：`ChatStream` 中持久化 user+assistant 消息、`IncrementMessageCount` 之后，发送 `done` 之前。

```go
// 首轮判断：persist 前取的 maxSeq == 0 表示这是该 session 第一条消息
var newTitle string
if maxSeqBefore == 0 {                                  // 首轮
    if t, terr := sessiontitle.Generate(ctx, message, fullContent.String()); terr == nil && t != "" {
        if uerr := b.ds.ChatbotSession().UpdateTitle(ctx, sessionID, t); uerr == nil {
            newTitle = t
        } else {
            log.C(ctx).Warnw("ChatStream: update title failed", "error", uerr)
        }
    } else if terr != nil {
        log.C(ctx).Warnw("ChatStream: generate title failed", "error", terr)
    }
}
// done 事件
if newTitle != "" {
    doneData["session_title"] = newTitle
}
```

约束：
- 同步生成（在 done 前），用户在首轮结束即看到新标题（done 事件带回），无需额外请求；现有 finally refetch 作为兜底。
- `maxSeqBefore` = 持久化前调用 `GetMaxSeq` 得到的值（已在该处获取，复用）。
- guard US3：仅 `maxSeqBefore == 0` 触发 → 首轮之后（含手动 rename 后）永不覆盖。

### 2.4 Agent 接入（`biz/agent/runner.go finalizeRun`）

挂载点：`finalizeRun` 中 `WriteTurn` 落库之后、return 之前，使用 `finalizeCtx`（`context.WithoutCancel`）。

```go
// 注意(S2 review P1)：finalizeRun 执行时当前 run 已落库(Create 早于 finalizeRun)，故 total≥1。
// 真正首轮 = total==1 且该 run 的 session_name 为空；去掉永不触发的 total==0 分支。
if sessionID != "" {
    runs, total, lerr := r.runStore.ListBySession(finalizeCtx, sessionID, 0, 1)
    firstRun := lerr == nil && total == 1 && len(runs) == 1 && runs[0].SessionName == ""
    if firstRun {
        if t, terr := sessiontitle.Generate(finalizeCtx, req.InputText, finalText); terr == nil && t != "" {
            if uerr := r.runStore.UpdateSessionName(finalizeCtx, sessionID, t); uerr != nil {
                log.Warnw("finalizeRun: update session_name failed", "error", uerr)
            }
        } else if terr != nil {
            log.Warnw("finalizeRun: generate title failed", "error", terr)
        }
    }
}
```

约束：
- best-effort：失败只 log，不影响 run 结果。
- guard US3：`session_name == ""` 才生成；手动 rename / 续跑继承的非空 name 不覆盖。
- `req.InputText` = 首轮用户指令；`finalText` = 最终回答（finalizeRun 已有形参）。
- 输入字段名以 S4 实际签名为准（runner 内 RunRequest 字段名、finalText 形参）。

### 2.5 Agent 侧边栏"展示全部"（前端 + 后端校验）

**前端**（`numind-web-v3`）：
- `src/stores/agentChat.ts` `fetchRecentSessions`：改为调用 `api.listAllHistorySessions()`（不限量），变量沿用 `recentSessions`（避免大范围 rename 波及 agent-output-refine 的文件）。
- `src/views/agent/AgentChatView.vue` `.sessions-list`：加滚动容器 `max-height: calc(100vh - <header/newbtn 高度>); overflow-y: auto;`（具体值 S5 截图微调）。
- 排序：依赖后端返回倒序；如后端未保证，在 `filteredSessions` computed 里按 `last_active_at` desc 兜底排序。

**后端校验/改动**（`numind-server`）：
- 确认 `GET /v1/agent-sessions/history`（`biz/agent/student_query.go ListAllHistorySessions` + store）排序为 `is_pinned DESC, last_active_at/started_at DESC`（最新在上）。若未排序则补 ORDER BY（仅排序，不动语义）。
- **（S2 review P2）去掉 30 天 `since` 窗口**：`ListAllHistorySessions` 现有 `since = now-30d` 过滤 + limit=500，与 US4"全部历史"不符。改为去掉 `since` 时间窗（展示真正全部），保留一个宽松上限（如 500，作安全阀，document）。此改动同时使全部历史页 `AgentHistoryView` 也显示全部，语义一致。
- `/recent` 端点的 limit=5 默认值**保留不改**（其他可能调用方/未来用途），本 feature 不再依赖它。

> ⚠️ 冲突注意：`AgentChatView.vue`、`agentChat.ts` 可能与活跃 feature `agent-output-refine`（S3）有交集。S3/S4 必须跑 `ndf-check-disjoint`，merge 时注意顺序。本 feature 对这两个文件的改动应尽量局部（仅 sessions-list 段 + fetch action）。

### 2.6 Chatbot 历史"加载更多"（前端）

- `src/stores/chatbot.ts`：新增 `sessionsOffset` 状态 + `loadMoreSessions(chatbotId)` action（offset += limit，append 到 `sessions`，已有 `sessionsTotal` 判断是否还有更多）。`fetchSessions` 重置 offset=0。
- `src/views/chatbot/ChatbotChat.vue`：`.sessions-list` 加滚动容器；底部加"加载更多"按钮（`v-if="chatbotSessions.length < sessionsTotal"`）。
- done 事件 `session_title` 处理：在 `sendMessage` 的 `done` case 里，若带 `session_title` 则就地更新 `currentSession.title` 及 `sessions` 中对应项（避免依赖 finally 的全量 refetch；refetch 保留作兜底）。

### 2.7 Trace Topology（AI 功能必填 — S2 gate 要求）

标题生成是 LLM 调用，须接入 Langfuse：
- **复用所属操作的 trace**：chatbot 用 `ChatStream` 已建的 trace（ctx 里有）；agent 用 finalizeRun 所在 run 的 trace。
- 在该 trace 下为标题调用记一个 **generation**：`name="session-title"`，`model=qwen-turbo`（实际命中模型），`input`=截断后的对话，`output`=生成标题，`usage`=prompt/completion tokens。
- 优雅降级：`langfuse.FromContext(ctx) == nil` 时不记录、不报错。
- 失败路径：title 调用 err 时按 `.claude/rules/ai-service.md §3` 记 generation error。

### 2.8 数据 / API 契约
- **无 DB schema 变更**（title / session_name 字段已存在）。
- **无新增对外 API 端点**。
- 契约变化：chatbot SSE `done` 事件新增可选字段 `session_title: string`（前端可选消费，向后兼容）。
- store 复用既有方法：`ChatbotSession().UpdateTitle`、`AgentRun().UpdateSessionName`、`AgentRun().ListBySession`、`ChatbotSession().GetMaxSeq`。

---

## §3 验证策略（S5，预告，S3 plan 细化）
- **后端**：Go 单测——`sessiontitle.Generate` 的 sanitize 纯函数（去引号/截断/空回退）；mock aiservice 验证不带 ContextFragments + 设了 WithSkipLegacyBilling。chatbot/agent 首轮 guard 的单测（maxSeq==0 / session_name=="" 才触发；非首轮不触发）。
- **计费断言**（US6 关键）：集成测试或 dev 实跑后查 `credit_reservation` / `credit_transaction` 无标题操作产生的行。
- **前端**：vitest（store loadMore / done session_title 更新）+ vue-tsc + eslint。
- **dev 浏览器验收**（S6 后）：browse/gstack 登录 → chatbot 发首条 → 标题变化；agent 发首条 → 标题变化 + 侧边栏显示 >5 个历史且最新在上、可滚动；chatbot 加载更多可用。
- **回归保护**：本 feature 非 bug-from-customer（用户主动提的体验改进 + 一个内部发现的 limit bug），按 Standard 写常规单测即可；agent 5-limit 修复建议补一条"侧边栏不再限 5"的前端 vitest 做回归。

---

## §4 风险与决策
1. **计费正确性（最高风险，S2 review 已加固）**：P0 已修——`Generate` 内部剥离全部 billing ctx（含新增 `WithoutGatewayBillingOnly`），chatbot 与 agent 两条路径都 pass-through，**不计费不依赖任何 DB policy**。S5 仍必须实测：首轮 chatbot/agent 后查 `credit_reservation`/`credit_transaction` 无标题操作产生的行。
2. **与 agent-output-refine 文件冲突**：两 standard feature 同改 agent 区。缓解：改动局部化 + S4 文件归属声明 + merge 顺序协调。
3. **标题质量**：qwen-turbo 6-12 字摘要，prompt 约束输出纯标题；sanitize 兜底；质量不达预期可后续调 prompt（非阻塞）。
4. **延迟**：chatbot 同步生成给 done 加 ~1s；agent 在 finalize（用户已拿到答案后）无感。可接受。
5. **路由依赖**：qwen-turbo 路由靠 DB registry，dev/prod 各配一次；漏配只影响"用哪个模型"不影响"不计费"。

---

## §5 落点清单（给 S3 切 task 参考）
- T1 后端：`aismw.WithoutGatewayBillingOnly` helper + `profile.SessionTitle` 常量(进 allTaskIDsList) + `sessiontitle` 包(Generate 含剥离 billing ctx + 3s 超时 + sanitize + Langfuse generation) + 单测（sanitize 纯函数 / mock aiservice 断言 req 无 ContextFragments、ctx 已剥离 billOnly+userID+skip-legacy）
- T2 后端：chatbot `ChatStream` 接入（maxSeq==0 guard + UpdateTitle + done.session_title）+ 单测
- T3 后端：agent `finalizeRun` 接入（total==1 && session_name=="" guard + UpdateSessionName）+ 单测；`ListAllHistorySessions` 去 30 天窗 + 校验排序 + 单测
- T4 前端：agent 侧边栏改用 listAllHistorySessions + 滚动容器 + 排序兜底 + vitest(不再限 5 回归)
- T5 前端：chatbot 加载更多 + 滚动容器 + done.session_title 实时更新 + vitest
- T6：S5 验证策略 task（独立列出，S3 reviewer 审）

## §6 S2 review 结论（Sonnet 独立审查 2026-06-16）
CONDITIONAL_PASS。2 个 P0（agent finalizeCtx 继承 billOnly 致计费依赖 DB policy；spec 原计费断言对 agent 路径错误）—— 均已在 §2.2 修正（剥离 billing ctx，不依赖 DB）。P1（agent guard total 恒≥1，去 total==0 分支）已在 §2.4 修正。P2（history 30 天窗 / allTaskIDsList / 3s 超时）已分别纳入 §2.5 / §2.2 / §2.2。需求覆盖 US1-US6 齐全；Langfuse 合规；aiservice 唯一入口合规。
