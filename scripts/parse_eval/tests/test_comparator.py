from parse_eval.report.comparator import compare_summaries, Regression


def test_detects_score_drop_over_10():
    baseline = [{"pipeline": "sop", "sample": "a.pdf", "total_score": 90.0}]
    latest = [{"pipeline": "sop", "sample": "a.pdf", "total_score": 70.0}]
    regs = compare_summaries(baseline, latest)
    assert len(regs) == 1
    assert regs[0].delta == -20.0
    assert regs[0].is_regression is True


def test_small_drop_not_regression():
    baseline = [{"pipeline": "sop", "sample": "a.pdf", "total_score": 90.0}]
    latest = [{"pipeline": "sop", "sample": "a.pdf", "total_score": 85.0}]
    regs = compare_summaries(baseline, latest)
    assert regs[0].is_regression is False


def test_new_sample_no_baseline():
    baseline = []
    latest = [{"pipeline": "sop", "sample": "new.pdf", "total_score": 80.0}]
    regs = compare_summaries(baseline, latest)
    assert regs == []
