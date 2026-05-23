"""
helpers/template.py — Jinja2 template rendering layer.

render_template: template name → rendered HTML string.

Design notes:
- Templates live in /skills/pdf-from-html/templates/ as *.html.j2 files.
  The TEMPLATE_DIR constant can be overridden in tests by patching.
- StrictUndefined is used intentionally: if a required template variable is
  missing the call fails loudly rather than silently rendering an empty string.
  Optional template variables must be guarded with {% if var %}...{% endif %}.
- autoescaping is enabled for .html and .j2 extensions: template variables are
  HTML-escaped by default. To embed trusted HTML fragments, mark them with
  | safe in the template: {{ var | safe }}.
- Two custom filters are registered:
    currency: float → "¥1,234.50" formatted string
    date_cn:  ISO date string → Chinese date "2026年5月24日"
"""

from __future__ import annotations

from datetime import datetime
from pathlib import Path

from jinja2 import (
    Environment,
    FileSystemLoader,
    StrictUndefined,
    select_autoescape,
)

# Default template directory (container path; overridden in tests via the
# template_dir parameter).
_DEFAULT_TEMPLATE_DIR = "/skills/pdf-from-html/templates"


def render_template(
    template_name: str,
    vars: dict,
    template_dir: str = _DEFAULT_TEMPLATE_DIR,
) -> str:
    """
    Render a named Jinja2 HTML template with the supplied variable mapping.

    The template file must exist at {template_dir}/{template_name}.html.j2.

    Parameters
    ----------
    template_name:
        One of "invoice", "report", "certificate" (or any .html.j2 file base
        name without extension).
    vars:
        Dictionary of template variables. Missing required variables cause
        jinja2.UndefinedError (StrictUndefined mode).
    template_dir:
        Override the template directory; useful in unit tests.

    Returns
    -------
    str
        Fully rendered HTML document ready for weasyprint.
    """
    env = _build_env(template_dir)
    template_file = f"{template_name}.html.j2"
    template = env.get_template(template_file)
    return template.render(**vars)


def _build_env(template_dir: str) -> Environment:
    """Construct the Jinja2 Environment with project-standard settings."""
    env = Environment(
        loader=FileSystemLoader(template_dir),
        autoescape=select_autoescape(["html", "j2"]),
        undefined=StrictUndefined,
        trim_blocks=True,
        lstrip_blocks=True,
    )
    env.filters["currency"] = _currency_filter
    env.filters["date_cn"] = _date_cn_filter
    return env


# ---------------------------------------------------------------------------
# Custom Jinja2 filters
# ---------------------------------------------------------------------------

def _currency_filter(value: float | int | str, symbol: str = "¥") -> str:
    """
    Format a numeric value as a currency string.

    Examples
    --------
    >>> _currency_filter(1234.5)
    '¥1,234.50'
    >>> _currency_filter(0, symbol="$")
    '$0.00'
    """
    try:
        num = float(value)
    except (ValueError, TypeError):
        return str(value)
    return f"{symbol}{num:,.2f}"


def _date_cn_filter(value: str) -> str:
    """
    Convert an ISO date string (YYYY-MM-DD) to Chinese date notation.

    Examples
    --------
    >>> _date_cn_filter("2026-05-24")
    '2026年5月24日'
    >>> _date_cn_filter("invalid")
    'invalid'
    """
    try:
        dt = datetime.strptime(str(value), "%Y-%m-%d")
        return f"{dt.year}年{dt.month}月{dt.day}日"
    except (ValueError, TypeError):
        return str(value)
