"""
chart.py — Chart embedding helpers for xlsx-author skill.

Two families of helpers are provided:

1. **openpyxl native charts** (BarChart / LineChart / PieChart):
   Fast, produce interactive Excel charts.  Compatibility with WPS/LibreOffice
   is limited; use the matplotlib fallback when cross-app fidelity matters.

2. **matplotlib-to-PNG embedding** (chart_from_matplotlib):
   Renders any matplotlib Figure to a PNG and inserts it as an image in the
   worksheet.  Works in every xlsx viewer but is not interactive.

Data range convention:
   All ``data_range`` parameters accept an Excel-style range string such as
   ``"A1:D10"`` (without sheet prefix).  Internally the helpers call
   ``_parse_range`` to convert to openpyxl Reference objects.
"""

from __future__ import annotations

import io
from typing import Optional

import openpyxl
from openpyxl.chart import BarChart, LineChart, PieChart, Reference, Series
from openpyxl.drawing.image import Image
from openpyxl.utils.cell import coordinate_from_string, column_index_from_string
from openpyxl.workbook.workbook import Workbook
from openpyxl.worksheet.worksheet import Worksheet

# Maximum safe column index (well below Excel's 16384 limit).
_MAX_SAFE_COL = 200


def _parse_range(data_range: str) -> tuple[int, int, int, int]:
    """Convert a range string like "A1:D10" to (min_row, min_col, max_row, max_col).

    Column indices are 1-based.  Raises ``ValueError`` for malformed input.
    """
    if ":" not in data_range:
        # Single cell: treat as 1x1 range
        col_str, row = coordinate_from_string(data_range)
        col = column_index_from_string(col_str)
        return row, col, row, col

    start, end = data_range.split(":", 1)
    start_col_str, start_row = coordinate_from_string(start)
    end_col_str, end_row = coordinate_from_string(end)
    return (
        start_row,
        column_index_from_string(start_col_str),
        end_row,
        column_index_from_string(end_col_str),
    )


def _clamp_col(col: int) -> int:
    """Clamp a column index to the safe maximum to prevent xlsx corruption."""
    return min(col, _MAX_SAFE_COL)


def embed_bar_chart(
    wb: Workbook,
    data_sheet_name: str,
    data_range: str,
    title: str,
    anchor_cell: str,
    target_sheet_name: Optional[str] = None,
    series_labels: Optional[list[str]] = None,
    width_cm: float = 15.0,
    height_cm: float = 10.0,
) -> None:
    """Embed an openpyxl BarChart (clustered, vertical bars) into a worksheet.

    Args:
        wb:                Workbook object.
        data_sheet_name:   Name of the sheet containing the source data.
        data_range:        Excel range string for the chart data, e.g. "A1:D10".
                           Row 1 is treated as the category axis (e.g. months);
                           columns 2..N are data series.
        title:             Chart title shown above the chart.
        anchor_cell:       Top-left cell where the chart is placed, e.g. "F2".
        target_sheet_name: Sheet to place the chart on.  Defaults to
                           ``data_sheet_name`` when None.
        series_labels:     Optional list of series names; if None, the chart
                           uses column headers from row 1 of data_range.
        width_cm:          Chart width in centimetres (default 15).
        height_cm:         Chart height in centimetres (default 10).

    Note:
        openpyxl BarChart compatibility with WPS/LibreOffice may vary.
        Use ``chart_from_matplotlib`` for guaranteed cross-app rendering.
    """
    data_ws = wb[data_sheet_name]
    min_row, min_col, max_row, max_col = _parse_range(data_range)
    min_col = _clamp_col(min_col)
    max_col = _clamp_col(max_col)

    chart = BarChart()
    chart.type = "col"
    chart.grouping = "clustered"
    chart.title = title
    chart.style = 10
    chart.y_axis.title = ""
    chart.x_axis.title = ""
    chart.width = width_cm
    chart.height = height_cm

    # Data reference (excludes the first column which is category labels)
    data_ref = Reference(
        data_ws,
        min_row=min_row,
        max_row=max_row,
        min_col=min_col + 1,  # skip category column
        max_col=max_col,
    )

    # Category labels reference (first column)
    cats = Reference(
        data_ws,
        min_row=min_row + 1,  # skip header row for categories
        max_row=max_row,
        min_col=min_col,
        max_col=min_col,
    )

    chart.add_data(data_ref, titles_from_data=True)
    chart.set_categories(cats)

    # Override series labels if provided
    if series_labels:
        for idx, label in enumerate(series_labels):
            if idx < len(chart.series):
                chart.series[idx].title = openpyxl.chart.label.SeriesLabel(v=label)  # type: ignore[attr-defined]

    target_ws = wb[target_sheet_name] if target_sheet_name else data_ws
    target_ws.add_chart(chart, anchor_cell)


def embed_line_chart(
    wb: Workbook,
    data_sheet_name: str,
    data_range: str,
    title: str,
    anchor_cell: str,
    target_sheet_name: Optional[str] = None,
    series_labels: Optional[list[str]] = None,
    smooth: bool = True,
    width_cm: float = 15.0,
    height_cm: float = 10.0,
) -> None:
    """Embed an openpyxl LineChart into a worksheet.

    Args:
        wb:                Workbook object.
        data_sheet_name:   Name of the sheet containing the source data.
        data_range:        Excel range string, e.g. "A1:D10".
        title:             Chart title.
        anchor_cell:       Placement cell, e.g. "P2".
        target_sheet_name: Sheet to place the chart on; defaults to data sheet.
        series_labels:     Optional override for series names.
        smooth:            Whether to draw smooth (bezier) lines (default True).
        width_cm:          Chart width in centimetres.
        height_cm:         Chart height in centimetres.
    """
    data_ws = wb[data_sheet_name]
    min_row, min_col, max_row, max_col = _parse_range(data_range)
    min_col = _clamp_col(min_col)
    max_col = _clamp_col(max_col)

    chart = LineChart()
    chart.title = title
    chart.style = 10
    chart.smooth = smooth
    chart.width = width_cm
    chart.height = height_cm

    data_ref = Reference(
        data_ws,
        min_row=min_row,
        max_row=max_row,
        min_col=min_col + 1,
        max_col=max_col,
    )
    cats = Reference(
        data_ws,
        min_row=min_row + 1,
        max_row=max_row,
        min_col=min_col,
        max_col=min_col,
    )

    chart.add_data(data_ref, titles_from_data=True)
    chart.set_categories(cats)

    if series_labels:
        for idx, label in enumerate(series_labels):
            if idx < len(chart.series):
                chart.series[idx].title = openpyxl.chart.label.SeriesLabel(v=label)  # type: ignore[attr-defined]

    target_ws = wb[target_sheet_name] if target_sheet_name else data_ws
    target_ws.add_chart(chart, anchor_cell)


def embed_pie_chart(
    wb: Workbook,
    data_sheet_name: str,
    data_range: str,
    title: str,
    anchor_cell: str,
    target_sheet_name: Optional[str] = None,
    width_cm: float = 15.0,
    height_cm: float = 10.0,
) -> None:
    """Embed an openpyxl PieChart into a worksheet.

    The data range should contain two columns: labels (col 1) and values (col 2).
    Example data_range: "A1:B6" where A is category names and B is values.

    Args:
        wb:                Workbook object.
        data_sheet_name:   Name of the sheet containing source data.
        data_range:        Two-column range: first col = labels, second = values.
        title:             Chart title.
        anchor_cell:       Placement cell.
        target_sheet_name: Sheet to place the chart on; defaults to data sheet.
        width_cm:          Chart width in centimetres.
        height_cm:         Chart height in centimetres.
    """
    data_ws = wb[data_sheet_name]
    min_row, min_col, max_row, max_col = _parse_range(data_range)
    min_col = _clamp_col(min_col)
    max_col = _clamp_col(max_col)

    chart = PieChart()
    chart.title = title
    chart.style = 10
    chart.width = width_cm
    chart.height = height_cm

    # Values: second column
    value_col = min(min_col + 1, max_col)
    data_ref = Reference(
        data_ws,
        min_row=min_row + 1,  # skip header
        max_row=max_row,
        min_col=value_col,
        max_col=value_col,
    )
    # Labels: first column
    cats = Reference(
        data_ws,
        min_row=min_row + 1,
        max_row=max_row,
        min_col=min_col,
        max_col=min_col,
    )

    chart.add_data(data_ref)
    chart.set_categories(cats)

    target_ws = wb[target_sheet_name] if target_sheet_name else data_ws
    target_ws.add_chart(chart, anchor_cell)


def chart_from_matplotlib(
    fig,  # matplotlib.figure.Figure (not typed to avoid hard import)
    ws: Worksheet,
    anchor_cell: str,
    dpi: int = 150,
) -> None:
    """Render a matplotlib Figure as PNG and embed it as an image in *ws*.

    Use this when openpyxl native charts are not compatible with the target
    xlsx viewer (e.g. WPS Mobile, LibreOffice Calc), or when the chart type
    is not supported natively by openpyxl.

    Chinese characters require the sandbox font to be configured before calling
    this function::

        import matplotlib
        matplotlib.rcParams['font.family'] = ['WenQuanYi Zen Hei', 'DejaVu Sans']

    The sandbox base image pre-installs ``fonts-wqy-zenhei``; the rcParam above
    activates it.

    Args:
        fig:         A ``matplotlib.figure.Figure`` object ready to render.
        ws:          Target worksheet where the image will be placed.
        anchor_cell: Top-left cell for the image, e.g. "F2".
        dpi:         Render resolution (default 150 — good balance of quality
                     vs file size; use 72 for draft quality).

    Example::

        import matplotlib.pyplot as plt
        from helpers.chart import chart_from_matplotlib

        fig, ax = plt.subplots()
        ax.bar(["A", "B", "C"], [10, 20, 15])
        chart_from_matplotlib(fig, ws, "F2")
        plt.close(fig)

    Note:
        After calling this function, close the figure with ``plt.close(fig)`` to
        free memory.
    """
    buf = io.BytesIO()
    fig.savefig(buf, format="png", dpi=dpi, bbox_inches="tight")
    buf.seek(0)
    img = Image(buf)
    ws.add_image(img, anchor_cell)
