# 飞书连续授权卡片与过期刷新修复 — 提案

## §1 方案概述 [客户可见]
本次不是修改飞书能力本身，而是补齐同一个 Agent 任务连续申请多项权限时的交接链路。

采用前后端配套修复：前端在 Agent 恢复后继续轮询时，既识别普通问题，也识别新的飞书授权动作，并在五秒轮询周期内追加第二张卡片；后端把“时间已经过期、数据库仍处于等待授权”的合法状态纳入刷新状态机，原子废弃旧会话并生成新链接。已经完成的 Base 操作不会重放。

用户最终看到的行为是：第二张授权卡片无需刷新页面即可出现；若一次性 URL 已不可用，点击“重新生成链接”会得到新链接，而不是内部服务器错误。

## §2 报价与周期 [客户可见]
- 预估工作量：快速 Standard，一次开发周期
- 报价：N/A（内部产品开发）
- 交付时间线：本轮完成开发、验证并部署 Dev

## §3 技术可行性 [AI 内部]
### 现有功能复用
- 复用 `refreshRunStatus()` 的会话/运行 epoch 围栏与五秒外部动作轮询。
- 复用 `externalActionMessage()` 的字段校验和官方 URL 白名单；快照不恢复一次性 URL。
- 复用现有 `POST /v1/feishu/actions/:session_id/refresh`，不新增 API。
- 复用 `ReplaceDeviceAuthSession` 的事务框架，将旧 session 终态化、清凭据、创建 replacement、重绑 operation 放在同一事务。
- 复用现有用户、generation、operation、summary、scope hash 与 lease 校验。

### 方案比较

#### A. 仅做表面容错（最小改动，拒绝）
- 前端补卡；后端只把 500 改成友好错误。
- 优点：改动少。
- 缺点：过期链接仍无法刷新，用户仍被卡住；掩盖状态机缺口，不能接受。

#### B. 前后端状态感知修复（选择）
- 前端补回连续 `external_action`，按 run + operation 去重并保留历史完成卡。
- 后端仅允许“服务器时间已过期、无活动 worker、精确绑定当前等待 operation”的 pending v2 session 原子替换。
- 优点：直接修复两个真实根因，沿用现有接口和安全模型，不重放业务命令。
- 缺点：需要跨仓库回归和并发测试。

#### C. 持久化/回放完整实时授权事件（长期架构，本次不做）
- 为恢复后的 Agent 建立新的事件订阅或持久化一次性 URL。
- 优点：理论上可让所有续跑事件完全实时回放。
- 缺点：扩大敏感 URL 生命周期和系统复杂度；为两个已定位缺陷引入新协议，不符合本次范围。

### 技术风险
- 快照中的 `external_action` 不含 URL，不能覆盖同 operation 已收到的 live URL，也不能由前端拼接 URL。
- 同一 run 连续授权的快照消息 ID 可能相同，必须为新 operation 生成独立本地 ID，避免 Vue key 冲突。
- 过期 pending session 可能尚有完整加密 device code，也可能已经由清理器清空；两种合法形态都要支持，部分损坏形态必须拒绝。
- 并发刷新只能一个成功；活动 lease 必须拒绝 replacement。

### 涉及仓库
- [x] numind-server
- [x] numind-web-v3
- [ ] numind-admin-web

### AI 可观测性
- [x] 涉及 LLM 调用：否。本次不新增或修改模型调用。
- Trace 起点：N/A
- Generation 点：N/A
- 关键元数据：N/A

## §4 产品需求定义 — PRD [AI 内部]
### 用户故事
- 作为在一个 Agent 任务中连续使用多项飞书能力的用户，我需要后续授权卡片自动出现在当前页面，以便无需刷新即可继续原任务。
- 作为看到授权链接已过期的用户，我需要“重新生成链接”可靠返回新链接，以便恢复操作而不遭遇 500。

### 验收标准
- [ ] 第一个 operation 授权完成后，若同一 run 产生不同的第二个 `external_action`，第二张卡在下一轮轮询内出现，无需刷新页面。
- [ ] 第一张卡保留完成态，第二张卡为待处理态；重复轮询不重复插入。
- [ ] 迟到快照、切换会话和同 operation 的无 URL 快照不能污染新会话或降级已有 live URL。
- [ ] 对服务器判断已过期、仍为 pending、无活动 lease、精确绑定 waiting operation 的 v2 session，刷新返回 200 和新 action。
- [ ] replacement 在一个事务内完成旧 session=expired、旧凭据清理、新 credential-free session 创建、account 更新、operation 重绑。
- [ ] 未过期 pending、活动 lease、部分凭据、错误用户/generation/operation/summary/scopes/hash 全部 fail closed。
- [ ] 两个并发刷新最多一个提交 replacement。
- [ ] 不调用 Agent `/answer`，不重放 Base/Docs/Wiki 业务命令。
- [ ] “链接刚过期后点击继续”的相邻路径不会再暴露无意义 500。

### 边界情况
- snapshot 中连续授权使用相同合成 message id。
- snapshot 响应到达前用户切换会话或 run 已终态。
- 同 operation live SSE 已携带合法 URL，随后轮询返回无 URL snapshot。
- 清理器已清 device credential 但保留 pending，或清理尚未发生仍有完整 credential。
- 两个浏览器同时刷新同一过期卡片。

### 权限规则
- 仅当前登录用户、当前飞书连接 generation、当前 operation 绑定的 session 可刷新。
- scope 与 scope hash 必须和原授权请求一致。
- 前端不持久化、不推断、不拼接一次性授权 URL。

### UI 行为规格
- 页面位置：Agent 对话时间线中的现有飞书授权卡片。
- 布局要求：复用现有 `FeishuActionCard`，不新增视觉组件。
- 交互模式：第二张卡自动出现；无 URL 时保留现有“重新生成链接”按钮；刷新成功后原位更新为新链接。
- 状态处理：轮询期间维持真实处理态；待授权显示卡片；刷新失败显示安全可重试文案；完成后结束卡片等待和工具 spinner。
