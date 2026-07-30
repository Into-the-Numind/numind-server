# T8/T9/T11 batch verification

Date: 2026-07-30

## Scope completed

- T8: broker startup recovery reconciles the durable journal with the dedicated
  Rootless runtime, restores scheduler slots for recoverable leases, deletes
  orphan/stale dedicated-daemon containers, and leaves bounded persistent
  `recovery_pending` compensation on daemon/list/inspect failures or timeout.
- T9: Unix-socket `/healthz`, `/readyz`, `/metrics`, low-cardinality Prometheus
  rendering, and structured redacted audit events are wired into the production
  `JournalRPCService`.
- T11: `numind-sandbox-reconcile` is a real dry-run-by-default command wired to
  the broker socket plus app DB. It only processes broker-reported
  recovery-pending leases, reconciles app session/run/reservation state through
  narrow store/finalizer interfaces, and never performs direct user-balance SQL.

## T11 interface decision

The original T11 file list did not include broker server/journal files, but a
safe implementation cannot read the broker SQLite journal file directly and
should not touch Docker. Therefore T11 exposes a small Unix-socket-only recovery
surface:

- `GET /v1/recovery-pending?limit=N`
- `POST /v1/recovery-pending/:lease_id/reconciled`

These routes still require the existing Unix peer authorization, expose no
container IDs or host paths, and only return content-free IDs required to match
broker leases to app DB rows.

Broker leases are marked reconciled only after app DB session/run/reservation
cleanup succeeds. A regression test covers the failure path so a failed refund
or DB write cannot hide a lease from future reconcile runs.

## Verification

- `go test ./internal/numind/sandboxbroker ./internal/numind/sandboxreconcile ./cmd/numind-sandbox-reconcile -race -count=1`
- `go test ./...`
- `PATH="$(go env GOPATH)/bin:$PATH" GOPROXY=https://goproxy.cn,direct task lint`

All passed. The only non-failing output was the existing macOS
`sqlite3_auto_extension` / `sqlite3_cancel_auto_extension` deprecation warning
from `sqlite-vec`.

## Review

Parallel quick review initially found one P1: broker reconciliation could be
marked complete after a store failure. The implementation now returns before
`Broker.MarkLeaseReconciled` when any DB cleanup step fails, and
`TestServiceDoesNotMarkBrokerWhenStoreReconcileFails` verifies the behavior.
The reviewer recheck passed.
