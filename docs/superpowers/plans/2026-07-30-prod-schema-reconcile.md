# Prod Schema Reconcile — Implementation Plan

**Feature:** `prod-schema-reconcile`
**Track:** NDF Standard
**Stage:** S3
**Rule:** Prod remains read-only throughout T01–T06.

## Task order

`T01 → T02 → T03 → T04 → T05 → T06 → S4 review → S5 verification → S6 Dev`

Tasks are intentionally serial because the SQL contract, model tags, test baseline,
and runbook depend on the same final schema.

## T01 — RED safety/contract tests

Files:

- `migrations/prod_schema_reconcile_test.go`

Add tests that fail until the migration package exists:

1. canonical migration, preflight, verify, test baseline, MySQL 8 runner, and
   runbook files exist;
2. canonical migration contains all required tables, fields, stable keys,
   notification constraints, and Qwen route;
3. canonical migration contains no `DROP TABLE`, `TRUNCATE`, meeting schema,
   credentials, or forbidden customer-table writes;
4. subscription update assignments are restricted to `plan_type` and
   `cycle_credits`;
5. preflight is read-only;
6. model schema tags match the final attachment/document identity types.

Commit:

`test(migrations): define prod schema reconcile contract`

Expected: focused test FAILS because implementation artifacts do not yet exist.

## T02 — Explicit model schema tags

Files:

- `internal/pkg/model/agent_attachment.go`
- `internal/pkg/model/document.go`
- focused model test if required

Changes:

- make attachment parsed-field type/null/default tags explicit;
- make document user identity columns explicit `BIGINT UNSIGNED`;
- do not change JSON/API behavior.

Verification:

- focused model tests;
- migration contract test progresses but still fails on missing SQL.

Commit:

`fix(model): align rollout schema types`

## T03 — Canonical idempotent migration

Files:

- `migrations/20260730_120000_prod_schema_reconcile.sql`

Implement:

- final-state document and Feishu tables;
- guarded subscription and attachment columns;
- guarded `agent_run` pending fields/index;
- notification unique keys/FKs;
- Qwen 3.5 service/route/task/pricing;
- exact project-seed cleanup;
- no meeting schema and no credentials.

Verification:

- contract tests;
- SQL syntax review.

Commit:

`feat(migrations): add prod schema reconcile`

## T04 — Preflight and verify SQL

Files:

- `scripts/2026-07-30-prod-schema-reconcile/00-preflight.sql`
- `scripts/2026-07-30-prod-schema-reconcile/02-verify.sql`

Implement stable, machine-readable result rows and all S2 stop checks. Both files
must be safe to run repeatedly. Preflight contains no write statement.

Verification:

- contract tests;
- read-only statement scanner;
- execute against isolated MySQL baseline after T05.

Commit:

`feat(migrations): add rollout preflight and verification`

## T05 — MySQL 8 double-apply rehearsal

Files:

- `scripts/2026-07-30-prod-schema-reconcile/testdata/prod-partial-baseline.sql`
- `scripts/2026-07-30-prod-schema-reconcile/test-mysql8.sh`

Baseline contains synthetic:

- user rows;
- two historical monthly subscriptions;
- non-empty historical attachment and agent-run rows plus notification tables
  with the current Prod partial shape;
- the old agent-run state constraint without external-resume states;
- old Qwen provider/service/route;
- empty Skill seed tables.

Runner:

1. creates an isolated MySQL 8 container/database;
2. loads baseline;
3. captures subscription, attachment, agent-run, and protected-table checksums;
4. applies migration once and verifies;
5. applies migration again and verifies;
6. simulates an interrupted exact partial state and resumes it;
7. verifies wrong same-name schema and duplicate notification rows fail preflight;
8. compares every protected projection/checksum;
9. removes only its named test container/volume.

Verification:

- local/build-host MySQL 8 double apply PASS;
- exact and partial final schemas/rows PASS;
- wrong schema and duplicate notification preflight FAIL;
- every protected projection/checksum unchanged.

Commit:

`test(migrations): rehearse prod reconcile on mysql8`

## T06 — Runbook and Dev rehearsal gate

Files:

- `scripts/2026-07-30-prod-schema-reconcile/README.md`
- `.ndf/decisions/prod-schema-reconcile/0001-s5-verification.md` (created in S5)
- `.ndf/decisions/prod-schema-reconcile/0002-s6-dev-rehearsal.md` (created in S6)

Runbook includes:

- read-only Prod preflight command;
- immutable SQL checksum;
- backup evidence fields;
- Dev apply/verify commands;
- final Prod order;
- stop conditions;
- application rollback that leaves additive schema in place;
- product-feature smoke checklist.

Do not include credentials or real API keys.

Commit:

`docs: add prod schema reconcile runbook`

## S4 — Review

Review checklist:

- spec compliance;
- SQL safety and idempotency;
- no customer-data mutation;
- no hidden meeting enablement;
- no credentials;
- table/column/index/constraint names match current models;
- Qwen configuration uses stable keys;
- test cleanup target is explicit and isolated.

Any P0/P1 finding returns to its owning task before S5.

## S5 — Verification

Required:

- `go test ./migrations`
- owned model package tests
- MySQL 8 double-apply runner
- `go test ./...`
- `task lint`
- `git diff --check`
- Prod preflight read-only output reviewed

No Prod DDL.

## S6 — Dev rehearsal

1. back up affected Dev tables;
2. run preflight;
3. apply migration;
4. run verify;
5. apply migration a second time;
6. run verify again;
7. deploy current backend user API and admin API;
8. execute product smoke tests from the S2 design.

No QA deployment.

## S7 — Prod gate

Prepare but do not execute:

- tagged backend/admin images;
- reviewed migration SHA256;
- full backup evidence;
- current preflight output;
- runtime secret/config readiness;
- Sandbox readiness;
- final deploy order and acceptance owner.

Execution requires a separate, explicit Prod authorization.
