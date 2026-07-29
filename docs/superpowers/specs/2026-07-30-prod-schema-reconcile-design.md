# Prod Schema Reconcile — Detailed Design

**Feature:** `prod-schema-reconcile`
**Track:** NDF Standard
**Stage:** S2
**Date:** 2026-07-30

## 1. Objective

Prepare the current backend schema and system routing for the full Dev-to-Prod
rollout without copying Dev data or mutating any pre-existing Prod customer or
billing value.

The deliverable is an explicit, idempotent migration package. Production remains
read-only during implementation and rehearsal; the package is not permission to
run DDL in Prod.

## 2. Decisions

### D1 — Reconcile final state, do not replay migration history

Prod is partially upgraded. The new migration represents the final required
state and guards every additive operation with `information_schema`.

Rejected:

- replaying historical SQL in timestamp order;
- relying on GORM AutoMigrate during API boot;
- copying the Dev database.

### D2 — Separate schema changes from application deployment

Order for Dev rehearsal and final Prod rollout:

1. read-only preflight;
2. backup;
3. apply schema/config migration;
4. verify;
5. start the new backend;
6. run product smoke tests.

The API is never used as the migration runner.

### D3 — Additive schema rollback

New tables, columns, indexes, and constraints remain after an application-image
rollback. Old code ignores them. Destructive down migrations are intentionally
not provided because they could delete data created after traffic is enabled.

If a protected old value changes unexpectedly, stop before image deployment and
restore from the pre-deploy backup.

### D4 — Product scope

Enabled:

- notification center;
- document editor;
- Feishu personal workspace;
- attachment canonical parsing;
- weekly membership schema;
- Qwen 3.5 Flash attachment vision route.

Disabled and excluded:

- meeting copilot;
- speaker diarization;
- `chatbot_query_rewrite`;
- `universal_rewriter`.

### D5 — Historical subscription preservation

For every historical row, only these newly created columns are populated:

- `plan_type = 'monthly'`
- `cycle_credits = 2000`

The pre-existing column projection is hashed before and after apply. The migration
does not update `first_started_at`, `current_started_at`, `expires_at`,
`total_months_purchased`, `source`, `granter_user_id`, timestamps, or any credit
ledger.

### D6 — Current model width wins over stale early migrations

User identity columns use `BIGINT UNSIGNED`, matching `user.id` and the currently
running Dev schema. `document.user_id`, `document.parent_user_id`, and all Feishu
user identity columns therefore use `BIGINT UNSIGNED`.

### D7 — Attachment schema tags become explicit

`AgentAttachment` tags are updated so AutoMigrate agrees with the authoritative
schema:

- SHA field: `VARCHAR(71) NOT NULL DEFAULT ''`;
- byte size: `BIGINT NOT NULL DEFAULT 0`;
- page count: `INT NOT NULL DEFAULT 0`;
- parsed timestamp: `DATETIME(3) NULL`.

This prevents architecture-dependent Go `int` inference from turning page count
into `BIGINT`.

### D8 — Existing target structures fail closed

An interrupted run may leave a subset of the new tables. Re-entry is allowed
only when every existing table, column, index, and foreign key exactly matches
the reviewed final contract. Missing whole tables or guarded columns are added.
Same-name structures with a different type, default, order, target, or delete
rule stop in preflight; the migration never reshapes or backfills them.

## 3. Artifacts

### 3.1 Canonical migration

`migrations/20260730_120000_prod_schema_reconcile.sql`

Contains only:

- guarded additive DDL;
- exact notification constraints;
- exact system configuration UPSERTs;
- narrowly targeted project-seed cleanup.

It must contain no:

- `DROP TABLE`;
- `TRUNCATE`;
- `DELETE` against customer-owned rows;
- updates to existing subscription columns;
- meeting schema;
- credentials or secrets.

Temporary migration procedures may be dropped after use; the ban on `DROP TABLE`
does not prohibit `DROP PROCEDURE IF EXISTS` for a migration-local helper.

### 3.2 Read-only preflight

`scripts/2026-07-30-prod-schema-reconcile/00-preflight.sql`

Outputs stable rows with `check_name`, `status`, `observed`, and `expected`.
Blocking checks:

- MySQL major version is 8;
- required base tables exist;
- required existing column types match;
- no partial document/Feishu table with an unknown shape;
- notification FK orphan counts are zero;
- `llm_provider.name='ali-dashscope'` exists exactly once;
- existing subscription rows do not contain weekly metadata before the new
  columns are created;
- no duplicate stable keys exist in AI service/task/pricing tables.

Preflight performs only `SELECT`, `SET`, and metadata inspection.

### 3.3 Verification

`scripts/2026-07-30-prod-schema-reconcile/02-verify.sql`

Verifies:

- all required tables/columns;
- column types/nullability/defaults;
- document and Feishu tables are empty during first rollout;
- seven notification FKs and two notification unique keys;
- `idx_ar_state_pending`;
- one active Qwen 3.5 service, one active Ali route, one attachment task route,
  and one active pricing row;
- old Qwen route disabled but row retained;
- subscription metadata distribution;
- no official template/example seed rows.

### 3.4 Runbook

`scripts/2026-07-30-prod-schema-reconcile/README.md`

Defines exact Dev rehearsal and final Prod steps, expected output, backup evidence,
stop conditions, and post-migration product checks.

### 3.5 Tests

- `migrations/prod_schema_reconcile_test.go`
  - required statements and stable keys exist;
  - forbidden destructive statements absent;
  - protected subscription columns never appear on the left side of an update.
- `scripts/2026-07-30-prod-schema-reconcile/test-mysql8.sh`
  - starts an isolated MySQL 8 container;
  - loads a synthetic partial-Prod baseline;
  - records the old subscription projection;
  - runs apply twice;
  - runs verify;
  - confirms the old projection is unchanged.
- `scripts/2026-07-30-prod-schema-reconcile/testdata/prod-partial-baseline.sql`
  - minimal affected-table baseline only;
  - synthetic users/subscriptions, no real customer data.

## 4. Schema Contract

### 4.1 New final-state tables

| Table | Primary identity | Required uniqueness |
|---|---|---|
| `document` | `id BIGINT UNSIGNED` | `(user_id, source_object_key)` |
| `user_third_party_account` | `id BIGINT UNSIGNED` | `(user_id, provider)` |
| `feishu_cli_vault` | `user_id BIGINT UNSIGNED` | primary key |
| `feishu_auth_session` | `id CHAR(36)` | primary key |
| `feishu_operation` | `id CHAR(36)` | `(user_id, idempotency_key)` |
| `feishu_operation_proof_consumption` | `source_operation_id CHAR(36)` | consumer operation |
| `feishu_operation_execution_gate` | `user_id BIGINT UNSIGNED` | primary key |

All use InnoDB, `utf8mb4_unicode_ci`, and `ROW_FORMAT=DYNAMIC`.

The proof-consumption table keeps its two restrictive FKs to `feishu_operation`.
The remaining Feishu tables intentionally avoid cross-table FKs because the
runtime state machine controls lock order and generation fencing.

### 4.2 Existing table additions

| Table | Addition |
|---|---|
| `subscription` | `plan_type VARCHAR(20) NOT NULL DEFAULT 'monthly'` |
| `subscription` | `cycle_credits INT NOT NULL DEFAULT 2000` |
| `agent_attachment` | `parsed_content LONGTEXT NULL` |
| `agent_attachment` | `parsed_content_sha256 VARCHAR(71) NOT NULL DEFAULT ''` |
| `agent_attachment` | `parsed_content_byte_size BIGINT NOT NULL DEFAULT 0` |
| `agent_attachment` | `parsed_page_count INT NOT NULL DEFAULT 0` |
| `agent_attachment` | `parsed_at DATETIME(3) NULL` |
| `agent_run` | `idx_ar_state_pending(state_reason, pending_question_at)` |

`pending_external_action_json/at` are verified, not re-added in the known Prod
state. The canonical migration still guards and adds them if absent.

### 4.3 Notification constraints

Before each FK is added, preflight must prove zero orphan rows.

| Constraint | Child | Parent | Delete |
|---|---|---|---|
| `fk_annread_announcement` | `announcement_read.announcement_id` | `announcement.id` | CASCADE |
| `fk_annread_user` | `announcement_read.user_id` | `user.id` | CASCADE |
| `fk_sq_announcement` | `survey_question.announcement_id` | `announcement.id` | CASCADE |
| `fk_sr_announcement` | `survey_response.announcement_id` | `announcement.id` | CASCADE |
| `fk_sr_user` | `survey_response.user_id` | `user.id` | CASCADE |
| `fk_sa_response` | `survey_answer.response_id` | `survey_response.id` | CASCADE |
| `fk_sa_question` | `survey_answer.question_id` | `survey_question.id` | CASCADE |

### 4.4 System configuration

Stable keys:

- provider: `llm_provider.name='ali-dashscope'`;
- service: `ai_service.model_key='qwen3.5-flash'`;
- route: `(model_id, provider_id)`;
- task: `task_profile.task_id='attachment.vision_describe'`;
- pricing: the repository's existing unique pricing key for
  `('llm_vision','ali-dashscope','qwen3.5-flash',...)`.

The canonical Qwen migration logic is reused without copying any provider API key.

## 5. Idempotency Pattern

For every column/index/constraint:

1. query `information_schema`;
2. set a dynamic statement to the required DDL or `SELECT 1`;
3. `PREPARE`, `EXECUTE`, `DEALLOCATE`;
4. verify final metadata.

For new tables, use `CREATE TABLE IF NOT EXISTS`.

For configuration data, use unique-key `INSERT ... ON DUPLICATE KEY UPDATE` or
guarded `INSERT ... SELECT`. Second execution must keep row counts unchanged.

## 6. Protected Data Evidence

The runbook records before/after:

- row count and ordered projection hash for all 102 subscription rows, excluding
  only the two new columns;
- ordered old-field projection hashes for attachment and agent-run rows,
  excluding only newly added columns;
- extended table checksums for user, credit account/cycle/reservation/transaction,
  SOP, chatbot, and sales history tables;
- row counts of all newly created tables;
- AI configuration rows affected by stable keys.

The migration SQL contains no `UPDATE` or `DELETE` against protected customer
tables. Subscription, attachment, and agent-run changes are additive schema only.

## 7. Runtime Configuration Dependency

Schema readiness alone does not expose the features. Before the new backend is
started, the runtime-secret/config track must provide environment overrides for:

- document system enabled;
- Feishu integration enabled;
- meeting copilot disabled;
- diarization disabled;
- chatbot query rewrite disabled;
- universal rewriter disabled;
- Feishu encryption key/key version and required CLI/runtime paths.

No value is written to `config_prod.yaml`.

## 8. Dev Rehearsal

1. Back up the affected Dev tables.
2. Run preflight against Dev and classify expected already-present tables.
3. Apply the canonical migration twice.
4. Run verify.
5. Deploy current backend image.
6. Smoke:
   - balance endpoint;
   - existing monthly membership;
   - weekly membership grant in a dedicated test account;
   - notification list;
   - document open;
   - Feishu status/auth initiation;
   - attachment parse/file_read;
   - Qwen attachment image description.

No QA deployment is performed.

## 9. Prod Stop Conditions

Do not apply or deploy when any condition is true:

- full backup missing or checksum not recorded;
- current Prod schema differs from reviewed preflight;
- notification orphan count is non-zero;
- Ali provider missing/duplicated;
- any protected-table pre-hash cannot be produced;
- migration SQL SHA differs from the reviewed tag;
- required runtime secrets are absent;
- Sandbox isolation or other required backend dependency is not ready;
- final Prod execution authorization has not been given.

## 10. Acceptance

The feature reaches S5 only when static tests, MySQL 8 double-apply, backend tests,
and lint pass. It reaches S6 only after Dev migration, current-backend deployment,
and product smoke evidence. Prod execution remains S7 and requires a separate
human authorization.
