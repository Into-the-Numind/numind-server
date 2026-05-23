"""
pdf-from-html skill — entry point.

Invoked by the sandbox runner as:
    python /skills/pdf-from-html/main.py

Parameters are read from /workspace/input/params.json (injected by the skill
framework).  Output is written to /output/<output_filename>.

Input contract (see manifest.json and SKILL.md for full documentation):
  {
    "output_filename": "report.pdf",          # required

    # ONE of the following three (priority: html_content > template > html_file)
    "html_content": "<html>...</html>",        # raw HTML string
    "template": "invoice",                     # built-in template name
    "html_file": "my_template.html",           # relative to /workspace/input/

    "template_vars": { ... },                  # Jinja2 variables (for template/html_file)
    "pdf_options": {
        "page_size": "A4",                     # default "A4"
        "orientation": "portrait",             # default "portrait"
        "margin_top_cm": 2.0,                  # default 2.0
        "margin_bottom_cm": 2.0,
        "margin_left_cm": 2.0,
        "margin_right_cm": 2.0,
        "base_url": "/workspace/input"         # default
    },
    "extra_css": "body { font-size: 9pt; }"   # optional, appended to injected CSS
  }

Return value (written to stdout as JSON):
  {
    "success": true,
    "output_path": "/output/report.pdf",
    "output_size_bytes": 123456,
    "page_count": 3,
    "error": null
  }
"""

from __future__ import annotations

import json
import os
import re
import sys
from pathlib import Path


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _sanitize_filename(name: str) -> str:
    """
    Remove / replace characters that are unsafe in filenames.

    - Strips path separators and null bytes.
    - Collapses whitespace to underscore.
    - Limits total length to 200 characters.
    """
    # Remove null bytes and path separators
    name = name.replace("\x00", "").replace("/", "_").replace("\\", "_")
    # Collapse runs of whitespace
    name = re.sub(r"\s+", "_", name.strip())
    # Limit length
    if len(name) > 200:
        stem = Path(name).stem[:190]
        suffix = Path(name).suffix
        name = stem + suffix
    return name or "output.pdf"


def _load_params() -> dict:
    """Load parameters from /workspace/input/params.json."""
    params_path = "/workspace/input/params.json"
    try:
        with open(params_path, encoding="utf-8") as f:
            return json.load(f)
    except FileNotFoundError:
        # Fallback: try reading from stdin (useful for local testing)
        return json.load(sys.stdin)


def _error_result(message: str) -> dict:
    return {
        "success": False,
        "output_path": None,
        "output_size_bytes": 0,
        "page_count": 0,
        "error": message,
    }


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def run(params: dict) -> dict:
    """
    Core skill logic.  Separated from __main__ block for testability.
    """
    from helpers.renderer import render_html_to_pdf
    from helpers.template import render_template
    from helpers.pdf_meta import get_page_count

    # ── Step 0: Validate required param ──────────────────────────────────
    output_filename = params.get("output_filename")
    if not output_filename:
        return _error_result("必须提供 output_filename 参数")
    output_filename = _sanitize_filename(output_filename)
    if not output_filename.lower().endswith(".pdf"):
        output_filename += ".pdf"

    output_path = f"/output/{output_filename}"

    # ── Step 1: Resolve HTML content ─────────────────────────────────────
    template_vars = params.get("template_vars") or {}

    if "html_content" in params:
        html = params["html_content"]
        if not html or not html.strip():
            return _error_result("html_content 不能为空字符串")

    elif "template" in params:
        template_name = params["template"]
        try:
            html = render_template(template_name, template_vars)
        except Exception as exc:
            return _error_result(f"模板渲染失败 (template={template_name!r}): {exc}")

    elif "html_file" in params:
        html_file_rel = params["html_file"]
        html_path = f"/workspace/input/{html_file_rel}"
        try:
            raw = Path(html_path).read_text(encoding="utf-8")
        except FileNotFoundError:
            return _error_result(f"html_file 不存在: {html_file_rel}")
        except Exception as exc:
            return _error_result(f"读取 html_file 失败: {exc}")
        # Render as Jinja2 template if template_vars provided
        if template_vars:
            try:
                from jinja2 import Template, StrictUndefined
                html = Template(raw, undefined=StrictUndefined).render(**template_vars)
            except Exception as exc:
                return _error_result(f"html_file Jinja2 渲染失败: {exc}")
        else:
            html = raw

    else:
        return _error_result(
            "必须提供以下参数之一：html_content / template / html_file"
        )

    # ── Step 2: Extract PDF options ───────────────────────────────────────
    opts = params.get("pdf_options") or {}
    page_size = opts.get("page_size", "A4")
    orientation = opts.get("orientation", "portrait")
    margin_top_cm = float(opts.get("margin_top_cm", 2.0))
    margin_bottom_cm = float(opts.get("margin_bottom_cm", 2.0))
    margin_left_cm = float(opts.get("margin_left_cm", 2.0))
    margin_right_cm = float(opts.get("margin_right_cm", 2.0))
    base_url = opts.get("base_url", "/workspace/input")
    extra_css = params.get("extra_css")

    # ── Step 3: Render PDF ────────────────────────────────────────────────
    try:
        render_html_to_pdf(
            html_content=html,
            output_path=output_path,
            base_url=base_url,
            extra_css=extra_css,
            page_size=page_size,
            orientation=orientation,
            margin_top_cm=margin_top_cm,
            margin_bottom_cm=margin_bottom_cm,
            margin_left_cm=margin_left_cm,
            margin_right_cm=margin_right_cm,
        )
    except Exception as exc:
        return _error_result(f"PDF 渲染失败: {exc}")

    # ── Step 4: Collect metadata ──────────────────────────────────────────
    try:
        file_size = os.path.getsize(output_path)
    except OSError:
        file_size = 0

    page_count = get_page_count(output_path)

    return {
        "success": True,
        "output_path": output_path,
        "output_size_bytes": file_size,
        "page_count": page_count,
        "error": None,
    }


if __name__ == "__main__":
    params = _load_params()
    result = run(params)
    print(json.dumps(result, ensure_ascii=False, indent=2))
