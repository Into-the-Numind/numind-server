# Proposal / PRD — answer-resume-lifecycle

## 目标

"提问→答题→续跑"全链路真实可见：答题后 run 的生命周期状态诚实（DB 即真相），前端持续跟踪到真正完成；续跑不丢历史；续跑再提问时卡片照常出现。

## 方案选型

**核心原则：修数据真相（DB row 诚实），前端守卫只做纵深防御。**

- **F1 状态真相（后端，根治）**：
  - `AnswerAndClear` 原子 UPDATE 中加 `status='running'`、`ended_at=NULL`——答题瞬间行就变回"运行中"
  - runner.Run ExistingRunID 接管处加防御性 `UpdateState(ctx, id, "running", "", nil)`（幂等；覆盖未来其他 resume 入口）。UpdateState 现有签名 endedAt 为指针，nil 即不动 ended_at——需要顺带支持清空：改为 map 内显式 `ended_at=NULL`（新增 store 方法或扩展，spec 定）
  - 否决替代案"在 GetRun DTO 层把 terminated+running 映射成 running"：掩盖数据谎言，admin/search/其他读者照样被骗
- **F2 续跑追加持久化（后端）**：接管时捕获 priorMessages；终态落库时 merged = prior + 本段 turns（本段首条 user turn 与 prior 末尾答案消息重复时去重）。纯函数 `mergeResumeTranscript(prior, turns)` 可单测
- **F3 前端终态守卫（纵深）**：两处 final_answer 推送点（refreshRunStatus / reconcileFromDB）把 `state_reason === 'running'` 视为"续跑中"——不推 final_answer、不判终。对老后端/竞态窗口仍稳
- **F4 二次提问注入（前端）**：refreshRunStatus 检测到 `state_reason==='waiting_for_user_choice'` 且本 run 无 pending 卡片时，拉 `GET /sessions/:id/snapshot`、抽取该 run 的 synthesized question_prompt 注入。复用今天 hotfix 刚验证过的快照合成机制，零后端改动
  - 否决替代案"后端 narration yield_payload 填充"：新增 SSE/poll 协议面，快照机制已存在且已验证

## 影响范围

- numind-server：store/agent_run.go、biz/agent/runner.go、（必要时）store 接口 + 测试
- numind-web-v3：stores/agentChat.ts + 测试
- 无 DB schema 变更、无新 API 端点、无新外部服务

## 不做什么

- 不改 yield 的"terminated+waiting"终态语义本身（状态机 19 reason 是 invariant I2，不新增不改写；只修答题后的翻转）
- 不把续跑改成 SSE 流式（RunStream resume 是更大的工程，本次保证轮询模式正确）
- 不动 narration 协议
