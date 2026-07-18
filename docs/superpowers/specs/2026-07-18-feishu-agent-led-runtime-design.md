# 飞书 Agent 主导运行时 — 设计规格

## 1. 设计目标

将现有“后端逐错误编排”改为“Agent 主导业务、平台提供安全运行时”。Agent 负责理解用户目标、读取官方技能、选择和修正 Docs/Base/Wiki/Drive 业务命令；平台负责当前用户身份、凭证、命令边界、权限恢复、确认和写入幂等。

该设计不是把裸 shell 或任意 `lark-cli` 暴露给模型。Codex-style 指的是保留 Agent 的观察—判断—行动闭环；SaaS 平台仍拥有不可绕过的护栏。

## 2. 不变量

1. 飞书连接属于当前登录 user，不属于 Agent；同一用户所有 Agent 复用同一 generation-fenced vault。
2. user_id、generation、risk、scopes、identity 和 idempotency 均由服务端派生，模型不可提供或覆盖。
3. 业务命令仍经过固定 Docs/Base/Wiki/Drive catalog；无 shell、IM、任意 OpenAPI、auth/config 写操作。
4. 写操作只有在 scope preflight 成功后才允许进入 business runner。
5. scope preflight 不是业务写入；它不能被计为业务 invocation，也不能制造写入成功证据。
6. 任何已经开始的写操作只在收到严格 `ok=true` 单 JSON envelope 时标记成功；timeout、进程异常、输出歧义继续为 unknown，禁止自动重试。
7. 授权完成只恢复原 operation 的加密 argv；模型不能在恢复边界替换命令。
8. 原始 stdout/stderr、message、hint、URL、App ID、token 和 receipt 不进入 Agent 结果、日志或 capability JSON。

## 3. 组件边界

### 3.1 CommandCatalog：策略与元数据，不负责编排

Catalog 继续规范化允许的业务 argv，并派生：

- command_path / domain / action
- risk / requires_cli_yes
- exact scopes
- exact skill receipt set

Catalog 不再承担“看到某个错误后该运行哪个授权命令”的职责。新增命令仍需显式安全审查，但新增命令不需要再复制一套权限恢复代码。

### 3.2 ScopePreflight：固定、只读、通用

新增窄接口：

```go
type ScopePreflight interface {
    Check(ctx context.Context, home string, scopes []string) (*ScopeCheckResult, error)
}

type ScopeCheckResult struct {
    Granted []string
    Missing []string
}
```

生产实现仅执行固定二进制和固定命令形状：

```text
/usr/local/bin/lark-cli auth check --scope "<sorted catalog scopes>" --json
```

安全契约：

- scopes 必须是 catalog 已登记命令的规范化 exact set，不能为空、不可重复、不可含 IM。
- stdout 必须是一个完整 JSON object；stderr 必须为空；输出继续受 controlled runner ceiling 和 timeout 约束。
- 仅接受字段 `ok`、`granted`、`missing` 和固定版本可选的 `suggestion`；suggestion 不解析、不持久化、不回显。
- `ok=true` 必须 exit 0、missing 为空、granted 与 requested 完全相等。
- `ok=false` 必须 exit 1、missing 非空、granted 与 missing 无交集且并集与 requested 完全相等。
- 未知字段、重复字段、非法 scope、错误 exit code、双流、trailing JSON 全部协议失败。

### 3.3 FeishuOperationService：安全执行顺序

已连接账号的执行顺序改为：

```text
catalog + receipt 校验
        ↓
创建/读取幂等 operation
        ↓
write/high-risk? ──否──→ business runner（读操作 execute-first）
        │
        是
        ↓
scope preflight
   ├─ missing → business_started=false → 授权卡 → 暂停 operation
   ├─ protocol/transport error → business_started=false → 安全失败
   └─ granted → 高风险确认（如需要）→ business runner 一次
```

授权完成后的 resume 使用同一个 operation：重新做 scope preflight；通过后才进入 business runner。若同一 recovery signature 仍缺权限，终止并返回稳定失败，不生成无限授权卡。

preflight 与实际写入之间如果发生权限撤销，business runner 的结构化 missing-scope 可以证明 API 拒绝，但本设计仍不依赖该猜测自动重放；started write 继续走 unknown fence。下一次由用户发起的新操作会重新 preflight。

### 3.4 读操作恢复

读取命令不做强制 preflight，保持用户确认的 execute-first 体验。读取没有写副作用，因此固定版本返回 `authorization/missing_scope` 时，即使 CLI 省略 `missing_scopes`，也可以使用 catalog exact scopes 生成授权卡。错误 message 永远不参与分类。

### 3.5 Agent 可见结构化结果

`OperationResult` 和 `lark_execute` 的 terminal output 增加非敏感失败对象：

```json
{
  "ok": false,
  "state": "failed",
  "operation_id": "opaque",
  "failure": {
    "code": "feishu_not_found",
    "category": "not_found",
    "retryable": false,
    "business_started": true,
    "required_scopes": []
  }
}
```

稳定 category：`policy_rejected`、`connection_required`、`scope_required`、`reauth_required`、`validation`、`not_found`、`resource_denied`、`rate_limited`、`temporary`、`unknown_result`、`failed`、`cancelled`。

只有 allowlisted scope 名称可以进入 `required_scopes`。unknown_result 必须 `retryable=false`。原始 CLI type/subtype/code/message 不直接暴露；后端把固定机器字段转换为稳定产品语义，Agent 因此能判断“修参数、请求用户提供资源、稍后重试或停止”，而不是盲试 auth/config/bot flags。

waiting 状态仍通过现有 ExternalAction 卡片 yield，不把 live URL 放入工具 JSON。授权完成后的合成 tool result 使用同一失败/成功 schema。

### 3.6 Agent 只读检查能力

新增只读平台工具 `lark_inspect`，只支持两种 mode：

- `connection`：返回当前用户连接状态和 docs/base/wiki/drive capability 状态。
- `command`：接收业务 argv + 当前 run receipts，经过同一 Catalog/Receipt 校验并运行 ScopePreflight，返回 command_path、risk、granted/missing 和 `ready`；不创建业务 operation、不执行业务命令、不生成授权卡。

身份只从 context 获取。command mode 仅接受 catalog 命令，不接受 auth/config/whoami；输出不含用户 ID、generation、App ID、URL、token 或 receipt。Hosted policy 要求业务优先：只有用户明确询问连接状态，或收到结构化失败后才使用 inspect，禁止每条飞书指令先检查一遍。

## 4. 错误与重试规则

| 情况 | Agent 可见结果 | 自动动作 |
|---|---|---|
| 未连接 | connection_required | 创建应用/授权卡，恢复原任务 |
| preflight 缺 scope | scope_required + required_scopes | 授权卡；业务写入未开始 |
| read missing scope | scope_required + required_scopes | 授权卡；恢复原读取 |
| 参数/资源不存在 | validation / not_found | Agent 最多修正一次或向用户询问 |
| rate limit / upstream | retryable=true（仅读或未开始写） | 受控重试规则 |
| started write timeout/歧义 | unknown_result | 停止；不得自动重试 |
| policy/receipt 拒绝 | policy_rejected | 未访问飞书；最多修正一次 |

Agent retry controller 按 category 而不是笼统 tool error 计数。授权等待和授权后系统 resume 不计为模型盲重试。

## 5. 多用户、多 Agent 与持久化

- `lark_inspect` 与 `lark_execute` 都从 request context 取 user_id。
- Vault materialization 继续按 user_id + generation + key_version AAD 隔离。
- Operation request/result 继续加密，idempotency key 继续绑定 agent_run_id + tool_call_id。
- 不新增 schema。preflight 是瞬时证据；只在既有 operation summary 中记录稳定 public code 和 allowlisted recovery scopes。
- capability 继续按用户账号记录；不写 Agent ID，因此同一用户的不同 Agent 自然复用，同一 Agent 的不同用户不会串联。

## 6. 测试矩阵

### 固定 CLI 合同
- 真实观察的 scope check granted/missing fixtures。
- exit/ok 不一致、缺字段、重复字段、未知字段、非法 scope、交集/非完整分区、trailing JSON、双流、timeout、超限全部拒绝。

### Operation 状态机
- Docs update 缺写 scope：preflight=1、business=0、waiting_user_auth。
- 授权后 granted：总 business=1、success、内容只修改一次。
- 授权后仍缺同一 scope：无第二张相同卡、business=0、terminal failed。
- preflight transport/protocol failure：business=0、非 unknown write。
- high-risk：preflight 后才请求 confirmation；confirmation 后 business=1。
- preflight 通过但 business timeout：unknown、不可重试。
- read missing scope：execute-first 后授权并恢复。

### Agent 工具
- `lark_execute` 输出每个稳定 category，永不泄漏 raw error。
- `lark_inspect` connection/command、身份缺失、跨 run receipt、非法 argv、IM/auth/config 全部边界。
- Hosted policy 明确业务优先、结构化纠错、unknown 停止。

### 业务域与隔离
- Docs create/fetch/update、Base create/read/update、Wiki create/read/update 共享 preflight，不存在按命令特判。
- Drive search 只读；Drive 写和 IM 拒绝。
- 同一用户两个 Agent 使用同一 vault；两个用户使用同一 Agent 分别落各自 operation/capability。
- 并发同 idempotency key 只有一次 preflight 和一次 business invocation。

### Gate
- 客户 RED 在修复前失败、修复后通过。
- `task lint`、`go test ./...`、Feishu/Agent/store race gate、双 reviewer PASS。
- Dev 使用真实账号验证：首次更新先出现一次写 scope 授权卡；授权后 str_replace 与 append 各执行一次；随后读取确认内容且第二次更新无需再授权。

## 7. 非目标

- 暴露裸 shell、任意 `lark-cli`、任意 auth/config 或 OpenAPI。
- 允许 Agent 自行批准权限或绕过用户卡片。
- Drive 写、IM、联系人、日历、邮件等新域。
- 新数据库表、HTTP API 或前端页面。
