# docx-author Skill Guide

Generate formatted Word documents (`.docx`) inside the sandbox.  Pass a
structured list of content blocks and the skill produces a ready-to-download
Word file — no manual editing required.

---

## 何时用（典型场景）

| 场景 | 说明 |
|------|------|
| **业务分析报告** | 季度 / 年度运营数据报告，包含多级标题、数据表格、图表截图（图片嵌入）和管理层摘要 |
| **SOP 操作手册** | 标准作业程序文档，含编号步骤（有序列表）、注意事项（无序列表）和操作截图 |
| **会议纪要** | 会议背景 + 决议事项 + 行动计划（带负责人、截止日期的表格） |
| **工作日志 / 周报** | 结构化工作记录，含完成事项、遗留问题和下周计划 |
| **合同 / 协议草稿** | 条款段落 + 签署信息表格 + 页眉页脚（含机密标识和页码） |
| **客户提案** | 问题分析 + 解决方案 + 报价表格 + 团队介绍，输出专业 Word 格式供客户下载 |
| **技术规范文档** | API 说明 + 参数表格 + 代码注释（注：代码块用 paragraph，等宽字体需额外设置） |

**不适用场景：**
- 需要 `.docx` → `.pdf` 直接转换（sandbox 无 LibreOffice，请改用 `pdf-from-html` skill）
- 复杂排版（分栏、文字绕图、SmartArt）— 超出 python-docx 能力边界
- 电子表格 / 数据透视 — 请使用 `xlsx-author` skill

---

## 快速开始（最小示例）

```python
params = {
    "output_filename": "快速入门示例.docx",
    "blocks": [
        {"type": "heading", "level": 1, "text": "项目概述"},
        {"type": "paragraph", "text": "这是第一段正文，支持中文和 English 混排。"},
        {"type": "paragraph", "text": "这是第二段正文，默认两端对齐，1.5 倍行距。"},
    ],
}
```

输出：`/output/快速入门示例.docx`

---

## 完整示例（含表格、列表、图片、页眉页脚）

```python
params = {
    "output_filename": "完整示例报告.docx",

    # 可选：加载通用报告模板（封面 + 目录占位 + 正文区 + 附录区）
    "template": "general-report",
    "template_vars": {
        "doc_title": "2026年度运营分析报告",
        "abstract": "本报告分析全年用户增长与收入变化趋势。",
        "author": "数据分析团队",
        "generated_date": "2026-05-24",
        "department": "产品部",
        "version": "v1.0",
    },

    # 文档属性（写入 Word 核心属性，在"文档信息"面板可见）
    "metadata": {
        "title": "2026年度运营分析报告",
        "author": "数据分析团队",
        "subject": "运营分析",
        "description": "包含用户增长、收入结构和产品迭代三章。",
    },

    # 页眉（左 / 中 / 右三列，用 Tab 分隔）
    "header": {
        "left": "莫小派 AI 工作台",
        "center": "",
        "right": "内部文件",
    },
    # 页脚（{{page_number}} 替换为 Word 页码域，打开文件自动更新）
    "footer": {
        "left": "机密 — 请勿外传",
        "center": "{{page_number}}",
        "right": "2026-05-24",
    },

    # 全局样式（覆盖模板默认值）
    "style_config": {
        "font_name": "Microsoft YaHei",   # 中文字体
        "font_size_pt": 10.5,             # 五号字
        "line_spacing": 1.5,
        "paragraph_spacing_pt": 6,
        "heading_color": "#1E293B",       # 深海军蓝，一级标题
        "page_margin_cm": 2.54,           # 1 英寸页边距
    },

    "blocks": [
        # 一级标题
        {"type": "heading", "level": 1, "text": "第一章  用户增长"},

        # 段落（支持 \n 换行，bold/italic，indent_level 缩进）
        {
            "type": "paragraph",
            "text": "全年注册用户同比增长 47.3%，MAU 达 12.8 万。",
            "bold": False,
            "italic": False,
            "alignment": "justify",   # left | center | right | justify
            "indent_level": 0,        # 0–3，每级缩进 0.75 cm
        },

        # 二级标题
        {"type": "heading", "level": 2, "text": "1.1 渠道分布"},

        # 表格（蓝色表头 + 可选列宽）
        {
            "type": "table",
            "headers": ["渠道", "注册量", "占比"],
            "rows": [
                ["有机搜索", 45200, "35%"],
                ["口碑推荐", 22600, "18%"],
                ["付费广告", 18900, "15%"],
            ],
            "style": "Table Grid",       # Word 英文样式名，必须用英文
            "col_widths_cm": [5.0, 4.0, 3.0],  # 不传则等分
        },

        # 无序列表
        {
            "type": "list",
            "items": ["口碑留存最高（52%）", "建议加大老用户激励"],
            "ordered": False,
        },

        # 有序列表（支持嵌套：传 dict 带 indent 字段）
        {
            "type": "list",
            "items": [
                "分析用户留存漏斗",
                {"text": "聚焦 Day 7 流失节点", "indent": 1},
                "制定激励方案",
            ],
            "ordered": True,
        },

        # 图片（相对 /workspace/input/ 的路径）
        {
            "type": "image",
            "path": "retention_chart.png",   # → /workspace/input/retention_chart.png
            "width_cm": 14.0,               # 高度按比例自动缩放
            "caption": "图 1-1  用户留存率趋势（2026）",
            "alignment": "center",
        },

        # 水平分割线
        {"type": "horizontal_rule"},

        # 硬分页
        {"type": "page_break"},

        {"type": "heading", "level": 1, "text": "第二章  收入结构"},
        {"type": "paragraph", "text": "全年总收入 ¥3,240 万元，订阅占比 78%。"},
    ],
}
```

---

## Block 类型参考

### heading

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `type` | `"heading"` | 是 | 固定值 |
| `level` | `int` 1–6 | 是 | Word Heading 1–6 样式 |
| `text` | `str` | 是 | 标题文字 |

### paragraph

| 字段 | 类型 | 默认 | 说明 |
|------|------|------|------|
| `text` | `str` | — | 支持 `\n` 换行 |
| `bold` | `bool` | `false` | 加粗 |
| `italic` | `bool` | `false` | 斜体 |
| `alignment` | `str` | `"justify"` | `"left"` / `"center"` / `"right"` / `"justify"` |
| `indent_level` | `int` | `0` | 0–3，每级 0.75 cm 左缩进 |

### table

| 字段 | 类型 | 默认 | 说明 |
|------|------|------|------|
| `headers` | `[str]` | — | 表头列名 |
| `rows` | `[[str\|int\|float]]` | — | 数据行 |
| `style` | `str` | `"Table Grid"` | Word 表格样式（必须用英文名） |
| `col_widths_cm` | `[float]` | 自动等分 | 各列宽度 cm，元素数须与 headers 一致 |

### list

| 字段 | 类型 | 默认 | 说明 |
|------|------|------|------|
| `items` | `[str\|dict]` | — | 列表项，dict 格式：`{"text": str, "indent": int}` |
| `ordered` | `bool` | `false` | `true` = 有序（1.2.3.），`false` = 无序（•） |

### image

| 字段 | 类型 | 默认 | 说明 |
|------|------|------|------|
| `path` | `str` | — | 相对 `/workspace/input/` 的路径，或绝对路径 |
| `width_cm` | `float` | `14.0` | 显示宽度（cm），高度自动保持比例 |
| `caption` | `str` | 无 | 图注（斜体，居中） |
| `alignment` | `str` | `"center"` | `"left"` / `"center"` / `"right"` |

### page_break

```json
{"type": "page_break"}
```

### horizontal_rule

```json
{"type": "horizontal_rule"}
```

---

## 样式配置参考 (`style_config`)

| 字段 | 类型 | 默认 | 说明 |
|------|------|------|------|
| `font_name` | `str` | `"微软雅黑"` | 正文字体，sandbox 内 WenQuanYi Zen Hei 可用 |
| `font_size_pt` | `float` | `10.5` | 正文字号（五号字 = 10.5pt） |
| `line_spacing` | `float` | `1.5` | 行距倍数 |
| `paragraph_spacing_pt` | `int` | `6` | 段前 / 段后间距（pt） |
| `heading_color` | `str` | `"#1E293B"` | 一–三级标题颜色（hex） |
| `page_margin_cm` | `float` | `2.54` | 四边页边距（cm） |

---

## 模板使用（general-report）

模板文件位于 `/skills/docx-author/templates/general-report.docx`，提供：

- **封面**：标题、摘要、作者、日期、部门、版本（占位变量用 `{{key}}` 格式）
- **目录页**：空白占位（在 Word 中选中后点击「引用 → 更新目录」自动生成）
- **正文区**：Heading 1 / 2 / 3 样式已预设为标准格式
- **附录页**：附录标题 + 空白内容区

**支持的占位变量：**

| 变量 | 说明 |
|------|------|
| `{{doc_title}}` | 文档标题（封面大标题） |
| `{{abstract}}` | 摘要 / 副标题 |
| `{{author}}` | 作者姓名或团队名 |
| `{{generated_date}}` | 生成日期（e.g. 2026-05-24） |
| `{{department}}` | 部门名称 |
| `{{version}}` | 版本号（e.g. v1.0） |

使用方式：在 `params` 中传 `"template": "general-report"` + `"template_vars": {...}`，
skill 先加载模板、替换占位变量，再追加 `blocks` 内容到正文区。

---

## 常见坑 & 解决方案

### 坑 1：中文字体显示不正确

**现象：** 在 Linux/macOS 打开 docx 后中文字体显示为宋体或黑方块。

**原因：** `run.font.name` 只设置 Latin 字体属性（`w:rFonts/@w:ascii`），中文需要单独设置 `w:rFonts/@w:eastAsia`。

**解决：** 本 skill 内部已同时设置两个属性，无需额外处理。在 Windows 下指定 `"Microsoft YaHei"` 效果最佳；sandbox 内（Linux）使用镜像预装的 `WenQuanYi Zen Hei`（fonts-wqy-zenhei 包）作为 fallback。

```python
"style_config": {"font_name": "Microsoft YaHei"}  # Windows 效果最佳
```

### 坑 2：表格样式报 `KeyError`

**现象：** `table.style = "表格网格"` 抛出 `KeyError`。

**原因：** python-docx 的表格样式名必须使用 **Word 英文内部名称**，不能用中文或本地化名称。

| 常用样式（中文 Word 显示名） | 英文内部名（代码中使用） |
|-----------------------------|--------------------------|
| 表格网格 | `Table Grid` |
| 普通表格 | `Table Normal` |
| 浅色列表 | `Light List` |
| 中等底纹 | `Medium Shading 1` |

**解决：** 使用英文样式名，或传 `"style": "Table Grid"`（默认值）。

### 坑 3：有序列表编号不连续

**现象：** 两段有序列表中间插入了一个段落，重新开始列表后编号从 1 重置。

**原因：** python-docx 使用 Word 的内置多级列表编号 XML，连续性由 `numId` XML 状态控制，中断后可能重置。

**解决：** V1.5 不保证跨 block 中断后有序列表编号连续。如需连续编号，将所有列表项放在同一个 `list` block 中；或在 `items` 内使用嵌套 dict 结构。

### 坑 4：页码域在本地预览不显示数字

**现象：** `{{page_number}}` 替换后，在 python-docx 或第三方库读取时页脚中没有数字。

**原因：** Word 页码是 `{ PAGE }` 字段代码（field code），不是静态数字。字段值在 Word 打开 / 打印时才计算更新。python-docx 写入的是正确的 XML 域代码（`<w:fldChar>` + `<w:instrText> PAGE </w:instrText>`），在 Microsoft Word 和 WPS 中打开后会正确显示。

**解决：** 生成后在 Word 中按 `Ctrl+A` 全选 → `F9` 更新所有域，即可看到页码。

### 坑 5：图片 FileNotFoundError

**现象：** `result["success"] == False`，`error` 包含 "not found"。

**原因：** 图片路径解析为 `/workspace/input/<path>`，但该文件不存在。

**解决：** 确保在调用 invoke_skill 之前已将图片上传到 sandbox 的 `/workspace/input/` 目录。路径中不需要包含 `/workspace/input/` 前缀，skill 会自动拼接。

```python
# 正确：文件已在 /workspace/input/charts/retention.png
{"type": "image", "path": "charts/retention.png"}

# 错误：路径重复
{"type": "image", "path": "/workspace/input/charts/retention.png"}
```

### 坑 6：模板占位变量未替换（跨 run 分割）

**现象：** `{{doc_title}}` 在文档中保持原样，未被替换。

**原因：** Word 的拼写检查 / 自动更正功能可能将 `{{doc_title}}` 拆分为多个 `<w:r>` run，导致正则只在单个 run 中匹配失败。

**解决：**
1. 制作模板时，在单独的段落中输入占位变量，**不要**在占位文字上设置字体变化、加粗等格式，保持为纯文本单一 run。
2. 如果仍然失败，skill 的段落级 fallback 会将整段替换为纯文本（丢失该段的字符格式，通常可接受）。

### 坑 7：.docx 无法直接转 PDF

**现象：** 用户需要 PDF 格式的"Word 风格"文档。

**原因：** .docx → .pdf 需要 LibreOffice 运行时，sandbox 内不提供（体积大、安全面宽）。

**解决：** 使用 `pdf-from-html` skill 配合相同的内容结构生成 PDF，而不是将 .docx 转换为 PDF。两种 skill 各自独立使用。

---

## Constraints

- 运行环境：Python 3.11 sandbox，依赖 `python-docx>=1.1`（纯 Python，无 C 扩展）
- 最大运行时间：30 秒
- 最大输出文件：50 MB
- 输入文件目录：`/workspace/input/`（图片等资源放这里）
- 输出文件目录：`/output/`
- 不支持：SmartArt、图文框、宏、VBA、.docx → .pdf 转换
- 列表编号连续性：跨 block 中断的有序列表不保证编号连续
- 页码域：写入正确 XML，需在 Word 打开后更新显示（`F9`）

---

## 文件路径约定

```
sandbox 内:
  /workspace/input/          ← 输入文件（图片等）
  /output/                   ← 输出 .docx 文件
  /skills/docx-author/       ← skill 本身（只读）
    templates/
      general-report.docx    ← 通用报告模板

output_filename 规则:
  - 中文字符保留（Unicode）
  - 非法字符自动剔除：\ / : * ? " < > |
  - 路径遍历（../ 等）自动剔除
  - 空文件名 fallback → "output.docx"
```
