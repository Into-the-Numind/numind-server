import argparse
import json
import os
import sys
from pathlib import Path

from parse_eval.pipelines.auth import login
from parse_eval.pipelines.sop import SopPipeline
from parse_eval.pipelines.chatbot import ChatbotPipeline
from parse_eval.pipelines.salesrag import SalesRagPipeline
from parse_eval.samples.loader import load_synthetic_manifest
from parse_eval.runner import make_run_context, run_matrix
from parse_eval.report.html_renderer import render_html, load_outputs
from parse_eval.report.comparator import compare_summaries

BASE_DIR = Path(__file__).parent


def _build_pipelines(names: list[str]):
    base_url = os.environ.get("LOCAL_API_URL", "http://localhost:9091")
    token = login(base_url)
    registry = {
        "sop": lambda: SopPipeline(base_url, token),
        "chatbot": lambda: ChatbotPipeline(base_url, token),
        "salesrag": lambda: SalesRagPipeline(base_url, token),
    }
    return [registry[n]() for n in names]


def _list_real_files() -> list[Path]:
    real_dir = BASE_DIR / "samples" / "real"
    if not real_dir.exists():
        return []
    return [f for f in sorted(real_dir.glob("*")) if f.is_file() and f.name != ".gitkeep"]


def _cmd_run(args: argparse.Namespace) -> int:
    if args.pipeline == "all":
        names = ["sop", "chatbot", "salesrag"]
    else:
        names = [args.pipeline]
    pipelines = _build_pipelines(names)

    synthetic_specs = load_synthetic_manifest(BASE_DIR / "samples" / "synthetic" / "manifest.yaml")

    if args.sample == "synthetic":
        real_files: list[Path] = []
    elif args.sample == "real":
        synthetic_specs = []
        real_files = _list_real_files()
    elif args.sample == "all":
        real_files = _list_real_files()
    else:
        single = Path(args.sample)
        if single.exists():
            synthetic_specs = []
            real_files = [single]
        else:
            print(f"sample not found: {single}", file=sys.stderr)
            return 1

    ctx = make_run_context(BASE_DIR)
    rows = run_matrix(pipelines, synthetic_specs, real_files, ctx)
    outputs = load_outputs(ctx.report_dir)
    render_html(ctx.run_id, rows, outputs, ctx.report_dir)
    print(f"\n✓ report written to {ctx.report_dir}/report.html")
    return 0


def _cmd_compare(baseline: str, latest: str) -> int:
    base = json.loads((BASE_DIR / "reports" / baseline / "summary.json").read_text(encoding="utf-8"))
    late = json.loads((BASE_DIR / "reports" / latest / "summary.json").read_text(encoding="utf-8"))
    regs = compare_summaries(base, late)
    any_reg = False
    for r in regs:
        flag = "🔴 REGRESSION" if r.is_regression else "·"
        print(f"{flag} {r.pipeline} × {r.sample}: {r.baseline_score:.1f} → {r.latest_score:.1f} ({r.delta:+.1f})")
        any_reg = any_reg or r.is_regression
    return 2 if any_reg else 0


def main() -> int:
    p = argparse.ArgumentParser(prog="run_eval", description="Document parsing pipeline evaluator")
    p.add_argument("--pipeline", default="all", choices=["all", "sop", "chatbot", "salesrag"],
                   help="which pipeline(s) to evaluate")
    p.add_argument("--sample", default="all",
                   help="'all' | 'synthetic' | 'real' | path/to/file")
    p.add_argument("--compare", nargs=2, metavar=("BASELINE", "LATEST"),
                   help="compare two report runs (by directory name); exits 2 on regression")

    args = p.parse_args()

    if args.compare:
        return _cmd_compare(args.compare[0], args.compare[1])

    return _cmd_run(args)


if __name__ == "__main__":
    sys.exit(main())
