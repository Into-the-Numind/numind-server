# Agent Run Survives Exit

## 来源
- 提出人：用户
- 提出日期：2026-08-03

## 需求描述
用户在使用任意一个 Agent 时，如果退出页面、关闭 tab、刷新或离开聊天页，当前 Agent 任务应继续在服务端运行。用户下次回到对应会话时，应能看到该任务最终生成的结果，而不是因为前端 SSE 连接断开而中止任务。

显式点击“停止任务”仍应取消 Agent；退出页面和停止任务必须在系统语义上区分。

## 业务目标
长任务和工具链任务经常超过用户保持页面打开的耐心或网络稳定窗口。让 Agent 在用户退出后继续完成，可以减少任务丢失、积分浪费感和“页面一关结果没了”的不确定感，让 Agent 更像可靠的后台工作台。

## 优先级
高

## Triage
- 推荐轨道：Standard
- 分类理由：
  1. 数据库 schema 变更：否（初步判断）
  2. 新增 API 端点：否（初步判断，优先复用现有 run/events/status API）
  3. 新外部服务集成：否
  4. 影响文件数：>3（后端 SSE 生命周期、run service、测试；可能涉及前端重连/状态处理）
  5. 高风险业务逻辑（支付/权限）：是（涉及 Agent 运行生命周期、取消语义、计费 reconcile 可靠性）
- 人类决定：确认 Standard，并要求开始

## 备注
- 当前流式路径 `/v1/agent-runs/stream` 会把 client disconnect 传播为 runner context cancellation，runner 将其映射为 `aborted_streaming`。
- 非流式 `/v1/agent-runs` 已使用 detached `context.Background()`，但用户端 AgentChat 主发送路径走流式 SSE。
- 目标语义：SSE 是可断开的观察通道；Agent 执行是服务端后台任务。只有显式 cancel API 才取消执行。
