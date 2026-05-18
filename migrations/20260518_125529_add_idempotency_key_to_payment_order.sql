-- P2#10 audit fix: persist Idempotency-Key on payment_order to dedup double-submit.
-- Feature: credits-audit (P2#10)
-- Date: 2026-05-18
--
-- Why: POST /v1/orders enforces the Idempotency-Key header via the
-- RequireIdempotencyKey middleware, but the key is never persisted with the
-- order. A flaky-network retry with the same Idempotency-Key creates a SECOND
-- payment_order row with a NEW order_no. The user pays at most one, so the
-- duplicate becomes a stranded pending order that lingers until CloseExpiredOrders
-- sweeps it 30 minutes later. CreateOrder now pre-checks by key and recovers
-- from concurrent races via the UNIQUE constraint.
--
-- Migration safety:
--  * Column is nullable: historical rows pre-dating this fix remain valid
--    (idempotency_key IS NULL). MySQL treats multiple NULLs as distinct under
--    a UNIQUE index, so the constraint does not collide with backfill.
--  * Additive — no existing data is rewritten. Rollback is trivial.
--  * VARCHAR(64) matches the middleware's maxIdempotencyKeyLen.

ALTER TABLE `payment_order`
    ADD COLUMN `idempotency_key` VARCHAR(64) DEFAULT NULL COMMENT 'Idempotency-Key header from POST /v1/orders; deduplicates double-submit',
    ADD UNIQUE KEY `uniq_order_idempotency_key` (`idempotency_key`);
