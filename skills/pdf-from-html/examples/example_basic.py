"""
example_basic.py — Simplest usage: pass html_content directly.

This example demonstrates generating a PDF without any template or Jinja2 by
passing a raw HTML string.  Suitable when the agent has dynamically assembled
the HTML content in memory.

Run inside the sandbox:
    python /skills/pdf-from-html/examples/example_basic.py
"""

import json
import sys
from pathlib import Path

# Allow running from the skill directory without install
sys.path.insert(0, str(Path(__file__).parent.parent))

from main import run

HTML = """<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<title>示例报告</title>
<style>
  body { font-size: 11pt; line-height: 1.8; }
  h1   { color: #1E293B; margin-bottom: 0.5em; }
  p    { text-indent: 2em; margin-bottom: 0.8em; }
  table { width: 100%; border-collapse: collapse; margin: 1em 0; }
  th { background: #1E293B; color: white; padding: 0.4em 0.6em; text-align: left; }
  td { border-bottom: 1px solid #E2E8F0; padding: 0.35em 0.6em; }
</style>
</head>
<body>
  <h1>2026年第一季度业务总结</h1>
  <p>
    本报告汇总了有数科技 2026 年第一季度的核心业务指标。本季度在 SOP 自动化领域取得了
    显著突破，用户规模和收入均实现双位数增长。
  </p>
  <p>
    技术层面，莫小派 Agent 模式 V1.5 正式上线，引入 invoke_skill 工具体系，使 AI 能够
    直接生成结构化文档（Excel、Word、PDF），大幅提升内容生产效率。
  </p>

  <h1>关键指标</h1>
  <table>
    <thead>
      <tr><th>指标</th><th>Q4 2025</th><th>Q1 2026</th><th>环比增长</th></tr>
    </thead>
    <tbody>
      <tr><td>月活用户数</td><td>1,200</td><td>1,850</td><td>+54%</td></tr>
      <tr><td>SOP 执行次数</td><td>48,000</td><td>72,000</td><td>+50%</td></tr>
      <tr><td>付费客户数</td><td>85</td><td>130</td><td>+53%</td></tr>
      <tr><td>月度 MRR（万元）</td><td>12.8</td><td>19.5</td><td>+52%</td></tr>
    </tbody>
  </table>
</body>
</html>"""


def main() -> None:
    params = {
        "output_filename": "example_basic_report.pdf",
        "html_content": HTML,
        "pdf_options": {
            "page_size": "A4",
            "orientation": "portrait",
            "margin_top_cm": 2.5,
            "margin_bottom_cm": 2.5,
            "margin_left_cm": 2.5,
            "margin_right_cm": 2.0,
        },
    }

    result = run(params)
    print(json.dumps(result, ensure_ascii=False, indent=2))

    if result["success"]:
        print(f"\n✓ PDF 生成成功: {result['output_path']}")
        print(f"  文件大小: {result['output_size_bytes']:,} 字节")
        print(f"  页数: {result['page_count']}")
    else:
        print(f"\n✗ 生成失败: {result['error']}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
