# ADR 0017: 生命周期、解绑与终态恢复边界

- Date: 2026-07-14
- Stage: S4 Task 13
- Status: accepted

## Context

飞书个人工作空间需要由设置页和 Agent 卡片共同驱动连接、授权恢复、刷新和解绑。HTTP 端点若直接暴露内部授权 action，会泄露 scopes 或临时凭据；解绑若只删除一条账户记录，则可能让旧授权 worker、业务 CLI、临时 HOME、跨实例执行或 Agent external wait 在“已解绑”后继续存在。

同时，写 operation 可能已经在飞书成功，但第一次回填原 Agent tool call 失败；取消授权或解绑又可能使 Agent 永久停在 external wait。所有这些状态必须在多实例、重试和进程中断下收敛，且绝不重跑不确定或已成功的写操作。

## Decision

1. Lifecycle API 从登录态获得用户，严格限制 body。公开 action 使用 allowlist DTO：只返回 operation/session、phase、expiry，且只在 connect/refresh 的实时响应提供 URL；不提供 scope、provider、token、device code 或内部错误细节。
2. `user_completed`、`confirmed`、app-scope 完成和已成功 operation 的补偿全部复用 Task 12 dispatcher。成功 operation 的重复动作只恢复已存 tool result，永不重新 Claim 或执行 CLI；`cancelled` 只返回摘要或终结等待。
3. 取消、未知和解绑通过精确的 `(user, run, operation, tool call)` terminalizer 清除 Agent external wait，并回填原 tool call 的固定安全终态。取消前未执行的 operation 表示已取消；执行中写被中断则表示结果未知、需核对。终结持久化后取消任何已登记 continuation，重复调用幂等。
4. 解绑首先持久化 generation fence，拒绝新连接、worker、operation 与普通 vault 打开；随后 join auth worker、本机业务执行、并等待任意跨实例账号级 execution gate 排空。旧 generation 无法提交、续领或晚注册。
5. 清理阶段使用 durable retired-teardown owner lease。owner heartbeat 续租；失租或超时立即停止不可逆步骤。只有有效 owner 能在单一账户→gate 事务中删除 retired vault、清空连接/敏感元数据并释放 gate。其他实例等待 owner 完成或租约到期接管；失败保留 `disconnecting` 和同一 retired generation。
6. `lark_cli_version`、app ID 和 capability cache 只由受控版本探测、已验证的应用配置及结构化 catalog/classifier outcome 写入，并与 operation 终态原子提交。不会写入或返回 token、secret、raw CLI、scope。

## Consequences

- 用户看到“有数侧连接已删除”时，本机和跨实例授权/执行均已收敛，本地凭据也已由有效 cleanup owner 删除；否则界面仍显示可重试的解绑中。
- 授权完成、取消、异常中断和已成功但未回填的 operation 都能终结原 Agent wait，不要求用户重新描述任务，也不会重复写飞书资源。
- 生命周期服务保留了明确的多实例租约和终态边界，后续前端只能调用这五个接口，不能自行决定 scope、命令、用户或恢复逻辑。
