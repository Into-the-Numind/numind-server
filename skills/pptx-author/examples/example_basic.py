"""
example_basic.py — Minimal 5-slide PowerPoint example.

Demonstrates:
  - 5 common layouts: cover / title-bullets / title-body / title-chart / end
  - No brand_config (uses DEFAULT_BRAND_CONFIG neutral defaults)
  - Bar chart with sample data

Run inside the sandbox:
    python /skills/pptx-author/examples/example_basic.py
Output written to: /output/example_basic.pptx
"""

import os
import sys

# Resolve helpers path (works both inside sandbox and in local dev)
_skill_dir = os.path.join(os.path.dirname(__file__), "..")
sys.path.insert(0, _skill_dir)

from helpers import DeckBuilder, BrandConfig
from helpers.brand import merge_brand_config

OUTPUT_PATH = "/output/example_basic.pptx"

# --------------------------------------------------------------------------
# Slide definitions (5 slides, no brand_config → neutral defaults)
# --------------------------------------------------------------------------

slides = [
    {
        "layout": "cover",
        "title": "2026 年度业务回顾",
        "subtitle": "2026-05-24  ·  战略发展部",
    },
    {
        "layout": "title-bullets",
        "title": "本次汇报议程",
        "bullet_points": [
            "Q1 销售业绩回顾",
            "Q2 关键里程碑",
            "客户增长分析",
            "下半年重点目标",
            "资源与预算规划",
        ],
    },
    {
        "layout": "title-body",
        "title": "执行摘要",
        "body": (
            "2026 年前两个季度，公司整体收入较去年同期增长 34%，"
            "新客户签约数量达到历史新高，产品满意度 NPS 分数维持在 72 分以上。\n\n"
            "核心驱动因素包括：企业市场拓展、产品迭代加速以及渠道合作伙伴生态建设。"
        ),
        "notes": "在此页花 2 分钟简介数据来源，强调同比口径。",
    },
    {
        "layout": "title-chart",
        "title": "季度收入趋势（万元）",
        "chart": {
            "chart_type": "bar",
            "title": "2025–2026 季度收入对比",
            "data": {
                "categories": ["Q1'25", "Q2'25", "Q3'25", "Q4'25", "Q1'26", "Q2'26"],
                "series": [
                    {"name": "实际收入", "values": [320, 410, 390, 520, 580, 640]},
                    {"name": "目标收入", "values": [300, 380, 420, 500, 550, 620]},
                ],
            },
        },
    },
    {
        "layout": "end",
        "title": "谢谢",
        "subtitle": "如有问题，欢迎交流",
    },
]

# --------------------------------------------------------------------------
# Build deck
# --------------------------------------------------------------------------

brand_cfg = merge_brand_config(None)  # use pure defaults
brand = BrandConfig.from_dict(brand_cfg)

builder = DeckBuilder(brand=brand)
for slide_def in slides:
    builder.add_slide(slide_def)
builder.apply_brand()

os.makedirs("/output", exist_ok=True)
builder.save(OUTPUT_PATH)

print(f"Done! Slides: {builder.slide_count}, Warnings: {builder.warnings}")
print(f"Output: {OUTPUT_PATH}")
