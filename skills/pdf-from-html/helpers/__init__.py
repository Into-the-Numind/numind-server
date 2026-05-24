"""
pdf-from-html helpers package.

Re-exports the three core functions used by main.py:
- render_html_to_pdf: weasyprint HTML-string → PDF file
- render_template: jinja2 template name → HTML string
- get_page_count: read page count from a weasyprint-generated PDF
"""

from .renderer import render_html_to_pdf
from .template import render_template
from .pdf_meta import get_page_count

__all__ = ["render_html_to_pdf", "render_template", "get_page_count"]
