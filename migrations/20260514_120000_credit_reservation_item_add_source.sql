-- credit_reservation_item add source_type/source_id (forward)
-- Feature: credits-deduct-cycle-wiring (T1/10)
-- Date: 2026-05-14
--
-- Why: Old path stores per-FIFO-credit-package allocations via package_id FK.
-- New path (credits-mode users) needs to record per-pool allocations across
-- credit_cycle / user_booster_balance / trial_grant. Adding source_type +
-- source_id (nullable, additive) lets Reconcile route refunds to the correct
-- new-schema table while preserving the existing FK for legacy reservations.
--
-- Migration safety:
--  * source_type / source_id are nullable additive columns. Existing rows are
--    untouched (NULL = legacy reservation, dispatched via package_id).
--  * package_id is left as NOT NULL for now; T8 inserts will need to pass a
--    dummy zero value if package_id stays NOT NULL — but the cleaner option
--    is to relax NOT NULL. Choose the latter for forward simplicity:
--    legacy rows already have non-NULL package_id; new rows leave it NULL.

ALTER TABLE credit_reservation_item
    MODIFY COLUMN package_id BIGINT UNSIGNED NULL COMMENT 'legacy credit_package FK; NULL for new-path rows',
    ADD COLUMN source_type VARCHAR(20) NULL COMMENT 'cycle/booster/trial; NULL = legacy old-path' AFTER package_id,
    ADD COLUMN source_id BIGINT UNSIGNED NULL COMMENT 'FK to credit_cycle.id / user_booster_balance.user_id / trial_grant.id depending on source_type' AFTER source_type,
    ADD INDEX idx_cri_source (source_type, source_id);
