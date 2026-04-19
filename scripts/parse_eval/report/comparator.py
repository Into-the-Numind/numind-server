from dataclasses import dataclass

REGRESSION_THRESHOLD = 10.0


@dataclass
class Regression:
    pipeline: str
    sample: str
    baseline_score: float
    latest_score: float
    delta: float
    is_regression: bool


def compare_summaries(baseline: list[dict], latest: list[dict]) -> list[Regression]:
    base_map = {(r["pipeline"], r["sample"]): r["total_score"] for r in baseline}
    out: list[Regression] = []
    for r in latest:
        key = (r["pipeline"], r["sample"])
        if key not in base_map:
            continue
        b = base_map[key]
        l = r["total_score"]
        delta = l - b
        out.append(Regression(
            pipeline=r["pipeline"], sample=r["sample"],
            baseline_score=b, latest_score=l, delta=delta,
            is_regression=(delta <= -REGRESSION_THRESHOLD),
        ))
    return out
