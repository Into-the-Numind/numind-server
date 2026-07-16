# Feishu Keychain App Evidence — Requirement

## Problem

After a user successfully creates a personal Feishu application through lark-cli 1.0.68, Numind marks the authorization session failed and `resume` returns an internal server error.

The pinned CLI stores `appSecret` as a keychain reference object, while the controlled completion probe currently expects a plaintext JSON string. The fake CLI fixture also uses the obsolete plaintext shape, so the real contract is not covered.

## Required behavior

- A successful controlled `config init --new` run must accept the official application evidence shape:
  `{"source":"keychain","id":"appsecret:<appId>"}`.
- The reference must be bound exactly to the same non-empty `appId`; wrong source, mismatched ID, unknown fields, malformed JSON, or empty evidence must fail closed.
- Existing non-empty plaintext fixtures/configs remain readable for backward compatibility, without returning or persisting the secret outside the encrypted CLI HOME.
- Encrypted CLI HOME round-trip coverage must include the lark-cli config and Linux keychain files (`master.key` and encrypted app-secret file).
- The user-facing lifecycle remains unchanged: create app on the official Feishu page, return to Numind, then continue the original task.

## Acceptance

1. A customer regression test using the official 1.0.68 keychain-reference shape fails before the fix and passes after it.
2. Strict negative tests reject forged or mismatched keychain references.
3. Vault round-trip restores keychain bytes exactly.
4. Focused tests, full Go tests, and lint pass.
5. The fix is merged to `develop`, deployed to dev, and `/healthz` is healthy before user validation.
