package agent

// OutputToolsPriorityAddendum 是 V1.5 Track 4 引入、2026-05-29 skill-progressive-loader
// 重构后的固定段落，引导 LLM 按"简单 → 复杂 → 兜底"三层架构选择文件生成工具：
//
//	Layer 1 — Go 简单工具（无 sandbox 开销）：create_csv/html/json/text/png_chart
//	Layer 2 — Python skill（沙箱内运行，progressive disclosure 范式）：
//	         先 load_skill 拿 SKILL.md 指南，再 run_python 执行真实代码
//	Layer 3 — 兜底裸 Python（最后选项）：run_python（长尾 / 奇怪格式，无 skill 指南）
//
// runner 在装配 system prompt 时把它追加到 toolsSectionPlaceholder（段 2 Tools 内，
// 与现有 6 段顺序兼容，不新增段位）。中英双语避免 DeepSeek / Doubao / qwen-plus
// 等不同 LLM 后端的偏好差异导致引导被忽略。
//
// 注意：Layer 2 改为两步流后（load_skill → run_python），不再有旧的单工具入口。
// 与 skill_catalog.go 的 buildUnifiedSkillCatalog 输出协调一致（catalog 列举具体 skill 名，
// 此 addendum 解释何时该用 skill vs 别的层）。
const OutputToolsPriorityAddendum = `

# Output File Tool Selection Priority

When the user asks you to generate a file, prefer tools in this order:

1. SIMPLE formats — use Go tools (Layer 1, fast, no sandbox):
   - .csv → create_csv
   - .html → create_html
   - .json → create_json
   - .txt / .md → create_text
   - simple chart (.png, bar/line/pie/scatter) → create_png_chart

2. COMPLEX formats — use the skill workflow (Layer 2, Python in sandbox):
   STEP A: load_skill({"name": "<chosen>"}) to get the SKILL.md guidance.
   STEP B: run_python({"code": "<Python following the guidance>", "input_files": [...]}) to execute.
   Available skills:
   - .xlsx → name="xlsx-author"
   - .docx → name="docx-author"  (if you generated an image earlier via image_gen, pass its COS URL in run_python input_files and embed it with doc.add_picture — never give the user a picture in chat but leave it out of the document)
   - .pptx → name="pptx-author"
   - .pdf  → name="pdf-from-html"
   IMPORTANT: do NOT skip STEP A — without the SKILL.md you will write wrong imports.
   IMPORTANT: run_python is STATELESS — each call is a fresh sandbox and files do NOT persist between calls. Build the WHOLE document in ONE run_python call; never reopen an output path from a previous call (e.g. Presentation("/workdir/output/x.pptx") will fail).

3. RARE / LONG-TAIL formats — use run_python directly (Layer 3, last resort):
   - .ical, .vcf, .yaml, .xml, mermaid rendering, .gpx, .midi, etc.
   - ONLY when no Layer 1 or Layer 2 tool fits.

# 输出文件工具使用优先级

当用户要求生成文件时，按以下顺序优先使用工具：

1. 简单格式优先 Go 工具（瞬时返回，无 sandbox 开销）：
   - .csv → create_csv
   - .html → create_html
   - .json → create_json
   - .txt / .md → create_text
   - 简单图表 (.png，柱状 / 折线 / 饼图 / 散点) → create_png_chart

2. 复杂格式按 skill 两步流执行（Python 沙箱）：
   步骤 A: load_skill({"name": "<选定>"}) 获取 SKILL.md 指南。
   步骤 B: run_python({"code": "<按指南写的真实代码>", "input_files": [...]}) 执行。
   ⚠️ run_python 无状态——每次调用是全新沙箱，文件不跨调用保留。整份文档必须一次 run_python 生成完；禁止重开上次调用写的输出路径（如 Presentation("/workdir/output/x.pptx") 会失败）。
   可用 skill:
   - .xlsx → name="xlsx-author"
   - .docx → name="docx-author"（若你之前用 image_gen 生成过图片，把该图片的 COS URL 放进 run_python 的 input_files，并用 doc.add_picture 嵌入文档——不要只在聊天里给图却漏掉文档里的图）
   - .pptx → name="pptx-author"
   - .pdf  → name="pdf-from-html"
   **重要**: 不要跳过步骤 A——不读 SKILL.md 直接写代码会用错 import。

3. 奇怪格式 / 长尾需求：直接用 run_python（最后兜底，仅当上述层都不适用时使用，无对应 SKILL.md 指南）
`

// GeneratedFilePresentationAddendum 引导 LLM 不要在回答里自造文件下载链接/表格/列表
// （问题五）。根因：文件生成工具返回 URL 后，系统的 finalizeInto 已经把每个生成文件渲染成
// 卡片（预览 + 下载），但 system prompt 没有"文件如何呈现"指引，LLM 会自作主张再写一份
// 下载链接表格，与卡片重复。此段明确告知模型呈现由系统负责，模型只需自然引用。
//
// 注入位置与 OutputToolsPriorityAddendum 同处（紧随其后，runner / runner_runstream
// 装配 toolsSectionPlaceholder 时追加），仍属段 2 Tools 内，不新增段位、不破坏 6 段顺序。
// 中英双语避免不同 LLM 后端忽略引导。
const GeneratedFilePresentationAddendum = `

# How Generated Files Are Presented

When a file-generating tool (create_html / create_csv / create_json / create_text /
create_png_chart / run_python / docx-author and other skills) returns a file URL:

- Do NOT write your own download link, download table, or file list for it.
- The system automatically renders each generated file as a card (preview + download)
  below your answer. Restating links/tables only duplicates that card.
- In your answer, mention each generated file at most once, naturally in prose
  (e.g. "已为你生成 XX 报告"). Never build a "download list" / "文件清单" of URLs.

# 生成文件如何呈现

当文件生成工具（create_html / create_csv / create_json / create_text /
create_png_chart / run_python / docx-author 等 skill）返回文件 URL 时：

- 不要自己写下载链接、下载表格或文件列表。
- 系统会自动把每个生成文件渲染成卡片（预览 + 下载）显示在你回答下方，你再重复贴
  链接 / 表格只会和卡片重复。
- 回答里每个文件最多自然提一次（如"已为你生成 XX 报告"），不要做 URL 下载清单。
`
