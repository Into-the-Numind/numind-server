-- T4 skill-3tier-visibility: 技能三级可见性 + 市场引用重设计
--
-- 新增 skill 列：
--   owner_user_id   真正创建者 user id（backfill = created_by）
--   visibility      三级可见性 ENUM('official','institution','sub_user')
--                   backfill: origin_type='official' → 'official'，其余 → 'institution'
--   subscription_id 市场引用指针：非零 ⇒ 本行是订阅引用（body 非权威）
--   marketplace_id  市场引用指针：非零 ⇒ 运行时改读 marketplace 当前快照
--
-- 新增 skill_subscription 列（FORWARD-ONLY，cloned_skill_id 保留不动）：
--   source_skill_id     发布方原始 skill id（reference-mode >0，legacy clone-mode =0）
--   subscribed_version  订阅时刻 source skill 版本（用于"原版已更新/已删除"提示）
--
-- 索引：
--   idx_skill_visibility ON skill(parent_user_id, visibility, is_active)
--   idx_skill_owner      ON skill(owner_user_id)
--   idx_subscription_source ON skill_subscription(source_skill_id)
--
-- ⚠️ dev/prod 部署不自动跑 migration（MEMORY: project_dev_deploy_migration_gap），须手工 SSH 执行。
-- 幂等：列用 ADD COLUMN IF NOT EXISTS；索引用 INFORMATION_SCHEMA 守卫 + 动态 SQL（MySQL 8 无 CREATE INDEX IF NOT EXISTS）。

-- ---- skill 列 ----
ALTER TABLE skill
  ADD COLUMN IF NOT EXISTS owner_user_id INT UNSIGNED NOT NULL DEFAULT 0
  COMMENT '真正创建者 user id（父建=父 id，子建=子 id）'
  AFTER parent_user_id;

ALTER TABLE skill
  ADD COLUMN IF NOT EXISTS visibility ENUM('official','institution','sub_user') NOT NULL DEFAULT 'institution'
  COMMENT '三级可见性：official=全局，institution=机构内，sub_user=仅创建者'
  AFTER owner_user_id;

ALTER TABLE skill
  ADD COLUMN IF NOT EXISTS subscription_id INT UNSIGNED NOT NULL DEFAULT 0
  COMMENT '市场订阅引用指针：非零=订阅引用行'
  AFTER is_active;

ALTER TABLE skill
  ADD COLUMN IF NOT EXISTS marketplace_id INT UNSIGNED NOT NULL DEFAULT 0
  COMMENT '市场引用指针：非零=运行时改读 marketplace 当前 SanitizedBodyMD 快照'
  AFTER subscription_id;

-- 回填 owner_user_id（仅对默认 0 的行，避免覆盖二次执行结果）
UPDATE skill SET owner_user_id = created_by WHERE owner_user_id = 0;

-- 回填 visibility：仅 *系统级* official 行（parent_user_id = 0）→ globally-visible 'official'，
-- 其余保留安全默认 'institution'（机构内可见，不跨租户泄漏）。
--
-- ⚠️ 安全（cross-tenant leak 防护）：origin_type='official' 是用户可达字段——用户端
-- POST /v1/skills 的 source_type='imported_from_template' 会把 origin_type 设成 'official'，
-- 而 parent_user_id = 调用方机构、body_md 完全由调用方控制。若无条件按 origin_type 升级，
-- 这些机构私有行会被翻成 globally-visible 'official'，导致机构 B 的 GET /v1/skills /
-- GET /v1/skills/:id 读到机构 A 的私有 body_md（visibility='official' 谓词无 parent_user_id 限定）。
-- 因此必须用 parent_user_id = 0 守卫：只有平台 seed 的系统行（owner/parent=0）才是真正的全局官方。
UPDATE skill SET visibility = 'official' WHERE origin_type = 'official' AND parent_user_id = 0;

-- ---- skill_subscription 列 ----
ALTER TABLE skill_subscription
  ADD COLUMN IF NOT EXISTS source_skill_id INT UNSIGNED NOT NULL DEFAULT 0
  COMMENT '发布方原始 skill id（reference-mode >0；legacy clone-mode =0）'
  AFTER cloned_skill_id;

ALTER TABLE skill_subscription
  ADD COLUMN IF NOT EXISTS subscribed_version INT UNSIGNED NOT NULL DEFAULT 0
  COMMENT '订阅时刻 source skill 版本（0=未知/legacy）'
  AFTER source_skill_id;

-- ---- 索引（INFORMATION_SCHEMA 守卫，幂等）----
SET @db := DATABASE();

-- idx_skill_visibility ON skill(parent_user_id, visibility, is_active)
SET @exists := (SELECT COUNT(*) FROM information_schema.statistics
  WHERE table_schema = @db AND table_name = 'skill' AND index_name = 'idx_skill_visibility');
SET @sql := IF(@exists = 0,
  'CREATE INDEX idx_skill_visibility ON skill (parent_user_id, visibility, is_active)',
  'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- idx_skill_owner ON skill(owner_user_id)
SET @exists := (SELECT COUNT(*) FROM information_schema.statistics
  WHERE table_schema = @db AND table_name = 'skill' AND index_name = 'idx_skill_owner');
SET @sql := IF(@exists = 0,
  'CREATE INDEX idx_skill_owner ON skill (owner_user_id)',
  'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- idx_subscription_source ON skill_subscription(source_skill_id)
SET @exists := (SELECT COUNT(*) FROM information_schema.statistics
  WHERE table_schema = @db AND table_name = 'skill_subscription' AND index_name = 'idx_subscription_source');
SET @sql := IF(@exists = 0,
  'CREATE INDEX idx_subscription_source ON skill_subscription (source_skill_id)',
  'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- ---- seed 1 个 official 技能（演示三级可见性；以 owner_user_id=0=系统）----
-- 仅在不存在同名 official 行时插入（幂等）。body 为占位，可后续由 admin 更新。
INSERT INTO skill (parent_user_id, owner_user_id, visibility, name, description, when_to_use,
                   allowed_tools, body_md, source_type, origin_type, version, is_active, created_by)
SELECT 0, 0, 'official', '官方示例技能', '平台内置的官方示例技能，所有用户可见',
       '当你需要一个官方示例参考时',
       JSON_ARRAY(), '# 官方示例技能\n\n这是平台内置的官方示例技能正文。', 'custom', 'official', 1, 1, 0
WHERE NOT EXISTS (
  SELECT 1 FROM (SELECT id FROM skill WHERE visibility = 'official' AND name = '官方示例技能' LIMIT 1) AS t
);
