package agent

// OutputToolsPriorityAddendum 是 V1.5 Track 4 引入的固定段落，
// 引导 LLM 按"简单 → 复杂 → 兜底"三层架构选择文件生成工具：
//
//	Layer 1 — Go 简单工具（无 sandbox 开销）：create_csv/html/json/text/png_chart
//	Layer 2 — Python skill（沙箱内运行）：invoke_skill(xlsx/docx/pptx/pdf-from-html)
//	Layer 3 — 兜底 Python（最后选项）：run_python（长尾 / 奇怪格式）
//
// runner 在装配 system prompt 时把它追加到 toolsSectionPlaceholder（段 2 Tools 内，
// 与现有 6 段顺序兼容，不新增段位）。中英双语避免 DeepSeek / Doubao / qwen-plus
// 等不同 LLM 后端的偏好差异导致引导被忽略。
const OutputToolsPriorityAddendum = `

# Output File Tool Selection Priority

When the user asks you to generate a file, prefer tools in this order:

1. SIMPLE formats — use Go tools (Layer 1, fast, no sandbox):
   - .csv → create_csv
   - .html → create_html
   - .json → create_json
   - .txt / .md → create_text
   - simple chart (.png, bar/line/pie/scatter) → create_png_chart

2. COMPLEX formats — use invoke_skill (Layer 2, Python in sandbox, stable output):
   - .xlsx → invoke_skill(skill_name="xlsx-author")
   - .docx → invoke_skill(skill_name="docx-author")
   - .pptx → invoke_skill(skill_name="pptx-author")
   - .pdf  → invoke_skill(skill_name="pdf-from-html")

3. RARE / LONG-TAIL formats — use run_python (Layer 3, last resort):
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

2. 复杂格式必须用专属 skill（产物质量稳定，Python 沙箱）：
   - .xlsx → invoke_skill(skill_name="xlsx-author")
   - .docx → invoke_skill(skill_name="docx-author")
   - .pptx → invoke_skill(skill_name="pptx-author")
   - .pdf  → invoke_skill(skill_name="pdf-from-html")

3. 奇怪格式 / 长尾需求：run_python（最后兜底，仅当上述工具都不适用时使用）
`
