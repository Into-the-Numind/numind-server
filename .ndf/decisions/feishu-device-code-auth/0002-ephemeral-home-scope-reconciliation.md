# ADR 0002: Reconcile split-device scopes from durable session evidence

## Status

Accepted after Dev S6 failure on 2026-07-18.

## Context

Numind runs `lark-cli auth login --scope ... --no-wait --json` in a temporary,
non-published HOME and stores the returned device code separately with
authenticated encryption. This is deliberate: starting authorization must not
publish a mutated credential HOME.

Official lark-cli v1.0.68 stores the requested scope string only in
`HOME/.lark/cache/auth_login_scopes/<device-code>.json`. Because the start HOME
is discarded, the later candidate HOME cannot load that cache. After successful
authorization it therefore emits a valid completion payload with the final
`granted` scopes but an empty `requested` array.

The Numind adapter required `requested` to be non-empty and classified this
official success payload as a terminal protocol failure before candidate-HOME
reconciliation. The API consequently returned HTTP 500 after the user had
actually authorized successfully.

## Decision

Pass the exact canonical scopes from the durable authorization session into the
controlled completion boundary. Accept an empty CLI `requested` array only for
this split-HOME case, and require every durable requested scope to be present in
the CLI's final `granted` scope set with no missing scopes. Continue to require a
successful candidate-HOME `auth status` check and exact App ID equality before
publishing the candidate.

Do not infer success from localized stderr, exit status alone, or user action.
Do not weaken duplicate-key, unknown-field, identity, canonical-scope, lease,
generation, or atomic-publication checks.

## Consequences

- Split authorization works without persisting lark-cli's short-lived cache.
- Authorization remains fail-closed against under-granted scopes or a different
  application/account HOME.
- The customer regression must begin with a failing test based on the official
  v1.0.68 no-cache completion shape.
