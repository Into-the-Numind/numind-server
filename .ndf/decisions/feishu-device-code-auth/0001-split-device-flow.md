# ADR 0001: 使用服务端两段式 lark-cli 用户授权

- 状态：Accepted
- 日期：2026-07-16
- 决策人：Michael
- 关联需求：`requirements/feishu-device-code-auth.md`
- 关联提案：`proposals/feishu-device-code-auth-proposal.md`

## 背景

现有服务把 `lark-cli auth login --json` 当成一个会先输出 URL、再持续等待用户完成的阻塞进程。固定版本 `lark-cli 1.0.68` 为 Agent 提供的正式协议是两段式：`--no-wait --json` 立即返回授权 URL 和恢复凭据，之后再以 `--device-code` 完成认证。旧假设导致服务等待 30 秒后失败，无法继续原来的飞书 operation。

## 决策

采用服务端拥有的两段式授权状态机：

1. start 阶段调用 `auth login --scope ... --no-wait --json`；
2. 恢复凭据只在服务端加密保存，并精确绑定用户、generation、应用、operation/manual、session、scope 和有效期；
3. 用户确认后由 lease owner 调用 `auth login --device-code ... --json`；
4. HOME 持久化和账号/session 事务完成后，恢复同一个持久化 operation；
5. Agent 不得通过普通 `bash_exec` 执行 `lark-cli`，飞书操作统一路由到 `lark_execute`。

## 不变量

- 恢复凭据不进入前端、LLM 上下文、日志、公开错误或普通 Agent 沙箱。
- 旧 generation、旧 session 和已终态 operation 不得调用外部写接口。
- 自动恢复只适用于明确尚未产生副作用的 waiting operation。
- 无远端幂等键的飞书写入采用 at-most-once；结果不确定进入 `unknown`，不盲目重试。
- 多实例并发由 auth session lease/fencing、账号 generation 和 Vault revision CAS 共同限制。

## 备选方案

- 延长阻塞流程：改动小，但依赖长进程和内存状态，不符合固定 CLI 协议，拒绝。
- Go 直接实现飞书 OAuth：控制力更强，但扩大公开回调、Token 生命周期和安全范围，留作未来替换 adapter 的选项。

## 影响

- 需要一次向后兼容的 nullable migration 保存加密恢复材料。
- 主要影响 `numind-server`；目标是复用现有前端卡片和确认接口。
- S2 必须锁定固定 CLI JSON contract、密文字段/AAD、崩溃对账和现有 API 状态映射。
