# Agent 产物卡片重设计 — 提案+技术设计（agent-output-redesign）

> NDF Standard 合并 S1+S2（均为明确选型/修复，无需重定义）。版本 1.0。设计选型见 `docs/numind-card-playground.html`（A1/B1/C3）。

## §1 概述 [客户可见]
按 User 在 playground 选定的设计，重做 agent 输出的文件卡（A1）、图片卡（B1）、多问题卡（C3），修掉「文件下载：空着」的 bug，删掉坏掉且没人用的反馈条，并强化 docx 嵌图引导。

## §2 范围/工作量
~1–1.5 天，跨仓库。交付：dev 部署 + browse 验证。

## §3 落点 + 复用
| # | 落点 | 设计/做法 |
|---|------|----------|
| #1 文件卡 A1 | `AgentArtifactItem.vue`（file-row 重设计） | doc badge(翠绿淡底)+文件名+`DOCX · 49 KB`+下载图标按钮 |
| #1 图片卡 B1 | `AgentArtifactItem.vue`（image-wrap 重设计） | 圆角缩略图(240×150)+说明，点击大图预览（保留既有 modal） |
| #1 问题卡 C3 | `QuestionPrompt.vue` 重设计 | 头像行「XX 想确认一下」+衬线问题+chip 选项+圆角输入框；保留多问题 tab 导航+直接提交（review 已删） |
| #4 就地渲染 | `AgentFinalAnswer.vue` + 新 util | **分段就地渲染**：prose/artifact 按原顺序交替，卡片出现在链接原位，不再 strip+底部追加 |
| #5 删反馈 | 前端 `AgentFinalAnswer`+`AgentFeedbackBar`+store/api+types；后端 route+controller+biz+types | 整条链路删除 |
| #2 docx 嵌图 | `skills/docx-author/SKILL.md` | 软提示→强指令+代码模板（必须 input_files+add_picture，位置交 AI） |

### AI 可观测性
无新增 LLM 调用入口（#2 改技能内容）。N/A。

## §4 PRD 验收
- [ ] #4：最终回答里下载链接/图片在**原位置**渲染成卡片，「文件下载：」后面直接是卡片不留空；刷新后卡片仍在（派生 markdown）。
- [ ] #1：文件卡=A1、图片卡=B1、问题卡=C3，与 playground 视觉一致，符合品牌（翠绿/衬线/克制）。
- [ ] #5：最终回答底部无「这个回答对你有帮助吗？」；前后端反馈代码删净；无残留引用/死端点。
- [ ] #2：docx-author 技能含强嵌图指令+代码模板（input_files+add_picture）。dev 观察 AI 是否照做（提升但不保证）。
- [ ] 回归：最终回答 prose 渲染/markdown 美化(上个 feature)/问题卡多问题逻辑/artifact 既有用法(AgentMessageItem) 不回归。门禁全绿。

### 边界
- 分段渲染：artifact 在句中（「文件下载：[link]」）→ prose「…文件下载：」+卡片(块) 紧随；可接受。
- COS 产物判定沿用上个 feature（host myqcloud/cos.ap- + path agent-outputs/），第三方链接不动。
- 删反馈：保留 agent_run.terminal_metadata 字段本身（其它用途如 error），只删 feedback 写入链路。

## §5 技术设计

### 5.1 #4 分段就地渲染
- 新 util `src/utils/agentArtifacts.ts` 加 `splitIntoSegments(markdown): Segment[]`，`Segment = {type:'prose',html:string} | {type:'artifact',ref:ArtifactRef}`。
  - 复用既有 COS 判定 + mime 推断 + filename 提取。
  - 扫描 markdown，遇 COS 产物节点（图/下载链接）就把前面的 prose 切一段（renderMarkdown 成 html），artifact 切一段；保持原顺序。非 COS 节点留在 prose。
  - 保留旧 `extractArtifacts`（其它处若用）或迁移；AgentFinalAnswer 改用 splitIntoSegments。
- `AgentFinalAnswer.vue`：`segments = computed(()=>splitIntoSegments(props.markdown))`；模板 `<template v-for="(s,i) in segments">` → `<div v-if="s.type==='prose'" class="markdown-body" v-html="s.html">` 或 `<AgentArtifactItem v-else :artifact="{id:i,...s.ref}">`。保留 copy/image-preview，删 feedback（见 #5）。markdown 美化 CSS（上个 feature）保留。

### 5.2 #1 卡片设计（A1/B1，参照 playground CSS）
- `AgentArtifactItem.vue`：
  - 非图非 html 文件行 → **A1**：`.file-card` doc-badge(40×40 圆角 翠绿淡底 `--accent-ultra-soft`/border `--accent-soft`)+文件名(`--text` 600)+meta(`DOCX · {size}` `--text-muted`)+下载按钮(翠绿淡底图标)。size 若无则省。
  - 图片 → **B1**：`.image-card` 圆角缩略图(max 240×150 border-radius 8)+说明(文件名/「点击看大图」)，点击 openPreview（保留 modal）。
  - HTML artifact 分支保留（沙箱预览不动）。
- 颜色严格用 token：`--primary hsl(160,72%,40%)` / `--accent-ultra-soft` / `--border` / `--text*` / `--radius-*` / `--shadow-card`。

### 5.3 #1 问题卡 C3（QuestionPrompt 重设计）
- 头像行：`.q-who` 圆形头像(--primary 底)+「{agent或"助手"} 想确认一下」。
- 问题：衬线(`--font-heading`)。
- 选项：chip 风格（圆角 pill，选中=翠绿实底白字）；多选/单选语义保留（aria-pressed）。
- 自由文本：圆角输入框 + send 按钮（或保留 textarea，C3 用单行 input+send；多行需求保留 textarea 亦可——实现时取 chip+圆角输入即可）。
- 多问题：保留 tab/进度导航（已答✓）+ 最后一题直接「提交」（上个 feature 已删 review）。单问题路径适配。
- answered 折叠卡保留（上个 feature 的 displayAnswer/真实答案）。

### 5.4 #5 删反馈（前后端）
- 前端：`AgentFinalAnswer.vue` 去掉 `<AgentFeedbackBar>` + import；删 `AgentFeedbackBar.vue` + 其 spec；删 `agentChat.ts` 的 `submitFeedback` + `api/agent.ts` 的 `submitFeedback` + `types/agent.ts` 的 `FeedbackRequest`；清相关 mock/测试。grep `submitFeedback`/`FeedbackBar`/`FeedbackRequest` 确认无残留。
- 后端：`student_query.go` 删 feedback route 注册 + `WriteFeedback` controller + `feedbackRequest` type；删 biz `StudentQueryService.WriteFeedback` + `FeedbackRequest`；清相关测试。grep `WriteFeedback`/`feedback` route 确认无残留死端点。保留 terminal_metadata 字段（error 等仍用）。

### 5.5 #2 docx 强嵌图（docx-author SKILL.md）
- 把上个 feature 的软提示（blockquote 建议）升级为**强指令 + 完整代码示例**：明确「生成过 image_gen 图就必须：①run_python 的 input_files 传图 URL ②add_picture('/workdir/input/<name>', width=Inches(5.5)) 放到封面/对应章节」，并给一段可直接抄的 python 片段。语气从"建议"改"必须"。
- 诚实声明：提升服从度（0%→预期 80-95%），非硬保证。

## §6 验证策略（S5）
- 后端：Go 单测（docx-author 含强指令；feedback 端点已删——路由/handler 不存在）。
- 前端：vitest（splitIntoSegments 分段顺序/COS 判定/就地渲染；AgentArtifactItem A1/B1 渲染；QuestionPrompt C3 渲染+多问题逻辑；feedback 删除无残留引用编译过）。
- dev browse：发起生成图+docx 的 run，确认 文件卡 A1/图片卡 B1 就地显示（「文件下载：」不空）、问题卡 C3、无反馈条、markdown 精致；docx 观察是否嵌图。
- 诚实：#1 视觉 + #2 AI 行为为一次性 dev 确认；#4/分段/删反馈有 vitest 持久保护。

## §7 非目标
- 不动 DB schema；不改 #3 agent 提示词（User 自改）；不保证 #2 每次嵌图；不重做 HTML artifact 沙箱预览。
