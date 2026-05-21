-- Agent Mode #14 Phase B/M-B0c — seed compliance_rule for M-B5 compliance block test.
-- DEV ONLY: prod must skip.
-- M-B5 (student-compliance-block.spec.ts) sends a message containing "竞品X";
-- the L1 forbid_phrase rule below makes compliance.gate.CheckUserInput deny,
-- and the agent returns the configured Q11 越界话术 to the learner.

INSERT INTO compliance_rule (
    parent_user_id,
    rule_type,
    pattern,
    is_active,
    created_at,
    updated_at
) VALUES (
    1,
    'forbid_phrase',
    '竞品X',
    1,
    NOW(),
    NOW()
) ON DUPLICATE KEY UPDATE updated_at = VALUES(updated_at);
