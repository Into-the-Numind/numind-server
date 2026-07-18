# 飞书资源发现与标题读取 — 提案

## §1 方案概述 [客户可见]
在所有 Agent 共用的飞书工具层增加“飞书云空间只读搜索”。当用户只说文档标题时，Agent 先按标题搜索，找到唯一精确结果后再调用 Docs/Wiki/Base 能力；找不到时明确告知未找到，重名时让用户选择。连接与授权仍属于当前登录用户，不需要给每个 Agent 单独配置飞书。

同时补齐官方 Wiki 的空间列表入口，并把平台拒绝、参数错误、连接/权限恢复区分开，避免 Agent 把“平台没开放这个命令”误说成“用户的飞书没连接好”。

## §2 报价与周期 [客户可见]
- 预估工作量：1 个紧凑开发周期
- 报价：内部产品迭代，不单独报价
- 交付时间线：完成后直接部署 Dev 验收

## §3 技术可行性 [AI 内部]
### 现有功能复用
- 复用全局注入的 `lark_skill_read` / `lark_execute`，无需改 Agent 定义或工具绑定。
- 复用 `SkillReader` 的 run 绑定 receipt、`CommandCatalog` 的固定白名单和 `FeishuOperationService` 的用户隔离、增量授权、只读安全重放。
- 复用固定 lark-cli 1.0.68 的 `drive +search` 与 `wiki +space-list`。

### 技术风险
- 扩大命令白名单可能意外开放 Drive 写能力：只注册 `drive +search`，严格限制 flags、类型、30 字符 query 和 20 条分页。
- 新 scope 可能绕过授权目录：将 `search:docs:read` 纳入服务端 canonical scope 集合，由既有 execute-first 恢复链生成增量授权卡片。
- capability 状态丢弃 `drive`：同步扩展 biz/store 的固定 capability domain 集合，不改 schema。
- 模型对重名资源误读：托管策略明确唯一精确命中、重名、零命中的分支。

### 涉及仓库
- [x] numind-server
- [ ] numind-web-v3
- [ ] numind-admin-web

### AI 可观测性（如功能涉及 LLM 调用）
- 涉及 LLM 调用：否（扩展现有 Agent 工具执行边界，不新增模型调用）
- Trace 起点：N/A
- Generation 点：N/A
- 关键元数据：沿用 agent_run_id、tool_call_id、user_id、operation_id，日志不得包含凭据和原始错误内容。

## §4 产品需求定义 — PRD [AI 内部 — 不要为可读性简化]
### 用户故事
- 作为飞书已连接用户，我需要在任意自建 Agent 的新对话中仅输入文档标题就能读取内容，以便不依赖历史上下文里的 URL/token。
- 作为同一 Agent 的另一位用户，我需要它只访问我自己的飞书工作区，以便保持租户隔离。

### 验收标准
- [ ] `lark_skill_read` 可读取官方 `lark-drive` 主技能并签发仅绑定当前 run 的 receipt。
- [ ] `drive +search --query <title> --only-title` 被固定目录接受，规范化为 `--format json --as user`，domain=`drive`、risk=`read`、scope=`search:docs:read`。
- [ ] query 超过 30 Unicode code points、未知 flags、Drive 写命令、IM 命令全部 fail closed。
- [ ] `wiki +space-list` 被接受，scope=`wiki:space:retrieve`，分页上限受控。
- [ ] Drive 命令要求且只接受同一 run 的 lark-shared + lark-drive receipts。
- [ ] 新 scope 能进入既有 app-scope/user-scope 增量授权恢复流程，成功结果可记录 drive capability。
- [ ] 所有 Agent 继续从执行上下文获取当前 user_id；Agent 定义中不新增飞书凭据或连接 ID。
- [ ] 托管策略指导：仅标题时先 Drive 搜索；唯一精确匹配后路由；多匹配请用户选择；零匹配不盲猜。
- [ ] 客户失败复现测试先 RED，修复后全量 Go 测试与 lint 通过。
- [ ] develop 合并推送、Dev server 部署、健康检查和固定版本 CLI 检查通过。

### 边界情况
- 标题为空、超长、含控制字符；重复/未知 flag；恶意位置参数；过大分页；无效 page token。
- 同名多个结果、无精确结果、结果类型为 docx/wiki/bitable。
- 缺少 app scope、缺少 user scope、连接尚未创建、凭据撤销。
- 不同用户使用同一 Agent；同一用户切换不同 Agent。

### 权限规则
- 当前登录用户身份由服务端上下文注入；模型不能提供或覆盖 user_id。
- 只开放 `search:docs:read` 和已有 Wiki 只读 scope；无 Drive 写能力。
- 缺 scope 使用既有最小增量授权卡片，不做每次强制预检。

### UI 行为规格
- 页面位置：沿用现有 Agent 对话与飞书授权卡片。
- 布局要求：无新增 UI。
- 交互模式：首次缺 scope 时显示现有授权卡片；完成后恢复原操作。
- 状态处理：沿用现有 processing / waiting / terminal 状态。

