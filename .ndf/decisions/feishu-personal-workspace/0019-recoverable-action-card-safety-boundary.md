# ADR 0019: 可恢复飞书操作卡的安全边界

- Date: 2026-07-15
- Stage: S4 Task 18
- Status: accepted

## Context

飞书授权与高风险确认是 Agent 对话中的外部动作，而不是用户对问题的普通回答。浏览器中的 URL 可能过期，刷新网络响应和 Agent SSE 也可能在页面离开、会话切换或操作已经终态后迟到。若把任何链接直接放入 `href`、允许不支持的步骤刷新，或让迟到响应覆盖终态，就会误导用户、泄露旧 URL 或重新暴露已完成操作。

## Decision

1. 只有 `external_action` 渲染飞书操作卡；resume、confirmed、cancelled 调 lifecycle operation API，绝不发 ordinary answer。历史 `pause_type=auth` 只显示不可交互的失效说明。
2. `create_app` 和 `user_auth` 才能刷新授权 URL。`app_scope`、`confirmation` 及无安全重建证据的失效状态仅提示用户重新发起，不能触发 refresh。
3. 授权 URL 在 Agent stream reducer 入口 fail closed：必须为 `https`、无 userinfo/fragment、仅默认 HTTPS 端口，且 host 仅为 `open.feishu.cn` 或 `open.larksuite.com`。通过校验后仍保留原始不透明字节供文本、复制、href 和二维码共同使用。
4. refresh 在请求前冻结 epoch、run、session 和 operation；响应返回后必须仍匹配当前 pending action，才允许按冻结 epoch 写回。session reset、动作替换、completed/terminal SSE 和卸载后的任何迟到响应都被丢弃。卡片的 expiry timer 在非 pending 状态立即清理。

## Consequences

- 用户在需要飞书官方操作时仍可在原消息处继续；授权完成、刷新、取消和确认不会被当成新的聊天输入，也不会重跑错误的旧路径。
- 非官方链接、不可重建的审批步骤和过期历史卡片均 fail closed；用户看到可解释的重新发起路径，而不是一个必然失败或不安全的按钮。
- Task 19 只消费这个安全的 action/state 契约，设置页不能重建旧的 POST connect 轮询或暴露临时 URL。
