# QA 报告 — agent-runtime-ui-simplify (S5)

> NDF Standard S5。日期 2026-07-19。分支 `feature/agent-runtime-ui-simplify`（`numind-web-v3`）。

## 范围

- 输入内容后永不显示或请求“预计消耗积分”。
- 删除运行页顶部重复的“取消任务”按钮。
- 删除首屏“emoji + Agent 名称”身份标签。
- 将“新内容”改为只有向下箭头的 32px 圆形按钮。
- 输入停止键立即关闭浏览器 SSE/轮询，并调用既有 `POST /v1/agent-runs/:id/cancel`；取消失败时保留 active run 与重试入口。

## 自动化门禁

| 检查 | 结果 |
| --- | --- |
| `npm run test:unit -- AgentInputArea AgentChatHeader AgentFirstRun AgentMessageList` | **5 passed / 4 files** |
| `npx playwright test --project=mocked e2e/agent-streaming.spec.ts --reporter=line` | **8 passed, 1 expected skipped**, exit 0 |
| `npm run lint` | **0 errors**；7 个既有未使用变量 warning，均不在本 feature 改动文件 |
| `npm run type-check` | **0 errors** |
| `git diff --check` | clean |

浏览器回归明确覆盖：输入停止键发出一次精确 `/cancel` POST；取消请求悬挂或 5xx 时本地 SSE 已立即 Abort 且按钮可重试；恢复的非 SSE active run 仍可停止；运行态没有顶部“取消任务”、预估积分或首屏 identity pill。

## 独立审查

- spec-compliance：PASS，0 P0/P1。确认 Header 无取消 props/emit/template，预估请求链移除，欢迎区仅保留 welcome heading，圆形箭头有 `aria-label`、title、hover 与 focus-visible。
- code-quality：PASS，0 P0/P1。审查期间提出两项 P2 测试可靠性问题，均已修复：浏览器 absence 断言先等待 textarea 挂载并使用 `toHaveCount(0)`；已跳过的 dev integration 不再引用已删除的 header credits selector。

## S6 Dev 部署与运行时验收

- `ndf-done` 已合并并推送前端 `develop`：merge commit `fbb2cce`；feature worktree 和本地分支均已清理。
- `bash scripts/cicd/release.sh dev --deploy-only` 已将镜像 `ccr.ccs.tencentyun.com/youshunumind/numind-web-v3:develop-fbb2cce` 部署至 Dev。
- Dev 容器健康、部署机本地 `/health`、公开 `http://49.233.219.254:9200/health` 均返回 `healthy`。
- 已使用测试账号进入 Dev 的真实 Agent 运行页并点击输入停止键：`POST /v1/agent-runs/:id/cancel` 两次均返回 HTTP 200 / `code: 0`，页面显示“已取消任务”。

真实 run 的服务器终态未能作为“running → cancelled”取证：两次分别使用会议纪要助手和 Web 调研助手，均在约 1–2 秒内被 Dev 上游模型以 `model_error` 结束为 `failed`（`credits_used: 0`），早于取消后的状态轮询。这是 Dev 模型可用性限制，不是本次前端调用链失败；浏览器契约测试已永久覆盖精确 cancel POST、成功/失败/悬挂取消及本地 stream abort。等待用户在 Dev 的人工确认；本次未申请或执行生产发布。
