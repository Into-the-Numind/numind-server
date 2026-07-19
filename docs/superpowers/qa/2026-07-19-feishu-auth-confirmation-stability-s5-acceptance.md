# QA Report — 飞书授权确认稳定性

## 验证环境

- 后端：Standard feature worktree；Go 单元、集成、race 与全仓测试使用受控 fake `lark-cli`
- 前端：Standard feature worktree；Vue 单元测试、lint、typecheck
- 浏览器：Playwright mocked-browser，覆盖真实 31 秒确认延迟
- 外部协议：官方 `lark-cli` 仍使用原生 5 秒查询节奏；没有 fork 或修改 CLI

## 自动化检查结果

| 检查项 | 命令 | 结果 | 备注 |
|--------|------|------|------|
| Go lint | `PATH="$HOME/go/bin:$PATH" task lint` | PASS | `go vet` 与 `golangci-lint` 均通过 |
| Go 全仓测试 | `go test -p 1 ./... -count=1` | PASS | 全仓串行通过；仅有既有 macOS SQLite deprecated warning |
| Feishu 全包 | `go test ./internal/numind/biz/feishu -count=1` | PASS | 授权、连接、操作恢复与安全边界全部通过 |
| Feishu focused race | `go test -race ./internal/numind/biz/feishu -run 'TestDeviceAuthFlow_(ConfirmationDeadlineBoundsSynchronousDispatch|CompleteSuccessPublishesCandidateAtomically|CompleteClaimUsesDetachedBoundedContext)' -count=1` | PASS | 无 data race |
| 租约时序复核 | `go test ./internal/numind/biz/feishu -run '^TestWorkspaceLifecycleUnbindLeaseExpiryPreventsStaleOwnerCompletion$' -count=10` | PASS | 并行全仓首轮的单次时序抖动无法复现，连续十轮通过 |
| Vue lint | `npm run lint` | PASS | 0 error；7 个仓库既有 warning |
| Vue typecheck | `npm run type-check` | PASS | 通过 |
| Feishu API unit | `npm run test:unit -- --run src/api/feishu.test.ts` | PASS | 28/28 |
| Feishu Playwright | `npx playwright test e2e/feishu-personal-workspace.spec.ts` | PASS | 6/6；含真实 31 秒延迟场景 |
| 双独立审查 | 规格审查 + 代码质量审查 | PASS | P0/P1/P2 = 0 |

## 客户 RED 与回归保护

- 后端首个代码 commit `3ea11c01` 复现授权确认时间预算错配、不可诊断的 CLI 失败，以及缺少完整 Agent 绑定仍可触达 CLI 的问题。
- 前端首个代码 commit `8ac703e` 复现飞书继续请求错误继承全局 30 秒超时。
- `03cddc4` 将 31 秒延迟场景隔离为稳定的浏览器契约，证明无需刷新页面即可继续。
- `e7fde0ae` 增加 50 秒主动处理边界与阻塞 dispatch 回归，确保即使 Agent 恢复卡住也先于浏览器 60 秒上限返回，同时保留已成功授权的 durable 状态供幂等恢复。

## 验收标准核对

| 验收标准 | 结果 | 备注 |
|----------|------|------|
| 授权任务有效期 10 分钟 | PASS | 服务端默认授权会话为 10 分钟 |
| 飞书链接有效期 10 分钟 | PASS | CLI 返回值被严格封顶到 10 分钟 |
| 官方 CLI 每 5 秒查询一次 | PASS | 未修改或 fork CLI；平台只控制 30 秒调用窗口 |
| 仅手动点击继续 | PASS | 未增加自动确认、后台轮询或自动恢复入口 |
| 前端最多等待 60 秒 | PASS | 仅飞书 resume 请求设置 60 秒，其他 API 保持全局 30 秒 |
| 成功后立即恢复原 Agent | PASS | finalize 后同步 dispatch；没有 sleep、timer 或额外等待 |
| 60 秒窗口有稳定余量 | PASS | 后端主动处理最多 50 秒，最慢持久化收尾最多 5 秒，HTTP/网络约留 5 秒 |
| 严格绑定当前授权与原任务 | PASS | 核对 user、generation、session、device credential、app、operation、scope、AgentRunID、ToolCallID |
| 手动连接兼容 | PASS | 无原 Agent 操作时保持 `OperationID=nil`，不会伪造任务绑定 |
| 安全诊断日志 | PASS | 只允许固定 phase/outcome 与 ID；不记录 device code、token、CLI 原始 stderr 或应用密钥 |
| 幂等与故障恢复 | PASS | 授权成功先 durable commit；dispatch 超时可用相同操作补偿，不重复授权或重复执行 |

## Dev 验收提示词

在一个新对话中，让尚未具备目标权限的 Agent 执行：

`请更新飞书文档「有数飞书二次连接测试」：把“当前状态：待验证”替换为“当前状态：更新成功”，并在正文末尾追加“测试编号：DOC-UPDATE-003”。完成后告诉我修改结果。`

预期：出现授权卡片后，用户在 10 分钟内完成飞书授权并点击“我已完成，继续”；页面最长可等待 60 秒，不超时、不刷新，授权确认后立即恢复同一 Agent 的原任务，并且不出现多余的红色“执行出错”。

## 结论

`ALL_PASS`。允许进入 S6 原子合并、推送和 backend-first Dev 部署；Prod 不在本次范围内。
