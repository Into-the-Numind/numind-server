from pathlib import Path

from jinja2 import Environment, FileSystemLoader, select_autoescape

from parse_eval.evaluator import EvalRow


def render_html(run_id: str, rows: list[EvalRow], outputs: dict, report_dir: Path) -> None:
    tpl_dir = Path(__file__).parent / "templates"
    env = Environment(loader=FileSystemLoader(tpl_dir), autoescape=select_autoescape())
    tpl = env.get_template("report.html.j2")

    pipelines = sorted({r.pipeline for r in rows})
    samples = sorted({r.sample for r in rows})
    matrix: dict = {s: {} for s in samples}
    for r in rows:
        matrix[r.sample][r.pipeline] = r

    html = tpl.render(
        run_id=run_id, pipelines=pipelines, samples=samples,
        matrix=matrix, outputs=outputs,
    )
    (report_dir / "report.html").write_text(html, encoding="utf-8")


def load_outputs(report_dir: Path) -> dict:
    outputs: dict = {}
    diffs_dir = report_dir / "diffs"
    if not diffs_dir.exists():
        return outputs
    for f in diffs_dir.glob("*.out.txt"):
        pipeline, rest = f.name.split("__", 1)
        sample = rest[:-len(".out.txt")]
        outputs.setdefault(sample, {})[pipeline] = f.read_text(encoding="utf-8")
    return outputs
