# Lark Skill Reference Resolution — Implementation Plan

## Task 1 — Customer regression (RED)
- File: `internal/numind/biz/feishu/skill_reader_test.go`
- Add a production-shaped main skill that declares `references/style/lark-doc-update.md`, then request only `lark-doc-update.md`.
- Assert the returned path/content are canonical and the CLI invocation reads the declared resource.
- Acceptance: the focused test fails on current develop before production changes; commit message starts with `test(qa): reproduce` and is the first code commit.

## Task 2 — Fail-closed shorthand resolver (GREEN)
- Files: `internal/numind/biz/feishu/skill_reader.go`, `internal/numind/biz/feishu/skill_reader_test.go`.
- Add a pure resolver that accepts existing canonical references or one safe basename; derive candidates only from `declaredSkillReferences`; require exactly one basename match.
- Canonicalize before cursor validation and CLI invocation so shorthand and canonical spelling bind the same resource.
- Add regressions for zero match, ambiguity, traversal/absolute/backslash/Unicode/path-like shorthand, cross-skill isolation, canonical compatibility and cursor continuation.
- Acceptance: all SkillReader tests pass; reference-resource command is never invoked for invalid or ambiguous input; no new resource class becomes readable.

## Task 3 — Quality, atomic merge and Dev deployment
- Run formatting, focused tests, repeated tests, Feishu package race, `go test ./...`, `task test` where stable, `task lint`, diff hygiene and secret scan.
- Run independent specification/security and code-quality reviews; resolve every P0–P2 finding.
- Write S5 QA evidence, run `ndf-done`, deploy `numind-server` to Dev, and verify exact image, public health, container health, lark-cli version and critical logs.
- Acceptance: all affected gates pass; develop is pushed; Dev runs the merged image with no startup regression.

## Coverage Matrix

| Spec requirement | Task |
|---|---|
| Unique basename succeeds | 1, 2 |
| Canonical path remains compatible | 2 |
| Zero/ambiguous match fail closed | 2 |
| Unsafe/path-like input rejected | 2 |
| Cursor binds canonical resource | 2 |
| Receipt/run/version/content boundaries unchanged | 2, 3 |
| Full quality and Dev evidence | 3 |

## Dependencies

Task 1 → Task 2 → Task 3. No task can run in parallel because Task 2 consumes the RED fixture and Task 3 reviews the final Task 2 result.
