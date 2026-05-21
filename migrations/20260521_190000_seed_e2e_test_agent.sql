-- Agent Mode #14 Phase B — seed E2E test agent for Playwright integration tests.
-- DEV ONLY: prod migration runs MUST skip this file.
-- The fixture-shared id 99999 is referenced by:
--   - numind-admin-web/e2e/fixtures/test-agent-id.json
--   - numind-web-v3/e2e/fixtures/test-agent-id.json
-- M-B2 ~ M-B8 specs read this id when targeting the seeded agent.

INSERT INTO agent_definition (
    id,
    parent_user_id,
    name,
    description,
    welcome_message,
    starters,
    generated_skill_body,
    questionnaire_answers,
    version,
    advanced_mode,
    is_active,
    credit_cap_per_session,
    daily_credit_cap,
    created_at,
    updated_at
) VALUES (
    99999,
    1,
    'E2E Test Assistant',
    '#14 Phase B 自动 e2e 测试专用 — 切勿在生产环境出现',
    '你好，我是 E2E 测试助手。',
    '["帮我搜索一个话题", "帮我分析数据", "帮我写一段文案"]',
    '## 任务\n回答用户问题。\n## 风格\n友好简洁。',
    '{"q6":["answer_questions"],"q7":["text"],"q8":800,"q9":"allow_search","q10":"","q11":"这个问题超出我的能力范围，请换个方式描述。","q12":"friendly"}',
    1,
    0,
    1,
    100,
    1000,
    NOW(),
    NOW()
) ON DUPLICATE KEY UPDATE updated_at = VALUES(updated_at);
