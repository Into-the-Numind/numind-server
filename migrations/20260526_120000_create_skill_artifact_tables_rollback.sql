-- agent-mode-v2-skill-as-artifact T01 rollback
-- 删 3 张新表 + 恢复 agent_definition 字段 comment

DROP TABLE IF EXISTS agent_skill_binding;
DROP TABLE IF EXISTS skill_history;
DROP TABLE IF EXISTS skill;

-- 恢复 agent_definition 字段 comment（去掉 deprecated 标记）
ALTER TABLE agent_definition
  MODIFY COLUMN generated_skill_body TEXT NOT NULL COMMENT 'v1 嵌入式 skill body（agent-mode-skill-system #5）',
  MODIFY COLUMN custom_skill_body TEXT NOT NULL COMMENT 'v1 高级模式 skill body',
  MODIFY COLUMN tool_flags JSON NOT NULL COMMENT 'v1 Agent 级工具白名单';
