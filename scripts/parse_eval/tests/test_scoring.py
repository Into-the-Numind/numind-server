from parse_eval.metrics.scoring import aggregate, Grade, ScoreBundle


def test_synthetic_sample_full_score():
    bundle = ScoreBundle(encoding=40, structure=30, completeness=30, encoding_failed=False)
    agg = aggregate(bundle, has_golden=True)
    assert agg.total == 100
    assert agg.grade == Grade.EXCELLENT


def test_encoding_fail_overrides_grade():
    bundle = ScoreBundle(encoding=0, structure=30, completeness=30, encoding_failed=True)
    agg = aggregate(bundle, has_golden=True)
    assert agg.grade == Grade.FAIL


def test_real_sample_skips_structure():
    bundle = ScoreBundle(encoding=40, structure=0, completeness=30, encoding_failed=False)
    agg = aggregate(bundle, has_golden=False)
    assert agg.total == 40 * (0.6 / 0.4) + 30 * (0.4 / 0.3)
    assert agg.grade in (Grade.EXCELLENT, Grade.GOOD)


def test_needs_review_band():
    bundle = ScoreBundle(encoding=25, structure=20, completeness=20, encoding_failed=False)
    agg = aggregate(bundle, has_golden=True)
    assert 60 <= agg.total < 75
    assert agg.grade == Grade.NEEDS_REVIEW
