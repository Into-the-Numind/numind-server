# 飞书 Agent 稳定执行 — 提案

## §1 方案概述 [客户可见]
把飞书执行改成与 Codex 同类的职责分工：Agent 负责理解用户目标并选择 Docs、Base、Wiki、Drive 业务命令；平台根据当前登录账号自动绑定对应飞书账号，校验命令、补齐固定参数、检查权限、发起授权、恢复任务并保证写操作不会重复。

Agent 读取飞书技能仍然有价值，但只用于学习命令，不再让 Agent 抄写一串内部票据作为执行门票。可自动修正或恢复的尝试在前端显示为“处理中”，不再提前显示红色“执行出错”；真正不可恢复或会产生未知写入风险的失败仍立即停止并明确展示。

## §2 报价与周期 [客户可见]
- 预估工作量：快速 Standard，一个开发周期
- 报价：内部产品开发，不单独报价
- 交付时间线：2026-07-19 部署 Dev 验收

## §3 技术可行性 [AI 内部]
### 现有功能复用
- `CommandCatalog.Normalize` 已能从 argv 派生 domain、risk、scope，并安全规范化 `--as user`、`--format json`。
- `FeishuOperationService` 已提供当前用户隔离、加密持久化、执行 gate、scope preflight、授权恢复、确认和写操作幂等。
- `larkExecuteRetry*` 已提供同一 run 的修正预算与不可重试结果停止。
- Agent SSE 与 narration 已区分真实 Go error 和可供模型修正的 soft error，可扩展 recoverable 标记。

### 方案比较
#### A. 仅取消 receipt 必填（最小方案）
- 优点：改动最少，立刻消除抄错票据。
- 缺点：错误仍被统一成策略拒绝；前端仍会把模型可修正尝试显示成红色错误；稳定性问题只解决一部分。

#### B. 平台拥有执行合同（采用）
- 做法：receipt 从模型协议移除，旧字段过渡期接受但忽略；服务端继续从受控 argv 和当前用户上下文推导安全条件；结构化区分输入拒绝、可恢复 soft error 和终止错误；前端将 recoverable 尝试显示成处理中。
- 优点：同时修复根因、错误语义和用户体验；复用当前安全与恢复基础设施；多实例和重启稳定。
- 缺点：跨后端和前端，必须增加协议兼容与回归测试。

#### C. 服务端 receipt 会话缓存
- 优点：保留现有 receipt 验证接口。
- 缺点：引入进程/实例状态、过期和重启一致性问题；仍让执行依赖技能读取时序；与目标相反，不采用。

### 技术风险
- 取消 receipt 后必须证明命令白名单、当前用户身份和 scope preflight 不受影响。通过禁止命令、跨用户、写幂等和 unknown-result 回归测试锁定。
- 旧对话可能仍发送 `skill_receipts`。过渡期后端接受但忽略该字段，避免已在运行的会话突然失败；新 schema 不再向模型展示。
- recoverable 只用于 nil-Go-error 的 soft tool result；真实执行错误和未知写结果仍为终止错误，禁止被前端淡化。
- 不在本期引入 DB、HTTP API 或后台自动任务。

### 涉及仓库
- [x] numind-server
- [x] numind-web-v3
- [ ] numind-admin-web

### AI 可观测性（如功能涉及 LLM 调用）
- [x] 涉及 LLM 调用：是，但不新增 LLM 调用
- Trace 起点：沿用 Agent run 现有 trace
- Generation 点：无新增 generation
- 关键元数据：沿用 agent_run_id、tool_call_id；禁止记录 receipt、token、argv 正文和飞书内容

## §4 产品需求定义 — PRD [AI 内部 — 不要为可读性简化]
### 用户故事
- 作为任意 Agent 的使用者，我需要 Agent 在我的飞书工作区稳定完成受控业务操作，而不因内部票据复制错误随机失败。
- 作为用户，我需要只在任务真正失败时看到红色错误，以便把“系统正在自动调整”和“任务已失败”区分开。
- 作为平台，我需要在提高稳定性的同时继续阻止跨账号、非白名单命令、缺权限写入和未知结果重试。

### 验收标准
- [ ] `lark_execute` 和 `lark_inspect command` 的模型 schema 不再要求或宣传 `skill_receipts`。
- [ ] 不带 receipt 的合法 Docs/Base/Wiki/Drive argv 能到达当前用户的受控 OperationService；携带旧 receipt 字段仍兼容，字段值不参与授权。
- [ ] `auth/config/whoami/im/shell`、额外身份字段、越权 argv 仍在访问飞书前失败。
- [ ] `lark_skill_read` 不向模型返回 receipt，托管规则不再要求复制 receipt。
- [ ] 可修正 soft tool error 的 SSE 标记为 recoverable，前端显示“正在调整执行方式”，不显示红色“执行出错”。
- [ ] 真实 Go error、unknown write、重试耗尽等终止错误仍显示红色错误并停止。
- [ ] 新旧流式与历史/轮询渲染行为一致；刷新页面不改变最终状态。
- [ ] 后端测试、lint、race gate，前端 Vitest、lint、type-check、Playwright 回归全部通过。

### 边界情况
- 旧会话发送空、错误、过期或伪造 receipt：忽略，不影响安全判断。
- 模型省略 receipt：合法 argv 正常执行。
- 轻微固定参数差异：只由现有 normalization 接受明确安全的形态；未知 flag 继续拒绝。
- 服务暂时错误或限流：只有结构化 retryable=true 且操作未进入未知写入状态时允许现有预算内重试。
- 写操作已开始但结果未知：立即终止，不自动重试，前端显示终止失败。

### 权限规则
- 只允许当前有数 user_id 绑定的当前 generation 飞书账号。
- 服务端 catalog 决定 domain/risk/scopes；模型不能提交或覆盖身份、scope、risk、HOME、token。
- Docs/Base/Wiki/Drive 维持现有命令白名单；IM 与本地 CLI 管理命令继续禁止。

### UI 行为规格
- 页面位置：Agent 对话执行时间线。
- 布局要求：沿用现有扁平时间线，不新增卡片。
- 交互模式：无需用户操作；可恢复步骤原位从 spinner/处理中变为完成。
- 状态处理：recoverable=处理中（绿色 spinner、文案“正在调整执行方式”）；success=绿色完成；terminal error=红色错误；授权等待继续使用现有飞书授权卡片。
