"""
helpers/renderer.py — Core weasyprint rendering layer.

render_html_to_pdf: HTML string → PDF file on disk.

Design notes:
- Page size / orientation / margins are injected as a CSS @page rule prepended to
  the HTML <head>; we do NOT rely on weasyprint Python API keyword arguments
  (those are less stable across minor weasyprint versions).
- weasyprint emits many harmless warnings (unsupported CSS properties, unknown
  MIME types, font substitution). We silence them via Python's warnings module so
  caller output stays clean. Real errors still surface as exceptions.
- CJK font is always appended to body font-family stack: WenQuanYi Zen Hei is
  pre-installed in the sandbox base image (fonts-wqy-zenhei package).
- base_url defaults to /workspace/input so <img src="logo.png"> resolves to
  /workspace/input/logo.png without agent needing to use absolute paths.
"""

import warnings
from pathlib import Path

import weasyprint


def render_html_to_pdf(
    html_content: str,
    output_path: str,
    base_url: str = "/workspace/input",
    extra_css: str | None = None,
    page_size: str = "A4",
    orientation: str = "portrait",
    margin_top_cm: float = 2.0,
    margin_bottom_cm: float = 2.0,
    margin_left_cm: float = 2.0,
    margin_right_cm: float = 2.0,
) -> None:
    """
    Render an HTML string to a PDF file using weasyprint.

    CSS @page rules for page size and margins are injected into the HTML <head>
    before rendering. The body font-family is also injected to ensure CJK
    characters render correctly with the sandbox-pre-installed WenQuanYi Zen Hei
    font.

    Parameters
    ----------
    html_content:
        Complete HTML string (should include <!DOCTYPE html> and <head>).
    output_path:
        Absolute path where the PDF will be written.
    base_url:
        Base URL passed to weasyprint for resolving relative resource paths
        (images, fonts). Defaults to "/workspace/input".
    extra_css:
        Optional additional CSS string appended after the injected @page CSS.
        Useful for one-off overrides without modifying the HTML template.
    page_size:
        CSS page size keyword: "A4" | "A3" | "Letter" | "Legal". Default "A4".
    orientation:
        "portrait" (default) or "landscape".
    margin_top_cm / margin_bottom_cm / margin_left_cm / margin_right_cm:
        Page margins in centimetres. Default 2.0 cm each side.
    """
    page_css = _build_page_css(
        page_size, orientation,
        margin_top_cm, margin_bottom_cm,
        margin_left_cm, margin_right_cm,
    )
    if extra_css:
        page_css += "\n" + extra_css

    html_with_css = _inject_css(html_content, page_css)

    # Suppress the many harmless weasyprint UserWarnings (font substitution,
    # unsupported CSS properties, missing MIME types, etc.).  Real errors raise
    # exceptions and are NOT suppressed.
    with warnings.catch_warnings():
        warnings.filterwarnings("ignore", category=UserWarning)
        pdf_bytes = weasyprint.HTML(
            string=html_with_css,
            base_url=base_url,
        ).write_pdf(
            presentational_hints=True,  # honour HTML presentational attrs like <td align="center">
        )

    output_file = Path(output_path)
    output_file.parent.mkdir(parents=True, exist_ok=True)
    output_file.write_bytes(pdf_bytes)


# ---------------------------------------------------------------------------
# Private helpers
# ---------------------------------------------------------------------------

def _build_page_css(
    size: str,
    orientation: str,
    mt: float,
    mb: float,
    ml: float,
    mr: float,
) -> str:
    """
    Build the CSS @page block and body font-family declaration.

    The @page size string must match the CSS Paged Media spec:
    "A4" for portrait, "A4 landscape" for landscape.
    """
    if orientation == "landscape":
        page_size_str = f"{size} landscape"
    else:
        page_size_str = f"{size} portrait"

    # WenQuanYi Zen Hei is the primary CJK font available in the sandbox image.
    # The remaining entries are fallbacks in case alternate font packages are
    # installed or future sandbox images change.
    cjk_font_stack = (
        "'WenQuanYi Zen Hei', 'Microsoft YaHei', '微软雅黑', "
        "'Noto Sans CJK SC', 'SimHei', 'Arial Unicode MS', sans-serif"
    )
    return (
        f"@page {{\n"
        f"  size: {page_size_str};\n"
        f"  margin: {mt}cm {mr}cm {mb}cm {ml}cm;\n"
        f"}}\n"
        f"body {{\n"
        f"  font-family: {cjk_font_stack};\n"
        f"}}\n"
    )


def _inject_css(html: str, css: str) -> str:
    """
    Inject a <style> block into the HTML document.

    Strategy:
    - If the document has a <head> tag (case-insensitive), insert immediately
      after the opening <head> tag.
    - Otherwise, prepend the <style> block to the document.

    The injected style comes before any existing <style> or <link> elements so
    that template-defined styles take precedence (cascade order).
    """
    style_tag = f"<style>\n{css}\n</style>\n"
    lower = html.lower()
    head_pos = lower.find("<head>")
    if head_pos != -1:
        insert_at = head_pos + len("<head>")
        return html[:insert_at] + "\n" + style_tag + html[insert_at:]
    return style_tag + html
