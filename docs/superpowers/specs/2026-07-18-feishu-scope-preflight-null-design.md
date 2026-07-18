# Feishu Scope Preflight Null Contract Design

## Problem

Pinned `lark-cli 1.0.68` builds the successful `auth check --json` result from a nil Go slice. With every requested scope granted, the wire payload contains a present `missing` field whose value is `null`. Numind currently decodes both an absent field and JSON null to a nil slice, then rejects both. The resumed operation therefore fails before the Docs write starts.

## Considered approaches

1. **Recommended: exact shape-aware decoding.** Preserve whether each field was present, accept explicit `null` only where the pinned CLI can emit an empty slice, then validate the complete requested partition and process exit code.
2. Upgrade or patch the CLI to always emit `[]`. Rejected because the server is pinned to an upstream release and must correctly consume that release's contract.
3. Treat every nil array as empty. Rejected because absent required fields would become indistinguishable from an official empty result and weaken the fail-closed boundary.

The customer approved approach 1 with a rapid Standard implementation.

## Contract

- Exit 0: `ok=true`; `granted` is present and equals the complete requested set; `missing` is present and is either `null` or an empty array; `error` and `suggestion` are absent.
- Exit 1, scope gap: `ok=false`; `granted` and `missing` are present (either may use `null` only for an empty set); the two values form the exact requested partition; `missing` is non-empty.
- Exit 1, auth state: `ok=false`; `error` is exactly `not_logged_in` or `no_token`; `granted` is absent; `missing` is present and equals all requested scopes. This produces normal user-authorization recovery.
- Unknown fields, duplicate fields, absent `missing`, unexpected error values, stderr output, contradictory exit codes, incomplete/overlapping partitions, and unregistered scopes remain rejected.

## Data flow and safety

Only `ControlledScopePreflight` changes. It still executes the server-owned fixed argv inside the encrypted current-user HOME. The result remains a caller-safe `Granted/Missing` partition. Operation persistence, recovery signatures, write confirmation, CLI invocation, and Agent continuation are unchanged, so the repair cannot introduce a second business write path.

## Tests

- Customer RED using the exact successful null payload.
- Exact happy, partial, no-token, not-logged-in, absent-field and ambiguous-shape unit cases.
- Existing operation resume tests, full Feishu package, full repository, race and lint gates.
- Dev deployment health plus a real post-authorization Agent write acceptance prompt.
