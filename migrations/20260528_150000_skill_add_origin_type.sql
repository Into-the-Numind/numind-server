-- agent-mode-v2-skill-as-artifact: skill.origin_type 字段补 migration
-- commit e1e1450 (2026-05-27 feat(skill): add origin_type tags and official preset import API)
-- 加了 model.Skill.OriginType 但未提交 migration，本文件补齐。
-- 字段位置：source_template_id 之后、version 之前（与 GORM struct 顺序一致）。
-- ENUM: official=官方预置（管理端导入），tenant=父账户私有，user=子账户私有（默认）。

ALTER TABLE skill
  ADD COLUMN IF NOT EXISTS origin_type ENUM('official','tenant','user') NOT NULL DEFAULT 'user'
  COMMENT '资源归属类型：official=官方预置，tenant=父账户私有，user=子账户私有'
  AFTER source_template_id;
