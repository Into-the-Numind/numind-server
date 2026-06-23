-- Migration: Create xhs_topic_note table for XHS topic collector feature
-- Date: 2026-06-24
-- Feature: xhs-collector (T1)
-- 字段/索引与 internal/pkg/model/xhs_topic.go 的 XhsTopicNote 严格一致。
-- 幂等：CREATE TABLE IF NOT EXISTS。dev 需手工 SSH 执行（CI 不跑 migration）。

CREATE TABLE IF NOT EXISTS xhs_topic_note (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  user_id INT UNSIGNED NOT NULL,
  xhs_note_id VARCHAR(100) NOT NULL,
  content_hash VARCHAR(64) COMMENT 'SHA256(title+content+video_url)，防重复富化/扣分',

  note_type VARCHAR(20) DEFAULT 'normal' COMMENT 'normal/video',
  title VARCHAR(500),
  content TEXT,
  tags JSON,
  cover_url VARCHAR(1000),
  note_url VARCHAR(1000),
  published_at TIMESTAMP NULL,
  video_url VARCHAR(1000),
  video_transcript TEXT COMMENT 'NULL=无转写(区分直链失效/未转)',
  like_count INT DEFAULT 0,
  collect_count INT DEFAULT 0,
  comment_count INT DEFAULT 0,
  share_count INT DEFAULT 0,
  comments JSON COMMENT '热门前 <=10 条，每条 text <=200 字',
  author_name VARCHAR(200),
  author_link VARCHAR(500),
  author_followers INT DEFAULT 0 COMMENT '取不到=0(已知限制)',

  ai_topic_angle TEXT,
  ai_viral_reason TEXT,
  ai_borrowable TEXT,
  ai_target_audience TEXT,
  ai_title_formula TEXT,
  ai_one_line VARCHAR(500),

  enrich_status VARCHAR(24) DEFAULT 'pending' COMMENT 'pending/enriching/done/partial/failed/insufficient_credits',
  collected_at TIMESTAMP NULL COMMENT '客户端采集时刻(payload 传入)',
  crawled_at TIMESTAMP NULL COMMENT '服务端入库时刻',
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,

  UNIQUE KEY uk_xtn_user_note (user_id, xhs_note_id),
  INDEX idx_xtn_user_crawled (user_id, crawled_at),
  INDEX idx_xtn_enrich (enrich_status),
  INDEX idx_xtn_hash (content_hash),
  INDEX idx_xtn_published (published_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='小红书选题采集笔记，累积选题库';
