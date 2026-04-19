-- Rollback: Reset all legacy_tier users back to credits
-- Note: This unconditionally reverts all legacy_tier markings. Only use if you need to re-run init.
UPDATE `user` SET billing_mode = 'credits' WHERE billing_mode = 'legacy_tier';
