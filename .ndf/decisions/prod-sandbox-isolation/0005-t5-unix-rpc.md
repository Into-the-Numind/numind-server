# T5 Unix-Only Broker RPC

Date: 2026-07-30

## Product behavior

- The customer-facing API can reach Sandbox only through one local Unix
  socket. The broker exposes no TCP listener.
- Linux `SO_PEERCRED` admits only explicitly configured API host UIDs.
- The API never sends Docker image, mount, network, device, capability, or
  cgroup options. Responses expose lease IDs and normalized state, not Docker
  container IDs or host paths.
- The broker serves create, activate, heartbeat, output-persisting, exec,
  copy-in, copy-out, mkdir, inspect, list, and delete operations.

## Hard limits and failure handling

- Metadata is capped at 64 KiB and decoded strictly.
- Accepted connections are capped at 32.
- Copy-in and copy-out share four global streams; each lease can have only one
  stream per direction.
- Copy traffic shares a 100 MiB/s limiter and uses 64 KiB chunks.
- HTTP read/write deadlines and the copy context cap one copy session at 300
  seconds.
- Copy-out declares a completion trailer. The API drains the response and
  accepts files only when the trailer reports `complete`; a truncated stream
  is returned as unavailable.
- Every mutation request ID is journaled. Delete reserves the caller request
  ID before any runtime effect, and activation replay repairs the idempotent
  scheduler count only while the durable lease is still active.
- Create failure cleanup uses a separate bounded background context. A journal
  lookup failure cannot by itself skip deletion of a known container or
  release of its scheduler slot.
- The Unix listener disables Go's automatic pathname unlink so shutdown uses
  the broker's explicit socket identity check.

## Task boundary

T5 defines and wires the `ContainerRuntime` boundary. The concrete fixed
Docker CLI adapter remains part of T10, where the deployable
`numind-sandboxd` composition root is built. The T10 plan now explicitly owns
`runtime_adapter.go` and its tests; it may expose only the fixed templates
validated by T3.

## Review and verification

- The implementation had already passed its focused race checks and Linux
  cross-build checks before the final hardening round.
- The product owner explicitly directed this step to skip additional test
  runs and continue the rollout. The final changes therefore received a build
  check and static review only; no later test result is claimed.
- `go build ./internal/numind/sandboxbroker ./internal/numind/biz/sandbox`
  passed at the final implementation.
- Final independent specification review: PASS, P0=0, P1=0.
- Final independent quality review: PASS, P0=0, P1=0.

The previously accepted socket-parent/umask isolation work remains deferred by
the product owner. Non-blocking follow-ups are also recorded for staging
copy-out before publication, copy-in content-aware idempotency, and cleanup of
a newly created socket when its first post-bind inspection fails.

Prod configuration, Prod services, databases, and user data remain untouched.
