-- semantic-chunk-reliability T1: 给 knowledge_document 加切块留痕列
-- split_strategy: semantic / rule_fallback / no_split / 空(历史行)
-- split_detail:   兜底原因(如 semantic_error: timeout / semantic_unavailable)
--
-- MySQL 8 不支持 ADD COLUMN IF NOT EXISTS,用 information_schema 守卫(幂等,可重复执行)。
-- Go struct model.KnowledgeDocument 也加了这两列 → AutoMigrate 覆盖新装/dev;本脚本覆盖存量/prod。

-- split_strategy
SET @col_exists := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE()
    AND TABLE_NAME = 'knowledge_document'
    AND COLUMN_NAME = 'split_strategy'
);
SET @ddl := IF(@col_exists = 0,
  'ALTER TABLE knowledge_document ADD COLUMN split_strategy VARCHAR(20) NULL, ADD INDEX idx_kd_split_strategy (split_strategy)',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- split_detail
SET @col_exists2 := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE()
    AND TABLE_NAME = 'knowledge_document'
    AND COLUMN_NAME = 'split_detail'
);
SET @ddl2 := IF(@col_exists2 = 0,
  'ALTER TABLE knowledge_document ADD COLUMN split_detail VARCHAR(512) NULL',
  'SELECT 1');
PREPARE stmt2 FROM @ddl2;
EXECUTE stmt2;
DEALLOCATE PREPARE stmt2;
