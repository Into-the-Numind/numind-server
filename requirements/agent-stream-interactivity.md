# NDF S0 Requirement Card · `agent-stream-interactivity`

**Track**：Standard
**Feature ID**：`agent-stream-interactivity`
**仓库**：`numind-server` + `numind-web-v3`
**起草日期**：2026-06-02
**起因**：上线前测试（见 `docs/agent-mode/agent-mode-prod-readiness-test-plan.md` BLK-4 / BLK-5）发现 agent 默认"流式"对话路径有两处功能断裂。

---

## 1. 问题（Why now）

用户实际对话走**流式路径**（`AgentChatView.vue` → `useAgentStream` → `POST /v1/agent-runs/stream`）。该路径有两个断裂：

- **BLK-4 — 流式不支持 `ask_user_question` 的 yield/resume**
  yield 协议只在非流式 `runner.Run`（`runner.go:1079-1133`：检测 `yieldError` → 持久化 `pending_question_json` → 状态机置 `waiting_for_user_choice` → 提前返回）实现。
  流式 `RunStream`/`consumeEinoStream` 把工具返回的 `yieldError` 当普通 stream error → `HandleLLMError` 分类成 `model_error`（`runner_stream.go:128-151`）。
  且 `EventQuestionPrompt`（`stream/events.go:48` 定义）**全后端无任何发射点**。
  结果：AI 反问 → 前端显示"任务失败"；即便答题也无人重启流 → 永久卡死"等待 agent 继续"。

- **BLK-5 — 流式期间前端 `store.currentRun` 全程为 `null`**
  `agentChat.ts` 仅在 `terminal`/`reconcile` 时才给 `currentRun` 赋值。流式进行中依赖 `currentRun` 的：顶栏状态徽标、取消按钮显隐、budget 60%/100% 预警与阻断弹窗、取消积分提示 —— 在主路径全部失效。

> 两个 bug 同根：**流式路径是"二等公民"，没把本次 run 的状态接好**。一并修。

---

## 2. 范围

### In scope
1. 流式路径支持 `ask_user_question`：检测 yield → 持久化 `pending_question` → 发射 `question_prompt` SSE → run 置 `waiting_for_user_choice`（不再 `model_error`）。
2. 答题后 resume：`POST /answer` 后前端能继续接收 AI 后续输出（重建流式或轮询，方案 S2 定）。
3. 流式开始即**乐观建立 `currentRun`**，使状态/取消/budget 提醒在流式路径生效。

### Out of scope（本 feature 不碰）
- 其它红线：计费不扣（BLK-2）、权限关闭（BLK-1）、bash 危险命令（BLK-3）、子账户不可用 —— 各自独立决策/feature。
- 断流无 terminal 兜底、附件上传进度、历史只读态可交互 等中小 UX 项 —— 后续。

---

## 3. 用户故事

- 学员问一个模糊问题 → AI 反问"你是要 A 还是 B?" → 学员看到选项、点选 → AI 接着回答（不再卡死/报失败）。
- 学员对话进行中 → 顶栏正确显示"运行中"+ 可取消；积分接近用尽时弹预警/阻断弹窗（不再静默耗尽）。

---

## 4. 成功指标 / 验收

- 流式路径触发 `ask_user_question` → 前端渲染 QuestionPrompt；答题 → AI 续答；run `state_reason` 经历 `waiting_for_user_choice` → `running`。
- 流式进行中 `currentRun.status='running'`，顶栏状态徽标 / 取消按钮 / budget 阈值弹窗正常工作。
- **回归**：正常完成的流式 run 不受影响（`terminal=completed` 行为、文本不重复、seq 单调 均不变）。

---

## 5. 风险

| # | 风险 | 缓解 |
|---|------|------|
| R1 | Eino `react.Agent` 在 Stream 模式下对"工具返回 error"的处理方式（传播为 stream error 让 `sr.Recv()` 拿到 vs 转成 tool message 回灌模型）未确认，决定 yield 拦截点 | **S2 必须先查清**（读 vendored eino react 源 + 写探针测试） |
| R2 | resume 机制：`/answer` 当前重跑**非流式** `runner.Run`（`answer.go:83`）。流式 resume 需重建 SSE 或轮询 | S2 定方案；优先复用现有 stream/narration 端点 |
| R3 | 回归：不能破坏正常 `completed` run 的流式收尾，也不能破坏现有 polling 路径的 question 处理 | S4 加回归测；改动尽量局部 |

---

## 6. Triage（Standard 理由）

| # | Hotfix 标准 | 本 feature |
|---|------|------|
| 1 | 不涉及 DB schema | ✅ 满足（`pending_question` 字段已存在） |
| 2 | 不涉及新增 API 端点 | ⚠️ 可能复用 stream/answer，S2 确认 |
| 3 | 不涉及新外部服务 | ✅ 满足 |
| 4 | 影响文件 ≤3 | ❌ **违反**（后端 `runner_runstream`/`runner_stream`/`consumeEinoStream` + 前端 `agentChat.ts`/`AgentChatView`/`useAgentStream`/`QuestionPrompt`，跨 2 仓库） |
| 5 | 不涉及支付/权限 | ✅ 满足（但触及核心 run loop，需谨慎） |

→ 违反"≤3 文件" + 跨仓库 + 核心 run loop 高风险 → **Standard**。

---

## 7. S5 验证策略（S0 预声明，S3 final — NDF Rule 10）

- **后端**：Go 集成测（mock LLM + mock yield 工具）验证流式 yield → `pending_question` 持久化 + `question_prompt` 发射 + `waiting_for_user_choice`；回归测正常 `completed` 流。
- **前端**：vitest（`currentRun` 乐观建立 + `question_prompt` 处理 + 答题后 resume）+ Playwright e2e（流式 `ask_user_question` 渲染 → 答题 → 续答）。
- **高风险判定**：不触支付/权限，但属核心交互路径，**强制 Playwright e2e** 端到端覆盖 → 满足 Rule 10。回归保护：Go + vitest + Playwright 均永久回归。

---

## 8. 不变量（不得破坏）

- `TerminalReason` / `LoopEvent` enum **不新增**（复用 `waiting_for_user_choice` / `LoopEventAskUserPaused`）。
- `aiservice` 唯一入口；hook chain 顺序不变。
- 非流式 `Run` 路径的 yield 行为**不变**（仅补齐流式路径与之对齐）。

---

*S0 草案。下一步 S1 proposal + PRD（含 Eino 流式工具错误行为调研结论）。*
