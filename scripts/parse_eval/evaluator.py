from dataclasses import dataclass, asdict
from pathlib import Path

from parse_eval.metrics.encoding import score_encoding
from parse_eval.metrics.structure import score_structure, StructureResult
from parse_eval.metrics.completeness import score_completeness, CompletenessResult
from parse_eval.metrics.scoring import aggregate, ScoreBundle, Grade
from parse_eval.pipelines.base import ParseResult
from parse_eval.samples.loader import SampleSpec


@dataclass
class EvalRow:
    pipeline: str
    sample: str
    total_score: float
    grade: str
    encoding_score: float
    structure_score: float
    completeness_score: float
    encoding_failed: bool
    char_count_ratio: float
    keyword_recall: float
    heading_recall: float
    table_detected: bool
    notes: list[str]
    elapsed_seconds: float
    error: str | None


def evaluate_parse_result(pr: ParseResult, spec: SampleSpec, golden_dir: Path) -> EvalRow:
    if pr.error:
        return EvalRow(
            pipeline=pr.pipeline, sample=spec.file, total_score=0.0, grade=Grade.FAIL.value,
            encoding_score=0.0, structure_score=0.0, completeness_score=0.0,
            encoding_failed=True, char_count_ratio=0.0, keyword_recall=0.0,
            heading_recall=0.0, table_detected=False, notes=[pr.error],
            elapsed_seconds=pr.elapsed_seconds, error=pr.error,
        )

    enc = score_encoding(pr.output_text)

    golden_path = golden_dir / spec.golden_name()
    has_golden = golden_path.exists()

    if has_golden:
        golden = golden_path.read_text(encoding="utf-8")
        comp = score_completeness(pr.output_text, golden, spec.keywords)
        struct = score_structure(pr.output_text, golden, spec.has_table)
    else:
        comp = CompletenessResult(0.0, 0.0, 0.0, [])
        struct = StructureResult(0.0, 0.0, False, 0.0, 0.0, [])

    bundle = ScoreBundle(
        encoding=enc.score, structure=struct.score, completeness=comp.score,
        encoding_failed=enc.failed,
    )
    agg = aggregate(bundle, has_golden)

    return EvalRow(
        pipeline=pr.pipeline, sample=spec.file, total_score=agg.total, grade=agg.grade.value,
        encoding_score=enc.score, structure_score=struct.score, completeness_score=comp.score,
        encoding_failed=enc.failed, char_count_ratio=comp.char_count_ratio,
        keyword_recall=comp.keyword_recall, heading_recall=struct.heading_recall,
        table_detected=struct.table_detected,
        notes=enc.notes + struct.notes + comp.notes,
        elapsed_seconds=pr.elapsed_seconds, error=None,
    )


def row_to_dict(row: EvalRow) -> dict:
    return asdict(row)
