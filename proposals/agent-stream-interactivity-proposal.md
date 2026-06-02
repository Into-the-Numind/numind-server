# agent-stream-interactivity — Proposal + Design + Plan（S1–S3 合并）

> Standard track，bug-fix 性质、设计明确、风险可控，故 S1 proposal + S2 design + S3 plan 合并为本文档。S0 见 `requirements/agent-stream-interactivity.md`。

---

## A. 设计决策（S2）

### A1. 后端：流式路径截获 yield（BLK-4 核心）
`ask_user_question.Execute` 返回 `*yieldError`（sentinel）。流式下工具经 `fullToolEinoAdapter.InvokableRun` 执行，该 error 冒泡（eino `tool_node.go` `return nil, err`），最终在 `consumeEinoStream` 的 `sr.Recv()` error 分支出现。为**不依赖 eino 对 error 的包装方式**（鲁棒），双保险：

1. `StreamSessionState` 加字段 `PendingYield *YieldPayload`（`runner_runstream.go`）。
2. `InvokableRun`（`adapter_full_to_eino.go`）：`errors.As(execErr, &yieldErr)` 命中 → 把 `&yieldErr.Payload` 写入 ctx 的 `StreamSessionState.PendingYield`（**源头捕获**），仍返回 error 让 graph 停。
3. `consumeEinoStream`（`runner_stream.go`）：
   - **error 分支**：在 `HandleLLMError`/model_error 分类**之前**检查 `state.PendingYield != nil`（兼存 `errors.As(err,&yieldErr)`）。命中 → `r.runStore.UpdatePendingQuestion(...)` 持久化 + `emit(EventQuestionPrompt, 结构化 payload)` + `st.TerminalReason = TerminalWaitingForUserChoice` + `emit(EventTerminal, reason=waiting_for_user_choice)` + return RunResult{waiting}（**nil error**）。
   - **EOF 分支**：置 `TerminalCompleted` 前同样检查 `PendingYield`（防御：yield 未以 error 冒泡而流正常结束）。
4. `RunStream`：检测 `result.TerminalReason == waiting_for_user_choice` → 仅 `UpdateState(terminated, waiting_for_user_choice)` + **提前返回，跳过 finalizeRun**（对齐非流式 Run 路径：yield 时不 WriteTurn/不 enqueue extractor/不 index；消息在 resume 时由 answer.go 续写）。

约束：不新增 `TerminalReason`/`LoopEvent`（复用 `waiting_for_user_choice` / `LoopEventAskUserPaused`）；**不动非流式 Run 路径**。

### A2. 后端：`frontendStatus` 映射 waiting（支撑 A3/A4）
`student_query.go::frontendStatus`：加 `case "waiting_for_user_choice": return "running"`。当前缺此 case → `default → "failed"`（latent bug，polling 路径也受影响）。修后 `getRun` 对暂停 run 返回"运行中"而非"失败"。

### A3. 前端：`currentRun` 乐观占位（BLK-5）
`agentChat.ts::applyStreamEvent` 的 `stream_start` 分支：set `currentRun = { id:e.run_id, session_id:<payload>, status:'running', state_reason:'running', credits_used:0, credits_budget:0, credits_threshold_state:'under_60' }`。使顶栏状态徽标 / 取消按钮 / budget 计算在流式期间生效。写入 `session_id` 触发既有 route replace（对齐 `startNewRun`）。
> 注：budget 60%/100% 实时预警还依赖后端流式推送 credits 用量（当前无）+ 真实计费（BLK-2，已 out-of-scope）；本 feature 只保证 currentRun 在位、状态/取消可用，budget 阈值待 BLK-2 修复后另接。

### A4. 前端：`question_prompt` 修正 + terminal waiting（BLK-4 前端）
- `question_prompt` 分支：options **透传结构化** `{key,label,description}`（当前误作 `{label:opt}`，与 `YieldOption` 不符）。
- `terminal` 分支：`reason==='waiting_for_user_choice'` → 不标 `failed`；保持 currentRun active（reconcile 安全，因 A2）。

### A5. 前端：答题后 resume（BLK-4 前端）
`QuestionPrompt` 答题成功 emit `answer-submitted` → AgentMessageItem → AgentMessageList → `AgentChatView` 监听 → `runCtrl.startStatusPolling()` + `narration.start()`。resume 后续输出经 polling 路径（answer.go 重跑非流式 Run，narration + final 落库）回灌。

---

## B. 任务计划（S3）

| Task | 仓库 | 主要文件 | 独立验收 |
|---|---|---|---|
| **T1** | web-v3 | `stores/agentChat.ts`（stream_start 占位 currentRun） | vitest：stream_start 后 currentRun.status='running'、id 正确 |
| **T2** | server | `biz/agent/student_query.go`（frontendStatus waiting→running） | go test：waiting_for_user_choice → "running"，其它 case 回归 |
| **T3** | server | `stream/events.go`（QuestionPromptPayload）+ `runner_runstream.go`（PendingYield 字段 + RunStream 提前返回）+ `adapter_full_to_eino.go`（捕获 PendingYield）+ `runner_stream.go`（consumeEinoStream 截获 yield） | go 集成测：mock LLM+mock yield 工具 → 流式 → pending 持久化 + question_prompt 发射 + waiting 终态；**回归**：正常 completed 流不变 |
| **T4** | web-v3 | `stores/agentChat.ts`（question_prompt options 透传 + terminal waiting）+ `types/agent-stream.ts`（QuestionPromptPayload 对齐） | vitest |
| **T5** | web-v3 | `QuestionPrompt.vue`/`AgentMessageItem.vue`/`AgentMessageList.vue`/`AgentChatView.vue`（resume 接线） | vitest + Playwright e2e |

**依赖**：T3 定义 SSE payload 契约 → T4 消费 → T5 接线。T1/T2 独立。
**顺序**：T1（快赢）→ T2 → T3 → T4 → T5。
**文件归属**（无交集，但 T1/T4 同改 agentChat.ts → 串行做，不并行）。

---

## C. S5 验证策略（final — NDF Rule 10）

- 后端：T2/T3 `go test`（集成 mock LLM + mock yield 工具）；回归 completed 流（文本不重复、seq 单调、terminal=completed）。
- 前端：T1/T4 vitest；T5 vitest + **Playwright e2e**（流式 `ask_user_question` 渲染 → 答题 → 续答 端到端）。
- 高风险判定：核心交互路径 → 强制 Playwright e2e。回归保护：go + vitest + Playwright 均永久。

---

## D. 风险与回归护栏

- **不动非流式 Run yield**（对照基线，确保不回归 polling 路径）。
- **不新增 enum**。
- eino error 包装不确定性 → 用 `PendingYield` ctx 源头捕获，不依赖 `errors.As` 穿透 eino graph 包装。
- 正常 `completed` 流回归测必跑（T3 含）。
- T3 触及核心 run loop，改动严格局部于 yield 分支，completed/error 路径零改动。
