# T1 Broker Client Review Hardening

Date: 2026-07-30

## Context

The first independent S4 review of T1 failed with 0 P0, 5 P1, and 3 P2
findings in each of the spec-compliance and code-quality reviews. The findings
covered the warm-lease business binding, mutation idempotency, copy lifecycle,
bounded streaming, cancellation, strict protocol decoding, file extraction,
safe configuration ceilings, and connection cleanup.

## Decisions

1. A warm lease is created as the explicit `ready/unbound` state with
   `agent_run_id=0` and `sandbox_session_id=0`.
2. After the durable `agent_sandbox_session` row exists, the API must call
   `activate` with both positive IDs. A binding failure destroys the lease and
   prevents user code execution.
3. Every mutation, including `DELETE`, carries the same UUID request ID in the
   JSON body and `X-Numind-Request-ID` header.
4. Copy-out first transitions the lease to `persisting`.
5. Client configuration may lower, but never raise, the compiled safety
   ceilings.
6. Request and response streams are bounded, context cancellation closes an
   underlying copy-in reader, and the broker transport closes idle Unix
   connections with the owning pool.
7. Broker JSON is strict and single-valued. Unknown broker error codes and host
   filesystem paths are not returned to product callers.
8. Tar extraction uses descriptor-relative `openat` operations with
   `O_NOFOLLOW`; path traversal, links, device nodes, FIFOs, overwrites, and
   configured size/count overages fail closed.
9. Stable owner and per-process owner boot ID remain separate protocol fields.
   Startup listing filters by stable owner so a restarted API can find its
   previous boot's leases.
10. Pool shutdown cancels and joins all warm-up/spawn workers before draining
    leases and closing the broker transport.
11. Broker copy-in accepts finite in-memory readers or cancellable
    `io.ReadCloser` streams; arbitrary blocking non-closers fail before I/O.
12. Required response fields and normalized states are validated in addition
    to strict JSON syntax.
13. Broker owner identity comes from the required stable deployment-slot
    setting `sandbox.broker_owner_id`; Docker hostname and caller labels cannot
    override it.
14. Copy-in accepts only finite in-memory readers or `*io.PipeReader`. Neither
    a generic `io.ReadCloser` nor `*os.File` is treated as proof that Close
    interrupts a blocked Read.
15. Inspect owner and owner-boot fields are required protocol response fields.
16. Create expiry and list lease IDs are presence-aware required fields.
    Missing, null, expired, empty, or duplicate values fail closed so malformed
    broker responses cannot suppress orphan cleanup.
17. The active Unix shutdown regression requires the server to observe request
    cancellation before the pool close is allowed to pass.

## Verification

- Focused broker tests pass repeatedly.
- Full Sandbox package tests pass.
- Focused race tests pass.
- `go vet ./internal/numind/biz/sandbox` passes.
- Repository `task lint` passes.

T1 remains unreviewed until both independent reviewers re-run against the
hardened commit and return PASS.
