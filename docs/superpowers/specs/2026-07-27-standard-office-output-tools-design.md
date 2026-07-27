# Standard Office Output Tools Design

## Tool Contract

`create_docx` accepts Markdown content and an optional filename. It does not require sandbox runtime and remains suitable for standard reports.

`create_xlsx` accepts either:

- `sheets`: an array of sheets, each with `name`, optional `headers`, and `rows`
- or top-level `headers` and `rows` for a single workbook

Rows can be arrays or objects. Object rows follow header order when headers are present, otherwise keys are sorted for stable output.

`create_pptx` accepts `slides`, each with `title`, optional `subtitle`, optional `bullets`, and optional `notes`.

## Native OOXML Boundary

The native implementation only covers standard structure. It intentionally avoids advanced Office capabilities that explode complexity:

- rich arbitrary styling
- formulas and pivot tables
- charts inside Office documents
- custom templates
- speaker layout design systems
- image embedding in native `create_pptx`

Those remain on `load_skill -> run_python`.

## Sandbox Boundary

After this change, these tools do not need sandbox:

- `create_docx`
- `create_xlsx`
- `create_pptx`
- `create_csv`
- `create_html`
- `create_json`
- `create_text`
- `create_png_chart`

These still need sandbox:

- `run_python`
- `bash_exec`
- complex Office generation through `docx-author`, `xlsx-author`, `pptx-author`, `pdf-from-html`

## Verification

Tests must prove:

- native Office files are valid zip packages with the required OOXML entries
- `create_docx/create_xlsx/create_pptx` are selected even when sandbox runtime is disabled
- sandbox hook allowlist no longer treats `create_docx` as a sandbox-exec tool
- platform registry metadata marks standard Office tools as non-sandbox
