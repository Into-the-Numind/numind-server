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
named columns and is not a product contract. Missing, extra, or differently
typed columns still change the fingerprint and fail.

## 4. Attachment compatibility

The five parsed-content columns may be in exactly one of two complete shapes:

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

The migration does not `MODIFY` these columns and does not backfill rows. A
mixed or third shape fails. Prod currently has zero attachment rows and missing
columns, so it still receives the final shape.

## 5. Agent state compatibility

The exact final `chk_ar_state_reason` adds only:

- `''`, because current Go creation paths can persist the string zero value
  while a run is initially/routinely `running`;
- `zombie_cleanup_2026_05_28`, the exact historical marker on two already
  deleted Dev rows.

All existing terminal values, `external_resume_ready`, and the exact
`LEFT(state_reason, 11) = 'ext_resume:'` prefix remain. Arbitrary
`zombie_cleanup_*`, `extXresume:*`, or unknown values remain rejected. The
normalized full CHECK clause SHA is recomputed and verified exactly.

## 6. Feishu proof foreign keys

Before apply, when `feishu_operation_proof_consumption` exists:

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

## 7. Test design

The isolated MySQL 8 runner adds scenarios for:

- reordered Feishu columns passing the order-independent contract;
- exact Dev attachment legacy shape passing without value changes;
- empty and exact zombie Agent states surviving and enforcing the final CHECK;
- populated proof rows with zero FKs passing preflight, receiving both FKs, and
  preserving rows;
- one proof FK, orphan proof rows, wrong column types, a third attachment shape,
  arbitrary zombie state, and prefix-like external state failing.

Existing exact, interrupted partial, malformed table/index/FK, double-apply,
protected-data, full Go, lint, and real Prod read-only checks remain.

## 8. Rollout

1. Implement and review in the follow-up worktree.
2. Run isolated MySQL 8 and full backend gates.
3. Re-run real Prod SELECT-only preflight.
4. Merge to `develop`.
5. Re-run Dev preflight against the already verified backup point.
6. Apply twice, verify twice, and compare all protected evidence.
7. Continue the original Dev backend deployment and product smoke.

Prod writes remain separately authorized.
