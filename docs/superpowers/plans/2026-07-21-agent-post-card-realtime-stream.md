# Agent 卡片后可恢复实时事件流 — 实施计划

## 目标

将卡片后的 detached Agent continuation 接回浏览器实时 SSE，支持多租户、多实例、断线游标恢复；快照轮询只做异常降级。后端先发布，前端后发布，最终部署 Dev，Prod 不动。

## 原子任务

### T1 — 后端 RED：复现 detached 事件被丢弃

- 仓库：`numind-server`
- 文件：`internal/numind/biz/agent/external_tool_resume_realtime_test.go`
- 行为：构造会发出 reasoning、text、tool、terminal 的 external streaming runner，断言这些事件进入注入的 run event publisher。
- RED 原因：当前 `StudentRunService` 没有 broker 注入能力，`callRunner` 空 drain。
- Commit：`test(qa): reproduce dropped post-card stream events`

### T2 — 前端 RED：跨卡片逐帧 Playwright

- 仓库：`numind-web-v3`
- 文件：`e2e/agent-post-card-realtime-stream.spec.ts`
- 行为：初始 SSE 分时发送文字和外部卡片；后续 run-events SSE 再分时发送 reasoning、文字、工具和 terminal；在后续流未结束时逐段断言 DOM。
- RED 原因：前端不会请求 run-events endpoint，只能轮询快照。
- Commit：`test(qa): reproduce missing post-card realtime stream`

### T3 — Redis broker 与传输协议

- 仓库：`numind-server`
- 文件：
  - `internal/numind/biz/agent/stream/run_event_broker.go`
  - `internal/numind/biz/agent/stream/run_event_broker_test.go`
- 行为：定义 broker interface/PublishedEvent；Redis Streams `XADD MAXLEN ~4096 + EXPIRE 24h`；进程级 `PSUBSCRIBE` 直接分发 live payload；首次订阅 `XRANGE` 回放；cursor exclusive/去重；bounded subscriber；Redis 错误关闭订阅而不影响 Agent。
- 验收：cursor 校验/比较、fan-out、多订阅者不抢消息、取消清理、慢消费者不阻塞。
- Commit：`feat(agent): add resumable run event broker`

### T4 — 后端生产接线、权限与 SSE endpoint

- 仓库：`numind-server`
- 文件：
  - `internal/numind/biz/agent/student_run_lifecycle.go`
  - `internal/numind/biz/agent/external_tool_resume.go`
  - `internal/numind/biz/biz.go`
  - `internal/numind/controller/v1/agent/student_run.go`
  - `internal/numind/controller/v1/agent/student_run_stream.go`
  - `internal/numind/controller/v1/agent/student_run_stream_test.go`
  - `internal/numind/controller/v1/agent/run_events_test.go`
- 行为：broker 注入；正常 Create/Answer stream 先 publish 再写带 `id:` 的 SSE；external drain 改 publish；新增 `GET /v1/agent-runs/:id/events?after=`；DB ownership 校验；waiting terminal 保持订阅，最终 terminal 关闭；心跳与 disconnect 清理。
- 验收：owner 成功、cross-user/unknown 无泄露、首次 flush、cursor replay、broker 故障不取消 runner/finalize。
- Commit：`fix(agent): stream detached continuation events`

### T5 — 前端 cursor、attach 与重连

- 仓库：`numind-web-v3`
- 文件：
  - `src/types/agent-stream.ts`
  - `src/api/agent-stream.ts`
  - `src/composables/useAgentStream.ts`
  - `src/composables/__tests__/useAgentStream.spec.ts`
  - `src/stores/agentChat.ts`
  - `src/stores/__tests__/agentChat-streaming.spec.ts`
- 行为：解析 SSE `id`；保存 run transport cursor；识别 external_action/auth pause；初始 SSE 关闭后从 cursor attach；有界指数退避；最终 terminal 关闭；连续失败才启动快照轮询；continuation `stream_start` 重置单段 seq block 并恢复 running 状态。
- 验收：重复 cursor 不重复 delta；重连 after 为最后 cursor；普通 ask_user_question 不误 attach；stop/session epoch 中止旧订阅。
- Commit：`fix(agent): follow post-card realtime events`

### T6 — 回归、双审查与修复

- 仓库：两个
- 服务端：focused tests、`go test ./internal/numind/biz/agent/... ./internal/numind/controller/v1/agent/...`、`go test -race` focused、`task lint`。
- 前端：focused Vitest、`npm run lint && npm run type-check`、现有 Agent tests。
- 独立审查：规格一致性 + 代码质量并行；P0/P1 全清，P2 能修则修。
- Commit：按发现使用 `fix(agent): ...`，不混入无关修改。

### T7 — S5 Playwright 与 S6 Dev

- 运行新的 timed Playwright 和现有 Agent streaming/外部授权关键路径。
- `ndf-done` 分别在两个 worktree 原子 merge/push/cleanup。
- 按滚动兼容顺序部署 Dev：server → web。
- 验证 exact image、公开 health、web-to-api、实时跨卡片 DOM、无 panic/fatal；记录 Dev acceptance。

## 依赖顺序

`T1/T2 RED → T3 broker → T4 server → T5 web → T6 review/gates → T7 Dev`。

T1 与 T2 位于不同仓库，均必须是各自 feature branch 的第一个业务 commit。T3/T4 同仓库且共享接口，串行；T5 消费 T4 API；审查与部署不得提前。

## 发布兼容性

- 新后端 + 旧前端：旧前端继续快照轮询；新增 broker 不改变现有 JSON event。
- 新前端 + 新后端：正常使用 run-events SSE。
- 新前端遇到旧/不可用 endpoint：有界重试后回退现有快照。
- 不改 schema、计费、Agent prompt、工具权限或 OAuth 语义。

## 完成定义

- 客户 bug 的 RED 测试均转绿并永久保留。
- 卡片后 reasoning/text/tool/final 在流未结束前可见，无刷新、正常路径无五秒等待。
- 两账号/两 run 与同 run 两订阅者隔离/完整。
- lint/typecheck/test/Playwright/双审查通过。
- 两仓 develop 合并推送，Dev exact image 健康，Prod 未触碰。
