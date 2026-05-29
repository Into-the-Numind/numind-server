# xlsx-author

Generate .xlsx Excel files via `openpyxl` inside `run_python`.

## How you use this

You are the agent. After `read_skill({"skill_name":"xlsx-author"})` returns this
guide, write Python following one of the templates below and run it via
`run_python({"code": "...", "input_files": [...optional CSV/source URLs...]})`.
`run_python` collects everything in `/workdir/output/` and gives you back a COS URL —
embed that URL in your final answer to the user.

Pre-installed: `openpyxl>=3.1`, `pandas>=2.0`, `matplotlib>=3.7`.

## Paths

- Input files → `/workdir/input/<filename>`
- Output → `/workdir/output/<your_filename>.xlsx`

## Template 1 — Single-sheet summary

```python
from openpyxl import Workbook
from openpyxl.styles import Font, PatternFill, Alignment
wb = Workbook(); ws = wb.active; ws.title = "Summary"
ws.append(["Metric", "Q1", "Q2", "Q3", "Q4"])
ws.append(["Revenue", 320, 410, 520, 640])
ws.append(["Cost",    180, 220, 260, 310])
ws.append(["Profit",  140, 190, 260, 330])
header_font = Font(bold=True, color="FFFFFF")
fill = PatternFill("solid", fgColor="1E40AF")
for cell in ws[1]:
    cell.font = header_font; cell.fill = fill
    cell.alignment = Alignment(horizontal="center")
for col in "ABCDE":
    ws.column_dimensions[col].width = 14
wb.save("/workdir/output/summary_demo.xlsx")
```

## Template 2 — Multi-sheet with an index

```python
from openpyxl import Workbook
wb = Workbook(); idx = wb.active; idx.title = "Index"
idx.append(["Sheet", "Description"])
sections = [("Sales", "Q1-Q4 sales by region"),
            ("Costs", "Q1-Q4 cost breakdown"),
            ("Trends", "YoY change %")]
for name, desc in sections:
    idx.append([name, desc])
    ws = wb.create_sheet(name)
    ws.append(["Header A", "Header B", "Header C"])
    ws.append([100, 200, 300])
wb.save("/workdir/output/multisheet_demo.xlsx")
```

## Template 3 — Data + embedded line chart

```python
from openpyxl import Workbook
from openpyxl.chart import LineChart, Reference
wb = Workbook(); ws = wb.active; ws.title = "Trend"
ws.append(["Quarter", "Revenue"])
for q, r in [("Q1", 320), ("Q2", 410), ("Q3", 520), ("Q4", 640)]:
    ws.append([q, r])
chart = LineChart(); chart.title = "Revenue trend"
chart.x_axis.title = "Quarter"; chart.y_axis.title = "10K CNY"
data = Reference(ws, min_col=2, min_row=1, max_row=5)
cats = Reference(ws, min_col=1, min_row=2, max_row=5)
chart.add_data(data, titles_from_data=True)
chart.set_categories(cats)
ws.add_chart(chart, "D2")
wb.save("/workdir/output/chart_demo.xlsx")
```

## Template 4 — Conditional formatting table

```python
from openpyxl import Workbook
from openpyxl.formatting.rule import ColorScaleRule
from openpyxl.styles import Font
wb = Workbook(); ws = wb.active; ws.title = "Heatmap"
ws.append(["Team", "Jan", "Feb", "Mar", "Apr"])
rows = [("North", 80, 65, 90, 95),
        ("South", 70, 88, 75, 60),
        ("East",  92, 95, 88, 90),
        ("West",  55, 60, 70, 78)]
for r in rows: ws.append(r)
for c in ws[1]: c.font = Font(bold=True)
rule = ColorScaleRule(
    start_type="min", start_color="F8696B",
    mid_type="percentile", mid_value=50, mid_color="FFEB84",
    end_type="max", end_color="63BE7B",
)
ws.conditional_formatting.add("B2:E5", rule)
wb.save("/workdir/output/heatmap_demo.xlsx")
```

## Tips

- For Chinese headers, openpyxl handles UTF-8 natively — no special config.
- Use `pandas.read_csv("/workdir/input/<name>.csv").to_excel(...)` to convert
  user-uploaded CSVs to formatted xlsx quickly.
- Number formats: `cell.number_format = "0.0%"` or `"#,##0"`.
- Freeze header row: `ws.freeze_panes = "A2"`.
- Always save under `/workdir/output/` so `run_python` collects + uploads it.
