-- agent-mode-billing-integration #12/14
-- 2026-05-21
-- Rollback: revert chk_ct_source_type to original 6-enum (remove 'admin_test')

-- Pre-check: ensure no admin_test rows exist (else rollback would violate CHECK)
SELECT
  'rollback_check_no_admin_test_rows' AS check_name,
  COUNT(*) AS orphan_rows
FROM credit_transaction
WHERE source_type = 'admin_test';
-- Expected: 0 (if non-zero, this rollback will leave orphaned rows violating the restored CHECK)

-- ALTER: drop and re-add with original 6-enum CHECK (no admin_test)
ALTER TABLE credit_transaction DROP CONSTRAINT chk_ct_source_type;
ALTER TABLE credit_transaction
  ADD CONSTRAINT chk_ct_source_type
  CHECK (source_type IN ('trial', 'subscription', 'cycle', 'booster', 'admin', 'system')
         OR source_type IS NULL);
