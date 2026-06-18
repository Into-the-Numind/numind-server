# Agent Mode 输出与附件呈现 UX 修复 — 技术设计 Spec

> S2 设计文档。代码必须实现本 spec 的全部内容。覆盖 PRD（proposal.md §4）的全部用户故事与验收标准。
> 不涉及新 LLM 调用 / 新 DB schema / 新 HTTP 端点。涉及一个**持久化数据形态**变更（user turn 增加 `attachments` 键，向后兼容）。

---

## FIX A — 技能名显示（问题一）[前端 numind-web-v3]

### 现状
- 后端 `tool_call_start` 事件 `input_preview` 含 skill 的 `name`（`runner_stream.go:278-283`）；narration yaml 模板 `load_skill` = `正在加载技能：{{ .input.name }}` / 结果 `已加载技能：{{ .input.name }}`（`configs/tool-display.yaml`）。
- 前端 `src/stores/agentChat.ts`：
  - `tool_call_start`（~1114-1162）：`actionLabels` 含 `use_skill` 但**漏 `load_skill`** → 落默认 `正在调用工具 load_skill...`；`streamingToolUseLabel()`（~119）无 `load_skill` 分支。
  - `tool_call_result`（~1212）：`TOOL_RESULT_LABELS.load_skill = '已加载技能'`（无名）。
- `AgentToolCallItem.vue` 的 `label` 取 active→`events[0].message`、done→`latest.message`。

### 设计
1. `streamingToolUseLabel()` 增加 `load_skill` 分支：从 `inputPreview.name`（string）取技能名 → 返回 `加载技能：${name}`；name 缺失时返回传入 base。
2. `tool_call_start` 的 `actionLabels` 增加 `load_skill: '正在加载技能...'`（作为 name 缺失时的 base 兜底）。
3. **完成态也要带名**：`tool_call_start` 处理时把解析出的技能名存到该 tool_call 的 aggregate（新增可选字段 `skill_name?: string` on `ToolCallAggregate`，或复用已有结构）。`tool_call_result` 处理 `load_skill` 时，若 aggregate 有 `skill_name`，结果事件 message 用 `已加载技能：${skill_name}`；否则回退现有静态 `已加载技能`。
   - 验证实现细节时确认 `load_skill` 工具输入参数名（`name` vs `skill_name`）：以后端 tool-display.yaml `{{ .input.name }}` 为准 → `input_preview.name`。实现时 grep `tool_load_skill` / `invoke_skill` 工具 schema 核对，二者若不同则按实际字段读。
4. 不破坏 `invoke_skill`（读 `input_preview.skill_name`）与 `use_skill` 现有分支。

### 验收
- 流式：加载中显示 `加载技能：<名>`，完成显示 `已加载技能：<名>`。
- reload：走 narration/persisted 路径，名仍在（本就正确，不回退）。

---

## FIX B — 不泄露系统提示 + 附件 chip（问题二）[后端 numind-server]

### 现状
- `buildAgentInput(message, urls)`（`student_run_lifecycle.go:413`）= `message + "\n\n【系统提示】用户上传了以下附件…file_read…\n- <url>…"`。
- 结果赋给 `runReq.Input`（Create :352 / RunStream :617）。
- `req.Input` 既喂 LLM（`buildEinoMessages` :1615，**正确**），又被持久化为 user turn：
  - `finalizeRun`（runner.go:1324 `userInput := req.Input`）→ `buildTranscriptTurns(userInput,…)`。
  - `persistYieldTranscript(ctx, run.ID, req.Input)`（runner.go:1181，yield/ask_user_question 路径）。
- `transformMessages` user 分支（`student_query.go:611-613`）`msg.Text = content` 原样渲染。
- 前端 `UserMessage.attachments?: {url,filename}[]` 已有、`AgentMessageItem.vue:143` 已渲染 chip；但后端从不回填。
- `agentMessage` 结构体（`student_query.go:456`）**无** attachments 字段。
- read 路径 `resignCOSLinks` 只重签 Markdown（assistant/final），不签 user 文本/附件。

### 设计
1. **RunRequest 增字段**（runner.go:40 struct）：
   - `DisplayInput *string` — 用于持久化/展示的用户原文。`nil` = 未提供（回退 `Input`，兼容 resume/test 路径）；非 nil（含空串）= 用它（支持纯附件无文字发送，Message="" 也要落空文本而非回退到含系统提示的 Input）。
   - `DisplayAttachments []displayAttachment` — `{URL, Filename string}`，持久化到 user turn 供 chip。
2. **统一取数 helper**：`func (r RunRequest) displayUserText() string { if r.DisplayInput != nil { return *r.DisplayInput }; return r.Input }`。
3. **持久化站点全改用 displayUserText()**：
   - `finalizeRun`：`userInput := req.displayUserText()`（替换 :1324）。
   - `persistYieldTranscript`：传 `req.displayUserText()`（替换 :1181 的 req.Input）。
   - LLM 侧（buildEinoMessages、memory userMsg :1440/:1461、test echo :949/:1012/:1018）**保持 req.Input 不变**（引导 file_read 指令仍进 LLM）。
4. **user turn 带 attachments**：`buildTranscriptTurns` 增加 attachments 入参（或 finalizeRun 组装 turn 后补 `turn["attachments"]`）。落库形如 `{"role":"user","content":"<原文>","attachments":[{"url":..,"filename":..}]}`。无附件则不写该键（omitempty 风味，保持旧 turn 兼容）。
   - 注意 `buildTranscriptTurns` 可能在多分支构造 user turn（含 collapsed shape、`turns = [{role:user,content:userInput}]` fallback :1390），需保证带附件路径覆盖正常完成的主路径；fallback/异常路径无附件可接受。
5. **lifecycle 赋值**（Create :352 + RunStream :617）：
   - `runReq.DisplayInput = &req.Message`（原始人类文本）。
   - `runReq.DisplayAttachments = …` 由两条附件路径解析：
     - AttachmentIDs 路径：从 `atts []*model.AgentAttachment` 取 `{cos_url, filename}`。
     - AttachmentURLs 路径：从 url 取 `{url, filenameFromURL(url)}`。
     - 都为空则 nil。
6. **读路径回填 + 重签**（student_query.go）：
   - `agentMessage` 增 `Attachments []messageAttachment` json `attachments,omitempty`；`messageAttachment{URL,Filename string}` json `url`/`filename`（匹配前端）。
   - `transformMessages` user 分支：解析 `turn["attachments"]`（[]any of map）→ `msg.Attachments`，每个 `url` 经 `resignCOSLinks(ctx, url)` 重签（复用现成函数，对单个 URL 子串替换也生效；非 COS/失败原样保留）。
   - `msg.Text = content`（现 content 已是原文，不再含系统提示）。

### 不回退保证
- LLM 仍收到 `buildAgentInput` 的完整提示（含 file_read 指令）：`buildEinoMessages` 用 `req.Input` 不动。
- 纯附件无文字：`DisplayInput = &""` → user 气泡空文本 + 附件 chip。

### 验收
- 上传附件发送 → 气泡无系统提示文字，显示原文 + 附件 chip；reload 同样。
- LLM 行为不变（仍会调 file_read 读附件）。

---

## FIX C — 工具进行中动效（问题四）[前端 numind-web-v3]

### 现状
`AgentToolCallItem.vue`：active 态 `label` 取 `events[0].message`（首事件），后续 `tool_call_progress` 的新 message 不显示；动效只有 `Loader2` spinner 旋转。

### 设计
1. **label 显示最新进度**：active 时改为优先取 `latest?.message`（`events[events.length-1]`）：
   ```
   const base = isActive.value
     ? (latest?.message || first?.message || props.group.tool_name)
     : (latest?.message || first?.message || props.group.tool_name)
   ```
   - 'use' 态只有一个事件 → latest===first，文案不变（含技能名）。
   - 'progress' 态 → 显示最新 progress 文案。
2. **流动点动效**：active 时在文案后渲染一个 `<span class="tl-dots" aria-hidden>` 三点流动（typing-indicator 风），CSS keyframes 让三点依次淡入/弹跳。
   - 仅 `isActive` 时渲染；done/error 不渲染。
   - `@media (prefers-reduced-motion: reduce)` → 动画关闭（点静态显示或隐藏），与现有 `.tl-spin` 同处理。
3. 保留 spinner 作为状态图标；动效是文案旁的附加可感知信号（满足"明显进行中"且非进度条）。

### 验收
- 工具运行中：单行有 spinner + 文案 + 流动点，可感知"在跑"。
- 有 progress 事件时显示最新文案。reduced-motion 下不动。

---

## FIX D — 所有生成文件统一走卡片（问题五）[后端 + 前端]

### 现状
- 后端 `imageCollector`（`image_collector.go`）只收**图片**；`adapter_full_to_eino.go:258` 仅 `mime image/*` 才 `add`。`finalizeRun`（runner.go:1340）/`runner_stream.go:373` 用 `drainMarkdownExcluding` 把图片嵌入最终回答。
- `artifactFromToolResult(output)` 已能解析任意文件 url/filename/mime；`mimeFromArtifact` 已认 html→text/html。
- 文档（docx/html/pdf）未被收集/嵌入 → 只靠模型自己写链接；行内链接 → 前端不成卡片（`standaloneArtifactOf` 只认独占行）。
- 前端 `agentArtifacts.ts`：`DOC_EXTS` 含 docx/doc/xlsx/xls/pptx/ppt/pdf/csv，**缺 html**；`MIME_BY_EXT` 缺 html。
- HTML 卡片（`AgentArtifactItem.vue` isHtml 分支）已实现（sandbox="" iframe 预览 + 下载）；但 html 走 attachment 签名（`uploadGeneratedFile` 非图片→`GenerateSignedDownloadURL`；`cos_resign` `cosIsImageName` 不含 html）→ iframe 会被强制下载。

### 设计（后端）
1. **泛化 collector**：`imageCollector` → `artifactCollector`（重命名 type / ctxKey / `withArtifactCollector` / `artifactCollectorFrom`，更新全部引用：runner.go:545+1340、runner_runstream.go:568、runner_stream.go:373、adapter_full_to_eino.go:258）。
   - entry 增 `kind`（image|doc）+ `objectKey`（= `imageObjectKey(url)`，签名无关路径，用于 dedup/strip）。
   - `add(url, filename, mime)`：mime `image/*` → kind=image，md=`![alt](url)`；否则 kind=doc，md=`[filename](url)`（filename 兜底"生成的文件"）。dedup by url（沿用 seen map）。
2. **call site**（adapter_full_to_eino.go:258）：去掉 `strings.HasPrefix(mime,"image/")` 过滤 → 任意 `url != ""` 都 `add(url, fname, mime)`。
3. **嵌入逻辑收敛为单一 helper**，两处 embed 站点（finalizeRun:1340 + runner_stream.go:373）都调用，保持 drain-once：
   - `func (c *artifactCollector) finalizeInto(content string) string`：
     a. **图片**：沿用 drainExcluding 语义——模型已在 content 写了该图片 objectKey 的则不重复，否则 append `![]`（保持图片就地展示行为不回退）。
     b. **文档**：对每个 doc entry，从 content 中**剥离**任何引用其 objectKey 的 markdown 节点（`!?\[[^\]]*\]\([^)]*<objectKey 子串>[^)]*\)` 正则删除），然后把所有 doc 的 `[filename](url)` 作为**独占行** append（彼此 `\n\n` 分隔）。
     c. 返回：`strip 后的 content` + 图片块 + 文档块（空块跳过，注意行间 `\n\n`）。clear 整个 collector。
   - 保证：每个生成文件最终回答中恰好一个独占行 → 前端 `splitIntoSegments` 转成一个卡片；无裸行内链接、无重复。图片行为不变。
4. **HTML inline 签名**：
   - `uploadGeneratedFile`（tool_create_helpers.go:161-171）：图片**或** html/htm → `GenerateSignedURL`（inline）；其余 → `GenerateSignedDownloadURL`。
   - `cos_resign.go`：`cosIsImageName` → 泛化为 `cosIsInlineRenderName`（或新增 inline 集），inline 集 = 现图片扩展名 + `.html`/`.htm`。read 路径据此对 html 走 `signImage`（inline）。
   - 安全：html inline 仅经 sandbox="" iframe 渲染（已有威胁模型注释），不变。

### 设计（前端 numind-web-v3）
5. `agentArtifacts.ts`：`MIME_BY_EXT` 增 `html: 'text/html'`（可加 `htm`）；`DOC_EXTS` 增 `'html'`（+`'htm'`）。→ COS agent-outputs 的 `[x.html](url)` 独占行被 `artifactRefOf` 识别为 mime=text/html → 渲染 `AgentArtifactItem` isHtml 卡片（预览 iframe + 下载）。
6. 其余前端零改动（卡片/预览/编辑器/displayName/重签消费 已就绪，自动受益）。

### 回归保证
- 第三方非 COS-agent-outputs 链接：`isCosArtifactUrl` 仍 false → 不成卡片。
- 图片：保持 inline/网格 + drainExcluding 行为不变。
- docx/pdf 卡片：已工作，不回退（download 签名不变）。

### 验收
- docx → 卡片（下载 + 打开编辑器）；html → 卡片（预览能渲染 + 下载）；pdf → 卡片。
- 每个生成文件只一个卡片；无裸链接/重复。

---

## 数据契约变更（持久化）
- `agent_run.messages[].` user turn 新增可选键 `attachments: [{url, filename}]`。向后兼容：旧 turn 无此键，transformMessages 读不到则 msg.Attachments 为空（不渲染 chip），不报错。
- `agentMessage` JSON 新增 `attachments` 字段（omitempty）。前端 `UserMessage.attachments` 已存在，无需改类型。

## 验证策略（详见 S3 plan 的 S5 task）
- 后端（问题二、五，Bug-from-Customer）：Go 单测复现 + TDD（断言持久化 user turn 不含系统提示文字、含 attachments；断言生成 doc 被嵌入最终回答为独占行、行内引用被剥离、图片行为不变）。
- 前端（问题一、四）：gstack `/qa` 浏览器验证（本地起服务，跑 agent 触发 skill + 长工具 + 生成 docx/html，截图核对技能名 / 流动点 / 卡片 + 预览）。
- 高风险？否（无支付/权限）。问题二/五因是正确性 bug 仍写 Go 回归测试永久留存。
