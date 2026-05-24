# pdf-from-html Skill Guide

Generate high-quality PDF documents from HTML content using **weasyprint** and
**Jinja2** templates — no headless Chrome required.

---

## 何时用（典型场景）

| 场景 | 推荐输入方式 | 推荐模板 |
|------|------------|---------|
| **报价单 / 收据 / 非正式发票** | `template: "invoice"` | invoice |
| **数据分析报告 / 业务汇报** | `template: "report"` | report |
| **荣誉证书 / 资质证明 / 结业证** | `template: "certificate"` | certificate |
| **合同、协议（自定义格式）** | `html_content: "<html>..."` | — |
| **从现有 HTML 文件生成 PDF** | `html_file: "my.html"` | — |
| **动态拼装的复杂文档** | `html_content` + `extra_css` | — |

> **注意：** `invoice` 模板生成的是格式化报价单/收据，**不具有税务发票法律效力**。
> 正式增值税电子发票须通过税务局授权系统开具。

---

## 快速开始（最小示例）

```python
result = invoke_skill("pdf-from-html", {
    "output_filename": "hello.pdf",
    "html_content": """<!DOCTYPE html>
<html lang="zh-CN">
<head><meta charset="UTF-8"><title>测试</title></head>
<body><h1>你好，世界！</h1><p>这是一份测试 PDF。</p></body>
</html>""",
})
# result["output_path"] → "/output/hello.pdf"
# result["page_count"] → 1
```

---

## 完整示例：invoice 模板

```python
result = invoke_skill("pdf-from-html", {
    "output_filename": "报价单_INV-2026-001.pdf",
    "template": "invoice",
    "template_vars": {
        "invoice_number": "INV-2026-001",
        "company_name": "有数科技（北京）有限公司",
        "company_address": "北京市海淀区中关村大街 1 号",
        "company_tax_id": "91110108MA01XXXX00",   # 可选
        "client_name": "某客户有限公司",
        "client_address": "上海市浦东新区",         # 可选
        "issue_date": "2026-05-24",
        "due_date": "2026-06-24",
        "items": [
            {
                "description": "AI 工作台订阅（月）",
                "qty": 1,
                "unit_price": 2999.00,
            },
            {
                "description": "积分加量包 × 3",
                "qty": 3,
                "unit_price": 299.00,
            },
        ],
        "tax_rate": 0.06,         # 6% 增值税
        "currency": "CNY",
        "notes": "付款方式：银行转账。30 天账期。",
        "logo_path": None,        # 可选：传入图片文件名（相对 /workspace/input/）
    },
})
```

### 可选字段一览（invoice 模板）

| 字段 | 类型 | 说明 |
|------|------|------|
| `company_address` | str | 公司地址，显示在名称下方 |
| `company_tax_id` | str | 纳税人识别号 |
| `client_address` | str | 客户地址 |
| `logo_path` | str \| None | 公司 logo 文件名（相对 /workspace/input/）|
| `notes` | str | 备注/付款说明 |

---

## 完整示例：report 模板

```python
result = invoke_skill("pdf-from-html", {
    "output_filename": "Q1_业务报告.pdf",
    "template": "report",
    "template_vars": {
        "title": "2026 年第一季度业务报告",
        "subtitle": "有数科技内部分发",          # 可选
        "author": "产品运营团队",
        "generated_date": "2026-05-24",
        "department": "战略发展部",               # 可选
        "version": "v1.0",                        # 可选
        "abstract": "本报告分析了 Q1 核心 KPI，重点关注 SOP 自动化渗透率和用户留存。",
        "sections": [
            {
                "heading": "执行摘要",
                "content": "本季度月活用户增长 54%，MRR 达到 19.5 万元。",
                "subsections": [
                    {
                        "heading": "亮点",
                        "content": "Agent 模式上线，PDF/Excel 生成能力显著提升用户活跃度。",
                    }
                ],
            },
            {
                "heading": "数据分析",
                "content": "详细数据见下表。",
                "table": {
                    "headers": ["指标", "Q4 2025", "Q1 2026", "增长率"],
                    "rows": [
                        ["月活用户", "1,200", "1,850", "+54%"],
                        ["MRR（万元）", "12.8", "19.5", "+52%"],
                    ],
                },
                "image_path": "sales_chart.png",   # 可选，相对 /workspace/input/
                "image_caption": "图 1：月度收入趋势",
            },
        ],
        "footer_note": "本报告为内部文件，请勿外传。",  # 可选
    },
})
```

---

## 完整示例：certificate 模板

```python
result = invoke_skill("pdf-from-html", {
    "output_filename": "优秀员工证书_张三.pdf",
    "template": "certificate",
    "template_vars": {
        "award_title": "优秀员工奖",
        "recipient_name": "张三",
        "description": "在 2026 年第一季度工作中表现突出，圆满完成各项目标，特此授予本证书以资鼓励。",
        "issue_date": "2026-05-24",
        "issuer_name": "有数科技（北京）有限公司",
        "issuer_title": "人力资源总监",             # 可选
        "issuer_org": "有数科技（北京）有限公司",   # 可选：封面机构名
        "logo_path": None,                          # 可选：logo 图片
        "seal_path": None,                          # 可选：印章图片
    },
    "pdf_options": {
        "page_size": "A4",
        "orientation": "landscape",                 # 证书推荐横向
    },
})
```

---

## 预置模板说明

### invoice（发票/报价单）

- **页面**：A4 竖向
- **布局**：公司名+LOGO 左上 / 发票标题右上 / 客户信息+日期元数据 / 明细表 / 合计区（右对齐）/ 备注 / 法律声明
- **自动计算**：小计、税额、合计（Jinja2 `namespace` 累加，避免 `sum` 限制）
- **页脚**：第 N 页，共 M 页（多页时显示）

### report（数据报告）

- **页面**：A4 竖向
- **布局**：封面页（标题/副标题/作者/日期）→ 摘要块 → 正文章节
- **章节编号**：CSS `counter(section)` 自动生成，无需在 `template_vars` 中手动编号
- **支持嵌套**：每个 section 可含 `subsections` 列表、`table` 数据表、`image_path` 图片
- **页眉页脚**：封面页无页眉/页脚；正文页页脚显示页码

### certificate（证书）

- **页面**：A4 横向（推荐）
- **布局**：双线边框 → 机构 LOGO → 奖项标题 → 受奖人姓名 → 描述正文 → 颁发人/日期/印章
- **字体**：楷体（WenQuanYi / AR PL UKai CN fallback），凸显证书仪式感

---

## jinja2 模板语法速查

自定义 `html_file` 模板中可使用以下 Jinja2 特性：

```html
<!-- 变量输出（自动 HTML 转义） -->
{{ variable }}

<!-- 输出已包含 HTML 的变量（标记为安全，跳过转义） -->
{{ html_fragment | safe }}

<!-- 条件 -->
{% if logo_path %}
<img src="{{ logo_path }}">
{% endif %}

<!-- 循环 -->
{% for item in items %}
<tr><td>{{ item.name }}</td><td>{{ item.qty }}</td></tr>
{% endfor %}

<!-- 内置 filter：货币格式化 -->
{{ 1234.5 | currency }}        → ¥1,234.50
{{ 1234.5 | currency("$") }}   → $1,234.50

<!-- 内置 filter：中文日期 -->
{{ "2026-05-24" | date_cn }}   → 2026年5月24日

<!-- 累加计算（jinja2 无内置 sum-with-expression，用 namespace） -->
{%- set ns = namespace(total=0) %}
{%- for item in items %}{%- set ns.total = ns.total + item.qty * item.unit_price %}{%- endfor %}
合计：{{ ns.total | currency }}
```

**StrictUndefined 模式**：模板变量未定义时立刻报错（不渲染空字符串）。
可选变量必须用 `{% if var is defined and var %}...{% endif %}` 包裹，
或在调用时显式传 `None`：`"logo_path": None`。

---

## CSS 与中文字体

sandbox 基础镜像预装了 `fonts-wqy-zenhei`（文泉驿正黑体），可正常渲染中文。
renderer.py 自动在 body 注入以下字体栈：

```css
body {
    font-family: 'WenQuanYi Zen Hei', 'Microsoft YaHei', '微软雅黑',
                 'Noto Sans CJK SC', 'SimHei', 'Arial Unicode MS', sans-serif;
}
```

如需在特定元素使用楷体（证书场景），在 HTML 或 `extra_css` 中覆盖：

```css
.cert-body {
    font-family: 'AR PL UKai CN', 'KaiTi', '楷体', 'WenQuanYi Zen Hei', serif;
}
```

**不要在 CSS 中使用 `@font-face` + 外部 URL**：sandbox 无外网，字体加载会超时。

---

## 嵌入图片

### 方式 A：文件路径（推荐）

把图片上传到 `/workspace/input/`，HTML 中用文件名引用：

```html
<img src="logo.png" alt="公司 LOGO">
```

weasyprint 通过 `base_url="/workspace/input"` 解析为 `/workspace/input/logo.png`。

### 方式 B：base64 内联（适合小图）

```html
<img src="data:image/png;base64,iVBORw0KGgoAAAANS..." alt="图标">
```

适合小图标（< 50KB）；大图会导致 HTML 字符串过大，影响渲染速度。

### 注意事项

- 图片建议不超过 **2MB**（高分辨率图片应先压缩到 150 DPI）
- 支持格式：PNG、JPEG、GIF、SVG（SVG 通过 Cairo 渲染，效果因图而异）
- **不支持** WebP（weasyprint 62.x）

---

## weasyprint 与 Playwright/Chrome headless 的区别

| 对比项 | weasyprint | Chrome headless (Playwright) |
|--------|-----------|------------------------------|
| 系统依赖 | libcairo + libpango（轻量） | Chromium 二进制（~300MB） |
| CSS 支持 | CSS Paged Media（页码/页眉页脚原生） | 完整 CSS 3，但打印 CSS 兼容性差 |
| JS 支持 | 不支持 | 完整 JS 运行时 |
| 渲染速度 | 快（< 2 秒） | 较慢（启动 Chromium ~3 秒）|
| 适用场景 | 表格型文档、报告、发票 | 含 JS 动态内容的网页截图 |
| 中文字体 | FontConfig 系统字体，稳定 | Bundled fonts，需配置 |

**结论**：文档类 PDF（发票/报告/证书）选 weasyprint；网页截图选 Playwright。

---

## 常见坑 & 解决方案

### 坑 1：中文显示方块（□□□）

**原因**：CJK 字体未找到，weasyprint 回退到内置拉丁字体。

**解决**：
1. 确认 sandbox 镜像已安装 `fonts-wqy-zenhei` 并运行 `fc-cache -fv`
2. renderer.py 已自动注入 WenQuanYi Zen Hei 字体栈；如 body 被自定义 CSS 覆盖，确保保留 CJK fallback

---

### 坑 2：图片未显示（空白区域）

**原因**：相对路径图片无法解析，或文件不存在。

**解决**：
1. 确认图片已上传到 `/workspace/input/`
2. `pdf_options.base_url` 默认为 `/workspace/input`；若图片在其他目录，显式传 `base_url`
3. 使用绝对路径：`<img src="/workspace/input/subdir/logo.png">`（始终有效）

---

### 坑 3：CSS Grid 不生效

weasyprint 62.x 不完整支持 CSS Grid。

**解决**：改用 `display: table` / `display: table-cell` 实现多栏布局（预置模板均采用此方案）。

---

### 坑 4：页码不显示

**原因**：CSS `@page { @bottom-center { content: counter(page); } }` 需配合 `renderer.py` 注入的 `@page` 基础规则。

**解决**：在 HTML `<style>` 内追加（不要替换 renderer 注入的 @page，避免覆盖 size/margin）：

```css
@page {
    @bottom-center {
        content: "第 " counter(page) " 页";
        font-size: 8pt;
        color: #94A3B8;
    }
}
```

---

### 坑 5：表格跨页拆行

**原因**：weasyprint 默认允许表格行在页面边界处拆断。

**解决**：在 CSS 中设置：

```css
table tbody tr { page-break-inside: avoid; }
```

---

### 坑 6：jinja2 `UndefinedError`（变量缺失）

**原因**：StrictUndefined 模式下，所有模板变量必须显式提供（含可选字段需传 `None`）。

**解决**：
- 可选字段传 `None`：`"logo_path": None`
- 模板内用 `{% if var is defined and var %}` 保护可选字段
- 错误信息包含缺失变量名，按提示补全即可

---

### 坑 7：jinja2 自动转义吃掉 HTML 标签

**原因**：`autoescape=True`（启用 HTML 转义），`{{ "<b>粗体</b>" }}` 渲染为 `&lt;b&gt;`。

**解决**：已包含 HTML 的变量用 `| safe` 标记：

```html
{{ rich_content | safe }}
```

---

### 坑 8：PDF 体积过大

**原因**：高分辨率图片未压缩，直接嵌入。

**解决**：生成 PDF 前，先用 png-chart-author skill 或 Pillow 将图片压缩到 150 DPI。
规则：单图建议不超过 2MB，整个 PDF 不超过 50MB（manifest 限制）。

---

### 坑 9：weasyprint 产生大量警告输出

weasyprint 对不支持的 CSS 属性、未知 MIME 类型、字体替换等情况发出 UserWarning。
这些警告通常无害（不影响 PDF 输出质量）。

renderer.py 已通过 `warnings.filterwarnings("ignore", category=UserWarning)` 静默处理。
若需要调试，临时去掉 `warnings.catch_warnings()` 块。

---

## Constraints（平台限制）

| 限制项 | 值 |
|--------|-----|
| 最大运行时间 | 30 秒 |
| 单文件最大体积 | 50 MB |
| 输入目录 | `/workspace/input/` |
| 输出目录 | `/output/` |
| 支持模板 | invoice / report / certificate |
| 不支持的 CSS | Grid（部分）、position:sticky、CSS 动画、@font-face + 外部 URL |
| 不支持的图片格式 | WebP |

---

## 文件路径约定

```
/workspace/input/          ← 输入文件（HTML、图片、params.json）
/output/                   ← 输出 PDF（skill 写入；框架收集上传）
/skills/pdf-from-html/     ← skill 代码（只读）
├── main.py
├── manifest.json
├── SKILL.md
├── helpers/
│   ├── __init__.py
│   ├── renderer.py        ← weasyprint 核心
│   ├── template.py        ← jinja2 模板渲染
│   └── pdf_meta.py        ← PDF 页数读取
└── templates/
    ├── invoice.html.j2
    ├── report.html.j2
    └── certificate.html.j2
```
