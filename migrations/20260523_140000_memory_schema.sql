-- Migration: create user_memory_profile + user_memory_facts tables
-- Feature: agent-mode-v15-memory-layer-a (Task 3.2)
-- Spec: /Users/zhiyuchen/Downloads/有数-Agent-Mode-V1.5-NDF-spec/03-memory/task-02-memory-schema.md
-- Rollback: 20260523_140000_memory_schema_rollback.sql
--
-- Scope:
--   Two new tables for V1.5 Agent Memory (Layer A — 对使用者本人画像):
--     1. user_memory_profile  -- 一个用户一行的画像 + dialectic cache
--     2. user_memory_facts    -- 用户 fact 列表，每用户多条
--
-- V2 Layer B 预留: user_memory_facts.subject_id VARCHAR(64) NULLABLE，V1.5 全 NULL
--   - V1.5 强制 NULL，store 层 Create/BatchCreate 拒绝非 NULL 写入
--   - V2 启用 Layer B 时填业务实体 ID（客户 / 数据集 / 文档 / 产线等）
--   - idx_user_subject_confidence 复合 index 已建好，V2 查询可直接 hit
--
-- D7 (拍板规则): B2B2C 父子账户 memory 完全隔离
--   - schema 故意不加 parent_user_id 字段
--   - store interface 强制只接受 userID 单参数
--   - 父账户 ID 不查子账户 facts，子账户 ID 不关联父账户
--
-- Idempotent: CREATE TABLE IF NOT EXISTS
-- FK: ON DELETE CASCADE 自动清理用户注销时的 memory（GDPR）
-- user_id 类型: INT UNSIGNED (匹配 user.id via gorm.Model)

-- ============================================================
-- Table 1: user_memory_profile
-- 一个 user 一行 profile + dialectic cache
-- ============================================================
CREATE TABLE IF NOT EXISTS user_memory_profile (
    user_id                    INT UNSIGNED NOT NULL                  COMMENT 'FK 到 user.id; per-user 单行画像',
    work_context               TEXT                                   COMMENT '工作背景, 如 "医疗器械销售3年"',
    personal_context           TEXT                                   COMMENT '个人偏好, 如 "偏好简洁话术"',
    top_of_mind                TEXT                                   COMMENT '当前关注, 如 "跟进XX医院CT订单"',
    cached_insight             TEXT                                   COMMENT 'task-07 dialectic 推理结果',
    cached_insight_at          TIMESTAMP NULL                         COMMENT '本次 dialectic 推理时间',
    cached_insight_fact_count  INT NOT NULL DEFAULT 0                 COMMENT '推理时的 fact 数, 失效判定用',
    total_facts                INT NOT NULL DEFAULT 0                 COMMENT '当前活跃 fact 计数 (应用层维护, 每日 cron 对账)',
    last_extraction_at         TIMESTAMP NULL                         COMMENT '上次 LLM extraction 跑过的时间',
    last_extraction_session_id VARCHAR(64) NOT NULL DEFAULT ''        COMMENT '上次 extraction 对应的 session_id',
    created_at                 TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at                 TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id),
    CONSTRAINT fk_ump_user FOREIGN KEY (user_id) REFERENCES user(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Agent Mode V1.5 Layer A 用户画像 + dialectic cache';

-- ============================================================
-- Table 2: user_memory_facts
-- 用户 fact 列表，每用户多条
-- ============================================================
CREATE TABLE IF NOT EXISTS user_memory_facts (
    id                   BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    uuid                 VARCHAR(64) NOT NULL                         COMMENT 'app-generated UUID, 业务唯一键',
    user_id              INT UNSIGNED NOT NULL                        COMMENT 'FK 到 user.id; per-user fact 归属',
    -- V2 Layer B 预留: V1.5 强制 NULL (store 层 Create 拒绝非 NULL)
    -- V2 启用时填业务实体 ID (客户 ID / 数据集 ID / 文档 ID / 产线 ID 等)
    subject_id           VARCHAR(64) NULL                             COMMENT 'V2 Layer B 预留, V1.5 强制 NULL',
    content              TEXT NOT NULL                                COMMENT 'fact 文本',
    category             VARCHAR(32) NOT NULL                         COMMENT 'preference/knowledge/context/behavior/goal/correction',
    confidence           DECIMAL(3,2) NOT NULL                        COMMENT '0.00-1.00; 入库阈值 >= 0.70',
    importance           DECIMAL(3,2) NOT NULL DEFAULT 0.50           COMMENT '0.00-1.00; task-07 dialectic 推理后更新',
    source_session_id    VARCHAR(64) NOT NULL DEFAULT ''              COMMENT '产生该 fact 的 session_id',
    source_message_uuid  VARCHAR(64) NOT NULL DEFAULT ''              COMMENT '产生该 fact 的 message_uuid',
    source_extracted_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT 'extraction 抽取此 fact 的时间',
    last_used_at         TIMESTAMP NULL                               COMMENT 'task-04 selector 选中时更新',
    use_count            INT NOT NULL DEFAULT 0                       COMMENT '被 selector 选中过的次数',
    embedding_hash       VARCHAR(64) NOT NULL DEFAULT ''              COMMENT 'SHA256(normalize(content))[:32]; dedup 用',
    is_archived          TINYINT(1) NOT NULL DEFAULT 0                COMMENT '软删除; 0=alive, 1=archived',
    created_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uk_uuid (uuid),
    -- V2 Layer B 兼容性复合 index（user_id, subject_id, confidence DESC）
    -- V1.5 内：subject_id 全 NULL，本 index 不参与查询路由（其他 index 处理）
    -- 接受 V1.5 写放大（per-row index maintenance）换取 V2 启用 Layer B 时零破坏性 ALTER
    -- 见 task-02-memory-schema.md §D-7
    KEY idx_user_subject_confidence (user_id, subject_id, confidence DESC),
    KEY idx_user_confidence         (user_id, confidence DESC, is_archived),
    KEY idx_user_category           (user_id, category, is_archived),
    KEY idx_user_recency            (user_id, source_extracted_at DESC, is_archived),
    KEY idx_user_importance         (user_id, importance DESC, is_archived),
    KEY idx_user_embedhash          (user_id, embedding_hash) COMMENT '同用户 embedding hash 去重查找',
    CONSTRAINT fk_umf_user        FOREIGN KEY (user_id) REFERENCES user(id) ON DELETE CASCADE,
    CONSTRAINT chk_umf_confidence CHECK (confidence >= 0.00 AND confidence <= 1.00),
    CONSTRAINT chk_umf_importance CHECK (importance >= 0.00 AND importance <= 1.00),
    CONSTRAINT chk_umf_category   CHECK (category IN ('preference','knowledge','context','behavior','goal','correction'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Agent Mode V1.5 Layer A 用户 fact 列表 (LLM extraction 入库, top-5 selector 注入)';
