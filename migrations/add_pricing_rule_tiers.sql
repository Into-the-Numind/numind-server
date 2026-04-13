-- 文件: numind-server/migrations/add_pricing_rule_tiers.sql
-- 定价规则分段支持 + 消费分析索引

-- 1. pricing_rule 新增 billing_mode 和 flat_unit
ALTER TABLE pricing_rule
  ADD COLUMN billing_mode VARCHAR(20) NOT NULL DEFAULT 'flat'
    COMMENT 'tiered_token | flat' AFTER model,
  ADD COLUMN flat_unit VARCHAR(10) NOT NULL DEFAULT 'call'
    COMMENT 'call | gb，仅 billing_mode=flat 时有效' AFTER billing_mode;

-- 2. 新建分段定价子表
CREATE TABLE IF NOT EXISTS pricing_rule_tier (
  id            BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  rule_id       BIGINT UNSIGNED NOT NULL COMMENT '关联 pricing_rule.id（BIGINT UNSIGNED，与父表一致）',
  token_type    VARCHAR(10)   NOT NULL COMMENT 'input | output',
  min_tokens    INT UNSIGNED  NOT NULL DEFAULT 0 COMMENT '区间起点（含）',
  max_tokens    INT UNSIGNED  NULL COMMENT '区间终点（含），NULL 表示不限',
  cost_per_mtok DECIMAL(12,6) NOT NULL DEFAULT 0 COMMENT '成本价（元/百万token）',
  sell_per_mtok DECIMAL(12,6) NOT NULL DEFAULT 0 COMMENT '售价（元/百万token）',
  created_at    DATETIME DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  updated_at    DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  FOREIGN KEY (rule_id) REFERENCES pricing_rule(id) ON DELETE CASCADE,
  INDEX idx_rule_type (rule_id, token_type)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='定价规则分段配置';

-- 3. usage_record 新增复合索引（加速 analytics 按 biz_ref_type 查询）
ALTER TABLE usage_record
  ADD INDEX idx_biz_ref_type_created (biz_ref_type, created_at);
