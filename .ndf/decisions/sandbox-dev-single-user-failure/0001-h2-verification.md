# H2 Verification: sandbox-dev-single-user-failure

## Context

Dev run 313 failed for an invited customer even though the customer was the only active sandbox user at the time. Dev logs showed one Agent run trying `run_python` and then discarding five dead warm sandbox containers. Startup logs showed multiple sandbox pools created inside the same backend process, and later pools reaped containers that earlier pools still believed were warm.

## Root Cause

The sandbox startup cleanup only filtered by the generic `numind.sandbox=1` label. When another Biz instance created a second sandbox pool in the same backend process, that second pool treated the first pool's live warm containers as orphans and destroyed them. The first pool kept stale container IDs in memory, so the next customer tool call could fail even with only one user using Dev.

Two hidden duplicate Biz sources were confirmed:

- `controller/v1/user/user.go` created a new Biz while the main entrypoint already owned one.
- `admin_router.go` created a new Biz for admin route wiring.

## Fix

Sandbox containers now include owner and boot labels:

- `numind.sandbox.owner`
- `numind.sandbox.owner_boot`

Startup cleanup now keeps containers owned by a live same-boot peer pool, while still reaping legacy unlabeled containers and stale containers from previous process boots. User and admin route wiring now prefer the entrypoint-owned `biz.B` and only fall back to `biz.NewBiz` when no entrypoint Biz exists.

## Verification

- RED-first commit: `9544c82c test(qa): reproduce sandbox peer pool reaping`.
- Focused sandbox tests passed.
- Focused Agent sandbox/tool-selection tests passed.
- Focused document tests passed.
- Full backend `go test ./...` passed on rerun. The previous isolated failure was an external model free-tier 403 in an Agent error-path test and reran green.
- `task lint` passed. macOS sqlite-vec dependency warnings are pre-existing and unrelated.

## Dev Memory Observation

Before the fix, Dev had 10 idle sandbox containers because duplicate pools had been initialized. `docker stats --no-stream` showed each idle sandbox at roughly 632-648 KiB of real memory usage against a 512 MiB container memory limit. The 512 MiB value is a cap, not preallocated memory.
