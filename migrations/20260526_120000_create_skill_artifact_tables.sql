-- agent-mode-v2-skill-as-artifact T01 forward migration
-- 创建 3 张新表：skill / skill_history / agent_skill_binding
-- 数据迁移见独立 CLI：numind migrate-skill-from-agent（T06）
-- 本 SQL 仅 DDL（建表 + 索引），可重入（IF NOT EXISTS）

CREATE TABLE IF NOT EXISTS skill (
  id                  INT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
  parent_user_id      INT UNSIGNED NOT NULL COMMENT '所属父账户 ID（租户隔离）',
  name                VARCHAR(100) NOT NULL COMMENT 'Skill 名称（同租户可重名，前端提示）',
  description         VARCHAR(300) NOT NULL DEFAULT '' COMMENT '简短描述（卡片/列表展示）',
  when_to_use         VARCHAR(500) NOT NULL DEFAULT '' COMMENT '何时使用（v2 #2 runtime 注入 system prompt）',
  allowed_tools       JSON NOT NULL DEFAULT (JSON_ARRAY()) COMMENT '允许的工具白名单 []string',
  body_md             MEDIUMTEXT NOT NULL COMMENT 'Skill 主体 Markdown 内容',
  source_type         ENUM('generated','custom','imported_from_template','imported_from_marketplace') NOT NULL DEFAULT 'custom' COMMENT '来源类型',
  source_template_id  INT UNSIGNED NULL COMMENT '若 source_type=imported_from_template，引用 skill_template.id',
  version             INT UNSIGNED NOT NULL DEFAULT 1 COMMENT '当前版本号，每次编辑 +1',
  is_active           TINYINT(1) NOT NULL DEFAULT 1 COMMENT '软删标记',
  created_by          INT UNSIGNED NOT NULL COMMENT '创建者 user_id',
  created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  KEY idx_skill_parent_active (parent_user_id, is_active, updated_at DESC),
  KEY idx_skill_source_template (source_template_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'agent-mode-v2-skill-as-artifact: 独立 Skill 资产表';

CREATE TABLE IF NOT EXISTS skill_history (
  id          INT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
  skill_id    INT UNSIGNED NOT NULL COMMENT '关联 skill.id',
  version     INT UNSIGNED NOT NULL COMMENT '版本号快照',
  snapshot    JSON NOT NULL COMMENT '完整 skill 行快照',
  created_by  INT UNSIGNED NOT NULL,
  created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY uk_skill_version (skill_id, version),
  KEY idx_history_skill_created (skill_id, created_at DESC)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'agent-mode-v2-skill-as-artifact: Skill 版本快照';

CREATE TABLE IF NOT EXISTS agent_skill_binding (
  id          INT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
  agent_id    INT UNSIGNED NOT NULL COMMENT '关联 agent_definition.id',
  skill_id    INT UNSIGNED NOT NULL COMMENT '关联 skill.id',
  sort_order  SMALLINT NOT NULL DEFAULT 0 COMMENT '装载顺序（拖拽）',
  is_active   TINYINT(1) NOT NULL DEFAULT 1 COMMENT '软删（卸载时置 0）',
  bound_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  unbound_at  DATETIME NULL COMMENT 'is_active=0 时填',
  UNIQUE KEY uk_agent_skill (agent_id, skill_id) COMMENT '同 agent 不能重复装载同一 skill（复装改 is_active=1）',
  KEY idx_binding_agent_active_sort (agent_id, is_active, sort_order),
  KEY idx_binding_skill_active (skill_id, is_active)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'agent-mode-v2-skill-as-artifact: Agent-Skill 装载关系';

-- v1 agent_definition 字段标 deprecated（不改类型，仅改 column comment）
-- runtime 仍读这些字段（本期不变），v2 #2 接管后切换到 binding+skill 表
ALTER TABLE agent_definition
  MODIFY COLUMN generated_skill_body TEXT NOT NULL COMMENT '【v2 已废弃】v1 嵌入式 skill body；v2 #2 接管 runtime 后改读 skill 表，v2 #1 期间双读保留',
  MODIFY COLUMN custom_skill_body TEXT NOT NULL COMMENT '【v2 已废弃】v1 高级模式 skill body；同上',
  MODIFY COLUMN tool_flags JSON NOT NULL COMMENT '【v2 已废弃】v1 Agent 级工具白名单；v2 #2 后改用 skill.allowed_tools 合并';
