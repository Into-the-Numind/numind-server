# Feishu Scope Preflight Null Contract — Implementation Plan

## Task 1 — Customer regression (RED)
- File: `internal/numind/biz/feishu/scope_preflight_test.go`
- Add the exact `lark-cli 1.0.68` successful payload with `missing:null`.
- Acceptance: focused test fails on develop with `required fields missing`; commit is first on the feature branch.

## Task 2 — Exact fixed-version parser (GREEN)
- Files: `internal/numind/biz/feishu/scope_preflight.go`, `scope_preflight_test.go`
- Preserve JSON field presence, normalize only official null empty-slice shapes, recognize exact auth-state negatives, and retain strict partition/exit validation.
- Acceptance: customer RED plus all scope preflight contract tests pass.

## Task 3 — Resume safety regression
- File: `internal/numind/biz/feishu/operation_service_test.go` only if existing coverage cannot express the official payload through the real preflight boundary.
- Prove a resumed pre-write operation reaches one business execution after the success predicate and that an auth-state negative still waits without business invocation.
- Acceptance: no duplicate write or inference path is added; relevant operation tests pass.

## Task 4 — Quality, merge and Dev
- Run focused tests, `go test ./...`, Feishu race tests and `task lint`.
- Perform independent spec and code-quality review, write S5 QA evidence, run `ndf-done`, deploy server to Dev, verify image identity and health.
- Acceptance: all gates pass; Dev is ready for the customer prompt.
