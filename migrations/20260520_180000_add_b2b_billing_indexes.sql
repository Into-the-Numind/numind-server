-- b2b-billing-rules-rewrite hotfix: add indexes for the new report queries.
--
-- The rewritten GetBillingReport in internal/numind/biz/b2b_billing/b2b_billing.go
-- introduces two new query shapes on `subscription` and one on `trial_grant`:
--
--   1. Rule A (first-month subscribers):
--        WHERE source = 'b2b_grant'
--          AND first_started_at >= ? AND first_started_at < ?
--          AND granter_user_id IS NOT NULL
--
--   2. Rule B (cross-month renewals):
--        WHERE source = 'b2b_grant'
--          AND first_started_at < ?
--          AND updated_at >= ? AND updated_at < ?
--          AND granter_user_id IS NOT NULL
--
--   3. Trial path:
--        WHERE source = 'b2b_grant'
--          AND granted_at >= ? AND granted_at < ?
--          AND granter_user_id IS NOT NULL
--
-- Without these indexes the queries full-scan the subscription / trial_grant
-- tables. With the indexes, prod (~50 active subs, dozens of trials per
-- month) hits index range scans only.
--
-- All three indexes use a composite (source, <date_col>) layout so the
-- equality filter on source short-circuits before the range scan.
--
-- Safe to apply online: CREATE INDEX is non-blocking in MySQL 8.0+ for
-- InnoDB (ALGORITHM=INPLACE default). Idempotent via IF NOT EXISTS.

-- subscription: Rule A scan key
CREATE INDEX IF NOT EXISTS `idx_sub_source_first_started_at`
    ON `subscription` (`source`, `first_started_at`);

-- subscription: Rule B scan key
CREATE INDEX IF NOT EXISTS `idx_sub_source_updated_at`
    ON `subscription` (`source`, `updated_at`);

-- trial_grant: trial path scan key
CREATE INDEX IF NOT EXISTS `idx_tg_source_granted_at`
    ON `trial_grant` (`source`, `granted_at`);
