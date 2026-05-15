# numind-server — Dev Environment Setup Guide

> **Last updated**: 2026-05-16 (post T12 cleanup)
>
> This document describes how to set up a fresh local development environment after
> dropping and recreating the database. The 5 new credits tables are NOT created by
> AutoMigrate — they require independent migration SQL files.

---

## Why this document exists

`internal/numind/helper.go` runs GORM AutoMigrate on server startup. AutoMigrate
handles ~22 tables (users, sop_*, chatbot_*, knowledge_*, etc.) but does NOT include
the 5 credits-redesign tables introduced in 2026-04:

| Table | Reason NOT in AutoMigrate |
|-------|--------------------------|
| `subscription` | Created by `20260430_membership_credits_redesign.sql` |
| `trial_grant` | Created by `20260430_membership_credits_redesign.sql` |
| `credit_cycle` | Created by `20260430_membership_credits_redesign.sql` |
| `user_booster_balance` | Created by `20260430_membership_credits_redesign.sql` |
| `membership_event` | Created by `20260430_membership_credits_redesign.sql` |

If you drop the DB and only run `task dev`, these 5 tables won't exist. The server
will start but `GetBalance`, `Reserve`, `Reconcile`, and all B2B membership flows
will panic with `Table '...' doesn't exist`.

---

## Quick-start: Drop + Rebuild from Scratch

### 1. Start the server (AutoMigrate for ~22 base tables)

```bash
task dev
```

Watch the startup log. AutoMigrate creates tables like `user`, `sop_template`,
`sop_run`, `chatbot_config`, etc. Stop the server after it's healthy.

### 2. Apply core credits infrastructure migration

This creates the 5 SOT tables plus seeds the billing configuration:

```bash
# Adjust connection string to match your local config (config_local.yaml)
mysql -u root -p numind_dev < migrations/20260430_membership_credits_redesign.sql
```

The migration creates:
- `subscription`, `trial_grant`, `credit_cycle`, `user_booster_balance`, `membership_event`
- `credit_account` (reserve/reconcile tracking), `credit_reservation`, `credit_reservation_item`
- Also creates `credit_transaction` schema updates if needed

### 3. Apply cumulative T1-T12 cleanup migrations

Run these in order. Each is idempotent where possible:

```bash
# T1: Add source_type/source_id to credit_transaction (backfill from credit_package if it exists)
mysql -u root -p numind_dev < migrations/20260515_100000_add_credit_transaction_source_type.sql

# T8: Ledger calibration (trial_grant + credit_cycle calibration — safe on empty fresh DB)
mysql -u root -p numind_dev < migrations/20260515_120000_t8_ledger_calibration.sql

# T10: Drop usage_record.credits_deducted column
# (Skip if the column doesn't exist — the migration is safe to re-run)
mysql -u root -p numind_dev < migrations/20260515152736_t10_drop_usage_record_credits_deducted.sql

# T11: Archive credit_package (creates legacy_credit_package_archive_20260515, then DROPs credit_package)
# On a fresh dev DB with no credit_package data, this migration is a no-op for the INSERT step.
# credit_package DROP will fail with "Table doesn't exist" on fresh DB — this is OK, it's idempotent.
# Run anyway to ensure legacy_credit_package_archive_20260515 table exists (needed by b2b_billing).
mysql -u root -p numind_dev < migrations/20260515_200000_t11_archive_credit_package.sql

# T12: Add hard FK constraints + CHECK constraint
mysql -u root -p numind_dev < migrations/20260516_100000_t12_add_foreign_keys.sql
```

> **Note on T11 on fresh DB**: The `credit_package` table won't exist on a fresh dev DB (AutoMigrate
> no longer creates it, and we just DROPped it in T11). The `DROP TABLE IF EXISTS credit_package`
> in T11 is a no-op. The archive table `legacy_credit_package_archive_20260515` will be created
> empty. This is correct — it's a historical archive, not a live table.

### 4. Restart and verify

```bash
task dev
```

Check the logs for:
- No `Table '...' doesn't exist` errors
- AutoMigrate completing without panics
- Server responding to health check: `curl http://localhost:9091/healthz`

Verify the 5 SOT tables exist:

```sql
SHOW TABLES LIKE 'subscription';
SHOW TABLES LIKE 'trial_grant';
SHOW TABLES LIKE 'credit_cycle';
SHOW TABLES LIKE 'user_booster_balance';
SHOW TABLES LIKE 'membership_event';
```

Verify T12 FK constraints:

```sql
SELECT TABLE_NAME, CONSTRAINT_NAME, CONSTRAINT_TYPE
FROM information_schema.TABLE_CONSTRAINTS
WHERE CONSTRAINT_SCHEMA = DATABASE()
  AND CONSTRAINT_NAME IN ('fk_cycle_subscription', 'fk_item_reservation', 'chk_ct_source_type')
ORDER BY TABLE_NAME;
-- Expected: 3 rows
```

---

## Additional migrations (feature-specific)

Other migrations in `migrations/` are feature-specific and may be needed depending on
which features you're working on. Apply in timestamp order:

```bash
# Credit estimation coefficients (for Reserve R2 estimation)
mysql -u root -p numind_dev < migrations/20260419_100400_seed_credit_estimation_coefficient.sql

# Pricing rules (needed for billing cost calculation)
mysql -u root -p numind_dev < migrations/seed_pricing_rules.sql
mysql -u root -p numind_dev < migrations/20260419_170000_seed_pricing_global_fallback.sql
```

---

## Connection string format

The connection string is configured in `config_local.yaml` (not in Git). Example:

```yaml
mysql:
  dsn: "root:password@tcp(127.0.0.1:3306)/numind_dev?charset=utf8mb4&parseTime=True&loc=Local"
```

Match the database name and credentials to your local MySQL setup.

For Docker-based MySQL (same as dev server):
```bash
# Run migrations inside docker container
docker exec -i <mysql_container_id> mysql -u root -pNumind2025 'numind-dev' < migrations/<file>.sql
```

---

## Notes for clean dev environment after T11

- `credit_package` table no longer exists (DROPped in T11, archived in `legacy_credit_package_archive_20260515`)
- `credit_account.balance` column no longer exists (DROPped in T11)
- `usage_record.credits_deducted` column no longer exists (DROPped in T10)
- `GORM AutoMigrate` will NOT attempt to recreate these — they were removed from model structs in T11
- B2B billing reports read from `membership_event` (not credit_package). Historical months before 2026-04-20 cutover read from `legacy_credit_package_archive_20260515`

See also:
- `numind-server/CLAUDE.md §1` — credits system current state overview
- `docs/legacy_credit_package_archive_README.md` — archive table query guide
- `docs/superpowers/plans/2026-05-15-membership-credits-redesign-cleanup-plan.md` — T1-T12 plan
