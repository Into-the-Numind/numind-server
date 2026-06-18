# Agent Output UX Followup-2 — Spec + Plan（S0-S3 合并）

> agent-output-ux-followup dev 验收发现的 6 个问题（用户 2026-06-18）。新 Standard。跨 numind-server + numind-web-v3。
> 三路只读调查定位根因（file:line 见下）。用 dynamic workflow 实现：BE(#2+#5) ‖ FE(#1+#3+#4+#6)。

## 任务

### 后端（numind-server worktree）

**BE-A（#2 附件名去 ID 前缀）**：上传 object key = `agent-attachments/<userID>/<unixnano>-<filename>`（`biz/attachment/upload.go:169-172`，unixnano≈19 位数字防冲突）。`model.AgentAttachment.Filename` 存的是**干净原始名**。chip 显示带前缀=走了 URL 派生路径（`student_run_lifecycle.go` `displayAttachmentsFromURLs`→`filenameFromURL` 取 object-key 末段含前缀）。
- 修：`filenameFromURL` 返回前剥掉**纳秒级前缀** `^\d{13,}-`（只剥 ≥13 位数字+连字符，避免误伤 "2024-报告.docx" 这种短前缀）。AttachmentIDs 路径已用 `att.Filename`（干净）不动。
- repro test：`filenameFromURL("https://x/agent-attachments/9/1781779536452527550-和皎皎的对话.docx?sig=a")` == "和皎皎的对话.docx"；且 "2024-plan.docx" 前缀不被剥。

**BE-B（#5 system prompt 指引，消除文件下载表格重复）**：根因=system prompt 无指引→AI 自作主张生成"下载链接表格"，叠加后端 finalizeInto 的卡片=重复。调查：prompt 段在 `runner.go` assembleSystemPrompt（~723 toolsSection），工具优先级指引在 `output_tools_priority_prompt.go`（OutputToolsPriorityAddendum），无"文件如何呈现"指引。
- 修：新增一段 system prompt 指引（中英），明确："当文件生成工具(create_html/run_python/docx-author 等)返回文件 URL 时，**不要自己写下载链接/表格/列表**；系统会自动把生成文件渲染成卡片(预览+下载)。回答里每个文件最多自然提一次，不要做下载清单。" 注入到工具段（与 OutputToolsPriorityAddendum 同处或紧随）。
- 这是引导性修复（概率性），是"表格 vs 卡片"根本矛盾的唯一干净解（系统负责呈现，模型只引用）。保留 finalizeInto 表格安全 append 不变。
- 验证：prompt 组装单测断言新指引段出现在 system prompt 中（若有 assembleSystemPrompt 可测路径）。

### 前端（numind-web-v3 worktree）

**FE-A（#1 附件 icon emoji→lucide）**：`AgentMessageItem.vue:177` 附件 chip 用 `📎` emoji（违反 no-emoji）。输入框 `AgentInputArea.vue` 用 lucide `Paperclip`。修：chip 改用 `<Paperclip :size="13"/>`（import 已有则复用），与输入框统一，去 emoji。

**FE-B（#2 前端 live chip 也用原始名）**：配合 BE-A——确认前端 live 发送时的 chip 文件名用的是原始 File.name / 上传响应的 filename，而非 URL 派生。若 live 也显示了带前缀名，改为用原始名（上传 store/composable 里）。reload 路径由 BE-A 修。

**FE-C（#3 工具调用+深度思考左对齐正文）**：assistant 消息 avatar(28px)+gap 把 content-wrap 右移；content-wrap 内正文 markdown-body 左边界=x。工具行 `AgentToolCallItem.vue .tl-line padding-left:8px` + icon + 连接器 → 工具文字比正文更右；ThinkingBlock 缩进类似。
- 修：让工具时间线 + ThinkingBlock 的**左边界与正文 markdown-body 左边界对齐**（去掉多余左缩进）。注意 `.tl-line::after` 虚线连接器 left 偏移依赖缩进，同步重算别画坏。目标：三者(工具/思考/正文)左对齐到同一 x。

**FE-D（#4 终止并入发送按钮）**：现状=发送按钮在 `AgentInputArea.vue`(230-237 常驻)，独立"终止"在 `AgentChatView.vue`(537-542 abort-bar，仅流式显)。
- 修：发送按钮按运行态切换——空闲(可发送)=发送图标(灰/绿)；运行中(isStreaming/sending/running)=终止图标，点击调 stop/cancel。删 AgentChatView 的中央 abort-bar。把运行态 + stop 动作传入 AgentInputArea（props/emit）。
- 终止链路已验证生效（cancelRun→后端 runner.Cancel→context cancel；流式/IO 立即，LLM/工具调用在当前 step 结束停）。本 task 只改 UI 合并，不改后端终止逻辑。

**FE-E（#6 可编辑卡片加"编辑/打开"图标）**：docx 卡片当前只有下载按钮，看着像没能力，但 `canEdit`(isDocumentSystemEnabled && isEditable) 时整卡可点开编辑器。`AgentArtifactItem.vue` isHtml 有眼睛(预览)，其它 doc 分支只有下载。
- 修：可编辑(canEdit)的非 html 文档卡片(docx/md/txt)加一个"编辑/打开"lucide 图标按钮(如 `Pencil`/`SquarePen`)，点击=openEditor，让可编辑能力可见。html 保持眼睛(预览)+编辑(整卡)。图片不变。

## S5 验证
- BE：Go 单测（filenameFromURL 剥前缀/不误伤；prompt 指引段出现）+ go test/vet。
- FE：vitest（若有可测逻辑：发送/终止态切换、附件名）+ type-check + eslint。
- 视觉端到端（icon/对齐/按钮合并/卡片图标/文件名干净/重复消除）→ S6 dev 后用户取证。

## 实现方式
dynamic workflow：BE(BE-A+BE-B) ‖ FE(FE-A..E)（Tier2 跨仓库并行，各自 worktree commit）+每仓库双 Sonnet review。主 session 收口 review + 门禁 + ndf-done + 部署 dev。#2 是 Bug-from-Customer（filenameFromURL repro test）。
