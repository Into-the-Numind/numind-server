from dataclasses import dataclass
import re

CJK_RANGE = re.compile(r"[\u4e00-\u9fff]")
CJK_WITH_SPACE = re.compile(r"[\u4e00-\u9fff] [\u4e00-\u9fff]")
CONTROL_CHARS = re.compile(r"[\x00-\x08\x0b\x0c\x0e-\x1f]")


@dataclass
class EncodingResult:
    score: float
    failed: bool
    replacement_char_rate: float
    control_char_rate: float
    cjk_broken_rate: float
    notes: list[str]


def score_encoding(text: str) -> EncodingResult:
    notes: list[str] = []
    if not text:
        return EncodingResult(0.0, True, 0.0, 0.0, 0.0, ["empty output"])

    n = len(text)

    try:
        text.encode("utf-8")
    except UnicodeEncodeError:
        return EncodingResult(0.0, True, 0.0, 0.0, 0.0, ["utf8_invalid"])

    rep_count = text.count("\ufffd")
    rep_rate = rep_count / n
    ctrl_count = len(CONTROL_CHARS.findall(text))
    ctrl_rate = ctrl_count / n

    cjk_chars = CJK_RANGE.findall(text)
    cjk_broken_rate = 0.0
    if len(cjk_chars) >= 4:
        broken_pairs = len(CJK_WITH_SPACE.findall(text))
        cjk_broken_rate = broken_pairs / max(1, len(cjk_chars) - 1)

    if rep_rate > 0.005:
        notes.append(f"replacement_char_rate={rep_rate:.2%} exceeds 0.5%")
        return EncodingResult(0.0, True, rep_rate, ctrl_rate, cjk_broken_rate, notes)

    score = 40.0
    score -= min(10.0, ctrl_rate * 1000 * 2)
    if cjk_broken_rate > 0.3:
        score -= 30.0
        notes.append(f"cjk_broken_rate={cjk_broken_rate:.2%}")
    elif cjk_broken_rate > 0.1:
        score -= 10.0
        notes.append(f"cjk_broken_rate={cjk_broken_rate:.2%}")

    score = max(0.0, score)
    return EncodingResult(score, False, rep_rate, ctrl_rate, cjk_broken_rate, notes)
