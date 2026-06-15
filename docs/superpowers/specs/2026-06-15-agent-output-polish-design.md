# Agent Mode 输出体验打磨 — 提案+技术设计（agent-output-polish）

> NDF Standard：本文件合并 S1 提案/PRD + S2 技术设计（4 个体验项均为明确 UX 改进，无需重定义问题，故合并）。版本 1.0。

## §1 方案概述 [客户可见]
打磨 agent mode「最终交付」体验：问题卡去掉多余检查页、生成的图片与可下载文档统一做成显眼产物卡片、AI 回复不再堆 emoji 且 markdown 渲染更精致。

## §2 工作量/范围
- 内部修复，无对外报价。预估 ~1–1.5 天。交付目标：dev 部署 + browse 验证。
- 涉及仓库：numind-server（提示 + 技能）、numind-web-v3（问题卡 + 最终回答渲染 + 产物卡片）。

## §3 技术可行性 + 落点
| # | 落点 | 复用 |
|---|------|------|
| 1 去检查页 | `QuestionPrompt.vue`：多问题最后一题按钮直接 `submitAnswers()`，不进 `reviewing` 步 | 既有 submit 逻辑 |
| 2a+4 产物卡片 | `AgentFinalAnswer.vue`：从最终回答 markdown 抽取图片(`![](cos-img)`)+可下载文档链接(`[t](cos-doc)`)→渲染成 `AgentArtifactItem` 卡片，prose 去掉这些原始节点 | **复用 `AgentArtifactItem.vue`**（已含图片缩略图+预览、文件行+下载、HTML 预览） |
| 2b docx 嵌图 | `skills/docx-author/SKILL.md`（已有 add_picture/input_files 模板）+ `output_tools_priority_prompt.go`：加「若本 run 生成过图片，把其 URL 传进 input_files 并 add_picture」的引导 | 既有技能模板 |
| 3 emoji | 后端 `skill/constants.go` `PlatformBasePrompt` 加「不用 emoji，用标题/加粗结构」；前端 `AgentFinalAnswer.vue` markdown CSS 美化（标题分级/间距/分隔线/列表） | 既有 markdown 渲染 |

### AI 可观测性
不涉及新增 LLM 调用入口（#3 改提示内容、#2b 改技能内容，都走既有 aiservice + run_python）。N/A。

## §4 PRD
### 用户故事
- 答多问题时，最后一题点一下就提交，不再多一步检查页。
- AI 生成的图片和可下载文档以**显眼卡片**呈现，一眼知道能看图/能下载，不被正文淹没。
- AI 回复干净专业，不堆 emoji；结构靠标题/加粗/分隔呈现，排版精致。

### 验收标准
- [ ] #1：多问题卡最后一题按钮为「提交」，点击直接发送（无 review 中间页）；单问题行为不变。
- [ ] #2a+#4：最终回答里的生成图片渲染为图片卡片（可点开大图）；可下载文档（docx/pdf/xlsx/pptx/csv）渲染为下载卡片（文件名+下载按钮），不再是纯文本链接；prose 不重复显示原始链接/inline 图。刷新后卡片仍在（派生自持久化 markdown）。
- [ ] #3：新对话里 AI 最终回答正文不含装饰 emoji（系统提示生效）；markdown 标题/列表/加粗渲染更精致（间距/分级/分隔）。
- [ ] #2b：docx-author 技能 + 输出提示包含「嵌入本 run 生成图片」的引导（尽力，不保证 AI 每次照做——诚实声明）。
- [ ] 回归：现有最终回答渲染、artifact 消息、问题卡单/多问题、流式等不回归。
- [ ] 门禁：server `go test ./...`+lint 0；web vitest+tsc+eslint 0。

### 边界
- 下载卡片 mime 推断：按 URL 扩展名（.docx/.pdf/.xlsx/.pptx/.csv→对应 mime；图片→image/*）。无法识别的链接保持普通链接（不误伤正文里的普通超链接，如引用来源 URL）。
- 抽取只针对「指向 COS / agent-outputs 的产物链接」，普通网页引用链接(`[来源](https://example.com)`)保持原样。
- emoji：仅平台基础提示约束最终回答正文；不影响工具 narration（那层已 deemoji）。
- reduced-motion：卡片为静态，合规。

## §5 技术设计细节

### 5.1 #1 去检查页（QuestionPrompt.vue）
- 现状：多问题 `goNext()` 在 `isLast` 时设 `reviewing=true` 进 review 步；footer 在 review 步显示「提交」。
- 改：多问题最后一题的 footer 按钮直接显示「提交」并调 `submitAnswers()`；去掉 review 步（`reviewing` 相关 UI/分支删除或不再进入）。`goNext` 在最后一题不再切 reviewing。保留 tab 导航（可回看/改答）。单问题路径不变。
- 测试：多问题答完 → 最后一题点「提交」→ emit('answer-submitted', answers) 含全部已答；不出现 review 面板。

### 5.2 #2a+#4 产物卡片（AgentFinalAnswer.vue）
- 在 `AgentFinalAnswer` 中，从 `props.markdown` 解析出 artifacts：
  - 图片：markdown 图片 `![alt](url)`，url 指向 COS/agent-outputs 图片。
  - 下载文档：markdown 链接 `[text](url)`，url 指向 COS/agent-outputs 且扩展名为 doc/office/pdf/csv。
- 渲染：prose markdown（移除上述 artifact 节点后）via 既有 v-html + 下方渲染 `<AgentArtifactItem v-for>` 卡片（mime 按扩展名推断；id 用序号）。
- 实现方式（稳健）：写一个纯函数 `extractArtifacts(markdown): { prose: string, artifacts: {filename,url,mime}[] }`（utils，可单测），AgentFinalAnswer 用它分离 prose + 卡片。
- COS 签名 URL（含 `;`/`&`）已验证 marked/DOMPurify 正常；卡片用原 url。
- 持久化：派生自 `props.markdown`（最终回答），刷新后 snapshot 仍有该 markdown → 卡片重现。

### 5.3 #3 emoji 禁用 + 美化
- 后端 `skill/constants.go`：`PlatformBasePrompt` 末尾加一段「输出风格」指令：不要用 emoji/表情符号装饰；用 markdown 标题(##)/加粗/列表/分隔来组织结构。
- 前端 `AgentFinalAnswer.vue` `.markdown-body` CSS：标题分级字号/字重、段落与标题间距、`<hr>` 分隔线样式、列表缩进/间距、加粗色重——让结构化 markdown 更精致（替代 emoji 的视觉分隔作用）。

### 5.4 #2b docx 嵌图（尽力）
- `skills/docx-author/SKILL.md`：在 Template 2 附近加一句「若本次对话中你用 image_gen 生成过图片，把图片的 COS URL 放进 run_python 的 input_files，并用 doc.add_picture 嵌入正文对应位置」。
- `output_tools_priority_prompt.go`：在 docx 输出引导处加同样的提示。
- 诚实声明：这是 prompt 引导，提升概率但不保证 AI 每次照做。

## §6 验证策略（S5）
- 后端：Go 单测（constants.go 含 no-emoji 指令的 prompt 组装不破坏既有；docx skill 内容存在）。
- 前端：vitest——`extractArtifacts` 纯函数（图片/文档/普通链接区分）、AgentFinalAnswer 渲染卡片、QuestionPrompt 去 review、markdown 美化不破坏渲染。
- dev browse 实跑（S6 后）：发起会生成图片+docx 的 agent run，确认图片卡片+下载卡片显眼、emoji 消失、markdown 精致、问题卡无检查页。

## §7 非目标
- 不实现真正的 docx 生成工具（仍走 run_python+技能）。
- 不动 DB schema / 不新增 API。
- 不保证 #2b 的 AI 每次嵌图。
