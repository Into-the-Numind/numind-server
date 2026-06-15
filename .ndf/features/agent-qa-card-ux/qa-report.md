# QA 报告 — agent-qa-card-ux (S5)

> NDF Standard S5 自动验收。日期 2026-06-15。分支 feature/agent-qa-card-ux（双 worktree）。

## 1. 范围
4 个 agent-mode 前端体验问题的修复：
- issue1 已答问题卡刷新后保持卡片形态（后端持久化重建 + 前端渲染真实答案）
- issue2 工具调用轻量绿底视觉分离
- issue3 任务完成后无残留转圈（终态清活信号）
- issue4 答题后续跑保持流式 narration（新增 SSE answer-stream）

## 2. 自动化门禁（全过）

### numind-server
| 检查 | 结果 |
|------|------|
| `go test ./...` | **0 FAIL**（全 ok） |
| `go vet ./...` | clean（仅 sqlite cgo deprecation 警告，非本改动） |
| `golangci-lint run`（biz/agent + store + controller/v1/agent） | **0 findings** |
| `gofmt -l` 改动文件 | clean |

新增/相关 Go 测试：
- `TestStudentQuery_SessionSnapshot_AnsweredQuestionCardSurvivesReload`（issue1 复现→PASS）
- `TestBuildAnswerTurn_*`、`TestReconstructAnsweredQuestions`（issue1 单元）
- `TestAnswerStream_*`、`TestAcquireResumeStreamLock_*`、`TestAnswerStream_PriorTranscriptInHistory`（issue4 后端）
- `TestCreateStream_*` 6 项不回归（pump 抽取）
- `TestAnswerAndClear_MarksRunResumed`（store，新签名）

### numind-web-v3
| 检查 | 结果 |
|------|------|
| `npx vitest run`（全量） | **832 passed / 80 files**（15 skipped, 3 todo），0 FAIL |
| `npx vue-tsc --noEmit` | **0** |
| `npx eslint`（全部改动文件） | **0** |

新增/更新前端测试：
- `QuestionPrompt.spec`：已答卡 reload 显示真实答案 + legacy fallback；submit 改为 emit 断言（21 tests）
- `agentChat-resume.spec`：issue3 终态不变量（stale waiting→非 waiting）+ terminal 清 stuckSince（含 waiting 跳过）（8 tests）
- `useAgentStream.spec`：startResume 开 SSE / 抛非 abort / 吞 abort（issue4 RED→GREEN）
- `AgentChatView.spec`：startResume mock

## 3. Rule 11 复现测试（issue1/3/4 = dev 实跑 bug，全部满足）
| issue | RED commit |
|------|------|
| issue1 | `2f2ffffc test(qa): reproduce answered question card renders as user bubble on reload` |
| issue3 | `d6c4c57 test(qa): reproduce stale waiting state keeps run looking active` |
| issue4 | `c3e6600 test(qa): reproduce no narration prose after answering (poll-only resume)` |
（issue2 = 视觉增强非 bug，无需复现测试。）

## 4. 运行时验收（S6 dev，按 plan T7）
完整「发起含 ask_user_question 的 agent run → 出 pending 卡 → 答题 → 续跑流式正文 → 完成 → 刷新」链路需要 dev 后端 + LLM + agent 定义 + 积分，**本地无法可靠复现**。按 plan T7（§6 硬规则：前端 UI 视觉/行为须运行时眼见为实；既有 agent-mode feature 标准做法 = dev 实跑 browse + E2E 凭据登录）：

**S6 ndf-done + /deploy-dev（server 先于 web）后，于 dev 用 browse + E2E 凭据实跑确证：**
1. issue4：答题后续跑期间正文 narration 流式持续出现（非只工具记录）。
2. issue3：续跑完成后无残留转圈（pulse/header/计时器全停、输入恢复）。
3. issue1：刷新页面后已答卡保持折叠卡形态 + 展开见真实答案，无孤立「用户已回答…」user 气泡。
4. issue2：工具行绿底分隔清晰，无巨卡/card chrome 回归。

**回归保护诚实声明**：issue1/3/4 的核心逻辑由 Go + vitest 持久回归保护；issue2（纯 CSS）+ issue3 的确切元素由一次性 dev browse 确证（视觉无自动回归保护，纯样式低风险可接受）。

## 5. 已知推迟项（2 P2，双 reviewer 均判可推迟）
- **P2-A**：T5 `markQuestionAnswered` 乐观翻转早于持久化。极罕见双失败（SSE 409/网络 + fallback POST 都失败）会留卡片 answered 但 run 未续跑——刷新自愈（run 仍 waiting → pending 卡重生）。清洁修需协调 QuestionPrompt submitting 生命周期 + revert，非 trivial → follow-up task。
- **P2-B**：`AgentChatView.handleAnswerSubmitted` 无 mount 级集成测试（mount 成本高）→ 由 S6 dev browse / 未来 E2E 覆盖。

## 6. ★S6 部署硬约束（review M1，P1）
**跨仓库 deploy 必须 server 先于 web**：前端 issue1 渲染依赖后端 question_answer 字段；若后端未先上线，前端 `questions[i].answer` 恒 undefined → reload 仍显示「已回答」占位，issue1 未根治。S6 顺序：ndf-done（两仓库）→ /deploy-dev server → /deploy-dev（web）。
