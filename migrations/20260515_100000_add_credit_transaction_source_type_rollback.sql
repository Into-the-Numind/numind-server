-- credit_transaction add source_type/source_id (rollback)
-- Feature: membership-credits-redesign cleanup T1
--
-- Warning: Rolling back loses all source_type/source_id data that has been
-- written by new code paths since the forward migration ran. The columns are
-- simply dropped — data cannot be recovered without re-running the forward
-- migration's backfill UPDATE. Run rollback only when confirmed no meaningful
-- new writes have occurred (e.g. immediately after a failed dev test).

ALTER TABLE credit_transaction
    DROP INDEX idx_ct_source,
    DROP COLUMN source_id,
    DROP COLUMN source_type;
