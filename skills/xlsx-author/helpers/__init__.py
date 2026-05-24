"""
helpers — xlsx-author skill helper package.

Re-exports the most commonly used functions from style and chart submodules
so skill code can import directly from `helpers`.

Usage::

    from helpers import apply_header_style, auto_column_width, THEME_PRESETS
    from helpers.chart import embed_bar_chart, embed_line_chart, embed_pie_chart
    from helpers.csv_reader import csv_to_sheet
    from helpers.template import load_template
"""

from .style import (
    apply_header_style,
    apply_alt_row_fill,
    auto_column_width,
    apply_number_format,
    sanitize_sheet_name,
    THEME_PRESETS,
)
from .chart import (
    embed_bar_chart,
    embed_line_chart,
    embed_pie_chart,
    chart_from_matplotlib,
)

__all__ = [
    "apply_header_style",
    "apply_alt_row_fill",
    "auto_column_width",
    "apply_number_format",
    "sanitize_sheet_name",
    "THEME_PRESETS",
    "embed_bar_chart",
    "embed_line_chart",
    "embed_pie_chart",
    "chart_from_matplotlib",
]
