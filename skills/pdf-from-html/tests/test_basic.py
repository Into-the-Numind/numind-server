"""
tests/test_basic.py — pytest unit tests for the pdf-from-html skill.

Test strategy:
- Pure logic tests (CSS building, CSS injection, filter functions) run without
  weasyprint (no system dependency needed).
- Rendering tests (write_pdf) require the weasyprint system libs (libcairo etc.)
  to be installed in the environment. These tests are marked with
  @pytest.mark.integration and are skipped when weasyprint cannot load Cairo.
- Template rendering tests require jinja2 (pip-installable) but NOT weasyprint.
- run() integration tests require both weasyprint AND a writeable /output/ dir;
  they create a temp directory and patch /output accordingly.

Run all tests:
    pytest tests/test_basic.py -v

Run only pure unit tests (no system deps required):
    pytest tests/test_basic.py -v -m "not integration"
"""

from __future__ import annotations

import os
import sys
import tempfile
import warnings
from pathlib import Path
from unittest.mock import patch

import pytest

# ── Add skill root to path ──────────────────────────────────────────────────
SKILL_ROOT = Path(__file__).parent.parent
sys.path.insert(0, str(SKILL_ROOT))


# ===========================================================================
# Fixtures
# ===========================================================================

@pytest.fixture
def tmp_output(tmp_path):
    """Provide a temporary /output directory and patch the default output path."""
    output_dir = tmp_path / "output"
    output_dir.mkdir()
    return output_dir


@pytest.fixture
def tmp_input(tmp_path):
    """Provide a temporary /workspace/input directory."""
    input_dir = tmp_path / "input"
    input_dir.mkdir(parents=True)
    return input_dir


@pytest.fixture
def templates_dir():
    """Return the actual templates directory shipped with the skill."""
    return str(SKILL_ROOT / "templates")


# ===========================================================================
# Helpers / renderer pure logic tests (no weasyprint needed)
# ===========================================================================

class TestBuildPageCSS:
    def test_portrait_contains_portrait(self):
        from helpers.renderer import _build_page_css
        css = _build_page_css("A4", "portrait", 2.0, 2.0, 2.0, 2.0)
        assert "A4 portrait" in css

    def test_landscape_contains_landscape(self):
        from helpers.renderer import _build_page_css
        css = _build_page_css("A4", "landscape", 2.0, 2.0, 2.0, 2.0)
        assert "A4 landscape" in css

    def test_margins_included(self):
        from helpers.renderer import _build_page_css
        css = _build_page_css("Letter", "portrait", 1.5, 2.5, 1.0, 3.0)
        assert "1.5cm" in css
        assert "2.5cm" in css
        assert "1.0cm" in css
        assert "3.0cm" in css

    def test_cjk_font_included(self):
        from helpers.renderer import _build_page_css
        css = _build_page_css("A4", "portrait", 2.0, 2.0, 2.0, 2.0)
        assert "WenQuanYi Zen Hei" in css


class TestInjectCSS:
    def test_with_head_tag(self):
        from helpers.renderer import _inject_css
        html = "<html><head></head><body><p>text</p></body></html>"
        result = _inject_css(html, "p { color: red; }")
        # Style must appear inside <head> section
        head_end = result.lower().find("</head>")
        style_pos = result.find("<style>")
        assert style_pos != -1, "<style> tag not found"
        assert style_pos < head_end, "<style> should be injected before </head>"

    def test_without_head_tag(self):
        from helpers.renderer import _inject_css
        html = "<p>text</p>"
        result = _inject_css(html, "p { color: red; }")
        # Style should be prepended to the document
        assert result.startswith("<style>")
        assert "p { color: red; }" in result

    def test_css_content_preserved(self):
        from helpers.renderer import _inject_css
        css = "@page { size: A4; } body { font-size: 12pt; }"
        result = _inject_css("<html><head></head><body></body></html>", css)
        assert "@page { size: A4; }" in result
        assert "font-size: 12pt;" in result

    def test_case_insensitive_head_detection(self):
        from helpers.renderer import _inject_css
        html = "<HTML><HEAD></HEAD><BODY></BODY></HTML>"
        result = _inject_css(html, "p{}")
        # Should detect <HEAD> regardless of case
        assert "<style>" in result


# ===========================================================================
# Template helper filter tests (jinja2 required, weasyprint NOT required)
# ===========================================================================

class TestCurrencyFilter:
    def test_basic_formatting(self):
        from helpers.template import _currency_filter
        assert _currency_filter(1234.5) == "¥1,234.50"

    def test_zero(self):
        from helpers.template import _currency_filter
        assert _currency_filter(0, symbol="$") == "$0.00"

    def test_large_number(self):
        from helpers.template import _currency_filter
        result = _currency_filter(1000000.0)
        assert "¥1,000,000.00" == result

    def test_custom_symbol(self):
        from helpers.template import _currency_filter
        assert _currency_filter(99.9, symbol="$") == "$99.90"

    def test_string_input(self):
        from helpers.template import _currency_filter
        # Should coerce numeric strings
        assert _currency_filter("1234.5") == "¥1,234.50"

    def test_invalid_input_returns_string(self):
        from helpers.template import _currency_filter
        result = _currency_filter("not-a-number")
        assert isinstance(result, str)


class TestDateCnFilter:
    def test_valid_date(self):
        from helpers.template import _date_cn_filter
        assert _date_cn_filter("2026-05-24") == "2026年5月24日"

    def test_single_digit_month_day(self):
        from helpers.template import _date_cn_filter
        assert _date_cn_filter("2026-01-05") == "2026年1月5日"

    def test_invalid_date_returns_original(self):
        from helpers.template import _date_cn_filter
        assert _date_cn_filter("invalid") == "invalid"

    def test_empty_string_returns_empty(self):
        from helpers.template import _date_cn_filter
        assert _date_cn_filter("") == ""


# ===========================================================================
# Template rendering tests (jinja2 required)
# ===========================================================================

INVOICE_MINIMAL_VARS = {
    "invoice_number": "TEST-001",
    "company_name": "测试公司",
    "client_name": "客户公司",
    "issue_date": "2026-05-24",
    "due_date": "2026-06-24",
    "items": [
        {"description": "服务费", "qty": 1, "unit_price": 100.0},
    ],
    "tax_rate": 0.06,
    "currency": "CNY",
    "logo_path": None,
    "notes": None,
}


class TestTemplateRendering:
    def test_invoice_template_renders(self, templates_dir):
        from helpers.template import render_template
        html = render_template("invoice", INVOICE_MINIMAL_VARS, template_dir=templates_dir)
        assert "发" in html  # "发 票" in invoice heading
        assert "TEST-001" in html
        assert "测试公司" in html
        assert "客户公司" in html

    def test_invoice_template_computes_totals(self, templates_dir):
        from helpers.template import render_template
        html = render_template("invoice", INVOICE_MINIMAL_VARS, template_dir=templates_dir)
        # 100 * 1 = 100; tax 6% = 6; total = 106
        assert "106.00" in html or "¥106.00" in html

    def test_invoice_currency_filter_in_template(self, templates_dir):
        from helpers.template import render_template
        vars_ = dict(INVOICE_MINIMAL_VARS)
        vars_["items"] = [{"description": "X", "qty": 2, "unit_price": 617.25}]
        html = render_template("invoice", vars_, template_dir=templates_dir)
        # 2 * 617.25 = 1234.50
        assert "1,234.50" in html

    def test_invoice_date_cn_filter_in_template(self, templates_dir):
        from helpers.template import render_template
        html = render_template("invoice", INVOICE_MINIMAL_VARS, template_dir=templates_dir)
        assert "2026年5月24日" in html

    def test_template_strict_undefined_raises_on_missing_var(self, templates_dir):
        from helpers.template import render_template
        from jinja2 import UndefinedError
        incomplete_vars = {k: v for k, v in INVOICE_MINIMAL_VARS.items() if k != "invoice_number"}
        with pytest.raises(UndefinedError):
            render_template("invoice", incomplete_vars, template_dir=templates_dir)

    def test_report_template_renders(self, templates_dir):
        from helpers.template import render_template
        vars_ = {
            "title": "季度报告",
            "author": "张三",
            "generated_date": "2026-05-24",
            "abstract": None,
            "subtitle": None,
            "department": None,
            "version": None,
            "sections": [
                {
                    "heading": "概述",
                    "content": "内容摘要。",
                    "subsections": [],
                    "image_path": None,
                    "table": None,
                }
            ],
            "footer_note": None,
        }
        html = render_template("report", vars_, template_dir=templates_dir)
        assert "季度报告" in html
        assert "张三" in html
        assert "概述" in html

    def test_certificate_template_renders(self, templates_dir):
        from helpers.template import render_template
        vars_ = {
            "award_title": "优秀员工奖",
            "recipient_name": "李四",
            "description": "表现突出。",
            "issue_date": "2026-05-24",
            "issuer_name": "公司管理层",
            "issuer_title": None,
            "issuer_org": None,
            "logo_path": None,
            "seal_path": None,
        }
        html = render_template("certificate", vars_, template_dir=templates_dir)
        assert "优秀员工奖" in html
        assert "李四" in html


# ===========================================================================
# pdf_meta tests (no system deps needed)
# ===========================================================================

class TestGetPageCount:
    def test_nonexistent_file_returns_minus_one(self):
        from helpers.pdf_meta import get_page_count
        result = get_page_count("/nonexistent/path/to/file.pdf")
        assert result == -1

    def test_non_pdf_file_returns_minus_one(self, tmp_path):
        from helpers.pdf_meta import get_page_count
        non_pdf = tmp_path / "test.txt"
        non_pdf.write_text("this is not a pdf")
        result = get_page_count(str(non_pdf))
        assert result == -1

    def test_synthetic_pdf_binary(self, tmp_path):
        """Verify counting works on a synthetic PDF-like binary blob."""
        from helpers.pdf_meta import get_page_count
        fake_pdf = tmp_path / "fake.pdf"
        # Embed two "/Type /Page" markers (one per page)
        content = b"%PDF-1.7\n/Type /Page\nsome stuff\n/Type /Page\n"
        fake_pdf.write_bytes(content)
        result = get_page_count(str(fake_pdf))
        assert result == 2


# ===========================================================================
# main.run() integration tests (weasyprint system libs required)
# ===========================================================================

def _weasyprint_available() -> bool:
    """Check if weasyprint can actually render (requires libcairo etc.)."""
    try:
        import weasyprint
        with warnings.catch_warnings():
            warnings.filterwarnings("ignore")
            weasyprint.HTML(string="<p>test</p>").write_pdf()
        return True
    except Exception:
        return False


WEASYPRINT_SKIP = pytest.mark.skipif(
    not _weasyprint_available(),
    reason="weasyprint system libs (libcairo/libpango) not available in this environment",
)


@WEASYPRINT_SKIP
class TestRunIntegration:
    """Integration tests that actually invoke weasyprint to produce PDF bytes."""

    def _run_with_tmpdir(self, params: dict, tmp_output, tmp_input):
        """Helper: patch output/input paths and call run()."""
        import main as skill_main
        output_file = str(tmp_output / params["output_filename"])
        # Patch output path in the run function by adjusting output_filename
        params = dict(params)
        params["output_filename"] = params["output_filename"]

        # We need to redirect /output/ to tmp_output.
        # Since run() constructs the path as f"/output/{filename}", we patch
        # the os module's path operations isn't feasible; instead we patch
        # render_html_to_pdf to write to tmp dir.
        with patch("main.run", wraps=skill_main.run):
            # Direct approach: just update the output dir in params via extra_css trick
            # won't work. Use monkeypatching of the output path constant.
            pass
        # Simpler: directly call helpers and check output
        return output_file

    def test_simple_html_to_pdf_produces_file(self, tmp_output, tmp_input):
        """run() with html_content should write a PDF to /output/."""
        import main as skill_main
        params = {
            "output_filename": "test_simple.pdf",
            "html_content": "<html><head><meta charset='UTF-8'></head><body><p>test 中文</p></body></html>",
        }
        # Temporarily patch output to write to tmp_output
        original_render = None
        from helpers import renderer as renderer_mod
        original_fn = renderer_mod.render_html_to_pdf

        written_paths = []

        def patched_render(html_content, output_path, **kwargs):
            new_path = str(tmp_output / Path(output_path).name)
            written_paths.append(new_path)
            original_fn(html_content, new_path, **kwargs)

        renderer_mod.render_html_to_pdf = patched_render
        try:
            result = skill_main.run(params)
        finally:
            renderer_mod.render_html_to_pdf = original_fn

        assert written_paths, "render was not called"
        out = written_paths[0]
        assert Path(out).exists(), f"PDF not written to {out}"
        assert Path(out).stat().st_size > 0

    def test_output_size_within_limit(self, tmp_output, tmp_input, templates_dir):
        """A 10-item invoice PDF should be well within 50MB."""
        import main as skill_main
        from helpers.renderer import render_html_to_pdf as original_fn
        from helpers import renderer as renderer_mod

        items = [
            {"description": f"服务项目 {i}", "qty": i, "unit_price": float(i * 100)}
            for i in range(1, 11)
        ]
        from helpers.template import render_template
        html = render_template(
            "invoice",
            {
                **INVOICE_MINIMAL_VARS,
                "items": items,
            },
            template_dir=templates_dir,
        )

        out_path = str(tmp_output / "big_invoice.pdf")
        # Call renderer directly with tmp path
        render_html_to_pdf(html, out_path)
        size = Path(out_path).stat().st_size
        limit = 50 * 1024 * 1024  # 50 MB
        assert size < limit, f"PDF size {size} bytes exceeds 50MB limit"

    def test_get_page_count_on_real_pdf(self, tmp_output):
        """A single-page PDF from weasyprint should have page_count == 1."""
        from helpers.renderer import render_html_to_pdf
        from helpers.pdf_meta import get_page_count
        out_path = str(tmp_output / "one_page.pdf")
        render_html_to_pdf(
            "<html><head><meta charset='UTF-8'></head><body><p>One page.</p></body></html>",
            out_path,
        )
        count = get_page_count(out_path)
        assert count == 1

    def test_extra_css_does_not_crash(self, tmp_output):
        """Passing extra_css should not raise any exception."""
        from helpers.renderer import render_html_to_pdf
        out_path = str(tmp_output / "extra_css.pdf")
        render_html_to_pdf(
            "<html><head><meta charset='UTF-8'></head><body><p>test</p></body></html>",
            out_path,
            extra_css="body { background: #F0F0F0; font-size: 9pt; }",
        )
        assert Path(out_path).exists()


# ===========================================================================
# main.run() error path tests (no system deps needed — errors raised before render)
# ===========================================================================

class TestRunErrorPaths:
    def test_no_input_source_error(self):
        from main import run
        result = run({"output_filename": "x.pdf"})
        assert result["success"] is False
        assert result["error"] is not None
        assert "html_content" in result["error"] or "template" in result["error"]

    def test_missing_output_filename(self):
        from main import run
        result = run({"html_content": "<p>hi</p>"})
        assert result["success"] is False
        assert "output_filename" in result["error"]

    def test_missing_html_file_error(self):
        from main import run
        result = run({
            "output_filename": "out.pdf",
            "html_file": "nonexistent_999.html",
        })
        assert result["success"] is False
        assert "nonexistent_999.html" in result["error"]

    def test_template_strict_undefined_wrapped_as_error(self, templates_dir):
        """Missing template var should return success=False, not raise."""
        from main import run
        # Patch template_dir to our actual templates
        import helpers.template as tpl_mod
        original = tpl_mod._DEFAULT_TEMPLATE_DIR
        # We test via run() — it catches the UndefinedError internally
        incomplete = {k: v for k, v in INVOICE_MINIMAL_VARS.items() if k != "invoice_number"}
        result = run({
            "output_filename": "err_test.pdf",
            "template": "invoice",
            "template_vars": incomplete,
        })
        assert result["success"] is False
        assert result["error"] is not None
