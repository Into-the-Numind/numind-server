-- Migration: create skill_template table
-- Feature: agent-mode-skill-system (#5/14)
-- Rollback: 20260522_220200_create_skill_template_rollback.sql
-- Note: seed data (10 rows) written by M7 in 20260522_220300_seed_skill_template.sql

CREATE TABLE IF NOT EXISTS skill_template (
  id                       BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  name                     VARCHAR(50) NOT NULL,
  description              VARCHAR(300) NULL,
  icon_url                 VARCHAR(512) NULL,
  category_tags            JSON NULL                              COMMENT '["小红书运营","数据分析"]',
  questionnaire_answers    JSON NOT NULL                          COMMENT '完整 12 题预填',
  default_tool_flags       JSON NULL,
  display_order            INT NOT NULL DEFAULT 100,
  is_active                TINYINT(1) NOT NULL DEFAULT 1,
  created_at               DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at               DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_st_active_order (is_active, display_order)
);
