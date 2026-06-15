# Agent Mode 问题卡 / Narration / 工具卡 体验修复 — 实施计划（agent-qa-card-ux）

> NDF Standard S3 plan。基于 spec `2026-06-15-agent-qa-card-ux-design.md`。后端 task 先于前端。每个 task 完成后双 Sonnet review（spec-compliance + code-quality 并行）。

## 依赖图（无环）
```
T1(后端 issue1 持久化) ──→ T2(后端 issue4 流式续跑端点, 复用 T1 的 AnswerAndClear/buildAnswerTurn)
T1 ──(数据形状契约)──→ T3(前端 issue1 渲染)
T2 ──(端点契约)──→ T5(前端 issue4 流式客户端)
T4(前端 issue2 CSS) 独立
T6(前端 issue3 终态收尾) 依赖 T5 的流式 terminal 路径(同改 agentChat.ts, 串行)
T7 = S5 验证策略(文档 task)
```
实现顺序（串行，单 session，避免同文件 git race）：T1 → T2 → T4 → T3 → T6 → T5 → T7。
（T5 放最后：触达文件最多 QuestionPrompt.vue + agentChat.ts + agent-stream.ts + AgentChatView + useAgentStream + AgentMessageList，整合收口。）

---

## T1 — 后端 issue1：答案 turn 内嵌 question_answer + 重建已答卡
**仓库**：numind-server
**Rule 11**：起因为 dev 实跑 bug → 第一个 commit 必须是失败复现测试。
- commit1 `test(qa): reproduce answered question card renders as user bubble on reload`：Go 测试——构造一个 run，messages 含一条 `{role:user, content:..., question_answer:{questions:[{question,answer,...}]}}` turn，断言 `transformMessages` 应 emit `question_prompt`(answer_status="answered", questions[].answer 非空) 且**不** emit user 气泡。当前实现 FAIL（渲成 user 气泡）。
**改动文件**：
- `internal/numind/store/agent_run.go`：`AnswerAndClear(ctx, id, userMessage string)` → `AnswerAndClear(ctx, id, turn json.RawMessage)`；store 不再自 marshal，直接 append 传入的 turn。同步 `IAgentRunStore` interface（55-59 注释 + 签名）。
- `internal/numind/store/agent_run_resume_test.go`：更新调用方为新签名。
- `internal/numind/biz/agent/answer.go`：新增 `buildAnswerTurn(pending YieldPayload, answers map[string]AnswerItem) (json.RawMessage, error)`——构造 `{role:"user", content:buildAnswerMessage(...), question_answer:{questions:[{question,header,multi_select,options,answer:resolveAnswer(item)}]}}`（只含已答题，与 buildAnswerMessage 顺序一致）；**（P1-A 修正）`Answer()` 内现有 `AnswerAndClear(ctx, runID, userMsg string)` 调用点必须在 T1 内一并改为 `AnswerAndClear(ctx, runID, turn json.RawMessage)`（turn=buildAnswerTurn 产物），否则 T1 commit 编译失败**。
- `internal/numind/biz/agent/student_query.go`：(a) `questionPromptItem` 加 `Answer string \`json:"answer,omitempty"\``；(b) `transformMessages` role=="user" 分支——解析 `turn["question_answer"]`，若含 ≥1 question 则 emit `agentMessage{Type:"question_prompt", RunID:runID, Questions:[...Answer], AnswerStatus:"answered", Timestamp:ts}` 并 `continue`（跳过 user 气泡）；否则维持现状。
**R2 验证（task 内必做）**：grep `turnsToHistoryMessages` 及 agent_run.messages 所有 reader，确认只读 role/content，`question_answer` 被忽略；结论写入 commit message。
**验收**：复现测试转 PASS；`go test ./internal/numind/store/... ./internal/numind/biz/agent/...` 0 FAIL；`task lint` 0。系统可编译。

## T2 — 后端 issue4：流式答题续跑端点
**仓库**：numind-server（依赖 T1 的 AnswerAndClear 新签名）
**Rule 11（P1-B 修正）**：issue4 起因为 dev 实跑 bug → 第一个 commit 必须是失败复现测试。
- commit1 `test(qa): reproduce answer-stream resume missing (404, backend)`：Go 测试断言 `POST /v1/agent-runs/:id/answer-stream` 端点存在且 AnswerStream 复用校验（路由未注册时 404 / handler 未实现时 FAIL）；实现后转 PASS。
**改动文件**：
- `internal/numind/biz/agent/answer.go`：抽 `validateAndPersistAnswer(ctx, userID, runID, req) (resumeHistory []*..., userMsg string, run *model.AgentRun, err error)`——把 Answer() 的 ownership/state/pending/逐项校验 + AnswerAndClear(含 buildAnswerTurn) + resumeHistory 构建抽出；`Answer`(旧轮询) 调 helper 后走 detached `runner.Run`；新增 `AnswerStream(ctx, userID, runID, req, ch chan<- stream.Event) (*RunResult, error)` 调 helper 后 `go forwardNarration(runID)` + `runner.RunStream(ctx, runReq, runID, ch)`。
- `internal/numind/controller/v1/agent/student_run_stream.go`：新增 `AnswerStream` SSE handler——复用 CreateStream 的 SSE pump（抽 `pumpSSE(c, eventCh, ...)` 公共函数或最小复制）；获取该 runID 的单订户锁（不新建 run 行，复用已存在的暂停 run；若无专用 lock 入口则加 `AcquireResumeStreamLock(runID)` 或复用既有锁逻辑）；调 `runSvc.AnswerStream`。
- 路由注册：`student_run.go`（流式路由所在）或 `router.go` 加 `POST /v1/agent-runs/:id/answer-stream`（user_token）。
- 测试：`answer_test.go` / 新 `answer_stream_test.go`——AnswerStream 复用校验（非 waiting 拒绝、pending null 拒绝、answer 合法性）、调 RunStream（mock runner）、旧 Answer 轮询路径不回归。
**验收**：`go test ./internal/numind/...`（agent + controller）0；`task lint` 0；端点在 router 注册（grep 确认）；旧 Answer 路径单测仍 PASS。

## T3 — 前端 issue1：已答卡渲染真实答案
**仓库**：numind-web-v3（依赖 T1 数据形状）
**前置确认（P1-C 修正）**：先读 `src/components/agent/AgentMessageItem.vue`，确认 `question_prompt` 且 `answer_status==='answered'` → `<QuestionPrompt :answered="true" :questions="...">` 的映射存在；若映射缺失/有误，纳入 T3 改动范围（否则 displayAnswer 改造永不被触达，issue1 静默未修）。
**改动文件**：
- `src/types/agent.ts`：`QuestionPromptItem` 加 `answer?: string`。
- `src/components/agent/QuestionPrompt.vue`：新增 `displayAnswer(i)`：优先 `props.questions[i]?.answer`，回退既有 `resolvedAnswer(i)`(live state)，再回退 `'已回答'`；answered 列表（378-385）改用 `displayAnswer(i)`。
- （如 P1-C 确认需要）`src/components/agent/AgentMessageItem.vue`：修正 answered 映射 + 透传 questions(含 answer)。
- `src/components/agent/__tests__/QuestionPrompt.spec.ts`：加用例——`answered=true` + questions 带 answer → 渲染真实答案（非"已回答"占位）。
**验收**：vitest 相关 spec PASS；`npm run type-check` 0；`npm run lint` 0；AgentMessageItem answered 映射确认到位。

## T4 — 前端 issue2：工具调用轻量绿色背景
**仓库**：numind-web-v3（独立）
**改动文件**：
- `src/components/agent/AgentToolCallItem.vue`：`.tl-line` 加 `background`(低透明度翠绿, 与 --primary 同源, ~6-8%) + `border-radius:6px` + 适度 `padding`；保持单行扁平，不加 border/box-shadow/厚 padding。active/error 态视觉不被绿底吃掉。
- （可选）`AgentToolCallList` / 全局 token：若需新 token `--color-primary-soft-bg` 则加到 token 定义处。
**验收**：`npm run lint` + `npm run type-check` 0；视觉 S5 dev 实跑确认（相邻工具分隔清晰、无巨卡/card chrome 回归）。

## T6 — 前端 issue3：run 终态清所有活信号
**仓库**：numind-web-v3
**Rule 11**：dev 实跑 bug → 复现测试。
- commit `test(qa): reproduce stuck spinner after run terminal`：vitest——模拟答题后续跑 + terminal 事件/状态，断言终态后 `isRunning===false`、`isWaitingForUser===false`(state_reason 非 waiting)、in-flight 工具被 finalize。先 FAIL（若现状残留）。
**改动文件**：
- `src/stores/agentChat.ts`：terminal 处理（applyStreamEvent case 'terminal' + refreshRunStatus 终态分支）确保清 waiting 态（state_reason 覆盖）+ 调 `finalizeToolGroups`；流式 terminal 路径也调 finalizeToolGroups（确认/补齐）。
- `src/components/agent/AgentRunPulse.vue`：确认 `visible` 在终态为 false（依赖 isRunning=false）。
**验收**：复现测试转 PASS；vitest 全量 0 FAIL；type-check/lint 0；S5 dev 实跑确证无残留转圈（§6，若确切元素与假设不符按实测调整）。

## T5 — 前端 issue4：流式答题续跑客户端
**仓库**：numind-web-v3（依赖 T2 端点契约；触达文件最多，放最后整合）
**Rule 11**：dev 实跑 bug → 复现测试。
- commit `test(qa): reproduce no narration prose after answering (poll-only resume)`：vitest——断言答题后走流式（applyStreamEvent 收到 token_delta 累积成 assistant 正文），而非仅轮询。先 FAIL。
**改动文件**：
- `src/api/agent-stream.ts`：新增 `answerAndResumeStream(runId, answers, onEvent, signal)`——POST `/v1/agent-runs/:id/answer-stream` + 复用 `readAgentSSEStream`/`parseAgentSseChunk`/409 处理。
- `src/composables/useAgentStream.ts`：新增 `startResume({runId, answers})` 包装 answerAndResumeStream，复用 terminal/error/reconcile 收尾。
- `src/components/agent/QuestionPrompt.vue`：`submitAnswers` 不再自调 `postAgentAnswer`，改 `emit('answer-submitted', buildAnswers())`；emit 签名 `'answer-submitted': [answers]`。保留 submitting spinner。**（P2-A）改此文件时保留 T3 已引入的 `displayAnswer(i)` 逻辑，不要覆盖。**
- `src/components/agent/AgentMessageList.vue` + `AgentMessageItem.vue`：转发 `answer-submitted` 的 payload（answers）。
- `src/views/agent/AgentChatView.vue`：`handleAnswerSubmitted(runId, answers)`——ensureCurrentRun + markQuestionAnswered（乐观）→ 开 SSE startResume；catch 409/网络失败 → 回退 `postAgentAnswer + narration.start() + startStatusPolling()`(既有 fallback)。
- 测试：`__tests__/agentChat-resume.spec.ts` / `AgentChatView.spec.ts` / `QuestionPrompt.spec.ts` 更新。
**验收**：复现测试转 PASS；vitest 全量 0；type-check/lint 0；fallback 路径保留可用。
**（P2-C）Q→A→Q 链**：续跑 leg 再 yield question_prompt 由既有 `applyStreamEvent` 的 `question_prompt` case 自动渲染新卡，无需额外代码；验收时确认（流式续跑复用同一事件管线）即可，不单列实现。

## T7 — S5 验证策略（Rule 10 专项 task）
- **方式**：后端 Go 单测（T1/T2）+ 前端 vitest（T3/T5/T6）+ **dev 部署后 browse + E2E 凭据实跑**（issue 1 刷新重建 / issue 2 绿底 / issue 3 终态 / issue 4 流式正文）。
- **理由**：issue 1/3/4 是 dev 实跑 bug，Rule 11 需持久化回归测试（Go/vitest 提供回归保护）；issue 2/3 视觉与终态 + issue 4 流式体验须运行时眼见为实（§6，agent-mode feature 标准姿势）。纯 gstack /qa 一次性截图不够（无回归保护），故核心逻辑用 Go/vitest 锁，运行时用 browse 确证体验。
- **关键用户路径**（dev 实跑）：
  1. 发起含 ask_user_question 的 agent run → 出 pending 问题卡。
  2. 答题提交 → 续跑期间正文 narration 流式持续（issue 4）。
  3. 续跑完成 → 无残留转圈（issue 3）+ 已答折叠卡。
  4. 刷新 → 已答折叠卡保持(含真实答案)、无孤立 user 气泡（issue 1）。
  5. 全程工具行绿底分隔、无巨卡（issue 2）。
- **回归保护诚实声明（P2-B 修正）**：issue 1（Go transformMessages/buildAnswerTurn + vitest QuestionPrompt）、issue 3（vitest T6 复现测试）、issue 4（Go AnswerStream + vitest 流式续跑）均有**持久回归保护**；issue 3 额外 browse 运行时双确证；issue 2（纯视觉 CSS）仅 browse 一次性确证（视觉回归无自动保护，纯样式低风险可接受）。
- **环境**：S5 优先本地（task dev + npm run dev）；若本地复现 ask_user_question 全流程受限（需 LLM/积分），则在 S6 ndf-done + /deploy-dev 后于 dev 用 browse + E2E 凭据实跑确证（既有 agent-mode feature 既定做法），QA 报告如实标注各项验证位置。

## 风险与缓解（承自 S2）
- R1 流式回归：旧 poll 路径保留 + 前端 fallback。
- R2 turn 内嵌字段污染 LLM 历史：T1 grep 验证。
- R3 issue3 根因未确证：T6 + S5 dev 实跑。
- R4 续跑 leg trace：复用 RunStream 既有 Langfuse（resume leg 自带 trace，可接受）。
