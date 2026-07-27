# Plan: Standard Office Output Tools

1. Add RED tests for native `create_docx`, `create_xlsx`, `create_pptx`, registry metadata, and disabled-sandbox selection.
2. Implement shared native OOXML helpers.
3. Convert `create_docx` from sandbox Python to native OOXML.
4. Add native `create_xlsx` and `create_pptx` tools.
5. Update platform factory metadata and output-priority prompt.
6. Run focused tests, `go test ./...`, and `task lint`.
7. Merge via NDF and deploy to Dev only.
