# Feishu Keychain App Evidence — Proposal

## Decision

Keep the existing personal-app product flow and repair only the controlled app-creation completion contract.

`ControlledLarkCLIRunner.AppIDFromHome` will use a dedicated evidence parser that understands the pinned lark-cli 1.0.68 union representation for `appSecret`. It returns only the app ID. It accepts an official exact keychain reference, plus a non-empty plaintext value for backwards compatibility, and rejects every ambiguous shape.

The legacy helper that reads a plaintext app secret will not be widened. That avoids accidentally changing the older runner's credential semantics.

## Why

The CLI's successful exit already proves its create flow and post-save checks completed. The exact keychain reference binds the encrypted secret material to the reported app ID, while Numind's encrypted HOME vault preserves both the config and keychain files. Numind does not need to extract the secret to prove app creation.

## Scope

- Backend parser and controlled runner only.
- Regression, strict-validation, and vault round-trip tests.
- No API, database, frontend, permission, or product-flow change.
