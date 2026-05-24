"""
example_branded.py — Branded 8-slide PowerPoint example with multiple chart types.

Demonstrates:
  - brand_config override (custom primary/secondary colour, font, company name)
  - Multiple layouts: cover / section / title-bullets / title-chart (bar + pie) /
                      title-table / two-column / end
  - Logo insertion (skipped if file absent — shows graceful warning path)
  - Bar chart + Pie chart embedded via matplotlib
  - Table with header styling

Run inside the sandbox:
    python /skills/pptx-author/examples/example_branded.py
Output written to: /output/example_branded.pptx
"""

import os
import sys

_skill_dir = os.path.join(os.path.dirname(__file__), "..")
sys.path.insert(0, _skill_dir)

from helpers import DeckBuilder, BrandConfig, merge_brand_config

OUTPUT_PATH = "/output/example_branded.pptx"

# --------------------------------------------------------------------------
# Brand configuration override
# All keys are optional — unspecified keys retain DEFAULT_BRAND_CONFIG values.
# --------------------------------------------------------------------------

USER_BRAND_CONFIG = {
    "company_name": "有数科技",
    "logo_path": None,              # set to "logo.png" if file placed in /workspace/input/
    "primary_color": "#2563EB",     # Tailwind blue-600
    "secondary_color": "#1E40AF",   # Tailwind blue-800
    "font_family": "Noto Sans CJK SC",
}

# --------------------------------------------------------------------------
# Slide definitions
# --------------------------------------------------------------------------

slides = [
    # 1. Cover
    {
        "layout": "cover",
        "title": "有数 AI 工作台\n产品提案 2026 Q3",
        "subtitle": "战略合作伙伴汇报  ·  2026-05-24",
    },

    # 2. Section transition
    {
        "layout": "section",
        "subtitle": "01",
        "title": "市场机会与痛点分析",
    },

    # 3. Bullet points
    {
        "layout": "title-bullets",
        "title": "核心痛点（来自 120 家企业访谈）",
        "bullet_points": [
            "SOP 依赖纸质流程，版本混乱，执行率低于 40%",
            "销售培训成本高，新人上手周期长达 3 个月",
            "知识库分散在多个系统，检索效率低",
            "AI 工具与业务系统割裂，无法融合工作流",
            "数据安全合规要求高，无法使用公有 LLM",
        ],
        "notes": "重点强调访谈数量 120 家，覆盖制造、零售、金融三个行业。",
    },

    # 4. Bar chart — revenue projection
    {
        "layout": "title-chart",
        "title": "有数平台 ARR 增长预测（万元）",
        "chart": {
            "chart_type": "bar",
            "title": "2026–2028 年度收入预测",
            "data": {
                "categories": ["2026", "2027", "2028"],
                "series": [
                    {"name": "悲观", "values": [800, 1500, 2800]},
                    {"name": "基准", "values": [1200, 2400, 4500]},
                    {"name": "乐观", "values": [1800, 3600, 7000]},
                ],
            },
        },
    },

    # 5. Pie chart — market breakdown
    {
        "layout": "title-chart",
        "title": "目标市场细分（按行业 TAM 占比）",
        "chart": {
            "chart_type": "pie",
            "title": "TAM 行业分布",
            "data": {
                "categories": ["制造业", "零售 & 消费品", "金融服务", "医疗健康", "其他"],
                "series": [
                    {"name": "TAM", "values": [35, 25, 20, 12, 8]},
                ],
            },
        },
    },

    # 6. Table — pricing tiers
    {
        "layout": "title-table",
        "title": "产品定价方案",
        "table": {
            "headers": ["方案", "适用规模", "月费（元）", "包含积分", "支持"],
            "rows": [
                ["入门版", "1–10 人", "¥999", "5,000", "邮件"],
                ["专业版", "11–50 人", "¥3,999", "30,000", "专属 CSM"],
                ["企业版", "51–200 人", "¥12,999", "150,000", "驻场 + SLA"],
                ["旗舰版", "200+ 人", "定制报价", "无限", "专属团队"],
            ],
        },
    },

    # 7. Two-column — product screenshot + bullets
    {
        "layout": "two-column",
        "title": "产品核心模块",
        "bullet_points": [
            "SOP 流程引擎：可视化编排，AI 节点一键接入",
            "SalesRAG 知识库：多格式文档解析 + 向量检索",
            "积分计费系统：Reserve/Reconcile 两阶段计费",
            "Agent 工作台：工具调用 + sandbox 安全执行",
        ],
        # image is optional; omit if no file available in sandbox
        # "image": {"path": "product_screenshot.jpg", "caption": "Agent 工作台界面"},
    },

    # 8. End
    {
        "layout": "end",
        "title": "期待合作",
        "subtitle": "contact@youshu.ai  ·  400-888-0000",
    },
]

# --------------------------------------------------------------------------
# Build deck
# --------------------------------------------------------------------------

merged_cfg = merge_brand_config(USER_BRAND_CONFIG)
brand = BrandConfig.from_dict(merged_cfg)

builder = DeckBuilder(brand=brand)
for slide_def in slides:
    builder.add_slide(slide_def)
builder.apply_brand()

os.makedirs("/output", exist_ok=True)
builder.save(OUTPUT_PATH)

print(f"Done! Slides: {builder.slide_count}")
if builder.warnings:
    print("Warnings:")
    for w in builder.warnings:
        print(f"  - {w}")
print(f"Output: {OUTPUT_PATH}")
