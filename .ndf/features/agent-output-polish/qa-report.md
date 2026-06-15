# QA 报告 — agent-output-polish (S5)

> NDF Standard S5 自动验收。日期 2026-06-15。分支 feature/agent-output-polish（双 worktree）。

## 1. 范围
4 项 agent 输出体验：#1 去问题卡检查页 / #2a+#4 产物卡片（图片+下载统一卡片）/ #3 emoji 禁用+markdown 美化 / #2b docx 嵌图引导（尽力）。

## 2. 自动化门禁（全过）
### numind-server
| 检查 | 结果 |
|------|------|
| `go test ./...` | **0 FAIL** |
| `golangci-lint`（skill+agent） | 0 finding |
| `gofmt -l` | clean |
新增测试：`TestPlatformBasePrompt_NoEmojiInstruction`（#3 提示防回归）、`TestOutputToolsPriorityAddendum_DocxEmbedImageNudge`（#2b 引导）。

### numind-web-v3
| 检查 | 结果 |
|------|------|
| `npx vitest run`（全量） | **857 passed / 81 files**（15 skipped, 3 todo），0 FAIL |
| `npx vue-tsc --noEmit` | 0 |
| `npx eslint`（改动文件） | 0 |
新增测试：`agentArtifacts.spec.ts`（23，含 P1-A 关键用例：签名URL抽取 / 第三方pdf不抽 / 第三方图不抽 / 混合 / mime 推断）、`AgentFinalAnswer.spec.ts`（卡片渲染+prose 去 COS 节点 + markdown 结构）、`QuestionPrompt.spec.ts`（去 review 步）。

## 3. 双 Sonnet review（每仓库双，共 4）
- 后端（T2+T5）：spec-compliance + code-quality **均 PASS（0 P0/P1）**。
- 前端（T1+T3+T4）：code-quality **PASS（0 P0/P1）**；spec-compliance 代码 **PASS**。
- spec-compliance 报的「P0 缺 S0-S3 工件」= **FALSE POSITIVE**：reviewer 在 web worktree 看到分支点旧 manifest + S0 `.ndf-active` 元数据；实际 requirement/proposal/spec/plan 均在 develop 已 commit（8e9e52d1/2bbf06b2/cdef1d43），manifest 条目 S3，`git show` 确认工件 EXISTS——流程正确（先工件后码）。

## 4. 运行时验收（S6 dev，按 plan T6）
完整链路（问题卡多问题答题→生成图片+docx→最终回答呈现）需 dev 后端+LLM+积分，本地不可复现。S6 ndf-done + /deploy-dev（server 先于 web）后于 dev 用 browse + E2E 凭据实跑确证：
1. #1：问题卡多问题答完最后一题点「提交」直接发送，无检查页。
2. #2a+#4：生成图片渲染为图片卡（可点大图）、可下载文档渲染为下载卡（文件名+下载），不再是纯文本/末尾小图；刷新后卡片仍在。
3. #3：AI 最终回答正文无装饰 emoji；markdown 标题/列表/分隔渲染精致。
4. #2b：观察 AI 是否把生成图嵌入 docx（prompt 引导，不保证）。

## 5. 回归保护诚实声明
- #1（去 review）、#2a+#4（extractArtifacts + 卡片渲染）有 vitest 持久回归保护。
- #3 emoji：后端 Go 单测锁「提示含禁 emoji 指令」（持久），但「AI 实际不输出 emoji」是行为，无强回归（dev 观察）。
- #3 markdown 美化（CSS）+ #2b（docx 嵌图，AI 行为）= 一次性 dev 确认，无自动回归保护（可接受）。

## 6. S6 部署约束
跨仓库 deploy **server 先于 web**（与 agent-qa-card-ux 同——前端 #3 渲染依赖后端无新依赖，但保持顺序一致 + #2b 引导在后端）。先 ndf-done（两仓库）→ /deploy-dev server → /deploy-dev（web）。TCR 100-tag 上限可能再次触发（见 memory），届时清旧 develop 标签后重部署。
