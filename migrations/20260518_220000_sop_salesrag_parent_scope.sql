-- +migrate Up
-- sop-salesrag-parent-scope: 多租户 Layer 0 父账户归属隔离
-- 见 docs/superpowers/specs/2026-05-18-sop-salesrag-parent-scope-design.md
--
-- 操作:
--   1. CREATE TABLE sales_agent_owner (IF NOT EXISTS 幂等)
--   2. INSERT user 30 (INSERT IGNORE 幂等)
--   3. UPDATE sop_template SET creator_user_id=30 WHERE id IN (1, 2)
--
-- 注: 本仓库 migration 由人工 SSH 跑 (CI 不跑, 见 memory dev_deploy_migration_gap)

CREATE TABLE IF NOT EXISTS sales_agent_owner (
  parent_user_id INT UNSIGNED NOT NULL PRIMARY KEY,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  CONSTRAINT fk_sao_parent FOREIGN KEY (parent_user_id) REFERENCES user(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
  COMMENT='销售智能体父账户归属表（owner tag）';

INSERT IGNORE INTO sales_agent_owner (parent_user_id, created_at)
  VALUES (30, NOW(3));

UPDATE sop_template SET creator_user_id = 30 WHERE id IN (1, 2);

-- 部署后人工 verify:
--   SELECT COUNT(*) FROM sales_agent_owner WHERE parent_user_id=30;  -- 应为 1
--   SELECT id, creator_user_id FROM sop_template WHERE id IN (1,2,3,4);  -- 应均为 30
