# legacy_credit_package_archive_20260515

## What this table is

`legacy_credit_package_archive_20260515` is a read-only historical archive of the
`credit_package` table that was created and populated on 2026-05-15 (T11 migration)
before the original `credit_package` table was dropped.

## Why it exists

The credits-system redesign (Q1 B2B2C project) introduced three authoritative pools
to replace `credit_package` as the live source of truth:

| Pool | Table | Covers |
|------|-------|--------|
| Subscription credits | `credit_cycle` | Monthly allotment |
| Trial credits | `trial_grant` | 200-credit trial grants |
| Booster credits | `user_booster_balance` | Paid add-on packs |

After the three-pool system was fully live (T6 cutover), `credit_package` became a
stale denormalized cache with no writers. T11 archived it and dropped the original table
to eliminate schema confusion and prevent accidental reads from a dead table.

## Schema

The archive table preserves all original columns plus two audit columns:

```sql
CREATE TABLE legacy_credit_package_archive_20260515 (
    -- Original credit_package columns (preserved verbatim)
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    user_id         INT UNSIGNED NOT NULL,
    type            ENUM('trial','subscription','booster') NOT NULL,
    total_credits   INT NOT NULL DEFAULT 0,
    remain_credits  INT NOT NULL DEFAULT 0,
    status          ENUM('pending','active','exhausted','expired') NOT NULL DEFAULT 'active',
    grant_source    ENUM('self_purchase','b2b_grant') DEFAULT NULL,
    granter_user_id INT UNSIGNED DEFAULT NULL,
    activated_at    DATETIME DEFAULT NULL,
    expires_at      DATETIME DEFAULT NULL,
    created_at      DATETIME NOT NULL,
    updated_at      DATETIME NOT NULL,

    -- Archive audit columns
    archived_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT 'when this row was archived',
    archive_reason  VARCHAR(100) NOT NULL DEFAULT 't11_drop_credit_package_20260515'
                    COMMENT 'migration step that created this archive'
);
```

## Row counts (at archive time)

The migration script verifies archive completeness with:

```sql
SELECT
    (SELECT COUNT(*) FROM credit_package) AS source_count,
    (SELECT COUNT(*) FROM legacy_credit_package_archive_20260515) AS archive_count;
```

Both counts must match before `DROP TABLE credit_package` executes.

## Who reads this table

### B2B billing: getLegacyEvents

`internal/numind/biz/b2b_billing/b2b_billing.go::getLegacyEvents` queries this table
for historical B2B grant events that predate the T6 membership_event cutover
(2026-04-20). In practice, prod had **0 B2B business before cutover**, so this function
returns empty for all pre-cutover months. It is preserved for:

- Direct audit queries via tooling
- Historical month verification scripts
- Future reactivation if cutover_split billing mode is needed

`getLegacyEvents` is **not called by `GetBillingReport`** in normal operation.
`GetBillingReport` always uses `getNewEvents` (reads `membership_event` with
`source='b2b_grant'`) per the T9 simplification.

### Rollback path

If the T11 migration needs to be rolled back (see
`migrations/20260515_200000_t11_archive_credit_package_rollback.sql`), rows are
re-inserted from this archive table into a recreated `credit_package` table. Note that
`credit_account.balance` values cannot be recovered from this table — they would need
to be restored from a pre-migration `mysqldump`.

## Access policy

- **Production**: READ-ONLY. No application code writes to this table post-archive.
- **Admin tooling**: May read for audit/reconciliation.
- **Rollback script**: Reads all rows to restore `credit_package`.

Do NOT drop this table without explicit authorization — it is the only complete record
of historical credit package state before the three-pool redesign.
