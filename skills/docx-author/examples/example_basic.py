"""
example_basic.py — Minimal docx-author example.

Generates a simple Word document with headings and paragraphs.

Usage (from the skill root directory):
    python examples/example_basic.py

Output:
    /output/basic-example.docx
"""

import os
import sys

# Allow running from any directory
_SKILL_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if _SKILL_DIR not in sys.path:
    sys.path.insert(0, _SKILL_DIR)

os.makedirs("/output", exist_ok=True)

from main import run

params = {
    "output_filename": "basic-example.docx",
    "metadata": {
        "title": "基础示例文档",
        "author": "docx-author skill",
        "subject": "示例",
        "description": "这是一个基础示例，展示标题、段落和表格。",
    },
    "style_config": {
        "font_name": "Microsoft YaHei",
        "font_size_pt": 10.5,
        "line_spacing": 1.5,
        "paragraph_spacing_pt": 6,
        "heading_color": "#1E293B",
        "page_margin_cm": 2.54,
    },
    "blocks": [
        {
            "type": "heading",
            "level": 1,
            "text": "项目概述",
        },
        {
            "type": "paragraph",
            "text": (
                "本文档由 docx-author skill 自动生成，展示基础文档结构：多级标题、"
                "格式化段落和数据表格。所有内容均以结构化参数驱动，无需手动编辑 Word。"
            ),
        },
        {
            "type": "heading",
            "level": 2,
            "text": "背景介绍",
        },
        {
            "type": "paragraph",
            "text": "莫小派 AI 工作台支持多种文档生成场景，包括分析报告、操作手册和会议纪要等。",
        },
        {
            "type": "heading",
            "level": 2,
            "text": "核心功能",
        },
        {
            "type": "list",
            "ordered": False,
            "items": [
                "多级标题（Heading 1–6）",
                "格式化段落（加粗、斜体、对齐、缩进）",
                "数据表格（蓝色表头，可配置列宽）",
                "有序 / 无序列表",
                "图片嵌入（自动保持宽高比）",
                "页眉 / 页脚（含页码域）",
            ],
        },
        {
            "type": "heading",
            "level": 1,
            "text": "数据汇总",
        },
        {
            "type": "table",
            "headers": ["指标", "Q1", "Q2", "Q3"],
            "rows": [
                ["销售额（万元）", 120, 145, 168],
                ["活跃用户数", 3200, 4100, 5600],
                ["NPS 净推荐值", 42, 51, 63],
            ],
            "col_widths_cm": [6.0, 3.0, 3.0, 3.0],
        },
        {
            "type": "paragraph",
            "text": "以上数据来源于内部系统，截止日期 2026-Q3。",
            "italic": True,
        },
    ],
}

result = run(params)
if result["success"]:
    print(f"[OK] Document saved: {result['output_path']}")
    print(f"     Size: {result['output_size_bytes']} bytes")
    print(f"     Blocks rendered: {result['blocks_rendered']}")
else:
    print(f"[ERROR] {result['error']}", file=sys.stderr)
    sys.exit(1)
