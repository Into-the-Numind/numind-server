# ADR 0013: 受控 Agent 飞书工具与等待状态边界

- Date: 2026-07-13
- Stage: S4 Task 10
- Status: accepted

## Context

Agent 需要自主读取固定版本飞书技能并执行 Docs/Base/Wiki 命令，但多租户托管环境不能让模型提供 user、run、tool call、权限或幂等身份。授权等待还必须复用 Task 9 的可持久化 external action，且旧的直连工具不能继续出现在 registry 中。

## Decision

1. Adapter 生成的 synthetic tool call ID 通过私有 typed context key 注入 `FullTool.Execute`；Execute、narration 与 SSE 使用同一 ID，不同调用保持唯一。模型输入不能覆盖该 ID。
2. Agent 只暴露 `lark_skill_read` 与 `lark_execute`。前者仅接收 skill/reference/cursor，不要求用户连接；后者仅接收 argv/stdin_json/skill_receipts。userID、runID、toolCallID 与 `<runID>:<toolCallID>` 幂等键全部由服务端 context 派生。
3. 两个工具使用窄接口注入，Factory 采用 both-or-none；旧 `lark_create_doc`、`lark_read_bitable`、`lark_send_message`、`feishu_connect` 不再注册。新实现直接使用 `feishu.LarkCLIVersion` 和独立枚举式 soft error，不依赖 Task 20 将删除的 legacy common。
4. 顶层输入逐 token 严格解析，拒绝 unknown、大小写变体、duplicate、trailing JSON 及模型提供的身份/权限字段。可选 reference/cursor 只有真实 JSON string 合法；null、对象、数组、数字和布尔值均拒绝，空字符串保留为合法缺省语义。
5. `lark_execute` 仅允许四种 waiting 状态进入 durable yield，仅允许 succeeded/failed/unknown/cancelled 四种 terminal 返回 envelope。not_started、executing 与未来未知状态在 Data 序列化前 fail closed，固定错误不泄漏 state、operation ID、argv、receipt 或 provider data。
6. Task 9 durable action 要求 session_id 与 expires_at。OperationService 在 server-normalize confirmation action 后、进入 waiting transition 前拒绝空 session、零值或已过期 expiry；工具层不得凭空生成。实时 URL 可展示但不持久化，跨进程重载后 URL 为空仍保持等待。

## Verification

- 首轮规格审查发现 confirmation action 与 durable external contract 不一致，以及无 URL 重载缺少回归；下沉 OperationService 校验并补测试后复审 PASS。
- 质量审查发现 executing/未知 state 被当作普通完成结果、optional string 接受 null；显式状态 allowlist 与真实 JSON string 校验后复审 PASS。
- 最终规格与质量审查 P0/P1/P2 均为 0。
- Agent/Feishu 定向与完整测试、三轮 race、`task lint` 与 `git diff --check` 全部 PASS。
