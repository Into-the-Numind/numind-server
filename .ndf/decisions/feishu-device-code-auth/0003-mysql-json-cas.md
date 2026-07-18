# ADR 0003: Make external-resume JSON compare-and-swap dialect-correct

## Status

Accepted after Dev S6 run 206 on 2026-07-18.

## Context

The Feishu authorization session and document operation completed successfully,
but the original Agent run could not claim the persisted tool result. The
compare-and-swap update matched the run status, state reason, cancellation flag,
deletion flag, and identity, yet affected zero rows.

`agent_run.pending_external_action_json` is a native JSON column in Dev MySQL.
The store reads it into `datatypes.JSON` and binds the resulting bytes as a text
parameter in `pending_external_action_json = ?`. MySQL compares a JSON value and
a SQL string as different types, so this predicate is false even when the text
is byte-for-byte the representation returned by the driver. The same code passes
the SQLite tests because their fixture column is TEXT.

A read-only query against the failing row proved:

- JSON column equals its `CAST(... AS CHAR)` text: false
- cast text equals the driver-returned text: true
- JSON column equals the driver-returned text parameter: false

## Decision

Centralize the exact pending-action compare-and-swap predicate in the Agent run
store. For MySQL, compare the server's textual JSON representation with the
driver-returned text; for SQLite test fixtures, retain direct TEXT equality.
Use the same helper in claim, release, touch, completion, and terminalization so
no later lifecycle stage repeats the dialect bug.

Add a real MySQL integration regression that fails on the current implementation
and proves the full claim/transition lifecycle. SQLite unit tests remain useful
for state-machine coverage but are not sufficient evidence for JSON-column CAS.

## Consequences

- The original Agent result can be claimed after a successful Feishu operation.
- Every external-resume transition keeps the same exact-value concurrency fence.
- Database-dialect behavior becomes an explicit release gate instead of an
  assumption inferred from SQLite.
