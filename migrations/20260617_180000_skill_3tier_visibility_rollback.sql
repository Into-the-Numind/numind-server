-- T4 skill-3tier-visibility ROLLBACK
--
-- ⚠️ 破坏性：删除 owner_user_id / visibility / subscription_id / marketplace_id 列会丢失三级可见性信息。
-- 删除 source_skill_id / subscribed_version 会丢失 reference-mode 订阅的来源指向（订阅引用行将失去 body 更新通道）。
-- 仅在确需回滚时执行。cloned_skill_id 未动，legacy clone 订阅不受影响。

-- 删 seed official 技能（best-effort，按名匹配）
DELETE FROM skill WHERE visibility = 'official' AND name = '官方示例技能' AND owner_user_id = 0;

-- 删索引（INFORMATION_SCHEMA 守卫，幂等）
SET @db := DATABASE();

SET @exists := (SELECT COUNT(*) FROM information_schema.statistics
  WHERE table_schema = @db AND table_name = 'skill' AND index_name = 'idx_skill_visibility');
SET @sql := IF(@exists > 0, 'DROP INDEX idx_skill_visibility ON skill', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @exists := (SELECT COUNT(*) FROM information_schema.statistics
  WHERE table_schema = @db AND table_name = 'skill' AND index_name = 'idx_skill_owner');
SET @sql := IF(@exists > 0, 'DROP INDEX idx_skill_owner ON skill', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @exists := (SELECT COUNT(*) FROM information_schema.statistics
  WHERE table_schema = @db AND table_name = 'skill_subscription' AND index_name = 'idx_subscription_source');
SET @sql := IF(@exists > 0, 'DROP INDEX idx_subscription_source ON skill_subscription', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- 删列
ALTER TABLE skill DROP COLUMN IF EXISTS marketplace_id;
ALTER TABLE skill DROP COLUMN IF EXISTS subscription_id;
ALTER TABLE skill DROP COLUMN IF EXISTS visibility;
ALTER TABLE skill DROP COLUMN IF EXISTS owner_user_id;

ALTER TABLE skill_subscription DROP COLUMN IF EXISTS subscribed_version;
ALTER TABLE skill_subscription DROP COLUMN IF EXISTS source_skill_id;
