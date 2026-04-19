from parse_eval.metrics.structure import score_structure, StructureResult


def test_headings_preserved():
    golden = "# 标题一\n内容1\n## 标题二\n内容2"
    output = "# 标题一\n内容1\n## 标题二\n内容2"
    result = score_structure(output, golden, has_table=False)
    assert result.heading_recall == 1.0


def test_missing_headings():
    golden = "# A\nx\n# B\ny\n# C\nz"
    output = "# A\nx\nB\ny\nC\nz"
    result = score_structure(output, golden, has_table=False)
    assert result.heading_recall < 0.5


def test_table_detected_when_expected():
    output = "| a | b |\n|---|---|\n| 1 | 2 |"
    result = score_structure(output, "| a | b |", has_table=True)
    assert result.table_detected is True


def test_table_missing_when_expected_deducts():
    output = "a b\n1 2"
    result = score_structure(output, "| a | b |", has_table=True)
    assert result.table_detected is False
    assert result.score < 30


def test_paragraph_ratio_in_range():
    golden = "第一段\n\n第二段\n\n第三段"
    output = "第一段\n\n第二段\n\n第三段"
    result = score_structure(output, golden, has_table=False)
    assert 0.7 <= result.paragraph_count_ratio <= 1.3
