"""
example_with_table.py — Advanced docx-author example.

Demonstrates:
  - Template loading (general-report.docx) with variable substitution
  - Mixed blocks: headings, paragraphs, ordered/unordered lists, tables
  - Page header and footer with page number field
  - Page breaks between sections
  - Horizontal rule separator

Usage (from the skill root directory):
    python examples/example_with_table.py

Output:
    /output/advanced-report.docx

Note: The "general-report" template is loaded from
/skills/docx-author/templates/general-report.docx inside the sandbox.
For local testing, set TEMPLATE_DIR env var or adjust the template_dir
in load_template_doc() call.
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
    "output_filename": "advanced-report.docx",

    # Template with variable substitution
    "template": "general-report",
    "template_vars": {
        "doc_title": "2026年度业务运营分析报告",
        "abstract": "本报告综合分析本年度各业务线运营数据，提出改进建议。",
        "author": "数据分析团队",
        "generated_date": "2026-05-24",
        "department": "产品与增长部",
        "version": "v1.0",
    },

    # Document metadata
    "metadata": {
        "title": "2026年度业务运营分析报告",
        "author": "数据分析团队",
        "subject": "业务运营",
        "description": "涵盖用户增长、收入分析和产品迭代三个维度的年度报告。",
    },

    # Page header and footer
    "header": {
        "left": "莫小派 AI 工作台",
        "center": "",
        "right": "2026年度分析报告",
    },
    "footer": {
        "left": "机密 — 仅供内部使用",
        "center": "{{page_number}}",
        "right": "2026-05-24",
    },

    # Global style overrides
    "style_config": {
        "font_name": "Microsoft YaHei",
        "font_size_pt": 10.5,
        "line_spacing": 1.5,
        "paragraph_spacing_pt": 6,
        "heading_color": "#1E293B",
        "page_margin_cm": 2.54,
    },

    "blocks": [
        # === Section 1 ===
        {
            "type": "heading",
            "level": 1,
            "text": "第一章  用户增长分析",
        },
        {
            "type": "paragraph",
            "text": (
                "2026年全年注册用户同比增长 47.3%，月均活跃用户（MAU）达到 12.8 万人次，"
                "较去年同期提升 38.5%。用户留存率在 Q3 优化改版后显著改善，30 日留存由 "
                "21% 提升至 34%。"
            ),
        },
        {
            "type": "heading",
            "level": 2,
            "text": "1.1 注册渠道分布",
        },
        {
            "type": "table",
            "headers": ["渠道", "注册量（人）", "占比", "90日留存"],
            "rows": [
                ["有机搜索", 45200, "35.2%", "38%"],
                ["内容营销", 28100, "21.9%", "41%"],
                ["口碑推荐", 22600, "17.6%", "52%"],
                ["付费广告", 18900, "14.7%", "22%"],
                ["合作渠道", 13700, "10.7%", "29%"],
            ],
            "col_widths_cm": [4.5, 4.0, 3.0, 3.5],
        },
        {
            "type": "paragraph",
            "text": (
                "口碑推荐渠道的 90 日留存率最高（52%），建议在 Q1 加大老用户激励计划预算。"
            ),
            "bold": False,
            "italic": True,
        },

        # === Horizontal rule ===
        {"type": "horizontal_rule"},

        # === Section 2 ===
        {
            "type": "heading",
            "level": 1,
            "text": "第二章  收入结构分析",
        },
        {
            "type": "paragraph",
            "text": "全年总收入 ¥3,240 万元，同比增长 61.2%。收入结构持续优化，订阅收入占比升至 78%。",
        },
        {
            "type": "heading",
            "level": 2,
            "text": "2.1 收入构成",
        },
        {
            "type": "list",
            "ordered": True,
            "items": [
                "订阅收入：¥2,527 万元（78.0%），含 B2B 企业版和个人版",
                "加量包收入：¥486 万元（15.0%）",
                "定制化服务：¥162 万元（5.0%）",
                "其他收入：¥65 万元（2.0%）",
            ],
        },
        {
            "type": "heading",
            "level": 2,
            "text": "2.2 季度收入趋势",
        },
        {
            "type": "table",
            "headers": ["季度", "订阅收入", "加量包收入", "服务收入", "合计"],
            "rows": [
                ["Q1", "¥521万", "¥98万", "¥39万", "¥658万"],
                ["Q2", "¥598万", "¥110万", "¥40万", "¥748万"],
                ["Q3", "¥672万", "¥128万", "¥41万", "¥841万"],
                ["Q4", "¥736万", "¥150万", "¥42万", "¥993万"],
            ],
        },

        # === Page break ===
        {"type": "page_break"},

        # === Section 3 ===
        {
            "type": "heading",
            "level": 1,
            "text": "第三章  产品迭代总结",
        },
        {
            "type": "paragraph",
            "text": "2026 年共完成 3 个大版本迭代（v2.0 / v2.1 / v2.2）和 47 个热修复，核心功能稳定性达 99.7%。",
        },
        {
            "type": "heading",
            "level": 2,
            "text": "3.1 重要里程碑",
        },
        {
            "type": "list",
            "ordered": False,
            "items": [
                "v2.0（2026-01）：发布 Agent Mode v1.0，支持工具调用和 SOP 自动执行",
                "v2.1（2026-04）：完成计费体系 T1-T12 改造，迁移至 credits 积分制",
                "v2.2（2026-08）：推出 Skills 框架，支持 docx/xlsx/pptx/pdf 文档生成",
                "v2.2.1（2026-10）：完成中文字体支持（WenQuanYi + Microsoft YaHei fallback）",
            ],
        },
        {
            "type": "heading",
            "level": 2,
            "text": "3.2 技术债清偿",
        },
        {
            "type": "paragraph",
            "text": "全年累计清偿 18 项技术债，重点包括：",
        },
        {
            "type": "list",
            "ordered": True,
            "items": [
                "移除 legacy_tier 双模式计费（v2.1.23）",
                "credit_package 表归档至 legacy_credit_package_archive_20260515",
                "统一 aiservice 入口，消除 3 处直接 HTTP 调用",
                "sandbox WriteFile/ReadFile stub 去 ErrNotImplemented",
            ],
        },

        # === Section 4 ===
        {
            "type": "heading",
            "level": 1,
            "text": "第四章  结论与建议",
        },
        {
            "type": "paragraph",
            "text": (
                "综合以上分析，建议 2027 年重点投入以下三个方向：\n"
                "1. 加大口碑推荐渠道激励，提升整体留存率；\n"
                "2. 扩展 Agent Skills 库，覆盖更多文档场景（PPT、合同模板）；\n"
                "3. 推进 B2B 企业版定制化，提高客单价和年度合同比例。"
            ),
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
