# 飞书资源发现与标题读取 — 设计规格

## 1. 问题与根因

新对话没有历史 token 时，Agent 正确地尝试加载 `lark-drive` 并按标题搜索，但平台控制面只注册 Docs/Base/Wiki：技能读取先被拒绝，Drive 命令再被拒绝。Agent 随后尝试官方 Wiki 前置命令 `wiki +space-list`，仍因目录缺项被拒绝。整个失败链没有创建 `feishu_operation`，因此根因不是飞书服务、用户连接或授权，而是平台能力目录不完整及错误提示误导。

## 2. 不变量

1. 飞书连接归属于当前登录用户，不归属于 Agent。
2. 每次 `lark_execute` 的 user_id 只取服务端 request context；模型输入没有 user_id、agent_id、connection_id。
3. 技能 receipt 绑定 agent_run_id、固定 CLI 版本和技能名，不可跨 run 复用。
4. 所有命令经过固定目录规范化，不执行 shell，不接受任意 API。
5. Drive 本期只读；不得注册上传、复制、移动、删除、权限或评论写操作。
6. 授权继续 execute-first：先尝试业务命令，精确识别 missing scope 后才生成最小授权卡片。

## 3. 能力设计

### 3.1 SkillReader

新增 `SkillDomainDrive = "drive"`。

- allowlist 加入 `lark-drive`。
- Drive domain 的 exact receipts 为 `lark-shared` + `lark-drive`，不能多、不能少、不能重复。
- Agent 工具 schema enum 和 description 同步加入 lark-drive。

### 3.2 Drive 命令目录

只注册：

```text
drive +search
domain: drive
action: search
risk: read
scope: search:docs:read
replay_safe_on_auth_error: true
```

允许 flags：

| flag | 约束 |
|---|---|
| `--query` | 必填、非空、UTF-8、无控制字符、最多 30 Unicode code points |
| `--only-title` | boolean，只接受出现一次或 `=true` |
| `--doc-types` | 逗号分隔、去重，允许 `doc,docx,wiki,bitable`，最多 4 类 |
| `--page-size` | 1..20 |
| `--page-token` | 8..128 的既有 opaque token 规则 |

平台继续接受且移除官方示例里的固定 `--format json --as user`，最终只追加一次固定值。位置参数、空 query、未知 flag、`--page-all`、其他 Drive verb 全部拒绝。

### 3.3 Wiki 空间发现

注册 `wiki +space-list`：domain=`wiki`、action=`space-list`、risk=`read`、scope=`wiki:space:retrieve`。

允许 `--page-size` 1..50、`--page-token`、`--page-all`、`--page-limit` 1..10；`page-token` 与 `page-all` 互斥，`page-limit` 仅能和 `page-all` 同用。无限 `page-limit=0` 不进入托管目录。

### 3.4 授权和 capability

- `canonicalDeviceAuthScopes` 的固定目录集合加入 driveScopes。
- operation biz 与 store capability domain 固定集合加入 drive。
- workspace status 默认返回 docs/base/wiki/drive 四个 domain；旧记录没有 drive 时显示 unknown。
- 不新增表和 migration；现有 JSON capability 字段向后兼容。

### 3.5 Agent 托管策略

`lark_skill_read` 每页返回的 hosted policy 增加：

1. 只有资源标题、没有 URL/token 时，先读 lark-drive 并执行 `drive +search --query <title> --only-title`。
2. 剥离搜索结果高亮后做标题精确匹配。
3. 一个精确匹配：按返回类型/URL 路由到 Docs/Base/Wiki。
4. 多个精确匹配：列出标题、类型、URL，让用户选择；不擅自读取。
5. 零精确匹配：明确未找到并请求链接；不把目录拒绝或零结果说成连接未就绪。
6. 命令目录拒绝是不可重试的本轮输入/平台策略错误；最多修正一次，不执行 auth/config/whoami。

## 4. 错误语义

- `ErrOperationRequestRejected`：命令/flags/receipts 不在平台策略内；未访问飞书；不得表述为用户连接故障。
- missing app/user scope：由既有结构化 classifier 转为授权卡片。
- CLI/network unavailable：固定的临时不可用提示，不泄漏 stderr、路径、argv、receipt 或凭据。
- Drive 零结果是成功结果，由 Agent 按 hosted policy 解释。

## 5. 安全与隔离

- 不增加任何模型可控身份字段。
- 查询内容进入 argv 前做 UTF-8、控制字符、Unicode 长度和 indirection 检查。
- 搜索输出仍受固定 stdout ceiling 限制。
- capability JSON 只存固定 domain/state/time/version，不存标题、token、URL 或正文。
- 不修改 `config_prod.yaml`，不引入新密钥。

## 6. 测试策略

- 客户 RED：新对话所需的 lark-drive skill、Drive search、Wiki space-list 当前均失败。
- Catalog 单测：精确规范化、scope/risk/replay、所有边界拒绝。
- Skill receipt 单测：Drive exact set、跨 run、重复、多余 receipt 拒绝。
- Agent tool 单测：schema/description/hosted policy 与错误文案。
- Operation/store/service 单测：drive capability 可记录/读取、canonical scope 接受。
- 全量：`go test ./...`、`task lint`；必要的 Feishu package race gate。

## 7. 非目标

- Drive 写入、目录整理、上传下载、权限治理、评论。
- IM、联系人、Sheets 内容能力。
- 按 Agent 单独保存飞书连接。
- 新 API、新表、新前端页面。

