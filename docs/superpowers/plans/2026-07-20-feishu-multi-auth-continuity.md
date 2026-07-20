# Implementation plan

1. Add RED backend regressions for snapshot identity, multi-yield safe result history, and read-only verification after unknown write.
2. Add RED frontend regressions for the real snapshot shape, one-shot URL recovery, narration follow signal, and explicit-error preservation.
3. Implement the backend snapshot contract and provider-safe continuation history.
4. Implement bounded read-only unknown-result verification without relaxing write safety.
5. Implement frontend reconciliation, one-shot URL recovery, follow-scroll, and truthful terminal rendering.
6. Run focused suites and mandatory lint/type checks; self-review security and lifecycle races.
7. Merge and push both `develop` branches, deploy backend first and frontend second to Dev, then health-check and hand off the real two-authorization scenario.
