from dataclasses import dataclass


@dataclass
class CompletenessResult:
    score: float
    char_count_ratio: float
    keyword_recall: float
    notes: list[str]


def score_completeness(output: str, golden: str, keywords: list[str]) -> CompletenessResult:
    notes: list[str] = []
    if not golden:
        return CompletenessResult(0.0, 0.0, 0.0, ["no golden reference"])

    ratio = len(output) / len(golden)
    severe_loss = ratio < 0.5
    if severe_loss:
        char_score = 0.0
        notes.append(f"char_count_ratio={ratio:.2f} < 0.5 (severe content loss)")
    elif 0.85 <= ratio <= 1.15:
        char_score = 20.0
    elif 0.7 <= ratio < 0.85 or 1.15 < ratio <= 1.3:
        char_score = 14.0
    else:
        char_score = 7.0

    if keywords:
        hits = sum(1 for kw in keywords if kw in output)
        recall = hits / len(keywords)
    else:
        recall = 1.0

    kw_score = 10.0 * recall
    if recall < 1.0:
        missed = [kw for kw in keywords if kw not in output]
        notes.append(f"missed_keywords={missed}")

    total = char_score + kw_score
    # When severe content loss, cap total below 10 to reflect poor quality
    if severe_loss:
        total = min(total, 9.0)
    return CompletenessResult(total, ratio, recall, notes)
