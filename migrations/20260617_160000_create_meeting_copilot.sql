-- 会议副驾 (Meeting Copilot) v1：4 张表 + 索引 + 3 个内置预设 seed（meeting-copilot feature）
-- feature flag: features.meeting_copilot.enabled（prod 默认 off → 本组表休眠不可达）
-- 手动执行（CI 不自动跑 migration，遵 dev-deploy-migration-gap）；仅在启用本功能的环境跑。
-- 全部 CREATE TABLE IF NOT EXISTS，外键列建索引，幂等（重复执行无副作用）。

-- 2.1 会话主表
CREATE TABLE IF NOT EXISTS `meeting_session` (
  `id`                    BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id`               INT UNSIGNED    NOT NULL,                       -- 归属用户
  `title`                 VARCHAR(255)    NOT NULL DEFAULT '',            -- 标题（默认「未命名会议 + 时间」）
  `role_prompt`           TEXT            NOT NULL,                       -- 角色定位 + 反馈规则
  `preset_id`             BIGINT UNSIGNED NULL,                           -- 若从预设载入（弱关联，无 FK）
  `status`                VARCHAR(20)     NOT NULL DEFAULT 'active',      -- active / ended
  `auto_interval_seconds` INT             NOT NULL DEFAULT 60,            -- 自动反馈最小间隔
  `recording_url`         VARCHAR(1024)   NULL,                           -- 预留（MVP 录音=分段列表）
  `duration_seconds`      INT             NOT NULL DEFAULT 0,             -- 结束时统计
  `summary`               MEDIUMTEXT      NULL,                           -- AI 纪要（markdown）
  `summary_status`        VARCHAR(20)     NOT NULL DEFAULT 'none',        -- none / generating / done / failed / skipped
  `started_at`            DATETIME(3)     NULL,
  `ended_at`              DATETIME(3)     NULL,
  `created_at`            DATETIME(3)     NOT NULL,
  `updated_at`            DATETIME(3)     NOT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_msess_user` (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;

-- 2.2 转写分段
CREATE TABLE IF NOT EXISTS `meeting_segment` (
  `id`               BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `session_id`       BIGINT UNSIGNED NOT NULL,
  `seq`              INT             NOT NULL,                            -- 顺序
  `text`             TEXT            NULL,                                -- 该段转写文本（可空字符串=静音段）
  `start_ms`         INT             NOT NULL DEFAULT 0,                  -- 相对会议开始的毫秒偏移（best-effort）
  `duration_seconds` DOUBLE          NOT NULL DEFAULT 0,                  -- ASR 返回的音频时长
  `audio_url`        VARCHAR(1024)   NULL,                               -- 该段音频在 COS 的地址（录音回放用）
  `created_at`       DATETIME(3)     NOT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_mseg_session` (`session_id`),
  KEY `idx_mseg_session_seq` (`session_id`, `seq`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;

-- 2.3 反馈事件
CREATE TABLE IF NOT EXISTS `meeting_feedback` (
  `id`         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `session_id` BIGINT UNSIGNED NOT NULL,
  `trigger`    VARCHAR(10)     NOT NULL,                                 -- auto / manual
  `anchor_seq` INT             NOT NULL DEFAULT 0,                       -- 生成时转写进度锚点
  `content`    TEXT            NULL,                                     -- 反馈正文（markdown）
  `created_at` DATETIME(3)     NOT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_mfb_session` (`session_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;

-- 2.4 角色预设
CREATE TABLE IF NOT EXISTS `meeting_preset` (
  `id`                    BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id`               INT UNSIGNED    NOT NULL,                       -- 0 = 系统内置模板
  `name`                  VARCHAR(100)    NOT NULL,
  `role_prompt`           TEXT            NOT NULL,
  `auto_interval_seconds` INT             NOT NULL DEFAULT 60,
  `is_builtin`            TINYINT(1)      NOT NULL DEFAULT 0,             -- 系统内置不可删
  `created_at`            DATETIME(3)     NOT NULL,
  `updated_at`            DATETIME(3)     NOT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_mpreset_user` (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;

-- seed 3 个内置预设（user_id=0, is_builtin=1）。
-- 幂等：按 name 在 (user_id=0, is_builtin=1) 范围内不存在时才插入，重复执行不产生重复行。
INSERT INTO `meeting_preset` (`user_id`, `name`, `role_prompt`, `auto_interval_seconds`, `is_builtin`, `created_at`, `updated_at`)
SELECT 0, '辩论陪练',
       '你是我的辩论陪练。实时听我和对手的论辩，当我出现逻辑漏洞、举证不足或被对方抓住把柄时立刻提醒我，并给出一句可立即使用的反驳或补强。其他时候保持沉默。',
       60, 1, NOW(3), NOW(3)
WHERE NOT EXISTS (
  SELECT 1 FROM `meeting_preset` WHERE `user_id` = 0 AND `is_builtin` = 1 AND `name` = '辩论陪练'
);

INSERT INTO `meeting_preset` (`user_id`, `name`, `role_prompt`, `auto_interval_seconds`, `is_builtin`, `created_at`, `updated_at`)
SELECT 0, '客户访谈记录员',
       '你是资深用户研究员。我在做客户访谈。当客户透露关键痛点、预算、决策链或竞品信息时，提示我该追问的下一个问题；当我问了引导性/封闭式问题时提醒我改开放式提问。',
       60, 1, NOW(3), NOW(3)
WHERE NOT EXISTS (
  SELECT 1 FROM `meeting_preset` WHERE `user_id` = 0 AND `is_builtin` = 1 AND `name` = '客户访谈记录员'
);

INSERT INTO `meeting_preset` (`user_id`, `name`, `role_prompt`, `auto_interval_seconds`, `is_builtin`, `created_at`, `updated_at`)
SELECT 0, '头脑风暴催化剂',
       '你是头脑风暴催化剂。当讨论卡壳或重复绕圈时，抛出一个新角度或一个''如果……会怎样''的发散问题；当出现好点子时帮我一句话凝练它。不要打断流畅的发散。',
       60, 1, NOW(3), NOW(3)
WHERE NOT EXISTS (
  SELECT 1 FROM `meeting_preset` WHERE `user_id` = 0 AND `is_builtin` = 1 AND `name` = '头脑风暴催化剂'
);
