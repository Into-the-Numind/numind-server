from dataclasses import dataclass
import re

HEADING_RE = re.compile(r"^#{1,6}\s+(.+)$", re.MULTILINE)
TABLE_ROW_RE = re.compile(r"\|.+\|")
LIST_ITEM_RE = re.compile(r"^\s*(?:[-*]\s+|\d+\.\s+)", re.MULTILINE)


@dataclass
class StructureResult:
    score: float
    heading_recall: float
    table_detected: bool
    paragraph_count_ratio: float
    list_recall: float
    notes: list[str]


def _extract_headings(text: str) -> list[str]:
    return [m.group(1).strip() for m in HEADING_RE.finditer(text)]


def _count_paragraphs(text: str) -> int:
    return len([p for p in text.split("\n\n") if p.strip()])


def _count_list_items(text: str) -> int:
    return len(LIST_ITEM_RE.findall(text))


def score_structure(output: str, golden: str, has_table: bool) -> StructureResult:
    notes: list[str] = []

    gold_headings = _extract_headings(golden)
    out_headings = _extract_headings(output)
    if gold_headings:
        hits = sum(1 for h in gold_headings if h in out_headings)
        heading_recall = hits / len(gold_headings)
    else:
        heading_recall = 1.0

    table_detected = bool(TABLE_ROW_RE.search(output))
    if has_table and not table_detected:
        notes.append("expected table but none detected in output")

    gold_paras = max(1, _count_paragraphs(golden))
    out_paras = _count_paragraphs(output)
    para_ratio = out_paras / gold_paras

    gold_lists = _count_list_items(golden)
    out_lists = _count_list_items(output)
    list_recall = min(1.0, out_lists / gold_lists) if gold_lists else 1.0

    score = 0.0
    score += 12.0 * heading_recall
    score += 8.0 if (not has_table or table_detected) else 0.0
    score += 6.0 if 0.7 <= para_ratio <= 1.3 else 3.0
    score += 4.0 * list_recall

    return StructureResult(
        score, heading_recall, table_detected, para_ratio, list_recall, notes
    )
