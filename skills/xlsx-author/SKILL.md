# xlsx-author Skill Guide

## 何时用

当需要生成 **Excel (.xlsx) 文件**时调用此 skill。典型场景：

- **报表/报告**：销售周报、月度 KPI 汇总、财务对账单
- **数据导出**：将数据库查询结果或 CSV 数据写成格式化表格供用户下载
- **多维度分析**：多 sheet 对比分析（基准 vs 对比），跨 sheet 汇总
- **含图表的文档**：在 Excel 内嵌入折线图、柱形图、饼图
- **模板填充**：基于预置样式模板批量生成相似结构文档

**不适用场景**：若只需纯 CSV，用 `create_csv` 工具；若需 PDF 输出，用 `pdf-from-html` skill；若需 PowerPoint，用 `pptx-author` skill。

---

## 文件路径约定

| 方向 | 路径 | 说明 |
|------|------|------|
| 输入文件（CSV/图片等）| `/workspace/input/` | agent 已将用户上传的文件放到此目录 |
| 输出文件 | `/output/` | 生成的 .xlsx 必须写入此目录；框架自动收集并上传 |

---

## 快速开始（简单示例）

```python
import openpyxl
from helpers import apply_header_style, auto_column_width, THEME_PRESETS

# 1. 创建 workbook
wb = openpyxl.Workbook()
wb.remove(wb.active)  # 移除默认 Sheet1

# 2. 写入数据
ws = wb.create_sheet(title="销售数据")
data = [
    ["月份", "销售额（元）", "完成率"],
    ["1月", 120000, 0.95],
    ["2月", 98000,  0.78],
    ["3月", 145000, 1.12],
]
for row in data:
    ws.append(row)

# 3. 应用样式
theme = THEME_PRESETS["blue"]
apply_header_style(ws, row=1,
    fill_color=theme["header_fill"],
    font_color=theme["font_color"])
auto_column_width(ws)
ws.freeze_panes = "A2"

# 4. 保存
wb.save("/output/销售月报.xlsx")
print("Done: /output/销售月报.xlsx")
```

---

## 复杂示例（多 sheet + 图表 + 样式）

```python
import openpyxl
from helpers import (
    apply_header_style, apply_alt_row_fill,
    auto_column_width, apply_number_format, THEME_PRESETS
)
from helpers.chart import embed_bar_chart, embed_line_chart

wb = openpyxl.Workbook()
wb.remove(wb.active)

theme = THEME_PRESETS["blue"]

# --- Sheet 1: 原始数据 ---
ws_data = wb.create_sheet(title="月度数据")
rows = [
    ["月份", "营收", "成本", "利润"],
    ["1月", 500000, 320000, 180000],
    ["2月", 480000, 310000, 170000],
    ["3月", 620000, 390000, 230000],
    ["4月", 710000, 430000, 280000],
]
for r in rows:
    ws_data.append(r)

apply_header_style(ws_data, row=1,
    fill_color=theme["header_fill"],
    font_color=theme["font_color"])
apply_alt_row_fill(ws_data, start_row=2,
    even_fill=theme["alt_row_fill"])
apply_number_format(ws_data, col_index=2, fmt="#,##0")
apply_number_format(ws_data, col_index=3, fmt="#,##0")
apply_number_format(ws_data, col_index=4, fmt="#,##0")
auto_column_width(ws_data)
ws_data.freeze_panes = "A2"

# --- Sheet 2: 图表 ---
ws_chart = wb.create_sheet(title="图表")

# 嵌入柱形图（数据来自"月度数据"sheet，A1:D5 范围）
embed_bar_chart(
    wb=wb,
    data_sheet_name="月度数据",
    data_range="A1:D5",
    title="月度收支对比",
    anchor_cell="A2",
    target_sheet_name="图表",
    series_labels=["营收", "成本", "利润"],
)

# 嵌入折线图（利润趋势）
embed_line_chart(
    wb=wb,
    data_sheet_name="月度数据",
    data_range="A1:A5,D1:D5",
    title="利润趋势",
    anchor_cell="P2",
    target_sheet_name="图表",
    smooth=True,
)

wb.save("/output/季度分析报告.xlsx")
print("Done: /output/季度分析报告.xlsx")
```

---

## CSV 输入读取

当用户上传了 CSV 文件（放在 `/workspace/input/`），可直接追加为 sheet：

```python
from helpers.csv_reader import csv_to_sheet

wb = openpyxl.Workbook()
wb.remove(wb.active)

# 自动读取 CSV，sheet 名取自文件名（最多 31 字符）
csv_to_sheet(wb, "/workspace/input/用户数据.csv", sheet_name="用户数据")

# 继续追加其他 sheet...
wb.save("/output/report.xlsx")
```

**注意**：CSV 文件需为 UTF-8 或 UTF-8-BOM 编码；GBK 文件请先通知用户转换。

---

## 使用预置模板

```python
from helpers.template import load_template

# 从 summary 模板加载并替换占位变量
wb = load_template("summary", {
    "title": "2026年Q2销售汇总",
    "period": "2026-04 ~ 2026-06",
    "generated_date": "2026-07-01",
    "summary_text": "本季度营收同比增长 23%，超额完成目标。",
})

# 在加载的 wb 上继续添加 sheet
ws = wb.create_sheet(title="明细数据")
# ... 写入数据 ...

wb.save("/output/Q2汇总报告.xlsx")
```

可用模板：`summary`（数据汇总）、`daily-report`（日报/周报）、`comparison`（对比分析）。

---

## matplotlib 图表嵌入（WPS/LibreOffice 兼容方案）

当 openpyxl 原生图表在 WPS 显示异常时，改用 matplotlib 渲染为 PNG 后嵌入：

```python
import matplotlib
matplotlib.rcParams['font.family'] = ['WenQuanYi Zen Hei', 'DejaVu Sans']
import matplotlib.pyplot as plt
from helpers.chart import chart_from_matplotlib

fig, ax = plt.subplots(figsize=(8, 5))
months = ["1月", "2月", "3月", "4月"]
revenue = [500000, 480000, 620000, 710000]
ax.bar(months, revenue, color="#2563EB")
ax.set_title("月度营收（元）", fontsize=12)
ax.yaxis.set_major_formatter(plt.FuncFormatter(lambda x, _: f"{x/10000:.0f}万"))

# 嵌入到 ws 的 F2 位置
chart_from_matplotlib(fig, ws_chart, anchor_cell="F2", dpi=150)
plt.close(fig)
```

---

## 图表类型参考

| chart_type | 函数 | 适用场景 |
|-----------|------|---------|
| `bar` | `embed_bar_chart` | 分类对比、周期比较 |
| `line` | `embed_line_chart` | 趋势分析、时间序列 |
| `pie` | `embed_pie_chart` | 占比分析（类别 ≤ 7） |
| matplotlib | `chart_from_matplotlib` | 复杂图表、WPS 兼容场景 |

`scatter` 图在 V1.5 返回 warning，暂未实现（V1.6 补齐）。

---

## 样式主题参考

| 主题名 | 标题行色 | 交替行色 | 适用场景 |
|-------|---------|---------|---------|
| `default` | `#1F2937`（深灰）| `#F9FAFB` | 通用 |
| `blue` | `#2563EB`（品牌蓝）| `#EFF6FF` | 正式报告 |
| `green` | `#16A34A`（成功绿）| `#F0FDF4` | 销售/增长指标 |
| `monochrome` | `#374151`（中灰）| `#F3F4F6` | 打印友好 |

覆盖单个颜色：`apply_header_style(ws, fill_color="FF6B35")`（hex 不带 `#`）。

---

## 常见坑 & 解决方案

### 坑 1：中文字体显示

openpyxl 只设置字体名称，不嵌入字体文件。在 sandbox 内设置 `font_name="微软雅黑"` 不影响写入，Windows 上打开效果最佳；macOS 用户可能看到系统默认中文字体（苹方），视觉效果略有差异但内容完全正确。若需要跨平台完全一致效果，建议用户在 Windows 端打开。

**matplotlib 中文**：sandbox 镜像预装了 `fonts-wqy-zenhei`，设置 `matplotlib.rcParams['font.family'] = ['WenQuanYi Zen Hei']` 即可显示中文坐标轴/标题。

### 坑 2：pandas NaN 写入 openpyxl 崩溃

```python
# 错误写法：NaN 传给 openpyxl 会报 ValueError
ws.cell(row=2, col=1, value=float('nan'))

# 正确写法：先替换为 None
df = df.where(df.notna(), other=None)
# 日期列还需转换
if pd.api.types.is_datetime64_any_dtype(df[col]):
    df[col] = df[col].dt.strftime("%Y-%m-%d")
```

`helpers/csv_reader.py` 中 `csv_to_sheet()` 已内置此处理，直接用即可。

### 坑 3：openpyxl 图表系列索引（1-based）

openpyxl 的 `Reference(ws, min_col=1, ...)` 列索引是 1-based，但 `data_range` 字符串解析时注意：

```python
# 正确：用 range_to_tuple 转换字符串范围
from openpyxl.utils.cell import range_to_tuple
((min_row, min_col), (max_row, max_col)) = range_to_tuple("Sheet1!A1:D10")
```

`helpers/chart.py` 已封装此逻辑，直接传字符串 `data_range="A1:D10"` 即可。

### 坑 4：大数据集（> 5 万行）内存溢出

超过 50000 行时，`main.py` 自动切换 `write_only` 模式（逐行写入，内存友好），此时**图表被忽略**（openpyxl 限制）。返回值中会有 `"warning": "write_only mode: charts skipped"` 字段。

解决方案：若必须同时有大数据 + 图表，拆分为两个 sheet：数据 sheet（write_only 写入）+ 图表 sheet（单独创建，从汇总数据做图）。

### 坑 5：sheet 名含非法字符

Excel sheet 名禁止字符：`[ ] : * ? / \`。最大长度 31 字符（中文按字符数而非字节数）。
`helpers/style.py` 的 `sanitize_sheet_name()` 会自动清理，也可手动调用：

```python
from helpers.style import sanitize_sheet_name
safe_name = sanitize_sheet_name("2026年[Q1]销售/分析")
# → "2026年Q1销售分析"（最多31字符）
```

### 坑 6：WPS/LibreOffice 图表兼容性

openpyxl 生成的 BarChart/LineChart 在 WPS 偶尔渲染异常（OpenDocument 兼容问题）。解决方案：改用 `chart_from_matplotlib()` 将图表渲染为 PNG 嵌入，完全兼容所有 xlsx 阅读器，但失去 Excel 内交互性（不能修改图表数据范围）。

### 坑 7：图表 anchor 位置溢出

Excel 最大列为 XFD（第 16384 列），若 anchor_cell 列索引超出会导致文件损坏。`embed_*` 函数内部将列索引 clamp 到最大值 200（安全上限），超出时自动调整并记录 warning。

---

## Constraints

- **输出目录**：只能写 `/output/`，不能写 `/workspace/` 或其他路径
- **输出大小**：单文件 ≤ 50 MB（超出时框架拒绝上传）
- **运行超时**：30 秒（超时后 sandbox 强制终止）
- **网络访问**：sandbox 内无网络，不能 `requests.get()` 外部 API
- **文件读取**：只能读 `/workspace/input/` 下的文件（用户上传的）
- **sheet 数量**：建议 ≤ 20 sheet，过多会导致文件体积快速增大
- **图表类型**：V1.5 支持 bar / line / pie；scatter 留 placeholder（V1.6 补齐）
- **模板路径**：`/skills/xlsx-author/templates/`（sandbox 内已挂载，代码直接引用）
