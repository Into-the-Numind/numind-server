# docx-author

Generate .docx Word files via `python-docx` inside `run_python`.

## How you use this

You are the agent. After `load_skill({"name":"docx-author"})` returns this
guide, write Python following one of the templates below and run it via
`run_python({"code": "...", "input_files": [...optional image URLs...]})`.
`run_python` collects everything in `/workdir/output/` and gives you back a COS URL —
embed that URL in your final answer to the user.

Pre-installed: `python-docx>=1.1`, `pillow>=10`.

## Paths

- Input files → `/workdir/input/<filename>`
- Output → `/workdir/output/<your_filename>.docx`

## Template 1 — Multi-level headings + body

```python
from docx import Document
from docx.shared import Pt
doc = Document()
doc.add_heading("2026 Q3 业务方案", level=0)
doc.add_heading("一、背景", level=1)
doc.add_paragraph("市场需求持续扩张，需要在 9 月前完成产品升级。")
doc.add_heading("二、目标", level=1)
doc.add_paragraph("Q3 收入提升 30%；新增 8 个企业客户。")
doc.add_heading("三、执行步骤", level=1)
for step in ["发布新版本", "客户回访", "复盘"]:
    p = doc.add_paragraph(step, style="List Number")
    p.runs[0].font.size = Pt(11)
doc.save("/workdir/output/headings_demo.docx")
```

## Template 2 — Inline image（强制嵌图规则）

**如果本次对话中你用 `image_gen` 生成过任何图片，写 docx 时你【必须】把那些图嵌进文档，绝不允许只在聊天里给用户一张图、却漏掉了文档里的图。** 具体两步缺一不可：

1. **把图的 COS URL 传给 `input_files`**：调用 `run_python` 时，在 `input_files` 数组里放上每张图的 COS URL。这些 URL 会被自动下载到 `/workdir/input/<filename>`（文件名取自 URL 末段），你的 Python 代码按这个本地路径读取。
2. **在正文用 `doc.add_picture(...)` 嵌入**：在封面、对应章节或图注位置，调用 `doc.add_picture('/workdir/input/<filename>', width=Inches(5.5))` 把图片插进文档。

下面是一段完整、可直接抄改的片段（封面嵌一张 image_gen 生成的图 + 章节内嵌另一张）：

```python
from docx import Document
from docx.shared import Inches

# 假设本次对话里 image_gen 生成了两张图，它们的 COS URL 已通过 run_python 的
# input_files=["https://cos.../cover.png", "https://cos.../chart.png"] 传入，
# 因此本地分别落在 /workdir/input/cover.png 和 /workdir/input/chart.png。
doc = Document()

# 封面：必须把生成的封面图嵌进来，而不是只在聊天里发给用户。
doc.add_heading("2026 Q3 业务方案", level=0)
doc.add_picture("/workdir/input/cover.png", width=Inches(5.5))
doc.add_paragraph("图 0：方案封面")

# 正文章节：同理嵌入章节配图。
doc.add_heading("一、市场分析", level=1)
doc.add_paragraph("下图为最新市场趋势：")
doc.add_picture("/workdir/input/chart.png", width=Inches(5.5))
doc.add_paragraph("图 1：市场趋势")

doc.save("/workdir/output/report_with_images.docx")
```

对应的 `run_python` 调用（注意 `input_files` 必须带上每张图的 COS URL）：

```json
{
  "code": "<上面的 Python 片段>",
  "input_files": ["https://cos.../cover.png", "https://cos.../chart.png"]
}
```

## Template 3 — Table

```python
from docx import Document
from docx.shared import Pt
doc = Document()
doc.add_heading("Top 5 项目", level=0)
table = doc.add_table(rows=6, cols=4); table.style = "Light Grid Accent 1"
hdr = table.rows[0].cells
for i, h in enumerate(["#", "Company", "Stage", "Amount"]):
    hdr[i].text = h
data = [(1, "Hark", "Series A", "$700M"),
        (2, "Nscale", "Equity", "$2B"),
        (3, "Zyphra", "Series B", "$500M"),
        (4, "Modal Labs", "Series C", "$355M"),
        (5, "Decart", "Strategic", "$300M")]
for r, row in enumerate(data, start=1):
    for c, v in enumerate(row):
        table.rows[r].cells[c].text = str(v)
doc.save("/workdir/output/table_demo.docx")
```

## Template 4 — Header + footer + sections

```python
from docx import Document
from docx.enum.section import WD_SECTION
doc = Document()
section = doc.sections[0]
header = section.header.paragraphs[0]
header.text = "Numind 业务报告"
footer = section.footer.paragraphs[0]
footer.text = "保密文档 · 仅供内部参阅"
doc.add_heading("封面页", level=0)
doc.add_paragraph("公司：有数科技")
doc.add_paragraph("日期：2026-09-01")
doc.add_section(WD_SECTION.NEW_PAGE)
doc.add_heading("正文章节", level=1)
doc.add_paragraph("第二节从新页开始，沿用顶部页眉与底部页脚。")
doc.save("/workdir/output/sections_demo.docx")
```

## Tips

- Chinese is native — set font via `run.font.name = "Microsoft YaHei"` or rely on default.
- Page break: `doc.add_page_break()`.
- Bold a run inline: `p.add_run("important").bold = True`.
- Bullet list: `doc.add_paragraph("...", style="List Bullet")`.
- Always save under `/workdir/output/` so `run_python` collects + uploads it.
