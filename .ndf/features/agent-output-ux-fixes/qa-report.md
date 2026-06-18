# QA 报告 — agent-output-ux-fixes (S5)

日期：2026-06-18
环境：本地（worktree feature 分支，未合并 develop）

## 验证策略（per S3 plan T6 / rule 10）
- 后端两正确性 bug（问题二、五，Bug-from-Customer）：Go 单测复现 + 永久回归。
- 前端 html 提取 / label 逻辑：vitest 永久回归。
- 视觉/交互（技能名、流动点动效、文件卡片 + HTML iframe 预览）：**S6 部署 dev 后用 gstack `/qa` 对 $DEV_SITE_URL 取证**（环境约束 + 用户"做到 dev"指令的诚实偏离；该 gstack /qa 是交付阻塞门禁）。

## 确定性门禁结果（本地真跑）

### 后端 numind-server（worktree）
| 检查 | 命令 | 结果 |
|------|------|------|
| 全量构建 | `go build ./...` | ✅ PASS（仅依赖 sqlite cgo deprecation 警告，非错误）|
| 单元测试 | `go test ./internal/numind/biz/agent/` | ✅ PASS |
| Race 检测 | `go test -race ./internal/numind/biz/agent/` | ✅ PASS（17.8s）|
| vet | `go vet ./internal/numind/biz/agent/` | ✅ PASS |
| gofmt | `gofmt -l` 改动文件 | ✅ 干净 |

> `task lint`（golangci-lint）因 golangci-lint 不在本机 PATH 未跑（环境问题，非代码），以 `go vet` 兜底（干净）。

复现测试（永久留存）：
- `TestFinalizeRun_UserTurnExcludesSystemPromptAndCarriesAttachments`（问题二：user turn 不含【系统提示】+ 含 attachments）+ `TestFinalizeRun_AttachmentOnlySend` + `TestRunRequest_displayUserText` + `TestTransformMessages_UserAttachmentsParsed`。
- `TestArtifactCollector_FinalizeInto_EmbedsDocAsStandaloneLink` / `_EmbedsHTML` / `_StripsInlineDocLink`（问题五）+ `TestStripNodesReferencing_NoPrefixCollision` + `TestCosIsInlineRenderName` + `TestResignCOSLinks_HTMLSignsInline` + 图片回归 3 例。

### 前端 numind-web-v3（worktree）
| 检查 | 命令 | 结果 |
|------|------|------|
| 类型检查 | `npm run type-check`（vue-tsc）| ✅ PASS |
| Lint | `eslint`（改动文件）| ✅ PASS（exit 0）|
| 单元测试 | `npx vitest run` | ✅ PASS（90 files / 965 passed / 11 skipped）|

新增 vitest（永久回归）：
- `agentArtifacts.spec.ts`：html/htm → text/html 卡片、第三方 .html 不提取、splitIntoSegments html 路径、mime 表 html/htm。
- `agentChat-streaming.spec.ts`：load_skill start+result 带名、name 空回退兜底。
- `AgentToolCallItem.spec.ts`：progress 取最新 label + 流动点 active 显/done 隐。

## 待 S6 dev 验证的关键用户路径（gstack /qa 取证，阻塞交付）
1. 上传附件发送 → 用户气泡无"【系统提示】…file_read…"、显示原文 +（有则）附件 chip；reload 仍然。
2. 触发 skill 的 agent run → 时间线"加载技能：<名>" / "已加载技能：<名>"。
3. 生成 docx → 文件卡片（下载 + 打开编辑器入口）；生成 HTML → 文件卡片，"预览"按钮 iframe 正常渲染（不被强制下载）；运行中可见流动点动效。

## 结论
确定性门禁全 PASS（覆盖两个后端正确性 bug + html 提取逻辑，永久回归）。视觉/交互验证转 S6 dev gstack /qa（阻塞门禁）。可进 S6。
