# Agent 产物卡片重设计 — 实施计划（agent-output-redesign）

> NDF Standard S3。基于 spec `2026-06-15-agent-output-redesign-design.md` + playground `docs/numind-card-playground.html`。后端先于前端。每 task 双 Sonnet review。

## 依赖图
```
T1(后端 #2 docx 强嵌图) 独立
T2(后端 #5 删反馈端点) 独立
T3(前端 #5 删反馈条, AgentFinalAnswer+组件+store/api/types)
T4(前端 #1+#4 卡片就地渲染, AgentFinalAnswer+splitIntoSegments+AgentArtifactItem) — 与 T3 同改 AgentFinalAnswer → 串行 T3 后
T5(前端 #1 问题卡 C3, QuestionPrompt) 独立
T6 = S5 验证策略
```
顺序：T1 → T2 → T3 → T4 → T5 → T6。

## T1 — 后端 #2：docx-author 强嵌图
**仓库**：numind-server
**改动**：`skills/docx-author/SKILL.md`——把上个 feature 的软提示(建议)升级为**强指令+完整可抄代码示例**：「若本对话用 image_gen 生成过图，写 docx 时**必须** ①run_python input_files 传图 URL ②`doc.add_picture('/workdir/input/<name>', width=Inches(5.5))` 放封面/对应章节」。语气"必须"。
**测试**：Go 单测/grep 断言 SKILL.md 含「必须」「input_files」「add_picture」强指令。
**验收**：`go test ./internal/numind/biz/agent/... ./internal/numind/biz/skill/...` 0；lint 0。诚实：非硬保证。

## T2 — 后端 #5：删反馈端点
**仓库**：numind-server
**改动**：`internal/numind/controller/v1/agent/student_query.go`——删 `POST /agent-runs/:id/feedback` 路由注册 + `WriteFeedback` handler + `feedbackRequest` type；`internal/numind/biz/agent/`（WriteFeedback 所在）——删 `StudentQueryService.WriteFeedback` + `FeedbackRequest`；清相关测试（feedback_test 等）。grep `WriteFeedback`/`feedbackRequest`/feedback route 确认无残留死端点。**保留 terminal_metadata 字段 + MergeTerminalMetadata**（error 等仍用）。
**测试**：删对应测试；`go test ./internal/numind/...` 0（确认无编译残留引用）。
**验收**：go test 0；lint 0；grep 确认端点删净。

## T3 — 前端 #5：删反馈条
**仓库**：numind-web-v3（worktree，node_modules symlink）
**改动**：`src/components/agent/AgentFinalAnswer.vue` 去 `<AgentFeedbackBar>` + import + **feedback-specific props `initialFeedback`/`initialNote`（删）；`runId` prop 保留（通用，无害）**；删 `src/components/agent/AgentFeedbackBar.vue` + `__tests__/AgentFeedbackBar.spec.ts`(若有)；`src/stores/agentChat.ts` 删 `submitFeedback`(+return export)；`src/api/agent.ts` 删 `submitFeedback`；`src/api/agent.mock.ts` 删 `submitFeedback`；`src/types/agent.ts` 删 `FeedbackRequest` + **`FinalAnswerMessage` 的 `feedback?`/`feedback_note?` 字段（删；`run_id?` 保留通用）**。
- **★P1-B（编译失败风险）**：`src/components/agent/AgentMessageItem.vue`（约 L287-289）删 `:initial-feedback="asFinalAnswer.feedback"` + `:initial-note="asFinalAnswer.feedback_note"` 绑定（`:run-id` 保留）。否则删 props 后 type-check 报 unknown prop。
- **★P1-C（mock 残留）**：清以下测试文件里的 `submitFeedback: vi.fn()` mock key：`agentChat.spec.ts`/`agentChat-resume.spec.ts`/`agentChat-streaming.spec.ts`/`views/agent/__tests__/AgentChatView.spec.ts`/`composables/__tests__/useAgentNarration.spec.ts`；`AgentMessageItem.spec.ts`(约 L26) 删 `initialFeedback` mock prop。
- grep `submitFeedback`/`FeedbackBar`/`FeedbackRequest`/`initialFeedback`/`feedback_note` 全仓库确认零残留。
**测试**：更新 AgentFinalAnswer.spec（去 feedback stub/断言）；**全量 vitest + vue-tsc 编译过无残留引用**（P1-B/C 的兜底）。
**验收**：vitest 0 FAIL；type-check 0；eslint 0。

## T4 — 前端 #1+#4：卡片就地渲染（A1/B1）
**仓库**：numind-web-v3（T3 后，同改 AgentFinalAnswer）
**改动**：
- `src/utils/agentArtifacts.ts` 加 `splitIntoSegments(markdown): Segment[]`（`Segment={type:'prose',html} | {type:'artifact',ref}`）：复用既有 COS 判定+mime+filename，按原顺序切 prose/artifact 段（prose 段 renderMarkdown 成 html）。**保留旧 extractArtifacts + 其既有测试不改**。
  - **★P1-A 切割规则（必须，否则破坏 markdown 块结构）**：只在 **COS 节点独占一行/段落**（前后是行边界或空行，可含 `[...]:` 前缀如「文件下载：」在同一行也算独立——即该行除了 label 文字+链接没有别的块结构）时才切出 artifact 段；**COS 链接嵌在列表项/表格单元格/被正文包围的行内**时**保留在 prose 中不切**（避免切出半截列表/表格）。实现取整行/整段为切割单位。
  - **★P1-A 测试用例必含**：①COS 下载链接独占一行（含「文件下载：[link]」整行）→ 切出卡片，prose 不留半截；②COS 链接在**列表项中间**（`- 报告：[link] 分析…`）→ **不切出，保留 prose**（断言 segments 里该 artifact 不被提取）；③多个独立行 artifact + prose 交替顺序正确；④第三方链接任何位置都不切。
- `src/components/agent/AgentFinalAnswer.vue`：`segments=computed(()=>splitIntoSegments(props.markdown))`；模板 `v-for` 段：prose→`<div class="markdown-body" v-html>`、artifact→`<AgentArtifactItem :artifact="{id:i,...ref}">`。保留 copy/image-preview + markdown 美化 CSS。
- `src/components/agent/AgentArtifactItem.vue`：文件行重设计为 **A1**（doc-badge 翠绿淡底+文件名+meta+下载图标按钮）；图片重设计为 **B1**（圆角缩略图+说明，点击 modal 保留）。严格用 token。参照 `docs/numind-card-playground.html` 的 .fa1/.ib1 样式。**（P2）ArtifactRef 无 size 字段 → A1 的 meta 显示文件类型标签（由 mime/扩展名推 `DOCX`/`PDF`/`XLSX` 等大写），不显示 KB（拿不到）。不扩 ArtifactRef 接口（保持 AgentMessageItem 既有用法不回归）。**
- 测试：`agentArtifacts.spec.ts` 加 splitIntoSegments（分段顺序/句中链接/COS判定/第三方不抽/混合）；`AgentFinalAnswer.spec.ts` 断言卡片就地（artifact 段在 prose 段之间）、「文件下载：」prose 后紧跟卡片；`AgentArtifactItem.spec.ts` 更新 A1/B1 结构。
**验收**：vitest 0；type-check 0；eslint 0；全量 vitest（AgentMessageItem 用 AgentArtifactItem 不回归）。

## T5 — 前端 #1：问题卡 C3
**仓库**：numind-web-v3
**改动**：`src/components/agent/QuestionPrompt.vue` 重设计为 **C3 对话感软卡**（参照 playground .c3）：头像行「{agent名或助手} 想确认一下」+衬线问题+chip 选项(选中翠绿实底)+圆角输入框/send（或保留 textarea 配 chip）。**保留**多问题 tab/进度导航(已答✓)+最后一题直接提交(上 feature 已删 review)+answered 折叠卡(displayAnswer 真实答案)+多选/单选 aria 语义+jsdom 友好(button 非 label-input)。
**测试**：`QuestionPrompt.spec.ts` 更新选择器到 C3 结构（chip class），保留行为断言（选/答/提交 emit/answered 渲染/多问题导航）。
**验收**：vitest QuestionPrompt PASS；type-check 0；eslint 0。

## T6 — S5 验证策略（Rule 10）
- 后端 Go 单测（T1 强指令、T2 端点删净）+ 前端 vitest（splitIntoSegments、A1/B1 卡片、C3 问题卡、删反馈无残留）+ dev browse 实跑（卡片就地显示「文件下载：」不空、A1/B1/C3 视觉、无反馈条、markdown 精致、docx 嵌图观察）。
- 回归保护诚实声明：#4 分段就地/删反馈/卡片结构有 vitest 持久保护；#1 视觉精细度 + #2 docx 嵌图(AI 行为)为一次性 dev 确认（无自动回归，可接受）。
- 关键路径：agent 生成图+docx → 最终回答里 A1 文件卡+B1 图片卡**就地**显示、问题卡 C3、无反馈条 → 刷新卡片仍在。
- 环境：本地难复现完整 run，主体 dev browse。

## 风险
- R1（分段渲染破坏 markdown 结构）：prose 段独立 renderMarkdown，artifact 段切出——确保切割不破坏列表/表格等块结构（artifact 通常在段落边界/独立行；句中链接切出后 prose 段仍是合法 markdown）。充分单测。
- R2（删反馈漏残留导致编译失败）：grep 全链路 + 全量 vitest/go test 兜底。
- R3（#2 不保证）：诚实声明。
- R4（C3 重设计丢多问题逻辑）：保留 tab/导航/answered，测试覆盖。
