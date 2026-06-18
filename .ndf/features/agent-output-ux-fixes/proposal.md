# Agent Mode 输出与附件呈现 UX 修复 — 提案

## §1 方案概述 [客户可见]
针对 Agent mode 使用中暴露的 4 个体验问题做一轮打磨：①调用技能时显示出具体技能名（"加载技能：docx-author"），不再是干巴巴的"正在调用工具 load_skill"；②用户上传附件后，聊天气泡只显示用户自己写的话 + 附件标签，不再暴露给 AI 看的内部系统提示文字；③AI 调用工具生成文档/网页等较慢操作时，给出更明显的"进行中"动效（流动的点），让用户知道在跑没卡死；④AI 生成的所有文件（Word/PDF/网页等）统一以"文件卡片"呈现（带下载按钮、可预览/可编辑），而不是一个点了就直接下载的裸链接。

预期效果：Agent 的过程更透明、输出更专业、生成物更易用。

## §2 报价与周期 [客户可见]
- 预估工作量：1-1.5 天（含 NDF 全流程 + dev 部署）
- 报价：内部研发，N/A（一人公司自研）
- 交付时间线：2026-06-18 当日推进至 dev 验收

## §3 技术可行性 [AI 内部]

### 现有功能复用（可行性高 — 基建齐全）
- **问题一**：后端 `tool_call_start` 事件 `input_preview` 已带 `name`，narration yaml 模板已正确；`AgentToolCallItem.vue` 已从 `events[0].message` 渲染。只需在前端 `agentChat.ts` 给 `load_skill` 补一个 label 分支（仿现有 `invoke_skill`/`use_skill`），从 `input_preview.name` 取技能名。
- **问题二**：前端 `UserMessage` 类型已有 `attachments?: {url, filename}[]` 字段，`AgentMessageItem.vue:143` 已渲染附件 chip。后端 `RunRequest` 已分别持有 `req.Message`（原文）与 `Input`（拼接后含系统提示）。复用 `buildTranscriptTurns` / `resignCOSLinks`。
- **问题四**：`AgentToolCallItem.vue` 已有 `Loader2` spinner + `isActive` 态 + `prefers-reduced-motion` 处理；后端已有 `tool_call_progress` 事件。复用现有时间线组件，只增强 active 态文案（取最新 progress 消息）+ 加流动点动效。
- **问题五**：前端 `AgentArtifactItem.vue` 的图片/HTML/docx 三种卡片**已完整实现**（含 HTML sandbox iframe 预览 + 下载、docx 打开编辑器）；`agentArtifacts.ts` 的 `standaloneArtifactOf`/`splitIntoSegments` 已能把独占行 COS 链接转卡片；`MIME_BY_EXT`/`DOC_EXTS` 已含 docx/pdf/xlsx 等。后端 `artifactFromToolResult` 已能解析任意文件的 url/filename/mime，`mimeFromArtifact` 已认 html→text/html。只需：(a) 后端把"只收图片"的 `imageCollector` 泛化为收集所有生成文件并嵌入最终回答；(b) 前端 `DOC_EXTS`/`MIME_BY_EXT` 补 `html`；(c) HTML 走 inline 签名以支持 iframe 预览。

### 技术风险与缓解
1. **文档链接重复/不成卡片（问题五核心风险）**：现有 `drainMarkdownExcluding` 对图片是"模型已写则不重复 append"。文档若被模型写成行内链接（非独占行），dedup 会跳过我们的独占行 → 前端不识别为卡片。**缓解**：文档单独处理——剥离最终回答里引用该文件 objectKey 的行内节点，再统一 append 独占 `[filename](url)` 行（保证每个文件恰好一个卡片，无裸链接、无重复）。图片行为保持不变（避免回退图片的就地展示）。
2. **HTML iframe 预览被 attachment disposition 强制下载**：现 html 走 `GenerateSignedDownloadURL`（attachment）。**缓解**：`uploadGeneratedFile` + `cos_resign` 把 html/htm 当 inline 签名（与图片同 inline 集），sandbox="" iframe 才能渲染；下载按钮走 `handleDownload` 新标签页打开不受影响。安全模型不变（iframe sandbox="" 无脚本无 same-origin，已有注释）。
3. **剥离系统提示后附件在 reload 丢失线索**：**缓解**：持久化 user turn 时带上 attachment refs（url+filename），`transformMessages` 回填 `msg.Attachments`，read 路径重签这些 URL（复用 `resignCOSLinks`），reload 显示干净文本 + 附件 chip。
4. **两处 embed 站点（streaming `runner_stream.go:373` + `finalizeRun` `runner.go:1340`）**：drain 会清空 collector。**缓解**：把 embed 逻辑收敛到共享 helper，两处复用，保持 drain-once 语义。
5. **Bug-from-Customer 回归保护**：问题二、五是正确性 bug（用户测试报告，Rule 11）→ 对应 task 第一个 commit 必须是失败的 Go 复现测试。

### 涉及仓库
- [x] numind-server（问题二、问题五后端）
- [x] numind-web-v3（问题一、问题四、问题五前端补 html + 复用卡片）
- [ ] numind-admin-web

### AI 可观测性（如功能涉及 LLM 调用）
- [ ] 涉及 LLM 调用：**否**（本批次不新增/不修改任何 LLM 调用；问题二仅调整持久化/展示文本，引导 file_read 的指令仍进 LLM 上下文不变；问题五仅处理工具产物的 markdown 嵌入）。
- Trace 起点 / Generation 点 / 关键元数据：N/A
- 约束：修改不得破坏现有 agent run 的 Langfuse trace/generation（`finalizeRun` 周边）。

## §4 产品需求定义 — PRD [AI 内部]

### 用户故事
- 作为 Agent 使用者，当 AI 加载技能时，我要看到**具体技能名**，以便知道它在用哪个能力。
- 作为 Agent 使用者，当我上传附件发消息后，我的气泡里**只看到我写的话 + 附件标签**，看不到给 AI 的内部系统提示，以便对话整洁可信。
- 作为 Agent 使用者，当 AI 调用工具生成文档/网页时，我要看到**明显的"进行中"动效**，以便知道它在跑而非卡死。
- 作为 Agent 使用者，当 AI 生成任何文件时，我要拿到**统一的文件卡片**（可下载、支持的可预览/可编辑），而不是裸下载链接。

### 验收标准
- [ ] **问题一**：agent 调用技能时，过程时间线显示"加载技能：{技能名}"，完成显示"已加载技能：{技能名}"（流式 + reload 后均含名）。
- [ ] **问题二**：上传附件并发送后，用户气泡**不含**"【系统提示】用户上传了以下附件…file_read…"字样；显示用户原文 +（若有）附件 chip；reload 后同样不含系统提示且附件 chip 仍在。
- [ ] **问题二（不回退）**：引导 AI 调用 `file_read` 的指令**仍进入 LLM 上下文**（即 LLM 仍被告知去读附件，行为不变）。
- [ ] **问题四**：AI 调用工具且处于运行中时，时间线条目有可感知的"进行中"动效（流动点/脉冲），而非完全静止的单一文案；若后端推送了 progress 消息，显示最新一条。
- [ ] **问题五**：生成 docx 文件 → 最终回答中以**文件卡片**呈现（带下载按钮 + 可打开编辑器入口），无裸下载链接。
- [ ] **问题五**：生成 HTML 文件 → 以**文件卡片**呈现，"预览"按钮能在 sandbox iframe 里**正常渲染页面**（不被强制下载），下载按钮可用。
- [ ] **问题五**：生成 PDF 等其它支持类型 → 同样卡片呈现。
- [ ] **问题五（不重复）**：同一个生成文件在最终回答里**只出现一个卡片**，不出现"卡片 + 裸链接"或"两个卡片"。
- [ ] 回归：第三方引用链接（非 COS agent-outputs 的 `[来源](http…)`）**不**被转成卡片，仍是普通链接。
- [ ] 回归：已有图片 inline/网格展示行为不变。

### 边界情况
- 附件为空 / 纯文字消息：问题二修复不影响（无附件则无 chip、无系统提示注入）。
- 模型自己把文件链接写成独占行 / 写成行内 / 根本没写：问题五都要保证恰好一个卡片。
- COS 预签名 URL 过期（>24h reload）：图片/文件/附件 URL 经 `resignCOSLinks` 重签。
- 文件名含中文 / 被 ASCII 化：复用现有 `filenameFromDisposition` + `keyTimestampPrefixRE` 取干净名（已有逻辑）。
- HTML 预览链接过期：卡片已有 iframe error 兜底（"页面无法显示，链接可能已过期 → 下载查看"）。

### 权限规则
- 不涉及权限/会员等级变更。document-system 编辑入口仍受 `VITE_ENABLE_DOCUMENT_SYSTEM` flag 控制（不改）。本批次纯展示/持久化/产物嵌入。

### UI 行为规格
- 页面位置：Agent 对话视图（`AgentChatView` / `AgentMessageItem` / `AgentToolCallItem` / `AgentFinalAnswer` / `AgentArtifactItem`）。
- 布局要求：技能名走过程时间线单行；附件 chip 走用户气泡下方；文件卡片走最终回答 segment。
- 交互模式：技能名只读展示；进行中动效自动播放（尊重 reduced-motion）；文件卡片点击预览/下载/打开编辑器（沿用现有交互）。
- 状态处理：进行中（spinner+流动点）/ 完成（绿勾+结果文案）/ 出错（红 alert）三态沿用；HTML iframe loading/error 兜底沿用。
