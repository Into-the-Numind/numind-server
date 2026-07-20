# QA Report — 飞书连续授权与实时任务推进

## 验证环境

- 后端：本地 Standard worktree
- 前端：本地 Standard worktree
- 浏览器：Dev 问题复现阶段已用 Chromium/Playwright 和 Network 请求诊断；修复后的真实多卡授权由用户在 Dev 集中验收

## 自动化检查结果

| 检查项 | 命令 | 结果 | 备注 |
|--------|------|------|------|
| Go lint | `PATH="$(go env GOPATH)/bin:$PATH" task lint` | PASS | `go vet` 与 `golangci-lint` 通过；仅 macOS sqlite 库弃用编译警告 |
| Go test | `go test ./internal/numind/biz/agent ./internal/numind/biz/narration ./internal/numind/biz/feishu -count=1` | PASS | 覆盖快照身份、多次授权历史、unknown_result 只读核验和跨进程写入围栏 |
| Vue lint | `npm run lint` | PASS | 0 errors；7 条原有未使用变量 warning |
| Vue type-check | `npm run type-check` | PASS | `vue-tsc --build --force` |
| Vue focused tests | `npm run test:unit -- src/stores/__tests__/agentChat-resume.spec.ts src/stores/__tests__/agentChat.spec.ts src/components/agent/__tests__/AgentMessageItem.spec.ts src/components/agent/__tests__/AgentMessageList.spec.ts` | PASS | 4 files / 137 tests |
| Admin checks | N/A | N/A | 管理端未涉及 |

## 浏览器 QA

- 修复前已用 Playwright、浏览器 Network 与 Dev run 252 日志确认：真实后端快照没有顶层 `session_id`，前端因此持续丢弃轮询结果；刷新页面走另一条加载路径才显示第二张卡片和最终内容。
- 用户要求省略非必要自动 E2E，并在 Dev 自行完成最终真人多授权验收。

## 可观测性验证

- 本次没有新增 trace 类型或日志协议；沿用现有 run、operation、session 和 tool narration 可观测性。
- 结论：N/A（无可观测性 schema 变更）

## 验收标准核对

| 验收标准 | 结果 | 备注 |
|----------|------|------|
| 第二张授权卡无需刷新即可出现 | PASS（自动化） | 快照显式返回并校验 session identity；无 URL 的持久化卡片仅自动恢复一次实时链接 |
| 卡片后 narration 和任务进展持续显示 | PASS（自动化） | 监听工具事件签名，原位更新也触发跟随展示 |
| 授权前已成功的飞书结果不会在下一段丢失或重复写 | PASS | 成功结果以 provider-safe tool turn 持久化，参数固定为空对象且内部证据不进入前端 JSON |
| 中途失败不会在最终成功时被伪装成绿色成功 | PASS | 仅收束仍在执行中的节点，明确 error/rejected 保持真实状态 |
| unknown_result 不重复写，同时允许确认结果 | PASS | 最多三次 Catalog 证明的只读核验；写入围栏跨 worker/restart 保持 |

## 结论

ALL_PASS（自动化 Gate）；待 Dev 真人多卡授权验收。

## 失败项修复要求

无自动化失败项。
