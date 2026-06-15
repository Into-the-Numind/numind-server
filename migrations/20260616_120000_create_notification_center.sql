-- ============================================================
-- Migration: Notification Center Schema
-- File: 20260616_120000_create_notification_center.sql
-- Spec: .ndf/features/notification-center/spec.md §1
-- MySQL 版本要求: 8.0+
-- 幂等性: 所有 CREATE TABLE 用 IF NOT EXISTS（FK + 索引内联，re-run 安全）；
--         补加 FK 通过 information_schema 条件守卫（AutoMigrate 已建表时不重复加 FK）。
-- 部署: AutoMigrate 在 boot 时建表/列/简单索引（model tag 驱动）；本 migration
--       负责 AutoMigrate 不可靠的 FK 约束 + 复合 UNIQUE，需上线前手工 SSH 执行
--       （参考 dev-deploy-migration-gap）。
-- ============================================================

-- ============================================================
-- Table 1: announcement — 公告/问卷主表（§1.1）
-- ============================================================
CREATE TABLE IF NOT EXISTS announcement (
    id            BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    type          VARCHAR(16) NOT NULL DEFAULT 'plain'
                   COMMENT 'plain | survey',
    title         VARCHAR(200) NOT NULL,
    content       LONGTEXT NOT NULL
                   COMMENT 'Markdown',
    is_important  TINYINT(1) NOT NULL DEFAULT 0
                   COMMENT '预留（铃铛+可选弹窗），V1 不用弹窗',
    audience      VARCHAR(32) NOT NULL DEFAULT 'all'
                   COMMENT '受众扩展位，V1 只用 all',
    status        VARCHAR(16) NOT NULL DEFAULT 'draft'
                   COMMENT 'draft | published | archived',
    published_at  DATETIME NULL,
    expires_at    DATETIME NULL
                   COMMENT 'NULL = 永不过期',
    created_by    INT UNSIGNED NOT NULL
                   COMMENT 'admin user id',
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    deleted_at    DATETIME NULL
                   COMMENT '软删（gorm.DeletedAt）',
    INDEX idx_ann_status_pub (status, published_at),
    INDEX idx_ann_type (type),
    INDEX idx_ann_deleted (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- Table 2: announcement_read — 已读回执（§1.2）
-- ============================================================
CREATE TABLE IF NOT EXISTS announcement_read (
    id               BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    announcement_id  BIGINT UNSIGNED NOT NULL,
    user_id          INT UNSIGNED NOT NULL,
    read_at          DATETIME NOT NULL
                      COMMENT '首次已读时间（幂等保留）',
    created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_annread (announcement_id, user_id),
    INDEX idx_annread_user (user_id),
    CONSTRAINT fk_annread_announcement
      FOREIGN KEY (announcement_id) REFERENCES announcement(id) ON DELETE CASCADE,
    CONSTRAINT fk_annread_user
      FOREIGN KEY (user_id) REFERENCES user(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- Table 3: survey_question — 问卷题目（§1.3）
-- ============================================================
CREATE TABLE IF NOT EXISTS survey_question (
    id               BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    announcement_id  BIGINT UNSIGNED NOT NULL,
    order_index      INT NOT NULL DEFAULT 0
                      COMMENT '题序',
    question_type    VARCHAR(16) NOT NULL
                      COMMENT 'single | multi | rating | text',
    title            VARCHAR(500) NOT NULL
                      COMMENT '题干',
    options          JSON NULL
                      COMMENT 'single/multi 的选项数组；rating/text 为 NULL',
    rating_max       INT NULL
                      COMMENT 'rating 题：最大分值（2-10）',
    rating_style     VARCHAR(10) NULL
                      COMMENT 'rating 题：star | nps',
    required         TINYINT(1) NOT NULL DEFAULT 1,
    created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_sq_ann (announcement_id),
    CONSTRAINT fk_sq_announcement
      FOREIGN KEY (announcement_id) REFERENCES announcement(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- Table 4: survey_response — 答卷（§1.4，一人一份）
-- ============================================================
CREATE TABLE IF NOT EXISTS survey_response (
    id               BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    announcement_id  BIGINT UNSIGNED NOT NULL,
    user_id          INT UNSIGNED NOT NULL,
    submitted_at     DATETIME NOT NULL,
    created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_sr (announcement_id, user_id),
    INDEX idx_sr_ann (announcement_id),
    CONSTRAINT fk_sr_announcement
      FOREIGN KEY (announcement_id) REFERENCES announcement(id) ON DELETE CASCADE,
    CONSTRAINT fk_sr_user
      FOREIGN KEY (user_id) REFERENCES user(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- Table 5: survey_answer — 单题答案（§1.5）
-- ============================================================
CREATE TABLE IF NOT EXISTS survey_answer (
    id             BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    response_id    BIGINT UNSIGNED NOT NULL,
    question_id    BIGINT UNSIGNED NOT NULL,
    answer_options JSON NULL
                    COMMENT '选中的选项值数组（single 1 个 / multi N 个）',
    answer_rating  INT NULL
                    COMMENT 'rating 值',
    answer_text    TEXT NULL
                    COMMENT '开放文本',
    created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_sa_response (response_id),
    INDEX idx_sa_question (question_id),
    CONSTRAINT fk_sa_response
      FOREIGN KEY (response_id) REFERENCES survey_response(id) ON DELETE CASCADE,
    CONSTRAINT fk_sa_question
      FOREIGN KEY (question_id) REFERENCES survey_question(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- FK backfill: 若表由 AutoMigrate 先建（无 FK），本段补加 FK + UNIQUE。
-- 用 information_schema 守卫，已存在则跳过 → re-run 安全。
-- （ON DELETE CASCADE: 删公告级联删回执/题目/答卷；删答卷级联删答案；
--   删用户级联删其回执/答卷。）
-- ============================================================
DROP PROCEDURE IF EXISTS _mig_notification_fk;
DELIMITER //
CREATE PROCEDURE _mig_notification_fk()
BEGIN
  -- announcement_read.uk_annread
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'announcement_read' AND INDEX_NAME = 'uk_annread'
  ) THEN
    ALTER TABLE announcement_read ADD UNIQUE KEY uk_annread (announcement_id, user_id);
  END IF;

  -- announcement_read FKs
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'announcement_read' AND CONSTRAINT_NAME = 'fk_annread_announcement'
  ) THEN
    ALTER TABLE announcement_read
      ADD CONSTRAINT fk_annread_announcement
      FOREIGN KEY (announcement_id) REFERENCES announcement(id) ON DELETE CASCADE;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'announcement_read' AND CONSTRAINT_NAME = 'fk_annread_user'
  ) THEN
    ALTER TABLE announcement_read
      ADD CONSTRAINT fk_annread_user
      FOREIGN KEY (user_id) REFERENCES user(id) ON DELETE CASCADE;
  END IF;

  -- survey_question FK
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'survey_question' AND CONSTRAINT_NAME = 'fk_sq_announcement'
  ) THEN
    ALTER TABLE survey_question
      ADD CONSTRAINT fk_sq_announcement
      FOREIGN KEY (announcement_id) REFERENCES announcement(id) ON DELETE CASCADE;
  END IF;

  -- survey_response.uk_sr
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'survey_response' AND INDEX_NAME = 'uk_sr'
  ) THEN
    ALTER TABLE survey_response ADD UNIQUE KEY uk_sr (announcement_id, user_id);
  END IF;

  -- survey_response FKs
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'survey_response' AND CONSTRAINT_NAME = 'fk_sr_announcement'
  ) THEN
    ALTER TABLE survey_response
      ADD CONSTRAINT fk_sr_announcement
      FOREIGN KEY (announcement_id) REFERENCES announcement(id) ON DELETE CASCADE;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'survey_response' AND CONSTRAINT_NAME = 'fk_sr_user'
  ) THEN
    ALTER TABLE survey_response
      ADD CONSTRAINT fk_sr_user
      FOREIGN KEY (user_id) REFERENCES user(id) ON DELETE CASCADE;
  END IF;

  -- survey_answer FKs
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'survey_answer' AND CONSTRAINT_NAME = 'fk_sa_response'
  ) THEN
    ALTER TABLE survey_answer
      ADD CONSTRAINT fk_sa_response
      FOREIGN KEY (response_id) REFERENCES survey_response(id) ON DELETE CASCADE;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'survey_answer' AND CONSTRAINT_NAME = 'fk_sa_question'
  ) THEN
    ALTER TABLE survey_answer
      ADD CONSTRAINT fk_sa_question
      FOREIGN KEY (question_id) REFERENCES survey_question(id) ON DELETE CASCADE;
  END IF;
END//
DELIMITER ;
CALL _mig_notification_fk();
DROP PROCEDURE IF EXISTS _mig_notification_fk;

-- ============================================================
-- Migration 完成
-- 验证查询：
--   SHOW TABLES LIKE 'announcement';        -- 应存在
--   SHOW TABLES LIKE 'announcement_read';    -- 应存在
--   SHOW TABLES LIKE 'survey_question';      -- 应存在
--   SHOW TABLES LIKE 'survey_response';      -- 应存在
--   SHOW TABLES LIKE 'survey_answer';        -- 应存在
--   SELECT TABLE_NAME, CONSTRAINT_NAME, CONSTRAINT_TYPE
--     FROM information_schema.TABLE_CONSTRAINTS
--     WHERE CONSTRAINT_SCHEMA = DATABASE()
--       AND CONSTRAINT_NAME IN ('fk_annread_announcement','fk_annread_user','fk_sq_announcement',
--                               'fk_sr_announcement','fk_sr_user','fk_sa_response','fk_sa_question',
--                               'uk_annread','uk_sr');
--     -- 应 = 9 行（7 FK + 2 UNIQUE）
-- ============================================================
