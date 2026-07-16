# 飞书分步用户授权与原任务恢复 — 提案

## §1 方案概述 [客户可见]

目前飞书个人应用已经可以创建，但有数在“用户授权”这一步使用了与新版 `lark-cli` 不匹配的旧流程，所以会等待约 30 秒后失败。修复后，流程会变为：

1. 有数立即生成飞书官方授权链接；
2. 用户在飞书完成授权；
3. 用户回到有数点击继续；
4. 有数完成授权，并自动继续用户原来要求的飞书操作。

用户不需要刷新页面、重发 Prompt、重复创建应用或手动复制密钥。授权成功后连接会持续保留，只有新增权限或凭据失效时才再次引导。

本次不新增独立飞书 Agent，也不重做前端页面。主要修复后端授权状态机，并继续复用现有 Agent、授权卡片和 `lark_execute`。

## §2 报价与周期 [客户可见]

- 预估工作量：1–2 个工作日，包括回归测试、数据库迁移、完整后端检查和 dev 真实飞书验收；不包含飞书侧人工审批等待时间
- 报价：内部产品开发，不单独报价
- 交付时间线：提案和技术设计确认后进入开发；通过自动验收后部署 dev，由客户完成真实账号验收，再单独确认生产发布

## §3 技术可行性 [AI 内部]

### 现有功能复用

- 复用 `FeishuAuthSession` 的用户、应用 generation、operation、phase、state、lease 和 expiry 绑定。
- 复用 `FeishuCLIVault` 的每用户隔离 HOME、恢复、密封和持久化能力。
- 复用 `FeishuOperation` 已有的加密请求、幂等键、exact-operation resume 和 unknown-result 防重复规则。
- 复用现有 external action 卡片、session ID、授权 URL、有效期和“我已完成，继续”接口；目标是不新增 API 端点和前端改动。
- 复用 operation cipher/key-version 模式，为 auth session 增加加密的短期恢复凭据。
- 复用 `ControlledLarkCLIRunner` 的受控进程边界，但为用户授权提供明确的 start/complete 两段接口，不再让 `RunBlocking` 同时承担两段生命周期。
- 复用 `lark_execute` 的服务器端操作入口；`lark_skill_read` 继续提供技能语法。

### 推荐技术路径

#### 第一段：启动授权

- 仅在 user-auth phase 执行固定命令形态：`lark-cli auth login --scope <scopes> --no-wait --json`。
- 严格解析固定版本返回的官方授权 URL、恢复凭据和有效期；拒绝缺字段、错误域名和超限输出。
- 将恢复凭据作为短期秘密加密保存，并绑定 `user_id + generation + operation_id + auth_session_id + scopes + expires_at`。
- API 只返回授权 URL、session ID、phase 和 expiry；不返回恢复凭据。
- CLI 启动进程返回后立即结束，不再依赖 30 秒 URL 等待或后台长进程。

#### 第二段：完成授权

- 用户点击继续后，使用已有 lease/CAS 机制原子领取 auth session。
- 解密精确绑定的恢复凭据，在当前用户恢复出的隔离 HOME 中执行：`lark-cli auth login --device-code <opaque-code> --json`。
- runner 的日志与错误输出必须隐藏 `--device-code` 的值。
- 成功后密封 HOME、将账号转为 `connected`、清空恢复凭据并完成 session。
- 通过现有 dispatcher 恢复同一个持久化 operation，且只允许执行一次。
- 重复确认返回当前确定状态，不重复调用飞书写操作。

#### 状态与并发约束

- 复用现有 auth session 状态：`pending / completed / expired / rejected / failed / superseded`，不为本次修复另建平行状态体系。
- `pending + 无 lease + 有恢复密文` 表示等待用户；`pending + 有效 lease` 表示某实例正在处理；前端分别映射为 waiting 和 processing。
- 新流程 session 写入 protocol v2；migration 将所有旧 protocol 的 pending user-auth session 直接 supersede，因此 v2 的“无密文 pending”只表示 start 尚未完成，可以安全重试。
- 领取继续使用 `lease_owner + lease_until` CAS 和续租；旧 owner 在 lease 失效后的晚提交必须被 fencing 条件拒绝。
- 同一用户、generation、operation 和 user-auth phase 最多一个可操作 pending session；刷新链接先 supersede 旧 session。
- 用户授权期间账号保持非 connected，业务命令不会同时打开同一个 HOME；Vault revision CAS 防止意外并发快照覆盖。
- auth complete 先生成 attempt-scoped 候选 HOME，只有仍持有同一 session lease、generation 和 expected Vault revision 的 owner 才能 fenced publish；失去 lease 的旧 owner 不能发布活动快照。

#### 崩溃恢复顺序

- 固定顺序：领取 session → 重校验用户/账号/generation/app/operation/scope → 完成 CLI 认证 → CAS 密封 HOME → 事务完成账号/session → durable dispatcher 恢复 operation。
- CLI 成功但 HOME 未密封时不能标记成功；lease 恢复先检查持久化 HOME，无法证明已授权则使用仍有效凭据继续，否则生成新链接。
- HOME 已密封但数据库事务失败时，通过服务器拥有的 `AuthStatus` 对账；只有持久化 HOME 证明 user auth 可用才补完事务。
- 数据库完成但 dispatcher 中断时，复用现有 operation 和 Agent continuation 的持久化 claim，再次 dispatch 不重复调用终态 operation。

#### Agent 工具边界

- 在 Agent 系统指引和工具描述中明确：飞书连接检查和业务命令不得使用 `bash_exec`。
- 在现有 bash validator 增加最小确定性规则：普通 Agent 沙箱拒绝执行 `lark-cli` 可执行文件；不扩大为通用 Shell 权限系统改造。
- Agent 只能调用 `lark_execute` 触发确定性连接编排；缺权限时由该工具返回授权 external action。
- `lark_skill_read` 读取到的本地 CLI 认证建议不能覆盖平台的服务器端认证边界。
- 服务器规则仅落在现有 `biz/agent/bashvalidator`：拒绝普通 Agent 执行名为 `lark-cli` 的命令或绝对路径，并提示改用 `lark_execute`；其他 Shell 安全治理不在本需求内。

### 数据库影响

- 预计需要一次 migration，为 `feishu_auth_session` 增加 protocol version、加密恢复凭据、密钥版本和凭据有效期字段；精确字段名、NULL/清除规则和索引在 S2 锁定。
- 不新增长期明文 Token 表，不改变“加密 CLI HOME 是用户授权材料载体”的既有模型。
- 旧版本留下的 pending user-auth session 没有恢复密文，首次读取/确认时必须 supersede 并为同一 operation 生成新链接；不能继续展示成可恢复。
- create-app 和 app-scope session 不使用新增密文字段。回滚旧应用版本前 supersede 未完成的新 user-auth session；nullable 列保留，不做破坏性回滚。

### 技术风险

- **恢复凭据泄露**：使用现有 envelope encryption；AAD 绑定 user/generation/app/operation-or-manual/session/scope-hash/key-version；禁止进入 API、前端、LLM、日志和公开错误；runner 对敏感 argv 做结构化脱敏。密文在任一终态清空，并由有界清理处理无人访问的过期 session。
- **操作系统进程表暴露**：固定 CLI 只支持 argv 传入 `device_code`。auth runner 必须运行在终端用户和 Agent 沙箱不可读取的后端进程命名空间；S2 验证现有部署边界，若不满足则生产前隔离到独立 UID/process namespace。
- **重复点击或多实例并发**：复用 session lease/CAS，只有一个请求可以领取并完成；其他请求返回处理中或最终状态。
- **过期或拒绝授权**：将 session 标记为 expired/failed，清除密文并生成新 session；绝不复用旧凭据。
- **CLI JSON 字段变化**：固定 `lark-cli 1.0.68`，建立真实等价 fixture 和严格 parser；未知结构明确失败，不回退到脆弱文本抓取。
- **授权成功但 operation 重复执行**：这里只承诺等待态 operation 的单次恢复，不承诺飞书远端绝对 exactly-once。无远端幂等键的写操作采用 at-most-once；只有明确未产生副作用的等待态可以恢复，unknown-result 不自动重试。
- **普通 Agent 沙箱误操作**：增加平台优先级更高的工具边界说明和回归测试，确保飞书认证不会走 `bash_exec`。
- **现有前端契约不够表达状态**：现有卡片已经具备 pending、提交中、完成、过期、刷新链接和重复点击保护；S2 仍需逐字段核对确认接口。内部“其他实例已领取”统一映射为 processing，不暴露竞争细节。如果 contract 仍有缺口，必须回到 S1 重新确认是否纳入 `numind-web-v3`，不能在编码时静默扩展范围。
- **误把 requested scope 当成已授权事实**：不持久化一个自称准确的 granted-scope 集合。后续命令仍直接执行；成功是可用证据，受控 missing-scope/revoked 才触发增量授权，保持既定“执行优先”原则。

### 涉及仓库

- [x] numind-server
- [ ] numind-web-v3
- [ ] numind-admin-web

### 工作量估算

- 预计修改 9–14 个后端代码、migration 和测试文件。
- 主要工作：失败复现测试、auth session 加密字段、两段式 CLI runner、并发/过期状态机、Agent 工具边界、真实等价 fixture、完整集成测试和 dev 真实验收。
- 复杂度：中高；风险集中在 OAuth 短期凭据和写操作恢复边界，而不是飞书文档 API 本身。

### AI 可观测性（如功能涉及 LLM 调用）

- [x] 涉及 LLM 调用：否（不新增或改变 LLM 调用；只收紧既有 Agent 工具说明和服务器端飞书授权编排）
- Trace 起点：N/A
- Generation 点：N/A
- 关键元数据：现有 Agent run/operation ID 日志继续保留，但不得记录恢复凭据

### 运行可观测性

- 记录脱敏的 session 状态转换审计：generation、operation/session correlation ID、phase、from/to state、attempt/lease ID、CLI error class、duration 和 recovery path。
- 指标覆盖 start/complete 成功率、lease expiry/reclaim、CLI contract failure、Vault reconcile、dispatcher retry 和 session expiry。
- URL query、恢复凭据、HOME 内容和完整 CLI argv 禁止进入日志或指标标签。

## §4 产品需求定义 — PRD [AI 内部 — 不要为可读性简化]

### 用户故事

- 作为首次使用飞书能力的有数用户，我需要快速看到飞书官方授权入口，以便不中断当前任务。
- 作为完成飞书授权的用户，我需要系统自动继续原来的 Docs/Base/Wiki 操作，以便不刷新页面、不重发 Prompt。
- 作为已经授权的用户，我需要后续相同权限的操作直接执行，以便持续使用自己的飞书工作空间。
- 作为多租户产品用户，我需要我的授权凭据只被自己的账号和当前应用使用，以便其他用户、会话和 Agent 沙箱无法访问。
- 作为执行飞书写操作的用户，我需要重复点击和服务重试不会产生重复文档或重复更新。

### 验收标准

- [ ] Bug-from-customer 的第一个代码 commit 是失败的复现测试，证明旧阻塞流程在 30 秒窗口内拿不到 URL，并使 operation 失败。
- [ ] user-auth 第一段使用固定版本支持的 `--no-wait --json` 协议，并在短进程结束后返回授权卡片，不依赖后台存活进程。
- [ ] 恢复凭据加密保存，并精确绑定用户、generation、operation、auth session、scope 和有效期。
- [ ] API、前端 payload、Agent/LLM 上下文、日志、错误消息和普通沙箱均不出现恢复凭据。
- [ ] Agent 沙箱和终端用户无法读取 auth runner 的进程表；若现有部署不满足，auth runner 使用独立 UID/process namespace。
- [ ] 用户点击继续后，系统用精确 session 完成第二段认证、密封用户 HOME、将账号标记为 connected 并清除恢复凭据。
- [ ] 原 operation 自动恢复且只执行一次；重复点击、并发确认和重放请求不产生第二次飞书写操作。
- [ ] “只执行一次”定义为同一 waiting operation 只进入一个业务 CLI 尝试；CLI 调用后结果不确定时进入 `unknown`，不自动重试。
- [ ] 过期、拒绝、CLI 输出异常和凭据解密失败都返回明确可恢复状态，不显示泛化 Internal server error。
- [ ] 后端在第一段和第二段之间重启后仍能继续；另一服务实例也能领取有效 session。
- [ ] 覆盖 CLI 成功但 HOME 未密封、HOME 已密封但事务未完成、事务完成但 dispatcher 未完成三处崩溃边界。
- [ ] 失去 lease 的旧 owner 即使完成 CLI，也不能发布候选 Vault revision 或提交 session/account 状态。
- [ ] Agent 不再通过 `bash_exec` 执行 `lark-cli auth status` 或飞书业务命令；连接和执行统一走 `lark_execute`。
- [ ] 真实等价 CLI fixture 覆盖 start/complete、过期、拒绝、重复确认、并发确认和敏感 argv 脱敏。
- [ ] 授权状态机的集成 fixture 至少覆盖 Docs 创建、Base 读取和 Wiki 更新，证明编排与业务命令域无关。
- [ ] dev 真实验收：第一次创建飞书文档时完成官方授权并自动得到文档结果；第二次直接创建成功且不再次要求相同权限。
- [ ] dev 真实验收期间不出现空白等待、必须刷新页面、Internal server error 或重复文档。
- [ ] 脱敏审计与指标可以区分 start、complete、lease reclaim、Vault reconcile 和 dispatcher recovery，但不包含 URL query 或秘密。

### 边界情况

- 用户在飞书页面未完成、拒绝或关闭授权。
- 授权链接和恢复凭据过期后再点击旧卡片。
- 同一用户连续点击继续，或浏览器和 Agent run 并发确认同一 session。
- 服务在返回授权链接后重启，或确认请求由另一个后端实例处理。
- 应用 generation 已更新、账号已解绑或 operation 已取消后，再使用旧 session。
- CLI 返回非 JSON、缺失字段、错误域名、超大输出、未知错误类型或非零退出码。
- 第二段认证成功但密封 HOME/数据库更新失败。
- 授权成功后 operation 已经完成、失败、unknown 或被其他 worker 领取。
- 新增 scope 与已有 scope 合并，不能丢失旧授权，也不能申请超出当前操作所需的权限。
- 部署前遗留的 pending user-auth session 没有恢复密文。

### 权限规则

- 沿用一期准入规则：有权创建个人自建应用并能完成租户审批的有数用户可使用。
- 每个有数 `user_id` 对应独立飞书账号状态、应用 generation、加密 HOME 和 auth session。
- auth session 只能由同一登录用户确认；用户、generation、operation 或 session 任一不匹配即拒绝。
- 普通 Agent 只能请求 `lark_execute`，不能读取密文、恢复凭据或用户 HOME。
- 管理端不新增查看、导出或代用户完成授权的能力。

### UI 行为规格

- 页面位置：现有 Agent 对话中的飞书 external action 授权卡片。
- 布局要求：复用现有卡片，不新增设置页；展示步骤标题、官方链接/二维码、有效期、继续按钮和明确错误。
- 交互模式：系统生成链接后立即展示；用户完成飞书页面操作后点击“我已完成，继续”；重复点击时按钮进入处理中且不重复执行。
- loading：复用现有提交中样式，表达“正在确认飞书授权并继续原任务”，保持当前消息可见。
- empty：不适用；没有有效 session 时生成新的授权卡片。
- error：区分未完成、已拒绝、已过期、连接已变更、结果未知和系统故障；过期提供“重新生成链接”，系统故障保留可追踪错误码但不泄露凭据。
- success：授权卡片转为完成状态，原 Agent run 继续输出最终飞书操作结果。

## §5 明确不包含

- 不新增独立飞书 Agent。
- 不实现 IM。
- 不把业务命令从 `lark-cli` 全量迁移到 Go OpenAPI。
- 不重新设计个人自建应用创建和应用权限审批阶段。
- 不新增管理端飞书授权页面。
- 不允许通过延长 30 秒超时或文本抓取作为正式兼容方案。
