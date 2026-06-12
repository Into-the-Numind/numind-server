# Plan — answer-resume-lifecycle

> 串行执行；每 task：commit 验证（Rule 8）→ 双 Sonnet reviewer 并行（Rule 6）→ P0/P1/P2 现场修 → manifest reviewed_tasks += 1。T1/T2 在 server worktree，T3 在 web worktree（跨仓库 Tier 2 天然 disjoint）。

## T0 — 失败复现测试（Rule 11 首 commit，双仓库各一）

- server：`internal/numind/store/agent_run_resume_test.go` `TestAnswerAndClear_MarksRunResumed`（sqlite testDB，沿用包内既有测试基建）——现 RED
- web：`src/stores/__tests__/agentChat-resume.spec.ts`——mock api.getRun 返回 terminated+running+旧文案，断言无 final_answer 且 run 仍活跃——现 RED
- commits：`test(qa): reproduce answer-resume run advertised as terminated (dev run 148)`
- 验收：两测试 RED 且失败信息指向被测契约

## T1 — F1 状态真相（server）

- spec F1a + F1b
- 验收：T0 server 测试 GREEN；`go test ./internal/numind/store/ ./internal/numind/biz/agent/...`
- commit：`fix(agent): answer-resume marks the run running again (status + ended_at)`

## T2 — F2 续跑追加持久化（server）

- `resume_transcript.go` + 表驱动单测（先写测试）；接管捕获 priorMessages；两处 WriteTurn 调用点统一 merge
- 验收：merge 单测全绿；既有 runner/answer 测试零回归
- commit：`fix(agent): resumed runs append to the pre-yield transcript instead of clobbering it`

## T3 — F3 终态守卫 + F4 二次提问注入（web）

- spec F3 + F4；T0 web 测试转 GREEN；新增 F4 注入 spec（含幂等用例）
- 验收：`npx vitest run src/stores/__tests__/` 全绿 + vue-tsc + eslint（scoped）
- commit：`fix(agent): keep following a resumed run; surface follow-up questions from snapshot`

## T4 — S5 验证策略（Rule 10 专项）

- **方式：双仓库单测 TDD + dev 部署后真实 e2e（browse 浏览器实测完整 提问→答题→续跑→交付 链路）。不新写 Playwright。**
- **理由**：核心是 run 生命周期状态语义，单测能完整表达（status/ended_at/转录合并/前端判终）；端到端的真实变量是"detached goroutine 续跑 + 轮询 UI"，dev 实测一次完整 Q&A 闭环比 Playwright 模拟更可信；回归保护由 T0 复现测试（双仓库）永久承担
- **诚实声明**：browse e2e 是一次性验证；未来对 answer/resume 路径的修改依赖 T0-T2 单测护栏
- **关键路径**：
  1. server：`go test ./...` + 改动包 `-race` + golangci-lint
  2. web：vitest 全量 + vue-tsc + eslint
  3. ndf-done（两仓库原子 merge + push）
  4. `/deploy-dev server` + web `/deploy-dev`
  5. dev e2e：发起定位调研 → 等提问 → 答题 → **断言：不再出现假"回答完毕"；narration 持续推进；最终报告抵达 UI**；DB 复核 run messages 含两段完整历史

## 排除项

- RunStream 流式 resume、narration 协议扩展（proposal 已否决）
- 状态机 TerminalReason 枚举改动（invariant）
