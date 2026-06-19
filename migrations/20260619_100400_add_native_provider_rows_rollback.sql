-- Rollback for 20260619_100400_add_native_provider_rows.sql
-- Removes the native-adapter llm_provider rows (claude-native / gemini-native).
--
-- SAFETY: only run AFTER the activation has been reversed (no ai_service_route may
-- still point at a native provider — STEP 4(c) must be undone first), otherwise a
-- route would be left dangling. The pre-flight guard below aborts (division by
-- zero) if any route still references a native provider, forcing the operator to
-- repoint routes back to 'dmxapi' before deleting the rows.
--
-- IDEMPOTENT: deleting absent rows is a no-op; scoped strictly by name.
-- NOTE: CI does NOT auto-run migrations (CLAUDE.md §5.2); apply via SSH.

-- Pre-flight guard: expect ZERO routes on native providers. If any exist, 1/0 aborts.
--   (COUNT = 0 → 1/(0+1)=1 OK;  COUNT > 0 → 1/(0)  → ER_DIVISION_BY_ZERO → abort)
SELECT 1 / (
  CASE WHEN (SELECT COUNT(*) FROM ai_service_route r
              JOIN llm_provider p ON p.id = r.provider_id
              WHERE p.name IN ('claude-native','gemini-native')) = 0
       THEN 1 ELSE 0 END
) AS guard_no_route_points_at_native_else_div_by_zero;

DELETE FROM llm_provider
  WHERE name IN ('claude-native', 'gemini-native');
