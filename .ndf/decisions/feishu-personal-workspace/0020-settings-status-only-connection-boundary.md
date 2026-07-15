# ADR 0020: 设置页只读状态与安全入口边界

- Date: 2026-07-15
- Stage: S4 Task 19
- Status: accepted

## Context

旧设置页会反复调用 `POST /v1/feishu/connect`、打开新窗口并轮询推进连接。这既绕开了 Agent 原任务的恢复语义，也可能在另一个授权会话已接管后暴露过期 URL。设置页需要保留连接、继续、重新授权和解绑入口，但它是辅助状态面板，主入口仍是 Agent。

## Decision

1. 设置页挂载和刷新仅调用 `GET /v1/feishu/status`；不调用 connect、不轮询、不打开临时 URL。连接、继续连接和重新授权按钮只把用户带回 Agent，由 Agent external action card 承接官方页面与原任务恢复。
2. 单卡展示真实 connection state、脱敏 app ID 和 Docs/Base/Wiki 最近已知能力。`unknown` 固定显示为“尚未验证”，不暗示已授权；文案明确能力首次使用时按需授权，不包含消息发送。
3. `disconnecting` 是安全收敛态：不能显示任何 Agent 连接入口，只有只读状态刷新。这样不会在 generation teardown 中尝试新 operation。
4. 解绑使用 ConfirmModal，并明确它只移除有数侧的连接/授权资料；飞书侧远端应用和已有资源保留。

## Consequences

- 用户不会被设置页的旧轮询或过期链接带偏；需要飞书官方操作时会回到能精确恢复原工具调用的 Agent 卡片。
- 连接状态与脱敏应用身份在等待、重新授权、已连接和解绑收敛态均保持可解释，移动端也不会出现横向溢出。
