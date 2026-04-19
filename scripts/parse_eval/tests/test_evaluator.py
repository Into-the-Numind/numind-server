from pathlib import Path

from parse_eval.evaluator import evaluate_parse_result, EvalRow
from parse_eval.pipelines.base import ParseResult
from parse_eval.samples.loader import SampleSpec


def test_synthetic_evaluation_produces_row(tmp_path):
    sample = tmp_path / "a.pdf"
    sample.write_bytes(b"dummy")
    golden = tmp_path / "a.golden.txt"
    golden.write_text("你好世界测试内容")
    spec = SampleSpec(file="a.pdf", description="t", keywords=["你好", "测试"], has_table=False)
    pr = ParseResult("sop", sample, "你好世界测试内容", None, 1.0)

    row = evaluate_parse_result(pr, spec, golden_dir=tmp_path)
    assert row.pipeline == "sop"
    assert row.sample == "a.pdf"
    assert row.total_score > 90
    assert row.error is None


def test_parse_error_produces_failed_row(tmp_path):
    sample = tmp_path / "a.pdf"
    spec = SampleSpec(file="a.pdf", description="t", keywords=[], has_table=False)
    pr = ParseResult("sop", sample, "", "api_error: 500", 0.5)

    row = evaluate_parse_result(pr, spec, golden_dir=tmp_path)
    assert row.error == "api_error: 500"
    assert row.total_score == 0
