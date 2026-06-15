# Agent Mode 问题卡 / Narration / 工具卡 体验修复 — 技术设计（agent-qa-card-ux）

> NDF Standard S2 spec。覆盖 S1 PRD 全部 4 个问题。版本 1.0。

## 0. 范围与原则
- 跨仓库：numind-server（后端持久化重建 + 流式续跑端点）、numind-web-v3（渲染 + CSS + 终态收尾 + 流式客户端）。
- 不动 DB schema（issue 1 用 turn JSON 内嵌）。
- 增量优先：issue 4 新端点纯增量，旧 postAgentAnswer + 轮询路径保留作 fallback。
- 不新增裸 LLM 调用入口（issue 4 复用 runner.RunStream 的既有 aiservice + Langfuse 集成）。

---

## 1. Issue 1 — 已回答问题卡刷新后保持卡片形态

### 1.1 根因（已确证）
- `store/agent_run.go:AnswerAndClear`（337-385）把答案以 `{role:"user", content:"用户已回答你的问题：…"}` turn 写入 `agent_run.messages`，并清空 `pending_question_json`。
- `biz/agent/student_query.go:synthesizeQuestionPrompt`（559-608）仅当 `run.StateReason == waiting_for_user_choice` 时合成问题卡。任务完成后状态非 waiting → 不再合成。
- `transformMessages`（618+，role=="user" 分支 655-657）把那条答案 turn 渲染为普通 user 气泡。

### 1.2 设计：答案 turn 内嵌 `question_answer` 结构
**持久化形状**（`agent_run.messages` 内该 user turn）：
```json
{
  "role": "user",
  "content": "用户已回答你的问题：\n- 「Q1」→ A1\n…\n请据此继续，不要重复已回答的问题。",
  "question_answer": {
    "questions": [
      { "question": "Q1 文本", "header": "可选", "multi_select": false,
        "options": [{"label":"A","description":"…"}],
        "answer": "用户实际所选（resolveAnswer 结果，含自由文本）" }
    ]
  }
}
```
- `content` 不变（LLM resume 历史只读 role+content）。
- `question_answer.questions[i].answer` 是 `resolveAnswer(item)` 的结果字符串；跳过的问题不出现在 questions 里（与 buildAnswerMessage 一致：只渲染已答）。

**后端改动**：
1. `store/agent_run.go:AnswerAndClear` 签名由 `(ctx, id, userMessage string)` 改为 `(ctx, id, turn json.RawMessage)`——biz 层构造完整 turn（含 question_answer）传入。store 不再自己 marshal `{role,content}`。更新 interface（`IAgentRunStore`）+ `agent_run_resume_test.go`。
2. `biz/agent/answer.go:Answer`：新增 `buildAnswerTurn(pending, answers)` 返回 `json.RawMessage`（role=user + content=buildAnswerMessage(...) + question_answer 结构）；调用 `AnswerAndClear(ctx, runID, turn)`。
3. `biz/agent/student_query.go:transformMessages`：role=="user" 分支增加——若 turn 含非空 `question_answer.questions`，emit `agentMessage{Type:"question_prompt", RunID, Questions:[...含 Answer], AnswerStatus:"answered"}`，**不** emit user 气泡。否则维持现状 emit user 气泡。
4. `agentMessage.questionPromptItem`（student_query.go ~543-553）增加 `Answer string \`json:"answer,omitempty"\``。

**契约不变量**：`synthesizeQuestionPrompt` 保持原样（仍处理「当前正 waiting」的 pending 卡注入）；issue 1 走 transformMessages 重建路径，二者互不冲突（一个 run 要么 waiting 出 pending 卡、要么已答出 answered 卡）。

**R2 验证（S4 必做）**：grep `turnsToHistoryMessages` 及所有 `agent_run.messages` 的 reader，确认只读 `role`/`content`/已知字段，`question_answer` 被忽略（遵循「加字段对 6+ readers 安全」既有结论）。

### 1.3 前端改动
- `types/agent.ts:QuestionPromptItem` 加 `answer?: string`。
- `QuestionPrompt.vue` answered 折叠卡（378-385）：`resolvedAnswer(i)` 改为优先 `props.questions[i].answer ?? 既有 state 推导 ?? '已回答'`。新增 `displayAnswer(i)` computed。
- 快照恢复路径（`agentChat.ts:loadSessionSnapshot`）已直接用后端 messages，question_prompt(answer_status=answered) 会被 AgentMessageItem 渲染为 answered 卡——无需额外改动，只需 items 带 answer。
- 验证 AgentMessageItem 把 `answer_status==='answered'` 映射到 `<QuestionPrompt :answered="true">`（确认现有映射）。

---

## 2. Issue 2 — 工具调用轻量绿色背景

### 2.1 根因
`AgentToolCallItem.vue:.tl-line`（~54-62）无 background/圆角（agent-process-timeline 故意削平为扁平时间线）。

### 2.2 设计
- 在 `.tl-line` 加：低透明度绿底 + 圆角 + 适度内边距，保持单行扁平：
  - `background: var(--color-primary-ultra-soft, hsla(...))` 或新 token `--color-primary-soft-bg`（与翠绿活信号 `--primary` 同源，透明度 ~6-8%）。
  - `border-radius: 6px;` `padding: 4px 8px;`（不破坏单行节奏）。
- 不加 box-shadow / border / 厚 padding（避免 card chrome 回归）。
- active / error 态保持既有左色条/图标语义；绿底是中性分隔层，error 行可叠加既有 error 视觉。
- reduced-motion 无关（纯静态）。
- 验证：`AgentToolCallList` 多行相邻时分隔清晰，不出现「巨卡」。

---

## 3. Issue 3 — 任务完成后残留转圈

### 3.1 假设根因（待 dev 运行时确证 — §6）
字面「等你回答一个问题」搜不到，疑似：
- (a) `AgentRunPulse`「处理中…」呼吸点在续跑终态后未隐藏（`visible = isRunning && !isWaitingForUser && !lastMsgStreaming`，若 `currentRun.status` 未达干净 terminal 则 isRunning 残留 true）；或
- (b) stale `currentRun.state_reason === 'waiting_for_user_choice'` 致 `isWaitingForUser` 残留 true。

### 3.2 设计
- **主修来自 issue 4**：答题续跑改 SSE 后，续跑 leg 以 `terminal` 事件收尾 → `applyStreamEvent` case 'terminal'（agentChat.ts ~1160-1200）走 `statusFromTerminalReason` + `reconcileFromDB` + finalize → 干净清掉 isRunning/waiting/计时器。当前轮询路径缺这个干净收尾。
- **独立加固 task**：审计「run 达 terminal 时清所有活信号」：
  - 终态时确保 `currentRun.state_reason` 不再是 waiting（reconcile/refresh 覆盖）。
  - `finalizeToolGroups`（agentChat.ts:417-433）在终态翻残留 in-flight 工具——确认流式 terminal 也调用它。
  - `AgentRunPulse.visible` 在 terminal 后为 false（依赖 isRunning=false）。
- **运行时确证**：dev 部署后 browse + E2E 实跑「提问→答题→续跑→完成」，确认无残留转圈；若发现确切元素与假设不符，按实测调整（§6 硬规则，不静态硬猜）。

---

## 4. Issue 4 — 答题后 narration 流式续跑

### 4.1 根因（已确证）
`AgentChatView.vue:handleAnswerSubmitted`（95-107）答题后走 `narration.start()`（500ms 轮询 narration 事件）+ `runCtrl.startStatusPolling()`（5s 轮询 status），**不重开 SSE**。轮询只拉工具 narration 事件 + 末尾 final_answer，不流式助理正文 → 续跑段无正文 narration。

### 4.2 后端设计：新增流式答题续跑端点

**API 契约**：
```
POST /v1/agent-runs/:id/answer-stream
Content-Type: application/json
Body: { "answers": { "<question text>": { "selected": ["..."], "free_text": "..." } } }  // 同 AnswerRequest
Response: text/event-stream（同 POST /v1/agent-runs/stream 的 SSE 事件流）
  - 验证失败 → error 事件 {code, message} 后关闭
  - 409 → 已有活跃 stream（单订户锁）→ 前端回退轮询
  - 成功 → stream_start … token_delta/reasoning_delta/tool_call_*/question_prompt … terminal
```
- 注册：`router.go` 用户端，user_token 中间件。
- Controller：`controller/v1/agent/student_run_stream.go` 新增 `AnswerStream` handler，复用 CreateStream 的 SSE pump（抽公共 pump 或复制最小逻辑）。
- Biz：`StudentRunService.AnswerStream(ctx, userID, runID, req AnswerRequest, ch chan<- stream.Event)`：
  1. 复用 `Answer` 的全部校验（ownership / state==waiting / pending not null / 每个 answer 合法）——抽 `validateAndPersistAnswer(ctx, userID, runID, req) (resumeHistory, userMsg, err)` 供 Answer 和 AnswerStream 共用，避免逻辑分叉。
  2. 持久化（AnswerAndClear，含 issue 1 的 question_answer turn）。
  3. `go s.forwardNarration(runID)`（保留，narration provider bridge）。
  4. `return s.runner.RunStream(ctx, runReq, runID, ch)`（runReq.ExistingRunID=runID, Input=userMsg, History=resumeHistory）——流式续跑。
- **重构**：把 `Answer` 的校验+持久化+resumeHistory 构建抽成共享 helper；`Answer`（旧轮询路径）保留，调用 helper 后走 detached `runner.Run`；`AnswerStream` 调用 helper 后走 `runner.RunStream`。两路径共享 SOT，降低分叉风险（R1）。

**Langfuse**：`runner.RunStream` 既有 trace/generation 集成自动覆盖续跑 leg；附 `resume=true` / run_id / session_id 元数据（若 RunStream 已注入则复用）。不新增裸 LLM 调用（合规 §0/§1）。

### 4.3 前端设计
- `api/agent-stream.ts` 新增 `answerAndResumeStream(runId, answers, onEvent, signal)`：POST `/v1/agent-runs/:id/answer-stream` + `readAgentSSEStream`（复用 streamAgentRun 的 SSE 读取/解析/409 处理）。
- `QuestionPrompt.vue`：`submitAnswers` 改为**不再自己** `postAgentAnswer`，而是把 answers 通过 `emit('answer-submitted', answers)` 上抛（emit 签名加 payload）。乐观折叠仍由父层 markQuestionAnswered 处理。
  - 保留：提交中 spinner（submitting 态）。
- `AgentChatView.vue:handleAnswerSubmitted(runId, answers)`：
  1. `store.ensureCurrentRun(runId)` + `store.markQuestionAnswered(runId)`（乐观）。
  2. 开 SSE：`useAgentStream.startResume({runId, answers})`（新增）或直接调 `answerAndResumeStream(runId, answers, store.applyStreamEvent, signal)`。
  3. catch 409/网络失败 → 回退现有 `postAgentAnswer(runId,{answers}) + narration.start() + startStatusPolling()`（fallback 路径）。
- `applyStreamEvent` 复用——续跑 leg 的 token_delta/reasoning_delta 累积成 AssistantMessage 正文（与首段一致）→ issue 4 解决。
- `useAgentStream`：新增 `startResume` 包装 `answerAndResumeStream`，复用现有 terminal/error/reconcile 收尾。
- 边界：续跑 leg 再 yield question_prompt → applyStreamEvent question_prompt case 自动渲染新卡（Q→A→Q 链不断）。

### 4.4 与 issue 1 的协同
- issue 4 的流式续跑只影响**内存中**的实时渲染；issue 1 的 question_answer turn 持久化保证**刷新后**重建。二者正交但都让答题体验闭环。

---

## 5. 验证策略（S5，详见 S3 plan 的专项 task）
- **方式**：后端 Go 单测（issue 1 transformMessages 重建 / buildAnswerTurn / AnswerStream 校验复用；issue 4 端点契约）+ 前端 vitest（QuestionPrompt answer 渲染 / agentChat 流式续跑事件）+ **dev 部署后 browse + E2E 实跑**（issue 2/3 视觉与终态 + issue 4 流式正文 + issue 1 刷新重建）。
- **理由**：issue 1/3/4 是 dev 实跑观察的 bug，Rule 11 需回归测试；issue 2/3 视觉与终态须运行时眼见为实（§6，既有 agent-mode feature 标准姿势：browse + E2E 凭据登录 dev）。
- **关键用户路径**：
  1. 发起 agent run → agent 调 ask_user_question → 出 pending 卡。
  2. 答题提交 → 续跑期间正文 narration 流式持续出现（issue 4）。
  3. 续跑完成 → 无残留转圈（issue 3）+ 已答折叠卡。
  4. 刷新页面 → 已答折叠卡保持（含真实答案），无孤立 user 气泡（issue 1）。
  5. 全程工具行有绿底分隔（issue 2）。

## 6. 不做 / 非目标
- 不改 DB schema。
- 不回溯修历史 run 的 answer turn（forward-only）。
- 不删除旧 postAgentAnswer + 轮询路径（保留作 fallback）。
- 不改 SSE 事件类型 / wire 格式（全复用）。
