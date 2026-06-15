# Agent Mode 问题卡 / Narration / 工具卡 体验修复（agent-qa-card-ux）

## 来源
- 提出人：User（产品负责人，dev 实跑观察）
- 提出日期：2026-06-15

## 需求描述
User 在 dev 上运行 agent mode 时观察到 4 个前端体验问题：

1. **问题卡刷新后退化成用户气泡**：agent 调用 multi-question-card 工具产生问题卡，用户选择并提交后卡片折叠（折叠态正确）。但**刷新页面后**，这张卡片变成了一个普通的「用户会话气泡」（见 User 截图 image1）。期望：刷新后仍保持 card 形态（折叠的已回答卡），而不是孤立的用户气泡。
2. **工具调用缺视觉分离**：每个工具调用希望加一个**浅绿色矩形背景**，让相邻工具调用之间的视觉分离度更明显。
3. **任务完成后 spinner 不停**：观察到一个任务已经完成之后，这个任务的「等你回答一个问题」仍在转圈（spinner 不停）。
4. **答题后 narration 风格断裂**：agent 在调用 multi-question-card **之前**有很多正文 narration（流式助理叙述）；一旦调用了 multi-question-card 并续跑**之后**，就只剩工具调用记录、不再出现任何正文 narration，前后体验不统一。期望：答题前后都保持一致的 narration 风格。

## 业务目标
Agent mode 是有数 AI 工作台的核心交互。问题卡（ask_user_question）是 agent 与用户协作的关键环节，其前后的展示一致性、刷新后的持久化正确性，直接决定用户对「agent 在认真为我工作」的信任感。这 4 个问题都集中在「问题卡 → 答题 → 续跑」这条协作链路上，破坏了连贯叙事体验。

## 优先级
高（核心交互链路体验缺陷，影响用户对 agent 协作的信任）

## 根因初判（AI 调查，read-only）
| # | 根因 | 层 |
|---|------|----|
| 1 | `answer.go:AnswerAndClear` 把答案以 `role:"user"` turn 写入 `agent_run.messages` 并**清空** `pending_question_json`；刷新时 `student_query.go:synthesizeQuestionPrompt` 只在 run 处于 `waiting_for_user_choice` 时合成卡片，任务完成后状态非 waiting → 不再合成；那条答案 turn 被 `transformMessages` 渲染成普通 user 气泡 | 后端 + 前端 |
| 2 | `AgentToolCallItem.vue` 的 `.tl-line` 无 background/圆角（agent-process-timeline 故意削平为扁平时间线，本次需在不破坏扁平美学前提下加轻量绿色分隔） | 前端 CSS |
| 3 | 字面文案搜不到，疑似 `AgentRunPulse`「处理中…」或问题卡 pending 态在 run terminal 后未清理；**需运行时浏览器诊断定位**（§6 硬规则） | 前端 |
| 4 | `AgentChatView.vue:handleAnswerSubmitted` 答题后**不重开 SSE 流**，改走轮询续跑，轮询只拉工具 narration + 末尾 final_answer，不流式助理正文 → 续跑段无正文 narration（即「流式 resume 未做」缺口） | 后端 + 前端 |

## Triage
- 推荐轨道：**Standard**
- 分类理由：
  1. 数据库 schema 变更：否（问题 1 拟用 turn JSON 嵌入重建，避免 migration）
  2. 新增 API 端点：否（问题 4 的流式续跑可复用现有 SSE stream 端点，待 S2 定）
  3. 新外部服务集成：否
  4. 影响文件数：>3（跨 numind-server + numind-web-v3，问题 1/4 各涉及后端+前端多文件）
  5. 高风险业务逻辑（支付/权限）：否
- 人类决定：**确认 Standard**（User 已在档位确认环节选择「一个 Standard feature」，4 个问题打包）

## 备注
- 这 4 个问题强耦合在「问题卡 / narration / 工具卡」同一渲染 + run 生命周期链路上（尤其 1/3/4 共享 run terminal 时序），打包为一个 feature 避免分开返工。
- 相关前序 feature（提供上下文）：`answer-resume-lifecycle`（答题续跑生命周期）、`agent-process-timeline`（扁平时间线 + 问题卡答后折叠）、`agent-run-pulse`（活信号尾行）、`narration-stable-toolcall-id`。
- 问题 1/3/4 为 User dev 实跑观察到的 bug → 按 NDF §12 / 规则 11，修复需配回归测试（spec-compliance reviewer 会 grep 测试 commit）。
- 问题 3 须按 §6 用浏览器抓运行时状态定位确切 spinner，禁纯静态推理。
- 问题 2 须克制：agent-process-timeline 已确立扁平时间线美学，绿色背景应轻量、与翠绿活信号（--primary）同源，不可加回厚重 card chrome。
