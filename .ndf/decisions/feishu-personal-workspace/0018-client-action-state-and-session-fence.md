# ADR 0018: 前端授权 action 状态与会话围栏

- Date: 2026-07-15
- Stage: S4 Task 17
- Status: accepted

## Context

飞书授权 action 会在 connect/refresh 的即时响应、Agent SSE、会话 snapshot、Task 11 durable continuation、页面隐藏/恢复、路由切换和终态事件之间流转。若沿用普通问答 answer 路径或把内部 action 直接传给 UI，可能泄露 scope、复用旧 URL、重复执行恢复，或在会话切换后让旧异步结果污染新对话。

后端 status/snapshot 不含临时 URL；Task 11 还会在授权完成后暴露 `external_resume_ready` 或 `ext_resume:<lease>`，此时前端必须停止授权步骤但继续观察原 Agent run，直到正常答案回到对话。

## Decision

1. 前端只调用 lifecycle 的 status/connect/resume/refresh/unbind API。公开 action 使用运行时 allowlist；scope、provider、token、argv、tool call 等内部字段不进入前端 action。URL 只在 connect/refresh 的内存 action 中存在，所有 snapshot、status、terminal、expired 和无 URL successor 强制剥离它。
2. `FeishuExternalAction` 是独立 Agent message 状态，不走普通 question answer。resume 只发送固定 action；成功、取消、未知、终态 SSE、expiry、`external_resume_ready` 和 `ext_resume:<lease>` 都结算卡片、清 URL、停止其 timer/listener。恢复队列不会伪造最终回答，反而启动原 run 的正常状态观察。
3. action 以 server `expires_at` 为唯一有效期边界。页面隐藏暂停，重新可见时仅 pending 且未过期 action 重新绑定当前会话 epoch 的单一轮询；terminal、替换、reset、dispose 和 session switch 全部清理 listener/timer。
4. 每次真正的 session switch/reset/unmount 都递增 client epoch 并停止旧 stream/narration/status observer。所有 await 结果、SSE、action timer、resume 回包必须同时匹配当前 epoch 与 run/session。唯一例外是严格验证的 `stream_start {session_id, run_id}` 将 optimistic `new` 绑定为同一服务器 UUID；已建立 session 的不匹配 fail closed。

## Consequences

- 授权完成后原 Agent operation 可自动回到页面，即使刷新、队列等待、页面隐藏或路由切换发生在中间；不会要求用户重复指令或重新点击旧链接。
- 旧会话、过期 action、终态事件和无 URL action 均不能在新会话中恢复 URL、重启轮询或写入结果。
- Task 18/19 只消费已收敛的 store/action 契约；设置页遗留的 connect-poll 行为必须在 Task 19 改为 status 驱动，不能以兼容层继续保留。
