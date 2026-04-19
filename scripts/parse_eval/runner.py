from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
import json

from parse_eval.evaluator import evaluate_parse_result, EvalRow, row_to_dict
from parse_eval.pipelines.base import Pipeline
from parse_eval.samples.loader import SampleSpec


@dataclass
class RunContext:
    run_id: str
    report_dir: Path
    synthetic_dir: Path
    real_dir: Path


def make_run_context(base_dir: Path) -> RunContext:
    run_id = datetime.now().strftime("%Y%m%d_%H%M%S")
    report_dir = base_dir / "reports" / run_id
    (report_dir / "diffs").mkdir(parents=True, exist_ok=True)
    return RunContext(
        run_id=run_id,
        report_dir=report_dir,
        synthetic_dir=base_dir / "samples" / "synthetic",
        real_dir=base_dir / "samples" / "real",
    )


def run_matrix(
    pipelines: list[Pipeline],
    synthetic_specs: list[SampleSpec],
    real_files: list[Path],
    ctx: RunContext,
) -> list[EvalRow]:
    rows: list[EvalRow] = []

    for spec in synthetic_specs:
        sample_path = ctx.synthetic_dir / spec.file
        if not sample_path.exists():
            print(f"[skip] missing sample: {sample_path}")
            continue
        for pipeline in pipelines:
            print(f"[run] {pipeline.name} × {spec.file}")
            pr = pipeline.parse(sample_path)
            row = evaluate_parse_result(pr, spec, ctx.synthetic_dir)
            rows.append(row)
            _write_output(ctx, pipeline.name, spec.file, pr.output_text)

    for real_path in real_files:
        spec = SampleSpec(file=real_path.name, description="real sample", keywords=[], has_table=False)
        for pipeline in pipelines:
            print(f"[run] {pipeline.name} × {real_path.name}")
            pr = pipeline.parse(real_path)
            row = evaluate_parse_result(pr, spec, ctx.real_dir)
            rows.append(row)
            _write_output(ctx, pipeline.name, real_path.name, pr.output_text)

    summary = [row_to_dict(r) for r in rows]
    (ctx.report_dir / "summary.json").write_text(
        json.dumps(summary, ensure_ascii=False, indent=2, default=str), encoding="utf-8"
    )
    return rows


def _write_output(ctx: RunContext, pipeline: str, sample: str, text: str) -> None:
    outdir = ctx.report_dir / "diffs"
    outdir.mkdir(parents=True, exist_ok=True)
    (outdir / f"{pipeline}__{sample}.out.txt").write_text(text, encoding="utf-8")
