# pptx-author

Generate .pptx PowerPoint files via `python-pptx` inside `run_python`.

## How you use this

You are the agent. After `read_skill({"skill_name":"pptx-author"})` returns this
guide, write Python following one of the templates below and run it via
`run_python({"code": "...", "input_files": [...optional logo URLs...]})`.
`run_python` collects everything in `/workdir/output/` and gives you back a COS URL —
embed that URL in your final answer to the user.

Pre-installed in the sandbox: `python-pptx>=1.0`, `pillow>=10`, `matplotlib>=3.7`.
Font `Noto Sans CJK SC` is available for Chinese.

## Paths

- Input files (uploaded attachments) → `/workdir/input/<filename>`
- Output → `/workdir/output/<your_filename>.pptx`

## Template 1 — Cover slide

```python
from pptx import Presentation
from pptx.util import Inches, Pt
from pptx.dml.color import RGBColor
prs = Presentation()
prs.slide_width = Inches(13.33); prs.slide_height = Inches(7.5)
s = prs.slides.add_slide(prs.slide_layouts[6])  # blank
tb = s.shapes.add_textbox(Inches(0.8), Inches(2.5), Inches(11.7), Inches(2))
p = tb.text_frame.paragraphs[0]
p.text = "2026 Q3 业务回顾"
p.font.size = Pt(54); p.font.bold = True
p.font.color.rgb = RGBColor(0x1E, 0x40, 0xAF)
sub = s.shapes.add_textbox(Inches(0.8), Inches(4.5), Inches(11.7), Inches(1))
sub.text_frame.text = "战略部 · 2026-09-01"
prs.save("/workdir/output/cover_demo.pptx")
```

## Template 2 — Title + bullets

```python
from pptx import Presentation
from pptx.util import Inches, Pt
prs = Presentation()
prs.slide_width = Inches(13.33); prs.slide_height = Inches(7.5)
s = prs.slides.add_slide(prs.slide_layouts[1])  # title + content
s.shapes.title.text = "本周三大趋势"
body = s.placeholders[1].text_frame
body.text = "具身智能进入量产期"
for bullet in ["算力基建持续加码", "端侧 AI 走向消费"]:
    p = body.add_paragraph(); p.text = bullet; p.font.size = Pt(24)
prs.save("/workdir/output/bullets_demo.pptx")
```

## Template 3 — Title + table

```python
from pptx import Presentation
from pptx.util import Inches, Pt
prs = Presentation()
prs.slide_width = Inches(13.33); prs.slide_height = Inches(7.5)
s = prs.slides.add_slide(prs.slide_layouts[5])  # title only
s.shapes.title.text = "Top 5 融资"
rows, cols = 6, 4
tbl = s.shapes.add_table(rows, cols,
    Inches(0.5), Inches(1.5), Inches(12.3), Inches(5)).table
hdr = ["#", "Company", "Round", "Amount"]
for c, h in enumerate(hdr):
    tbl.cell(0, c).text = h
data = [(1,"Hark","A","$700M"),(2,"Nscale","-","$2B"),
        (3,"Zyphra","B","$500M"),(4,"Modal","C","$355M"),(5,"Decart","-","$300M")]
for r, row in enumerate(data, start=1):
    for c, v in enumerate(row):
        tbl.cell(r, c).text = str(v)
prs.save("/workdir/output/table_demo.pptx")
```

## Template 4 — Title + bar chart (PNG via matplotlib)

```python
import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
from pptx import Presentation
from pptx.util import Inches
fig, ax = plt.subplots(figsize=(10, 5))
ax.bar(["Q1","Q2","Q3","Q4"], [320, 410, 520, 640], color="#2563EB")
ax.set_title("季度营收 (万元)")
plt.tight_layout(); plt.savefig("/tmp/chart.png", dpi=120); plt.close()
prs = Presentation()
prs.slide_width = Inches(13.33); prs.slide_height = Inches(7.5)
s = prs.slides.add_slide(prs.slide_layouts[5])
s.shapes.title.text = "季度营收趋势"
s.shapes.add_picture("/tmp/chart.png", Inches(1), Inches(1.5), Inches(11), Inches(5))
prs.save("/workdir/output/chart_demo.pptx")
```

## Tips

- Chinese text: set `font.name = "Noto Sans CJK SC"` on the run.
- Hex colors: `RGBColor.from_string("2563EB")` (no `#`).
- Multi-slide deck: build a list of slide dicts and reuse the templates above.
- User-uploaded logo: read from `/workdir/input/<name>` and pass to `add_picture`.
- Always save under `/workdir/output/` so `run_python` collects + uploads it.
