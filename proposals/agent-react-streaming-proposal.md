# Agent ReAct 流式化 — 提案

## §1 方案概述 [客户可见]

把 agent 聊天页面从"轮询 + 一次性结果"改造成"流式 + 实时事件"。

用户进 agent 聊天，立刻能看到：
- LLM 逐字生成中间想法和最终答案，像打字一样
- 每次调用工具时，工具卡片立即出现并显示进度
- 状态变化（"正在搜索…"、"已完成 8/10 步"、"思考中…"）实时刷新

技术上，新增一条 SSE 流式接口给前端 agent 聊天页面用；现有的同步 POST 接口保留给 SDK / 第三方调用方。后端 agent runner 改造成 async event 模式，工具调用、LLM token、状态机转换都作为事件推送给前端。

## §2 报价与周期 [客户可见]

- 预估工作量：**6-8 工程日**（含调研、实现、测试、上线、监控验收）
- 报价：N/A（内部团队，无外部计费）
- 交付时间线：S2-S7 走完约 1-1.5 周

具体拆解：
- S2-S3 spec + plan：1-1.5 天
- S4 后端实现：2-3 天（runner 重构是大头）
- S4 前端实现：1.5-2 天（store + 组件）
- S5 E2E 验收：0.5-1 天
- S6 dev 上线 + 观察 / S7 prod：0.5 天

## §3 技术可行性 [AI 内部]

### 现有功能复用

| 现有模块 | 复用方式 |
|---------|---------|
| `internal/numind/biz/narration/` (provider + memStreamer) | 直接复用 — 已是 in-memory event channel，只是出口从 HTTP poll 换成 SSE writer。新增"LLM text delta"事件类型即可。|
| `internal/numind/controller/v1/chatbot/chatbot.go:262-371` | 复制 SSE 写法（gin headers + `fmt.Fprintf(w, "data: %s\n\n", ...)` + `w.Flush()`）|
| `numind-web-v3/src/api/sales.ts:401-480` (`readSSEStream` + `fetchSSE`) | 直接调用 — 已是通用 SSE 消费器 |
| `numind-web-v3/src/types/agent.ts` Message discriminated union | 复用 — `user`/`assistant`/`tool_group`/`plan`/`final_answer`/`system`/`question_prompt` 已覆盖大部分场景，只需为 streaming 状态加 `isStreaming` 字段 |
| `numind-web-v3/src/stores/agentChat.ts` ToolGroupMessage 聚合 | 复用 — 已按 `tool_call_id` 聚合 narration events，把数据源从 poll 换成 SSE event 流即可 |
| `aiservice.ChatStream` + `aiserviceAdapter.Stream()` ([adapter.go:177](numind-server/internal/numind/biz/agent/adapter.go:177)) | 启用 — 这条路径已存在但被 runner 跳过，本 feature 把它接通 |

### 技术风险

| 风险 | 描述 | 缓解 |
|------|------|------|
| **R1: Eino ReAct loop 不支持流式** | `einoAgent.Generate(ctx, msgs)` 是 blocking 调用；Eino 框架的 React agent 可能没有原生 streaming 接口。如果是这样，我们要么用 Eino 内部 API 改造，要么用自写的 ReAct loop 替代 einoAgent.Generate。 | S2 spec 阶段第一件事是验证 Eino 是否支持 `react.NewAgent(...).Stream()`。如果不支持，**降级方案**：保留 einoAgent.Generate 用作完整 step，但每个 step 完成后立刻发 `step_done` 事件 + 最终 step 的 LLM 输出走 ChatStream 拿到 token-level 流。这能拿到 70% 的 Manus 效果（步骤可见 + 最终答案流式），保 ReAct 内核不动。 |
| **R2: Hook 链与流式的兼容性** | compliance / budget / permission / sandbox / narration 5 个 hook 是按"一次 LLM call → 一次 hook 检查"设计的（I-invariants）。流式时每个 token delta 是否要过 hook？答案应是"否"——hook 在 message boundary 触发，不是 token boundary。但要明确写到 spec 里。 | S2 spec 明确"hook trigger 在 message_stop / tool_call boundary，不是 token boundary"，并加测试覆盖。|
| **R3: 断流恢复** | SSE 连接断开（网络抖动 / 浏览器刷新）后，前端怎么续接？Claude Code 的 SDK 用 session 持久化 + 重连。我们的 agent_run 已经落 DB（terminal_metadata, messages），重连可读 DB 拿最新状态。 | 简化方案：断流后前端**自动 fallback 到现有 GET /v1/agent-runs/:id 轮询**（已有），等运行结束读 messages 拿最终结果。失去的是流式体验，但不丢数据。S2 spec 明确此行为。|
| **R4: 多浏览器标签共享一个 run** | 用户在两个标签都打开同一个 agent_run。两个 SSE 流都连到同一个 `narration.Provider.Subscribe(runID)`？memStreamer 当前是 buffered channel，多订阅会丢数据。 | S2 阶段评估 narration.Provider 是否支持多订阅（看代码）。若不支持，方案是 "fan-out"：在 SSE controller 层维护 `map[runID][]chan Event`。或简化：明确"一个 run 只允许一个活跃 SSE 连接，后开的标签退化到轮询"。|
| **R5: terminal_metadata / messages 与流式事件的一致性** | 流式时事件实时推；run 结束时还要落 DB 的 `messages` JSON 和 `terminal_metadata`。如果两边不一致（事件流说 step 7 失败，但 DB 写了 step 7 成功），前端混乱。 | 单一 SoT：DB 是最终一致性的事实源，事件流是 cache-with-eviction。前端：流式事件渲染中间状态；run terminal 后**用 DB 的 messages 校准最终消息列表**（也就是说，stream 关闭时拉一次 GET 做最终对账）。|
| **R6: aiservice 中间件链与流式的相互作用** | `aiservice.ChatStream` 路径上有：Tracing → Fallback → ContextBudgetCredits → Billing → Retry。Billing 在流式下要等 stream 结束才能拿到 token usage；Retry 在 firstChunkSent 之后不能 retry（已实现：`ctxKeyFirstChunkSent`）。这些已经设计过，但要测一遍。 | S2 spec 引用现有中间件 streaming 约定，S5 E2E 验收覆盖。|

### 涉及仓库

- [x] numind-server
- [x] numind-web-v3
- [ ] numind-admin-web（明确不在范围）

### AI 可观测性

- [x] 涉及 LLM 调用：是
- **Trace 起点**：`agentRunner.Run()` — 已有 trace ID（feature 14 落地时引入），本 feature 不改变 trace 起点。
- **Generation 点**：每次 LLM step 是一个 generation。流式不改变 generation 数量；只改变每个 generation 的"输入 → 输出"采集时机（output 改为 stream 累积后写入）。
- **关键元数据**：`agent_run_id`、`step_count`、`stream_protocol_version=v2`（用于区分新老路径，便于线上对比 SLI）。
- **新增观察点**：
  - `sse.first_byte_ms` — SSE 连接建立到第一个事件的延迟（核心 UX 指标）
  - `sse.event_count` — 一次 run 总事件数（成本观察）
  - `sse.disconnect_reason` — 客户端断流、服务端 close、超时、错误（运维信号）

## §4 产品需求定义 — PRD [AI 内部]

### 用户故事

- 作为**普通用户**，我需要看到 agent 实时打字的最终答案，以便我能边读边判断质量、提前打断或追问。
- 作为**普通用户**，我需要看到 agent 调用了哪些工具及其结果摘要，以便我能信任结果背后的依据。
- 作为**普通用户**，我需要在 agent 长时间思考时看到状态变化（"正在搜索…"、"正在分析…"），以便我知道它没死，愿意继续等。
- 作为**开发者 / SDK 用户**，我需要保留原有的同步 POST /v1/agent-runs 接口，以便我已有的脚本不破坏。

### 验收标准

#### 后端
- [ ] 新增 `GET /v1/agent-runs/:id/stream` 或 `POST /v1/agent-runs?stream=1` 接口，返回 `text/event-stream`，每个事件按 SSE 帧格式（`data: {json}\n\n`）。
- [ ] 事件协议覆盖：`token_delta`、`assistant_message`、`tool_call_start`、`tool_call_progress`、`tool_call_result`、`tool_call_error`、`step_done`、`state_change`、`terminal`、`error`、`ping`（每 25s 维持连接）。
- [ ] LLM 调用走 `aiservice.ChatStream`，token-level delta 在 ≤500ms 内到达前端（dev 上抽样测量）。
- [ ] Eino ReAct loop **要么**直接支持 streaming（首选），**要么**降级为"step-end-level 事件 + 仅最终 step 流式"（fallback）。S2 spec 决策落定。
- [ ] 旧的 POST /v1/agent-runs（非流式）依然存在且行为不变（SDK 兼容）。
- [ ] hook 链（compliance/budget/permission/sandbox/narration）所有 5 个 hook 在流式路径下行为与非流式一致——通过新增 e2e 测试覆盖。
- [ ] 流结束时 `agent_run.status` / `state_reason` / `terminal_metadata` / `messages` 与非流式路径一致。
- [ ] SSE 连接异常断开时，服务端不泄露 goroutine / channel（用 ctx.Done() 联动清理）。
- [ ] Langfuse trace 在流式路径下完整记录（generation 的 output 用累积后的全文，不是 token-by-token）。

#### 前端
- [ ] 用户提交问题后 ≤500ms 看到第一个反馈（状态文本或 token）。
- [ ] LLM 文本逐字渲染，含 markdown 实时格式化（标题、列表、代码块流式渲染不闪烁）。
- [ ] Tool call 卡片在 `tool_call_start` 时立即出现，状态依次变化（queued → use → progress → result）。
- [ ] 多个工具串行 / 并行调用时按时间序正确分组渲染。
- [ ] 流断开后 fallback 到现有 GET 轮询（不丢数据，但失去流式动效）。
- [ ] 用户中途主动取消（点击"中止"）→ 立即停止渲染并发送 cancel 信号到后端。
- [ ] question_prompt yield 事件流式到达时正确弹出选项 UI（沿用现有 [agentChat.ts](numind-web-v3/src/stores/agentChat.ts) 逻辑）。

### 边界情况

- **空消息流**：LLM 不输出文本只调工具 → 不渲染空气泡，仅显示 tool group。
- **超长输出**：单个 assistant message > 50k tokens → 前端按 chunk 累积，不阻塞渲染线程（virtual list 已支持）。
- **极快返回**：LLM <500ms 全部完成 → 不应该出现"打字动画"假象，体验上等同非流式直接显示。
- **极慢返回**：超过 180s timeout → 走现有 model_error 路径，前端显示错误气泡 + 重试按钮。
- **中途网络断开**：SSE 自动重连 1 次失败后转 fallback poll。
- **后端崩溃 / OOM**：connection 强制 close，前端 fallback poll 读到 agent_run.status='failed' 或 stuck。
- **同一 run 多标签订阅**：第二个标签连入时，要么共享流（fan-out）要么退化到 poll。S2 决策。
- **取消并发**：用户中途点击取消 → 发送 cancel 信号，后端 ctx 取消，stream 优雅关闭（emit `terminal: aborted` 事件）。

### 权限规则

不变。流式 endpoint 复用现有的 user_token 中间件 + parent_user_id scope。B2B2C 父子账户隔离规则不变。

### UI 行为规格

- **页面位置**：[AgentChatView.vue](numind-web-v3/src/views/agent/AgentChatView.vue) — 不新建页面，只改数据流。
- **布局**：维持现有 messages list 列表布局。每条消息一个 row component（user / assistant / tool_group / plan / final_answer）。
- **新增视觉元素**：
  - **流式光标**：assistant 文本气泡末尾闪烁的 `▎`，stream 关闭时消失。参考 Claude Code 的 `AssistantTextMessage.tsx`。
  - **Tool call 状态徽章**：每个 tool 卡片右上角 dot（灰=queued, 蓝=use, 蓝旋转=progress, 绿=result, 红=error），过渡动画 200ms。参考 Manus。
  - **"正在思考…"占位**：LLM 还没产生 token 但已经在 stream → 显示 placeholder 文本 + dots 动画。
- **交互模式**：
  - 用户提交 → 立即进入"streaming"态（输入框 disabled、显示"中止"按钮）。
  - "中止"按钮：发送 cancel API；UI 立即 freeze 当前内容并显示"已取消"系统消息。
  - 流式过程中用户滚动 → 不强制下滚；新内容到达时若滚动条已在底部则自动跟随，否则保持位置（"sticky scroll"）。
- **状态处理**：
  - **loading**（请求已发但 SSE 还没握手成功）：spinner + "连接中…"
  - **streaming**（SSE 已建立，正在接收事件）：当前消息显示流式光标
  - **empty**（agent 历史为空）：与现有空状态一致
  - **error**（SSE 失败 + fallback poll 也失败）：错误气泡 + 重试按钮
  - **terminal**（agent run 结束）：所有流式光标消失，最终 final_answer 气泡浮现，输入框重新启用

### 不在本 feature 范围内

明确剔除（参见 requirement card 末尾"NOT 在本 feature 范围"段落）：管理端流式监控、多 run 并发窗口、UX 整体重设计、ali/volc 同步升级。
