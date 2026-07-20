# 飞书绑定连续性与精确会话收敛 — 技术设计

## 1. 背景与根因

Dev 用户 438 的 run 261 证明同一 operation 已从 create-app session `54af8273-ddb8-45a8-9d6d-97885121996a` 进入 user-auth session `f175bc96-446e-4091-be39-d54f549f30a5`，但 Agent Run 的 `pending_external_action_json` 仍保留 create-app session。旧卡片第一次点击发生在新 user-auth 启动约 2.6 秒后，服务端却把这次旧卡片确认用于新 session，并为其运行 30 秒完成检查。

根因有两个，必须同时修复：

1. `WorkspaceResumeDispatcher.dispatch` 对所有非终态 operation 直接返回，丢弃 `OperationResult.Action`，因此 Agent Run 的持久化动作不随授权阶段推进。
2. `POST /v1/feishu/operations/:id/resume` 只接收泛化 `action`，没有接收用户所见卡片的 `session_id`，因此服务端无法区分当前确认与旧卡片确认。

本设计覆盖首次建应用、应用权限审批、用户授权、增量授权和链接刷新。它不针对 user 438 写特殊逻辑。

## 2. 设计目标

- 每次 operation 产生新的非终态 action，都持久化替换同一 Agent Run 的当前 external action。
- 每次浏览器确认都绑定卡片显示时的精确 session；旧 session 不得推进当前 session。
- 旧卡片、重复点击、多标签页、页面刷新、服务重启和响应丢失自动收敛到最新安全 action。
- 对话中同一 operation 只有一个当前可操作状态；旧异步响应不能覆盖新状态。
- 授权完成后只恢复原 operation 一次；unknown/cancelled/failed/已完成状态维持既有外部写安全语义。
- 不新增表、migration、endpoint、OAuth 实现、连接中心或 LLM 调用。

## 3. 不变量

### 3.1 服务端动作不变量

1. 当前 operation summary 的 session 是授权阶段的事实来源。
2. Agent Run pending action 是当前 UI/重启快照的事实投影；它只能向 operation 当前 action 前进，不能倒退。
3. 新 action 的持久化投影严格只含 provider、operation_id、session_id、tool_call_id、phase、expires_at；URL、scope、device code、App Secret、CLI HOME 和 argv 不进入 Agent Run。
4. operation terminal 后不再创建或替换 pending action。

### 3.2 浏览器确认不变量

1. `user_completed`、`confirmed`、`cancelled` 均携带当前卡片 session ID。
2. 服务端只信任 route operation ID、登录 user ID 和数据库当前 generation/session；客户端 phase/status 不是授权输入。
3. session 不匹配或缺失时，服务端不调用 `CompleteAppApproval`、`CompleteUserAuthorization`、operation dispatcher、Confirm、Cancel 或任何 CLI；只返回当前 action 投影。
4. 当前 session 的重复请求由现有 auth-session lease/CAS 幂等处理。

### 3.3 前端收敛不变量

1. resume 请求以 `(operation_id, session_id)` 去重，不以 operation 单独去重。
2. 响应只能更新发起请求时仍然可见的同一 `(operation_id, session_id, run_id, session epoch)`。
3. 服务端返回 replacement action 时，原消息原位替换 session/phase/expiry/URL；旧 URL 和 notice 先清除。
4. replacement action 无 URL 时沿用现有一次性 refresh 恢复，不持久化 URL。

## 4. 后端设计

### 4.1 Operation action 交接证据

在 `persistedOperationSummary` 增加内部字段：

```go
SupersededSessionIDs []string `json:"superseded_session_ids,omitempty"`
```

每次 operation 或 auth refresh 把 session 从 old 切到 new 时：

1. 将 old session 与已有 lineage 合并、去重。
2. lineage 最多保留 32 个稳定 session ID；超过上限时保留最近 32 个。
3. 将 `SessionID` 更新为 new。

`OperationResult` 增加不序列化字段：

```go
SupersededSessionIDs []string `json:"-"`
```

`startRecoveryAndWait`、`resultFromOperation` 与 refresh/recovery summary builder 都携带该 lineage。它让“operation 已提交、Agent action 尚未交接、进程随后重启”仍可重放交接，也防止延迟的旧 dispatcher 把 action 倒退：旧结果的 lineage 不包含更新的当前 session，CAS 会拒绝覆盖。

lineage 不是凭据，只包含 opaque session ID；它不进入公共 API、前端或 LLM。

### 4.2 Agent Run 原子交接

新增窄 store capability：

```go
type IExternalActionTransitioner interface {
    TransitionPendingExternalAction(
        ctx context.Context,
        userID uint,
        runID uint64,
        operationID string,
        toolCallID string,
        supersededSessionIDs []string,
        nextPayloadJSON []byte,
    ) (transitioned bool, err error)
}
```

事务行为：

1. 对 next payload 运行现有 `externalaction.CanonicalJSON` 与严格字段校验。
2. 锁定 `agent_runs.id=runID`，并校验 user、未取消、未删除、operation/tool identity。
3. 若当前 pending session 已是 next session，返回幂等成功。
4. 否则要求当前 session 位于 `supersededSessionIDs`，run 仍处于外部等待状态，再原子更新 pending JSON 与 timestamp。
5. 当前 session 不在 lineage、身份不一致或 run 已结束时，返回 `transitioned=false`，不改数据。

`AgentRunResumer` 增加 `HandoffExternalToolWait` 窄方法，内部 type-assert `IExternalActionTransitioner`。`WorkspaceResumeDispatcher` 对 waiting_connection、waiting_app_scope、waiting_user_auth 的 `result.Action` 调用此方法；Action 缺 session/expiry、身份不完整或 handoff 失败均作为可重试错误，不再静默返回 nil。

confirmation 仅保留滚动升级兼容，不创建新的业务确认流程。

### 4.3 Refresh 持久化交接

`WorkspaceLifecycleService.RefreshAction` 在 operation-bound refresh 成功后，使用 old session 作为 lineage 源，并通过同一 `HandoffExternalToolWait` 更新 Agent pending action。若 auth replacement 已提交而 handoff 暂时失败：

- API 返回 dependency/unavailable，不声称流程已完成；
- operation summary 已保留 lineage；重复 refresh/recovery 可幂等修复；
- 不回滚或复用已经 superseded 的一次性 session。

手动连接且 `session.OperationID == nil` 的路径没有 Agent Run，不调用 handoff。

### 4.4 精确 session resume

`WorkspaceLifecycleService.Resume` 改为：

```go
Resume(ctx, userID, operationID, sessionID, action)
```

在任何非终态 mutation 前：

1. 读取当前 account generation 与 operation。
2. 从 operation summary/recovery session 得到当前 session。
3. 比较请求 session 与当前 session。

不匹配或请求 session 为空时返回：

```json
{
  "operation_id": "<same operation>",
  "state": "waiting_user_auth",
  "notice_code": "authorization_updated",
  "action": {
    "operation_id": "<same operation>",
    "session_id": "<current session>",
    "phase": "user_auth",
    "expires_at": "<server expiry>"
  }
}
```

该 action 可不含 URL。前端随后使用现有 refresh endpoint 获取新的官方链接。session mismatch 分支严格零副作用。

terminal operation 可直接返回 terminal summary，因为它不会再触发授权或业务写。cross-user、旧 generation 或不存在的 operation 继续返回既有 404/安全错误，不泄露当前 session。

### 4.5 崩溃与并发顺序

```text
auth phase completed
  -> operation Resume commits next waiting state + next session + lineage
  -> dispatcher transitions Agent Run pending action
  -> frontend poll/snapshot observes next action
```

- 崩溃发生在 operation commit 前：旧 operation/session 仍是事实来源，重试旧确认安全。
- 崩溃发生在 operation commit 后、Agent handoff 前：operation summary lineage 允许后续 dispatcher 重放 handoff。
- 两个实例同时 handoff：一个 CAS 获胜；另一个看到 next session 后幂等成功。
- 延迟旧 handoff：当前 session 不在旧结果 lineage，无法倒退。
- 旧浏览器确认：session mismatch，零副作用返回最新 action。

## 5. HTTP API 契约（锁定）

### 5.1 请求

现有 endpoint 不变：

```http
POST /v1/feishu/operations/:operation_id/resume
Content-Type: application/json

{
  "action": "user_completed",
  "session_id": "54af8273-ddb8-45a8-9d6d-97885121996a"
}
```

- `action` 仍是 `user_completed | confirmed | cancelled` 固定枚举。
- `session_id` 是 opaque 非空字符串，不接受 phase、URL、scope、device code 或 user ID。
- 为 backend-first 滚动部署，服务端解析阶段允许旧客户端省略 `session_id`，但缺失请求永不推进状态，只返回 `authorization_updated + current action`。新前端发布后始终发送 session ID。

### 5.2 响应

响应字段集合保持：`operation_id,state,data?,action?,notice_code?`。不增加新的公共字段或 notice 枚举。

- exact current + still pending：`authorization_pending` 或 `authorization_processing`。
- exact current + next phase：返回 replacement `action`。
- stale/missing：`authorization_updated` + current action，零副作用。
- terminal：返回 terminal state，不带 action。
- replacement action 无 URL合法；前端调用现有 `/v1/feishu/actions/:session_id/refresh` 恢复链接。

### 5.3 部署兼容顺序

1. 后端先部署：旧前端缺 session 时安全停留并收到 current action，不会误推进。
2. 前端随后部署：所有确认携带 session，恢复完整能力。
3. 不允许前端先部署到严格旧后端，因为旧 strict JSON decoder 会拒绝 `session_id`。

## 6. 前端交互设计

### 6.1 API 与 store

```ts
resumeFeishuOperation(operationId, sessionId, action = 'user_completed')
```

请求体为 `{ action, session_id: sessionId }`。

Pinia store 的方法同样接收 session ID：

```ts
store.resumeFeishuOperation(operationId, sessionId, action?)
```

- 查找 exact operation+session pending message。
- in-flight key 为 `${operationId}:${sessionId}`。
- response 只在原 session 仍当前时生效。
- replacement action 原位更新同一消息；旧 session 的迟到 response 被忽略。

### 6.2 卡片行为

- `AgentMessageItem` 从当前 card 捕获 session ID，并传给 store；不在 await 后重新读取旧引用。
- 点击后立即进入 busy，按钮 disabled/loading，状态区显示“正在检测飞书授权，请稍候…”。
- `authorization_updated` 文案改为“授权已进入下一步，正在为你切换到最新步骤。”
- replacement action 到达后 phase/title/link 原位切换；不生成第二个用户消息。
- snapshot 或 live SSE 已替换 session 时，旧请求错误和 finally 不得清除新卡片 busy/error 状态。
- 缺 URL 的最新 action 继续沿用现有一次 auto-refresh；失败后保留手动“重新生成链接”。

不增加新页面、弹窗、步骤条或额外确认。

## 7. 错误与安全边界

- stale/missing session 是可恢复状态，不记录为授权失败；结构化观测 outcome 为 `stale_action_reconciled`。
- handoff persistence failure 记录 `action_handoff_retry` 并返回可重试错误；不能假装成功。
- 日志只记录 user_id、run_id、operation_id、session correlation、phase、from/to state 与分类；不记录 URL query、scope 详情、argv、HOME 或凭据。
- session ID 只作为 opaque 关联 ID，所有 ownership/generation 校验仍在服务端。
- existing unknown-result、operation lease、execution gate、Vault revision 与 Task11 resume lease 均不改变。

## 8. 测试设计

### 8.1 第一条客户 RED

后端第一条 feature commit 必须在 `feishu_resume_dispatcher_test.go` 复现：operation 从 create_app 返回新的 waiting_user_auth action，而 Agent Run 仍是 create_app；当前 dispatcher 返回 nil 且不更新 Agent pending action，测试失败。

前端第一条 feature commit 必须证明卡片 session 没有进入 store/API 请求：现有组件只传 operation ID，断言 exact session 参数时失败。

两条 RED commit 均早于生产代码修改并永久保留。

### 8.2 后端矩阵

- create_app → app_scope、create_app → user_auth、app_scope → user_auth handoff。
- operation commit 后 response loss/restart，lineage 重放修复 Agent pending action。
- 当前已是 next session 的幂等 handoff。
- 延迟旧 handoff 不覆盖更新 session。
- stale/missing/cross-user/wrong-generation session 零副作用。
- exact session 才调用 CompleteAppApproval/CompleteUserAuthorization。
- duplicate/concurrent exact resume 单 owner、processing/pending 映射不变。
- refresh replacement 同步 Agent pending action；连续 refresh lineage 可收敛。
- cancelled/deleted/terminal/unknown run 不重新挂 action或执行外部写。
- strict persistence/API/log regression 无 URL、device code、secret、HOME、argv。

### 8.3 前端矩阵

- API request body 含 exact session，runtime response allowlist 不放宽。
- component 对 user_completed/confirmed 都传当前 session。
- store exact `(operation,session)` 去重；新 session 不被旧 in-flight promise 阻塞。
- stale response/new SSE/session navigation race 不修改新卡片。
- `authorization_updated` 原位换 phase/session，URL-less action auto-refresh。
- busy 状态、notice 文案、expired/denied/processing/terminal 状态可访问性。
- Playwright 复现真实顺序：create_app 卡片 → 服务端 user_auth snapshot → 点击旧 DOM/旧请求 → 只显示 user_auth 最新卡片，不出现误导 pending，最终继续原任务一次。

## 9. PRD 对照

- 首次用户看到下一步：由 dispatcher durable handoff + snapshot poll 保证。
- 点击当前步骤即时反馈：由 card busy/status 保证。
- 刷新/多标签页自动收敛：由 exact session + stale current action response 保证。
- 授权后自动继续一次：复用 Task11 durable resume lease；本设计不改变外部写重试语义。
- 过期/拒绝/刷新：复用现有 replacement action，补齐 Agent pending handoff。
- 隐私与权限：严格 JSON allowlist、user/generation/operation/session fence 不变并收紧。

## 10. 非目标

- 不直接调用飞书 OAuth/OpenAPI 替换 lark-cli。
- 不把一次性 URL 持久化到数据库或 Agent Run。
- 不新增连接管理页面、管理员页面或产品教程。
- 不自动重试结果未知的飞书写操作。
- 不修改生产配置或部署生产环境。

