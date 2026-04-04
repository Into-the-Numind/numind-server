-- Migration: Create monitor tables for content monitoring feature
-- Date: 2026-04-04
-- Feature: content-monitor

CREATE TABLE IF NOT EXISTS monitor_blogger (
  id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  user_id INT UNSIGNED NOT NULL,
  xhs_user_id VARCHAR(100) NOT NULL,
  nickname VARCHAR(200),
  avatar_url VARCHAR(500),
  bio TEXT,
  followers INT UNSIGNED DEFAULT 0,
  category VARCHAR(100),
  is_active BOOLEAN DEFAULT TRUE,
  check_error VARCHAR(500),
  consecutive_failures INT UNSIGNED DEFAULT 0,
  last_check_at TIMESTAMP NULL,
  last_note_at TIMESTAMP NULL,
  next_check_at TIMESTAMP NULL,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  deleted_at TIMESTAMP NULL,
  UNIQUE KEY uk_user_blogger (user_id, xhs_user_id),
  INDEX idx_blogger_active (user_id, is_active),
  INDEX idx_blogger_deleted (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS monitor_note (
  id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  user_id INT UNSIGNED NOT NULL,
  blogger_id INT UNSIGNED NOT NULL,
  xhs_note_id VARCHAR(100) NOT NULL,
  title VARCHAR(500),
  content TEXT,
  note_type VARCHAR(20) DEFAULT 'image',
  tags JSON,
  likes INT UNSIGNED DEFAULT 0,
  comments INT UNSIGNED DEFAULT 0,
  collects INT UNSIGNED DEFAULT 0,
  shares INT UNSIGNED DEFAULT 0,
  images JSON,
  video_url VARCHAR(1000),
  transcript TEXT,
  ai_summary TEXT,
  ai_topics JSON,
  ai_category VARCHAR(100),
  published_at TIMESTAMP NULL,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  UNIQUE KEY uk_user_note (user_id, xhs_note_id),
  INDEX idx_note_blogger (blogger_id),
  INDEX idx_note_published (user_id, published_at DESC)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS monitor_briefing (
  id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  user_id INT UNSIGNED NOT NULL,
  type VARCHAR(20) NOT NULL,
  title VARCHAR(200),
  content TEXT,
  note_count INT UNSIGNED DEFAULT 0,
  highlights JSON,
  trends JSON,
  period_start DATE,
  period_end DATE,
  feishu_sent BOOLEAN DEFAULT FALSE,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY uk_briefing_user_type_period (user_id, type, period_end),
  INDEX idx_briefing_user_date (user_id, period_end DESC)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS monitor_config (
  id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  user_id INT UNSIGNED NOT NULL UNIQUE,
  crawl_cron VARCHAR(50) DEFAULT '0 */8 * * *',
  briefing_cron VARCHAR(50) DEFAULT '0 20 * * *',
  briefing_type VARCHAR(20) DEFAULT 'daily',
  feishu_webhook VARCHAR(500),
  feishu_bitable_config JSON,
  notify_on_update BOOLEAN DEFAULT TRUE,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
