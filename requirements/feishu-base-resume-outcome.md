# 飞书 Base 授权后终态修复需求

## 背景

Dev 用户创建飞书多维表格时，按需授权成功并点击“我已完成，继续”。后端已确认用户、应用、授权 session、原 operation 与 Agent tool call 绑定一致，随后 `base +base-create` 启动真实写入并进入 `unknown`，但浏览器最终收到 HTTP 500 `Internal server error`。

## 用户目标

1. 授权成功后立即恢复原操作。
2. 飞书写入成功时，原 Agent 继续完成任务。
3. 飞书写入失败、取消或结果未知时，原 Agent 等待必须被安全终结，浏览器不得收到伪装成服务器故障的 500。
4. 写操作结果未知时绝不自动重试，避免重复创建 Base。
5. 日志必须能区分 CLI 失败类型与 Agent 交接结果，但不得记录 token、HOME、argv、正文、字段值、URL、scope 或原始 stdout/stderr。

## 已确认事实

- 官方 lark-cli v1.0.68 的 `+base-create` 命令、字段 JSON 和 scopes 与本次 Agent 请求一致。
- v1.0.72 没有修改 `+base-create` 实现，因此升级 CLI 不能直接解释或修复本次故障。
- 当前 dispatcher 把 `failed/unknown/cancelled` 也送入仅用于成功 continuation 的 `AgentRunResumer.Resume`；该错误阻断 lifecycle 后续的 terminal finalization，最终映射成 HTTP 500。
- 当前 write-like operation 在业务进程启动后统一进入 `unknown`，且没有保留安全的结构化失败分类，导致无法判断真实 Base API 失败阶段。

## 验收标准

- [ ] `succeeded` 仍通过原 tool call 精确恢复 Agent。
- [ ] `failed/unknown/cancelled` 使用 durable terminal finalizer，不触发模型 continuation。
- [ ] terminal finalizer 成功或幂等 no-op 时，授权确认接口正常返回业务终态，不返回 500。
- [ ] finalizer 暂时失败时仍返回可重试服务错误，不能假装成功。
- [ ] write-like operation 保持最多一次真实 CLI 调用。
- [ ] 产生严格 allowlist 的 operation observation，包含固定 phase/outcome/CLI error tuple 与 operation identity，不含敏感业务数据。
- [ ] 客户故障回归测试先 RED 后 GREEN；全量 Go test、race、lint 通过。
- [ ] 合并 develop 并部署 Dev；不部署 Prod。

## Triage

推荐并已确认：Standard。无 DB schema、新 API 或新外部集成，但修改第三方授权、Agent durable resume 和安全可观测性边界，属于高风险权限链路且预计超过 3 个文件。
