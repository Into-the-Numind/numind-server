# Agent Run Survives Exit — 提案

- **Feature ID**: agent-run-survives-exit
- **日期**: 2026-08-03
- **关联需求**: requirements/agent-run-survives-exit.md

## §1 方案概述 [客户可见]

用户在 Agent 运行中刷新、关闭 tab、离开聊天页或网络断开时，Agent 不再因为前端 SSE 连接断开而中止。服务端会继续把当前任务跑完并落库；用户下次回到对应会话时，可以看到最终结果。如果回来时任务还没完成，页面会恢复运行态并重新观察进度；如果实时事件已经过期，也能通过会话快照看到已落库的最终答案。

显式点击“停止任务”保持原语义：它仍然会调用取消接口并终止本次 Agent 执行。退出页面和停止任务在系统中必须是两种不同事件。

本次范围覆盖用户端 AgentChat 中的任意 Agent 定义，包括新发起的流式 run、ask_user_question 回答后的流式 resume、以及现有外部授权/外部工具续跑链路的兼容观察。服务进程重启后恢复未完成执行不在本次承诺内；那需要独立的队列/worker/watchdog 设计。

## §2 报价与周期 [客户可见]

- 预估工作量：2-3 个开发日
- 报价：内部产品迭代，不单独报价
- 交付时间线：S1 确认后进入 S2 技术设计；通过 S2/S3/S4 后开发、验证、Dev 验收

## §3 技术可行性 [AI 内部]

### S1 产品判断

office-hours 评估后的问题重定义：用户真正需要的不是“页面退出后继续保持那条 SSE”，而是“Agent 执行生命周期不被观察通道绑架”。因此系统应拆成两层：

- **执行生命周期**：由服务端托管，直到完成、失败、等待用户输入、显式取消或真实运行错误。
- **观察生命周期**：由页面 SSE/事件订阅负责，允许断开、重连、多 tab 观察和轮询降级。

这个拆分能保留现有流式体验，同时解决用户离开页面导致 `aborted_streaming` 的核心问题。

### 现有功能复用

- `POST /v1/agent-runs` 非流式路径已经用 detached goroutine 跑 `runner.Run(context.Background())`，证明“HTTP 结束后继续跑”在业务语义上已经被接受。
- `agent_run` DB 行与 `agent_run.messages/status/state_reason` 已是最终结果 SOT；`GET /v1/agent-sessions/:session_id/snapshot` 已能返回最新 run 和会话消息。
- `RunEventBroker` 已有 Redis Stream + Pub/Sub 的短期回放能力，`GET /v1/agent-runs/:id/events` 已能按 cursor 订阅，且 DB snapshot 是 broker 不可用时的最终 fallback。
- `runner.Cancel(runID)` 与 `POST /v1/agent-runs/:id/cancel` 已有显式取消链路。
- `runner.RunStream` 已用 `context.WithoutCancel(ctx)` 做终态落库，说明持久化侧已有“不要被 SSE 取消打断”的局部修复。
- 前端已有 `transport_cursor` 记录、`attachContinuation`、status polling、session snapshot replay，可以扩展到普通 running run。

### 推荐方案

采用 **服务端 supervised streaming + 前端普通 running run 重附着**。

后端把当前 `streamEvents` 的职责拆开：

- controller 仍负责鉴权、创建/校验 run、返回 SSE。
- 新增服务端 run supervisor/publisher，启动 `RunStream` 或 `AnswerStream` 后，独立 drain runner 的事件 channel，并把事件 best-effort 发布到 `RunEventBroker`。
- SSE handler 改成事件订阅者：初始 `CreateStream` 可以在启动 supervised run 后订阅该 run 的事件；浏览器断开只关闭订阅，不取消 runner。
- 事件发布使用 detached/bounded context，避免 HTTP request ctx 被取消后导致 Redis publish 直接失败。
- 运行锁与观察锁分离：执行只启动一次；页面连接、刷新、第二 tab 不应该“抢走”执行。

前端在恢复会话时扩展普通 running run 处理：

- `loadSessionSnapshot` 对 `status=running/pending` 的普通 run 也恢复 `currentRun`，不只处理 waiting/external continuation。
- 如果有 saved cursor，则用 `/events?after=<cursor>` 继续观察；没有 cursor 时使用 `/events?after=` 回放 Redis 窗口内事件。
- 如果 broker 不可用、事件过期或订阅失败，自动进入 status polling，并在 terminal 后用 snapshot/reconcile 渲染最终结果。
- `onUnmounted` 的 `stopStream()` 只关闭本地观察；只有点击停止按钮才调用 cancel API。

### 备选方案与取舍

- **仅把 `runCtx` 换成 `context.Background()`**：不推荐。当前 runner 事件 channel 由 HTTP handler drain；页面断开后 handler 返回，producer 可能因 channel 无人消费而阻塞，不能保证继续跑完。
- **全部改成非流式创建 + 轮询**：可快速避免断连取消，但会丢掉当前 token/tool 实时体验，退化太大。
- **引入持久队列 worker**：可靠性最强，可覆盖服务进程重启，但超过本次目标；应作为后续“Agent durable worker”单独立项。

### 技术风险

- **事件 channel 背压**：必须由后台 publisher 持续 drain `RunStream` channel，不能依赖 HTTP writer。
- **重复执行**：刷新/多 tab 不能重复启动同一个 run；需要清晰区分 execution lock 与 subscriber lock。
- **显式取消语义**：前端关闭 stream 不等于取消；`POST /cancel` 必须仍能命中 runner 注册的 cancel func。
- **Redis 不可用**：事件发布失败不能影响 Agent 完成；浏览器要降级到 polling/snapshot。
- **计费与 reconcile**：页面退出后的正常完成应走正常 Reserve/Reconcile；只有显式取消或真实错误进入对应终态。
- **跨租户/权限**：事件订阅必须继续先查 DB owner，再碰 Redis key。
- **外部续跑兼容**：已有 external action detached continuation 逻辑不能被新 supervisor 重复启动或打乱 cursor baseline。

### 涉及仓库

- [x] numind-server
- [x] numind-web-v3
- [ ] numind-admin-web

### AI 可观测性（如功能涉及 LLM 调用）

- [x] 涉及 LLM 调用：是。本功能不新增 LLM 调用点，但改变用户端 Agent 流式调用的生命周期。
- Trace 起点：现有 `agentRunner.Run` / `agentRunner.RunStream` 内部创建 `agent-runtime-run` trace；supervisor 不应另建业务 trace。
- Generation 点：沿用现有 Agent runtime / Eino / provider adapter generation 记录；本次不新增 generation 名称。
- 关键元数据：保持 `agent_run_id`, `session_id`, `user_id`, `agent_definition_id`, `state_reason`, `terminal_reason`, `disconnect_reason`。新增/调整 SSE span 时要区分 `client_disconnect_observer_only` 与真实执行终止。

## §4 产品需求定义 — PRD [AI 内部 — 不要为可读性简化]

### 用户故事

- 作为用户，我在 Agent 执行中关闭页面或网络断开后，Agent 应继续在服务端完成，以便我回来能看到结果。
- 作为用户，我点击“停止任务”时，系统应明确取消当前 Agent，而不是继续消耗积分和资源。
- 作为用户，我在另一个 tab 或稍后回到同一会话时，应看到正在运行、等待输入、失败或完成的真实状态。
- 作为维护者，我需要 SSE 观察断开不再产生 `aborted_streaming` 僵尸 run，以便减少“继续”后上下文/附件丢失类问题。

### 验收标准

- [ ] 初始流式 run 中断 HTTP/SSE 连接后，服务端 run 不进入 `aborted_streaming`，并最终按真实执行结果落库为 completed / waiting / failed 等状态。
- [ ] ask_user_question 的 `answer-stream` 在提交答案后断开页面，resume leg 继续执行并落库。
- [ ] 点击“停止任务”仍会调用 cancel API；runner 收到 cancel 后停止，run 终态为 cancelled/对应取消语义。
- [ ] 用户回到同一 session 时，若 run 已完成，snapshot 展示最终 assistant 输出和工具结果。
- [ ] 用户回到同一 session 时，若 run 仍在 running/pending，前端恢复 `currentRun`，优先 reattach events，失败后自动 polling。
- [ ] 多 tab/刷新不会重复启动同一个已存在 run，也不会互相消费掉事件。
- [ ] Redis event broker 不可用时，Agent 执行和 DB 最终落库不受影响；前端可通过 polling/snapshot 收敛。
- [ ] 非本用户订阅 run events 继续返回安全错误，不泄露 Redis key 或 run 存在性。
- [ ] 断连后正常完成的 run 走正常 billing reconcile；显式 cancel 与真实错误的计费分类不被混淆。

### 边界情况

- 浏览器在第一帧 `stream_start` 前断开。
- 浏览器在 terminal frame 前断开。
- 浏览器在 terminal frame 后、finalize DB 写入前断开。
- 页面关闭后立刻重新打开，同一 run 仍在执行。
- Redis replay TTL 过期后打开历史会话。
- 两个 tab 同时观察同一个 running run。
- 用户退出后 Agent 进入 waiting_for_user_choice，需要回来后继续显示可回答问题卡。
- 外部授权 callback 已触发 detached continuation，而用户页面不在线。
- 进程重启/部署中断正在执行的 in-memory run：本次不承诺恢复执行，但 snapshot/status 不应伪造完成。

### 权限规则

- 仅 run owner 可看 snapshot、run detail、event subscription、cancel。
- 本功能不改变会员、积分、父子账号权限。
- Admin test/管理端不纳入 UI 改动；若复用同一后端流式接口，则自然获得后端断连不取消能力。

### UI 行为规格

- 页面位置：用户端 AgentChat。
- 布局要求：不新增页面结构；复用现有消息流、运行状态、停止按钮、等待问题卡和工具进度展示。
- 交互模式：
  - 页面卸载/路由离开：只停止本地 SSE/轮询观察，不调用 cancel。
  - 点击停止：先停止本地观察，再调用 cancel API，并把当前 run 更新为取消态。
  - 页面重新进入历史 session：加载 snapshot；若 snapshot run 仍 active，则恢复运行观察。
- 状态处理：
  - loading：沿用现有发送中/运行中态。
  - empty：历史 session 无消息时保持现有空态。
  - error：事件订阅失败时不立即判 run 失败，转 polling；只有 DB/run terminal failure 才展示失败。
  - terminal：最终以 DB snapshot/run detail 为准，清理 saved cursor。
