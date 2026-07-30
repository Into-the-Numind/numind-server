# T6 Production Capacity Gate

Date: 2026-07-30

## Product behavior

- Customer Sandbox traffic remains blocked unless capacity evidence contains at
  least seven days of established history or an explicitly selected 72-hour
  fresh sampling window.
- The evidence must be continuous with no gap over one hour, end no more than
  one hour before evaluation, and contain no sample over five minutes in the
  future.
- Business-period evidence is derived from timestamps using the fixed
  Asia/Shanghai 08:00-23:00 window. A CSV cannot choose which memory samples
  count toward the P1.

## Capacity formula

- `baseline = business-period MemAvailable P1` using nearest rank.
- `parent_max = floor64MiB(min(2.75GiB, baseline - 1.25GiB))`.
- `workload_max = parent_max - 384MiB control - 128MiB headroom`.
- `workload_high = min(2GiB, 90% * workload_max)`.
- Recovery is 80% and proactive shed is 96% of `workload_max`.
- A parent below 2GiB or workload below 1.5GiB blocks release.
- The fixed control slice remains 256MiB high / 384MiB max.

## Release-gate hardening

- Go uses a trusted current clock at its public entry. Only an unexported test
  helper accepts an injected time.
- A private seal binds the P1 baseline, parent source formula, and every
  systemd value. Zero, blocked, or publicly modified plans cannot export
  deployment values.
- The calculator runs Python in isolated mode, requires `O_NOFOLLOW`, verifies
  one regular single-link evidence inode, uses strict CSV, and checks the
  inode metadata again after reading.
- Blocked JSON contains a stable reason and evidence summary but no `systemd`
  map. Ready JSON contains aggregate evidence only, never raw samples, paths,
  credentials, or user data.

## Review and verification

- `go test ./internal/numind/sandboxbroker -run Capacity -count=1`: PASS.
- `bash scripts/cicd/test-sandbox-capacity.sh`: PASS.
- `bash -n` for both capacity scripts: PASS.
- `go vet ./internal/numind/sandboxbroker`: PASS.
- The full `task lint` reached and passed repository `go vet`; installing
  `golangci-lint@latest` then failed only because `proxy.golang.org` timed out.
- Final independent specification review: PASS, P0=0, P1=0, P2=1.
- Final independent quality review: PASS, P0=0, P1=0, P2=1.

The remaining P2 is test-only: add independent fixed timestamp assertions for
Shanghai 07:59, 08:00, 22:59, and 23:00 so the fixture cannot share the same
off-by-one as the production classifier.

Prod configuration, Prod services, databases, and user data remain untouched.
