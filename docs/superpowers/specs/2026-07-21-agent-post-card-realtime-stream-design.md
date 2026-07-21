# Agent 卡片后可恢复实时事件流设计

## 1. 问题与证据

外部操作卡片会结束原 HTTP/SSE 响应，外部回调随后在 detached continuation 中继续 Agent。`external_tool_resume.go` 当前创建 `stream.Event` channel 后用空循环 drain，明确丢弃全部事件；前端只能每五秒重新读取持久化快照。因此存储最终一致，但 reasoning、assistant text 和工具进度没有实时传输路径。

已有 Playwright happy-path 把整个 SSE body 一次性返回且只断言最终答案，无法发现跨卡片、按时间到达的事件缺失。

OpenAI Codex app-server runtime 的关键参考是稳定的事件身份和生命周期：item 先 started，再按 `threadId + turnId + itemId` 发送 delta，最后 completed。莫小派保留现有事件类型，但增加独立 transport cursor，使同一 run 跨多段 HTTP 和 detached continuation 仍有唯一全序。

## 2. 方案选择

### A. 单进程内存事件总线

改动最小，但外部回调和浏览器落到不同实例、进程重启或连接短暂中断时会丢事件，不满足可恢复与多实例目标。

### B. Redis Streams + 进程级通知复用（批准）

Redis Stream 保存短期有序事件，Redis Pub/Sub 只发送“有新事件”的通知；每个应用进程用一个模式订阅连接把通知分发给本地 SSE 会话。客户端按 cursor 用短 `XRANGE` 回放，不为每个在线用户占用阻塞 Redis 连接。现有 Redis 已部署，方案支持多账号、多会话和多后端实例。

### C. 数据库事件表/outbox

持久性最强，但引入 migration、写放大、清理任务和更高查询延迟。Agent run/messages 已是最终持久化 SOT，实时层不需要永久事件日志。

## 3. 后端组件

### 3.1 RunEventBroker

新增窄接口，runner/controller 不依赖 Redis 细节：

```go
type RunEventBroker interface {
    Publish(ctx context.Context, runID uint64, event stream.Event) (cursor string, err error)
    Subscribe(ctx context.Context, runID uint64, after string) (<-chan PublishedEvent, <-chan error)
}
```

`PublishedEvent` 包含 Redis Stream ID 和原 `stream.Event`。Redis 实现使用 key `numind:agent:run:{runID}:events`，entry 只保存 JSON event；每次 publish 执行 `XADD MAXLEN ~ 4096`、刷新 24 小时 TTL，再 publish wake channel。正文不进入日志或指标，只存在有 TTL 的事件值。

应用进程维护一个 `PSUBSCRIBE numind:agent:run:*:wake` 连接和 `runID -> local subscribers` 映射。订阅者注册后先从 `after` 做 `XRANGE`，再处理 wake；所有读取都是短命令。replay/wake 竞态可导致再次读取但不能丢失，cursor 去重保证只交付一次。

Redis 未初始化或命令失败时，`Publish` 返回可观测错误但不得让 Agent execution/finalize 失败。

### 3.2 生产事件接线

- `CreateStream` 与 `AnswerStream`：controller 收到每个 event 后先 publish，再输出 `id: <redis-stream-id>` 与 `data: <event-json>`；publish 失败仍输出 data，但无 cursor，并标记降级。
- detached external continuation：把当前空 drain 改成逐事件 publish，不再丢弃。publisher 使用 run 的服务端 ID，不读取客户端 user ID。
- 普通非流式 run 不新增事件；持久化流程不变。

### 3.3 可恢复订阅 API

新增并注册：

```http
GET /v1/agent-runs/:id/events?after=<redis-stream-id>
Accept: text/event-stream
Authorization: Bearer <user_token>
```

处理顺序：解析 ID → 按 `run_id + current_user_id` 查询 run → 建立 broker subscription → flush 首字节 → 对每条 event 写 SSE `id`/`data` 并 flush → heartbeat。无权访问与不存在使用同一安全错误外观。客户端断开立即注销本地订阅。

等待外部操作的 terminal 不关闭 attached subscription；completed/failed/cancelled 等最终 terminal 在写出后关闭。断线由浏览器携带最后 cursor 重连。

## 4. 前端协议与状态机

### 4.1 Transport cursor

SSE parser 同时解析 `id:` 和 `data:`，把 `cursor?: string` 附加到 transport envelope。Redis Stream ID 是跨 continuation 的总序；现有事件 `seq` 继续用于单段兼容，但排序和去重优先使用 cursor，避免 continuation 从 `seq=1` 开始后被排到旧消息之前。

store 按 run 保存最后已应用 cursor。游标比较按 Redis ID 的 `<milliseconds>-<sequence>` 两段整数进行，不转 JavaScript `Number`。

### 4.2 Attach 生命周期

原始 `streamAgentRun` 每帧更新 cursor。首次流因外部操作卡片进入 waiting terminal 后，composable 对同一 run 调用 `subscribeAgentRunEvents(after=lastCursor)`。订阅保持到最终 terminal、用户取消、切换 session 或组件卸载。

网络错误使用有界指数退避并从最后 cursor 重连。连续失败或服务端明确 broker unavailable 时才启动既有 run snapshot polling。恢复订阅后停止快照轮询，防止双写 UI。

同一账号多个标签页各自维护 cursor；服务端不使用 Consumer Group，所以一个标签页不会夺走另一个标签页的事件。

### 4.3 UI 事件

卡片后的 reasoning、assistant text 和工具事件继续走同一个 `applyStreamEvent`，不另建第二套渲染逻辑。重复 cursor 在进入 reducer 前丢弃；稳定的 `message_id`、`tool_call_id` 继续聚合同一 item。最终 terminal 后做一次 snapshot 对账，仅用于修正持久化完成与账单状态。

## 5. 安全、容量与退化

- 所有订阅先做数据库 ownership 检查，Redis key 不构成授权。
- 不记录 JWT、prompt、reasoning、assistant text 或工具输出到结构化日志。
- 每 run 最多约 4096 个短期事件且 24 小时过期；活跃 run 每次 publish 续期。
- 慢客户端使用有界本地 channel；溢出时断开并要求按最后 cursor 重连，不阻塞 publisher。
- Redis/PubSub 断开自动重订阅；Stream 提供断开窗口的回放。
- Redis 故障不取消 Agent，不影响 DB snapshot/finalize；前端降级轮询。

## 6. 测试与验收

### 后端

1. RED：detached continuation 发出 reasoning/text/tool/terminal 后，可恢复订阅目前收不到。
2. broker：顺序、cursor-exclusive replay、重复 wake 去重、TTL/MAXLEN、慢订阅者、Redis unavailable。
3. controller：首字节、逐帧 flush、SSE id、最终 terminal、client abort。
4. 权限：owner 成功；其他用户、未知 run 无事件泄露。
5. 回归：CreateStream/AnswerStream 仍实时，Agent finalize 不依赖 broker 成功。

### 前端

1. parser 解析 SSE id；cursor 比较覆盖大整数与同毫秒序列。
2. composable 在 waiting external action 后 attach，重连从最后 cursor 继续。
3. store 对重复 cursor 幂等，post-card 的重置 `seq` 不会乱序。
4. Playwright 分时写入卡片前文字、卡片、卡片后 reasoning/text/tool，流未结束前逐段断言 DOM。
5. Redis/broker 失败时才启用 snapshot fallback。

## 7. 发布与回滚

后端先部署 Dev：新 endpoint 与 publisher 对旧前端无副作用。再部署前端：新前端若 endpoint 不可用会回退快照。验证多人隔离、跨卡片实时性、重连和健康日志。回滚前端后后端事件流可保留；回滚后端时前端自动走兼容降级。Prod 不在本次授权范围。

## 8. 明确不做

- 不持久化完整事件到新数据库表。
- 不改变 Agent 的 LLM、工具执行、计费或外部授权语义。
- 不删除现有 run/messages snapshot 与 narration 机制。
- 不以缩短五秒轮询间隔冒充实时流。
