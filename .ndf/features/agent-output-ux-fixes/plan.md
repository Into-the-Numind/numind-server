# Agent Mode 输出与附件呈现 UX 修复 — 实施计划

> S3 plan。依据 spec.md。后端 task 在前端之前。每个代码 task 完成后跑 §6 双 Sonnet review。
> 依赖：T1→T2 串行（同改 runner.go，Tier 4）；T3/T4/T5 互相独立、与后端独立。T6 是文档（S5 策略，已含于本 plan）。

---

## T1 — 后端：分离持久化/展示文本，不泄露系统提示 + 附件 chip 回填（问题二）
**类型**：Bug-from-Customer（用户测试报告）→ 第一个 commit 必须是失败的复现测试。

**描述**：让持久化/展示的 user turn 用原始人类文本（不含 buildAgentInput 拼接的系统提示），并回填附件 chip；LLM 侧仍收完整提示（含 file_read 指令）不回退。

**改动文件**：
- `internal/numind/biz/agent/runner.go`：`RunRequest` 增 `DisplayInput *string` + `DisplayAttachments []displayAttachment{URL,Filename string}`；增 `func (r RunRequest) displayUserText() string`；`finalizeRun` 用 `displayUserText()` 替 `req.Input`（:1324）；`persistYieldTranscript` 调用处用 `displayUserText()`（:1181）；`buildTranscriptTurns` 增 attachments 入参，主成功路径 user turn 落 `attachments` 键。LLM 侧（buildEinoMessages/memory/test echo）保持 req.Input。
- `internal/numind/biz/agent/student_run_lifecycle.go`：Create（:352）+ RunStream（:617）设 `runReq.DisplayInput = &req.Message`；从两条附件路径（AttachmentIDs 的 atts 实体 / AttachmentURLs）组装 `DisplayAttachments`。
- `internal/numind/biz/agent/student_query.go`：`agentMessage` 增 `Attachments []messageAttachment` json `attachments,omitempty`（`messageAttachment{URL,Filename}` json `url`/`filename`）；`transformMessages` user 分支解析 `turn["attachments"]`→`msg.Attachments`，每 url 经 `resignCOSLinks` 重签。
- 测试：`*_test.go`（repro-first）。

**验收条件**：
- 复现测试先失败后通过：构造带 AttachmentURLs 的 run → 完成 → 断言持久化 messages 的 user turn `content` **不含** `【系统提示】`/`file_read`，且 `attachments` 非空。
- 断言 `buildEinoMessages(req)` 的 user message 仍 == `req.Input`（含系统提示）— LLM 不回退。
- 纯附件无文字（Message=""）：user turn content 为空字符串、attachments 非空。
- `go test ./internal/numind/biz/agent/...` + `task lint` 绿。

---

## T2 — 后端：artifactCollector 泛化，所有生成文件嵌入最终回答 + HTML inline 签名（问题五后端）
**类型**：Bug-from-Customer → 第一个 commit 必须是失败的复现测试。
**依赖**：T1 之后（同改 runner.go，串行）。

**描述**：把只收图片的 imageCollector 泛化为收集所有生成文件；嵌入收敛为单一 helper；文档以独占行嵌入（剥行内重复）；HTML 走 inline 签名以支持 iframe 预览。

**改动文件**：
- `internal/numind/biz/agent/image_collector.go` → 重命名 `artifact_collector.go`：`imageCollector`→`artifactCollector`、`imageCollectorCtxKey`/`withImageCollector`/`imageCollectorFrom`→`artifact*`；entry 增 `kind`(image|doc)+`objectKey`；`add(url,filename,mime)` 按 mime 分类（image→`![alt](url)`，否则 doc→`[filename](url)`）；新增 `finalizeInto(content string) string`（图片沿用 drainExcluding 语义；文档剥离 content 中引用其 objectKey 的行内节点后 append 独占行；clear collector）。
- `internal/numind/biz/agent/adapter_full_to_eino.go`（:258）：去掉 `image/` 过滤，任意 `url!=""` 都 `add(url,fname,mime)`。
- `internal/numind/biz/agent/runner.go`（:545 with / :1340 embed）+ `runner_runstream.go`（:568）+ `runner_stream.go`（:373 embed）：改用 `artifactCollectorFrom`/`finalizeInto`。
- `internal/numind/biz/agent/tool_create_helpers.go`：`uploadGeneratedFile` 图片**或** html/htm → `GenerateSignedURL`（inline）；其余 download。
- `internal/numind/biz/agent/cos_resign.go`：`cosIsImageName`→`cosIsInlineRenderName`（inline 集加 `.html`/`.htm`）。
- 测试：`*_test.go`（repro-first）。

**验收条件**：
- 复现测试先失败后通过：模拟工具产出 docx → 断言最终回答 markdown 含独占行 `[<name>.docx](<cos agent-outputs url>)`；html 同理；模型把链接写成行内 `点击[报告](url)下载` → 断言行内被剥离、底部出现独占行（恰一个）。
- 图片行为不变：模型已写 `![](url)` → 不重复 append（drainExcluding 回归）。
- `cosIsInlineRenderName(".html")`==true、docx==false 单测。
- **（P1-2 横切补强）** html reload 读路径也要跑通：`resignCOSLinksWithHost` 对 `.html` objectKey 走 `signImage`（inline）而非 download → 断言重签后的 html url 是 inline 签名（无 attachment disposition）。这样最终回答 markdown 里的 html 卡片在 reload 后 iframe 预览仍能渲染（读路径 student_query.go:305 调 resignCOSLinks 已存在、本 task 改的是其内部 cosIsInlineRenderName 判定，无需改 T1 的 student_query 附件分支）。
- `go test ./internal/numind/biz/agent/...` + `task lint` 绿。

---

## T3 — 前端：技能名显示（问题一）
**描述**：load_skill 在过程时间线显示技能名（加载中 + 完成态）。

**改动文件**：
- `numind-web-v3/src/stores/agentChat.ts`：`streamingToolUseLabel()` 加 `load_skill` 分支读 `inputPreview.name`→`加载技能：${name}`；`actionLabels` 加 `load_skill: '正在加载技能...'`；tool_call_start 把技能名存 aggregate；tool_call_result 的 `load_skill` 用 `已加载技能：${name}`（有则用，无则回退静态）。
- `numind-web-v3/src/types/agent.ts`：`ToolCallAggregate` 增 `skill_name?: string`。
- 实现前 grep 确认 load_skill 工具输入字段名（`name` vs `skill_name`），以实际为准。

**验收条件**：
- 单测/vitest 覆盖加载态：`streamingToolUseLabel('load_skill',{name:'docx-author'})`==`加载技能：docx-author`。
- **（P2-1 补强）** vitest 覆盖完成态带名：模拟先收 `tool_call_start{tool_name:'load_skill', input_preview:{name:'docx-author'}}` 再收 `tool_call_result{tool_call_id:..}`，断言该 aggregate 末条 event message == `已加载技能：docx-author`（防只改 start 漏 result 态）。
- gstack /qa：跑会触发 skill 的 agent → 时间线显示带名（S5）。
- `npm run lint && npm run type-check` 绿。

---

## T4 — 前端：工具进行中动效（问题四）
**描述**：active 态显示最新 progress 文案 + 流动点动效（尊重 reduced-motion）。

**改动文件**：
- `numind-web-v3/src/components/agent/AgentToolCallItem.vue`：`label` active 时取 latest 事件 message；template 加 active-only 流动点 `<span class="tl-dots">`；CSS keyframes + `prefers-reduced-motion` 关闭。

**验收条件**：
- **（P2-2 补强）** vitest 覆盖 label 切换：模拟 aggregate 收多个 progress 事件后，active 态 `label` 取最新 message（非首事件）；done 态取 latest。低成本永久回归。
- gstack /qa：长工具（docx/html 生成）运行中可见 spinner + 流动点（S5）。
- reduced-motion 模拟下动画停。
- `npm run lint && npm run type-check` 绿。

---

## T5 — 前端：HTML 文件识别为卡片（问题五前端）
**描述**：补 html 到 extraction 层，使 COS html 独占行链接成卡片（卡片+预览已实现）。

**改动文件**：
- `numind-web-v3/src/utils/agentArtifacts.ts`：`MIME_BY_EXT` 加 `html:'text/html'`（+`htm`）；`DOC_EXTS` 加 `'html'`（+`'htm'`）。
- 测试：`agentArtifacts` 的 vitest spec 增 html 用例（pure function，低成本永久回归）。

**验收条件**：
- vitest：`extractArtifacts`/`standaloneArtifactOf` 对 COS agent-outputs 的 `[page.html](url)` 独占行 → 返回 mime=text/html 的 artifact；非 COS html 不提取。
- `npm run lint && npm run type-check` + vitest 绿。

---

## T6 — S5 验证策略（rule 10，文档 task，由 S3 reviewer 一并审）
**验证方式**：
- **后端 TDD（永久回归）**：T1、T2 是正确性 bug（Bug-from-Customer），用 Go 单测复现+回归，永久留存。`go test ./internal/numind/biz/agent/...` + `task lint`。
- **前端纯函数 vitest（永久回归）**：T5 的 html extraction（agentArtifacts 纯函数）写 vitest 用例。T3 的 `streamingToolUseLabel` 若可纯测亦覆盖。`npm run lint && npm run type-check` + vitest。
- **浏览器视觉/交互验证（gstack /qa）**：问题一（技能名）、问题四（流动点）、问题五端到端（docx/html/pdf 卡片 + html 预览渲染）。

**关于 S5 本地 vs dev 的诚实声明**：
- 可在本地自主跑的确定性门禁（go test/vet/lint + 前端 vitest/type-check/lint）**必须在 S5 本地真跑通**，覆盖两个后端正确性 bug（最高价值）+ html 提取逻辑。
- 浏览器视觉/交互验证需全栈（Go+MySQL+Redis+前端）运行，本环境自主拉起全栈不可靠；按团队既有实践，在 **S6 部署 dev 后用 gstack /qa 对 $DEV_SITE_URL 取证**（AI 取证，关键用户路径由用户亲手走/确认 per feedback_walkthrough_user_executes）。这是基于环境约束 + 用户"做到 dev"指令的诚实偏离，记入 manifest decision。
- **回归保护诚实声明**：后端两 bug + html 提取有永久自动回归（Go test + vitest）；技能名/流动点动效新增 vitest 纯逻辑覆盖（P2 补强后），但**视觉渲染结果**（iframe 真渲染、动效真动）无持久化自动回归（gstack /qa 一次性），符合 rule 10——它们非支付/权限高风险，Playwright 像素级断言低 ROI。
- **（P1-1 阻塞语义）** gstack /qa 在 dev 上是**交付阻塞门禁**（非 advisory）：下列 3 条关键用户路径逐一截图存档，**任意一条失败则阻塞 feature 交付声明，必须修复并重验**。本批次按用户指令止于 dev，不发 prod；该门禁即"做到 dev 可交付"的验收闸。

**关键用户路径（S5/S6 gstack 验）**：
1. 上传附件发消息 → 气泡无系统提示、有原文+附件 chip；reload 仍然。
2. 触发 skill 的 agent run → 时间线"加载技能：<名>"/"已加载技元：<名>"。
3. 生成 docx → 卡片（下载+打开编辑器）；生成 html → 卡片（预览渲染+下载）；运行中可见流动点。

---

## 依赖图（无环）
```
T1 ──→ T2        (后端, 串行: 同改 runner.go)
T3   T4   T5     (前端, 三者独立, 与后端独立)
T6 = 文档(S5策略)
```
执行顺序：T1 → T2（后端 worktree 串行）；T3/T4/T5（前端 worktree，可串可并）；最后 S5 跑确定性门禁 → S6 ndf-done+部署 dev → gstack /qa 取证。
