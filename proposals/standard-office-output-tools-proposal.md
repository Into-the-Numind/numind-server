# Proposal: Standard Office Output Tools

## Selected Approach

Implement lightweight native Office Open XML generation in Go for the standard cases:

- `create_docx`: Markdown-like text into headings, paragraphs, bullets, numbered items, and simple tables.
- `create_xlsx`: one or more sheets from JSON rows and optional headers.
- `create_pptx`: a simple deck from JSON slides with title, subtitle, bullets, and notes.

The native tools generate valid `.docx`, `.xlsx`, and `.pptx` zip packages directly in memory and upload the bytes through the existing `uploadGeneratedFile` helper.

## Why This Approach

This removes sandbox pressure from routine files while keeping the sandbox for tasks that truly need code execution or richer libraries. It also keeps deployment simple: no new service, no binary dependency, no Python runtime in the main path, and no model-authored code execution for standard files.

## Fallback Contract

The Agent prompt should prefer native `create_docx/create_xlsx/create_pptx` for standard requests. It should use `load_skill -> run_python` only when the user asks for complex formatting, templates, macros-like logic, formulas, charts, precise design control, or unusual file formats.
