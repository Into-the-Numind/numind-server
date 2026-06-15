# QA 报告 — agent-output-redesign (S5)

> NDF Standard S5。日期 2026-06-15。分支 feature/agent-output-redesign（双 worktree）。

## 1. 范围
#1 卡片重设计(文件 A1/图片 B1/问题卡 C3) + #4 卡片就地渲染(修「文件下载：空着」) + #5 删反馈 + #2 docx 强嵌图。

## 2. 自动化门禁（全过）
| 仓库 | 检查 | 结果 |
|------|------|------|
| numind-server | `go test ./...` | **0 FAIL** |
| numind-server | golangci-lint(agent+skill+controller) | 0 finding |
| numind-server | gofmt | clean |
| numind-web-v3 | `npx vitest run`(全量) | **872 passed / 81 文件**, 0 FAIL |
| numind-web-v3 | `vue-tsc` | 0 |
| numind-web-v3 | eslint(改动文件) | 0 |

新增/更新测试：`docx_author_skillmd_test.go`(#2 强指令守卫)、`agentArtifacts.spec.ts`(splitIntoSegments 独占行切/列表内不切/第三方不切/顺序/mime)、`AgentFinalAnswer.spec.ts`(卡片就地顺序)、`AgentArtifactItem.spec.ts`(A1/B1)、`QuestionPrompt.spec.ts`(C3+保留全部行为)。删 feedback 后所有 store/view spec 不回归。

## 3. 双 Sonnet review（每仓库双，共 4）全 PASS（0 P0/P1）
- 后端 T1+T2：spec+quality 均 PASS。spec-compliance 报的「chatbot maxtokens 删除」P0/P1 = **FALSE POSITIVE**（用错 diff base `develop..HEAD`；feature `merge-base..HEAD` 不碰 chatbot，是 develop 领先 `fix/chatbot-thinking-maxtokens` merge，已 git 验证，ndf-done 无回归）。
- 前端 T3+T4+T5：spec+quality 均 PASS。P2 已修(stale 注释/QuestionPrompt 蓝→翠绿)，其余 P2 推迟(代码围栏内 COS 链接理论边界/extractArtifacts 重复/C3 textarea vs pill 功能超集)。

## 4. 运行时验收（S6 dev，按 plan T6）
完整链路需 dev 后端+LLM+积分，本地不可复现。S6 ndf-done + /deploy-dev(server 先于 web)后于 dev browse+E2E 实跑：
1. #4+#1：最终回答里文件卡(A1)/图片卡(B1)**就地**显示，「文件下载：」后直接是卡片不空；刷新后卡片仍在。
2. #1：问题卡 C3 对话感样式；多问题答完最后一题直接提交无检查页。
3. #5：底部无反馈条。
4. #2：观察 AI 是否把生成图嵌进 docx（强提示，预期提升不保证）。

## 5. 回归保护诚实声明
#4 分段就地/删反馈/A1/B1 卡片结构/C3 行为 有 vitest 持久保护；#1 视觉精细度 + #2 docx 嵌图(AI 行为) 为一次性 dev 确认（无自动回归，可接受）。

## 6. S6 部署
server 先于 web；TCR 100-tag 上限可能触发(清旧 develop 标签后重部署)。
