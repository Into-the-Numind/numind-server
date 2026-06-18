# Agent Output UX Followup-3 — Spec + Plan（S0-S3 合并）

> agent-output-ux-followup2 dev 验收后用户第 4 轮反馈（2026-06-18）。Standard。跨 numind-server + numind-web-v3。
> 设计经长对话 Q&A 全部敲定（见下决策）。dynamic workflow 实现：BE(numind-server) ‖ FE(numind-web-v3)，Tier 2 跨仓库并行，各自 worktree。

## 用户已拍板的设计决策

1. **HTML 从源头切断预览，只下载**。卡片点击行为：图片→modal 预览（保留）；HTML→提示"此格式暂不支持预览"（删 iframe）；docx/md/txt→点击开右侧编辑器（**方案 B，保留编辑能力**）；其它(pdf/xlsx/pptx/csv)→提示"不支持预览"（不变）。
2. **生成动效**：尾部脉动三点 → 前置转圈 spinner。
3. **eino 流式吐"写代码"增量** + 前端折叠代码框：
   - 只吐**工具调用参数（写代码/内容）那一段**，不吐思考段。
   - "大参数"判定 = **按工具名白名单**（不用长度阈值，避免闪烁/不可预测）。白名单 = 参数本身即代码/文档内容的生成类工具：`run_python` `create_html` `create_docx` `create_csv` `create_json` `create_text` `create_png_chart`。其它工具不吐、不显示代码框。
   - 前端代码框：**默认展开**；**生成成功即收起**；切换用**纯箭头图标**（不要"展开查看生成过程 ▸"文字）；**固定高度**滚动框（展开时固定高度，自动滚到底；未展开只显示"正在生成…"）。
4. **markdown→docx 确定性快路**（`create_docx`）：Agent 靠**工具选择**决定走哪条——普通文档（标题/段落/列表/表格/插图）用 `create_docx` 传 markdown（少吐 token=快）；复杂版式才用 run_python+docx-author。**工具描述必须写准确**让模型正确路由。

## 任务（BE = numind-server worktree；FE = numind-web-v3 worktree）

### 后端（一个 implementer 串行做 BE-1/BE-2/BE-3，文件互不重叠）

**BE-1（eino 流式吐工具参数增量）**
- 底层 arg 增量已可得：`internal/pkg/aiservice/adapter/stream.go:170-171`（`existing.Function.Arguments += tcd.Function.Arguments` 累积点）。
- 改法：①`ChatChunk`（adapter/types.go 或其定义处）加可选字段 `ToolCallArgsDelta *ToolCallArgsDelta{ToolCallID, FunctionName, ArgsDelta string}`，在 stream.go 拼接处**额外**填充（不破坏 IsFinal 给完整 ToolCall 的执行契约）；②`internal/numind/biz/agent/stream/events.go` 加 `EventToolCallArgsDelta` 事件类型 + payload `{tool_call_id, function_name, args_delta}`；③`internal/numind/biz/agent/runner_stream.go`（consumeEinoStream ~262-283）在收到 ChatChunk.ToolCallArgsDelta 时 `emit(EventToolCallArgsDelta, ...)`。
- **后端白名单门禁**：只对生成类工具（`run_python`/`create_html`/`create_docx`/`create_csv`/`create_json`/`create_text`/`create_png_chart`）emit arg-delta；其它工具不 emit。门禁按 FunctionName 判断（首个 chunk 带 name）。定义一个 `isCodeStreamingTool(name) bool` allowlist。
- 验证：单测——给定生成类工具的流式 chunk 序列，emit 出 N 个 args_delta 事件且拼接=完整参数；给定 web_search，不 emit。执行契约不变（IsFinal 仍给完整 ToolCall）。

**BE-2（create_docx —— markdown→docx 确定性快路）**
- 新工具 `internal/numind/biz/agent/tool_create_docx.go`：`Name()="create_docx"`，InputSchema `{markdown:string(required), filename?:string, input_files?:[]string(COS URLs for images)}`。
- **机制（已定调）**：复用 run_python 的沙箱路径——不是让 LLM 写代码，而是把一个**固定的、版本控制的 `md_to_docx.py` 脚本**（用 `embed` 或 Go 字符串常量内嵌）+ 用户传的 markdown 写进沙箱，exec `python3`，收集 `/workdir/output/*.docx` → `uploadGeneratedFile`。复用 tool_run_python.go 的沙箱 session 获取 / `ExecMkdir` / `writeFileToSandbox` / 输出收集逻辑（抽公共 helper 或直接复用）。`input_files`（图片 COS URL）下载到 `/workdir/input/` 供脚本嵌图，逻辑同 run_python。
- **固定脚本 `md_to_docx.py`**（我们写死、测试过）：只用 `python-docx`（沙箱已装 `python-docx>=1.1`+`pillow`），解析 markdown → 标题(#/##/###)→add_heading、段落→add_paragraph、有序/无序列表→List Number/List Bullet、表格(`|...|`)→add_table、图片`![](path)`→add_picture(从 /workdir/input 读)。从约定路径读 markdown，写 `/workdir/output/<filename>.docx`。脚本要健壮（容错坏 markdown 不崩）。
- **工具描述（务必准确，决定 Agent 路由）**：英文描述明确——"Generate a .docx Word document from **Markdown** content. Use this for **standard documents** (headings, paragraphs, lists, tables, inline images). Faster and more reliable than writing python-docx code. For **complex custom layouts / precise styling**, use run_python with the docx-author skill instead."
- **system prompt 引导**：在 `output_tools_priority_prompt.go`（或 runner 的 assembleSystemPrompt 工具段）加一句引导：普通文档优先 `create_docx` 传 markdown；仅特殊版式用 run_python+docx-author。
- 注册：`factory_platform.go` 的 LoadTools 加 `&createDocxTool{}` + 元数据（参考 create_csv 注册）。
- 验证：Go 单测覆盖输入校验（markdown 空→友好错误、filename 清洗、强制 .docx 扩展名）。`md_to_docx.py` 脚本逻辑单独验证（本地有 python-docx 则跑一遍样例 markdown 断言生成 .docx 非空且可打开；无则文档化手测步骤）。go build + go test + task lint 通过。沙箱端到端 docx 生成 S5 dev 验证。

**BE-3（HTML 转下载的后端配套）**
- HTML 不再 iframe 预览 → 应以**附件下载**而非 inline 呈现。`internal/numind/biz/agent/cos_resign.go`：从 `cosInlineHTMLExts`/`cosIsInlineRenderName` 移除 `.html`/`.htm`（让 HTML 签名走 attachment disposition=下载）。
- `tool_create_helpers.go` `uploadGeneratedFile`：确认 text/html 不再走 inline 签名（与 cos_resign 两端对齐）。
- 更新 followup2 留下的相关测试（若有断言 .html 为 inline 的回归测试，改为断言 attachment/download）。
- 验证：Go 单测——html object key 经 cos_resign 得到 attachment（下载）签名；go build + task lint。

### 前端（一个 implementer 串行做 FE-1/FE-2/FE-3；FE-1+FE-3 都改 AgentMessageItem.vue 故必须同一 agent）

**FE-1（生成动效：尾部点→前置 spinner）**
- `AgentMessageItem.vue` 现 `<span class="generation-stall">正在生成<span class="gen-dots">...` → 改为**前置转圈 spinner + "正在生成…"**。删 `.gen-dots` 脉动点，加一个 CSS spinner（reduced-motion 退化为静态点）。

**FE-2（HTML 只下载 + 方案 B 保留 docx 编辑）**
- `AgentArtifactItem.vue` 重写 onCardClick：`isHtml` 分支从 `openHtmlPreview()` 改为 `flashHint('此格式暂不支持预览')`；**删除** HTML iframe 预览相关代码（showHtmlPreview / iframeLoading / onIframeLoad / iframe 模板）。
- 保留：`canEdit`(isDocumentSystemEnabled && isEditable, docx/md/txt) → `openEditor()`（点击开右侧编辑器）；图片→modal 预览；其它→`flashHint('此格式暂不支持预览')`。
- 卡片仍只一个【下载】按钮（图标）。
- 更新/删除 followup2 的 `sandbox==='allow-scripts'` 精确断言测试（iframe 已移除）。

**FE-3（eino 流式写代码框）**
- `types/agent*.ts` 加事件类型 `tool_call_args_delta` + payload `{tool_call_id, function_name, args_delta}`。
- `stores/agentChat.ts` applyStreamEvent 加 case：按 tool_call_id 累积 args_delta 到当前流式工具调用（白名单工具才会收到，因后端已门禁）。状态供 AgentMessageItem 读。
- `AgentMessageItem.vue`：在"正在生成…"（FE-1 的 spinner 区）**下方**加折叠代码框组件——**默认展开**、固定高度（如 160px）等宽滚动框、流入自动滚到底；**纯箭头图标**切换折叠/展开；**生成成功（流式结束/工具完成）即收起**（或消失）。与 FE-1 协调同一区域。

## S5 验证策略（rule 10）
- **方式**：BE Go 单测（BE-1 arg-delta emit + 白名单门禁；BE-2 输入校验 + md_to_docx.py 脚本样例；BE-3 cos_resign attachment）+ go build/vet + task lint。FE vitest（FE-3 store args 累积；FE-2 onCardClick 分支）+ type-check + eslint。
- **理由**：纯逻辑/渲染改动，无支付/权限高风险，Go 单测 + vitest 覆盖逻辑足够；视觉（spinner/折叠框/HTML 不预览/docx 编辑入口）+ 沙箱端到端 docx 生成 → S6 dev 后用户亲手取证（gstack /qa 或浏览器走查）。
- **关键路径（S6 dev 取证）**：①生成 docx 时看到 spinner + 默认展开的代码框、成功后收起；②HTML 卡片点击提示不支持预览、下载可用；③docx 卡片点击开右侧编辑器；④create_docx 真生成可打开的 .docx；⑤图片卡片仍 modal 预览。
- **回归保护诚实声明**：BE/FE 单测留库做回归；视觉与沙箱端到端走 dev 手测（无持久化 E2E），未来改动需手动重测。

## 实现方式 / Tier
- dynamic workflow：BE-implementer(BE-1+BE-2+BE-3) ‖ FE-implementer(FE-1+FE-2+FE-3)，**Tier 2 跨仓库并行**（两 worktree 物理隔离，无文件交集）。各 implementer 在自己仓库内**串行**做完所有 task（避免 Tier 3）。
- 每仓库**双 Sonnet review**（spec-compliance + code-quality）。主 session 收口：核对 commit、triage P0/P1 修复、gate（reviewed==completed）、ndf-done（两仓库）、部署 dev。
- 非 Bug-from-Customer 强制 repro（这些是增强/移除，非在位修 bug）；按常规 TDD 写测试即可。
