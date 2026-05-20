-- agent-mode-billing-integration #12/14
-- 2026-05-21
-- Extend chk_ct_source_type CHECK constraint to include 'admin_test' for Agent 试聊配额池

-- agent-mode-billing-integration #12: extend chk_ct_source_type to include 'admin_test'

-- Pre-check: ensure no rows would violate new CHECK (expected: 0 invalid rows)
SELECT
  'pre_check_no_invalid_source_type' AS check_name,
  COUNT(*) AS invalid_rows
FROM credit_transaction
WHERE source_type NOT IN ('trial', 'subscription', 'cycle', 'booster', 'admin', 'system', 'admin_test')
  AND source_type IS NOT NULL;
-- Expected: 0

-- ALTER: drop and re-add with new 7-enum CHECK
ALTER TABLE credit_transaction DROP CONSTRAINT chk_ct_source_type;
ALTER TABLE credit_transaction
  ADD CONSTRAINT chk_ct_source_type
  CHECK (source_type IN ('trial', 'subscription', 'cycle', 'booster', 'admin', 'system', 'admin_test')
         OR source_type IS NULL);

-- Post-check: confirm constraint exists
SELECT
  CONSTRAINT_NAME,
  CONSTRAINT_TYPE,
  TABLE_NAME
FROM information_schema.TABLE_CONSTRAINTS
WHERE TABLE_SCHEMA = DATABASE()
  AND TABLE_NAME = 'credit_transaction'
  AND CONSTRAINT_NAME = 'chk_ct_source_type';
