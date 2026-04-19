from parse_eval.metrics.encoding import score_encoding, EncodingResult


def test_clean_text_scores_full():
    result = score_encoding("你好，世界！Hello world 123.")
    assert result.score == 40
    assert result.failed is False
    assert result.replacement_char_rate == 0.0


def test_replacement_char_triggers_fail():
    text = "正常文本" + "\ufffd" * 50 + "更多文本"
    result = score_encoding(text)
    assert result.failed is True
    assert result.score == 0


def test_control_chars_deduct():
    text = "正常" + "\x01\x02\x03\x04\x05" * 20 + "尾部"
    result = score_encoding(text)
    assert result.score < 40
    assert result.control_char_rate > 0


def test_cjk_broken_detected():
    text = "你 好 世 界 这 是 中 文 " * 5
    result = score_encoding(text)
    assert result.cjk_broken_rate > 0.5
    assert result.score < 40


def test_newline_tab_not_penalised():
    result = score_encoding("第一行\n第二行\t缩进")
    assert result.control_char_rate == 0.0
    assert result.score == 40
