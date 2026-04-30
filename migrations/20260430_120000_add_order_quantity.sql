-- Migration: Add quantity column to payment_order table for booster orders
-- Date: 2026-04-30
-- Author: Agent K (tech debt fix from membership-credits-redesign task 11)
--
-- Background: task 11 reused Order.Months to store booster "quantity" (number of
-- booster packs purchased). This migration adds a dedicated Quantity column to
-- cleanly separate the semantics:
--   - Months: meaningful only for monthly subscription orders (number of months)
--   - Quantity: meaningful only for booster orders (number of 600-credit packs)
--
-- The months column is NOT dropped — it remains for monthly subscription orders
-- and for backward compatibility with already-deployed query code.

-- Forward migration
ALTER TABLE `payment_order`
  ADD COLUMN `quantity` INT NOT NULL DEFAULT 1
  COMMENT 'Booster 订单的购买份数（每份 600 积分）。月订阅/trial 订单此字段无意义，保持默认 1。';

-- Backfill: copy months -> quantity for existing booster orders
-- months > 0 guard avoids clobbering any edge-case 0-value rows with 1 (default)
UPDATE `payment_order`
SET `quantity` = `months`
WHERE `product_type` = 'booster' AND `months` > 1;

-- Note: booster orders with months=0 or months=1 remain at quantity=1 (default),
-- which is correct (0 is invalid and 1 is the correct quantity for those orders).
