# Weekly to annual membership conversion — S5 acceptance

Date: 2026-08-10  
Reviewed commits: `fde34435`, `14fc5558`, `e8d12d86`

## Result

PASS for the membership change and the completed production data calibration.

## Verification evidence

- The first feature commit is a failing customer-regression test proving that
  granting annual/monthly membership on an active weekly subscription used to
  succeed incorrectly.
- `go test ./internal/numind/biz/membership -count=1`: PASS.
- Relevant plan-transition regression tests: PASS.
- `go test -tags sqlite_fts5 -race ./internal/numind/biz/membership -count=1`:
  PASS.
- `go test ./...`: PASS.
- `task lint`: PASS.
- `go test -tags sqlite_fts5 -race ./internal/numind/biz/feishu -count=1`:
  PASS.
- Two full `task test` attempts exposed pre-existing, timing-sensitive Feishu
  authorization tests. The failing cases are outside this change; both passed
  when repeated five times, and the full Feishu race suite subsequently passed.
- Independent specification and quality reviews: PASS, with no open P0/P1/P2.
- Production calibration was protected by a prior backup, transaction
  preconditions, and post-write verification. Users 561, 568, 569, 575, and
  584 now have monthly `cycle_credits=2000`; user 569 has exactly 12 purchased
  months; user 584's annual membership begins when the retained weekly period
  ends.

## S6 handoff

Merge and deploy the backend guard. It rejects a monthly/annual grant while a
weekly subscription is still active, and every new, renewed, or reopened
monthly subscription explicitly writes the monthly plan and 2,000-credit cycle.
