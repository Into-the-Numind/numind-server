# Prod schema reconcile — Dev historical compatibility design

Date: 2026-07-30  
Track: Standard  
Repository: `numind-server`

## 1. Scope

This follow-up repairs the rollout package after the S6 Dev preflight exposed
real historical schema states. It changes migration safety contracts only. It
does not change product APIs, UI, feature flags, customer values, credit values,
attachment content, Agent history, or Feishu business rows.

## 2. Non-negotiable invariants

1. `00-preflight.sql` remains SELECT-only.
2. The migration contains no DML against protected customer, subscription,
   three-pool credit, attachment, Agent, SOP, chatbot, sales, or Feishu business
   tables.
3. Prod missing tables/columns still receive the exact reviewed final schema.
4. Unknown third schema states fail before apply.
5. Migration and verification remain idempotent.

## 3. Table contract normalization

The seven document/Feishu table fingerprints continue to include:

- engine;
- table collation;
- row format;
- every column name, type, nullability, default, and extra attribute;
- every index name, uniqueness flag, indexed column order, prefix length, and
  index type.

`ORDINAL_POSITION` is removed from the column fingerprint and columns are sorted
by `COLUMN_NAME`. Physical column order does not affect MySQL reads/writes by
named columns and is not a product contract. The fingerprint adds column
`CHARACTER_SET_NAME` and `COLLATION_NAME`, plus index `COLLATION`, `IS_VISIBLE`,
and `EXPRESSION`. Missing, extra, differently collated, invisible, descending,
expression-based, prefixed, or differently typed contracts still fail.

## 4. Attachment compatibility

Preflight accepts only the following complete state matrix:

1. all five columns absent;
2. a migration-produced final prefix, in this order:
   `parsed_content` → `parsed_content_sha256` →
   `parsed_content_byte_size` → `parsed_page_count` → `parsed_at`;
3. the complete final shape;
4. the complete verified Dev historical shape.

Any other missing-column combination, final/legacy mixture, or third shape
fails. Verify accepts only complete final or complete Dev historical shape.

### Final Prod shape

| Column | Shape |
|---|---|
| `parsed_content` | `longtext NULL` |
| `parsed_content_sha256` | `varchar(71) NOT NULL DEFAULT ''` |
| `parsed_content_byte_size` | `bigint NOT NULL DEFAULT 0` |
| `parsed_page_count` | `int NOT NULL DEFAULT 0` |
| `parsed_at` | `datetime(3) NULL` |

### Verified Dev historical shape

| Column | Shape |
|---|---|
| `parsed_content` | `longtext NULL` |
| `parsed_content_sha256` | `varchar(71) NULL DEFAULT NULL` |
| `parsed_content_byte_size` | `bigint NULL DEFAULT 0` |
| `parsed_page_count` | `bigint NULL DEFAULT 0` |
| `parsed_at` | `datetime(3) NULL` |

The migration does not `MODIFY` these columns and does not backfill rows. Prod
currently has zero attachment rows and missing columns, so it still receives
the final shape.

The row gate and test evidence cover legacy NULLs in SHA/byte-size/page-count.
A real MySQL 8 + GORM store test must prove those NULLs scan into the current
non-pointer Go model without error before the legacy schema can pass. If the
test fails, the legacy shape is not accepted and the implementation must choose
an explicit model/query compatibility change rather than a data backfill.

Startup `AutoMigrate(&model.AgentAttachment{})` is removed. Startup only checks
that the table exists and reports an explicit-migration error if it does not.
The reviewed migration becomes the sole production schema writer for this
table.

## 5. Agent state compatibility

The exact final `chk_ar_state_reason` adds only:

- `state_reason = '' AND status = 'running'`, because current Go creation paths
  can persist the string zero value while a run is running;
- `zombie_cleanup_2026_05_28`, the exact historical marker on two already
  deleted Dev rows, only when `is_deleted = 1`.

All existing terminal values, `external_resume_ready`, and the exact
`LEFT(state_reason, 11) = 'ext_resume:'` prefix remain. Arbitrary
`zombie_cleanup_*`, active zombie rows, terminated empty rows, `extXresume:*`,
or unknown values remain rejected. The normalized full relational CHECK clause
SHA is recomputed and verified exactly.

## 6. Feishu proof foreign keys

Both SELECT-only preflight and the migration's first assertion block, before any
mutation, enforce the following when
`feishu_operation_proof_consumption` exists:

1. existing FK state must be either zero constraints or the exact two final
   restrictive constraints; a one-FK or wrong-FK state fails;
2. every `source_operation_id` and `consumer_operation_id` must reference an
   existing `feishu_operation.id`;
3. both child columns and the parent ID must have identical type, charset, and
   collation.

When the table exists with zero FKs, one atomic `ALTER TABLE` adds both:

- `fk_feishu_proof_source_operation`;
- `fk_feishu_proof_consumer_operation`.

When both already exist exactly, migration is a no-op. The final verify requires
both exact FKs.

Repeating the gates inside the migration closes the check/apply race and makes
direct execution fail closed. A test inserts an orphan after preflight but
before migration; migration must fail while the FK count remains zero.

## 7. Protected row evidence

Preflight emits dynamic projections that work whether the new tables/columns
are absent or present:

- `agent_attachment_complete_projection`, hashing old fields plus all five
  parsed-content fields per row;
- `feishu_proof_business_projection`, hashing every business column of every
  proof row.

Verify emits the same projections after apply. The isolated runner and Dev S6
compare them before migration, after both migration executions, and after a
full backend startup. This detects NULL-to-default normalization or any other
hidden change. The existing old-field attachment and broad protected-table
evidence remains.

## 8. Test design

The isolated MySQL 8 runner adds scenarios for:

- reordered Feishu columns passing the order-independent contract;
- exact Dev attachment legacy shape passing without value changes;
- empty and exact zombie Agent states surviving and enforcing the final CHECK;
- populated proof rows with zero FKs passing preflight, receiving both FKs, and
  preserving rows;
- one proof FK, orphan proof rows, wrong column types, a third attachment shape,
  arbitrary zombie state, and prefix-like external state failing.
- real MySQL 8 GORM reads of legacy NULL attachment fields;
- a static startup guard proving `AgentAttachment` is not passed to AutoMigrate;
- full Dev backend startup with before/after complete projections.

Existing exact, interrupted partial, malformed table/index/FK, double-apply,
protected-data, full Go, lint, and real Prod read-only checks remain.

## 9. Rollout

1. Implement and review in the follow-up worktree.
2. Run isolated MySQL 8 and full backend gates.
3. Re-run real Prod SELECT-only preflight.
4. Merge to `develop`.
5. Re-run Dev preflight against the already verified backup point.
6. Apply twice, verify twice, and compare all protected evidence.
7. Continue the original Dev backend deployment and product smoke.

Prod writes remain separately authorized.
