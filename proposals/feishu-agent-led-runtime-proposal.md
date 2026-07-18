# 飞书 Agent 主导运行时 — 提案

## §1 方案概述 [客户可见]
采用“Agent 主导、平台加护栏”的运行方式。Agent 继续读取官方飞书技能并自行选择业务命令；平台不再猜测每一种飞书错误，而是统一提供权限预检、结构化结果、授权卡、当前用户隔离和写入幂等。

读取命令直接执行。创建、更新等写命令先进行不会修改飞书内容的 scope check：权限齐全才执行业务写入；缺权限则先让用户授权，授权完成后恢复同一个加密操作并只执行一次。Agent 可检查当前连接/命令就绪状态，但不能指定用户、凭证、连接 ID 或执行 auth/config/shell。

## §2 方案选择 [客户可见]
- 方案 A：完整裸 `lark-cli` 交给 Agent。自主性最高，但不适合多租户 SaaS 的凭证和写入安全。
- 方案 B：Agent 主导、平台加护栏。保留 Agent 判断力，同时由平台掌握身份、授权和幂等。**已确认采用。**
- 方案 C：后端继续逐错误编排。短期代码少，但会继续逐个踩坑。已否决。

## §3 技术可行性 [AI 内部]
### 复用能力
- 复用固定 `lark-cli 1.0.68`、加密 CLI HOME、当前用户 generation fence。
- 复用 `CommandCatalog`、Skill receipt、Feishu operation、设备授权、外部动作卡和 Agent run resume。
- 复用现有 Docs/Base/Wiki/Drive 命令，不新增 HTTP API、表或 UI。

### 新增能力
- 固定版本 scope preflight：服务端用 catalog 派生的 scope 执行 `auth check --scope ... --json`。
- Agent 可见的安全失败结构：稳定 code、是否可重试、业务调用是否开始、缺失 scopes。
- 只读检查能力：Agent 可检查当前用户连接和某条 allowlisted 命令是否就绪；不回显凭证、App ID、用户标识或原始 CLI 文本。
- 通用恢复：所有写命令统一在业务调用前发现缺 scope；所有读命令可以从结构化 missing-scope 进入恢复。

### 真实契约证据
Dev 当前用户的固定版本 CLI 已验证：
- 已授权读取：`auth check` exit 0，stdout 为单个 JSON，`ok=true`、`granted=[docx:document:readonly]`、`missing=[]`。
- 缺少写入：exit 1，stdout 为单个 JSON，`ok=false`、`granted=[docx:document:readonly]`、`missing=[docx:document:write_only]`；stderr 为空。
- 该检查不调用 Docs 写接口，因此能够在不猜测副作用的前提下启动授权恢复。

### 风险与控制
- scope check 输出异常：在业务写入前失败，返回不可写的临时/协议错误，不猜测权限。
- 授权后仍缺同一 scopes：既有 recovery signature 防循环，终止而不创建第二张相同卡。
- scope check 与业务调用之间权限被撤销：业务写入仍使用现有 unknown-write fence；无确定结果时不重试。
- Agent 误用诊断：托管策略规定业务优先，诊断只在明确查询状态或结构化失败后使用。

## §4 PRD [AI 内部]
### 用户故事
- 作为飞书用户，我希望任意自建 Agent 能自行完成 Docs/Base/Wiki 任务，而不是遇到一种新错误就等待平台升级。
- 作为数据所有者，我希望授权和重试不会造成重复文档、重复追加、重复记录或覆盖。
- 作为多 Agent 用户，我希望连接属于我的账号，而不是某个 Agent。

### 验收标准
- [ ] write/high-risk 命令在业务 runner 前统一 scope preflight；scope 只取 catalog，模型不可覆盖。
- [ ] scope 缺失时 business invocation count=0，进入现有授权卡；授权后 business invocation count=1。
- [ ] preflight 协议错误、超时、输出歧义均在写入前 fail closed。
- [ ] 已开始写入后的 timeout、未知输出或网络错误继续为 unknown，永不自动重试。
- [ ] 读取命令保持 execute-first；结构化 missing_scope 可安全恢复。
- [ ] Agent 终端工具结果包含稳定、非敏感、可行动的失败结构。
- [ ] Agent 可读取当前用户连接/能力和 allowlisted 命令就绪状态，不可读取凭证或指定身份。
- [ ] Docs/Base/Wiki 现有 CRUD 命令共享机制；Drive 仅搜索；IM 拒绝。
- [ ] 同一用户多 Agent 共用连接；不同用户 operation、vault、capability 和 receipt 完全隔离。
- [ ] 授权循环、并发同幂等键、进程恢复、高风险确认顺序均有回归测试。
- [ ] 全量 Go、race、lint、双审通过，合并 develop 并部署 Dev。
