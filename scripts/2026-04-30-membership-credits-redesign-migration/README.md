# Membership Credits Redesign Migration (2026-04-30)

One-time migration: populates 5 new membership tables
(`subscription`, `trial_grant`, `credit_cycle`, `user_booster_balance`, `membership_event`)
from existing `credit_package` rows.

## Scope

| New table | Source | Logic |
|---|---|---|
| `subscription` | `credit_package WHERE type='subscription'` | Consecutive packages merged into one row per user (gap <= 2 days). `total_months_purchased = CEIL(days/30)`. |
| `trial_grant` | `credit_package WHERE type='trial'` | Earliest trial package per user. `credits_remaining` = `remain_credits`. |
| `user_booster_balance` | `credit_package WHERE type='booster'` | SUM of active booster `remain_credits` per user. |
| `membership_event` | All `credit_package` rows | One event per package. `idempotency_key = 'migration-20260430-cp-{id}'`. |
| `credit_cycle` | n/a | Empty at migration time — application fills on next billing cycle. |

## Execution Order (on prod)

```bash
# 1. Dry-run — read-only, check blockers (all BLOCKER_Fxx violation_count must be 0)
docker exec -i numind-mysql-prod mysql -uroot -pNumind2025 numind-prod < 01-dry-run.sql

# 2. Apply — single transaction, creates backup table first
docker exec -i numind-mysql-prod mysql -uroot -pNumind2025 numind-prod < 02-apply.sql

# 3. Verify — 8 invariants (I1-I8), all violation_count must be 0
docker exec -i numind-mysql-prod mysql -uroot -pNumind2025 numind-prod < 03-verify.sql

# 4. (Emergency only, T+24h window) Rollback
docker exec -i numind-mysql-prod mysql -uroot -pNumind2025 numind-prod < 04-rollback.sql
```

## Time Budget

| Step | Estimated time |
|---|---|
| 01-dry-run | < 30 seconds |
| 02-apply | 1–3 minutes (depends on credit_package row count) |
| 03-verify | < 30 seconds |
| 04-rollback (if needed) | 1–2 minutes |

## Circuit Breaker Conditions

Abort and run `04-rollback.sql` if any of the following are true after apply:

1. `03-verify.sql` shows any `violation_count > 0`
2. Application error rate spikes > 1% within 5 minutes of deployment
3. Any membership_event with `event_type = 'booster_purchased'` appears (wrong enum value — indicates code bug, not migration bug)
4. `user_booster_balance` rows count does not match expected booster user count from dry-run

## Design Notes

- **Backup table**: `migration_20260430_credit_pkg_backup` created outside the transaction; survives tx abort. Contains full snapshot of all `credit_package` rows at migration time.
- **apply_log**: `migration_20260430_apply_log` records per-step inserted row counts; rollback uses it to calculate the cutover timestamp window for targeted DELETEs.
- **Idempotency**: Each `membership_event` row carries `idempotency_key = 'migration-20260430-cp-{credit_package.id}'` — rollback can DELETE with a LIKE filter even if apply_log is lost.
- **Rollback window**: 24 hours. After T+24h, application may have created new subscription/trial/booster rows; rollback becomes destructive without DBA review.
- **credit_cycle**: Not populated by this migration. The application creates credit_cycle rows at subscription renewal time.
- **event_type enum**: `booster_granted` (not `booster_purchased`) — matches `membership/constants.go EventTypeBoosterGranted`.
