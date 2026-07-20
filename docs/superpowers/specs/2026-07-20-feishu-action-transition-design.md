# 飞书无业务确认与生命周期兼容 — 技术设计

日期：2026-07-20  
状态：APPROVED  
范围：numind-server、numind-web-v3、Dev

## 1. 目标

1. 新飞书操作永远不进入 `waiting_confirmation`，也不返回 `phase=confirmation`。
2. 飞书官方创建应用、用户授权和管理员授权流程保持不变。
3. catalog 白名单中的读写命令在账号、generation、scope、权限、加密请求和幂等检查通过后直接执行。
4. catalog 标记 `RequiresCLIYes` 的命令由服务器自动追加 `--yes`；模型和浏览器均不能提供该信号。
5. 历史 `waiting_confirmation` 操作可直接恢复且最多执行一次；旧卡片动作不再造成参数错误。
6. 前端不展示确认/取消业务按钮。

## 2. 不做的事

- 不取消飞书官方授权。
- 不新增 API、数据库字段、后台配置开关或外部服务。
- 不把 argv、URL、设备码、App Secret、scope 证明交给浏览器决定。
- 不修改命令白名单或放宽命令参数校验。
- 不运行与本故障无关的全量 E2E、全仓 race/coverage 或视觉回归；客户负责最终产品验收。

## 3. 状态机

### 3.1 新操作

```text
not_started
  ├─ connection/scope missing → waiting_connection / waiting_app_scope / waiting_user_auth
  │                              └─ official authorization complete → executing
  └─ prerequisites satisfied ───────────────────────────────────────→ executing

executing → succeeded / failed / unknown
```

`waiting_confirmation` 不在新操作状态机中。

### 3.2 历史操作

部署时已是 `waiting_confirmation` 的行保留兼容：

```text
waiting_confirmation --Resume/user_completed/confirmed--> executing --> terminal
waiting_confirmation --cancelled--------------------------> cancelled
```

恢复始终重新打开该 operation 原有的加密请求；浏览器动作不携带业务参数。现有 operation lease、execution gate、request fingerprint 和 idempotency key 继续保证最多一次。

## 4. 后端设计

### 4.1 OperationService

- 删除 `executeClaimed` 中创建 confirmation action 和转换到 `waiting_confirmation` 的主动分支。
- 所有已连接的非本地命令都获取账户级 execution gate，包括 `RiskHigh`。
- `invokeOnce` 在 `persisted.RequiresCLIYes` 时无条件、服务器侧追加 `--yes`。
- `Resume` 遇到历史 `waiting_confirmation` 时直接 claim and execute，不再原样返回等待结果。
- `Confirm` 仅作为滚动升级兼容别名调用 `Resume`；不再是新状态转换入口。
- `Cancel` 只为历史确认行保留，尊重旧客户端已经明确发出的取消。
- `ConfirmationRequester` 可以暂留为源码兼容依赖，但生产路径和新 operation 都不得调用它；后续清理不属于本次快速交付。

### 4.2 WorkspaceLifecycleService

- `user_completed` 以服务器当前 operation 状态为准。
- 如果旧授权卡片到达时 operation 已处于历史 `waiting_confirmation`，调用共享 dispatcher 直接恢复，而不是返回 InvalidParameter。
- `confirmed` 保留为旧客户端兼容动作，效果等同恢复历史 operation；新前端不再发送。
- terminal operation 继续只补偿 Agent handoff，不重放飞书命令。

### 4.3 安全与一致性不变量

- 当前登录 `user_id`、当前 account generation、operation owner 必须一致。
- operation 的 `agent_run_id`、`tool_call_id`、加密请求和 request fingerprint 不可由请求覆盖。
- CLI 参数仍必须由 CommandCatalog 标准化，且 runner 只接收标准化 argv。
- 每次真正写操作仍持有 execution gate；未知结果仍不得自动重试。
- 取消业务确认不等于取消权限检查、命令白名单、账号绑定或幂等控制。

## 5. 前端设计

- `FeishuActionCard` 不再提供 confirmation 的确认/取消按钮和相关文案。
- 对滚动升级期间收到的 legacy `phase=confirmation`，前端以“正在继续原任务”的非交互状态触发一次兼容 resume；同一 operation 使用现有 in-flight 去重，禁止重复请求。
- create_app、user_auth、app_scope 卡片和重新生成链接逻辑不变。
- 不展示计时，不新增弹窗或新组件。

## 6. API 契约

沿用：`POST /v1/feishu/operations/:operation_id/resume`

请求：

```json
{"action":"user_completed|confirmed|cancelled"}
```

- `user_completed`：官方授权步骤完成；若服务器发现历史确认态，则兼容为直接恢复。
- `confirmed`：只为旧客户端保留；不得由新 UI 产生。
- `cancelled`：只对历史等待确认行有意义。

响应沿用现有 `OperationResult`。新操作不得返回 `action.phase=confirmation`。

错误边界：

- 归属/generation 不匹配：NotFound/Unavailable，避免泄漏。
- 非法 action 字符串：InvalidParameter。
- 合法旧卡片动作但服务器状态已推进：返回当前幂等结果，不返回 InvalidParameter。

## 7. 回归保护

必须先提交失败测试：

1. 高风险/`RequiresCLIYes` 命令在 scope 满足后应直接调用 runner，旧实现会返回 confirmation。
2. 历史 `waiting_confirmation` 通过 `Resume` 应直接执行且重复调用不重放。
3. 旧 `user_completed` 卡片遇到历史确认态不应 InvalidParameter。
4. 前端 legacy confirmation 不显示确认按钮，并只触发一次兼容 resume。

执行范围：上述定向测试、受影响 Go package 测试、`task lint`、前端定向 Vitest、`npm run lint`、`npm run type-check`。其余由客户在 Dev 手动验收。

## 8. 验收映射

| PRD 验收项 | 设计覆盖 |
|---|---|
| 不产生 confirmation | §3.1、§4.1 |
| `--yes` 服务端自动追加 | §4.1、§4.3 |
| 历史确认态直接执行且幂等 | §3.2、§4.1 |
| 旧授权卡片不再参数错误 | §4.2、§6 |
| 前端无业务确认 UI | §5 |
| 无新 API/DB/凭据持久化 | §2、§6 |

## 9. 已接受风险

客户明确要求暂不考虑业务确认安全风险。设计仍保留飞书官方授权、命令目录、账号绑定、作用域预检、加密请求、执行租约和幂等机制，但不再要求用户对写操作做第二次确认。
