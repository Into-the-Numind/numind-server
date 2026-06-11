-- Feature: thinking-activation-style
-- Adds ai_service.thinking_style — selects HOW thinking is activated on the
-- OpenAI-compatible wire when supports_thinking=1 AND thinking_only=0.
--   '' / 'reasoning_effort'  → reasoning_effort:"medium" (legacy default, today's behavior)
--   'enable_thinking_kwarg'  → chat_template_kwargs:{"enable_thinking":true} (Qwen/vLLM style)
--   'none'                   → inject nothing
--
-- NOTE: GORM AutoMigrate also creates this column on service start (model field
-- carries gorm:"size:32;default:''"). This migration is the authoritative record
-- and, crucially, performs the agnes-2.0-flash data fix that AutoMigrate cannot.
--
-- Idempotency: MySQL does NOT support `ADD COLUMN IF NOT EXISTS` (MariaDB only;
-- errored ERROR 1064 on prod mysql:8.4.2 — see 20260515_100000). Use the
-- information_schema pre-check + prepared statement pattern instead. Safe to
-- re-run and safe even if AutoMigrate already created the column.

-- ── 1. Schema: idempotent column add (MySQL-compatible) ──────────────────────
SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
                    WHERE TABLE_SCHEMA = DATABASE()
                      AND TABLE_NAME   = 'ai_service'
                      AND COLUMN_NAME  = 'thinking_style');
SET @sql := IF(@col_exists = 0,
               'ALTER TABLE ai_service ADD COLUMN thinking_style VARCHAR(32) NOT NULL DEFAULT '''' COMMENT ''thinking activation style: reasoning_effort (default/empty) | enable_thinking_kwarg | none'' AFTER thinking_only',
               'SELECT 1');
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- ── 2. Data fix: agnes-2.0-flash (0-price member-only model) ─────────────────
-- agnes activates thinking via chat_template_kwargs.enable_thinking, NOT
-- reasoning_effort. Prior config was contradictory (supports_thinking=0 +
-- thinking_only=1) so no thinking param was ever sent. Correct it to an
-- optional-thinking model using the kwarg style. Naturally idempotent.
UPDATE ai_service
SET thinking_style    = 'enable_thinking_kwarg',
    supports_thinking = 1,
    thinking_only     = 0
WHERE model_key = 'agnes-2.0-flash';
