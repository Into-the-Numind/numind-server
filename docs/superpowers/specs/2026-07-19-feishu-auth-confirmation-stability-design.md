# 飞书授权确认稳定性设计

## 1. 已锁定约束

1. 授权 session 与 provider device link 均以 10 分钟为上限。
2. 不改 lark-cli；其 device flow 保持每 5 秒一次查询。
3. 用户点击继续后，浏览器只调用现有 operation resume API，超时 60 秒。
4. 后端 CLI completion 最长 55 秒，给 HTTP 返回预留 5 秒。
5. 成功提交后立即调用现有 durable dispatcher，禁止人为延迟和自动确认。

## 2. 时序

`点击继续 → 校验当前用户/account generation/operation/session/phase/Agent link → 解密精确绑定的 device code → 官方 CLI 最多轮询 55 秒 → 校验结构化 scopes + HOME auth status + app ID → 原子提交 vault/session/account → 立即 dispatch 原 operation → HTTP 返回`。

浏览器 60 秒上限只覆盖这个请求，不改变其他 API 的 30 秒默认值。

## 3. 安全绑定

- URL 只作为短暂展示值；服务器以 opaque session/operation ID 关联它。
- DeviceAuthCredentialBinding 的 AAD 继续绑定 user、generation、app、operation、session、scope hash 和 resume expiry。
- 完成前重新读取 current account，并核对 app 与 generation。
- operation summary 必须与 waiting state、session ID、phase、recovery kind 完全一致。
- Agent operation 必须同时具有合法 AgentRunID 和 ToolCallID；两者缺一即拒绝。最终 dispatcher 继续使用 operation/run/tool tuple 的 durable exactly-once 校验。
- 任一不一致都不发布 candidate HOME，不恢复 Agent。

## 4. 安全诊断

扩展固定 `DeviceAuthOutcome` 与观察 phase，不返回也不记录原始 stderr。仅识别官方 CLI 的固定警告前缀并归类为：

- polling_pending_timeout
- polling_network_failure
- polling_read_failure
- polling_parse_failure
- polling_slow_down
- protocol_failure / retryable_dependency / completed
- reconcile_status_* / reconcile_app_*
- binding_verified / binding_rejected
- dispatch_succeeded / dispatch_retry

日志边界继续二次校验 phase、outcome、UUID 和固定 CLI 版本。禁止日志携带 URL、device code、token、scope 值、HOME、stdout/stderr 或文档内容。

## 5. 失败语义

- 55 秒内未得到完成证据：释放 lease 并返回 authorization_pending，允许用户再次点击。
- 网络/read/parse/slow-down：同样保持 pending，但日志保留准确分类。
- 协议或绑定不一致：fail closed，不发布凭据。
- 已完成但 dispatch 暂时失败：保持 durable completed，后续同一操作可幂等补偿，不重新执行飞书写入。

## 6. 拒绝方案

- 不把所有请求全局改成 60 秒。
- 不缩短官方 5 秒轮询或 fork lark-cli。
- 不自动确认，避免引入后台 worker 和额外竞态。
- 不相信浏览器传入的 app、device、run、tool 或 scope 字段。
