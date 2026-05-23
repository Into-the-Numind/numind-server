"""
style.py — Font and paragraph style application helpers.

Provides functions for applying document-level default styles and per-section
formatting to python-docx Document objects.

Decision T12: Chinese fonts use the sandbox-preinstalled WenQuanYi Zen Hei
font (fonts-wqy-zenhei package) as a fallback.  When rendered in Microsoft
Word on Windows, "微软雅黑" (Microsoft YaHei) is used.  Both are declared via
the eastAsia XML attribute so each environment picks the best available font.
"""

from __future__ import annotations

from docx import Document
from docx.enum.text import WD_LINE_SPACING
from docx.oxml.ns import qn
from docx.shared import Cm, Pt, RGBColor


# ---------------------------------------------------------------------------
# Page margins
# ---------------------------------------------------------------------------


def set_page_margins(doc: Document, margin_cm: float = 2.54) -> None:
    """Set uniform page margins on all sections in the document.

    Args:
        doc: python-docx Document object.
        margin_cm: Margin size in centimetres applied to top, bottom, left, and
                   right of every page.  Default is 2.54 cm (1 inch).
    """
    for section in doc.sections:
        margin = Cm(margin_cm)
        section.top_margin = margin
        section.bottom_margin = margin
        section.left_margin = margin
        section.right_margin = margin


# ---------------------------------------------------------------------------
# Default body font
# ---------------------------------------------------------------------------


def set_default_font(
    doc: Document,
    font_name: str = "微软雅黑",
    font_size_pt: float = 10.5,
) -> None:
    """Set the document-level default font via the Normal style.

    Modifies the "Normal" paragraph style so that new paragraphs created
    without an explicit style inherit the desired font and size.

    The font is applied to both the Latin (w:rFonts/@w:ascii) and East-Asian
    (w:rFonts/@w:eastAsia) slots.  This ensures Chinese characters render in
    the same typeface as Latin characters instead of falling back to the
    system's default CJK font.

    Note: This does *not* retroactively change runs already added to the
    document.  Call this function immediately after creating a new Document
    before adding any content.

    Args:
        doc: python-docx Document object.
        font_name: Font name (e.g. "微软雅黑", "WenQuanYi Zen Hei").
        font_size_pt: Body text size in points (e.g. 10.5 for 五号字).
    """
    style = doc.styles["Normal"]
    font = style.font
    font.name = font_name
    font.size = Pt(font_size_pt)

    # Also set the East-Asian font slot in XML
    rPr = style.element.get_or_add_rPr()
    rFonts = rPr.get_or_add_rFonts()
    rFonts.set(qn("w:eastAsia"), font_name)


# ---------------------------------------------------------------------------
# Heading colours
# ---------------------------------------------------------------------------


def set_heading_color(
    doc: Document,
    color_hex: str = "#1E293B",
    levels: tuple[int, ...] = (1, 2, 3),
) -> None:
    """Set the font colour for heading styles at the specified levels.

    Modifies the "Heading N" styles so headings added via ``doc.add_heading``
    inherit the specified colour without needing per-run colour overrides.

    Args:
        doc: python-docx Document object.
        color_hex: Hex colour string (with or without leading '#').
        levels: Tuple of heading levels to apply the colour to.
    """
    hex_clean = color_hex.lstrip("#")
    r, g, b = (int(hex_clean[i : i + 2], 16) for i in (0, 2, 4))
    colour = RGBColor(r, g, b)

    for level in levels:
        style_name = f"Heading {level}"
        try:
            style = doc.styles[style_name]
            style.font.color.rgb = colour
            style.font.name = "微软雅黑"
            rPr = style.element.get_or_add_rPr()
            rFonts = rPr.get_or_add_rFonts()
            rFonts.set(qn("w:eastAsia"), "微软雅黑")
        except KeyError:
            # Style not present in this document — skip silently
            pass


# ---------------------------------------------------------------------------
# Apply all defaults from a style_config dict
# ---------------------------------------------------------------------------


def apply_style_config(doc: Document, style_config: dict) -> None:
    """Apply a ``style_config`` dict (from the invoke_skill params) to a Document.

    Expected keys (all optional):
        font_name (str): Default body font.  Default "微软雅黑".
        font_size_pt (int|float): Body font size in points.  Default 10.5.
        line_spacing (float): Line spacing multiplier.  (Note: line spacing is
            applied per-paragraph, not document-wide, so this is stored for
            DocxBuilder to use when adding paragraphs.)
        paragraph_spacing_pt (int): Space before/after paragraphs in pt.
        heading_color (str): Hex colour for heading levels 1–3.
        page_margin_cm (float): Page margin in cm.

    Unrecognised keys are silently ignored.

    Args:
        doc: python-docx Document object.
        style_config: Dict with optional style overrides.
    """
    if not style_config:
        return

    font_name = style_config.get("font_name", "微软雅黑")
    font_size_pt = float(style_config.get("font_size_pt", 10.5))
    heading_color = style_config.get("heading_color", "#1E293B")
    page_margin_cm = float(style_config.get("page_margin_cm", 2.54))

    set_default_font(doc, font_name=font_name, font_size_pt=font_size_pt)
    set_heading_color(doc, color_hex=heading_color)
    set_page_margins(doc, margin_cm=page_margin_cm)
