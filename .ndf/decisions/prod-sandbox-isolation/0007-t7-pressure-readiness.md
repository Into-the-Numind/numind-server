# T7 Pressure Admission and Readiness

Date: 2026-07-30

## Decision

Production Sandbox admission starts closed and opens only after three fresh,
healthy two-second observations. The broker closes admission on workload,
host-memory, cgroup, mount, disk, image, invalid telemetry, or sampling
failures. Real capacity pressure maps to HTTP 429; unavailable telemetry or
infrastructure maps to HTTP 503.

The production scheduler owns the final admission decision. It rechecks the
gate when granting a FIFO container slot and when activating a ready/warm
lease. `ReadinessChecker` publishes an open state only while holding a
generation-validated pressure token, so a newer pressure observation cannot be
overwritten by a stale healthy result.

Create replay is checked read-only before the write-side preflight. A closed
gate therefore creates no journal rows, while existing idempotent replay still
works. Activation reserves the scheduler slot before publishing durable Active
state and rolls back to Ready on journal failure.

Output persistence receives at most ten total seconds of protection, including
time elapsed before the first controller observation. Shed retry and lifecycle
history survive temporary sample omissions and are bounded to 4096 records.
Activation serialization is per lease and context-cancellable.

## Verification

- `go test ./internal/numind/sandboxbroker -race -count=1`: PASS
- `go vet ./internal/numind/sandboxbroker`: PASS
- `task lint`: repository-wide `go vet ./...` PASS; the later
  `golangci-lint@latest` installation could not reach `proxy.golang.org` and
  timed out before that tool could run.
- Independent specification review at `820341e5`: PASS, P0/P1/P2 = 0/0/0.
- Independent quality review at `820341e5`: PASS, P0/P1/P2 = 0/0/0.

No Prod service, database, customer record, credit balance, or
`config_prod.yaml` was changed.
