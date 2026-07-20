# QA 报告 — 飞书显式连接入口 (S5)

> NDF Standard 精简执行。日期 2026-07-21。分支 `feature/feishu-explicit-connect-entrypoint`，涉及 `numind-server` 与 `numind-web-v3`。

## 验证范围

- 未连接用户明确说“连接/重新连接/授权飞书”时，Agent 本轮调用独立 `lark_connect` 并生成可恢复授权动作。
- 设置页原地启动连接并按服务器返回的精确 `session_id` 完成，不跳转到无动作的 Agent 页面。
- Agent、设置页和业务授权共享同一个账号 generation bootstrap owner；重复点击、并发入口、崩溃占位与滚动升级均不会创建并行个人应用 worker。
- 连接只申请固定 `offline_access`；Docs/Base/Wiki 等业务 scope 继续按真实业务命令增量申请。
- 旧卡片、缺失 URL、早点击、刷新与进程恢复都能收敛到服务端当前真值。

## 自动化门禁

| 检查 | 结果 |
| --- | --- |
| `PATH="$(go env GOPATH)/bin:$PATH" task lint` | **PASS** |
| `go test -timeout 10m ./internal/numind/biz/feishu ./internal/numind/store` | **PASS**；Feishu 153.796s，Store cached |
| `go test -timeout 10m ./...` | 本 feature 及其依赖包 **PASS**；仅既有 `internal/numind/biz/xhsscript` analytics 测试失败，develop 同一精确测试同样失败（期望计数非零、实际为零），与本次零依赖、零改动 |
| `npm run lint` | **PASS**；0 errors，7 个既有无关 warnings |
| `npm run type-check` | **PASS** |
| `npm run test:unit -- --run` | **PASS**；99 files，1145 passed，11 skipped，3 todo |
| `npx playwright test e2e/feishu-personal-workspace.spec.ts --workers=1` | **PASS**；11/11 |
| `git diff --check` | **PASS**；双仓库 clean |

## 独立审查

- 状态机/原子性审查：PASS，0 P0/P1。确认 connection-only operation、exact-session completion、单 bootstrap owner、崩溃接管、近期 completed-session dispatch grace 和 v1/v2 滚动兼容。
- 安全/UX 审查：PASS，0 P0/P1。确认 user/generation/session/operation 均由服务端校验，只接受飞书官方链接，不允许 scope、URL 或凭据注入；旧 terminal 卡自动清理并刷新真值。

## 验收标准

| 验收标准 | 结果 |
| --- | --- |
| Agent 暴露独立 `lark_connect` 并覆盖显式 connect/reconnect/authorize 意图 | PASS |
| 当前用户、run、tool call 创建幂等 connection-only operation；未连接 yield，已连接成功返回 | PASS |
| `lark_inspect` 保持只读，业务请求仍由 `lark_execute` 触发增量授权 | PASS |
| 设置页 `POST /v1/feishu/connect` 后原地显示官方链接和继续动作 | PASS |
| “我已完成，继续”确认精确 session，缺 URL 时恢复同一步骤 | PASS |
| 三个入口在同 generation 内最多一个 bootstrap worker，超时占位可安全接管 | PASS |
| 客户 RED 测试转绿，关键 Go/Vue/Playwright 回归通过 | PASS |

## 可观测性

N/A：本次不新增 LLM 调用；沿用既有 Feishu operation/session 状态和 Agent external-action 恢复日志。

## 结论

**FEATURE_GATES_PASS**。全库唯一红项已在未含本 feature 的 `develop` 基线上复现，记录为既有 `xhsscript` 测试故障，不阻断本次飞书修复进入 Dev。

## S6 Dev 部署与运行时验收

- `ndf-done` 已合并、推送并清理两个 feature worktree/本地分支：后端 merge commit `37ffbf2a`，前端 merge commit `66a3ad0`。
- Dev 后端运行精确镜像 `numind-server:develop-37ffbf2a`，registry digest `sha256:5140cf381e9e28c4493ab71635310672c4e7f200c1121133f923d5fbd4ada566`。
- Dev 前端运行精确镜像 `numind-web-v3:develop-66a3ad0`，registry digest `sha256:2ef6cc8eb541f7cf6891ec8953562f4b0efd785cccfc97949e80d091be696377`。
- 公开后端 `http://49.233.219.254:9091/healthz` 返回 `code: 0 / status: ok`；公开前端 `http://49.233.219.254:9200/health` 返回 `healthy`。
- 两个容器最终状态均为 `running / healthy`；后端本次启动日志未发现 `panic` 或 `fatal`。
- 功能停在 S6，等待用户 438 在 Dev 验收；未申请、未执行 Prod 发布。
