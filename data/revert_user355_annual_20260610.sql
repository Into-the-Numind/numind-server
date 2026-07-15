-- ============================================================
-- REVERT script for user 355 annual conversion
-- Created: 2026-06-10 (prod billing correction)
-- Purpose: undo the change that cancelled the 05-28 renewal and
--          converted the 05-21 monthly grant into a 12-month annual.
-- DB: numind-prod (container numind-mysql-prod)
-- ============================================================

-- 1. Re-insert the deleted renewal event (membership_event id=239)
INSERT INTO membership_event
  (id, user_id, event_type, product_type, months, quantity, amount_cents,
   source, granter_user_id, idempotency_key, subscription_id, occurred_at)
VALUES
  (239, 355, 'sub_renewed', 'monthly', 1, NULL, 9900,
   'b2b_grant', 30, '02805ee5-3482-4f40-93cc-21212fabf476', 17, '2026-05-28 14:40:37');

-- 2. Revert the grant event (membership_event id=218) back to monthly ¥99
UPDATE membership_event
   SET months = 1, amount_cents = 9900
 WHERE id = 218;

-- 3. Revert subscription id=17 back to 2 months / 2026-07-21 expiry
UPDATE subscription
   SET total_months_purchased = 2,
       expires_at  = '2026-07-21 13:17:46',
       updated_at  = '2026-05-28 14:40:37'
 WHERE id = 17;

-- credit_cycle 644 was never modified, so nothing to revert there.
