# agent-mode-p0-tools — S5 验证策略

> NDF Rule 10 要求 S3 plan 必须包含独立的 S5 验证策略 task（T9）。本文件是该 task 的交付物，在 S3 gate 与 plan 一并 review。

---

## §1 验证方式分层

### 1. 后端 TDD（强制 + 永久回归保护）

- T1-T7 每个 task 完成后 reviewer PASS 含其单测
- 单测覆盖：4 工具 happy path + error path + 边界 + 权限
- state machine + runner yield handler 单测
- biz.AnswerPendingQuestion + answer controller 单测

**理由**：单测最快、可重复、零依赖；保护后端核心逻辑长期不被破坏。

### 2. Playwright E2E（强制 1 个）

- `numind-web-v3/e2e/agent-ask-user-question.spec.ts`
- 覆盖：登录 → agent chat → 触发 ask_user_question → QuestionPrompt 渲染 → 选项点击 → narration 继续

**理由**：ask_user_question 是最复杂工具（yield turn + 跨 SSE event + 跨前后端 + 跨用户态）；e2e 覆盖能验证整链路。

**回归保护诚实声明**：本 e2e 用 fixture agent 触发 ask_user_question，未来如 fixture 变更（如 agent prompt 调整导致不再触发此工具）需同步维护测试。选择 Playwright E2E 而非仅 gstack /qa 的理由：ask_user_question 涉及用户态转换（running → waiting_for_user_choice → running）和 SSE 事件流，自动化 e2e 能稳定重放此路径，gstack /qa 一次性验证无持久回归保护。

### 3. 后端集成测试（覆盖 yield-resume 完整流程）

- `internal/numind/biz/agent/runner_yield_test.go`（T3 落地）
- 用 in-mem SQLite + mock LLM 跑完整 Run → yield → Answer → resume → 第二段 Run → completion

**理由**：跨 process boundary 的集成测；单测无法覆盖 goroutine restart 行为。

### 4. 本地 manual smoke（S5 阶段执行）

由 AI 在 S5 阶段执行：

1. 本地 server start (`cd numind-server && task dev`)
2. 本地 web-v3 start (`cd numind-web-v3 && npm run dev`)
3. 浏览器登录 `$E2E_USERNAME`
4. 跑测试用例（见 §2 关键用户路径 P1-P5）
5. 看本地 Langfuse UI（`docker compose -f docker-compose.langfuse.yml up -d`）确认 4 工具的 Span/Generation 出现

---

## §2 关键用户路径列表（S5 阶段 manual smoke 必跑）

| # | 路径 | 期望 |
|---|------|------|
| P1 | login → agent chat → "今天高考新闻" → 等回复 | agent 调 web_search → narration 含 ≥1 URL；Langfuse Span `tool.web_search.execute` 可见 |
| P2 | login → agent chat → 含 URL 的消息（如"看这篇 https://www.moe.gov.cn/..."）→ 等回复 | agent 调 web_fetch → narration 总结网页内容；Langfuse Span `tool.web_fetch.execute` 可见 |
| P3 | login → agent chat → 模糊问题（如"我表姐想学 XX，你觉得适合她吗"）→ 等 QuestionPrompt → 点击选项 | agent 调 ask_user_question → 前端 QuestionPrompt 渲染 + 选项按钮 → 点击后 → agent 继续回答；run state_reason 经历 waiting_for_user_choice → running |
| P4 | login → agent chat → 附件按钮上传 PDF → "读这个文件讲什么" → 等回复 | agent 调 file_read → narration 含 PDF 内容摘要；Langfuse Generation `tool.file_read.execute` 含 page_count |
| P5 | login → agent chat → 同时触发搜索意图 + 模糊问题（如"最近有啥好的 XX 课程，适合我妹妹吗？她是零基础"）→ 等 | agent 先调 web_search 再调 ask_user_question（或反），最终回答合理；两个工具的 Langfuse Span 都出现 |

---

## §3 不在 S5 范围（明确）

以下场景不在 S5 验收范围，原因附后：

- **Production load test**（v1 不做）：4 个新工具均有外部依赖（Tavily/qwen-long/OCR），压测需要 mock provider 或承担费用，v1 先跑业务验证
- **跨账户 file_read 安全压测**：单测已覆盖正例 + 1 个反例（ErrPermissionDenied），安全行为由代码逻辑保证，不需要额外压测
- **Tavily quota 耗尽场景压测**：人工触发不现实；runbook §8 已提供 oncall 处理流程
- **web_fetch SSRF 压测**：SSRF 防护逻辑有完整单测（TestWebFetch_SSRF），已覆盖 10+ 攻击向量
- **多租户并发 ask_user_question**：v1 单租户场景验证足够；并发 yield/resume 行为由 DB 事务保证，不需要额外验收

---

## §4 验证工具清单

| 层次 | 工具 | 用途 |
|------|------|------|
| 后端单测 | `go test ./internal/numind/biz/agent/... -run TestWebSearch\|TestWebFetch\|TestFileRead\|TestAskUserQuestion` | T1-T5 工具覆盖 |
| 后端单测 | `go test ./internal/numind/biz/agent/... -run TestLoopState\|TestRunnerYield` | state machine + yield handler |
| 后端单测 | `go test ./internal/numind/biz/agent/... -run TestAnswerPendingQuestion` + `go test ./internal/numind/controller/v1/agent/... -run TestAnswer` | T4 biz + controller |
| 后端集成 | `go test ./internal/numind/biz/agent/... -run TestRunnerYield_IntegrationFlow` | yield-resume 完整流程 |
| 前端单测 | `npm run test:unit -- QuestionPrompt` | QuestionPrompt 组件 |
| E2E | `E2E_USERNAME=$E2E_USERNAME E2E_PASSWORD=$E2E_PASSWORD npm run test:e2e -- agent-ask-user-question` | ask_user_question 端到端 |
| Manual smoke | 浏览器 + Langfuse UI | 4 工具综合体验 P1-P5 |

---

*由 T9 agent 创建于 2026-05-22。S3 gate review 后此文件锁定，S5 阶段按此策略执行。*
