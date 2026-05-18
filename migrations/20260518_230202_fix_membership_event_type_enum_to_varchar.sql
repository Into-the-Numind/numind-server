-- Audit post-deploy fix: membership_event ENUM-vs-VARCHAR schema drift
-- exposed by P0-2 ReservationSweeper.
--
-- Issue 1 — event_type:
--   DB was ENUM('trial_granted','sub_granted','sub_renewed','booster_granted',
--   'admin_calibration'). Go model declares VARCHAR(30). RefundCreditsTx
--   fallback writes 'refund_lost' which the ENUM rejected with Error 1265.
--
-- Issue 2 — product_type:
--   DB was ENUM('trial','monthly','booster'). cycle.go refund_lost writes
--   `ProductType: string(source)` where source is the DeductSource enum
--   ('trial','cycle','booster'). 'cycle' is rejected.
--
-- Both ENUMs were inherited from original membership-credits-redesign
-- migration (2026-04-30). Go model already declares VARCHAR; align DB.
ALTER TABLE `membership_event`
  MODIFY COLUMN `event_type` VARCHAR(30) NOT NULL,
  MODIFY COLUMN `product_type` VARCHAR(20) NOT NULL;
