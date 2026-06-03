-- Rollback for agent-mode-billing T10 M1 image_gen pricing rule.
DELETE FROM pricing_rule
WHERE service_type='image_gen' AND provider='dmxapi' AND model='gemini-2.5-flash-image';
