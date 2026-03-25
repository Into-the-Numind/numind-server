-- 双价格计费：为 pricing_rule 添加售价字段，为 usage_record 添加收入字段

ALTER TABLE pricing_rule
  ADD COLUMN sell_input_price_per_mtok DECIMAL(10,4) NOT NULL DEFAULT 0 COMMENT '售价：每百万输入 tokens（元）',
  ADD COLUMN sell_output_price_per_mtok DECIMAL(10,4) NOT NULL DEFAULT 0 COMMENT '售价：每百万输出 tokens（元）',
  ADD COLUMN sell_price_per_call DECIMAL(10,4) NOT NULL DEFAULT 0 COMMENT '售价：每次调用（元）',
  ADD COLUMN sell_price_per_gb DECIMAL(10,4) NOT NULL DEFAULT 0 COMMENT '售价：每 GB（元）';

ALTER TABLE usage_record
  ADD COLUMN revenue_cents BIGINT NOT NULL DEFAULT 0 COMMENT '客户计费金额（分）';

-- 为 revenue_cents 添加索引（用于收入统计）
CREATE INDEX idx_ur_revenue ON usage_record (created_at, revenue_cents);
