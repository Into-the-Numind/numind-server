# Spec — answer-resume-lifecycle

## F1 状态真相（numind-server）

### F1a `store/agent_run.go` AnswerAndClear（L366-371 的 UPDATE map）

```go
result := tx.Model(&model.AgentRun{}).Where("id = ?", id).Updates(map[string]interface{}{
    "messages":              datatypes.JSON(newMsgs),
    "pending_question_json": nil,
    "pending_question_at":   nil,
    "state_reason":          "running",
    "status":                "running", // NEW: 答题瞬间行变回运行中（根治 run148 假终态）
    "ended_at":              nil,       // NEW: yield 写入的 ended_at 必须清掉
})
```

### F1b `biz/agent/runner.go` ExistingRunID 接管分支（L440-445 之后）

加防御性校正（幂等，覆盖 Answer 之外的潜在 resume 入口；与 AnswerAndClear 后的行状态一致）：

```go
if uerr := r.runStore.UpdateState(ctx, run.ID, "running", "running", nil); uerr != nil {
    log.Warnw("AgentRunner.Run: mark resumed run running failed", "agent_run_id", run.ID, "error", uerr)
}
```

复用现有 `UpdateState`（5 处既有调用，签名不动；endedAt=nil 不触碰 ended_at——清空由 F1a 负责）。失败仅告警不终止（行状态已由 F1a 保证主路径正确）。

## F2 续跑追加持久化（numind-server `biz/agent/runner.go`）

新纯函数（同包新文件 `resume_transcript.go`，可独立单测）：

```go
// mergeResumeTranscript prepends a resumed run's pre-yield transcript to the
// current leg's turns. prior is the agent_run.messages value captured at
// ExistingRunID takeover (leg1 turns + the answer user message appended by
// AnswerAndClear). When the current leg's first turn is the same user message
// that prior already ends with (the resume Input), it is dropped to avoid
// duplication. Empty/invalid prior → turns unchanged (non-resume runs are a
// strict no-op).
func mergeResumeTranscript(prior json.RawMessage, turns []map[string]any) []map[string]any
```

- 接管处捕获：`var priorMessages json.RawMessage; if req.ExistingRunID != 0 { priorMessages = existing.Messages }`
- **Run 内所有 WriteTurn 调用点**（短路径 ~L923 与完整路径 ~L1359，grep 确认仅此两处）落库前统一 `turns = mergeResumeTranscript(priorMessages, turns)`
- **排除声明（S3 reviewer P2）**：runner_stream.go 的 persistYieldTranscript（~L395/L546）自带 multi-yield merge（HW-33），且 resume 只走 runner.Run 非 RunStream（answer.go:167 实证），不在本 feature 范围
- 去重判据：`turns[0].role=="user" && turns[0].content == prior末turn.content && prior末turn.role=="user"`
- prior 解析失败 / 为空 / "[]" → 原样返回 turns（非续跑零变化）

## F3 前端终态守卫（numind-web-v3 `stores/agentChat.ts`）

resume 签名 = `state_reason === 'running'`（仅 AnswerAndClear/接管会写入此组合；正常完成是 'completed' 等真终因）。

- `refreshRunStatus`（~L358）：
  - `const isResuming = next.state_reason === 'running' && next.status !== 'running' && next.status !== 'pending'`
  - isResuming 时 `currentRun.value = { ...next, status: 'running' }`（保持轮询/narration 存活——老后端兼容），否则照旧赋值
  - final_answer 推送条件追加 `&& !isResuming`
- `reconcileFromDB`（~L724 的 final_answer push）：条件改为 `finalOut && run.state_reason !== 'waiting_for_user_choice' && run.state_reason !== 'running'`
- **（S3 reviewer P1 澄清）全文件只有两处 final_answer 推送点**：refreshRunStatus（~L379）与 reconcileFromDB（~L724）。不存在第三处"next handler"——L368 是 refreshRunStatus 体内的 isTerminal 变量本身。两处都必须加 resuming 守卫，reconcileFromDB 是 SSE 流式终态的必经路径，遗漏即流式场景仍假终
- **（S3 reviewer P1 边界标注）**`statusFromTerminalReason`（~L103）没有 'running' 分支，未知值 fallback 'failed'。当前 resume 只走轮询（runner.Run 非 RunStream）故不受影响；本 feature 不触 RunStream。未来若做流式 resume，必须先给该函数加 'running' 映射

## F4 二次提问注入（numind-web-v3 `stores/agentChat.ts` refreshRunStatus 内）

```
if next.state_reason === 'waiting_for_user_choice'
   && messages 中不存在 (type==='question_prompt' && run_id===next.id && answer_status==='pending'):
    snap = await api.getSessionSnapshot(next.session_id)   // src/api/agent.ts:65，loadSessionSnapshot 同款（今日 hotfix 已验证）
    qp = snap.messages.find(m => m.type==='question_prompt' && m.run_id===next.id)
    if (qp) messages.push({...qp 映射为 QuestionPromptMessage, answer_status:'pending'})
```

- 复用后端 `synthesizeQuestionPrompt`（今日 hotfix 已验证 options 永远为数组）
- 幂等：pending 卡片存在即跳过；注入失败静默（下轮 poll 重试）
- isWaitingForUser 的现有派生逻辑不动（确认其来源，若派生自 question_prompt 消息则注入即生效）

## 风险敞口（S3 reviewer P2，有意接受）

F1a 之后，"AnswerAndClear 已执行但 detached goroutine 未启动就进程重启"的孤儿窗口，run 将永久滞留 status='running'（假活），替代修复前的 terminated+running（假终）。无新增数据损坏风险；当前无 watchdog，运维以 updated_at 超时的 running run 人工识别。与 Create 路径的预建 row + 异步派发暴露面一致，可接受；watchdog 为独立 follow-up。

## 不变量

- I1: 状态机 19 TerminalReason 不增不改；yield 仍写 terminated+waiting_for_user_choice
- I2: 非续跑 run（ExistingRunID==0）的持久化字节级行为不变
- I3: AnswerAndClear 仍是单事务原子操作
- I4: 前端对 status='running' 的现有路径（流式、narration、stuck 检测）行为不变
- I5: F4 注入的卡片可正常作答（走既有 postAgentAnswer 流程）

## 测试规格

- Rule 11 复现（先 RED）：
  - server：`TestAnswerAndClear_MarksRunResumed`——构造 status='terminated'/state_reason=waiting/ended_at 非空的行，AnswerAndClear 后断言 status='running' 且 ended_at IS NULL（现 RED）
  - web：`agentChat-resume.spec.ts`——mock getRun 返回 {status:'terminated', state_reason:'running', final_output:'提问前旧文案'}，断言不推 final_answer 且 currentRun 仍视为活跃（现 RED）
- T2：`mergeResumeTranscript` 表驱动（空 prior / 续跑去重 / 首 turn 非 user / prior 解析失败 / 多轮 prior）
- T4：vitest——waiting 且无卡片时从快照注入一次且幂等
