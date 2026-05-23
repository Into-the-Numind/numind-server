"""
docx-author helpers package.

Provides the high-level DocxBuilder class and low-level element functions for
generating Word documents (.docx) inside the sandbox environment.

Usage:
    from helpers.builder import DocxBuilder          # high-level chainable API
    from helpers.document import (                   # low-level element functions
        add_heading, add_paragraph, add_table,
        add_list, add_image, add_header_footer,
        add_page_break, add_horizontal_rule,
    )
    from helpers.style import apply_default_styles   # style helpers
    from helpers.template import load_template_doc   # template variable substitution
"""

from .builder import DocxBuilder
from .document import (
    add_heading,
    add_paragraph,
    add_table,
    add_list,
    add_image,
    add_header_footer,
    add_page_break,
    add_horizontal_rule,
)

__all__ = [
    "DocxBuilder",
    "add_heading",
    "add_paragraph",
    "add_table",
    "add_list",
    "add_image",
    "add_header_footer",
    "add_page_break",
    "add_horizontal_rule",
]
