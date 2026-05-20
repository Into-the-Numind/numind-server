# Follow-up: agent-mode-skill-system monitoring API

**触发**: #10 `agent-mode-configurator-ux` S2 spec §14（commit `a41cded5`）
**优先级**: P2（监控功能要等 #14 e2e rollout agent_run 表真实数据后才有意义）
**类型**: backend

---

## Problem

`numind-admin-web` 的 `/agent-monitoring` UI（`AgentMonitoring.vue`）已就位：
- NoticeBanner: "实时监控功能即将上线（v1 不联机）"
- DataTable 列定义就位（学员 / Agent / 开始时间 / 已用时 / 已用积分 / 状态 / 操作）
- v1 fetcher 返回硬编码空数组，0 HTTP 调用（避免 404 噪音）
- TODO(#14) 注释标记真实 API wire 点

后端**缺这两个端点**：
1. `GET /v1/agent/sessions/active` — 返回当前父账户名下所有正在运行的 agent_run（含学员姓名、agent 名、开始时间、已用积分、状态）
2. `POST /v1/agent/sessions/:id/cancel` — 强制取消 agent_run（管理员紧急停止）

## #10 当前缓解（v1）

v1 UI 永远空状态。当 #14 完成后，开本 follow-up 接入真实数据源。

## Proposed scope (后续 feature)

### Backend

1. `numind-server/internal/numind/controller/v1/agent/session.go`（新文件）：
   - `GET /v1/agent/sessions/active` handler
     - 查询参数：page / page_size
     - 返回当前 parent_user_id 下所有 `agent_run.status IN ('running', 'pending')` 的会话
     - JOIN agent_definition 获取 agent 名字
     - JOIN user 获取学员名字
     - 响应：`{ list: SessionRow[], total }`
   - `POST /v1/agent/sessions/:id/cancel` handler
     - 校验 session 隶属当前 parent_user_id（防越权）
     - 调 #14 提供的 `runner.Cancel(runId)` 中断 ReAct loop
     - 写 `agent_run.status = 'cancelled_by_admin'` + `cancelled_at`
2. `numind-server/internal/numind/router.go` 注册两端点（user_token middleware）
3. `numind-server/internal/numind/biz/session/`（新子包，可选）：
   - 查询 active sessions（GORM query）
   - cancel session（调 runner.Cancel + DB update 事务）

### Frontend

升级 `numind-admin-web/src/views/agent/AgentMonitoring.vue`：
- 移除 NoticeBanner（功能上线）
- onMounted 启动 30s setInterval 调 `GET /v1/agent/sessions/active`
- onBeforeUnmount 清理 interval
- 表格行 [强制取消] 按钮调 `POST /sessions/:id/cancel`（用 ConfirmModal danger）
- [查看 Trace] 按钮跳 Langfuse URL（由 #14 提供 trace_id field 的话）

### Out of scope（此 follow-up 不包含）

- Langfuse trace 真实跳转 URL（依赖 #14 真实 LLM + Langfuse 集成）
- WebSocket 推送（v1 用 30s 轮询足够）
- 历史 session 查询（仅 active；历史走 #14 e2e rollout 后的独立审计页）

## Acceptance

- 父账户 GET /v1/agent/sessions/active 返回所有运行中会话
- 跨父账户隔离：父账户 A 看不到父账户 B 的 sessions
- POST /sessions/:id/cancel 立即中断 ReAct loop + DB status 更新 + 学员端 SSE 推送 "管理员强制取消"
- admin-web `/agent-monitoring` 显示真实数据 + 30s 自动刷新 + 强制取消可用

## 依赖

- #14 `agent-mode-e2e-rollout` 必须先 merged：
  - 真实 `runner.Cancel(runId)` API
  - `agent_run` 表 status 含 `cancelled_by_admin` 枚举值
  - trace_id field on agent_run（如果要支持 [查看 Trace]）

## 估算

- Backend: ~250 行 + 6 单测，~2-3 小时
- Frontend: ~100 行修改 + 1 e2e spec，~1 小时
- 总 ~3-4 小时

## 关联

- #10 `agent-mode-configurator-ux`（in progress）— UI 骨架已就位
- #14 `agent-mode-e2e-rollout`（pending）— 提供 runner.Cancel + agent_run 表实际数据
