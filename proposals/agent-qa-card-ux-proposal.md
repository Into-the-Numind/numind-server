# Agent Mode 问题卡 / Narration / 工具卡 体验修复 — 提案（agent-qa-card-ux）

## §1 方案概述 [客户可见]
修复 agent mode 协作链路（问题卡 → 答题 → 续跑）上的 4 个体验缺陷，让 agent 在向用户提问、用户回答、之后继续工作的整个过程中，展示连贯、一致、可信赖：
1. 刷新页面后，已回答的问题卡**保持卡片形态**（可展开回看问题与你的回答），不再退化成一条孤立的用户气泡。
2. 每个工具调用加**轻量绿色背景**，相邻动作视觉分离更清晰。
3. 任务完成后不再有**残留转圈**。
4. 答题之后 agent 继续工作时，**正文叙述（narration）与答题前风格一致**（流式叙述持续出现，而非只剩工具记录）。

## §2 报价与周期 [客户可见]
- 内部修复（一人公司自研），无对外报价。
- 预估工作量：~1.5–2 天（含跨仓库 + 流式续跑改造 + 回归测试 + dev 验证）。
- 交付目标：dev 环境部署 + 浏览器实跑验证（prod 不在本次授权）。

## §3 技术可行性 [AI 内部]
### 现有功能复用
- **问题 4 流式续跑**：完全复用现有 SSE 事件管线——`stream/events.go`（14 种事件类型）、controller 的 SSE pump（`student_run_stream.go:CreateStream`）、biz `StudentRunService.RunStream` + `runner.RunStream`（已支持 `req.ExistingRunID`）、前端 `streamAgentRun` + `applyStreamEvent` + `useAgentStream`。只需新增一个「答题流式续跑」入口，事件类型/wire 格式/前端处理零改动。
- **问题 1 已答卡渲染**：前端 `QuestionPrompt.vue` 已有 `answered` 折叠卡只读路径（line 355-386），只缺「带真实答案的数据」；后端 `synthesizeQuestionPrompt` + `transformMessages` 已是重建入口。
- **问题 3 终态收尾**：`finalizeToolGroups`（停残留工具计时器）已存在；问题 4 的流式 `terminal` 事件会触发 `applyStreamEvent` 的干净 finalize。
- **问题 2**：纯 CSS，复用 `--primary` 翠绿 token 体系。

### 技术风险
- **R1 流式续跑回归风险**（中）：新增 `answer-stream` 端点不得破坏现有「postAgentAnswer + 轮询」路径。缓解：新端点**纯增量**，旧路径保留作 fallback（同 `streamAgentRun` 409 fallback 模式）；前端流式失败回退轮询。
- **R2 答案 turn 内嵌字段污染 LLM 历史**（低）：issue 1 在 user turn 内嵌 `question_answer` 结构。缓解：LLM resume 历史适配器（`turnsToHistoryMessages`）只读 `role`/`content`，额外字段被忽略——S4 实现时 grep 验证；遵循「加字段/新 role 对 6+ readers 安全」既有结论。
- **R3 问题 3 根因未运行时确证**（中）：字面文案搜不到，疑似 AgentRunPulse 残留或 stale `state_reason`。缓解：按 §6 硬规则在 dev 部署后用 browse + E2E 凭据实跑复现确证（既有 agent-mode feature 的标准验证姿势）。
- **R4 双 leg trace 连续性**（低）：续跑 leg 是独立 SSE，需挂同一 session 的 trace。缓解：复用 RunStream 既有 Langfuse 集成（`stream.StartSSESpanWithFirstByte`），续跑 leg 复用同 runID。

### 涉及仓库
- [x] numind-server（issue 1 持久化重建 + issue 4 answer-stream 端点/biz/runner）
- [x] numind-web-v3（issue 1 渲染 + issue 2 CSS + issue 3 终态收尾 + issue 4 流式客户端）
- [ ] numind-admin-web（不涉及）

### AI 可观测性（涉及 LLM 调用）
- [x] 涉及 LLM 调用：**是**（问题 4 答题续跑会触发新的 LLM 调用 leg）
- Trace 起点：续跑复用既有 run 的 trace（`RunStream` 路径已有 SSE span）；`answer-stream` 端点复用 `runner.RunStream` 的 trace topology，不新建独立 trace 起点。
- Generation 点：续跑 leg 内每次 `aiservice.Chat`（ReAct 循环）——已由 runner/aiservice 既有 Langfuse 集成覆盖，本 feature 不新增裸 LLM 调用。
- 关键元数据：run_id、session_id、resume=true（区分续跑 leg）。
- 结论：**不新增任何裸 LLM 调用入口**，仅把已有续跑从 `runner.Run` 切到 `runner.RunStream`，可观测性自动继承。

## §4 产品需求定义 — PRD [AI 内部]
### 用户故事
- 作为 agent 用户，刷新页面后，我**仍能看到**那张已回答的问题卡（折叠态，可展开看问题与我选的答案），而不是一条看不懂上下文的用户气泡。
- 作为 agent 用户，我能**一眼区分**每个工具调用动作（轻量绿色背景分隔）。
- 作为 agent 用户，任务完成后界面**没有残留转圈**，我能确信它真的完成了。
- 作为 agent 用户，我回答问题后，agent 继续工作时的**正文叙述持续出现**，体验和答题前一致。

### 验收标准
- [ ] **issue 1**：答题完成 + 任务结束后**刷新页面**，该问题卡渲染为「✓ 已回答」折叠卡，展开可见原问题 + 用户实际所选答案；**不出现**「用户已回答你的问题：…」的孤立 user 气泡。
- [ ] **issue 1**：刷新前后问题卡形态一致（都是折叠卡）。
- [ ] **issue 2**：每个工具调用行有轻量绿色矩形背景，相邻动作视觉分离明显；不破坏 agent-process-timeline 扁平单行时间线（不出现厚重 card chrome / 巨卡）。
- [ ] **issue 3**：一个含问题卡的任务完整跑完（提问→答题→续跑→完成）后，**无任何残留转圈/活信号**（AgentRunPulse 隐藏、问题卡无 spinner、工具计时器停、无 stale waiting 态）。
- [ ] **issue 4**：答题后 agent 续跑期间，正文 narration（助理叙述/思考）以流式持续出现，风格与答题前一致；不再是「只剩工具记录直到末尾 final answer 突然出现」。
- [ ] 回归：现有「首段流式 run」「无问题的普通 run」「答题轮询 fallback」全部不回归。
- [ ] 门禁：`task lint` + `go test ./...`（server）+ `npm run lint` + `npm run type-check` + vitest 全量（web）退出码 0。
- [ ] Rule 11：issue 1/3/4 各配回归测试，commit log 含 `test(qa):`/`test(repro):` 复现测试。

### 边界情况
- 答题后续跑**再次提问**（Q→A→Q 链）：流式续跑 leg 再 yield question_prompt → 前端流式渲染新卡（复用 applyStreamEvent question_prompt case）。
- 流式续跑连接中断 / 409 冲突：回退到现有 postAgentAnswer + 轮询路径。
- 历史 run（本 feature 上线前已答的 run）：其 answer turn 无 `question_answer` 字段 → 仍渲染为 user 气泡（forward-only，可接受；不回溯历史数据）。
- 多选 / 自由文本 / 跳过部分问题：已答卡展示实际所选 + 自由文本（resolvedAnswer 逻辑）。
- reduced-motion：绿色背景为静态样式，无动效，天然合规。

### 权限规则
- 无新增权限维度；agent mode 现有 user_token 鉴权 + run ownership 校验不变。续跑端点复用 `verifyRunOwnership`。

### UI 行为规格
- 页面位置：agent 对话页（`AgentChatView` / `AgentMessageList` / `AgentToolCallItem` / `QuestionPrompt` / `AgentRunPulse`）。
- 布局要求：问题卡折叠态 = 既有 `question-prompt--answered`；工具行 = 既有扁平单行 + 轻量绿底。
- 交互模式：已答卡点击展开/收起；答题提交 → 开 SSE 流式续跑。
- 状态处理：loading（流式 narration / pulse）/ empty（无）/ error（流式失败 toast + 回退轮询）/ success（已答卡 + 流式正文 + 干净终态）。
