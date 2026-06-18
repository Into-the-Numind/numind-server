-- 会议副驾 · 说话人分离（meeting-speaker-diarization feature，DIARIZATION_SPEC §6）。
-- 实时 ASR 管「说什么」，自建轻量声纹引擎（CAM++ 192 维 embedding + 聚类）管「谁说」：
--   · meeting_segment 加在线/离线说话人列；
--   · meeting_speaker 出场序稳定编号 + 色板映射（离线全局重聚类产物）；
--   · meeting_segment_embedding 落库逐段 192-d embedding（离线 AHC 重聚类主路径前提，P0-2）；
--   · meeting_session 加说话人数 + diarization_status。
--
-- feature flag: features.meeting_diarization.enabled（effective = meeting_copilot.enabled && meeting_diarization.enabled）；
--               prod 默认 OFF → 本组列/表休眠不可达。
-- 手动执行（CI 不自动跑 migration，遵 dev-deploy-migration-gap）；仅在启用本功能的环境跑。
-- MySQL 8.0 不支持 ADD COLUMN IF NOT EXISTS，用 information_schema 守卫保证幂等；
-- 建表一律 CREATE TABLE IF NOT EXISTS，重复执行无副作用。
-- NULL 列 GORM 用 *int / *float32 对应；online_provisional / diarization_status 无 default:true，
-- 规避 §database.md 的 default:true bool Create 坑。

-- ── 6.1 meeting_segment：在线/离线说话人列 ──────────────────────────────────────

-- online_speaker_id（在线增量聚类临时簇号；NULL=声纹未就绪/软降级）
-- Rollback: ALTER TABLE `meeting_segment` DROP COLUMN `online_speaker_id`;
SET @col_exists := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE()
    AND TABLE_NAME = 'meeting_segment'
    AND COLUMN_NAME = 'online_speaker_id'
);
SET @ddl := IF(@col_exists = 0,
  'ALTER TABLE `meeting_segment` ADD COLUMN `online_speaker_id` INT NULL COMMENT ''在线增量聚类临时簇号'' AFTER `audio_url`',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- online_provisional（灰区暂定标，不更新质心；无 default:true 规避 GORM Create 坑）
-- Rollback: ALTER TABLE `meeting_segment` DROP COLUMN `online_provisional`;
SET @col_exists := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE()
    AND TABLE_NAME = 'meeting_segment'
    AND COLUMN_NAME = 'online_provisional'
);
SET @ddl := IF(@col_exists = 0,
  'ALTER TABLE `meeting_segment` ADD COLUMN `online_provisional` TINYINT(1) NOT NULL DEFAULT 0 COMMENT ''灰区暂定标（provisional，不更新质心）'' AFTER `online_speaker_id`',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- final_speaker_id（离线全局重聚类稳定簇号；NULL=尚未精修）
-- Rollback: ALTER TABLE `meeting_segment` DROP COLUMN `final_speaker_id`;
SET @col_exists := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE()
    AND TABLE_NAME = 'meeting_segment'
    AND COLUMN_NAME = 'final_speaker_id'
);
SET @ddl := IF(@col_exists = 0,
  'ALTER TABLE `meeting_segment` ADD COLUMN `final_speaker_id` INT NULL COMMENT ''离线全局重聚类稳定簇号'' AFTER `online_provisional`',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- speaker_confidence（聚类置信度，前端弱化展示低 conf；NULL=未知）
-- Rollback: ALTER TABLE `meeting_segment` DROP COLUMN `speaker_confidence`;
SET @col_exists := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE()
    AND TABLE_NAME = 'meeting_segment'
    AND COLUMN_NAME = 'speaker_confidence'
);
SET @ddl := IF(@col_exists = 0,
  'ALTER TABLE `meeting_segment` ADD COLUMN `speaker_confidence` FLOAT NULL COMMENT ''说话人聚类置信度'' AFTER `final_speaker_id`',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- ── 6.2 meeting_speaker：出场序稳定编号 + 色板映射（离线产物）─────────────────────
-- Rollback: DROP TABLE IF EXISTS `meeting_speaker`;
CREATE TABLE IF NOT EXISTS `meeting_speaker` (
  `id`            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `meeting_id`    BIGINT UNSIGNED NOT NULL,                      -- 所属会话（弱关联，无 FK）
  `cluster_id`    INT             NOT NULL,                      -- 离线重聚类簇号
  `display_label` VARCHAR(32)     NOT NULL,                      -- 展示编号（出场序 1/2/3…）
  `color_index`   INT             NOT NULL,                      -- 前端取色板下标
  `created_at`    DATETIME(3)     NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_ms_meeting_cluster` (`meeting_id`, `cluster_id`),
  KEY `idx_ms_meeting` (`meeting_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;

-- ── 6.3 meeting_segment_embedding：逐段 192-d embedding（离线 AHC 主路径前提，P0-2）──
-- Rollback: DROP TABLE IF EXISTS `meeting_segment_embedding`;
-- embedding 用 LONGBLOB（对齐项目现有 agent_session_memory.embedding 约定）。
CREATE TABLE IF NOT EXISTS `meeting_segment_embedding` (
  `id`         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `meeting_id` BIGINT UNSIGNED NOT NULL,                         -- 所属会话（弱关联，无 FK）
  `segment_id` BIGINT UNSIGNED NOT NULL,                         -- 所属分段（弱关联，无 FK）
  `embedding`  LONGBLOB        NOT NULL,                         -- float32×192 packed
  `created_at` DATETIME(3)     NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_mse_segment` (`segment_id`),
  KEY `idx_mse_meeting` (`meeting_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;

-- ── 6.4 meeting_session：说话人数 + diarization_status ────────────────────────────

-- speaker_count（离线精修出的说话人数；NULL=未精修）
-- Rollback: ALTER TABLE `meeting_session` DROP COLUMN `speaker_count`;
SET @col_exists := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE()
    AND TABLE_NAME = 'meeting_session'
    AND COLUMN_NAME = 'speaker_count'
);
SET @ddl := IF(@col_exists = 0,
  'ALTER TABLE `meeting_session` ADD COLUMN `speaker_count` INT NULL COMMENT ''离线精修出的说话人数'' AFTER `running_summary`',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- diarization_status（none/online/refining/done/failed；无 default:true 规避 GORM Create 坑）
-- Rollback: ALTER TABLE `meeting_session` DROP COLUMN `diarization_status`;
SET @col_exists := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE()
    AND TABLE_NAME = 'meeting_session'
    AND COLUMN_NAME = 'diarization_status'
);
SET @ddl := IF(@col_exists = 0,
  'ALTER TABLE `meeting_session` ADD COLUMN `diarization_status` VARCHAR(20) NOT NULL DEFAULT ''none'' COMMENT ''none/online/refining/done/failed'' AFTER `speaker_count`',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
