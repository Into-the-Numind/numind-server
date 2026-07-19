# 飞书授权确认稳定性 — 提案

## §1 方案概述 [客户可见]
只延长飞书“继续”这一个前端请求到 60 秒，后端最多用 45 秒调用官方 CLI，并为校验、提交和 HTTP 返回保留 15 秒。授权任务和飞书链接统一为 10 分钟。服务器继续使用官方 CLI 的 5 秒查询节奏；一旦确认成功，立即提交连接并恢复原 Agent，不增加额外等待或自动确认。

现有的用户、应用、设备码、操作和会话绑定继续保留；新增原 Agent 运行与工具调用链接的完整性检查。日志只记录固定分类和非敏感标识，能区分等待、网络、读取、解析、协议、应用不一致和恢复调度，不记录 URL、设备码、Token、权限正文或 HOME 路径。

## §2 报价与周期 [客户可见]
- 预估工作量：快速 Standard，当日完成
- 报价：N/A（现有项目修复）
- 交付时间线：2026-07-19 Dev

## §3 技术可行性 [AI 内部]
### 现有功能复用
- 复用 DeviceAuthCredentialBinding 的加密 AAD 绑定。
- 复用 WorkspaceLifecycleService、durable operation/session summary 和 Agent resume dispatcher。
- 复用 DeviceAuthObservation 的安全白名单日志边界。

### 技术风险
- 60 秒浏览器等待仍需给校验、提交和 HTTP 返回留余量：CLI 上限设为 45 秒。
- CLI 超时可能掩盖网络/解析问题：从固定 stderr 前缀提取安全诊断分类，绝不记录原文。
- 并发重复点击：继续沿用 session lease、generation fence 和 exactly-once dispatcher。

### 涉及仓库
- [x] numind-server
- [x] numind-web-v3
- [ ] numind-admin-web

### AI 可观测性（如功能涉及 LLM 调用）
- [x] 涉及 LLM 调用：否（只恢复既有 Agent 任务，不新增模型调用）
- Trace 起点：N/A
- Generation 点：N/A
- 关键元数据：固定授权观察字段 user_id、generation、operation_id、session_id、phase、outcome_class、cli_version、duration

## §4 产品需求定义 — PRD [AI 内部 — 不要为可读性简化]
### 用户故事
- 作为已在飞书完成授权的用户，我点击继续后需要系统当场核对并恢复原任务，而不是 30 秒后显示请求超时。
- 作为运维者，我需要从安全日志判断是仍待授权、网络、读取、解析、协议、应用绑定还是恢复调度问题。

### 验收标准
- [ ] 默认授权 session 与飞书设备授权链接均为 10 分钟。
- [ ] 官方 CLI 仍以其固定 5 秒间隔轮询，无 Numind 侧改频。
- [ ] 后端 CLI 完成窗口为 45 秒，前端仅 resume 请求为 60 秒。
- [ ] 成功后同一调用立即 dispatch 原 Agent operation。
- [ ] 用户、generation、operation、session、phase、app、scope、device credential 与 Agent run/tool link 均 fail-closed。
- [ ] 日志可区分 CLI pending timeout/network/read/parse/slow-down/protocol 及 reconciliation/dispatch 阶段，且不泄漏凭据。

### 边界情况
- 链接或 session 过期；重复点击；多实例争抢 lease；用户/应用/generation 变化；operation summary 或 Agent link 损坏；CLI 网络、read、parse、输出截断；授权成功但应用不一致。

### 权限规则
- 仅当前登录有数用户、当前飞书 account generation 可继续自己的 operation。
- 浏览器仍只提交固定 action，不提交设备码、URL、scope、app ID、Agent run ID 或 tool call ID。

### UI 行为规格
- 页面位置：Agent 对话中的飞书授权卡片。
- 布局要求：不改现有布局。
- 交互模式：用户点击“我已完成，继续”。
- 状态处理：立即显示处理中；60 秒内使用现有 success/pending/replacement/error 状态；不增加自动确认。
