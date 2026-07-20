# 飞书绑定连续性修复 — 提案

## §1 方案概述 [客户可见]

修复首次连接飞书时“已经完成授权，但系统一直提示尚未完成”的阻断问题，并把创建个人应用、应用权限配置、用户授权、恢复原 Agent 任务统一成一条可恢复的连续流程。

用户在任何时刻只会看到一个当前可执行步骤。旧卡片、重复点击、页面刷新、多标签页、链接过期或服务重启都不会把旧操作误用到新授权阶段；系统会自动收敛到最新安全步骤。授权成功后，原 Agent 任务自动继续，不需要刷新页面或重新发送指令。

## §2 报价与周期 [客户可见]

- 预估工作量：1 个紧急开发日
- 报价：内部 P0 缺陷修复，不单独报价
- 交付时间线：2026-07-20 完成 Dev 部署与自动化验收

## §3 技术可行性 [AI 内部]

### 现有功能复用

- 复用 `FeishuOperationService`、持久化 operation/auth session、Agent Run external action 和现有 resume/refresh API。
- 复用 auth session 的 generation、lease、superseded、expiry 与 ownership fence。
- 复用 `FeishuActionCard`，不增加独立连接页或新入口。

### 技术风险

- **阶段串线**：旧卡片确认误触发新 session。通过浏览器提交精确 `session_id`，服务端校验当前阶段/session；不匹配时不执行副作用，只返回最新动作。
- **动作丢失**：operation 进入下一个等待阶段但 Agent Run 仍保存旧动作。dispatcher 对每个非终态 `OperationResult.Action` 做持久化替换，禁止静默丢弃。
- **并发与重复点击**：沿用 session lease/CAS；相同 session 幂等，不同或旧 session 只做只读收敛。
- **外部写重复**：授权阶段只恢复原有持久化 operation；未知写结果继续遵守现有 at-most-once fence，不扩大自动重试。
- **隐私**：不持久化授权 URL、App Secret、device code、CLI HOME 或完整命令；API 与日志仅保留安全关联 ID 和状态分类。

### 涉及仓库

- [x] numind-server
- [x] numind-web-v3
- [ ] numind-admin-web

### AI 可观测性（如功能涉及 LLM 调用）

- [x] 涉及 LLM 调用：否。本修复只调整已有 Agent 外部动作连续性，不增加 LLM 调用。
- Trace 起点：N/A
- Generation 点：N/A
- 关键元数据：结构化审计只记录 user_id、run_id、operation_id、session_id、phase、from/to status、stale/current 分类，不记录秘密或授权 URL。

## §4 产品需求定义 — PRD [AI 内部 — 不要为可读性简化]

### 用户故事

- 作为首次连接飞书的用户，我完成个人应用创建后，应立即看到“完成用户授权”的下一步，而不是继续操作已经失效的创建应用卡片。
- 作为正在授权的用户，我点击当前卡片后应立即看到处理中反馈；如果飞书尚未完成，系统应在短时检查后明确告诉我下一步，而不是无反馈等待 30 秒。
- 作为刷新页面或打开多个标签页的用户，我看到的所有旧状态都应自动收敛到服务端当前步骤，不会误确认另一个阶段。
- 作为已完成授权的用户，原 Agent 任务应自动继续一次；我无需重新发送 Prompt，系统也不会重复创建飞书内容。

### 验收标准

- [ ] user 438 等价流程中，create_app 完成后 Agent Run 的 pending external action 原子/持久地切换为新的 user_auth session。
- [ ] resume 请求携带卡片显示时的精确 session ID；旧 session 无权完成、领取或轮询当前 session。
- [ ] 服务端收到旧卡片、过期卡片或被替代卡片时，返回最新安全动作与 `stale_action` 结果，不返回误导性的 `authorization_pending`。
- [ ] 相同当前 session 的重复/并发确认保持幂等，只有合法 owner 能推进；服务重启后仍可恢复。
- [ ] 前端只把最新动作展示为可操作卡片，旧卡片保留历史但按钮禁用，并解释“已进入下一步”。
- [ ] 点击后 300 ms 内展示处理中状态；长期外部检查不造成无反馈或重复提交。
- [ ] 当前授权完成后原 operation 只恢复一次；取消、完成、unknown 或 generation 变化的 operation 不执行外部写。
- [ ] 首次连接、增量权限、刷新链接、拒绝、过期、重启、重复点击、多标签页均有永久回归测试。
- [ ] Go 相关测试、`task lint`、Vue lint/type-check、相关 unit 与 Playwright 全部通过。

### 边界情况

- 旧 create_app 卡片在 user_auth session 创建后被点击。
- 同一卡片双击、两个标签页同时点击、请求超时后再次点击。
- 当前 session 被 refresh/supersede、自然过期、被拒绝或 generation 改变。
- dispatcher 在 operation 状态提交后、Agent action 替换前崩溃。
- Agent Run 已取消/完成，或外部写结果为 unknown。
- 页面重新加载时快照缺 URL，需通过 operation summary 恢复一次性当前动作。

### 权限规则

- 仅当前登录用户可读取或推进自己的 operation/session/run；服务端同时校验 user_id、operation_id、run_id、generation 与 session ownership。
- 客户端提供的 phase/status 不作为授权依据；所有当前态从服务端数据库读取。
- 旧 session 只能得到安全的最新动作摘要，不能领取当前 session 或触发飞书调用。

### UI 行为规格

- 页面位置：现有 Agent 对话中的飞书动作卡片。
- 布局要求：保留现有卡片，不增加连接中心；标题、步骤说明、主按钮和状态提示保持单一主路径。
- 交互模式：当前卡片可点击；点击后立即禁用并显示“正在检测授权”；服务端返回新阶段时原位切换；旧卡片显示“此步骤已完成，已进入下一步”。
- 状态处理：`loading` 立即反馈；`waiting` 明确提示去飞书完成；`processing` 禁止重复点击；`stale` 自动切到最新步骤；`expired/denied/failed` 给出可执行恢复入口；`completed` 展示成功并自动回到原任务。

