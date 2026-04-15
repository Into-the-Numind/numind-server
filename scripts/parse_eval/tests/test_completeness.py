from parse_eval.metrics.completeness import score_completeness, CompletenessResult


def test_exact_match_scores_full():
    golden = "这是一份测试文档。它有两段。" * 10
    output = golden
    result = score_completeness(output, golden, keywords=["测试", "文档", "两段"])
    assert result.score == 30
    assert 0.85 <= result.char_count_ratio <= 1.15
    assert result.keyword_recall == 1.0


def test_heavy_content_loss_scores_low():
    golden = "完整内容 " * 200
    output = "完整内容"
    result = score_completeness(output, golden, keywords=["完整", "内容"])
    assert result.score < 10
    assert result.char_count_ratio < 0.5


def test_keyword_recall_partial():
    golden = "苹果 香蕉 橙子 葡萄 西瓜"
    output = "苹果 香蕉 橙子"
    result = score_completeness(output, golden, keywords=["苹果", "香蕉", "橙子", "葡萄", "西瓜"])
    assert result.keyword_recall == 0.6
