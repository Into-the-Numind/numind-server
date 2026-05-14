-- credit_reservation_item add source_type/source_id (rollback)
-- Feature: credits-deduct-cycle-wiring (T1/10)
--
-- Warning: New-path reservations created post-migration have source_type set
-- and package_id NULL. Rolling back loses their refund routing info. Run only
-- when confirmed no in-flight new-path reservations.

ALTER TABLE credit_reservation_item
    DROP INDEX idx_cri_source,
    DROP COLUMN source_id,
    DROP COLUMN source_type,
    MODIFY COLUMN package_id BIGINT UNSIGNED NOT NULL;
