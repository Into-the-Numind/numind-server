# 飞书连续授权卡片与过期刷新 — 技术设计

日期：2026-07-20  
状态：APPROVED（用户已授权快速 Standard 直至 Dev）

## 1. 问题与非目标

### 1.1 已证实的问题

1. Agent run 在第一次飞书授权后恢复，并再次进入 `waiting_for_user_choice`。后端 session snapshot 已包含第二个 `external_action`，但前端 `refreshRunStatus()` 只补回 `question_prompt`，导致第二张飞书卡片直到整页刷新才出现。
2. protocol-v2 `user_auth` session 的服务器过期时间已到，但数据库 state 仍可合法保持 `pending`。当前 refresh 分类器只接受未过期 pending 或已落库 expired/rejected，因而把 `pending + ExpiresAt <= now` 拒绝为 unavailable，并最终返回 HTTP 500。

### 1.2 非目标

- 不新增飞书权限、Base 命令或 Agent 判断规则。
- 不新增 API endpoint，不改变前端 refresh 请求为空 body 的契约。
- 不持久化、回放或由前端拼接一次性飞书 URL。
- 不自动确认授权，不调用普通 Agent `/answer`。
- 不重试或重放 Base/Docs/Wiki 业务操作。
- 不修改数据库 schema，不部署 Prod。

## 2. 核心不变量

### 2.1 身份与租户边界

刷新必须同时匹配：当前登录 user、当前飞书 account generation、旧 session、旧 session 的 operation、`user_auth` phase、operation 的 `waiting_user_auth` state、operation summary 的旧 session binding、canonical scopes 与 scope hash。

### 2.2 凭据边界

- 快照只恢复卡片身份字段，不恢复 URL。
- 旧 session 可能是完整加密 resume credential，也可能已经被 sweep 清空；二者均可在服务器确认过期且无活动 lease 时替换。
- 部分 credential 形态、错误 scope hash、活动 lease 或不完整 lease 形态全部拒绝。
- 旧 ciphertext、key version、resume expiry 与 lease 必须在 replacement 同一事务清除。

### 2.3 执行边界

refresh 只更换授权 session。它不得调用原 Base 命令、不得创建新的业务 operation、不得改变 idempotency key，也不得把未知写结果改成可重试。

## 3. 前端设计

### 3.1 等待交互快照协调

`refreshRunStatus()` 在 run 为 `waiting_for_user_choice` 且当前没有 pending interaction 时读取 session snapshot。协调顺序：

1. await 前记录 route/session epoch、run ID 和 conversation session ID。
2. await 后再次校验 epoch、current run、run 仍等待、snapshot session 与当前 conversation 一致，且此时没有更晚的 pending interaction。
3. 优先寻找当前 run 的 `external_action`；若不存在再寻找 `question_prompt`。
4. `external_action` 必须经 `externalActionMessage(..., allowLiveURL=false)` 白名单重建。
5. 以 `(run_id, operation_id)` upsert：
   - 新 operation：生成新的本地 UUID 并追加，保留旧 operation 的完成卡。
   - 同 operation + 同 session：不重复插入，不能用无 URL 快照覆盖已有 live URL。
   - 同 operation + 新 session：撤销旧 URL，更新为新 durable card。
6. 注入 pending action 后启动现有五秒外部动作轮询。

### 3.2 用户可见行为

- 第二张授权卡片最迟在下一次五秒轮询完成后出现，无需刷新页面。
- 第一张已完成卡片继续显示为完成态。
- 快照卡没有 URL 时显示现有“重新生成链接”入口；用户点击后调用现有 refresh API，成功后原位更新为官方 URL。
- 本次不做自动 refresh，避免重复轮询或组件重挂载不断 supersede server session。

## 4. 后端设计

### 4.1 Refresh source 分类

在 protocol-v2 `user_auth` 中增加一种合法 source：

```text
state == pending
AND ExpiresAt <= serverNow
AND lease is absent or its complete owner/until pair has expired
AND exact operation/session/scope binding
AND credential shape is either complete or credential-free
```

它的 replacement terminal state 为 `expired`，notice 为 `authorization_expired`，不需要先 claim worker lease。现有“尚未过期 pending → claim 精确 lease → superseded replacement”路径必须保持不变，因为刚从快照恢复的卡片没有 URL，但 session 通常仍未过期；用户此时点击刷新必须返回 200 新 URL。

以下仍拒绝：

- pending 且 lease 仍存活；
- lease owner / lease until 只有一半；
- credential ciphertext/key/expiry 只有一部分；
- protocol、phase、operation、summary、user、generation、scope 或 hash 不匹配。

### 4.2 原子 replacement

扩展现有 `ReplaceDeviceAuthSession` 的 pending 分支，允许两种且仅两种 CAS 来源：

1. 现有的 live exact lease owner；
2. 新增的服务器已过期 pending v2，且 lease 为以下安全形态之一：
   - `lease_owner='' AND lease_until IS NULL`；
   - owner 与 until 成对完整，且 `lease_until <= serverNow`。

事务内顺序保持：

1. 锁当前 user/generation 的飞书 account；
2. 锁旧 session 并复核 exact source；
3. 锁 waiting operation 并复核 JSON summary 等价且指向旧 session；
4. CAS 将旧 session 标为 `expired`，写 completed_at，并清除所有 resume credential/lease；
5. 创建 credential-free protocol-v2 replacement；
6. account 进入 `waiting_user_auth`；
7. operation summary 原子改绑新 session；
8. commit 后启动新 device authorization，返回新 action。

任何一步冲突均 rollback。同一个 source session 只能被一个事务替换；并发 loser 必须冲突或转入现有 stale-card 恢复语义，不能让同一 source 提交两次。生命周期层允许后来拿着旧卡的请求按现有规则刷新“当前绑定的 replacement”，因此本次不承诺多标签页在整个 operation 生命周期中永远只有一个 replacement，而是承诺每个 source 至多替换一次、最终 operation 只绑定一个权威 session。

### 4.3 相邻继续路径

对“链接已过期后点击我已完成，继续”的 operation-bound 路径写死行为：`ResumeActionUserCompleted` 在调用 CLI completion 前识别服务器已过期的 pending user-auth session，委托同一个 `RefreshOperationAction` 原子 replacement，并返回 HTTP 200、`state=waiting_user_auth`、`notice_code=authorization_expired` 与新 action。它不得伪造授权完成，也不得返回内部 500。

## 5. API 契约

API 不变：

```http
POST /v1/feishu/actions/{session_id}/refresh
Authorization: Bearer <user token>
Content-Length: 0
```

成功 replacement：

```json
{
  "code": 0,
  "message": "success",
  "data": {
    "action": {
      "operation_id": "opaque-operation-id",
      "session_id": "new-opaque-session-id",
      "phase": "user_auth",
      "expires_at": "RFC3339",
      "url": "https://open.feishu.cn/..."
    }
  }
}
```

业务 operation 已终态时保持现有 terminal 分支。请求不接受 body，响应不含 scopes、device code、密文、App Secret 或内部错误文本。

## 6. 测试设计

### 6.1 客户 RED（必须先提交）

- 前端：op1 completed 后，run 再次 waiting，snapshot 只有 op2 `external_action`；修复前 `refreshRunStatus()` 后无第二卡。
- 后端：expired-but-pending v2、无 lease、exact operation binding；修复前 refresh 返回 unavailable/500，预期新 action。

### 6.2 前端回归

- op1 完成 + op2 pending 同时存在；重复 poll 不重复。
- 同 operation live URL 不被无 URL snapshot 降级。
- 同 run 的合成 message id 不造成 Vue key 冲突。
- route/session 切换后的迟到 snapshot 与 refresh response 不污染新会话。
- Playwright 证明无需 reload 出现第二卡；点击 refresh 发送空 body、不调用 `/answer`，返回后显示 URL。
- 进入第二次 `waiting_for_user_choice` 后不保留误导性的 active tool spinner。

### 6.3 后端回归

- expired pending：credential-free 与完整 credential 均成功；旧 secret 清空、old=expired、operation 重绑。
- 未过期 pending 保持现有 claim + superseded replacement 成功路径；live lease、部分 credential、错误 binding/scope/hash/user/generation 全拒绝。
- 针对同一个 source session 的两个并发 refresh 仅一个 replacement commit。
- 刚从 snapshot 恢复、尚未过期但没有 URL 的 pending 卡片，点击 refresh 继续走现有 superseded replacement 并返回 200。
- lifecycle/controller 返回 200 新 action，不再是 500。
- 不执行 Base business argv。

## 7. 部署与回滚

- 顺序：后端 develop + Dev 健康检查，再前端 develop + Dev 健康检查。
- 回滚：后端和前端均可回退到部署前镜像；无 schema 变更。
- Dev 验收：新窗口运行原 Base 创建/字段/记录/重读提示词，观察连续授权卡片与刷新链路；Prod 不在本次授权范围。
