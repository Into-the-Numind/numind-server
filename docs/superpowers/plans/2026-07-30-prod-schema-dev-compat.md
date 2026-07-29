# Dev schema compatibility implementation plan

Date: 2026-07-30  
Track: Standard  
Repository: `numind-server`

## T1 — Define failing compatibility contracts

Files:

- `migrations/prod_schema_reconcile_test.go`
- `scripts/2026-07-30-prod-schema-reconcile/test-mysql8.sh`
- `scripts/2026-07-30-prod-schema-reconcile/testdata/prod-partial-baseline.sql`

Work:

1. Add static requirements for order-independent column fingerprints, the two
   exact attachment shapes/state matrix, relational legacy Agent states,
   proof FK repair, and removal of attachment AutoMigrate.
2. Add MySQL 8 scenarios that reproduce the current Dev preflight failures.
3. Add negative cases for third attachment shape, arbitrary zombie state,
   proof orphan/type/partial-FK state, and wildcard-like external state.
4. Commit the failing contract tests before implementation.
5. Add complete attachment/proof business projections and a real MySQL 8 GORM
   legacy-NULL read gate.

Verify:

- Go contract tests fail for the missing implementation.
- The MySQL runner fails at the intended Dev-compat scenario.

## T2 — Make table fingerprints order-independent

Files:

- `scripts/2026-07-30-prod-schema-reconcile/00-preflight.sql`
- `scripts/2026-07-30-prod-schema-reconcile/02-verify.sql`
- expected contract fixtures in tests/runbook

Work:

1. Remove `ORDINAL_POSITION` from the column fingerprint payload.
2. Sort the complete column payload by `COLUMN_NAME`.
3. Add column charset/collation and index direction/visibility/expression.
4. Recompute all seven expected hashes from canonical MySQL 8 tables.
5. Keep engine, table collation, row format, and complete metadata fail closed.

Verify:

- Canonical tables pass.
- Reordered columns pass with the same hash.
- Missing/extra/wrong-shape columns still fail.

## T3 — Accept only the verified attachment shapes

Files:

- `scripts/2026-07-30-prod-schema-reconcile/00-preflight.sql`
- `scripts/2026-07-30-prod-schema-reconcile/02-verify.sql`

Work:

1. Represent absent, final prefixes, complete final, and complete Dev legacy
   as the only allowed preflight states.
2. Verify accepts only complete final or complete legacy.
3. Gate legacy rows with a real MySQL 8 GORM read test including NULL values.
4. Remove AgentAttachment from startup AutoMigrate and fail when the table is
   absent instead of silently creating/modifying it.
5. Keep the migration additive-only; do not `MODIFY`, `UPDATE`, or backfill
   attachment rows.
6. Hash all five parsed fields before/after both migrations and service startup.

Verify:

- Final shape passes.
- Dev historical shape passes.
- Any mixed/third shape fails.
- Attachment protected projection stays identical.

## T4 — Tighten the exact Agent CHECK

Files:

- `migrations/20260730_120000_prod_schema_reconcile.sql`
- `scripts/2026-07-30-prod-schema-reconcile/00-preflight.sql`
- `scripts/2026-07-30-prod-schema-reconcile/02-verify.sql`
- runner baseline

Work:

1. Add relational branches for running+`''` and deleted+exact
   `zombie_cleanup_2026_05_28`.
2. Recompute the normalized exact CHECK SHA.
3. Preserve the `LEFT(state_reason, 11) = 'ext_resume:'` prefix test.

Verify:

- Existing exact values, running+empty, deleted+zombie marker, and external
  resume states pass.
- Terminated+empty, active+zombie, arbitrary unknown/`extXresume:` values fail.
- No Agent row changes across migration.

## T5 — Repair missing Feishu proof foreign keys safely

Files:

- `migrations/20260730_120000_prod_schema_reconcile.sql`
- `scripts/2026-07-30-prod-schema-reconcile/00-preflight.sql`
- `scripts/2026-07-30-prod-schema-reconcile/02-verify.sql`
- MySQL runner

Work:

1. Preflight and the migration's first assertion allow only zero proof FKs or
   the exact final pair.
2. Both layers check source/consumer orphans and parent/child metadata.
3. When zero, add both restrictive FKs in one atomic `ALTER TABLE`.
4. When exact two, do nothing; any other state fails before apply.
5. Hash every proof business column before/after both migrations and startup.

Verify:

- Populated valid proof rows survive zero-FK repair.
- Double apply stays stable.
- Orphans, type mismatch, one/wrong FK fail.

## T6 — Revalidate and hand back to S6

Files:

- `scripts/2026-07-30-prod-schema-reconcile/README.md`
- migration SHA pin
- NDF verification decision

Work:

1. Run focused Go tests and migration race tests.
2. Run the complete isolated MySQL 8 runner.
3. Run `go test ./... -count=1` and `task lint`.
4. Run real Prod SELECT-only preflight; require no FAIL rows.
5. Run dual independent S4 reviews; resolve every P0/P1.
6. Merge with `ndf-done`, then resume the already-backed-up Dev S6 rehearsal.

Verify:

- All gates pass and worktree is clean.
- Reviewed migration SHA matches the runbook.
- Prod remains read-only.

## Dependencies

`T1 → T2 → T3 → T4 → T5 → T6`.

All writes are serial because the SQL files overlap. Review and read-only
environment inspection may run independently.
