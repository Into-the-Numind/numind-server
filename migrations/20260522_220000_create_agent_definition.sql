-- Migration: create agent_definition table
-- Feature: agent-mode-skill-system (#5/14)
-- Rollback: 20260522_220000_create_agent_definition_rollback.sql

CREATE TABLE IF NOT EXISTS agent_definition (
  id                       BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  parent_user_id           INT UNSIGNED NOT NULL                  COMMENT 'FK 到 user.id；agent 必属于父账户',
  name                     VARCHAR(50) NOT NULL                   COMMENT 'Q1: Agent 名字，2-20 字',
  description              VARCHAR(150) NULL                      COMMENT 'Q3: 一句话描述，10-100 字',
  icon_url                 VARCHAR(512) NULL                      COMMENT 'Q2: 头像 URL',
  welcome_message          TEXT NULL                              COMMENT 'Q4: 欢迎语，20-500 字',
  starters                 JSON NULL                              COMMENT 'Q5: conversation starters []string，≤4 条',
  questionnaire_answers    JSON NULL                              COMMENT '完整问卷答案 q1-q12 快照',
  generated_skill_body     TEXT NULL                              COMMENT 'skill_builder 组装的 SKILL.md',
  advanced_mode            TINYINT(1) NOT NULL DEFAULT 0          COMMENT '0=问卷模式 1=高级模式（不可逆）',
  custom_skill_body        TEXT NULL                              COMMENT '高级模式自定义 prompt；advanced_mode=1 时生效',
  tool_flags               JSON NULL                              COMMENT '{"code_sandbox":true, ...} map[string]bool',
  credit_cap_per_session   INT UNSIGNED NULL                      COMMENT 'Q8: 每次任务积分上限 200-2000，NULL=不限',
  daily_credit_cap         INT UNSIGNED NULL                      COMMENT '每日累计积分上限，NULL=不限',
  version                  INT UNSIGNED NOT NULL DEFAULT 1        COMMENT '当前版本号；每次更新+1',
  is_active                TINYINT(1) NOT NULL DEFAULT 1          COMMENT '软删除：0=已下架',
  source_template_id       BIGINT UNSIGNED NULL                   COMMENT '软引用 skill_template.id；无 FK',
  created_by               INT UNSIGNED NOT NULL                  COMMENT 'JWT.userID 创建者；同 parent_user_id 但保留供审计',
  created_at               DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at               DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_ad_parent_active (parent_user_id, is_active),
  KEY idx_ad_template (source_template_id)
);
