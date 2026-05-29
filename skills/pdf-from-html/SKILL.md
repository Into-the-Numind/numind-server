# pdf-from-html

Generate .pdf files by rendering HTML/CSS via `weasyprint` inside `run_python`.

## How you use this

You are the agent. After `read_skill({"skill_name":"pdf-from-html"})` returns
this guide, write Python following one of the templates below and run it via
`run_python({"code": "...", "input_files": [...optional logo/image URLs...]})`.
`run_python` collects everything in `/output/` and gives you back a COS URL —
embed that URL in your final answer to the user.

Pre-installed: `weasyprint>=60`, `pillow>=10`. Fonts: `Noto Sans CJK SC` for Chinese.

## Paths

- Input files → `/workspace/input/<filename>`
- Output → `/output/<your_filename>.pdf`

## Template 1 — Plain Chinese report

```python
from weasyprint import HTML
html = """
<html><head><meta charset="utf-8">
<style>
  body{font-family:"Noto Sans CJK SC";margin:36px;line-height:1.6}
  h1{color:#1E40AF}
  h2{border-bottom:2px solid #2563EB;padding-bottom:4px}
</style></head><body>
<h1>2026 Q3 业务回顾</h1>
<h2>一、概况</h2>
<p>本季度营收同比增长 28%，新增企业客户 12 家。</p>
<h2>二、亮点</h2>
<ul><li>产品新增 4 个核心功能</li><li>客户 NPS 升至 58</li></ul>
</body></html>
"""
HTML(string=html).write_pdf("/output/report_demo.pdf")
```

## Template 2 — Cover page with logo

```python
from weasyprint import HTML
# user-uploaded logo at /workspace/input/logo.png (substitute the real filename)
html = """
<html><head><meta charset="utf-8">
<style>
  @page{size:A4;margin:0}
  body{font-family:"Noto Sans CJK SC";margin:0}
  .cover{height:100vh;display:flex;flex-direction:column;justify-content:center;align-items:center;background:linear-gradient(135deg,#1E40AF,#2563EB);color:#fff}
  .cover img{width:120px;margin-bottom:24px}
  .cover h1{font-size:48px;margin:0}
  .cover .sub{font-size:18px;margin-top:12px;opacity:.85}
</style></head><body>
<div class="cover">
  <img src="file:///workspace/input/logo.png" alt="logo"/>
  <h1>2026 Q3 业务方案</h1>
  <div class="sub">有数科技 · 2026-09-01</div>
</div>
</body></html>
"""
HTML(string=html).write_pdf("/output/cover_demo.pdf")
```

## Template 3 — Multi-page with page numbers

```python
from weasyprint import HTML
html = """
<html><head><meta charset="utf-8">
<style>
  @page{size:A4;margin:36px 36px 48px 36px;
        @bottom-center{content:counter(page) " / " counter(pages);font-size:10px;color:#666}}
  body{font-family:"Noto Sans CJK SC";line-height:1.6}
  h1{color:#1E40AF}
  .break{page-break-before:always}
</style></head><body>
<h1>章节 1：背景</h1><p>……（正文内容）</p>
<div class="break"></div>
<h1>章节 2：方案</h1><p>……（正文内容）</p>
<div class="break"></div>
<h1>章节 3：结论</h1><p>……（正文内容）</p>
</body></html>
"""
HTML(string=html).write_pdf("/output/paged_demo.pdf")
```

## Template 4 — Invoice with table

```python
from weasyprint import HTML
rows = [("A001", "AI 工作台年费", 1, 12000),
        ("A002", "技术支持", 1, 3000),
        ("A003", "培训服务", 2, 2000)]
trs = "".join(f"<tr><td>{c}</td><td>{n}</td><td>{q}</td><td>¥{p:,}</td></tr>" for c,n,q,p in rows)
total = sum(q*p for _,_,q,p in rows)
html = f"""
<html><head><meta charset="utf-8">
<style>
  body{{font-family:"Noto Sans CJK SC";margin:36px}}
  h1{{color:#1E40AF}}
  table{{width:100%;border-collapse:collapse;margin-top:16px}}
  th,td{{border:1px solid #ddd;padding:8px;text-align:left}}
  th{{background:#1E40AF;color:#fff}}
  .total{{text-align:right;font-size:18px;margin-top:12px;font-weight:bold}}
</style></head><body>
<h1>发票 · 2026-09-01</h1>
<p>客户：示例公司</p>
<table><tr><th>编号</th><th>项目</th><th>数量</th><th>金额</th></tr>{trs}</table>
<div class="total">合计：¥{total:,}</div>
</body></html>
"""
HTML(string=html).write_pdf("/output/invoice_demo.pdf")
```

## Tips

- Chinese: `font-family:"Noto Sans CJK SC"` in CSS.
- Local images: `file:///workspace/input/<name>`.
- `@page{size:A4;margin:36px;@bottom-center{content:counter(page)}}` for headers/footers.
- Always save under `/output/`.
