from dataclasses import dataclass
from enum import Enum


class Grade(str, Enum):
    EXCELLENT = "Excellent"
    GOOD = "Good"
    NEEDS_REVIEW = "NeedsReview"
    FAIL = "Fail"


@dataclass
class ScoreBundle:
    encoding: float
    structure: float
    completeness: float
    encoding_failed: bool


@dataclass
class Aggregate:
    total: float
    grade: Grade


def aggregate(bundle: ScoreBundle, has_golden: bool) -> Aggregate:
    if has_golden:
        total = bundle.encoding + bundle.structure + bundle.completeness
    else:
        total = (bundle.encoding / 40) * 60 + (bundle.completeness / 30) * 40

    if bundle.encoding_failed:
        return Aggregate(total, Grade.FAIL)

    if total >= 90:
        grade = Grade.EXCELLENT
    elif total >= 75:
        grade = Grade.GOOD
    elif total >= 60:
        grade = Grade.NEEDS_REVIEW
    else:
        grade = Grade.FAIL

    return Aggregate(total, grade)
