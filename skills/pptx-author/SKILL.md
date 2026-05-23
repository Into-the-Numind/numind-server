# pptx-author Skill Guide

Generate production-quality PowerPoint presentations (`.pptx`) from structured slide definitions, with optional brand identity injection (logo, primary/secondary colours, fonts).

---

## 何时用（When to Use）

Use `pptx-author` whenever the agent needs to produce a multi-slide presentation file:

| 场景 | 说明 |
|------|------|
| **业务汇报 / 季度回顾** | SOP 执行数据、KPI 达成情况、团队绩效汇总 |
| **销售提案 / 客户演示** | 方案介绍、产品演示、ROI 分析、差异化对比 |
| **培训材料 / 知识分享** | 操作规程讲义、新人入职指引、内部课程 |
| **数据分析报告** | 图表可视化（柱形/折线/饼图/散点）+ 数据表格 |
| **产品发布 / 功能演示** | 功能介绍、技术架构、路线图规划 |
| **融资路演 / 投资者 Deck** | 市场机会、增长数据、商业模式、团队介绍 |

**不适合的场景**：需要在 PowerPoint 内交互式编辑图表数据（本 skill 嵌入 PNG，不支持原生 pptx chart XML）。

---

## 快速开始（3 张幻灯片）

```python
import invoke_skill

result = invoke_skill("pptx-author", {
    "output_filename": "quick_start.pptx",
    "slides": [
        {
            "layout": "cover",
            "title": "2026 年度业务回顾",
            "subtitle": "战略发展部  ·  2026-06-01",
        },
        {
            "layout": "title-bullets",
            "title": "本次议程",
            "bullet_points": [
                "Q1–Q2 销售业绩",
                "关键里程碑",
                "下半年目标",
            ],
        },
        {
            "layout": "end",
            "title": "谢谢",
            "subtitle": "欢迎提问",
        },
    ],
})

# result["output_path"] → "/output/quick_start.pptx"
print(result["output_path"], result["slides_created"])
```

---

## 完整示例（品牌 config + 图表 + 表格）

```python
result = invoke_skill("pptx-author", {
    "output_filename": "branded_proposal.pptx",

    # -- 品牌配置（所有字段可选） --
    "brand_config": {
        "company_name": "有数科技",
        "logo_path": "company_logo.png",     # 相对 /workspace/input/ 的路径
        "primary_color": "#2563EB",           # 主色（Tailwind blue-600）
        "secondary_color": "#1E40AF",         # 辅色
        "font_family": "Noto Sans CJK SC",    # 字体名（sandbox 已预装）
    },

    "slides": [
        {
            "layout": "cover",
            "title": "有数 AI 工作台\n产品提案 2026 Q3",
            "subtitle": "战略合作提案  ·  2026-06-01",
        },
        {
            "layout": "section",
            "subtitle": "01",          # 章节编号（大字显示）
            "title": "市场机会分析",
        },
        {
            "layout": "title-chart",
            "title": "季度收入趋势（万元）",
            "chart": {
                "chart_type": "bar",   # bar | line | pie | scatter
                "title": "2025–2026 季度收入",
                "data": {
                    "categories": ["Q1'25", "Q2'25", "Q3'25", "Q4'25", "Q1'26", "Q2'26"],
                    "series": [
                        {"name": "实际", "values": [320, 410, 390, 520, 580, 640]},
                        {"name": "目标", "values": [300, 380, 420, 500, 550, 620]},
                    ],
                },
                "size": {"left": 0.05, "top": 0.25, "width": 0.90, "height": 0.68},
            },
            "notes": "注意 Q3'25 低于目标，原因见附录。",
        },
        {
            "layout": "title-table",
            "title": "产品定价方案",
            "table": {
                "headers": ["方案", "月费（元）", "积分", "支持"],
                "rows": [
                    ["入门版", "¥999",   "5,000",   "邮件"],
                    ["专业版", "¥3,999", "30,000",  "CSM"],
                    ["企业版", "¥12,999","150,000", "驻场"],
                ],
            },
        },
        {
            "layout": "two-column",
            "title": "核心功能亮点",
            "bullet_points": [
                "SOP 流程引擎：可视化编排",
                "SalesRAG：向量检索知识库",
                "Agent 工作台：安全 sandbox",
                "积分计费：两阶段 Reserve/Reconcile",
            ],
            "chart": {
                "chart_type": "pie",
                "title": "用户时间分配",
                "data": {
                    "categories": ["SOP 执行", "知识查询", "Agent 任务", "其他"],
                    "series": [{"name": "时间", "values": [40, 30, 20, 10]}],
                },
            },
        },
        {
            "layout": "end",
            "title": "期待合作",
            "subtitle": "contact@youshu.ai",
        },
    ],
})
```

---

## SlideLayout 参考（10 种布局）

| layout 名称 | 用途 | 关键字段 |
|-------------|------|---------|
| `cover` | 封面：大标题 + 副标题 + 公司名 | `title`, `subtitle` |
| `section` | 章节过渡页：章节编号（大字）+ 标题 | `subtitle`（编号）, `title` |
| `title-body` | 标题 + 长文段落（支持 `\n` 分段）| `title`, `body` |
| `title-bullets` | 标题 + 要点列表（最多 6 条）| `title`, `bullet_points` |
| `title-table` | 标题 + 数据表格 | `title`, `table.headers`, `table.rows` |
| `title-chart` | 标题 + matplotlib 图表 | `title`, `chart` |
| `title-image` | 标题 + 图片（自动保持宽高比）| `title`, `image.path`, `image.caption` |
| `two-column` | 左文字 / 右图表或图片 | `bullet_points` 或 `body` + `chart` 或 `image` |
| `blank` | 空白幻灯片（高级自定义）| 无 |
| `end` | 结束页：感谢语 + 公司名 + logo | `title`, `subtitle` |

---

## brand_config 参数说明

### 默认值（T18 决策：通用中性，不带产品品牌）

```python
DEFAULT_BRAND_CONFIG = {
    "company_name": "",                # 空字符串，封面/底部不显示公司名
    "logo_path": None,                 # None → 不插入 logo
    "primary_color": "#1F2937",        # Tailwind gray-800（中性深灰）
    "secondary_color": "#6B7280",      # Tailwind gray-500（中性中灰）
    "font_family": "Noto Sans CJK SC", # CJK 字体（sandbox 内 alias → wqy-zenhei）
}
```

> 不传 `brand_config` 时自动使用以上默认值。传入部分字段时**深合并**，未指定的字段保留默认值。

### 字段说明

| 字段 | 类型 | 说明 |
|------|------|------|
| `company_name` | `str` | 公司名，出现在封面下方和结束页底部；空字符串则不渲染 |
| `logo_path` | `str \| None` | 相对 `/workspace/input/` 的路径，支持 **PNG / JPG**；`None` 跳过 logo 插入 |
| `primary_color` | `str` | CSS hex 颜色（如 `"#2563EB"`）；用于标题字色、图表主色、封面背景色块 |
| `secondary_color` | `str` | 副色；用于副标题、辅助文字 |
| `font_family` | `str` | 字体名，必须与 sandbox 内已安装字体名称完全匹配（大小写敏感） |

### logo 位置（via DeckBuilder.apply_brand）

默认插入右上角 (`top-right`)。若需调整，在代码中调用 `apply_brand_to_deck(prs, brand, logo_position="bottom-right")`。

```python
# 支持的位置：
# "top-right"    （默认）
# "bottom-right"
# "bottom-left"
```

### 覆盖示例

```python
# 只改主色，其余保留默认
"brand_config": {"primary_color": "#DC2626"}          # Tailwind red-600

# 完整配置
"brand_config": {
    "company_name": "Acme Corp",
    "logo_path": "acme_logo.png",
    "primary_color": "#059669",    # green-600
    "secondary_color": "#047857",  # green-700
    "font_family": "Microsoft YaHei",
}
```

---

## 图表（chart）详细说明

### 数据格式

```python
"chart": {
    "chart_type": "bar",          # "bar" | "line" | "pie" | "scatter"
    "title": "图表标题",
    "data": {
        "categories": ["Q1", "Q2", "Q3"],   # X 轴标签（pie 时为扇区标签）
        "series": [
            {"name": "2025 年", "values": [100, 150, 200]},
            {"name": "2026 年", "values": [120, 180, 240]},
        ],
    },
    "size": {                     # 可选，相对幻灯片的位置（0.0–1.0）
        "left": 0.05,
        "top": 0.25,
        "width": 0.90,
        "height": 0.68,
    },
}
```

### 各图表类型注意事项

| 类型 | 注意 |
|------|------|
| `bar` | 多 series 时自动并排；categories > 5 时标签自动旋转 |
| `line` | 每个 series 一条折线 + 圆点；适合时序趋势 |
| `pie` | 只读第一个 series 的 values；categories 为扇区标签 |
| `scatter` | **categories 必须为数值列表**（如 `["1.0", "2.0"]`）；字符串 categories 会 fallback 到 index，视觉效果较差 |

### 品牌着色

提供 `brand_config.primary_color` 后，图表第一个 series 自动使用该颜色；其余 series 从 Blues colormap 中取近似色。

---

## 常见坑 & 解决方案

### 坑 1：CJK 字体显示为方块（matplotlib 图表内）

**原因**：matplotlib 默认字体不含中文字形。

**应对**：
- sandbox 镜像预装了 `fonts-wqy-zenhei`；`render_chart_to_image` 会自动检测并使用。
- 若仍有问题，chart 标题和标签改为英文/拼音。

### 坑 2：logo 为 SVG 格式

**应对**：本 skill 仅支持 PNG / JPG logo。SVG 请先用 `cairosvg` 或 Figma 导出为 PNG，再上传到 `/workspace/input/`。

### 坑 3：字体名与系统不匹配

**应对**：`font_family` 必须与 sandbox 内字体的 exact name 匹配（大小写敏感）。

已知可用字体（sandbox 预装）：
- `"Noto Sans CJK SC"` — 思源黑体（alias：WenQuanYi Zen Hei）
- `"Calibri"` — 英文无衬线（非 CJK）

### 坑 4：图片/logo 宽高比失真

**应对**：本 skill 用 Pillow 读取原始尺寸并计算正确高度，不依赖文件名猜格式。正常情况下宽高比自动保持。

### 坑 5：图表在 PowerPoint 内无法交互编辑

**说明**：本 skill 嵌入 matplotlib PNG 图片，而非 pptx 原生 chart XML。优点是跨平台兼容性好；缺点是在 PowerPoint 内无法直接修改图表数据。如需可编辑图表，请告知后人工处理。

### 坑 6：scatter 图表 X 轴出现 0, 1, 2... 而非实际值

**原因**：categories 非数值（如 `["Jan", "Feb"]`）时自动 fallback 到整数索引。

**应对**：scatter 的 categories 请传数值字符串，如 `["1.0", "2.5", "3.0"]`。

### 坑 7：幻灯片尺寸不是 16:9

**说明**：DeckBuilder 强制设置 33.87 cm × 19.05 cm（标准 16:9 宽屏）。如需其他比例，传入 `style_config.slide_width_cm` / `slide_height_cm`。

### 坑 8：演讲者备注（notes）不显示在幻灯片上

**说明**：正确行为。备注写入 `notes_slide`，在 PowerPoint 演讲者视图或 "备注" 面板中可见，不影响幻灯片正面内容。

---

## Constraints

- **输出格式**：仅 `.pptx`（OpenXML），不支持导出为 PDF/PNG（后续版本）
- **logo 格式**：PNG / JPG；不支持 SVG / WebP / TIFF
- **bullet_points 上限**：6 条（超出自动截断）
- **max_runtime_seconds**：45（图表多时接近上限）
- **max_output_size_mb**：50
- **python-pptx 版本**：`>=1.0`（manifest 锁定；1.0 前为 0.6.x 系列）
- **图表不可在 PPT 内交互编辑**（已知限制，V2 改进项）

---

## 文件路径约定

| 路径 | 说明 |
|------|------|
| `/workspace/input/` | 输入文件挂载点（logo、图片）；`image.path` / `logo_path` 相对于此目录 |
| `/output/` | 输出目录；生成的 .pptx 写入此处 |
| `/skills/pptx-author/templates/` | 三套内置模板（briefing / analysis / proposal）|

---

## 品牌一致性工作流（Brand Consistency Workflow）

当 agent 需要为同一家企业反复生成 PPT 时，推荐将 `brand_config` 存入记忆系统（Track 3 Memory），避免每次重复传参：

```python
# 1. 首次调用时存储品牌配置
await memory.set("user_brand_config", {
    "company_name": "有数科技",
    "logo_path": "youshu_logo.png",
    "primary_color": "#2563EB",
    "secondary_color": "#1E40AF",
    "font_family": "Noto Sans CJK SC",
})

# 2. 后续调用时从记忆层读取
brand_cfg = await memory.get("user_brand_config")

result = invoke_skill("pptx-author", {
    "output_filename": "weekly_report.pptx",
    "brand_config": brand_cfg,      # 直接传入
    "slides": [...],
})
```

> **注意**：`pptx-author` skill 本身不访问记忆系统。由 agent 在调用前从记忆层取出 brand_config 并作为参数传入。

---

## 依赖版本（required_libs）

```
python-pptx>=1.0     # PowerPoint 文件生成
pillow>=10.0         # logo/图片 宽高比计算 + SVG 拦截
matplotlib>=3.7      # 图表渲染（Agg 无头后端）
```

sandbox 镜像中由 `requirements.txt` 锁定 patch 版本（reproducible build）。中文字体通过系统包 `fonts-wqy-zenhei` 预装。
