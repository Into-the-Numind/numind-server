# Standard Office Output Tools

## Problem

Standard Office file generation currently overuses the sandbox path. `create_docx` is a deterministic Markdown-to-Word helper, but it still borrows a Docker sandbox. `.xlsx` and `.pptx` have no direct standard create tool, so the Agent must use `load_skill -> run_python` even for simple workbook or deck requests.

This makes high-frequency standard file generation compete with genuinely complex sandbox tasks. It also means a disabled or busy sandbox can hide otherwise simple standard output options.

## Goals

- `create_docx` generates standard Word documents without borrowing a sandbox.
- Add `create_xlsx` for standard Excel workbooks without borrowing a sandbox.
- Add `create_pptx` for standard PowerPoint decks without borrowing a sandbox.
- Keep `load_skill -> run_python` as the fallback for complex layout, advanced styling, formulas, charts, templates, or long-tail formats.
- Preserve existing COS upload and generated artifact card behavior.

## Non-Goals

- No Prod sandbox enablement in this feature.
- No full queue/concurrency scheduler in this feature.
- No pixel-perfect Office designer.
- No new HTTP endpoints or DB schema changes.
