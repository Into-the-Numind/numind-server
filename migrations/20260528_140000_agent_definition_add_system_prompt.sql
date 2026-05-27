-- agent_prompt_simplification S2: 新增 system_prompt 字段
-- 老 agent 默认空字符串，运行时 fallback 到 generated_skill_body / custom_skill_body
-- DB 列类型 MEDIUMTEXT（16MB 兜底），后端 biz 层校验 64KB 软上限

ALTER TABLE agent_definition
  ADD COLUMN system_prompt MEDIUMTEXT NOT NULL DEFAULT ''
  AFTER custom_skill_body;
