# Feishu Keychain App Evidence Implementation Plan

## Task 1 — Reproduce the customer failure

- Update the controlled fake CLI to write the pinned 1.0.68 keychain-reference shape and representative Linux keychain files.
- Add a focused test that completes `RunBlocking` and then calls `AppIDFromHome`.
- Run the focused test and commit the failing test before production code changes.

## Task 2 — Implement strict controlled evidence parsing

- Add a dedicated parser used only by `ControlledLarkCLIRunner.AppIDFromHome`.
- Accept exact official keychain evidence and non-empty legacy plaintext.
- Add negative tests for wrong source, wrong ID, extra/duplicate/case-variant fields, malformed values, and empty evidence.
- Run focused tests.

## Task 3 — Prove encrypted HOME completeness

- Add a vault round-trip test for config, keychain master key, and encrypted secret file.
- Run Feishu package tests, full test suite, and lint.

## Task 4 — Review and release to dev

- Run independent specification and code-quality reviews.
- Record S5 acceptance, merge with `ndf-done`, push `develop`, deploy the dev server, and verify health.
- Ask the user to generate a fresh link, complete the official page, and continue the original task.
