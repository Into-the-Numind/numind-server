# T4 Global Five-Slot Scheduler

Date: 2026-07-30

## Product behavior

- The broker owns one global pool shared by rolling API owners.
- At most five containers can be creating, ready, or active.
- At most five tasks can be active.
- A sixth task waits in strict FIFO order for at most 30 seconds.
- Capacity is released only after container destruction completes.
- When five tasks are active, the broker does not create another standby.
- Replaying the same request cannot consume a second slot.

## Safety decisions

- New unique queued requests are capped at 32.
- Live and recently completed replay records are capped at 4096.
- Completed replay records remain for ten minutes and are removed
  incrementally in completion order rather than by scanning the full map.
- Queue and replay-cache saturation fail closed without changing slot counts.
- The first admission context owns shared cancellation. A replay can stop
  waiting for itself but cannot cancel the original request.
- Grant checks the first admission cancellation channel and the fixed queue
  deadline under the scheduler lock before assigning a slot.
- Context values are not retained after a request reaches an outcome.

## Review and verification

- The first focused race run passed before review hardening.
- The product owner then explicitly directed the rollout to skip further T4
  test runs and continue. No later test result is claimed.
- `go vet ./internal/numind/sandboxbroker` passed on the final implementation.
- The full `task lint` attempt reached and passed `go vet`; installation of the
  lint binary then failed because `proxy.golang.org` timed out, not because of
  a code finding.
- Final independent specification review: PASS, P0=0, P1=0, P2=2.
- Final independent quality review: PASS, P0=0, P1=0, P2=2.

The remaining P2 items do not change capacity or user data: repeated callers
are ultimately bounded by the T5 connection ceiling; an exceptionally narrow
manual-cancel/deadline race can change only whether the internal error is
classified as cancelled or deadline exceeded. Dedicated regression tests for
the new queue/cache boundaries were intentionally not added under the
product-owner instruction above.

Prod configuration, Prod services, databases, and user data remain untouched.
