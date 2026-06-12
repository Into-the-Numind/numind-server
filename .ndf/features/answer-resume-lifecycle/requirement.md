# Requirement Card — answer-resume-lifecycle

- **ID**: answer-resume-lifecycle
- **Track**: Standard（跨 2 仓库、run 生命周期核心语义、影响文件 >3）
- **Date**: 2026-06-12
- **起因（Bug-from-Customer，Rule 11）**: 用户报告"agent 提问→我答题→agent 回了一句旧话就结束了"，且自述"出现了无数次"。

## 事故证据（dev run 148，2026-06-12 22:00-22:08）

- 用户答题后，UI 立即把**提问前的旧文案**（"现在我需要向你确认一些网上查不到的细节"）渲染成最终答案 + 反馈 footer，停止跟踪
- 后端实际**继续干了 8.5 分钟**：38 次 web_search + image_gen + create_png_chart + run_python，22:08:41 产出完整 DOCX 报告写入 DB——无人观看
- 续跑落库**覆写** messages：run 148 只剩 3 条消息（答案、tool_group、报告），第一段历史（原始 prompt、首轮搜索、提问）被冲掉

## 根因链（每环有代码+DB 实证）

1. **yield 暂停被实现为"终止"**：runner 终态路径 `UpdateState(…,"terminated", waiting_for_user_choice, &endedAt)`（runner.go:1424）
2. **答题只改 state_reason 不改 status**：`AnswerAndClear` 的 UPDATE map 只有 messages/pending/state_reason="running"（store/agent_run.go:366-371），status 仍是 terminated
3. **续跑接管不翻转 status**：runner.Run 的 ExistingRunID 分支只 load 不更新；`Status:"running"` 只在新建分支（runner.go:451）。整个续跑期间行内 status='terminated' + state_reason='running'
4. **前端终态判定只看 status**：`isTerminal = next.status !== 'running' && next.status !== 'pending'`（agentChat.ts:368 与 reconcileFromDB:718 同类）；waiting 守卫只豁免 `waiting_for_user_choice`，不豁免 'running' → 答题后一轮询即判终，把 final_output（= 提问前最后一条 assistant 文本）推成 final_answer
5. **墓碑证据**：run 141（2026-06-12 00:52）永久冻结在 status=terminated + state_reason=running（续跑中途崩溃留下的现场），证明该矛盾组合真实存在

## 关联缺口（本 feature 一并修）

- **G1 历史覆写**：终态持久化 `buildTranscriptTurns(userInput,…)` 只构建本段 + `WriteTurn` 整体覆写（runner.go:1346-1360）→ 续跑冲掉第一段
- **G2 二次提问不可见**：续跑走非流式 runner.Run，narration 轮询的 `tool_call_yield.yield_payload` 后端不填（agentChat.ts:313 注释自证）→ 若续跑再次提问，无任何机制注入问题卡片（Q→A→Q 链在轮询模式断裂）

## 验收标准

- AC1（Rule 11 复现，先 RED 后 GREEN 永久留库）：(a) AnswerAndClear 后 status 必须为 running、ended_at 清空；(b) 前端收到 {status:'terminated', state_reason:'running'} 不得推 final_answer、必须继续跟踪
- AC2：续跑接管后行内 status='running'（防御性校正，独立于 AnswerAndClear）
- AC3：续跑终态落库 = 第一段历史 + 答案消息 + 续跑段（无重复、无丢失）；非续跑 run 行为零变化
- AC4：续跑中再次提问时，轮询模式下问题卡片可见可答（Q→A→Q 链闭环）
- AC5：双仓库全量测试/lint/type-check 全绿；dev 部署后真实走完一次 提问→答题→续跑可见 的端到端
