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
