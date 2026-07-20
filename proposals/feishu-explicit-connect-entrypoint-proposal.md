# 飞书显式连接入口方案

## 决策

采用独立的现代 `lark_connect` Agent 工具，并让设置页直接调用既有 lifecycle connect API。两者共享当前账户、授权 session、generation fence 和恢复状态机。

## 备选

- 仅改 Prompt：仍依赖模型是否选择业务命令，无法保证显式连接立即发生。
- 让 `lark_inspect` 在未连接时产生副作用：破坏只读语义，用户只问状态也会被强制授权。
- 自动执行虚假的 Drive 搜索：会提前申请业务 scope，并在连接后执行用户未要求的业务动作。

## 方案边界

- Agent 侧创建 `connection_only` 的持久化 operation。它在未连接时走现有 RecoveryCreateApp/RecoveryReauth，在连接建立后直接成功，不执行 CLI 业务命令。
- 工具输入为空，身份和幂等键只来自可信上下文；等待结果复用现有 external-action 卡片、resume、refresh 和自动续跑。
- 设置页保留 manual lifecycle：按钮立即调用既有 `/v1/feishu/connect`，只渲染服务端 live action；完成后重新调用 connect/status 以收敛到下一阶段。
- 更新 hosted policy 和三个 Agent definition tool flags，让显式连接意图必须选择 `lark_connect`，而业务意图仍走 `lark_execute`。

## 风险控制

- connection-only 标志只由服务端构造并加密持久化，不进入模型或 HTTP 输入。
- connection-only operation 只允许固定 path/domain/risk/scopes，不进入 command catalog，也不调用 runner。
- 现有 operation/user/generation/tool-call 幂等和恢复约束保持不变。
