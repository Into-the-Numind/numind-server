# Feishu Keychain App Evidence — S5 Acceptance

## Customer regression

- First code commit: `5beccf2b test(qa): reproduce feishu keychain app evidence failure`.
- Before production changes, the focused test failed because the controlled completion probe rejected lark-cli 1.0.68's official keychain-reference object after successful config initialization.
- After `92d32139 fix(feishu): accept keychain-backed app evidence`, the same test passes.

## Automated verification

- Focused official-config, strict-reference, and encrypted-HOME round-trip tests: PASS.
- Focused race test: PASS.
- `go test ./...`: PASS.
- `PATH="$(go env GOPATH)/bin:$PATH" task lint`: PASS.
- `git diff --check`: PASS; worktree clean.
- The only emitted compiler warnings are the repository's pre-existing macOS sqlite-vec deprecation warnings.

## Security and regression coverage

- Accepts exact official `source=keychain`, `id=appsecret:<same appId>` evidence.
- Rejects wrong source/ID, duplicates, case variants, unknown fields, malformed/trailing JSON, empty evidence, and invalid app IDs.
- Retains non-empty plaintext config compatibility only inside the dedicated non-secret completion probe.
- Returns only app ID; no secret or keychain reference enters API responses or logs.
- Vault round-trip preserves config, master key, and encrypted app-secret bytes.

## Independent review

- Specification/security review: PASS, P0/P1/P2 = 0.
- Code-quality/test review: PASS, P0/P1/P2 = 0.
- Both reviewers confirmed the customer-bug commit ordering and fail-closed behavior.

## Remaining acceptance

Merge and deploy the dev server, verify health, then repeat the real Feishu flow with a fresh link. Production deployment is out of scope.
