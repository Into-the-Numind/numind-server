-- Rollback: restore the seeded official example skill removed by
-- 20260729_143500_remove_official_example_skill.sql.

INSERT INTO skill (parent_user_id, owner_user_id, visibility, name, description, when_to_use,
                   allowed_tools, body_md, source_type, origin_type, version, is_active, created_by)
SELECT 0, 0, 'official', '官方示例技能', '平台内置的官方示例技能，所有用户可见',
       '当你需要一个官方示例参考时',
       JSON_ARRAY(), '# 官方示例技能\n\n这是平台内置的官方示例技能正文。', 'custom', 'official', 1, 1, 0
WHERE NOT EXISTS (
  SELECT 1 FROM (SELECT id FROM skill WHERE visibility = 'official' AND name = '官方示例技能' LIMIT 1) AS t
);
