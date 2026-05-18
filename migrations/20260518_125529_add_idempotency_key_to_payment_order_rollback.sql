-- P2#10 audit fix: persist Idempotency-Key on payment_order (rollback).
-- Feature: credits-audit (P2#10)
--
-- Warning: Post-migration order rows have idempotency_key set. Rolling back
-- loses this dedup signal — clients retrying with the same key after rollback
-- will once again create duplicate stranded orders. Run only when no in-flight
-- order traffic depends on the column.

ALTER TABLE `payment_order`
    DROP INDEX `uniq_order_idempotency_key`,
    DROP COLUMN `idempotency_key`;
