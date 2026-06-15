# Agent Mode 输出体验打磨 — 实施计划（agent-output-polish）

> NDF Standard S3。基于 spec `2026-06-15-agent-output-polish-design.md`。后端 task 先于前端。每 task 完成后双 Sonnet review。

## 依赖图（无环）
```
T2(后端 #3 emoji 提示) 独立
T5(后端 #2b docx 技能引导) 独立
T1(前端 #1 去检查页, QuestionPrompt.vue) 独立
T3(前端 #2a+#4 产物卡片, extractArtifacts util + AgentFinalAnswer.vue)
T4(前端 #3 美化, AgentFinalAnswer.vue CSS) — 与 T3 同改 AgentFinalAnswer → 串行 T3 后
T6 = S5 验证策略
```
实现顺序：T2 → T5 → T1 → T3 → T4 → T6。

## T2 — 后端 #3：基础提示禁用 emoji
**仓库**：numind-server
**改动**：`internal/numind/biz/skill/constants.go` `PlatformBasePrompt` 末尾加「输出风格」段：不用 emoji/表情符号装饰；用 markdown 标题/加粗/列表/分隔组织结构。
**测试**：Go 单测断言 PlatformBasePrompt 含 no-emoji 指令；既有 prompt 组装测试不回归（assembleSystemPrompt 注入 base prompt）。
**验收**：`go test ./internal/numind/biz/skill/... ./internal/numind/biz/agent/...` 0；`task lint` 0。

## T5 — 后端 #2b：docx 嵌图引导（尽力）
**仓库**：numind-server
**改动**：`skills/docx-author/SKILL.md`（Template 2 附近加「若本 run 用 image_gen 生成过图，把其 COS URL 放进 input_files + doc.add_picture 嵌入」）；`internal/numind/biz/agent/output_tools_priority_prompt.go`（docx 引导处加同样提示）。
**测试**：Go 单测/grep 断言 output_tools_priority_prompt 含嵌图引导；SKILL.md 含引导文案。
**验收**：`go test ./internal/numind/biz/agent/...` 0；lint 0。诚实声明：prompt 引导不保证 AI 每次照做。

## T1 — 前端 #1：去掉问题卡检查页
**仓库**：numind-web-v3（worktree，node_modules symlink）
**改动**：`src/components/agent/QuestionPrompt.vue`——多问题最后一题 footer 按钮直接「提交」调 `submitAnswers()`；`goNext` 最后一题不再设 `reviewing=true`；移除/不进 review 面板分支（保留 tab 导航可回看改答）。单问题路径不变。
**测试**：`QuestionPrompt.spec.ts` 更新——多问题答完最后一题点「提交」→ emit('answer-submitted', answers) 含全部已答，无 review 面板（删/改既有 review-step 测试）。
**验收**：vitest QuestionPrompt PASS；type-check 0；eslint 0。

## T3 — 前端 #2a+#4：产物卡片
**仓库**：numind-web-v3
**改动**：
- 新增 `src/utils/agentArtifacts.ts`：`extractArtifacts(markdown): { prose: string, artifacts: ArtifactRef[] }`——正则抽取指向 COS/agent-outputs 的图片 `![](url)` + 可下载文档链接 `[t](url)`(扩展名 docx/pdf/xlsx/pptx/csv/doc)，从 prose 移除这些节点；mime 按扩展名推断；普通网页链接不动。
- `src/components/agent/AgentFinalAnswer.vue`：用 extractArtifacts 分离 → prose 部分 renderMarkdown(v-html) + 下方 `<AgentArtifactItem v-for>` 卡片（复用既有组件，传 {id:序号, filename, url, mime}）。
- 测试：`agentArtifacts.spec.ts`（图片/文档/普通链接/混合/COS判定）+ `AgentFinalAnswer.spec.ts`（渲染图片卡+下载卡，prose 不含原始链接）。
**验收**：vitest PASS；type-check 0；eslint 0；刷新持久化（派生自 markdown）由设计保证。

## T4 — 前端 #3：markdown 美化
**仓库**：numind-web-v3（T3 后，同改 AgentFinalAnswer.vue）
**改动**：`AgentFinalAnswer.vue` `.markdown-body` scoped CSS——标题(h1-h3)分级字号/字重/上下间距、段落行距、`<hr>` 分隔线、列表缩进/项间距、加粗色重、引用块样式。替代 emoji 的结构分隔视觉。
**测试**：CSS 视觉为主（S6 dev browse 确认）；vitest 确保渲染不破坏（markdown-body 结构存在）。
**验收**：type-check 0；eslint 0；vitest 不回归。

## T6 — S5 验证策略（Rule 10）
- **方式**：后端 Go 单测（T2 prompt 指令、T5 引导文案）+ 前端 vitest（extractArtifacts 纯函数、AgentFinalAnswer 卡片、QuestionPrompt 去 review）+ **dev browse 实跑**（发起生成图片+docx 的 run：图片卡+下载卡显眼、emoji 消失、markdown 精致、问题卡无检查页）。
- **理由**：#1/#2a/#4 逻辑可 vitest 锁；#3 emoji（prompt 行为）+ markdown 美化 + 卡片视觉须运行时眼见为实（§6）。#2b 仅引导，dev 观察 AI 是否照做（不保证）。
- **回归保护诚实声明**：#1/#2a/#4/extractArtifacts 有 vitest 持久保护；#3 emoji 靠 prompt（无强回归测试，dev 观察）；markdown 美化 + #2b 为一次性 dev 确认（视觉/AI 行为无自动保护，可接受）。
- **关键路径**：问题卡多问题答题(无检查页)→ agent 生成图片+docx → 最终回答：图片卡+下载卡显眼、正文无 emoji、markdown 精致 → 刷新卡片仍在。
- **环境**：本地难复现完整 agent run（LLM/积分），主体走 dev browse（既有 agent-mode 做法）。

## 风险
- R1（产物抽取误伤）：extractArtifacts 必须只抽 COS/agent-outputs 产物链接，不动正文引用 URL → 充分单测覆盖。
- R2（去检查页丢"确认"环节）：用户可能误提交未答全——保留 tab 导航(已答✓标记)+ 至少一题已答才能提交(既有 canSubmit)，可接受。
- R3（#2b 不保证）：诚实声明，dev 观察。
