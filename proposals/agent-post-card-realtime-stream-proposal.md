# Agent 卡片后实时流恢复 — 提案与 PRD

## §1 方案概述 [客户可见]

Agent 出现授权或外部操作卡片后，任务会在服务器继续执行，但当前页面已经失去与任务的实时事件连接。本修复为每个 Agent run 建立独立、可短期回放的 Redis 事件流。浏览器在卡片阶段保持或恢复订阅，实时收到后续思考、正式文字、工具进度和结束状态；刷新与五秒快照轮询只作为异常降级。

多账号和多会话按服务端 run 归属隔离。每个浏览器使用自己的游标读取，不使用会让客户端互相抢消息的 Consumer Group；进程级通知复用避免每个在线客户长期占用一个 Redis 阻塞连接。

## §2 报价与周期 [客户可见]

- 预估工作量：1 个快速 Standard 周期
- 报价：内部缺陷修复，不单独报价
- 交付时间线：完成设计、回归测试、实现、审查后部署 Dev

## §3 技术可行性 [AI 内部]

### 现有功能复用

- 复用现有 `stream.Event` 协议与 `stream_start`、delta、工具和 terminal 事件。
- 复用现有 Redis v9 客户端；不新增外部服务或数据库 schema。
- 复用 `GET /v1/agent-runs/:id` 的 current-user 所有权边界。
- 复用前端 `applyStreamEvent`，新增的只是可恢复订阅与 transport cursor。
- 复用持久化 run/messages 快照作为 Redis 故障时的最终一致性恢复。

### 技术风险

- Redis 连接池耗尽：禁止每个 SSE 客户端独占 `XREAD BLOCK`；用单进程 Pub/Sub 通知复用，按通知执行短 `XRANGE`。
- 卡片结束与浏览器重连之间丢事件：所有事件先 `XADD` 再发送/通知，浏览器按 SSE `id` 从 Redis Stream 补发。
- continuation 的现有 `seq` 从 1 重新开始：新增 transport cursor 作为跨 continuation 的全序，业务 `seq` 保持兼容。
- Redis 暂时不可用：不能阻断 Agent 执行；记录指标并退化到现有快照恢复。
- 跨租户读取：订阅端点先从数据库验证 run 属于当前用户，Redis key 和用户输入不能绕过验证。

### 涉及仓库

- [x] numind-server
- [x] numind-web-v3
- [ ] numind-admin-web

### AI 可观测性

- 涉及新的 LLM 调用：否
- Trace/Generation：N/A；修复只传输既有 Agent runtime 事件，不新增调用或计费。
- 新增指标：publish/subscribe/replay 数量、通知积压、Redis 降级、SSE 断开原因，不记录 prompt、思考正文或工具结果。

## §4 产品需求定义 — PRD [AI 内部]

### 用户故事

- 作为正在执行打标任务的用户，我需要在完成外部操作卡片后继续实时看到 Agent 的思考、文字和工具活动，以便确认任务仍在推进而无需刷新。
- 作为同时使用系统的不同客户，我需要自己的事件绝不进入其他账号或会话。
- 作为网络短暂中断的用户，我需要从最后确认事件继续，而不是重复整段内容或丢失结果。

### 验收标准

- [ ] 卡片前后 `reasoning_delta`、`token_delta`、工具开始/进度/结果和 terminal 均逐帧进入 DOM。
- [ ] 卡片后正常路径不依赖五秒 run 快照轮询，不刷新即可看到最终文本。
- [ ] 断线重连携带最后 SSE cursor，服务端仅补发 cursor 之后的事件。
- [ ] 两个用户即使并发订阅不同 run，也只能读取各自数据；猜测他人 run ID 返回无数据的授权错误。
- [ ] 同一 run 的两个标签页各自收到完整事件，不互相消费。
- [ ] Redis 不可用时 Agent 仍完成并持久化，前端明确退化到快照恢复。
- [ ] Playwright 用真实时间间隔验证流结束前 DOM 已出现卡片后思考、文字和工具状态。

### 边界情况

- 浏览器在卡片完成前、完成瞬间或 continuation 已开始后订阅。
- 页面切换、AbortController 取消、SSE 代理断开和重复重连。
- Redis 重启、Stream 已过 TTL、通知先到而 replay 尚未结束。
- continuation 重发 `stream_start` 且业务 `seq` 重置。
- 多实例后端分别承接外部回调和浏览器 SSE。
- run 已完成、失败、取消或等待用户回答。

### 权限规则

- 仅登录用户可订阅。
- controller/biz 必须从数据库按 `run_id + current_user_id` 验证归属；不存在与无权访问均不得暴露事件或元数据。
- Redis Stream 不接受客户端指定 user ID，服务端只使用已验证身份和 run ID。

### UI 行为规格

- 页面位置：现有 Agent Chat，不新增页面或卡片类型。
- 交互模式：外部卡片结算后自动订阅；用户无需点击刷新或重新提交。
- loading：保留现有思考/文字光标和工具执行态。
- error：短暂断开自动按 cursor 重连；Redis/重试耗尽后启动快照降级。
- completed：最终 terminal 收到后关闭订阅并以持久化快照做一次轻量对账。
