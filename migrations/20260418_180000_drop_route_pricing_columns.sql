-- Drop 5 dead pricing columns from ai_service_route.
--
-- Rationale (pricing_architecture_decision "搞法 2"):
--   Pricing data is authoritative in the pricing_rule table.
--   The duplicate columns on ai_service_route (input_price_per_mtok,
--   output_price_per_mtok, price_per_call, price_per_second, pricing_unit)
--   were rendered redundant when the billing middleware was updated to read
--   pricing snapshots directly from pricing_rule at call time.
--   Keeping dead columns causes vocabulary drift between the two tables
--   (as diagnosed by the T-arch gate) and misleads future maintainers.
--
-- ⚠️  PROD WARNING: DO NOT run this migration until AFTER the code that
--   removes AIServiceRoute.{InputPricePerMTok,OutputPricePerMTok,PricingUnit,
--   PricePerCall,PricePerSecond} has been DEPLOYED and confirmed healthy.
--   Running the migration before code deploy causes the GORM model to attempt
--   to SELECT columns that no longer exist → runtime panic on first query.
--   Sequence: deploy code → verify health → run migration.
--
-- Idempotent: each DROP is guarded by an INFORMATION_SCHEMA check so
--   re-running is safe (MySQL 8.0.28- does not support DROP COLUMN IF EXISTS).
--
-- ============================================================
-- Column: input_price_per_mtok
-- ============================================================
SET @col_exists := (
    SELECT COUNT(*)
    FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME   = 'ai_service_route'
      AND COLUMN_NAME  = 'input_price_per_mtok'
);
SET @sql := IF(@col_exists > 0,
    'ALTER TABLE ai_service_route DROP COLUMN input_price_per_mtok',
    'SELECT "column input_price_per_mtok already dropped" AS note'
);
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- ============================================================
-- Column: output_price_per_mtok
-- ============================================================
SET @col_exists := (
    SELECT COUNT(*)
    FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME   = 'ai_service_route'
      AND COLUMN_NAME  = 'output_price_per_mtok'
);
SET @sql := IF(@col_exists > 0,
    'ALTER TABLE ai_service_route DROP COLUMN output_price_per_mtok',
    'SELECT "column output_price_per_mtok already dropped" AS note'
);
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- ============================================================
-- Column: price_per_call
-- ============================================================
SET @col_exists := (
    SELECT COUNT(*)
    FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME   = 'ai_service_route'
      AND COLUMN_NAME  = 'price_per_call'
);
SET @sql := IF(@col_exists > 0,
    'ALTER TABLE ai_service_route DROP COLUMN price_per_call',
    'SELECT "column price_per_call already dropped" AS note'
);
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- ============================================================
-- Column: price_per_second
-- ============================================================
SET @col_exists := (
    SELECT COUNT(*)
    FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME   = 'ai_service_route'
      AND COLUMN_NAME  = 'price_per_second'
);
SET @sql := IF(@col_exists > 0,
    'ALTER TABLE ai_service_route DROP COLUMN price_per_second',
    'SELECT "column price_per_second already dropped" AS note'
);
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- ============================================================
-- Column: pricing_unit
-- ============================================================
SET @col_exists := (
    SELECT COUNT(*)
    FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME   = 'ai_service_route'
      AND COLUMN_NAME  = 'pricing_unit'
);
SET @sql := IF(@col_exists > 0,
    'ALTER TABLE ai_service_route DROP COLUMN pricing_unit',
    'SELECT "column pricing_unit already dropped" AS note'
);
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- ============================================================
-- ROLLBACK (run manually if needed — restores original DDL)
-- ============================================================
-- ALTER TABLE ai_service_route
--   ADD COLUMN input_price_per_mtok  DECIMAL(10,4) NOT NULL DEFAULT 0          AFTER priority,
--   ADD COLUMN output_price_per_mtok DECIMAL(10,4) NOT NULL DEFAULT 0          AFTER input_price_per_mtok,
--   ADD COLUMN price_per_call        DECIMAL(10,6)              DEFAULT NULL    AFTER output_price_per_mtok,
--   ADD COLUMN price_per_second      DECIMAL(10,6)              DEFAULT NULL    AFTER price_per_call,
--   ADD COLUMN pricing_unit          VARCHAR(20)   NOT NULL DEFAULT 'per_1m_tokens' AFTER price_per_second;
