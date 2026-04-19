-- Rollback: 移除 20260419_170000 新增的 9 行 pricing_rule seed
--   - 1 全局兜底 (llm_chat, '', '')
--   - 7 dmxapi 新增行
--   - 1 aihubmix 新增行

DELETE FROM pricing_rule
WHERE service_type = 'llm_chat' AND is_active = 1
  AND (
    (provider = '' AND model = '')
    OR (provider = 'dmxapi' AND model IN (
      'claude-sonnet-4-6',
      'claude-sonnet-4-6-thinking',
      'DeepSeek-V3.2',
      'DeepSeek-V3.2-Thinking',
      'gemini-3.1-pro-preview',
      'gemini-3.1-pro-preview-thinking',
      'gpt-5.4'
    ))
    OR (provider = 'aihubmix' AND model = 'claude-sonnet-4-6-think')
  );
