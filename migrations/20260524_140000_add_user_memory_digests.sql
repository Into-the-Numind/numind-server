-- Migration: create 4 user memory digest tables (daily / weekly / monthly / quarterly)
-- Feature: agent-mode-v15-memory-layer-a (Task 3.8)
-- Spec: /Users/zhiyuchen/Downloads/有数-Agent-Mode-V1.5-NDF-spec/03-memory/task-08-temporal-tree.md
-- Rollback: 20260524_140000_add_user_memory_digests_rollback.sql
--
-- Scope:
--   4 new digest tables for Agent Mode V1.5 Layer A 分层时间感知 (temporal tree):
--     1. user_memory_digest_daily      -- 日级别（昨日活动总结）
--     2. user_memory_digest_weekly     -- ISO 周级别（上周活动综合）
--     3. user_memory_digest_monthly    -- 月级别（上月活动综合）
--     4. user_memory_digest_quarterly  -- 季度级别（上季度活动综合）
--
-- Cron 调度 (Asia/Shanghai timezone):
--   daily     `0 0 4 * * *`           — 每日 04:00 聚合昨日 agent_run/messages
--   weekly    `0 30 4 * * 1`          — 每周一 04:30 聚合上周 7 天 daily
--   monthly   `0 30 4 1 * *`          — 每月 1 号 04:30 聚合上月 weekly
--   quarterly `0 30 4 1 1,4,7,10 *`   — 季度首日 04:30 聚合上季度 monthly
--
-- D7 (拍板规则): B2B2C 父子账户 memory **完全隔离**
--   - schema 故意不加 parent_user_id 字段
--   - digest 严格按 user_id 隔离，cron 每用户独立生成
--   - 父账户**看不到**子账户 digest（这是有意识的 trade-off）
--
-- Idempotent: CREATE TABLE IF NOT EXISTS
--             UNIQUE KEY per-user-period 支持 ON DUPLICATE KEY UPDATE (cron 重跑覆盖)
-- FK: ON DELETE CASCADE 自动清理用户注销时的 digest (GDPR)
--
-- user_id 类型: BIGINT UNSIGNED (匹配 user.id; ⚠️ 历史修正 2026-05-23:
--               原 implementer 误判 user.id 为 INT UNSIGNED, 实际是 BIGINT UNSIGNED
--               via gorm.Model. dev 数据库验证 SHOW COLUMNS FROM user WHERE Field='id'.
--               FK 类型必须严格匹配, 否则 InnoDB ERROR 3780 拒绝创建外键)

-- ============================================================
-- Table 1: user_memory_digest_daily
-- 每用户每日一行：聚合昨日所有 agent_run / messages → LLM 生成 100-200 字总结
-- ============================================================
CREATE TABLE IF NOT EXISTS user_memory_digest_daily (
    id                    BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id               BIGINT UNSIGNED NOT NULL                    COMMENT 'FK 到 user.id',
    digest_date           DATE NOT NULL                                COMMENT '日期 (Asia/Shanghai 时区)',
    session_count         INT NOT NULL DEFAULT 0                       COMMENT '当日 session 数',
    message_count         INT NOT NULL DEFAULT 0                       COMMENT '当日 message 总数',
    extracted_facts_count INT NOT NULL DEFAULT 0                       COMMENT '当日新增 user_memory_facts 数',
    summary               TEXT                                         COMMENT 'LLM 生成 100-200 字第三人称总结',
    key_topics            JSON                                         COMMENT 'JSON array, 3-5 个关键主题 e.g. ["医疗器械","XX医院"]',
    llm_cost_credits      INT NOT NULL DEFAULT 0                       COMMENT 'LLM 调用消耗的 credits (cost 追踪)',
    generated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT 'digest 生成时间 (cron 跑的时间)',
    PRIMARY KEY (id),
    UNIQUE KEY uniq_user_date (user_id, digest_date),
    KEY idx_date (digest_date),
    CONSTRAINT fk_umdd_user FOREIGN KEY (user_id) REFERENCES user(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Agent Mode V1.5 Layer A 日级 digest (cron 每日 04:00 跑)';

-- ============================================================
-- Table 2: user_memory_digest_weekly (ISO 周, 跨自然年用 isoYear)
-- 每用户每 ISO 周一行：聚合上周 7 天 daily digest
-- ============================================================
CREATE TABLE IF NOT EXISTS user_memory_digest_weekly (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id         BIGINT UNSIGNED NOT NULL                    COMMENT 'FK 到 user.id',
    iso_year        INT NOT NULL                                 COMMENT 'ISO 年 (可能跨自然年, e.g. 2026-01-01 → 2026-W01)',
    iso_week        TINYINT NOT NULL                             COMMENT 'ISO 周 1-53',
    week_start_date DATE NOT NULL                                COMMENT '本 ISO 周一日期 (Asia/Shanghai)',
    week_end_date   DATE NOT NULL                                COMMENT '本 ISO 周日日期 (Asia/Shanghai)',
    summary         TEXT                                         COMMENT 'LLM 综合归纳 200-300 字',
    key_topics      JSON                                         COMMENT 'JSON array, 5-10 个关键主题',
    generated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT 'digest 生成时间',
    PRIMARY KEY (id),
    UNIQUE KEY uniq_user_week (user_id, iso_year, iso_week),
    KEY idx_week_range (week_start_date),
    CONSTRAINT fk_umdw_user FOREIGN KEY (user_id) REFERENCES user(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Agent Mode V1.5 Layer A 周级 digest (cron 每周一 04:30 跑)';

-- ============================================================
-- Table 3: user_memory_digest_monthly (自然年/月)
-- 每用户每月一行：聚合上月所有 weekly digest
-- ============================================================
CREATE TABLE IF NOT EXISTS user_memory_digest_monthly (
    id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id      BIGINT UNSIGNED NOT NULL                    COMMENT 'FK 到 user.id',
    year         INT NOT NULL                                 COMMENT '自然年',
    month        TINYINT NOT NULL                             COMMENT '月 1-12',
    summary      TEXT                                         COMMENT 'LLM 综合归纳 200-300 字',
    key_topics   JSON                                         COMMENT 'JSON array, 5-10 个关键主题',
    generated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT 'digest 生成时间',
    PRIMARY KEY (id),
    UNIQUE KEY uniq_user_month (user_id, year, month),
    CONSTRAINT fk_umdm_user FOREIGN KEY (user_id) REFERENCES user(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Agent Mode V1.5 Layer A 月级 digest (cron 每月 1 号 04:30 跑)';

-- ============================================================
-- Table 4: user_memory_digest_quarterly (自然季度: Q1=Jan-Mar, Q2=Apr-Jun, Q3=Jul-Sep, Q4=Oct-Dec)
-- 每用户每季度一行：聚合上季度所有 monthly digest
-- ============================================================
CREATE TABLE IF NOT EXISTS user_memory_digest_quarterly (
    id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id      BIGINT UNSIGNED NOT NULL                    COMMENT 'FK 到 user.id',
    year         INT NOT NULL                                 COMMENT '自然年',
    quarter      TINYINT NOT NULL                             COMMENT '季度 1-4',
    summary      TEXT                                         COMMENT 'LLM 综合归纳 200-300 字',
    key_topics   JSON                                         COMMENT 'JSON array, 5-10 个关键主题',
    generated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT 'digest 生成时间',
    PRIMARY KEY (id),
    UNIQUE KEY uniq_user_quarter (user_id, year, quarter),
    CONSTRAINT fk_umdq_user FOREIGN KEY (user_id) REFERENCES user(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Agent Mode V1.5 Layer A 季度级 digest (cron 季度首日 04:30 跑)';
