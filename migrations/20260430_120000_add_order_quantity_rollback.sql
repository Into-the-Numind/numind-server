-- Rollback: Remove quantity column from payment_order table
-- Pair: 20260430_120000_add_order_quantity.sql
--
-- Note: The months column was never modified by the forward migration,
-- so no months-related restoration is needed. Booster orders will fall back
-- to reading Order.Months for quantity (pre-migration behavior).

ALTER TABLE `payment_order` DROP COLUMN `quantity`;
