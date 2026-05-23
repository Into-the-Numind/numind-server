-- Migration: create agent_message_search table for FULLTEXT ngram Chinese search
-- Feature: agent-mode-v15-memory-layer-a (Task 3.5)
-- Spec: /Users/zhiyuchen/Downloads/有数-Agent-Mode-V1.5-NDF-spec/03-memory/task-05-fts5-search.md
-- Rollback: 20260523_153000_create_agent_message_search_rollback.sql
--
-- D9 (拍板规则): MySQL 8 FULLTEXT + ngram parser (n=2) 双字符 token.
--   - 量大再升 Elasticsearch (V2 范围，不在本 task)
--   - SQLite 不支持 FULLTEXT — 测试中相关 case skip 并加注释
--
-- 写入容忍: agent_message_search 是衍生数据, BulkInsert 失败仅 log warn 不阻塞主流程.
--
-- B2B2C 隔离: search SQL 强制 WHERE user_id = ? (跨 user 严格隔离).
--
-- 部署前确认 (不修改, 仅 verify):
--   SHOW VARIABLES LIKE 'ngram_token_size';  -- 期望 = 2 (MySQL 8 默认值)
--   若不是 2 需 my.cnf 配 [mysqld] ngram_token_size=2 + 重启 MySQL.
--
-- Idempotent: CREATE TABLE IF NOT EXISTS

CREATE TABLE IF NOT EXISTS agent_message_search (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    agent_run_id    BIGINT UNSIGNED NOT NULL                COMMENT 'FK 到 agent_run.id',
    user_id         INT UNSIGNED NOT NULL                   COMMENT 'FK 到 user.id; 强制隔离',
    session_id      VARCHAR(64) NOT NULL                    COMMENT 'agent_run.session_id 副本',
    message_uuid    VARCHAR(64) NOT NULL                    COMMENT 'app-generated message UUID',
    role            VARCHAR(32) NOT NULL                    COMMENT 'user / assistant (tool 不入库)',
    content         TEXT NOT NULL                           COMMENT 'message content (FULLTEXT 索引)',
    content_length  INT NOT NULL                            COMMENT 'len(content) 字符数',
    message_index   INT NOT NULL                            COMMENT '该 message 在 agent_run.messages 数组中的位置',
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE INDEX uniq_msg_uuid (message_uuid),
    INDEX idx_user_recency (user_id, created_at DESC),
    INDEX idx_run (agent_run_id),
    INDEX idx_session (session_id),
    FULLTEXT INDEX ft_content (content) WITH PARSER ngram
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Agent Mode V1.5 Task 3.5 中文 FULLTEXT 搜索表 (ngram parser, n=2)';
