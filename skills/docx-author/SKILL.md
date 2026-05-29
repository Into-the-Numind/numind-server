# docx-author

Generate .docx Word files via `python-docx` inside `run_python`.

## How you use this

You are the agent. After `read_skill({"skill_name":"docx-author"})` returns this
guide, write Python following one of the templates below and run it via
`run_python({"code": "...", "input_files": [...optional image URLs...]})`.
`run_python` collects everything in `/output/` and gives you back a COS URL —
embed that URL in your final answer to the user.

Pre-installed: `python-docx>=1.1`, `pillow>=10`.

## Paths

- Input files → `/workspace/input/<filename>`
- Output → `/output/<your_filename>.docx`

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
doc.save("/output/headings_demo.docx")
```

## Template 2 — Inline image

```python
from docx import Document
from docx.shared import Inches
doc = Document()
doc.add_heading("产品截图汇总", level=0)
doc.add_paragraph("以下截图来自最新构建：")
# image at /workspace/input/screenshot.png (user-uploaded; substitute filename)
doc.add_picture("/workspace/input/screenshot.png", width=Inches(5.5))
doc.add_paragraph("图 1：主页面")
doc.save("/output/image_demo.docx")
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
doc.save("/output/table_demo.docx")
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
doc.save("/output/sections_demo.docx")
```

## Tips

- Chinese is native — set font via `run.font.name = "Microsoft YaHei"` or rely on default.
- Page break: `doc.add_page_break()`.
- Bold a run inline: `p.add_run("important").bold = True`.
- Bullet list: `doc.add_paragraph("...", style="List Bullet")`.
- Always save under `/output/` so `run_python` collects + uploads it.
