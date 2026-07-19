# 飞书连续授权卡片与过期刷新 — 实施计划

设计依据：`docs/superpowers/specs/2026-07-20-feishu-sequential-auth-refresh-design.md`

## 执行顺序

后端契约保持不变，先完成后端 RED/GREEN，再完成前端 RED/GREEN，最后统一审查、验收、合并和 Dev 部署。两个仓库的 feature 分支第一个实现 commit 都必须是失败的客户复现测试。

## Task 1 — 后端客户 RED：expired-but-pending 刷新失败

### 描述
在不修改生产代码的前提下，复现 Dev session 的精确状态：protocol-v2、`user_auth`、state=pending、服务器时间已过期、无活动 lease、operation 仍精确等待该 session。覆盖完整凭据、已 sweep 的无凭据形态、完整但已过期 lease，以及尚未过期无 URL 卡片的现有 refresh 路径。

### 涉及文件
- `internal/numind/biz/feishu/device_auth_flow_test.go`
- `internal/numind/store/feishu_workspace_test.go`
- `internal/numind/biz/feishu/service_test.go`
- 必要时 `internal/numind/controller/v1/feishu/feishu_test.go`

### 验收条件
- 修复前至少一个 focused test 明确 FAIL，错误为 unavailable/500 或 replacement conflict。
- 测试断言设计 §5 的现有 API 契约：`POST /v1/feishu/actions/{session_id}/refresh` 使用空 body，返回 HTTP 200 + fresh action；尚未过期 pending 继续返回 superseded replacement 200。
- 响应不得泄漏 scopes、device code、密文、App Secret 或内部错误，operation 终态继续保持现有 terminal 分支。
- “已过期后点击我已完成，继续”必须返回 200、`authorization_expired` + replacement action，不进入 CLI completion、不伪造完成、不重放 Base。
- commit message：`test(qa): reproduce expired Feishu action refresh`
- Task 完成后并行 reviewer：spec reviewer 核对真实 FAIL 与首 commit 顺序；quality reviewer 核对测试没有放宽身份/权限边界。

## Task 2 — 后端 GREEN：安全原子替换过期 pending session

### 描述
扩展 device refresh source 分类和现有 replacement transaction，仅新增服务器已过期、无活动 lease、完整 exact binding 的 pending v2 source，同时保持未过期 pending 的现有 claim + superseded replacement。处理 credential-free 与 complete credential；接受 owner/until 成对完整但已过期的 lease；拒绝 partial credential、live lease、半截 lease 与身份/摘要/scope 冲突。同一个 source session 并发刷新仅一个 commit。

### 涉及文件
- `internal/numind/biz/feishu/device_auth_flow.go`
- `internal/numind/biz/feishu/service.go`
- `internal/numind/store/feishu_workspace.go`
- Task 1 的测试文件（补安全与并发矩阵，不删除 RED）

### 验收条件
- Task 1 RED 全部转绿。
- 旧 session 在同一事务变为 expired 并清密文/key/expiry/lease。
- replacement 为 credential-free v2，operation summary 原子指向它。
- 设计 §5 的 refresh 空 body / 200 action / terminal 分支契约不变，响应不泄漏任何敏感字段或内部错误。
- 未过期 pending 继续走现有 replacement；live lease、部分 credential、半截 lease、异常 CompletedAt、错误 protocol/phase/operation state/summary/user/generation/scope/hash 全拒绝。
- 同一个 source session 的两个并发 refresh 仅一个 replacement commit；生命周期 stale-card 恢复保持现有语义。
- 过期后点击“我已完成，继续”返回 200 replacement action，不进入 CLI completion 或 500。
- 未触发 Base business executor。
- focused Go tests 与 targeted `-race` 通过。
- Task 完成后并行 reviewer：spec reviewer 核对 API/相邻 continue/安全矩阵；quality reviewer 核对事务、CAS、并发和 secret 清理。

## Task 3 — 前端客户 RED：恢复后第二张卡片不出现

### 描述
复现 op1 已完成，同一 run 的 resumed leg 再次 waiting，session snapshot 返回不同 op2 `external_action`。固定无需页面 reload 即应出现第二张卡，并覆盖重复 poll、历史卡保留、同 operation live URL 与跨会话迟到响应。

### 涉及文件
- `src/stores/__tests__/agentChat-resume.spec.ts`
- `e2e/feishu-personal-workspace.spec.ts`

### 验收条件
- 修复前 Vitest 客户路径明确 FAIL：messages 中缺少 op2。
- Playwright 场景明确要求 page load count 保持 1、第二卡出现、refresh body 为空、普通 `/answer` 调用为 0。
- 不同 operation 即使 snapshot 合成 message id 相同，也必须期待唯一的本地 message id；第二张卡出现时不得保留孤立的通用“处理中” spinner。
- commit message：`test(qa): reproduce missing sequential Feishu action`
- Task 完成后并行 reviewer：spec reviewer 核对客户链路真实 FAIL；quality reviewer 核对测试隔离、时序和无 reload 证明。

## Task 4 — 前端 GREEN：等待交互快照协调

### 描述
让 `refreshRunStatus()` 同时协调 snapshot 中的 `external_action` 与 `question_prompt`。复用 action 白名单，按 run+operation upsert，为新 operation 生成独立本地 ID；保留 epoch/session/run/waiting 围栏，不能让无 URL snapshot 覆盖 live URL。注入后启动现有五秒轮询。

### 涉及文件
- `src/stores/agentChat.ts`
- Task 3 的测试文件（只补断言/边界，不删除 RED）

### 验收条件
- op1 completed + op2 pending 同时正确显示，重复 poll 不重复。
- route/session 切换和 run 终态的迟到 snapshot 不注入。
- 同 operation 已有 live URL 不被降级。
- same operation + new session 原位换卡并撤销旧 URL；迟到 refresh response 与迟到 snapshot 均不能污染新 route/session/run。
- 不同 operation 始终使用唯一的本地 message id；第二张卡出现时旧的 active tool spinner 已收口。
- 无 URL snapshot 只显示手动“重新生成链接”，不得自动 refresh。
- Playwright 无 reload 显示第二卡，refresh 成功后显示官方 URL，无 console/page error。
- Task 完成后并行 reviewer：spec reviewer 核对去重/围栏/手动 refresh；quality reviewer 核对类型安全、Vue key、轮询和 UX 状态。

## Task 5 — S4 全量质量门禁与最终双审查

### 描述
完成跨仓库编译、静态检查、完整测试及最终安全/规范审查，关闭 T1-T4 reviewer 的所有 P0/P1。

### 涉及文件
- `.ndf/manifest.yaml`
- 仅审查发现需要修复的既有 Task 文件

### 验收条件
- 后端：focused tests、`go test ./...`、targeted race、`task lint` 全部退出码 0。
- 前端：focused Vitest、`npm run lint`、`npm run type-check` 通过。
- 独立 spec-compliance 与 code-quality/security reviewer 均无 P0/P1。
- 两仓库第一个 feature commit 均为客户 RED 且修复后保留。

## Task 6 — S5 本地验收与 QA 报告

### 描述
在 feature worktree 启动本地后端和前端，运行完整测试与真实浏览器验收；禁止把验证推迟到 Dev。

### 涉及文件
- `.ndf/features/feishu-sequential-auth-refresh/qa-report.md` 或 `docs/superpowers/qa/2026-07-20-feishu-sequential-auth-refresh-s5.md`
- 仅验收发现需要修复的既有 Task 文件

### 验收条件
- 后端 `task test`、`task lint` 全部退出码 0。
- 前端 lint/type-check、focused Vitest、相关 Playwright 全部通过。
- 前端 `npm run test:e2e` 完整套件退出码 0；focused Playwright 仅作为客户回归的快速定位证据。
- Playwright 证明 op1 完成后 op2 无 reload 出现、无孤立 spinner、手动 refresh 空 body 且无 `/answer`。
- 浏览器截图 QA 无 P0 视觉/交互回归。
- QA 报告逐条核对 PRD 验收标准，结论为 ALL_PASS。

## Task 7 — S6 原子合并、后端优先 Dev 部署与冒烟

### 描述
分别运行 `ndf-done` 原子合并并 push 两仓库 develop，随后先部署后端 Dev、验证健康，再部署前端 Dev、验证健康和真实页面行为。

### 涉及文件
- `.ndf/manifest.yaml`
- Dev 部署记录文档（如项目既有流程要求）

### 验收条件
- `ndf-done` 成功合并并 push 两仓库 develop，worktree 和本地 feature branch 被清理。
- 后端 Dev 容器、公开 healthz 与关键启动日志正常。
- 前端 Dev 容器与公开 root/health 正常。
- Dev 浏览器冒烟证明无需 reload 显示连续授权卡；过期 refresh 返回新 action，不出现 Internal server error。
- 进入 S6 等待用户验收；Prod 未部署。

## 依赖图

```text
T1 backend RED -> T2 backend GREEN
T2 -> T3 frontend RED -> T4 frontend GREEN
T2 + T4 -> T5 full gates / final review -> T6 local QA -> T7 merge / Dev
```

任务按上述顺序串行。虽然前后端属于不同仓库，但前端验收依赖锁定后的后端 refresh 语义；本次不以并行写入换取速度，避免测试契约漂移。
