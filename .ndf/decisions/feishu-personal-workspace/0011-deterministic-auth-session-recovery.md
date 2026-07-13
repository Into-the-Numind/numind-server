# ADR 0011：确定性授权会话恢复与 operation activation barrier

- 状态：Accepted
- 日期：2026-07-13
- Feature：feishu-personal-workspace

## 背景

飞书个人应用创建和用户授权都是会先输出 URL、再阻塞等待用户完成的长生命周期 CLI 过程。URL/device code 不能持久化，而有数后端存在多实例、进程重启、租约接管和原 operation 自动恢复；若只靠单机内存或先 dispatch 后写 waiting，会产生重复 worker、递归 resume 或丢失恢复。

## 决策

1. `feishu_auth_session` 是跨实例会话 SOT，只持久化 tenant、generation、operation、phase、规范 scopes、state、lease 和 expiry，不保存 URL/device code。相同 intent 在 account 行锁内原子复用；另一进程没有内存 URL 时返回同 session 的无 URL 等待卡，由 Task 13 显式 refresh 废弃并重生链接。
2. 每次 worker、过期恢复或 app approval 使用唯一 lease token。Claim、Renew、terminal update 和 completed finalize 均先锁 active account，再对 pending session 做 owner + unexpired lease CAS；过期 token 不可复活，account 状态与 session completed 在同一事务提交。
3. Operation-linked worker 输出合法 URL 后必须停在 activation barrier。`OperationService` 先持久化 waiting，再调用强制接口 `Activate`；失败路径调用 `Abort`。`RecoveryStarter` 必须同时实现 Start/Activate/Abort，不允许可选能力。
4. CLI exit 0 和 `ok=true` 仍不足以判定授权 worker 成功；本次进程必须先观察并通过 phase-specific 官方 HTTPS URL 校验。无 URL、非法 URL、输出超限、lease 丢失均 fail closed。
5. completed 或 recovery-only `auth status` 已授权时，`StartRecovery` 只返回 nil，让当前 `Operation.Resume` 调用链直接 claim/replay，不同步反向 dispatch，避免递归。真实 activation worker 和 app approval 完成后才投递共用 dispatcher；completed app approval 允许幂等重试 dispatch。
6. 手动首次连接完成 create_app 后自动启动仅含 `offline_access` 的 user_auth；业务 Docs/Base/Wiki scopes 永远来自当前 Catalog 命令并增量申请。

## 后果

- 正常业务热路径仍不执行 `auth status`；该命令只用于过期授权会话恢复。
- 多实例不会复制 blocking login，也不会为共享 URL 违反无持久化约束；无 URL 卡必须由后续 refresh API 提供明确恢复动作。
- Task 12 生产 bridge 必须暴露强类型 Start/Activate/Abort；Task 13 必须实现 session refresh；Task 21 在真实 MySQL 8 双连接环境复验 account→session 锁序和 generation 交错。
