"""
main.py — Entry point for the docx-author sandbox skill.

The sandbox runner executes this module via:
    python /skills/docx-author/main.py

The ``run(params)`` function is the canonical interface.  It reads structured
parameters, builds a Word document, saves it to /output/, and returns a result
dict.  All errors are caught and returned as ``{"success": False, "error": ...}``.

File paths:
    Input files  → /workspace/input/<filename>
    Output files → /output/<filename>
    Templates    → /skills/docx-author/templates/

See SKILL.md for the full parameter contract and usage examples.
"""

from __future__ import annotations

import json
import os
import re
import sys

# Ensure the skill helpers are importable when run directly
_SKILL_DIR = os.path.dirname(os.path.abspath(__file__))
if _SKILL_DIR not in sys.path:
    sys.path.insert(0, _SKILL_DIR)

from helpers.builder import DocxBuilder
from helpers.template import load_template_doc


# ---------------------------------------------------------------------------
# Filename sanitisation
# ---------------------------------------------------------------------------

_ILLEGAL_CHARS_RE = re.compile(r'[\\/:*?"<>|]')
_DOTDOT_RE = re.compile(r"\.\.")


def sanitize_filename(name: str) -> str:
    """Strip illegal filesystem characters and path traversal sequences.

    Keeps Unicode characters (including Chinese) intact, removes only the
    characters forbidden in filenames on Windows/Linux: \\ / : * ? " < > |
    Also strips any ".." sequences to prevent path traversal.

    Args:
        name: Proposed output filename (e.g. "Q2报告.docx").

    Returns:
        A safe filename (basename only, no directory components).
    """
    # Extract basename only (no directory traversal)
    name = os.path.basename(name)
    # Remove ".." sequences
    name = _DOTDOT_RE.sub("", name)
    # Remove illegal characters
    name = _ILLEGAL_CHARS_RE.sub("", name)
    # Ensure non-empty fallback
    return name.strip() or "output.docx"


# ---------------------------------------------------------------------------
# Main entry point
# ---------------------------------------------------------------------------


def run(params: dict) -> dict:
    """Build a Word document from structured parameters.

    Args:
        params: Parameter dict per the invoke_skill contract.  Required key:
                "output_filename".  See SKILL.md for the full schema.

    Returns:
        Result dict:
            {
              "success": bool,
              "output_path": str,       # set on success
              "output_size_bytes": int, # set on success
              "blocks_rendered": int,   # set on success
              "error": str | None,
            }
    """
    output_filename = sanitize_filename(params.get("output_filename", "output.docx"))
    if not output_filename.lower().endswith(".docx"):
        output_filename += ".docx"

    output_path = os.path.join("/output", output_filename)

    try:
        template_name = params.get("template")
        template_vars = params.get("template_vars") or {}
        style_config = params.get("style_config") or {}

        if template_name:
            existing_doc = load_template_doc(
                template_name=template_name,
                template_vars=template_vars,
            )
            builder = DocxBuilder(style_config=style_config, existing_doc=existing_doc)
        else:
            builder = DocxBuilder(style_config=style_config)

        # Metadata
        metadata = params.get("metadata") or {}
        if metadata:
            builder.set_metadata(metadata)

        # Header / footer
        header = params.get("header") or {}
        footer = params.get("footer") or {}
        if header or footer:
            builder.set_header_footer(header=header, footer=footer)

        # Render blocks
        blocks_rendered = 0
        for block in params.get("blocks") or []:
            builder.add_block(block)
            blocks_rendered += 1

        builder.save(output_path)

        return {
            "success": True,
            "output_path": output_path,
            "output_size_bytes": os.path.getsize(output_path),
            "blocks_rendered": blocks_rendered,
            "error": None,
        }

    except FileNotFoundError as exc:
        return {
            "success": False,
            "output_path": None,
            "output_size_bytes": 0,
            "blocks_rendered": 0,
            "error": f"File not found: {exc}",
        }
    except Exception as exc:  # noqa: BLE001
        return {
            "success": False,
            "output_path": None,
            "output_size_bytes": 0,
            "blocks_rendered": 0,
            "error": str(exc),
        }


# ---------------------------------------------------------------------------
# CLI runner (for sandbox execution)
# ---------------------------------------------------------------------------

if __name__ == "__main__":
    # The sandbox runner injects params via stdin as JSON
    raw = sys.stdin.read().strip()
    if raw:
        try:
            params_in = json.loads(raw)
        except json.JSONDecodeError as e:
            result = {
                "success": False,
                "output_path": None,
                "output_size_bytes": 0,
                "blocks_rendered": 0,
                "error": f"Invalid JSON input: {e}",
            }
            print(json.dumps(result, ensure_ascii=False))
            sys.exit(1)
    else:
        params_in = {}

    result_out = run(params_in)
    print(json.dumps(result_out, ensure_ascii=False))
    sys.exit(0 if result_out["success"] else 1)
